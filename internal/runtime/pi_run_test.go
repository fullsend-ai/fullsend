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

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

func TestTranslatePiModel(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", translatePiModel("opus", nil))
	assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", translatePiModel("sonnet", nil))
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", translatePiModel("haiku", nil))
	assert.Equal(t, "anthropic-vertex/claude-fable-5-1", translatePiModel("fable", nil))
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", translatePiModel("", nil), "empty falls back to the opus alias")
	assert.Equal(t, "anthropic-vertex/claude-opus-4-8", translatePiModel("claude-opus-4-8", nil), "bare ids get the provider prefix")
	assert.Equal(t, "anthropic/claude-sonnet-4-6", translatePiModel("anthropic/claude-sonnet-4-6", nil), "provider/id passes through")

	// xai/ normalization: "xai/grok-4.6" becomes "xai-vertex/xai/grok-4.6"
	// so the provider gate in buildPiRunCommand fires correctly.
	assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel("xai/grok-4.6", nil), "xai/ is normalized to xai-vertex/xai/")
	assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel("xai-vertex/xai/grok-4.6", nil), "already-normalized three-segment spec passes through")

	// Case-insensitive, because the gate in buildPiRunCommand is: a spec that
	// escapes normalization reaches pi's built-in xai provider with
	// XAI_API_KEY still set, which is the failure #6571 exists to close.
	for _, spec := range []string{"XAI/grok-4.6", "Xai/grok-4.6", "xAI/grok-4.6"} {
		assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel(spec, nil), "case-varied short form is still normalized: %s", spec)
	}
	for _, spec := range []string{"XAI-VERTEX/xai/grok-4.6", "Xai-Vertex/XAI/grok-4.6"} {
		assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel(spec, nil), "case-varied long form is canonicalised: %s", spec)
	}

	// A bare id under FULLSEND_PI_PROVIDER=xai-vertex must still get the
	// publisher segment. Before harness `model:` accepted "/" (#6570) this
	// was the only way a harness reached Grok, and it stays supported --
	// the two-segment "xai-vertex/grok-4.6" is a model the extension
	// does not register, which pi silently substitutes a fallback for.
	t.Setenv(piProviderEnv, piXaiVertexProvider)
	assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel("grok-4.6", nil), "bare id gets the publisher segment too")
	assert.Equal(t, "xai-vertex/xai/grok-4.6", translatePiModel("xai/grok-4.6", nil), "short form is unaffected by the provider env")

	t.Setenv(piProviderEnv, "anthropic")
	assert.Equal(t, "anthropic/claude-opus-4-6", translatePiModel("opus", nil))

	// The model override is resolved by the CLI (--model, FULLSEND_MODEL,
	// FULLSEND_PI_MODEL) and arrives as the model argument; the runtime no
	// longer reads FULLSEND_PI_MODEL itself.
	t.Setenv("FULLSEND_PI_MODEL", "google-vertex/gemini-2.5-pro")
	assert.Equal(t, "anthropic/claude-opus-4-6", translatePiModel("opus", nil), "runtime ignores FULLSEND_PI_MODEL")
	assert.Equal(t, "google-vertex/gemini-2.5-pro", translatePiModel("google-vertex/gemini-2.5-pro", nil))
	t.Setenv(piProviderEnv, "")
	assert.Equal(t, "anthropic-vertex/claude-opus-4-8", translatePiModel("claude-opus-4-8", nil), "a bare override still gets the provider prefix")
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
	cmd := buildPiRunCommand(params, m, nil, "")

	// The guard runs before the agent-writable .env is sourced; the
	// runner-owned locations are re-pinned right after it.
	assert.True(t, strings.HasPrefix(cmd, `cd '/sandbox/workspace/repo' && `+piBinaryPin()+` && `+piHooksGuard("/sandbox/pi-config/fullsend-hooks.js", "/sandbox/pi-config/fullsend-manifest.json")+` && . '/sandbox/workspace/.env' && `+piLoaderEnvUnset()+` && `+strings.Join(PiRuntime{}.EnvExports(), " && ")+` && export FULLSEND_PI_MANIFEST='/sandbox/pi-config/fullsend-manifest.json' && export FULLSEND_RUNTIME=pi && export GOOGLE_CLOUD_LOCATION="${GOOGLE_CLOUD_LOCATION:-$CLOUD_ML_REGION}" && unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL ANTHROPIC_VERTEX_BASE_URL && export GOOGLE_CLOUD_PROJECT="${ANTHROPIC_VERTEX_PROJECT_ID:-$GOOGLE_CLOUD_PROJECT}" && "$FULLSEND_PI_BIN" --print --mode json`), cmd)
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
	cmd = buildPiRunCommand(params, m, nil, "")
	assert.Contains(t, cmd, "&& unset ANTHROPIC_API_KEY")
	assert.Contains(t, cmd, "--model 'Anthropic-Vertex/claude-opus-4-6'")
}

