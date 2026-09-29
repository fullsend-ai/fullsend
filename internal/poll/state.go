package poll

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	want, err := computeStateHMAC(p.opts.DispatchSecret, p.hmacDomain(), state)
	if err != nil {
		return persistedPollState{}, err
	}
	if state.HMAC == "" || !hmac.Equal([]byte(state.HMAC), []byte(want)) {
		if discardErr := p.discardPollState(ctx, owner, repo, branch); discardErr != nil {
			return persistedPollState{}, fmt.Errorf("%w on %s; also failed to discard branch: %v", errPollStateTampered, branch, discardErr)
		}
		return persistedPollState{}, fmt.Errorf("%w on %s; discarded branch", errPollStateTampered, branch)
	}
	return p.modeDocument(state), nil
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
}

func (p *Poller) commitPollState(ctx context.Context, owner, repo string, state persistedPollState, expectedSHA string) error {
	if p.opts.DispatchSecret == "" {
		return errDispatchSecretUnset
	}
	state = p.modeDocument(state)
	sig, err := computeStateHMAC(p.opts.DispatchSecret, p.hmacDomain(), state)
	if err != nil {
		return err
	}
	state.HMAC = sig
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
	if deltas.dispatched != nil {
		unionDispatchedKeys(&state, p.slashCommandsOnly, deltas.dispatched, deltas.pruneCut)
	}
	if deltas.failed != nil {
		unionFailedKeys(&state, p.slashCommandsOnly, deltas.failed)
	}
	if deltas.watermark != nil {
		p.applyWatermark(&state, *deltas.watermark)
	}
	if deltas.labels != nil {
		state.LabelState = mergeLabelState(state.LabelState, deltas.labels)
	}
	return state
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
	if watermark != nil {
		deltas.pruneCut = *watermark
	}
	return p.persistWithCAS(ctx, owner, repo, deltas)
}

func (p *Poller) persistWithCAS(ctx context.Context, owner, repo string, deltas persistDeltas) error {
	if p.opts.DispatchSecret == "" {
		return errDispatchSecretUnset
	}
	branch := p.stateBranch()
	var lastErr error
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
		state = p.applyPersistDeltas(state, deltas)
		err = p.commitPollState(ctx, owner, repo, state, expectedSHA)
		if err == nil {
			return nil
		}
		if !forge.IsNonFastForward(err) {
			return err
		}
		lastErr = err
		log.Printf("poll state CAS conflict on %s (attempt %d/%d): %v", branch, attempt, maxPollStateCASAttempts, err)
	}
	return fmt.Errorf("%w after %d attempts: %w", errPollStateCASExhausted, maxPollStateCASAttempts, lastErr)
}

func pruneDispatchedKeys(keys map[string]int64, watermark time.Time) map[string]int64 {
	cutoff := watermark.Unix()
	pruned := make(map[string]int64, len(keys))
	for k, ts := range keys {
		if ts >= cutoff {
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
func unionDispatchedKeys(state *persistedPollState, slash bool, keys map[string]int64, watermark time.Time) {
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
	pruned := pruneDispatchedKeys(merged, watermark)
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
//   - updatedState: the new label state (caller persists after dispatch)
//   - previousLabels: snapshot of prior state per issue (for rollback)
//   - error
func (p *Poller) detectNewLabels(ctx context.Context, owner, repo string, issues []Issue) (map[int][]string, LabelState, map[int][]string, error) {
	ps, err := p.loadPollState(ctx, owner, repo)
	if err != nil {
		return nil, nil, nil, err
	}
	state := ps.LabelState
	if state == nil {
		state = LabelState{}
	}

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
			state[iss.IID] = currentRoutable
		} else {
			// Tombstone (present, empty), not delete: this writer
			// observed the issue currently has no routable labels, and
			// mergeLabelState must apply that deletion even against a
			// freshly reloaded document that still carries an older
			// entry for this IID (see mergeLabelState).
			state[iss.IID] = []string{}
		}
	}

	// Prune closed issues that were NOT in the current poll set.
	for iid := range state {
		if polledIIDs[iid] {
			continue
		}
		if p.isIssueClosed(ctx, owner, repo, iid) {
			// Tombstone rather than delete; see the comment above.
			state[iid] = []string{}
		}
	}

	return newLabels, state, previousLabels, nil
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
