package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestProhibitsCoAuthoredBy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "rrasouli-style do not add",
			content: "5. **No attribution.** Do not add Co-Authored-By lines, AI signatures, or any mention of Claude.",
			want:    true,
		},
		{
			name:    "konflux never add backticks",
			content: "Never add `Co-Authored-By` to commit messages; use `Assisted-by: Claude` trailer instead",
			want:    true,
		},
		{
			name:    "do not add trailers",
			content: "Do NOT add Co-Authored-By trailers to commits.",
			want:    true,
		},
		{
			name:    "don't add",
			content: "Don't add Co-authored-by entries for any AI tool.",
			want:    true,
		},
		{
			name:    "curly apostrophe don't",
			content: "Don’t add Co-authored-by lines to commits",
			want:    true,
		},
		{
			name:    "never add a trailer",
			content: "Never add a Co-authored-by trailer.",
			want:    true,
		},
		{
			name:    "no co-authored-by",
			content: "No Co-Authored-By lines in commits.",
			want:    true,
		},
		{
			name:    "without co-authored-by",
			content: "Commit without Co-authored-by trailers.",
			want:    true,
		},
		{
			name:    "forbidden after name",
			content: "Co-authored-by trailers are forbidden in this repository.",
			want:    true,
		},
		{
			name:    "do not add any",
			content: "Do not add any Co-authored-by tags for an AI agent on the commit.",
			want:    true,
		},
		{
			name:    "requires co-authored-by",
			content: "Include a Co-authored-by trailer for pair-programming commits.",
			want:    false,
		},
		{
			name:    "mentions trailer without ban",
			content: "GitHub shows co-authors from Co-authored-by trailers in the UI.",
			want:    false,
		},
		{
			name:    "empty",
			content: "",
			want:    false,
		},
		{
			name:    "unrelated agents.md",
			content: "Always run make test. Use Conventional Commits. Do not commit secrets.",
			want:    false,
		},
		{
			name:    "assisted-by instead is still a ban on co-authored-by",
			content: "Never add Co-Authored-By; use Assisted-by: Claude instead.",
			want:    true,
		},
		{
			name:    "never merge without is a requirement, not a ban",
			content: "Never merge without Co-authored-by trailers.",
			want:    false,
		},
		{
			name:    "is not forbidden",
			content: "Co-authored-by is not forbidden here.",
			want:    false,
		},
		{
			name:    "required not forbidden",
			content: "Co-authored-by trailers are required, not forbidden.",
			want:    false,
		},
		{
			name:    "do not commit without is a requirement, not a ban",
			content: "Do not commit without Co-authored-by trailers.",
			want:    false,
		},
		{
			name:    "cannot commit without is a requirement, not a ban",
			content: "You cannot commit without Co-authored-by trailers.",
			want:    false,
		},
		{
			name:    "can't merge without is a requirement, not a ban",
			content: "Can't merge without Co-authored-by.",
			want:    false,
		},
		{
			name:    "couldn't commit without is a requirement, not a ban",
			content: "You couldn't commit without Co-authored-by trailers attached.",
			want:    false,
		},
		{
			name:    "never ever merge without is still a requirement",
			content: "Never ever merge without Co-authored-by trailers.",
			want:    false,
		},
		{
			name:    "earlier requirement does not mask a later ban",
			content: "Never merge without Co-authored-by trailers. Commit without Co-authored-by is not optional.",
			want:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, prohibitsCoAuthoredBy(tc.content))
		})
	}
}

func TestRepoProhibitsCoAuthoredBy(t *testing.T) {
	t.Parallel()

	t.Run("agents.md", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"),
			[]byte("Do not add Co-Authored-By lines to commits.\n"), 0o644))
		assert.True(t, repoProhibitsCoAuthoredBy(dir, ""))
	})

	t.Run("claude.md only", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"),
			[]byte("Never add `Co-Authored-By` to commit messages\n"), 0o644))
		assert.True(t, repoProhibitsCoAuthoredBy(dir, ""))
	})

	t.Run("dot claude.md", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.md"),
			[]byte("No Co-authored-by trailers.\n"), 0o644))
		assert.True(t, repoProhibitsCoAuthoredBy(dir, ""))
	})

	t.Run("lowercase agents.md", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "agents.md"),
			[]byte("Don't add Co-authored-by entries.\n"), 0o644))
		assert.True(t, repoProhibitsCoAuthoredBy(dir, ""))
	})

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		assert.False(t, repoProhibitsCoAuthoredBy(t.TempDir(), ""))
	})

	t.Run("agents.md without ban", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"),
			[]byte("# Agent instructions\n\nRun make test.\n"), 0o644))
		assert.False(t, repoProhibitsCoAuthoredBy(dir, ""))
	})

	t.Run("readme is ignored", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"),
			[]byte("Do not add Co-Authored-By lines.\n"), 0o644))
		assert.False(t, repoProhibitsCoAuthoredBy(dir, ""))
	})

	t.Run("org default applies when repo has no agents.md", func(t *testing.T) {
		t.Parallel()
		org := filepath.Join(t.TempDir(), "AGENTS.md")
		require.NoError(t, os.WriteFile(org,
			[]byte("Do not add Co-Authored-By lines.\n"), 0o644))
		assert.True(t, repoProhibitsCoAuthoredBy(t.TempDir(), org))
	})

	t.Run("org default ignored when repo has its own agents.md", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"),
			[]byte("# Agent instructions\n\nRun make test.\n"), 0o644))
		org := filepath.Join(t.TempDir(), "AGENTS.md")
		require.NoError(t, os.WriteFile(org,
			[]byte("Do not add Co-Authored-By lines.\n"), 0o644))
		assert.False(t, repoProhibitsCoAuthoredBy(dir, org))
	})
}

