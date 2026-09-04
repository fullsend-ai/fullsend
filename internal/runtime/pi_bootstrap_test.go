package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// piFakeUsageFile is where the fake serves the Agent extension's usage
// file from: Run reads it with a `mv` + `head -c` fragment
// (piSubagentUsageReadCommand), which the store's remote-path keying cannot
// express. A test that wants sub-agent usage writes this file; absent, the
// read is empty, as for a run that dispatched no sub-agent. The fake
// renames it away once read, the way the real command does, so a test can
// observe that a second read folds nothing.
const piFakeUsageFile = "subagent-usage.jsonl"

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
    "command -v pi"*) printf '%s\n' /usr/bin/pi '/usr/local/share/pi-extensions/anthropic-vertex'; exit 0 ;;
    *usage.jsonl*mv*) u='` + storeDir + `'/` + piFakeUsageFile + `; if [ -f "$u" ]; then cat "$u"; mv -f "$u" "$u.read"; fi; exit 0 ;;
    cat\ *) f=$(printf '%s' "${last#cat }" | tr -d "'" | tr '/' '_'); cat '` + storeDir + `'/"$f"; exit $? ;;
    *"--print --mode json"*) cat '` + streamFixture + `'; exit 0 ;;
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
			plugins:     claudePlugins("/tmp/some-plugin"),
		},
		hooks: security.SandboxHookConfigFromHarness(h),
	}
	require.NoError(t, PiRuntime{}.Bootstrap(in))

	cfg := PiRuntime{}.ConfigDir()
	appendSystem := string(storedUpload(t, store, cfg+"/APPEND_SYSTEM.md"))
	assert.Contains(t, appendSystem, "## Runtime note", "the no-sub-agent note is appended so skills take their single-context path deliberately")
	assert.Contains(t, appendSystem, "No sub-agent tool (Agent/Task) is available")
	assert.True(t, strings.HasPrefix(appendSystem, "# Agent: triage\n\nInspect an issue.\n\nYou are the triage agent."), appendSystem)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(storedUpload(t, store, cfg+"/settings.json"), &settings))
	assert.Equal(t, "never", settings["defaultProjectTrust"])
	assert.Equal(t, false, settings["enableSkillCommands"])
	assert.Equal(t, []any{"read", "bash", "edit", "write", "grep", "find", "ls"}, settings["defaultTools"])

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
	assert.Contains(t, logStr, "mkdir -p '"+cfg+"/skills' '"+cfg+"/extensions' '"+cfg+"/sessions' '"+cfg+"/hooks'")
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
	var settings map[string]any
	require.NoError(t, json.Unmarshal(storedUpload(t, store, PiRuntime{}.ConfigDir()+"/settings.json"), &settings))
	assert.Equal(t, []any{"read", "bash", "edit", "write", "grep", "find", "ls"}, settings["defaultTools"],
		"an agent without tools: frontmatter gets every built-in through defaultTools, not pi's four-tool default")
	assert.Equal(t, "enforce", m.BashAllowlistMode, "FULLSEND_PI_BASH_ALLOWLIST=enforce opts into blocking")
	_, err := os.Stat(filepath.Join(store, strings.ReplaceAll(PiRuntime{}.ConfigDir()+"/fullsend-hooks.js", "/", "_")))
	assert.True(t, os.IsNotExist(err), "no hook extension without security config")
}

func TestPiRuntimeBootstrap_AgentNameMismatch(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, logPath, store, "/dev/null")

	agentFile := writeAgentFile(t, "---\nname: code\n---\n# Code agent")
	err := PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   agentFile,
		agentName:   "coder",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent name mismatch")
	assert.Contains(t, err.Error(), `"coder"`)
	assert.Contains(t, err.Error(), `"code"`)
}

func TestPiRuntimeBootstrap_AgentNameMatch(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, logPath, store, "/dev/null")

	agentFile := writeAgentFile(t, "---\nname: triage\n---\n# Triage agent")
	err := PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   agentFile,
		agentName:   "triage",
	})
	assert.NoError(t, err)
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
	t.Setenv("FULLSEND_PI_MODEL", "")
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

