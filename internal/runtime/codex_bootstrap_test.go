package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// fakeOpenshellCodex installs a fake "openshell" that records every argv line
// to logPath, stores each upload payload under storeDir keyed by the remote
// path, answers `codex --version` with versionOutput verbatim and `cat <remote>`
// execs from that store,
// and streams streamFixture (when non-empty) for the codex run command.
// Everything else succeeds silently.
func fakeOpenshellCodex(t *testing.T, logPath, storeDir, versionOutput string, streamFixture ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(storeDir, 0o755))
	streamCase := "exit 0"
	if len(streamFixture) > 0 && streamFixture[0] != "" {
		streamCase = "cat '" + streamFixture[0] + "'; exit 0"
	}
	findCase := "exit 0"
	if len(streamFixture) > 1 && streamFixture[1] != "" {
		findCase = "echo '" + streamFixture[1] + "'; exit 0"
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> '` + logPath + `'
if [ -n "${FULLSEND_TEST_FAIL_MATCH:-}" ]; then
  case "$*" in
    *"$FULLSEND_TEST_FAIL_MATCH"*) echo "fake openshell: refusing $FULLSEND_TEST_FAIL_MATCH" >&2; exit 1 ;;
  esac
fi
if [ "$2" = "download" ]; then
  base=$(basename "$4")
  mkdir -p "$5" 2>/dev/null
  # A real rollout envelope, unless the remote path says it is planted: the
  # extractor now refuses anything that is not one.
  default_body='{"type":"session_meta","payload":{}}'
  case "$4" in
    *planted*) body='not a rollout' ;;
    *) body="${FULLSEND_TEST_DOWNLOAD_BODY:-$default_body}" ;;
  esac
  if [ -d "$5" ]; then printf '%s\n' "$body" > "$5/$base"; else printf '%s\n' "$body" > "$5"; fi
  exit 0
fi
if [ "$2" = "upload" ]; then
  cp "$4" '` + storeDir + `'/"$(printf '%s' "$5" | tr '/' '_')"
  exit 0
fi
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    "codex --version") echo "` + versionOutput + `"; exit 0 ;;
    "command -v python3") echo "/usr/bin/python3"; exit 0 ;;
    *"sys.version_info"*) echo "${FULLSEND_TEST_PYVER:-3.12}"; exit 0 ;;
    *fullsend-env-sep*) printf '%s' "${FULLSEND_TEST_ENV_READ-|fullsend-env-sep|}"; exit 0 ;;
    cat\ *) f=$(printf '%s' "${last#cat }" | tr -d "'" | tr '/' '_'); cat '` + storeDir + `'/"$f"; exit $? ;;
    *"exec --json"*) ` + streamCase + ` ;;
    find\ *) ` + findCase + ` ;;
  esac
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type codexHooksBootstrapInput struct {
	bootstrapInput
	hooks security.SandboxHookConfig
}

func (b codexHooksBootstrapInput) SandboxHookConfig() security.SandboxHookConfig { return b.hooks }

const codexTestAgentDef = `---
name: triage
description: Inspect an issue.
tools: Bash(gh,jq),Read,Skill
model: openai/gpt-5.6-luna
---
You are the triage agent. Use gh.
`

func TestCodexRuntimeBootstrap_WritesConfigAndManifest(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")

	r := CodexRuntime{}
	err := r.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, codexTestAgentDef),
		agentName:   "triage",
	})
	require.NoError(t, err)

	// config.toml carries the agent body as developer_instructions.
	cfg := string(storedUpload(t, storeDir, r.codexConfigPath()))
	assert.Contains(t, cfg, `model_provider = "`+codexProviderID+`"`)
	assert.Contains(t, cfg, "# Agent: triage")
	assert.Contains(t, cfg, "You are the triage agent. Use gh.")
	assert.Contains(t, cfg, "FULLSEND_RUNTIME=codex", "the runtime note tells skills which runtime they are on")

	// The auth script is uploaded byte-identical to the embedded copy — the
	// run guard pins its SHA-256 — and made executable, which uploadBytes
	// does not do on its own.
	assert.Equal(t, codexAuthScriptSH, storedUpload(t, storeDir, r.codexAuthScriptPath()))
	assert.Contains(t, readFileString(t, logPath), "chmod 755 '"+r.codexAuthScriptPath()+"'")

	var m codexManifest
	require.NoError(t, json.Unmarshal(storedUpload(t, storeDir, r.codexManifestPath()), &m))
	assert.Equal(t, "triage", m.AgentName)
	assert.Equal(t, "openai/gpt-5.6-luna", m.Model)
	assert.Equal(t, "0.152.1", m.CodexVersion, "the bare number: the renderer prefixes \"v\"")
	// Tools stay in Claude vocabulary: codex has no native allowlist, so this
	// is what FULLSEND_TOOL_ALLOWLIST and the allowlist hook match on (#608).
	assert.Equal(t, []string{"Bash", "Read", "Skill"}, m.Tools)
	assert.Equal(t, []string{"gh", "jq"}, m.BashAllowlist)
	assert.Nil(t, m.Hooks, "no hook plan when the input carries no sandbox hook config")

	// Without SandboxHooksBootstrap nothing hook-related is installed.
	log := readFileString(t, logPath)
	assert.NotContains(t, log, codexAdapterFile)
	assert.NotContains(t, log, codexHooksFile)
}

