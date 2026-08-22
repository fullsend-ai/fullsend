package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// fakeOpenshellPi installs a fake "openshell" that records every argv line
// to logPath, stores each "sandbox upload <name> <local> <remote>" payload
// under storeDir keyed by the remote path, answers `pi --version` and `cat
// <remote>` execs from that store, and streams streamFixture for the pi run
// command. Everything else succeeds silently.
func fakeOpenshellPi(t *testing.T, logPath, storeDir, streamFixture string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(storeDir, 0o755))
	binDir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> '` + logPath + `'
if [ "$2" = "upload" ]; then
  cp "$4" '` + storeDir + `'/"$(printf '%s' "$5" | tr '/' '_')"
  exit 0
fi
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    "pi --version") echo "0.84.2"; exit 0 ;;
    cat\ *) f=$(printf '%s' "${last#cat }" | tr -d "'" | tr '/' '_'); cat '` + storeDir + `'/"$f"; exit $? ;;
    *"pi --print --mode json"*) cat '` + streamFixture + `'; exit 0 ;;
  esac
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func storedUpload(t *testing.T, storeDir, remotePath string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(storeDir, strings.ReplaceAll(remotePath, "/", "_")))
	require.NoError(t, err, "expected an upload to %s", remotePath)
	return data
}

type piHooksBootstrapInput struct {
	bootstrapInput
	hooks security.SandboxHookConfig
}

func (b piHooksBootstrapInput) SandboxHookConfig() security.SandboxHookConfig { return b.hooks }

func writeAgentFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "triage.md")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

const testAgentDef = `---
name: triage
description: Inspect an issue.
tools: Bash(gh,jq),Skill
model: opus
---
You are the triage agent. Use gh.
`

func TestPiRuntimeBootstrap_WritesConfigAndManifest(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, logPath, store, "/dev/null")

	skillDir := filepath.Join(t.TempDir(), "issue-labels")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: issue-labels\n---\n# labels"), 0o644))

	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{}}}
	in := piHooksBootstrapInput{
		bootstrapInput: bootstrapInput{
			sandboxName: "sb",
			agentPath:   writeAgentFile(t, testAgentDef),
			agentName:   "triage",
			skillDirs:   []string{skillDir},
			pluginDirs:  []string{"/tmp/some-plugin"},
		},
		hooks: security.SandboxHookConfigFromHarness(h),
	}
	require.NoError(t, PiRuntime{}.Bootstrap(in))

	cfg := PiRuntime{}.ConfigDir()
	appendSystem := string(storedUpload(t, store, cfg+"/APPEND_SYSTEM.md"))
	assert.True(t, strings.HasPrefix(appendSystem, "# Agent: triage\n\nInspect an issue.\n\nYou are the triage agent."), appendSystem)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(storedUpload(t, store, cfg+"/settings.json"), &settings))
	assert.Equal(t, "never", settings["defaultProjectTrust"])
	assert.Equal(t, false, settings["enableSkillCommands"])

	ext := string(storedUpload(t, store, cfg+"/fullsend-hooks.js"))
	assert.Contains(t, ext, "export default function")

	var m piManifest
	require.NoError(t, json.Unmarshal(storedUpload(t, store, cfg+"/fullsend-manifest.json"), &m))
	assert.Equal(t, "triage", m.AgentName)
	assert.Equal(t, "opus", m.Model)
	assert.Equal(t, []string{"bash", "read"}, m.Tools, "Skill (and shipped skills) need pi's read tool for the skills prompt section")
	assert.Equal(t, []string{"gh", "jq"}, m.BashAllowlist)
	assert.Equal(t, "warn", m.BashAllowlistMode, "advisory by default (ADR 0027 parity)")
	assert.Equal(t, "0.84.2", m.PiVersion)
	require.NotNil(t, m.Hooks)
	assert.Equal(t, cfg+"/hooks", m.Hooks.Dir)
	assert.Equal(t, "Bash", m.Hooks.ToolNames["bash"])
	var phases []string
	for _, g := range m.Hooks.Groups {
		phases = append(phases, g.Phase)
	}
	assert.Contains(t, phases, "PreToolUse")
	assert.Contains(t, phases, "PostToolUse")

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	logStr := string(log)
	assert.Contains(t, logStr, "mkdir -p '"+cfg+"/skills' '"+cfg+"/sessions' '"+cfg+"/hooks'")
	assert.Contains(t, logStr, "pi --version")
	assert.Contains(t, logStr, cfg+"/hooks/tirith_check.py", "hook scripts are installed under the pi config dir")
	// Skills go through the tar path; the archive lands under skills/.
	assert.Contains(t, logStr, cfg+"/skills/")
}

