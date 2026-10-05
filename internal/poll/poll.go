package poll

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Poller discovers GitLab events and dispatches agent stages.
type Poller struct {
	client            GitLabClient
	router            dispatch.EventRouter
	projectPath       string
	owner             string
	repo              string
	botUserID         int
	gitlabURL         string
	opts              Options
	dispatches        []Dispatch
	warnedNoHMAC      bool
	slashCommandsOnly bool
	labelBase         LabelState       // LabelState snapshot the current cycle's label delta derives from
	discoveryErrs     []error          // label-event lookup failures of the current discovery, reported by Run after state is persisted
	unresolvedLabels  LabelState       // label additions whose occurrence lookup failed during discovery; their concurrently recorded presence is removed at persist
	now               func() time.Time // clock for webhook freshness checks; nil uses time.Now
}

// New creates a Poller for the given project.
func New(client GitLabClient, router dispatch.EventRouter, projectPath string, opts Options) *Poller {
	owner, repo := splitOwnerRepo(projectPath)
	gitlabURL := opts.GitLabURL
	if gitlabURL == "" {
		gitlabURL = "https://gitlab.com"
	}
	return &Poller{
		client:      client,
		router:      router,
		projectPath: projectPath,
		owner:       owner,
		repo:        repo,
		botUserID:   opts.BotUserID,
		gitlabURL:   strings.TrimRight(gitlabURL, "/"),
		opts:        opts,
	}
}

const maxEventRetries = 3

