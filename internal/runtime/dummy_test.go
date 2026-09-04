package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestLoadBehaviourScript(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "current-scenario.yaml")
	content := `ops:
  - description: Emit JSON
    op: write_fixture
    args: output/agent-result.json, fixtures/triage/sufficient.json
    content: '{"action":"sufficient"}'
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	script, err := LoadBehaviourScript(path)
	require.NoError(t, err)
	require.Len(t, script.Ops, 1)
	assert.Equal(t, "Emit JSON", script.Ops[0].Description)
	assert.Equal(t, "write_fixture", script.Ops[0].Op)
	assert.Contains(t, script.Ops[0].Content, "sufficient")
}

func TestResolveWriteFixtureEmbeddedContent(t *testing.T) {
	t.Parallel()

	dest, content, err := resolveWriteFixture(BehaviourOperation{
		Op:      "write_fixture",
		Args:    "output/agent-result.json, fixtures/triage/sufficient.json",
		Content: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "output/agent-result.json", dest)
	assert.Equal(t, "hello", content)
}

func TestResolveWriteFixtureMissingContent(t *testing.T) {
	t.Parallel()

	_, _, err := resolveWriteFixture(BehaviourOperation{
		Op:   "write_fixture",
		Args: "output/agent-result.json, fixtures/triage/sufficient.json",
	})
	require.Error(t, err)
}

func TestResolveSandboxPathWithinBase(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.MkdirAll(base, 0o755))

	got, err := resolveSandboxPath(base, "output/file.json")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "output/file.json"), got)
}

func TestResolveSandboxPathRejectsEscape(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.MkdirAll(base, 0o755))

	_, err := resolveSandboxPath(base, "../outside")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes base")
}

func TestResolveSandboxPathRejectsAbsoluteOutsideWorkspace(t *testing.T) {
	t.Parallel()

	_, err := resolveSandboxPath(sandbox.SandboxWorkspace, "/etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes sandbox workspace")
}

func TestExecuteBehaviourOpUnknown(t *testing.T) {
	t.Parallel()

	err := executeBehaviourOp(DummyRuntime{}, "unused", t.TempDir(), BehaviourOperation{Op: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown op")
}

func TestLoadBehaviourScript_MissingFile(t *testing.T) {
	t.Parallel()

	_, err := LoadBehaviourScript(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading behaviour script")
}

func TestLoadBehaviourScript_InvalidYAML(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(":\n- bad"), 0o644))

	_, err := LoadBehaviourScript(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing behaviour script")
}

func TestLoadBehaviourScript_EmptyOps(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "empty.yaml")
	require.NoError(t, os.WriteFile(path, []byte("ops: []\n"), 0o644))

	_, err := LoadBehaviourScript(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no operations")
}

func TestResolveWriteFixture_BadArgs(t *testing.T) {
	t.Parallel()

	_, _, err := resolveWriteFixture(BehaviourOperation{Op: "write_fixture", Args: "onlyone"})
	require.Error(t, err)
}

func TestResolveWriteFixture_EmptyDest(t *testing.T) {
	t.Parallel()

	_, _, err := resolveWriteFixture(BehaviourOperation{
		Op:   "write_fixture",
		Args: " ,fixtures/triage/sufficient.json",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dest_path")
}

func TestShellQuote(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "'hello'", shellQuote("hello"))
	assert.Equal(t, "'don'\\''t'", shellQuote("don't"))
}

func TestDummyRuntimeMetadata(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{}
	assert.Equal(t, "dummy", rt.Name())
	assert.Equal(t, "fullsend.dummy", rt.System())
	assert.Contains(t, rt.ConfigDir(), ".dummy")
	assert.Equal(t, sandbox.SandboxWorkspace, rt.WorkspaceDir())
	assert.Nil(t, rt.EnvExports())
}

func TestDummyRuntimeNoopMethods(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{}
	assert.NoError(t, rt.ExtractTranscripts("", "", ""))
	assert.NoError(t, rt.ExtractDebugLog("", "", ""))
	assert.Nil(t, rt.ParseTranscriptErrors(""))

	var buf bytes.Buffer
	rt.EmitTranscriptErrors(&buf, nil)
}

func TestDummyRuntime_ParseTranscriptFile(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{}
	_, ok := rt.ParseTranscriptFile("/nonexistent/path.jsonl")
	assert.False(t, ok)
}

func TestExecuteBehaviourScript_ValidationFailures(t *testing.T) {
	t.Parallel()

	script := &BehaviourScript{Ops: []BehaviourOperation{
		{Op: "read_file", Description: "missing path"},
		{Op: "url_get", Description: "missing url"},
	}}
	results, err := executeBehaviourScript(context.Background(), DummyRuntime{}, "unused", t.TempDir(), script)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires a path")
	require.Len(t, results.Operations, 2)
	assert.False(t, results.Operations[0].Success)
	assert.Contains(t, results.Operations[0].Error, "requires a path")
	assert.False(t, results.Operations[1].Success)
	assert.Contains(t, results.Operations[1].Error, "requires a URL")
}

func TestExecuteBehaviourOp_ReadFileEmptyPath(t *testing.T) {
	t.Parallel()

	err := executeBehaviourOp(DummyRuntime{}, "unused", t.TempDir(), BehaviourOperation{Op: "read_file"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a path")
}

func TestExecuteBehaviourOp_URLGetEmpty(t *testing.T) {
	t.Parallel()

	err := executeBehaviourOp(DummyRuntime{}, "unused", t.TempDir(), BehaviourOperation{Op: "url_get"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a URL")
}

func TestResolveSandboxPath_AbsoluteWithinWorkspace(t *testing.T) {
	t.Parallel()

	ws := sandbox.SandboxWorkspace
	rel := filepath.Join(ws, "output", "file.json")
	got, err := resolveSandboxPath(ws, rel)
	require.NoError(t, err)
	assert.Equal(t, rel, got)
}

type stubBootstrapInput struct {
	sandboxName string
}

func (s stubBootstrapInput) SandboxName() string  { return s.sandboxName }
func (s stubBootstrapInput) AgentPath() string    { return "" }
func (s stubBootstrapInput) AgentName() string    { return "test" }
func (s stubBootstrapInput) SkillDirs() []string  { return nil }
func (s stubBootstrapInput) PluginDirs() []string { return nil }

func TestDummyRuntime_Bootstrap(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{}
	err := rt.Bootstrap(stubBootstrapInput{sandboxName: "nonexistent-sandbox"})
	require.Error(t, err)
}

func TestDummyRuntime_Bootstrap_NonZeroExit(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
		return "", "sandbox not found", 1, nil
	}}
	err := rt.Bootstrap(stubBootstrapInput{sandboxName: "nonexistent"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox not found")
}

func TestDummyRuntime_RunMissingScript(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{}
	exit, err := rt.Run(context.Background(), RunParams{
		FullsendDir: t.TempDir(),
		SandboxName: "unused",
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "behaviour script")
}

func TestDummyRuntime_ClearIterationArtifacts(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{}
	err := rt.ClearIterationArtifacts("nonexistent-sandbox")
	require.Error(t, err)
}

func TestDummyRuntime_ClearIterationArtifacts_NonZeroExit(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "rm -rf") {
			return "", "sandbox not found", 1, nil
		}
		// clearStrayProcesses call succeeds
		return "stray processes killed: 0\n", "", 0, nil
	}}
	err := rt.ClearIterationArtifacts("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox not found")
}

func TestExecuteBehaviourOp_ReadFileExecFailure(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "f.txt"), []byte("x"), 0o644))
	err := executeBehaviourOp(DummyRuntime{}, "nonexistent-sandbox", base, BehaviourOperation{
		Op:   "read_file",
		Args: "f.txt",
	})
	require.Error(t, err)
}

func TestExecuteBehaviourOp_URLGetExecFailure(t *testing.T) {
	t.Parallel()

	err := executeBehaviourOp(DummyRuntime{}, "nonexistent-sandbox", t.TempDir(), BehaviourOperation{
		Op:   "url_get",
		Args: "https://example.com",
	})
	require.Error(t, err)
}

func TestExecuteBehaviourOp_WriteFixtureInvalidArgs(t *testing.T) {
	t.Parallel()

	err := executeBehaviourOp(DummyRuntime{}, "nonexistent-sandbox", t.TempDir(), BehaviourOperation{
		Op:   "write_fixture",
		Args: "only-one-part",
	})
	require.Error(t, err)
}

func TestWriteBehaviourResults(t *testing.T) {
	t.Parallel()

	err := DummyRuntime{}.writeBehaviourResults("nonexistent-sandbox", BehaviourResults{
		Operations: []BehaviourOpResult{{Success: true, Description: "ok"}},
	})
	require.Error(t, err)
}

func TestDummyRuntime_RunFailedOps(t *testing.T) {
	fullsendDir := t.TempDir()
	scriptDir := filepath.Join(fullsendDir, "behaviour")
	require.NoError(t, os.MkdirAll(scriptDir, 0o755))
	script := `ops:
