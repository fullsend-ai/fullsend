package steps

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeForgeStateSCM implements the scm.Driver reads the forge-state
// steps use. Unused methods come from the embedded interface and panic
// when called.
type fakeForgeStateSCM struct {
	scm.Driver

	items      map[int]forge.Issue          // issue/PR number → item returned by GetIssue
	comments   map[int][]forge.IssueComment // issue/PR number → comments
	openIssues []forge.Issue
	labelsSeen [][]string // label filters ListOpenIssues was called with
	err        error      // returned by every read when set
}

func (f *fakeForgeStateSCM) GetIssue(_ context.Context, _, _ string, number int) (*forge.Issue, error) {
	if f.err != nil {
		return nil, f.err
	}
	item, ok := f.items[number]
	if !ok {
		return nil, forge.ErrNotFound
	}
	return &item, nil
}

func (f *fakeForgeStateSCM) ListComments(_ context.Context, _, _ string, number int) ([]forge.IssueComment, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.comments[number], nil
}

func (f *fakeForgeStateSCM) ListOpenIssues(_ context.Context, _, _ string, labels ...string) ([]forge.Issue, error) {
	f.labelsSeen = append(f.labelsSeen, labels)
	if f.err != nil {
		return nil, f.err
	}
	return f.openIssues, nil
}

// forgeStateWorld returns a World whose scenario issue is #7 and pull
// request is #8.
func forgeStateWorld(f *fakeForgeStateSCM) *world.World {
	return &world.World{
		SCM:         f,
		RepoOwner:   "org",
		RepoName:    "repo",
		IssueNumber: 7,
		PRNumber:    8,
	}
}

func TestTargetNumber(t *testing.T) {
	t.Parallel()

	w := forgeStateWorld(&fakeForgeStateSCM{})
	n, err := targetNumber(w, targetIssue)
	require.NoError(t, err)
	assert.Equal(t, 7, n)
	n, err = targetNumber(w, targetPullRequest)
	require.NoError(t, err)
	assert.Equal(t, 8, n)

	_, err = targetNumber(&world.World{}, targetIssue)
	require.ErrorContains(t, err, "no issue created")
	_, err = targetNumber(&world.World{}, targetPullRequest)
	require.ErrorContains(t, err, "no pull request opened")
	_, err = targetNumber(w, "commit")
	require.ErrorContains(t, err, `unknown forge item "commit"`)
}

func TestThenTargetLabel(t *testing.T) {
	t.Parallel()

	f := &fakeForgeStateSCM{items: map[int]forge.Issue{
		7: {Number: 7, Labels: []string{"ready-to-code"}},
		8: {Number: 8, Labels: []string{"ready-for-review"}},
	}}
	w := forgeStateWorld(f)

	// Issue label present (the existing step) and absent.
	require.NoError(t, thenIssueHasLabel(w, "ready-to-code"))
	require.ErrorContains(t, thenIssueHasLabel(w, "needs-info"), `issue #7 labels [ready-to-code] do not include "needs-info"`)
	require.NoError(t, thenTargetLabel(w, targetIssue, "needs-info", false))
	require.ErrorContains(t, thenTargetLabel(w, targetIssue, "ready-to-code", false), `issue #7 labels [ready-to-code] include "ready-to-code"`)

	// Pull request label present and absent read the PR, not the issue.
	require.NoError(t, thenTargetLabel(w, targetPullRequest, "ready-for-review", true))
	require.ErrorContains(t, thenTargetLabel(w, targetPullRequest, "ready-to-code", true), "pull request #8")
	require.NoError(t, thenTargetLabel(w, targetPullRequest, "ready-to-code", false))
	require.ErrorContains(t, thenTargetLabel(w, targetPullRequest, "ready-for-review", false), "include")
}

func TestThenTargetLabel_Errors(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, thenTargetLabel(&world.World{}, targetPullRequest, "x", true), "no pull request opened")

	w := forgeStateWorld(&fakeForgeStateSCM{err: errors.New("HTTP 403")})
	require.ErrorContains(t, thenTargetLabel(w, targetIssue, "x", false), "reading issue #7: HTTP 403")
}

func TestThenIssueClosedAs(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		issue   forge.Issue
		reason  string
		wantErr string
	}{
		"closed completed":        {forge.Issue{Number: 7, State: "closed", StateReason: "completed"}, "completed", ""},
		"closed not planned":      {forge.Issue{Number: 7, State: "closed", StateReason: "not_planned"}, "not_planned", ""},
		"closed duplicate":        {forge.Issue{Number: 7, State: "closed", StateReason: "duplicate"}, "duplicate", ""},
		"still open":              {forge.Issue{Number: 7, State: "open"}, "completed", `issue #7 is "open", want closed as "completed"`},
		"wrong reason":            {forge.Issue{Number: 7, State: "closed", StateReason: "completed"}, "not_planned", `issue #7 closed as "completed", want "not_planned"`},
		"no reason (e.g. GitLab)": {forge.Issue{Number: 7, State: "closed"}, "completed", `closed as "", want "completed"`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := forgeStateWorld(&fakeForgeStateSCM{items: map[int]forge.Issue{7: tc.issue}})
			err := thenIssueClosedAs(w, tc.reason)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
		})
	}

	require.ErrorContains(t, thenIssueClosedAs(&world.World{}, "completed"), "no issue created")
}

