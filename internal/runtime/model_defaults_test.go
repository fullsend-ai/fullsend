package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Both Claude-family runtimes default the three aliases to the same ids
// (#8426), and every alias with a default has a chain back to the previous
// one.
func TestClaudeAliasDefaults_MatchPiTable(t *testing.T) {
	for alias, id := range claudeAliasDefaults {
		assert.Equal(t, id, piModelAliases[alias], "pi and Claude Code default %s alike", alias)
		assert.NotEmpty(t, claudeAliasFallbacks[alias], "%s has a built-in fallback chain", alias)
	}
	assert.Equal(t, "claude-opus-5-5", claudeAliasDefaults["opus"])
	assert.Equal(t, "claude-sonnet-5-5", claudeAliasDefaults["sonnet"])
	assert.Equal(t, "claude-haiku-5-5", claudeAliasDefaults["haiku"])
	assert.Equal(t, "claude-fable-5-1", piModelAliases["fable"], "fable is unchanged")
}

func TestImplicitFallbackModels(t *testing.T) {
	assert.Equal(t, []string{"claude-opus-4-6"}, ImplicitFallbackModels("opus", nil))
	assert.Equal(t, []string{"claude-sonnet-4-6"}, ImplicitFallbackModels(" sonnet ", nil))
	assert.Equal(t, []string{"claude-haiku-4-5"}, ImplicitFallbackModels("haiku", map[string]string{"sonnet": "claude-sonnet-5"}),
		"a retarget of another alias leaves this one's chain alone")

	assert.Nil(t, ImplicitFallbackModels("sonnet", map[string]string{"sonnet": "claude-sonnet-5"}),
		"a models.aliases retarget has no implicit chain")
	assert.Nil(t, ImplicitFallbackModels("fable", nil), "fable has no chain")
	assert.Nil(t, ImplicitFallbackModels("claude-opus-5-5", nil), "a pinned id has no chain")
	assert.Nil(t, ImplicitFallbackModels("anthropic-vertex/claude-opus-5-5", nil), "a provider/id spec has no chain")
	assert.Nil(t, ImplicitFallbackModels("", nil))

	got := ImplicitFallbackModels("opus", nil)
	got[0] = "mutated"
	assert.Equal(t, []string{"claude-opus-4-6"}, ImplicitFallbackModels("opus", nil), "the table is not shared")
}

func TestEffectiveFallbackModels(t *testing.T) {
	assert.Equal(t, []string{"claude-opus-4-8"}, effectiveFallbackModels("opus", []string{"claude-opus-4-8"}, nil),
		"an explicit FULLSEND_FALLBACK_MODELS wins over the built-in chain")
	assert.Equal(t, []string{"claude-opus-4-6"}, effectiveFallbackModels("opus", nil, nil))
	assert.Nil(t, effectiveFallbackModels("claude-opus-5-5", nil, nil))
}

func TestModelFallbackLoop(t *testing.T) {
	now := func() time.Time { return time.Unix(1000, 0) }
	var tried []string
	attempt := func(model string, _ time.Duration, mayFallBack bool) string {
		tried = append(tried, model)
		if model == "c" {
			assert.False(t, mayFallBack, "the last model cannot fall back")
		}
		return model
	}
	notServed := func(s string) bool { return s != "c" }

	res, served := modelFallbackLoop([]string{"a", "b", "c"}, time.Minute, time.Second, now, attempt, notServed,
		func(string, string, time.Duration) bool { return true })
	assert.Equal(t, "c", res)
	assert.Equal(t, 2, served)
	assert.Equal(t, []string{"a", "b", "c"}, tried)

	tried = nil
	res, served = modelFallbackLoop([]string{"a", "b"}, time.Minute, time.Second, now, attempt, notServed,
		func(string, string, time.Duration) bool { return false })
	assert.Equal(t, "a", res, "a refused fallback makes the abandoned attempt final")
	assert.Equal(t, 0, served)
	assert.Equal(t, []string{"a"}, tried)
}

func TestClaudeModelID(t *testing.T) {
	assert.Equal(t, "claude-opus-5-5", claudeModelID("opus", nil))
	assert.Equal(t, "claude-sonnet-5", claudeModelID("sonnet", map[string]string{"sonnet": "claude-sonnet-5"}))
	assert.Equal(t, "fable", claudeModelID("fable", nil), "fable stays on Claude Code's own mapping")
	assert.Equal(t, "claude-opus-4-6", claudeModelID("claude-opus-4-6", nil))
}

