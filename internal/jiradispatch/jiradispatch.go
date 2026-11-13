// Package jiradispatch holds the Jira dispatch core shared by the
// production jira-poll driver (internal/cli) and the behaviour
// scenarios (pkg/behaviourtest): the CEL trigger matcher that turns a
// normalized event into jirapoll dispatch records, and the GitLab record
// dispatcher that recovers the embedded normalized event and starts the
// agent pipeline. Both callers carried their own copy of each before
// this package, so the scenarios exercised a mirror of the production
// path instead of the path itself.
package jiradispatch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// Matcher evaluates a normalized event against harness CEL triggers and
// returns one dispatch record per matching harness. Candidates are
// loaded once at construction and reused for every event in the cycle;
// Auth is the dispatch gate policy, derived by the production caller
// from the configured jira authorization provider and left at the zero
// value by the behaviour scenarios.
type Matcher struct {
	Candidates []harnessdispatch.TriggeredHarness
	Auth       harnessdispatch.AuthOptions
}

// Match evaluates the event against all CEL trigger candidates and
// returns dispatch records for each matching harness: events the gate
// policy refuses are dropped silently, and each matching harness is
// projected into a jirapoll.DispatchRecord carrying its execution ref
// and the normalized event as the record's event payload.
func (m *Matcher) Match(_ context.Context, event *normevent.Event) ([]jirapoll.DispatchRecord, error) {
	if !harnessdispatch.IsEventAuthorizedWith(event, m.Auth) {
		return nil, nil
	}
	candidates := harnessdispatch.FilterAuthorizedHarnessesWith(m.Candidates, event, m.Auth)
	matched, err := harnessdispatch.MatchHarnesses(candidates, event)
	if err != nil {
		return nil, err
	}
	var records []jirapoll.DispatchRecord
	for _, h := range matched {
		ref, err := harnessdispatch.ProjectExecutionRef(h.Name, h.Harness.Role, event)
		if err != nil {
			return nil, fmt.Errorf("build execution ref for %s: %w", h.Name, err)
		}
		records = append(records, jirapoll.DispatchRecord{
			Agent:         ref.Agent,
			Role:          ref.Role,
			SourceRepo:    ref.SourceRepo,
			EventType:     ref.EventType,
			EventPayload:  ref.EventPayload,
			TriggerSource: ref.TriggerSource,
			StatusRepo:    ref.StatusRepo,
			StatusNumber:  ref.StatusNumber,
		})
	}
	return records, nil
}

// EventPipelineDispatcher is the GitLab dispatch surface the record
// dispatcher needs; poll.Poller implements it.
type EventPipelineDispatcher interface {
	DispatchJiraEvent(ctx context.Context, stage string, event *normevent.Event) error
}

// GitLabDispatcher is the jirapoll output driver for GitLab targets. It
// recovers the normalized event the CEL matcher embedded in the record's
// event_payload, validates it, and starts the agent pipeline for the
// record's agent.
type GitLabDispatcher struct {
	Poller EventPipelineDispatcher
}

// Dispatch implements jirapoll.RecordDispatcher.
func (d GitLabDispatcher) Dispatch(ctx context.Context, record jirapoll.DispatchRecord) error {
	var payload struct {
		NormalizedEvent json.RawMessage `json:"_normalized_event"`
	}
	if err := json.Unmarshal([]byte(record.EventPayload), &payload); err != nil {
		return fmt.Errorf("parse event payload for %s: %w", record.Agent, err)
	}
	if len(payload.NormalizedEvent) == 0 {
		return fmt.Errorf("event payload for %s has no _normalized_event", record.Agent)
	}
	event, err := normevent.ParseJSON(payload.NormalizedEvent)
	if err != nil {
		return fmt.Errorf("event payload for %s: %w", record.Agent, err)
	}
	return d.Poller.DispatchJiraEvent(ctx, record.Agent, event)
}
