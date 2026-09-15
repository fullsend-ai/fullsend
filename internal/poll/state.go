package poll

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Poller state is stored as a single JSON document in the GitLab Generic
// Package Registry so the bot PAT can run at Developer access (30)
// instead of Maintainer (40). CI/CD variable writes require Maintainer;
// package publish does not. See #7313 and ADR 0067.
const (
	pollStatePackageName    = "fullsend-poll-state"
	pollStatePackageVersion = "1.0"
	pollStateFileName       = "state.json"

	// PollStatePackageName is exported so callers outside this package
	// (e.g. GitLab uninstall) can clean up the poller-state package
	// without duplicating the literal name.
	PollStatePackageName = pollStatePackageName
)

// persistedPollState is the JSON document stored at
// fullsend-poll-state/1.0/state.json. Slash and events polls share the
// file; each mode updates only its own fields. Dual-schedule jobs use
// separate resource groups so they can overlap — last write wins, which
// is accepted because the poller already delivers at-least-once.
type persistedPollState struct {
	LastPollAtFast     string           `json:"last_poll_at_fast,omitempty"`
	LastPollAtFull     string           `json:"last_poll_at_full,omitempty"`
	LabelState         LabelState       `json:"label_state,omitempty"`
	DispatchedKeysFast map[string]int64 `json:"dispatched_keys_fast,omitempty"`
	DispatchedKeysFull map[string]int64 `json:"dispatched_keys_full,omitempty"`
	FailedKeysFast     map[string]int   `json:"failed_keys_fast,omitempty"`
	FailedKeysFull     map[string]int   `json:"failed_keys_full,omitempty"`

	// HMAC is an HMAC-SHA256 signature (hex-encoded) over the rest of
	// this document, computed with the HMAC field cleared. It is set
	// by savePollState when a DispatchSecret is configured and
	// verified by loadPollState so that a Developer-level (or
	// unprotected-branch CI_JOB_TOKEN) actor able to write this
	// package file cannot forge watermarks or dispatch/failed-key
	// state without knowing the shared secret. See the
	// permission-expansion finding on PR #7317.
	HMAC string `json:"hmac,omitempty"`
}

// computeStateHMAC computes an HMAC-SHA256 (hex-encoded) over the
// canonical JSON encoding of state with its own HMAC field cleared.
// It reuses the same shared secret as dispatch-variable signing
// (FULLSEND_DISPATCH_SECRET) rather than introducing a second secret.
func computeStateHMAC(secret string, state persistedPollState) (string, error) {
	state.HMAC = ""
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (p *Poller) loadPollState(ctx context.Context, owner, repo string) (persistedPollState, error) {
	// Fail closed when no secret is configured, before touching the
	// registry or the legacy-variable migration path. Poll state lives
	// in a Developer-writable package file, so an unsigned document
	// cannot be trusted; equally important, this is the first state read
	// of the poll cycle, so aborting here stops the cycle *before* event
	// discovery and dispatch. Otherwise an unset secret would let the
	// cycle discover events, dispatch them (an irreversible side effect),
	// then fail to persist — and, since state never advances, re-dispatch
	// the same events on every subsequent cycle. FULLSEND_DISPATCH_SECRET
	// is auto-provisioned by repos install/converge, so a missing secret
	// here means a broken/incomplete install to repair rather than
	// forgeable state to silently trust.
	if p.opts.DispatchSecret == "" {
		return persistedPollState{}, errors.New("FULLSEND_DISPATCH_SECRET is not set: refusing to load unsigned poll state (re-run repos install/converge to provision it)")
	}
	data, err := p.client.DownloadPackageFile(ctx, owner, repo, pollStatePackageName, pollStatePackageVersion, pollStateFileName)
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return p.migrateLegacyPollState(ctx, owner, repo)
		}
		return persistedPollState{}, err
	}
	var state persistedPollState
	if err := json.Unmarshal(data, &state); err != nil {
		// Fail closed: a corrupted document must not be treated as "no
		// prior state," since that would silently reset watermarks,
		// label state, and both modes' dispatched/failed keys together.
		// Let the caller abort the poll cycle instead of persisting a
		// reset document over the (still-recoverable) corrupt one.
		return persistedPollState{}, fmt.Errorf("unmarshal poll state: %w", err)
	}
	want, err := computeStateHMAC(p.opts.DispatchSecret, state)
	if err != nil {
		return persistedPollState{}, err
	}
	if state.HMAC == "" || !hmac.Equal([]byte(state.HMAC), []byte(want)) {
		// Fail closed: a Developer-level token (or an unprotected-branch
		// CI_JOB_TOKEN) can write this package file directly. Without
		// signature verification it could forge watermarks/dispatch
		// state undetected.
		return persistedPollState{}, fmt.Errorf("poll state signature missing or invalid (tampered, or written without FULLSEND_DISPATCH_SECRET)")
	}
	return state, nil
}

