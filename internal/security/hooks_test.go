package security

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

func TestGenerateHooksConfig_AllDefaults(t *testing.T) {
	h := &harness.Harness{Agent: "test.md"}
	data, err := GenerateHooksConfig(SandboxHookConfigFromHarness(h))
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
	assert.Len(t, chainedHooks, 3) // context_suppress → unicode → secret_redact

	// Verify canary hook has its own * matcher.
	canaryMatcher := postTools[1].(map[string]any)
	assert.Equal(t, "*", canaryMatcher["matcher"])
	canaryHooks := canaryMatcher["hooks"].([]any)
	assert.Len(t, canaryHooks, 1)
}

func TestGenerateHooksConfig_TirithDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				Tirith: &harness.TirithConfig{Enabled: &disabled},
			},
		},
	}
	data, err := GenerateHooksConfig(SandboxHookConfigFromHarness(h))
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	preTools := hooks["PreToolUse"].([]any)
	assert.Len(t, preTools, 2) // ssrf + canary_pretool
}

func TestGenerateHooksConfig_AllHooksDisabled(t *testing.T) {
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
	data, err := GenerateHooksConfig(SandboxHookConfigFromHarness(h))
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	assert.NotContains(t, hooks, "PreToolUse")
	assert.NotContains(t, hooks, "PostToolUse")
}

func TestHookFiles_AllDefaults(t *testing.T) {
	h := &harness.Harness{Agent: "test.md"}
	files := HookFiles(SandboxHookConfigFromHarness(h))
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
	files := HookFiles(SandboxHookConfigFromHarness(h))
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
	files := HookFiles(SandboxHookConfigFromHarness(h))
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

func TestGenerateHooksConfig_UnicodeDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				UnicodePostTool: &disabled,
			},
		},
	}
	data, err := GenerateHooksConfig(SandboxHookConfigFromHarness(h))
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

func TestGenerateHooksConfig_SecretRedactDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				SecretRedactPostTool: &disabled,
			},
		},
	}
	data, err := GenerateHooksConfig(SandboxHookConfigFromHarness(h))
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

func TestGenerateHooksConfig_ContextSuppressDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				ContextSuppressPostTool: &disabled,
			},
		},
	}
	data, err := GenerateHooksConfig(SandboxHookConfigFromHarness(h))
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	hooks := settings["hooks"].(map[string]any)
	postTools := hooks["PostToolUse"].([]any)
	assert.Len(t, postTools, 2) // chain matcher + canary matcher

	// With context_suppress disabled: unicode + secret_redact in the chain.
	matcher := postTools[0].(map[string]any)
	chainedHooks := matcher["hooks"].([]any)
	assert.Len(t, chainedHooks, 2) // unicode + secret_redact
}

func TestGenerateHooksConfig_PostToolSanitizeHookOrder(t *testing.T) {
	h := &harness.Harness{Agent: "test.md"}
	data, err := GenerateHooksConfig(SandboxHookConfigFromHarness(h))
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	postTools := settings["hooks"].(map[string]any)["PostToolUse"].([]any)
	matcher := postTools[0].(map[string]any)
	require.Equal(t, "Bash|WebFetch|Read", matcher["matcher"])

	chainedHooks := matcher["hooks"].([]any)
	commands := make([]string, len(chainedHooks))
	for i, h := range chainedHooks {
		commands[i] = h.(map[string]any)["command"].(string)
	}

	hookIndex := func(substr string) int {
		for i, cmd := range commands {
			if strings.Contains(cmd, substr) {
				return i
			}
		}
		t.Fatalf("hook containing %q not found in %v", substr, commands)
		return -1
	}

	suppressIdx := hookIndex("context_suppress_posttool.py")
	unicodeIdx := hookIndex("unicode_posttool.py")
	redactIdx := hookIndex("secret_redact_posttool.py")

	assert.Less(t, suppressIdx, unicodeIdx, "context_suppress must run before unicode")
	assert.Less(t, unicodeIdx, redactIdx, "unicode must run before secret_redact")
}

func TestGenerateHooksConfig_CanaryPostToolDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				CanaryPostTool: &disabled,
			},
		},
	}
	data, err := GenerateHooksConfig(SandboxHookConfigFromHarness(h))
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

func TestGenerateHooksConfig_CanaryPreToolDisabled(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				CanaryPreTool: &disabled,
			},
		},
	}
	data, err := GenerateHooksConfig(SandboxHookConfigFromHarness(h))
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

func TestGenerateHooksConfig_ToolAllowlistEnabled(t *testing.T) {
	enabled := true
	h := &harness.Harness{
		Agent: "test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				ToolAllowlistPreTool: &harness.ToolAllowlistConfig{Enabled: &enabled},
			},
		},
	}
	data, err := GenerateHooksConfig(SandboxHookConfigFromHarness(h))
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
	files := HookFiles(SandboxHookConfigFromHarness(h))
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
	files := HookFiles(SandboxHookConfigFromHarness(h))
	assert.Len(t, files, 6) // both canary hooks still enabled
	assert.NotContains(t, files, "context_suppress_posttool.py")
}

