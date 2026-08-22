package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

func TestTranslatePiModel(t *testing.T) {
	t.Setenv(piModelEnv, "")
	t.Setenv(piProviderEnv, "")
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", translatePiModel("opus"))
	assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", translatePiModel("sonnet"))
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", translatePiModel("haiku"))
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", translatePiModel(""), "empty falls back to the opus alias")
	assert.Equal(t, "anthropic-vertex/claude-opus-4-8", translatePiModel("claude-opus-4-8"), "bare ids get the provider prefix")
	assert.Equal(t, "anthropic/claude-sonnet-4-6", translatePiModel("anthropic/claude-sonnet-4-6"), "provider/id passes through")

	t.Setenv(piProviderEnv, "anthropic")
	assert.Equal(t, "anthropic/claude-opus-4-6", translatePiModel("opus"))

	t.Setenv(piModelEnv, "google-vertex/gemini-2.5-pro")
	assert.Equal(t, "google-vertex/gemini-2.5-pro", translatePiModel("opus"), "FULLSEND_PI_MODEL overrides everything")

	t.Setenv(piProviderEnv, "")
	t.Setenv(piModelEnv, "claude-opus-4-8")
	assert.Equal(t, "anthropic-vertex/claude-opus-4-8", translatePiModel("opus"), "a bare override still gets the provider prefix")
}

func TestPiThinkingFor(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		got, ok := piThinkingFor(level)
		assert.True(t, ok)
		assert.Equal(t, level, got)
	}
	got, ok := piThinkingFor("")
	assert.True(t, ok)
	assert.Equal(t, piDefaultThinking, got, "unset effort runs at the default, not at pi's medium")
	got, ok = piThinkingFor("turbo")
	assert.False(t, ok, "unknown levels are reported")
	assert.Equal(t, piDefaultThinking, got, "and fall back to the default")
}

func piTestParams() RunParams {
	return RunParams{
		SandboxName:   "sb",
		AgentBaseName: "triage",
		RepoDir:       sandbox.SandboxWorkspace + "/repo",
		Timeout:       time.Minute,
	}
}

func TestBuildPiRunCommand_Basic(t *testing.T) {
	t.Setenv(piModelEnv, "")
	t.Setenv(piProviderEnv, "")
	m := &piManifest{AgentName: "triage", Model: "opus", Tools: []string{"bash"}, BashAllowlist: []string{"gh"}, Hooks: &piHooksManifest{}}
	params := piTestParams()
	params.HooksSettingsPath = "/sandbox/claude-config/hooks.json"
	cmd := buildPiRunCommand(params, m)

	// The guard runs before the agent-writable .env is sourced.
	assert.True(t, strings.HasPrefix(cmd, `cd '/sandbox/workspace/repo' && `+piHooksGuard("/sandbox/pi-config/fullsend-hooks.js", "/sandbox/pi-config/fullsend-manifest.json")+` && . '/sandbox/workspace/.env' && export FULLSEND_PI_MANIFEST='/sandbox/pi-config/fullsend-manifest.json' && unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL ANTHROPIC_VERTEX_BASE_URL && export GOOGLE_CLOUD_PROJECT="${ANTHROPIC_VERTEX_PROJECT_ID:-$GOOGLE_CLOUD_PROJECT}" && pi --print --mode json`), cmd)
	for _, want := range []string{
		"--no-approve", "--no-extensions", "--no-prompt-templates", "--no-themes",
		"--session-dir '/sandbox/pi-config/sessions'",
		"-e '/opt/pi-extensions/anthropic-vertex'",
		"-e '/sandbox/pi-config/fullsend-hooks.js'",
		"--tools 'bash'",
		"--model 'anthropic-vertex/claude-opus-4-6'",
		"'Run the agent task'",
	} {
		assert.Contains(t, cmd, want)
	}
	assert.Contains(t, cmd, "--thinking 'high'", "no harness effort: pi's own default is medium, Claude Code's is high")
	assert.NotContains(t, cmd, "2>>")
	assert.NotContains(t, cmd, "  ", "no double spaces")
	assert.True(t, strings.HasSuffix(cmd, "'Run the agent task' </dev/null"), "stdin is closed: pi --print reads an open stdin to EOF")

	// Claude-on-Vertex: stray direct-API variables never reach pi, and the
	// project is pinned to the variable Claude Code on Vertex uses.
	unsetIdx := strings.Index(cmd, "&& unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL ANTHROPIC_VERTEX_BASE_URL")
	envIdx := strings.Index(cmd, ". '/sandbox/workspace/.env'")
	piIdx := strings.Index(cmd, "&& pi ")
	assert.True(t, unsetIdx > envIdx && unsetIdx < piIdx, "unset runs after sourcing .env and before pi: %s", cmd)
	assert.Contains(t, cmd, `&& export GOOGLE_CLOUD_PROJECT="${ANTHROPIC_VERTEX_PROJECT_ID:-$GOOGLE_CLOUD_PROJECT}"`)

	// pi resolves the provider prefix case-insensitively; so must the gate.
	t.Setenv(piModelEnv, "Anthropic-Vertex/claude-opus-4-6")
	cmd = buildPiRunCommand(params, m)
	assert.Contains(t, cmd, "&& unset ANTHROPIC_API_KEY")
	assert.Contains(t, cmd, "--model 'Anthropic-Vertex/claude-opus-4-6'")
}

