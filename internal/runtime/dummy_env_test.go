package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stripHarnessEnv checks that cmd loads the harness environment from the
// sandbox .env before anything else and returns the op's own command.
func stripHarnessEnv(t *testing.T, cmd string) string {
	t.Helper()
	prefix := harnessEnvCommand(DummyRuntime{}.harnessEnvLoad(), "")
	require.True(t, strings.HasPrefix(cmd, prefix), "the scenario command must load the harness environment first")
	return strings.TrimPrefix(cmd, prefix)
}

// harnessEnvLoadFor is the harness environment load for the fixture
// workspace f, with the .env.d and iteration.env paths pointed at it.
func harnessEnvLoadFor(f envDataFixture) string {
	return envDataTrustedPin() + " && " + envDataLoadWith(f.envFile, envDataControlLines(f.ws), nil)
}

// emptyHarnessEnvLoad loads an empty harness .env, for real-shell tests
// that need no harness variables. It skips the test without python3.
func emptyHarnessEnvLoad(t *testing.T) string {
	t.Helper()
	f := newEnvDataFixture(t)
	f.write(t, ".env", "")
	return harnessEnvLoadFor(f)
}

// shellExec runs each command under sh -c with only env, the way
// sandbox.Exec runs it in a fresh sandbox shell.
func shellExec(env []string) sandboxExecFunc {
	return func(_, cmd string, _ time.Duration) (string, string, int, error) {
		c := exec.Command("sh", "-c", cmd)
		c.Env = env
		var stdout, stderr bytes.Buffer
		c.Stdout, c.Stderr = &stdout, &stderr
		err := c.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
		}
		if err != nil {
			return stdout.String(), stderr.String(), -1, err
		}
		return stdout.String(), stderr.String(), 0, nil
	}
}

// scenarioEnvOps are one of each op that runs a command in the sandbox.
var scenarioEnvOps = []BehaviourOperation{
	{Description: "read", Op: "read_file", Args: "README.md"},
	{Description: "get", Op: "url_get", Args: "https://example.com/"},
	{Description: "env", Op: "assert_env", Args: "GH_HOST"},
	{Description: "not jwt", Op: "assert_not_jwt", Args: "GH_HOST"},
	{Description: "file", Op: "assert_file", Args: "output/result.json"},
	{Description: "json", Op: "assert_json", Args: "output/result.json,a.b"},
	{Description: "jq", Op: "assert_jq", Args: "output/result.json, .a"},
	{Description: "probe", Op: "http_probe", Args: "GET https://gw.example/ INFERENCE_GATEWAY_API_KEY"},
	{Description: "branch", Op: "checkout_branch", Args: "agent/1-fix"},
}

func TestHarnessEnvCommand_Shape(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "trap 'exit 112' EXIT; { LOAD; } || exit 112; trap - EXIT; op args", harnessEnvCommand("LOAD", "op args"))
	assert.Equal(t, EnvDataLoad("/sandbox/workspace/.env"), DummyRuntime{}.harnessEnvLoad(), "the scenario commands read the sandbox .env as the probes do")
	assert.Equal(t, "custom", DummyRuntime{envLoad: "custom"}.harnessEnvLoad())
}

// Every op that runs a command in the sandbox loads the harness
// environment first (#9046).
func TestExecuteBehaviourScript_OpsLoadHarnessEnv(t *testing.T) {
	t.Parallel()

	var cmds []string
	git := &fakeHostGit{results: map[string]fakeGitResult{"ls-remote": {exitCode: 2}}}
	rt := DummyRuntime{
		ExecFn: func(_, cmd string, _ time.Duration) (string, string, int, error) {
			cmds = append(cmds, cmd)
			return `{"status":200,"body":"ok"}` + "\n", "", 0, nil
		},
		HostGitFn:   git.run,
		hostRepoDir: "/host/repo",
	}
	results, err := executeBehaviourScript(context.Background(), rt, "sb", "/sandbox/workspace/repo", &BehaviourScript{Ops: scenarioEnvOps})
	require.NoError(t, err)
	require.Len(t, results.Operations, len(scenarioEnvOps))
	for _, res := range results.Operations {
		assert.True(t, res.Success, res.Description)
	}
	require.Len(t, cmds, len(scenarioEnvOps))
	for _, cmd := range cmds {
		assert.NotEmpty(t, stripHarnessEnv(t, cmd))
	}
}

