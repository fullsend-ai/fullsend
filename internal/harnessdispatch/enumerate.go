package harnessdispatch

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// TriggeredHarness pairs a registered agent with its loaded harness.
type TriggeredHarness struct {
	Name    string
	Harness *harness.Harness
	Path    string
}

// ListTriggeredHarnesses returns config-registered agents whose harness has a non-empty trigger.
func ListTriggeredHarnesses(ctx context.Context, configDir string, cfg *config.PerRepoConfig, agents []config.AgentEntry) ([]TriggeredHarness, error) {
	if len(agents) == 0 {
		return nil, nil
	}
	allowlist := configAllowlist(cfg)
	var out []TriggeredHarness
	for _, entry := range agents {
		name := entry.DerivedName()
		path, err := resolveHarnessPath(ctx, configDir, entry, allowlist)
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", name, err)
		}
		h, err := harness.Load(path)
		if err != nil {
			return nil, fmt.Errorf("agent %s: %w", name, err)
		}
		if strings.TrimSpace(h.Trigger) == "" {
			continue
		}
		out = append(out, TriggeredHarness{Name: name, Harness: h, Path: path})
	}
	return out, nil
}

func configAllowlist(cfg *config.PerRepoConfig) []string {
	if cfg == nil || cfg.AllowedRemoteResources == nil {
		return config.DefaultAllowedRemoteResources()
	}
	return cfg.AllowedRemoteResources
}

func resolveHarnessPath(ctx context.Context, configDir string, entry config.AgentEntry, allowlist []string) (string, error) {
	if harness.IsURL(entry.Source) {
		opts := harness.ComposeOpts{
			WorkspaceRoot: filepath.Dir(configDir),
			OrgAllowlist:  allowlist,
		}
		localPath, _, err := harness.FetchAgentHarness(ctx, entry.Source, opts)
		if err != nil {
			return "", err
		}
		return localPath, nil
	}
	return containedHarnessPath(configDir, entry.Source)
}

func containedHarnessPath(configDir, source string) (string, error) {
	if filepath.IsAbs(source) {
		return "", fmt.Errorf("local harness path must be relative, not absolute")
	}
	resolved := filepath.Clean(filepath.Join(configDir, source))
	if rel, err := filepath.Rel(configDir, resolved); err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("harness path %q escapes config directory", source)
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", fmt.Errorf("harness path %s: %w", source, err)
	}
	return resolved, nil
}

// MatchHarnesses evaluates CEL triggers and returns matching harnesses.
func MatchHarnesses(candidates []TriggeredHarness, event *normevent.Event) ([]TriggeredHarness, error) {
	eventMap, err := event.ToMap()
	if err != nil {
		return nil, err
	}
	var matched []TriggeredHarness
	for _, c := range candidates {
		ok, err := harness.EvaluateTrigger(c.Harness.Trigger, eventMap)
		if err != nil {
			log.Printf("harness dispatch: trigger eval failed for %s: %v", c.Name, err)
			continue
		}
		if ok {
			matched = append(matched, c)
		}
	}
	return matched, nil
}

// MergedConfigAgents loads agent entries from per-repo config directory.
func MergedConfigAgents(configDir string) ([]config.AgentEntry, error) {
	cfg, err := LoadConfigDir(configDir)
	if err != nil {
		return nil, err
	}
	return cfg.Agents, nil
}
