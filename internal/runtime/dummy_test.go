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
func (s stubBootstrapInput) Effort() string                     { return "" }
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

type skillsBootstrapInput struct {
	stubBootstrapInput
	skillDirs []string
}

func (s skillsBootstrapInput) SkillDirs() []string { return s.skillDirs }

func TestDummyRuntime_Bootstrap_UploadsSkills(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	review := filepath.Join(root, "pr-review")
	lint := filepath.Join(root, "lint")
	require.NoError(t, os.MkdirAll(review, 0o755))
	require.NoError(t, os.MkdirAll(lint, 0o755))

	var mkdir string
	type upload struct{ local, remote string }
	var uploads []upload
	rt := DummyRuntime{
		ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
			mkdir = cmd
			return "", "", 0, nil
		},
		UploadFn: func(_ string, local, remote string) error {
			uploads = append(uploads, upload{local, remote})
			return nil
		},
	}
	err := rt.Bootstrap(skillsBootstrapInput{
		stubBootstrapInput: stubBootstrapInput{sandboxName: "sb"},
		skillDirs:          []string{review, lint},
	})
	require.NoError(t, err)
	assert.Contains(t, mkdir, rt.ConfigDir()+"/skills")
	assert.Equal(t, []upload{
		{review, rt.ConfigDir() + "/skills/pr-review"},
		{lint, rt.ConfigDir() + "/skills/lint"},
	}, uploads)
}

func TestDummyRuntime_Bootstrap_SkillUploadError(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn: func(_ string, _, _ string) error {
			return errors.New("upload refused")
		},
	}
	err := rt.Bootstrap(skillsBootstrapInput{
		stubBootstrapInput: stubBootstrapInput{sandboxName: "sb"},
		skillDirs:          []string{filepath.Join(t.TempDir(), "pr-review")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload refused")
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

func TestDummyRuntime_RunExpectedFailureExitsZero(t *testing.T) {
	fullsendDir := t.TempDir()
	scriptDir := filepath.Join(fullsendDir, "behaviour")
	require.NoError(t, os.MkdirAll(scriptDir, 0o755))
	script := `ops:
- description: blocked fetch
  op: url_get
  args: https://example.com/blocked
  expect_fail: true
`
	require.NoError(t, os.WriteFile(filepath.Join(scriptDir, "current-scenario.yaml"), []byte(script), 0o644))

	var got BehaviourResults
	rt := DummyRuntime{WriteResultsFn: func(_ string, r BehaviourResults) error { got = r; return nil }}
	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "nonexistent-sandbox",
		RepoDir:     t.TempDir(),
		FullsendDir: fullsendDir,
	}, ui.New(io.Discard), time.Now(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	require.Len(t, got.Operations, 1)
	assert.False(t, got.Operations[0].Success, "the failure is still recorded")
}

func TestBehaviourExitCode(t *testing.T) {
	t.Parallel()
	script := &BehaviourScript{Ops: []BehaviourOperation{{Op: "url_get", ExpectFail: true}, {Op: "assert_env"}}}
	tests := []struct {
		name    string
		results []BehaviourOpResult
		want    int
	}{
		{"all pass", []BehaviourOpResult{{Success: true}, {Success: true}}, 0},
		{"expected failure only", []BehaviourOpResult{{Success: false}, {Success: true}}, 0},
		{"unexpected failure", []BehaviourOpResult{{Success: true}, {Success: false}}, 1},
		{"both fail", []BehaviourOpResult{{Success: false}, {Success: false}}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, behaviourExitCode(script, BehaviourResults{Operations: tt.results}))
		})
	}
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

// fakeHostGit records runner-side git calls and answers each by its git
// subcommand (args[0]); a subcommand without an entry succeeds.
type fakeHostGit struct {
	calls   [][]string
	results map[string]fakeGitResult
}

type fakeGitResult struct {
	stderr   string
	exitCode int
	err      error
}

func (f *fakeHostGit) run(_ context.Context, _ string, args ...string) (string, string, int, error) {
	f.calls = append(f.calls, args)
	r := f.results[args[0]]
	return "", r.stderr, r.exitCode, r.err
}

func (f *fakeHostGit) subcommands() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c[0])
	}
	return out
}

