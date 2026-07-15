package config

// LayeredConfig provides accessor-based lookup for per-repo configuration
// with merge order: overlay -> base -> compiled-in defaults (ADR 0069,
// Decision 2). Additional file layers can be inserted between base and
// defaults without changing call sites.
//
// # Merge semantics per field
//
//   - Scalar override: first non-zero layer value wins (Version, Runtime).
//   - Boolean OR: any layer setting true wins (KillSwitch). Because YAML
//     cannot distinguish "field absent" from "field: false", OR is the
//     only safe semantic for a safety-critical boolean.
//   - Slice override: first layer with a non-nil slice wins; layers do
//     not merge individual elements (Roles, Agents, AllowedRemoteResources).
//   - Pointer override: first non-nil pointer wins (CreateIssues).
//
// Compiled-in defaults are NOT embedded in the accessor. When no layer
// provides a value, accessors return the zero value for the type (empty
// string, nil slice, nil pointer, false). Callers that need defaults
// apply them at the call site, matching the existing pre-layered behavior
// where ParsePerRepoConfig returns zero values for absent fields.
type LayeredConfig struct {
	layers []*PerRepoConfig // [0] = overlay (highest priority), then base, ...
}

// NewLayeredConfig creates a LayeredConfig from the given layers ordered
// from highest priority (overlay) to lowest (base). Nil layers are
// filtered out. When no layers remain, all accessors return zero values.
func NewLayeredConfig(layers ...*PerRepoConfig) *LayeredConfig {
	var filtered []*PerRepoConfig
	for _, l := range layers {
		if l != nil {
			filtered = append(filtered, l)
		}
	}
	return &LayeredConfig{layers: filtered}
}

// Version returns the config version from the highest-priority layer
// that sets it. Scalar override semantic.
func (c *LayeredConfig) Version() string {
	for _, l := range c.layers {
		if l.Version != "" {
			return l.Version
		}
	}
	return ""
}

// KillSwitch returns true if any layer activates the kill switch.
// Boolean OR semantic: any layer can shut down the system. This is
// intentionally not overridable by higher-priority layers because
// YAML bool false and absent are indistinguishable.
func (c *LayeredConfig) KillSwitch() bool {
	for _, l := range c.layers {
		if l.KillSwitch {
			return true
		}
	}
	return false
}

// Runtime returns the runtime from the first layer that sets it.
// Scalar override semantic.
func (c *LayeredConfig) Runtime() string {
	for _, l := range c.layers {
		if l.Runtime != "" {
			return l.Runtime
		}
	}
	return ""
}

// Roles returns roles from the first layer with a non-nil slice.
// Slice override semantic: the highest-priority layer replaces the
// lower layer entirely rather than merging individual entries.
func (c *LayeredConfig) Roles() []string {
	for _, l := range c.layers {
		if l.Roles != nil {
			return l.Roles
		}
	}
	return nil
}

// Agents returns agent entries from the first layer with a non-nil
// slice. Slice override semantic.
func (c *LayeredConfig) Agents() []AgentEntry {
	for _, l := range c.layers {
		if l.Agents != nil {
			return l.Agents
		}
	}
	return nil
}

// AllowedRemoteResources returns the allowed remote resource prefixes
// from the first layer with a non-nil slice. Slice override semantic.
func (c *LayeredConfig) AllowedRemoteResources() []string {
	for _, l := range c.layers {
		if l.AllowedRemoteResources != nil {
			return l.AllowedRemoteResources
		}
	}
	return nil
}

// CreateIssues returns the create-issues config from the first layer
// that sets it. Pointer override semantic.
func (c *LayeredConfig) CreateIssues() *CreateIssuesConfig {
	for _, l := range c.layers {
		if l.CreateIssues != nil {
			return l.CreateIssues
		}
	}
	return nil
}

// Overlay returns the overlay (highest priority) PerRepoConfig, or nil
// if no overlay was provided. This is the config.yaml layer.
func (c *LayeredConfig) Overlay() *PerRepoConfig {
	if len(c.layers) > 0 {
		return c.layers[0]
	}
	return nil
}

// Base returns the base PerRepoConfig (second layer), or nil if no base
// was provided. This is the config.base.yaml layer.
func (c *LayeredConfig) Base() *PerRepoConfig {
	if len(c.layers) > 1 {
		return c.layers[1]
	}
	return nil
}

// LayerCount returns the number of active layers (excluding compiled-in
// defaults, which are implicit and applied at the call site).
func (c *LayeredConfig) LayerCount() int {
	return len(c.layers)
}
