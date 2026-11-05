package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
  case "$6" in /tmp/fs-upload-*/) cp -- "$5" "$6/"; exit $? ;; esac
  cp "$5" '` + storeDir + `'/"$(printf '%s' "$6" | tr '/' '_')"
  exit 0
fi
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    "mkdir -m 700 -- /tmp/"*|"mkdir -m 700 -- '/tmp/"*|"rm -f -- '/tmp/fs-upload-"*) sh -c "$last"; exit $? ;;
    "test -f '/tmp/fs-upload-"*)
      sh -c 'mkdir() { :; }; mv() { shift; shift; cp -- "$1" '\''` + storeDir + `/'\''"$(printf "%s" "$2" | tr / _)"; }; '"$last"
      exit $? ;;
    "codex --version") echo "` + versionOutput + `"; exit 0 ;;
    "codex debug models --bundled") cat "${FULLSEND_TEST_CATALOG:-testdata/codex/models-bundled-0.159.3.json}"; exit 0 ;;
    *"` + hookInterpreterSep + `"*) printf '%s` + hookInterpreterSep + `%s\n' /usr/bin/python3 /usr/local/bin:/usr/bin:/bin; exit 0 ;;
    # What a plain PATH lookup finds in the fleet image; Bootstrap must not use it.
    "command -v python3") echo "/sandbox/.venv/bin/python3"; exit 0 ;;
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
	assert.Contains(t, cfg, "at most 200 lines", "the runtime note tells Codex to chunk long skill reads (#7831)")
	assert.Contains(t, cfg, "END { print NR }", "the runtime note counts lines with awk, so an unterminated last line is read (#7831)")
	assert.Contains(t, cfg, "never skip the back half", "the runtime note's retry rule must not silently drop lines on retry (#7831)")

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
	// Every hook command starts with the pinned interpreter, not the plain
	// PATH lookup's venv python3.
	rawHooks := string(storedUpload(t, storeDir, r.codexHooksPath()))
	assert.Contains(t, rawHooks, "/usr/bin/python3 -I ")
	assert.NotContains(t, rawHooks, "/sandbox/.venv")

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
		plugins:     claudePlugins("/plugins/example"),
	})
	require.NoError(t, err)

	// codex discovers $CODEX_HOME/skills natively.
	assert.Contains(t, readFileString(t, logPath), r.ConfigDir()+"/skills/")
}

func TestCodexRuntimeBootstrap_NestedForgeSkillsShareBasename(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.152.1")

	root := t.TempDir()
	labelsDir := filepath.Join(root, "issue-labels", "github")
	reviewDir := filepath.Join(root, "pr-review", "github")
	require.NoError(t, os.MkdirAll(labelsDir, 0o755))
	require.NoError(t, os.MkdirAll(reviewDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(labelsDir, "SKILL.md"),
		[]byte("---\nname: issue-labels\n---\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(reviewDir, "SKILL.md"),
		[]byte("---\nname: pr-review-github\n---\n"), 0o644))

	r := CodexRuntime{}
	err := r.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, codexTestAgentDef),
		agentName:   "triage",
		skillDirs:   []string{labelsDir, reviewDir},
	})
	require.NoError(t, err)

	log := readFileString(t, logPath)
	assert.Contains(t, log, r.ConfigDir()+"/skills/issue-labels")
	assert.Contains(t, log, r.ConfigDir()+"/skills/pr-review-github")
	assert.NotContains(t, log, r.ConfigDir()+"/skills/github")
}