// TestBuildPiRunCommand_AgentTool is the golden for the Agent tool wiring:
// the extension's integrity guard runs before .env whether or not hooks are
// on, its -e comes right after the hook adapter and before declared
// extensions, --tools carries Agent,Task, and the parent's own provider
// -e set is unchanged (the manifest's child extension list is for children).
func TestBuildPiRunCommand_AgentTool(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	agentExt := "/sandbox/pi-config/fullsend-agent.js"
	hooksExt := "/sandbox/pi-config/fullsend-hooks.js"
	agent := &piAgentManifest{Enabled: true, Extensions: []string{piVertexExtensionPath, piXaiVertexExtensionPath, hooksExt}}
	declared := []piManifestExtension{{Name: "go-diagnostics", Path: "/sandbox/pi-config/extensions/go-diagnostics", SHA256: strings.Repeat("a", 64)}}

	// Hooks on, default tool set.
	m := &piManifest{AgentName: "review", Hooks: &piHooksManifest{}, Agent: agent}
	params := piTestParams()
	params.HooksSettingsPath = "/sandbox/claude-config/hooks.json"
	cmd := buildPiRunCommand(params, m, declared, "")
	assert.Contains(t, cmd, "-e '"+hooksExt+"' -e '"+agentExt+"' -e '/sandbox/pi-config/extensions/go-diagnostics'",
		"load order: hook adapter, Agent extension, declared extensions")
	assert.Contains(t, cmd, "-e '"+piVertexExtensionPath+"' -e '"+hooksExt+"'", "the provider extension still comes first")
	assert.NotContains(t, cmd, "-e '"+piXaiVertexExtensionPath+"'", "the parent loads only its own provider; the child list in the manifest is not the parent's")
	assert.NotContains(t, cmd, "--tools", "no tools: frontmatter → pi's default set plus the extension's tools")
	guard := piAgentGuard(agentExt)
	assert.Contains(t, cmd, "&& "+piHooksGuard(hooksExt, "/sandbox/pi-config/fullsend-manifest.json")+" && "+guard+" && "+piExtensionsGuard(declared),
		"the runner-owned guards run in order, ahead of the declared-extension guard")
	assert.Contains(t, cmd, piExtensionsGuard(declared)+" && . '/sandbox/workspace/.env'",
		"every guard runs before the agent-writable .env is sourced")
	assert.Contains(t, guard, fmt.Sprintf("exit %d", piAgentTamperedExit), "its own code, so Run names this extension and not the hook adapter")
	assert.NotContains(t, guard, fmt.Sprintf("exit %d", piHooksMissingExit))
	assert.Contains(t, guard, "command -p sha256sum")
	sum := sha256.Sum256(piAgentExtensionJS)
	assert.Contains(t, guard, hex.EncodeToString(sum[:]), "the guard pins the embedded copy's hash")

	// Hooks off: the Agent guard and -e still apply, on their own.
	params.HooksSettingsPath = ""
	cmd = buildPiRunCommand(params, m, nil, "")
	assert.NotContains(t, cmd, hooksExt)
	assert.Contains(t, cmd, "&& "+guard+" && . '/sandbox/workspace/.env'")
	assert.Contains(t, cmd, "-e '"+piVertexExtensionPath+"' -e '"+agentExt+"' --model")

	// A declared tools: list naming Agent carries both names into --tools.
	m.Tools = []string{"bash", "read", "Agent", "Task"}
	cmd = buildPiRunCommand(params, m, nil, "")
	assert.Contains(t, cmd, "--tools 'bash,read,Agent,Task'")

	// Tool off: nothing of it in the command line.
	off := &piManifest{AgentName: "triage", Tools: []string{"bash"}, Hooks: &piHooksManifest{}}
	params.HooksSettingsPath = "/sandbox/claude-config/hooks.json"
	cmd = buildPiRunCommand(params, off, nil, "")
	assert.NotContains(t, cmd, "fullsend-agent")
	assert.Contains(t, cmd, "&& "+piHooksGuard(hooksExt, "/sandbox/pi-config/fullsend-manifest.json")+" && . '/sandbox/workspace/.env'")
	disabled := &piManifest{AgentName: "triage", Agent: &piAgentManifest{Enabled: false}}
	assert.NotContains(t, buildPiRunCommand(params, disabled, nil, ""), "fullsend-agent")
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

// TestPiAgentGuard runs the rendered Agent-extension guard under a real sh:
// like the hook adapter it must exit before pi starts when the file is
// missing or is not the embedded copy, and the tools it uses must not be
// shadowable from the agent-writable .env.
func TestPiAgentGuard(t *testing.T) {
	t.Parallel()
	if err := exec.Command("sh", "-c", "command -p sha256sum /dev/null >/dev/null && command -p cut -d' ' -f1 /dev/null").Run(); err != nil {
		t.Skip("sha256sum/cut not on the default PATH (stock macOS); the sandbox image has coreutils")
	}
	dir := t.TempDir()
	ext := filepath.Join(dir, "fullsend-agent.js")

	run := func(prefix string) (int, string) {
		out, err := exec.Command("sh", "-c", prefix+piAgentGuard(ext)+" && echo RAN").CombinedOutput()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			require.NoError(t, err, string(out))
		}
		return code, string(out)
	}

	code, out := run("")
	assert.Equal(t, piAgentTamperedExit, code, "missing extension")
	assert.NotContains(t, out, "RAN")
	assert.Contains(t, out, "Agent extension missing or modified")

	require.NoError(t, os.WriteFile(ext, append(append([]byte{}, piAgentExtensionJS...), []byte("\n// tampered\n")...), 0o644))
	code, out = run("")
	assert.Equal(t, piAgentTamperedExit, code, "modified extension")
	assert.NotContains(t, out, "RAN")

	require.NoError(t, os.WriteFile(ext, piAgentExtensionJS, 0o644))
	code, out = run("")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "RAN")

	// A function or PATH entry standing in for sha256sum must not make a
	// tampered copy pass: the guard uses `command -p`.
	require.NoError(t, os.WriteFile(ext, []byte("// tampered\n"), 0o644))
	sum := sha256.Sum256(piAgentExtensionJS)
	hexSum := hex.EncodeToString(sum[:])
	shadowDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(shadowDir, "sha256sum"), []byte("#!/bin/sh\necho '"+hexSum+"  x'\n"), 0o755))
	code, out = run("sha256sum() { echo '" + hexSum + "  x'; }; cut() { echo '" + hexSum + "'; }; PATH=" + shellQuote(shadowDir) + ":$PATH; ")
	assert.Equal(t, piAgentTamperedExit, code, "shadowed sha256sum")
	assert.NotContains(t, out, "RAN")

	// Each runner-owned artifact has its own code, so Run names the one that
	// failed instead of listing every guard the command line carries.
	assert.NotEqual(t, piHooksMissingExit, piAgentTamperedExit)
	for _, other := range []int{piManifestTamperedExit, piExtensionTamperedExit, piConfigTamperedExit} {
		assert.NotEqual(t, other, piAgentTamperedExit, "the Agent guard's exit code must not collide with another guard's")
	}
}