func TestExecuteBehaviourOp_CheckoutBranchMissingRemoteCommand(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	var gotCmd string
	git := &fakeHostGit{results: map[string]fakeGitResult{"ls-remote": {exitCode: 2}}}
	rt := DummyRuntime{
		ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
			gotCmd = cmd
			return "", "", 0, nil
		},
		UploadFn: func(_, _, _ string) error {
			t.Fatal("no bundle is uploaded when the remote ref is absent")
			return nil
		},
		HostGitFn:   git.run,
		hostRepoDir: "/host/repo",
	}
	err := executeBehaviourOp(rt, "sandbox", repoDir, BehaviourOperation{
		Op:   "checkout_branch",
		Args: "agent/42-fix-widget",
	})
	require.NoError(t, err)
	require.Len(t, git.calls, 1)
	assert.Equal(t, []string{"ls-remote", "--exit-code", "--heads", "origin", "refs/heads/agent/42-fix-widget"}, git.calls[0])
	assert.Contains(t, gotCmd, "cd '"+repoDir+"'")
	assert.Contains(t, gotCmd, "git checkout -B 'agent/42-fix-widget' &&")
	assert.NotContains(t, gotCmd, "ls-remote", "the sandbox must not contact the remote")
	assert.NotContains(t, gotCmd, "fetch")
	assert.NotContains(t, gotCmd, "/dev/null", "git stderr must not be discarded")
	assert.Contains(t, gotCmd, "behaviour/marker.txt")
	assert.Contains(t, gotCmd, "commit -m")
	assert.Contains(t, gotCmd, "git -c gc.auto=0 -c maintenance.auto=false -c user.name=", "the commit must not start background maintenance")
}

