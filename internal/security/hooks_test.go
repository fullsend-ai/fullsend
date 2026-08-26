package security

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// hookLibraryFile reports scripts shipped as imports for other hooks, not
// invoked directly by HookPlan or settings.json.
func hookLibraryFile(name string) bool {
	switch name {
	case "hook_io.py",
		"context_suppress_posttool.py",
		"unicode_posttool.py",
		"secret_redact_posttool.py",
		"canary_posttool.py":
		return true
	default:
		return false
	}
}

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
	assert.Len(t, postTools, 1) // single chain matcher covers sanitizers + canary

	// Failed calls: canary detection only, same driver.
	assert.Contains(t, hooks, "PostToolUseFailure")
	failed := hooks["PostToolUseFailure"].([]any)
	require.Len(t, failed, 1)
	failedEntry := failed[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	assert.Contains(t, failedEntry["command"], "posttool_chain.py")

	// Every hook carries an explicit timeout: Claude Code's 600 s default
	// fails open and would stall an iteration on a wedged script.
	for _, phase := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"} {
		for _, m := range hooks[phase].([]any) {
			for _, e := range m.(map[string]any)["hooks"].([]any) {
				assert.EqualValues(t, HookTimeoutSeconds, e.(map[string]any)["timeout"], phase)
			}
		}
	}

	// Verify sanitization is a single chained driver (Claude Code runs hooks
	// in parallel; ordering is enforced inside posttool_chain.py).
	matcher := postTools[0].(map[string]any)
	assert.Equal(t, "*", matcher["matcher"])
	chainedHooks := matcher["hooks"].([]any)
	assert.Len(t, chainedHooks, 1)
	assert.Contains(t, chainedHooks[0].(map[string]any)["command"], "posttool_chain.py")
	assert.NotContains(t, string(data), "canary_posttool.py")
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
	assert.Len(t, files, 9) // 7 default + hook_io + posttool_chain
	assert.Contains(t, files, "tirith_check.py")
	assert.Contains(t, files, "ssrf_pretool.py")
	assert.Contains(t, files, "secret_redact_posttool.py")
	assert.Contains(t, files, "unicode_posttool.py")
	assert.Contains(t, files, "context_suppress_posttool.py")
	assert.Contains(t, files, "canary_pretool.py")
	assert.Contains(t, files, "canary_posttool.py")
	assert.Contains(t, files, "hook_io.py")
	assert.Contains(t, files, "posttool_chain.py")
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
	assert.Len(t, files, 8) // both canary hooks still enabled; chain + hook_io remain
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
	assert.Len(t, files, 8) // both canary hooks still enabled; chain + hook_io remain
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
	assert.NotEmpty(t, HookIO)
	assert.NotEmpty(t, PostToolChainHook)
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
	assert.Len(t, postTools, 1) // chain matcher (canary is an in-process stage)

	// Unicode disabled: chain still runs (suppress + redact as siblings).
	matcher := postTools[0].(map[string]any)
	chainedHooks := matcher["hooks"].([]any)
	assert.Len(t, chainedHooks, 1)
	assert.Contains(t, chainedHooks[0].(map[string]any)["command"], "posttool_chain.py")
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
	assert.Len(t, postTools, 1) // chain matcher (canary is an in-process stage)

	// Secret redact disabled: chain still runs (suppress + unicode as siblings).
	matcher := postTools[0].(map[string]any)
	chainedHooks := matcher["hooks"].([]any)
	assert.Len(t, chainedHooks, 1)
	assert.Contains(t, chainedHooks[0].(map[string]any)["command"], "posttool_chain.py")
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
	assert.Len(t, postTools, 1) // chain matcher (canary is an in-process stage)

	// Context suppress disabled: chain still runs (unicode + redact as siblings).
	matcher := postTools[0].(map[string]any)
	chainedHooks := matcher["hooks"].([]any)
	assert.Len(t, chainedHooks, 1)
	assert.Contains(t, chainedHooks[0].(map[string]any)["command"], "posttool_chain.py")
}

func TestGenerateHooksConfig_PostToolSanitizeHookOrder(t *testing.T) {
	h := &harness.Harness{Agent: "test.md"}
	data, err := GenerateHooksConfig(SandboxHookConfigFromHarness(h))
	require.NoError(t, err)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	postTools := settings["hooks"].(map[string]any)["PostToolUse"].([]any)
	matcher := postTools[0].(map[string]any)
	require.Equal(t, "*", matcher["matcher"])

	chainedHooks := matcher["hooks"].([]any)
	require.Len(t, chainedHooks, 1)
	assert.Contains(t, chainedHooks[0].(map[string]any)["command"].(string), "posttool_chain.py")
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
	assert.Equal(t, "*", matcher["matcher"])

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

	// canary_pretool disabled: PostToolUse chain is unchanged
	postTools := hooks["PostToolUse"].([]any)
	assert.Len(t, postTools, 1) // chain still scheduled; canary is an in-process stage
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
	assert.Len(t, files, 10) // 9 default + tool_allowlist
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
	assert.Len(t, files, 8) // both canary hooks still enabled; chain + hook_io remain
	assert.NotContains(t, files, "context_suppress_posttool.py")
}

