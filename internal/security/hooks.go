package security

import (
	_ "embed"
	"encoding/json"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
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
const SandboxHooksDir = sandbox.SandboxWorkspace + "/.claude/hooks"

// GenerateClaudeSettings produces a .claude/settings.json with security hooks
// configured according to hooks. Returns the JSON bytes.
func GenerateClaudeSettings(hooks ClaudeSandboxHooks) ([]byte, error) {
	settings := claudeSettings{
		Hooks: make(map[string][]hookMatcher),
	}

	var preToolMatchers []hookMatcher
	var postToolMatchers []hookMatcher

	// Tirith PreToolUse hook (Bash commands).
	if tirithEnabled(hooks) {
		preToolMatchers = append(preToolMatchers, hookMatcher{
			Matcher: "Bash",
			Hooks: []hookEntry{
				{Type: "command", Command: "python3 " + SandboxHooksDir + "/tirith_check.py"},
			},
		})
	}

	// SSRF PreToolUse hook (Bash + WebFetch).
	if ssrfPreToolEnabled(hooks) {
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
	if canaryPreToolEnabled(hooks) {
		preToolMatchers = append(preToolMatchers, hookMatcher{
			Matcher: "*",
			Hooks: []hookEntry{
				{Type: "command", Command: "python3 " + SandboxHooksDir + "/canary_pretool.py"},
			},
		})
	}

	// Tool allowlist PreToolUse hook (all tools). Disabled by default.
	if toolAllowlistPreToolEnabled(hooks) {
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
	// Order: context suppress (compacts verbose success output) → unicode normalize
	// → secret redact. Suppressing first avoids scanning text we'd discard.
	// Invariant: unicode normalization must run before secret redaction so
	// zero-width characters cannot break prefix regexes and reconstruct secrets.
	var postToolHooks []hookEntry
	if contextSuppressPostToolEnabled(hooks) {
		postToolHooks = append(postToolHooks, hookEntry{
			Type: "command", Command: "python3 " + SandboxHooksDir + "/context_suppress_posttool.py",
		})
	}
	if unicodePostToolEnabled(hooks) {
		postToolHooks = append(postToolHooks, hookEntry{
			Type: "command", Command: "python3 " + SandboxHooksDir + "/unicode_posttool.py",
		})
	}
	if secretRedactPostToolEnabled(hooks) {
		postToolHooks = append(postToolHooks, hookEntry{
			Type: "command", Command: "python3 " + SandboxHooksDir + "/secret_redact_posttool.py",
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
	if canaryPostToolEnabled(hooks) {
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
func HookFiles(hooks ClaudeSandboxHooks) map[string][]byte {
	files := make(map[string][]byte)

	if tirithEnabled(hooks) {
		files["tirith_check.py"] = TirithCheckHook
	}
	if ssrfPreToolEnabled(hooks) {
		files["ssrf_pretool.py"] = SSRFPreToolHook
	}
	if secretRedactPostToolEnabled(hooks) {
		files["secret_redact_posttool.py"] = SecretRedactPostToolHook
	}
	if unicodePostToolEnabled(hooks) {
		files["unicode_posttool.py"] = UnicodePostToolHook
	}
	if contextSuppressPostToolEnabled(hooks) {
		files["context_suppress_posttool.py"] = ContextSuppressPostToolHook
	}
	if canaryPreToolEnabled(hooks) {
		files["canary_pretool.py"] = CanaryPreToolHook
	}
	if canaryPostToolEnabled(hooks) {
		files["canary_posttool.py"] = CanaryPostToolHook
	}
	if toolAllowlistPreToolEnabled(hooks) {
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

func tirithEnabled(hooks ClaudeSandboxHooks) bool {
	sh := hooks.sandboxHooks()
	if sh == nil || sh.Tirith == nil {
		return true // default: enabled
	}
	return boolDefault(sh.Tirith.Enabled, true)
}

func ssrfPreToolEnabled(hooks ClaudeSandboxHooks) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true
	}
	return boolDefault(sh.SSRFPreTool, true)
}

func secretRedactPostToolEnabled(hooks ClaudeSandboxHooks) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true
	}
	return boolDefault(sh.SecretRedactPostTool, true)
}

func unicodePostToolEnabled(hooks ClaudeSandboxHooks) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true
	}
	return boolDefault(sh.UnicodePostTool, true)
}

func contextSuppressPostToolEnabled(hooks ClaudeSandboxHooks) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true
	}
	return boolDefault(sh.ContextSuppressPostTool, true)
}

func canaryPreToolEnabled(hooks ClaudeSandboxHooks) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true // default: enabled
	}
	return boolDefault(sh.CanaryPreTool, true)
}

func canaryPostToolEnabled(hooks ClaudeSandboxHooks) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true // default: enabled
	}
	return boolDefault(sh.CanaryPostTool, true)
}

func toolAllowlistPreToolEnabled(hooks ClaudeSandboxHooks) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return false // default: disabled (opt-in)
	}
	if sh.ToolAllowlistPreTool == nil {
		return false
	}
	return boolDefault(sh.ToolAllowlistPreTool.Enabled, false)
}