func TestCodexRuntimeBootstrap_RejectsAgentNameMismatch(t *testing.T) {
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")

	err := CodexRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, codexTestAgentDef),
		agentName:   "review",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent name mismatch")
}

func TestCodexRuntimeBootstrap_InstallsHooksWiringAndAdapter(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")

	r := CodexRuntime{}
	err := r.Bootstrap(codexHooksBootstrapInput{
		bootstrapInput: bootstrapInput{
			sandboxName: "sb",
			agentPath:   writeAgentFile(t, codexTestAgentDef),
			agentName:   "triage",
		},
		hooks: security.SandboxHookConfigFromHarness(&harness.Harness{}),
	})
	require.NoError(t, err)

	// The adapter lands byte-identical to the embedded copy: the run guard
	// pins its SHA-256, so any drift would fail every iteration closed.
	assert.Equal(t, codexHookAdapterPy, storedUpload(t, storeDir, r.codexAdapterPath()))

	// Every hook script from the plan is installed beside it, where the
	// adapter resolves them from its own location.
	log := readFileString(t, logPath)
	for name := range security.HookFiles(security.SandboxHookConfigFromHarness(&harness.Harness{})) {
		assert.Contains(t, log, r.codexHooksDir()+"/"+name, "hook script %s was not installed", name)
	}

	var hooksJSON codexHooksConfig
	require.NoError(t, json.Unmarshal(storedUpload(t, storeDir, r.codexHooksPath()), &hooksJSON))
	assert.NotEmpty(t, hooksJSON.Hooks[string(security.HookPhasePreToolUse)])
	assert.NotEmpty(t, hooksJSON.Hooks[string(security.HookPhasePostToolUse)])

	var m codexManifest
	require.NoError(t, json.Unmarshal(storedUpload(t, storeDir, r.codexManifestPath()), &m))
	require.NotNil(t, m.Hooks)
	assert.Equal(t, r.codexHooksDir(), m.Hooks.Dir)
	assert.NotEmpty(t, m.Hooks.Groups)
	assert.Equal(t, "Edit", m.Hooks.ToolNames["apply_patch"])

	// The PostToolUseFailure group is recorded as seen but not wired: codex
	// has no such event, and its PostToolUse already fires for failed
	// commands, so nothing is lost.
	var sawFailurePhase bool
	for _, g := range m.Hooks.Groups {
		if g.Phase == string(security.HookPhasePostToolUseFailure) {
			sawFailurePhase = true
			assert.False(t, g.Wired)
		}
	}
	assert.True(t, sawFailurePhase, "the plan's failure group must be recorded, not dropped silently")
}

func TestCodexRuntimeBootstrap_UploadsSkillsAndWarnsOnPlugins(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.152.1")

	skillDir := filepath.Join(t.TempDir(), "code-review")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: code-review\n---\nbody\n"), 0o644))

	r := CodexRuntime{}
	err := r.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, codexTestAgentDef),
		agentName:   "triage",
		skillDirs:   []string{skillDir},
		pluginDirs:  []string{"/plugins/example"},
	})
	require.NoError(t, err)

	// codex discovers $CODEX_HOME/skills natively.
	assert.Contains(t, readFileString(t, logPath), r.ConfigDir()+"/skills/")
}

