package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeEntrypointHelperRunsWithHooksModelAndArgBoundaries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HELPER_CAPTURE", filepath.Join(dir, "args"))
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	claude := filepath.Join(bin, "claude")
	require.NoError(t, os.WriteFile(claude, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HELPER_CAPTURE\"\n"), 0o755))
	helper := filepath.Join(dir, "fullsend-claude")
	content := ClaudeEntrypointHelper("sonnet", "high", "/home/sandbox/.claude/hooks.json", []string{"/home/sandbox/.claude/plugins/review"}, map[string]string{"sonnet": "claude-test-model"}, "", map[string]string{"TIRITH_REQUIRED": "1"})
	assert.Contains(t, string(content), "export TIRITH_REQUIRED='1'")
	assert.NotContains(t, string(content), "FULLSEND_ENV_FILE")
	assert.NotContains(t, string(content), "/sandbox/workspace/.env")
	assert.NotContains(t, string(content), "writable; refusing to launch child")
	require.NoError(t, os.WriteFile(helper, content, 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NoError(t, exec.Command(helper, "a prompt with spaces").Run())
	data, err := os.ReadFile(filepath.Join(dir, "args"))
	require.NoError(t, err)
	args := string(data)
	for _, want := range []string{"--model\nclaude-test-model", "--effort\nhigh", "--settings\n/home/sandbox/.claude/hooks.json", "--plugin-dir\n/home/sandbox/.claude/plugins/review", "a prompt with spaces"} {
		require.Contains(t, args, want)
	}
	require.True(t, strings.HasPrefix(args, "--print\n"))
}

func TestRunEntrypointPreservesArgvAndCapturesOutput(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, os.MkdirAll(bin, 0o755))
	entry := filepath.Join(dir, "run.sh")
	require.NoError(t, os.WriteFile(entry, []byte("#!/bin/sh\nprintf '<%s>\\n' \"$@\"\n"), 0o755))
	// The fake OpenShell CLI removes the fixed sandbox .env source and runs
	// the resulting argv command locally. This exercises shell quoting and
	// output capture without requiring a gateway.
	fakeOpenShell := filepath.Join(bin, "openshell")
	fake := `#!/bin/sh
while [ "$1" != "--" ]; do shift; done
shift
shift
shift
command=$1
prefix=" && . '/sandbox/workspace/.env' && "
command=${command#*"$prefix"}
exec /bin/sh -c "$command"
`
	require.NoError(t, os.WriteFile(fakeOpenShell, []byte(fake), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output := filepath.Join(dir, "stdout")
	argv := []string{entry, "two words", "$(touch should-not-run)", ""}
	savedStdout := os.Stdout
	console, err := os.Create(filepath.Join(dir, "console"))
	require.NoError(t, err)
	os.Stdout = console
	t.Cleanup(func() { os.Stdout = savedStdout; console.Close() })
	code, err := RunEntrypoint(context.Background(), "test-sandbox", repo, argv, "none", 5*time.Second, output, nil, nil, &RunMetrics{})
	require.NoError(t, err)
	require.Equal(t, 0, code)
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "<two words>\n<$(touch should-not-run)>\n<>\n", string(data))
	consoleData, err := os.ReadFile(console.Name())
	require.NoError(t, err)
	require.Equal(t, data, consoleData)
	assert.NoFileExists(t, filepath.Join(repo, "should-not-run"))
}

func TestRunEntrypointValidatesLaunchAndReportsOutputFailures(t *testing.T) {
	_, err := RunEntrypoint(context.Background(), "sb", "/tmp", nil, "none", time.Second, "", nil, nil, nil)
	require.ErrorContains(t, err, "command is empty")
	_, err = RunEntrypoint(context.Background(), "sb", "/tmp", []string{"run"}, "pi", time.Second, "", nil, nil, nil)
	require.ErrorContains(t, err, "unsupported entrypoint stream format")

	bin := t.TempDir()
	t.Setenv("PATH", bin)
	_, err = RunEntrypoint(context.Background(), "sb", "/tmp", []string{"run"}, "none", time.Second, "", nil, nil, nil)
	require.ErrorContains(t, err, "starting openshell exec")

	installFakeOpenShell(t, `#!/bin/sh
while [ "$1" != "--" ]; do shift; done
shift
shift
shift
command=$1
prefix=" && . '/sandbox/workspace/.env' && "
command=${command#*"$prefix"}
exec /bin/sh -c "$command"
`)
	_, err = RunEntrypoint(context.Background(), "sb", t.TempDir(), []string{"/bin/true"}, "none", time.Second, filepath.Join(t.TempDir(), "missing", "out"), nil, nil, nil)
	require.ErrorContains(t, err, "creating entrypoint output")
}

func TestRunEntrypointParsesClaudeStream(t *testing.T) {
	dir := t.TempDir()
	installFakeOpenShell(t, "#!/bin/sh\nprintf '%s\\n' '"+`{"type":"result","num_turns":2,"total_cost_usd":0.25,"is_error":false,"subtype":"success","usage":{"input_tokens":12,"output_tokens":3}}`+"'\n")
	output := filepath.Join(dir, "stream.jsonl")
	metrics := &RunMetrics{}
	var events []AgentEvent
	code, err := RunEntrypoint(context.Background(), "sb", dir, []string{"unused"}, "claude", 5*time.Second, output, nil, func(e AgentEvent) { events = append(events, e) }, metrics)
	require.NoError(t, err)
	require.Equal(t, 0, code)
	require.Len(t, events, 1)
	require.Equal(t, 2, metrics.NumTurns)
	require.Equal(t, 0.25, metrics.TotalCostUSD)
	require.Equal(t, 12, metrics.InputTokens)
	require.FileExists(t, output)
}

func installFakeOpenShell(t *testing.T, content string) {
	t.Helper()
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "openshell"), []byte(content), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