// TestPiManifestGuard covers the manifest integrity check: the manifest
// names the pi binary children run, the -e list they load and their tool
// allowlists, and the config dir is writable by the agent between
// iterations — so Run refuses to start on a manifest that is not the one
// Bootstrap wrote.
func TestPiManifestGuard(t *testing.T) {
	t.Parallel()
	if err := exec.Command("sh", "-c", "command -p sha256sum /dev/null >/dev/null && command -p cut -d' ' -f1 /dev/null").Run(); err != nil {
		t.Skip("sha256sum/cut not on the default PATH (stock macOS); the sandbox image has coreutils")
	}
	dir := t.TempDir()
	manifest := filepath.Join(dir, "fullsend-manifest.json")
	body := []byte(`{"agentName":"triage"}`)
	sum := sha256.Sum256(body)
	guard := piManifestGuard(manifest, hex.EncodeToString(sum[:]))

	run := func() (int, string) {
		out, err := exec.Command("sh", "-c", guard+" && echo RAN").CombinedOutput()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			require.NoError(t, err, string(out))
		}
		return code, string(out)
	}

	code, out := run()
	assert.Equal(t, piManifestTamperedExit, code, "missing manifest")
	assert.NotContains(t, out, "RAN")
	assert.Contains(t, out, "manifest missing or modified")

	require.NoError(t, os.WriteFile(manifest, body, 0o644))
	code, out = run()
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "RAN")

	require.NoError(t, os.WriteFile(manifest, []byte(`{"agentName":"triage","agent":{"enabled":true,"piBin":"/tmp/evil"}}`), 0o644))
	code, out = run()
	assert.Equal(t, piManifestTamperedExit, code, "a rewritten manifest is refused")
	assert.NotContains(t, out, "RAN")
}