func TestCodexRuntimeBootstrap_PreflightFailureIsReportedEarly(t *testing.T) {
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    "codex --version") echo "codex: command not found" >&2; exit 127 ;;
  esac
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := CodexRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, codexTestAgentDef),
		agentName:   "triage",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "codex preflight")
	assert.Contains(t, err.Error(), "exited 127")
}

func TestCodexUnsupportedTools(t *testing.T) {
	t.Parallel()

	// codex does reading, searching and fetching through its shell, so these
	// entries are documentation rather than capabilities it lacks entirely.
	assert.Equal(t, []string{"Read", "Grep", "WebFetch"},
		codexUnsupportedTools([]string{"Bash", "Read", "Grep", "WebFetch", "Write", "Skill"}))
	assert.Empty(t, codexUnsupportedTools([]string{"Bash", "Write", "Edit"}))
	assert.Empty(t, codexUnsupportedTools(nil))
}

func TestCodexDeveloperInstructions(t *testing.T) {
	t.Parallel()

	def, err := parsePiAgent([]byte(codexTestAgentDef))
	require.NoError(t, err)
	got := codexDeveloperInstructions("triage", def)

	assert.Contains(t, got, "# Agent: triage")
	assert.Contains(t, got, "Inspect an issue.")
	assert.Contains(t, got, "You are the triage agent. Use gh.")
	// Skills written for Claude Code's Agent tool must take their
	// single-context path deliberately rather than recording a failed
	// dispatch (the same note pi carries, #6527).
	assert.Contains(t, got, "No fullsend sub-agent roster is available")
}

func TestReadCodexManifest_RejectsGarbage(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")

	r := CodexRuntime{}
	path := r.codexManifestPath()
	require.NoError(t, os.WriteFile(
		filepath.Join(storeDir, strings.ReplaceAll(path, "/", "_")), []byte("not json"), 0o644))

	_, err := readCodexManifest("sb", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding codex manifest")
}

func TestCodexClearIterationArtifacts_SweepsSessionsAndLogs(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.152.1")

	r := CodexRuntime{}
	require.NoError(t, r.ClearIterationArtifacts("sb"))

	log := readFileString(t, logPath)
	// The stray-process sweep runs first, so a process the previous iteration
	// left behind cannot write into the directories being cleared.
	assert.Contains(t, log, shellQuote(r.codexSessionsDir())+"/*")
	assert.Contains(t, log, shellQuote(sandbox.SandboxWorkspace)+"/output/*")
	assert.Contains(t, log, codexDebugLogFile)
}

// readFileString returns the file's contents, or "" when it does not exist:
// a fake-openshell log only appears once something actually invoked it, and
// "nothing ran" is exactly what several of these tests assert.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

// The remaining Bootstrap branches are infrastructure failures — an upload or
// exec that does not succeed. The fake openshell fails on demand for one path
// so each is reported with its own message rather than a bare wrapped error.
func TestCodexRuntimeBootstrap_ReportsInfrastructureFailures(t *testing.T) {
	r := CodexRuntime{}
	for name, tc := range map[string]struct{ match, want string }{
		"config dirs": {"mkdir -p", "creating codex config dirs"},
		"config.toml": {codexConfigFile, "writing " + codexConfigFile},
		"auth script": {codexAuthScriptFile, "writing " + codexAuthScriptFile},
		"version":     {"codex --version", "codex preflight"},
		"manifest":    {codexManifestFile, "writing " + codexManifestFile},
	} {
		t.Run(name, func(t *testing.T) {
			fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
			t.Setenv("FULLSEND_TEST_FAIL_MATCH", tc.match)

			err := r.Bootstrap(bootstrapInput{
				sandboxName: "sb",
				agentPath:   writeAgentFile(t, codexTestAgentDef),
				agentName:   "triage",
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestCodexRuntimeBootstrap_HookInstallFailureIsReported(t *testing.T) {
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
	t.Setenv("FULLSEND_TEST_FAIL_MATCH", codexAdapterFile)

	err := CodexRuntime{}.Bootstrap(codexHooksBootstrapInput{
		bootstrapInput: bootstrapInput{
			sandboxName: "sb",
			agentPath:   writeAgentFile(t, codexTestAgentDef),
			agentName:   "triage",
		},
		hooks: security.SandboxHookConfigFromHarness(&harness.Harness{}),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "installing hook adapter")
}

// An agent definition with no frontmatter name and no harness-supplied name
// falls back to the file's basename, so developer_instructions still carries a
// header the prompt can refer to.
func TestCodexRuntimeBootstrap_DerivesAgentNameFromTheFile(t *testing.T) {
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), storeDir, "codex-cli 0.152.1")

	r := CodexRuntime{}
	require.NoError(t, r.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, "Just a body, no frontmatter."+"\n"),
		// Deliberately no agentName, and an empty skill entry, which is
		// skipped rather than uploaded as "".
		skillDirs: []string{""},
	}))

	var m codexManifest
	require.NoError(t, json.Unmarshal(storedUpload(t, storeDir, r.codexManifestPath()), &m))
	assert.Equal(t, "triage", m.AgentName, "the basename of the agent file")
	assert.Contains(t, string(storedUpload(t, storeDir, r.codexConfigPath())), "# Agent: triage")
}

func TestCodexPreflightVersion_RejectsAnUnexpectedShape(t *testing.T) {
	// Fail closed rather than let the run log render "(vsomething odd)".
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "0.152.1")

	_, err := codexPreflightVersion("sb")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected")
}

func TestCodexPreflightVersion_UsesTheLastLine(t *testing.T) {
	storeDir := t.TempDir()
	// A real codex may print a warning first — the sandbox image logs one
	// about PATH aliases — and the version is the last line either way.
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), storeDir, "warning: something\ncodex-cli 0.152.1")

	got, err := codexPreflightVersion("sb")
	require.NoError(t, err)
	assert.Equal(t, "0.152.1", got)
}