// TestPiRuntimeRun_FoldsSubagentUsage: a child's tokens never reach the
// parent's --mode json stream, so Run reads the Agent extension's usage
// file after the iteration and adds it to the run metrics, with the
// per-model breakdown that makes the totals attributable.
func TestPiRuntimeRun_FoldsSubagentUsage(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	t.Setenv(piAgentThinkingEnv, "")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fixture, err := filepath.Abs(filepath.Join("testdata", "pi", "basic_run.ndjson"))
	require.NoError(t, err)
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, fixture)

	// No tools: frontmatter → the Agent tool is on, so Run reads the file.
	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: writeAgentFile(t, "---\nname: review\nmodel: opus\n---\nReview."), agentName: "review",
	}))
	require.NoError(t, os.WriteFile(filepath.Join(store, piFakeUsageFile), []byte(
		`{"seq":1,"model":"anthropic-vertex/claude-sonnet-4-6","usage":{"input":300,"output":40,"cacheRead":10,"cacheWrite":5,"cost":0.2},"stopReason":"stop","isError":false}`+"\n"+
			`{"seq":2,"model":"anthropic-vertex/claude-sonnet-4-6","usage":{"input":100,"output":10,"cost":0.1},"stopReason":"error","isError":true}`+"\n"), 0o644))

	var metrics RunMetrics
	exit, err := PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", AgentBaseName: "review", RepoDir: "/sandbox/workspace/repo",
		Timeout: 30 * time.Second, OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)

	require.NotNil(t, metrics.PerModelUsage)
	parent := metrics.PerModelUsage["anthropic-vertex/claude-opus-4-6"]
	child := metrics.PerModelUsage["anthropic-vertex/claude-sonnet-4-6"]
	assert.Equal(t, 1, parent.Requests, "the parent's own iteration is one request, keyed by its full model spec")
	assert.Equal(t, 2, child.Requests, "a failed sub-agent still spent tokens")
	assert.InDelta(t, 0.3, child.CostUSD, 1e-9)
	assert.InDelta(t, parent.CostUSD+child.CostUSD, metrics.TotalCostUSD, 1e-9, "the breakdown sums to the run total")
	assert.Equal(t, parent.InputTokens+child.InputTokens, metrics.InputTokens)
	assert.Equal(t, parent.CacheReadInputTokens+10, metrics.CacheReadInputTokens)
	assert.Equal(t, parent.CacheCreationInputTokens+5, metrics.CacheCreationInputTokens)
	assert.Equal(t, "claude-opus-4-6", metrics.Model, "the reported model stays the bare parent id")

	// The read consumes the file, so a retry iteration whose
	// ClearIterationArtifacts failed cannot count the same children twice.
	var again RunMetrics
	_, err = PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", AgentBaseName: "review", RepoDir: "/sandbox/workspace/repo",
		Timeout: 30 * time.Second, OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &again)
	require.NoError(t, err)
	assert.NotContains(t, again.PerModelUsage, "anthropic-vertex/claude-sonnet-4-6", "the children were already folded")
	assert.Len(t, again.PerModelUsage, 1, "only the parent's own iteration")
}

// Without sub-agent usage the totals are exactly what the stream reported,
// and the breakdown is the parent's single entry.
func TestPiRuntimeRun_NoSubagentUsageLeavesMetricsAlone(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	work := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("testdata", "pi", "basic_run.ndjson"))
	require.NoError(t, err)
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), filepath.Join(work, "store"), fixture)
	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: writeAgentFile(t, "---\nname: review\nmodel: opus\n---\nReview."), agentName: "review",
	}))

	var metrics RunMetrics
	_, err = PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", AgentBaseName: "review", RepoDir: "/sandbox/workspace/repo",
		Timeout: 30 * time.Second, OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &metrics)
	require.NoError(t, err)
	assert.InDelta(t, 0.015, metrics.TotalCostUSD, 0.001)
	// The parent's own entry is still recorded: iterations are summed, so
	// an iteration missing from the breakdown makes the run's
	// per_model_usage stop matching its totals.
	require.Len(t, metrics.PerModelUsage, 1)
	parent := metrics.PerModelUsage["anthropic-vertex/claude-opus-4-6"]
	assert.Equal(t, 1, parent.Requests)
	assert.InDelta(t, metrics.TotalCostUSD, parent.CostUSD, 1e-9, "no children means the parent entry is the whole breakdown")
}