// legacyPollStateVarNames lists the pre-#7313 poller CI/CD variable
// names that were superseded by the package-registry state document.
// Mirrors internal/repos.gitlabLegacyPollerVars, which uninstall uses
// to delete these same names.
var legacyPollStateVarNames = []string{
	forge.VarLastPollAtFast, forge.VarLastPollAtFull, forge.VarLabelState,
	forge.VarDispatchedKeysFast, forge.VarDispatchedKeysFull,
	forge.VarFailedKeysFast, forge.VarFailedKeysFull,
}

// buildLegacyPollStateFromVars assembles a persistedPollState from a
// name-to-value map of pre-#7313 poller CI/CD variables (as returned by
// either a per-name lookup or forge.Client.ListRepoVariables). Returns
// found=false if none of legacyPollStateVarNames are present in vars —
// a genuinely new install has nothing to migrate.
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
		var ls LabelState
		if err := json.Unmarshal([]byte(v), &ls); err != nil {
			log.Printf("warning: failed to unmarshal legacy label state for %s/%s, skipping: %v", owner, repo, err)
		} else {
			state.LabelState = ls
			found = true
		}
	}
	if v, ok := vars[forge.VarDispatchedKeysFast]; ok {
		var m map[string]int64
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			log.Printf("warning: failed to unmarshal legacy dispatched keys (fast) for %s/%s, skipping: %v", owner, repo, err)
		} else {
			state.DispatchedKeysFast = m
			found = true
		}
	}
	if v, ok := vars[forge.VarDispatchedKeysFull]; ok {
		var m map[string]int64
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			log.Printf("warning: failed to unmarshal legacy dispatched keys (full) for %s/%s, skipping: %v", owner, repo, err)
		} else {
			state.DispatchedKeysFull = m
			found = true
		}
	}
	if v, ok := vars[forge.VarFailedKeysFast]; ok {
		var m map[string]int
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			log.Printf("warning: failed to unmarshal legacy failed keys (fast) for %s/%s, skipping: %v", owner, repo, err)
		} else {
			state.FailedKeysFast = m
			found = true
		}
	}
	if v, ok := vars[forge.VarFailedKeysFull]; ok {
		var m map[string]int
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			log.Printf("warning: failed to unmarshal legacy failed keys (full) for %s/%s, skipping: %v", owner, repo, err)
		} else {
			state.FailedKeysFull = m
			found = true
		}
	}

	return state, found
}

// migrateLegacyPollState reads pre-#7313 poller CI/CD variables (if any
// are still present) and seeds the initial package-registry state
// document from them. Without this, a repo that was installed before
// #7313 would silently lose its watermarks, label state, and dispatch
// history the first time it polls with the new binary: a missing
// package file (404) would otherwise be treated as "first run" and
// reset everything, causing detectNewLabels to re-dispatch every
// currently-set routable label.
//
// Returns an empty state (no error) when no legacy variables are
// present — a genuinely new install has nothing to migrate. In
// practice this path rarely finds anything: a Developer-level bot PAT
// (the access level this PR creates) gets 403 Forbidden from GitLab's
// project-level CI/CD Variables API for every operation, including
// reads of variables that don't exist, so it can't tell "absent" from
// "exists but unreadable" and both resolve to "nothing to migrate"
// here. SeedGitLabPollStateFromLegacyVars performs the real migration
// during install/converge, while a Maintainer-or-higher client can
// still read these variables — see its doc comment and the
// logic-error finding on PR #7317.
func (p *Poller) migrateLegacyPollState(ctx context.Context, owner, repo string) (persistedPollState, error) {
	vars := make(map[string]string)
	for _, name := range legacyPollStateVarNames {
		val, err := p.client.GetCIVariable(ctx, owner, repo, name)
		if err != nil {
			if errors.Is(err, forge.ErrNotFound) || errors.Is(err, forge.ErrForbidden) {
				// See the doc comment above: a Developer-level token
				// can never distinguish these, so both mean "nothing
				// to migrate" rather than aborting the poll cycle.
				continue
			}
			return persistedPollState{}, fmt.Errorf("read legacy variable %s: %w", name, err)
		}
		vars[name] = val
	}

	state, found := buildLegacyPollStateFromVars(vars, owner, repo)
	if !found {
		return persistedPollState{}, nil
	}

	log.Printf("migrating legacy CI/CD poller state to package registry for %s/%s", owner, repo)
	if err := p.savePollState(ctx, owner, repo, state); err != nil {
		return persistedPollState{}, fmt.Errorf("persist migrated poll state: %w", err)
	}
	return state, nil
}

