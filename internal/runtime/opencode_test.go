package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeRuntimeMetadata(t *testing.T) {
	t.Parallel()

	rt := OpenCodeRuntime{}
	assert.Equal(t, "opencode", rt.Name())
	// Multi-provider convention, matching pi's System()=="pi" — NOT a single
	// model vendor like "anthropic".
	assert.Equal(t, "opencode", rt.System())
	// Runner-owned config dir, off the agent-writable workspace (#515 pin).
	assert.Equal(t, sandbox.SandboxOpenCodeConfig, rt.ConfigDir())
	assert.Equal(t, "/sandbox/opencode-config", rt.ConfigDir())
	assert.NotEqual(t, sandbox.SandboxWorkspace+"/.opencode", rt.ConfigDir(),
		"ConfigDir must be moved off the agent-writable workspace")
	assert.Equal(t, sandbox.SandboxWorkspace, rt.WorkspaceDir())
	assert.Equal(t, openCodeDebugLogFile, rt.DebugLogName())
}

func TestOpenCodeRuntimeEnvExports(t *testing.T) {
	t.Parallel()

	rt := OpenCodeRuntime{}
	env := rt.EnvExports()
	assert.Contains(t, env, "export OPENCODE_CONFIG_DIR="+rt.ConfigDir())
	assert.Contains(t, env, "export OPENCODE_DISABLE_PROJECT_CONFIG=true")
	assert.Contains(t, env, "export OPENCODE_CONFIG_CONTENT")
	assert.Contains(t, env, "export GOOGLE_APPLICATION_CREDENTIALS")
}

func TestOpenCodeRuntimeResolvesFromRegistry(t *testing.T) {
	t.Parallel()

	b, err := Resolve("opencode")
	require.NoError(t, err)
	_, isRT := b.Runtime.(OpenCodeRuntime)
	assert.True(t, isRT, "Runtime should be OpenCodeRuntime")
	_, isTX := b.Transcripts.(OpenCodeRuntime)
	assert.True(t, isTX, "Transcripts should be OpenCodeRuntime")
}

func TestTranslateOpenCodeModel(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", translateOpenCodeModel("opus", nil))
	assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", translateOpenCodeModel("sonnet", nil))
	// Empty falls back to the default alias.
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", translateOpenCodeModel("", nil))
	// A bare (non-alias) id gets the provider prefix.
	assert.Equal(t, "anthropic-vertex/claude-3-5", translateOpenCodeModel("claude-3-5", nil))
	// A provider/model spec passes through unchanged.
	assert.Equal(t, "openai/gpt-5", translateOpenCodeModel("openai/gpt-5", nil))

	// Provider override from the environment.
	t.Setenv(openCodeProviderEnv, "myprov")
	assert.Equal(t, "myprov/claude-opus-4-6", translateOpenCodeModel("opus", nil))
}

func TestTranslateOpenCodeModel_ConfigAliases(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	// Per-repo alias overrides the built-in alias.
	overrides := map[string]string{"sonnet": "claude-sonnet-5"}
	assert.Equal(t, "anthropic-vertex/claude-sonnet-5", translateOpenCodeModel("sonnet", overrides))
	// An alias not overridden still uses the built-in.
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", translateOpenCodeModel("opus", overrides))
	// A per-repo alias that maps to a provider/id spec passes through.
	overrides = map[string]string{"sonnet": "xai/grok-4.6"}
	assert.Equal(t, "xai/grok-4.6", translateOpenCodeModel("sonnet", overrides))
}

func TestMergedOpenCodeModelAliases(t *testing.T) {
	t.Parallel()
	// nil configAliases returns a copy of the built-in aliases.
	merged := mergedOpenCodeModelAliases(nil)
	assert.Equal(t, openCodeModelAliases["opus"], merged["opus"])
	// Mutating the merged map must not affect the package-level table.
	merged["opus"] = "mutated"
	assert.NotEqual(t, "mutated", openCodeModelAliases["opus"], "merged map must be a copy")
	// Per-repo overrides win.
	merged = mergedOpenCodeModelAliases(map[string]string{"sonnet": "claude-sonnet-5"})
	assert.Equal(t, "claude-sonnet-5", merged["sonnet"])
	assert.Equal(t, openCodeModelAliases["opus"], merged["opus"], "fleet default preserved")
	assert.Equal(t, openCodeModelAliases["haiku"], merged["haiku"], "fleet default preserved")
}

