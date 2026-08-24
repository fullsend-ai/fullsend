package security

import (
	_ "embed"
	"encoding/json"
	"strings"

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

// HookIO is the shared PostToolUse protocol library imported by the chain
// and by individual sanitizer/canary scripts. It is not itself a hook.
//
//go:embed hooks/hook_io.py
var HookIO []byte

//go:embed hooks/posttool_chain.py
var PostToolChainHook []byte

// hookEntry represents a single hook command in Claude settings.
type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	// Timeout bounds one hook run in seconds. Claude Code's default is 600 s
	// and a hook that exceeds it fails open, so a wedged sanitizer would stall
	// an iteration for ten minutes; the scripts finish in well under a second.
	Timeout int `json:"timeout,omitempty"`
}

// HookTimeoutSeconds is the per-run timeout written for every sandbox hook.
const HookTimeoutSeconds = 30

// hookMatcher groups a tool matcher with its hooks.
type hookMatcher struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// hooksConfig represents the hooks.json structure for Claude Code hook wiring.
type hooksConfig struct {
	Hooks map[string][]hookMatcher `json:"hooks"`
}

// SandboxHooksDir is the directory where hook scripts are installed inside
// the sandbox. Co-located with SandboxHooksSettings under the runner-owned
// config directory so they are outside the agent-writable workspace tree.
const SandboxHooksDir = sandbox.SandboxClaudeConfig + "/hooks"

// SandboxHooksSettings is the path where the hook wiring hooks.json is
// written inside the sandbox. buildRunCommand passes this via --settings so
// Claude Code loads the hooks regardless of its working directory.
const SandboxHooksSettings = sandbox.SandboxClaudeConfig + "/hooks.json"

// HookPhase identifies when a sandbox hook group runs relative to a tool call.
// The names match Claude Code's settings.json event names; other runtimes map
// them onto their own hook/plugin/extension events (e.g. OpenCode
// tool.execute.before/after, pi tool_call/tool_result).
type HookPhase string

const (
	// HookPhasePreToolUse runs before the tool executes and may block it.
	HookPhasePreToolUse HookPhase = "PreToolUse"
	// HookPhasePostToolUse runs after the tool executes and may rewrite its result.
	HookPhasePostToolUse HookPhase = "PostToolUse"
	// HookPhasePostToolUseFailure runs after a tool call fails. Claude Code
	// delivers the error text but allows no rewrite, so this phase detects
	// rather than sanitizes: a canary halts the session, and credential-shaped
	// or control content is logged and returned to the agent as an
	// additionalContext warning. Runtimes whose post-tool event already covers
	// failed calls (pi) map it onto nothing.
	HookPhasePostToolUseFailure HookPhase = "PostToolUseFailure"
)

// HookGroup is one ordered chain of hook scripts bound to a set of tools.
// Tools are Claude Code tool names ("Bash", "Read", "WebFetch", ...); "*"
// means every tool. Scripts are filenames from HookFiles, run sequentially in
// the listed order — ordering is load-bearing for PostToolUse chains (see
// HookPlan). Runtimes that use different tool names translate before matching.
type HookGroup struct {
	Phase   HookPhase
	Tools   []string
	Scripts []string
}

// AllTools is the wildcard tool matcher.
const AllTools = "*"

// HookPlan returns the runtime-neutral wiring for the enabled sandbox hooks:
// which scripts run in which phase, for which tools, in what order. It is the
// single source of truth consumed by GenerateHooksConfig (Claude Code) and
// by any other runtime's hook adapter, so the two cannot diverge.
func HookPlan(hooks SandboxHookConfig) []HookGroup {
	var plan []HookGroup

	// Tirith PreToolUse hook (Bash commands).
	if tirithEnabled(hooks) {
		plan = append(plan, HookGroup{
			Phase: HookPhasePreToolUse, Tools: []string{"Bash"},
			Scripts: []string{"tirith_check.py"},
		})
	}

	// SSRF PreToolUse hook (Bash + WebFetch).
	if ssrfPreToolEnabled(hooks) {
		plan = append(plan, HookGroup{
			Phase: HookPhasePreToolUse, Tools: []string{"Bash", "WebFetch"},
			Scripts: []string{"ssrf_pretool.py"},
		})
	}

	// Canary PreToolUse hook (all tools). Catches exfiltration of the
	// canary token via tool inputs before data leaves the sandbox.
	// Uses * to cover MCP tools (issue comments, PR bodies, etc.)
	// in addition to Bash and WebFetch.
	if canaryPreToolEnabled(hooks) {
		plan = append(plan, HookGroup{
			Phase: HookPhasePreToolUse, Tools: []string{AllTools},
			Scripts: []string{"canary_pretool.py"},
		})
	}

	// Tool allowlist PreToolUse hook (all tools). Disabled by default.
	if toolAllowlistPreToolEnabled(hooks) {
		plan = append(plan, HookGroup{
			Phase: HookPhasePreToolUse, Tools: []string{AllTools},
			Scripts: []string{"tool_allowlist_pretool.py"},
		})
	}

	// PostToolUse driver for every tool. Claude Code runs matching hooks in
	// parallel and does not merge two updatedToolOutput rewrites, so the stages
	// (unicode → canary → suppress → redact, in that order) must share one
	// process (fullsend#6357). The
	// driver skips sibling scripts that HookFiles omitted. Adapters that
	// invoke HookPlan should call this one script rather than the stages.
	if postToolChainEnabled(hooks) {
		plan = append(plan, HookGroup{
			Phase: HookPhasePostToolUse, Tools: []string{AllTools},
			Scripts: []string{"posttool_chain.py"},
		})
	}

	// Failed tool calls never reach PostToolUse under Claude Code; the same
	// driver runs on PostToolUseFailure so a leak in a failing command's output
	// still halts the session (canary) and credential-shaped or control
	// content is still logged and flagged (sanitizers, detection-only — the
	// event allows no output rewrite, which the runtimes matrix records).
	// Scheduled only when something actually runs there: context suppression
	// cannot (it rewrites output, which this event does not allow).
	if failurePhaseEnabled(hooks) {
		plan = append(plan, HookGroup{
			Phase: HookPhasePostToolUseFailure, Tools: []string{AllTools},
			Scripts: []string{"posttool_chain.py"},
		})
	}

	return plan
}

