package cli

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// discoverAgentSlugs discovers agent slugs using a three-tier fallback:
//
//  1. Harness wrapper files in the config repo (via DiscoverRemoteAgents)
//  2. config.yaml agents: block (legacy, emits deprecation warning)
//  3. Empty — caller is responsible for its own default-role fallback
//
// The ref parameter specifies the git ref for harness directory discovery.
// When an agent has a role but no slug, the slug is derived from appSet and
// the role using the standard naming convention.
func discoverAgentSlugs(ctx context.Context, client forge.Client, owner, configRepo, ref, appSet string, cfg *config.OrgConfig, printer *ui.Printer) []string {
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
			return slugs
		}
	}

	if cfg != nil && len(cfg.Agents) > 0 {
		printer.StepWarn("agent identity read from config.yaml agents: block; migrate to harness files with role/slug fields")
		var slugs []string
		seen := make(map[string]bool, len(cfg.Agents))
		for _, a := range cfg.Agents {
			slug := a.Slug
			if slug == "" && a.Role != "" {
				slug = appsetup.AppSlug(appSet, a.Role)
			}
			if slug != "" && !seen[slug] {
				seen[slug] = true
				slugs = append(slugs, slug)
			}
		}
		if len(slugs) > 0 {
			return slugs
		}
	}

	return nil
}
