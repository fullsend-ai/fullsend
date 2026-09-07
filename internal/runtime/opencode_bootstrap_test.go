package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOpenshellOpenCode installs a fake "openshell" that records every argv
// line to logPath, stores each "sandbox upload <name> <local> <remote>"
// payload under storeDir keyed by the remote path, and answers
// `opencode --version` execs. Everything else succeeds silently. Mirrors
// fakeOpenshellPi.
func fakeOpenshellOpenCode(t *testing.T, logPath, storeDir string) {
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
    "opencode --version") echo "0.1.0"; exit 0 ;;
  esac
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const openCodeTestAgentDef = `---
name: triage
description: Inspect an issue.
tools: Bash(gh,jq),Read,Skill
model: opus
---
You are the triage agent. Use gh.
`

func TestOpenCodeRuntimeBootstrap_WritesAgentDefinition(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	store := filepath.Join(work, "store")
	fakeOpenshellOpenCode(t, logPath, store)

	skillDir := filepath.Join(t.TempDir(), "issue-labels")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: issue-labels\n---\n# labels"), 0o644))

	in := bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, openCodeTestAgentDef),
		agentName:   "triage",
		skillDirs:   []string{skillDir},
		plugins:     claudePlugins("/tmp/some-plugin"),
	}
	require.NoError(t, OpenCodeRuntime{}.Bootstrap(in))

	r := OpenCodeRuntime{}
	agentMD := string(storedUpload(t, store, r.openCodeAgentPath("triage")))
	assert.Contains(t, agentMD, `"mode": "primary"`)
	assert.Contains(t, agentMD, `"description": "Inspect an issue."`)
	assert.Contains(t, agentMD, `"model": "opus"`)
	assert.Contains(t, agentMD, `"bash": true`)
	assert.Contains(t, agentMD, `"read": true`)
	assert.Contains(t, agentMD, "You are the triage agent. Use gh.")

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	logStr := string(log)
	cfg := r.ConfigDir()
	assert.Contains(t, logStr, "mkdir -p '"+cfg+"/agent' '"+cfg+"/skills' '"+cfg+"/plugins'")
	assert.Contains(t, logStr, "opencode --version")
	// Skills go through the upload/tar path; the archive lands under skills/.
	assert.Contains(t, logStr, cfg+"/skills/")
}

func TestOpenCodeRuntimeBootstrap_EmptyAgentPath(t *testing.T) {
	err := OpenCodeRuntime{}.Bootstrap(bootstrapInput{sandboxName: "sb"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent path is required")
}

func TestOpenCodeRuntimeBootstrap_NilInput(t *testing.T) {
	err := OpenCodeRuntime{}.Bootstrap(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bootstrap input is required")
}

func TestOpenCodePreflightVersionFailure(t *testing.T) {
	work := t.TempDir()
	binDir := t.TempDir()
	// A fake openshell whose `opencode --version` exec exits non-zero.
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    "opencode --version") echo "boom" >&2; exit 1 ;;
  esac
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	in := bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, openCodeTestAgentDef),
		agentName:   "triage",
	}
	err := OpenCodeRuntime{}.Bootstrap(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "opencode preflight")
	_ = work
}

func TestOpenCodeExtractTranscripts_NoneFound(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	store := filepath.Join(work, "store")
	// The fake's default exec returns empty stdout for the `test -f ... && echo
	// found` probe, so no transcript is reported and the call is a clean no-op.
	fakeOpenshellOpenCode(t, logPath, store)

	outDir := filepath.Join(work, "out")
	err := OpenCodeRuntime{}.ExtractTranscripts("sb", "triage", outDir)
	require.NoError(t, err)
	// The output dir is created even when nothing is downloaded.
	_, statErr := os.Stat(outDir)
	require.NoError(t, statErr)
	entries, _ := os.ReadDir(outDir)
	assert.Empty(t, entries, "no transcript files when none are found")
}

func TestOpenCodeExtractTranscripts_DownloadsTeedStream(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	binDir := t.TempDir()
	// The `test -f <transcript> && echo found` probe reports the sandbox tee'd
	// transcript exists; download writes an ndjson stream into the requested
	// destination dir (openshell sandbox download always treats the last arg as
	// a directory), which DownloadFile renames to the requested local name.
	script := `#!/bin/sh
echo "$@" >> '` + logPath + `'
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    *"echo found"*) echo found; exit 0 ;;
  esac
  exit 0
fi
if [ "$2" = "download" ]; then
  printf '{"type":"step_finish","timestamp":1,"sessionID":"s1","part":{"reason":"stop","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}\n' > "$5/$(basename "$4")"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out := filepath.Join(work, "transcripts")
	require.NoError(t, OpenCodeRuntime{}.ExtractTranscripts("sb", "triage", out))

	// The tee'd transcript is downloaded as <agentLabel>-output.jsonl.
	saved := filepath.Join(out, "triage-"+openCodeOutputFile)
	data, err := os.ReadFile(saved)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"step_finish"`)

	// The probe and download targeted the sandbox tee path Run writes to.
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), openCodeSandboxTranscriptPath())

	// A clean (non-error) stream produces no error annotations.
	assert.Empty(t, OpenCodeRuntime{}.ParseTranscriptErrors(out))
}