func TestPiRuntimeRun_ExitZeroWithStreamErrorReturnsOne(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
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
	t.Setenv("FULLSEND_PI_MODEL", "")
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
	assert.NotContains(t, err.Error(), "Agent extension",
		"the Agent guard has its own exit code; naming it here would send the operator looking at the wrong artifact")
}

// TestPiRuntimeRun_TamperedAgentExtensionFailsClosed is the counterpart of
// the hook-adapter case: the Agent extension's guard exits with its own
// code, so Run names that extension instead of the hook adapter. Hooks are
// off here, which is exactly the run where the old shared code was
// ambiguous — nothing else in the command line exits 97.
func TestPiRuntimeRun_TamperedAgentExtensionFailsClosed(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	forgetPiManifestHash(t, "sb")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")
	// No tools: frontmatter means the default set, which carries the Agent
	// tool — so Run emits the extension's -e and its guard.
	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: writeAgentFile(t, "---\nname: review\nmodel: opus\n---\nReview the PR."), agentName: "review",
	}))
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    cat\ *) f=$(printf '%s' "${last#cat }" | tr -d "'" | tr '/' '_'); cat '` + store + `'/"$f"; exit $? ;;
    *"exit 94"*) echo 'fullsend: pi Agent extension missing or modified' >&2; exit 94 ;;
  esac
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	exit, err := PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", RepoDir: "/r", Timeout: 30 * time.Second,
		OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &RunMetrics{})
	assert.Equal(t, piAgentTamperedExit, exit)
	require.ErrorContains(t, err, "Agent extension missing or modified")
	assert.NotContains(t, err.Error(), "hook adapter")
}

func TestPiRuntimeRun_SecurityOnButManifestWithoutHooksFailsFast(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
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

// TestPiRuntime_ExtractTranscripts_Children covers the sub-agent artifacts:
// a child session under sessions/agent-<seq>/ is saved under a name that
// carries the sequence number (so several children with the same session
// basename do not collide), and the Agent extension's usage file comes down
// beside the transcripts.
func TestPiRuntime_ExtractTranscripts_Children(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	binDir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> '` + logPath + `'
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    if\ [\ -s\ *) printf '%s\n' '/sandbox/pi-config/subagents/usage.jsonl.read'; exit 0 ;;
    find\ *) printf '%s\n' '/sandbox/pi-config/sessions/2026-08-29T10-00-00_parent.jsonl' '/sandbox/pi-config/sessions/agent-1/2026-08-29T10-01-00_kid.jsonl' '/sandbox/pi-config/sessions/agent-12/repo/2026-08-29T10-01-00_kid.jsonl'; exit 0 ;;
  esac
  exit 0
fi
if [ "$2" = "download" ]; then
  case "$4" in
    *usage.jsonl) printf '{"seq":1,"model":"anthropic-vertex/claude-sonnet-4-6","usage":{"input":1,"output":2,"cost":0.1}}\n' > "$5/$(basename "$4")" ;;
    *) printf '{"type":"message","message":{"role":"assistant","stopReason":"stop"}}\n' > "$5/$(basename "$4")" ;;
  esac
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := filepath.Join(work, "transcripts")
	require.NoError(t, PiRuntime{}.ExtractTranscripts("sb", "review", out))
	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{
		"review-2026-08-29T10-00-00_parent.jsonl",
		"review-sub1-2026-08-29T10-01-00_kid.jsonl",
		"review-sub12-2026-08-29T10-01-00_kid.jsonl",
		"review-subagents-usage.jsonl",
	}, names, "children are named after the Agent call that spawned them, so equal basenames do not collide")
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "download sb /sandbox/pi-config/subagents/usage.jsonl.read",
		"Run consumed the usage file by renaming it; extraction has to follow the rename")
	assert.Empty(t, PiRuntime{}.ParseTranscriptErrors(out), "clean child sessions produce no error annotations")
}

