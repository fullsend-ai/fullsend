package harnessdispatch

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestProjectExecutionRef_PRWithChangeProposal(t *testing.T) {
	ev := mustEvent(t, "pr-opened.json")
	ref, err := ProjectExecutionRef("pr-ping", "triage", ev)
	require.NoError(t, err)
	assert.Equal(t, "pr-ping", ref.Agent)
	assert.Contains(t, ref.EventPayload, `"pull_request"`)
	assert.Contains(t, ref.EventPayload, `"head"`)
	assert.Equal(t, "100", ref.StatusNumber)
}

func TestProjectExecutionRef_FsFixTriggerSource(t *testing.T) {
	ev := mustEvent(t, "fs-fix-comment.json")
	ref, err := ProjectExecutionRef("fix", "fix", ev)
	require.NoError(t, err)
	assert.Equal(t, ev.Actor.ID, ref.TriggerSource)
	assert.Contains(t, ref.EventPayload, `"comment"`)
}

func TestProjectExecutionRef_ReviewTriggerSource(t *testing.T) {
	ev := mustEvent(t, "review-changes-requested.json")
	ref, err := ProjectExecutionRef("review", "review", ev)
	require.NoError(t, err)
	assert.Equal(t, ev.Transition.Review.ReviewerID, ref.TriggerSource)
}

func TestProjectExecutionRef_LinkedChangeProposal(t *testing.T) {
	ev := mustEvent(t, "fs-fix-comment.json")
	require.NotNil(t, ev.Entity.LinkedChangeProposal)
	require.NotNil(t, ev.State.ChangeProposal)

	ref, err := ProjectExecutionRef("fix", "fix", ev)
	require.NoError(t, err)
	assert.Contains(t, ref.EventPayload, `"pull_request"`)
}

func TestTriggerSource_NoMatch(t *testing.T) {
	ev := mustEvent(t, "issue-opened.json")
	assert.Empty(t, triggerSource(ev))
}

func TestBuildPullRequestPayload_NoChangeProposal(t *testing.T) {
	ev := &normevent.Event{
		Entity: normevent.Entity{Kind: normevent.EntityChangeProposal, ID: 7, URL: "https://example.com/pull/7"},
	}
	pr := buildPullRequestPayload(ev)
	assert.Equal(t, 7, pr["number"])
}

func TestBuildEventPayload_ContainsNormalizedEvent(t *testing.T) {
	ev := mustEvent(t, "jira-fs-triage-comment.json")
	payload, err := buildEventPayload(ev)
	require.NoError(t, err)

	// Legacy fields should still be present.
	assert.Contains(t, payload, "issue")
	assert.Contains(t, payload, "comment")

	// The complete normalized event should be embedded.
	normRaw, ok := payload["_normalized_event"]
	require.True(t, ok, "_normalized_event must be present")

	normMap, ok := normRaw.(map[string]any)
	require.True(t, ok, "_normalized_event must be a map")

	// Verify source.system is preserved (the field lost before #6748).
	src, ok := normMap["source"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "jira", src["system"])

	// Verify entity.key is preserved.
	ent, ok := normMap["entity"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "PROJ-123", ent["key"])
}

func TestBuildEventPayload_NormalizedEventRoundTrips(t *testing.T) {
	// Verify the embedded _normalized_event can be re-parsed as a valid
	// NormalizedEvent — the same path fullsend run takes.
	ev := mustEvent(t, "ready-to-code-labeled.json")
	payload, err := buildEventPayload(ev)
	require.NoError(t, err)

	normBytes, err := json.Marshal(payload["_normalized_event"])
	require.NoError(t, err)

	roundTripped, err := normevent.ParseJSON(normBytes)
	require.NoError(t, err)
	assert.Equal(t, ev.Repo, roundTripped.Repo)
	assert.Equal(t, ev.Source.System, roundTripped.Source.System)
	assert.Equal(t, ev.Entity.Kind, roundTripped.Entity.Kind)
}
