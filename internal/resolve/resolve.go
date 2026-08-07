package resolve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/skill"
)

const (
	DefaultMaxDepth     = 10
	DefaultMaxResources = 50
)

// Dependency records a single URL that was resolved to a local cache path.
type Dependency struct {
	Field     string
	URL       string
	LocalPath string
	SHA256    string
	FetchedAt time.Time
	CacheHit  bool
	Type      string // "file" or "directory"
	Warning   string // non-fatal warning about this dependency
}

// ResolvedProfile is a profile definition resolved from a URL or local path
// and validated to have a non-empty id field.
type ResolvedProfile struct {
	ID        string
	LocalPath string
	FromURL   bool // true when resolved from a URL (including lock-file reconstruction)
}

// ResolvedProvider is a provider definition resolved from a URL or local path.
type ResolvedProvider struct {
	Def       harness.ProviderDef
	LocalPath string
	FromURL   bool // true when resolved from a URL (including lock-file reconstruction)
}

// ResolveResult contains all outputs from harness resolution.
type ResolveResult struct {
	Deps      []Dependency
	Profiles  []ResolvedProfile
	Providers []ResolvedProvider
	Warnings  []string
}

// ProfileYAML is the subset of an openshell profile definition needed
// for validation. The full profile schema is openshell's concern.
type ProfileYAML struct {
	ID string `yaml:"id"`
}

// validIdentifier matches the same format as harness.validProviderName:
// alphanumeric, underscore, and hyphen. Applied to URL-fetched profile
// ids and provider names/types to close the trust-boundary gap between
// local and URL-sourced definitions.
var validIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ValidIdentifier reports whether s is a safe identifier for use in CLI
// arguments and gateway operations: starts with an alphanumeric, then
// alphanumerics, underscores, or hyphens.
func ValidIdentifier(s string) bool { return validIdentifier.MatchString(s) }

// ParseProfileID extracts and validates the profile id from YAML content.
func ParseProfileID(data []byte) (string, error) {
	var prof ProfileYAML
	if err := yaml.Unmarshal(data, &prof); err != nil {
		return "", fmt.Errorf("parsing profile YAML: %w", err)
	}
	if prof.ID == "" {
		return "", fmt.Errorf("profile has no id field")
	}
	if !validIdentifier.MatchString(prof.ID) {
		return "", fmt.Errorf("profile id %q contains invalid characters (must match [a-zA-Z0-9_-]+)", prof.ID)
	}
	return prof.ID, nil
}