// TestPiRuntime_ExtractTranscripts_UsageFileIsContained checks the usage
// file's local name goes through the same os.Root containment as the
// transcripts. Both names are built from agentLabel, which is the caller's,
// so joining either onto outputDir unchecked would let a label write
// outside the artifact directory.
func TestPiRuntime_ExtractTranscripts_UsageFileIsContained(t *testing.T) {
	work := t.TempDir()
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    if\ [\ -s\ *) printf '%s\n' '/sandbox/pi-config/subagents/usage.jsonl.read'; exit 0 ;;
    find\ *) printf '%s\n' '/sandbox/pi-config/sessions/2026-08-29T10-00-00_parent.jsonl'; exit 0 ;;
  esac
  exit 0
fi
if [ "$2" = "download" ]; then
  printf 'downloaded\n' > "$5/$(basename "$4")"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := filepath.Join(work, "artifacts", "transcripts")
	require.NoError(t, PiRuntime{}.ExtractTranscripts("sb", "../../escaped", out),
		"a rejected name is reported and skipped, not an extraction failure")

	// The usage file and a session transcript go through the same check,
	// and both names are built from the label, so neither may land outside
	// the output dir.
	for _, name := range []string{"escaped-subagents-usage.jsonl", "escaped-2026-08-29T10-00-00_parent.jsonl"} {
		assert.NoFileExists(t, filepath.Join(work, name),
			"a label that traverses out of the output dir must not place %s there", name)
		assert.NoFileExists(t, filepath.Join(work, "artifacts", name))
	}
	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	assert.Empty(t, entries, "the rejected names leave nothing behind either")
}

func TestPiSubagentTranscriptName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "review-s.jsonl", piSubagentTranscriptName("review", "/sandbox/pi-config/sessions/s.jsonl"))
	assert.Equal(t, "review-sub3-s.jsonl", piSubagentTranscriptName("review", "/sandbox/pi-config/sessions/agent-3/s.jsonl"))
	assert.Equal(t, "review-sub3-s.jsonl", piSubagentTranscriptName("review", "/sandbox/pi-config/sessions/agent-3/nested/s.jsonl"),
		"pi may nest a session under its working directory")
	assert.Equal(t, "review-agent-x.jsonl", piSubagentTranscriptName("review", "/sandbox/pi-config/sessions/agent-x.jsonl"),
		"only a directory named agent-<digits> marks a child")
}