// TestBuildPiRunCommand_ManifestGuard checks where the guard is emitted:
// before .env can shadow the tools it uses, and again after .env, since
// .env could have rewritten the manifest between the two.
func TestBuildPiRunCommand_ManifestGuard(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	m := &piManifest{AgentName: "triage", Model: "opus"}
	params := piTestParams()

	assert.NotContains(t, buildPiRunCommand(params, m, nil, ""), "manifest missing or modified",
		"no recorded digest (Bootstrap did not run in this process) means no guard, not a failed run")

	cmd := buildPiRunCommand(params, m, nil, "abc123")
	assert.Equal(t, 2, strings.Count(cmd, "manifest missing or modified"), "checked before and after .env")
	first := strings.Index(cmd, "manifest missing or modified")
	envSource := strings.Index(cmd, ". '"+sandbox.SandboxWorkspace+"/.env'")
	assert.Less(t, first, envSource, "the first check runs before the agent-writable .env is sourced")
	assert.Contains(t, cmd, "unset -f test [ command sha256sum cut",
		"the second check restores the real tools first; unset is a special builtin")
	assert.Contains(t, cmd, "= 'abc123' ]")

	// The digest is handed to the extensions so a process that loads the
	// manifest later in the iteration — a sub-agent's hook adapter — can
	// re-check it. Exported after .env, so .env cannot set or clear it, and
	// after the second guard, so it can only carry a digest that matched.
	export := strings.Index(cmd, "export "+piManifestSumEnv+"='abc123'")
	require.Positive(t, export, "the digest is exported for the hook adapter to re-check")
	assert.Greater(t, export, envSource, "after .env, which cannot then set it")
	assert.Greater(t, export, strings.LastIndex(cmd, "manifest missing or modified"),
		"after the post-.env guard, so it only ever carries a digest that just matched")

	assert.NotContains(t, buildPiRunCommand(params, m, nil, ""), piManifestSumEnv,
		"no recorded digest means nothing to re-check against either")
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
	cmd := buildPiRunCommand(params, m, nil, "")

	assert.Contains(t, cmd, "--model 'xai-vertex/xai/grok-4.6'", "normalized model spec")
	assert.Contains(t, cmd, "-e '"+sandbox.SandboxPiExtensionsDir+"/xai-vertex'", "xai-vertex extension is loaded")
	assert.Contains(t, cmd, "&& unset XAI_API_KEY", "XAI_API_KEY is unset")
	assert.Contains(t, cmd, `&& export XAI_VERTEX_PROJECT_ID="${XAI_VERTEX_PROJECT_ID:-${ANTHROPIC_VERTEX_PROJECT_ID:-$GOOGLE_CLOUD_PROJECT}}"`,
		"project defaults to the fleet's Vertex project but does not override an explicit XAI_VERTEX_PROJECT_ID")
	assert.NotContains(t, cmd, "unset ANTHROPIC_API_KEY", "anthropic env hygiene does not fire for xai-vertex")
	assert.NotContains(t, cmd, "-e '"+sandbox.SandboxPiExtensionsDir+"/anthropic-vertex'", "anthropic-vertex extension is not loaded")

	// Long form: xai-vertex/xai/grok-4.6 passes through.
	params.Model = "xai-vertex/xai/grok-4.6"
	cmd = buildPiRunCommand(params, m, nil, "")
	assert.Contains(t, cmd, "--model 'xai-vertex/xai/grok-4.6'")
	assert.Contains(t, cmd, "-e '"+sandbox.SandboxPiExtensionsDir+"/xai-vertex'")
	assert.Contains(t, cmd, "&& unset XAI_API_KEY")

	// Case variants must all reach the gate. A short form that escapes
	// normalization would load no extension and leave XAI_API_KEY set,
	// silently sending traffic to xAI's native API instead of Vertex.
	for _, spec := range []string{"Xai-Vertex/xai/grok-4.6", "XAI/grok-4.6", "Xai/grok-4.6"} {
		params.Model = spec
		cmd = buildPiRunCommand(params, m, nil, "")
		assert.Contains(t, cmd, "--model 'xai-vertex/xai/grok-4.6'", "canonical spec for %s", spec)
		assert.Contains(t, cmd, "&& unset XAI_API_KEY", "XAI_API_KEY unset for %s", spec)
		assert.Contains(t, cmd, "-e '"+sandbox.SandboxPiExtensionsDir+"/xai-vertex'", "extension loaded for %s", spec)
	}

	// unset must run after the agent-writable .env is sourced, or the .env
	// could re-export XAI_API_KEY after we cleared it.
	params.Model = "xai/grok-4.6"
	cmd = buildPiRunCommand(params, m, nil, "")
	assert.Less(t, strings.Index(cmd, ". '"+sandbox.SandboxWorkspace+"/.env'"), strings.Index(cmd, "&& unset XAI_API_KEY"),
		"XAI_API_KEY is unset after .env is sourced")
}

