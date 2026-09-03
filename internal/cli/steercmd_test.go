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
	got, err := buildSteerComment("", "re-check the migration")
	require.NoError(t, err)
	assert.Equal(t, "/fs-steer re-check the migration", got)

	got, err = buildSteerComment("fix", "rebase onto main")
	require.NoError(t, err)
	assert.Equal(t, "/fs-steer fix: rebase onto main", got)

	for _, stage := range []string{"review", "triage"} {
		got, err = buildSteerComment(stage, "look again")
		require.NoError(t, err)
		assert.Equal(t, "/fs-steer "+stage+": look again", got)
	}
}

func TestBuildSteerComment_Rejects(t *testing.T) {
	_, err := buildSteerComment("", "   ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")

	_, err = buildSteerComment("code", "do something")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be review, fix or triage")
}

type fakePoster struct {
	owner, repo, body string
	number            int
	err               error
}

func (f *fakePoster) CreateIssueComment(_ context.Context, owner, repo string, number int, body string) (*forge.IssueComment, error) {
	f.owner, f.repo, f.number, f.body = owner, repo, number, body
	if f.err != nil {
		return nil, f.err
	}
	return &forge.IssueComment{HTMLURL: "https://github.com/org/repo/pull/123#issuecomment-1"}, nil
}

func withFakePoster(t *testing.T, p steerCommentPoster) {
	t.Helper()
	prev := newSteerCommentPoster
	newSteerCommentPoster = func(string) steerCommentPoster { return p }
	t.Cleanup(func() { newSteerCommentPoster = prev })
	t.Setenv("GH_TOKEN", "test-token")
}

func TestSteerCmd_PostsTheComment(t *testing.T) {
	p := &fakePoster{}
	withFakePoster(t, p)

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"https://github.com/org/repo/pull/123", "re-check the migration"})
	require.NoError(t, cmd.Execute())

	assert.Equal(t, "org", p.owner)
	assert.Equal(t, "repo", p.repo)
	assert.Equal(t, 123, p.number)
	assert.Equal(t, "/fs-steer re-check the migration", p.body)
}

func TestSteerCmd_StageFlag(t *testing.T) {
	p := &fakePoster{}
	withFakePoster(t, p)

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"--stage", "fix", "https://github.com/org/repo/pull/123", "rebase onto main"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, "/fs-steer fix: rebase onto main", p.body)
}

func TestSteerCmd_GitLabIsNotSupportedYet(t *testing.T) {
	withFakePoster(t, &fakePoster{})

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"https://gitlab.com/group/repo/-/merge_requests/4", "re-check"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported on gitlab yet")
}

func TestSteerCmd_PostFailureSurfaces(t *testing.T) {
	withFakePoster(t, &fakePoster{err: errors.New("403 Forbidden")})

	cmd := newSteerCmd()
	cmd.SetArgs([]string{"https://github.com/org/repo/pull/123", "re-check"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "posting the steer comment")
}

func TestSteerCmd_NoToken(t *testing.T) {
	withFakePoster(t, &fakePoster{})
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", t.TempDir()) // no gh binary to fall back to

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