// CollectProfileIDs scans a profiles directory and returns the id from each
// YAML file. Returns nil (not an error) if the directory does not exist.
func CollectProfileIDs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading profiles directory %s: %w", dir, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading profile %s: %w", e.Name(), err)
		}
		id, err := ParseProfileID(data)
		if err != nil {
			return nil, fmt.Errorf("parsing profile %s: %w", e.Name(), err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// envVarPattern requires the entire value to be a single ${VAR} reference.
// Compound expressions like "${HOST}:${PORT}" are intentionally flagged —
// credential values in URL-fetched providers should be a single env var
// per field, not interpolated strings.
var envVarPattern = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// WarnLiteralCredentials returns a warning string if any credential value
// does not look like a ${VAR} reference. Returns empty string if all OK.
func WarnLiteralCredentials(providerName string, creds map[string]string) string {
	var bad []string
	for k, v := range creds {
		if v != "" && !envVarPattern.MatchString(v) {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	sort.Strings(bad)
	return fmt.Sprintf(
		"provider %q: credential(s) %s do not look like ${VAR} references — ensure secrets are not embedded in URL-fetched provider definitions",
		providerName, strings.Join(bad, ", "))
}

// parseProviderDef unmarshals a provider definition from YAML content,
// validates required fields, and checks for literal credentials.
func parseProviderDef(content []byte, index int, source string) (harness.ProviderDef, string, error) {
	var def harness.ProviderDef
	if err := yaml.Unmarshal(content, &def); err != nil {
		return harness.ProviderDef{}, "", fmt.Errorf("parsing provider %s: %w", source, err)
	}
	if def.Name == "" {
		return harness.ProviderDef{}, "", fmt.Errorf("providers[%d]: provider name is required in %s", index, source)
	}
	if !validIdentifier.MatchString(def.Name) {
		return harness.ProviderDef{}, "", fmt.Errorf("providers[%d]: provider name %q contains invalid characters (must match [a-zA-Z0-9][a-zA-Z0-9_-]*) in %s", index, def.Name, source)
	}
	if def.Type == "" {
		return harness.ProviderDef{}, "", fmt.Errorf("providers[%d]: provider type is required in %s", index, source)
	}
	if !validIdentifier.MatchString(def.Type) {
		return harness.ProviderDef{}, "", fmt.Errorf("providers[%d]: provider type %q contains invalid characters (must match [a-zA-Z0-9][a-zA-Z0-9_-]*) in %s", index, def.Type, source)
	}
	w := WarnLiteralCredentials(def.Name, def.Credentials)
	return def, w, nil
}

// isContainedPath reports whether the absolute path p is inside root.
// Used as defense-in-depth when reading local profile/provider files —
// upstream guards (ResolveRelativeTo, validateBaseRelPath) already constrain
// paths, but this check catches bugs in those guards.
// When the path exists on disk, resolves symlinks to prevent escape.
func isContainedPath(p, root string) bool {
	if root == "" {
		return false
	}
	cleaned := filepath.Clean(p)
	rootCleaned := filepath.Clean(root)
	if cleaned != rootCleaned && !strings.HasPrefix(cleaned, rootCleaned+string(filepath.Separator)) {
		return false
	}
	realPath, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return true // path doesn't exist yet; syntactic check passed
	}
	realRoot, err := filepath.EvalSymlinks(rootCleaned)
	if err != nil {
		return true // root doesn't exist; syntactic check passed
	}
	return realPath == realRoot || strings.HasPrefix(realPath, realRoot+string(filepath.Separator))
}

// isCachePath reports whether p is inside the .fullsend-cache directory.
// Duplicates compose.isFullsendCachePath intentionally to avoid an import cycle.
func isCachePath(p, workspaceRoot string) bool {
	if !filepath.IsAbs(p) || workspaceRoot == "" {
		return false
	}
	cacheDir := filepath.Join(workspaceRoot, ".fullsend-cache")
	rel, err := filepath.Rel(cacheDir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ResolveOpts controls how URL-referenced resources are resolved.
type ResolveOpts struct {
	WorkspaceRoot string
	FetchPolicy   fetch.FetchPolicy
	TraceID       string
	AuditLogPath  string

	// TreeFetcher fetches all files under a path in a remote repository.
	// When nil, defaults to gitfetch.FetchTree (git sparse checkout).
	TreeFetcher gitfetch.TreeFetchFunc

	// GitToken is an optional token for authenticating git fetches.
	// Empty means unauthenticated (sufficient for public repos).
	GitToken string

	// MaxDepth controls transitive dependency resolution depth.
	// 0 disables transitive resolution (Phase 1 behavior).
	// <0 uses DefaultMaxDepth (10).
	//
	// MaxResources uses different semantics: <=0 always uses
	// DefaultMaxResources (50). The asymmetry exists because MaxDepth=0
	// is a meaningful "disable" value, while MaxResources=0 ("allow zero
	// resources") would prevent even non-transitive URL resolution.
	MaxDepth     int
	MaxResources int
}

type resolveState struct {
	inProgress    map[string]bool
	resolved      map[string]Dependency
	inDeps        map[string]bool
	resourceCount int
	deps          []Dependency
	warnings      []string
	maxDepth      int
	maxResources  int
}

// ResolveHarness resolves URL-referenced declarative fields (Agent, Policy,
// Skills, Plugins, Profiles, Providers) in the harness to local cache paths.
// Local paths are left unchanged. The harness is modified in place: URL fields
// are replaced with cache paths, and h.Skills may grow to include transitively
// resolved skill dependencies. Returns a ResolveResult containing all resolved
// dependencies, profiles, and providers.
//
// Skills are directories: when a skill field is a URL, the resolver uses
// git sparse checkout (via TreeFetcher / gitfetch.FetchTree) to fetch the
// directory tree and cache it locally. Only URLs pointing to supported forges
// (github.com) are accepted for skills. Agents and policies remain single files.
//
// Profiles and Providers: URL-referenced entries are fetched, validated, and
// returned in the result. URL-based providers are removed from h.Providers,
// leaving only local provider names.
//
// Skills with dependencies: frontmatter are recursively resolved up to
// MaxDepth levels. Diamond dependencies are deduplicated; cycles are rejected.
// Set MaxDepth to 0 to disable transitive resolution. Negative values use
// DefaultMaxDepth (10).
//
// Trusting a skill means trusting its entire transitive dependency closure:
// a skill's frontmatter can declare relative references that resolve to
// different paths on the same allowed domain. All transitive deps are still
// validated against allowed_remote_resources and SHA256 integrity hashes.
//
// The default limits (depth=10, resources=50) bound worst-case resolution.
// CI environments with untrusted harnesses should set tighter limits.
// See ADR-0038 for the security model and trust semantics.
func ResolveHarness(ctx context.Context, h *harness.Harness, opts ResolveOpts) (ResolveResult, error) {
	maxDepth := opts.MaxDepth
	if maxDepth < 0 {
		maxDepth = DefaultMaxDepth
	}
	maxResources := opts.MaxResources
	if maxResources <= 0 {
		maxResources = DefaultMaxResources
	}

	state := &resolveState{
		inProgress:   make(map[string]bool),
		resolved:     make(map[string]Dependency),
		inDeps:       make(map[string]bool),
		maxDepth:     maxDepth,
		maxResources: maxResources,
	}

	recurse := maxDepth > 0

	if h.Agent != "" && harness.IsURL(h.Agent) {
		dep, localPath, err := resolveFileURL(ctx, "agent", h.Agent, h, opts, state)
		if err != nil {
			return ResolveResult{}, fmt.Errorf("resolving agent: %w", err)
		}
		h.Agent = localPath
		state.appendDependency(dep)
	}

	if h.Policy != "" && harness.IsURL(h.Policy) {
		dep, localPath, err := resolveFileURL(ctx, "policy", h.Policy, h, opts, state)
		if err != nil {
			return ResolveResult{}, fmt.Errorf("resolving policy: %w", err)
		}
		h.Policy = localPath
		state.appendDependency(dep)
	}

	for i, s := range h.Skills {
		if harness.IsURL(s) {
			dep, localPath, err := resolveSkillDirURL(ctx, fmt.Sprintf("skills[%d]", i), s, h, opts, state, recurse, 0)
			if err != nil {
				return ResolveResult{}, fmt.Errorf("resolving skills[%d]: %w", i, err)
			}
			if !state.inDeps[dep.URL] {
				h.Skills[i] = localPath
			} else {
				h.Skills[i] = ""
			}
			state.appendDependency(dep)
		}
	}

	// Remove entries that were already appended transitively.
	filtered := h.Skills[:0]
	for _, s := range h.Skills {
		if s != "" {
			filtered = append(filtered, s)
		}
	}
	h.Skills = filtered

	// Resolve plugins — same directory fetch as skills, but without
	// transitive dependency resolution (plugins have no SKILL.md frontmatter).
	for i, p := range h.Plugins {
		if harness.IsURL(p) {
			dep, localPath, err := resolveSkillDirURL(ctx, fmt.Sprintf("plugins[%d]", i), p, h, opts, state, false, 0)
			if err != nil {
				return ResolveResult{}, fmt.Errorf("resolving plugins[%d]: %w", i, err)
			}

			// Validate the plugin basename from the forge URL path, not
			// from the post-symlink local path. CacheNamedSymlink falls
			// back to "tree" for empty/reserved path components, which
			// would silently pass ValidPluginBasename. Checking the URL
			// path catches repo-root URLs (Path=="") and reserved names.
			forgeInfo, parseErr := forge.ParseForgeURL(dep.URL)
			if parseErr == nil {
				urlBase := filepath.Base(forgeInfo.Path)
				if forgeInfo.Path == "" || !harness.ValidPluginBasename(urlBase) {
					return ResolveResult{}, fmt.Errorf("plugins[%d]: URL path %q does not end in a valid plugin basename (allowed: a-z, A-Z, 0-9, _, -)", i, forgeInfo.Path)
				}
			}

			// Always assign — plugins have no transitive re-append, so
			// blanking the slot (as skills do for dedup) would drop the plugin.
			h.Plugins[i] = localPath
			state.appendDependency(dep)

			// Make plugin files executable. The cache writes all files
			// as 0600 (via atomicWrite), but plugins may contain scripts
			// or MCP server binaries that need the executable bit.
			// NOTE: this mutates the shared content-addressed cache — files
			// in the same tree referenced as skills will also become 0755.
			if err := chmodPluginDir(h.Plugins[i]); err != nil {
				return ResolveResult{}, fmt.Errorf("setting plugin permissions for plugins[%d]: %w", i, err)
			}
		}
	}

	// De-duplicate plugins by resolved path (e.g. two slots referencing
	// the same URL resolve to identical local paths).
	seen := make(map[string]bool, len(h.Plugins))
	deduped := h.Plugins[:0]
	for _, p := range h.Plugins {
		if !seen[p] {
			seen[p] = true
			deduped = append(deduped, p)
		}
	}
	h.Plugins = deduped

	// Resolve profiles: URL entries are fetched and cached; local paths
	// (from ResolveRelativeTo or base composition cache) are used directly.
	var profiles []ResolvedProfile
	for i, p := range h.OpenShellProfiles() {
		var localPath string
		if harness.IsURL(p) {
			dep, lp, err := resolveFileURL(ctx, fmt.Sprintf("openshell.profiles[%d]", i), p, h, opts, state)
			if err != nil {
				return ResolveResult{}, fmt.Errorf("resolving openshell.profiles[%d]: %w", i, err)
			}
			localPath = lp

			content, err := os.ReadFile(localPath)
			if err != nil {
				return ResolveResult{}, fmt.Errorf("reading resolved profile %s: %w", localPath, err)
			}
			id, err := ParseProfileID(content)
			if err != nil {
				return ResolveResult{}, fmt.Errorf("openshell.profiles[%d]: %w (from %s)", i, err, p)
			}

			// Create a named symlink so openshell sees a .yaml extension
			// instead of the extensionless cache-internal "content" filename.
			localPath, err = fetch.CacheNamedSymlink(localPath, id+".yaml")
			if err != nil {
				return ResolveResult{}, fmt.Errorf("naming cached profile for openshell.profiles[%d]: %w", i, err)
			}
			dep.LocalPath = localPath
			// Keep the fetch-dedup cache in sync with the renamed path, so a
			// second reference to the same profile URL (elsewhere in the same
			// harness) doesn't resolve to the pre-rename cache path.
			state.resolved[dep.URL] = dep

			state.appendDependency(dep)
			profiles = append(profiles, ResolvedProfile{ID: id, LocalPath: localPath, FromURL: true})
		} else {
			localPath = p

			if !filepath.IsAbs(localPath) {
				return ResolveResult{}, fmt.Errorf("openshell.profiles[%d]: non-URL profile %q must be an absolute path", i, p)
			}
			if !isContainedPath(localPath, opts.WorkspaceRoot) {
				return ResolveResult{}, fmt.Errorf("openshell.profiles[%d]: path %q is outside workspace root", i, localPath)
			}
			content, err := os.ReadFile(localPath)
			if err != nil {
				return ResolveResult{}, fmt.Errorf("reading profile %s: %w", localPath, err)
			}
			id, err := ParseProfileID(content)
			if err != nil {
				return ResolveResult{}, fmt.Errorf("openshell.profiles[%d]: %w (from %s)", i, err, localPath)
			}

			ext := strings.ToLower(filepath.Ext(localPath))
			if ext != ".yaml" && ext != ".yml" && isCachePath(localPath, opts.WorkspaceRoot) {
				localPath, err = fetch.CacheNamedSymlink(localPath, id+".yaml")
				if err != nil {
					return ResolveResult{}, fmt.Errorf("naming cached profile for openshell.profiles[%d]: %w", i, err)
				}
			}
			profiles = append(profiles, ResolvedProfile{ID: id, LocalPath: localPath})
		}
	}

	// Resolve providers: URL entries are fetched and cached; absolute-path
	// entries (from ResolveRelativeTo or base composition cache) are read
	// directly; bare provider names are kept in h.Providers for LoadProviderDefs.
	var resolvedProviders []ResolvedProvider
	remaining := h.Providers[:0]
	for i, p := range h.Providers {
		if !harness.IsURL(p) {
			if filepath.IsAbs(p) {
				if !isContainedPath(p, opts.WorkspaceRoot) {
					return ResolveResult{}, fmt.Errorf("providers[%d]: path %q is outside workspace root", i, p)
				}
				content, err := os.ReadFile(p)
				if err != nil {
					return ResolveResult{}, fmt.Errorf("reading provider %s: %w", p, err)
				}
				def, w, err := parseProviderDef(content, i, p)
				if err != nil {
					return ResolveResult{}, err
				}
				if w != "" {
					state.warnings = append(state.warnings, w)
				}
				resolvedProviders = append(resolvedProviders, ResolvedProvider{Def: def, LocalPath: p})
				continue
			}
			remaining = append(remaining, p)
			continue
		}
		dep, localPath, err := resolveFileURL(ctx, fmt.Sprintf("providers[%d]", i), p, h, opts, state)
		if err != nil {
			return ResolveResult{}, fmt.Errorf("resolving providers[%d]: %w", i, err)
		}

		content, err := os.ReadFile(localPath)
		if err != nil {
			return ResolveResult{}, fmt.Errorf("reading resolved provider %s: %w", localPath, err)
		}
		def, w, err := parseProviderDef(content, i, dep.URL)
		if err != nil {
			return ResolveResult{}, err
		}
		if w != "" {
			dep.Warning = w
		}
		state.appendDependency(dep)
		resolvedProviders = append(resolvedProviders, ResolvedProvider{Def: def, LocalPath: localPath, FromURL: true})
	}
	h.Providers = remaining
	if h.OpenShell != nil {
		h.OpenShell.Profiles = nil
	}

	return ResolveResult{
		Deps:      state.deps,
		Profiles:  profiles,
		Providers: resolvedProviders,
		Warnings:  state.warnings,
	}, nil
}

func (s *resolveState) appendDependency(dep Dependency) {
	if s.inDeps[dep.URL] {
		return
	}
	s.inDeps[dep.URL] = true
	s.deps = append(s.deps, dep)
}

// resolveFileURL fetches a single file from a URL and caches it.
// Used for agents, policies, and other single-file resources.
func resolveFileURL(ctx context.Context, field, rawURL string, h *harness.Harness,
	opts ResolveOpts, state *resolveState,
) (Dependency, string, error) {
	cleanURL, expectedHash, hasHash := harness.ParseIntegrityHash(rawURL)
	if !hasHash {
		return Dependency{}, "", fmt.Errorf("%s: URL must include #sha256=... integrity hash", field)
	}
	if !strings.HasPrefix(cleanURL, "https://") {
		return Dependency{}, "", fmt.Errorf("%s: URL scheme must be https: %s", field, cleanURL)
	}

	if dep, ok := state.resolved[cleanURL]; ok {
		if dep.SHA256 != expectedHash {
			return Dependency{}, "", fmt.Errorf(
				"%s: URL %s has conflicting integrity hashes: previously resolved with %s, now referenced with %s",
				field, cleanURL, dep.SHA256, expectedHash)
		}
		depCopy := dep
		depCopy.Field = field
		return depCopy, dep.LocalPath, nil
	}
	if state.inProgress[cleanURL] {
		return Dependency{}, "", fmt.Errorf("%s: circular dependency detected for %s", field, cleanURL)
	}
	if state.resourceCount >= state.maxResources {
		return Dependency{}, "", fmt.Errorf("%s: exceeded maximum resource count of %d for %s", field, state.maxResources, cleanURL)
	}

	state.inProgress[cleanURL] = true
	defer delete(state.inProgress, cleanURL)
	state.resourceCount++

	allowedBy := h.MatchingAllowedPrefix(cleanURL)
	if allowedBy == "" {
		return Dependency{}, "", fmt.Errorf("%s: URL %q is not in allowed_remote_resources", field, cleanURL)
	}

	content, entry, err := fetch.CacheGet(opts.WorkspaceRoot, expectedHash)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("cache lookup for %s: %w", field, err)
	}

	cacheHit := content != nil

	if content == nil {
		content, err = fetch.FetchURL(ctx, cleanURL, opts.FetchPolicy)
		if err != nil {
			return Dependency{}, "", fmt.Errorf("fetching %s from %s: %w", field, cleanURL, err)
		}

		actualHash := fetch.ComputeSHA256(content)
		if actualHash != expectedHash {
			return Dependency{}, "", fmt.Errorf("%s: integrity check failed for %s: expected %s, got %s", field, cleanURL, expectedHash, actualHash)
		}

		if err := fetch.CachePut(opts.WorkspaceRoot, cleanURL, content); err != nil {
			return Dependency{}, "", fmt.Errorf("caching %s: %w", field, err)
		}
	}

	cachePath, err := fetch.CachePath(opts.WorkspaceRoot, expectedHash)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("computing cache path for %s: %w", field, err)
	}
	localPath := filepath.Join(cachePath, "content")

	fetchedAt := time.Now().UTC()
	if entry != nil {
		fetchedAt = entry.FetchTime
	}

	if opts.AuditLogPath != "" {
		if err := fetch.AppendFetchAudit(opts.AuditLogPath, fetch.FetchAuditEntry{
			TraceID:   opts.TraceID,
			FetchTime: fetchedAt,
			URL:       cleanURL,
			SHA256:    expectedHash,
			FetchType: "static",
			AllowedBy: allowedBy,
			CacheHit:  cacheHit,
		}); err != nil {
			return Dependency{}, "", fmt.Errorf("writing fetch audit log: %w", err)
		}
	}

	dep := Dependency{
		Field:     field,
		URL:       cleanURL,
		LocalPath: localPath,
		SHA256:    expectedHash,
		FetchedAt: fetchedAt,
		CacheHit:  cacheHit,
		Type:      "file",
	}

	state.resolved[cleanURL] = dep

	return dep, localPath, nil
}

// resolveSkillDirURL fetches a skill directory from a supported forge and caches
// the reconstructed directory tree. Skills are always directories containing at
// minimum a SKILL.md file plus optional companion files (scripts/, sub-agents/).
// Only URLs pointing to supported forges are accepted; non-forge HTTPS URLs are
// rejected because HTTP has no standard directory listing mechanism.
func resolveSkillDirURL(ctx context.Context, field, rawURL string, h *harness.Harness,
	opts ResolveOpts, state *resolveState, recurse bool, depth int,
) (Dependency, string, error) {
	cleanURL, expectedHash, hasHash := harness.ParseIntegrityHash(rawURL)
	if !hasHash {
		return Dependency{}, "", fmt.Errorf("%s: URL must include #sha256=... integrity hash", field)
	}
	if !strings.HasPrefix(cleanURL, "https://") {
		return Dependency{}, "", fmt.Errorf("%s: URL scheme must be https: %s", field, cleanURL)
	}

	if dep, ok := state.resolved[cleanURL]; ok {
		if dep.SHA256 != expectedHash {
			return Dependency{}, "", fmt.Errorf(
				"%s: URL %s has conflicting integrity hashes: previously resolved with %s, now referenced with %s",
				field, cleanURL, dep.SHA256, expectedHash)
		}
		depCopy := dep
		depCopy.Field = field
		return depCopy, dep.LocalPath, nil
	}
	if state.inProgress[cleanURL] {
		return Dependency{}, "", fmt.Errorf("%s: circular dependency detected for %s", field, cleanURL)
	}
	if state.resourceCount >= state.maxResources {
		return Dependency{}, "", fmt.Errorf("%s: exceeded maximum resource count of %d for %s", field, state.maxResources, cleanURL)
	}

	state.inProgress[cleanURL] = true
	defer delete(state.inProgress, cleanURL)
	state.resourceCount++

	allowedBy := h.MatchingAllowedPrefix(cleanURL)
	if allowedBy == "" {
		return Dependency{}, "", fmt.Errorf("%s: URL %q is not in allowed_remote_resources", field, cleanURL)
	}

	forgeInfo, err := forge.ParseForgeURL(cleanURL)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("%s: URLs must be hosted on a supported forge: %w", field, err)
	}
	if forgeInfo.Forge != "github" {
		return Dependency{}, "", fmt.Errorf("%s: forge %q is recognized but fetch support has not landed yet", field, forgeInfo.Forge)
	}

	treePath, dirEntry, err := fetch.CacheGetDir(opts.WorkspaceRoot, expectedHash)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("dir cache lookup for %s: %w", field, err)
	}

	cacheHit := treePath != ""
	fetchedAt := time.Now().UTC()

	if !cacheHit {
		if opts.FetchPolicy.Offline {
			return Dependency{}, "", fmt.Errorf("fetching %s from %s: offline mode, no cache entry", field, cleanURL)
		}

		fetcher := opts.TreeFetcher
		if fetcher == nil {
			fetcher = gitfetch.FetchTree
		}

		files, err := fetcher(ctx, forgeInfo.CloneURL(), forgeInfo.Path, forgeInfo.Ref, opts.GitToken)
		if err != nil {
			if opts.GitToken == "" {
				return Dependency{}, "", fmt.Errorf("fetching directory for %s at %s: %w (hint: set GH_TOKEN or GITHUB_TOKEN for private repos)", field, cleanURL, err)
			}
			return Dependency{}, "", fmt.Errorf("fetching directory for %s at %s: %w", field, cleanURL, err)
		}

		actualHash := fetch.ComputeTreeHash(files)
		if actualHash != expectedHash {
			return Dependency{}, "", fmt.Errorf("%s: integrity check failed for %s: expected %s, got %s", field, cleanURL, expectedHash, actualHash)
		}

		if _, err := fetch.CachePutDir(opts.WorkspaceRoot, cleanURL, files); err != nil {
			return Dependency{}, "", fmt.Errorf("caching directory for %s: %w", field, err)
		}

		cachePath, err := fetch.CachePath(opts.WorkspaceRoot, expectedHash)
		if err != nil {
			return Dependency{}, "", fmt.Errorf("computing cache path for %s: %w", field, err)
		}
		treePath = filepath.Join(cachePath, "tree")
	} else {
		fetchedAt = dirEntry.FetchTime
	}

	// Create a symlink named after the directory so downstream consumers
	// (sandbox upload, logging) see the real name instead of "tree".
	treePath, err = fetch.CacheNamedSymlink(treePath, filepath.Base(forgeInfo.Path))
	if err != nil {
		return Dependency{}, "", fmt.Errorf("naming cached directory for %s: %w", field, err)
	}

	if opts.AuditLogPath != "" {
		if err := fetch.AppendFetchAudit(opts.AuditLogPath, fetch.FetchAuditEntry{
			TraceID:   opts.TraceID,
			FetchTime: fetchedAt,
			URL:       cleanURL,
			SHA256:    expectedHash,
			FetchType: "static",
			AllowedBy: allowedBy,
			CacheHit:  cacheHit,
		}); err != nil {
			return Dependency{}, "", fmt.Errorf("writing fetch audit log: %w", err)
		}
	}

	if recurse {
		if err := resolveSkillTransitiveDeps(ctx, cleanURL, treePath, h, opts, state, depth+1); err != nil {
			return Dependency{}, "", fmt.Errorf("resolving transitive deps for %s (%s): %w", field, cleanURL, err)
		}
	}

	dep := Dependency{
		Field:     field,
		URL:       cleanURL,
		LocalPath: treePath,
		SHA256:    expectedHash,
		FetchedAt: fetchedAt,
		CacheHit:  cacheHit,
		Type:      "directory",
	}

	state.resolved[cleanURL] = dep

	return dep, treePath, nil
}

// resolveSkillTransitiveDeps reads SKILL.md from a cached skill directory,
// parses its frontmatter, and recursively resolves declared dependencies.
// Skill dependencies are resolved as directories; policy references as files.
// depth is the current nesting level (1 for first-level transitive deps).
func resolveSkillTransitiveDeps(ctx context.Context, parentURL, skillDirPath string,
	h *harness.Harness, opts ResolveOpts, state *resolveState, depth int,
) error {
	skillMDPath := filepath.Join(skillDirPath, "SKILL.md")
	content, err := os.ReadFile(skillMDPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading SKILL.md from %s: %w", parentURL, err)
	}

	meta, err := skill.ParseFrontmatter(content)
	if err != nil {
		return fmt.Errorf("%s: %w", parentURL, err)
	}
	if meta == nil || (len(meta.Dependencies) == 0 && meta.Policy == "") {
		return nil
	}

	if depth > state.maxDepth {
		return fmt.Errorf("exceeded maximum dependency depth of %d for %s", state.maxDepth, parentURL)
	}

	for i, ref := range meta.Dependencies {
		resolved, err := ResolveRelativeURL(parentURL, ref)
		if err != nil {
			return fmt.Errorf("resolving dependency ref %q from %s: %w", ref, parentURL, err)
		}

		field := fmt.Sprintf("skills[%s:dep%d]", parentURL, i)
		dep, localPath, err := resolveSkillDirURL(ctx, field, resolved, h, opts, state, true, depth)
		if err != nil {
			return err
		}

		if !state.inDeps[dep.URL] {
			h.Skills = append(h.Skills, localPath)
		}
		state.appendDependency(dep)
	}

	if meta.Policy != "" {
		resolved, err := ResolveRelativeURL(parentURL, meta.Policy)
		if err != nil {
			return fmt.Errorf("resolving policy ref %q from %s: %w", meta.Policy, parentURL, err)
		}

		field := fmt.Sprintf("policy[%s]", parentURL)
		dep, _, err := resolveFileURL(ctx, field, resolved, h, opts, state)
		if err != nil {
			return err
		}

		state.appendDependency(dep)
	}

	return nil
}

// chmodPluginDir sets all regular files in dir to 0755 so that scripts
// and MCP server binaries within a fetched plugin directory are
// executable. Uses 0755 for consistency with pre_script/post_script
// permission handling in lock.go.
//
// WARNING: this mutates the shared content-addressed cache. Files in
// the same tree referenced as skills from another harness field will
// also become 0755. This is acceptable because making declarative
// files (markdown, JSON) executable is harmless, and the SHA-256
// integrity pin constrains content, not permissions.
func chmodPluginDir(dir string) error {
	return harness.ChmodPluginDir(dir)
}
