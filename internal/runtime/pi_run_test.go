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
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", translatePiModel("opus"))
	assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", translatePiModel("sonnet"))
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", translatePiModel("haiku"))
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", translatePiModel(""), "empty falls back to the opus alias")
	assert.Equal(t, "anthropic-vertex/claude-opus-4-8", translatePiModel("claude-opus-4-8"), "bare ids get the provider prefix")
	assert.Equal(t, "anthropic/claude-sonnet-4-6", translatePiModel("anthropic/claude-sonnet-4-6"), "provider/id passes through")

	// xai/ normalization: "xai/grok-4.6" becomes "xai-vertex/xai/grok-4.6"
	// so the provider gate in buildPiRunCommand fires correctly.
	assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel("xai/grok-4.6"), "xai/ is normalized to xai-vertex/xai/")
	assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel("xai-vertex/xai/grok-4.6"), "already-normalized three-segment spec passes through")

	// Case-insensitive, because the gate in buildPiRunCommand is: a spec that
	// escapes normalization reaches pi's built-in xai provider with
	// XAI_API_KEY still set, which is the failure #6571 exists to close.
	for _, spec := range []string{"XAI/grok-4.6", "Xai/grok-4.6", "xAI/grok-4.6"} {
		assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel(spec), "case-varied short form is still normalized: %s", spec)
	}
	for _, spec := range []string{"XAI-VERTEX/xai/grok-4.6", "Xai-Vertex/XAI/grok-4.6"} {
		assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel(spec), "case-varied long form is canonicalised: %s", spec)
	}

	// A bare id under FULLSEND_PI_PROVIDER=xai-vertex must still get the
	// publisher segment. Before harness `model:` accepted "/" (#6570) this
	// was the only way a harness reached Grok, and it stays supported --
	// the two-segment "xai-vertex/grok-4.6" is a model the extension
	// does not register, which pi silently substitutes a fallback for.
	t.Setenv(piProviderEnv, piXaiVertexProvider)
	assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel("grok-4.6"), "bare id gets the publisher segment too")
	assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel("xai/grok-4.6"), "short form is unaffected by the provider env")

	t.Setenv(piProviderEnv, "anthropic")
	assert.Equal(t, "anthropic/claude-opus-4-6", translatePiModel("opus"))

	// The model override is resolved by the CLI (--model, FULLSEND_MODEL,
	// FULLSEND_PI_MODEL) and arrives as the model argument; the runtime no
	// longer reads FULLSEND_PI_MODEL itself.
	t.Setenv("FULLSEND_PI_MODEL", "google-vertex/gemini-2.5-pro")
	assert.Equal(t, "anthropic/claude-opus-4-6", translatePiModel("opus"), "runtime ignores FULLSEND_PI_MODEL")
	assert.Equal(t, "google-vertex/gemini-2.5-pro", translatePiModel("google-vertex/gemini-2.5-pro"))
	t.Setenv(piProviderEnv, "")
	assert.Equal(t, "anthropic-vertex/claude-opus-4-8", translatePiModel("claude-opus-4-8"), "a bare override still gets the provider prefix")
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
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	m := &piManifest{AgentName: "triage", Model: "opus", Tools: []string{"bash"}, BashAllowlist: []string{"gh"}, Hooks: &piHooksManifest{}}
	params := piTestParams()
	params.HooksSettingsPath = "/sandbox/claude-config/hooks.json"
	cmd := buildPiRunCommand(params, m)

	// The guard runs before the agent-writable .env is sourced.
	assert.True(t, strings.HasPrefix(cmd, `cd '/sandbox/workspace/repo' && `+piHooksGuard("/sandbox/pi-config/fullsend-hooks.js", "/sandbox/pi-config/fullsend-manifest.json")+` && . '/sandbox/workspace/.env' && export FULLSEND_PI_MANIFEST='/sandbox/pi-config/fullsend-manifest.json' && export FULLSEND_RUNTIME=pi && export GOOGLE_CLOUD_LOCATION="${GOOGLE_CLOUD_LOCATION:-$CLOUD_ML_REGION}" && unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL ANTHROPIC_VERTEX_BASE_URL && export GOOGLE_CLOUD_PROJECT="${ANTHROPIC_VERTEX_PROJECT_ID:-$GOOGLE_CLOUD_PROJECT}" && pi --print --mode json`), cmd)
	// Gemini on Vertex needs GOOGLE_CLOUD_LOCATION; the fleet exports the
	// region as CLOUD_ML_REGION, so it is mirrored after .env is sourced.
	assert.Contains(t, cmd, `&& export GOOGLE_CLOUD_LOCATION="${GOOGLE_CLOUD_LOCATION:-$CLOUD_ML_REGION}"`)
	assert.Contains(t, cmd, "&& export FULLSEND_RUNTIME=pi")
	for _, want := range []string{
		"--no-approve", "--no-extensions", "--no-prompt-templates", "--no-themes",
		"--session-dir '/sandbox/pi-config/sessions'",
		"-e '/usr/local/share/pi-extensions/anthropic-vertex'",
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
	params.Model = "Anthropic-Vertex/claude-opus-4-6"
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

func TestPiBareModelID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "claude-opus-4-6", piBareModelID("anthropic-vertex/claude-opus-4-6"), "two-segment: strips provider")
	assert.Equal(t, "xai/grok-4.6", piBareModelID("xai-vertex/xai/grok-4.6"), "three-segment: strips only the provider, keeps wire model id")
	assert.Equal(t, "grok-4.6", piBareModelID("grok-4.6"), "no provider: returns as-is")
	assert.Equal(t, "claude-sonnet-4-6", piBareModelID("anthropic/claude-sonnet-4-6"), "direct anthropic: strips provider")
}

