package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestParseWorkItemURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    workItemRef
		wantErr string
	}{
		{
			name: "pull request",
			url:  "https://github.com/org/repo/pull/123",
			want: workItemRef{Forge: "github", Owner: "org", Repo: "repo", Number: 123},
		},
		{
			name: "issue",
			url:  "https://github.com/org/repo/issues/7",
			// Both URL forms parse to the same shape: the kind is not read
			// off the path, it is resolved from the forge.
			want: workItemRef{Forge: "github", Owner: "org", Repo: "repo", Number: 7},
		},
		{
			name: "a URL copied from the browser keeps its comment anchor",
			url:  "https://github.com/org/repo/pull/123#issuecomment-999",
			want: workItemRef{Forge: "github", Owner: "org", Repo: "repo", Number: 123},
		},
		{
			name: "a files tab URL still names the PR",
			url:  "https://github.com/org/repo/pull/123/files",
			want: workItemRef{Forge: "github", Owner: "org", Repo: "repo", Number: 123},
		},
		{
			name: "gitlab is recognised so the error can name the real gap",
			url:  "https://gitlab.com/group/sub/repo/-/merge_requests/4",
			want: workItemRef{Forge: "gitlab"},
		},
		{
			name:    "a commit URL is not a work item",
			url:     "https://github.com/org/repo/commit/abc123",
			wantErr: "not a GitHub issue or pull request URL",
		},
		{
			name:    "a repo URL is not a work item",
			url:     "https://github.com/org/repo",
			wantErr: "not a GitHub issue or pull request URL",
		},
		{
			name:    "a non-numeric item",
			url:     "https://github.com/org/repo/pull/abc",
			wantErr: "is not a valid item number",
		},
		{
			name:    "wrong scheme",
			url:     "ssh://github.com/org/repo/pull/1",
			wantErr: "unsupported scheme",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWorkItemURL(tt.url)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The files-tab case above resolves the PR from the last two segments, so a
// URL whose trailing segment is a number but whose parent is not issues/pull
// must not be mistaken for a work item.
func TestParseWorkItemURL_DoesNotGuess(t *testing.T) {
	_, err := parseWorkItemURL("https://github.com/org/repo/releases/tag/12")
	require.Error(t, err)
}

func TestBuildSteerComment(t *testing.T) {
	// The default follows the item type, which is how the stage commands
	// themselves divide: a PR is reviewed, an issue is triaged.
	got, err := buildSteerComment("", "re-check the migration", true)
	require.NoError(t, err)
	assert.Equal(t, "/fs-review re-check the migration", got)

	got, err = buildSteerComment("", "re-label this", false)
	require.NoError(t, err)
	assert.Equal(t, "/fs-triage re-label this", got)

	// --stage picks explicitly; fix is unreachable without it.
	got, err = buildSteerComment("fix", "rebase onto main", true)
	require.NoError(t, err)
	assert.Equal(t, "/fs-fix rebase onto main", got)

	got, err = buildSteerComment("triage", "look again", true)
	require.NoError(t, err)
	assert.Equal(t, "/fs-triage look again", got)
}

func TestBuildSteerComment_Rejects(t *testing.T) {
	_, err := buildSteerComment("", "   ", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")

	_, err = buildSteerComment("code", "do something", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be review, fix or triage")

	// review and fix are pull-request stages; the route arms nullify them
	// on an issue, so the CLI says so rather than posting a no-op comment.
	for _, stage := range []string{"review", "fix"} {
		_, err = buildSteerComment(stage, "do something", false)
		require.Error(t, err, stage)
		assert.Contains(t, err.Error(), "pull request")
	}
}

// fakePoster is a forge.Client that records the one call the steer command
// makes. Embedding the fake gives it the rest of the interface.
type fakePoster struct {
	*forge.FakeClient
	owner, repo, body string
	number            int
	err               error
	clientErr         error
	// isPR is what the forge reports for the resolved number, and
	// getIssueErr makes that lookup fail.
	isPR        bool
	getIssueErr error
}

func (f *fakePoster) GetIssue(_ context.Context, _, _ string, number int) (*forge.Issue, error) {
	if f.getIssueErr != nil {
		return nil, f.getIssueErr
	}
	return &forge.Issue{Number: number, IsPullRequest: f.isPR}, nil
}

func newFakePoster() *fakePoster {
	return &fakePoster{FakeClient: forge.NewFakeClient()}
}

func (f *fakePoster) CreateIssueComment(_ context.Context, owner, repo string, number int, body string) (*forge.IssueComment, error) {
	f.owner, f.repo, f.number, f.body = owner, repo, number, body
	if f.err != nil {
		return nil, f.err
	}
	return &forge.IssueComment{HTMLURL: "https://github.com/org/repo/pull/123#issuecomment-1"}, nil
}

func withFakePoster(t *testing.T, p *fakePoster) {
	t.Helper()
	prev := newSteerForgeClient
	newSteerForgeClient = func() (forge.Client, error) {
		if p.clientErr != nil {
			return nil, p.clientErr
		}
		return p, nil
	}
	t.Cleanup(func() { newSteerForgeClient = prev })
	t.Setenv("GH_TOKEN", "test-token")
}

func TestSteerCmd_PostsTheComment(t *testing.T) {
	p := newFakePoster()
	p.isPR = true
	withFakePoster(t, p)

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"https://github.com/org/repo/pull/123", "re-check the migration"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, "org", p.owner)
	assert.Equal(t, "repo", p.repo)
	assert.Equal(t, 123, p.number)
	assert.Equal(t, "/fs-review re-check the migration", p.body)
}

func TestSteerCmd_StageFlag(t *testing.T) {
	p := newFakePoster()
	p.isPR = true
	withFakePoster(t, p)

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"--stage", "fix", "https://github.com/org/repo/pull/123", "rebase onto main"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "/fs-fix rebase onto main", p.body)
}

func TestSteerCmd_GitLabIsNotSupportedYet(t *testing.T) {
	withFakePoster(t, newFakePoster())

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"https://gitlab.com/group/repo/-/merge_requests/4", "re-check"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported on gitlab yet")
}

func TestSteerCmd_PostFailureSurfaces(t *testing.T) {
	withFakePoster(t, &fakePoster{FakeClient: forge.NewFakeClient(), err: errors.New("403 Forbidden")})

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"https://github.com/org/repo/pull/123", "re-check"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "posting the steer comment")
}

func TestSteerCmd_NoToken(t *testing.T) {
	// The token chain lives in the shared forge composition path, so the
	// command surfaces its error rather than resolving tokens itself.
	withFakePoster(t, &fakePoster{
		FakeClient: forge.NewFakeClient(),
		clientErr:  errors.New("no GitHub token found: set GH_TOKEN, GITHUB_TOKEN, or run 'gh auth login'"),
	})

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"https://github.com/org/repo/pull/123", "re-check"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no GitHub token found")
}

func TestParseWorkItemURL_UnknownHost(t *testing.T) {
	_, err := parseWorkItemURL("https://example.com/org/repo/pull/1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported forge host")
}

func TestParseWorkItemURL_SelfHostedGitLab(t *testing.T) {
	got, err := parseWorkItemURL("https://gitlab.example.com/group/repo/-/merge_requests/4")
	require.NoError(t, err)
	assert.Equal(t, "gitlab", got.Forge)
}

// TestSteerCmd_PullRequestViaIssuesURL is the regression. GitHub serves a
// pull request from /issues/N as well as /pull/N, so reading the kind off
// the path segment called a real PR an issue: --stage review and fix were
// rejected with a false message, and the default posted /fs-triage, whose
// route arm has no ISSUE_IS_PR guard — so a triage run dispatched against
// a pull request.
func TestSteerCmd_PullRequestViaIssuesURL(t *testing.T) {
	p := newFakePoster()
	p.isPR = true // the forge says this number is a pull request
	withFakePoster(t, p)

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"https://github.com/org/repo/issues/123", "re-check the migration"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, "/fs-review re-check the migration", p.body,
		"the forge's answer decides the stage, not the URL's path segment")
}