func TestExecuteBehaviourOp_CheckoutBranchExistingRemoteCommand(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	var gotCmd, uploadedTo string
	git := &fakeHostGit{}
	rt := DummyRuntime{
		ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
			gotCmd = cmd
			return "", "", 0, nil
		},
		UploadFn: func(_, _, remotePath string) error {
			uploadedTo = remotePath
			return nil
		},
		HostGitFn:   git.run,
		hostRepoDir: "/host/repo",
	}
	err := executeBehaviourOp(rt, "sandbox", repoDir, BehaviourOperation{
		Op:   "checkout_branch",
		Args: "agent/42-fix-widget",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ls-remote", "fetch", "bundle", "update-ref"}, git.subcommands())
	assert.Equal(t, []string{"fetch", "--no-tags", "--no-write-fetch-head", "origin",
		"+refs/heads/agent/42-fix-widget:refs/fullsend-behaviour/heads/agent/42-fix-widget"}, git.calls[1])
	assert.Equal(t, "refs/fullsend-behaviour/heads/agent/42-fix-widget", git.calls[2][len(git.calls[2])-1])
	assert.Equal(t, []string{"update-ref", "-d", "refs/fullsend-behaviour/heads/agent/42-fix-widget"}, git.calls[3])
	assert.Equal(t, checkoutBranchBundlePath, uploadedTo)
	assert.Contains(t, gotCmd, "git -c gc.auto=0 -c maintenance.auto=false fetch --no-tags '"+checkoutBranchBundlePath+"' 'refs/fullsend-behaviour/heads/agent/42-fix-widget'")
	assert.Contains(t, gotCmd, "git -c gc.auto=0 -c maintenance.auto=false -c user.name=")
	assert.Contains(t, gotCmd, "git checkout -B 'agent/42-fix-widget' FETCH_HEAD")
	assert.Contains(t, gotCmd, "rm -f '"+checkoutBranchBundlePath+"'")
	assert.NotContains(t, gotCmd, "origin", "the sandbox must not contact the remote")
	assert.NotContains(t, gotCmd, "/dev/null", "git stderr must not be discarded")
	assert.Contains(t, gotCmd, "behaviour/marker.txt")
}

func TestExecuteBehaviourOp_CheckoutBranchRunnerFailures(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		results   map[string]fakeGitResult
		uploadErr error
		want      []string
	}{
		"probe exec error": {
			results: map[string]fakeGitResult{"ls-remote": {err: io.ErrUnexpectedEOF}},
			want:    []string{"runner ls-remote", io.ErrUnexpectedEOF.Error()},
		},
		"probe failure": {
			results: map[string]fakeGitResult{"ls-remote": {exitCode: 128, stderr: "remote: Repository not found.\nfatal: repository not found\n"}},
			want:    []string{"runner ls-remote failed with 128", "fatal: repository not found"},
		},
		"fetch exec error": {
			results: map[string]fakeGitResult{"fetch": {err: io.ErrUnexpectedEOF}},
			want:    []string{"runner fetch", io.ErrUnexpectedEOF.Error()},
		},
		"fetch failure": {
			results: map[string]fakeGitResult{"fetch": {exitCode: 128, stderr: "fatal: couldn't find remote ref\n"}},
			want:    []string{"runner fetch failed with 128", "fatal: couldn't find remote ref"},
		},
		"bundle exec error": {
			results: map[string]fakeGitResult{"bundle": {err: io.ErrUnexpectedEOF}},
			want:    []string{"runner bundle", io.ErrUnexpectedEOF.Error()},
		},
		"bundle failure": {
			results: map[string]fakeGitResult{"bundle": {exitCode: 128, stderr: "fatal: Refusing to create empty bundle.\n"}},
			want:    []string{"runner bundle failed with 128", "fatal: Refusing to create empty bundle."},
		},
		"upload failure": {
			uploadErr: errors.New("upload refused"),
			want:      []string{"bundle upload", "upload refused"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			git := &fakeHostGit{results: tc.results}
			rt := DummyRuntime{
				ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
					t.Fatal("the sandbox command must not run after a runner-side failure")
					return "", "", 0, nil
				},
				UploadFn:    func(_, _, _ string) error { return tc.uploadErr },
				HostGitFn:   git.run,
				hostRepoDir: "/host/repo",
			}
			err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
				Op:   "checkout_branch",
				Args: "agent/42-fix-widget",
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "checkout_branch agent/42-fix-widget")
			for _, w := range tc.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

func TestExecuteBehaviourOp_CheckoutBranchNoHostRepoDir(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{HostGitFn: func(context.Context, string, ...string) (string, string, int, error) {
		t.Fatal("runner git must not run without a host repository")
		return "", "", 0, nil
	}}
	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "checkout_branch",
		Args: "agent/42-fix-widget",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runner-side repository directory is not set")
}

func TestDummyRuntime_RunPassesHostRepoDir(t *testing.T) {
	t.Parallel()

	fullsendDir := t.TempDir()
	scriptPath := filepath.Join(fullsendDir, behaviourScriptRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(scriptPath), 0o755))
	require.NoError(t, os.WriteFile(scriptPath, []byte("ops:\n  - description: checkout\n    op: checkout_branch\n    args: agent/42-impl\n"), 0o644))

	var gotDir string
	rt := DummyRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) { return "", "", 0, nil },
		HostGitFn: func(_ context.Context, dir string, _ ...string) (string, string, int, error) {
			gotDir = dir
			return "", "", 2, nil
		},
		WriteResultsFn: func(string, BehaviourResults) error { return nil },
	}
	code, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		RepoDir:     "/sandbox/workspace/repo",
		HostRepoDir: "/host/repo",
		FullsendDir: fullsendDir,
	}, ui.New(io.Discard), time.Now(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "/host/repo", gotDir)
}

