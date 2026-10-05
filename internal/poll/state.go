package poll

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Poller state lives on two dedicated, unprotected branches so the bot
// PAT can run at Developer access (30) instead of Maintainer (40).
// Each poll mode owns its own branch so concurrent slash+events runs
// cannot clobber each other, and force-re-root pruning cannot drop a
// sibling file. See #7343 and ADR 0067.
const (
	// PollStateBranchSlash is written by the slash poll (*/5).
	PollStateBranchSlash = "fullsend-poll-state-slash"
	// PollStateBranchEvents is written by the event poll (2,17,32,47).
	PollStateBranchEvents = "fullsend-poll-state-events"
	// PollStateFileName is the single HMAC-signed document on each branch.
	PollStateFileName = "state.json"

	// hmacDomainSlash / hmacDomainEvents are per-branch domain prefixes
	// mixed into the HMAC so a signed file cannot be substituted across
	// branches. The trailing newline matches the #7343 spec
	// (`fullsend-poll-state-slash/1\n`).
	hmacDomainSlash  = PollStateBranchSlash + "/1\n"
	hmacDomainEvents = PollStateBranchEvents + "/1\n"
)

var (
	errDispatchSecretUnset = errors.New("FULLSEND_DISPATCH_SECRET is not set: refusing to load or write unsigned poll state (re-run repos install/converge to provision it)")
	errPollStateTampered   = errors.New("poll state signature missing or invalid (tampered, or written without FULLSEND_DISPATCH_SECRET)")
	// errPollStateCASExhausted is returned when persistWithCAS could not
	// land a conflict-detecting persist after maxPollStateCASAttempts.
	// Fail closed rather than overwrite a concurrent writer's keys.
	errPollStateCASExhausted = errors.New("poll state persist exhausted CAS retries")
)

// maxPollStateCASAttempts bounds the read-merge-write loop in
// persistWithCAS. Attempt 1 is the uncontended write; later attempts
// reload, re-apply this writer's deltas, and recommit. Exhaustion
// fails closed so a concurrent writer cannot silently drop keys.
const maxPollStateCASAttempts = 5

// persistedPollState is the JSON document stored at state.json on a
// poll-state branch. Slash and events polls persist disjoint field
// subsets so concurrent modes cannot clobber each other.
type persistedPollState struct {
	LastPollAtFast     string           `json:"last_poll_at_fast,omitempty"`
	LastPollAtFull     string           `json:"last_poll_at_full,omitempty"`
	LabelState         LabelState       `json:"label_state,omitempty"`
	DispatchedKeysFast map[string]int64 `json:"dispatched_keys_fast,omitempty"`
	DispatchedKeysFull map[string]int64 `json:"dispatched_keys_full,omitempty"`
	FailedKeysFast     map[string]int   `json:"failed_keys_fast,omitempty"`
	FailedKeysFull     map[string]int   `json:"failed_keys_full,omitempty"`
	// PendingLabels holds label additions whose webhook dispatch failed,
	// keyed by occurrence (RoutableEvent.Key()). The events poll retries
	// them independently of LabelState and the updated_at watermark.
	//
	// It is an in-memory view only (json:"-"): on the wire each entry is an
	// encoded key in FailedKeysFull (see encodePendingLabels). A writer from
	// before this field existed round-trips unknown failed-key entries
	// untouched, so it cannot drop a pending handoff, and the legacy HMAC
	// covers them, so removing or editing one fails verification.
	PendingLabels map[string]PendingLabel `json:"-"`
	// LegacyMirrors is the ledger of Unix-second label keys this version
	// mirrors for older pollers (see legacy_mirror.go). Like PendingLabels it
	// is an in-memory view only: on the wire each entry is an encoded key in
	// FailedKeysFull, which an older writer round-trips untouched.
	LegacyMirrors mirrorSet `json:"-"`

	// HMAC is an HMAC-SHA256 signature (hex-encoded) over the rest of
	// this document, computed with the HMAC field cleared and prefixed
	// by a per-branch domain string. It reuses FULLSEND_DISPATCH_SECRET
	// (the same secret as computeDispatchHMAC) rather than introducing
	// a second secret.
	HMAC string `json:"hmac,omitempty"`
}

func (p *Poller) stateBranch() string {
	if p.slashCommandsOnly {
		return PollStateBranchSlash
	}
	return PollStateBranchEvents
}

// hmacDomainFor returns the per-branch HMAC domain prefix bound to
// projectPath ("owner/repo"), so a validly-signed document captured
// from one project cannot be replayed against another project that
// shares the same FULLSEND_DISPATCH_SECRET.
func hmacDomainFor(branch, projectPath string) string {
	base := hmacDomainEvents
	if branch == PollStateBranchSlash {
		base = hmacDomainSlash
	}
	return base + projectPath + "\n"
}

// hmacDomain returns the per-branch domain prefix further bound to this
// project (owner/repo). p.projectPath is the same "owner/repo" value
// dispatch.go mixes into pipeline variables via REPO_FULL_NAME.
func (p *Poller) hmacDomain() string {
	return hmacDomainFor(p.stateBranch(), p.projectPath)
}

// pollStateForMode returns a copy of state containing only the fields
// the given poll mode is allowed to persist, so a slash save cannot
// write events fields (and vice versa) even if they were present on
// the loaded document.
func pollStateForMode(slash bool, state persistedPollState) persistedPollState {
	if slash {
		return persistedPollState{
			LastPollAtFast:     state.LastPollAtFast,
			DispatchedKeysFast: state.DispatchedKeysFast,
			FailedKeysFast:     state.FailedKeysFast,
		}
	}
	return persistedPollState{
		LastPollAtFull:     state.LastPollAtFull,
		LabelState:         state.LabelState,
		DispatchedKeysFull: state.DispatchedKeysFull,
		FailedKeysFull:     state.FailedKeysFull,
		PendingLabels:      state.PendingLabels,
		LegacyMirrors:      state.LegacyMirrors,
	}
}

// modeDocument returns a copy of state containing only the fields this
// poll mode is allowed to persist.
func (p *Poller) modeDocument(state persistedPollState) persistedPollState {
	return pollStateForMode(p.slashCommandsOnly, state)
}

// computeStateHMAC computes an HMAC-SHA256 (hex-encoded) over the
// per-branch domain prefix plus the canonical JSON encoding of state
// with its own HMAC field cleared. It reuses FULLSEND_DISPATCH_SECRET
// and the same HMAC-SHA256-hex construction as computeDispatchHMAC.
//
// The pending-label handoff travels inside failed_keys_full (see
// encodePendingLabels), so this signature authenticates its presence and its
// absence, and an older reader verifies the same canonical document.
func computeStateHMAC(secret, domain string, state persistedPollState) (string, error) {
	state.HMAC = ""
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(domain))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// pendingKeyPrefix marks the failed_keys_full entries that carry pending
// label handoffs on the wire. The whole PendingLabel is encoded in the key
// (count 1, inside the retry-budget range every writer's prune keeps) because
// the document's schema is the only thing a writer from before the handoff
// existed is guaranteed to round-trip. Such a writer ignores the entry —
// no event key matches it — and re-signs it with the rest of the document.
const pendingKeyPrefix = "fullsend-pending-label:"

type pendingWire struct {
	Key     string       `json:"k"`
	Pending PendingLabel `json:"p"`
}

// encodePendingLabels returns state with PendingLabels written into
// FailedKeysFull as pendingKeyPrefix entries (replacing any already there).
// The input maps are not modified.
func encodePendingLabels(state persistedPollState) (persistedPollState, error) {
	if len(state.PendingLabels) == 0 && !hasPendingKeys(state.FailedKeysFull) {
		return state, nil
	}
	failed := make(map[string]int, len(state.FailedKeysFull)+len(state.PendingLabels))
	for k, c := range state.FailedKeysFull {
		if !strings.HasPrefix(k, pendingKeyPrefix) {
			failed[k] = c
		}
	}
	for k, pl := range state.PendingLabels {
		data, err := json.Marshal(pendingWire{Key: k, Pending: pl})
		if err != nil {
			return state, err
		}
		failed[pendingKeyPrefix+base64.RawURLEncoding.EncodeToString(data)] = 1
	}
	if len(failed) == 0 {
		failed = nil
	}
	state.FailedKeysFull = failed
	return state, nil
}

// decodePendingLabels is the inverse of encodePendingLabels: it moves
// pendingKeyPrefix entries out of FailedKeysFull into PendingLabels. A
// malformed entry is dropped with a warning; the document is signed, so that
// only happens on a writer bug, and the occurrence's own failure count and
// the poller's discovery still bound the damage.
func decodePendingLabels(state persistedPollState) persistedPollState {
	if !hasPendingKeys(state.FailedKeysFull) {
		return state
	}
	failed := make(map[string]int, len(state.FailedKeysFull))
	pending := make(map[string]PendingLabel)
	for k, c := range state.FailedKeysFull {
		if !strings.HasPrefix(k, pendingKeyPrefix) {
			failed[k] = c
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(k, pendingKeyPrefix))
		var w pendingWire
		if err == nil {
			err = json.Unmarshal(raw, &w)
		}
		if err != nil || w.Key == "" {
			log.Printf("WARNING: dropping malformed pending-label entry in poll state")
			continue
		}
		pending[w.Key] = w.Pending
	}
	if len(failed) == 0 {
		failed = nil
	}
	state.FailedKeysFull = failed
	if len(pending) > 0 {
		state.PendingLabels = pending
	}
	return state
}

func hasPendingKeys(failed map[string]int) bool {
	for k := range failed {
		if strings.HasPrefix(k, pendingKeyPrefix) {
			return true
		}
	}
	return false
}