// TestSteerCmd_IssueViaIssuesURL is the other side: a real issue still
// posts /fs-triage, so the fix does not simply treat everything as a PR.
func TestSteerCmd_IssueViaIssuesURL(t *testing.T) {
	p := newFakePoster()
	p.isPR = false
	withFakePoster(t, p)

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"https://github.com/org/repo/issues/7", "re-label this"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, "/fs-triage re-label this", p.body)
}

// TestSteerCmd_StageFixOnPullRequestViaIssuesURL: the --stage rejection is
// kept, but it now rests on what the forge reports rather than on the URL
// shape, so the PR form that used to be refused is accepted.
func TestSteerCmd_StageFixOnPullRequestViaIssuesURL(t *testing.T) {
	p := newFakePoster()
	p.isPR = true
	withFakePoster(t, p)

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"--stage", "fix", "https://github.com/org/repo/issues/123", "rebase onto main"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, "/fs-fix rebase onto main", p.body)
}

// TestSteerCmd_ResolveFailureIsFatal: the lookup failing must fail the
// command and post nothing. Falling back to the URL guess is exactly the
// silent wrong-stage dispatch this replaces, and an error the user sees is
// strictly better.
func TestSteerCmd_ResolveFailureIsFatal(t *testing.T) {
	p := newFakePoster()
	p.getIssueErr = errors.New("403 Forbidden")
	withFakePoster(t, p)

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"https://github.com/org/repo/issues/123", "re-check the migration"})
	err := cmd.Execute()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving org/repo#123")
	assert.Empty(t, p.body, "no comment may be posted when the kind is unknown")
}
