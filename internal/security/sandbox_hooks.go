package security

import (
	"github.com/fullsend-ai/fullsend/internal/harness"
)

// SandboxHookConfig configures the runtime-neutral sandbox tool hooks
// (PreToolUse/PostToolUse scripts in hooks/). It is derived from the harness
// `security.sandbox_hooks` block and consumed by any agent runtime that
// installs the hook scripts — Claude Code wires them through settings.json;
// other runtimes call the same scripts from their own hook/plugin/extension
// mechanism. A nil internal hook config uses the same defaults as an unset
// harness security block.
type SandboxHookConfig struct {
	hooks *harness.SandboxHooks
}

// SandboxHookConfigFromHarness extracts sandbox hook settings from a harness.
func SandboxHookConfigFromHarness(h *harness.Harness) SandboxHookConfig {
	if h == nil || h.Security == nil || h.Security.SandboxHooks == nil {
		return SandboxHookConfig{}
	}
	return SandboxHookConfig{hooks: h.Security.SandboxHooks}
}

func (c SandboxHookConfig) sandboxHooks() *harness.SandboxHooks {
	return c.hooks
}

// TirithFailOn returns the Tirith severity threshold env value, or empty when unset.
func (c SandboxHookConfig) TirithFailOn() string {
	sh := c.sandboxHooks()
	if sh == nil || sh.Tirith == nil {
		return ""
	}
	return sh.Tirith.FailOn
}

// TirithRequired reports whether Tirith Bash scanning should be required in the sandbox.
func (c SandboxHookConfig) TirithRequired() bool {
	sh := c.sandboxHooks()
	if sh == nil || sh.Tirith == nil {
		return true
	}
	return boolDefault(sh.Tirith.Enabled, true)
}

// SSRFEgressAllowlist returns the comma-separated egress allowlist, or
// empty when unset.  The allowlist tells the SSRF hook which hosts may
// bypass the DNS-failure fail-closed check (the L7 egress proxy will
// resolve and enforce the network policy for those hosts).
func (c SandboxHookConfig) SSRFEgressAllowlist() string {
	sh := c.sandboxHooks()
	if sh == nil {
		return ""
	}
	return sh.SSRFEgressAllowlist
}