func TestOpenCodePathHelpers(t *testing.T) {
	t.Parallel()
	r := OpenCodeRuntime{}
	assert.Equal(t, "/sandbox/opencode-config/agent/triage.md", r.openCodeAgentPath("triage"))
	assert.Equal(t, "/sandbox/opencode-config/skills", r.openCodeSkillsPath())
	assert.True(t, strings.HasPrefix(r.openCodeHooksExtensionPath(), r.ConfigDir()+"/plugins/"))
}

// fakeOpenshellOpenCodeStream installs a fake openshell that streams
// streamFixture for the `opencode run` command and otherwise succeeds.
func fakeOpenshellOpenCodeStream(t *testing.T, streamFixture string) {
	t.Helper()
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    *"opencode run"*) cat '` + streamFixture + `'; exit 0 ;;
  esac
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestOpenCodeRuntimeRun_HappyPath(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")
	work := t.TempDir()
	fixture := filepath.Join(work, "stream.jsonl")
	stream := strings.Join([]string{
		`{"type":"tool_use","timestamp":1,"sessionID":"s1","part":{"tool":"bash","state":{"status":"completed","title":"ls"}}}`,
		`{"type":"text","timestamp":2,"sessionID":"s1","part":{"text":"all done"}}`,
		`{"type":"step_finish","timestamp":3,"sessionID":"s1","part":{"reason":"stop","cost":0.02,"tokens":{"input":100,"output":50,"reasoning":0,"cache":{"read":0,"write":0}}}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(fixture, []byte(stream), 0o644))
	fakeOpenshellOpenCodeStream(t, fixture)

	var events []AgentEvent
	printer := ui.New(&strings.Builder{})
	metrics := &RunMetrics{}
	params := RunParams{
		SandboxName:   "sb",
		AgentBaseName: "triage",
		Model:         "opus",
		RepoDir:       "/sandbox/workspace/repo",
		Timeout:       30 * time.Second,
		OnEvent:       func(e AgentEvent) { events = append(events, e) },
	}
	exit, err := OpenCodeRuntime{}.Run(context.Background(), params, printer, time.Now(), metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)

	// InitEvent emitted from RunParams.Model (bare id), since the wire carries
	// no model metadata.
	require.NotEmpty(t, events)
	init, ok := events[0].(InitEvent)
	require.True(t, ok, "first event should be InitEvent")
	assert.Equal(t, "claude-opus-4-6", init.Model)
	assert.Equal(t, "claude-opus-4-6", metrics.Model)

	// Metrics captured from the stream.
	assert.Equal(t, 1, metrics.NumTurns)
	assert.InDelta(t, 0.02, metrics.TotalCostUSD, 1e-9)
	assert.Equal(t, 100, metrics.InputTokens)
	assert.Equal(t, 50, metrics.OutputTokens)
	assert.EqualValues(t, 1, metrics.ToolCalls.Load())
}

func TestOpenCodeRuntimeRun_TeesToOutputPath(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")
	work := t.TempDir()
	fixture := filepath.Join(work, "stream.jsonl")
	stream := `{"type":"step_finish","timestamp":1,"sessionID":"s1","part":{"reason":"stop","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}` + "\n"
	require.NoError(t, os.WriteFile(fixture, []byte(stream), 0o644))
	fakeOpenshellOpenCodeStream(t, fixture)

	out := filepath.Join(work, "output.jsonl")
	printer := ui.New(&strings.Builder{})
	params := RunParams{
		SandboxName:   "sb",
		AgentBaseName: "triage",
		RepoDir:       "/repo",
		Timeout:       30 * time.Second,
		OutputPath:    out,
		OnEvent:       func(AgentEvent) {},
	}
	exit, err := OpenCodeRuntime{}.Run(context.Background(), params, printer, time.Now(), &RunMetrics{})
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	// The stream was tee'd to OutputPath.
	data, readErr := os.ReadFile(out)
	require.NoError(t, readErr)
	assert.Contains(t, string(data), `"type":"step_finish"`)
}

func TestOpenCodeRuntimeClearIterationArtifacts(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	store := filepath.Join(work, "store")
	fakeOpenshellOpenCode(t, logPath, store)

	err := OpenCodeRuntime{}.ClearIterationArtifacts("sb")
	require.NoError(t, err)
	log, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(log), "rm -rf")
	assert.Contains(t, string(log), openCodeDebugLogFile)
}

func TestOpenCodeRuntimeRun_RequiresAgentBaseName(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	_, err := OpenCodeRuntime{}.Run(context.Background(), RunParams{}, printer, time.Now(), &RunMetrics{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent base name is required")
}

func TestOpenCodeRuntimeRun_StreamErrorOverridesExit(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")
	work := t.TempDir()
	fixture := filepath.Join(work, "err.jsonl")
	stream := `{"type":"error","timestamp":1,"sessionID":"s1","error":{"name":"ProviderError","data":{"message":"quota exhausted"}}}` + "\n"
	require.NoError(t, os.WriteFile(fixture, []byte(stream), 0o644))
	fakeOpenshellOpenCodeStream(t, fixture)

	printer := ui.New(&strings.Builder{})
	params := RunParams{
		SandboxName:   "sb",
		AgentBaseName: "triage",
		RepoDir:       "/repo",
		Timeout:       30 * time.Second,
		OnEvent:       func(AgentEvent) {},
	}
	// opencode exits 0 but the stream reports an error → Run returns 1.
	exit, err := OpenCodeRuntime{}.Run(context.Background(), params, printer, time.Now(), &RunMetrics{})
	require.NoError(t, err)
	assert.Equal(t, 1, exit)
}
