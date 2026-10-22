package repos

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// gitlabRepoNamePattern matches GitLab project paths which may include
// arbitrarily nested group/subgroup segments (e.g. "group/subgroup/project").
var gitlabRepoNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+(/[a-zA-Z0-9_.-]+)+$`)

// ManifestEditConfig holds inputs for manifest remove operations.
type ManifestEditConfig struct {
	Manifest     *Manifest
	ManifestPath string
	DryRun       bool
}

// ManifestRemoveResult holds the outcome of removing repos from a manifest.
type ManifestRemoveResult struct {
	Removed []string
	Skipped []string
}

// carveOutForForge carves an explicit entry for name from the covering glob
// in the discovered forge section, or from the first covering glob across
// forges (GitHub before GitLab) when discovered is empty. It returns the new
// entry and the forge section it was added to.
func carveOutForForge(m *Manifest, name, discovered string) (*RepoEntry, string, error) {
	order := []string{ForgeGitHub, ForgeGitLab}
	if discovered != "" {
		order = []string{discovered}
	}
	for _, forgeName := range order {
		platform := m.PlatformFor(forgeName)
		if platform == nil {
			continue
		}
		carved, err := carveOutGlobEntry([]*PlatformConfig{platform}, name)
		if err != nil {
			return nil, "", err
		}
		if carved != nil {
			return carved, forgeName, nil
		}
	}
	return nil, "", nil
}

// carveOutGlobEntry appends an explicit entry for name copied from the first
// glob entry (GitHub before GitLab, matching resolution order) that covers
// it, and returns a pointer to the new entry. It returns nil when no glob
// covers name.
func carveOutGlobEntry(platforms []*PlatformConfig, name string) (*RepoEntry, error) {
	for _, platform := range platforms {
		if platform == nil {
			continue
		}
		for _, entry := range platform.Repos {
			if !isGlob(entry.Name) {
				continue
			}
			ok, err := matchesPattern(entry.Name, name)
			if err != nil {
				return nil, fmt.Errorf("invalid manifest repo pattern %q: %w", entry.Name, err)
			}
			if !ok {
				continue
			}
			explicit := entry
			explicit.Name = name
			explicit.Config = entry.Config.Clone()
			if entry.Vendor != nil {
				v := *entry.Vendor
				explicit.Vendor = &v
			}
			if entry.Signoff != nil {
				s := *entry.Signoff
				explicit.Signoff = &s
			}
			platform.Repos = append(platform.Repos, explicit)
			return &platform.Repos[len(platform.Repos)-1], nil
		}
	}
	return nil, nil
}

// RemoveFromManifest removes matching repo entries from all platform
// sections in the manifest. Patterns containing glob characters (*, ?, [)
// are matched against manifest entries using filepath.Match. Returns the
// result and the modified manifest.
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

	allEntries := cfg.Manifest.AllRepos()
	toRemove, err := matchManifestEntries(allEntries, repos)
	if err != nil {
		return nil, nil, err
	}

	result := &ManifestRemoveResult{}

	// Remove matching entries from each platform section.
	removePlatformEntries := func(platform *PlatformConfig) {
		if platform == nil {
			return
		}
		kept := make([]RepoEntry, 0, len(platform.Repos))
		for _, entry := range platform.Repos {
			if toRemove[entry.Name] {
				result.Removed = append(result.Removed, entry.Name)
			} else {
				kept = append(kept, entry)
			}
		}
		platform.Repos = kept
	}

	removePlatformEntries(cfg.Manifest.GitHub)
	removePlatformEntries(cfg.Manifest.GitLab)

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
	allEntries := manifest.AllRepos()
	matched, err := matchManifestEntries(allEntries, patterns)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(matched))
	for _, entry := range allEntries {
		if matched[entry.Name] {
			result = append(result, entry.Name)
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
			ok, err := matchesPattern(pattern, entry.Name)
			if err != nil {
				return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
			}
			if ok {
				matched[entry.Name] = true
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

// WriteManifest persists m to path in one write. `repos install` plans every
// manifest edit in memory and calls it once the layered safety comparison has
// passed, so a rejected install leaves the file untouched.
func WriteManifest(path string, m *Manifest) error {
	return writeManifest(path, m)
}

func writeManifest(path string, m *Manifest) error {
	if ManifestReadOnly(m, path) {
		return readOnlyManifestError(path)
	}
	data, err := MarshalWithHeader(m)
	if err != nil {
		return fmt.Errorf("marshalling manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	return nil
}
