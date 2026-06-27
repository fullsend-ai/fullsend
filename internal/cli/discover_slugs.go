package cli

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// discoverAgentSlugs discovers agent slugs from harness wrapper files in the
// config repo. Returns (nil, nil) when no slugs are found — the caller is
// responsible for its own default-role fallback.
//
// When DiscoverRemoteAgents returns a non-nil error and no usable agents are
// found, the error is propagated so callers can distinguish "no harness files
// exist" from "discovery failed due to a transient error."
//
// When an agent has a role but no slug, the slug is derived from appSet and
// the role using the standard naming convention.
func discoverAgentSlugs(ctx context.Context, client forge.Client, owner, configRepo, ref, appSet string, printer *ui.Printer) ([]string, error) {
	agents, err := harness.DiscoverRemoteAgents(ctx, client, owner, configRepo, ref)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("some harness files could not be read: %v", err))
	}
	if len(agents) > 0 {
		seen := make(map[string]bool, len(agents))
		var slugs []string
		for _, a := range agents {
			slug := a.Slug
			if slug == "" && a.Role != "" {
				slug = appsetup.AppSlug(appSet, a.Role)
			}
			if slug == "" {
				continue
			}
			if !seen[slug] {
				seen[slug] = true
				slugs = append(slugs, slug)
			}
		}
		if len(slugs) > 0 {
			return slugs, nil
		}
	}

	// No usable agents found. If discovery itself failed, propagate the error
	// so callers can distinguish "no harness files" from "transient failure."
	if err != nil {
		return nil, fmt.Errorf("harness discovery failed: %w", err)
	}

	return nil, nil
}
