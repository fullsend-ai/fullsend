package repos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

var repoNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`)

// ManifestEditConfig holds inputs for manifest add/remove operations.
type ManifestEditConfig struct {
	Manifest     *Manifest
	ManifestPath string
	DryRun       bool
}

// ManifestAddResult holds the outcome of adding repos to a manifest.
type ManifestAddResult struct {
	Added   []string
	Skipped []string
}

// ManifestRemoveResult holds the outcome of removing repos from a manifest.
type ManifestRemoveResult struct {
	Removed []string
	Skipped []string
}

// AddToManifest appends repo entries to the manifest, skipping duplicates.
// When client is non-nil, each non-glob repo is probed for existing
// installation state and per-repo overrides are populated where the
// discovered values differ from manifest defaults.
// Returns the result and the modified manifest. The manifest is written to
// disk only when ManifestPath is set and DryRun is false.
func AddToManifest(ctx context.Context, cfg ManifestEditConfig, entries []RepoEntry, client forge.Client, progress ProgressFunc) (*ManifestAddResult, *Manifest, error) {
	if cfg.Manifest == nil {
		return nil, nil, fmt.Errorf("manifest is required")
	}
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("at least one repo is required")
	}
	if progress == nil {
		progress = func(_, _, _ string) {}
	}

	existing := make(map[string]bool, len(cfg.Manifest.Repos))
	for _, e := range cfg.Manifest.Repos {
		existing[strings.ToLower(e.Repo)] = true
	}

	for _, entry := range entries {
		if !isGlob(entry.Repo) && !repoNamePattern.MatchString(entry.Repo) {
			return nil, nil, fmt.Errorf("invalid repo name %q: expected owner/repo format", entry.Repo)
		}
	}

	if client != nil {
		for i := range entries {
			if isGlob(entries[i].Repo) || existing[strings.ToLower(entries[i].Repo)] {
				continue
			}
			parts := strings.SplitN(entries[i].Repo, "/", 2)
			if len(parts) != 2 {
				continue
			}
			state, err := ProbeRepoState(ctx, client, parts[0], parts[1])
			if err != nil && !state.Installed {
				progress(entries[i].Repo, "discover", fmt.Sprintf("probe failed: %v", err))
				continue
			}
			if !state.Installed {
				continue
			}
			progress(entries[i].Repo, "discover", "existing installation detected")
			if state.InferenceRegion != "" && state.InferenceRegion != cfg.Manifest.Defaults.InferenceRegion {
				entries[i].InferenceRegion = NullableString{Set: true, Value: state.InferenceRegion}
			}
			if state.FullsendRef != "" && state.FullsendRef != cfg.Manifest.Defaults.FullsendRef {
				entries[i].FullsendRef = NullableString{Set: true, Value: state.FullsendRef}
			}
		}
	}

	result := &ManifestAddResult{}
	var toAdd []RepoEntry

	for _, entry := range entries {
		if existing[strings.ToLower(entry.Repo)] {
			result.Skipped = append(result.Skipped, entry.Repo)
			progress(entry.Repo, "manifest", "Already in manifest, skipping")
			continue
		}
		result.Added = append(result.Added, entry.Repo)
		toAdd = append(toAdd, entry)
		existing[strings.ToLower(entry.Repo)] = true
	}

	if len(toAdd) == 0 {
		return result, cfg.Manifest, nil
	}

	if cfg.DryRun {
		for _, entry := range toAdd {
			progress(entry.Repo, "dry-run", "Would add to manifest")
		}
		return result, cfg.Manifest, nil
	}

	cfg.Manifest.Repos = append(cfg.Manifest.Repos, toAdd...)

	if cfg.ManifestPath != "" {
		if err := writeManifest(cfg.ManifestPath, cfg.Manifest); err != nil {
			return nil, nil, err
		}
	}

	for _, entry := range toAdd {
		progress(entry.Repo, "manifest", "Added to manifest")
	}

	return result, cfg.Manifest, nil
}

// RemoveFromManifest removes matching repo entries from the manifest. Patterns
// containing glob characters (*, ?, [) are matched against manifest entries
// using filepath.Match. Returns the result and the modified manifest.
func RemoveFromManifest(cfg ManifestEditConfig, repos []string, progress ProgressFunc) (*ManifestRemoveResult, *Manifest, error) {
	if cfg.Manifest == nil {
		return nil, nil, fmt.Errorf("manifest is required")
	}
	if len(repos) == 0 {
		return nil, nil, fmt.Errorf("at least one repo is required")
	}
	if progress == nil {
		progress = func(_, _, _ string) {}
	}

	toRemove, err := matchManifestEntries(cfg.Manifest.Repos, repos)
	if err != nil {
		return nil, nil, err
	}

	result := &ManifestRemoveResult{}
	kept := make([]RepoEntry, 0, len(cfg.Manifest.Repos))
	for _, entry := range cfg.Manifest.Repos {
		if toRemove[entry.Repo] {
			result.Removed = append(result.Removed, entry.Repo)
		} else {
			kept = append(kept, entry)
		}
	}

	for _, pattern := range repos {
		matched := false
		for _, r := range result.Removed {
			if ok, _ := matchesPattern(pattern, r); ok {
				matched = true
				break
			}
		}
		if !matched && !isGlob(pattern) {
			result.Skipped = append(result.Skipped, pattern)
			progress(pattern, "manifest", "Not found in manifest")
		}
	}

	if len(result.Removed) == 0 {
		return result, cfg.Manifest, nil
	}

	if cfg.DryRun {
		for _, r := range result.Removed {
			progress(r, "dry-run", "Would remove from manifest")
		}
		return result, cfg.Manifest, nil
	}

	cfg.Manifest.Repos = kept

	if cfg.ManifestPath != "" {
		if err := writeManifest(cfg.ManifestPath, cfg.Manifest); err != nil {
			return nil, nil, err
		}
	}

	for _, r := range result.Removed {
		progress(r, "manifest", "Removed from manifest")
	}

	return result, cfg.Manifest, nil
}

// MatchManifestRepos returns the list of repo names from the manifest that
// match any of the given patterns. Used by CLI commands to resolve positional
// args (which may contain globs) against manifest entries.
func MatchManifestRepos(manifest *Manifest, patterns []string) ([]string, error) {
	matched, err := matchManifestEntries(manifest.Repos, patterns)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(matched))
	for _, entry := range manifest.Repos {
		if matched[entry.Repo] {
			result = append(result, entry.Repo)
		}
	}
	return result, nil
}

// matchManifestEntries builds a set of manifest repo names that match any of
// the given patterns (exact or glob).
func matchManifestEntries(entries []RepoEntry, patterns []string) (map[string]bool, error) {
	matched := make(map[string]bool)
	for _, entry := range entries {
		for _, pattern := range patterns {
			ok, err := matchesPattern(pattern, entry.Repo)
			if err != nil {
				return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
			}
			if ok {
				matched[entry.Repo] = true
				break
			}
		}
	}
	return matched, nil
}

func matchesPattern(pattern, name string) (bool, error) {
	if strings.EqualFold(pattern, name) {
		return true, nil
	}
	if !isGlob(pattern) {
		return false, nil
	}
	return filepath.Match(strings.ToLower(pattern), strings.ToLower(name))
}

func isGlob(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

func writeManifest(path string, m *Manifest) error {
	data, err := MarshalWithHeader(m)
	if err != nil {
		return fmt.Errorf("marshalling manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	return nil
}
