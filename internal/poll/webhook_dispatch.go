package poll

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// GitLab webhook dispatch entry (gitlab-webhook input driver, ADR 0125).
//
// RunWebhook is the dispatcher job's second writer into the poller's
// dispatch spine: it builds validated, routed events with
// BuildWebhookEvents and launches each stage through the same dispatch()
// the cron poller uses (typed pipeline inputs, signedDispatchKeys HMAC —
// ADR 0131), so the agent job's gate accepts a dispatcher-launched
// pipeline unchanged.
//
// Shared poller state, intentionally changed for this driver (issue #7773,
// maintainer-authorized): the webhook and the cron poller are two writers
// of one dedup state, so the poller's state handling is occurrence-aware
// rather than presence- and watermark-based:
//
//   - Label identity. A label addition's identity is its GitLab resource
//     label event ID (RoutableEvent.LabelEventID), which both this driver
//     and poll discovery read from the same API, so a remove-then-re-add
//     is a distinct occurrence on both paths. Where no ID is available the
//     key falls back to a millisecond timestamp. Keys persisted by earlier
//     versions carry Unix-second timestamps and stay recognised
//     (labelKeyAtOrAfter never reads seconds as milliseconds); their retry
//     counts migrate onto the new key. Mixed-version operation is explicit:
//     every dispatch and retry count is also written under the Unix-second
//     key an older poller reads, registered in a ledger so this version can
//     tell such a mirror from genuine legacy evidence and never lets it
//     suppress a distinct same-second occurrence (see legacy_mirror.go).
//   - No watermark proof. A poll watermark never counts as evidence that an
//     occurrence was dispatched: presence-based polling can advance it past
//     a remove-then-re-add it never observed. A delayed but still-fresh
//     webhook therefore dispatches unless a dispatch key for the
//     occurrence exists, and dispatched keys are retained for the webhook
//     freshness window plus clock skew behind the watermark
//     (dispatchedKeyRetention) so that evidence outlives any replay.
//   - Retry handoff. A failed label dispatch is persisted as a pending
//     occurrence (persistedPollState.PendingLabels, keyed by the
//     occurrence's key) that the events poll retries independently of
//     LabelState and the updated_at watermark. A validated label addition
//     that fails normalization or routing (for example a transient
//     membership lookup) is handed off the same way, and a successful
//     retry records the label in LabelState if the issue still carries it.
//     Pending state is merged
//     under CAS by exact key: a successful dispatch clears only that
//     occurrence, and a stale in-flight poll cannot erase a newer handoff.
//
// The cron poller's discovery, routing and dispatch are otherwise
// unchanged. This file reads the poller's persisted state and writes
// through the existing CAS persist.
//
// Deduplication shares the poller's per-mode state.json rather than
// keeping its own: a /fs-* slash-command note is checked against and
// recorded in the slash branch's DispatchedKeysFast (the slash poll
// discovers those notes), and every other event uses the events branch's
// DispatchedKeysFull, with the same stage:RoutableEvent.Key() dispatch
// keys. A dispatched label addition is also unioned into LabelState, if
// the issue still carries that exact addition, so the poller's
// detectNewLabels does not see it as new. Writes go through
// persistWithCAS, so a concurrent poll cycle's keys are merged, not
// overwritten.
//
// Like the poller, delivery is at-least-once: pipelines are created before
// the state persist, so a webhook and a poll cycle that both read state
// before either persists can still each dispatch the same event. A failed
// dispatch is returned as an error (the dispatcher job fails and can be
// re-run; dedup keys make the re-run idempotent), and a failed label
// addition is additionally handed to the poller as above. An event whose
// stage was coalesced into a successful dispatch of the same stage and
// entity in the same payload records its own dispatch key, so replaying
// the payload creates no further pipeline. A fan-out where only some
// stages failed may re-dispatch the stages that succeeded on a re-run.

// ReadWebhookPayloadFile reads the native webhook body from path — the
// value of GitLab's file-type TRIGGER_PAYLOAD variable. It refuses a
// symlink or any non-regular file (so a crafted path cannot redirect the
// read out of the runner-provided location) and bounds the read to the
// size BuildWebhookEvents accepts.
func ReadWebhookPayloadFile(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("webhook payload path is empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat webhook payload: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("webhook payload is not a regular file (symlinks are refused)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open webhook payload: %w", err)
	}
	defer f.Close()
	// Re-check the opened file is the one Lstat saw, so a swap to a
	// symlink between the two calls is refused.
	opened, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat opened webhook payload: %w", err)
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("webhook payload changed while being opened")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxWebhookPayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read webhook payload: %w", err)
	}
	if len(data) > maxWebhookPayloadBytes {
		return nil, fmt.Errorf("webhook payload exceeds the %d-byte limit", maxWebhookPayloadBytes)
	}
	return data, nil
}

// isSlashCommandEvent reports whether the slash poll owns event's
// dedup state: a note whose body is a /fs-* command, matching
// discoverSlashCommands and the events-mode /fs-* skip.
func isSlashCommandEvent(event RoutableEvent) bool {
	return event.NoteID != 0 && strings.HasPrefix(strings.TrimSpace(event.NoteBody), "/fs-")
}