func TestClaudeModelChain(t *testing.T) {
	cases := []struct {
		name   string
		params RunParams
		gw     bool
		want   []string
	}{
		{"alias gets its built-in chain", RunParams{Model: "opus"}, false, []string{"claude-opus-5-5", "claude-opus-4-6"}},
		{"explicit list wins", RunParams{Model: "sonnet", FallbackModels: []string{"haiku", "claude-sonnet-4-6"}}, false,
			[]string{"claude-sonnet-5-5", "claude-haiku-5-5", "claude-sonnet-4-6"}},
		{"duplicates and provider/id entries are dropped", RunParams{Model: "opus", FallbackModels: []string{"opus", "anthropic-vertex/x", "claude-opus-4-6"}}, false,
			[]string{"claude-opus-5-5", "claude-opus-4-6"}},
		{"retarget has no implicit chain", RunParams{Model: "sonnet", ModelAliases: map[string]string{"sonnet": "claude-sonnet-5"}}, false, []string{"sonnet"}},
		{"retarget with an explicit list", RunParams{Model: "sonnet", ModelAliases: map[string]string{"sonnet": "claude-sonnet-5"}, FallbackModels: []string{"claude-sonnet-4-6"}}, false,
			[]string{"claude-sonnet-5", "claude-sonnet-4-6"}},
		{"pinned id", RunParams{Model: "claude-opus-5-5", FallbackModels: []string{"claude-opus-4-6"}}, false, []string{"claude-opus-5-5"}},
		{"provider/id", RunParams{Model: "anthropic-vertex/claude-opus-5-5"}, false, []string{"anthropic-vertex/claude-opus-5-5"}},
		{"fable has no chain", RunParams{Model: "fable"}, false, []string{"fable"}},
		{"fable takes an explicit list", RunParams{Model: "fable", FallbackModels: []string{"opus"}}, false,
			[]string{"claude-fable-5-1", "claude-opus-5-5"}},
		{"gateway runs never fall back", RunParams{Model: "opus"}, true, []string{"opus"}},
		{"no model", RunParams{}, false, []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.params.SandboxName = "fs-model-chain-none"
			assert.Equal(t, tc.want, claudeModelChain(tc.params, tc.gw))
		})
	}

	t.Run("agent frontmatter alias when the run names none", func(t *testing.T) {
		const sb = "fs-model-chain-frontmatter"
		claudeAgentModels.Store(sb, "haiku")
		t.Cleanup(func() { claudeAgentModels.Delete(sb) })
		assert.Equal(t, []string{"claude-haiku-5-5", "claude-haiku-4-5"}, claudeModelChain(RunParams{SandboxName: sb}, false))
		assert.Equal(t, []string{"claude-opus-4-6"}, claudeModelChain(RunParams{SandboxName: sb, Model: "claude-opus-4-6"}, false),
			"the run's model wins over the frontmatter")
	})

	// A frontmatter alias the repo retargets has no fallback chain, but its
	// retarget must still reach --model: left empty, Claude Code would
	// resolve the alias with its own table and ignore models.aliases.
	t.Run("retargeted frontmatter alias resolves to the retarget", func(t *testing.T) {
		const sb = "fs-model-chain-frontmatter-retarget"
		claudeAgentModels.Store(sb, "sonnet")
		t.Cleanup(func() { claudeAgentModels.Delete(sb) })
		aliases := map[string]string{"sonnet": "claude-sonnet-5"}
		assert.Equal(t, []string{"claude-sonnet-5"}, claudeModelChain(RunParams{SandboxName: sb, ModelAliases: aliases}, false))
		assert.Equal(t, []string{"claude-sonnet-5", "claude-sonnet-4-6"},
			claudeModelChain(RunParams{SandboxName: sb, ModelAliases: aliases, FallbackModels: []string{"claude-sonnet-4-6"}}, false))
		assert.Equal(t, []string{""}, claudeModelChain(RunParams{SandboxName: sb, ModelAliases: aliases}, true),
			"the gateway route resolves the frontmatter model itself")
	})

	t.Run("frontmatter fable stays on Claude Code's own table", func(t *testing.T) {
		const sb = "fs-model-chain-frontmatter-fable"
		claudeAgentModels.Store(sb, "fable")
		t.Cleanup(func() { claudeAgentModels.Delete(sb) })
		assert.Equal(t, []string{""}, claudeModelChain(RunParams{SandboxName: sb}, false))
	})
}

func TestClaudeShouldFallBack(t *testing.T) {
	notFound := "API Error: 404 {\"error\":{\"code\":404,\"message\":\"Publisher model projects/p/locations/us-east5/publishers/anthropic/models/claude-opus-5-5 not found.\",\"status\":\"NOT_FOUND\"}}"
	dataSharing := "API Error: 403 Access to this model requires data sharing to be enabled for publisher 'anthropic'"
	errResult := func(msg string) *ResultEvent { return &ResultEvent{IsError: true, ErrorMessage: msg} }

	assert.True(t, claudeShouldFallBack(claudeAttemptResult{exitCode: 1, lastResult: errResult(notFound)}))
	assert.True(t, claudeShouldFallBack(claudeAttemptResult{exitCode: 1, lastResult: errResult(dataSharing)}))

	assert.False(t, claudeShouldFallBack(claudeAttemptResult{lastResult: errResult("API Error: 403 PERMISSION_DENIED: caller lacks aiplatform.endpoints.predict")}),
		"another 403 is final")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{lastResult: errResult("API Error: 429 quota exceeded")}), "quota is final")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{lastResult: &ResultEvent{ErrorMessage: notFound}}), "not an error result")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{lastResult: errResult(notFound), answered: true}), "the model already answered")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{lastResult: &ResultEvent{IsError: true, ErrorMessage: notFound, OutputTokens: 3}}), "output tokens were spent")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{execErr: io.EOF, lastResult: errResult(notFound)}), "infrastructure failure")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{}), "no result")
}

// Claude Code 2.1.296 on Vertex never surfaces Vertex's text for a model the
// project does not serve; it reports its own message naming the model.
func TestClaudeShouldFallBack_ClaudeCodeSelectedModelMessage(t *testing.T) {
	msg := func(model string) string {
		return "There's an issue with the selected model (" + model + "). It may not exist or you may not have access to it. Run --model to pick a different model."
	}
	errResult := func(m string) *ResultEvent { return &ResultEvent{IsError: true, ErrorMessage: m} }

	for _, model := range []string{"claude-opus-4-1", "claude-3-5-sonnet-20240620", "claude-opus-9-9"} {
		assert.True(t, claudeShouldFallBack(claudeAttemptResult{model: model, exitCode: 1, lastResult: errResult(msg(model))}), model)
	}
	assert.True(t, claudeShouldFallBack(claudeAttemptResult{model: "Claude-Opus-9-9", lastResult: errResult(msg("claude-opus-9-9"))}),
		"the model id is compared case-insensitively")
	assert.True(t, claudeShouldFallBack(claudeAttemptResult{model: "claude-opus-5-5", lastResult: errResult(strings.Replace(msg("claude-opus-5-5"), "'", "’", 1))}),
		"a typographic apostrophe does not break the match")

	assert.False(t, claudeShouldFallBack(claudeAttemptResult{model: "claude-opus-5-5", lastResult: errResult(msg("claude-opus-4-6"))}),
		"the message must name the attempt's own model")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{model: "claude-opus-5", lastResult: errResult(msg("claude-opus-5-5"))}),
		"a prefix of the named model is not the attempt's model")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{lastResult: errResult(msg("claude-opus-5-5"))}),
		"no attempt model, no match")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{model: "claude-opus-5-5", lastResult: &ResultEvent{IsError: true, ErrorMessage: msg("claude-opus-5-5"), OutputTokens: 1}}),
		"output tokens were spent")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{model: "claude-opus-5-5", answered: true, lastResult: errResult(msg("claude-opus-5-5"))}),
		"the model already answered")
}

