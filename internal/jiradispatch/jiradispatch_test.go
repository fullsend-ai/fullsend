package jiradispatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// Both halves of this package moved out of internal/cli and
// pkg/behaviourtest, whose cross-package tests still pin them from outside.
// Per-package Go coverage does not attribute cross-package test runs to
// this package's lines, so these in-package tests mirror the callers'
// tests here: the CEL match core (role thresholds, the silent
// unauthorized drop, the record projection) and the GitLab record
// dispatcher's payload recovery and error shapes.

// normativeExample loads a normalized-event example from the normative
// spec, the way the production and behaviour callers do.
func normativeExample(t *testing.T, name string) *normevent.Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "normative", "normalized-event", "v1", "examples", name))
	require.NoError(t, err)
	ev, err := normevent.ParseJSON(raw)
	require.NoError(t, err)
	return ev
}

func jiraLabelAddedEvent(role normevent.ActorRole) *normevent.Event {
	return &normevent.Event{
		Repo:   "acme/platform",
		Source: normevent.Source{System: normevent.SystemJira, RawType: "changelog", RawAction: "updated"},
		Entity: normevent.Entity{Kind: normevent.EntityWorkItem, ID: 10042, Key: "PROJ-123"},
		Transition: normevent.Transition{
			Kind:  normevent.TransitionLabelChanged,
			Label: &normevent.LabelChange{Name: "ready-to-code", Action: "added"},
		},
		Actor: normevent.Actor{ID: "557058:abc", Kind: normevent.ActorHuman, Role: role},
	}
}

func agents(records []jirapoll.DispatchRecord) []string {
	var got []string
	for _, r := range records {
		got = append(got, r.Agent)
	}
	return got
}

func TestMatcher_MatchProjectsExecutionRef(t *testing.T) {
	trigger := `event.entity.kind == "work_item" && event.transition.kind == "opened"`
	m := &Matcher{Candidates: []harnessdispatch.TriggeredHarness{
		{Name: "triage", Harness: &harness.Harness{Role: "triage", Trigger: trigger}},
	}}

	ev := normativeExample(t, "issue-opened.json")
	ev.Actor.Role = normevent.RoleWrite

	records, err := m.Match(context.Background(), ev)
	require.NoError(t, err)
	require.Len(t, records, 1)

	ref, err := harnessdispatch.ProjectExecutionRef("triage", "triage", ev)
	require.NoError(t, err)
	r := records[0]
	assert.Equal(t, ref.Agent, r.Agent)
	assert.Equal(t, ref.Role, r.Role)
	assert.Equal(t, ref.SourceRepo, r.SourceRepo)
	assert.Equal(t, ref.EventType, r.EventType)
	assert.Equal(t, ref.EventPayload, r.EventPayload)
	assert.Equal(t, ref.TriggerSource, r.TriggerSource)
	assert.Equal(t, ref.StatusRepo, r.StatusRepo)
	assert.Equal(t, ref.StatusNumber, r.StatusNumber)

	// The record's payload carries the embedded normalized event the
	// GitLab dispatcher recovers.
	var payload struct {
		NormalizedEvent json.RawMessage `json:"_normalized_event"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.EventPayload), &payload))
	assert.NotEmpty(t, payload.NormalizedEvent, "record payload embeds _normalized_event")
}

func TestMatcher_MatchPerHarnessRoleThreshold(t *testing.T) {
	trigger := `event.entity.kind == "work_item" && event.transition.kind == "opened"`
	candidates := []harnessdispatch.TriggeredHarness{
		{Name: "triage", Harness: &harness.Harness{Role: "triage", Trigger: trigger}},
		{Name: "code", Harness: &harness.Harness{Role: "coder", Trigger: trigger}},
	}
	m := &Matcher{Candidates: candidates}

	tests := []struct {
		name      string
		actorRole normevent.ActorRole
		want      []string
	}{
		{name: "triage actor gets observation only", actorRole: normevent.RoleTriage, want: []string{"triage"}},
		{name: "write actor gets both", actorRole: normevent.RoleWrite, want: []string{"triage", "code"}},
		{name: "read actor gets neither", actorRole: normevent.RoleRead},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := normativeExample(t, "issue-opened.json")
			ev.Actor.Role = tc.actorRole
			records, err := m.Match(context.Background(), ev)
			require.NoError(t, err)
			assert.Equal(t, tc.want, agents(records))
		})
	}
}

func TestMatcher_MatchDropsUnauthorizedEventSilently(t *testing.T) {
	// A Jira label-added event from an actor below the triage threshold,
	// with no authorization policy that lifts it: the event-level gate
	// refuses the event for every harness and Match returns no records
	// and no error — the poller advances past it.
	m := &Matcher{Candidates: []harnessdispatch.TriggeredHarness{
		{Name: "triage", Harness: &harness.Harness{Role: "triage", Trigger: `event.transition.kind == "label_changed"`}},
	}}
	records, err := m.Match(context.Background(), jiraLabelAddedEvent(normevent.RoleNone))
	require.NoError(t, err)
	assert.Empty(t, records)
}

type fakeEventPipelineDispatcher struct {
	stage string
	event *normevent.Event
	err   error
}

func (f *fakeEventPipelineDispatcher) DispatchJiraEvent(_ context.Context, stage string, event *normevent.Event) error {
	f.stage = stage
	f.event = event
	return f.err
}

func dispatchRecord(t *testing.T, agent string) jirapoll.DispatchRecord {
	t.Helper()
	ev := normativeExample(t, "jira-fs-triage-comment.json")
	ref, err := harnessdispatch.ProjectExecutionRef(agent, "triage", ev)
	require.NoError(t, err)
	return jirapoll.DispatchRecord{Agent: ref.Agent, Role: ref.Role, EventType: ref.EventType, EventPayload: ref.EventPayload}
}

func TestGitLabDispatcher_ForwardsNormalizedEvent(t *testing.T) {
	fake := &fakeEventPipelineDispatcher{}
	d := GitLabDispatcher{Poller: fake}
	record := dispatchRecord(t, "triage")
	require.NoError(t, d.Dispatch(context.Background(), record))
	assert.Equal(t, "triage", fake.stage)
	require.NotNil(t, fake.event)
	assert.Equal(t, record.Agent, "triage")
	assert.Equal(t, "PROJ-123", fake.event.Entity.Key)

	fake.err = errors.New("pipeline rejected")
	err := d.Dispatch(context.Background(), record)
	assert.ErrorIs(t, err, fake.err)
}

func TestGitLabDispatcher_RejectsBadPayloads(t *testing.T) {
	valid := dispatchRecord(t, "triage")
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(valid.EventPayload), &payload))
	norm := payload["_normalized_event"].(map[string]any)
	delete(norm, "repo")
	invalid, err := json.Marshal(payload)
	require.NoError(t, err)

	for name, tc := range map[string]struct{ payload, wantErr string }{
		"not JSON":                 {"{", "parse event payload"},
		"no normalized event":      {`{"issue":{"number":1}}`, "no _normalized_event"},
		"invalid normalized event": {string(invalid), "event payload"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeEventPipelineDispatcher{}
			err := GitLabDispatcher{Poller: fake}.Dispatch(context.Background(), jirapoll.DispatchRecord{Agent: "triage", EventPayload: tc.payload})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Nil(t, fake.event, "no pipeline dispatch for an invalid payload")
		})
	}
}