func TestFileProhibitsCoAuthoredBy_Missing(t *testing.T) {
	t.Parallel()
	assert.False(t, fileProhibitsCoAuthoredBy(filepath.Join(t.TempDir(), "nope.md")))
}

func TestApplyCoAuthorSuppress(t *testing.T) {
	t.Parallel()

	t.Run("security on", func(t *testing.T) {
		t.Parallel()
		h := &harness.Harness{Agent: "agents/test.md"}
		boot, err := newHarnessBootstrap(h, "sb", "code", "", nil, nil, "")
		require.NoError(t, err)
		got := applyCoAuthorSuppress(boot)
		hw, ok := got.(*harnessBootstrapWithHooks)
		require.True(t, ok)
		assert.True(t, hw.hooks.SuppressCoAuthoredBy())
	})

	t.Run("does not mutate original", func(t *testing.T) {
		t.Parallel()
		h := &harness.Harness{Agent: "agents/test.md"}
		boot, err := newHarnessBootstrap(h, "sb", "code", "", nil, nil, "")
		require.NoError(t, err)
		orig := boot.(*harnessBootstrapWithHooks)
		_ = applyCoAuthorSuppress(boot)
		assert.False(t, orig.hooks.SuppressCoAuthoredBy())
	})

	t.Run("security off is a no-op", func(t *testing.T) {
		t.Parallel()
		off := false
		h := &harness.Harness{
			Agent:    "agents/test.md",
			Security: &harness.SecurityConfig{Enabled: &off},
		}
		boot, err := newHarnessBootstrap(h, "sb", "code", "", nil, nil, "")
		require.NoError(t, err)
		got := applyCoAuthorSuppress(boot)
		_, hooked := got.(*harnessBootstrapWithHooks)
		assert.False(t, hooked)
		assert.Equal(t, boot, got)
	})
}

func TestPrepareCoAuthorSuppress(t *testing.T) {
	t.Parallel()

	t.Run("no ban leaves boot unchanged", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"),
			[]byte("# Agent instructions\n"), 0o644))
		h := &harness.Harness{Agent: "agents/test.md"}
		boot, err := newHarnessBootstrap(h, "sb", "code", "", nil, nil, "")
		require.NoError(t, err)
		got, suppress := prepareCoAuthorSuppress(dir, "", boot)
		assert.False(t, suppress)
		assert.Equal(t, boot, got)
		hw := got.(*harnessBootstrapWithHooks)
		assert.False(t, hw.hooks.SuppressCoAuthoredBy())
	})

	t.Run("ban sets flag and settings", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"),
			[]byte("Do not add Co-Authored-By lines.\n"), 0o644))
		h := &harness.Harness{Agent: "agents/test.md"}
		boot, err := newHarnessBootstrap(h, "sb", "code", "", nil, nil, "")
		require.NoError(t, err)
		got, suppress := prepareCoAuthorSuppress(dir, "", boot)
		assert.True(t, suppress)
		hw := got.(*harnessBootstrapWithHooks)
		assert.True(t, hw.hooks.SuppressCoAuthoredBy())
		assert.False(t, boot.(*harnessBootstrapWithHooks).hooks.SuppressCoAuthoredBy())
	})
}

func TestMaybeInstallCoAuthorStripHook(t *testing.T) {
	t.Parallel()

	t.Run("skips when not suppressing", func(t *testing.T) {
		t.Parallel()
		called := false
		mockExec := func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			called = true
			return "", "", 0, nil
		}
		maybeInstallCoAuthorStripHook("sb", "/repo", false, ui.New(io.Discard), mockExec)
		assert.False(t, called)
	})

	t.Run("installs when suppressing", func(t *testing.T) {
		t.Parallel()
		var cmds []string
		mockExec := func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
			cmds = append(cmds, cmd)
			return "", "", 0, nil
		}
		maybeInstallCoAuthorStripHook("sb", "/repo", true, ui.New(io.Discard), mockExec)
		require.Len(t, cmds, 1)
		assert.Contains(t, cmds[0], "commit-msg")
	})

	t.Run("warns on install failure", func(t *testing.T) {
		t.Parallel()
		mockExec := func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "denied", 1, nil
		}
		maybeInstallCoAuthorStripHook("sb", "/repo", true, ui.New(io.Discard), mockExec)
	})
}