func TestCodexRuntimeBootstrap_RejectsInvalidSkillDestinationName(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(root string) string
	}{
		{"dotdot", func(root string) string { return root + "/sub/.." }},
		{"dot", func(root string) string { return root + "/sub/." }},
		{"control-char", func(root string) string { return root + "/bad\x01name" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "openshell.log")
			fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.152.1")

			err := CodexRuntime{}.Bootstrap(bootstrapInput{
				sandboxName: "sb",
				agentPath:   writeAgentFile(t, codexTestAgentDef),
				agentName:   "triage",
				skillDirs:   []string{tc.path(t.TempDir())},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid sandbox name")
		})
	}
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
	got := codexDeveloperInstructions("triage", def, nil)

	assert.Contains(t, got, "# Agent: triage")
	assert.Contains(t, got, "Inspect an issue.")
	assert.Contains(t, got, "You are the triage agent. Use gh.")
	// Skills written for Claude Code's Agent tool must take their
	// single-context path deliberately rather than recording a failed
	// dispatch (the same note pi carries, #6527).
	assert.Contains(t, got, "No fullsend sub-agent roster is available")
	// Codex 0.157.0 truncates a single exec at 10k tokens (head+tail),
	// which drops the middle of long SKILL.md files (#7831). The note
	// must keep the operational rule, not just mention truncation.
	// awk's NR, not `wc -l`: see the comment on codexNoSubagentNote.
	assert.Contains(t, got, "`awk 'END { print NR }' <file>`")
	assert.Contains(t, got, "`sed -n")
	assert.Contains(t, got, "at most 200 lines")
	assert.Contains(t, got, "one range per tool call")
	assert.Contains(t, got, "truncation warning")
	// A prior review round caught a reread rule that replaced a truncated
	// range with a smaller prefix and then advanced, silently dropping the
	// back half of the range. The fix pins an explicit partition rule and a
	// worked example instead — assert both survive future edits (#7831).
	assert.Contains(t, got, "never skip the back half")
	assert.Contains(t, got, "`1,200p`, `201,400p`, `401,425p`")
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

// fakeOpenshellHookProbe installs a fake openshell whose exec of the hook
// interpreter probe prints probeOut on stdout and probeErr on stderr and
// exits probeExit; the version check answers 3.12.
func fakeOpenshellHookProbe(t *testing.T, probeOut, probeErr string, probeExit int) {
	t.Helper()
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    *"` + hookInterpreterSep + `"*) printf '%s' '` + probeOut + `'; printf '%s' '` + probeErr + `' >&2; exit ` + strconv.Itoa(probeExit) + ` ;;
    *sys.version_info*) echo 3.12; exit 0 ;;
  esac
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestHookPreflightInterpreter covers the resolution Bootstrap does on a shell
// the agent has not touched, so hooks.json can name an absolute interpreter and
// a PATH from directories the agent cannot write.
func TestHookPreflightInterpreter(t *testing.T) {
	t.Run("returns the pinned interpreter and the trusted PATH", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "log")
		fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.152.1")
		python, path, err := hookPreflightInterpreter("sb")
		require.NoError(t, err)
		assert.Equal(t, "/usr/bin/python3", python, "not the plain PATH lookup's answer")
		assert.Equal(t, "/usr/local/bin:/usr/bin:/bin", path)
		log, err := os.ReadFile(logPath)
		require.NoError(t, err)
		assert.Contains(t, string(log), envDataPin(), "the probe runs the launch's own pin")
		assert.NotContains(t, string(log), "command -v python3")
	})

	// The version floor runs on the pinned interpreter, so an older protected
	// python3 fails Bootstrap even when a newer one sits in the venv.
	t.Run("refuses a pinned interpreter below the floor", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_PYVER", "3.8")
		_, _, err := hookPreflightInterpreter("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "3.11")
	})

	t.Run("takes the last line", func(t *testing.T) {
		fakeOpenshellHookProbe(t, "warning: something\n/usr/bin/python3.12"+hookInterpreterSep+"/usr/bin:/bin\n", "", 0)
		python, path, err := hookPreflightInterpreter("sb")
		require.NoError(t, err)
		assert.Equal(t, "/usr/bin/python3.12", python)
		assert.Equal(t, "/usr/bin:/bin", path)
	})

	t.Run("refuses a relative answer", func(t *testing.T) {
		fakeOpenshellHookProbe(t, "python3"+hookInterpreterSep+"/usr/bin", "", 0)
		_, _, err := hookPreflightInterpreter("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not an absolute path")
	})

	t.Run("refuses a missing interpreter", func(t *testing.T) {
		fakeOpenshellHookProbe(t, "", "fullsend: python3 not found in a PATH directory the agent cannot write", 127)
		_, _, err := hookPreflightInterpreter("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolving python3 for the hooks")
		assert.Contains(t, err.Error(), "exit 127")
		assert.Contains(t, err.Error(), "python3 not found in a PATH directory the agent cannot write")
	})

	t.Run("refuses output without the separator", func(t *testing.T) {
		fakeOpenshellHookProbe(t, "/usr/bin/python3\n", "", 0)
		_, _, err := hookPreflightInterpreter("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolving python3 for the hooks")
	})

	t.Run("refuses an empty PATH", func(t *testing.T) {
		fakeOpenshellHookProbe(t, "/usr/bin/python3"+hookInterpreterSep, "", 0)
		_, _, err := hookPreflightInterpreter("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolving the PATH for the hooks")
	})
}

// TestHookPreflightInterpreter_RealShell runs the probe in a real sh with an
// agent-writable directory holding a python3 first on PATH: neither that
// python3 nor that directory reaches the result.
func TestHookPreflightInterpreter_RealShell(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root every directory is writable")
	}
	if err := exec.Command("/bin/sh", "-c", envDataPin()).Run(); err != nil {
		t.Skip("no python3 in a non-writable PATH directory on this host")
	}
	writable := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(writable, "python3"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    *sys.version_info*) echo 3.12; exit 0 ;;
  esac
  PATH='` + writable + `':"$PATH" exec /bin/sh -c "$last"
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	python, path, err := hookPreflightInterpreter("sb")
	require.NoError(t, err)
	assert.False(t, strings.HasPrefix(python, writable+"/"), python)
	assert.NotContains(t, ":"+path+":", ":"+writable+":")
	assert.NotContains(t, ":"+path+":", ":"+binDir+":")
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
		t.Setenv("FULLSEND_TEST_ENV_READ", "canary-abc"+hookEnvReadSeparator+"Bash,Read")

		got, err := readHarnessHookEnv("sb")
		require.NoError(t, err)
		assert.Equal(t, []hookEnvPair{
			{"FULLSEND_CANARY_TOKEN", "canary-abc"},
			{"FULLSEND_TOOL_ALLOWLIST", "Bash,Read"},
		}, got)
	})

	// Pinned as empty, so an agent that sets the allowlist in .env later
	// cannot widen the fail-closed empty set the harness left.
	t.Run("pins an unset value as empty", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_ENV_READ", "canary-abc"+hookEnvReadSeparator)

		got, err := readHarnessHookEnv("sb")
		require.NoError(t, err)
		assert.Equal(t, []hookEnvPair{
			{"FULLSEND_CANARY_TOKEN", "canary-abc"},
			{"FULLSEND_TOOL_ALLOWLIST", ""},
		}, got)
	})

	t.Run("refuses a malformed answer", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_ENV_READ", "only-one-value")

		_, err := readHarnessHookEnv("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected 2 values")
	})
}

