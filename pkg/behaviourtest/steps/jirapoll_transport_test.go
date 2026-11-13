package steps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/jiradispatch"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// These tests drive the jira-transport-bounds.feature scenarios' step
// functions directly (the behaviour suite itself needs a leased GitHub
// org), the same way jirapoll_test.go covers jira-poll-dispatch.feature.

const transportCodeTrigger = `event.source.system == "jira"
&& event.entity.kind == "work_item"
&& event.transition.kind == "label_changed"
&& event.transition.label.action == "added"
&& "ready-to-code" in event.state.labels`

const transportTriageTrigger = `event.source.system == "jira"
&& event.entity.kind == "work_item"
&& event.transition.kind == "comment_added"
&& has(event.transition.comment.command)
&& event.transition.comment.command == "/fs-triage"
&& event.actor.kind == "human"`

func newTransportWorld(t *testing.T) *world.World {
	t.Helper()
	w := &world.World{Logf: t.Logf}
	require.NoError(t, givenMockJiraServer(w))
	t.Cleanup(func() {
		CleanupScenario(w)
	})
	require.NoError(t, givenLocalHarness(w, "code", "coder", transportCodeTrigger))
	require.NoError(t, givenLocalHarness(w, "triage", "triage", transportTriageTrigger))
	require.NoError(t, givenJiraGitLabTarget(w, "group/sub/project"))
	return w
}

// TestJiraTransport_LabelPastBoundDoesNotFire mirrors "A trigger keyed on
// a label past the transport bound does not fire": the GitLab target
// bounds state.labels before trigger matching, so a trigger keyed on a
// label past the 1024-byte bound does not launch an agent whose
// transported event lacks the label, while a within-bound trigger does.
func TestJiraTransport_LabelPastBoundDoesNotFire(t *testing.T) {
	t.Parallel()
	w := newTransportWorld(t)

	require.NoError(t, givenJiraIssue(w, "PROJ-501", "backlog"))
	require.NoError(t, givenPadLabels(w, 30, 40, "PROJ-501"))
	require.NoError(t, whenJiraLabelAdded(w, "PROJ-501", "ready-to-code"))
	require.NoError(t, givenJiraIssue(w, "PROJ-503", "backlog"))
	require.NoError(t, givenPadLabels(w, 20, 40, "PROJ-503"))
	require.NoError(t, whenJiraLabelAdded(w, "PROJ-503", "ready-to-code"))
	require.NoError(t, givenJiraIssue(w, "PROJ-502", "backlog"))
	require.NoError(t, whenJiraLabelAdded(w, "PROJ-502", "ready-to-code"))

	require.NoError(t, whenJiraGitLabPollerRuns(context.Background(), w))

	require.NoError(t, thenNoPipelineCreated(w, "PROJ-501"))
	require.NoError(t, thenPipelineCreated(w, "PROJ-503"))
	require.NoError(t, thenPipelineCreated(w, "PROJ-502"))
}

// TestJiraTransport_OversizedCommandProducesNoRun mirrors "An oversized
// /fs-* command produces no run": the dispatch door refuses a command
// payload that cannot fit the transport budget instead of dispatching a
// shortened instruction, the refusal does not fail the poll cycle, and a
// within-budget command on a sibling issue still dispatches.
func TestJiraTransport_OversizedCommandProducesNoRun(t *testing.T) {
	t.Parallel()
	w := newTransportWorld(t)

	require.NoError(t, givenJiraIssue(w, "PROJ-601", "bug"))
	require.NoError(t, whenOversizedCommand(w, "/fs-triage", "PROJ-601"))
	require.NoError(t, givenJiraIssue(w, "PROJ-602", "bug"))
	require.NoError(t, whenJiraComment(w, "PROJ-602", "/fs-triage check acceptance criteria"))

	require.NoError(t, whenJiraGitLabPollerRuns(context.Background(), w))

	require.NoError(t, thenNoPipelineCreated(w, "PROJ-601"))
	require.NoError(t, thenPipelineCreated(w, "PROJ-602"))
}

