package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/tracker"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// The keepHistoryDeprecationWarning snippet every door must print when
// --keep-history is passed explicitly. Shell scripts never read help text,
// so this stderr line is the only advance notice external automation gets
// before a cleanup PR removes the flag.
const keepHistoryDeprecationWarning = "warning: --keep-history is deprecated and has no effect"

// The three doors that accept --keep-history (issues post-comment,
// post-comment and post-review) share warnKeepHistoryDeprecated, but its
// flag-Changed branch only runs through Cobra's flag parsing, so each door
// is driven at the command level: with the flag passed explicitly the
// warning must reach stderr, without it stderr must stay free of it.

func TestIssuesPostCommentCmd_KeepHistoryWarnsOnExplicitFlag(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", "/nonexistent")

	bodyFile := filepath.Join(t.TempDir(), "body.md")
	require.NoError(t, os.WriteFile(bodyFile, []byte("comment body"), 0o644))

	run := func(extra ...string) string {
		cmd := newIssuesPostCommentCmd()
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs(append([]string{
			"--tracker", "github",
			"--project", "owner/repo",
			"--number", "1",
			"--marker", "<!-- fullsend:test -->",
			"--result", bodyFile,
		}, extra...))
		var stderr strings.Builder
		cmd.SetErr(&stderr)
		err := cmd.Execute()
		// No token is available, so the run stops before posting; the
		// warning is printed before that, at the top of RunE.
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no GitHub token found")
		return stderr.String()
	}

	assert.Contains(t, run("--keep-history=false"), keepHistoryDeprecationWarning,
		"an explicit --keep-history must warn on stderr")
	assert.NotContains(t, run(), keepHistoryDeprecationWarning,
		"the default flag value must not warn")
}

func TestPostCommentCmd_KeepHistoryWarnsOnExplicitFlag(t *testing.T) {
	t.Setenv("FULLSEND_DIR", "")

	bodyFile := filepath.Join(t.TempDir(), "body.md")
	require.NoError(t, os.WriteFile(bodyFile, []byte("comment body"), 0o644))

	run := func(extra ...string) string {
		cmd := newPostCommentCmd()
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs(append([]string{
			"--repo", "owner/repo",
			"--number", "1",
			"--marker", "<!-- fullsend:test -->",
			"--result", bodyFile,
			"--token", "test-token",
			"--dry-run",
		}, extra...))
		var stderr strings.Builder
		cmd.SetErr(&stderr)
		require.NoError(t, cmd.Execute())
		return stderr.String()
	}

	assert.Contains(t, run("--keep-history=false"), keepHistoryDeprecationWarning,
		"an explicit --keep-history must warn on stderr")
	assert.NotContains(t, run(), keepHistoryDeprecationWarning,
		"the default flag value must not warn")
}

func TestPostReviewCmd_KeepHistoryWarnsOnExplicitFlag(t *testing.T) {
	t.Setenv("FULLSEND_DIR", "")

	resultFile := filepath.Join(t.TempDir(), "result.json")
	require.NoError(t, os.WriteFile(resultFile, []byte(`{"action":"comment","body":"ok"}`), 0o644))

	run := func(extra ...string) string {
		cmd := newPostReviewCmd()
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs(append([]string{
			"--repo", "owner/repo",
			"--pr", "1",
			"--result", resultFile,
			"--token", "test-token",
			"--dry-run",
		}, extra...))
		var stderr strings.Builder
		cmd.SetErr(&stderr)
		require.NoError(t, cmd.Execute())
		return stderr.String()
	}

	assert.Contains(t, run("--keep-history=false"), keepHistoryDeprecationWarning,
		"an explicit --keep-history must warn on stderr")
	assert.NotContains(t, run(), keepHistoryDeprecationWarning,
		"the default flag value must not warn")
}

func TestRunIssuesPostComment_KeepHistoryWarnsOnConfigPath(t *testing.T) {
	// The config route: with no explicit --keep-history, resolveKeepHistory
	// reads config.yaml through --fullsend-dir, and every door prints the
	// same shared notice when that resolution fails. The flag-path tests
	// above pin warnKeepHistoryDeprecated; this pins the config leg, so a
	// regression dropping the StepWarn (issues.go, postcomment.go and
	// postreview.go carry the same branch) fails here.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("{{{"), 0o644))

	var out bytes.Buffer
	cfg := &issuesPostCommentConfig{
		trackerName: trackerGitHub,
		project:     "acme/widgets",
		number:      42,
		marker:      "<!-- fullsend:test -->",
		fullsendDir: dir,
		testClient:  tracker.NewForgeClient(forge.NewFakeClient()),
		testPrinter: ui.New(&out),
		testBody:    "comment body",
	}
	require.NoError(t, runIssuesPostComment(context.Background(), cfg))
	assert.Contains(t, out.String(), "keep_history is deprecated and unused",
		"a config.yaml that cannot load must still print the keep_history deprecation warning")
}
