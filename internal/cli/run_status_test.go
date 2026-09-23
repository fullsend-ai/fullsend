package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
)

func TestCompletionStatus(t *testing.T) {
	t.Parallel()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	runErr := errors.New("boom")

	tests := []struct {
		name       string
		ctx        context.Context
		runErr     error
		skipped    bool
		skipReason string
		noChanges  bool
		wantStatus string
		wantDetail string
	}{
		{
			name:       "success",
			ctx:        context.Background(),
			wantStatus: statusSuccess,
		},
		{
			name:       "cancelled wins over everything",
			ctx:        cancelled,
			runErr:     runErr,
			skipped:    true,
			noChanges:  true,
			wantStatus: statusCancelled,
		},
		{
			name:       "failure wins over skip and no-changes",
			ctx:        context.Background(),
			runErr:     runErr,
			skipped:    true,
			noChanges:  true,
			wantStatus: statusFailure,
			wantDetail: "boom",
		},
		{
			name:       "skipped wins over no-changes",
			ctx:        context.Background(),
			skipped:    true,
			skipReason: "already in review",
			noChanges:  true,
			wantStatus: statusSkipped,
			wantDetail: "already in review",
		},
		{
			name:       "no changes",
			ctx:        context.Background(),
			noChanges:  true,
			wantStatus: statusNoChangesMade,
			wantDetail: noChangesMadeDetail,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotStatus, gotDetail := completionStatus(tt.ctx, tt.runErr, tt.skipped, tt.skipReason, tt.noChanges)
			assert.Equal(t, tt.wantStatus, gotStatus)
			assert.Equal(t, tt.wantDetail, gotDetail)
		})
	}
}

func TestNoChangesMadeDetailFitsStatusCap(t *testing.T) {
	t.Parallel()
	// statuscomment.maxDetailLen is 200 runes; keep this assertion in
	// lockstep so a longer message is truncated in the comment.
	assert.LessOrEqual(t, len([]rune(noChangesMadeDetail)), 200)
}

func TestResolvePreAgentHead_PrefersEnv(t *testing.T) {
	t.Setenv("PRE_AGENT_HEAD", "abc123def")
	assert.Equal(t, "abc123def", resolvePreAgentHead(nil, "/does-not-matter"))
}

func TestResolvePreAgentHead_EmptyWithoutRepo(t *testing.T) {
	t.Setenv("PRE_AGENT_HEAD", "")
	assert.Empty(t, resolvePreAgentHead(nil, ""))
}

func TestResolvePreAgentHead_FallsBackToRepoHEAD(t *testing.T) {
	t.Setenv("PRE_AGENT_HEAD", "")
	dir, sha := initGitTestRepo(t)
	assert.Equal(t, sha, resolvePreAgentHead(nil, dir))
}

func TestResolvePreAgentHead_MissingRepo(t *testing.T) {
	t.Setenv("PRE_AGENT_HEAD", "")
	assert.Empty(t, resolvePreAgentHead(nil, t.TempDir()))
}

func TestResolvePreAgentHead_WarnsOnUnexpectedError(t *testing.T) {
	t.Setenv("PRE_AGENT_HEAD", "")
	out := &strings.Builder{}
	printer := ui.New(out)
	// t.TempDir() is not a git repo, so gitRevParse fails unexpectedly
	// (as opposed to the "" input handled above without calling git at all).
	assert.Empty(t, resolvePreAgentHead(printer, t.TempDir()))
	assert.Contains(t, out.String(), "Could not resolve pre-agent HEAD")
}

func TestIsNoCommitOutcome(t *testing.T) {
	dir, sha := initGitTestRepo(t)

	t.Run("matching HEAD", func(t *testing.T) {
		assert.True(t, isNoCommitOutcome(nil, dir, sha))
	})
	t.Run("different HEAD", func(t *testing.T) {
		assert.False(t, isNoCommitOutcome(nil, dir, "0000000000000000000000000000000000000000"))
	})
	t.Run("empty preHead", func(t *testing.T) {
		assert.False(t, isNoCommitOutcome(nil, dir, ""))
	})
	t.Run("empty repoDir", func(t *testing.T) {
		assert.False(t, isNoCommitOutcome(nil, "", sha))
	})
	t.Run("not a git repo", func(t *testing.T) {
		assert.False(t, isNoCommitOutcome(nil, t.TempDir(), sha))
	})
	t.Run("new commit is not a no-op", func(t *testing.T) {
		runGitTestRepoCmd(t, dir, "commit", "--allow-empty", "-m", "second")
		assert.False(t, isNoCommitOutcome(nil, dir, sha))
	})
	t.Run("warns on unexpected rev-parse error", func(t *testing.T) {
		out := &strings.Builder{}
		printer := ui.New(out)
		assert.False(t, isNoCommitOutcome(printer, t.TempDir(), sha))
		assert.Contains(t, out.String(), "Could not resolve HEAD")
	})
}
