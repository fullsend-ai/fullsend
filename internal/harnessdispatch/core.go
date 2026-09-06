package harnessdispatch

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

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

// Dispatch evaluates authorization, kill switch, harness triggers, and returns execution refs.
// Returns empty slice (not error) when denied or no matches.
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

	// Compute the effective role for the auth gate without mutating the
	// caller's event. The OWNERS-upgraded role is used only for
	// IsAuthorized; downstream CEL evaluation sees the original
	// collaborator-API role.
	repoRoot := filepath.Dir(opts.ConfigDir)
	effectiveRole := opts.Event.Actor.Role
	if dirCfg.AuthorizationOwnersFile() && opts.Event.Actor.ID != "" {
		ownersPath := filepath.Join(repoRoot, "OWNERS")
		aliasesPath := filepath.Join(repoRoot, "OWNERS_ALIASES")
		role, err := owners.Resolve(ownersPath, aliasesPath, opts.Event.Actor.ID)
		if err != nil {
			log.Printf("harness dispatch: OWNERS resolution failed for %s: %v", opts.Event.Actor.ID, err)
		} else if role != owners.None {
			effectiveRole = owners.MapToActorRole(role, effectiveRole)
			log.Printf("harness dispatch: OWNERS file resolved user %s as %s", opts.Event.Actor.ID, role)
		}
	}

	authCheck := *opts.Event
	authCheck.Actor.Role = effectiveRole
	if !IsAuthorized(&authCheck) {
		return nil, nil
	}

	candidates, err := ListTriggeredHarnesses(ctx, opts.ConfigDir, dirCfg, opts.FetchPolicy)
	if err != nil {
		return nil, err
	}

	matched, err := MatchHarnesses(candidates, opts.Event)
	if err != nil {
		return nil, err
	}

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