// A harness environment that cannot be loaded fails every op with the
// loader's error, not with the op's own (such as "unset or empty").
func TestExecuteBehaviourScript_HarnessEnvLoadFailure(t *testing.T) {
	t.Parallel()

	const loaderErr = "fullsend: cannot read '/sandbox/workspace/.env': No such file or directory"
	git := &fakeHostGit{results: map[string]fakeGitResult{"ls-remote": {exitCode: 2}}}
	rt := DummyRuntime{
		ExecFn: func(_, _ string, _ time.Duration) (string, string, int, error) {
			return "", loaderErr + "\n", dummyEnvLoadFailedExit, nil
		},
		HostGitFn:   git.run,
		hostRepoDir: "/host/repo",
	}
	results, err := executeBehaviourScript(context.Background(), rt, "sb", "/sandbox/workspace/repo", &BehaviourScript{Ops: scenarioEnvOps})
	require.Error(t, err)
	require.Len(t, results.Operations, len(scenarioEnvOps))
	for _, res := range results.Operations {
		assert.False(t, res.Success, res.Description)
		assert.Contains(t, res.Error, "loading the harness environment failed: "+loaderErr, res.Description)
		assert.NotContains(t, res.Error, "unset or empty", res.Description)
	}
}

// The reproduction from #9046, in a real shell: a variable the harness
// delivers through env.sandbox or an .env.d file is visible to a
// scenario command, one it does not deliver is not, and the loaded
// values are not printed.
func TestScenarioExec_RealShellSeesHarnessEnv(t *testing.T) {
	t.Parallel()

	f := newEnvDataFixture(t)
	control := envDataControlLines(f.ws)
	f.write(t, ".env", strings.Join([]string{
		control[0],
		"export FULLSEND_OUTPUT_DIR=/sandbox/workspace/output",
		control[1],
		"export GH_HOST='example.com'",
		control[2],
		control[3],
	}, "\n")+"\n")
	f.write(t, ".env.d/host.env", `export FROM_ENVD='spaced "quoted" $literal'`+"\n")
	rt := DummyRuntime{
		ExecFn:  shellExec([]string{"PATH=" + os.Getenv("PATH"), "HOME=/home/agent"}),
		envLoad: harnessEnvLoadFor(f),
	}

	for _, name := range []string{"GH_HOST", "FROM_ENVD"} {
		require.NoError(t, executeBehaviourOp(rt, "sb", f.ws, BehaviourOperation{Op: "assert_env", Args: name}), name)
	}
	require.NoError(t, executeBehaviourOp(rt, "sb", f.ws, BehaviourOperation{Op: "assert_not_jwt", Args: "GH_HOST"}))

	err := executeBehaviourOp(rt, "sb", f.ws, BehaviourOperation{Op: "assert_env", Args: "UNDELIVERED_VAR"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "assert_env UNDELIVERED_VAR unset or empty")

	stdout, stderr, code, err := rt.scenarioExec("sb", "printenv FROM_ENVD", 10*time.Second)
	require.NoError(t, err, stderr)
	assert.Equal(t, 0, code)
	assert.Equal(t, "spaced \"quoted\" $literal\n", stdout)

	stdout, stderr, code, err = rt.scenarioExec("sb", "true", 10*time.Second)
	require.NoError(t, err, stderr)
	assert.Equal(t, 0, code)
	assert.Empty(t, stdout, "the loaded values are not printed")
	assert.NotContains(t, stderr, "example.com", "the loaded values are not printed")
}

// A missing .env fails the op with the loader's error.
func TestScenarioExec_RealShellMissingEnvFails(t *testing.T) {
	t.Parallel()

	f := newEnvDataFixture(t)
	rt := DummyRuntime{
		ExecFn:  shellExec([]string{"PATH=" + os.Getenv("PATH"), "HOME=/home/agent", "GH_HOST=from-the-process"}),
		envLoad: harnessEnvLoadFor(f),
	}
	err := executeBehaviourOp(rt, "sb", f.ws, BehaviourOperation{Op: "assert_env", Args: "GH_HOST"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "assert_env exec: loading the harness environment failed: fullsend: cannot read")
	assert.Contains(t, err.Error(), ".env")
}

// A load that ends the shell itself, as the interpreter pin does with
// exit 127 when no trusted python3 is found, is still reported as a load
// failure rather than as the command's exit status.
func TestScenarioExec_RealShellLoaderExitIsLoadFailure(t *testing.T) {
	t.Parallel()

	rt := DummyRuntime{
		ExecFn:  shellExec([]string{"PATH=" + os.Getenv("PATH")}),
		envLoad: `{ echo 'fullsend: python3 not found' >&2; exit 127; }`,
	}
	_, _, code, err := rt.scenarioExec("sb", "echo ran", 10*time.Second)
	require.Error(t, err)
	assert.Equal(t, dummyEnvLoadFailedExit, code)
	assert.Equal(t, "loading the harness environment failed: fullsend: python3 not found", err.Error())

	// After a successful load the command's own exit status is kept.
	rt.envLoad = "true"
	stdout, _, code, err := rt.scenarioExec("sb", "echo ran; exit 3", 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, 3, code)
	assert.Equal(t, "ran\n", stdout)
}