// For an id in its own model list — every default and fallback id except
// claude-haiku-4-5 — Claude Code 2.1.296 writes "The model <id> is not
// available on your vertex deployment" instead.
func TestClaudeShouldFallBack_ClaudeCodeModelNotAvailableMessage(t *testing.T) {
	msg := func(model string) string {
		return "The model " + model + " is not available on your vertex deployment. Try --model to switch to claude-opus-5, or ask your admin to enable this model."
	}
	errResult := func(m string) *ResultEvent { return &ResultEvent{IsError: true, ErrorMessage: m} }

	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-opus-4-6", "claude-sonnet-4-6"} {
		assert.True(t, claudeShouldFallBack(claudeAttemptResult{model: model, exitCode: 1, lastResult: errResult(msg(model))}), model)
	}
	assert.True(t, claudeShouldFallBack(claudeAttemptResult{model: "Claude-Opus-5-5", lastResult: errResult(msg("claude-opus-5-5"))}),
		"the model id is compared case-insensitively")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{model: "claude-opus-5-5", lastResult: errResult(msg("claude-opus-4-6"))}),
		"the message must name the attempt's own model")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{model: "claude-opus-5", lastResult: errResult(msg("claude-opus-5-5"))}),
		"a prefix of the named model is not the attempt's model")
	assert.False(t, claudeShouldFallBack(claudeAttemptResult{model: "claude-opus-5-5", lastResult: &ResultEvent{IsError: true, ErrorMessage: msg("claude-opus-5-5"), OutputTokens: 1}}),
		"output tokens were spent")
}

func TestClaudeAttemptOutput(t *testing.T) {
	assert.False(t, claudeAttemptOutput(TextEvent{Text: "The model claude-opus-5-5 is not available on your vertex deployment. Try --model to switch to claude-opus-5, or ask your admin to enable this model."}),
		"Claude Code's model-not-available message for a listed id is not output")
	assert.False(t, claudeAttemptOutput(TextEvent{Text: "There's an issue with the selected model (claude-opus-4-1). It may not exist or you may not have access to it. Run --model to pick a different model."}),
		"Claude Code's own model-not-available message is not output")
	assert.False(t, claudeAttemptOutput(TextEvent{Text: "There’s an issue with the selected model (claude-opus-4-1)."}))
	assert.False(t, claudeAttemptOutput(TextEvent{Text: "API Error: 404 not found"}), "the synthetic API error message is not output")
	assert.True(t, claudeAttemptOutput(TextEvent{Text: "Looking at the issue"}))
	assert.True(t, claudeAttemptOutput(ToolUseEvent{Name: "Bash"}))
	assert.True(t, claudeAttemptOutput(ThinkingEvent{Text: "hmm"}))
	assert.False(t, claudeAttemptOutput(InitEvent{Model: "m"}))
	assert.False(t, claudeAttemptOutput(ResultEvent{}))
}

func TestBuildRunCommand_FallbackModelsUseBuiltinDefault(t *testing.T) {
	cmd := buildRunCommand(RunParams{
		AgentBaseName:  "agent",
		Model:          "claude-opus-5-5",
		FallbackModels: []string{"sonnet", "claude-haiku-4-5"},
		RepoDir:        "/sandbox/workspace/repo",
	})
	assert.Contains(t, cmd, "--fallback-model 'claude-sonnet-5-5,claude-haiku-4-5'",
		"an aliased fallback entry gets its built-in default id")
	assert.Contains(t, cmd, "--model 'claude-opus-5-5'")
}