// Run executes a single poll cycle: read watermark, discover events,
// filter, deduplicate, convert to NormalizedEvent, route, dispatch,
// and advance the watermark.
//
// Conversion, routing, and dispatch failures are returned after poll
// state is persisted so the poll job fails instead of reporting a
// healthy cycle. Failed events remain retryable until maxEventRetries.
// The cycle that exhausts the budget also fails (the event is dropped);
// later rediscovery of an already-dropped event is a skip, not a new
// failure, so the 30s watermark overlap cannot pin the poll job red.
//
// Poll mode is determined by Options.Mode:
//   - "slash": fast poll — only /fs-* slash commands via the Events API
//   - "events": full discovery — labels, merges, MR opens, closed-unmerged MRs, non-slash notes (filters out /fs-* notes)
//   - "": backward compatibility — uses events discovery path but does not filter /fs-* notes
func (p *Poller) Run(ctx context.Context) error {
	if p.client == nil {
		return fmt.Errorf("poller requires a GitLab client (Phase 1 wiring incomplete)")
	}

	p.slashCommandsOnly = p.opts.Mode == "slash"
	switch p.opts.Mode {
	case "slash":
		log.Printf("poll mode: slash (slash commands only)")
	case "events":
		log.Printf("poll mode: events (full discovery, /fs-* notes filtered)")
	default:
		log.Printf("poll mode: events (full discovery, legacy — no /fs-* filtering)")
	}

	lastPollAt, err := p.readWatermark(ctx, p.owner, p.repo)
	if err != nil {
		return fmt.Errorf("read watermark: %w", err)
	}

	var events []RoutableEvent
	var labelState LabelState
	var minSkippedAt time.Time
	if p.slashCommandsOnly {
		events, minSkippedAt, err = p.discoverSlashCommands(ctx, p.owner, p.repo, lastPollAt)
	} else {
		p.labelBase = nil
		p.discoveryErrs = nil
		p.unresolvedLabels = nil
		events, labelState, minSkippedAt, err = p.discoverAllEvents(ctx, p.owner, p.repo, lastPollAt)
	}
	if err != nil {
		return fmt.Errorf("discover events: %w", err)
	}

	events = p.filterBotEvents(events)
	events = p.deduplicate(events)

	// Pending label occurrences handed over by the webhook driver after a
	// failed dispatch are retried here, independent of LabelState and of
	// the updated_at watermark that bound discovery above. They do not
	// advance the watermark (pendingOnly): the occurrence time says
	// nothing about what discovery has covered.
	pendingByKey := map[string]PendingLabel{}
	pendingOnly := map[string]bool{}
	var unresolvedMarkers []string
	if !p.slashCommandsOnly {
		pending, err := p.readPendingLabels(ctx, p.owner, p.repo)
		if err != nil {
			return fmt.Errorf("pending labels: %w", err)
		}
		pendingByKey = pending
		discovered := len(events)
		keys := make([]string, 0, len(pending))
		for k := range pending {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if pending[k].Unresolved {
				unresolvedMarkers = append(unresolvedMarkers, k)
				continue
			}
			events = append(events, pending[k].routableEvent())
		}
		events = p.deduplicate(events)
		for _, event := range events[discovered:] {
			pendingOnly[event.Key()] = true
		}
	}
	discoveredCount := len(events) - len(pendingOnly)

	// One snapshot for all three: see readDedupState.
	previouslyDispatched, failedKeys, mirrors, err := p.readDedupState(ctx, p.owner, p.repo)
	if err != nil {
		return fmt.Errorf("dedup state: %w", err)
	}

	dispatched := 0
	newDispatchedKeys := make(map[string]int64)
	var maxUpdatedAt time.Time
	var minFailedAt time.Time
	failedLabelEvents := make(map[int]map[string]bool)
	// Discovery-time label-event lookup failures: the additions were held
	// back (watermark and label state), but the cycle must still fail so a
	// persistent lookup failure does not look like a healthy empty cycle.
	cycleErrs := append([]error(nil), p.discoveryErrs...)
	resolvedPending := make(map[string]bool)

	// Unresolved markers are issue-level reconciliation work queued when a
	// writer could not resolve which occurrence of a label is current. They
	// are consumed here independent of the watermark: each resolves to the
	// issue's current add occurrence (with its own actor), which is
	// dispatched like a pending-only occurrence and handed off as a pending
	// occurrence of its own until it is resolved, so a failed dispatch is
	// not lost when the marker is cleared. A lookup failure keeps the marker
	// queued, bounded by the retry budget.
	pendingAdd := make(map[string]PendingLabel)
	markerClear := make(map[string]bool)
	queued := make(map[string]bool, len(events))
	for _, event := range events {
		queued[event.Key()] = true
	}
	for _, k := range unresolvedMarkers {
		if failedKeys[k] >= maxEventRetries {
			log.Printf("WARNING: exhausted retry budget (%d) for %s, dropping", maxEventRetries, k)
			cycleErrs = append(cycleErrs, fmt.Errorf("exhausted retry budget (%d) for %s, dropping", maxEventRetries, k))
			markerClear[k] = true
			failedKeys[k] = 0
			continue
		}
		pl := pendingByKey[k]
		event, found, err := p.resolveUnresolvedLabel(ctx, pl)
		if err != nil {
			log.Printf("WARNING: resolve label %q on issue %d: %v (keeping for retry)", pl.Label, pl.IID, err)
			failedKeys[k]++
			cycleErrs = append(cycleErrs, fmt.Errorf("resolve label %q on issue %d: %w", pl.Label, pl.IID, err))
			continue
		}
		markerClear[k] = true
		failedKeys[k] = 0
		if !found {
			continue
		}
		if queued[event.Key()] {
			// Already queued this cycle, by discovery or as a pending
			// occurrence. Clearing the marker must not leave a discovered
			// occurrence held only by the retry count and the watermark
			// holdback: a competing poll can advance the persisted watermark
			// past the issue, so hand it off as pending until it is resolved
			// (pendingAdd is pruned below once it succeeds or exhausts its
			// budget). An occurrence that is already pending is durable.
			if _, pending := pendingByKey[event.Key()]; !pending {
				pendingAdd[event.Key()] = pendingLabelFor(event)
			}
			continue
		}
		queued[event.Key()] = true
		events = append(events, event)
		pendingOnly[event.Key()] = true
		pendingAdd[event.Key()] = pendingLabelFor(event)
	}
	var reconcile []RoutableEvent
	observe := func(event RoutableEvent) {
		if !pendingOnly[event.Key()] && event.UpdatedAt.After(maxUpdatedAt) {
			maxUpdatedAt = event.UpdatedAt
		}
	}

	// Entity-level deduplication: within a single poll cycle, dispatch
	// at most one pipeline per stage+entity (e.g. "triage:issue-3").
	// Multiple /fs-triage comments on the same issue produce distinct
	// event Keys (note-123, note-456) but share the same entity key.
	// Without this, each comment dispatches a separate pipeline that
	// blocks on the same resource group. The first event per entity
	// wins; subsequent events are skipped.
	dispatchedEntities := make(map[string]bool)
	// boundEvents holds each event as dispatched, with the actor bound to
	// its exact occurrence, so a failed discovered occurrence is handed off
	// under that actor rather than whoever applied the label last.
	boundEvents := make(map[string]RoutableEvent, len(events))

	for _, event := range events {
		eventKey := event.Key()

		if failedCount(failedKeys, mirrors, event) >= maxEventRetries {
			log.Printf("WARNING: exhausted retry budget (%d) for %s, skipping", maxEventRetries, eventKey)
			observe(event)
			resolvedPending[eventKey] = true
			continue
		}

		normalizedEvent, actorID, err := p.toNormalizedEvent(ctx, event)
		if err != nil {
			log.Printf("WARNING: skipping %s event on IID %d: %v", event.Type, event.IID, err)
			recordEventFailure(&cycleErrs, failedKeys, mirrors, event, &minFailedAt, failedLabelEvents,
				fmt.Errorf("skipping %s event on IID %d: %w", event.Type, event.IID, err))
			continue
		}

		if event.Type == "issue_label" && actorID != 0 {
			event.NoteAuthorID = actorID
		}
		boundEvents[eventKey] = event

		var stages []string
		if p.router != nil {
			stages, err = p.router.Route(&normalizedEvent)
			if err != nil {
				log.Printf("dispatch core error for %s: %v", eventKey, err)
				recordEventFailure(&cycleErrs, failedKeys, mirrors, event, &minFailedAt, failedLabelEvents,
					fmt.Errorf("dispatch core error for %s: %w", eventKey, err))
				continue
			}
		}

		if len(stages) == 0 {
			observe(event)
			// A pending occurrence that routes to no stages (for example its
			// actor's access was revoked) is about to be cleared. A later
			// re-add by an authorized actor is hidden from presence-based
			// discovery by the LabelState entry for this label, so clearing
			// this entry could lose the only reconciliation work that finds
			// it. Reconcile first: persistWithCAS hands any superseding,
			// undispatched occurrence off as its own pending entry, bound to
			// its own actor. A failed lookup keeps this entry pending.
			if pendingOnly[eventKey] && event.Type == "issue_label" && event.ChangedLabel != "" {
				_, err := p.pendingLabelIsCurrent(ctx, event, failedLabelEvents)
				switch {
				case err == nil:
					reconcile = append(reconcile, event)
				case forge.IsNotFound(err):
					// The issue is gone: nothing to reconcile.
				default:
					failedKeys[eventKey] = failedCount(failedKeys, mirrors, event) + 1
					retireFallbackFailureKey(failedKeys, event)
					mirrorFailureCount(failedKeys, mirrors, event)
					cycleErrs = append(cycleErrs, fmt.Errorf("reconcile label state for %s: %w", eventKey, err))
					continue
				}
			}
			resolvedPending[eventKey] = true
			continue
		}

		anyDispatched := false
		allSkipped := true
		eventFailed := false
		for _, stage := range stages {
			if alreadyDispatched(previouslyDispatched, mirrors, stage, event) {
				continue
			}
			// Entity-level dedup: skip if this stage+entity was
			// already dispatched in the current poll cycle.
			entityKey := stage + ":" + resourceKey(event)
			if dispatchedEntities[entityKey] {
				log.Printf("  skipping %s for %s (already dispatched for entity %s)", stage, eventKey, entityKey)
				// Satisfied by this cycle's successful dispatch of the same
				// stage and entity (dispatchedEntities is only set on
				// success). Record this occurrence's own key so a webhook
				// replay of it finds the evidence and creates no further
				// pipeline.
				recordDispatch(newDispatchedKeys, mirrors, stage, event, event.dispatchedAtUnix())
				continue
			}
			allSkipped = false
			if err := p.dispatch(ctx, p.owner, p.repo, stage, event); err != nil {
				log.Printf("dispatch %s for %s failed: %v", stage, eventKey, err)
				recordEventFailure(&cycleErrs, failedKeys, mirrors, event, &minFailedAt, failedLabelEvents,
					fmt.Errorf("dispatch %s for %s failed: %w", stage, eventKey, err))
				eventFailed = true
				continue
			}
			dispatched++
			anyDispatched = true
			recordDispatch(newDispatchedKeys, mirrors, stage, event, event.dispatchedAtUnix())
			dispatchedEntities[entityKey] = true
			observe(event)
		}

		if allSkipped {
			observe(event)
		}
		// A pending retry that succeeded must leave durable occurrence
		// evidence in LabelState: pending-only occurrences are not
		// discovered this cycle, so nothing else records the label, and
		// once the dispatch key is pruned an unrelated issue update
		// would rediscover and re-dispatch the same addition. Record the
		// label only if the issue still carries this exact occurrence (see
		// pendingLabelIsCurrent). A failed lookup keeps
		// the occurrence pending (counting against its retry budget);
		// the retry then skips the already-dispatched stages and
		// reconciles again.
		reconcileFailed := false
		if !eventFailed && pendingOnly[eventKey] && event.Type == "issue_label" && event.ChangedLabel != "" {
			_, err := p.pendingLabelIsCurrent(ctx, event, failedLabelEvents)
			switch {
			case err == nil:
				// Revalidated by persistWithCAS after each state load
				// (including CAS retries) before it is recorded. An
				// occurrence that is no longer current is queued too: a
				// newer undispatched re-add that replaced it is handed off
				// as pending there (see revalidateReconcile), since neither
				// LabelState nor the watermark would let a later poll
				// rediscover it.
				reconcile = append(reconcile, event)
			case forge.IsNotFound(err):
				// The issue is gone: nothing to record.
			default:
				reconcileFailed = true
				failedKeys[eventKey] = failedCount(failedKeys, mirrors, event) + 1
				mirrorFailureCount(failedKeys, mirrors, event)
				cycleErrs = append(cycleErrs, fmt.Errorf("reconcile label state for %s: %w", eventKey, err))
			}
		}
		if !eventFailed && !reconcileFailed {
			// Every stage was dispatched now or earlier: the exact
			// occurrence needs no more retries.
			resolvedPending[eventKey] = true
		}

		if anyDispatched && !eventFailed && !reconcileFailed {
			// Tombstone (present, 0), not delete: unionFailedKeys treats
			// a 0-count entry as this writer's explicit deletion, which
			// survives the CAS merge even if the freshly reloaded
			// document still carries a stale pre-cycle count for this
			// key. A plain delete here would let that merge resurrect
			// it (see unionFailedKeys in state.go).
			clearEventFailure(failedKeys, mirrors, event)
		}

		if anyDispatched && event.NoteID != 0 && strings.HasPrefix(strings.TrimSpace(event.NoteBody), "/fs-") {
			noteableType := "Issue"
			if strings.HasPrefix(event.Type, "mr_") {
				noteableType = "MergeRequest"
			}
			_ = p.client.CreateNoteAwardEmoji(ctx, p.owner, p.repo, noteableType, event.IID, event.NoteID, "eyes")
		}
	}

	// Merge newly dispatched keys into the in-memory map. Pipelines were
	// already created via API during dispatch — if the combined persist
	// below fails, events may re-dispatch on the next cycle (at-least-once).
	for k, ts := range newDispatchedKeys {
		previouslyDispatched[k] = ts
	}

	if maxUpdatedAt.IsZero() && discoveredCount == 0 && len(cycleErrs) == 0 {
		maxUpdatedAt = time.Now()
	}

	// Clear exactly the pending occurrences this cycle resolved, or whose
	// retry budget is spent. Nothing else is touched, so a handoff written
	// after this cycle read state survives the CAS merge.
	pendingClear := make(map[string]bool)
	// Retiring an exhausted label occurrence (a spent budget on the final
	// attempt, or already spent on entry) removes its handoff, and LabelState
	// plus the watermark can hide a later remove/re-add from discovery.
	// Reconcile the issue's current occurrence first: persistWithCAS hands a
	// distinct undispatched replacement off as its own pending entry (never the
	// exhausted occurrence itself, so its budget is not restarted), and a failed
	// lookup keeps the exhausted entry pending, or queues issue-level
	// reconciliation, until it resolves. An occurrence without a label event ID
	// cannot be told apart from its replacement, so it is not reconciled.
	retireExhausted := func(k string, event RoutableEvent) {
		if failedCount(failedKeys, mirrors, event) < maxEventRetries || event.LabelEventID == 0 || event.ChangedLabel == "" {
			return
		}
		for _, queuedEvent := range reconcile {
			if queuedEvent.Key() == k {
				return
			}
		}
		reconcile = append(reconcile, event)
	}
	for k, pl := range pendingByKey {
		if pl.Unresolved {
			if markerClear[k] {
				pendingClear[k] = true
			}
			continue
		}
		retireExhausted(k, pl.routableEvent())
		if resolvedPending[k] || failedCount(failedKeys, mirrors, pl.routableEvent()) >= maxEventRetries {
			pendingClear[k] = true
		}
	}
	// An occurrence resolved from a marker is handed off as pending only
	// while it is still unresolved, so a failed dispatch is retried after
	// its marker is cleared.
	for k, pl := range pendingAdd {
		retireExhausted(k, pl.routableEvent())
		if resolvedPending[k] || failedCount(failedKeys, mirrors, pl.routableEvent()) >= maxEventRetries {
			delete(pendingAdd, k)
		}
	}
	// A poll-discovered label occurrence that spent its budget on this cycle's
	// final attempt has no pending entry to visit above (a supported legacy
	// state carries a retry count but no handoff), yet retiring it hides a
	// later remove/re-add from discovery just the same. Reconcile it too.
	for _, event := range events {
		key := event.Key()
		if event.Type != "issue_label" || event.ChangedLabel == "" || pendingOnly[key] || !failedLabelEvents[event.IID][event.ChangedLabel] {
			continue
		}
		if bound, ok := boundEvents[key]; ok {
			event = bound
		}
		retireExhausted(key, event)
	}
	// A poll-discovered label occurrence that failed this cycle and still has
	// retries left is handed off as a pending occurrence, with the actor bound
	// to it. Its retry count and the watermark holdback alone cannot recover
	// it: a competing poll may persist a watermark past the issue, after which
	// updated_after discovery never lists it again. Occurrences that are
	// already pending (or queued above) are durable and left alone.
	for _, event := range events {
		key := event.Key()
		if event.Type != "issue_label" || event.ChangedLabel == "" || pendingOnly[key] {
			continue
		}
		if _, queuedForHandoff := pendingAdd[key]; queuedForHandoff {
			continue
		}
		if !failedLabelEvents[event.IID][event.ChangedLabel] {
			continue
		}
		if n := failedCount(failedKeys, mirrors, event); n == 0 || n >= maxEventRetries {
			continue
		}
		bound, ok := boundEvents[key]
		if !ok {
			bound = event
		}
		pendingAdd[key] = pendingLabelFor(bound)
	}
	// Additions whose occurrence lookup failed during discovery are queued
	// as markers: holding the watermark cannot recover them once a
	// competing poll persists a later one.
	unresolvedQueue := withUnresolvedMarkers(persistDeltas{pendingAdd: pendingAdd}, p.unresolvedLabels, time.Now())
	pendingAdd = unresolvedQueue.pendingAdd
	// failedPresence lists the poll-discovered label occurrences that failed
	// this cycle although this writer's baseline did not record the label.
	// Rolling the label back out of the snapshot is then a no-op relative to
	// that baseline, so the merge would keep a presence a concurrent writer
	// recorded for an older occurrence and later polls would never retry the
	// failed one. persistWithCAS removes that presence after the merge. It is
	// computed before the all-failed early return below so that path cleans
	// up the stale presence too.
	var failedPresence LabelState
	if labelState != nil && p.labelBase != nil {
		for _, event := range events {
			if event.Type != "issue_label" || pendingOnly[event.Key()] {
				continue
			}
			if !failedLabelEvents[event.IID][event.ChangedLabel] || toSet(p.labelBase[event.IID])[event.ChangedLabel] {
				continue
			}
			if failedPresence == nil {
				failedPresence = make(LabelState)
			}
			if !toSet(failedPresence[event.IID])[event.ChangedLabel] {
				failedPresence[event.IID] = append(failedPresence[event.IID], event.ChangedLabel)
			}
		}
	}

	// Additions whose occurrence lookup failed during discovery are
	// unresolved: restoring the previous snapshot is a no-op relative to this
	// writer's baseline, so a presence a concurrent writer recorded for an
	// older occurrence would survive the merge and hide the replacement from
	// later polls. Remove it too; dispatch keys deduplicate the rediscovered
	// occurrence if it was in fact dispatched.
	for iid, labels := range p.unresolvedLabels {
		for _, label := range labels {
			if failedPresence == nil {
				failedPresence = make(LabelState)
			}
			if !toSet(failedPresence[iid])[label] {
				failedPresence[iid] = append(failedPresence[iid], label)
			}
		}
	}

	if maxUpdatedAt.IsZero() {
		log.Printf("WARNING: watermark not advanced (%d events, %d dispatched, %d errors)", len(events), dispatched, len(cycleErrs))
		// The watermark must stay put, but dispatches that did succeed (for
		// example pending-only occurrences, which never move it) still need
		// their keys recorded: pendingClear below drops them from the retry
		// queue, so without their keys a fresh webhook replay would dispatch
		// them again.
		deltas := persistDeltas{failed: failedKeys, pendingClear: pendingClear, pendingAdd: pendingAdd, mirrors: mirrors}
		deltas.failedPresence = failedPresence
		if len(newDispatchedKeys) > 0 {
			deltas.dispatched = newDispatchedKeys
		}
		if len(reconcile) > 0 {
			deltas.reconcile = reconcile
			deltas.reconcileFailed = failedLabelEvents
		}
		if err := p.persistCycleDeltas(ctx, p.owner, p.repo, deltas); err != nil {
			cycleErrs = append(cycleErrs, fmt.Errorf("persist failed keys: %w", err))
		}
		if err := errors.Join(cycleErrs...); err != nil {
			return fmt.Errorf("poll cycle: %w", err)
		}
		return fmt.Errorf("all %d dispatches failed, watermark not advanced", len(events))
	}
	if !minFailedAt.IsZero() && minFailedAt.Before(maxUpdatedAt) {
		maxUpdatedAt = minFailedAt
	}
	if !minSkippedAt.IsZero() && minSkippedAt.Before(maxUpdatedAt) {
		maxUpdatedAt = minSkippedAt
	}
	newWatermark := maxUpdatedAt.Add(-30 * time.Second)

	if len(newDispatchedKeys) > 0 {
		keys := make([]string, 0, len(newDispatchedKeys))
		for k := range newDispatchedKeys {
			keys = append(keys, k)
		}
		log.Printf("persisting %d new dispatched keys: %v", len(keys), keys)
	}

	if labelState != nil {
		for iid, failedLabels := range failedLabelEvents {
			if current, ok := labelState[iid]; ok {
				var kept []string
				for _, label := range current {
					if !failedLabels[label] {
						kept = append(kept, label)
					}
				}
				labelState[iid] = kept
			}
		}
	}

	// One load-modify-save for dispatched keys, failed keys, watermark,
	// and label state. Splitting these across separate commits created
	// redundant poll-state history and skipped pipeline records.
	// Pipelines were already created via API — if this persist fails,
	// events may re-dispatch on the next cycle (at-least-once delivery).
	cycle := persistDeltas{
		dispatched:   previouslyDispatched,
		watermark:    &newWatermark,
		failed:       failedKeys,
		labels:       labelState,
		labelsBase:   p.labelBase,
		pendingClear: pendingClear,
		pendingAdd:   pendingAdd,
		mirrors:      mirrors,
	}
	if len(reconcile) > 0 {
		cycle.reconcile = reconcile
	}
	cycle.reconcileFailed = failedLabelEvents
	cycle.failedPresence = failedPresence
	for _, event := range events {
		if event.Type == "issue_label" && !pendingOnly[event.Key()] {
			cycle.discovered = append(cycle.discovered, event)
		}
	}
	if err := p.persistCycleDeltas(ctx, p.owner, p.repo, cycle); err != nil {
		cycleErrs = append(cycleErrs, fmt.Errorf("persist poll state: %w", err))
		return fmt.Errorf("poll cycle: %w", errors.Join(cycleErrs...))
	}

	log.Printf("poll complete: %d events discovered, %d dispatched", len(events), dispatched)
	if err := errors.Join(cycleErrs...); err != nil {
		return fmt.Errorf("poll cycle: %w", err)
	}
	return nil
}

