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
	assert.Contains(t, env, "OPENCODE_CONFIG_CONTENT")
	assert.Contains(t, env, "GOOGLE_APPLICATION_CREDENTIALS")
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

	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", translateOpenCodeModel("opus"))
	assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", translateOpenCodeModel("sonnet"))
	// Empty falls back to the default alias.
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", translateOpenCodeModel(""))
	// A bare (non-alias) id gets the provider prefix.
	assert.Equal(t, "anthropic-vertex/claude-3-5", translateOpenCodeModel("claude-3-5"))
	// A provider/model spec passes through unchanged.
	assert.Equal(t, "openai/gpt-5", translateOpenCodeModel("openai/gpt-5"))

	// Provider override from the environment.
	t.Setenv(openCodeProviderEnv, "myprov")
	assert.Equal(t, "myprov/claude-opus-4-6", translateOpenCodeModel("opus"))
}

func TestOpenCodeBareModelID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "claude-opus-4-6", openCodeBareModelID("anthropic-vertex/claude-opus-4-6"))
	assert.Equal(t, "gpt-5", openCodeBareModelID("openai/gpt-5"))
	assert.Equal(t, "bare", openCodeBareModelID("bare"))
}

func TestOpenCodeToolsRecord(t *testing.T) {
	t.Parallel()

	// nil (no restriction) → nil, so OpenCode's default set applies.
	assert.Nil(t, openCodeToolsRecord(nil))

	rec := openCodeToolsRecord([]string{"Bash", "Read", "Edit", "Glob", "LS", "Task"})
	assert.Equal(t, []string{"bash", "edit", "glob", "list", "read", "task"}, openCodeToolNamesSorted(rec))

	// Skill is dropped (native discovery), unsupported names dropped, and an
	// agent listing only those gets an explicit empty record (not nil, so
	// OpenCode does not fall back to its full default set).
	rec = openCodeToolsRecord([]string{"Skill", "NoSuchTool"})
	assert.NotNil(t, rec)
	assert.Empty(t, openCodeToolNamesSorted(rec))
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
	assert.Contains(t, s, `"bash": true`)
	assert.Contains(t, s, `"read": true`)
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
	assert.Contains(t, cmd, "&& opencode run --format json --thinking")
	assert.Contains(t, cmd, "--model "+shellQuote("anthropic-vertex/claude-opus-4-6"))
	assert.Contains(t, cmd, "--variant "+shellQuote("high"))
	assert.Contains(t, cmd, "--agent "+shellQuote("triage"))
	assert.Contains(t, cmd, shellQuote(DefaultAgentPrompt))
	assert.Contains(t, cmd, "</dev/null")
	// No hooks signal → no integrity guard.
	assert.NotContains(t, cmd, "refusing to run unhooked")
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