func TestHookPlan_DefaultsAndOrder(t *testing.T) {
	plan := HookPlan(SandboxHookConfigFromHarness(&harness.Harness{}))

	var pre, post []HookGroup
	for _, g := range plan {
		switch g.Phase {
		case HookPhasePreToolUse:
			pre = append(pre, g)
		case HookPhasePostToolUse:
			post = append(post, g)
		default:
			t.Fatalf("unexpected phase %q", g.Phase)
		}
	}

	// Defaults: tirith, ssrf, canary pre-tool on; tool allowlist off.
	require.Len(t, pre, 3)
	assert.Equal(t, []string{"Bash"}, pre[0].Tools)
	assert.Equal(t, []string{"tirith_check.py"}, pre[0].Scripts)
	assert.Equal(t, []string{"Bash", "WebFetch"}, pre[1].Tools)
	assert.Equal(t, []string{"ssrf_pretool.py"}, pre[1].Scripts)
	assert.Equal(t, []string{AllTools}, pre[2].Tools)
	assert.Equal(t, []string{"canary_pretool.py"}, pre[2].Scripts)

	// Post-tool chain order is an invariant: suppress → unicode → redact.
	require.Len(t, post, 2)
	assert.Equal(t, []string{"Bash", "WebFetch", "Read"}, post[0].Tools)
	assert.Equal(t, []string{
		"context_suppress_posttool.py", "unicode_posttool.py", "secret_redact_posttool.py",
	}, post[0].Scripts)
	assert.Equal(t, []string{AllTools}, post[1].Tools)
	assert.Equal(t, []string{"canary_posttool.py"}, post[1].Scripts)

	// Every script the plan references is shipped by HookFiles, and vice versa.
	files := HookFiles(SandboxHookConfigFromHarness(&harness.Harness{}))
	seen := map[string]bool{}
	for _, g := range plan {
		for _, s := range g.Scripts {
			assert.Contains(t, files, s)
			seen[s] = true
		}
	}
	for name := range files {
		assert.True(t, seen[name], "HookFiles ships %s but HookPlan never runs it", name)
	}
}

func TestHookPlan_CoversHookFiles_AllEnabled(t *testing.T) {
	on := true
	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		ToolAllowlistPreTool: &harness.ToolAllowlistConfig{Enabled: &on},
	}}}
	cfg := SandboxHookConfigFromHarness(h)
	plan := HookPlan(cfg)
	files := HookFiles(cfg)

	// With the opt-in allowlist enabled, every shipped script must be scheduled
	// exactly once and every scheduled script must be shipped — the "cannot
	// diverge" invariant between HookFiles, HookPlan and GenerateHooksConfig.
	seen := map[string]int{}
	for _, g := range plan {
		for _, s := range g.Scripts {
			assert.Contains(t, files, s)
			seen[s]++
		}
	}
	for name := range files {
		assert.Equal(t, 1, seen[name], "script %s scheduled %d times", name, seen[name])
	}
	assert.Contains(t, seen, "tool_allowlist_pretool.py")

	settings, err := GenerateHooksConfig(cfg)
	require.NoError(t, err)
	for name := range files {
		assert.Contains(t, string(settings), SandboxHooksDir+"/"+name)
	}
}

func TestHookPlan_AllDisabled(t *testing.T) {
	off := false
	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		Tirith:                  &harness.TirithConfig{Enabled: &off},
		SSRFPreTool:             &off,
		CanaryPreTool:           &off,
		CanaryPostTool:          &off,
		SecretRedactPostTool:    &off,
		UnicodePostTool:         &off,
		ContextSuppressPostTool: &off,
		ToolAllowlistPreTool:    &harness.ToolAllowlistConfig{Enabled: &off},
	}}}
	assert.Empty(t, HookPlan(SandboxHookConfigFromHarness(h)))
	assert.Empty(t, HookFiles(SandboxHookConfigFromHarness(h)))
}

func TestSandboxHookConfig_Tirith(t *testing.T) {
	// Unset harness → Tirith required, no fail-on override.
	cfg := SandboxHookConfigFromHarness(nil)
	assert.True(t, cfg.TirithRequired())
	assert.Empty(t, cfg.TirithFailOn())

	off := false
	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		Tirith: &harness.TirithConfig{Enabled: &off, FailOn: "medium"},
	}}}
	cfg = SandboxHookConfigFromHarness(h)
	assert.False(t, cfg.TirithRequired())
	assert.Equal(t, "medium", cfg.TirithFailOn())
}
