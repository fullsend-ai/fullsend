package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

	// The guard runs before the agent-writable .env is sourced; the
	// runner-owned locations are re-pinned right after it.
	assert.True(t, strings.HasPrefix(cmd, `cd '/sandbox/workspace/repo' && `+piBinaryPin()+` && `+piHooksGuard("/sandbox/pi-config/fullsend-hooks.js", "/sandbox/pi-config/fullsend-manifest.json")+` && . '/sandbox/workspace/.env' && `+strings.Join(PiRuntime{}.EnvExports(), " && ")+` && export FULLSEND_PI_MANIFEST='/sandbox/pi-config/fullsend-manifest.json' && export FULLSEND_RUNTIME=pi && export GOOGLE_CLOUD_LOCATION="${GOOGLE_CLOUD_LOCATION:-$CLOUD_ML_REGION}" && unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL ANTHROPIC_VERTEX_BASE_URL && export GOOGLE_CLOUD_PROJECT="${ANTHROPIC_VERTEX_PROJECT_ID:-$GOOGLE_CLOUD_PROJECT}" && "$FULLSEND_PI_BIN" --print --mode json`), cmd)
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
	piIdx := strings.Index(cmd, `&& "$FULLSEND_PI_BIN" `)
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

func TestBuildPiRunCommand_OpenAI(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	m := &piManifest{AgentName: "triage", Model: "opus", Tools: []string{"bash"}}
	params := piTestParams()

	// openai/gpt-5.6-luna passes through as a two-segment spec.
	params.Model = "openai/gpt-5.6-luna"
	cmd := buildPiRunCommand(params, m)

	assert.Contains(t, cmd, "--model 'openai/gpt-5.6-luna'", "model spec")
	assert.NotContains(t, cmd, "--api-key", "no --api-key: it would outrank the auth.json pi re-reads per request and pin the iteration to one placeholder")
	assert.Contains(t, cmd, "&& "+PiOpenAIAuthSeed(PiRuntime{}.ConfigDir()), "auth.json is seeded from the injected placeholder")
	assert.Contains(t, cmd, "&& unset OPENAI_BASE_URL AZURE_OPENAI_API_KEY OPENAI_API_KEY", "stray env vars and the env placeholder are cleared")
	// Config-dir guard: auth.json and models.json must not exist.
	assert.Contains(t, cmd, "auth.json", "config-dir guard checks auth.json")
	assert.Contains(t, cmd, "models.json", "config-dir guard checks models.json")
	assert.Contains(t, cmd, fmt.Sprintf("exit %d", piConfigTamperedExit), "config-dir guard uses its own exit code")
	// No Vertex hygiene or extensions.
	assert.NotContains(t, cmd, "unset ANTHROPIC_API_KEY", "anthropic env hygiene does not fire for openai")
	assert.NotContains(t, cmd, "unset XAI_API_KEY", "xai env hygiene does not fire for openai")
	assert.NotContains(t, cmd, "-e '"+sandbox.SandboxPiExtensionsDir+"/anthropic-vertex'", "no vertex extension for openai")
	assert.NotContains(t, cmd, "-e '"+sandbox.SandboxPiExtensionsDir+"/xai-vertex'", "no xai extension for openai")
	// No -e extension for openai (built-in provider).
	// The only -e should be the hooks extension if enabled.

	// Case-insensitive gate: pi resolves providers case-insensitively.
	for _, spec := range []string{"OpenAI/gpt-5.6-luna", "OPENAI/gpt-5.6-luna", "Openai/gpt-5.6-sol"} {
		params.Model = spec
		cmd = buildPiRunCommand(params, m)
		assert.Contains(t, cmd, "&& "+PiOpenAIAuthSeed(PiRuntime{}.ConfigDir()), "seed for %s", spec)
		assert.Contains(t, cmd, "&& unset OPENAI_BASE_URL AZURE_OPENAI_API_KEY", "unset for %s", spec)
	}

	// The env hygiene runs after the agent-writable .env is sourced; the
	// config-dir guard runs before it (nothing can shadow `test` yet) and
	// again after it, behind `unset -f test`, in case .env wrote a file.
	params.Model = "openai/gpt-5.6-luna"
	cmd = buildPiRunCommand(params, m)
	envIdx := strings.Index(cmd, ". '"+sandbox.SandboxWorkspace+"/.env'")
	unsetIdx := strings.Index(cmd, "&& unset OPENAI_BASE_URL")
	assert.Less(t, envIdx, unsetIdx, "unset after .env sourced")
	guard := piOpenAIConfigGuard(PiRuntime{}.ConfigDir())
	assert.Equal(t, 2, strings.Count(cmd, guard), "guard runs twice")
	firstGuard := strings.Index(cmd, guard)
	secondGuard := strings.LastIndex(cmd, guard)
	assert.Less(t, firstGuard, envIdx, "first guard before .env sourced")
	assert.Less(t, envIdx, secondGuard, "second guard after .env sourced")
	assert.Less(t, strings.Index(cmd, "&& unset -f test command grep tr sed printf pi"), secondGuard, "unset -f precedes the second guard")
	assert.Greater(t, strings.Index(cmd, "&& unset -f test command grep tr sed printf pi"), envIdx, "unset -f after .env sourced")
	assert.Less(t, strings.Index(cmd, "&& "+piBinaryPin()), envIdx, "the pi binary is pinned before .env")
	assert.Contains(t, cmd, `&& "$FULLSEND_PI_BIN" --print`, "pi is launched by its pinned path")
	seedIdx := strings.Index(cmd, "&& "+PiOpenAIAuthSeed(PiRuntime{}.ConfigDir()))
	assert.Less(t, seedIdx, envIdx, "auth.json is seeded before .env can touch OPENAI_API_KEY")
	assert.Greater(t, seedIdx, firstGuard, "after the first guard")
	// .env cannot relocate pi's config dir: the runner re-pins it afterwards.
	pin := strings.Index(cmd, "&& export PI_CODING_AGENT_DIR="+PiRuntime{}.ConfigDir())
	assert.Greater(t, pin, envIdx, "PI_CODING_AGENT_DIR re-exported after .env")
	assert.Less(t, pin, secondGuard, "and before the second guard checks that dir")
}