// TestTranslatePiModel_XaiVertexBareIDFromHarness covers the legacy harness
// path: a bare id plus FULLSEND_PI_PROVIDER, which predates harness `model:`
// accepting "/" (#6570) and must keep working.
func TestTranslatePiModel_XaiVertexBareIDFromHarness(t *testing.T) {
	t.Setenv(piProviderEnv, piXaiVertexProvider)
	for _, bare := range []string{"grok-4.6", "grok-4.5"} {
		spec := translatePiModel(bare, nil)
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
	cmd := buildPiRunCommand(params, m, nil, "")

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
		cmd = buildPiRunCommand(params, m, nil, "")
		assert.Contains(t, cmd, "&& "+PiOpenAIAuthSeed(PiRuntime{}.ConfigDir()), "seed for %s", spec)
		assert.Contains(t, cmd, "&& unset OPENAI_BASE_URL AZURE_OPENAI_API_KEY", "unset for %s", spec)
	}

	// The env hygiene runs after the agent-writable .env is sourced; the
	// config-dir guard runs before it (nothing can shadow `test` yet) and
	// again after it, behind `unset -f test`, in case .env wrote a file.
	params.Model = "openai/gpt-5.6-luna"
	cmd = buildPiRunCommand(params, m, nil, "")
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
	assert.Equal(t, "openai/gpt-5.6-luna", translatePiModel("openai/gpt-5.6-luna", nil))
	// Case variants pass through too (the gate in buildPiRunCommand is
	// case-insensitive, so they are safe).
	assert.Equal(t, "OpenAI/gpt-5.6-luna", translatePiModel("OpenAI/gpt-5.6-luna", nil))

	// A bare id under FULLSEND_PI_PROVIDER=openai gets the prefix.
	t.Setenv(piProviderEnv, piOpenAIProvider)
	assert.Equal(t, "openai/gpt-5.6-luna", translatePiModel("gpt-5.6-luna", nil))
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
	cmd := buildPiRunCommand(piTestParams(), &piManifest{}, nil, "")
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
	cmd := buildPiRunCommand(params, m, nil, "")

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
	cmd := buildPiRunCommand(piTestParams(), m, nil, "")
	assert.Contains(t, cmd, "--no-builtin-tools")
	assert.NotContains(t, cmd, "--tools ")
}

