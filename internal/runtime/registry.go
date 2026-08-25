package runtime

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// Resolve returns the agent backend for the given runtime name.
func Resolve(name string) (Backend, error) {
	switch name {
	case "", "claude":
		r := ClaudeRuntime{}
		return Backend{Runtime: r, Transcripts: r}, nil
	case "dummy":
		// Selected only via explicit per-repo/org config (behaviour test orgs).
		r := DummyRuntime{}
		return Backend{Runtime: r, Transcripts: r}, nil
	case "opencode":
		r := OpenCodeRuntime{}
		return Backend{Runtime: r, Transcripts: r}, nil
	case "pi":
		r := PiRuntime{}
		return Backend{Runtime: r, Transcripts: r}, nil
	default:
		return Backend{}, fmt.Errorf("unknown runtime %q: must be one of %s", name, strings.Join(config.ValidRuntimes(), ", "))
	}
}

// ResolveFromConfig selects the runtime backend from org config defaults.
// The runtime name is validated against [config.ValidRuntimes] before
// resolution so that stub runtimes registered in [Resolve] for dev/testing
// cannot be activated through config files.
func ResolveFromConfig(cfg config.OrgConfigReader) (Backend, error) {
	rt := "claude"
	if cfg != nil && cfg.OrgRepoDefaults().Runtime != "" {
		rt = cfg.OrgRepoDefaults().Runtime
	}
	if err := validateConfigRuntime(rt); err != nil {
		return Backend{}, err
	}
	return Resolve(rt)
}

// ResolveFromPerRepoConfig selects the runtime backend from per-repo config.
// The runtime name is validated against [config.ValidRuntimes] before
// resolution so that stub runtimes registered in [Resolve] for dev/testing
// cannot be activated through config files.
func ResolveFromPerRepoConfig(cfg config.PerRepoConfigReader) (Backend, error) {
	rt := "claude"
	if cfg != nil && cfg.ConfigRuntime() != "" {
		rt = cfg.ConfigRuntime()
	}
	if err := validateConfigRuntime(rt); err != nil {
		return Backend{}, err
	}
	return Resolve(rt)
}

// ResolveForAgent selects the runtime backend for one agent: the agents:
// entry's runtime when set, else repoRuntime (the repo-wide runtime: key,
// "claude" when empty). The boolean reports whether the per-agent entry
// decided. Both values are validated against [config.ValidRuntimes] so an
// agents: entry cannot activate a stub runtime any more than the repo-wide
// key can.
func ResolveForAgent(agents []config.AgentEntry, repoRuntime, agent string) (Backend, bool, error) {
	if agent != "" {
		if entry, ok := config.AgentSettingsFor(agents, agent); ok && entry.Runtime != "" {
			if err := validateConfigRuntime(entry.Runtime); err != nil {
				return Backend{}, false, fmt.Errorf("agents.%s: %w", entry.DerivedName(), err)
			}
			backend, err := Resolve(entry.Runtime)
			if err != nil {
				return Backend{}, false, err
			}
			return backend, true, nil
		}
	}
	rt := repoRuntime
	if rt == "" {
		rt = "claude"
	}
	if err := validateConfigRuntime(rt); err != nil {
		return Backend{}, false, err
	}
	backend, err := Resolve(rt)
	return backend, false, err
}

// validateConfigRuntime checks that rt is in the set of user-facing
// runtimes allowed in config files.  Stub runtimes (e.g. "opencode")
// are intentionally excluded from [config.ValidRuntimes] so they
// cannot be activated through org or per-repo config.
func validateConfigRuntime(rt string) error {
	valid := config.ValidRuntimes()
	if !slices.Contains(valid, rt) {
		return fmt.Errorf("invalid runtime %q in config: must be one of %s", rt, strings.Join(valid, ", "))
	}
	return nil
}