func TestHookPlan_DefaultsAndOrder(t *testing.T) {
	plan := HookPlan(SandboxHookConfigFromHarness(&harness.Harness{}))

	var pre, post, failed []HookGroup
	for _, g := range plan {
		switch g.Phase {
		case HookPhasePreToolUse:
			pre = append(pre, g)
		case HookPhasePostToolUse:
			post = append(post, g)
		case HookPhasePostToolUseFailure:
			failed = append(failed, g)
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

	// Post-tool: one chained driver on * (canary is an in-process stage).
	// Individual sanitizers and canary_posttool.py are libraries.
	require.Len(t, post, 1)
	assert.Equal(t, []string{AllTools}, post[0].Tools)
	assert.Equal(t, []string{"posttool_chain.py"}, post[0].Scripts)

	// Failed tool calls: the same driver runs canary detection on the
	// PostToolUseFailure error text (no rewrite is possible there).
	require.Len(t, failed, 1)
	assert.Equal(t, []string{AllTools}, failed[0].Tools)
	assert.Equal(t, []string{"posttool_chain.py"}, failed[0].Scripts)

	// Every script the plan references is shipped by HookFiles. Library
	// modules (hook_io + sanitizer stages) are shipped but not scheduled.
	files := HookFiles(SandboxHookConfigFromHarness(&harness.Harness{}))
	seen := map[string]bool{}
	for _, g := range plan {
		for _, s := range g.Scripts {
			assert.Contains(t, files, s)
			seen[s] = true
		}
	}
	for name := range files {
		if hookLibraryFile(name) {
			continue
		}
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
		if hookLibraryFile(name) {
			assert.Equal(t, 0, seen[name], "library script %s should not be scheduled", name)
			continue
		}
		expected := 1
		if name == "posttool_chain.py" {
			expected = 2 // PostToolUse rewrite + PostToolUseFailure canary detection
		}
		assert.Equal(t, expected, seen[name], "script %s scheduled %d times", name, seen[name])
	}
	assert.Contains(t, seen, "tool_allowlist_pretool.py")
	assert.Contains(t, seen, "posttool_chain.py")

	settings, err := GenerateHooksConfig(cfg)
	require.NoError(t, err)
	for name := range files {
		if hookLibraryFile(name) {
			assert.NotContains(t, string(settings), SandboxHooksDir+"/"+name)
			continue
		}
		assert.Contains(t, string(settings), SandboxHooksDir+"/"+name)
	}
}

func TestHookPlan_CanaryOnlyUsesChain(t *testing.T) {
	off := false
	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		SecretRedactPostTool:    &off,
		UnicodePostTool:         &off,
		ContextSuppressPostTool: &off,
	}}}
	cfg := SandboxHookConfigFromHarness(h)
	plan := HookPlan(cfg)
	var post []HookGroup
	for _, g := range plan {
		if g.Phase == HookPhasePostToolUse {
			post = append(post, g)
		}
	}
	require.Len(t, post, 1)
	assert.Equal(t, []string{AllTools}, post[0].Tools)
	assert.Equal(t, []string{"posttool_chain.py"}, post[0].Scripts)

	// The failure phase is scheduled whenever either half of the chain has
	// something to do there — canary halt or detection-only warnings.
	assert.Equal(t, 1, countPhase(plan, HookPhasePostToolUseFailure))

	files := HookFiles(cfg)
	assert.Contains(t, files, "posttool_chain.py")
	assert.Contains(t, files, "canary_posttool.py")
	assert.Contains(t, files, "hook_io.py")
	assert.NotContains(t, files, "secret_redact_posttool.py")
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

func TestSandboxHookConfig_SSRFEgressAllowlist(t *testing.T) {
	// Unset harness → empty allowlist.
	cfg := SandboxHookConfigFromHarness(nil)
	assert.Empty(t, cfg.SSRFEgressAllowlist())

	// Harness with no allowlist → empty.
	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{}}}
	cfg = SandboxHookConfigFromHarness(h)
	assert.Empty(t, cfg.SSRFEgressAllowlist())

	// Harness with allowlist → returned.
	h = &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		SSRFEgressAllowlist: "gitlab.internal:443,other.host:8443",
	}}}
	cfg = SandboxHookConfigFromHarness(h)
	assert.Equal(t, "gitlab.internal:443,other.host:8443", cfg.SSRFEgressAllowlist())
}

func countPhase(plan []HookGroup, phase HookPhase) int {
	n := 0
	for _, g := range plan {
		if g.Phase == phase {
			n++
		}
	}
	return n
}

func TestHookPlan_FailurePhaseFollowsSanitizersToo(t *testing.T) {
	off := false

	// Canary off, sanitizers on: the detection-only warnings still need the
	// failure phase scheduled.
	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		CanaryPostTool: &off,
	}}}
	plan := HookPlan(SandboxHookConfigFromHarness(h))
	assert.Equal(t, 1, countPhase(plan, HookPhasePostToolUseFailure), "sanitizers still run there")

	// Suppression alone cannot run there (it rewrites output), so a
	// suppress-only configuration schedules no failure group.
	h = &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		CanaryPostTool:       &off,
		SecretRedactPostTool: &off,
		UnicodePostTool:      &off,
	}}}
	plan = HookPlan(SandboxHookConfigFromHarness(h))
	assert.Equal(t, 0, countPhase(plan, HookPhasePostToolUseFailure))
	assert.Equal(t, 1, countPhase(plan, HookPhasePostToolUse), "suppression still runs on success")

	// Everything the chain does post-tool is off: no failure group at all.
	h = &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		CanaryPostTool:          &off,
		SecretRedactPostTool:    &off,
		UnicodePostTool:         &off,
		ContextSuppressPostTool: &off,
	}}}
	plan = HookPlan(SandboxHookConfigFromHarness(h))
	assert.Equal(t, 0, countPhase(plan, HookPhasePostToolUseFailure))
	assert.Equal(t, 0, countPhase(plan, HookPhasePostToolUse))
}
