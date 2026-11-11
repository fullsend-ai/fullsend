package repos

import (
	"context"
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
// are matched against manifest entries using filepath.Match. Entries are
// matched in cfg.Manifest; when ManifestPath is a writable regular file, the
// removal is applied to the file as it is at write time, so edits made after
// cfg.Manifest was loaded are kept. Returns the result and the modified
// manifest (the copy that was written, when one was).
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

	result := &ManifestRemoveResult{
		Removed: removeManifestEntries(cfg.Manifest, toRemove),
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

	updated := cfg.Manifest
	if cfg.ManifestPath != "" {
		// The caller may have loaded cfg.Manifest long before this write
		// (uninstall loads it before teardown). Apply the removal to the
		// file as it is now, so edits another command made in the meantime
		// are kept.
		if !ManifestReadOnly(cfg.Manifest, cfg.ManifestPath) {
			current, err := reloadManifestForWrite(cfg.ManifestPath)
			if err != nil {
				return nil, nil, err
			}
			if current != nil {
				removeManifestEntries(current, toRemove)
				updated = current
			}
		}
		if err := writeManifest(cfg.ManifestPath, updated); err != nil {
			return nil, nil, err
		}
	}

	for _, r := range result.Removed {
		progress(r, "manifest", "Removed from manifest")
	}

	return result, updated, nil
}

// removeManifestEntries removes the entries named in names from every
// platform section of m and returns the names it removed. Names are compared
// case-insensitively, as repo patterns are, so an entry whose capitalization
// changed after names was built is still removed.
func removeManifestEntries(m *Manifest, names map[string]bool) []string {
	folded := make(map[string]bool, len(names))
	for name, ok := range names {
		if ok {
			folded[strings.ToLower(name)] = true
		}
	}
	var removed []string
	for _, platform := range []*PlatformConfig{m.GitHub, m.GitLab} {
		if platform == nil {
			continue
		}
		kept := make([]RepoEntry, 0, len(platform.Repos))
		for _, entry := range platform.Repos {
			if folded[strings.ToLower(entry.Name)] {
				removed = append(removed, entry.Name)
			} else {
				kept = append(kept, entry)
			}
		}
		platform.Repos = kept
	}
	return removed
}

// reloadManifestForWrite reads the local manifest at path again, as it is
// on disk now, and validates it as uninstall validates the manifest it
// loads. It returns nil when path is not a regular file (for example a pipe
// that cannot be read a second time); the caller then writes the copy it
// already holds.
func reloadManifestForWrite(path string) (*Manifest, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil
	}
	// A local path is read without the context; it only bounds HTTPS fetches.
	current, err := LoadManifest(context.Background(), path)
	if err != nil {
		return nil, fmt.Errorf("re-reading manifest before write: %w", err)
	}
	if err := current.ValidateStructure(); err != nil {
		return nil, fmt.Errorf("manifest %q changed and no longer validates; not writing: %w", path, err)
	}
	return current, nil
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