// GenerateHooksConfig produces the hooks.json Claude Code hook wiring,
// loaded via --settings in buildRunCommand. Returns the JSON bytes. The
// wiring comes from HookPlan; this function only renders it in Claude Code's
// settings format.
func GenerateHooksConfig(hooks SandboxHookConfig) ([]byte, error) {
	cfg := hooksConfig{
		Hooks: make(map[string][]hookMatcher),
	}

	for _, g := range HookPlan(hooks) {
		entries := make([]hookEntry, 0, len(g.Scripts))
		for _, script := range g.Scripts {
			entries = append(entries, hookEntry{
				Type: "command", Command: "python3 " + SandboxHooksDir + "/" + script,
				Timeout: HookTimeoutSeconds,
			})
		}
		cfg.Hooks[string(g.Phase)] = append(cfg.Hooks[string(g.Phase)], hookMatcher{
			Matcher: strings.Join(g.Tools, "|"),
			Hooks:   entries,
		})
	}

	return json.MarshalIndent(cfg, "", "  ")
}

// HookFiles returns a map of filename -> content for all enabled hook scripts.
func HookFiles(hooks SandboxHookConfig) map[string][]byte {
	files := make(map[string][]byte)

	if tirithEnabled(hooks) {
		files["tirith_check.py"] = TirithCheckHook
	}
	if ssrfPreToolEnabled(hooks) {
		files["ssrf_pretool.py"] = SSRFPreToolHook
	}
	if postToolChainEnabled(hooks) {
		files["posttool_chain.py"] = PostToolChainHook
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
	if postToolChainEnabled(hooks) {
		files["hook_io.py"] = HookIO
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

func tirithEnabled(hooks SandboxHookConfig) bool {
	sh := hooks.sandboxHooks()
	if sh == nil || sh.Tirith == nil {
		return true // default: enabled
	}
	return boolDefault(sh.Tirith.Enabled, true)
}

func ssrfPreToolEnabled(hooks SandboxHookConfig) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true
	}
	return boolDefault(sh.SSRFPreTool, true)
}

// failurePhaseEnabled reports whether anything the chain does on a failed
// tool call is enabled. Context suppression is deliberately excluded: it
// rewrites output, which PostToolUseFailure does not allow, so a
// suppress-only configuration would schedule a hook that does nothing.
func failurePhaseEnabled(hooks SandboxHookConfig) bool {
	return canaryPostToolEnabled(hooks) ||
		secretRedactPostToolEnabled(hooks) ||
		unicodePostToolEnabled(hooks)
}

func postToolSanitizeEnabled(hooks SandboxHookConfig) bool {
	return contextSuppressPostToolEnabled(hooks) ||
		unicodePostToolEnabled(hooks) ||
		secretRedactPostToolEnabled(hooks)
}

func postToolChainEnabled(hooks SandboxHookConfig) bool {
	return postToolSanitizeEnabled(hooks) || canaryPostToolEnabled(hooks)
}

func secretRedactPostToolEnabled(hooks SandboxHookConfig) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true
	}
	return boolDefault(sh.SecretRedactPostTool, true)
}

func unicodePostToolEnabled(hooks SandboxHookConfig) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true
	}
	return boolDefault(sh.UnicodePostTool, true)
}

func contextSuppressPostToolEnabled(hooks SandboxHookConfig) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true
	}
	return boolDefault(sh.ContextSuppressPostTool, true)
}

func canaryPreToolEnabled(hooks SandboxHookConfig) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true // default: enabled
	}
	return boolDefault(sh.CanaryPreTool, true)
}

func canaryPostToolEnabled(hooks SandboxHookConfig) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return true // default: enabled
	}
	return boolDefault(sh.CanaryPostTool, true)
}

func toolAllowlistPreToolEnabled(hooks SandboxHookConfig) bool {
	sh := hooks.sandboxHooks()
	if sh == nil {
		return false // default: disabled (opt-in)
	}
	if sh.ToolAllowlistPreTool == nil {
		return false
	}
	return boolDefault(sh.ToolAllowlistPreTool.Enabled, false)
}
