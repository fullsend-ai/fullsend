package security

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

func TestGenerateClaudeSettings_AllDefaults(t *testing.T) {
	h := &harness.Harness{Agent: "test.md"}
	data, err := GenerateClaudeSettings(h)
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	assert.Contains(t, hooks, "PreToolUse")
	assert.Contains(t, hooks, "PostToolUse")

	preTools := hooks["PreToolUse"].([]any)
	assert.Len(t, preTools, 3) // tirith + ssrf + canary_pretool (tool_allowlist disabled by default)

	postTools := hooks["PostToolUse"].([]any)
	assert.Len(t, postTools, 2) // Bash|WebFetch|Read chain + canary * matcher

	// Verify sanitization hooks are chained within the first matcher.
	matcher := postTools[0].(map[string]any)
	assert.Equal(t, "Bash|WebFetch|Read", matcher["matcher"])
	chainedHooks := matcher["hooks"].([]any)
	assert.Len(t, chainedHooks, 3) // context_suppress → secret_redact → unicode

	// Verify canary hook has its own * matcher.
	canaryMatcher := postTools[1].(map[string]any)
	assert.Equal(t, "*", canaryMatcher["matcher"])
	canaryHooks := canaryMatcher["hooks"].([]any)
	assert.Len(t, canaryHooks, 1)
}

func TestGenerateClaudeSettings_TirithDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				Tirith: &harness.TirithConfig{Enabled: &disabled},
			},
		},
	}
	data, err := GenerateClaudeSettings(h)
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	preTools := hooks["PreToolUse"].([]any)
	assert.Len(t, preTools, 2) // ssrf + canary_pretool
}

func TestGenerateClaudeSettings_AllHooksDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				Tirith:                  &harness.TirithConfig{Enabled: &disabled},
				SSRFPreTool:             &disabled,
				SecretRedactPostTool:    &disabled,
				UnicodePostTool:         &disabled,
				ContextSuppressPostTool: &disabled,
				CanaryPreTool:           &disabled,
				CanaryPostTool:          &disabled,
				// ToolAllowlistPreTool omitted — already disabled by default
			},
		},
	}
	data, err := GenerateClaudeSettings(h)
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	assert.NotContains(t, hooks, "PreToolUse")
	assert.NotContains(t, hooks, "PostToolUse")
}

func TestHookFiles_AllDefaults(t *testing.T) {
	h := &harness.Harness{Agent: "test.md"}
	files := HookFiles(h)
	assert.Len(t, files, 7) // 5 existing + canary_pretool + canary_posttool (tool_allowlist disabled by default)
	assert.Contains(t, files, "tirith_check.py")
	assert.Contains(t, files, "ssrf_pretool.py")
	assert.Contains(t, files, "secret_redact_posttool.py")
	assert.Contains(t, files, "unicode_posttool.py")
	assert.Contains(t, files, "context_suppress_posttool.py")
	assert.Contains(t, files, "canary_pretool.py")
	assert.Contains(t, files, "canary_posttool.py")
	assert.NotContains(t, files, "tool_allowlist_pretool.py")

	// Verify embedded content is non-empty.
	for name, content := range files {
		assert.NotEmpty(t, content, "hook %s should have content", name)
	}
}

func TestHookFiles_SSRFDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				SSRFPreTool: &disabled,
			},
		},
	}
	files := HookFiles(h)
	assert.Len(t, files, 6) // both canary hooks still enabled
	assert.NotContains(t, files, "ssrf_pretool.py")
}

func TestHookFiles_UnicodeDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				UnicodePostTool: &disabled,
			},
		},
	}
	files := HookFiles(h)
	assert.Len(t, files, 6) // both canary hooks still enabled
	assert.NotContains(t, files, "unicode_posttool.py")
}

func TestEmbeddedHooksNotEmpty(t *testing.T) {
	assert.NotEmpty(t, SSRFPreToolHook)
	assert.NotEmpty(t, SecretRedactPostToolHook)
	assert.NotEmpty(t, TirithCheckHook)
	assert.NotEmpty(t, UnicodePostToolHook)
	assert.NotEmpty(t, ContextSuppressPostToolHook)
	assert.NotEmpty(t, CanaryPreToolHook)
	assert.NotEmpty(t, CanaryPostToolHook)
	assert.NotEmpty(t, ToolAllowlistPreToolHook)
}

