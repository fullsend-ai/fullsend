package harnessdispatch

import (
	"context"
	"log"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
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
// fetchPolicy controls SSRF protection for URL-sourced agents. When nil,
// fetch.DefaultPolicy is used. Callers that need custom domain lists (e.g.
// tests using httptest) can pass a policy with the test server's domain.
func ListTriggeredHarnesses(ctx context.Context, configDir string, cfg *config.DirConfig, fetchPolicy *fetch.FetchPolicy) ([]TriggeredHarness, error) {
	registered, err := harness.RegisteredAgents(cfg)
	if err != nil {
		return nil, err
	}
	if len(registered) == 0 {
		return nil, nil
	}

	allowlist := cfg.AllowedRemoteResources
	if allowlist == nil {
		allowlist = config.DefaultAllowedRemoteResources()
	}

	policy := fetch.DefaultPolicy
	if fetchPolicy != nil {
		policy = *fetchPolicy
	}

	var out []TriggeredHarness
	for _, agent := range registered {
		resolved, err := harness.ResolveRegisteredPath(ctx, configDir, agent.Entry, allowlist, harness.ComposeOpts{
			WorkspaceRoot: filepath.Dir(configDir),
			OrgAllowlist:  allowlist,
			FetchPolicy:   policy,
		})
		if err != nil {
			log.Printf("harness dispatch: skipping agent %s: resolve failed: %v", agent.Name, err)
			continue
		}
		h, err := harness.Load(resolved.Path)
		if err != nil {
			log.Printf("harness dispatch: skipping agent %s: load failed: %v", agent.Name, err)
			continue
		}
		if strings.TrimSpace(h.Trigger) == "" {
			continue
		}
		out = append(out, TriggeredHarness{Name: agent.Name, Harness: h, Path: resolved.Path})
	}
	return out, nil
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
	cfg, err := config.LoadFromDir(configDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return nil, err
	}
	return cfg.Agents, nil
}