func TestThenPullRequestClosed(t *testing.T) {
	t.Parallel()

	f := &fakeForgeStateSCM{items: map[int]forge.Issue{
		7: {Number: 7, State: "closed"},
		8: {Number: 8, State: "open"},
	}}
	w := forgeStateWorld(f)
	require.ErrorContains(t, thenPullRequestClosed(w), `pull request #8 is "open", want closed`)

	f.items[8] = forge.Issue{Number: 8, State: "closed"}
	require.NoError(t, thenPullRequestClosed(w))

	require.ErrorContains(t, thenPullRequestClosed(&world.World{}), "no pull request opened")
}

func TestThenTargetCommentContains(t *testing.T) {
	t.Parallel()

	f := &fakeForgeStateSCM{comments: map[int][]forge.IssueComment{
		7: {
			{Body: "unrelated"},
			{Body: "<!-- fullsend:agent-status:123 -->\nTriage finished"},
		},
		8: {{Body: "<!-- fullsend:review -->\nLooks good"}},
	}}
	w := forgeStateWorld(f)

	require.NoError(t, thenTargetCommentContains(w, targetIssue, "<!-- fullsend:agent-status:"))
	require.NoError(t, thenTargetCommentContains(w, targetPullRequest, "Looks good"))
	require.ErrorContains(t, thenTargetCommentContains(w, targetPullRequest, "Triage finished"),
		`no comment on pull request #8 contains "Triage finished" (1 comments checked)`)

	require.ErrorContains(t, thenTargetCommentContains(&world.World{}, targetIssue, "x"), "no issue created")

	f.err = errors.New("HTTP 403")
	require.ErrorContains(t, thenTargetCommentContains(w, targetIssue, "x"), "listing comments on issue #7")
}

func TestThenPullRequestBodyContains(t *testing.T) {
	t.Parallel()

	f := &fakeForgeStateSCM{items: map[int]forge.Issue{
		8: {Number: 8, Body: "## Summary\n\nFix the thing.\n\nCloses #7"},
	}}
	w := forgeStateWorld(f)

	require.NoError(t, thenPullRequestBodyContains(w, "Closes #<issue>"))
	require.NoError(t, thenPullRequestBodyContains(w, "Fix the thing"))

	w.IssueNumber = 70
	require.ErrorContains(t, thenPullRequestBodyContains(w, "Closes #<issue>"), `pull request #8 body does not contain "Closes #70"`)

	require.ErrorContains(t, thenPullRequestBodyContains(&world.World{IssueNumber: 7}, "Closes #<issue>"), "no pull request opened")
	require.ErrorContains(t, thenPullRequestBodyContains(&world.World{PRNumber: 8}, "Closes #<issue>"), "no issue exists yet")
}

func TestThenIssueWithLabelAndTitleExists(t *testing.T) {
	t.Parallel()

	f := &fakeForgeStateSCM{openIssues: []forge.Issue{
		{Number: 7, Title: "Retro: flaky login test"}, // the scenario's own issue
		{Number: 12, Title: "Unrelated follow-up"},
		{Number: 13, Title: "Retro: flaky login test (follow-up)"},
	}}
	w := forgeStateWorld(f)

	require.NoError(t, thenIssueWithLabelAndTitleExists(w, "retro", "flaky login"))
	assert.Equal(t, [][]string{{"retro"}}, f.labelsSeen, "the label filter is passed to the forge")
	assert.Equal(t, []int{13}, w.CreatedIssueNumbers, "the matched issue is closed at cleanup")

	err := thenIssueWithLabelAndTitleExists(w, "retro", "missing")
	require.ErrorContains(t, err, `no open issue labeled "retro" has a title containing "missing"`)
	assert.Contains(t, err.Error(), `#12 "Unrelated follow-up"`)

	f.openIssues = append(f.openIssues, forge.Issue{Number: 14, Title: "Retro for #7"})
	require.NoError(t, thenIssueWithLabelAndTitleExists(w, "retro", "for #<issue>"))
	assert.Equal(t, []int{13, 14}, w.CreatedIssueNumbers, "<issue> expands to the scenario's issue")

	f.openIssues = f.openIssues[:1]
	err = thenIssueWithLabelAndTitleExists(w, "retro", "flaky login")
	require.ErrorContains(t, err, "(labeled: none)", "the scenario's own issue does not count")
}

func TestThenIssueWithLabelAndTitleExists_Errors(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, thenIssueWithLabelAndTitleExists(&world.World{SCM: &fakeForgeStateSCM{}}, "x", "y"), "no repo configured")

	w := forgeStateWorld(&fakeForgeStateSCM{err: errors.New("HTTP 403")})
	require.ErrorContains(t, thenIssueWithLabelAndTitleExists(w, "x", "y"), `listing open issues labeled "x": HTTP 403`)
}

func TestGivenPullRequestChangingPath(t *testing.T) {
	t.Parallel()

	w, drivers := actorWorld()
	require.NoError(t, givenPullRequestChangingPath(w, "scripts/x.sh"))
	d := drivers["default"]
	assert.Equal(t, []string{"scripts/x.sh"}, d.paths)
	require.Len(t, d.openedPRs, 1)
	assert.Equal(t, 11, w.PRNumber)
	assert.Equal(t, []int{11}, w.CreatedPRNumbers, "the PR is closed at cleanup")
	assert.Equal(t, d.branches, w.CreatedBranches, "the branch is deleted at cleanup")

	require.ErrorContains(t, givenPullRequestChangingPath(w, " "), "must not be empty")

	w, drivers = actorWorld()
	drivers["default"].commitErr = errors.New("push rejected")
	require.ErrorContains(t, givenPullRequestChangingPath(w, "scripts/x.sh"), "push rejected")
	assert.Equal(t, drivers["default"].branches, w.CreatedBranches, "a pushed branch is still cleaned up")
	assert.Empty(t, w.CreatedPRNumbers)
}