func TestGenerateClaudeSettings_UnicodeDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				UnicodePostTool: &disabled,
			},
		},
	}
	data, err := GenerateClaudeSettings(h)
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	postTools := hooks["PostToolUse"].([]any)
	assert.Len(t, postTools, 2) // chain matcher + canary matcher

	// With unicode disabled: context_suppress + secret_redact in the chain.
	matcher := postTools[0].(map[string]any)
	chainedHooks := matcher["hooks"].([]any)
	assert.Len(t, chainedHooks, 2) // context_suppress + secret_redact
}

func TestGenerateClaudeSettings_SecretRedactDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				SecretRedactPostTool: &disabled,
			},
		},
	}
	data, err := GenerateClaudeSettings(h)
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	postTools := hooks["PostToolUse"].([]any)
	assert.Len(t, postTools, 2) // chain matcher + canary matcher

	// With secret_redact disabled: context_suppress + unicode in the chain.
	matcher := postTools[0].(map[string]any)
	chainedHooks := matcher["hooks"].([]any)
	assert.Len(t, chainedHooks, 2) // context_suppress + unicode
}

func TestGenerateClaudeSettings_ContextSuppressDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				ContextSuppressPostTool: &disabled,
			},
		},
	}
	data, err := GenerateClaudeSettings(h)
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	postTools := hooks["PostToolUse"].([]any)
	assert.Len(t, postTools, 2) // chain matcher + canary matcher

	// With context_suppress disabled: secret_redact + unicode in the chain.
	matcher := postTools[0].(map[string]any)
	chainedHooks := matcher["hooks"].([]any)
	assert.Len(t, chainedHooks, 2) // secret_redact + unicode
}

func TestGenerateClaudeSettings_CanaryPostToolDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				CanaryPostTool: &disabled,
			},
		},
	}
	data, err := GenerateClaudeSettings(h)
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	postTools := hooks["PostToolUse"].([]any)
	assert.Len(t, postTools, 1) // only the chain matcher, no canary posttool

	matcher := postTools[0].(map[string]any)
	assert.Equal(t, "Bash|WebFetch|Read", matcher["matcher"])

	// canary_pretool should still be in PreToolUse
	preTools := hooks["PreToolUse"].([]any)
	assert.Len(t, preTools, 3) // tirith + ssrf + canary_pretool
}

func TestGenerateClaudeSettings_CanaryPreToolDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				CanaryPreTool: &disabled,
			},
		},
	}
	data, err := GenerateClaudeSettings(h)
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	preTools := hooks["PreToolUse"].([]any)
	assert.Len(t, preTools, 2) // tirith + ssrf, no canary_pretool

	// canary_posttool should still be in PostToolUse
	postTools := hooks["PostToolUse"].([]any)
	assert.Len(t, postTools, 2) // chain + canary_posttool
}

func TestGenerateClaudeSettings_ToolAllowlistEnabled(t *testing.T) {
	enabled := true
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				ToolAllowlistPreTool: &harness.ToolAllowlistConfig{Enabled: &enabled},
			},
		},
	}
	data, err := GenerateClaudeSettings(h)
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	preTools := hooks["PreToolUse"].([]any)
	assert.Len(t, preTools, 4) // tirith + ssrf + canary_pretool + tool_allowlist

	// Tool allowlist should be the last PreToolUse matcher.
	allowlistMatcher := preTools[3].(map[string]any)
	assert.Equal(t, "*", allowlistMatcher["matcher"])
	allowlistHooks := allowlistMatcher["hooks"].([]any)
	assert.Contains(t, allowlistHooks[0].(map[string]any)["command"], "tool_allowlist_pretool.py")
}

func TestHookFiles_ToolAllowlistEnabled(t *testing.T) {
	enabled := true
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				ToolAllowlistPreTool: &harness.ToolAllowlistConfig{Enabled: &enabled},
			},
		},
	}
	files := HookFiles(h)
	assert.Len(t, files, 8) // 7 default + tool_allowlist
	assert.Contains(t, files, "tool_allowlist_pretool.py")
}

func TestHookFiles_ContextSuppressDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				ContextSuppressPostTool: &disabled,
			},
		},
	}
	files := HookFiles(h)
	assert.Len(t, files, 6) // both canary hooks still enabled
	assert.NotContains(t, files, "context_suppress_posttool.py")
}