// TestCodexReadHarnessSecurityEnvCmd runs the read command through a real sh:
// the fake openshell above answers it without evaluating it, which is how
// single-quoted references re-asserted the literal "${KEY:-}" text.
func TestCodexReadHarnessSecurityEnvCmd(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	for name, tc := range map[string]struct{ env, want string }{
		"both set":     {"export FULLSEND_CANARY_TOKEN='canary-abc'\nexport FULLSEND_TOOL_ALLOWLIST='Bash,Read'\n", "canary-abc|fullsend-env-sep|Bash,Read"},
		"canary unset": {"export FULLSEND_TOOL_ALLOWLIST='Bash,Read'\n", "|fullsend-env-sep|Bash,Read"},
		"none set":     {"", "|fullsend-env-sep|"},
		"chatty .env":  {"echo hello\nexport FULLSEND_CANARY_TOKEN='canary-abc'\n", "canary-abc|fullsend-env-sep|"},
		// The runner's .env ends with these lines; an empty .env.d and a
		// missing iteration.env must not read as a failed source.
		"runner-shaped .env": {"export FULLSEND_CANARY_TOKEN='canary-abc'\nfor f in /nonexistent/.env.d/*.env; do [ -f \"$f\" ] && . \"$f\"; done\nif [ -f /nonexistent/iteration.env ]; then . /nonexistent/iteration.env; fi\n", "canary-abc|fullsend-env-sep|"},
	} {
		t.Run(name, func(t *testing.T) {
			envFile := filepath.Join(t.TempDir(), ".env")
			require.NoError(t, os.WriteFile(envFile, []byte(tc.env), 0o600))
			cmd := exec.Command("/bin/sh", "-c", readHarnessHookEnvCmd(envFile))
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			out, err := cmd.Output()
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(out))
		})
	}

	// .env is read as data, as at launch (#8574): a line the loader does not
	// take is skipped, and the values around it are still read rather than
	// pinned as unset.
	for name, env := range map[string]string{
		"syntax error": "export FULLSEND_CANARY_TOKEN='canary-abc'\nif then\n",
		"failing last": "export FULLSEND_CANARY_TOKEN='canary-abc'\nfalse\n",
	} {
		t.Run(name, func(t *testing.T) {
			envFile := filepath.Join(t.TempDir(), ".env")
			require.NoError(t, os.WriteFile(envFile, []byte(env), 0o600))
			cmd := exec.Command("/bin/sh", "-c", readHarnessHookEnvCmd(envFile))
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			require.NoError(t, err, stderr.String())
			assert.Equal(t, strings.SplitN(env, "'", 3)[1]+hookEnvReadSeparator, string(out))
			assert.Contains(t, stderr.String(), "line 2:", "the skipped line is reported by number")
		})
	}
	// No .env line runs as shell during the read.
	t.Run("shell constructs do not run", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "pwned")
		envFile := filepath.Join(dir, ".env")
		require.NoError(t, os.WriteFile(envFile, []byte("touch "+marker+"\n"+
			"export FULLSEND_TOOL_ALLOWLIST=$(touch "+marker+")\n"+
			"printf() { touch "+marker+"; }\n"+
			"export FULLSEND_TOOL_ALLOWLIST='Bash,Read'\n"), 0o600))
		cmd := exec.Command("/bin/sh", "-c", readHarnessHookEnvCmd(envFile))
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		out, err := cmd.Output()
		require.NoError(t, err)
		assert.Equal(t, hookEnvReadSeparator+"Bash,Read", string(out))
		assert.NoFileExists(t, marker, "a construct from .env ran")
	})
	t.Run("missing file", func(t *testing.T) {
		cmd := exec.Command("/bin/sh", "-c", readHarnessHookEnvCmd(filepath.Join(t.TempDir(), ".env")))
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		out, err := cmd.Output()
		require.Error(t, err)
		assert.Empty(t, string(out))
	})
}

// TestCodexBootstrap_PinsTheHarnessHookEnv is the end-to-end half: what the
// harness set at bootstrap is what the hooks receive on a later iteration,
// whatever the agent wrote into .env in between.
func TestCodexBootstrap_PinsTheHarnessHookEnv(t *testing.T) {
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), storeDir, "codex-cli 0.152.1")
	t.Setenv("FULLSEND_TEST_ENV_READ", "canary-from-harness"+hookEnvReadSeparator+"Bash,Read")
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
	}, codexModel{ID: "gpt-5-mini"}, "", true, held)
	envAt := strings.Index(cmd, codexEnvLoad)
	require.Positive(t, envAt)
	for key, value := range got {
		at := strings.Index(cmd, "export "+key+"="+shellQuote(value))
		require.Positive(t, at, "%s is not re-exported", key)
		assert.Greater(t, at, envAt, "%s must be re-exported after .env", key)
	}
}

// noRepoDirInput hides bootstrapInput's RepoDir: only the interface's methods
// are promoted from the embedded field.
type noRepoDirInput struct{ BootstrapInput }

func TestCodexRuntimeBootstrap_RequiresRepoDir(t *testing.T) {
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")

	for name, in := range map[string]BootstrapInput{
		"no RepoDir method": noRepoDirInput{bootstrapInput{sandboxName: "sb", agentPath: writeAgentFile(t, codexTestAgentDef), agentName: "triage"}},
		"empty RepoDir":     emptyRepoDirInput{bootstrapInput{sandboxName: "sb", agentPath: writeAgentFile(t, codexTestAgentDef), agentName: "triage"}},
	} {
		err := CodexRuntime{}.Bootstrap(in)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "target repository's sandbox path is required", name)
	}
}

type emptyRepoDirInput struct{ bootstrapInput }

func (emptyRepoDirInput) RepoDir() string { return "" }

