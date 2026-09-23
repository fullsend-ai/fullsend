package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Equal(t, "abc123def", resolvePreAgentHead("/does-not-matter"))
}

func TestResolvePreAgentHead_EmptyWithoutRepo(t *testing.T) {
	t.Setenv("PRE_AGENT_HEAD", "")
	assert.Empty(t, resolvePreAgentHead(""))
}

func TestResolvePreAgentHead_FallsBackToRepoHEAD(t *testing.T) {
	t.Setenv("PRE_AGENT_HEAD", "")
	dir, sha := initTestRepo(t)
	assert.Equal(t, sha, resolvePreAgentHead(dir))
}

func TestResolvePreAgentHead_MissingRepo(t *testing.T) {
	t.Setenv("PRE_AGENT_HEAD", "")
	assert.Empty(t, resolvePreAgentHead(t.TempDir()))
}

func TestNoCommitOutcome(t *testing.T) {
	dir, sha := initTestRepo(t)

	t.Run("matching HEAD", func(t *testing.T) {
		assert.True(t, noCommitOutcome(dir, sha))
	})
	t.Run("different HEAD", func(t *testing.T) {
		assert.False(t, noCommitOutcome(dir, "0000000000000000000000000000000000000000"))
	})
	t.Run("empty preHead", func(t *testing.T) {
		assert.False(t, noCommitOutcome(dir, ""))
	})
	t.Run("empty repoDir", func(t *testing.T) {
		assert.False(t, noCommitOutcome("", sha))
	})
	t.Run("not a git repo", func(t *testing.T) {
		assert.False(t, noCommitOutcome(t.TempDir(), sha))
	})
	t.Run("new commit is not a no-op", func(t *testing.T) {
		runGit(t, dir, "commit", "--allow-empty", "-m", "second")
		assert.False(t, noCommitOutcome(dir, sha))
	})
}

func initTestRepo(t *testing.T) (dir, head string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644))
	runGit(t, dir, "add", "README")
	runGit(t, dir, "commit", "-m", "init")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	return dir, strings.TrimSpace(string(out))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign",
		"GIT_CONFIG_VALUE_0=false",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