// fakeOpenshellClaude installs an openshell stub whose Claude launch answers
// with the stream in streams for the first model it finds in the command,
// and records every invocation to logPath.
func fakeOpenshellClaude(t *testing.T, logPath string, streams map[string]string) {
	t.Helper()
	dir := t.TempDir()
	var cases strings.Builder
	for model, stream := range streams {
		f := filepath.Join(dir, model+".ndjson")
		require.NoError(t, os.WriteFile(f, []byte(stream), 0o644))
		// Claude Code exits 1 when its result is an error.
		code := "0"
		if strings.Contains(stream, `"is_error":true`) {
			code = "1"
		}
		fmt.Fprintf(&cases, "  *\"--model '%s'\"*) cat '%s'; exit %s ;;\n", model, f, code)
	}
	script := "#!/bin/sh\necho \"$*\" >> '" + logPath + "'\ncase \"$*\" in\n" + cases.String() + "esac\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const claudeNotServedStream = `{"type":"system","subtype":"init","model":"claude-opus-5-5","claude_code_version":"2.1.296","session_id":"0b7b6c8a-930e-4750-89b9-3129edaa5ccf"}
{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: 404 Publisher model projects/p/locations/us-east5/publishers/anthropic/models/claude-opus-5-5 not found."}]}}
{"type":"result","subtype":"success","is_error":true,"result":"API Error: 404 Publisher model projects/p/locations/us-east5/publishers/anthropic/models/claude-opus-5-5 not found.","num_turns":1,"total_cost_usd":0}
`

const claudeServedStream = `{"type":"system","subtype":"init","model":"claude-opus-4-6","claude_code_version":"2.1.296","session_id":"1c8c7d9b-0000-4750-89b9-3129edaa5ccf"}
{"type":"assistant","message":{"model":"claude-opus-4-6","content":[{"type":"text","text":"done"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"done","num_turns":1,"total_cost_usd":0.01,"usage":{"input_tokens":10,"output_tokens":2}}
`

// A project that does not serve the alias's default runs on the fallback:
// the abandoned attempt leaves no events and no transcript behind, and the
// fallback is recorded in the metrics.
func TestClaudeRuntime_Run_FallsBackWhenNotServed(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellClaude(t, logPath, map[string]string{
		"claude-opus-5-5": claudeNotServedStream,
		"claude-opus-4-6": claudeServedStream,
	})

	var events []AgentEvent
	var metrics RunMetrics
	var out bytes.Buffer
	exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "fs-claude-fallback", AgentBaseName: "code", RepoDir: "/r", Model: "opus",
		Timeout: 30 * time.Second, OnEvent: func(e AgentEvent) { events = append(events, e) },
	}, ui.New(&out), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)

	assert.Equal(t, "claude-opus-4-6", metrics.Model)
	assert.Equal(t, []string{"claude-opus-5-5"}, metrics.FallbackFrom)
	assert.Equal(t, []string{"not_served"}, metrics.FallbackReasons)
	assert.Contains(t, out.String(), "model claude-opus-5-5 is not available (not served in this project); falling back to claude-opus-4-6")
	for _, e := range events {
		if txt, ok := e.(TextEvent); ok {
			assert.NotContains(t, txt.Text, "API Error", "the abandoned attempt's events are discarded")
		}
		if res, ok := e.(ResultEvent); ok {
			assert.False(t, res.IsError, "only the served attempt's result is rendered")
		}
	}

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "--model 'claude-opus-5-5'")
	assert.Contains(t, string(log), "--model 'claude-opus-4-6'")
	assert.NotContains(t, string(log), "--fallback-model", "the built-in chain is not passed to Claude Code")
	assert.Contains(t, string(log), "0b7b6c8a-930e-4750-89b9-3129edaa5ccf.jsonl", "the abandoned transcript is removed")
}

// Any other error is final: no second attempt.
func TestClaudeRuntime_Run_OtherErrorsDoNotFallBack(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	denied := strings.ReplaceAll(claudeNotServedStream,
		"404 Publisher model projects/p/locations/us-east5/publishers/anthropic/models/claude-opus-5-5 not found.",
		"403 PERMISSION_DENIED: Permission denied on resource project p.")
	fakeOpenshellClaude(t, logPath, map[string]string{"claude-opus-5-5": denied, "claude-opus-4-6": claudeServedStream})

	var metrics RunMetrics
	exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "fs-claude-nofallback", AgentBaseName: "code", RepoDir: "/r", Model: "opus",
		Timeout: 30 * time.Second, OnEvent: func(AgentEvent) {},
	}, ui.New(io.Discard), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 1, exit)
	assert.Empty(t, metrics.FallbackFrom)

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.NotContains(t, string(log), "--model 'claude-opus-4-6'")
}

// claudeSelectedModelStream is what the pinned Claude Code (2.1.296) on
// Vertex (CLAUDE_CODE_USE_VERTEX=1) writes for a model the project does not
// serve, as probed live: a synthetic assistant message and an error result
// with "terminal_reason":"api_error", both with zero output tokens and
// Claude Code's own text rather than Vertex's. Only the observed fields
// are reproduced. %[1]s is the model id in the message.
const claudeSelectedModelStream = `{"type":"system","subtype":"init","model":"%[1]s","claude_code_version":"2.1.296","session_id":"2d9e8f0a-1111-4750-89b9-3129edaa5ccf"}
{"type":"assistant","message":{"model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0},"content":[{"type":"text","text":"There's an issue with the selected model (%[1]s). It may not exist or you may not have access to it. Run --model to pick a different model."}]}}
{"type":"result","subtype":"success","is_error":true,"result":"There's an issue with the selected model (%[1]s). It may not exist or you may not have access to it. Run --model to pick a different model.","num_turns":1,"total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0},"terminal_reason":"api_error"}
`

// claudeModelNotAvailableStream is what the pinned Claude Code (2.1.296)
// writes when Vertex answers 404 for an id in Claude Code's own model list,
// reproduced against a stub Vertex endpoint. %[1]s is the model id.
const claudeModelNotAvailableStream = `{"type":"system","subtype":"init","model":"%[1]s","claude_code_version":"2.1.296","session_id":"5b0c7d3e-2222-4f1a-9a77-0d8e1c2b3a44"}
{"type":"assistant","message":{"model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0},"content":[{"type":"text","text":"The model %[1]s is not available on your vertex deployment. Try --model to switch to claude-opus-5, or ask your admin to enable this model."}]},"error":"model_not_found","is_api_error_message":true}
{"type":"result","subtype":"success","is_error":true,"api_error_status":404,"result":"The model %[1]s is not available on your vertex deployment. Try --model to switch to claude-opus-5, or ask your admin to enable this model.","num_turns":1,"total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0},"terminal_reason":"api_error"}
`

// The shipped default (claude-opus-5-5) is in Claude Code's model list, so
// an unserved default reports "The model ... is not available"; the run
// still falls back to claude-opus-4-6.
func TestClaudeRuntime_Run_FallsBackOnClaudeCodeModelNotAvailableMessage(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellClaude(t, logPath, map[string]string{
		"claude-opus-5-5": fmt.Sprintf(claudeModelNotAvailableStream, "claude-opus-5-5"),
		"claude-opus-4-6": claudeServedStream,
	})

	var events []AgentEvent
	var metrics RunMetrics
	exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "fs-claude-fallback-na", AgentBaseName: "code", RepoDir: "/r", Model: "opus",
		Timeout: 30 * time.Second, OnEvent: func(e AgentEvent) { events = append(events, e) },
	}, ui.New(io.Discard), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Equal(t, "claude-opus-4-6", metrics.Model)
	assert.Equal(t, []string{"claude-opus-5-5"}, metrics.FallbackFrom)
	for _, e := range events {
		if txt, ok := e.(TextEvent); ok {
			assert.NotContains(t, txt.Text, "is not available on your", "the abandoned attempt's events are discarded")
		}
		if res, ok := e.(ResultEvent); ok {
			assert.False(t, res.IsError, "only the served attempt's result is rendered")
		}
	}

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "--model 'claude-opus-4-6'")
	assert.Contains(t, string(log), "5b0c7d3e-2222-4f1a-9a77-0d8e1c2b3a44.jsonl", "the abandoned transcript is removed")
}

// The fallback fires on the stream Claude Code writes for an id outside its
// model list, and none of the abandoned attempt's synthetic text reaches
// the run.
func TestClaudeRuntime_Run_FallsBackOnClaudeCodeSelectedModelMessage(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellClaude(t, logPath, map[string]string{
		"claude-opus-5-5": fmt.Sprintf(claudeSelectedModelStream, "claude-opus-5-5"),
		"claude-opus-4-6": claudeServedStream,
	})

	var events []AgentEvent
	var metrics RunMetrics
	exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "fs-claude-fallback-cc", AgentBaseName: "code", RepoDir: "/r", Model: "opus",
		Timeout: 30 * time.Second, OnEvent: func(e AgentEvent) { events = append(events, e) },
	}, ui.New(io.Discard), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Equal(t, "claude-opus-4-6", metrics.Model)
	assert.Equal(t, []string{"claude-opus-5-5"}, metrics.FallbackFrom)
	for _, e := range events {
		if txt, ok := e.(TextEvent); ok {
			assert.NotContains(t, txt.Text, "issue with the selected model", "the abandoned attempt's events are discarded")
		}
		if res, ok := e.(ResultEvent); ok {
			assert.False(t, res.IsError, "only the served attempt's result is rendered")
		}
		if ini, ok := e.(InitEvent); ok {
			assert.NotEqual(t, claudeSyntheticModel, ini.Model)
		}
	}

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "--model 'claude-opus-4-6'")
	assert.Contains(t, string(log), "2d9e8f0a-1111-4750-89b9-3129edaa5ccf.jsonl", "the abandoned transcript is removed")
}

// The same message naming a model other than the attempt's is final.
func TestClaudeRuntime_Run_SelectedModelMessageForAnotherModelIsFinal(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellClaude(t, logPath, map[string]string{
		"claude-opus-5-5": fmt.Sprintf(claudeSelectedModelStream, "claude-haiku-4-5"),
		"claude-opus-4-6": claudeServedStream,
	})

	var metrics RunMetrics
	exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "fs-claude-fallback-cc-other", AgentBaseName: "code", RepoDir: "/r", Model: "opus",
		Timeout: 30 * time.Second, OnEvent: func(AgentEvent) {},
	}, ui.New(io.Discard), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 1, exit)
	assert.Empty(t, metrics.FallbackFrom)

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.NotContains(t, string(log), "--model 'claude-opus-4-6'")
	assert.NotContains(t, string(log), "-delete", "the final attempt's transcript is kept")
}

// Text on a <synthetic> assistant message is never taken as output, whatever
// it says, so it cannot mark the attempt answered and block the fallback.
func TestClaudeRuntime_Run_SyntheticTextIsNotOutput(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	stream := strings.Replace(claudeNotServedStream,
		`"text":"API Error: 404 Publisher model`, `"text":"Request failed: 404 Publisher model`, 1)
	require.NotEqual(t, claudeNotServedStream, stream)
	fakeOpenshellClaude(t, logPath, map[string]string{"claude-opus-5-5": stream, "claude-opus-4-6": claudeServedStream})

	var metrics RunMetrics
	exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "fs-claude-fallback-synthetic", AgentBaseName: "code", RepoDir: "/r", Model: "opus",
		Timeout: 30 * time.Second, OnEvent: func(AgentEvent) {},
	}, ui.New(io.Discard), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Equal(t, []string{"claude-opus-5-5"}, metrics.FallbackFrom)
}

// When no fallback can start (the budget left is under a second past the
// minimum attempt), the not-served attempt is final and its transcript is
// kept: cleanup runs only once a retry actually starts.
func TestClaudeRuntime_Run_NoBudgetKeepsFinalTranscript(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellClaude(t, logPath, map[string]string{
		"claude-opus-5-5": claudeNotServedStream,
		"claude-opus-4-6": claudeServedStream,
	})

	var metrics RunMetrics
	exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "fs-claude-fallback-nobudget", AgentBaseName: "code", RepoDir: "/r", Model: "opus",
		Timeout: minFallbackAttemptTimeout + 500*time.Millisecond, OnEvent: func(AgentEvent) {},
	}, ui.New(io.Discard), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 1, exit)
	assert.Empty(t, metrics.FallbackFrom)

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.NotContains(t, string(log), "--model 'claude-opus-4-6'", "no fallback starts")
	assert.NotContains(t, string(log), "0b7b6c8a-930e-4750-89b9-3129edaa5ccf.jsonl", "the final attempt's transcript is kept")
}

// Removing an abandoned transcript is best effort: a failure is reported,
// and a session id that is not one is never put in a sandbox command.
func TestClaudeRemoveTranscript(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "openshell.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + logPath + "'\necho 'find: permission denied' >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out strings.Builder
	ClaudeRuntime{}.claudeRemoveTranscript("fs-rm", "0b7b6c8a-930e-4750-89b9-3129edaa5ccf", time.Second, ui.New(&out))
	assert.Contains(t, out.String(), "Could not remove the abandoned attempt's transcript")
	assert.Contains(t, out.String(), "permission denied")

	require.NoError(t, os.Remove(logPath))
	out.Reset()
	ClaudeRuntime{}.claudeRemoveTranscript("fs-rm", "x; rm -rf /", time.Second, ui.New(&out))
	ClaudeRuntime{}.claudeRemoveTranscript("fs-rm", "", time.Second, ui.New(&out))
	assert.NoFileExists(t, logPath, "an invalid session id runs nothing")
	assert.Empty(t, out.String())
}

// A frontmatter alias the repo retargets reaches Claude Code as the
// retarget's id, not as the bare alias Claude Code would map itself.
func TestClaudeRuntime_Run_FrontmatterAliasRetarget(t *testing.T) {
	const sb = "fs-claude-frontmatter-retarget"
	claudeAgentModels.Store(sb, "sonnet")
	t.Cleanup(func() { claudeAgentModels.Delete(sb) })
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellClaude(t, logPath, map[string]string{"claude-sonnet-5": claudeServedStream})

	var metrics RunMetrics
	exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: sb, AgentBaseName: "code", RepoDir: "/r",
		ModelAliases: map[string]string{"sonnet": "claude-sonnet-5"},
		Timeout:      30 * time.Second, OnEvent: func(AgentEvent) {},
	}, ui.New(io.Discard), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "--model 'claude-sonnet-5'")
}

// pi Run applies the alias's built-in chain with no FULLSEND_FALLBACK_MODELS
// set, and records the fallback in the metrics.
func TestPiRuntimeRun_ImplicitFallbackWhenNotServed(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	logPath := filepath.Join(work, "openshell.log")
	fixture, err := filepath.Abs(filepath.Join("testdata", "pi", "basic_run.ndjson"))
	require.NoError(t, err)
	fakeOpenshellPi(t, logPath, store, fixture)
	inner, err := exec.LookPath("openshell")
	require.NoError(t, err)

	notServed := filepath.Join(work, "not-served.ndjson")
	msg := `{"role":"assistant","content":[],"model":"claude-opus-5-5","provider":"anthropic-vertex","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"total":0}},"stopReason":"error","errorMessage":"404 Publisher model projects/p/locations/us-east5/publishers/anthropic/models/claude-opus-5-5 not found."}`
	require.NoError(t, os.WriteFile(notServed, []byte(`{"type":"agent_start"}`+"\n"+
		`{"type":"message_end","message":`+msg+"}\n"+
		`{"type":"agent_end","messages":[`+msg+"]}\n"), 0o644))
	wrapDir := t.TempDir()
	wrapper := "#!/bin/sh\ncase \"$*\" in\n  *\"--model 'anthropic-vertex/claude-opus-5-5'\"*) echo \"$@\" >> '" + logPath + "'; cat '" + notServed + "'; exit 0 ;;\nesac\nexec '" + inner + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(wrapDir, "openshell"), []byte(wrapper), 0o755))
	t.Setenv("PATH", wrapDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: writeAgentFile(t, testAgentDef), agentName: "triage",
	}))
	var events []AgentEvent
	var metrics RunMetrics
	var out bytes.Buffer
	exit, err := PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", AgentBaseName: "triage", RepoDir: "/sandbox/workspace/repo", Model: "opus",
		Timeout: 30 * time.Second, OnEvent: func(e AgentEvent) { events = append(events, e) },
	}, ui.New(&out), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Equal(t, "claude-opus-4-6", metrics.Model)
	assert.Equal(t, []string{"claude-opus-5-5"}, metrics.FallbackFrom)
	assert.Equal(t, []string{"not_served"}, metrics.FallbackReasons)
	assert.Contains(t, out.String(), "model claude-opus-5-5 is not available (not served in this project); falling back to claude-opus-4-6")
	for _, e := range events {
		if res, ok := e.(ResultEvent); ok {
			assert.False(t, res.IsError, "the abandoned attempt's error is not rendered")
		}
	}
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "--model 'anthropic-vertex/claude-opus-5-5'")
	assert.Contains(t, string(log), "--model 'anthropic-vertex/claude-opus-4-6'")
}

func TestPiAgentModelFallbacks(t *testing.T) {
	assert.Equal(t, map[string][]string{
		"opus":   {"anthropic-vertex/claude-opus-4-6"},
		"sonnet": {"anthropic-vertex/claude-sonnet-4-6"},
		"haiku":  {"anthropic-vertex/claude-haiku-4-5"},
	}, piAgentModelFallbacks(nil))
	got := piAgentModelFallbacks(map[string]string{"sonnet": "claude-sonnet-5", "opus": "x", "haiku": "y"})
	assert.Nil(t, got, "every alias retargeted leaves no table")
}

func TestPiApplyChildFallbacks(t *testing.T) {
	strp := func(s string) *string { return &s }
	m := &piAgentManifest{
		ModelFallbacks:  piAgentModelFallbacks(nil),
		SubagentDefault: "anthropic-vertex/claude-sonnet-5-5",
		Personas: map[string]piPersonaManifestEntry{
			"challenger":  {Model: "anthropic-vertex/claude-opus-5-5"},
			"correctness": {Model: "anthropic-vertex/claude-haiku-5-5"},
			"pinned":      {Model: "anthropic-vertex/claude-opus-5-5"},
			"inherits":    {},
			"defaulted":   {Model: "anthropic-vertex/claude-sonnet-5-5"},
		},
	}
	personas := []piPersona{
		{Name: "challenger", Model: "sonnet"},
		{Name: "correctness", Model: "haiku@default"},
		{Name: "pinned", Model: "claude-opus-5-5"},
		{Name: "inherits"},
		{Name: "defaulted"},
	}
	cfg := map[string]*string{"default": strp("sonnet"), "challenger": strp("opus")}
	piApplyChildFallbacks(m, personas, cfg)

	assert.Equal(t, []string{"anthropic-vertex/claude-sonnet-4-6"}, m.SubagentDefaultFallbacks)
	assert.Equal(t, []string{"anthropic-vertex/claude-opus-4-6"}, m.Personas["challenger"].Fallbacks, "subagents.<persona> outranks the frontmatter")
	assert.Equal(t, []string{"anthropic-vertex/claude-haiku-4-5"}, m.Personas["correctness"].Fallbacks, "an @suffix is dropped")
	assert.Nil(t, m.Personas["pinned"].Fallbacks, "a pinned id never falls back")
	assert.Nil(t, m.Personas["inherits"].Fallbacks, "an inheriting persona takes the parent's live model")
	assert.Equal(t, []string{"anthropic-vertex/claude-sonnet-4-6"}, m.Personas["defaulted"].Fallbacks, "subagents.default applies to a persona that says nothing")

	pinnedDefault := &piAgentManifest{ModelFallbacks: piAgentModelFallbacks(nil), SubagentDefault: "anthropic-vertex/claude-sonnet-5-5"}
	piApplyChildFallbacks(pinnedDefault, nil, map[string]*string{"default": strp("anthropic-vertex/claude-sonnet-5-5")})
	assert.Nil(t, pinnedDefault.SubagentDefaultFallbacks)
}

// The fallback-chain ids were the alias defaults before #8426; a child
// pinned to one, bare or qualified, keeps resolving and stays trusted.
func TestPiTrustedSpecs_KeepFallbackChainIDs(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	models := piAgentModels("opus", nil)
	fallbacks := piAgentModelFallbacks(nil)
	trusted := piTrustedSpecs(models, fallbacks, nil, "", nil)
	resolve := piChildSpecResolver(models, fallbacks)
	for _, id := range []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5"} {
		spec := "anthropic-vertex/" + id
		assert.Contains(t, trusted, spec)
		assert.Equal(t, spec, resolve(id), "the bare id resolves to its Vertex spec")
		assert.Equal(t, spec, resolve(strings.ToUpper(id)+"@default"))
	}
	assert.Equal(t, "anthropic-vertex/claude-sonnet-5-5", resolve("sonnet"), "an alias still resolves to its own default")
	assert.Equal(t, "claude-sonnet-4-20250514", resolve("claude-sonnet-4-20250514"), "an id in no table is returned as written")
	assert.NotContains(t, trusted, "anthropic-vertex/claude-sonnet-4-20250514")

	retargeted := map[string]string{"sonnet": "claude-sonnet-5"}
	trusted = piTrustedSpecs(piAgentModels("opus", retargeted), piAgentModelFallbacks(retargeted), nil, "", retargeted)
	assert.NotContains(t, trusted, "anthropic-vertex/claude-sonnet-4-6",
		"a retargeted alias has no chain, so its old default is not trusted (as before #8426)")
	assert.Contains(t, trusted, "anthropic-vertex/claude-opus-4-6")
}

// Bootstrap accepts a persona or subagents entry pinned to a previous
// default, bare or qualified, and still rejects an id it cannot serve.
func TestPiBootstrap_PinnedPreviousGenerationChildModels(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	cfg := PiRuntime{}.ConfigDir()
	bootstrap := func(t *testing.T, personaModel string, subagents map[string]*string, aliases map[string]string) (*piManifest, error) {
		t.Helper()
		forgetPiManifestHash(t, "sb")
		work := t.TempDir()
		store := filepath.Join(work, "store")
		fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")
		skill := t.TempDir()
		frontmatter := "---\nname: checker\n---\nCheck something.\n"
		if personaModel != "" {
			frontmatter = "---\nname: checker\nmodel: " + personaModel + "\n---\nCheck something.\n"
		}
		writePersonaFile(t, skill, "checker", frontmatter)
		err := PiRuntime{}.Bootstrap(bootstrapInput{
			sandboxName:    "sb",
			agentPath:      writeAgentFile(t, "---\nname: review\nmodel: opus\n---\nReview the PR."),
			agentName:      "review",
			skillDirs:      []string{skill},
			agentSubagents: subagents,
			modelAliases:   aliases,
		})
		if err != nil {
			return nil, err
		}
		var m piManifest
		require.NoError(t, json.Unmarshal(storedUpload(t, store, cfg+"/fullsend-manifest.json"), &m))
		require.NotNil(t, m.Agent)
		return &m, nil
	}

	for _, model := range []string{"claude-sonnet-4-6", "anthropic-vertex/claude-sonnet-4-6"} {
		t.Run("subagents entry "+model, func(t *testing.T) {
			m, err := bootstrap(t, "", map[string]*string{"checker": strp(model), "default": strp(model)}, nil)
			require.NoError(t, err)
			assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", m.Agent.Personas["checker"].Model)
			assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", m.Agent.SubagentDefault)
			assert.Empty(t, m.Agent.Personas["checker"].Fallbacks, "a pinned id never falls back")
		})
		t.Run("persona frontmatter "+model, func(t *testing.T) {
			m, err := bootstrap(t, model, nil, nil)
			require.NoError(t, err)
			assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", m.Agent.Personas["checker"].Model)
		})
	}

	t.Run("an id in no table is still rejected", func(t *testing.T) {
		_, err := bootstrap(t, "", map[string]*string{"checker": strp("claude-sonnet-4-20250514")}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `resolved model "claude-sonnet-4-20250514" is not available in this run`)
	})
}

// With an explicit FULLSEND_FALLBACK_MODELS list, Claude Code 2.1.296 moves
// to the next --fallback-model entry within the session on a Vertex 404 and
// reports it as a "model_fallback" system event (observed live); the init
// event still names the model that failed. The metrics must name the model
// that answered and record the one that did not.
func TestRecordClaudeMetrics_InSessionModelFallback(t *testing.T) {
	stream := `{"type":"system","subtype":"init","model":"claude-opus-4-5","claude_code_version":"2.1.296","session_id":"7c1d2e3f-3333-4a5b-8c9d-0e1f2a3b4c5d"}
{"type":"system","subtype":"model_fallback","trigger":"last_resort","original_model":"claude-opus-4-5","fallback_model":"claude-haiku-5-5","content":"Switched to Haiku 5.5 because claude-opus-4-5 returned an error that could not be retried (404 Publisher model ... not found)"}
{"type":"assistant","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":10,"output_tokens":2},"content":[{"type":"text","text":"done"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"done","num_turns":1,"usage":{"input_tokens":10,"output_tokens":2}}
`
	var metrics RunMetrics
	var events []AgentEvent
	require.NoError(t, parseClaudeStream(strings.NewReader(stream), func(e AgentEvent) {
		recordClaudeMetrics(&metrics, e)
		events = append(events, e)
	}))
	assert.Equal(t, "claude-haiku-5-5", metrics.Model)
	assert.Equal(t, []string{"claude-opus-4-5"}, metrics.FallbackFrom)
	assert.Equal(t, []string{"claude_code:last_resort"}, metrics.FallbackReasons)
	assert.Contains(t, events, AgentEvent(ModelFallbackEvent{From: "claude-opus-4-5", To: "claude-haiku-5-5", Trigger: "last_resort"}))
	assert.False(t, claudeAttemptOutput(ModelFallbackEvent{}), "a fallback notice is not model output")

	// The same model is not recorded twice.
	recordClaudeMetrics(&metrics, ModelFallbackEvent{From: "claude-opus-4-5", To: "claude-haiku-5-5"})
	assert.Equal(t, []string{"claude-opus-4-5"}, metrics.FallbackFrom)
	assert.Equal(t, []string{"claude_code:last_resort"}, metrics.FallbackReasons)

	// A switch with no trigger still records a reason, aligned with its model.
	recordClaudeMetrics(&metrics, ModelFallbackEvent{From: "claude-haiku-5-5", To: "claude-haiku-4-5"})
	assert.Equal(t, []string{"claude-opus-4-5", "claude-haiku-5-5"}, metrics.FallbackFrom)
	assert.Equal(t, []string{"claude_code:last_resort", "claude_code"}, metrics.FallbackReasons)
}

func TestFallbackReasonText(t *testing.T) {
	for reason, want := range map[string]string{
		"not_served":              "not served in this project",
		"claude_code":             "Claude Code",
		"claude_code:last_resort": "Claude Code: last_resort",
		"something_else":          "something_else",
	} {
		assert.Equal(t, want, fallbackReasonText(reason), reason)
	}
	assert.Equal(t, "claude_code:overloaded", claudeCodeFallbackReason(" overloaded "))
	assert.Equal(t, "claude_code", claudeCodeFallbackReason(" "))
}

func TestRecordFallback_IgnoresEmptyModel(t *testing.T) {
	var m RunMetrics
	m.recordFallback("", "not_served")
	assert.Empty(t, m.FallbackFrom)
	assert.Empty(t, m.FallbackReasons)
}

// BuiltinAliasModel names the id a bare alias resolves to through the
// runtime's own table, which the plan block prints; a retargeted alias, a
// pinned id and a runtime without a table have none.
func TestBuiltinAliasModel(t *testing.T) {
	for _, tc := range []struct {
		runtime, model string
		aliases        map[string]string
		want           string
	}{
		{"claude", "haiku", nil, "claude-haiku-5-5"},
		{"claude", " opus ", nil, "claude-opus-5-5"},
		{"claude", "fable", nil, ""},
		{"claude", "haiku", map[string]string{"haiku": "claude-haiku-4-5"}, ""},
		{"claude", "claude-opus-4-6", nil, ""},
		{"pi", "sonnet", nil, "claude-sonnet-5-5"},
		{"pi", "fable", nil, "claude-fable-5-1"},
		{"pi", "anthropic-vertex/claude-opus-4-6", nil, ""},
		{"codex", "opus", nil, ""},
	} {
		assert.Equal(t, tc.want, BuiltinAliasModel(tc.runtime, tc.model, tc.aliases), "%s %q", tc.runtime, tc.model)
	}
}

// A runner fallback followed by Claude Code's own in-session fallback on the
// next attempt records both models, in the order the run left them.
func TestClaudeRuntime_Run_RunnerThenInSessionFallback(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	inSession := `{"type":"system","subtype":"init","model":"claude-sonnet-5-5","claude_code_version":"2.1.296","session_id":"9e8d7c6b-4444-4a5b-8c9d-0e1f2a3b4c5d"}
{"type":"system","subtype":"model_fallback","trigger":"last_resort","original_model":"claude-sonnet-5-5","fallback_model":"claude-haiku-5-5"}
{"type":"assistant","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":10,"output_tokens":2},"content":[{"type":"text","text":"done"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"done","num_turns":1,"usage":{"input_tokens":10,"output_tokens":2}}
`
	fakeOpenshellClaude(t, logPath, map[string]string{
		"claude-opus-5-5":   fmt.Sprintf(claudeModelNotAvailableStream, "claude-opus-5-5"),
		"claude-sonnet-5-5": inSession,
	})

	var metrics RunMetrics
	exit, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "fs-claude-fallback-mixed", AgentBaseName: "code", RepoDir: "/r", Model: "opus",
		FallbackModels: []string{"sonnet", "haiku"},
		Timeout:        30 * time.Second, OnEvent: func(AgentEvent) {},
	}, ui.New(io.Discard), time.Now(), &metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Equal(t, "claude-haiku-5-5", metrics.Model)
	assert.Equal(t, []string{"claude-opus-5-5", "claude-sonnet-5-5"}, metrics.FallbackFrom)
	assert.Equal(t, []string{"not_served", "claude_code:last_resort"}, metrics.FallbackReasons)
}
