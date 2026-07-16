package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// RegisteredAgent is a validated config-registered agent entry.
type RegisteredAgent struct {
	Entry  config.AgentEntry
	Name   string
	Source string
}

// ResolvedPath is the local filesystem path for a registered agent source.
type ResolvedPath struct {
	Path string
	Dep  Dependency
}

// RegisteredAgents validates and returns entries from a ConfigReader.
func RegisteredAgents(cfg config.ConfigReader) ([]RegisteredAgent, error) {
	if cfg == nil || reflect.ValueOf(cfg).IsNil() {
		return nil, fmt.Errorf("config is required")
	}
	allowlist := cfg.AllowedResources()
	if allowlist == nil {
		allowlist = config.DefaultAllowedRemoteResources()
	}
	agents := cfg.AgentEntries()
	if err := config.ValidateAgentEntries(agents, allowlist); err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return nil, nil
	}
	out := make([]RegisteredAgent, 0, len(agents))
	for _, entry := range agents {
		out = append(out, RegisteredAgent{
			Entry:  entry,
			Name:   entry.DerivedName(),
			Source: entry.Source,
		})
	}
	return out, nil
}

// ResolveRegisteredPath resolves a config AgentEntry to a local filesystem path.
// URL sources use FetchAgentHarness; local sources are contained under configDir.
func ResolveRegisteredPath(ctx context.Context, configDir string, entry config.AgentEntry, allowlist []string, opts ComposeOpts) (ResolvedPath, error) {
	if IsURL(entry.Source) {
		if opts.WorkspaceRoot == "" {
			opts.WorkspaceRoot = filepath.Dir(configDir)
		}
		if opts.OrgAllowlist == nil {
			opts.OrgAllowlist = allowlist
		}
		localPath, dep, err := FetchAgentHarness(ctx, entry.Source, opts)
		if err != nil {
			return ResolvedPath{}, err
		}
		return ResolvedPath{Path: localPath, Dep: dep}, nil
	}

	path, err := containedRegisteredPath(configDir, entry.Source)
	if err != nil {
		return ResolvedPath{}, err
	}
	return ResolvedPath{Path: path}, nil
}

func containedRegisteredPath(baseDir, source string) (string, error) {
	if filepath.IsAbs(source) {
		return "", fmt.Errorf("local path must be relative, not absolute")
	}
	resolved := filepath.Clean(filepath.Join(baseDir, source))
	if rel, err := filepath.Rel(baseDir, resolved); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("local path %q escapes config directory", source)
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", fmt.Errorf("harness path %s: %w", source, err)
	}
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	realBase, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(realBase, real); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("local path %q escapes config directory via symlink", source)
	}
	return real, nil
}
