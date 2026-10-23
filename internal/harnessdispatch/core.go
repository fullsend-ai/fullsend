package harnessdispatch

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/owners"
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

// Result describes a dispatch evaluation. AuthorizationDenied is set only
// when a confirmed authorization failure prevents at least one matching
// harness from running; kill-switch and no-trigger results leave it false.
type Result struct {
	Refs                  []ExecutionRef
	AuthorizationDenied   bool
	DispatchBlockedReason string
}

// Dispatch evaluates authorization, kill switch, harness triggers, and returns execution refs.
// Returns empty slice (not error) when denied, kill-switched, or no triggers match.
// Returns an error when every registered harness fails to resolve or load.
func Dispatch(ctx context.Context, opts Options) ([]ExecutionRef, error) {
	result, err := DispatchResult(ctx, opts)
	return result.Refs, err
}

// DispatchResult evaluates authorization, kill switch, harness triggers, and
// returns execution refs together with the reason an empty result occurred.
func DispatchResult(ctx context.Context, opts Options) (Result, error) {
	if opts.Event == nil {
		return Result{}, fmt.Errorf("event is required")
	}
	if opts.ConfigDir == "" {
		return Result{}, fmt.Errorf("config dir is required")
	}

	dirCfg, err := config.LoadConfig(opts.ConfigDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return Result{}, err
	}
	killSwitchActive := dirCfg.IsKillSwitchActive()
	if killSwitchActive {
		// A kill switch blocks execution, but it must not turn every slash
		// command into a paused-agent notice. Evaluate triggers first so the
		// caller can distinguish an actual agent launch request from commands
		// handled by another workflow, such as /fs-fix-stop.
		candidates, err := ListTriggeredHarnesses(ctx, opts.ConfigDir, dirCfg, opts.FetchPolicy)
		if err != nil {
			return Result{}, nil
		}
		matched, err := MatchHarnesses(candidates, opts.Event)
		if err != nil {
			return Result{}, err
		}
		if len(matched) == 0 {
			return Result{}, nil
		}
		return Result{DispatchBlockedReason: "kill_switch"}, nil
	}

	// Compute the effective role for the auth gate without mutating the
	// caller's event. The OWNERS-upgraded role is used only for the
	// event-level and per-harness auth checks; downstream CEL evaluation
	// sees the original collaborator-API role.
	repoRoot := filepath.Dir(opts.ConfigDir)
	effectiveRole := opts.Event.Actor.Role
	if pr, ok := dirCfg.(config.PerRepoConfigReader); ok && pr.IsOwnersFileAuthEnabled() && opts.Event.Actor.ID != "" {
		ownersPath := filepath.Join(repoRoot, "OWNERS")
		aliasesPath := filepath.Join(repoRoot, "OWNERS_ALIASES")
		role, err := owners.Resolve(ownersPath, aliasesPath, opts.Event.Actor.ID)
		if err != nil {
			log.Printf("harness dispatch: OWNERS resolution failed for %s: %v", opts.Event.Actor.ID, err)
		} else if role != owners.RoleNone {
			effectiveRole = owners.MapToActorRole(role, effectiveRole)
			log.Printf("harness dispatch: OWNERS file resolved user %s as %s", opts.Event.Actor.ID, role)
		}
	}

	authCheck := *opts.Event
	authCheck.Actor.Role = effectiveRole
	eventAuthorized := IsEventAuthorized(&authCheck)
	if !eventAuthorized && !opts.Event.AuthorizationAvailable() {
		return Result{}, nil
	}

	candidates, err := ListTriggeredHarnesses(ctx, opts.ConfigDir, dirCfg, opts.FetchPolicy)
	if err != nil {
		// An already-denied event cannot launch anything, so an unavailable
		// harness should not turn the denial-notification path into a failure.
		// Authorized events still surface resolution errors normally.
		if !eventAuthorized {
			return Result{}, nil
		}
		return Result{}, err
	}

	if !eventAuthorized {
		// Keep the execution gate fail-closed, but use a non-dispatching CEL
		// probe to establish whether this denied event actually targeted a
		// harness. A denial notice is only justified when both gates fail: an
		// authorization failure and a matching trigger. No execution refs are
		// projected from this branch.
		matched, err := MatchHarnesses(candidates, opts.Event)
		if err != nil {
			return Result{}, err
		}
		return Result{AuthorizationDenied: len(matched) > 0}, nil
	}

	authorizedCandidates, deniedCandidates := partitionHarnessesByAuthorization(candidates, &authCheck)

	matched, err := MatchHarnesses(authorizedCandidates, opts.Event)
	if err != nil {
		return Result{}, err
	}

	// The normal dispatch path evaluates CEL only for authorized candidates.
	// For the notice path, evaluate denied candidates separately and never
	// project their refs; this distinguishes a denied launch from an event
	// that simply does not match any harness.
	var authorizationDenied bool
	if len(deniedCandidates) > 0 {
		deniedMatches, err := MatchHarnesses(deniedCandidates, opts.Event)
		if err != nil {
			return Result{}, err
		}
		authorizationDenied = len(deniedMatches) > 0
	}
	matched = dropGitHubRoutedBuiltins(matched, opts.Event)

	var refs []ExecutionRef
	for _, m := range matched {
		role := m.Harness.Role
		ref, err := ProjectExecutionRef(m.Name, role, opts.Event)
		if err != nil {
			return Result{}, fmt.Errorf("projecting %s: %w", m.Name, err)
		}
		refs = append(refs, ref)
	}
	return Result{Refs: refs, AuthorizationDenied: authorizationDenied}, nil
}

// partitionHarnessesByAuthorization keeps the dispatch path's CEL input
// restricted to authorized harnesses while retaining denied candidates for a
// separate, non-dispatching notice probe.
func partitionHarnessesByAuthorization(harnesses []TriggeredHarness, event *normevent.Event) (authorized, denied []TriggeredHarness) {
	for _, harness := range harnesses {
		role := ""
		if harness.Harness != nil {
			role = harness.Harness.Role
		}
		if IsHarnessAuthorized(event, role) {
			authorized = append(authorized, harness)
		} else {
			denied = append(denied, harness)
		}
	}
	return authorized, denied
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