// pendingLabelIsCurrent reports whether a successfully retried pending label
// addition may be recorded in LabelState: the issue must still carry the
// label, the label's latest resource label event must be this very addition
// (a later remove/re-add is a different occurrence that discovery must still
// see), and no occurrence of the same label failed earlier this cycle (its
// rollback must not be overridden by the later label union). An
// occurrence without a label event ID cannot be matched, so only presence
// is checked for it. Errors are returned unwrapped so the caller can
// recognise forge.IsNotFound.
func (p *Poller) pendingLabelIsCurrent(ctx context.Context, event RoutableEvent, failedLabelEvents map[int]map[string]bool) (bool, error) {
	issue, err := p.client.GetIssue(ctx, p.owner, p.repo, event.IID)
	if err != nil {
		return false, err
	}
	if !toSet(issue.Labels)[event.ChangedLabel] {
		return false, nil
	}
	if failedLabelEvents[event.IID][event.ChangedLabel] {
		return false, nil
	}
	if event.LabelEventID == 0 {
		return true, nil
	}
	labelEvents, err := p.client.ListResourceLabelEvents(ctx, p.owner, p.repo, event.IID)
	if err != nil {
		return false, err
	}
	latest, ok := latestLabelEvent(labelEvents, event.ChangedLabel)
	return ok && latest.Action == "add" && latest.ID == event.LabelEventID, nil
}

