package harness

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// DiscoverRemoteAgents discovers agent identity (role, slug) from harness files
// in a remote config repo via the forge API. It is the remote counterpart of
// DiscoverAgents, which reads from the local filesystem.
//
// Files where both role and slug are empty are skipped. Per-file errors (parse
// failures, GetFileContentAtRef failures) are collected into a multi-error;
// valid files are still returned alongside the error.
//
// Results are sorted by Role, then by Filename for deterministic output.
// Returns (nil, nil) when the harness/ directory does not exist.
func DiscoverRemoteAgents(ctx context.Context, client forge.Client, owner, repo, ref string) ([]AgentInfo, error) {
	entries, err := client.ListDirectoryContents(ctx, owner, repo, "harness", ref, false)
	if forge.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing harness directory: %w", err)
	}

	var agents []AgentInfo
	var errs []error

	for _, e := range entries {
		if e.Type != "file" {
			continue
		}
		name := path.Base(e.Path)
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		data, err := client.GetFileContentAtRef(ctx, owner, repo, "harness/"+name, ref)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}

		h, err := parseRaw(data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}

		if h.Role == "" && h.Slug == "" {
			continue
		}

		agents = append(agents, AgentInfo{
			Role:     h.Role,
			Slug:     h.Slug,
			Filename: name,
		})
	}

	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Role != agents[j].Role {
			return agents[i].Role < agents[j].Role
		}
		return agents[i].Filename < agents[j].Filename
	})

	return agents, errors.Join(errs...)
}
