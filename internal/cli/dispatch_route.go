package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/celtrigger"
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
	// routeRun: the agent's agents[].trigger matched an authorized event.
	routeRun = "run"
	// routeSkip: the agent must not run for this event.
	routeSkip = "skip"
	// routeBash: the bash router in reusable-dispatch.yml keeps deciding:
	// the repository set no agents[].trigger for the agent, the event is not
	// one the agent's CEL route covers, or the kill switch or a disabled
	// agent entry is left to the route job's own checks.
	routeBash = "bash"
)

// routableAgent is a built-in agent whose routing a CEL trigger may decide
// through --route. It still runs in its own reusable-dispatch.yml job;
// --route only answers whether that job starts.
//
// Adding an agent takes an entry here, its event gate in the route job's
// "Detect CEL route opt-in" step, and tests.
type routableAgent struct {
	// accepts reports whether the event is one this agent's CEL route
	// covers. Other events keep bash routing (routeBash).
	accepts func(*normevent.Event) bool
	// role is the role the agent's reusable-dispatch.yml job mints its
	// token for, used by the authorization gate.
	role string
}

var routableAgents = map[string]routableAgent{
	"retro":  {accepts: retroRouteAccepts, role: "retro"},
	"review": {accepts: reviewRouteAccepts, role: "review"},
}

// retroRouteAccepts covers the events bash routes to retro: a pull request
// closed (merged or not), and /fs-retro on an issue or a pull request.
func retroRouteAccepts(e *normevent.Event) bool {
	if e.Entity.Kind == normevent.EntityChangeProposal &&
		(e.Transition.Kind == normevent.TransitionClosed || e.Transition.Kind == normevent.TransitionMerged) {
		return true
	}
	return e.Transition.Kind == normevent.TransitionCommentAdded &&
		e.Transition.Comment != nil && e.Transition.Comment.Command == "/fs-retro"
}

// reviewRouteAccepts covers pull request events and comments on a pull
// request: bash routes /fs-review only on pull requests, and a review needs
// one.
func reviewRouteAccepts(e *normevent.Event) bool {
	if e.Entity.Kind == normevent.EntityChangeProposal {
		return true
	}
	return e.Transition.Kind == normevent.TransitionCommentAdded && e.Entity.LinkedChangeProposal != nil
}

