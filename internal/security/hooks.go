package security

import (
	_ "embed"
	"encoding/json"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

//go:embed hooks/ssrf_pretool.py
var SSRFPreToolHook []byte

//go:embed hooks/secret_redact_posttool.py
var SecretRedactPostToolHook []byte

//go:embed hooks/tirith_check.py
var TirithCheckHook []byte

//go:embed hooks/unicode_posttool.py
var UnicodePostToolHook []byte

//go:embed hooks/context_suppress_posttool.py
var ContextSuppressPostToolHook []byte

//go:embed hooks/canary_pretool.py
var CanaryPreToolHook []byte

//go:embed hooks/canary_posttool.py
var CanaryPostToolHook []byte

//go:embed hooks/tool_allowlist_pretool.py
var ToolAllowlistPreToolHook []byte

// hookEntry represents a single hook command in Claude settings.
type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// hookMatcher groups a tool matcher with its hooks.
type hookMatcher struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// claudeSettings represents the .claude/settings.json structure.
type claudeSettings struct {
	Hooks map[string][]hookMatcher `json:"hooks"`
}

// SandboxHooksDir is the path where hook scripts are installed inside the
// sandbox. Must match sandbox.SandboxWorkspace + "/.claude/hooks".
const SandboxHooksDir = "/tmp/workspace/.claude/hooks"

// GenerateClaudeSettings produces a .claude/settings.json with security hooks
// configured according to the harness SecurityConfig. Returns the JSON bytes.
func GenerateClaudeSettings(h *harness.Harness) ([]byte, error) {
	settings := claudeSettings{
		Hooks: make(map[string][]hookMatcher),
	}

	var preToolMatchers []hookMatcher
	var postToolMatchers []hookMatcher

	sec := h.Security // may be nil — callers should check SecurityEnabled() first

	// Tirith PreToolUse hook (Bash commands).
	if tirithEnabled(sec) {
		preToolMatchers = append(preToolMatchers, hookMatcher{
			Matcher: "Bash",
			Hooks: []hookEntry{
				{Type: "command", Command: "python3 " + SandboxHooksDir + "/tirith_check.py"},
			},
		})
	}

	// SSRF PreToolUse hook (Bash + WebFetch).
	if ssrfPreToolEnabled(sec) {
		preToolMatchers = append(preToolMatchers, hookMatcher{
			Matcher: "Bash|WebFetch",
			Hooks: []hookEntry{
				{Type: "command", Command: "python3 " + SandboxHooksDir + "/ssrf_pretool.py"},
			},
		})
	}

	// Canary PreToolUse hook (all tools). Catches exfiltration of the
	// canary token via tool inputs before data leaves the sandbox.
	// Uses * to cover MCP tools (issue comments, PR bodies, etc.)
	// in addition to Bash and WebFetch.
	if canaryPreToolEnabled(sec) {
		preToolMatchers = append(preToolMatchers, hookMatcher{
			Matcher: "*",
			Hooks: []hookEntry{
				{Type: "command", Command: "python3 " + SandboxHooksDir + "/canary_pretool.py"},
			},
		})
	}

	// Tool allowlist PreToolUse hook (all tools). Disabled by default.
	if toolAllowlistPreToolEnabled(sec) {
		preToolMatchers = append(preToolMatchers, hookMatcher{
			Matcher: "*",
			Hooks: []hookEntry{
				{Type: "command", Command: "python3 " + SandboxHooksDir + "/tool_allowlist_pretool.py"},
			},
		})
	}

	// PostToolUse hooks for Bash|WebFetch|Read. Combined into a single matcher
	// so Claude Code chains them sequentially (separate matchers run in parallel
	// on the original result, which would cause modifications to conflict).
	// Order: context suppress (compacts verbose success output) → secret redact
	// → unicode scan. Suppressing first avoids scanning text we'd discard.
	var postToolHooks []hookEntry
	if contextSuppressPostToolEnabled(sec) {
		postToolHooks = append(postToolHooks, hookEntry{
			Type: "command", Command: "python3 " + SandboxHooksDir + "/context_suppress_posttool.py",
		})
	}
	if secretRedactPostToolEnabled(sec) {
		postToolHooks = append(postToolHooks, hookEntry{
			Type: "command", Command: "python3 " + SandboxHooksDir + "/secret_redact_posttool.py",
		})
	}
	if unicodePostToolEnabled(sec) {
		postToolHooks = append(postToolHooks, hookEntry{
			Type: "command", Command: "python3 " + SandboxHooksDir + "/unicode_posttool.py",
		})
	}
	if len(postToolHooks) > 0 {
		postToolMatchers = append(postToolMatchers, hookMatcher{
			Matcher: "Bash|WebFetch|Read",
			Hooks:   postToolHooks,
		})
	}

	// Canary PostToolUse hook (all tools). Separate matcher from the
	// Bash|WebFetch|Read chain because canary must catch leaks from any
	// tool including MCP tools.
	if canaryPostToolEnabled(sec) {
		postToolMatchers = append(postToolMatchers, hookMatcher{
			Matcher: "*",
			Hooks: []hookEntry{
				{Type: "command", Command: "python3 " + SandboxHooksDir + "/canary_posttool.py"},
			},
		})
	}

	if len(preToolMatchers) > 0 {
		settings.Hooks["PreToolUse"] = preToolMatchers
	}
	if len(postToolMatchers) > 0 {
		settings.Hooks["PostToolUse"] = postToolMatchers
	}

	return json.MarshalIndent(settings, "", "  ")
}

// HookFiles returns a map of filename -> content for all enabled hook scripts.
func HookFiles(h *harness.Harness) map[string][]byte {
	sec := h.Security
	files := make(map[string][]byte)

	if tirithEnabled(sec) {
		files["tirith_check.py"] = TirithCheckHook
	}
	if ssrfPreToolEnabled(sec) {
		files["ssrf_pretool.py"] = SSRFPreToolHook
	}
	if secretRedactPostToolEnabled(sec) {
		files["secret_redact_posttool.py"] = SecretRedactPostToolHook
	}
	if unicodePostToolEnabled(sec) {
		files["unicode_posttool.py"] = UnicodePostToolHook
	}
	if contextSuppressPostToolEnabled(sec) {
		files["context_suppress_posttool.py"] = ContextSuppressPostToolHook
	}
	if canaryPreToolEnabled(sec) {
		files["canary_pretool.py"] = CanaryPreToolHook
	}
	if canaryPostToolEnabled(sec) {
		files["canary_posttool.py"] = CanaryPostToolHook
	}
	if toolAllowlistPreToolEnabled(sec) {
		files["tool_allowlist_pretool.py"] = ToolAllowlistPreToolHook
	}

	return files
}

// boolDefault returns the value of a *bool, or the default if nil.
func boolDefault(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func tirithEnabled(sec *harness.SecurityConfig) bool {
	if sec == nil || sec.SandboxHooks == nil || sec.SandboxHooks.Tirith == nil {
		return true // default: enabled
	}
	return boolDefault(sec.SandboxHooks.Tirith.Enabled, true)
}

func ssrfPreToolEnabled(sec *harness.SecurityConfig) bool {
	if sec == nil || sec.SandboxHooks == nil {
		return true
	}
	return boolDefault(sec.SandboxHooks.SSRFPreTool, true)
}

func secretRedactPostToolEnabled(sec *harness.SecurityConfig) bool {
	if sec == nil || sec.SandboxHooks == nil {
		return true
	}
	return boolDefault(sec.SandboxHooks.SecretRedactPostTool, true)
}

func unicodePostToolEnabled(sec *harness.SecurityConfig) bool {
	if sec == nil || sec.SandboxHooks == nil {
		return true
	}
	return boolDefault(sec.SandboxHooks.UnicodePostTool, true)
}

func contextSuppressPostToolEnabled(sec *harness.SecurityConfig) bool {
	if sec == nil || sec.SandboxHooks == nil {
		return true
	}
	return boolDefault(sec.SandboxHooks.ContextSuppressPostTool, true)
}

func canaryPreToolEnabled(sec *harness.SecurityConfig) bool {
	if sec == nil || sec.SandboxHooks == nil {
		return true // default: enabled
	}
	return boolDefault(sec.SandboxHooks.CanaryPreTool, true)
}

func canaryPostToolEnabled(sec *harness.SecurityConfig) bool {
	if sec == nil || sec.SandboxHooks == nil {
		return true // default: enabled
	}
	return boolDefault(sec.SandboxHooks.CanaryPostTool, true)
}

func toolAllowlistPreToolEnabled(sec *harness.SecurityConfig) bool {
	if sec == nil || sec.SandboxHooks == nil {
		return false // default: disabled (opt-in)
	}
	if sec.SandboxHooks.ToolAllowlistPreTool == nil {
		return false
	}
	return boolDefault(sec.SandboxHooks.ToolAllowlistPreTool.Enabled, false)
}