// RunWebhook builds events from a native GitLab webhook body and
// dispatches their routed stages, deduplicated against the poller's
// per-mode state. It fails closed — dispatching nothing — when the
// payload cannot be validated.
func (p *Poller) RunWebhook(ctx context.Context, raw []byte) error {
	events, err := p.BuildWebhookEvents(ctx, raw)
	if err != nil {
		return fmt.Errorf("build webhook events: %w", err)
	}

	var slash, full []WebhookEvent
	for _, we := range events {
		if isSlashCommandEvent(we.Event) {
			slash = append(slash, we)
		} else {
			full = append(full, we)
		}
	}

	var errs []error
	if len(slash) > 0 {
		if err := p.dispatchWebhookEvents(ctx, true, slash); err != nil {
			errs = append(errs, err)
		}
	}
	if len(full) > 0 {
		if err := p.dispatchWebhookEvents(ctx, false, full); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// dispatchWebhookEvents dispatches events against one poll mode's state
// branch (slash or events) and persists the new dispatched keys and
// label handoff to that branch in a single CAS write.
func (p *Poller) dispatchWebhookEvents(ctx context.Context, slash bool, events []WebhookEvent) error {
	p.slashCommandsOnly = slash
	branch := p.stateBranch()

	state, err := p.loadPollState(ctx, p.owner, p.repo)
	if err != nil {
		return fmt.Errorf("load poll state on %s: %w", branch, err)
	}
	previouslyDispatched := state.DispatchedKeysFull
	if slash {
		previouslyDispatched = state.DispatchedKeysFast
	}

	newDispatchedKeys := make(map[string]int64)
	mirrors := state.LegacyMirrors.clone()
	var reconcile []RoutableEvent
	// Entity-level dedup within this payload, as in Run: a label fan-out
	// routing two labels to the same stage dispatches it once.
	dispatchedEntities := make(map[string]bool)
	pendingAdd := make(map[string]PendingLabel)
	pendingClear := make(map[string]bool)
	var errs []error

	for _, we := range events {
		event := we.Event
		eventKey := event.Key()

		if we.Deferred != nil {
			// The label occurrence was validated but could not be normalized
			// or routed. Report the failure and hand the exact occurrence to
			// the poller; its retry skips stages already dispatched per the
			// dispatch keys and clears the occurrence.
			errs = append(errs, we.Deferred)
			pendingAdd[eventKey] = pendingLabelFor(event)
			continue
		}

		isLabelAdd := event.Type == "issue_label" && event.ChangedLabel != ""
		anyDispatched := false
		eventFailed := false
		for _, stage := range we.Stages {
			dispatchKey := stage + ":" + eventKey
			if alreadyDispatched(previouslyDispatched, mirrors, stage, event) {
				log.Printf("  skipping %s (already dispatched per %s)", dispatchKey, branch)
				continue
			}
			entityKey := stage + ":" + resourceKey(event)
			if dispatchedEntities[entityKey] {
				// Satisfied by a successful dispatch of the same stage and
				// entity earlier in this payload. Record this event's own
				// key (a failed dispatch never sets dispatchedEntities) so
				// a replay does not dispatch it again.
				log.Printf("  skipping %s for %s (already dispatched for entity %s)", stage, eventKey, entityKey)
				recordDispatch(newDispatchedKeys, mirrors, stage, event, event.UpdatedAt.Unix())
				anyDispatched = true
				continue
			}
			if err := p.dispatch(ctx, p.owner, p.repo, stage, event); err != nil {
				errs = append(errs, fmt.Errorf("dispatch %s for %s failed: %w", stage, eventKey, err))
				eventFailed = true
				continue
			}
			anyDispatched = true
			recordDispatch(newDispatchedKeys, mirrors, stage, event, event.UpdatedAt.Unix())
			dispatchedEntities[entityKey] = true
		}
		// A replay can find every stage already dispatched while the
		// occurrence is still handed off: an earlier run created the
		// pipeline but failed to reconcile LabelState. That handoff must be
		// reconciled here, not just cleared, or its removal would leave the
		// label's presence unrecorded.
		reconcileHandoff := false
		if isLabelAdd {
			if eventFailed {
				// Hand this exact occurrence to the poller, which retries
				// it regardless of LabelState or its watermark.
				pendingAdd[eventKey] = pendingLabelFor(event)
			} else if _, ok := state.PendingLabels[eventKey]; ok {
				pendingClear[eventKey] = true
				reconcileHandoff = true
			}
		}
		if !anyDispatched && !reconcileHandoff {
			continue
		}

		if isLabelAdd && !eventFailed && routableLabels[event.ChangedLabel] {
			// Record presence only if the issue still carries this exact
			// addition: the event was validated before dispatch, and a
			// removal (possibly persisted by a concurrent poll) since then
			// must not be undone by the union, or a later re-add whose
			// webhook is missed would be invisible to presence-based
			// discovery. The dispatch keys above are persisted regardless.
			_, err := p.pendingLabelIsCurrent(ctx, event, nil)
			switch {
			case err == nil:
				// Revalidated by persistWithCAS after each state load, so a
				// removal landing between this check and the commit is not
				// undone. An occurrence that is no longer current is queued
				// too, as the poll path does: a replacement re-add that arrived
				// during the dispatch is recorded nowhere (LabelState may still
				// hold the earlier addition, which presence-based polling
				// treats as seen), and revalidateReconcile hands it off as
				// pending when it has no dispatch evidence.
				reconcile = append(reconcile, event)
			case forge.IsNotFound(err):
				// The issue is gone: nothing to record.
			default:
				// Keep a reconciliation handoff: the poller's retry skips
				// the already-dispatched stages and reconciles LabelState.
				errs = append(errs, fmt.Errorf("reconcile label state for %s: %w", eventKey, err))
				pendingAdd[eventKey] = pendingLabelFor(event)
				delete(pendingClear, eventKey)
			}
		}
		if isSlashCommandEvent(event) {
			noteableType := "Issue"
			if strings.HasPrefix(event.Type, "mr_") {
				noteableType = "MergeRequest"
			}
			_ = p.client.CreateNoteAwardEmoji(ctx, p.owner, p.repo, noteableType, event.IID, event.NoteID, "eyes")
		}
	}

	if len(newDispatchedKeys) > 0 || len(pendingAdd) > 0 || len(pendingClear) > 0 || len(reconcile) > 0 {
		deltas := persistDeltas{dispatched: newDispatchedKeys, pendingAdd: pendingAdd, pendingClear: pendingClear, reconcile: reconcile, mirrors: mirrors}
		if err := p.persistWithCAS(ctx, p.owner, p.repo, deltas); err != nil {
			errs = append(errs, fmt.Errorf("persist poll state on %s: %w", branch, err))
		}
	}

	log.Printf("webhook dispatch on %s: %d events, %d dispatched", branch, len(events), len(newDispatchedKeys))
	return errors.Join(errs...)
}

// alreadyDispatched reports whether stage was already dispatched for
// event. The exact stage:Key() entry decides for every event. A label
// addition additionally matches evidence recorded under keys an earlier
// version, or an ID-less discovery, wrote:
//
//   - The Unix-second form (LegacyLabelKey). When a ledger entry marks that
//     key as a mirror this version wrote for an older poller (see
//     legacy_mirror.go), it is evidence only for the occurrence that owns it:
//     a distinct addition of the same label in the same second, such as a
//     remove then re-add, is not suppressed by it. A second key no ledger
//     entry claims was written by an older poller itself and counts as proof.
//   - Any other timestamp-keyed dispatch of the same label on the same issue
//     at or after the addition's time: poll discovery without a label event
//     ID keys on the issue's updated_at, which is never earlier than the
//     addition it observed, so the exact keys can differ for one addition. A
//     re-addition after a removal is later than every such key, so it is not
//     mistaken for a duplicate (label presence is not an occurrence identity).
//     Mirrors are skipped here: they are bookkeeping for older readers, not
//     occurrence evidence.
//
// Known ambiguity: the timestamp match is a compatibility heuristic, not
// proof that this exact occurrence was dispatched. A historical key carries
// the issue's updated_at, not a label-event identity, so an ID-keyed
// addition can be suppressed when a numeric-suffix key for the same stage,
// issue, and label is at or after its time (for example, an addition made
// after the issue was last updated by an unrelated change that an older
// poller keyed). It errs toward not re-dispatching, bounded to state written
// by pollers that predate label-event IDs; occurrence identity cannot be
// recovered from those keys.
func alreadyDispatched(dispatched map[string]int64, mirrors mirrorSet, stage string, event RoutableEvent) bool {
	if _, ok := dispatched[stage+":"+event.Key()]; ok {
		return true
	}
	if event.Type != "issue_label" || event.ChangedLabel == "" {
		return false
	}
	owners := make([]string, 0, 2)
	for _, k := range eventIdentityKeys(event) {
		owners = append(owners, stage+":"+k)
	}
	if legacy := event.LegacyLabelKey(); legacy != "" {
		key := stage + ":" + legacy
		if _, ok := dispatched[key]; ok && mirrors.ownedBy(mirrorKindDispatch, key, owners...) {
			return true
		}
	}
	prefix := fmt.Sprintf("%s:%s-%d-%s-", stage, event.Type, event.IID, event.ChangedLabel)
	for key := range dispatched {
		// A label name that merely extends event's label, or an
		// ID-keyed ("e<ID>") entry, leaves a non-numeric remainder and is
		// ignored by the parse.
		if strings.HasPrefix(key, prefix) && !mirrors.isMirror(mirrorKindDispatch, key) &&
			labelKeyAtOrAfter(strings.TrimPrefix(key, prefix), event.UpdatedAt) {
			return true
		}
	}
	return false
}