func TestBuildPiRunCommand_QuotesRepoDirAndModel(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	params := piTestParams()
	params.RepoDir = "/sandbox/workspace/it's"
	params.Model = "anthropic/claude'x"
	cmd := buildPiRunCommand(params, &piManifest{}, nil, "")
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
	cmd := buildPiRunCommand(params, &piManifest{AgentName: "triage", Model: "opus"}, nil, "")
	assert.Contains(t, cmd, "--thinking 'high'", "unknown effort falls back to the default, not to pi's medium")
}

// TestPiModelAliases_CoversDocumentedAliases asserts that every alias in the
// documented vocabulary has a piModelAliases entry that resolves to a
// provider/id spec (not a bare passthrough). This catches a missing mapping
// locally rather than in a live run where pi silently substitutes a fallback.
func TestPiModelAliases_CoversDocumentedAliases(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	for alias := range piDocumentedAliases {
		t.Run(alias, func(t *testing.T) {
			id, ok := piModelAliases[alias]
			require.True(t, ok, "documented alias %q missing from piModelAliases", alias)
			require.NotEmpty(t, id, "piModelAliases[%q] must not be empty", alias)
			spec := translatePiModel(alias, nil)
			require.Contains(t, spec, "/", "translated spec must be provider/id")
			require.NotEqual(t, piDefaultProvider+"/"+alias, spec,
				"alias %q should not pass through as bare id", alias)
		})
	}
}

func TestValidatePiModel(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	// All documented aliases pass validation (they are in piModelAliases).
	for alias := range piDocumentedAliases {
		assert.NoError(t, validatePiModel(alias, nil), "documented alias %q should pass validation", alias)
	}
	// Empty model (defaults to piDefaultModel) passes.
	assert.NoError(t, validatePiModel("", nil))
	// Bare ids that are not documented aliases pass (they are catalog ids).
	assert.NoError(t, validatePiModel("claude-opus-4-8", nil))
	// Provider/id specs pass without alias checking.
	assert.NoError(t, validatePiModel("anthropic/claude-sonnet-4-6", nil))
	assert.NoError(t, validatePiModel("xai/grok-4.6", nil))
}

// TestValidatePiModel_MissingMapping exercises the actual failure the guard
// exists to catch: a documented alias with no piModelAliases entry. It
// mutates the package-level map rather than relying on a real gap, so the
// test stays valid even after every current alias is mapped.
func TestValidatePiModel_MissingMapping(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	const alias = "fable"
	id, ok := piModelAliases[alias]
	require.True(t, ok, "test fixture assumes %q starts mapped", alias)
	delete(piModelAliases, alias)
	t.Cleanup(func() { piModelAliases[alias] = id })

	err := validatePiModel(alias, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), alias)
	assert.Contains(t, err.Error(), "documented but has no pi mapping")
}

func TestBuildPiRunCommand_HonoursPromptOverride(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	m := &piManifest{AgentName: "triage", Model: "opus"}

	params := piTestParams()
	cmd := buildPiRunCommand(params, m, nil, "")
	assert.Contains(t, cmd, shellQuote(DefaultAgentPrompt), "empty prompt falls back to the default")

	// The validation loop injects the previous iteration's failure here; a
	// runtime that ignores it turns feedback_mode into a blind retry (#1050).
	params.Prompt = "Previous iteration failed: tests did not pass.\nFix it; don't repeat it."
	cmd = buildPiRunCommand(params, m, nil, "")
	assert.Contains(t, cmd, shellQuote(params.Prompt))
	assert.NotContains(t, cmd, shellQuote(DefaultAgentPrompt))
	assert.True(t, strings.HasSuffix(cmd, "</dev/null"), "stdin stays closed")
}

// --- models.aliases tests (#6882) ---

