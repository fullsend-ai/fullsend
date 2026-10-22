package steps

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

type fakeStatusCommentSCM struct {
	scm.Driver

	comments []forge.IssueComment
	listErr  error
}

func (f *fakeStatusCommentSCM) ListComments(_ context.Context, _, _ string, _ int) ([]forge.IssueComment, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.comments, nil
}

func TestThenTargetHasStatusComment(t *testing.T) {
	statusComment := "<!-- fullsend:agent-status:run-123 -->\n🤖 Agent · Started 1:00 PM UTC"
	plainComment := "Normal discussion comment"

	t.Run("found on issue", func(t *testing.T) {
		s := &fakeStatusCommentSCM{
			comments: []forge.IssueComment{{Body: plainComment}, {Body: statusComment}},
		}
		w := &world.World{IssueNumber: 42, RepoOwner: "org", RepoName: "repo", SCM: s}
		require.NoError(t, thenTargetHasStatusComment(w, "issue"))
	})

	t.Run("found on pull request", func(t *testing.T) {
		s := &fakeStatusCommentSCM{comments: []forge.IssueComment{{Body: statusComment}}}
		w := &world.World{PRNumber: 10, RepoOwner: "org", RepoName: "repo", SCM: s}
		require.NoError(t, thenTargetHasStatusComment(w, "pull request"))
	})

	t.Run("not found", func(t *testing.T) {
		s := &fakeStatusCommentSCM{
			comments: []forge.IssueComment{{Body: plainComment}},
		}
		w := &world.World{IssueNumber: 42, RepoOwner: "org", RepoName: "repo", SCM: s}
		err := thenTargetHasStatusComment(w, "issue")
		require.ErrorContains(t, err, "issue #42 does not have a status comment (1 comments checked)")
	})

	t.Run("missing issue number", func(t *testing.T) {
		w := &world.World{RepoOwner: "org", RepoName: "repo", SCM: &fakeStatusCommentSCM{}}
		err := thenTargetHasStatusComment(w, "issue")
		require.ErrorContains(t, err, "no issue created")
	})

	t.Run("missing PR number", func(t *testing.T) {
		w := &world.World{RepoOwner: "org", RepoName: "repo", SCM: &fakeStatusCommentSCM{}}
		err := thenTargetHasStatusComment(w, "pull request")
		require.ErrorContains(t, err, "no pull request opened")
	})

	t.Run("invalid target", func(t *testing.T) {
		w := &world.World{RepoOwner: "org", RepoName: "repo", SCM: &fakeStatusCommentSCM{}}
		err := thenTargetHasStatusComment(w, "commit")
		require.ErrorContains(t, err, "unsupported target \"commit\"")
	})

	t.Run("list error propagates", func(t *testing.T) {
		s := &fakeStatusCommentSCM{listErr: errors.New("api rate limit")}
		w := &world.World{IssueNumber: 42, RepoOwner: "org", RepoName: "repo", SCM: s}
		err := thenTargetHasStatusComment(w, "issue")
		require.ErrorContains(t, err, "listing comments on issue #42: api rate limit")
	})
}

func TestThenTargetDoesNotHaveStatusComment(t *testing.T) {
	statusComment := "<!-- fullsend:agent-status:run-123 -->\n🤖 Agent · Started 1:00 PM UTC"
	plainComment := "Normal discussion comment"

	t.Run("no status comment exists", func(t *testing.T) {
		s := &fakeStatusCommentSCM{
			comments: []forge.IssueComment{{Body: plainComment}},
		}
		w := &world.World{IssueNumber: 42, RepoOwner: "org", RepoName: "repo", SCM: s}
		require.NoError(t, thenTargetDoesNotHaveStatusComment(w, "issue"))
	})

	t.Run("empty comments list passes", func(t *testing.T) {
		s := &fakeStatusCommentSCM{comments: nil}
		w := &world.World{PRNumber: 15, RepoOwner: "org", RepoName: "repo", SCM: s}
		require.NoError(t, thenTargetDoesNotHaveStatusComment(w, "pull request"))
	})

	t.Run("unexpectedly has status comment", func(t *testing.T) {
		s := &fakeStatusCommentSCM{
			comments: []forge.IssueComment{{Body: plainComment}, {Body: statusComment}},
		}
		w := &world.World{IssueNumber: 42, RepoOwner: "org", RepoName: "repo", SCM: s}
		err := thenTargetDoesNotHaveStatusComment(w, "issue")
		require.ErrorContains(t, err, "issue #42 unexpectedly has a status comment")
	})

	t.Run("missing target entity", func(t *testing.T) {
		w := &world.World{RepoOwner: "org", RepoName: "repo", SCM: &fakeStatusCommentSCM{}}
		err := thenTargetDoesNotHaveStatusComment(w, "issue")
		require.ErrorContains(t, err, "no issue created")
	})

	t.Run("list error propagates", func(t *testing.T) {
		s := &fakeStatusCommentSCM{listErr: errors.New("network down")}
		w := &world.World{IssueNumber: 42, RepoOwner: "org", RepoName: "repo", SCM: s}
		err := thenTargetDoesNotHaveStatusComment(w, "issue")
		require.ErrorContains(t, err, "listing comments on issue #42: network down")
	})
}
