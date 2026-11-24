package harnessdispatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// annotationWriter is the destination for GitHub Actions workflow command
// annotations (::error::, ::warning::). Defaults to os.Stderr; tests
// replace it to capture output.
var annotationWriter io.Writer = os.Stderr

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
//
// Per-agent resolve/load failures are logged as GitHub Actions error
// annotations and skipped so a single broken harness does not block the
// rest. If every registered agent fails to resolve or load, an error is
// returned so a fully-unreadable harness set is not indistinguishable from
// "no trigger matched".
func ListTriggeredHarnesses(ctx context.Context, configDir string, cfg config.ConfigReader, fetchPolicy *fetch.FetchPolicy) ([]TriggeredHarness, error) {
	return listTriggeredHarnesses(ctx, configDir, cfg, fetchPolicy, nil)
}

// BuiltinResolver resolves a built-in agent (config.ValidAgentNames) to a
// local harness path when no agents: entry registers it with a source. It
// receives the compose options ListEffectiveTriggeredHarnesses loads
// harnesses with and returns the options to load this harness with (for
// example with SourceURL set when it was fetched from a URL). ok is false
// when the harness could not be resolved; the resolver reports why.
type BuiltinResolver func(ctx context.Context, name string, opts harness.ComposeOpts) (path string, loadOpts harness.ComposeOpts, ok bool)

// ListEffectiveTriggeredHarnesses returns the triggered harnesses of every
// agent the config makes effective, for a dispatcher with no stage routing
// of its own, such as the Jira poller: the agents registered with a source,
// as ListTriggeredHarnesses lists them, plus each built-in agent that no
// such entry registers, resolved with builtin. An entry with no source (it
// only tunes the agent, ADR 0091) does not replace the built-in harness, so
// the built-in is listed with the entry's harness: blocks applied. An agent
// whose last agents: entry disables it is not listed, whether registered
// or built-in, and each agent is listed at most once. When the config sets
// roles:, only the built-in agents those roles select are listed (code is
// selected by coder); agents registered with a source are not scoped by
// roles.
//
// Unlike ListTriggeredHarnesses, any failure is an error: an agent that
// cannot be resolved or loaded (registered or built-in) is also logged as an
// error annotation, but the list is not returned incomplete. A dispatcher
// that advances its checkpoints past events no agent matched, such as the
// Jira poller, would otherwise lose the events only the unavailable agent
// could match. Failing the run lets the next run retry them.
func ListEffectiveTriggeredHarnesses(ctx context.Context, configDir string, cfg config.ConfigReader, fetchPolicy *fetch.FetchPolicy, builtin BuiltinResolver) ([]TriggeredHarness, error) {
	if builtin == nil {
		return nil, fmt.Errorf("built-in agent resolver is required")
	}
	return listTriggeredHarnesses(ctx, configDir, cfg, fetchPolicy, builtin)
}

