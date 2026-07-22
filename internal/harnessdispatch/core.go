package harnessdispatch

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// Options configures a dispatch run.
type Options struct {
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

	dirCfg, err := config.LoadFromDir(opts.ConfigDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return nil, err
	}
	if dirCfg.KillSwitch {
		return nil, nil
	}

	if !IsAuthorized(opts.Event) {
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
