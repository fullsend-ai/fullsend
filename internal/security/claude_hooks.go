package security

import (
	"github.com/fullsend-ai/fullsend/internal/harness"
)

// ClaudeSandboxHooks configures Claude Code PreToolUse/PostToolUse security hooks.
// A nil internal hook config uses the same defaults as an unset harness security block.
type ClaudeSandboxHooks struct {
	hooks *harness.SandboxHooks
}

// ClaudeSandboxHooksFromHarness extracts sandbox hook settings from a harness.
func ClaudeSandboxHooksFromHarness(h *harness.Harness) ClaudeSandboxHooks {
	if h == nil || h.Security == nil || h.Security.SandboxHooks == nil {
		return ClaudeSandboxHooks{}
	}
	return ClaudeSandboxHooks{hooks: h.Security.SandboxHooks}
}

func (c ClaudeSandboxHooks) sandboxHooks() *harness.SandboxHooks {
	return c.hooks
}

// TirithFailOn returns the Tirith severity threshold env value, or empty when unset.
func (c ClaudeSandboxHooks) TirithFailOn() string {
	sh := c.sandboxHooks()
	if sh == nil || sh.Tirith == nil {
		return ""
	}
	return sh.Tirith.FailOn
}

// TirithRequired reports whether Tirith Bash scanning should be required in the sandbox.
func (c ClaudeSandboxHooks) TirithRequired() bool {
	sh := c.sandboxHooks()
	if sh == nil || sh.Tirith == nil {
		return true
	}
	return boolDefault(sh.Tirith.Enabled, true)
}