// replayKeyPrefix marks failed_keys_{fast,full} entries that mirror a
// dispatched key on the wire. A writer from before the webhook driver prunes
// dispatched_keys_* at the poll watermark and re-signs the document, which
// would erase replay evidence the webhook freshness window still needs. It
// round-trips unknown failed-key entries untouched (count 1, inside the
// retry-budget range every writer's prune keeps), so the mirror survives it
// and the next current writer restores the dispatched key from it.
const replayKeyPrefix = "fullsend-replay-key:"

type replayWire struct {
	Key string `json:"k"`
	TS  int64  `json:"t"`
}

// encodeReplayKeys returns state with every dispatched key mirrored into the
// matching failed-keys map (replacing mirrors already there). The input maps
// are not modified.
func encodeReplayKeys(state persistedPollState) persistedPollState {
	state.FailedKeysFast = mirrorDispatchedKeys(state.FailedKeysFast, state.DispatchedKeysFast)
	state.FailedKeysFull = mirrorDispatchedKeys(state.FailedKeysFull, state.DispatchedKeysFull)
	return state
}

func mirrorDispatchedKeys(failed map[string]int, dispatched map[string]int64) map[string]int {
	if len(dispatched) == 0 && !hasKeyPrefix(failed, replayKeyPrefix) {
		return failed
	}
	out := make(map[string]int, len(failed)+len(dispatched))
	for k, c := range failed {
		if !strings.HasPrefix(k, replayKeyPrefix) {
			out[k] = c
		}
	}
	for k, ts := range dispatched {
		// Marshalling a string and an int64 cannot fail.
		data, _ := json.Marshal(replayWire{Key: k, TS: ts})
		out[replayKeyPrefix+base64.RawURLEncoding.EncodeToString(data)] = 1
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// decodeReplayKeys is the inverse of encodeReplayKeys: it merges mirrored
// entries back into the dispatched maps (max timestamp wins), restoring
// evidence an older writer pruned, and strips them from the failed-keys maps.
func decodeReplayKeys(state persistedPollState) persistedPollState {
	state.FailedKeysFast, state.DispatchedKeysFast = restoreDispatchedKeys(state.FailedKeysFast, state.DispatchedKeysFast)
	state.FailedKeysFull, state.DispatchedKeysFull = restoreDispatchedKeys(state.FailedKeysFull, state.DispatchedKeysFull)
	return state
}

func restoreDispatchedKeys(failed map[string]int, dispatched map[string]int64) (map[string]int, map[string]int64) {
	if !hasKeyPrefix(failed, replayKeyPrefix) {
		return failed, dispatched
	}
	outFailed := make(map[string]int, len(failed))
	outDispatched := make(map[string]int64, len(dispatched))
	for k, ts := range dispatched {
		outDispatched[k] = ts
	}
	for k, c := range failed {
		if !strings.HasPrefix(k, replayKeyPrefix) {
			outFailed[k] = c
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(k, replayKeyPrefix))
		var w replayWire
		if err == nil {
			err = json.Unmarshal(raw, &w)
		}
		if err != nil || w.Key == "" {
			log.Printf("WARNING: dropping malformed replay-key entry in poll state")
			continue
		}
		if prev, ok := outDispatched[w.Key]; !ok || w.TS > prev {
			outDispatched[w.Key] = w.TS
		}
	}
	if len(outFailed) == 0 {
		outFailed = nil
	}
	if len(outDispatched) == 0 {
		outDispatched = nil
	}
	return outFailed, outDispatched
}

func hasKeyPrefix(failed map[string]int, prefix string) bool {
	for k := range failed {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// signPollState encodes state's pending labels and replay-evidence mirrors
// into the wire document and fills in state.HMAC over it.
func signPollState(secret, domain string, state persistedPollState) (persistedPollState, error) {
	state, err := encodePendingLabels(encodeLegacyMirrors(encodeReplayKeys(state)))
	if err != nil {
		return state, err
	}
	sig, err := computeStateHMAC(secret, domain, state)
	if err != nil {
		return state, err
	}
	state.HMAC = sig
	return state, nil
}

// verifyPollState reports whether state's HMAC is valid over the wire
// document as read (pending handoffs included).
func verifyPollState(secret, domain string, state persistedPollState) (bool, error) {
	want, err := computeStateHMAC(secret, domain, state)
	if err != nil {
		return false, err
	}
	return state.HMAC != "" && hmac.Equal([]byte(state.HMAC), []byte(want)), nil
}

// loadPollState loads and verifies the poll-state document at the current
// branch tip. See loadPollStateAtRef for the SHA-pinned variant
// persistWithCAS uses.
func (p *Poller) loadPollState(ctx context.Context, owner, repo string) (persistedPollState, error) {
	return p.loadPollStateAtRef(ctx, owner, repo, p.stateBranch())
}

// loadPollStateAtRef is loadPollState pinned to a specific ref (a branch
// name, or a commit SHA) instead of always resolving the branch tip at
// read time. persistWithCAS uses this to pin its content read to the same
// SHA its own GetBranchRef snapshot returned, so the two reads cannot
// observe two different commits if another writer's commit lands on the
// branch between them.
func (p *Poller) loadPollStateAtRef(ctx context.Context, owner, repo, ref string) (persistedPollState, error) {
	// Fail closed when no secret is configured, before touching the
	// branch. Poll state lives on a Developer-writable branch, so an
	// unsigned document cannot be trusted; aborting here stops the
	// cycle before event discovery and dispatch.
	if p.opts.DispatchSecret == "" {
		return persistedPollState{}, errDispatchSecretUnset
	}
	branch := p.stateBranch()
	data, err := p.client.GetFileContentAtRef(ctx, owner, repo, PollStateFileName, ref)
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			// Missing branch/file is not tampering: start from a
			// fresh baseline. The next save recreates the branch.
			return persistedPollState{}, nil
		}
		return persistedPollState{}, err
	}
	var state persistedPollState
	if err := json.Unmarshal(data, &state); err != nil {
		if discardErr := p.discardPollState(ctx, owner, repo, branch); discardErr != nil {
			return persistedPollState{}, fmt.Errorf("unmarshal poll state on %s: %w (also failed to discard branch: %v)", branch, err, discardErr)
		}
		return persistedPollState{}, fmt.Errorf("unmarshal poll state on %s: %w", branch, err)
	}
	valid, err := verifyPollState(p.opts.DispatchSecret, p.hmacDomain(), state)
	if err != nil {
		return persistedPollState{}, err
	}
	if !valid {
		if discardErr := p.discardPollState(ctx, owner, repo, branch); discardErr != nil {
			return persistedPollState{}, fmt.Errorf("%w on %s; also failed to discard branch: %v", errPollStateTampered, branch, discardErr)
		}
		return persistedPollState{}, fmt.Errorf("%w on %s; discarded branch", errPollStateTampered, branch)
	}
	return p.modeDocument(decodeLegacyMirrors(decodePendingLabels(decodeReplayKeys(state)))), nil
}

func (p *Poller) discardPollState(ctx context.Context, owner, repo, branch string) error {
	err := p.client.DeleteRef(ctx, owner, repo, "heads/"+branch)
	if err != nil && !errors.Is(err, forge.ErrNotFound) {
		return err
	}
	return nil
}

// persistDeltas is the set of mutations this writer wants to land. On a
// CAS conflict, persistWithCAS reloads the latest document and re-applies
// these deltas so concurrent writers union rather than overwrite.
type persistDeltas struct {
	dispatched map[string]int64
	pruneCut   time.Time // cutoff for dispatched-key prune; zero keeps all ts>=0
	watermark  *time.Time
	failed     map[string]int
	labels     LabelState
	// labelsBase is the LabelState snapshot this writer's labels were
	// derived from. When non-nil, only the difference between labels and
	// labelsBase is applied to the reloaded document (see
	// mergeLabelStateAgainst), so a concurrent change to an issue this
	// writer also observed is preserved rather than overwritten by this
	// writer's whole-issue snapshot. Nil keeps the replace-per-IID merge.
	labelsBase LabelState
	// discovered lists the label-addition events this writer found while
	// building labels, so persistWithCAS can revalidate each newly added
	// label's exact occurrence (see revalidateAdditions).
	discovered []RoutableEvent
	// reconcile lists the label-addition occurrences this writer wants
	// recorded in LabelState. persistWithCAS revalidates each one against
	// the forge after every SHA-pinned state load (so a CAS retry rechecks
	// too) and unions only the still-current ones into LabelState via
	// addedLabels. An occurrence whose lookup fails is not recorded; it is
	// handed off as pending instead. reconcileFailed carries the labels
	// that failed dispatch this cycle (see pendingLabelIsCurrent).
	reconcile       []RoutableEvent
	reconcileFailed map[int]map[string]bool
	// addedLabels is unioned into LabelState (never replacing or removing
	// labels) — the webhook driver's label handoff; see unionLabelState.
	// persistWithCAS derives it from reconcile on every attempt.
	addedLabels LabelState
	// stalePresence lists label presences to remove from the reloaded
	// LabelState after the merge: a label recorded for an occurrence that a
	// newer, undispatched occurrence has replaced (see revalidateAdditions).
	// Left in place, that presence would make the next poll treat the
	// replacement as already seen. persistWithCAS derives it on every
	// attempt; it is never set by callers.
	stalePresence LabelState
	// failedPresence lists label presences to remove from the reloaded
	// LabelState after the merge: a poll-discovered occurrence that failed
	// this cycle while this writer's baseline lacked the label, so a
	// concurrent writer's presence for an older occurrence would otherwise
	// hide the failed one from every later poll. Callers set it.
	failedPresence LabelState
	// pendingAdd hands failed label occurrences to the poller and
	// pendingClear removes exactly the occurrences that were dispatched;
	// see mergePendingLabels. Neither touches any other occurrence.
	pendingAdd   map[string]PendingLabel
	pendingClear map[string]bool
	// mirrors registers the Unix-second mirror keys this writer recorded in
	// dispatched / failed for older pollers (see legacy_mirror.go). It is
	// merged add-only, except that a dispatch mirror whose second key the
	// loaded document already holds as genuine legacy evidence is not
	// registered: it stays a plain legacy key.
	mirrors mirrorSet
}

// PendingLabel is a label addition whose webhook dispatch failed, kept
// in poll state until the poller dispatches it (or its retry budget is
// spent). It carries what the poller needs to rebuild the event without
// rediscovering it from the issue list: At is the occurrence time in Unix
// milliseconds. ActorID, ActorLogin and ActorBot are the actor the webhook
// builder bound to this exact addition; they are carried in the signed
// handoff so a retry routes the occurrence under its own actor (checking
// that actor's current access), never under whoever applied the label last.
type PendingLabel struct {
	IID        int      `json:"iid"`
	Label      string   `json:"label"`
	EventID    int      `json:"event_id,omitempty"`
	At         int64    `json:"at"`
	Labels     []string `json:"labels,omitempty"`
	ActorID    int      `json:"actor_id,omitempty"`
	ActorLogin string   `json:"actor_login,omitempty"`
	ActorBot   bool     `json:"actor_bot,omitempty"`
	// OccurredAt is the addition's own occurrence time in Unix milliseconds,
	// kept apart from At (the issue snapshot time) so a pending-only retry
	// keeps the dispatch key's retention anchored to the occurrence. Zero
	// for handoffs written before the field existed.
	OccurredAt int64 `json:"occurred_at,omitempty"`
	// SnapshotAt is RoutableEvent.SnapshotAt in Unix milliseconds: the issue
	// snapshot time the legacy label key derives from when At is the label
	// event's own time. Zero when At is the snapshot time.
	SnapshotAt int64 `json:"snapshot_at,omitempty"`
	// Unresolved marks issue-level reconciliation work rather than a known
	// occurrence: a writer could not resolve which occurrence of Label on
	// IID is current (a label-event lookup failed), so it hands the poller
	// the obligation to resolve and dispatch it. The marker is independent
	// of LabelState and of the updated_at watermark, so a competing poll
	// advancing the watermark cannot lose it. EventID, At and the actor
	// fields are unset; the poller binds them when it resolves the marker.
	Unresolved bool `json:"unresolved,omitempty"`
}

// unresolvedLabelKeyPrefix starts the key of an Unresolved pending marker.
const unresolvedLabelKeyPrefix = "unresolved-label-"

// unresolvedLabelMarker builds the Unresolved pending marker for label on
// issue iid. The key carries the queue time (Unix milliseconds) so that
// clearing a marker the poller resolved removes exactly that marker, never a
// newer one a concurrent writer queued for the same issue and label after
// this poll read state; markers for one issue and label that resolve to the
// same occurrence deduplicate by occurrence key.
func unresolvedLabelMarker(iid int, label string, at time.Time) (string, PendingLabel) {
	key := fmt.Sprintf("%s%d-%s-%d", unresolvedLabelKeyPrefix, iid, label, at.UnixMilli())
	return key, PendingLabel{IID: iid, Label: label, At: at.UnixMilli(), Unresolved: true}
}

// withUnresolvedMarkers returns a copy of deltas that queues an Unresolved
// pending marker for every issue/label in unresolved.
func withUnresolvedMarkers(deltas persistDeltas, unresolved LabelState, at time.Time) persistDeltas {
	if len(unresolved) == 0 {
		return deltas
	}
	pendingAdd := make(map[string]PendingLabel, len(deltas.pendingAdd)+len(unresolved))
	for k, v := range deltas.pendingAdd {
		pendingAdd[k] = v
	}
	pendingClear := make(map[string]bool, len(deltas.pendingClear))
	for k, v := range deltas.pendingClear {
		pendingClear[k] = v
	}
	for iid, labels := range unresolved {
		for _, label := range labels {
			key, marker := unresolvedLabelMarker(iid, label, at)
			pendingAdd[key] = marker
			delete(pendingClear, key)
		}
	}
	out := deltas
	out.pendingAdd = pendingAdd
	out.pendingClear = pendingClear
	return out
}

// pendingLabelFor records event as a pending occurrence.
func pendingLabelFor(event RoutableEvent) PendingLabel {
	pl := PendingLabel{
		IID:        event.IID,
		Label:      event.ChangedLabel,
		EventID:    event.LabelEventID,
		At:         event.UpdatedAt.UnixMilli(),
		Labels:     event.Labels,
		ActorID:    event.NoteAuthorID,
		ActorLogin: event.NoteAuthorLogin,
		ActorBot:   event.IsBot,
	}
	if !event.OccurredAt.IsZero() {
		pl.OccurredAt = event.OccurredAt.UnixMilli()
	}
	if !event.SnapshotAt.IsZero() {
		pl.SnapshotAt = event.SnapshotAt.UnixMilli()
	}
	return pl
}

// routableEvent rebuilds the label-addition event of a pending occurrence,
// including the actor validated when it was recorded.
func (pl PendingLabel) routableEvent() RoutableEvent {
	var occurredAt time.Time
	if pl.OccurredAt != 0 {
		occurredAt = time.UnixMilli(pl.OccurredAt)
	}
	var snapshotAt time.Time
	if pl.SnapshotAt != 0 {
		snapshotAt = time.UnixMilli(pl.SnapshotAt)
	}
	return RoutableEvent{
		OccurredAt:      occurredAt,
		SnapshotAt:      snapshotAt,
		Type:            "issue_label",
		IID:             pl.IID,
		UpdatedAt:       time.UnixMilli(pl.At),
		Labels:          pl.Labels,
		ChangedLabel:    pl.Label,
		LabelEventID:    pl.EventID,
		NoteAuthorID:    pl.ActorID,
		NoteAuthorLogin: pl.ActorLogin,
		IsBot:           pl.ActorBot,
	}
}

// mergePendingLabels applies add and clear onto the freshly loaded
// pending set. Only the exact occurrence keys named in clear are removed,
// and add never removes anything, so a stale in-flight poll (which clears
// only what it dispatched) cannot erase a newer retry handoff, and a
// handoff cannot erase a concurrent poll's clear of another occurrence.
func mergePendingLabels(existing, add map[string]PendingLabel, clear map[string]bool) map[string]PendingLabel {
	merged := make(map[string]PendingLabel, len(existing)+len(add))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range add {
		merged[k] = v
	}
	for k := range clear {
		if _, handedOff := add[k]; handedOff {
			continue
		}
		delete(merged, k)
	}
	return merged
}

func (p *Poller) commitPollState(ctx context.Context, owner, repo string, state persistedPollState, expectedSHA string) error {
	if p.opts.DispatchSecret == "" {
		return errDispatchSecretUnset
	}
	state, err := signPollState(p.opts.DispatchSecret, p.hmacDomain(), p.modeDocument(state))
	if err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	branch := p.stateBranch()
	return p.client.CommitFileToBranch(ctx, owner, repo, branch, PollStateFileName, "fullsend: persist poll state", data, expectedSHA)
}

// applyPersistDeltas applies this writer's deltas onto state, which was
// just (re)loaded fresh in persistWithCAS's loop. Every field is merged
// against that freshly loaded document on every attempt, not only after a
// detected 409: "uncontended" only means no other commit landed between
// this writer's own GetBranchRef and its commit, not that the reloaded
// document is free of a concurrent writer's disjoint update that raced
// this writer's in-flight cycle (a concurrent writer's commit that this
// writer's own commit still fast-forwards past never triggers a 409, so a
// wholesale replace on the "uncontended" first attempt could silently
// drop it).
//
// Deletions this writer intentionally made this cycle — a failed-key
// count cleared by a successful dispatch, a label entry cleared by a
// closed issue or a label removal — are carried as tombstones so a merge
// against a stale existing value cannot resurrect what this writer
// explicitly cleared: a failed-key count of exactly 0 (see
// unionFailedKeys), or a present-but-empty label slice (see
// mergeLabelState). A field this writer has no opinion on this cycle is
// simply absent from the delta/map and is left untouched by the merge.
func (p *Poller) applyPersistDeltas(state persistedPollState, deltas persistDeltas) persistedPollState {
	// Pending handoffs merge first: the dispatched-key prune below must keep
	// the completion evidence of every occurrence still pending afterwards.
	if !p.slashCommandsOnly && (len(deltas.pendingAdd) > 0 || len(deltas.pendingClear) > 0) {
		state.PendingLabels = mergePendingLabels(state.PendingLabels, deltas.pendingAdd, deltas.pendingClear)
	}
	if len(deltas.mirrors) > 0 && !p.slashCommandsOnly {
		state.LegacyMirrors = mergeLegacyMirrors(state, deltas.mirrors)
	}
	if deltas.dispatched != nil {
		unionDispatchedKeys(&state, p.slashCommandsOnly, deltas.dispatched, deltas.pruneCut, protectedDispatchKey(state.PendingLabels))
	}
	if deltas.failed != nil {
		loadedFailed := make(map[string]int, len(state.FailedKeysFull))
		for k, c := range state.FailedKeysFull {
			loadedFailed[k] = c
		}
		unionFailedKeys(&state, p.slashCommandsOnly, deltas.failed)
		if !p.slashCommandsOnly {
			restoreSharedFailureMirrors(&state, deltas.failed, loadedFailed)
		}
	}
	if !p.slashCommandsOnly {
		state.LegacyMirrors = pruneLegacyMirrors(state)
	}
	if deltas.watermark != nil {
		p.applyWatermark(&state, *deltas.watermark)
	}
	if deltas.labels != nil {
		if deltas.labelsBase != nil {
			state.LabelState = mergeLabelStateAgainst(state.LabelState, deltas.labelsBase, deltas.labels)
		} else {
			state.LabelState = mergeLabelState(state.LabelState, deltas.labels)
		}
	}
	if deltas.addedLabels != nil {
		state.LabelState = unionLabelState(state.LabelState, deltas.addedLabels)
	}
	if deltas.stalePresence != nil {
		state.LabelState = removeLabelPresence(state.LabelState, deltas.stalePresence)
	}
	if deltas.failedPresence != nil {
		state.LabelState = removeLabelPresence(state.LabelState, deltas.failedPresence)
	}
	return state
}

// mergeLegacyMirrors unions the writer's mirror registrations into the loaded
// ledger. A dispatch mirror whose second key the loaded document already holds
// without a ledger entry is genuine legacy evidence from an older poller and
// is left unregistered, so it keeps suppressing what it always did.
func mergeLegacyMirrors(state persistedPollState, add mirrorSet) mirrorSet {
	merged := state.LegacyMirrors.clone()
	for ref := range add {
		if merged[ref] {
			continue
		}
		if ref.Kind == mirrorKindDispatch {
			if _, held := state.DispatchedKeysFull[ref.Legacy]; held && !state.LegacyMirrors.isMirror(ref.Kind, ref.Legacy) {
				continue
			}
		}
		merged[ref] = true
	}
	return merged
}

// pruneLegacyMirrors drops ledger entries whose second key is no longer
// present, so the ledger never outlives the keys it describes.
func pruneLegacyMirrors(state persistedPollState) mirrorSet {
	if len(state.LegacyMirrors) == 0 {
		return nil
	}
	out := make(mirrorSet, len(state.LegacyMirrors))
	for ref := range state.LegacyMirrors {
		switch ref.Kind {
		case mirrorKindDispatch:
			if _, ok := state.DispatchedKeysFull[ref.Legacy]; !ok {
				continue
			}
		case mirrorKindFailure:
			if _, ok := state.FailedKeysFull[ref.Legacy]; !ok {
				continue
			}
		}
		out[ref] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// readWatermark reads the last-polled timestamp from poller state.
// On first run (no stored value), it defaults to one hour ago.
func (p *Poller) readWatermark(ctx context.Context, owner, repo string) (time.Time, error) {
	state, err := p.loadPollState(ctx, owner, repo)
	if err != nil {
		return time.Time{}, err
	}
	val := state.LastPollAtFull
	if p.slashCommandsOnly {
		val = state.LastPollAtFast
	}
	if val == "" {
		return time.Now().Add(-1 * time.Hour), nil
	}
	t, err := time.Parse(time.RFC3339, val)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// updateWatermark persists the given timestamp as the poll watermark.
func (p *Poller) updateWatermark(ctx context.Context, owner, repo string, t time.Time) error {
	return p.persistCycleState(ctx, owner, repo, nil, &t, nil, nil)
}

// persistCycleState loads poll state, applies the provided mutations, and
// writes a single commit under a conflict-detecting CAS loop. Nil dispatched
// / watermark / failed / labels leave the corresponding stored fields
// unchanged. One poll cycle therefore produces one poll-state commit instead
// of one commit per field. On ErrNonFastForward the latest document is
// reloaded and this writer's deltas are re-applied so concurrent writers
// union rather than overwrite.
func (p *Poller) persistCycleState(ctx context.Context, owner, repo string, dispatched map[string]int64, watermark *time.Time, failed map[string]int, labels LabelState) error {
	deltas := persistDeltas{
		dispatched: dispatched,
		watermark:  watermark,
		failed:     failed,
		labels:     labels,
	}
	return p.persistCycleDeltas(ctx, owner, repo, deltas)
}

// persistCycleDeltas is persistCycleState for a caller that also carries
// pending-label clears; the dispatched-key prune cutoff follows the
// watermark delta.
func (p *Poller) persistCycleDeltas(ctx context.Context, owner, repo string, deltas persistDeltas) error {
	if deltas.watermark != nil {
		deltas.pruneCut = *deltas.watermark
	}
	return p.persistWithCAS(ctx, owner, repo, deltas)
}

func (p *Poller) persistWithCAS(ctx context.Context, owner, repo string, deltas persistDeltas) error {
	if p.opts.DispatchSecret == "" {
		return errDispatchSecretUnset
	}
	branch := p.stateBranch()
	var lastErr error
	var reconcileErrs []error
	for attempt := 1; attempt <= maxPollStateCASAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		expectedSHA, err := p.client.GetBranchRef(ctx, owner, repo, branch)
		if err != nil {
			if !forge.IsNotFound(err) {
				return err
			}
			expectedSHA = ""
		}
		// Pin the content read to the exact SHA GetBranchRef just
		// returned (falling back to the branch name when the branch was
		// observed missing) so the two reads cannot diverge if another
		// commit lands on the branch between them.
		ref := branch
		if expectedSHA != "" {
			ref = expectedSHA
		}
		state, err := p.loadPollStateAtRef(ctx, owner, repo, ref)
		if err != nil {
			return err
		}
		// Revalidate label additions against the forge now, after this
		// attempt's state load: a removal another poll recorded since an
		// earlier check (or since the previous attempt) is visible on the
		// forge, so the union below cannot restore its stale presence. A
		// removal whose state commit lands after this load makes the commit
		// below lose its CAS, and the next attempt revalidates again.
		attemptDeltas := deltas
		reconcileErrs = nil
		if len(deltas.reconcile) > 0 {
			attemptDeltas, reconcileErrs = p.revalidateReconcile(ctx, state, deltas)
		}
		// Likewise revalidate the label removals this writer derived from
		// its own, possibly stale, snapshot: one must not erase a
		// re-addition another writer recorded (and dispatched) since.
		attemptDeltas, removalErrs := p.revalidateRemovals(ctx, state, attemptDeltas)
		reconcileErrs = append(reconcileErrs, removalErrs...)
		if len(removalErrs) > 0 {
			// A removal whose revalidation failed leaves its label recorded.
			// Nothing else stores that unresolved removal, so the watermark
			// must not advance past the issue: the next poll has to rediscover
			// it and derive the removal again. Dispatch keys and the other
			// deltas are still persisted. Retention is relative to the
			// watermark rediscovery will start from, so the prune cutoff
			// that followed the proposed watermark is reset too: it must
			// not drop dispatch evidence the retained watermark needs.
			attemptDeltas.watermark = nil
			attemptDeltas.pruneCut = time.Time{}
		}
		// Revalidate the label additions this writer discovered the same way:
		// its raw label delta must not restore presence for an occurrence that
		// was removed (and recorded as removed) since the snapshot.
		attemptDeltas, additionErrs := p.revalidateAdditions(ctx, state, attemptDeltas)
		reconcileErrs = append(reconcileErrs, additionErrs...)
		if len(additionErrs) > 0 {
			// A dropped addition whose lookup failed is handed off as a pending
			// label (see revalidateAdditions), so recovery does not depend on
			// the watermark. Hold the watermark as well so the next poll can
			// rediscover the issue; the dispatch keys persisted here
			// deduplicate the rediscovered occurrence. Reset the prune cutoff
			// with it so that evidence is not pruned against the proposed
			// (advanced) watermark.
			attemptDeltas.watermark = nil
			attemptDeltas.pruneCut = time.Time{}
		}
		state = p.applyPersistDeltas(state, attemptDeltas)
		err = p.commitPollState(ctx, owner, repo, state, expectedSHA)
		if err == nil {
			return errors.Join(reconcileErrs...)
		}
		if !forge.IsNonFastForward(err) {
			return err
		}
		lastErr = err
		log.Printf("poll state CAS conflict on %s (attempt %d/%d): %v", branch, attempt, maxPollStateCASAttempts, err)
	}
	return fmt.Errorf("%w after %d attempts: %w", errPollStateCASExhausted, maxPollStateCASAttempts, lastErr)
}

// revalidateReconcile resolves deltas.reconcile into the label presence that
// may be recorded right now. It returns a copy of deltas with addedLabels set
// to the occurrences the issue still carries (see pendingLabelIsCurrent) and,
// for an occurrence whose lookup failed, a pending handoff (so the poller
// reconciles it later without re-dispatching) instead of a recorded label.
// Dispatch keys and every other delta are left untouched, so a successful
// dispatch is preserved independently of label reconciliation. The lookup
// errors are returned for the caller to report once the commit lands.
func (p *Poller) revalidateReconcile(ctx context.Context, state persistedPollState, deltas persistDeltas) (persistDeltas, []error) {
	added := make(LabelState)
	pendingAdd := make(map[string]PendingLabel, len(deltas.pendingAdd))
	for k, v := range deltas.pendingAdd {
		pendingAdd[k] = v
	}
	pendingClear := make(map[string]bool, len(deltas.pendingClear))
	for k, v := range deltas.pendingClear {
		pendingClear[k] = v
	}
	var errs []error
	var replacements []RoutableEvent
	var unresolved LabelState
	for _, event := range deltas.reconcile {
		record, err := p.pendingLabelIsCurrent(ctx, event, deltas.reconcileFailed)
		switch {
		case err == nil:
			if record {
				added[event.IID] = append(added[event.IID], event.ChangedLabel)
				continue
			}
			// Not current: the issue lost the label, or a newer occurrence
			// replaced this one. A replacement with no dispatch evidence is
			// recorded nowhere, so hand it off as pending; if it cannot be
			// looked up, queue issue-level reconciliation instead.
			_, undispatched, replacement, lookupErr := p.removalSupersededByDispatchedAdd(ctx, state, deltas, event.IID, event.ChangedLabel)
			// The occurrence being reconciled is never its own replacement: an
			// exhausted occurrence that is still current (but was not recorded
			// because it failed) must not be handed off again.
			if undispatched && replacement.Key() != event.Key() {
				replacements = append(replacements, replacement)
			}
			if lookupErr != nil {
				errs = append(errs, fmt.Errorf("reconcile label replacement for issue %d label %q: %w", event.IID, event.ChangedLabel, lookupErr))
				if unresolved == nil {
					unresolved = make(LabelState)
				}
				unresolved[event.IID] = append(unresolved[event.IID], event.ChangedLabel)
			}
		case forge.IsNotFound(err):
			// The issue is gone: nothing to record.
		default:
			key := event.Key()
			errs = append(errs, fmt.Errorf("reconcile label state for %s: %w", key, err))
			pendingAdd[key] = pendingLabelFor(event)
			delete(pendingClear, key)
		}
	}
	out := deltas
	out.pendingAdd = pendingAdd
	out.pendingClear = pendingClear
	out = withPendingReplacements(out, replacements)
	out = withUnresolvedMarkers(out, unresolved, time.Now())
	out.addedLabels = nil
	if len(added) > 0 {
		out.addedLabels = added
	}
	return out, errs
}

// revalidateRemovals drops, from the label removals in deltas.labels, those
// that would erase a newer re-addition. A removal is a label in
// deltas.labelsBase that deltas.labels no longer carries: this writer's
// issue snapshot lacked the label. If the freshly loaded state still records
// the label, a concurrent writer (the webhook driver) may have recorded a
// re-addition after that snapshot, and applying the stale removal would
// erase that presence — after the dispatch key expires, discovery would then
// dispatch the same re-addition again.
//
// The forge decides: the removal is kept only when the issue is gone, closed,
// or no longer carries the label, or when its latest label event is an
// occurrence with no dispatch evidence (so the next poll must still see it as
// new). It is dropped (the label stays recorded) when the issue carries the
// label and the latest add occurrence already has a dispatch key. A failed
// lookup also keeps the label recorded and is returned for the caller to
// report; the next cycle sees the label still absent from the issue and
// removes it again. Returns a copy of deltas; deltas itself is not modified.
func (p *Poller) revalidateRemovals(ctx context.Context, state persistedPollState, deltas persistDeltas) (persistDeltas, []error) {
	if deltas.labels == nil || deltas.labelsBase == nil {
		return deltas, nil
	}
	var labels, unresolved LabelState
	var errs []error
	var replacements []RoutableEvent
	holdWatermark := false
	for iid, incoming := range deltas.labels {
		incomingSet := toSet(incoming)
		recorded := toSet(state.LabelState[iid])
		for _, label := range deltas.labelsBase[iid] {
			if incomingSet[label] || !recorded[label] {
				continue
			}
			keep, undispatched, replacement, err := p.removalSupersededByDispatchedAdd(ctx, state, deltas, iid, label)
			if undispatched {
				holdWatermark = true
				replacements = append(replacements, replacement)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("revalidate label removal for issue %d label %q: %w", iid, label, err))
				// The removal could not be judged against the current
				// occurrence: a stale snapshot may be followed by an
				// undispatched re-addition, and the retained presence would
				// suppress its discovery on later issue updates. Holding this
				// writer's watermark advance cannot undo a competing poll's
				// persisted one, so queue issue-level reconciliation: the poller
				// resolves and dispatches the current occurrence independent of
				// the watermark and of the retained presence.
				if unresolved == nil {
					unresolved = make(LabelState)
				}
				unresolved[iid] = append(unresolved[iid], label)
			}
			if !keep {
				continue
			}
			if labels == nil {
				labels = make(LabelState, len(deltas.labels))
				for k, v := range deltas.labels {
					labels[k] = append([]string{}, v...)
				}
			}
			labels[iid] = append(labels[iid], label)
			incomingSet[label] = true
		}
	}
	out := withPendingReplacements(deltas, replacements)
	out = withUnresolvedMarkers(out, unresolved, time.Now())
	if labels != nil {
		out.labels = labels
	}
	if holdWatermark {
		// The removal is applied, but a currently present re-addition has no
		// dispatch evidence and is recorded nowhere. Hold the watermark so the
		// next poll rediscovers the issue and dispatches the addition even if
		// another event advanced the watermark past the issue's updated_at.
		// The prune cutoff followed the proposed watermark, so reset it too.
		out.watermark = nil
		out.pruneCut = time.Time{}
	}
	return out, errs
}

// revalidateAdditions drops, from deltas.labels, the label additions (labels
// in the incoming snapshot that deltas.labelsBase lacks) whose occurrence the
// forge no longer shows as current: the issue is gone, closed, no longer
// carries the label, or its latest label event is a different occurrence than
// the one this writer discovered (deltas.discovered supplies the event ID).
// Without this, the merge would add the stale presence on top of a removal
// another writer already recorded, and a later re-add whose webhook is missed
// would be suppressed by that presence. A dropped addition is simply not
// recorded: the next poll rediscovers the label and deduplicates by dispatch
// key. A failed lookup is handled the same way and returned for the caller to
// report; since the superseded-versus-current question is then unresolved, any
// presence a concurrent writer recorded for the label is removed as well
// (deltas.stalePresence), so the rediscovery is not suppressed by it, and
// the discovered occurrence is handed off as a pending label so it is
// reconciled even when a competing poll has advanced the watermark.
//
// When the addition is dropped because a newer addition is currently present
// and has no dispatch evidence (a replacement occurrence this writer never
// saw), that replacement is recorded nowhere, so the watermark is held for the
// next poll to rediscover and dispatch it even if an unrelated event would
// otherwise advance the watermark past the issue's updated_at, and any label
// presence a concurrent writer recorded for the superseded occurrence is
// removed (deltas.stalePresence) so the replacement is not mistaken for an
// already-seen label. Returns a copy
// of deltas; deltas itself is not modified.
func (p *Poller) revalidateAdditions(ctx context.Context, state persistedPollState, deltas persistDeltas) (persistDeltas, []error) {
	if deltas.labels == nil || deltas.labelsBase == nil {
		return deltas, nil
	}
	discovered := make(map[int]map[string]RoutableEvent)
	for _, event := range deltas.discovered {
		if discovered[event.IID] == nil {
			discovered[event.IID] = make(map[string]RoutableEvent)
		}
		discovered[event.IID][event.ChangedLabel] = event
	}
	var labels, stale, unresolved LabelState
	var errs []error
	var replacements []RoutableEvent
	holdWatermark := false
	for iid, incoming := range deltas.labels {
		baseSet := toSet(deltas.labelsBase[iid])
		for _, label := range incoming {
			if baseSet[label] {
				continue
			}
			event, ok := discovered[iid][label]
			if !ok {
				event = RoutableEvent{Type: "issue_label", IID: iid, ChangedLabel: label}
			}
			current, err := p.pendingLabelIsCurrent(ctx, event, deltas.reconcileFailed)
			if err == nil && current {
				continue
			}
			if err != nil && !forge.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("revalidate label addition for issue %d label %q: %w", iid, label, err))
				// The occurrence could not be resolved, so a presence a
				// concurrent writer recorded for it may belong to a
				// superseded occurrence, and the merge would keep it. Holding
				// the watermark alone cannot recover then: the rediscovery
				// would see the label as already recorded. Remove the
				// unresolved presence conservatively; the dispatch keys
				// persisted here deduplicate the rediscovered occurrence if
				// it was in fact current and dispatched.
				if stale == nil {
					stale = make(LabelState)
				}
				stale[iid] = append(stale[iid], label)
				// Presence removal and a held watermark cannot recover the
				// occurrence if a competing poll already advanced the
				// persisted watermark past the issue. Hand the discovered
				// occurrence off as pending: the poller retries it
				// independent of the watermark, skips its already
				// dispatched stages, and restores the presence once the
				// lookup succeeds.
				if ok {
					replacements = append(replacements, event)
				}
				// The discovered occurrence may itself be superseded by an
				// addition this writer never saw (removed and re-added
				// before the lookup failed), and it is queued above only
				// as the old occurrence. Queue issue-level reconciliation
				// as well, so the poller resolves whichever occurrence is
				// current and dispatches it with its own actor.
				if unresolved == nil {
					unresolved = make(LabelState)
				}
				unresolved[iid] = append(unresolved[iid], label)
			}
			if err == nil {
				// Not current without a lookup error: either the issue lost
				// the label (nothing to recover) or a different occurrence
				// replaced the discovered one. Hold the watermark when that
				// replacement is present without dispatch evidence. A repeat
				// lookup failure holds it conservatively.
				_, undispatched, replacement, lookupErr := p.removalSupersededByDispatchedAdd(ctx, state, deltas, iid, label)
				if undispatched {
					replacements = append(replacements, replacement)
				}
				if lookupErr != nil {
					// The replacement lookup failed, so whether an
					// undispatched replacement exists is unresolved. Report
					// the failure and queue issue-level reconciliation, as
					// the other revalidation-failure branches do: holding
					// this writer's watermark cannot undo a competing poll
					// that already advanced the persisted one.
					errs = append(errs, fmt.Errorf("revalidate label replacement for issue %d label %q: %w", iid, label, lookupErr))
					if unresolved == nil {
						unresolved = make(LabelState)
					}
					unresolved[iid] = append(unresolved[iid], label)
				}
				if undispatched || lookupErr != nil {
					holdWatermark = true
					// A concurrent writer may already have recorded presence
					// for the superseded occurrence, and the merge keeps it:
					// the next poll would then see the replacement's label as
					// already seen. Remove it so the replacement is
					// rediscovered (dispatch keys deduplicate if it was
					// dispatched meanwhile).
					if stale == nil {
						stale = make(LabelState)
					}
					stale[iid] = append(stale[iid], label)
				}
			}
			if labels == nil {
				labels = make(LabelState, len(deltas.labels))
				for k, v := range deltas.labels {
					labels[k] = append([]string{}, v...)
				}
			}
			kept := labels[iid][:0]
			for _, l := range labels[iid] {
				if l != label {
					kept = append(kept, l)
				}
			}
			labels[iid] = kept
		}
	}
	out := withPendingReplacements(deltas, replacements)
	out = withUnresolvedMarkers(out, unresolved, time.Now())
	if labels != nil {
		out.labels = labels
	}
	if stale != nil {
		out.stalePresence = stale
	}
	if holdWatermark {
		out.watermark = nil
		out.pruneCut = time.Time{}
	}
	return out, errs
}

// removalSupersededByDispatchedAdd reports whether the recorded presence of
// label on issue iid must survive a removal derived from a stale snapshot:
// true when the issue currently carries the label and its latest add
// occurrence already has a dispatch key in the loaded state or in this
// writer's own dispatched keys. On a lookup failure it returns true with the
// error, so the presence is kept rather than guessed away.
//
// The second result is true when the removal is applied although the issue
// currently carries the label under a latest add occurrence with no dispatch
// evidence: that re-addition is recorded nowhere, so the caller must hold the
// watermark for the next poll to rediscover it. The third result is then that
// undispatched replacement occurrence, which the caller also hands off as a
// pending label (see withPendingReplacements): holding the watermark alone is
// not CAS-safe, since a competing poll may already have advanced the persisted
// one.
func (p *Poller) removalSupersededByDispatchedAdd(ctx context.Context, state persistedPollState, deltas persistDeltas, iid int, label string) (bool, bool, RoutableEvent, error) {
	issue, err := p.client.GetIssue(ctx, p.owner, p.repo, iid)
	switch {
	case err == nil:
	case forge.IsNotFound(err):
		return false, false, RoutableEvent{}, nil
	default:
		return true, false, RoutableEvent{}, err
	}
	if issue.State == "closed" || !toSet(issue.Labels)[label] {
		return false, false, RoutableEvent{}, nil
	}
	labelEvents, err := p.client.ListResourceLabelEvents(ctx, p.owner, p.repo, iid)
	if err != nil {
		return true, false, RoutableEvent{}, err
	}
	latest, ok := latestLabelEvent(labelEvents, label)
	if !ok || latest.Action != "add" || latest.ID == 0 {
		return false, false, RoutableEvent{}, nil
	}
	suffix := ":" + RoutableEvent{Type: "issue_label", IID: iid, ChangedLabel: label, LabelEventID: latest.ID}.Key()
	for _, keys := range []map[string]int64{state.DispatchedKeysFull, state.DispatchedKeysFast, deltas.dispatched} {
		for k := range keys {
			if strings.HasSuffix(k, suffix) {
				return true, false, RoutableEvent{}, nil
			}
		}
	}
	return false, true, labelAdditionEvent(issue, label, latest), nil
}

// labelAdditionEvent builds the label-addition event for the add occurrence
// latest of label on issue, binding the actor of that exact occurrence as
// discovery does. SnapshotAt carries the issue's updated_at, which an older
// poller keys the addition on, so the legacy compatibility mirror stays
// recognisable to it.
func labelAdditionEvent(issue *Issue, label string, latest ResourceLabelEvent) RoutableEvent {
	event := RoutableEvent{
		Type:         "issue_label",
		IID:          issue.IID,
		UpdatedAt:    latest.CreatedAt,
		SnapshotAt:   issue.UpdatedAt,
		Labels:       issue.Labels,
		ChangedLabel: label,
		LabelEventID: latest.ID,
	}
	if latest.User.ID != 0 && latest.User.Username != "" {
		event.NoteAuthorID = latest.User.ID
		event.NoteAuthorLogin = latest.User.Username
		event.IsBot = latest.User.Bot
	}
	return event
}

// resolveUnresolvedLabel resolves an Unresolved pending marker: the current
// add occurrence of the marker's label on its issue, with its own actor. The
// bool is false when there is nothing to dispatch (the issue is gone or
// closed, no longer carries the label, or its latest label event is not an
// addition). A lookup failure is returned so the marker stays queued.
func (p *Poller) resolveUnresolvedLabel(ctx context.Context, pl PendingLabel) (RoutableEvent, bool, error) {
	issue, err := p.client.GetIssue(ctx, p.owner, p.repo, pl.IID)
	if err != nil {
		if forge.IsNotFound(err) {
			return RoutableEvent{}, false, nil
		}
		return RoutableEvent{}, false, err
	}
	if issue.State == "closed" || !toSet(issue.Labels)[pl.Label] {
		return RoutableEvent{}, false, nil
	}
	labelEvents, err := p.client.ListResourceLabelEvents(ctx, p.owner, p.repo, pl.IID)
	if err != nil {
		return RoutableEvent{}, false, err
	}
	latest, ok := latestLabelEvent(labelEvents, pl.Label)
	if !ok || latest.Action != "add" || latest.ID == 0 {
		return RoutableEvent{}, false, nil
	}
	return labelAdditionEvent(issue, pl.Label, latest), true, nil
}

// withPendingReplacements returns a copy of deltas that hands each
// replacement occurrence to the poller as a pending label. The pending
// handoff is keyed by occurrence, merged add-only against the freshly loaded
// document, and retried by the poller independent of the watermark and of
// LabelState, so it survives a competing poll advancing the watermark.
func withPendingReplacements(deltas persistDeltas, replacements []RoutableEvent) persistDeltas {
	if len(replacements) == 0 {
		return deltas
	}
	pendingAdd := make(map[string]PendingLabel, len(deltas.pendingAdd)+len(replacements))
	for k, v := range deltas.pendingAdd {
		pendingAdd[k] = v
	}
	pendingClear := make(map[string]bool, len(deltas.pendingClear))
	for k, v := range deltas.pendingClear {
		pendingClear[k] = v
	}
	for _, event := range replacements {
		key := event.Key()
		pendingAdd[key] = pendingLabelFor(event)
		delete(pendingClear, key)
	}
	out := deltas
	out.pendingAdd = pendingAdd
	out.pendingClear = pendingClear
	return out
}

// dispatchedKeyRetention is how far behind the poll watermark dispatched
// keys are kept. A watermark is not proof that a particular occurrence was
// dispatched, and the webhook driver accepts event evidence for up to
// webhookMaxEventAge (plus clock skew), so a key must outlive that window
// or a replayed or delayed webhook could dispatch the occurrence again.
const dispatchedKeyRetention = webhookMaxEventAge + webhookMaxClockSkew

// pruneDispatchedKeys drops keys older than the retention window behind
// watermark, except keys protect reports as still needed: the completion
// evidence of an occurrence pending reconciliation outlives the freshness
// window, or its next retry (after a failed reconcile lookup) would create the
// pipeline again once an unrelated event advanced the watermark.
func pruneDispatchedKeys(keys map[string]int64, watermark time.Time, protect func(string) bool) map[string]int64 {
	cutoff := watermark.Add(-dispatchedKeyRetention).Unix()
	pruned := make(map[string]int64, len(keys))
	for k, ts := range keys {
		if ts >= cutoff || (protect != nil && protect(k)) {
			pruned[k] = ts
		}
	}
	return pruned
}

func pruneFailedKeys(keys map[string]int) map[string]int {
	pruned := make(map[string]int, len(keys))
	for k, count := range keys {
		if count > 0 && count <= maxEventRetries {
			pruned[k] = count
		}
	}
	return pruned
}

// unionDispatchedKeys merges this writer's keys into the loaded document
// (max timestamp wins per key) then prunes by watermark. Used on every
// persistWithCAS attempt so a concurrent writer's keys survive.
func unionDispatchedKeys(state *persistedPollState, slash bool, keys map[string]int64, watermark time.Time, protect func(string) bool) {
	existing := state.DispatchedKeysFull
	if slash {
		existing = state.DispatchedKeysFast
	}
	merged := make(map[string]int64, len(existing)+len(keys))
	for k, ts := range existing {
		merged[k] = ts
	}
	for k, ts := range keys {
		if prev, ok := merged[k]; !ok || ts > prev {
			merged[k] = ts
		}
	}
	pruned := pruneDispatchedKeys(merged, watermark, protect)
	if slash {
		state.DispatchedKeysFast = pruned
	} else {
		state.DispatchedKeysFull = pruned
	}
}

// unionFailedKeys merges this writer's failed-key retry counts into the
// loaded document (max count per key survives) then prunes via
// pruneFailedKeys, mirroring unionDispatchedKeys. Used on every
// persistWithCAS attempt (not only a detected 409) so a concurrent
// writer's disjoint failed-key counts survive even when this writer's own
// commit lands as a clean fast-forward.
//
// A key present in keys with a count of exactly 0 is a tombstone: this
// writer intentionally resolved it this cycle (e.g. a successful dispatch
// cleared its retry count via poll.go's `delete` becoming a 0-count
// entry before persisting), and that deletion wins over whatever count
// the freshly reloaded document still carries for it. A plain max-count
// union would otherwise resurrect a pre-cycle failure count that this
// writer explicitly cleared, letting a later failure exhaust
// maxEventRetries one successful dispatch too early. Real failure counts
// are always >= 1 (recordEventFailure only increments), so 0 is
// unambiguous as a tombstone. pruneFailedKeys already strips any
// remaining <= 0 entries, so a tombstone never reaches the persisted
// document.
func unionFailedKeys(state *persistedPollState, slash bool, keys map[string]int) {
	existing := state.FailedKeysFull
	if slash {
		existing = state.FailedKeysFast
	}
	merged := make(map[string]int, len(existing)+len(keys))
	for k, c := range existing {
		merged[k] = c
	}
	for k, c := range keys {
		if c <= 0 {
			delete(merged, k)
			continue
		}
		if prev, ok := merged[k]; !ok || c > prev {
			merged[k] = c
		}
	}
	pruned := pruneFailedKeys(merged)
	if slash {
		state.FailedKeysFast = pruned
	} else {
		state.FailedKeysFull = pruned
	}
}

// restoreSharedFailureMirrors recomputes a shared Unix-second failure mirror
// that this writer tombstoned, from the merged document. The tombstone
// reflects only this writer's owner: a concurrent writer may have recorded a
// failure for another occurrence of the same second after this writer loaded
// its state, and that occurrence's count survives the merge under its own
// key. Deleting the mirror would drop its ledger registration (see
// pruneLegacyMirrors) and let an older poller restart that occurrence's
// budget, so the mirror carries the largest surviving owner count instead.
//
// A positive incoming mirror count is recomputed the same way: the max-count
// union would otherwise keep a persisted count inflated by an owner this
// writer just cleared (owners A=2 and B=1, A cleared: the writer's mirror is
// 1 but the union keeps 2), and an older poller reading the mirror would see
// B's occurrence with a count it never reached. A mirror with no registered
// owner is genuine legacy evidence and is left to the union.
//
// loaded is the document's failed-key counts before this writer's union. A
// mirror there above all of its owners' counts holds a retry an older writer
// advanced after this writer read its state; recomputation keeps it (and
// moves a single owner's count up to it) instead of restoring retry budget.
func restoreSharedFailureMirrors(state *persistedPollState, incoming map[string]int, loaded map[string]int) {
	for k, c := range incoming {
		owners := state.LegacyMirrors.owners(mirrorKindFailure, k)
		if c > 0 && len(owners) == 0 {
			continue
		}
		best := 0
		loadedBest := 0
		for _, owner := range owners {
			if n := state.FailedKeysFull[owner]; n > best {
				best = n
			}
			if n := loaded[owner]; n > loadedBest {
				loadedBest = n
			}
		}
		// A current writer keeps the mirror equal to its largest owner count, so
		// a loaded mirror above every loaded owner count is an advance only an
		// older writer could have made (it knows no occurrence keys). It is a
		// retry the document already holds, not something this writer's
		// recomputation may roll back. A tombstone (c == 0) is this writer's
		// explicit resolution and never preserves it.
		if advance := loaded[k]; c > 0 && advance > loadedBest {
			if len(owners) == 1 && incoming[owners[0]] != 0 {
				// An unambiguous single owner: the advance is that occurrence's
				// own retry, so carry it onto the occurrence key as well.
				if state.FailedKeysFull[owners[0]] < advance {
					state.FailedKeysFull[owners[0]] = advance
				}
			}
			if advance > best {
				best = advance
			}
		}
		if best > 0 {
			state.FailedKeysFull[k] = best
		}
	}
}

// mergeLabelState merges this writer's computed label-state snapshot
// (incoming) with the freshly reloaded document (existing) so entries for
// issues this writer did not itself observe — most likely a concurrent
// writer's update recorded during a CAS retry window, or simply not
// re-derived on an uncontended attempt — survive instead of being dropped
// by a wholesale replace. incoming wins per issue IID it is present for.
//
// An IID present in incoming with an empty (but non-nil) label slice is a
// tombstone: this writer observed the issue lose its routable labels, or
// close, this cycle (see detectNewLabels and discoverAllEvents), and that
// deletion wins over whatever the reloaded document still carries for it.
// An IID absent from incoming entirely is left untouched by the merge —
// this writer has no opinion on it this cycle.
func mergeLabelState(existing, incoming LabelState) LabelState {
	merged := make(LabelState, len(existing)+len(incoming))
	for iid, labels := range existing {
		merged[iid] = labels
	}
	for iid, labels := range incoming {
		if len(labels) == 0 {
			delete(merged, iid)
			continue
		}
		merged[iid] = labels
	}
	return merged
}

// mergeLabelStateAgainst is mergeLabelState for a writer that knows the
// snapshot (base) its incoming entries were derived from. For each IID in
// incoming it applies only what this writer changed relative to base — the
// labels it added (incoming minus base) and removed (base minus incoming) —
// on top of the freshly reloaded entry in existing. A label a concurrent
// writer added or removed after this writer loaded base, and that this
// writer did not itself change, is therefore preserved; a stale unchanged
// snapshot is a no-op rather than a revert. An entry left with no labels is
// deleted.
func mergeLabelStateAgainst(existing, base, incoming LabelState) LabelState {
	merged := make(LabelState, len(existing)+len(incoming))
	for iid, labels := range existing {
		merged[iid] = labels
	}
	for iid, labels := range incoming {
		baseSet := toSet(base[iid])
		incomingSet := toSet(labels)
		var added []string
		for _, l := range labels {
			if !baseSet[l] {
				added = append(added, l)
			}
		}
		removed := make(map[string]bool)
		for l := range baseSet {
			if !incomingSet[l] {
				removed[l] = true
			}
		}
		if len(added) == 0 && len(removed) == 0 {
			continue
		}
		var result []string
		seen := make(map[string]bool)
		for _, l := range append(append([]string(nil), merged[iid]...), added...) {
			if removed[l] || seen[l] {
				continue
			}
			seen[l] = true
			result = append(result, l)
		}
		if len(result) == 0 {
			delete(merged, iid)
			continue
		}
		merged[iid] = result
	}
	return merged
}

// unionLabelState adds each IID's labels in adds to existing without
// removing anything, so a label handoff never drops labels a concurrent
// poll cycle recorded for the same issue.
func unionLabelState(existing, adds LabelState) LabelState {
	merged := make(LabelState, len(existing)+len(adds))
	for iid, labels := range existing {
		merged[iid] = labels
	}
	for iid, labels := range adds {
		have := toSet(merged[iid])
		out := append([]string(nil), merged[iid]...)
		for _, l := range labels {
			if !have[l] {
				have[l] = true
				out = append(out, l)
			}
		}
		merged[iid] = out
	}
	return merged
}

// removeLabelPresence deletes each IID's listed labels from existing,
// dropping an entry left with no labels. Nothing else is touched.
func removeLabelPresence(existing, remove LabelState) LabelState {
	merged := make(LabelState, len(existing))
	for iid, labels := range existing {
		merged[iid] = labels
	}
	for iid, labels := range remove {
		drop := toSet(labels)
		var kept []string
		for _, l := range merged[iid] {
			if !drop[l] {
				kept = append(kept, l)
			}
		}
		if len(kept) == 0 {
			delete(merged, iid)
			continue
		}
		merged[iid] = kept
	}
	return merged
}

// applyWatermark advances the stored watermark to the later of the newly
// observed timestamp and whatever is already on the (freshly loaded)
// document, so a CAS retry cannot roll the watermark backward if a
// concurrent writer already advanced it further.
func (p *Poller) applyWatermark(state *persistedPollState, t time.Time) {
	current := state.LastPollAtFull
	if p.slashCommandsOnly {
		current = state.LastPollAtFast
	}
	if current != "" {
		if parsed, err := time.Parse(time.RFC3339, current); err == nil && parsed.After(t) {
			t = parsed
		}
	}
	formatted := t.Format(time.RFC3339)
	if p.slashCommandsOnly {
		state.LastPollAtFast = formatted
	} else {
		state.LastPollAtFull = formatted
	}
}

// detectNewLabels compares current issue labels against stored state to find
// newly-added routable labels. It returns:
//   - newLabels: IID → labels that were added since the last poll
//   - delta: the label mutations this cycle observed (caller persists after
//     dispatch). It holds only the IIDs this poll discovered or pruned as
//     closed; entries for other open issues are omitted rather than echoed
//     back from the loaded snapshot, because mergeLabelState lets incoming
//     entries win per IID and an echoed stale entry would erase a label a
//     concurrent webhook recorded after this poll loaded state.
//   - previousLabels: snapshot of prior state per issue (for rollback)
//   - error
func (p *Poller) detectNewLabels(ctx context.Context, owner, repo string, issues []Issue) (map[int][]string, LabelState, map[int][]string, error) {
	ps, err := p.loadPollState(ctx, owner, repo)
	if err != nil {
		return nil, nil, nil, err
	}
	state := ps.LabelState
	delta := make(LabelState, len(issues))
	base := make(LabelState, len(issues))
	// Whatever this cycle's delta says about an issue is relative to this
	// snapshot; the persist step applies only the difference (see
	// mergeLabelStateAgainst).
	p.labelBase = base

	// Snapshot previous labels for each issue (used for rollback).
	previousLabels := make(map[int][]string, len(issues))
	for _, iss := range issues {
		if prev, ok := state[iss.IID]; ok {
			cp := make([]string, len(prev))
			copy(cp, prev)
			previousLabels[iss.IID] = cp
		}
	}

	// Detect newly-added routable labels per issue.
	newLabels := make(map[int][]string, len(issues))
	polledIIDs := make(map[int]bool, len(issues))
	for _, iss := range issues {
		polledIIDs[iss.IID] = true
		if prev, ok := state[iss.IID]; ok {
			base[iss.IID] = append([]string(nil), prev...)
		}

		currentRoutable := filterRoutableLabels(iss.Labels)
		prevSet := toSet(state[iss.IID])

		var added []string
		for _, lbl := range currentRoutable {
			if !prevSet[lbl] {
				added = append(added, lbl)
			}
		}
		if len(added) > 0 {
			newLabels[iss.IID] = added
		}

		if len(currentRoutable) > 0 {
			delta[iss.IID] = currentRoutable
		} else {
			// Tombstone (present, empty), not delete: this writer
			// observed the issue currently has no routable labels, and
			// mergeLabelState must apply that deletion even against a
			// freshly reloaded document that still carries an older
			// entry for this IID (see mergeLabelState).
			delta[iss.IID] = []string{}
		}
	}

	// Prune closed issues that were NOT in the current poll set.
	for iid := range state {
		if polledIIDs[iid] {
			continue
		}
		if p.isIssueClosed(ctx, owner, repo, iid) {
			// Tombstone rather than delete; see the comment above.
			base[iid] = append([]string(nil), state[iid]...)
			delta[iid] = []string{}
		}
	}

	return newLabels, delta, previousLabels, nil
}

// persistLabelState writes the label state to the poller state document.
func (p *Poller) persistLabelState(ctx context.Context, owner, repo string, state LabelState) error {
	return p.persistCycleState(ctx, owner, repo, nil, nil, nil, state)
}

// isIssueClosed checks whether the given issue is closed.
// Returns false on any error (including not-found).
func (p *Poller) isIssueClosed(ctx context.Context, owner, repo string, iid int) bool {
	iss, err := p.client.GetIssue(ctx, owner, repo, iid)
	if err != nil {
		return false
	}
	return iss.State == "closed"
}

// readDispatchedKeys reads the map of recently-dispatched event keys
// (key → unix timestamp) from poller state. Returns an empty map on
// first run. Returns error on transient failures to prevent clobbering
// history.
func (p *Poller) readDispatchedKeys(ctx context.Context, owner, repo string) (map[string]int64, error) {
	state, err := p.loadPollState(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("read dispatched keys: %w", err)
	}
	keys := state.DispatchedKeysFull
	if p.slashCommandsOnly {
		keys = state.DispatchedKeysFast
	}
	if keys == nil {
		return make(map[string]int64), nil
	}
	return keys, nil
}

// persistDispatchedKeys writes the dispatched keys map, pruning entries
// older than the given watermark. The stored watermark is not updated.
func (p *Poller) persistDispatchedKeys(ctx context.Context, owner, repo string, keys map[string]int64, watermark time.Time) error {
	return p.persistWithCAS(ctx, owner, repo, persistDeltas{
		dispatched: keys,
		pruneCut:   watermark,
	})
}

// readFailedKeys reads the map of event keys to failure counts.
func (p *Poller) readFailedKeys(ctx context.Context, owner, repo string) (map[string]int, error) {
	state, err := p.loadPollState(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("read failed keys: %w", err)
	}
	keys := state.FailedKeysFull
	if p.slashCommandsOnly {
		keys = state.FailedKeysFast
	}
	if keys == nil {
		return make(map[string]int), nil
	}
	return keys, nil
}

// readPendingLabels reads the failed webhook label occurrences awaiting a
// poller retry. Returns an empty map when there are none.
func (p *Poller) readPendingLabels(ctx context.Context, owner, repo string) (map[string]PendingLabel, error) {
	state, err := p.loadPollState(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("read pending labels: %w", err)
	}
	pending := make(map[string]PendingLabel, len(state.PendingLabels))
	for k, v := range state.PendingLabels {
		pending[k] = v
	}
	return pending, nil
}

// readDedupState reads the dispatched keys, failure counts, and legacy
// mirror ledger from one poll-state snapshot. Deduplication depends on the
// three corresponding (a mirror key is only genuine legacy evidence while
// its ownership ledger entry is visible alongside it), so they must not be
// loaded through separate branch-tip reads that another cycle's prune or
// clear can interleave. The returned maps and set are private to the caller
// where it extends them: the mirror set is a clone.
func (p *Poller) readDedupState(ctx context.Context, owner, repo string) (map[string]int64, map[string]int, mirrorSet, error) {
	state, err := p.loadPollState(ctx, owner, repo)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read dedup state: %w", err)
	}
	dispatched := state.DispatchedKeysFull
	failed := state.FailedKeysFull
	if p.slashCommandsOnly {
		dispatched = state.DispatchedKeysFast
		failed = state.FailedKeysFast
	}
	if dispatched == nil {
		dispatched = make(map[string]int64)
	}
	if failed == nil {
		failed = make(map[string]int)
	}
	return dispatched, failed, state.LegacyMirrors.clone(), nil
}

