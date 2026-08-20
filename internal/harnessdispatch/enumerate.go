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
func ListTriggeredHarnesses(ctx context.Context, configDir string, cfg config.ConfigReader, fetchPolicy *fetch.FetchPolicy) ([]TriggeredHarness, error) {
	registered, err := harness.RegisteredAgents(cfg)
	if err != nil {
		return nil, err
	}
	if len(registered) == 0 {
		return nil, nil
	}

	allowlist := cfg.AllowedResources()
	if allowlist == nil {
		allowlist = config.DefaultAllowedRemoteResources()
	}

	policy := fetch.DefaultPolicy
	if fetchPolicy != nil {
		policy = *fetchPolicy
	}

	composeOpts := harness.ComposeOpts{
		WorkspaceRoot: filepath.Dir(configDir),
		OrgAllowlist:  allowlist,
		FetchPolicy:   policy,
		Config:        harness.BuildConfigMap(cfg),
	}

	var out []TriggeredHarness
	for _, agent := range registered {
		resolved, err := harness.ResolveRegisteredPath(ctx, configDir, agent.Entry, allowlist, composeOpts)
		if err != nil {
			log.Printf("harness dispatch: skipping agent %s: resolve failed: %v", agent.Name, err)
			continue
		}
		// Use LoadWithBase to handle harnesses with base: composition
		// (ADR-0045). Load() rejects harnesses with base: fields, but
		// per-repo harnesses commonly use base: to inherit from upstream
		// harness definitions.
		loadOpts := composeOpts
		if harness.IsURL(agent.Entry.Source) {
			loadOpts.SourceURL = agent.Entry.Source
		}
		h, _, err := harness.LoadWithBase(ctx, resolved.Path, loadOpts)
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
	cfg, err := config.LoadConfig(configDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return nil, err
	}
	return cfg.AgentEntries(), nil
}