- description: missing file
  op: read_file
  args: does-not-exist.txt
`
	require.NoError(t, os.WriteFile(filepath.Join(scriptDir, "current-scenario.yaml"), []byte(script), 0o644))

	rt := DummyRuntime{}
	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "nonexistent-sandbox",
		RepoDir:     t.TempDir(),
		FullsendDir: fullsendDir,
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
}

func TestDummyRuntime_RunOpFailureReturnsNilGoError(t *testing.T) {
	fullsendDir := t.TempDir()
	scriptDir := filepath.Join(fullsendDir, "behaviour")
	require.NoError(t, os.MkdirAll(scriptDir, 0o755))
	script := `ops:
- description: blocked fetch
  op: url_get
  args: https://example.com/blocked
`
	require.NoError(t, os.WriteFile(filepath.Join(scriptDir, "current-scenario.yaml"), []byte(script), 0o644))

	rt := DummyRuntime{WriteResultsFn: func(string, BehaviourResults) error { return nil }}
	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "nonexistent-sandbox",
		RepoDir:     t.TempDir(),
		FullsendDir: fullsendDir,
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.NoError(t, err)
}

func TestExecuteBehaviourOp_URLGetNonZeroExit(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "curl") {
			return "", "blocked by sandbox policy", 22, nil
		}
		return "", "", 0, nil
	}}

	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "url_get",
		Args: "https://www.google.com/search?q=foo",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "url_get failed")
	assert.Contains(t, err.Error(), "blocked by sandbox policy")
}

func TestExecuteBehaviourOp_ReadFileNonZeroExit(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
		return "", "missing file", 1, nil
	}}

	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "read_file",
		Args: "output/missing.json",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read_file failed")
}

func TestExecuteBehaviourOp_WriteFixtureSuccess(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn: func(_, _, _ string) error { return nil },
	}

	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:      "write_fixture",
		Args:    "output/agent-result.json, fixtures/triage/sufficient.json",
		Content: `{"action":"sufficient"}`,
	})
	require.NoError(t, err)
}

func TestWriteBehaviourResultsSuccess(t *testing.T) {
	t.Parallel()

	var uploaded bool
	rt := DummyRuntime{UploadFn: func(_, _, _ string) error {
		uploaded = true
		return nil
	}}

	err := rt.writeBehaviourResults("sandbox", BehaviourResults{
		Operations: []BehaviourOpResult{{Description: "ok", Success: true}},
	})
	require.NoError(t, err)
	assert.True(t, uploaded)
}

func TestValidateHTTPURL(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateHTTPURL("https://example.com/path"))
	require.Error(t, validateHTTPURL("file:///etc/passwd"))
	require.Error(t, validateHTTPURL(""))
	require.Error(t, validateHTTPURL("://missing"))
}

func TestExecuteBehaviourOp_AssertEnvEmpty(t *testing.T) {
	t.Parallel()

	err := executeBehaviourOp(DummyRuntime{}, "sandbox", t.TempDir(), BehaviourOperation{Op: "assert_env"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a variable name")
}

func TestExecuteBehaviourOp_AssertEnvSuccess(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
		return "", "", 0, nil
	}}
	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "assert_env",
		Args: "GITHUB_ISSUE_URL",
	})
	require.NoError(t, err)
}

func TestExecuteBehaviourOp_AssertFileSuccess(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
		return "", "", 0, nil
	}}
	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "assert_file",
		Args: ".fullsend/dispatch/event-payload.json",
	})
	require.NoError(t, err)
}

func TestExecuteBehaviourOp_AssertJSONSuccess(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "jq") {
			return "42", "", 0, nil
		}
		return "", "", 0, nil
	}}
	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "assert_json",
		Args: ".fullsend/dispatch/event-payload.json,issue.number",
	})
	require.NoError(t, err)
}

func TestExecuteBehaviourOp_AssertJSONBadArgs(t *testing.T) {
	t.Parallel()

	err := executeBehaviourOp(DummyRuntime{}, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "assert_json",
		Args: "only-path",
	})
	require.Error(t, err)
}

func TestExecuteBehaviourOp_AssertJSONInvalidPath(t *testing.T) {
	t.Parallel()

	err := executeBehaviourOp(DummyRuntime{}, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "assert_json",
		Args: ".fullsend/dispatch/event-payload.json,issue.number; rm -rf /",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid json_path")
}

func TestExecuteBehaviourOp_AssertEnvInvalidName(t *testing.T) {
	t.Parallel()

	err := executeBehaviourOp(DummyRuntime{}, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "assert_env",
		Args: "GITHUB_ISSUE_URL; rm -rf /",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid variable name")
}

func TestExecuteBehaviourScript_CancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	script := &BehaviourScript{Ops: []BehaviourOperation{
		{Op: "read_file", Args: "x.txt", Description: "read"},
	}}
	_, err := executeBehaviourScript(ctx, DummyRuntime{}, "sandbox", t.TempDir(), script)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
}

func TestExecuteBehaviourOp_CheckoutBranchSuccess(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	var gotCmd string
	rt := DummyRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		gotCmd = cmd
		return "", "", 0, nil
	}}
	err := executeBehaviourOp(rt, "sandbox", repoDir, BehaviourOperation{
		Op:   "checkout_branch",
		Args: "agent/42-fix-widget",
	})
	require.NoError(t, err)
	assert.Contains(t, gotCmd, "cd '"+repoDir+"'")
	assert.Contains(t, gotCmd, "git ls-remote --exit-code --heads origin 'agent/42-fix-widget'")
	assert.Contains(t, gotCmd, "git checkout -B 'agent/42-fix-widget' FETCH_HEAD")
	assert.Contains(t, gotCmd, "git checkout -B 'agent/42-fix-widget'; fi")
	assert.Contains(t, gotCmd, "behaviour/marker.txt")
	assert.Contains(t, gotCmd, "commit -m")
}

// runCheckoutBranchForReal executes the checkout_branch shell command with
// a real shell and git, returning the op error. The ExecFn override runs
// the command via sh -c instead of a sandbox.
func runCheckoutBranchForReal(t *testing.T, repoDir, branch string) error {
	t.Helper()
	rt := DummyRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return "", string(out), exitErr.ExitCode(), nil
			}
			return "", string(out), -1, err
		}
		return string(out), "", 0, nil
	}}
	return executeBehaviourOp(rt, "sandbox", repoDir, BehaviourOperation{
		Op:   "checkout_branch",
		Args: branch,
	})
}

// initCheckoutBranchRepos creates a bare "origin" with an initial commit
// on main plus a clone, and returns the clone path and origin path.
func initCheckoutBranchRepos(t *testing.T) (clone, origin string) {
	t.Helper()
	base := t.TempDir()
	origin = filepath.Join(base, "origin.git")
	clone = filepath.Join(base, "clone")
	seed := filepath.Join(base, "seed")

	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.invalid")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}

	run(base, "init", "--bare", "-b", "main", origin)
	run(base, "init", "-b", "main", seed)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644))
	run(seed, "add", "README.md")
	run(seed, "commit", "-m", "initial")
	run(seed, "push", origin, "main")
	// Seed a remote-only branch with one extra commit.
	run(seed, "checkout", "-b", "agent/7-existing")
	require.NoError(t, os.WriteFile(filepath.Join(seed, "extra.txt"), []byte("extra\n"), 0o644))
	run(seed, "add", "extra.txt")
	run(seed, "commit", "-m", "extra")
	run(seed, "push", origin, "agent/7-existing")
	run(base, "clone", "-b", "main", origin, clone)
	return clone, origin
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func TestExecuteBehaviourOp_CheckoutBranchRealShellExistingRemote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	clone, origin := initCheckoutBranchRepos(t)
	require.NoError(t, runCheckoutBranchForReal(t, clone, "agent/7-existing"))

	assert.Equal(t, "agent/7-existing", gitOut(t, clone, "branch", "--show-current"))
	// The branch is based on the remote tip plus exactly one marker commit.
	remoteTip := gitOut(t, origin, "rev-parse", "refs/heads/agent/7-existing")
	assert.Equal(t, remoteTip, gitOut(t, clone, "rev-parse", "HEAD~1"))
	assert.Equal(t, "test: add scripted marker commit", gitOut(t, clone, "log", "-1", "--format=%s"))
	assert.FileExists(t, filepath.Join(clone, "extra.txt"), "remote branch content is present")
	assert.FileExists(t, filepath.Join(clone, "behaviour", "marker.txt"))
}

func TestExecuteBehaviourOp_CheckoutBranchRealShellMissingRemote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	clone, _ := initCheckoutBranchRepos(t)
	mainTip := gitOut(t, clone, "rev-parse", "HEAD")
	require.NoError(t, runCheckoutBranchForReal(t, clone, "agent/7-brand-new"))

	assert.Equal(t, "agent/7-brand-new", gitOut(t, clone, "branch", "--show-current"))
	// Missing remote ref falls back to the current HEAD plus the marker.
	assert.Equal(t, mainTip, gitOut(t, clone, "rev-parse", "HEAD~1"))
	assert.FileExists(t, filepath.Join(clone, "behaviour", "marker.txt"))
}

func TestExecuteBehaviourOp_CheckoutBranchRealShellPrefersBranchOverSameNamedTag(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	clone, origin := initCheckoutBranchRepos(t)
	branchTip := gitOut(t, origin, "rev-parse", "refs/heads/agent/7-existing")

	// Tag the *initial* commit with the same name as the branch. Without
	// scoping the fetch to refs/heads/, git's default ref disambiguation
	// would resolve the tag ahead of the branch.
	seedTagCmd := exec.Command("git", "tag", "agent/7-existing", "main")
	seedTagCmd.Dir = filepath.Join(filepath.Dir(clone), "seed")
	require.NoError(t, seedTagCmd.Run())
	pushTagCmd := exec.Command("git", "push", origin, "refs/tags/agent/7-existing")
	pushTagCmd.Dir = filepath.Join(filepath.Dir(clone), "seed")
	out, err := pushTagCmd.CombinedOutput()
	require.NoError(t, err, string(out))

	require.NoError(t, runCheckoutBranchForReal(t, clone, "agent/7-existing"))
	assert.Equal(t, branchTip, gitOut(t, clone, "rev-parse", "HEAD~1"), "checkout must resolve the branch, not the same-named tag")
}

func TestExecuteBehaviourOp_CheckoutBranchRealShellRemoteError(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	clone, origin := initCheckoutBranchRepos(t)
	// Break the remote so ls-remote fails with a non-2 exit code: the op
	// must fail rather than silently branching off HEAD.
	require.NoError(t, os.RemoveAll(origin))
	err := runCheckoutBranchForReal(t, clone, "agent/7-existing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checkout_branch")
}

func TestExecuteBehaviourOp_CheckoutBranchEmpty(t *testing.T) {
	t.Parallel()

	err := executeBehaviourOp(DummyRuntime{}, "sandbox", t.TempDir(), BehaviourOperation{
		Op: "checkout_branch",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a branch name")
}

func TestExecuteBehaviourOp_CheckoutBranchInvalidName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"agent/42; rm -rf /",
		"-oProxyCommand=evil",
		"--force",
		"agent//double-slash",
		"agent/../escape",
		"has space",
		"/leading-slash",
		"trailing-slash/",
		".hidden-lead-dot",
	} {
		err := executeBehaviourOp(DummyRuntime{}, "sandbox", t.TempDir(), BehaviourOperation{
			Op:   "checkout_branch",
			Args: name,
		})
		require.Error(t, err, "branch name %q should be rejected", name)
		assert.Contains(t, err.Error(), "invalid branch name", "branch name %q", name)
	}
}

func TestExecuteBehaviourOp_CheckoutBranchNonZeroExit(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
		return "", "fatal: not a git repository", 1, nil
	}}
	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "checkout_branch",
		Args: "agent/42-fix-widget",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestExecuteBehaviourOp_CheckoutBranchExecError(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
		return "", "", 0, io.ErrUnexpectedEOF
	}}
	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "checkout_branch",
		Args: "agent/42-fix-widget",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checkout_branch exec")
}

// ClearIterationArtifacts sweeps stray processes before removing files, so
// nothing from iteration N keeps writing into what iteration N+1 reads.
func TestDummyRuntime_ClearIterationArtifacts_SweepsStraysBeforeFiles(t *testing.T) {
	t.Parallel()

	var cmds []string
	rt := DummyRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		cmds = append(cmds, cmd)
		return "stray processes killed: 0\n", "", 0, nil
	}}
	require.NoError(t, rt.ClearIterationArtifacts("sb"))
	require.Len(t, cmds, 2)
	assert.Equal(t, killStrayProcessesScript(), cmds[0])
	assert.Contains(t, cmds[1], "rm -rf")
}

// A failed sweep (exit 124 is the only exec failure sandbox.Exec reports)
// is warning-only: the file cleanup still runs and the result is nil.
func TestDummyRuntime_ClearIterationArtifacts_SweepFailureIsNotAnError(t *testing.T) {
	t.Parallel()

	var cmds []string
	rt := DummyRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		cmds = append(cmds, cmd)
		if len(cmds) == 1 {
			return "", "boom", 124, errors.New("command timed out after 15s")
		}
		return "", "", 0, nil
	}}
	require.NoError(t, rt.ClearIterationArtifacts("sb"))
	require.Len(t, cmds, 2)
	assert.Contains(t, cmds[1], "rm -rf")
}