func TestRunHostGit(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	stdout, _, code, err := runHostGit(context.Background(), dir, "--version")
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "git version")

	// A non-zero exit is reported as an exit code with git's stderr.
	_, stderr, code, err := runHostGit(context.Background(), dir, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "fatal:")

	// Automatic gc and maintenance are disabled, so no detached git
	// process outlives the command.
	stdout, _, code, err = runHostGit(context.Background(), dir, "config", "--get", "gc.auto")
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "0", strings.TrimSpace(stdout))
	stdout, _, code, err = runHostGit(context.Background(), dir, "config", "--get", "maintenance.auto")
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "false", strings.TrimSpace(stdout))

	// A cancelled context is an error, not an exit code.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, code, err = runHostGit(ctx, dir, "--version")
	require.Error(t, err)
	assert.Equal(t, -1, code)
}

// runCheckoutBranchForReal runs the checkout_branch op with real git on
// both sides: runner-side git in hostRepo and the sandbox command via
// sh -c against sandboxRepo. The upload copies the bundle to a local path
// that stands in for checkoutBranchBundlePath, which the sandbox command
// is rewritten to use; bundlePath returns that path so tests can check the
// sandbox removed it.
func runCheckoutBranchForReal(t *testing.T, hostRepo, sandboxRepo, branch string) (bundlePath string, err error) {
	t.Helper()
	bundlePath = filepath.Join(t.TempDir(), "checkout-branch.bundle")
	rt := DummyRuntime{
		ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
			cmd = strings.ReplaceAll(cmd, checkoutBranchBundlePath, bundlePath)
			var stdout, stderr bytes.Buffer
			c := exec.Command("sh", "-c", cmd)
			c.Env = gitTestEnv()
			c.Stdout = &stdout
			c.Stderr = &stderr
			if err := c.Run(); err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
				}
				return stdout.String(), stderr.String(), -1, err
			}
			return stdout.String(), stderr.String(), 0, nil
		},
		UploadFn: func(_, localPath, remotePath string) error {
			if remotePath != checkoutBranchBundlePath {
				return errors.New("unexpected upload destination " + remotePath)
			}
			data, err := os.ReadFile(localPath)
			if err != nil {
				return err
			}
			return os.WriteFile(bundlePath, data, 0o644)
		},
		hostRepoDir: hostRepo,
	}
	err = executeBehaviourOp(rt, "sandbox", sandboxRepo, BehaviourOperation{
		Op:   "checkout_branch",
		Args: branch,
	})
	return bundlePath, err
}

// gitTestEnv isolates git from the caller's global and system config and
// disables automatic gc and maintenance, which can detach into a
// background process that keeps writing into .git/objects after the
// command returns and races t.TempDir cleanup.
func gitTestEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
		"GIT_EDITOR=true")
}

func TestGitTestEnvDisablesAutoMaintenance(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	assert.Equal(t, "0", gitOut(t, dir, "config", "--get", "gc.auto"))
	assert.Equal(t, "false", gitOut(t, dir, "config", "--get", "maintenance.auto"))
}

// runGit runs git in dir without inheriting host signing or editor config.
// Seed commits and tags must not depend on the caller's git configuration.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitTestEnv()
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
	// Pushes run receive-pack in origin without the GIT_CONFIG_* variables
	// from gitTestEnv, so stop it starting gc through origin's own config.
	runGit(t, origin, "config", "receive.autogc", "false")
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