// TestPiBinaryPin runs the pin under a real sh: pi resolves to the real
// binary before .env, a pi() function or a PATH swap in .env changes
// nothing, and an attempt to reassign the pinned path aborts the sourcing.
func TestPiBinaryPin(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\necho REAL\n"), 0o755))
	launch := func(envBody string) (string, error) {
		env := filepath.Join(t.TempDir(), ".env")
		require.NoError(t, os.WriteFile(env, []byte(envBody), 0o644))
		cmd := exec.Command("sh", "-c", piBinaryPin()+" && . "+shellQuote(env)+` && unset -f pi && "$`+piBinaryVar+`"`)
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	out, err := launch("pi() { echo FAKE; }\nexport PATH=/nonexistent\n")
	require.NoError(t, err, out)
	assert.Equal(t, "REAL", out, "a function and a PATH swap in .env do not change which pi runs")

	out, err = launch(piBinaryVar + "=/bin/false\n")
	require.Error(t, err, "reassigning the pinned path aborts: %s", out)
	assert.NotContains(t, out, "REAL")
}

// TestPiOpenAIAuthSeed runs the seed under a real sh: a gateway placeholder
// lands in auth.json in exactly the shape the config guard accepts; anything
// else is refused.
func TestPiOpenAIAuthSeed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pi-config")
	seed := PiOpenAIAuthSeed(dir)
	auth := filepath.Join(dir, "auth.json")

	cmd := exec.Command("sh", "-c", seed)
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+piPlaceholderPrefix+"v7571978000873942056_OPENAI_API_KEY")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	data, err := os.ReadFile(auth)
	require.NoError(t, err)
	assert.Equal(t, `{"openai":{"type":"api_key","key":"`+piPlaceholderPrefix+`v7571978000873942056_OPENAI_API_KEY"}}`+"\n", string(data))
	_, err = os.Stat(auth + ".fullsend")
	assert.True(t, os.IsNotExist(err), "the temp file is renamed away")

	// The seeded file is what the guard accepts, whitespace or not.
	require.NoError(t, exec.Command("sh", "-c", piOpenAIConfigGuard(dir)).Run(), "seeded auth.json passes the guard")
	require.NoError(t, os.WriteFile(auth, []byte("{\n  \"openai\": {\n    \"type\": \"api_key\",\n    \"key\": \""+piPlaceholderPrefix+"v1_OPENAI_API_KEY\"\n  }\n}\n"), 0o644))
	require.NoError(t, exec.Command("sh", "-c", piOpenAIConfigGuard(dir)).Run(), "a pretty-printed seed passes the guard")
	// Re-seeding replaces whatever pi or an agent left there.
	cmd = exec.Command("sh", "-c", seed)
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+piPlaceholderPrefix+"v2_OPENAI_API_KEY")
	require.NoError(t, cmd.Run())
	data, _ = os.ReadFile(auth)
	assert.Contains(t, string(data), "v2_OPENAI_API_KEY")

	// A value that is not a gateway placeholder (a raw key that reached the
	// sandbox by another route), an unset variable, or a placeholder with
	// characters that could break out of the JSON string are refused, and
	// nothing is written.
	require.NoError(t, os.Remove(auth))
	for _, in := range []string{"sk-local-static", "", piPlaceholderPrefix + "GH_TOKEN", piPlaceholderPrefix + `v1_OPENAI_API_KEY"}}`} {
		cmd := exec.Command("sh", "-c", seed)
		cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+in)
		out, err := cmd.CombinedOutput()
		require.Error(t, err, "%q must be refused: %s", in, out)
		_, statErr := os.Stat(auth)
		assert.True(t, os.IsNotExist(statErr), "nothing written for %q", in)
	}

	// The seed runs before .env is sourced, so a .env that swaps
	// OPENAI_API_KEY afterwards does not reach the file.
	env := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(env, []byte("export OPENAI_API_KEY="+piPlaceholderPrefix+"GH_TOKEN\n"), 0o644))
	cmd = exec.Command("sh", "-c", seed+" && . "+shellQuote(env))
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY="+piPlaceholderPrefix+"v3_OPENAI_API_KEY")
	require.NoError(t, cmd.Run())
	data, _ = os.ReadFile(auth)
	assert.Contains(t, string(data), "v3_OPENAI_API_KEY")
}

