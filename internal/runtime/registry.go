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
