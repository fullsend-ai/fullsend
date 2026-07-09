package harnessdispatch

import (
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
