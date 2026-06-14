package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentInfo holds the identity of an agent discovered from a harness file.
type AgentInfo struct {
	Role     string // from the harness role: field
	Slug     string // from the harness slug: field
	Filename string // e.g. "triage.yaml"
	Path     string // absolute path to the harness file
}

// DiscoverAgents scans dir for harness YAML files and returns agent identity
// (role, slug) from each. Files where both role and slug are empty are skipped.
// Parse errors on individual files are collected into a multi-error; valid
// files are still returned alongside the error.
//
// Results are sorted by Role, then by Filename for deterministic output.
// Only top-level role/slug fields are read — base chains are not resolved.
// This is correct because generated wrappers set role/slug at the top level.
func DiscoverAgents(dir string) ([]AgentInfo, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving absolute path: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading harness directory: %w", err)
	}

	var agents []AgentInfo
	var errs []error

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		absPath := filepath.Join(dir, name)
		h, err := LoadRaw(absPath)
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
			Path:     absPath,
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