// TestPiAgentTool_ManifestBlock covers the `agent` manifest block Bootstrap
// writes for the fullsend-agent.js extension: enabled/disabled cases, the
// model alias table, the extension list from the sandbox probe, and the
// thinking default plus its env override.
func TestPiAgentTool_ManifestBlock(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	t.Setenv(piAgentThinkingEnv, "")
	cfg := PiRuntime{}.ConfigDir()

	bootstrap := func(t *testing.T, agentDef string, hooks bool) (*piManifest, string, string) {
		t.Helper()
		forgetPiManifestHash(t, "sb")
		work := t.TempDir()
		store := filepath.Join(work, "store")
		fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")
		base := bootstrapInput{sandboxName: "sb", agentPath: writeAgentFile(t, agentDef), agentName: "review"}
		var in BootstrapInput = base
		if hooks {
			h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{}}}
			in = piHooksBootstrapInput{bootstrapInput: base, hooks: security.SandboxHookConfigFromHarness(h)}
		}
		require.NoError(t, PiRuntime{}.Bootstrap(in))
		var m piManifest
		require.NoError(t, json.Unmarshal(storedUpload(t, store, cfg+"/fullsend-manifest.json"), &m))
		return &m, store, string(storedUpload(t, store, cfg+"/APPEND_SYSTEM.md"))
	}

	t.Run("enabled without tools frontmatter, hooks on", func(t *testing.T) {
		m, store, appendSystem := bootstrap(t, "---\nname: review\nmodel: opus\n---\nReview the PR.", true)
		require.NotNil(t, m.Agent)
		assert.True(t, m.Agent.Enabled)
		assert.Equal(t, "/usr/bin/pi", m.Agent.PiBin, "resolved by the sandbox probe")
		assert.Equal(t, cfg+"/sessions", m.Agent.SessionsDir)
		assert.Equal(t, []string{piVertexExtensionPath, cfg + "/fullsend-hooks.js"}, m.Agent.Extensions,
			"only the provider extensions the image has (the probe found anthropic-vertex, not xai-vertex), then the hook adapter")
		assert.Equal(t, map[string]string{
			"default": "anthropic-vertex/claude-opus-4-6",
			"opus":    "anthropic-vertex/claude-opus-4-6",
			"sonnet":  "anthropic-vertex/claude-sonnet-4-6",
			"haiku":   "anthropic-vertex/claude-haiku-4-5",
			"fable":   "anthropic-vertex/claude-fable-5",
		}, m.Agent.Models)
		assert.Equal(t, map[string][]string{
			"google-vertex":     piGoogleVertexModels,
			piXaiVertexProvider: piXaiVertexModels,
		}, m.Agent.ProviderModels,
			"the extension needs a closed id list for every provider a run can serve with no model-table entry")
		assert.Contains(t, m.Agent.ProviderModels["google-vertex"], "gemini-3.7-flash", "the spec documented in docs/runtimes/pi.md")
		assert.Equal(t, []string{"xai/grok-4.6"}, m.Agent.ProviderModels[piXaiVertexProvider],
			"the publisher-qualified wire id the vendored extension registers, so xai-vertex/xai/grok-4.6 resolves and an invented Grok id does not")
		hooksExt := cfg + "/fullsend-hooks.js"
		require.Len(t, m.Agent.ExtensionDigests, 1,
			"the one child -e entry Bootstrap wrote into the config dir is digest-covered, so the extension can re-check it before every dispatch")
		require.NotEmpty(t, m.Agent.ExtensionDigests[hooksExt])
		assert.Contains(t, piHooksGuard(hooksExt, cfg+"/fullsend-manifest.json"), m.Agent.ExtensionDigests[hooksExt],
			"and against the same digest the launch guard checks, so the two cannot drift")
		assert.NotContains(t, m.Agent.ExtensionDigests, piVertexExtensionPath,
			"the vendored provider extension is root-owned and read-only in the image; nothing to re-check")
		assert.Equal(t, "medium", m.Agent.Thinking, "children default to medium: the roster overran the review budget at high")
		assert.Equal(t, piDefaultTools, m.Agent.Tools, "no tools: frontmatter → the default built-in set")
		assert.Equal(t, piExploreTools, m.Agent.ExploreTools)
		assert.Equal(t, piAgentMaxConcurrent, m.Agent.MaxConcurrent)
		assert.Equal(t, piAgentTimeoutSeconds, m.Agent.TimeoutSeconds)
		assert.Equal(t, cfg+"/subagents/usage.jsonl", m.Agent.UsageFile)
		assert.Nil(t, m.Tools, "the parent's --tools stays the default set")
		require.NotNil(t, m.Hooks)
		assert.Equal(t, "Agent", m.Hooks.ToolNames["Agent"], "the adapter reports Agent calls in Claude vocabulary")
		assert.Equal(t, "Task", m.Hooks.ToolNames["Task"])

		ext := string(storedUpload(t, store, cfg+"/fullsend-agent.js"))
		assert.Contains(t, ext, "export default function")
		assert.Contains(t, appendSystem, "## Runtime note")
		assert.Contains(t, appendSystem, "The Agent tool (alias Task)")
		assert.Contains(t, appendSystem, "several Agent calls in one message")
		assert.NotContains(t, appendSystem, "No sub-agent tool")
	})

	t.Run("enabled by a tools list naming Task, hooks off", func(t *testing.T) {
		m, _, _ := bootstrap(t, "---\nname: review\nmodel: claude-sonnet-4-6@default\ntools: Read, Grep, Task\n---\nbody", false)
		require.NotNil(t, m.Agent)
		assert.Equal(t, []string{"read", "grep", "Agent", "Task"}, m.Tools, "--tools carries both tool names")
		assert.Equal(t, []string{"read", "grep"}, m.Agent.Tools, "children get the built-ins only")
		assert.Equal(t, []string{piVertexExtensionPath}, m.Agent.Extensions, "no hook adapter without security")
		assert.Empty(t, m.Agent.ExtensionDigests, "and so nothing in the child -e list that Bootstrap wrote, hence no digests")
		assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", m.Agent.Models["default"], "the agent's own model, @suffix stripped, is the default for children")
		assert.Nil(t, m.Hooks)
	})

	t.Run("disabled by a tools list without Agent or Task", func(t *testing.T) {
		// Same shape as testAgentDef, named to match the requested agent so
		// Bootstrap's name-mismatch check does not fire first.
		m, store, appendSystem := bootstrap(t, "---\nname: review\ntools: Bash(gh,jq),Skill\nmodel: opus\n---\nbody", true)
		assert.Nil(t, m.Agent)
		assert.NotContains(t, m.Hooks.ToolNames, "Agent")
		_, err := os.Stat(filepath.Join(store, strings.ReplaceAll(cfg+"/fullsend-agent.js", "/", "_")))
		assert.True(t, os.IsNotExist(err), "no Agent extension upload when the tool is off")
		assert.Contains(t, appendSystem, "No sub-agent tool (Agent/Task) is available", "the single-context note stays for agents that opted out")
	})

	t.Run("thinking env override", func(t *testing.T) {
		t.Setenv(piAgentThinkingEnv, "low")
		m, _, _ := bootstrap(t, "---\nname: review\n---\nbody", false)
		assert.Equal(t, "low", m.Agent.Thinking)
		t.Setenv(piAgentThinkingEnv, "turbo")
		m, _, _ = bootstrap(t, "---\nname: review\n---\nbody", false)
		assert.Equal(t, "medium", m.Agent.Thinking, "an unknown level falls back to the default")
	})

	t.Run("provider env sets the default but not the Claude aliases", func(t *testing.T) {
		t.Setenv(piProviderEnv, piXaiVertexProvider)
		m, _, _ := bootstrap(t, "---\nname: review\nmodel: grok-4.6\n---\nbody", false)
		assert.Equal(t, "xai-vertex/xai/grok-4.6", m.Agent.Models["default"])
		assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", m.Agent.Models["sonnet"], "aliases are Claude models on the Anthropic Vertex provider regardless of the parent's provider")
	})

	t.Run("config aliases reach the sub-agent model table", func(t *testing.T) {
		forgetPiManifestHash(t, "sb")
		work := t.TempDir()
		store := filepath.Join(work, "store")
		fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")
		in := bootstrapInput{
			sandboxName:  "sb",
			agentPath:    writeAgentFile(t, "---\nname: review\nmodel: opus\n---\nReview the PR."),
			agentName:    "review",
			modelAliases: map[string]string{"sonnet": "claude-sonnet-5"},
		}
		require.NoError(t, PiRuntime{}.Bootstrap(in))
		var m piManifest
		require.NoError(t, json.Unmarshal(storedUpload(t, store, cfg+"/fullsend-manifest.json"), &m))
		require.NotNil(t, m.Agent)
		assert.Equal(t, "anthropic-vertex/claude-sonnet-5", m.Agent.Models["sonnet"],
			"per-repo config alias overrides the fleet default in the child model table")
		assert.Equal(t, "anthropic-vertex/claude-opus-4-6", m.Agent.Models["opus"],
			"unstated aliases keep the fleet default")
		assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", m.Agent.Models["haiku"],
			"unstated aliases keep the fleet default")
		assert.Equal(t, "anthropic-vertex/claude-fable-5", m.Agent.Models["fable"],
			"unstated aliases keep the fleet default")
	})
}