func TestHomeAgentsMDPath(t *testing.T) {
	assert.Equal(t, sandbox.SandboxCodexConfig+"/AGENTS.md", HomeAgentsMDPath(CodexRuntime{}))
	assert.Empty(t, HomeAgentsMDPath(ClaudeRuntime{}), "Claude reads the repo's own context files")
}

// The catalog preflight is a sandbox exec like codexPreflightVersion: a
// failed command, unparsable output and an empty catalog each fail
// Bootstrap with their own message rather than leaving the gate half decided.
func TestCodexPreflightCatalog(t *testing.T) {
	t.Run("reads the fixture", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		catalog, err := codexPreflightCatalog("sb")
		require.NoError(t, err)
		assert.True(t, catalog.resolvesV1("gpt-5.6-luna"))
	})
	t.Run("command fails", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_FAIL_MATCH", "codex debug models --bundled")
		_, err := codexPreflightCatalog("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "codex preflight: `codex debug models --bundled` exited 1")
	})
	t.Run("output is not a catalog", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "catalog.json")
		require.NoError(t, os.WriteFile(bad, []byte("codex-cli 0.152.1\n"), 0o644))
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_CATALOG", bad)
		_, err := codexPreflightCatalog("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "codex preflight: `codex debug models --bundled` printed no model catalog")
	})
}

// codexDelegatingHooksInput is the ADR 0126 gate met: an agent whose tools
// include Agent, with harness security on, whose definition model (the
// run-level model of the double is "") is the listed V1 model of the fixture.
func codexDelegatingHooksInput(t *testing.T, skillDirs ...string) codexHooksBootstrapInput {
	t.Helper()
	return codexHooksBootstrapInput{
		bootstrapInput: bootstrapInput{
			sandboxName: "sb",
			agentPath:   writeAgentFile(t, codexTestDelegatingAgentDef),
			agentName:   "review",
			skillDirs:   skillDirs,
		},
		hooks: security.SandboxHookConfigFromHarness(&harness.Harness{}),
	}
}

func TestCodexRolesDir(t *testing.T) {
	t.Parallel()
	assert.Equal(t, sandbox.SandboxCodexConfig+"/agents", CodexRuntime{}.codexRolesDir(),
		"the directory codex scans for roles at launch")
}

// TestCodexRuntimeBootstrap_WritesRoleFilesForADelegatingAgent covers the
// gate passing: one regular file per role under agents/, digests recorded
// for the spawn guard.
func TestCodexRuntimeBootstrap_WritesRoleFilesForADelegatingAgent(t *testing.T) {
	t.Setenv(codexSubagentModelEnv, "")
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")
	forgetRunnerHeldDigests("sb")
	t.Cleanup(func() { forgetRunnerHeldDigests("sb") })
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", codexTestPersona)

	r := CodexRuntime{}
	require.NoError(t, r.Bootstrap(codexDelegatingHooksInput(t, skill)))

	log := readFileString(t, logPath)
	assert.Contains(t, log, "mkdir -p "+shellQuote(r.codexRolesDir()), "the roles directory is created only when the gate passes")
	assert.Contains(t, log, "codex debug models --bundled", "the catalog preflight runs for a delegating agent")

	held, ok := lookupRunnerHeldDigests("sb")
	require.True(t, ok)
	want, _ := codexRoles([]string{skill}, "review")
	require.Len(t, held.RoleFiles, len(want))
	var names []string
	for name := range held.RoleFiles {
		names = append(names, name)
	}
	assert.ElementsMatch(t, []string{"correctness", "default", "explore"}, names)
	for _, role := range want {
		path := r.codexRolesDir() + "/" + role.Name + ".toml"
		got := storedUpload(t, storeDir, path)
		assert.Equal(t, role.toml(), got, "role file %s", path)
		assert.Equal(t, codexAssetSHA256(got), held.RoleFiles[role.Name], "the spawn guard's digest for %s is bound to the file name", role.Name)
	}
}