// TestPiOpenAIConfigGuard_ShadowedTest proves the post-.env pass: a sourced
// file that redefines `test` to always succeed cannot hide a planted
// auth.json once `unset -f test` has run.
func TestPiOpenAIConfigGuard_ShadowedTest(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"openai":{"type":"api_key","key":"sk-planted"}}`), 0o644))
	env := filepath.Join(t.TempDir(), ".env")
	// Every tool the guard uses, shadowed the way a hostile .env would.
	require.NoError(t, os.WriteFile(env, []byte("test() { return 1; }\ncommand() { return 1; }\ngrep() { return 1; }\ntr() { return 1; }\n"), 0o644))

	shadowed := exec.Command("sh", "-c", ". "+shellQuote(env)+" && "+piOpenAIConfigGuard(dir)+" && echo RAN")
	out, err := shadowed.CombinedOutput()
	require.NoError(t, err, "without unset -f the shadowed builtins let the guard pass: %s", out)

	guarded := exec.Command("sh", "-c", ". "+shellQuote(env)+" && unset -f test command grep tr && "+piOpenAIConfigGuard(dir)+" && echo RAN")
	out, err = guarded.CombinedOutput()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "output: %s", out)
	assert.Equal(t, piConfigTamperedExit, exitErr.ExitCode())
	assert.NotContains(t, string(out), "RAN")
}

func TestTranslatePiModel_OpenAI(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	// openai/gpt-5.6-luna is a two-segment spec; it passes through.
	assert.Equal(t, "openai/gpt-5.6-luna", translatePiModel("openai/gpt-5.6-luna"))
	// Case variants pass through too (the gate in buildPiRunCommand is
	// case-insensitive, so they are safe).
	assert.Equal(t, "OpenAI/gpt-5.6-luna", translatePiModel("OpenAI/gpt-5.6-luna"))

	// A bare id under FULLSEND_PI_PROVIDER=openai gets the prefix.
	t.Setenv(piProviderEnv, piOpenAIProvider)
	assert.Equal(t, "openai/gpt-5.6-luna", translatePiModel("gpt-5.6-luna"))
}

func TestPiOpenAIConfigGuard(t *testing.T) {
	t.Parallel()
	if err := exec.Command("sh", "-c", "echo ok").Run(); err != nil {
		t.Skip("sh not available")
	}

	dir := t.TempDir()

	run := func() (int, string) {
		cmd := exec.Command("sh", "-c", piOpenAIConfigGuard(dir)+" && echo RAN")
		out, err := cmd.CombinedOutput()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return code, string(out)
	}

	// No auth.json or models.json: passes.
	code, out := run()
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "RAN")

	// pi writes an empty auth.json on every start: passes.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{}\n"), 0o644))
	code, out = run()
	assert.Equal(t, 0, code, "pi's own empty auth.json is not tampering")
	assert.Contains(t, out, "RAN")

	// An openai entry in auth.json would supply a different key: fails.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"openai":{"type":"api_key","key":"sk-planted"}}`), 0o644))
	code, out = run()
	assert.Equal(t, piConfigTamperedExit, code, "auth.json with an openai entry")
	assert.NotContains(t, out, "RAN")
	assert.Contains(t, out, "placeholder-leak risk")
	require.NoError(t, os.Remove(filepath.Join(dir, "auth.json")))

	// models.json present: fails.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models.json"), []byte("{}"), 0o644))
	code, out = run()
	assert.Equal(t, piConfigTamperedExit, code, "models.json present")
	assert.NotContains(t, out, "RAN")
	require.NoError(t, os.Remove(filepath.Join(dir, "models.json")))

	// A key that unescapes to openai: fails (substring check, escape rule).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"open\u0061i":{"type":"api_key","key":"sk-planted"}}`), 0o644))
	code, _ = run()
	assert.Equal(t, piConfigTamperedExit, code, "escaped openai key")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"OpenAI":{"type":"api_key","key":"sk-planted"}}`), 0o644))
	code, _ = run()
	assert.Equal(t, piConfigTamperedExit, code, "case-varied openai key")
	// auth.json is runner-owned in the sandbox: an entry for any other
	// provider is not something pi or the runner writes, so it fails too.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"anthropic":{"type":"api_key","key":"sk-a..."}}`), 0o644))
	code, out = run()
	assert.Equal(t, piConfigTamperedExit, code, "an unrelated provider entry is not the seeded shape")
	assert.NotContains(t, out, "RAN")
	require.NoError(t, os.Remove(filepath.Join(dir, "auth.json")))

	// Both present: fails.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"openai":{}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models.json"), []byte("{}"), 0o644))
	code, _ = run()
	assert.Equal(t, piConfigTamperedExit, code, "both present")
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