func TestTranslatePiModel_WithConfigAlias(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	configAliases := map[string]string{"sonnet": "claude-sonnet-5"}

	// Config alias overrides the fleet default for sonnet.
	assert.Equal(t, "anthropic-vertex/claude-sonnet-5",
		translatePiModel("sonnet", configAliases),
		"config alias overrides fleet default")

	// Unstated aliases retain fleet defaults.
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6",
		translatePiModel("opus", configAliases),
		"unstated alias keeps fleet default")

	// Bare ids and provider/id specs are unaffected.
	assert.Equal(t, "anthropic-vertex/claude-opus-4-8",
		translatePiModel("claude-opus-4-8", configAliases),
		"bare id unaffected by config aliases")
	assert.Equal(t, "anthropic/claude-sonnet-4-6",
		translatePiModel("anthropic/claude-sonnet-4-6", configAliases),
		"provider/id passes through regardless of config aliases")

	// An alias may map to a provider/id spec (validation accepts it). The
	// alias is resolved first and the spec passes through untouched —
	// not re-prefixed to "anthropic-vertex/anthropic-vertex/…".
	assert.Equal(t, "anthropic-vertex/claude-sonnet-5",
		translatePiModel("sonnet", map[string]string{"sonnet": "anthropic-vertex/claude-sonnet-5"}),
		"provider/id alias value is not re-prefixed")
	assert.Equal(t, "google-vertex/gemini-3.7-flash",
		translatePiModel("haiku", map[string]string{"haiku": "google-vertex/gemini-3.7-flash"}),
		"an alias can retarget to another provider")

	// An alias mapped to Grok goes through the same xai normalisation as
	// a direct spec, so buildPiRunCommand's xai-vertex gate fires.
	for _, val := range []string{"xai/grok-4.6", "xai-vertex/xai/grok-4.6", "XAI/grok-4.6"} {
		assert.Equal(t, "xai-vertex/xai/grok-4.6",
			translatePiModel("sonnet", map[string]string{"sonnet": val}),
			"xai alias value is normalised: %s", val)
	}
}

func TestMergedPiModelAliases(t *testing.T) {
	t.Parallel()
	// nil config aliases returns the fleet defaults unchanged — as a copy,
	// so a caller cannot mutate the package-level table through it.
	merged := mergedPiModelAliases(nil)
	assert.Equal(t, piModelAliases, merged)
	merged["opus"] = "mutated"
	assert.NotEqual(t, "mutated", piModelAliases["opus"], "merged map must be a copy")

	// Config alias overrides per key.
	merged = mergedPiModelAliases(map[string]string{"sonnet": "claude-sonnet-5"})
	assert.Equal(t, "claude-sonnet-5", merged["sonnet"], "config override")
	assert.Equal(t, piModelAliases["opus"], merged["opus"], "fleet default preserved")
	assert.Equal(t, piModelAliases["haiku"], merged["haiku"], "fleet default preserved")
	assert.Equal(t, piModelAliases["fable"], merged["fable"], "fleet default preserved")
}

func TestValidatePiModel_WithConfigAlias(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	// Make the guard observable: with "sonnet" removed from the compiled-in
	// table, validation must fail without a config entry and pass with one
	// — proving the merged table, not just piModelAliases, is consulted.
	const alias = "sonnet"
	id, ok := piModelAliases[alias]
	require.True(t, ok, "test fixture assumes %q starts mapped", alias)
	delete(piModelAliases, alias)
	t.Cleanup(func() { piModelAliases[alias] = id })

	err := validatePiModel(alias, nil)
	require.Error(t, err, "no compiled-in entry and no config entry")
	assert.Contains(t, err.Error(), "documented but has no pi mapping")

	assert.NoError(t, validatePiModel(alias, map[string]string{alias: "claude-sonnet-5"}),
		"a config entry satisfies the guard on its own")
}

func TestBuildPiRunCommand_WithConfigAlias(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	m := &piManifest{AgentName: "triage", Model: "sonnet", Tools: []string{"bash"}}
	params := piTestParams()
	params.ModelAliases = map[string]string{"sonnet": "claude-sonnet-5"}
	cmd := buildPiRunCommand(params, m, nil)
	assert.Contains(t, cmd, "--model 'anthropic-vertex/claude-sonnet-5'",
		"config alias remaps the model in the build command")

	// An alias retargeted to Grok must trip the xai-vertex gate exactly as
	// a direct xai spec does: extension loaded, XAI_API_KEY unset. Before
	// the alias was resolved ahead of normalisation this produced
	// "anthropic-vertex/xai/grok-4.6" and skipped the gate.
	params.ModelAliases = map[string]string{"sonnet": "xai/grok-4.6"}
	cmd = buildPiRunCommand(params, m, nil)
	assert.Contains(t, cmd, "--model 'xai-vertex/xai/grok-4.6'", "xai alias value is normalised")
	assert.Contains(t, cmd, "&& unset XAI_API_KEY", "xai-vertex gate fires for an aliased Grok spec")
	assert.NotContains(t, cmd, "anthropic-vertex/xai", "no double prefix")
}