func TestReadCodexManifest_ReportsAMissingFile(t *testing.T) {
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")

	_, err := readCodexManifest("sb", CodexRuntime{}.codexManifestPath())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "was Bootstrap run?")
}

// TestCodexPreflightPython covers the resolution Bootstrap does on a shell the
// agent has not touched, so hooks.json can name an absolute interpreter.
func TestCodexPreflightPython(t *testing.T) {
	t.Run("returns the absolute path", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		got, err := codexPreflightPython("sb")
		require.NoError(t, err)
		assert.Equal(t, "/usr/bin/python3", got)
	})

	t.Run("refuses a relative answer", func(t *testing.T) {
		binDir := t.TempDir()
		script := "#!/bin/sh\nif [ \"$2\" = \"exec\" ]; then echo 'python3'; fi\nexit 0\n"
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		_, err := codexPreflightPython("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not an absolute path")
	})

	t.Run("refuses a missing interpreter", func(t *testing.T) {
		binDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		_, err := codexPreflightPython("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found in the sandbox image")
	})
}

func TestCodexPreflightPythonVersion(t *testing.T) {
	t.Run("accepts the image's interpreter", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		require.NoError(t, codexPreflightPythonVersion("sb", "/usr/bin/python3"))
	})

	// The adapter appends the hooks directory behind the standard library and
	// relies on -I keeping the script's own directory off sys.path; below the
	// floor a planted hooks/json/ could shadow a stdlib import.
	t.Run("refuses an interpreter below the floor", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_PYVER", "3.8")

		err := codexPreflightPythonVersion("sb", "/usr/bin/python3")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "needs at least 3.11")
	})

	t.Run("refuses an unreadable version", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_PYVER", "python 2 maybe")

		err := codexPreflightPythonVersion("sb", "/usr/bin/python3")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected 3.x")
	})
}

// TestCodexBootstrap_RecordsRunnerHeldDigests pins the anchor: the name to
// digest map has to reach the runner-held digests, since Run cannot re-derive
// which scripts the harness enabled.
func TestCodexBootstrap_RecordsRunnerHeldDigests(t *testing.T) {
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), storeDir, "codex-cli 0.152.1")
	forgetRunnerHeldDigests("sb")
	t.Cleanup(func() { forgetRunnerHeldDigests("sb") })

	r := CodexRuntime{}
	require.NoError(t, r.Bootstrap(codexHooksBootstrapInput{
		bootstrapInput: bootstrapInput{
			sandboxName: "sb",
			agentPath:   writeAgentFile(t, codexTestAgentDef),
			agentName:   "triage",
		},
		hooks: security.SandboxHookConfigFromHarness(&harness.Harness{}),
	}))

	got, ok := lookupRunnerHeldDigests("sb")
	require.True(t, ok, "Bootstrap must record the digests for Run to guard against")
	assert.NotEmpty(t, got.ConfigTOML)
	assert.NotEmpty(t, got.HooksJSON)
	want := security.HookFiles(security.SandboxHookConfigFromHarness(&harness.Harness{}))
	require.Len(t, got.HookScripts, len(want))
	for name, content := range want {
		assert.Equal(t, codexAssetSHA256(content), got.HookScripts[name], "digest for %s", name)
	}
}