// sandboxCopy copies the runner's repository the way the runner uploads it
// into the sandbox, then points the copy's origin at a path that does not
// exist: a sandbox-side fetch or ls-remote would fail, so a passing test
// proves the remote was reached only from the runner side.
func sandboxCopy(t *testing.T, hostRepo string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "sandbox-repo")
	require.NoError(t, os.CopyFS(dst, os.DirFS(hostRepo)))
	runGit(t, dst, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unreachable.git"))
	return dst
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitTestEnv()
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
	sandboxRepo := sandboxCopy(t, clone)
	bundlePath, err := runCheckoutBranchForReal(t, clone, sandboxRepo, "agent/7-existing")
	require.NoError(t, err)

	assert.Equal(t, "agent/7-existing", gitOut(t, sandboxRepo, "branch", "--show-current"))
	// The branch is based on the remote tip, delivered through the bundle,
	// plus exactly one marker commit.
	remoteTip := gitOut(t, origin, "rev-parse", "refs/heads/agent/7-existing")
	assert.Equal(t, remoteTip, gitOut(t, sandboxRepo, "rev-parse", "HEAD~1"))
	assert.Equal(t, "test: add scripted marker commit", gitOut(t, sandboxRepo, "log", "-1", "--format=%s"))
	assert.FileExists(t, filepath.Join(sandboxRepo, "extra.txt"), "remote branch content is present")
	assert.FileExists(t, filepath.Join(sandboxRepo, "behaviour", "marker.txt"))
	assert.NoFileExists(t, bundlePath, "the sandbox removes the bundle")

	// The runner's checkout keeps no trace beyond the fetched objects: no
	// private ref and no local branch.
	assert.Empty(t, gitOut(t, clone, "for-each-ref", "refs/fullsend-behaviour/"))
	assert.Empty(t, gitOut(t, clone, "branch", "--list", "agent/7-existing"))
	assert.Equal(t, "main", gitOut(t, clone, "branch", "--show-current"))
}

// Runners check the target repository out shallow (fetch-depth: 1); the
// runner-side fetch and bundle must work there, and the sandbox copy
// (equally shallow) must accept the bundle.
func TestExecuteBehaviourOp_CheckoutBranchRealShellShallowRunner(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, origin := initCheckoutBranchRepos(t)
	// Move main past the branch point so the shallow boundary is not an
	// ancestor of the remote branch.
	seed := filepath.Join(filepath.Dir(origin), "seed")
	runGit(t, seed, "checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(seed, "later.txt"), []byte("later\n"), 0o644))
	runGit(t, seed, "add", "later.txt")
	runGit(t, seed, "commit", "-m", "later")
	runGit(t, seed, "push", origin, "main")

	shallow := filepath.Join(t.TempDir(), "shallow")
	runGit(t, filepath.Dir(shallow), "clone", "--no-local", "--depth", "1", "-b", "main", origin, shallow)
	require.FileExists(t, filepath.Join(shallow, ".git", "shallow"))
	sandboxRepo := sandboxCopy(t, shallow)

	_, err := runCheckoutBranchForReal(t, shallow, sandboxRepo, "agent/7-existing")
	require.NoError(t, err)
	remoteTip := gitOut(t, origin, "rev-parse", "refs/heads/agent/7-existing")
	assert.Equal(t, remoteTip, gitOut(t, sandboxRepo, "rev-parse", "HEAD~1"))
	assert.FileExists(t, filepath.Join(sandboxRepo, "extra.txt"))
	assert.NoFileExists(t, filepath.Join(sandboxRepo, "later.txt"))
}

func TestExecuteBehaviourOp_CheckoutBranchRealShellMissingRemote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	clone, _ := initCheckoutBranchRepos(t)
	sandboxRepo := sandboxCopy(t, clone)
	mainTip := gitOut(t, sandboxRepo, "rev-parse", "HEAD")
	bundlePath, err := runCheckoutBranchForReal(t, clone, sandboxRepo, "agent/7-brand-new")
	require.NoError(t, err)

	assert.Equal(t, "agent/7-brand-new", gitOut(t, sandboxRepo, "branch", "--show-current"))
	// Missing remote ref falls back to the current HEAD plus the marker.
	assert.Equal(t, mainTip, gitOut(t, sandboxRepo, "rev-parse", "HEAD~1"))
	assert.FileExists(t, filepath.Join(sandboxRepo, "behaviour", "marker.txt"))
	assert.NoFileExists(t, bundlePath, "no bundle is shipped for an absent ref")
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

	sandboxRepo := sandboxCopy(t, clone)
	_, err := runCheckoutBranchForReal(t, clone, sandboxRepo, "agent/7-existing")
	require.NoError(t, err)
	assert.Equal(t, branchTip, gitOut(t, sandboxRepo, "rev-parse", "HEAD~1"), "checkout must resolve the branch, not the same-named tag")
}

