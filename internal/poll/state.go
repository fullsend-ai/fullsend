package poll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// readWatermark reads the last-polled timestamp from a CI variable.
// On first run (variable not found), it defaults to one hour ago.
func (p *Poller) readWatermark(ctx context.Context, owner, repo string) (time.Time, error) {
	val, err := p.client.GetCIVariable(ctx, owner, repo, p.watermarkVarName())
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return time.Now().Add(-1 * time.Hour), nil
		}
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339, val)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// watermarkVarName returns the CI variable name used for the poll watermark.
// Slash-command-only mode uses a faster polling cadence with its own variable.
func (p *Poller) watermarkVarName() string {
	if p.opts.SlashCommandsOnly {
		return "FULLSEND_LAST_POLL_AT_FAST"
	}
	return "FULLSEND_LAST_POLL_AT_FULL"
}

// updateWatermark persists the given timestamp as the poll watermark.
func (p *Poller) updateWatermark(ctx context.Context, owner, repo string, t time.Time) error {
	return p.client.UpdateCIVariable(ctx, owner, repo, p.watermarkVarName(), t.Format(time.RFC3339), true)
}

// detectNewLabels compares current issue labels against stored state to find
// newly-added routable labels. It returns:
//   - newLabels: IID → labels that were added since the last poll
//   - updatedState: the new label state (caller persists after dispatch)
//   - previousLabels: snapshot of prior state per issue (for rollback)
//   - error
func (p *Poller) detectNewLabels(ctx context.Context, owner, repo string, issues []Issue) (map[int][]string, LabelState, map[int][]string, error) {
	// Read persisted label state from CI variable.
	raw, err := p.client.GetCIVariable(ctx, owner, repo, "FULLSEND_LABEL_STATE")
	if err != nil && !errors.Is(err, forge.ErrNotFound) {
		return nil, nil, nil, err
	}
	if errors.Is(err, forge.ErrNotFound) {
		raw = "{}"
	}

	var state LabelState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		log.Printf("warning: failed to unmarshal label state, starting fresh: %v", err)
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

// persistLabelState writes the label state to a CI variable.
func (p *Poller) persistLabelState(ctx context.Context, owner, repo string, state LabelState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return p.client.UpdateCIVariable(ctx, owner, repo, "FULLSEND_LABEL_STATE", string(data), true)
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

// dispatchedKeysVarName returns the per-mode CI variable name for dispatched keys.
func (p *Poller) dispatchedKeysVarName() string {
	if p.opts.SlashCommandsOnly {
		return "FULLSEND_DISPATCHED_KEYS_FAST"
	}
	return "FULLSEND_DISPATCHED_KEYS_FULL"
}

// readDispatchedKeys reads the map of recently-dispatched event keys
// (key → unix timestamp) from a CI variable. Returns an empty map on
// first run (ErrNotFound). Returns error on transient failures to
// prevent clobbering history.
func (p *Poller) readDispatchedKeys(ctx context.Context, owner, repo string) (map[string]int64, error) {
	raw, err := p.client.GetCIVariable(ctx, owner, repo, p.dispatchedKeysVarName())
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return make(map[string]int64), nil
		}
		return nil, fmt.Errorf("read dispatched keys: %w", err)
	}
	var keys map[string]int64
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		log.Printf("warning: failed to unmarshal dispatched keys, starting fresh: %v", err)
		return make(map[string]int64), nil
	}
	return keys, nil
}

// persistDispatchedKeys writes the dispatched keys map, pruning entries
// older than the given watermark to stay within CI variable size limits.
func (p *Poller) persistDispatchedKeys(ctx context.Context, owner, repo string, keys map[string]int64, watermark time.Time) error {
	cutoff := watermark.Unix()
	pruned := make(map[string]int64, len(keys))
	for k, ts := range keys {
		if ts >= cutoff {
			pruned[k] = ts
		}
	}
	data, err := json.Marshal(pruned)
	if err != nil {
		return err
	}
	return p.client.UpdateCIVariable(ctx, owner, repo, p.dispatchedKeysVarName(), string(data), true)
}

// failedKeysVarName returns the CI variable name for failed event retry counts.
func (p *Poller) failedKeysVarName() string {
	if p.opts.SlashCommandsOnly {
		return "FULLSEND_FAILED_KEYS_FAST"
	}
	return "FULLSEND_FAILED_KEYS_FULL"
}

// readFailedKeys reads the map of event keys to failure counts.
func (p *Poller) readFailedKeys(ctx context.Context, owner, repo string) (map[string]int, error) {
	raw, err := p.client.GetCIVariable(ctx, owner, repo, p.failedKeysVarName())
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return make(map[string]int), nil
		}
		return nil, fmt.Errorf("read failed keys: %w", err)
	}
	var keys map[string]int
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		log.Printf("warning: failed to unmarshal failed keys, starting fresh: %v", err)
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
	data, err := json.Marshal(pruned)
	if err != nil {
		return err
	}
	return p.client.UpdateCIVariable(ctx, owner, repo, p.failedKeysVarName(), string(data), true)
}

// toSet converts a string slice to a set for O(1) lookups.
func toSet(labels []string) map[string]bool {
	s := make(map[string]bool, len(labels))
	for _, l := range labels {
		s[l] = true
	}
	return s
}