// TestJiraTransport_PadLabelsFillBound pins the fixture arithmetic the
// label-bound scenario rests on: 30 forty-character pad labels put the
// issue's label list past the 1024-byte JSON bound (so the trigger label
// added after them is dropped by the bound the GitLab target applies),
// while 20 stay within it.
func TestJiraTransport_PadLabelsFillBound(t *testing.T) {
	t.Parallel()
	w := newTransportWorld(t)

	require.NoError(t, givenJiraIssue(w, "PROJ-501", "backlog"))
	require.NoError(t, givenPadLabels(w, 30, 40, "PROJ-501"))
	require.NoError(t, whenJiraLabelAdded(w, "PROJ-501", "ready-to-code"))
	labels := w.JiraMockState.Labels("PROJ-501")
	require.Len(t, labels, 31, "pads plus the trigger label")
	require.Equal(t, "ready-to-code", labels[len(labels)-1])
	require.Greater(t, boundTransportLabelsJSONSize(labels), 1024,
		"the over-bound fixture's label state must exceed the transport labels bound")

	require.NoError(t, givenJiraIssue(w, "PROJ-503", "backlog"))
	require.NoError(t, givenPadLabels(w, 20, 40, "PROJ-503"))
	require.NoError(t, whenJiraLabelAdded(w, "PROJ-503", "ready-to-code"))
	within := w.JiraMockState.Labels("PROJ-503")
	require.Len(t, within, 21, "pads plus the trigger label")
	require.LessOrEqual(t, boundTransportLabelsJSONSize(within), 1024,
		"the within-bound fixture's label state must fit the transport labels bound")
}