// persistFailedKeys writes the failed event retry counts, pruning
// entries that have exceeded the retry budget.
func (p *Poller) persistFailedKeys(ctx context.Context, owner, repo string, keys map[string]int) error {
	return p.persistCycleState(ctx, owner, repo, nil, nil, keys, nil)
}

// toSet converts a string slice to a set for O(1) lookups.
func toSet(labels []string) map[string]bool {
	s := make(map[string]bool, len(labels))
	for _, l := range labels {
		s[l] = true
	}
	return s
}

// buildLegacyPollStateFromVars assembles a persistedPollState from a
// name-to-value map of pre-branch-store poller CI/CD variables.
// Returns found=false if none of the seven legacy state vars are present.
func buildLegacyPollStateFromVars(vars map[string]string, owner, repo string) (persistedPollState, bool) {
	var state persistedPollState
	found := false

	if v, ok := vars[forge.VarLastPollAtFast]; ok {
		state.LastPollAtFast = v
		found = true
	}
	if v, ok := vars[forge.VarLastPollAtFull]; ok {
		state.LastPollAtFull = v
		found = true
	}
	if v, ok := vars[forge.VarLabelState]; ok {
		found = true
		var ls LabelState
		if err := json.Unmarshal([]byte(v), &ls); err != nil {
			log.Printf("WARNING: failed to unmarshal legacy label state for %s/%s, skipping: %v", owner, repo, err)
		} else {
			state.LabelState = ls
		}
	}
	if v, ok := vars[forge.VarDispatchedKeysFast]; ok {
		found = true
		var m map[string]int64
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			log.Printf("WARNING: failed to unmarshal legacy dispatched keys (fast) for %s/%s, skipping: %v", owner, repo, err)
		} else {
			state.DispatchedKeysFast = m
		}
	}
	if v, ok := vars[forge.VarDispatchedKeysFull]; ok {
		found = true
		var m map[string]int64
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			log.Printf("WARNING: failed to unmarshal legacy dispatched keys (full) for %s/%s, skipping: %v", owner, repo, err)
		} else {
			state.DispatchedKeysFull = m
		}
	}
	if v, ok := vars[forge.VarFailedKeysFast]; ok {
		found = true
		var m map[string]int
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			log.Printf("WARNING: failed to unmarshal legacy failed keys (fast) for %s/%s, skipping: %v", owner, repo, err)
		} else {
			state.FailedKeysFast = m
		}
	}
	if v, ok := vars[forge.VarFailedKeysFull]; ok {
		found = true
		var m map[string]int
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			log.Printf("WARNING: failed to unmarshal legacy failed keys (full) for %s/%s, skipping: %v", owner, repo, err)
		} else {
			state.FailedKeysFull = m
		}
	}
	return state, found
}

