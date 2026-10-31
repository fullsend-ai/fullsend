package cli

import "fmt"

// OpenCode's old security-disabled workaround must not remain a way to launch
// a write-capable/custom role without the now-available security adapter.
func validateOpenCodeSecurity(runtimeName string, securityEnabled bool) error {
	if runtimeName == "opencode" && !securityEnabled {
		return fmt.Errorf("OpenCode requires security.enabled: true; the sandbox hook adapter must be enabled for every role")
	}
	return nil
}
