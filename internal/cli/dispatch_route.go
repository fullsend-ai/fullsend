package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Route decisions printed by `fullsend dispatch --route <agent>`.
const (
	// routeRun: the resolved harness trigger matched an authorized event.
	routeRun = "run"
	// routeSkip: the agent must not run for this event.
	routeSkip = "skip"
	// routeBash: the resolved harness has no trigger, so the bash router in
	// reusable-dispatch.yml keeps deciding (compatibility guard).
	routeBash = "bash"
)

// routableAgents lists the built-in agents whose routing a CEL trigger may
// decide through --route. Each one still runs in its own reusable-dispatch.yml
// job; --route only answers whether that job runs.
var routableAgents = map[string]bool{"review": true}

// routeDecision is the one-line JSON answer of `fullsend dispatch --route`.
type routeDecision struct {
	Agent    string `json:"agent"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	// Harness names the harness the decision used: "config:<source>" for a
	// config-registered harness, "agents@<sha>" for the agents-repo fallback.
	Harness string `json:"harness,omitempty"`
}

type routeOpts struct {
	agent     string
	configDir string
	event     *normevent.Event
	// forgeClient resolves the agents-repo ref for the fallback. When nil,
	// an unauthenticated GitHub client is used (fullsend-ai/agents is public).
	forgeClient forge.Client
	// fetchPolicy overrides fetch.DefaultPolicy (tests).
	fetchPolicy *fetch.FetchPolicy
	printer     *ui.Printer
}

// routeBuiltinAgent decides whether a built-in agent runs for an event. It
// resolves the agent harness the way `fullsend run` does (a config entry with
// source:, else the agents-repo fallback at the lockstep ref), and returns
// routeBash when that harness has no trigger. Otherwise the event must pass
// the dispatch authorization gate and the trigger must match.
func routeBuiltinAgent(ctx context.Context, o routeOpts) (routeDecision, error) {
	d := routeDecision{Agent: o.agent}
	if o.event == nil {
		return d, fmt.Errorf("event is required")
	}

	cfg, err := config.LoadConfig(o.configDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return d, err
	}
	if cfg.IsKillSwitchActive() {
		return withDecision(d, routeSkip, "kill switch active"), nil
	}
	if config.IsAgentExplicitlyDisabled(cfg.AgentEntries(), o.agent) {
		return withDecision(d, routeSkip, "agent disabled in config"), nil
	}

	absConfigDir, err := filepath.Abs(o.configDir)
	if err != nil {
		return d, fmt.Errorf("resolving config dir: %w", err)
	}
	allowlist := cfg.AllowedResources()
	if allowlist == nil {
		allowlist = config.DefaultAllowedRemoteResources()
	}
	policy := fetch.DefaultPolicy
	if o.fetchPolicy != nil {
		policy = *o.fetchPolicy
	}
	eventMap, err := o.event.ToMap()
	if err != nil {
		return d, err
	}
	composeOpts := harness.ComposeOpts{
		WorkspaceRoot: absConfigDir,
		OrgAllowlist:  allowlist,
		FetchPolicy:   policy,
		Config:        harness.BuildConfigMap(cfg),
		Event:         eventMap,
	}
	// Resolve forge: blocks for the event's forge, as `fullsend run` does.
	switch o.event.Source.System {
	case normevent.SystemGitHub, normevent.SystemGitLab:
		composeOpts.ForgePlatform = string(o.event.Source.System)
	}

	forgeClient := o.forgeClient
	if forgeClient == nil {
		forgeClient = gh.New("")
	}
	printer := o.printer
	if printer == nil {
		printer = ui.New(os.Stderr)
	}
	harnessPath, deps, err := resolveAgentSource(ctx, absConfigDir, o.agent, forgeClient, cfg, composeOpts, printer)
	if err != nil {
		return d, err
	}
	if len(deps) > 0 && deps[0].URL != "" {
		composeOpts.SourceURL = deps[0].URL
	}
	d.Harness = routeHarnessLabel(cfg, o.agent, deps)

	h, _, err := harness.LoadWithBase(ctx, harnessPath, composeOpts)
	if err != nil {
		return d, fmt.Errorf("loading %s harness: %w", o.agent, err)
	}
	if strings.TrimSpace(h.Trigger) == "" {
		return withDecision(d, routeBash, "resolved harness has no trigger"), nil
	}

	if !harnessdispatch.AuthorizeEvent(o.configDir, cfg, o.event) {
		return withDecision(d, routeSkip, "actor not authorized"), nil
	}
	ok, err := harness.EvaluateTrigger(h.Trigger, eventMap)
	if err != nil {
		return d, fmt.Errorf("evaluating %s trigger: %w", o.agent, err)
	}
	if !ok {
		return withDecision(d, routeSkip, "trigger did not match"), nil
	}
	return withDecision(d, routeRun, "trigger matched"), nil
}

func withDecision(d routeDecision, decision, reason string) routeDecision {
	d.Decision = decision
	d.Reason = reason
	return d
}

// routeHarnessLabel names the harness source for the decision log.
func routeHarnessLabel(cfg config.ConfigReader, agent string, deps []harness.Dependency) string {
	if entry := findConfigAgentEntry(cfg.AgentEntries(), agent); entry != nil && entry.Source != "" {
		return "config:" + entry.Source
	}
	if len(deps) > 0 && strings.HasPrefix(deps[0].URL, defaultAgentsRepoURLPrefix) {
		sha, _, _ := strings.Cut(strings.TrimPrefix(deps[0].URL, defaultAgentsRepoURLPrefix), "/")
		if len(sha) > 12 {
			sha = sha[:12]
		}
		return "agents@" + sha
	}
	return ""
}
