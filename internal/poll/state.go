package poll

import (
	"context"
	"encoding/json"
	"errors"
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

		// Update state with current routable labels.
		state[iss.IID] = currentRoutable
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

// toSet converts a string slice to a set for O(1) lookups.
func toSet(labels []string) map[string]bool {
	s := make(map[string]bool, len(labels))
	for _, l := range labels {
		s[l] = true
	}
	return s
}
