package runtime

import (
	"fmt"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/pluginformat"
)

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
	// Plugins returns the harness's declared plugin directories (ADR 0094),
	// each tagged with the runtime format it is in. A runtime loads the
	// entries of its own kind and names and skips the rest.
	Plugins() []PluginInput
	// ModelAliases returns the per-repo model alias overrides from
	// .fullsend/config.yaml (models.aliases). A nil map means no overrides.
	// Used by the pi runtime to thread the overrides into sub-agent dispatch
	// so children resolve aliases the same way the parent does (#7020).
	ModelAliases() map[string]string
	// AgentSubagents returns the per-agent subagents map from the merged
	// config agents: entry for this run's agent. Keys are persona names
	// (or "default"); values are model references (nil = tombstone).
	// Used by the pi runtime to resolve each persona's model (#7031).
	AgentSubagents() map[string]*string
	// ParentModel returns the model this run's agent actually runs on --
	// after the agents: entry, --model and the alias remap -- or "" when
	// the caller does not know it. The pi runtime adds it to the set a
	// persona's model is checked against, so a persona pinned to the
	// parent's own model is accepted even when that model is outside the
	// alias table (#7031).
	ParentModel() string
}

// PluginInput is one declared plugin: a host directory to upload, the
// sandbox name it is uploaded as, the format the directory is in, and the
// environment and pi options the harness gave it. Name is optional — the
// path basename is used when empty.
type PluginInput struct {
	Name   string
	Path   string
	Kind   pluginformat.Kind
	Env    map[string]string
	PiArgs []string
}

// SandboxName is the directory name the plugin takes in the sandbox:
// Name when set, else the path basename.
func (p PluginInput) SandboxName() string {
	if p.Name != "" {
		return p.Name
	}
	return filepath.Base(p.Path)
}

// pluginsOfKind returns the entries with a non-empty path that a runtime
// reading the given format loads.
func pluginsOfKind(inputs []PluginInput, kind pluginformat.Kind) []PluginInput {
	var out []PluginInput
	for _, in := range inputs {
		if in.Path != "" && in.Kind == kind {
			out = append(out, in)
		}
	}
	return out
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