// SeedGitLabPollStateFromLegacyVars reads pre-#7313 poller CI/CD
// variables via client — expected to be a Maintainer-or-higher-level
// client, such as the one used during GitLab install/converge, before
// the bot PAT is created/rotated to Developer access — and, if any are
// present, seeds the initial fullsend-poll-state/1.0/state.json package
// document from them, signed with dispatchSecret (when non-empty) the
// same way the runtime poller signs it.
//
// The Developer-level bot PAT this PR creates cannot read legacy CI/CD
// variables at all (GitLab's project Variables API requires Maintainer
// for every operation, including reads), so a pre-#7313 repo that
// converges to a Developer-level bot PAT without this being called
// first starts its next poll with empty state: reset watermarks and
// detectNewLabels treating every currently-set routable label as newly
// added. See the logic-error finding on PR #7317.
//
// It is a no-op (returns false, nil) if a poll state package already
// exists (avoids clobbering live poller state written since install)
// or if none of legacyPollStateVarNames are present (a genuinely new
// install has no legacy history to migrate).
func SeedGitLabPollStateFromLegacyVars(ctx context.Context, client forge.Client, owner, repo, dispatchSecret string) (bool, error) {
	if _, err := client.DownloadPackageFile(ctx, owner, repo, pollStatePackageName, pollStatePackageVersion, pollStateFileName); err == nil {
		return false, nil
	} else if !errors.Is(err, forge.ErrNotFound) {
		return false, fmt.Errorf("checking for existing poll state: %w", err)
	}

	vars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		return false, fmt.Errorf("listing repo variables: %w", err)
	}
	state, found := buildLegacyPollStateFromVars(vars, owner, repo)
	if !found {
		return false, nil
	}

	if dispatchSecret != "" {
		sig, err := computeStateHMAC(dispatchSecret, state)
		if err != nil {
			return false, err
		}
		state.HMAC = sig
	}
	data, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	if err := client.UploadPackageFile(ctx, owner, repo, pollStatePackageName, pollStatePackageVersion, pollStateFileName, data); err != nil {
		return false, fmt.Errorf("seeding poll state: %w", err)
	}
	return true, nil
}