// TestCodexReadHarnessSecurityEnv covers the values the runner has no typed
// copy of. They are read at Bootstrap because the workspace .env is still
// exactly what the runner wrote then — reading them in Run would read whatever
// the previous iteration left behind, which is the exposure this closes.
func TestCodexReadHarnessSecurityEnv(t *testing.T) {
	t.Run("reads both values", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_ENV_READ", "canary-abc"+codexEnvReadSeparator+"Bash,Read")

		got, err := codexReadHarnessSecurityEnv("sb")
		require.NoError(t, err)
		assert.Equal(t, []codexEnvPair{
			{"FULLSEND_CANARY_TOKEN", "canary-abc"},
			{"FULLSEND_TOOL_ALLOWLIST", "Bash,Read"},
		}, got)
	})

	// Nothing to re-assert when the harness set neither, and an agent that
	// *sets* one later can only cause spurious blocks, not slip past a check.
	t.Run("skips unset values", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_ENV_READ", codexEnvReadSeparator)

		got, err := codexReadHarnessSecurityEnv("sb")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("keeps one when only one is set", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_ENV_READ", "canary-abc"+codexEnvReadSeparator)

		got, err := codexReadHarnessSecurityEnv("sb")
		require.NoError(t, err)
		assert.Equal(t, []codexEnvPair{{"FULLSEND_CANARY_TOKEN", "canary-abc"}}, got)
	})

	t.Run("refuses a malformed answer", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_ENV_READ", "only-one-value")

		_, err := codexReadHarnessSecurityEnv("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected 2 values")
	})
}

// TestCodexBootstrap_PinsTheHarnessHookEnv is the end-to-end half: what the
// harness set at bootstrap is what the hooks receive on a later iteration,
// whatever the agent wrote into .env in between.
func TestCodexBootstrap_PinsTheHarnessHookEnv(t *testing.T) {
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), storeDir, "codex-cli 0.152.1")
	t.Setenv("FULLSEND_TEST_ENV_READ", "canary-from-harness"+codexEnvReadSeparator+"Bash,Read")
	forgetRunnerHeldDigests("sb")
	t.Cleanup(func() { forgetRunnerHeldDigests("sb") })

	r := CodexRuntime{}
	require.NoError(t, r.Bootstrap(codexHooksBootstrapInput{
		bootstrapInput: bootstrapInput{
			sandboxName: "sb",
			agentPath:   writeAgentFile(t, codexTestAgentDef),
			agentName:   "triage",
		},
		hooks: security.SandboxHookConfigFromHarness(&harness.Harness{}),
	}))

	held, ok := lookupRunnerHeldDigests("sb")
	require.True(t, ok)
	got := map[string]string{}
	for _, p := range held.SecurityEnv {
		got[p.Key] = p.Value
	}
	assert.Equal(t, "canary-from-harness", got["FULLSEND_CANARY_TOKEN"])
	assert.Equal(t, "Bash,Read", got["FULLSEND_TOOL_ALLOWLIST"])

	// And the launch re-exports them after .env, so an iteration-1 rewrite of
	// that file cannot change what iteration 2's hooks see.
	cmd := buildCodexRunCommand(RunParams{
		RepoDir:           sandbox.SandboxWorkspace + "/repo",
		HooksSettingsPath: r.codexHooksPath(),
	}, "gpt-5-mini", "", true, held)
	envAt := strings.Index(cmd, ". '"+sandbox.SandboxWorkspace+"/.env'")
	require.Positive(t, envAt)
	for key, value := range got {
		at := strings.Index(cmd, "export "+key+"="+shellQuote(value))
		require.Positive(t, at, "%s is not re-exported", key)
		assert.Greater(t, at, envAt, "%s must be re-exported after .env", key)
	}
}