// effectiveBuiltinAgents returns the built-in agents (config.ValidAgentNames)
// that registered does not already list, that the configured roles select
// (config.BuiltinAgentSelected) and that no agents: entry disables, in
// ValidAgentNames order. An unselected built-in is dropped here, before
// anything resolves or fetches its harness.
func effectiveBuiltinAgents(entries []config.AgentEntry, registered []harness.RegisteredAgent, roles []string) []string {
	var out []string
	for _, name := range config.ValidAgentNames() {
		if !config.BuiltinAgentSelected(name, roles) {
			continue
		}
		if config.IsAgentExplicitlyDisabled(entries, name) {
			continue
		}
		if slices.ContainsFunc(registered, func(a harness.RegisteredAgent) bool {
			return strings.EqualFold(a.Name, name)
		}) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// listTriggeredHarnesses implements ListTriggeredHarnesses and, with a
// non-nil builtin, ListEffectiveTriggeredHarnesses.
func listTriggeredHarnesses(ctx context.Context, configDir string, cfg config.ConfigReader, fetchPolicy *fetch.FetchPolicy, builtin BuiltinResolver) ([]TriggeredHarness, error) {
	registered, err := harness.RegisteredAgents(cfg)
	if err != nil {
		return nil, err
	}
	entries := cfg.AgentEntries()
	var builtins []string
	if builtin != nil {
		// RegisteredAgents keeps every enabled entry with a source, even
		// one a later entry disables; the effective list honors the last
		// entry for the name.
		registered = slices.DeleteFunc(registered, func(a harness.RegisteredAgent) bool {
			return config.IsAgentExplicitlyDisabled(entries, a.Name)
		})
		var roles []string
		if pr, ok := cfg.(config.PerRepoConfigReader); ok {
			roles = pr.ConfigRoles()
		}
		builtins = effectiveBuiltinAgents(entries, registered, roles)
	}
	if len(registered) == 0 && len(builtins) == 0 {
		return nil, nil
	}

	allowlist := config.EffectiveAllowedResources(cfg)

	policy := fetch.DefaultPolicy
	if fetchPolicy != nil {
		policy = *fetchPolicy
	}

	composeOpts := newComposeOpts(configDir, cfg, allowlist, policy)

	var out []TriggeredHarness
	var loadErrs []error
	loaded := 0
	// load composes one agent's harness with its entry's harness: blocks
	// (ADR 0162), which may override the trigger, and records the result.
	load := func(name, path string, loadOpts harness.ComposeOpts) {
		loadOpts.Blocks = config.HarnessBlocksFor(entries, name)
		loadOpts.Agent = name
		h, _, err := harness.LoadWithBase(ctx, path, loadOpts)
		if err != nil {
			fmt.Fprintf(annotationWriter, "::error::harness dispatch: skipping agent %s: load failed: %v\n", name, err)
			loadErrs = append(loadErrs, fmt.Errorf("agent %s: load failed: %w", name, err))
			return
		}
		loaded++
		if strings.TrimSpace(h.Trigger) == "" {
			return
		}
		out = append(out, TriggeredHarness{Name: name, Harness: h, Path: path})
	}
	for _, agent := range registered {
		resolved, err := harness.ResolveRegisteredPath(ctx, configDir, agent.Entry, allowlist, composeOpts)
		if err != nil {
			fmt.Fprintf(annotationWriter, "::error::harness dispatch: skipping agent %s: resolve failed: %v\n", agent.Name, err)
			loadErrs = append(loadErrs, fmt.Errorf("agent %s: resolve failed: %w", agent.Name, err))
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
		load(agent.Name, resolved.Path, loadOpts)
	}
	for _, name := range builtins {
		path, loadOpts, ok := builtin(ctx, name, composeOpts)
		if !ok {
			fmt.Fprintf(annotationWriter, "::error::harness dispatch: skipping built-in agent %s: its harness could not be resolved\n", name)
			loadErrs = append(loadErrs, fmt.Errorf("built-in agent %s: harness could not be resolved", name))
			continue
		}
		load(name, path, loadOpts)
	}
	// Each failure above was already written as a ::error:: annotation, and
	// its text is also folded into the aggregated error returned here. That
	// duplication is intentional for the total-failure case: the
	// annotations surface inline in the GitHub Actions UI next to the step
	// that produced them, while the aggregated error drives dispatch's
	// non-zero exit and appears in the plain-text CI log/exit trace. Partial
	// failures (loaded > 0) keep the annotation-only behavior unchanged,
	// except for the effective list below.
	if loaded == 0 && len(loadErrs) > 0 {
		return nil, fmt.Errorf("no agents could be loaded: %w", errors.Join(loadErrs...))
	}
	// With a built-in resolver (the effective list) a partial failure is
	// also an error, so the caller never matches against an incomplete list.
	if builtin != nil && len(loadErrs) > 0 {
		return nil, fmt.Errorf("some agents could not be loaded: %w", errors.Join(loadErrs...))
	}
	return out, nil
}

// newComposeOpts builds the options ListTriggeredHarnesses resolves and loads
// harnesses with. GitHubURL carries the configured GitHub host, so base
// skill, plugin and script directories served by a GitHub Enterprise Server
// instance parse as raw content, as they do for run, lint and lock.
func newComposeOpts(configDir string, cfg config.ConfigReader, allowlist []string, policy fetch.FetchPolicy) harness.ComposeOpts {
	return harness.ComposeOpts{
		WorkspaceRoot: filepath.Dir(configDir),
		OrgAllowlist:  allowlist,
		FetchPolicy:   policy,
		Config:        harness.BuildConfigMap(cfg),
		GitHubURL:     forge.ConfiguredGitHubURL(),
	}
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
			fmt.Fprintf(annotationWriter, "::error::harness dispatch: skipping agent %s: trigger eval failed: %v\n", c.Name, err)
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