// len(RoleFiles) > 0 exactly when the ADR 0126 gate holds: part one
// (security on, tools include Agent) and part two (codex resolves the parent
// model to V1: its entry selects V1 or carries no version). With part one
// failed, no catalog is read; with part two failed, the catalog was read and
// nothing else happens: no directory is created, nothing is uploaded and the
// digest set has no role entry. The gpt-5.5 case is the boundary: an entry
// without a version delegates (ADR 0126), so its files are present.
func TestCodexRuntimeBootstrap_NoRoleFilesWhenTheGateFails(t *testing.T) {
	t.Setenv(codexSubagentModelEnv, "")
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", codexTestPersona)
	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{})
	delegating := func(agentDef, parentModel string) codexHooksBootstrapInput {
		return codexHooksBootstrapInput{
			bootstrapInput: bootstrapInput{sandboxName: "sb", agentPath: writeAgentFile(t, agentDef), agentName: "review", skillDirs: []string{skill}, parentModel: parentModel},
			hooks:          hooks,
		}
	}
	v2Def := strings.Replace(codexTestDelegatingAgentDef, "gpt-5.6-luna", "gpt-5.6-sol", 1)
	unlistedDef := strings.Replace(codexTestDelegatingAgentDef, "gpt-5.6-luna", "gpt-5.6-nova", 1)
	noVersionDef := strings.Replace(codexTestDelegatingAgentDef, "gpt-5.6-luna", "gpt-5.5", 1)
	for name, tc := range map[string]struct {
		input      BootstrapInput
		preflight  bool
		delegating bool
		reason     string // the one stderr line of a declined parent
	}{
		"security on, agent without the Agent tool": {codexHooksBootstrapInput{
			bootstrapInput: bootstrapInput{sandboxName: "sb", agentPath: writeAgentFile(t, codexTestAgentDef), agentName: "triage", skillDirs: []string{skill}},
			hooks:          hooks,
		}, false, false, ""},
		"security off, delegating agent": {bootstrapInput{
			sandboxName: "sb", agentPath: writeAgentFile(t, codexTestDelegatingAgentDef), agentName: "review", skillDirs: []string{skill},
		}, false, false, ""},
		"security on, definition model selects V2":               {delegating(v2Def, ""), true, false, `parent model "gpt-5.6-sol" selects collaboration "v2"; native sub-agents need a parent Codex resolves to collaboration V1`},
		"security on, definition model not in the catalog":       {delegating(unlistedDef, ""), true, false, `parent model "gpt-5.6-nova" is not in the bundled catalog of the pinned codex; native sub-agents need a parent Codex resolves to collaboration V1`},
		"security on, run-level V2 model beats a V1 definition":  {delegating(codexTestDelegatingAgentDef, "openai/gpt-5.6-sol"), true, false, `parent model "gpt-5.6-sol" selects collaboration "v2"; native sub-agents need a parent Codex resolves to collaboration V1`},
		"security on, run-level gateway route to a V1 id":        {delegating(codexTestDelegatingAgentDef, "gateway/gpt-5.6-luna"), true, false, `parent model "gateway/gpt-5.6-luna" takes the inference gateway route, where native sub-agents are not supported`},
		"security on, definition model has no version (gpt-5.5)": {delegating(noVersionDef, ""), true, true, ""},
	} {
		t.Run(name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "openshell.log")
			fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.152.1")
			forgetRunnerHeldDigests("sb")
			t.Cleanup(func() { forgetRunnerHeldDigests("sb") })

			r := CodexRuntime{}
			var err error
			out := captureStderr(t, func() { err = r.Bootstrap(tc.input) })
			require.NoError(t, err)
			wantLines := 0
			if tc.reason != "" {
				wantLines = 1
				assert.Contains(t, out, "sub-agents: "+tc.reason+", running in one context\n")
			}
			assert.Equal(t, wantLines, strings.Count(out, "sub-agents: "), "one stderr line exactly when part two of the gate declines")

			log := readFileString(t, logPath)
			assert.Equal(t, tc.delegating, strings.Contains(log, r.codexRolesDir()), "the roles directory exists exactly when the gate holds")
			assert.Equal(t, tc.preflight, strings.Contains(log, "codex debug models --bundled"),
				"the catalog is read only once part one of the gate passed")
			held, ok := lookupRunnerHeldDigests("sb")
			require.True(t, ok)
			assert.Equal(t, tc.delegating, held.RoleFiles != nil, "role digests are recorded exactly when the gate holds")
		})
	}
}