func TestDoInstallCoAuthorStripHook_Success(t *testing.T) {
	t.Parallel()
	var cmds []string
	mockExec := func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		cmds = append(cmds, cmd)
		return "", "", 0, nil
	}
	printer := ui.New(io.Discard)
	require.NoError(t, doInstallCoAuthorStripHook("sb", "/sandbox/workspace/repo", printer, mockExec))
	require.Len(t, cmds, 1)
	assert.Contains(t, cmds[0], shellQuote("/sandbox/workspace/repo/.git/hooks"))
	assert.Contains(t, cmds[0], shellQuote("/sandbox/workspace/repo/.git/hooks/commit-msg"))
	assert.Contains(t, cmds[0], "chmod 755")
	assert.Contains(t, cmds[0], "<< 'FULLSEND_COAUTHOR_HOOK'")
	assert.Contains(t, cmds[0], coAuthorStripHookScript,
		"hook body must be written via a quoted heredoc so $1 is not expanded")
}

func TestDoInstallCoAuthorStripHook_WritesScript(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mockExec := func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
		if err != nil {
			return "", string(out), 1, err
		}
		return string(out), "", 0, nil
	}
	require.NoError(t, doInstallCoAuthorStripHook("sb", dir, ui.New(io.Discard), mockExec))
	got, err := os.ReadFile(filepath.Join(dir, ".git/hooks/commit-msg"))
	require.NoError(t, err)
	assert.Equal(t, coAuthorStripHookScript, string(got))
}

func TestDoInstallCoAuthorStripHook_ExecError(t *testing.T) {
	t.Parallel()
	mockExec := func(_ string, _ string, _ time.Duration) (string, string, int, error) {
		return "", "boom", 1, fmt.Errorf("exec failed")
	}
	err := doInstallCoAuthorStripHook("sb", "/sandbox/workspace/repo", ui.New(io.Discard), mockExec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "installing Co-authored-by strip hook")
}

func TestDoInstallCoAuthorStripHook_NonzeroExit(t *testing.T) {
	t.Parallel()
	mockExec := func(_ string, _ string, _ time.Duration) (string, string, int, error) {
		return "", "mkdir: read-only", 1, nil
	}
	err := doInstallCoAuthorStripHook("sb", "/sandbox/workspace/repo", ui.New(io.Discard), mockExec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 1")
	assert.Contains(t, err.Error(), "read-only")
}

func TestCoAuthorStripHookScript(t *testing.T) {
	t.Parallel()
	hook := filepath.Join(t.TempDir(), "commit-msg")
	require.NoError(t, os.WriteFile(hook, []byte(coAuthorStripHookScript), 0o755))

	t.Run("strips trailer keeps body and other trailers", func(t *testing.T) {
		t.Parallel()
		msg := filepath.Join(t.TempDir(), "MSG")
		original := "fix: dashboard\n\nSwitch the reporter.\n\nCo-authored-by: Claude <noreply@anthropic.com>\nAssisted-by: Claude\nCloses #14\n"
		require.NoError(t, os.WriteFile(msg, []byte(original), 0o644))
		cmd := exec.Command("sh", hook, msg)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		got, err := os.ReadFile(msg)
		require.NoError(t, err)
		body := string(got)
		assert.Contains(t, body, "fix: dashboard")
		assert.Contains(t, body, "Switch the reporter.")
		assert.Contains(t, body, "Assisted-by: Claude")
		assert.Contains(t, body, "Closes #14")
		assert.NotContains(t, body, "Co-authored-by")
		assert.NotContains(t, body, "noreply@anthropic.com")
	})

	t.Run("case insensitive", func(t *testing.T) {
		t.Parallel()
		msg := filepath.Join(t.TempDir(), "MSG")
		require.NoError(t, os.WriteFile(msg, []byte("feat: x\n\nCo-Authored-By: fullsend-code <bot@example.com>\n"), 0o644))
		require.NoError(t, exec.Command("sh", hook, msg).Run())
		got, err := os.ReadFile(msg)
		require.NoError(t, err)
		assert.NotContains(t, string(got), "Co-Authored-By")
		assert.Contains(t, string(got), "feat: x")
	})

	t.Run("no trailer is unchanged", func(t *testing.T) {
		t.Parallel()
		msg := filepath.Join(t.TempDir(), "MSG")
		original := "feat: x\n\nbody line\n"
		require.NoError(t, os.WriteFile(msg, []byte(original), 0o644))
		require.NoError(t, exec.Command("sh", hook, msg).Run())
		got, err := os.ReadFile(msg)
		require.NoError(t, err)
		assert.Equal(t, original, string(got))
	})

	t.Run("missing file is success", func(t *testing.T) {
		t.Parallel()
		cmd := exec.Command("sh", hook, filepath.Join(t.TempDir(), "missing"))
		require.NoError(t, cmd.Run())
	})

	t.Run("empty argv is success", func(t *testing.T) {
		t.Parallel()
		cmd := exec.Command("sh", hook)
		require.NoError(t, cmd.Run())
	})
}

func TestCoAuthorStripHookScript_EndsWithNewline(t *testing.T) {
	t.Parallel()
	assert.True(t, strings.HasSuffix(coAuthorStripHookScript, "\n"))
}