// DiscardUnsignedGitLabPollState deletes an existing but *unsigned* poll
// state package document so it cannot be silently trusted. It is a no-op
// (returns false, nil) when dispatchSecret is empty (signing is not yet
// configured, so there is nothing to enforce), when no poll state
// package exists yet, when the existing document already carries a
// signature, or when the document is corrupt/unparseable (left for the
// runtime fail-closed path to surface).
//
// This is the upgrade bridge for repos whose state.json was written by a
// pre-#7317 (unsigned) poller. It deliberately does NOT re-sign the
// document in place. The whole point of HMAC signing is that a
// Developer-level `api`-scoped token — or, depending on job-token
// settings, an unprotected-branch CI_JOB_TOKEN — can write this package
// file directly. An unsigned document therefore has no trustworthy
// provenance: re-signing it would launder attacker-writable (or merely
// stale) bytes into a document the runtime poller then trusts as
// authentic. Instead we discard it; the trustworthy migration source is
// the Maintainer-only legacy CI/CD variables read by
// SeedGitLabPollStateFromLegacyVars, which callers run immediately after
// this (see provisionGitLabDispatchSecret). A repo with neither legacy
// variables nor a signed document simply starts its next poll fresh —
// a one-time, at-least-once re-dispatch that downstream idempotency
// tolerates — which is strictly preferable to trusting unauthenticated
// state. A document with a *non-empty but invalid* signature is left in
// place: the runtime fail-closed check rejects it, so an attacker gains
// nothing by writing a bogus HMAC.
func DiscardUnsignedGitLabPollState(ctx context.Context, client forge.Client, owner, repo, dispatchSecret string) (bool, error) {
	if dispatchSecret == "" {
		return false, nil
	}
	data, err := client.DownloadPackageFile(ctx, owner, repo, pollStatePackageName, pollStatePackageVersion, pollStateFileName)
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("checking poll state signature: %w", err)
	}
	var state persistedPollState
	if err := json.Unmarshal(data, &state); err != nil {
		// Leave corrupt documents for the runtime fail-closed path.
		return false, nil
	}
	if state.HMAC != "" {
		return false, nil
	}
	if err := client.DeletePackage(ctx, owner, repo, pollStatePackageName); err != nil {
		return false, fmt.Errorf("discarding unsigned poll state: %w", err)
	}
	return true, nil
}

func (p *Poller) savePollState(ctx context.Context, owner, repo string, state persistedPollState) error {
	// Fail closed: never persist an unsigned document to a
	// Developer-writable package file. See loadPollState.
	if p.opts.DispatchSecret == "" {
		return errors.New("FULLSEND_DISPATCH_SECRET is not set: refusing to write unsigned poll state (re-run repos install/converge to provision it)")
	}
	sig, err := computeStateHMAC(p.opts.DispatchSecret, state)
	if err != nil {
		return err
	}
	state.HMAC = sig
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return p.client.UploadPackageFile(ctx, owner, repo, pollStatePackageName, pollStatePackageVersion, pollStateFileName, data)
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
	state, err := p.loadPollState(ctx, owner, repo)
	if err != nil {
		return err
	}
	formatted := t.Format(time.RFC3339)
	if p.slashCommandsOnly {
		state.LastPollAtFast = formatted
	} else {
		state.LastPollAtFull = formatted
	}
	return p.savePollState(ctx, owner, repo, state)
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
			delete(state, iss.IID)
		}
	}

	// Prune closed issues that were NOT in the current poll set.
	for iid := range state {
		if polledIIDs[iid] {
			continue
		}
		if p.isIssueClosed(ctx, owner, repo, iid) {
			delete(state, iid)
		}
	}

	return newLabels, state, previousLabels, nil
}

// persistLabelState writes the label state to the poller state document.
func (p *Poller) persistLabelState(ctx context.Context, owner, repo string, state LabelState) error {
	ps, err := p.loadPollState(ctx, owner, repo)
	if err != nil {
		return err
	}
	ps.LabelState = state
	return p.savePollState(ctx, owner, repo, ps)
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
// older than the given watermark.
func (p *Poller) persistDispatchedKeys(ctx context.Context, owner, repo string, keys map[string]int64, watermark time.Time) error {
	cutoff := watermark.Unix()
	pruned := make(map[string]int64, len(keys))
	for k, ts := range keys {
		if ts >= cutoff {
			pruned[k] = ts
		}
	}
	state, err := p.loadPollState(ctx, owner, repo)
	if err != nil {
		return err
	}
	if p.slashCommandsOnly {
		state.DispatchedKeysFast = pruned
	} else {
		state.DispatchedKeysFull = pruned
	}
	return p.savePollState(ctx, owner, repo, state)
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
	pruned := make(map[string]int, len(keys))
	for k, count := range keys {
		if count > 0 && count <= maxEventRetries {
			pruned[k] = count
		}
	}
	state, err := p.loadPollState(ctx, owner, repo)
	if err != nil {
		return err
	}
	if p.slashCommandsOnly {
		state.FailedKeysFast = pruned
	} else {
		state.FailedKeysFull = pruned
	}
	return p.savePollState(ctx, owner, repo, state)
}

// toSet converts a string slice to a set for O(1) lookups.
func toSet(labels []string) map[string]bool {
	s := make(map[string]bool, len(labels))
	for _, l := range labels {
		s[l] = true
	}
	return s
}