// The three new infrastructure failures are reported with their own message,
// like the rows of TestCodexRuntimeBootstrap_ReportsInfrastructureFailures.
func TestCodexRuntimeBootstrap_ReportsRoleFileFailures(t *testing.T) {
	t.Setenv(codexSubagentModelEnv, "")
	r := CodexRuntime{}
	for name, tc := range map[string]struct{ match, want string }{
		"roles dir":         {"mkdir -p " + shellQuote(r.codexRolesDir()), "creating codex roles dir"},
		"role file":         {r.codexRolesDir() + "/default.toml", `writing role "default"`},
		"catalog preflight": {"codex debug models --bundled", "codex preflight: `codex debug models --bundled` exited 1"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
			t.Setenv("FULLSEND_TEST_FAIL_MATCH", tc.match)

			err := r.Bootstrap(codexDelegatingHooksInput(t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestCodexBootstrap_PlantedRoleFileDeniesSpawn checks the runner-held role
// digests (#6970): the files Bootstrap wrote admit a registered role; one
// extra file in agents/, which is agent-writable, denies every spawn with
// exit 2 and a reason, through the installed hook adapter. Driven directly:
// no Codex process runs here; the hook is invoked with the spawn input the
// test builds.
func TestCodexBootstrap_PlantedRoleFileDeniesSpawn(t *testing.T) {
	t.Setenv(codexSubagentModelEnv, "")
	h := newCodexAdapterHarness(t)
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), storeDir, "codex-cli 0.152.1")
	forgetRunnerHeldDigests("sb")
	t.Cleanup(func() { forgetRunnerHeldDigests("sb") })
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", codexTestPersona)

	r := CodexRuntime{}
	require.NoError(t, r.Bootstrap(codexDelegatingHooksInput(t, skill)))
	held, ok := lookupRunnerHeldDigests("sb")
	require.True(t, ok)
	require.NotEmpty(t, held.RoleFiles)

	// CODEX_HOME on the host, exactly the bytes Bootstrap uploaded: the
	// adapter resolves hooks.json and agents/ relative to its own directory.
	h.write(codexHooksFile, string(storedUpload(t, storeDir, r.codexHooksPath())))
	for name := range held.RoleFiles {
		h.write("agents/"+name+".toml", string(storedUpload(t, storeDir, r.codexRolesDir()+"/"+name+".toml")))
	}
	spawn := func(role string) codexAdapterResult {
		return h.spawnGuardWith(codexSpawnInput(role), codexSpawnDigestsValue(held))
	}

	t.Run("admits a registered role from the files Bootstrap wrote", func(t *testing.T) {
		got := spawn("correctness")
		assert.Equal(t, 0, got.exitCode, "stderr: %s", got.stderr)
	})
	t.Run("denies a role that was never registered", func(t *testing.T) {
		got := spawn("planted")
		assert.Equal(t, 2, got.exitCode)
		assert.NotEmpty(t, got.stderr, "a block is exit 2 with a non-empty reason, or codex lets the spawn through")
	})
	t.Run("denies every spawn once a file is planted in agents/", func(t *testing.T) {
		h.write("agents/planted.toml", "name = \"planted\"\ndescription = \"x\"\ndeveloper_instructions = \"x\"\n")
		t.Cleanup(func() { require.NoError(t, os.Remove(filepath.Join(h.dir, "agents", "planted.toml"))) })
		got := spawn("correctness")
		assert.Equal(t, 2, got.exitCode, "an entry that is not a runner-held key denies the spawn (exhaustive check)")
		assert.NotEmpty(t, got.stderr)
	})
	t.Run("denies a registered role whose file was edited", func(t *testing.T) {
		h.write("agents/correctness.toml", "name = \"correctness\"\ndescription = \"x\"\ndeveloper_instructions = \"changed\"\n")
		got := spawn("correctness")
		assert.Equal(t, 2, got.exitCode)
		assert.NotEmpty(t, got.stderr)
	})
}

// codexDelegatingBootstrap runs Bootstrap for the delegating review agent
// with one correctness persona (and a worker persona codex skips), harness
// security on and the given subagents map. It returns the runtime, the
// fake's argv log and Bootstrap's error.
func codexDelegatingBootstrap(t *testing.T, storeDir string, subagents map[string]*string) (CodexRuntime, string, error) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")
	forgetRunnerHeldDigests("sb")
	t.Cleanup(func() { forgetRunnerHeldDigests("sb") })
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", codexTestPersona)
	writePersonaFile(t, skill, "worker", "---\nname: worker\ndescription: d\n---\nbody\n")

	input := codexDelegatingHooksInput(t, skill)
	input.agentSubagents = subagents
	r := CodexRuntime{}
	return r, logPath, r.Bootstrap(input)
}

// TestCodexRuntimeBootstrap_RejectsNonOpenAISubagentModel pins the wiring:
// the subagents map, the skipped personas, the catalog and the runner
// variable reach the resolver, and a bad value fails Bootstrap naming the
// key before config.toml is written. The breaking case is a pi-shaped
// review entry (xai and google-vertex values) run with runtime codex.
func TestCodexRuntimeBootstrap_RejectsNonOpenAISubagentModel(t *testing.T) {
	for name, tc := range map[string]struct {
		env       string
		subagents map[string]*string
		want      []string
	}{
		"a persona entry from another provider": {
			subagents: map[string]*string{"correctness": strp("xai-vertex/xai/grok-4.6")},
			want:      []string{"subagents.correctness", `"xai-vertex/xai/grok-4.6"`, "correctness: ~"}},
		"a model the pinned catalog does not list": {
			subagents: map[string]*string{"correctness": strp("openai/gpt-5.6-nova")},
			want:      []string{`subagents.correctness: model "gpt-5.6-nova" is not in the bundled catalog`}},
		"the runner variable": {env: "opus", want: []string{codexSubagentModelEnv, `"opus"`, "unset"}},
		"a persona codex skipped": {
			subagents: map[string]*string{"worker": strp("openai/gpt-5.6-sol")},
			want:      []string{`subagents.worker: persona "worker" was not registered: name is reserved on codex`}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(codexSubagentModelEnv, tc.env)
			r, logPath, err := codexDelegatingBootstrap(t, t.TempDir(), tc.subagents)
			require.Error(t, err)
			for _, w := range tc.want {
				assert.Contains(t, err.Error(), w)
			}
			assert.NotContains(t, readFileString(t, logPath), r.codexConfigPath(),
				"the model check runs before config.toml is written")
		})
	}
}

// The check applies to a delegating agent only. An agent without the Agent
// tool, any agent with harness security off, and a delegating agent whose
// parent model codex does not resolve to collaboration V1 (a V2 or unlisted
// parent) keep codex's multi-agent tools off and ignore subagents (ADR 0126:
// "Every other agent runs with Codex's multi-agent tools off").
func TestCodexRuntimeBootstrap_NonDelegatingAgentIgnoresSubagents(t *testing.T) {
	t.Setenv(codexSubagentModelEnv, "")
	bad := map[string]*string{"correctness": strp("xai-vertex/xai/grok-4.6")}
	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{})
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", codexTestPersona)

	for name, tc := range map[string]struct {
		input     BootstrapInput
		preflight bool // part two of the gate read the catalog
	}{
		"no Agent tool, security on": {codexHooksBootstrapInput{
			bootstrapInput: bootstrapInput{sandboxName: "sb", agentPath: writeAgentFile(t, codexTestAgentDef), agentName: "triage", agentSubagents: bad},
			hooks:          hooks,
		}, false},
		"Agent tool, security off": {bootstrapInput{
			sandboxName: "sb", agentPath: writeAgentFile(t, codexTestDelegatingAgentDef), agentName: "review", skillDirs: []string{skill}, agentSubagents: bad,
		}, false},
		"Agent tool, security on, run-level parent selects V2": {codexHooksBootstrapInput{
			bootstrapInput: bootstrapInput{sandboxName: "sb", agentPath: writeAgentFile(t, codexTestDelegatingAgentDef), agentName: "review",
				skillDirs: []string{skill}, parentModel: "openai/gpt-5.6-sol", agentSubagents: bad},
			hooks: hooks,
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			storeDir := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "openshell.log")
			fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")
			forgetRunnerHeldDigests("sb")
			t.Cleanup(func() { forgetRunnerHeldDigests("sb") })

			r := CodexRuntime{}
			require.NoError(t, r.Bootstrap(tc.input), "a bad subagents value is ignored: the chain is never resolved")
			log := readFileString(t, logPath)
			assert.Equal(t, tc.preflight, strings.Contains(log, "codex debug models --bundled"))
			assert.NotContains(t, log, r.codexRolesDir(), "no role directory, no role file")
			cfg := string(storedUpload(t, storeDir, r.codexConfigPath()))
			assert.Contains(t, cfg, "[agents]\nenabled = false")
			assert.Contains(t, cfg, "No fullsend sub-agent roster is available")
		})
	}
}