func TestExecuteBehaviourOp_CheckoutBranchRealShellRemoteError(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	clone, origin := initCheckoutBranchRepos(t)
	sandboxRepo := sandboxCopy(t, clone)
	mainTip := gitOut(t, sandboxRepo, "rev-parse", "HEAD")
	// Break the remote so the runner-side ls-remote fails with a non-2
	// exit code: the op must fail rather than silently branching off HEAD,
	// and the error must carry git's own stderr.
	require.NoError(t, os.RemoveAll(origin))
	_, err := runCheckoutBranchForReal(t, clone, sandboxRepo, "agent/7-existing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checkout_branch agent/7-existing: runner ls-remote failed with 128")
	assert.Contains(t, err.Error(), "does not appear to be a git repository")
	assert.Equal(t, mainTip, gitOut(t, sandboxRepo, "rev-parse", "HEAD"), "the sandbox repository is untouched")
}

// A sandbox-side failure (here: a corrupt bundle) reports git's stderr.
func TestExecuteBehaviourOp_CheckoutBranchRealShellSandboxFetchError(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo, _ := initCheckoutBranchRepos(t)
	bundlePath := filepath.Join(t.TempDir(), "corrupt.bundle")
	require.NoError(t, os.WriteFile(bundlePath, []byte("not a bundle\n"), 0o644))
	cmd := strings.ReplaceAll(checkoutBranchCommand(repo, "agent/7-existing", "refs/fullsend-behaviour/heads/agent/7-existing"), checkoutBranchBundlePath, bundlePath)
	c := exec.Command("sh", "-c", cmd)
	c.Env = gitTestEnv()
	out, err := c.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "fatal:", "git's stderr is kept")
	assert.NoFileExists(t, bundlePath, "the bundle is removed even when the fetch fails")
	assert.Equal(t, "main", gitOut(t, repo, "branch", "--show-current"))
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

	git := &fakeHostGit{results: map[string]fakeGitResult{"ls-remote": {exitCode: 2}}}
	rt := DummyRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "fatal: not a git repository", 1, nil
		},
		HostGitFn:   git.run,
		hostRepoDir: "/host/repo",
	}
	err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{
		Op:   "checkout_branch",
		Args: "agent/42-fix-widget",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestExecuteBehaviourOp_CheckoutBranchExecError(t *testing.T) {
	t.Parallel()

	git := &fakeHostGit{results: map[string]fakeGitResult{"ls-remote": {exitCode: 2}}}
	rt := DummyRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, io.ErrUnexpectedEOF
		},
		HostGitFn:   git.run,
		hostRepoDir: "/host/repo",
	}
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

func TestExecuteWait(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "0", "-1", "1.5", "x", "1201"} {
		err := executeWait(context.Background(), BehaviourOperation{Op: "wait", Args: bad})
		require.Error(t, err, "%q", bad)
		assert.Contains(t, err.Error(), "whole number of seconds")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := executeWait(ctx, BehaviourOperation{Op: "wait", Args: "30"})
	require.ErrorContains(t, err, "cancelled")
	start := time.Now()
	require.NoError(t, executeWait(context.Background(), BehaviourOperation{Op: "wait", Args: "1"}))
	assert.GreaterOrEqual(t, time.Since(start), time.Second)
}

