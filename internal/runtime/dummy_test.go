package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func (s stubBootstrapInput) SandboxName() string                { return s.sandboxName }
func (s stubBootstrapInput) AgentPath() string                  { return "" }
func (s stubBootstrapInput) AgentName() string                  { return "test" }
func (s stubBootstrapInput) SkillDirs() []string                { return nil }
func (s stubBootstrapInput) Plugins() []PluginInput             { return nil }
func (s stubBootstrapInput) ModelAliases() map[string]string    { return nil }
func (s stubBootstrapInput) AgentSubagents() map[string]*string { return nil }
func (s stubBootstrapInput) ParentModel() string                { return "" }
func (s stubBootstrapInput) OpenAIProviderAttached() bool       { return false }

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

	require.NoError(t, validateHTTPURL("url_get", "https://example.com/path"))
	require.Error(t, validateHTTPURL("url_get", "file:///etc/passwd"))
	require.Error(t, validateHTTPURL("url_get", ""))
	require.Error(t, validateHTTPURL("url_get", "://missing"))
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

// runGit runs git in dir without inheriting host signing or editor config.
// Seed commits and tags must not depend on the caller's git configuration.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// initCheckoutBranchRepos creates a bare "origin" with an initial commit
// on main plus a clone, and returns the clone path and origin path.
func initCheckoutBranchRepos(t *testing.T) (clone, origin string) {
	t.Helper()
	base := t.TempDir()
	origin = filepath.Join(base, "origin.git")
	clone = filepath.Join(base, "clone")
	seed := filepath.Join(base, "seed")

	runGit(t, base, "init", "--bare", "-b", "main", origin)
	runGit(t, base, "init", "-b", "main", seed)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644))
	runGit(t, seed, "add", "README.md")
	runGit(t, seed, "commit", "-m", "initial")
	runGit(t, seed, "push", origin, "main")
	// Seed a remote-only branch with one extra commit.
	runGit(t, seed, "checkout", "-b", "agent/7-existing")
	require.NoError(t, os.WriteFile(filepath.Join(seed, "extra.txt"), []byte("extra\n"), 0o644))
	runGit(t, seed, "add", "extra.txt")
	runGit(t, seed, "commit", "-m", "extra")
	runGit(t, seed, "push", origin, "agent/7-existing")
	runGit(t, base, "clone", "-b", "main", origin, clone)
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
	// would resolve the tag ahead of the branch. Route through runGit so
	// host tag.gpgsign / editor config cannot turn this into a signed
	// annotated tag that requires a message.
	seed := filepath.Join(filepath.Dir(clone), "seed")
	runGit(t, seed, "tag", "agent/7-existing", "main")
	runGit(t, seed, "push", origin, "refs/tags/agent/7-existing")

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

func TestParseHTTPProbeArgs(t *testing.T) {
	t.Parallel()

	p, err := ParseHTTPProbeArgs(`POST https://gw.example/v1/chat/completions INFERENCE_GATEWAY_API_KEY {"model": "echo", "x": 1}`)
	require.NoError(t, err)
	assert.Equal(t, "POST", p.Method)
	assert.Equal(t, "https://gw.example/v1/chat/completions", p.URL)
	assert.Equal(t, "INFERENCE_GATEWAY_API_KEY", p.HeaderEnv)
	assert.Equal(t, `{"model": "echo", "x": 1}`, p.Body)

	p, err = ParseHTTPProbeArgs("GET http://gw.example/v1/models INFERENCE_GATEWAY_API_KEY")
	require.NoError(t, err)
	assert.Empty(t, p.Body)

	for _, bad := range []string{
		"",
		"GET http://gw.example/",
		"DELETE http://gw.example/ OPENAI_API_KEY",
		"GET ftp://gw.example/ OPENAI_API_KEY",
		"GET http://gw.example/ BAD-NAME",
		"GET http://gw.example/ $(id)",
		"GET http://gw.example/ GITHUB_TOKEN",
		"GET http://gw.example/ OPENAI_API_KEY body-on-get",
	} {
		_, err := ParseHTTPProbeArgs(bad)
		assert.Error(t, err, bad)
	}
}