func TestBuildPiRunCommand_XaiVertex(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	m := &piManifest{AgentName: "triage", Model: "opus", Tools: []string{"bash"}}
	params := piTestParams()

	// Short form: xai/grok-4.6 is normalized to xai-vertex/xai/grok-4.6.
	params.Model = "xai/grok-4.6"
	cmd := buildPiRunCommand(params, m)

	assert.Contains(t, cmd, "--model 'xai-vertex/xai/grok-4.6'", "normalized model spec")
	assert.Contains(t, cmd, "-e '"+sandbox.SandboxPiExtensionsDir+"/xai-vertex'", "xai-vertex extension is loaded")
	assert.Contains(t, cmd, "&& unset XAI_API_KEY", "XAI_API_KEY is unset")
	assert.Contains(t, cmd, `&& export XAI_VERTEX_PROJECT_ID="${XAI_VERTEX_PROJECT_ID:-${ANTHROPIC_VERTEX_PROJECT_ID:-$GOOGLE_CLOUD_PROJECT}}"`,
		"project defaults to the fleet's Vertex project but does not override an explicit XAI_VERTEX_PROJECT_ID")
	assert.NotContains(t, cmd, "unset ANTHROPIC_API_KEY", "anthropic env hygiene does not fire for xai-vertex")
	assert.NotContains(t, cmd, "-e '"+sandbox.SandboxPiExtensionsDir+"/anthropic-vertex'", "anthropic-vertex extension is not loaded")

	// Long form: xai-vertex/xai/grok-4.6 passes through.
	params.Model = "xai-vertex/xai/grok-4.6"
	cmd = buildPiRunCommand(params, m)
	assert.Contains(t, cmd, "--model 'xai-vertex/xai/grok-4.6'")
	assert.Contains(t, cmd, "-e '"+sandbox.SandboxPiExtensionsDir+"/xai-vertex'")
	assert.Contains(t, cmd, "&& unset XAI_API_KEY")

	// Case variants must all reach the gate. A short form that escapes
	// normalization would load no extension and leave XAI_API_KEY set,
	// silently sending traffic to xAI's native API instead of Vertex.
	for _, spec := range []string{"Xai-Vertex/xai/grok-4.6", "XAI/grok-4.6", "Xai/grok-4.6"} {
		params.Model = spec
		cmd = buildPiRunCommand(params, m)
		assert.Contains(t, cmd, "--model 'xai-vertex/xai/grok-4.6'", "canonical spec for %s", spec)
		assert.Contains(t, cmd, "&& unset XAI_API_KEY", "XAI_API_KEY unset for %s", spec)
		assert.Contains(t, cmd, "-e '"+sandbox.SandboxPiExtensionsDir+"/xai-vertex'", "extension loaded for %s", spec)
	}

	// unset must run after the agent-writable .env is sourced, or the .env
	// could re-export XAI_API_KEY after we cleared it.
	params.Model = "xai/grok-4.6"
	cmd = buildPiRunCommand(params, m)
	assert.Less(t, strings.Index(cmd, ". '"+sandbox.SandboxWorkspace+"/.env'"), strings.Index(cmd, "&& unset XAI_API_KEY"),
		"XAI_API_KEY is unset after .env is sourced")
}

