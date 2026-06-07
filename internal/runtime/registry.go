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
		r := DummyRuntime{}
		return Backend{Runtime: r, Transcripts: r}, nil
	default:
		return Backend{}, fmt.Errorf("unknown runtime %q: must be one of %s", name, strings.Join(config.ValidRuntimes(), ", "))
	}
}

// ResolveFromConfig selects the runtime backend from org config defaults.
func ResolveFromConfig(cfg *config.OrgConfig) (Backend, error) {
	rt := "claude"
	if cfg != nil && cfg.Defaults.Runtime != "" {
		rt = cfg.Defaults.Runtime
	}
	return Resolve(rt)
}