func TestHTTPProbeHeaderEnvAllowlist(t *testing.T) {
	t.Parallel()

	for _, name := range httpProbeHeaderEnvs {
		_, err := ParseHTTPProbeArgs("GET https://gw.example/v1/models " + name)
		require.NoError(t, err, name)
	}
	_, err := ParseHTTPProbeArgs("GET https://gw.example/v1/models GITHUB_TOKEN")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `http_probe header_env "GITHUB_TOKEN" is not allowed`)
}

func TestValidateHTTPURLNamesOp(t *testing.T) {
	t.Parallel()

	for _, op := range []string{"url_get", "http_probe"} {
		err := validateHTTPURL(op, "ftp://gw.example/")
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), op+" requires http or https"), err.Error())
		err = validateHTTPURL(op, "https://")
		require.Error(t, err)
		assert.Equal(t, op+" requires a host", err.Error())
	}
	_, err := ParseHTTPProbeArgs("GET ftp://gw.example/ OPENAI_API_KEY")
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "http_probe requires http or https"), err.Error())
}

func TestHTTPProbeFromOpPrefersFields(t *testing.T) {
	t.Parallel()

	p, err := httpProbeFromOp(BehaviourOperation{Op: "http_probe", Args: "ignored", Method: "POST", URL: "https://gw.example/x", HeaderEnv: "INFERENCE_GATEWAY_API_KEY", Body: "{}"})
	require.NoError(t, err)
	assert.Equal(t, HTTPProbe{Method: "POST", URL: "https://gw.example/x", HeaderEnv: "INFERENCE_GATEWAY_API_KEY", Body: "{}"}, p)

	_, err = httpProbeFromOp(BehaviourOperation{Op: "http_probe", Method: "PUT", URL: "https://gw.example/x", HeaderEnv: "INFERENCE_GATEWAY_API_KEY"})
	assert.Error(t, err)
}

func TestHTTPProbeCommandNeverInterpolatesValues(t *testing.T) {
	t.Parallel()

	cmd, err := httpProbeCommand(HTTPProbe{Method: "POST", URL: "https://gw.example/'; rm -rf /", HeaderEnv: "INFERENCE_GATEWAY_API_KEY", Body: `'$(id)'`})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(cmd, "NODE_USE_ENV_PROXY=1 node -e "))
	assert.NotContains(t, cmd, "rm -rf")
	assert.NotContains(t, cmd, "$(id)")
}

func TestExecuteBehaviourScript_HTTPProbeRecordsStatusAndBody(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("a", httpProbeBodyLimit+100)
	responses := []string{
		`{"status":200,"body":"{\"authorization\":\"Bearer stub\"}"}`,
		`{"status":403,"body":"` + big + `"}`,
		`{"error":"connect ECONNREFUSED"}`,
		`not json`,
	}
	call := 0
	rt := DummyRuntime{ExecFn: func(_, cmd string, _ time.Duration) (string, string, int, error) {
		assert.Contains(t, cmd, "node -e ")
		out := responses[call]
		call++
		code := 0
		if strings.Contains(out, "error") {
			code = 2
		}
		return "noise\n" + out + "\n", "stderr text", code, nil
	}}
	script := &BehaviourScript{Ops: []BehaviourOperation{
		{Description: "ok", Op: "http_probe", Args: `POST https://gw.example/v1/chat/completions INFERENCE_GATEWAY_API_KEY {"model":"echo"}`},
		{Description: "denied", Op: "http_probe", Method: "POST", URL: "https://gw.example/v1/chat/completions", HeaderEnv: "INFERENCE_GATEWAY_API_KEY"},
		{Description: "refused", Op: "http_probe", Args: "GET https://other.example/ INFERENCE_GATEWAY_API_KEY"},
		{Description: "garbled", Op: "http_probe", Args: "GET https://other.example/ INFERENCE_GATEWAY_API_KEY"},
		{Description: "invalid", Op: "http_probe", Args: "PUT https://other.example/ INFERENCE_GATEWAY_API_KEY"},
	}}
	results, err := executeBehaviourScript(context.Background(), rt, "sb", "/sandbox/workspace/repo", script)
	require.Error(t, err)
	require.Len(t, results.Operations, 5)
	assert.Equal(t, 4, call, "invalid op must not reach the sandbox")

	assert.True(t, results.Operations[0].Success)
	assert.Equal(t, 200, results.Operations[0].HTTPStatus)
	assert.Contains(t, results.Operations[0].ResponseBody, "Bearer stub")

	assert.False(t, results.Operations[1].Success)
	assert.Equal(t, 403, results.Operations[1].HTTPStatus)
	assert.Len(t, results.Operations[1].ResponseBody, httpProbeBodyLimit)
	assert.Contains(t, results.Operations[1].Error, "HTTP 403")

	assert.False(t, results.Operations[2].Success)
	assert.Zero(t, results.Operations[2].HTTPStatus)
	assert.Contains(t, results.Operations[2].Error, "ECONNREFUSED")

	assert.False(t, results.Operations[3].Success)
	assert.Contains(t, results.Operations[3].Error, "unreadable output")

	assert.False(t, results.Operations[4].Success)
	assert.Contains(t, results.Operations[4].Error, "GET or POST")
	for _, res := range results.Operations {
		assert.False(t, res.BodyHadJWT, res.Description)
	}
}

