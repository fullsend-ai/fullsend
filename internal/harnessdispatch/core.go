package harnessdispatch

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// Options configures a dispatch run.
type Options struct {
	// ConfigDir is the fullsend config directory (e.g. ".fullsend").
	// Must be a direct child of the repo root; filepath.Dir is used
	// to locate OWNERS and OWNERS_ALIASES for authorization.
	ConfigDir string
	Event     *normevent.Event

	// FetchPolicy controls SSRF protection for URL-sourced agent harnesses.
	// When nil, fetch.DefaultPolicy is used (allows github.com and
	// raw.githubusercontent.com). Set this in tests to allow httptest domains.
	FetchPolicy *fetch.FetchPolicy
}

// Dispatch evaluates authorization, kill switch, harness triggers, and returns execution refs.
// Returns empty slice (not error) when denied, kill-switched, or no triggers match.
// Returns an error when every registered harness fails to resolve or load.
func Dispatch(ctx context.Context, opts Options) ([]ExecutionRef, error) {
	if opts.Event == nil {
		return nil, fmt.Errorf("event is required")
	}
	if opts.ConfigDir == "" {
		return nil, fmt.Errorf("config dir is required")
	}

	dirCfg, err := config.LoadConfig(opts.ConfigDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return nil, err
	}
	if dirCfg.IsKillSwitchActive() {
		return nil, nil
	}
	if err := config.ValidateRuntimeAuthorization(dirCfg); err != nil {
		return nil, fmt.Errorf("invalid authorization config: %w", err)
	}

	// The OWNERS-upgraded role is used only for the event-level and
	// per-harness auth checks; downstream CEL evaluation sees the original
	// collaborator-API role.
	authCheck := AuthCheckEvent(opts.ConfigDir, dirCfg, opts.Event)
	authOpts := AuthOptionsFor(dirCfg)
	if !IsEventAuthorizedWith(&authCheck, authOpts) {
		return nil, nil
	}

	candidates, err := ListTriggeredHarnesses(ctx, opts.ConfigDir, dirCfg, opts.FetchPolicy)
	if err != nil {
		return nil, err
	}

	candidates = FilterAuthorizedHarnessesWith(candidates, &authCheck, authOpts)

	matched, err := MatchHarnesses(candidates, opts.Event)
	if err != nil {
		return nil, err
	}
	matched = dropGitHubRoutedBuiltins(matched, opts.Event)

	var refs []ExecutionRef
	for _, m := range matched {
		role := m.Harness.Role
		ref, err := ProjectExecutionRef(m.Name, role, opts.Event)
		if err != nil {
			return nil, fmt.Errorf("projecting %s: %w", m.Name, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// dropGitHubRoutedBuiltins removes matched harnesses registered under a
// built-in agent name (config.ValidAgentNames, case-insensitive) when the
// event comes from GitHub. On GitHub the built-in stages are already routed
// by the route job in reusable-dispatch.yml; dispatching an override of a
// built-in agent here as well would run it twice. Other sources are left
// untouched: Jira polling has no stage routing and registers built-in names
// with triggers on purpose.
func dropGitHubRoutedBuiltins(matched []TriggeredHarness, event *normevent.Event) []TriggeredHarness {
	if event.Source.System != normevent.SystemGitHub {
		return matched
	}
	builtins := config.ValidAgentNames()
	var kept []TriggeredHarness
	for _, m := range matched {
		if slices.Contains(builtins, strings.ToLower(m.Name)) {
			log.Printf("harness dispatch: %s is a built-in agent; its GitHub stage routing handles this event", m.Name)
			continue
		}
		kept = append(kept, m)
	}
	return kept
}
