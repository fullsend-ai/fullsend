package harnessdispatch

import (
	"context"
	"fmt"
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
func ListTriggeredHarnesses(ctx context.Context, configDir string, agents []config.AgentEntry) ([]TriggeredHarness, error) {
	if len(agents) == 0 {
		return nil, nil
	}
	var out []TriggeredHarness
	for _, entry := range agents {
		name := entry.DerivedName()
		path, err := resolveHarnessPath(configDir, entry)
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

func resolveHarnessPath(configDir string, entry config.AgentEntry) (string, error) {
	if harness.IsURL(entry.Source) {
		return "", fmt.Errorf("URL agent sources are not supported in dispatch enumerate yet: %s", entry.Source)
	}
	src := entry.Source
	if !filepath.IsAbs(src) {
		src = filepath.Join(configDir, src)
	}
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("harness path %s: %w", entry.Source, err)
	}
	return src, nil
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
			return nil, fmt.Errorf("evaluating trigger for %s: %w", c.Name, err)
		}
		if ok {
			matched = append(matched, c)
		}
	}
	return matched, nil
}

// MergedConfigAgents loads agent entries from per-repo config directory.
func MergedConfigAgents(configDir string) ([]config.AgentEntry, error) {
	cfgPath := filepath.Join(configDir, "config.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cfg, err := config.ParsePerRepoConfig(data)
	if err != nil {
		return nil, err
	}
	return cfg.Agents, nil
}