// TestTranslatePiModel_XaiVertexBareIDFromHarness covers the legacy harness
// path: a bare id plus FULLSEND_PI_PROVIDER, which predates harness `model:`
// accepting "/" (#6570) and must keep working.
func TestTranslatePiModel_XaiVertexBareIDFromHarness(t *testing.T) {
	t.Setenv(piProviderEnv, piXaiVertexProvider)
	for _, bare := range []string{"grok-4.6", "grok-4.5"} {
		spec := translatePiModel(bare)
		assert.Equal(t, "xai-vertex/xai/"+bare, spec)
		provider, _, _ := strings.Cut(spec, "/")
		assert.True(t, strings.EqualFold(provider, piXaiVertexProvider), "gate must fire for %s", bare)
		assert.Equal(t, "xai/"+bare, piBareModelID(spec), "wire id keeps the publisher segment")
	}
}

func TestBuildPiRunCommand_DirectProviderKeepsAnthropicEnv(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "anthropic")
	cmd := buildPiRunCommand(piTestParams(), &piManifest{})
	assert.Contains(t, cmd, "--model 'anthropic/claude-opus-4-6'")
	assert.NotContains(t, cmd, "unset ANTHROPIC_API_KEY", "direct Anthropic provider needs its key")
	assert.NotContains(t, cmd, "GOOGLE_CLOUD_PROJECT")
	assert.NotContains(t, cmd, "/usr/local/share/pi-extensions/anthropic-vertex", "the Vertex extension is only loaded for the anthropic-vertex provider")
}

func TestBuildPiRunCommand_HarnessOverridesAndFlags(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
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
	assert.Contains(t, cmd, "-e '/usr/local/share/pi-extensions/anthropic-vertex'")
	assert.True(t, strings.HasSuffix(cmd, "'Run the agent task' </dev/null 2>>'/sandbox/workspace/pi-debug.log'"), cmd)
}

func TestBuildPiRunCommand_EmptyToolRestriction(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	m := &piManifest{Tools: []string{}}
	cmd := buildPiRunCommand(piTestParams(), m)
	assert.Contains(t, cmd, "--no-builtin-tools")
	assert.NotContains(t, cmd, "--tools ")
}

func TestBuildPiRunCommand_QuotesRepoDirAndModel(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
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

func TestBuildPiRunCommand_HonoursPromptOverride(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	m := &piManifest{AgentName: "triage", Model: "opus"}

	params := piTestParams()
	cmd := buildPiRunCommand(params, m)
	assert.Contains(t, cmd, shellQuote(DefaultAgentPrompt), "empty prompt falls back to the default")

	// The validation loop injects the previous iteration's failure here; a
	// runtime that ignores it turns feedback_mode into a blind retry (#1050).
	params.Prompt = "Previous iteration failed: tests did not pass.\nFix it; don't repeat it."
	cmd = buildPiRunCommand(params, m)
	assert.Contains(t, cmd, shellQuote(params.Prompt))
	assert.NotContains(t, cmd, shellQuote(DefaultAgentPrompt))
	assert.True(t, strings.HasSuffix(cmd, "</dev/null"), "stdin stays closed")
}