// TestPiHooksGuard runs the rendered guard under a real sh: it must exit 97
// without running what follows when the adapter is missing or modified, and
// fall through when both files are intact.
func TestPiHooksGuard(t *testing.T) {
	t.Parallel()
	// Probe the way the guard looks the tools up (`command -p`, the system
	// default PATH), not the caller's PATH: Homebrew coreutils on macOS
	// would pass LookPath and still fail the intact case.
	if err := exec.Command("sh", "-c", "command -p sha256sum /dev/null >/dev/null && command -p cut -d' ' -f1 /dev/null").Run(); err != nil {
		t.Skip("sha256sum/cut not on the default PATH (stock macOS); the sandbox image has coreutils")
	}
	dir := t.TempDir()
	ext := filepath.Join(dir, "fullsend-hooks.js")
	manifest := filepath.Join(dir, "fullsend-manifest.json")
	require.NoError(t, os.WriteFile(manifest, []byte("{}"), 0o644))

	run := func() (int, string) {
		cmd := exec.Command("sh", "-c", piHooksGuard(ext, manifest)+" && echo RAN")
		out, err := cmd.CombinedOutput()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			require.NoError(t, err)
		}
		return code, string(out)
	}

	code, out := run()
	assert.Equal(t, piHooksMissingExit, code, "missing adapter")
	assert.NotContains(t, out, "RAN")
	assert.Contains(t, out, "refusing to run unhooked")

	require.NoError(t, os.WriteFile(ext, append(append([]byte{}, piHooksExtensionJS...), []byte("\n// tampered\n")...), 0o644))
	code, out = run()
	assert.Equal(t, piHooksMissingExit, code, "modified adapter")
	assert.NotContains(t, out, "RAN")

	require.NoError(t, os.WriteFile(ext, piHooksExtensionJS, 0o644))
	code, out = run()
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "RAN")

	require.NoError(t, os.Remove(manifest))
	code, _ = run()
	assert.Equal(t, piHooksMissingExit, code, "missing manifest")

	// A shell function or PATH entry standing in for sha256sum must not
	// make a tampered adapter pass: the guard uses `command -p`.
	require.NoError(t, os.WriteFile(manifest, []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(ext, []byte("// tampered\n"), 0o644))
	shadowDir := t.TempDir()
	sum := sha256.Sum256(piHooksExtensionJS)
	fake := "#!/bin/sh\necho '" + hex.EncodeToString(sum[:]) + "  x'\n"
	require.NoError(t, os.WriteFile(filepath.Join(shadowDir, "sha256sum"), []byte(fake), 0o755))
	shadowed := exec.Command("sh", "-c", "sha256sum() { echo '"+hex.EncodeToString(sum[:])+"  x'; }; cut() { echo '"+hex.EncodeToString(sum[:])+"'; }; PATH="+shellQuote(shadowDir)+":$PATH; "+piHooksGuard(ext, manifest)+" && echo RAN")
	out2, err := shadowed.CombinedOutput()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, string(out2))
	assert.Equal(t, piHooksMissingExit, exitErr.ExitCode(), "shadowed sha256sum")
	assert.NotContains(t, string(out2), "RAN")
}