func TestPiAgentProbeCommand(t *testing.T) {
	t.Parallel()
	cmd := piAgentProbeCommand()
	assert.True(t, strings.HasPrefix(cmd, "command -v pi"), cmd)
	assert.Contains(t, cmd, shellQuote(piVertexExtensionPath))
	assert.Contains(t, cmd, shellQuote(piXaiVertexExtensionPath))
	assert.Contains(t, cmd, `test -d "$d" && echo "$d"`)

	bin, exts := parsePiAgentProbe("/usr/bin/pi\n/usr/local/share/pi-extensions/xai-vertex\n")
	assert.Equal(t, "/usr/bin/pi", bin)
	assert.Equal(t, []string{piXaiVertexExtensionPath}, exts)
	bin, exts = parsePiAgentProbe("")
	assert.Equal(t, "pi", bin, "no probe output (the exec failed silently) falls back to PATH lookup at spawn time")
	assert.Empty(t, exts)
	bin, exts = parsePiAgentProbe("/nonsense\n/etc/passwd\n")
	assert.Equal(t, "/nonsense", bin)
	assert.Empty(t, exts, "only the known extension paths are accepted")
}

func TestPiRuntimeClearIterationArtifacts(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	fakeOpenshellPi(t, logPath, filepath.Join(work, "store"), "/dev/null")
	require.NoError(t, PiRuntime{}.ClearIterationArtifacts("sb"))
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "rm -rf '/sandbox/workspace'/output/* '/sandbox/pi-config/sessions'/* '/sandbox/workspace/pi-debug.log' '/sandbox/pi-config/subagents/usage.jsonl'",
		"the sessions glob takes the sub-agent session dirs; their usage file sits outside it and is named")
	// Stray processes from the previous iteration are swept before the
	// files go, so nothing keeps writing into the cleared directories.
	sweep := strings.Index(string(log), "ps -o pid= -o ppid=")
	rm := strings.Index(string(log), "rm -rf '/sandbox/workspace'/output/*")
	require.NotEqual(t, -1, sweep, "expected the stray-process sweep to run")
	assert.Less(t, sweep, rm, "sweep must precede the file cleanup")
}

