package runtime

import (
	"fmt"
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
	default:
		return Backend{}, fmt.Errorf("unknown runtime %q: must be one of %s", name, strings.Join(config.ValidRuntimes(), ", "))
	}
}

// ResolveFromConfig selects the runtime backend from org config defaults.
func ResolveFromConfig(cfg config.OrgConfigReader) (Backend, error) {
	rt := "claude"
	if cfg != nil && cfg.OrgRepoDefaults().Runtime != "" {
		rt = cfg.OrgRepoDefaults().Runtime
	}
	return Resolve(rt)
}

// ResolveFromPerRepoConfig selects the runtime backend from per-repo config.
func ResolveFromPerRepoConfig(cfg config.PerRepoConfigReader) (Backend, error) {
	rt := "claude"
	if cfg != nil && cfg.ConfigRuntime() != "" {
		rt = cfg.ConfigRuntime()
	}
	return Resolve(rt)
}