// EnsureDispatchSecret returns the project's existing
// FULLSEND_DISPATCH_SECRET CI/CD variable value, or generates and
// stores a new one (masked, protected, like the bot PAT) if none is
// set. created is true when a new secret was written. Without this,
// poll-state HMAC signing requires a manual operator step and the
// poller fails closed.
func EnsureDispatchSecret(ctx context.Context, client forge.Client, owner, repo string) (secret string, created bool, err error) {
	vars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		return "", false, fmt.Errorf("listing repo variables: %w", err)
	}
	if existing, ok := vars[forge.SecretDispatch]; ok && existing != "" {
		return existing, false, nil
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, fmt.Errorf("generating dispatch secret: %w", err)
	}
	secret = hex.EncodeToString(buf)
	if err := client.CreateRepoSecret(ctx, owner, repo, forge.SecretDispatch, secret); err != nil {
		return "", false, fmt.Errorf("storing dispatch secret: %w", err)
	}
	return secret, true, nil
}

// SeedGitLabPollStateBranches creates both poll-state branches with an
// HMAC-signed state.json, using the operator's Maintainer-capable
// client so legacy CI/CD variables can still be read. *Fast fields are
// written to the slash branch; *Full fields plus LabelState go to the
// events branch.
//
// A missing branch is seeded from any present legacy variables, or with
// an empty signed baseline when none are present. An already-present
// state.json is left untouched so live poller writes are not clobbered.
// dispatchSecret must be non-empty: unsigned documents would be
// discarded as tampered by the runtime poller.
//
// Returns true if at least one branch was written.
func SeedGitLabPollStateBranches(ctx context.Context, client forge.Client, owner, repo, dispatchSecret string) (bool, error) {
	if dispatchSecret == "" {
		return false, errDispatchSecretUnset
	}

	vars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		return false, fmt.Errorf("listing repo variables: %w", err)
	}
	legacy, _ := buildLegacyPollStateFromVars(vars, owner, repo)
	projectPath := owner + "/" + repo

	branches := []struct {
		name  string
		slash bool
	}{
		{PollStateBranchSlash, true},
		{PollStateBranchEvents, false},
	}

	seeded := false
	for _, b := range branches {
		_, err := client.GetFileContentAtRef(ctx, owner, repo, PollStateFileName, b.name)
		if err == nil {
			continue
		}
		if !errors.Is(err, forge.ErrNotFound) {
			return seeded, fmt.Errorf("checking poll state on %s: %w", b.name, err)
		}

		doc := pollStateForMode(b.slash, legacy)
		sig, err := computeStateHMAC(dispatchSecret, hmacDomainFor(b.name, projectPath), doc)
		if err != nil {
			return seeded, fmt.Errorf("computing poll state HMAC on %s: %w", b.name, err)
		}
		doc.HMAC = sig
		data, err := json.Marshal(doc)
		if err != nil {
			return seeded, fmt.Errorf("marshaling poll state on %s: %w", b.name, err)
		}
		if err := client.ForceCommitFileToBranch(ctx, owner, repo, b.name, PollStateFileName, "fullsend: seed poll state", data); err != nil {
			return seeded, fmt.Errorf("seeding poll state on %s: %w", b.name, err)
		}
		seeded = true
	}
	return seeded, nil
}