// TestCodexSubagentNote_CarriesTheDispatchContract pins the sentences the
// agents repository's pr-review skill keys on and the content the note must
// carry: the spawn arguments, the four-slot rule with close, no resume, only
// the registered roles, an errored child final, and the chunked-read
// paragraph byte-equal to the single-context note's.
func TestCodexSubagentNote_CarriesTheDispatchContract(t *testing.T) {
	t.Parallel()

	roles := []codexRole{{Name: "challenger"}, {Name: "correctness"}, {Name: "default"}, {Name: "explore"}, {Name: "security"}}
	got := codexSubagentNote(roles)

	assert.True(t, strings.HasPrefix(got, "\n## Runtime note\n\n"), "the heading the pr-review skill looks for")
	// The first sentence.
	assert.Contains(t, got, "This agent runs on the codex runtime (FULLSEND_RUNTIME=codex).")
	// The roster sentence, in roster order (codexRoles sorts).
	assert.Contains(t, got, "Registered sub-agent roles: challenger, correctness, default, explore, security.")
	// The denied-spawn sentence, verbatim.
	assert.Contains(t, got, "A denied spawn is final for that role: do not retry it, execute that sub-agent "+
		"definition yourself with the same context package, and treat the output as that sub-agent's result.")
	assert.NotContains(t, got, "No fullsend sub-agent roster")

	for _, want := range []string{
		"`spawn_agent`", "`agent_type`", "`message`",
		"`explore`", "for a task that names no persona, omit `agent_type`",
		"`fork_context: false`", "never pass a `model` or `reasoning_effort` argument",
		"`explorer` and `worker`",
		"Spawn only the sub-agents the skill selected",
		"At most four children can be open at once", "keeps its slot until it is closed",
		"`wait_agent` on one child at a time", "`close_agent`",
		"Close every child you spawned and never resume one",
		"`timed_out: true` and no status has timed out, not failed: call it again",
		"`errored` is final for that role",
	} {
		assert.Contains(t, got, want)
	}
	assert.True(t, strings.HasSuffix(got, codexChunkedReadNote), "the chunked-read paragraph, byte-equal")
	assert.Equal(t, 1, strings.Count(got, "contiguous ranges of at most 200 lines"), "the chunked-read rule appears once")
	assert.NotContains(t, got, "You are a sub-agent", "the child contract is the role file's (codexChildNote), not the parent's")
}

func TestCodexDeveloperInstructions_DelegatingAgentGetsTheDispatchNote(t *testing.T) {
	t.Parallel()

	def, err := parsePiAgent([]byte(codexTestDelegatingAgentDef))
	require.NoError(t, err)
	roles := []codexRole{{Name: "correctness"}, {Name: "default"}, {Name: "explore"}}

	got := codexDeveloperInstructions("review", def, roles)
	assert.Contains(t, got, "# Agent: review")
	assert.Contains(t, got, "Review a pull request.")
	assert.Contains(t, got, "You are the review agent.")
	assert.Contains(t, got, "Registered sub-agent roles: correctness, default, explore.")
	assert.Contains(t, got, "A denied spawn is final for that role")
	assert.Contains(t, got, "at most 200 lines")
	assert.NotContains(t, got, "No fullsend sub-agent roster")

	single := codexDeveloperInstructions("review", def, nil)
	assert.Contains(t, single, "No fullsend sub-agent roster is available")
	assert.NotContains(t, single, "Registered sub-agent roles")
	assert.Equal(t, strings.TrimSuffix(single, codexNoSubagentNote), strings.TrimSuffix(got, codexSubagentNote(roles)),
		"the two notes are the only difference")
}