func recordEventFailure(cycleErrs *[]error, failedKeys map[string]int, mirrors mirrorSet, event RoutableEvent, minFailedAt *time.Time, failedLabelEvents map[int]map[string]bool, cause error) {
	*cycleErrs = append(*cycleErrs, cause)
	eventKey := event.Key()
	failedKeys[eventKey] = failedCount(failedKeys, mirrors, event) + 1
	retireFallbackFailureKey(failedKeys, event)
	mirrorFailureCount(failedKeys, mirrors, event)
	if failedKeys[eventKey] >= maxEventRetries {
		*cycleErrs = append(*cycleErrs, fmt.Errorf("exhausted retry budget (%d) for %s, dropping", maxEventRetries, eventKey))
	}
	trackFailure(minFailedAt, event.UpdatedAt)
	trackLabelFailure(failedLabelEvents, event)
}

// failedCount is event's retry count: its current key's count, or the count
// recorded under another key that is evidence about this same occurrence (the
// larger wins), so a migrated label, or one whose ID lookup failed on an
// earlier cycle, keeps its retry budget. Those keys are the millisecond
// fallback key used while the label event ID was unknown, and the Unix-second
// key: an older poller's count when nothing mirrors that key, or this
// occurrence's own mirror. A second key mirrored for a different occurrence
// (a distinct addition in the same second) is not this occurrence's count.
func failedCount(failedKeys map[string]int, mirrors mirrorSet, event RoutableEvent) int {
	n := failedKeys[event.Key()]
	if fb := event.FallbackLabelKey(); fb != "" && failedKeys[fb] > n {
		n = failedKeys[fb]
	}
	legacy := event.LegacyLabelKey()
	if legacy != "" && legacy != event.Key() && failedKeys[legacy] > n &&
		mirrors.ownedBy(mirrorKindFailure, legacy, eventIdentityKeys(event)...) &&
		len(mirrors.owners(mirrorKindFailure, legacy)) <= 1 {
		// A mirror shared by several registered owners carries the largest of
		// their counts (see mirrorFailureCount), so it is no evidence for any
		// one of them: each owner's own key is authoritative. Unregistered
		// (genuine legacy) evidence and a single-owner mirror still import.
		n = failedKeys[legacy]
	}
	return n
}