// A sweep that fails (exit 124 is the only exec failure sandbox.Exec
// reports) is warning-only: the rm -rf still runs and the result is nil.
func TestPiRuntimeClearIterationArtifacts_SweepFailureIsNotAnError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> '" + logPath + "'\n" +
		"for last; do :; done\n" +
		"case \"$last\" in *\"stray processes killed\"*) echo boom >&2; exit 124 ;; esac\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	require.NoError(t, PiRuntime{}.ClearIterationArtifacts("sb"))
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "rm -rf '/sandbox/workspace'/output/*", "file cleanup still runs after a failed sweep")
}

// TestPiDefaultTools_CoversToolMap keeps the settings.json default set and
// the Claude→pi tool map from drifting apart: every pi tool an agent can
// name must be active when it lists no tools.
func TestPiDefaultTools_CoversToolMap(t *testing.T) {
	defaults := map[string]bool{}
	for _, name := range piDefaultTools {
		defaults[name] = true
	}
	for claudeName, piName := range piToolForClaude {
		assert.Truef(t, defaults[piName], "pi tool %q (mapped from %s) is missing from piDefaultTools", piName, claudeName)
	}
	// And the reverse: every default pi tool has a Claude name for the hook
	// adapter, so hook scripts never see a raw pi tool name.
	for _, name := range piDefaultTools {
		_, ok := claudeToolForPi[name]
		assert.Truef(t, ok, "default pi tool %q has no claudeToolForPi entry", name)
	}
}

// forgetPiManifestHash drops the digest Bootstrap recorded for a sandbox,
// before and after the test. piManifestHashes is a package-level map keyed
// by sandbox name and the tests reuse a handful of names, so without this a
// Bootstrap in one test decides whether a later test's buildPiRunCommand
// emits the manifest guard - and against which digest.
func forgetPiManifestHash(t *testing.T, sandboxName string) {
	t.Helper()
	piManifestHashes.Delete(sandboxName)
	t.Cleanup(func() { piManifestHashes.Delete(sandboxName) })
}

// TestPiManifestHash covers the Bootstrap-to-Run seam: the digest is
// recorded per sandbox and read back by name, and a sandbox this process
// never bootstrapped yields "" so Run emits no guard rather than failing a
// caller that bootstrapped elsewhere.
func TestPiManifestHash(t *testing.T) {
	forgetPiManifestHash(t, "sb-hash")
	assert.Empty(t, piManifestHash("sb-hash"), "not bootstrapped in this process")

	body := []byte(`{"agentName":"triage"}`)
	recordPiManifestHash("sb-hash", body)
	sum := sha256.Sum256(body)
	assert.Equal(t, hex.EncodeToString(sum[:]), piManifestHash("sb-hash"))
	assert.Empty(t, piManifestHash("another-sandbox"), "the digest is per sandbox")

	forgetPiManifestHash(t, "sb-hash")
	assert.Empty(t, piManifestHash("sb-hash"), "and the test helper clears it again")
}