// TestPiDocumentedAliasesMatchConfigKeys pins the two hand-maintained
// copies of the alias vocabulary to each other: config validation accepts
// exactly the keys the pi runtime treats as aliases. Adding an alias to one
// side without the other would either reject a working alias in
// config.yaml or let a config key through that pi never resolves.
func TestPiDocumentedAliasesMatchConfigKeys(t *testing.T) {
	t.Parallel()
	want := config.ValidModelAliasKeys()
	got := make([]string, 0, len(piDocumentedAliases))
	for alias := range piDocumentedAliases {
		got = append(got, alias)
	}
	assert.ElementsMatch(t, want, got, "config.ValidModelAliasKeys and piDocumentedAliases drifted")
}

// TestBuildPiRunCommand_LoaderEnvHygiene pins the module-loader environment
// hygiene that runs on *every* provider path. The agent-writable .env can
// otherwise hand jiti a JITI_ALIAS map that swaps the file behind an `-e`
// path for another one (reproduced on pi 0.84.4: the bundled cli.js takes
// the isBundledNode branch of createJiti, which passes no `alias`, so jiti
// fills it from the environment) — the extension tree hash and the hook
// adapter's SHA-256 both stay clean because the source file is untouched.
// NODE_OPTIONS/NODE_PATH are the same class of hole and were only cleared
// on the openai path.
func TestBuildPiRunCommand_LoaderEnvHygiene(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	m := &piManifest{AgentName: "triage", Model: "opus"}
	envSource := ". '" + sandbox.SandboxWorkspace + "/.env'"

	for _, model := range []string{
		"opus",                  // anthropic-vertex
		"xai/grok-4",            // xai-vertex
		"openai/gpt-5.6-luna",   // built-in openai
		"anthropic/claude-opus", // a provider with no gate at all
	} {
		params := piTestParams()
		params.Model = model
		cmd := buildPiRunCommand(params, m, nil, "")

		assert.Contains(t, cmd, "&& "+piLoaderEnvUnset(), "loader env hygiene runs for %s", model)
		envIdx := strings.Index(cmd, envSource)
		require.GreaterOrEqual(t, envIdx, 0, ".env is sourced for %s", model)
		unsetIdx := strings.Index(cmd, "&& "+piLoaderEnvUnset())
		assert.Less(t, envIdx, unsetIdx, "the unset must come after .env for %s", model)
		// JITI_FS_CACHE is re-exported after the unset, not before it.
		assert.Less(t, unsetIdx, strings.Index(cmd, "export JITI_FS_CACHE=false"),
			"JITI_FS_CACHE is re-exported after the family is cleared, for %s", model)
	}

	// Every JITI_* name jiti 2.7.0 reads from the environment (jiti/dist/
	// jiti.cjs) is cleared, except JITI_FS_CACHE, which EnvExports pins.
	unset := piLoaderEnvUnset()
	for _, name := range []string{
		"JITI_ALIAS", "JITI_CACHE", "JITI_DEBUG", "JITI_ESM_EVAL_TEMP_FILE",
		"JITI_EXTENSIONS", "JITI_INTEROP_DEFAULT", "JITI_JSX", "JITI_MODULE_CACHE",
		"JITI_NATIVE_MODULES", "JITI_REBUILD_FS_CACHE", "JITI_REQUIRE_CACHE",
		"JITI_RESPECT_TMPDIR_ENV", "JITI_SOURCE_MAPS", "JITI_TRANSFORM_MODULES",
		"JITI_TRY_NATIVE", "JITI_TSCONFIG_PATHS",
		"NODE_OPTIONS", "NODE_PATH",
	} {
		assert.Contains(t, strings.Fields(unset), name, "%s is cleared", name)
	}
	assert.NotContains(t, strings.Fields(unset), "JITI_FS_CACHE", "JITI_FS_CACHE is pinned by EnvExports, not unset")

	// `unset` is a special builtin, so a function defined by .env cannot
	// stand in for it — but it still has to be spelled as one word.
	assert.True(t, strings.HasPrefix(unset, "unset "), "the fragment is a bare `unset` invocation: %q", unset)
}