func TestOpenCodeBareModelID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "claude-opus-4-6", openCodeBareModelID("anthropic-vertex/claude-opus-4-6"))
	assert.Equal(t, "gpt-5", openCodeBareModelID("openai/gpt-5"))
	assert.Equal(t, "bare", openCodeBareModelID("bare"))
}

func TestOpenCodePermissionRecord(t *testing.T) {
	t.Parallel()

	// nil (no restriction) → empty map, so OpenCode's default set applies.
	rec := openCodePermissionRecord(nil)
	assert.NotNil(t, rec)
	assert.Empty(t, rec)

	rec = openCodePermissionRecord([]string{"Bash", "Read", "Edit", "Glob", "LS", "Task"})
	assert.Equal(t, []string{"bash", "edit", "glob", "read", "task"}, openCodeToolNamesSorted(rec))

	// Agent (current name) and Task (legacy alias) both map to "task".
	rec = openCodePermissionRecord([]string{"Agent", "Read"})
	assert.Equal(t, []string{"read", "task"}, openCodeToolNamesSorted(rec))
	// Both Agent and Task present → "task" appears once (map key dedup).
	rec = openCodePermissionRecord([]string{"Agent", "Task", "Read"})
	assert.Equal(t, []string{"read", "task"}, openCodeToolNamesSorted(rec))

	// Skill is dropped (native discovery), unsupported names dropped, and an
	// agent listing only those gets all tools denied (not nil/empty, so
	// OpenCode does not fall back to its full default set).
	rec = openCodePermissionRecord([]string{"Skill", "NoSuchTool"})
	assert.NotNil(t, rec)
	assert.Empty(t, openCodeToolNamesSorted(rec))
	// Every known tool is explicitly denied.
	assert.Len(t, rec, len(openCodeAllToolIDs))
	for _, id := range openCodeAllToolIDs {
		assert.Equal(t, "deny", rec[id], "tool %q should be denied", id)
	}
}

func TestOpenCodeAgentMarkdown(t *testing.T) {
	t.Parallel()

	def := &piAgentDef{
		Name:        "triage",
		Description: "Triage incoming issues",
		Model:       "opus",
		Tools:       []string{"Bash", "Read"},
		Body:        "You are the triage agent.",
	}
	md, err := openCodeAgentMarkdown("triage", def)
	require.NoError(t, err)
	s := string(md)
	assert.True(t, strings.HasPrefix(s, "---\n"), "starts with frontmatter fence")
	assert.Contains(t, s, `"mode": "primary"`)
	assert.Contains(t, s, `"description": "Triage incoming issues"`)
	assert.Contains(t, s, `"model": "opus"`)
	assert.Contains(t, s, `"bash": "allow"`)
	assert.Contains(t, s, `"read": "allow"`)
	// Tools not in the allowlist are explicitly denied.
	assert.Contains(t, s, `"write": "deny"`)
	assert.Contains(t, s, `"webfetch": "deny"`)
	assert.Contains(t, s, "You are the triage agent.")
	assert.True(t, strings.HasSuffix(s, "\n"))
	// The body follows a closing fence.
	assert.Contains(t, s, "\n---\n\n")
}

func TestOpenCodeValidatedArg(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", openCodeValidatedArg("anthropic-vertex/claude-opus-4-6"))
	assert.Equal(t, "high", openCodeValidatedArg("high"))
	// Control characters and shell metacharacters are stripped.
	assert.Equal(t, "opusrm -rf", openCodeValidatedArg("opus;rm -rf"))
	assert.Equal(t, "abc", openCodeValidatedArg("a\x00b\nc"))
	assert.Equal(t, "safe.name-1_2@x", openCodeValidatedArg("safe.name-1_2@x"))
}

func TestOpenCodeInstructionsConfig(t *testing.T) {
	t.Parallel()

	cfg := openCodeInstructionsConfig("/sandbox/workspace/myrepo")
	assert.Equal(t, `{"instructions":["/sandbox/workspace/myrepo/AGENTS.md"]}`, cfg)
}