func TestPiRuntimeBootstrap_NoSecurityNoHooks(t *testing.T) {
	t.Setenv(piBashAllowlistEnv, "enforce")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")

	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, "---\nname: code\n---\nbody"),
		agentName:   "code",
	}))
	var m piManifest
	require.NoError(t, json.Unmarshal(storedUpload(t, store, PiRuntime{}.ConfigDir()+"/fullsend-manifest.json"), &m))
	assert.Nil(t, m.Hooks)
	assert.Nil(t, m.Tools)
	assert.Equal(t, "enforce", m.BashAllowlistMode, "FULLSEND_PI_BASH_ALLOWLIST=enforce opts into blocking")
	_, err := os.Stat(filepath.Join(store, strings.ReplaceAll(PiRuntime{}.ConfigDir()+"/fullsend-hooks.js", "/", "_")))
	assert.True(t, os.IsNotExist(err), "no hook extension without security config")
}

func TestPiRuntimeBootstrap_PreflightFailure(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\nfor last; do :; done\ncase \"$last\" in \"pi --version\") echo 'sh: pi: not found' >&2; exit 127 ;; esac\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := PiRuntime{}.Bootstrap(bootstrapInput{sandboxName: "sb", agentPath: writeAgentFile(t, "---\nname: x\n---\nb"), agentName: "x"})
	require.ErrorContains(t, err, "pi preflight")
	assert.Contains(t, err.Error(), "exited 127")
}

func TestPiRuntimeRun_StreamsFixtureAndReportsMetrics(t *testing.T) {
	t.Setenv(piModelEnv, "")
	t.Setenv(piProviderEnv, "")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fixture, err := filepath.Abs(filepath.Join("testdata", "pi", "basic_run.ndjson"))
	require.NoError(t, err)
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, fixture)

	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: writeAgentFile(t, testAgentDef), agentName: "triage",
	}))

	var events []AgentEvent
	var metrics RunMetrics
	outPath := filepath.Join(work, "output.jsonl")
	exit, err := PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", AgentBaseName: "triage", RepoDir: "/sandbox/workspace/repo",
		Timeout: 30 * time.Second, OutputPath: outPath,
		OnEvent: func(e AgentEvent) { events = append(events, e) },
	}, ui.New(os.Stderr), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)

	require.NotEmpty(t, events)
	init, ok := events[0].(InitEvent)
	require.True(t, ok, "InitEvent is emitted first")
	assert.Equal(t, "claude-opus-4-6", init.Model, "bare model id, as Claude Code reports it")
	assert.Equal(t, "0.84.2", init.Version, "pi version comes from Bootstrap's preflight")
	inits := 0
	for _, e := range events {
		if _, ok := e.(InitEvent); ok {
			inits++
		}
	}
	assert.Equal(t, 1, inits, "the parser's own InitEvent is suppressed")

	assert.Equal(t, 1, metrics.NumTurns)
	assert.Equal(t, int32(1), metrics.ToolCalls.Load())
	assert.Equal(t, "claude-opus-4-6", metrics.Model)
	assert.InDelta(t, 0.015, metrics.TotalCostUSD, 0.001)

	teed, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(teed), `"type":"agent_end"`)
}

func TestPiRuntimeRun_ExitZeroWithStreamErrorReturnsOne(t *testing.T) {
	t.Setenv(piModelEnv, "")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fixture, err := filepath.Abs(filepath.Join("testdata", "pi", "error_run.ndjson"))
	require.NoError(t, err)
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, fixture)
	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: writeAgentFile(t, testAgentDef), agentName: "triage",
	}))

	var metrics RunMetrics
	exit, err := PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", RepoDir: "/r", Timeout: 30 * time.Second,
		OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 1, exit, "pi's exit 0 on model error is overridden by the stream verdict")
}

func TestPiRuntimeRun_MissingHookAdapterFailsClosed(t *testing.T) {
	t.Setenv(piModelEnv, "")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	// Bootstrap with security on (so the manifest carries a hook plan),
	// then replace the fake so the run command's guard fails the way a
	// deleted or modified adapter would (exit 97).
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")
	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{}}}
	require.NoError(t, PiRuntime{}.Bootstrap(piHooksBootstrapInput{
		bootstrapInput: bootstrapInput{sandboxName: "sb", agentPath: writeAgentFile(t, testAgentDef), agentName: "triage"},
		hooks:          security.SandboxHookConfigFromHarness(h),
	}))
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    cat\ *) f=$(printf '%s' "${last#cat }" | tr -d "'" | tr '/' '_'); cat '` + store + `'/"$f"; exit $? ;;
    *"exit 97"*) echo 'fullsend: pi hook adapter or manifest missing' >&2; exit 97 ;;
  esac
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	exit, err := PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", RepoDir: "/r", Timeout: 30 * time.Second, HooksSettingsPath: "/sandbox/claude-config/hooks.json",
		OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &RunMetrics{})
	assert.Equal(t, piHooksMissingExit, exit)
	require.ErrorContains(t, err, "hook adapter or manifest missing")
}

