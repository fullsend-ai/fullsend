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
	assert.Len(t, preTools, 2) // tirith + ssrf

	postTools := hooks["PostToolUse"].([]any)
	assert.Len(t, postTools, 1) // secret_redact
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
	assert.Len(t, preTools, 1) // only ssrf
}

func TestGenerateClaudeSettings_AllHooksDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				Tirith:               &harness.TirithConfig{Enabled: &disabled},
				SSRFPreTool:          &disabled,
				SecretRedactPostTool: &disabled,
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
	assert.Len(t, files, 3)
	assert.Contains(t, files, "tirith_check.py")
	assert.Contains(t, files, "ssrf_pretool.py")
	assert.Contains(t, files, "secret_redact_posttool.py")

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
	assert.Len(t, files, 2)
	assert.NotContains(t, files, "ssrf_pretool.py")
}

func TestEmbeddedHooksNotEmpty(t *testing.T) {
	assert.NotEmpty(t, SSRFPreToolHook)
	assert.NotEmpty(t, SecretRedactPostToolHook)
	assert.NotEmpty(t, TirithCheckHook)
}