func TestBuildOpenCodeRunCommand(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	params := RunParams{
		SandboxName:   "sbx",
		AgentBaseName: "triage",
		Model:         "opus",
		Effort:        "high",
		RepoDir:       sandbox.SandboxWorkspace + "/myrepo",
	}
	cmd := buildOpenCodeRunCommand(params, "triage")

	assert.Contains(t, cmd, "cd "+shellQuote(params.RepoDir))
	assert.Contains(t, cmd, "&& . "+shellQuote(sandbox.SandboxWorkspace+"/.env"))
	assert.Contains(t, cmd, "&& export "+openCodeRuntimeEnv+"=opencode")
	// opencode runs inside the tee pipeline so its stream lands in a sandbox
	// transcript file for ExtractTranscripts while still streaming to the host.
	assert.Contains(t, cmd, "opencode run --format json --thinking")
	assert.Contains(t, cmd, "--model "+shellQuote("anthropic-vertex/claude-opus-4-6"))
	assert.Contains(t, cmd, "--variant "+shellQuote("high"))
	assert.Contains(t, cmd, "--agent "+shellQuote("triage"))
	assert.Contains(t, cmd, shellQuote(DefaultAgentPrompt))
	assert.Contains(t, cmd, "</dev/null")
	// The stream is tee'd to the sandbox transcript path and the transcript
	// dir is created first.
	assert.Contains(t, cmd, "mkdir -p "+shellQuote(sandbox.SandboxWorkspace+"/"+openCodeOutputSubdir))
	assert.Contains(t, cmd, "| tee "+shellQuote(openCodeSandboxTranscriptPath()))
	// opencode's real exit code is re-raised past tee (which always exits 0).
	assert.Contains(t, cmd, "echo $? > "+shellQuote(sandbox.SandboxWorkspace+"/"+openCodeRunRCFile))
	assert.Contains(t, cmd, "exit \"$(cat "+shellQuote(sandbox.SandboxWorkspace+"/"+openCodeRunRCFile))
	// No hooks signal → no integrity guard.
	assert.NotContains(t, cmd, "refusing to run unhooked")

	// Runner-owned opencode.json with instructions pointing at workspace
	// AGENTS.md is written by the prelude before .env is sourced.
	configPath := OpenCodeRuntime{}.ConfigDir() + "/" + openCodeConfigFile
	assert.Contains(t, cmd, "> "+shellQuote(configPath))
	expectedJSON := openCodeInstructionsConfig(params.RepoDir)
	assert.Contains(t, cmd, shellQuote(expectedJSON))
	// The config write must appear before .env sourcing.
	configIdx := strings.Index(cmd, shellQuote(configPath))
	envIdx := strings.Index(cmd, "&& . "+shellQuote(sandbox.SandboxWorkspace+"/.env"))
	require.NotEqual(t, -1, configIdx, "config write must be present")
	require.NotEqual(t, -1, envIdx, ".env source must be present")
	assert.Less(t, configIdx, envIdx, "runner-owned opencode.json must be written before .env is sourced")
}

func TestBuildOpenCodeRunCommand_PromptOverride(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	params := RunParams{
		AgentBaseName: "fix",
		RepoDir:       "/repo",
		Prompt:        "Previous attempt failed: retry with the fix.",
	}
	cmd := buildOpenCodeRunCommand(params, "fix")
	assert.Contains(t, cmd, shellQuote("Previous attempt failed: retry with the fix."))
	assert.NotContains(t, cmd, shellQuote(DefaultAgentPrompt))
}

func TestBuildOpenCodeRunCommand_HooksGuard(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	params := RunParams{
		AgentBaseName:     "fix",
		RepoDir:           "/repo",
		HooksSettingsPath: "/sandbox/opencode-config/hooks.json",
	}
	cmd := buildOpenCodeRunCommand(params, "fix")
	// The sha256 fail-closed guard is emitted before .env is sourced.
	guardIdx := strings.Index(cmd, "refusing to run unhooked")
	envIdx := strings.Index(cmd, "&& . "+shellQuote(sandbox.SandboxWorkspace+"/.env"))
	require.NotEqual(t, -1, guardIdx, "hooks guard must be present")
	require.NotEqual(t, -1, envIdx)
	assert.Less(t, guardIdx, envIdx, "integrity guard must run before .env is sourced")
	assert.Contains(t, cmd, "command -p sha256sum")
	assert.Contains(t, cmd, OpenCodeRuntime{}.openCodeHooksExtensionPath())
	assert.Contains(t, cmd, "exit 97")
}