// routableAgentNames lists the routable agents for error messages.
func routableAgentNames() string {
	names := make([]string, 0, len(routableAgents))
	for name := range routableAgents {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

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

// routeBuiltinAgent decides whether a built-in agent runs for an event.
// Routing moves to CEL only when the repository sets agents[].trigger on the
// agent's entry in config.yaml. A harness trigger: never opts in, whether the
// user's own harness writes it or inherits it through base: (the default
// harnesses' triggers keep bash routing until the cutover, #2902).
//
// Without a config trigger the decision is routeBash, and so it is for the
// kill switch, a disabled entry and an event outside the agent's CEL route.
// A bot's comment returns routeSkip. Otherwise the event must pass the
// dispatch authorization gate, including the threshold for the harness
// role:, and the trigger must match. The harness is resolved the way
// `fullsend run` does (a config entry with source:, else the agents-repo
// fallback at the lockstep ref) to name it in the decision and read its
// role.
//
// An invalid trigger or agent list is an error for every actor, so the
// route job keeps the bash stage. A valid trigger that fails to evaluate on
// the event returns routeSkip.
func routeBuiltinAgent(ctx context.Context, o routeOpts) (routeDecision, error) {
	d := routeDecision{Agent: o.agent}
	if o.event == nil {
		return d, fmt.Errorf("event is required")
	}
	agent, ok := routableAgents[o.agent]
	if !ok {
		return d, fmt.Errorf("unsupported --route agent %q (supported: %s)", o.agent, routableAgentNames())
	}

	cfg, err := config.LoadConfig(o.configDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return d, err
	}
	// The route job's own kill-switch and agent-enabled checks report these
	// cases loudly, so leave the decision to the bash router.
	if cfg.IsKillSwitchActive() {
		return withDecision(d, routeBash, "kill switch active"), nil
	}
	if config.IsAgentExplicitlyDisabled(cfg.AgentEntries(), o.agent) {
		return withDecision(d, routeBash, "agent disabled in config"), nil
	}
	entry := findConfigAgentEntry(cfg.AgentEntries(), o.agent)
	configTrigger := ""
	if entry != nil {
		configTrigger = strings.TrimSpace(entry.Trigger)
	}
	if configTrigger == "" {
		return withDecision(d, routeBash, "no agents[].trigger"), nil
	}
	if err := celtrigger.Validate(configTrigger); err != nil {
		return d, fmt.Errorf("invalid %s agents[].trigger: %w", o.agent, err)
	}
	if !agent.accepts(o.event) {
		return withDecision(d, routeBash, "event is not routable for "+o.agent), nil
	}
	// Bash never starts a stage from a bot's slash command; keep that for
	// any trigger, including one that leaves out an actor.kind guard.
	if o.event.Transition.Kind == normevent.TransitionCommentAdded && o.event.Actor.Kind == normevent.ActorBot {
		return withDecision(d, routeSkip, "bot comments do not start "+o.agent), nil
	}

	// Same check as resolveAgentSource, so an invalid entry is an error
	// rather than a failed harness resolution.
	if err := config.ValidateAgentEntries(cfg.AgentEntries(), cfg.AllowedResources()); err != nil {
		return d, fmt.Errorf("invalid agent config: %w", err)
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
	composeOpts := harness.ComposeOpts{
		WorkspaceRoot: absConfigDir,
		OrgAllowlist:  allowlist,
		FetchPolicy:   policy,
		Config:        harness.BuildConfigMap(cfg),
	}
	forgeClient := o.forgeClient
	if forgeClient == nil {
		forgeClient = gh.New("")
	}
	printer := o.printer
	if printer == nil {
		printer = ui.New(os.Stderr)
	}
	// The harness is resolved to name it in the decision and to read its
	// role:, which sets the per-harness authorization threshold. When it
	// cannot be fetched or loaded, the threshold fails closed to write; the
	// agent's job reports the failure when it runs.
	harnessRole := ""
	harnessUnavailable := false
	if path, deps, err := resolveAgentSource(ctx, absConfigDir, o.agent, forgeClient, cfg, composeOpts, printer); err == nil {
		d.Harness = routeHarnessLabel(cfg, o.agent, deps)
		if len(deps) > 0 && deps[0].URL != "" {
			composeOpts.SourceURL = deps[0].URL
		}
		if h, _, err := harness.LoadWithBase(ctx, path, composeOpts); err == nil {
			harnessRole = h.Role
		} else {
			harnessUnavailable = true
			printer.StepWarn(fmt.Sprintf("Could not load the %s harness to read its role, requiring write access: %v", o.agent, err))
		}
	} else {
		harnessUnavailable = true
		printer.StepWarn(fmt.Sprintf("Could not resolve the %s harness, requiring write access: %v", o.agent, err))
	}

	// Same gate as harness dispatch: the event-level rules, then the
	// harness role threshold (IsHarnessAuthorized applies both), on the
	// OWNERS-upgraded role. A closed pull request starts a retro whoever
	// closed it (the lifecycle-close exception), keyed on the resolved
	// harness role:, or on the agent's own role only when the harness could
	// not be loaded (the agent's job mints that role either way).
	exceptionRole := harnessRole
	if harnessUnavailable {
		exceptionRole = agent.role
	}
	authCheck := harnessdispatch.AuthCheckEvent(o.configDir, cfg, o.event)
	if !harnessdispatch.IsLifecycleCloseAuthorized(o.event, exceptionRole) &&
		!harnessdispatch.IsHarnessAuthorized(&authCheck, harnessRole) {
		if harnessUnavailable {
			return withDecision(d, routeSkip, "harness unavailable; actor below write"), nil
		}
		return withDecision(d, routeSkip, "actor not authorized"), nil
	}
	eventMap, err := o.event.ToMap()
	if err != nil {
		return d, fmt.Errorf("converting event for %q route: %w", o.agent, err)
	}
	// The trigger compiled above, so an error here comes from evaluating it
	// on this event (e.g. a missing key without has()). Like
	// harnessdispatch.MatchHarnesses, that is no match.
	matched, err := harness.EvaluateTrigger(configTrigger, eventMap)
	if err != nil {
		return withDecision(d, routeSkip, fmt.Sprintf("trigger evaluation failed: %v", err)), nil
	}
	if !matched {
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