func TestExecuteBehaviourScript_Wait(t *testing.T) {
	t.Parallel()
	script := &BehaviourScript{Ops: []BehaviourOperation{{Op: "wait", Args: "nope", Description: "Wait"}}}
	res, err := executeBehaviourScript(context.Background(), DummyRuntime{}, "sandbox", t.TempDir(), script)
	require.Error(t, err)
	require.Len(t, res.Operations, 1)
	assert.False(t, res.Operations[0].Success)
}

// assertNotJWTCommand runs under a real sh: a placeholder value passes, a
// JWT value or a token file holding one fails with its own exit code, and
// an unset variable fails.
func TestAssertNotJWTCommand(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dir := t.TempDir()
	placeholderFile := filepath.Join(dir, "placeholder.token")
	require.NoError(t, os.WriteFile(placeholderFile, []byte("openshell:resolve:env:v1_INFERENCE_GATEWAY_API_KEY"), 0o600))
	jwtFile := filepath.Join(dir, "jwt.token")
	require.NoError(t, os.WriteFile(jwtFile, []byte("eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.sig"), 0o600))
	run := func(value string) int {
		cmd := exec.Command("sh", "-c", assertNotJWTCommand("PROBE_VAR"))
		cmd.Env = append(os.Environ(), "PROBE_VAR="+value)
		err := cmd.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		require.NoError(t, err)
		return 0
	}
	assert.Equal(t, 0, run("openshell:resolve:env:v1_INFERENCE_GATEWAY_API_KEY"))
	assert.Equal(t, 0, run(placeholderFile))
	assert.Equal(t, assertNotJWTFound, run("eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.sig"))
	assert.Equal(t, assertNotJWTFound, run(jwtFile))
	assert.Equal(t, assertNotJWTUnset, run(""))
	// An absolute path that names no readable file cannot be inspected, so
	// it must not pass.
	assert.Equal(t, assertNotJWTUninspected, run(filepath.Join(dir, "missing.token")))
	assert.Equal(t, assertNotJWTUninspected, run(dir))
}

// A grep that fails (exit above 1, here a missing binary on PATH is not
// usable, so a stub grep exits 2) must fail the check, not pass it.
func TestAssertNotJWTCommand_GrepFailure(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "grep"), []byte("#!/bin/sh\nexit 2\n"), 0o755))
	cmd := exec.Command("sh", "-c", assertNotJWTCommand("PROBE_VAR"))
	cmd.Env = append(os.Environ(), "PROBE_VAR=openshell:resolve:env:v1_X", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := cmd.Run()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, assertNotJWTUninspected, exitErr.ExitCode())
}

func TestExecuteBehaviourOp_AssertNotJWT(t *testing.T) {
	t.Parallel()
	err := executeBehaviourOp(DummyRuntime{}, "sandbox", t.TempDir(), BehaviourOperation{Op: "assert_not_jwt"})
	require.ErrorContains(t, err, "requires a variable name")
	err = executeBehaviourOp(DummyRuntime{}, "sandbox", t.TempDir(), BehaviourOperation{Op: "assert_not_jwt", Args: "X; rm -rf /"})
	require.ErrorContains(t, err, "invalid variable name")
	for code, want := range map[int]string{0: "", assertNotJWTUnset: "unset or empty", assertNotJWTFound: "holds a JWT", assertNotJWTUninspected: "could not be inspected", 1: "failed"} {
		rt := DummyRuntime{ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "boom", code, nil
		}}
		err := executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{Op: "assert_not_jwt", Args: "INFERENCE_GATEWAY_TOKEN_FILE"})
		if want == "" {
			require.NoError(t, err)
			continue
		}
		require.ErrorContains(t, err, want)
	}
	rt := DummyRuntime{ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
		return "", "", -1, errors.New("exec down")
	}}
	err = executeBehaviourOp(rt, "sandbox", t.TempDir(), BehaviourOperation{Op: "assert_not_jwt", Args: "X"})
	require.ErrorContains(t, err, "exec down")
}
