package repos

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
func AddToManifest(ctx context.Context, cfg ManifestEditConfig, entries []RepoEntry, clients ForgeClientFactory, progress ProgressFunc) (*ManifestAddResult, *Manifest, error) {
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

	if clients != nil {
		for i := range entries {
			if isGlob(entries[i].Repo) || existing[strings.ToLower(entries[i].Repo)] {
				continue
			}
			parts := strings.SplitN(entries[i].Repo, "/", 2)
			if len(parts) != 2 {
				continue
			}
			entryForge := resolveField(entries[i].Forge, cfg.Manifest.Defaults.Forge, ForgeGitHub)
			fc, fcErr := clients.ConfigFor(entryForge)
			if fcErr != nil {
				progress(entries[i].Repo, "discover", fmt.Sprintf("forge client error: %v", fcErr))
				continue
			}
			state, err := ProbeRepoState(ctx, fc.Client, parts[0], parts[1], fc)
			if err != nil && !state.Installed {
				progress(entries[i].Repo, "discover", fmt.Sprintf("probe failed: %v", err))
				continue
			}
			if state.Installed {
				progress(entries[i].Repo, "discover", "existing installation detected")

				// Populate per-repo overrides from discovered state
				// when values differ from manifest defaults.
				if entryForge == ForgeGitHub {
					gh := cfg.Manifest.Forge.GitHub
					if state.MintURL != "" && state.MintURL != gh.MintURL && !entries[i].MintURL.Set {
						entries[i].MintURL = NullableString{Set: true, Value: state.MintURL}
					}
					if state.FullsendRef != "" && state.FullsendRef != gh.FullsendRef && !entries[i].FullsendRef.Set {
						entries[i].FullsendRef = NullableString{Set: true, Value: state.FullsendRef}
					}
				}
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

// ValidDefaultKeys lists the manifest keys that can be set via
// `repos set-default`. Order matches the help text.
var ValidDefaultKeys = []string{
	"defaults.allowed_remote_resources",
	"forge.github.url",
	"forge.github.mint_url",
	"forge.github.fullsend_ref",
	"forge.gitlab.url",
	"forge.gitlab.runner_tags",
}

// validDefaultKeySet is the lookup set for ValidDefaultKeys.
var validDefaultKeySet = func() map[string]bool {
	m := make(map[string]bool, len(ValidDefaultKeys))
	for _, k := range ValidDefaultKeys {
		m[k] = true
	}
	return m
}()

// SetDefault sets or removes a forge-level default in the manifest.
// An empty value removes the key. The file is created with version: 1
// if it does not exist.
func SetDefault(manifestPath, key, value string) error {
	if !validDefaultKeySet[key] {
		return fmt.Errorf("invalid key %q; valid keys: %s\nSee --help for details",
			key, strings.Join(ValidDefaultKeys, ", "))
	}

	// Load or create manifest.
	var m *Manifest
	data, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return fmt.Errorf("reading manifest: %w", readErr)
		}
		m = &Manifest{Version: 1}
	} else {
		var parsed Manifest
		if err := parseManifestBytes(data, &parsed); err != nil {
			return fmt.Errorf("parsing manifest: %w", err)
		}
		m = &parsed
	}

	// Validate value (unless removing).
	if value != "" {
		if err := validateDefaultValue(key, value); err != nil {
			return err
		}
	}

	// Apply.
	switch key {
	case "defaults.allowed_remote_resources":
		if value == "" {
			m.Defaults.AllowedRemoteResources = nil
		} else {
			parts := strings.Split(value, ",")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
			}
			m.Defaults.AllowedRemoteResources = parts
		}
	case "forge.github.url":
		m.Forge.GitHub.URL = value
	case "forge.github.mint_url":
		m.Forge.GitHub.MintURL = value
	case "forge.github.fullsend_ref":
		m.Forge.GitHub.FullsendRef = value
	case "forge.gitlab.url":
		m.Forge.GitLab.URL = value
	case "forge.gitlab.runner_tags":
		if value == "" {
			m.Forge.GitLab.RunnerTags = nil
		} else {
			parts := strings.Split(value, ",")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
			}
			m.Forge.GitLab.RunnerTags = parts
		}
	}

	return writeManifest(manifestPath, m)
}

// validateDefaultValue checks that value is appropriate for the given key.
func validateDefaultValue(key, value string) error {
	switch key {
	case "forge.github.url", "forge.github.mint_url", "forge.gitlab.url":
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("%s must be a valid HTTPS URL, got %q", key, value)
		}
		if key == "forge.github.url" || key == "forge.gitlab.url" {
			if err := rejectExtraneousURLParts(u, key); err != nil {
				return err
			}
		}
	case "forge.github.fullsend_ref":
		if !IsValidRef(value) {
			return fmt.Errorf("%s %q contains invalid characters; only alphanumeric, dot, underscore, and hyphen are allowed", key, value)
		}
	case "defaults.allowed_remote_resources":
		for _, raw := range strings.Split(value, ",") {
			v := strings.TrimSpace(raw)
			if v == "" {
				continue
			}
			u, err := url.Parse(v)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("defaults.allowed_remote_resources: %q must be a valid HTTPS URL", v)
			}
		}
	case "forge.gitlab.runner_tags":
		for _, raw := range strings.Split(value, ",") {
			if strings.TrimSpace(raw) == "" {
				return fmt.Errorf("forge.gitlab.runner_tags: tags must not be empty")
			}
		}
	}
	return nil
}