// TestCodexRuntimeBootstrap_DelegatingAgentRunsNativeChildren is the switch:
// with the Agent tool, harness security on and a V1 parent (the ADR 0126
// gate), config.toml turns the multi-agent tools on with the roster, the
// persona's own model lands in its role file and in the runner-held digest,
// and the dispatch note replaces the single-context note.
func TestCodexRuntimeBootstrap_DelegatingAgentRunsNativeChildren(t *testing.T) {
	t.Setenv(codexSubagentModelEnv, "")
	storeDir := t.TempDir()
	r, logPath, err := codexDelegatingBootstrap(t, storeDir, map[string]*string{"correctness": strp("openai/gpt-5.6-sol")})
	require.NoError(t, err)

	cfg := string(storedUpload(t, storeDir, r.codexConfigPath()))
	assert.Contains(t, cfg, "[agents]\nenabled = true\nmax_concurrent_threads_per_session = 4\nmax_depth = 1\ndefault_subagent_model = \"gpt-5.6-luna\"\n")
	for _, role := range []string{"correctness", "default", "explore"} {
		assert.Contains(t, cfg, "[agents.\""+role+"\"]\n")
		assert.Contains(t, cfg, "config_file = \""+r.codexRolesDir()+"/"+role+".toml\"\n")
	}
	assert.NotContains(t, cfg, "[agents]\nenabled = false")
	assert.Contains(t, cfg, "trust_level = \"untrusted\"", "the project pin stays")
	assert.Contains(t, cfg, "Registered sub-agent roles: correctness, default, explore.")
	assert.Contains(t, cfg, "A denied spawn is final for that role")
	assert.Contains(t, cfg, "at most 200 lines", "the chunked-read rule stays (#7832)")
	assert.NotContains(t, cfg, "No fullsend sub-agent roster")

	correctness := storedUpload(t, storeDir, r.codexRolesDir()+"/correctness.toml")
	assert.Contains(t, string(correctness), "\nmodel = \"gpt-5.6-sol\"\n")
	assert.NotContains(t, string(storedUpload(t, storeDir, r.codexRolesDir()+"/default.toml")), "model = ")
	assert.NotContains(t, string(storedUpload(t, storeDir, r.codexRolesDir()+"/explore.toml")), "model = ")

	held, ok := lookupRunnerHeldDigests("sb")
	require.True(t, ok)
	assert.Equal(t, codexAssetSHA256(correctness), held.RoleFiles["correctness"], "the digest covers the model line")
	assert.Equal(t, codexAssetSHA256([]byte(cfg)), held.ConfigTOML)
	assert.Contains(t, readFileString(t, logPath), r.codexHooksPath(), "hooks.json is still written: the guard is installed")
}

// The run's effort reaches config.toml as every child's effort, and an
// effort a child model lacks fails Bootstrap before config.toml is written;
// an agent that does not delegate gets neither, whatever its effort.
func TestCodexRuntimeBootstrap_BindsTheRunEffort(t *testing.T) {
	t.Setenv(codexSubagentModelEnv, "")
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", codexTestPersona)
	run := func(t *testing.T, input codexHooksBootstrapInput) (CodexRuntime, string, string, error) {
		storeDir := t.TempDir()
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "openshell.log"), storeDir, "codex-cli 0.152.1")
		forgetRunnerHeldDigests("sb")
		t.Cleanup(func() { forgetRunnerHeldDigests("sb") })
		r := CodexRuntime{}
		var err error
		out := captureStderr(t, func() { err = r.Bootstrap(input) })
		return r, storeDir, out, err
	}

	t.Run("delegating, effort high", func(t *testing.T) {
		input := codexDelegatingHooksInput(t, skill)
		input.effort = "high"
		r, storeDir, out, err := run(t, input)
		require.NoError(t, err)
		assert.Contains(t, string(storedUpload(t, storeDir, r.codexConfigPath())), "default_subagent_reasoning_effort = \"high\"\n")
		assert.Contains(t, out, "subagents: effort → high (from the run's effort; every child)")
	})
	t.Run("delegating, no effort", func(t *testing.T) {
		r, storeDir, out, err := run(t, codexDelegatingHooksInput(t, skill))
		require.NoError(t, err)
		assert.NotContains(t, string(storedUpload(t, storeDir, r.codexConfigPath())), "default_subagent_reasoning_effort")
		assert.Contains(t, out, "subagents: effort → medium (from the catalog default of gpt-5.6-luna; every child)")
	})
	t.Run("delegating, effort max with a gpt-5.5 default", func(t *testing.T) {
		input := codexDelegatingHooksInput(t, skill)
		input.effort = "max"
		input.agentSubagents = map[string]*string{"default": strp("openai/gpt-5.5")}
		r, storeDir, _, err := run(t, input)
		require.ErrorContains(t, err, `subagents.default: model "gpt-5.5" does not support reasoning effort "max"`)
		_, statErr := os.Stat(filepath.Join(storeDir, strings.ReplaceAll(r.codexConfigPath(), "/", "_")))
		assert.True(t, os.IsNotExist(statErr), "config.toml is not written")
	})
	t.Run("not delegating, effort max", func(t *testing.T) {
		input := codexHooksBootstrapInput{
			bootstrapInput: bootstrapInput{sandboxName: "sb", agentPath: writeAgentFile(t, codexTestAgentDef), agentName: "triage", skillDirs: []string{skill}, effort: "max",
				agentSubagents: map[string]*string{"default": strp("openai/gpt-5.5")}},
			hooks: security.SandboxHookConfigFromHarness(&harness.Harness{}),
		}
		r, storeDir, out, err := run(t, input)
		require.NoError(t, err)
		assert.NotContains(t, string(storedUpload(t, storeDir, r.codexConfigPath())), "default_subagent_reasoning_effort")
		assert.NotContains(t, out, "subagents:")
	})
}

// FULLSEND_CODEX_SUBAGENT_MODEL beats subagents.default for the run-level
// default; a persona without its own entry still gets no model line.
func TestCodexRuntimeBootstrap_SubagentModelEnvBeatsTheConfigDefault(t *testing.T) {
	t.Setenv(codexSubagentModelEnv, "openai/gpt-5.6-terra")
	storeDir := t.TempDir()
	r, _, err := codexDelegatingBootstrap(t, storeDir, map[string]*string{"default": strp("openai/gpt-5.6-sol")})
	require.NoError(t, err)

	cfg := string(storedUpload(t, storeDir, r.codexConfigPath()))
	assert.Contains(t, cfg, "default_subagent_model = \"gpt-5.6-terra\"\n")
	assert.NotContains(t, cfg, "gpt-5.6-sol")
	assert.NotContains(t, string(storedUpload(t, storeDir, r.codexRolesDir()+"/correctness.toml")), "model = ",
		"the run-level default is bound in config.toml, not copied into role files")
}