// boundTransportLabelsJSONSize measures the JSON size of the label list
// the way the transport bound does (marshal, then cut at the limit).
func boundTransportLabelsJSONSize(labels []string) int {
	var b strings.Builder
	b.WriteByte('[')
	for i, l := range labels {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(l)
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.Len()
}

// TestJiraTransport_FakeGitLabAPISurface drives every fakeGitLabAPI
// method the transport scenarios do not reach: the inert poll-cycle
// surface returns empty and not-found, the authenticated-user reads are
// stable, and CreateChildPipelineWithInputs records through the same
// path as CreatePipelineWithInputs.
func TestJiraTransport_FakeGitLabAPISurface(t *testing.T) {
	t.Parallel()
	w := newTransportWorld(t)
	f := &fakeGitLabAPI{w: w}
	ctx := context.Background()

	issues, err := f.ListIssuesUpdatedSince(ctx, "group", "project", time.Time{})
	require.NoError(t, err)
	require.Empty(t, issues)
	mrs, err := f.ListMergeRequestsUpdatedSince(ctx, "group", "project", time.Time{})
	require.NoError(t, err)
	require.Empty(t, mrs)
	events, err := f.ListProjectEvents(ctx, "group", "project", "note", time.Time{})
	require.NoError(t, err)
	require.Empty(t, events)
	notes, err := f.ListIssueNotes(ctx, "group", "project", 1)
	require.NoError(t, err)
	require.Empty(t, notes)
	mrNotes, err := f.ListMergeRequestNotes(ctx, "group", "project", 1)
	require.NoError(t, err)
	require.Empty(t, mrNotes)
	versions, err := f.ListMergeRequestDiffVersions(ctx, "group", "project", 1)
	require.NoError(t, err)
	require.Empty(t, versions)
	labelEvents, err := f.ListResourceLabelEvents(ctx, "group", "project", 1)
	require.NoError(t, err)
	require.Empty(t, labelEvents)

	wrapper, err := f.GetFileContentAtRef(ctx, "group", "project", ".gitlab/ci/fullsend-pipeline.yml", "main")
	require.NoError(t, err)
	require.Contains(t, string(wrapper), "event_payload_chunk_08")
	_, err = f.GetFileContentAtRef(ctx, "group", "project", "some/other/file.yml", "main")
	require.ErrorIs(t, err, forge.ErrNotFound)

	_, err = f.GetBranchRef(ctx, "group", "project", "main")
	require.ErrorIs(t, err, forge.ErrNotFound)
	require.ErrorIs(t, f.CommitFileToBranch(ctx, "group", "project", "refs/heads/b", "p", "m", nil, ""), forge.ErrNotFound)
	require.ErrorIs(t, f.ForceCommitFileToBranch(ctx, "group", "project", "refs/heads/b", "p", "m", nil), forge.ErrNotFound)
	require.ErrorIs(t, f.DeleteRef(ctx, "group", "project", "refs/heads/b"), forge.ErrNotFound)
	require.ErrorIs(t, f.CreateNoteAwardEmoji(ctx, "group", "project", "Issue", 1, 1, "eyes"), forge.ErrNotFound)
	_, err = f.GetIssue(ctx, "group", "project", 1)
	require.ErrorIs(t, err, forge.ErrNotFound)
	_, err = f.GetMergeRequest(ctx, "group", "project", 1)
	require.ErrorIs(t, err, forge.ErrNotFound)
	_, err = f.GetMemberAccessLevel(ctx, "group", "project", 1)
	require.ErrorIs(t, err, forge.ErrNotFound)
	_, err = f.GetProjectPath(ctx, 1)
	require.ErrorIs(t, err, forge.ErrNotFound)

	login, err := f.GetAuthenticatedUser(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, login)
	uid, err := f.GetAuthenticatedUserID(ctx)
	require.NoError(t, err)
	require.NotZero(t, uid)
	bot, err := f.GetUserBot(ctx, uid)
	require.NoError(t, err)
	require.False(t, bot)

	id, url, err := f.CreateChildPipelineWithInputs(ctx, "group", "project", "main", "job-token", map[string]forge.PipelineInputValue{
		"stage":        forge.StringInput("triage"),
		"resource_key": forge.StringInput("jira-PROJ-1"),
	})
	require.NoError(t, err)
	require.NotZero(t, id)
	require.NotEmpty(t, url)
	require.Equal(t, []world.JiraTransportPipeline{{Stage: "triage", ResourceKey: "jira-PROJ-1"}}, w.JiraTransport.Pipelines)

	// A non-string input reads as the empty string, not an error.
	require.Empty(t, pipelineInputString(map[string]forge.PipelineInputValue{"n": forge.NumberInput(1)}, "n"))
	require.Empty(t, pipelineInputString(nil, "absent"))
}

// TestJiraTransport_DispatcherRejectsBadPayloads covers the output
// driver's fail-closed parse branches: a record whose event payload is
// not JSON, one without _normalized_event, and one whose normalized
// event does not parse.
func TestJiraTransport_DispatcherRejectsBadPayloads(t *testing.T) {
	t.Parallel()
	var reached recordingJiraDoor
	d := jiradispatch.GitLabDispatcher{Poller: &reached}
	ctx := context.Background()

	require.Error(t, d.Dispatch(ctx, jirapoll.DispatchRecord{Agent: "triage", EventPayload: json.RawMessage("not json")}))
	require.Error(t, d.Dispatch(ctx, jirapoll.DispatchRecord{Agent: "triage", EventPayload: json.RawMessage(`{}`)}))
	require.Error(t, d.Dispatch(ctx, jirapoll.DispatchRecord{Agent: "triage", EventPayload: json.RawMessage(`{"_normalized_event": "[]"}`)}))
	require.False(t, reached.called, "a bad payload must fail before the dispatch door is reached")
}

// recordingJiraDoor records whether the dispatch door was reached; the
// bad-payload branches must fail before it is.
type recordingJiraDoor struct {
	called bool
}

func (r *recordingJiraDoor) DispatchJiraEvent(_ context.Context, _ string, _ *normevent.Event) error {
	r.called = true
	return nil
}

// TestJiraTransport_StepGuards covers the steps' argument and state
// guards: every misuse names the missing precondition instead of
// panicking.
func TestJiraTransport_StepGuards(t *testing.T) {
	t.Parallel()

	bare := &world.World{Logf: t.Logf}
	require.ErrorContains(t, givenLocalHarness(bare, "code", "coder", "t"), "mock Jira server")
	require.ErrorContains(t, givenJiraGitLabTarget(bare, "group/project"), "mock Jira server")
	require.ErrorContains(t, whenJiraGitLabPollerRuns(context.Background(), bare), "mock Jira server")
	require.ErrorContains(t, givenPadLabels(bare, 30, 40, "PROJ-1"), "mock Jira server")
	require.ErrorContains(t, whenOversizedCommand(bare, "/fs-triage", "PROJ-1"), "mock Jira server")
	require.ErrorContains(t, thenPipelineCreated(bare, "PROJ-1"), "GitLab target")
	require.ErrorContains(t, thenNoPipelineCreated(bare, "PROJ-1"), "GitLab target")

	w := newTransportWorld(t)
	require.NoError(t, givenJiraIssue(w, "PROJ-1", "bug"))
	require.ErrorContains(t, givenPadLabels(w, 30, 0, "PROJ-1"), "width must be positive")
	require.ErrorContains(t, whenOversizedCommand(w, "/nope", "PROJ-1"), "not a /fs-* command")
	require.ErrorContains(t, givenLocalHarness(w, "code", "coder", "t"), "already registered")
	require.ErrorContains(t, givenJiraGitLabTarget(w, "other/project"), "already set")

	noTarget := &world.World{Logf: t.Logf}
	require.NoError(t, givenMockJiraServer(noTarget))
	t.Cleanup(func() {
		CleanupScenario(noTarget)
	})
	require.ErrorContains(t, whenJiraGitLabPollerRuns(context.Background(), noTarget), "GitLab target not set")
}