// TestExecuteHTTPProbe_RedactsJWT checks the runner-side redaction: a
// JWT-shaped substring in the sandbox's output never reaches the recorded
// body, and the flag records that it was there.
func TestExecuteHTTPProbe_RedactsJWT(t *testing.T) {
	t.Parallel()

	jwt := strings.Join([]string{"eyJhbGciOiJSUzI1NiJ9", "eyJzdWIiOiJyZXBvIn0", "c2ln"}, ".")
	for _, tc := range []struct {
		name   string
		stdout string
		hadJWT bool
	}{
		{"redacted here", `{"status":200,"body":"token=` + jwt + ` end"}`, true},
		{"flagged by the sandbox", `{"status":200,"body":"token=<redacted-jwt> end","body_had_jwt":true}`, true},
		{"no jwt", `{"status":200,"body":"eyJ alone is not a token"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := DummyRuntime{ExecFn: func(_, _ string, _ time.Duration) (string, string, int, error) {
				return tc.stdout + "\n", "", 0, nil
			}}
			status, body, hadJWT, err := executeHTTPProbe(rt, "sb", BehaviourOperation{Op: "http_probe", Args: "GET https://gw.example/v1/models INFERENCE_GATEWAY_API_KEY"})
			require.NoError(t, err)
			assert.Equal(t, 200, status)
			assert.Equal(t, tc.hadJWT, hadJWT)
			assert.NotContains(t, body, jwt)
			if tc.hadJWT {
				assert.Equal(t, "token="+httpProbeJWTRedaction+" end", body)
			}
		})
	}
}

func TestExecuteHTTPProbe_ExecError(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{ExecFn: func(_, _ string, _ time.Duration) (string, string, int, error) {
		return "", "", 0, errors.New("boom")
	}}
	_, _, _, err := executeHTTPProbe(rt, "sb", BehaviourOperation{Op: "http_probe", Args: "GET https://gw.example/ INFERENCE_GATEWAY_API_KEY"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http_probe exec")
}

// TestExecuteHTTPProbe_RealNode runs the generated command through a real
// shell and node against a local server, proving the fixed script reads
// its argument, sends the bearer from the named variable and records the
// status and capped body.
func TestExecuteHTTPProbe_RealNode(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("content-type", "application/json")
		if r.URL.Path == "/denied" {
			w.WriteHeader(http.StatusForbidden)
		}
		switch r.URL.Path {
		case "/jwt":
			// A JWT in the body and another cut off at the read limit.
			pad := strings.Repeat("p", httpProbeBodyLimit-len(`{"t":"`+realNodeJWT+`","pad":"`)-10)
			_, _ = w.Write([]byte(`{"t":"` + realNodeJWT + `","pad":"` + pad + `",` + realNodeJWT + `}`))
			return
		case "/endless":
			// Far more than the limit: the probe must stop reading.
			chunk := []byte(strings.Repeat("e", 64<<10))
			for range 256 {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"method":        r.Method,
			"authorization": r.Header.Get("Authorization"),
			"content_type":  r.Header.Get("Content-Type"),
			"body":          string(body),
			"pad":           strings.Repeat("p", 5000),
		})
	}))
	defer srv.Close()

	var env []string
	for _, kv := range os.Environ() {
		k := strings.ToUpper(strings.SplitN(kv, "=", 2)[0])
		if strings.HasSuffix(k, "_PROXY") || k == "INFERENCE_GATEWAY_API_KEY" || k == "OPENAI_API_KEY" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "INFERENCE_GATEWAY_API_KEY=placeholder-value")
	rt := DummyRuntime{ExecFn: func(_, cmd string, _ time.Duration) (string, string, int, error) {
		c := exec.Command("sh", "-c", cmd)
		c.Env = env
		var stdout, stderr bytes.Buffer
		c.Stdout, c.Stderr = &stdout, &stderr
		err := c.Run()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code, err = exitErr.ExitCode(), nil
		}
		return stdout.String(), stderr.String(), code, err
	}}

	status, body, hadJWT, err := executeHTTPProbe(rt, "sb", BehaviourOperation{Op: "http_probe", Args: "POST " + srv.URL + `/ok INFERENCE_GATEWAY_API_KEY {"model": "echo"}`})
	require.NoError(t, err)
	assert.Equal(t, 200, status)
	assert.Len(t, body, httpProbeBodyLimit)
	assert.Contains(t, body, `"authorization":"Bearer placeholder-value"`)
	assert.Contains(t, body, `"content_type":"application/json"`)
	assert.Contains(t, body, `"method":"POST"`)
	assert.False(t, hadJWT)

	status, _, _, err = executeHTTPProbe(rt, "sb", BehaviourOperation{Op: "http_probe", Args: "GET " + srv.URL + "/denied INFERENCE_GATEWAY_API_KEY"})
	require.Error(t, err)
	assert.Equal(t, 403, status)

	_, _, _, err = executeHTTPProbe(rt, "sb", BehaviourOperation{Op: "http_probe", Args: "GET " + srv.URL + "/ok OPENAI_API_KEY"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_API_KEY is unset")

	// JWT-shaped substrings are redacted in the sandbox, including one cut
	// off at the read limit.
	status, body, hadJWT, err = executeHTTPProbe(rt, "sb", BehaviourOperation{Op: "http_probe", Args: "GET " + srv.URL + "/jwt INFERENCE_GATEWAY_API_KEY"})
	require.NoError(t, err)
	assert.Equal(t, 200, status)
	assert.True(t, hadJWT)
	assert.NotContains(t, body, "eyJ")
	assert.True(t, strings.HasPrefix(body, `{"t":"`+httpProbeJWTRedaction+`"`), body[:40])
	assert.True(t, strings.HasSuffix(body, httpProbeJWTRedaction), "the cut-off JWT is redacted too")

	// An endless reply is read only up to the limit.
	status, body, _, err = executeHTTPProbe(rt, "sb", BehaviourOperation{Op: "http_probe", Args: "GET " + srv.URL + "/endless INFERENCE_GATEWAY_API_KEY"})
	require.NoError(t, err)
	assert.Equal(t, 200, status)
	assert.Len(t, body, httpProbeBodyLimit)
}

// realNodeJWT is a JWT-shaped value the RealNode test server echoes. It is
// joined at run time so secret scanners do not flag a fake token in source.
var realNodeJWT = strings.Join([]string{"eyJhbGciOiJSUzI1NiJ9", "eyJzdWIiOiJyZXBvOmFjbWUvd2lkZ2V0In0", "c2lnbmF0dXJl"}, ".")
