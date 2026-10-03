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
			events = append(events, pending[k].routableEvent())
		}
		events = p.deduplicate(events)
		for _, event := range events[discovered:] {
			pendingOnly[event.Key()] = true
		}
	}
	discoveredCount := len(events) - len(pendingOnly)

	previouslyDispatched, err := p.readDispatchedKeys(ctx, p.owner, p.repo)
	if err != nil {
		return fmt.Errorf("dispatched keys: %w", err)
	}

	failedKeys, err := p.readFailedKeys(ctx, p.owner, p.repo)
	if err != nil {
		return fmt.Errorf("failed keys: %w", err)
	}

	dispatched := 0
	newDispatchedKeys := make(map[string]int64)
	var maxUpdatedAt time.Time
	var minFailedAt time.Time
	failedLabelEvents := make(map[int]map[string]bool)
	var cycleErrs []error
	resolvedPending := make(map[string]bool)
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

	for _, event := range events {
		eventKey := event.Key()

		if failedCount(failedKeys, event) >= maxEventRetries {
			log.Printf("WARNING: exhausted retry budget (%d) for %s, skipping", maxEventRetries, eventKey)
			observe(event)
			resolvedPending[eventKey] = true
			continue
		}

		normalizedEvent, actorID, err := p.toNormalizedEvent(ctx, event)
		if err != nil {
			log.Printf("WARNING: skipping %s event on IID %d: %v", event.Type, event.IID, err)
			recordEventFailure(&cycleErrs, failedKeys, event, &minFailedAt, failedLabelEvents,
				fmt.Errorf("skipping %s event on IID %d: %w", event.Type, event.IID, err))
			continue
		}

		if event.Type == "issue_label" && actorID != 0 {
			event.NoteAuthorID = actorID
		}

		var stages []string
		if p.router != nil {
			stages, err = p.router.Route(&normalizedEvent)
			if err != nil {
				log.Printf("dispatch core error for %s: %v", eventKey, err)
				recordEventFailure(&cycleErrs, failedKeys, event, &minFailedAt, failedLabelEvents,
					fmt.Errorf("dispatch core error for %s: %w", eventKey, err))
				continue
			}
		}

		if len(stages) == 0 {
			observe(event)
			resolvedPending[eventKey] = true
			continue
		}

		anyDispatched := false
		allSkipped := true
		eventFailed := false
		for _, stage := range stages {
			dispatchKey := stage + ":" + eventKey
			if alreadyDispatched(previouslyDispatched, stage, event) {
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
				newDispatchedKeys[dispatchKey] = event.dispatchedAtUnix()
				continue
			}
			allSkipped = false
			if err := p.dispatch(ctx, p.owner, p.repo, stage, event); err != nil {
				log.Printf("dispatch %s for %s failed: %v", stage, eventKey, err)
				recordEventFailure(&cycleErrs, failedKeys, event, &minFailedAt, failedLabelEvents,
					fmt.Errorf("dispatch %s for %s failed: %w", stage, eventKey, err))
				eventFailed = true
				continue
			}
			dispatched++
			anyDispatched = true
			newDispatchedKeys[dispatchKey] = event.dispatchedAtUnix()
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
			record, err := p.pendingLabelIsCurrent(ctx, event, failedLabelEvents)
			switch {
			case err == nil:
				if record {
					// Revalidated by persistWithCAS after each state load
					// (including CAS retries) before it is recorded.
					reconcile = append(reconcile, event)
				}
			case forge.IsNotFound(err):
				// The issue is gone: nothing to record.
			default:
				reconcileFailed = true
				failedKeys[eventKey] = failedCount(failedKeys, event) + 1
				retireLegacyFailureKey(failedKeys, event)
				cycleErrs = append(cycleErrs, fmt.Errorf("reconcile label state for %s: %w", eventKey, err))
			}
		}
		if !eventFailed && !reconcileFailed {
			// Every stage was dispatched now or earlier: the exact
			// occurrence needs no more retries.
			resolvedPending[eventKey] = true
		}

		if anyDispatched && !reconcileFailed {
			// Tombstone (present, 0), not delete: unionFailedKeys treats
			// a 0-count entry as this writer's explicit deletion, which
			// survives the CAS merge even if the freshly reloaded
			// document still carries a stale pre-cycle count for this
			// key. A plain delete here would let that merge resurrect
			// it (see unionFailedKeys in state.go).
			clearEventFailure(failedKeys, event)
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
	for k, pl := range pendingByKey {
		if resolvedPending[k] || failedCount(failedKeys, pl.routableEvent()) >= maxEventRetries {
			pendingClear[k] = true
		}
	}
	if maxUpdatedAt.IsZero() {
		log.Printf("WARNING: watermark not advanced (%d events, %d dispatched, %d errors)", len(events), dispatched, len(cycleErrs))
		// The watermark must stay put, but dispatches that did succeed (for
		// example pending-only occurrences, which never move it) still need
		// their keys recorded: pendingClear below drops them from the retry
		// queue, so without their keys a fresh webhook replay would dispatch
		// them again.
		deltas := persistDeltas{failed: failedKeys, pendingClear: pendingClear}
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

	// failedPresence lists the poll-discovered label occurrences that failed
	// this cycle although this writer's baseline did not record the label.
	// Rolling the label back out of the snapshot is then a no-op relative to
	// that baseline, so the merge would keep a presence a concurrent writer
	// recorded for an older occurrence and later polls would never retry the
	// failed one. persistWithCAS removes that presence after the merge.
	var failedPresence LabelState
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
		if p.labelBase != nil {
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

func recordEventFailure(cycleErrs *[]error, failedKeys map[string]int, event RoutableEvent, minFailedAt *time.Time, failedLabelEvents map[int]map[string]bool, cause error) {
	*cycleErrs = append(*cycleErrs, cause)
	eventKey := event.Key()
	failedKeys[eventKey] = failedCount(failedKeys, event) + 1
	retireLegacyFailureKey(failedKeys, event)
	if failedKeys[eventKey] >= maxEventRetries {
		*cycleErrs = append(*cycleErrs, fmt.Errorf("exhausted retry budget (%d) for %s, dropping", maxEventRetries, eventKey))
	}
	trackFailure(minFailedAt, event.UpdatedAt)
	trackLabelFailure(failedLabelEvents, event)
}

// alternateFailureKeys lists the other keys event's retry count may be
// recorded under: the Unix-second label key an earlier version wrote, and
// the millisecond fallback key used while the label event ID was unknown.
func alternateFailureKeys(event RoutableEvent) []string {
	var keys []string
	for _, k := range []string{event.LegacyLabelKey(), event.FallbackLabelKey()} {
		if k != "" && k != event.Key() {
			keys = append(keys, k)
		}
	}
	return keys
}

// failedCount is event's retry count: its current key's count, or the
// count recorded under an alternate key (the larger wins), so a migrated
// label, or one whose ID lookup failed on an earlier cycle, keeps its retry
// budget.
func failedCount(failedKeys map[string]int, event RoutableEvent) int {
	n := failedKeys[event.Key()]
	for _, k := range alternateFailureKeys(event) {
		if failedKeys[k] > n {
			n = failedKeys[k]
		}
	}
	return n
}

// retireLegacyFailureKey tombstones event's alternate failure keys once
// their count has been carried onto the current key (a 0 count is a
// deletion in unionFailedKeys).
func retireLegacyFailureKey(failedKeys map[string]int, event RoutableEvent) {
	for _, k := range alternateFailureKeys(event) {
		if _, ok := failedKeys[k]; ok {
			failedKeys[k] = 0
		}
	}
}

// clearEventFailure tombstones event's failure counts after a dispatch.
func clearEventFailure(failedKeys map[string]int, event RoutableEvent) {
	failedKeys[event.Key()] = 0
	retireLegacyFailureKey(failedKeys, event)
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