func TestPiRuntimeRun_SecurityOnButManifestWithoutHooksFailsFast(t *testing.T) {
	t.Setenv(piModelEnv, "")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	logPath := filepath.Join(work, "openshell.log")
	fakeOpenshellPi(t, logPath, store, "/dev/null")
	// Bootstrap without the hook config (or a manifest rewritten to drop
	// it): Run must refuse before starting pi rather than let the adapter
	// block every tool call for a whole iteration.
	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: writeAgentFile(t, testAgentDef), agentName: "triage",
	}))
	exit, err := PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", RepoDir: "/r", Timeout: 30 * time.Second, HooksSettingsPath: "/sandbox/claude-config/hooks.json",
		OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &RunMetrics{})
	assert.Equal(t, -1, exit)
	require.ErrorContains(t, err, "carries no hook plan")
	log, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.NotContains(t, string(log), "pi --print", "pi must not have been started")

	// A hooks object without a groups array is what the adapter's `wired`
	// check rejects too; Run must apply the same predicate.
	manifestStore := filepath.Join(store, strings.ReplaceAll(PiRuntime{}.piManifestPath(), "/", "_"))
	require.NoError(t, os.WriteFile(manifestStore, []byte(`{"agentName":"triage","hooks":{"dir":"/sandbox/pi-config/hooks"}}`), 0o644))
	exit, err = PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", RepoDir: "/r", Timeout: 30 * time.Second, HooksSettingsPath: "/sandbox/claude-config/hooks.json",
		OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &RunMetrics{})
	assert.Equal(t, -1, exit)
	require.ErrorContains(t, err, "carries no hook plan")
	log, readErr = os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.NotContains(t, string(log), "pi --print", "pi must not have been started")
}

func TestPiRuntimeBootstrap_SkillDirsOnlyAddsRead(t *testing.T) {
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")

	skillDir := filepath.Join(t.TempDir(), "issue-labels")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# labels"), 0o644))

	// No Skill in tools:, but a shipped skill dir still needs pi's read
	// tool for the skills section of the system prompt to be emitted.
	in := bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, "---\nname: code\ntools: Bash(go)\n---\nBody"),
		agentName:   "code",
		skillDirs:   []string{skillDir},
	}
	require.NoError(t, PiRuntime{}.Bootstrap(in))

	var m piManifest
	require.NoError(t, json.Unmarshal(storedUpload(t, store, PiRuntime{}.ConfigDir()+"/fullsend-manifest.json"), &m))
	assert.Equal(t, []string{"bash", "read"}, m.Tools)
	assert.Equal(t, []string{"go"}, m.BashAllowlist)
}

func TestPiRuntimeExtractTranscripts(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	binDir := t.TempDir()
	// find lists two sessions (one nested); download writes a session
	// file into the requested local dir, as `openshell sandbox download`
	// does. Local names come from the remote basename, so the nested entry
	// lands flat in outputDir.
	script := `#!/bin/sh
echo "$@" >> '` + logPath + `'
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    find\ *) printf '%s\n' '/sandbox/pi-config/sessions/2026-08-22T10-00-00_abc.jsonl' '/sandbox/pi-config/sessions/sub/2026-08-22T11-00-00_def.jsonl'; exit 0 ;;
  esac
  exit 0
fi
if [ "$2" = "download" ]; then
  printf '{"type":"message","message":{"role":"assistant","stopReason":"stop"}}\n' > "$5/$(basename "$4")"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := filepath.Join(work, "transcripts")
	require.NoError(t, PiRuntime{}.ExtractTranscripts("sb", "triage", out))
	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"triage-2026-08-22T10-00-00_abc.jsonl", "triage-2026-08-22T11-00-00_def.jsonl"}, names)
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "find '/sandbox/pi-config/sessions' -name '*.jsonl'")
	assert.Empty(t, PiRuntime{}.ParseTranscriptErrors(out), "clean sessions produce no error annotations")
}

func TestPiRuntimeClearIterationArtifacts(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	fakeOpenshellPi(t, logPath, filepath.Join(work, "store"), "/dev/null")
	require.NoError(t, PiRuntime{}.ClearIterationArtifacts("sb"))
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "rm -rf '/sandbox/workspace'/output/* '/sandbox/pi-config/sessions'/* '/sandbox/workspace/pi-debug.log'")
}