func TestBuildPiRunCommand_DirectProviderKeepsAnthropicEnv(t *testing.T) {
	t.Setenv(piModelEnv, "")
	t.Setenv(piProviderEnv, "anthropic")
	cmd := buildPiRunCommand(piTestParams(), &piManifest{})
	assert.Contains(t, cmd, "--model 'anthropic/claude-opus-4-6'")
	assert.NotContains(t, cmd, "unset ANTHROPIC_API_KEY", "direct Anthropic provider needs its key")
	assert.NotContains(t, cmd, "GOOGLE_CLOUD_PROJECT")
	assert.NotContains(t, cmd, "/opt/pi-extensions/anthropic-vertex", "the Vertex extension is only loaded for the anthropic-vertex provider")
}

func TestBuildPiRunCommand_HarnessOverridesAndFlags(t *testing.T) {
	t.Setenv(piModelEnv, "")
	t.Setenv(piProviderEnv, "")
	params := piTestParams()
	params.Model = "sonnet"
	params.Effort = "high"
	params.Debug = "*"
	// A manifest claiming hooks must not matter: the runner's signal decides.
	m := &piManifest{AgentName: "code", Model: "opus", Tools: nil, Hooks: &piHooksManifest{}}
	cmd := buildPiRunCommand(params, m)

	assert.Contains(t, cmd, "--model 'anthropic-vertex/claude-sonnet-4-6'", "harness model wins over the agent definition")
	assert.Contains(t, cmd, "--thinking 'high'")
	assert.NotContains(t, cmd, "--tools", "nil tools keeps pi's default tool set")
	assert.NotContains(t, cmd, "--no-builtin-tools")
	assert.NotContains(t, cmd, "fullsend-hooks.js", "no hook extension when the runner has security disabled")
	assert.NotContains(t, cmd, "test -f")
	assert.Contains(t, cmd, "-e '/opt/pi-extensions/anthropic-vertex'")
	assert.True(t, strings.HasSuffix(cmd, "'Run the agent task' </dev/null 2>>'/sandbox/workspace/pi-debug.log'"), cmd)
}

func TestBuildPiRunCommand_EmptyToolRestriction(t *testing.T) {
	t.Setenv(piModelEnv, "")
	m := &piManifest{Tools: []string{}}
	cmd := buildPiRunCommand(piTestParams(), m)
	assert.Contains(t, cmd, "--no-builtin-tools")
	assert.NotContains(t, cmd, "--tools ")
}

func TestBuildPiRunCommand_QuotesRepoDirAndModel(t *testing.T) {
	t.Setenv(piModelEnv, "")
	params := piTestParams()
	params.RepoDir = "/sandbox/workspace/it's"
	params.Model = "anthropic/claude'x"
	cmd := buildPiRunCommand(params, &piManifest{})
	assert.Contains(t, cmd, `cd '/sandbox/workspace/it'\''s'`)
	assert.Contains(t, cmd, `--model 'anthropic/claude'\''x'`)
}

func TestPiThinkingFor_DefaultAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		effort, want string
		ok           bool
	}{
		{"", "high", true},
		{"  ", "high", true},
		{"low", "low", true},
		{"max", "max", true},
		{"bogus", "high", false},
	} {
		got, ok := piThinkingFor(tc.effort)
		assert.Equal(t, tc.want, got, tc.effort)
		assert.Equal(t, tc.ok, ok, tc.effort)
	}

	params := piTestParams()
	params.Effort = "bogus"
	cmd := buildPiRunCommand(params, &piManifest{AgentName: "triage", Model: "opus"})
	assert.Contains(t, cmd, "--thinking 'high'", "unknown effort falls back to the default, not to pi's medium")
}