func TestBuildOpenCodeRunCommand_DebugMode(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	params := RunParams{
		AgentBaseName: "triage",
		RepoDir:       "/repo",
		Debug:         "true",
	}
	cmd := buildOpenCodeRunCommand(params, "triage")
	assert.Contains(t, cmd, "--print-logs")
	assert.Contains(t, cmd, "--log-level")
	assert.Contains(t, cmd, "DEBUG")
	assert.Contains(t, cmd, "2>>"+shellQuote(sandbox.SandboxWorkspace+"/"+openCodeDebugLogFile))
}

func TestBuildOpenCodeRunCommand_FallbackModelsIgnored(t *testing.T) {
	// Fallback models are logged as a warning in Run, not in the command
	// builder; the command should still be built without error.
	t.Setenv(openCodeProviderEnv, "")
	params := RunParams{
		AgentBaseName:  "triage",
		RepoDir:        "/repo",
		FallbackModels: []string{"sonnet", "haiku"},
	}
	cmd := buildOpenCodeRunCommand(params, "triage")
	assert.Contains(t, cmd, "opencode run")
}

func TestOpenCodeHooksExtensionPath(t *testing.T) {
	t.Parallel()
	r := OpenCodeRuntime{}
	assert.Equal(t, "/sandbox/opencode-config/plugins/fullsend-hooks.ts", r.openCodeHooksExtensionPath())
}

func TestParseOpenCodeTranscriptFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// A healthy stream: one text event + a step_finish → non-error result.
	okPath := filepath.Join(dir, "ok.jsonl")
	okStream := strings.Join([]string{
		`{"type":"text","timestamp":1,"sessionID":"s1","part":{"text":"done"}}`,
		`{"type":"step_finish","timestamp":2,"sessionID":"s1","part":{"reason":"stop","cost":0.01,"tokens":{"input":10,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(okPath, []byte(okStream), 0o644))

	te, ok := parseOpenCodeTranscriptFile(okPath)
	require.True(t, ok)
	assert.False(t, te.IsError)
	assert.Equal(t, "ok.jsonl", te.Source)
	assert.Equal(t, "stop", te.Subtype)

	// An error stream trips IsError.
	errPath := filepath.Join(dir, "err.jsonl")
	errStream := `{"type":"error","timestamp":1,"sessionID":"s1","error":{"name":"ProviderError","data":{"message":"boom"}}}` + "\n"
	require.NoError(t, os.WriteFile(errPath, []byte(errStream), 0o644))

	te, ok = parseOpenCodeTranscriptFile(errPath)
	require.True(t, ok)
	assert.True(t, te.IsError)
	assert.Contains(t, te.ErrorMessage, "boom")

	// A zero-turn (empty content) capture is treated as an error (false-success
	// guard), because parseOpenCodeStream still synthesizes a ResultEvent.
	zeroPath := filepath.Join(dir, "zero.jsonl")
	require.NoError(t, os.WriteFile(zeroPath, []byte(`{"type":"step_start","timestamp":1,"sessionID":"s1"}`+"\n"), 0o644))
	te, ok = parseOpenCodeTranscriptFile(zeroPath)
	require.True(t, ok)
	assert.True(t, te.IsError, "zero-turn stream should be flagged as error")

	// An empty file yields ok=false.
	emptyPath := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(emptyPath, nil, 0o644))
	_, ok = parseOpenCodeTranscriptFile(emptyPath)
	assert.False(t, ok)

	// A missing file yields ok=false.
	_, ok = parseOpenCodeTranscriptFile(filepath.Join(dir, "nope.jsonl"))
	assert.False(t, ok)
}

func TestOpenCodeParseTranscriptErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	errStream := `{"type":"error","timestamp":1,"sessionID":"s1","error":{"name":"E","data":{"message":"kaboom"}}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-output.jsonl"), []byte(errStream), 0o644))
	okStream := `{"type":"step_finish","timestamp":2,"sessionID":"s1","part":{"reason":"stop","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b-output.jsonl"), []byte(okStream), 0o644))

	summaries := OpenCodeRuntime{}.ParseTranscriptErrors(dir)
	require.Len(t, summaries, 1)
	assert.Equal(t, "a-output.jsonl", summaries[0].Source)
	assert.True(t, summaries[0].IsError)
}

func TestOpenCodeEmitTranscriptErrors(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	OpenCodeRuntime{}.EmitTranscriptErrors(&buf, nil)
	// No panic, no output for an empty summary set.
	assert.Empty(t, buf.String())
}

func TestOpenCodeParseTranscriptFile_Delegates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "output.jsonl")
	stream := `{"type":"error","timestamp":1,"sessionID":"s1","error":{"name":"E","data":{"message":"nope"}}}` + "\n"
	require.NoError(t, os.WriteFile(p, []byte(stream), 0o644))

	te, ok := OpenCodeRuntime{}.ParseTranscriptFile(p)
	require.True(t, ok)
	assert.True(t, te.IsError)
	assert.Contains(t, te.ErrorMessage, "nope")

	// Missing file → ok=false.
	_, ok = OpenCodeRuntime{}.ParseTranscriptFile(filepath.Join(dir, "missing.jsonl"))
	assert.False(t, ok)
}

func TestOpenCodeExtractDebugLog_NoDebugNoop(t *testing.T) {
	t.Parallel()
	// An empty debug value is a no-op (no sandbox interaction).
	err := OpenCodeRuntime{}.ExtractDebugLog("sb", "/tmp/does-not-matter", "")
	assert.NoError(t, err)
}

func TestOpenCodeExtractDebugLog_DownloadsWhenDebugSet(t *testing.T) {
	binDir := t.TempDir()
	// A fake openshell that writes a debug log file on download.
	script := `#!/bin/sh
if [ "$2" = "download" ]; then
  printf 'debug log content\n' > "$5/$(basename "$4")"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	localPath := filepath.Join(t.TempDir(), "debug.log")
	// Exercise the code path where debug != "" (line 77 of opencode_transcript.go).
	err := OpenCodeRuntime{}.ExtractDebugLog("sb", localPath, "true")
	require.NoError(t, err)
	data, readErr := os.ReadFile(localPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(data), "debug log content")
}

func TestOpenCodeParseTranscriptErrors_BadDir(t *testing.T) {
	t.Parallel()
	// A non-existent directory returns nil (no panic).
	summaries := OpenCodeRuntime{}.ParseTranscriptErrors("/nonexistent/dir")
	assert.Nil(t, summaries)
}

func TestOpenCodeParseTranscriptErrors_SkipsDirsAndNonJSONL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a subdirectory with .jsonl suffix that should be skipped.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir.jsonl"), 0o755))
	// Create a non-jsonl file that should be skipped.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not jsonl"), 0o644))
	summaries := OpenCodeRuntime{}.ParseTranscriptErrors(dir)
	assert.Empty(t, summaries)
}

func TestOpenCodeExtractTranscripts_ProbeExecError(t *testing.T) {
	// When openshell is not on PATH, sandbox.Exec returns an error,
	// which ExtractTranscripts wraps as "checking transcript".
	t.Setenv("PATH", t.TempDir()) // empty PATH → openshell not found

	outDir := filepath.Join(t.TempDir(), "out")
	err := OpenCodeRuntime{}.ExtractTranscripts("sb", "triage", outDir)
	require.Error(t, err)
}

func TestOpenCodeExtractTranscripts_CreateRejected(t *testing.T) {
	// An agentLabel containing path traversal causes root.Create to reject
	// the filename, exercising the createErr != nil path.
	binDir := t.TempDir()
	// Fake openshell that always reports "found" for the probe.
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  echo found
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	outDir := filepath.Join(t.TempDir(), "out")
	// A label with "../" triggers root.Create rejection (path containment).
	err := OpenCodeRuntime{}.ExtractTranscripts("sb", "../escape", outDir)
	// Should not error (prints a warning instead), but should not create
	// any file outside the output dir.
	assert.NoError(t, err)
	entries, _ := os.ReadDir(outDir)
	assert.Empty(t, entries)
}
