package runtime

import "fmt"

// BootstrapInput is the portable contract every runtime needs to provision
// agent content into the sandbox. Implementations live outside this package
// (runner adapter, tests).
type BootstrapInput interface {
	SandboxName() string
	// AgentPath returns the local filesystem path to the agent definition file.
	// For cached agents this may be a content-addressed path with a generic basename.
	AgentPath() string
	// AgentName returns the logical agent name (e.g. "review") used to construct
	// the destination filename as {name}.md inside the sandbox. Populated from
	// the CLI positional argument; must not be empty in production (enforced by
	// cobra arg validation in cmd/fullsend).
	AgentName() string
	SkillDirs() []string
	PluginDirs() []string
}

// validateAgentNameMatch returns an error when requestedName and
// definitionName are both non-empty and do not match. Both ClaudeRuntime
// and PiRuntime call this shared helper so the mismatch message is defined
// in one place.
func validateAgentNameMatch(requestedName, definitionName string) error {
	if requestedName == "" || definitionName == "" {
		return nil
	}
	if definitionName != requestedName {
		return fmt.Errorf("agent name mismatch: requested %q but definition declares %q", requestedName, definitionName)
	}
	return nil
}