// retireFallbackFailureKey tombstones event's millisecond fallback failure
// key once its count has been carried onto the current key (a 0 count is a
// deletion in unionFailedKeys). The Unix-second key is not retired: it is kept
// in step by mirrorFailureCount so an older poller keeps the budget.
func retireFallbackFailureKey(failedKeys map[string]int, event RoutableEvent) {
	if fb := event.FallbackLabelKey(); fb != "" && fb != event.Key() {
		if _, ok := failedKeys[fb]; ok {
			failedKeys[fb] = 0
		}
	}
}

// clearEventFailure tombstones event's failure counts after a dispatch.
func clearEventFailure(failedKeys map[string]int, mirrors mirrorSet, event RoutableEvent) {
	failedKeys[event.Key()] = 0
	retireFallbackFailureKey(failedKeys, event)
	mirrorFailureCount(failedKeys, mirrors, event)
}

func trackFailure(minFailedAt *time.Time, updatedAt time.Time) {
	if minFailedAt.IsZero() || updatedAt.Before(*minFailedAt) {
		*minFailedAt = updatedAt
	}
}

func trackLabelFailure(failedLabelEvents map[int]map[string]bool, event RoutableEvent) {
	if event.Type != "issue_label" || event.ChangedLabel == "" {
		return
	}
	if failedLabelEvents[event.IID] == nil {
		failedLabelEvents[event.IID] = make(map[string]bool)
	}
	failedLabelEvents[event.IID][event.ChangedLabel] = true
}

// splitOwnerRepo splits "group/subgroup/project" into owner="group/subgroup" and repo="project".
func splitOwnerRepo(projectPath string) (string, string) {
	idx := strings.LastIndex(projectPath, "/")
	if idx < 0 {
		return "", projectPath
	}
	return projectPath[:idx], projectPath[idx+1:]
}
