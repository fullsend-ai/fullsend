package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
	"gopkg.in/yaml.v3"
)

// MaxBaseDepth is the maximum depth of base chain inheritance.
// This prevents runaway recursion from circular or pathologically deep chains.
const MaxBaseDepth = 5

// Dependency records a single URL that was resolved to a local cache path.
// This mirrors resolve.Dependency but lives here to avoid circular imports.
type Dependency struct {
	Field     string
	URL       string
	LocalPath string
	SHA256    string
	FetchedAt time.Time
	CacheHit  bool
	Type      string // "file" for base harnesses
	Warning   string // non-fatal warning about this dependency (e.g., partial skill fetch)
}

// ComposeOpts controls base composition behavior.
type ComposeOpts struct {
	// WorkspaceRoot is the root directory for cache paths (typically the repo root).
	WorkspaceRoot string

	// FetchPolicy controls SSRF protection, offline mode, and size limits.
	FetchPolicy fetch.FetchPolicy

	// TraceID is a correlation ID for audit log entries.
	TraceID string

	// AuditLogPath is the path to the fetch audit log (JSONL).
	// If empty, audit logging is skipped.
	AuditLogPath string

	// ForgePlatform is the platform to resolve after base merging (e.g., "github").
	// If empty, ResolveForge is a no-op.
	ForgePlatform string

	// OrgAllowlist is the allowed_remote_resources from config.yaml (org-level
	// or per-repo-level). Base URLs and agent source URLs must match a prefix
	// in this list. Callers should merge org and per-repo allowlists when both
	// are available.
	OrgAllowlist []string

	// TreeFetcher fetches all files under a path in a remote repository.
	// When nil, defaults to gitfetch.FetchTree (git sparse checkout).
	TreeFetcher gitfetch.TreeFetchFunc

	// GitToken is an optional token for authenticating git fetches.
	// Empty means unauthenticated (sufficient for public repos).
	GitToken string

	// SourceURL is the URL from which the harness was fetched (e.g., via
	// FetchAgentHarness for config-registered agents). When set and the harness
	// has no base: field, LoadWithBase resolves relative resource paths (agent,
	// policy, skills, scripts) against this URL using the same infrastructure
	// as base composition (ADR-0045). If empty, no URL resolution is performed
	// for no-base harnesses.
	SourceURL string

	// allowSelfAllowlist permits using the child harness's own AllowedRemoteResources
	// when OrgAllowlist is empty. This is for testing only; production callers should
	// always provide OrgAllowlist from config.yaml. Unexported to prevent misuse.
	allowSelfAllowlist bool
}

// LoadWithBase loads a harness with base composition and forge resolution.
// If the harness has a `base` field, the base chain is recursively loaded
// and merged before forge resolution. Returns the merged harness and a list
// of dependencies for any URL bases that were fetched.
//
// Pipeline:
//  1. LoadRaw(path) — preserves forge map
//  2. If base absent: ResolveForge → Validate → return
//  3. If base present: loadBaseChain recursively, then mergeBaseIntoChild
//  4. ResolveForge once on final merged result
//  5. Validate
//
// When base is absent, this behaves identically to LoadWithOpts.
func LoadWithBase(ctx context.Context, path string, opts ComposeOpts) (*Harness, []Dependency, error) {
	childDir := filepath.Dir(path)

	child, err := LoadRaw(path)
	if err != nil {
		return nil, nil, err
	}

	if child.Base == "" {
		// No base — resolve URL-sourced resources if the harness was
		// fetched from a URL (ADR-0045). Config-registered agents fetched
		// via FetchAgentHarness have relative paths that must be resolved
		// against the source URL before validation.
		var deps []Dependency
		if opts.SourceURL != "" {
			scriptDeps, err := resolveBaseScripts(ctx, child, opts.SourceURL, opts.OrgAllowlist, opts)
			if err != nil {
				return nil, nil, fmt.Errorf("resolving URL-sourced scripts: %w", err)
			}
			deps = append(deps, scriptDeps...)

			resourceDeps, err := resolveBaseResources(ctx, child, opts.SourceURL, opts.OrgAllowlist, opts)
			if err != nil {
				return nil, nil, fmt.Errorf("resolving URL-sourced resources: %w", err)
			}
			deps = append(deps, resourceDeps...)

			hostFileDeps, err := resolveBaseHostFiles(ctx, child, opts.SourceURL, opts.OrgAllowlist, opts)
			if err != nil {
				return nil, nil, fmt.Errorf("resolving URL-sourced host_files: %w", err)
			}
			deps = append(deps, hostFileDeps...)
		}

		if err := child.validateForge(); err != nil {
			return nil, nil, fmt.Errorf("invalid harness: %w", err)
		}
		if err := child.ResolveForge(opts.ForgePlatform); err != nil {
			return nil, nil, fmt.Errorf("resolving forge config: %w", err)
		}
		if err := child.Validate(); err != nil {
			return nil, nil, fmt.Errorf("invalid harness: %w", err)
		}
		return child, deps, nil
	}

	// Org allowlist is the authority for URL bases.
	// Reject URL bases when no org allowlist is configured to prevent
	// self-authorization (child harness declaring its own allowed URLs).
	allowlist := opts.OrgAllowlist
	if len(allowlist) == 0 && IsURL(child.Base) && !opts.allowSelfAllowlist {
		return nil, nil, fmt.Errorf("URL base requires org-level allowed_remote_resources")
	}
	// For testing, allowSelfAllowlist permits using the child's own list.
	if opts.allowSelfAllowlist && len(allowlist) == 0 {
		allowlist = child.AllowedRemoteResources
	}

	visited := make(map[string]bool)
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving absolute path: %w", err)
	}
	visited[absPath] = true // Mark child as visited to detect self-reference

	base, deps, err := loadBaseChain(ctx, child.Base, childDir, allowlist, opts, visited, 1)
	if err != nil {
		return nil, nil, fmt.Errorf("loading base chain: %w", err)
	}

	// Merge base into child (child overrides base)
	mergeBaseIntoChild(base, child)

	// Clear the base field (consumed)
	child.Base = ""

	// ResolveForge once on the merged result
	if err := child.validateForge(); err != nil {
		return nil, nil, fmt.Errorf("invalid harness: %w", err)
	}
	if err := child.ResolveForge(opts.ForgePlatform); err != nil {
		return nil, nil, fmt.Errorf("resolving forge config: %w", err)
	}
	if err := child.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid harness: %w", err)
	}

	return child, deps, nil
}

// loadBaseChain recursively loads a base harness and its ancestors.
// Returns the fully-merged base harness and any URL dependencies.
func loadBaseChain(
	ctx context.Context,
	baseRef string,
	childDir string,
	allowlist []string,
	opts ComposeOpts,
	visited map[string]bool,
	depth int,
) (*Harness, []Dependency, error) {
	if depth > MaxBaseDepth {
		return nil, nil, fmt.Errorf("exceeded maximum base depth of %d", MaxBaseDepth)
	}

	var base *Harness
	var deps []Dependency
	var baseDir string

	// Reject non-HTTPS URLs before they get treated as local paths
	if strings.HasPrefix(baseRef, "http://") {
		return nil, nil, fmt.Errorf("base URL scheme must be https, got http://")
	}

	if IsURL(baseRef) {
		// Check for cycle before fetching to avoid wasted network round-trips
		cleanURL, _, _ := ParseIntegrityHash(baseRef)
		if visited[cleanURL] {
			return nil, nil, fmt.Errorf("circular base reference: %s", cleanURL)
		}
		visited[cleanURL] = true

		// URL base — fetch, verify, cache
		dep, content, err := fetchBaseURL(ctx, baseRef, allowlist, opts)
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, dep)

		// Parse the fetched content
		base, err = parseHarnessContent(content)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing base harness from %s: %w", cleanURL, err)
		}

		// Resolve script fields in the base by fetching them from the base's
		// source URL. This extends ADR-0038: standalone script URL references
		// (pre_script: https://...) remain rejected, but scripts inherited
		// through base: composition are fetched using the same integrity and
		// allowlist infrastructure. After resolution, all script paths are
		// local cache paths, so ValidateResourceTypes still passes.
		scriptDeps, err := resolveBaseScripts(ctx, base, baseRef, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving base scripts from %s: %w", cleanURL, err)
		}
		deps = append(deps, scriptDeps...)

		// Declarative resources (agent, policy, skills) inherited from a
		// URL base are fetched and cached locally so they don't resolve
		// against the child's directory where they may not exist.
		resourceDeps, err := resolveBaseResources(ctx, base, baseRef, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving base resources from %s: %w", cleanURL, err)
		}
		deps = append(deps, resourceDeps...)

		// host_files with relative src paths need the same fetch-cache-rewrite
		// treatment as scripts and resources. Entries using ${VAR} expansion
		// are left unchanged — they resolve at bootstrap time on the host.
		hostFileDeps, err := resolveBaseHostFiles(ctx, base, baseRef, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving base host_files from %s: %w", cleanURL, err)
		}
		deps = append(deps, hostFileDeps...)

		baseDir = childDir
	} else {
		// Local path base
		basePath := baseRef
		if !filepath.IsAbs(basePath) {
			basePath = filepath.Join(childDir, basePath)
		}

		// Canonicalize for cycle detection
		absBasePath, err := filepath.Abs(basePath)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving base path: %w", err)
		}
		absBasePath = filepath.Clean(absBasePath)

		// Directory containment check: base must be within workspace root.
		// Default to child's directory if WorkspaceRoot is not set.
		containmentRoot := opts.WorkspaceRoot
		if containmentRoot == "" {
			containmentRoot = childDir
		}
		absWorkspace, err := filepath.Abs(containmentRoot)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving containment root: %w", err)
		}
		absWorkspace = filepath.Clean(absWorkspace)
		rel, err := filepath.Rel(absWorkspace, absBasePath)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, nil, fmt.Errorf("base path %q escapes workspace root", baseRef)
		}

		if visited[absBasePath] {
			return nil, nil, fmt.Errorf("circular base reference: %s", absBasePath)
		}
		visited[absBasePath] = true

		base, err = LoadRaw(basePath)
		if err != nil {
			return nil, nil, fmt.Errorf("loading base harness %s: %w", basePath, err)
		}

		baseDir = filepath.Dir(absBasePath)
	}

	// If base has its own base, recurse
	if base.Base != "" {
		ancestorBase, ancestorDeps, err := loadBaseChain(ctx, base.Base, baseDir, allowlist, opts, visited, depth+1)
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, ancestorDeps...)

		// Merge ancestor into base
		mergeBaseIntoChild(ancestorBase, base)
		base.Base = ""
	}

	return base, deps, nil
}

// fetchBaseURL fetches a URL-referenced base harness using the ADR-0038 infrastructure.
func fetchBaseURL(ctx context.Context, rawURL string, allowlist []string, opts ComposeOpts) (Dependency, []byte, error) {
	cleanURL, expectedHash, hasHash := ParseIntegrityHash(rawURL)
	if !hasHash {
		return Dependency{}, nil, fmt.Errorf("base URL must include #sha256=... integrity hash: %s", cleanURL)
	}
	if !strings.HasPrefix(cleanURL, "https://") {
		return Dependency{}, nil, fmt.Errorf("base URL scheme must be https: %s", cleanURL)
	}

	// Check allowlist
	allowedBy := matchingAllowedPrefix(cleanURL, allowlist)
	if allowedBy == "" {
		return Dependency{}, nil, fmt.Errorf("base URL %q is not in allowed_remote_resources", cleanURL)
	}

	// Check cache
	content, entry, err := fetch.CacheGet(opts.WorkspaceRoot, expectedHash)
	if err != nil {
		return Dependency{}, nil, fmt.Errorf("cache lookup for base: %w", err)
	}

	cacheHit := content != nil
	fetchedAt := time.Now().UTC()

	if content == nil {
		// Offline mode check
		if opts.FetchPolicy.Offline {
			return Dependency{}, nil, fmt.Errorf("base URL %s not in cache and offline mode is enabled", cleanURL)
		}

		// Fetch
		content, err = fetch.FetchURL(ctx, cleanURL, opts.FetchPolicy)
		if err != nil {
			return Dependency{}, nil, fmt.Errorf("fetching base from %s: %w", cleanURL, err)
		}

		// Verify integrity
		actualHash := fetch.ComputeSHA256(content)
		if actualHash != expectedHash {
			return Dependency{}, nil, fmt.Errorf("base integrity check failed for %s: expected %s, got %s", cleanURL, expectedHash, actualHash)
		}

		// Cache
		if err := fetch.CachePut(opts.WorkspaceRoot, cleanURL, content); err != nil {
			return Dependency{}, nil, fmt.Errorf("caching base: %w", err)
		}
	} else {
		fetchedAt = entry.FetchTime
	}

	// Audit log
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
			return Dependency{}, nil, fmt.Errorf("writing fetch audit log: %w", err)
		}
	}

	// Compute local path
	cachePath, err := fetch.CachePath(opts.WorkspaceRoot, expectedHash)
	if err != nil {
		return Dependency{}, nil, fmt.Errorf("computing cache path: %w", err)
	}
	localPath := filepath.Join(cachePath, "content")

	dep := Dependency{
		Field:     "base",
		URL:       cleanURL,
		LocalPath: localPath,
		SHA256:    expectedHash,
		FetchedAt: fetchedAt,
		CacheHit:  cacheHit,
		Type:      "file",
	}

	return dep, content, nil
}

// parseHarnessContent parses harness YAML content without validation.
func parseHarnessContent(content []byte) (*Harness, error) {
	var h Harness
	if err := yaml.Unmarshal(content, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// matchingAllowedPrefix checks if a URL matches any prefix in the allowlist.
// Returns the matching prefix or "" if none match.
func matchingAllowedPrefix(rawURL string, allowlist []string) string {
	return MatchingAllowedPrefixInList(rawURL, allowlist)
}

// mergeBaseIntoChild merges base harness fields into child harness.
// Child values override base values following ADR-0045 merge rules:
//   - Scalars: child overrides if non-zero
//   - Slices (skills, plugins, providers, api_servers): base + child (concatenated)
//   - Maps (runner_env): base merged with child; child keys win
//   - Pointer structs (validation_loop, security): child replaces if non-nil
//   - host_files: concatenated with last-writer-wins dedup by Dest
//   - forge: key-by-key merge; per-platform uses same rules
//   - allowed_remote_resources: NOT merged (security; child must declare its own)
func mergeBaseIntoChild(base, child *Harness) {
	// Scalars: child overrides if non-zero
	if child.Agent == "" {
		child.Agent = base.Agent
	}
	if child.Doc == "" {
		child.Doc = base.Doc
	}
	if child.Description == "" {
		child.Description = base.Description
	}
	if child.Role == "" {
		child.Role = base.Role
	}
	if child.Slug == "" {
		child.Slug = base.Slug
	}
	if child.Image == "" {
		child.Image = base.Image
	}
	if child.Policy == "" {
		child.Policy = base.Policy
	}
	if child.Model == "" {
		child.Model = base.Model
	}
	if child.PreScript == "" {
		child.PreScript = base.PreScript
	}
	if child.PostScript == "" {
		child.PostScript = base.PostScript
	}
	if child.AgentInput == "" {
		child.AgentInput = base.AgentInput
	}
	if child.TimeoutMinutes == 0 {
		child.TimeoutMinutes = base.TimeoutMinutes
	}
	if child.SandboxTimeoutSeconds == 0 {
		child.SandboxTimeoutSeconds = base.SandboxTimeoutSeconds
	}

	// Concatenated slices: base + child.
	// Pre-allocate new slices to avoid mutating base's backing array.
	if base.Skills != nil {
		merged := make([]string, 0, len(base.Skills)+len(child.Skills))
		merged = append(merged, base.Skills...)
		merged = append(merged, child.Skills...)
		child.Skills = merged
	}
	if base.Plugins != nil {
		merged := make([]string, 0, len(base.Plugins)+len(child.Plugins))
		merged = append(merged, base.Plugins...)
		merged = append(merged, child.Plugins...)
		child.Plugins = merged
	}
	if base.Providers != nil {
		merged := make([]string, 0, len(base.Providers)+len(child.Providers))
		merged = append(merged, base.Providers...)
		merged = append(merged, child.Providers...)
		child.Providers = merged
	}
	// AllowedRemoteResources, AllowRuntimeFetch, and MaxRuntimeFetches are
	// NOT merged from base harnesses to prevent privilege escalation: a base
	// cannot inject arbitrary URL prefixes or enable runtime fetching in the
	// child. The child must declare its own allowlist and fetch settings.
	if base.APIServers != nil {
		merged := make([]APIServer, 0, len(base.APIServers)+len(child.APIServers))
		merged = append(merged, base.APIServers...)
		merged = append(merged, child.APIServers...)
		child.APIServers = merged
	}

	// HostFiles: concatenated with last-writer-wins dedup by Dest
	if base.HostFiles != nil {
		child.HostFiles = mergeHostFiles(base.HostFiles, child.HostFiles)
	}

	// RunnerEnv: merge maps, child keys win
	if base.RunnerEnv != nil {
		merged := make(map[string]string, len(base.RunnerEnv)+len(child.RunnerEnv))
		for k, v := range base.RunnerEnv {
			merged[k] = v
		}
		for k, v := range child.RunnerEnv {
			merged[k] = v
		}
		child.RunnerEnv = merged
	}

	// Env: merge sub-maps independently, child keys win (ADR 0055)
	if base.Env != nil {
		if child.Env == nil {
			child.Env = &EnvConfig{}
		}
		child.Env.mergeEnvFrom(base.Env, false)
	}

	// Pointer structs: child replaces if non-nil
	if child.ValidationLoop == nil {
		child.ValidationLoop = base.ValidationLoop
	}
	// Security: child inherits base's config if nil. Note that a base harness
	// (even integrity-pinned) could set fail_mode: open. Child authors must
	// explicitly set their own security block to prevent inheriting a weaker posture.
	if child.Security == nil {
		child.Security = base.Security
	}

	// Forge: key-by-key merge
	if base.Forge != nil {
		child.Forge = mergeForgeBlocks(base.Forge, child.Forge)
	}
}

// resolveBaseScripts fetches script fields from a URL-referenced base harness.
// For each script field (pre_script, post_script, validation_loop.script) that
// is a non-empty relative path, the script is fetched from the base URL's
// directory, cached content-addressed, and the field is rewritten to the local
// cache path. Forge-level scripts are also resolved. agent_input is excluded
// because runtime treats it as a directory (uploaded recursively).
// Returns additional dependencies for the fetched scripts.
func resolveBaseScripts(ctx context.Context, base *Harness, baseURL string, allowlist []string, opts ComposeOpts) ([]Dependency, error) {
	// Script paths in harness YAMLs are relative to the scaffold root (the
	// parent of the harness/ directory), not the YAML file. Use
	// urlParentDirPrefix to match the local resolution behavior where
	// ResolveRelativeTo is called with absFullsendDir (the workspace root).
	baseURLDir := urlParentDirPrefix(baseURL)
	if baseURLDir == "" {
		return nil, fmt.Errorf("cannot determine directory from base URL")
	}

	var deps []Dependency

	// agent_input is excluded: runtime treats it as a directory (uploaded
	// recursively), so single-file fetch is not appropriate.
	scriptFields := []struct {
		name string
		ptr  *string
	}{
		{"pre_script", &base.PreScript},
		{"post_script", &base.PostScript},
	}

	for _, f := range scriptFields {
		if *f.ptr == "" {
			continue
		}
		if err := validateBaseRelPath(f.name, *f.ptr); err != nil {
			return nil, err
		}
		dep, cachePath, err := fetchBaseFile(ctx, f.name, baseURLDir, *f.ptr, allowlist, opts, "script", true)
		if err != nil {
			return nil, err
		}
		*f.ptr = cachePath
		deps = append(deps, dep)
	}

	if base.ValidationLoop != nil && base.ValidationLoop.Script != "" {
		if err := validateBaseRelPath("validation_loop.script", base.ValidationLoop.Script); err != nil {
			return nil, err
		}
		dep, cachePath, err := fetchBaseFile(ctx, "validation_loop.script", baseURLDir, base.ValidationLoop.Script, allowlist, opts, "script", true)
		if err != nil {
			return nil, err
		}
		base.ValidationLoop.Script = cachePath
		deps = append(deps, dep)
	}
	if base.ValidationLoop != nil && base.ValidationLoop.Schema != "" {
		if err := validateBaseRelPath("validation_loop.schema", base.ValidationLoop.Schema); err != nil {
			return nil, err
		}
		dep, cachePath, err := fetchBaseFile(ctx, "validation_loop.schema", baseURLDir, base.ValidationLoop.Schema, allowlist, opts, "resource", false)
		if err != nil {
			return nil, err
		}
		base.ValidationLoop.Schema = cachePath
		deps = append(deps, dep)
	}

	for platform, fc := range base.Forge {
		if fc == nil {
			continue
		}
		forgeScripts := []struct {
			name string
			ptr  *string
		}{
			{fmt.Sprintf("forge.%s.pre_script", platform), &fc.PreScript},
			{fmt.Sprintf("forge.%s.post_script", platform), &fc.PostScript},
		}
		for _, f := range forgeScripts {
			if *f.ptr == "" {
				continue
			}
			if err := validateBaseRelPath(f.name, *f.ptr); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, f.name, baseURLDir, *f.ptr, allowlist, opts, "script", true)
			if err != nil {
				return nil, err
			}
			*f.ptr = cachePath
			deps = append(deps, dep)
		}
		if fc.ValidationLoop != nil && fc.ValidationLoop.Script != "" {
			fieldName := fmt.Sprintf("forge.%s.validation_loop.script", platform)
			if err := validateBaseRelPath(fieldName, fc.ValidationLoop.Script); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, fc.ValidationLoop.Script, allowlist, opts, "script", true)
			if err != nil {
				return nil, err
			}
			fc.ValidationLoop.Script = cachePath
			deps = append(deps, dep)
		}
		if fc.ValidationLoop != nil && fc.ValidationLoop.Schema != "" {
			fieldName := fmt.Sprintf("forge.%s.validation_loop.schema", platform)
			if err := validateBaseRelPath(fieldName, fc.ValidationLoop.Schema); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, fc.ValidationLoop.Schema, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			fc.ValidationLoop.Schema = cachePath
			deps = append(deps, dep)
		}
	}

	// agent_input is a directory at runtime (uploaded recursively) and cannot
	// be fetched as a single file from a URL. Clear it so it doesn't resolve
	// against the child's local directory where it won't exist.
	if base.AgentInput != "" {
		base.AgentInput = ""
	}

	return deps, nil
}

// resolveBaseResources fetches declarative resource fields (agent, policy,
// skills) from a URL-referenced base harness. For agent and policy (single
// files), the file is fetched, cached content-addressed, and the field is
// rewritten to the local cache path. For skills (directories), SKILL.md is
// fetched and cached as a directory via CachePutDir, and the field is
// rewritten to the cache tree directory. Fields that are already URLs or
// absolute paths are left unchanged — they will be handled by
// ResolveHarness in the caller.
func resolveBaseResources(ctx context.Context, base *Harness, baseURL string, allowlist []string, opts ComposeOpts) ([]Dependency, error) {
	baseURLDir := urlParentDirPrefix(baseURL)
	if baseURLDir == "" {
		return nil, fmt.Errorf("cannot determine directory from base URL")
	}

	var deps []Dependency

	fileFields := []struct {
		name string
		ptr  *string
	}{
		{"agent", &base.Agent},
		{"policy", &base.Policy},
	}

	for _, f := range fileFields {
		if *f.ptr == "" || IsURL(*f.ptr) || filepath.IsAbs(*f.ptr) {
			continue
		}
		if err := validateBaseRelPath(f.name, *f.ptr); err != nil {
			return nil, err
		}
		dep, cachePath, err := fetchBaseFile(ctx, f.name, baseURLDir, *f.ptr, allowlist, opts, "resource", false)
		if err != nil {
			return nil, err
		}
		*f.ptr = cachePath
		deps = append(deps, dep)
	}

	for i, skill := range base.Skills {
		if skill == "" || IsURL(skill) || filepath.IsAbs(skill) {
			continue
		}
		fieldName := fmt.Sprintf("skills[%d]", i)
		if err := validateBaseRelPath(fieldName, skill); err != nil {
			return nil, err
		}
		dep, localDir, err := fetchBaseSkill(ctx, fieldName, baseURLDir, skill, allowlist, opts)
		if err != nil {
			return nil, err
		}
		base.Skills[i] = localDir
		deps = append(deps, dep)
	}

	return deps, nil
}

// resolveBaseHostFiles fetches host_files with relative src paths from a
// URL-referenced base harness. For each host_files entry whose src is a
// non-empty relative path (not a ${VAR} reference, URL, or absolute path),
// the file is fetched from the base URL's directory, cached content-addressed,
// and the src field is rewritten to the local cache path. This ensures
// host_files inherited through base: composition resolve correctly at sandbox
// setup time, the same way scripts and resources do.
func resolveBaseHostFiles(ctx context.Context, base *Harness, baseURL string, allowlist []string, opts ComposeOpts) ([]Dependency, error) {
	baseURLDir := urlParentDirPrefix(baseURL)
	if baseURLDir == "" {
		return nil, fmt.Errorf("cannot determine directory from base URL")
	}

	var deps []Dependency

	for i := range base.HostFiles {
		src := base.HostFiles[i].Src
		if src == "" || strings.Contains(src, "${") || IsURL(src) || filepath.IsAbs(src) {
			continue
		}
		fieldName := fmt.Sprintf("host_files[%d].src", i)
		if err := validateBaseRelPath(fieldName, src); err != nil {
			return nil, err
		}
		dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, src, allowlist, opts, "resource", false)
		if err != nil {
			return nil, err
		}
		base.HostFiles[i].Src = cachePath
		deps = append(deps, dep)
	}

	return deps, nil
}

// validateBaseRelPath validates that a relative path inherited from a URL base
// is safe to resolve. Rejects null bytes, query/fragment markers, URLs,
// absolute paths, and path traversal segments.
func validateBaseRelPath(field, val string) error {
	if strings.ContainsRune(val, 0) {
		return fmt.Errorf("base %s must not contain null bytes (got %q)", field, val)
	}
	if strings.ContainsAny(val, "?#") {
		return fmt.Errorf("base %s must not contain query or fragment markers (got %q)", field, val)
	}
	if IsURL(val) {
		return fmt.Errorf("base %s must be a relative path, not a URL (got %q)", field, val)
	}
	if filepath.IsAbs(val) {
		return fmt.Errorf("base %s must be a relative path, not an absolute path (got %q)", field, val)
	}
	for _, seg := range strings.Split(val, "/") {
		if seg == ".." {
			return fmt.Errorf("base %s must not contain path traversal segments (got %q)", field, val)
		}
	}
	return nil
}

// fetchBaseFile fetches a single file from a URL derived from the base
// harness's directory and the file's relative path. The file is cached
// content-addressed and the local cache path is returned. When executable
// is true, the cached file is chmod'd to 0o755. depType controls the
// Dependency.Type and audit log FetchType ("script" or "resource").
func fetchBaseFile(ctx context.Context, field, baseURLDir, relPath string, allowlist []string, opts ComposeOpts, depType string, executable bool) (Dependency, string, error) {
	fileURL := baseURLDir + relPath

	allowedBy := matchingAllowedPrefix(fileURL, allowlist)
	if allowedBy == "" {
		return Dependency{}, "", fmt.Errorf("base %s: URL %q is not in allowed_remote_resources", field, fileURL)
	}

	hash, indexHit := urlIndexLookup(opts.WorkspaceRoot, fileURL)
	if indexHit {
		content, entry, err := fetch.CacheGet(opts.WorkspaceRoot, hash)
		if err == nil && content != nil {
			cachePath, cpErr := fetch.CachePath(opts.WorkspaceRoot, hash)
			if cpErr != nil {
				return Dependency{}, "", fmt.Errorf("base %s: computing cache path: %w", field, cpErr)
			}
			contentPath := filepath.Join(cachePath, "content")

			if executable {
				if chErr := os.Chmod(contentPath, 0o755); chErr != nil {
					return Dependency{}, "", fmt.Errorf("base %s: setting executable permission on cached file: %w", field, chErr)
				}
			}

			if aErr := auditBaseFetch(opts, fileURL, hash, allowedBy, true, entry.FetchTime, depType); aErr != nil {
				return Dependency{}, "", aErr
			}

			return Dependency{
				Field:     field,
				URL:       fileURL,
				LocalPath: contentPath,
				SHA256:    hash,
				FetchedAt: entry.FetchTime,
				CacheHit:  true,
				Type:      depType,
			}, contentPath, nil
		}
	}

	if opts.FetchPolicy.Offline {
		return Dependency{}, "", fmt.Errorf("base %s: URL %s not in cache and offline mode is enabled (run 'fullsend lock' first)", field, fileURL)
	}

	content, err := fetch.FetchURL(ctx, fileURL, opts.FetchPolicy)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: fetching %s: %w", field, fileURL, err)
	}

	if err := fetch.CachePut(opts.WorkspaceRoot, fileURL, content); err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: caching: %w", field, err)
	}

	hash = fetch.ComputeSHA256(content)
	cachePath, err := fetch.CachePath(opts.WorkspaceRoot, hash)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: computing cache path: %w", field, err)
	}
	contentPath := filepath.Join(cachePath, "content")

	if executable {
		if chErr := os.Chmod(contentPath, 0o755); chErr != nil {
			return Dependency{}, "", fmt.Errorf("base %s: setting executable permission: %w", field, chErr)
		}
	}

	if iErr := urlIndexPut(opts.WorkspaceRoot, fileURL, hash); iErr != nil {
		return Dependency{}, "", fmt.Errorf("base %s: updating URL index: %w", field, iErr)
	}

	fetchedAt := time.Now().UTC()
	if aErr := auditBaseFetch(opts, fileURL, hash, allowedBy, false, fetchedAt, depType); aErr != nil {
		return Dependency{}, "", aErr
	}

	return Dependency{
		Field:     field,
		URL:       fileURL,
		LocalPath: contentPath,
		SHA256:    hash,
		FetchedAt: fetchedAt,
		CacheHit:  false,
		Type:      depType,
	}, contentPath, nil
}

// fetchBaseSkill fetches a skill directory from a URL-referenced base harness.
// It uses git sparse checkout (via TreeFetcher) to fetch the full directory
// tree (SKILL.md plus companion files like sub-agents/, scripts/, meta-prompts/).
func fetchBaseSkill(ctx context.Context, field, baseURLDir, skillPath string, allowlist []string, opts ComposeOpts) (Dependency, string, error) {
	skillDirURL := baseURLDir + skillPath
	skillFileURL := skillDirURL + "/SKILL.md"

	allowedBy := matchingAllowedPrefix(skillFileURL, allowlist)
	if allowedBy == "" {
		return Dependency{}, "", fmt.Errorf("base %s: URL %q is not in allowed_remote_resources", field, skillFileURL)
	}

	// Check cache first (keyed by SKILL.md URL for backward compat).
	hash, indexHit := urlIndexLookup(opts.WorkspaceRoot, skillFileURL)
	var staleFallback *Dependency
	var staleFallbackPath string
	if indexHit {
		treeHash, ok := urlIndexLookup(opts.WorkspaceRoot, "skill:"+skillFileURL)
		if ok {
			treePath, entry, err := fetch.CacheGetDir(opts.WorkspaceRoot, treeHash)
			if err == nil && treePath != "" {
				cachedDep := Dependency{
					Field:     field,
					URL:       skillFileURL,
					LocalPath: treePath,
					SHA256:    treeHash,
					FetchedAt: entry.FetchTime,
					CacheHit:  true,
					Type:      "directory",
				}
				if !entry.FullListing && !opts.FetchPolicy.Offline {
					staleFallback = &cachedDep
					staleFallbackPath = treePath
				} else {
					if aErr := auditBaseFetch(opts, skillFileURL, treeHash, allowedBy, true, entry.FetchTime, "skill"); aErr != nil {
						return Dependency{}, "", aErr
					}
					return cachedDep, treePath, nil
				}
			}
		}
		_ = hash
	}

	if opts.FetchPolicy.Offline {
		if staleFallback != nil {
			return *staleFallback, staleFallbackPath, nil
		}
		return Dependency{}, "", fmt.Errorf("base %s: URL %s not in cache and offline mode is enabled (run 'fullsend lock' first)", field, skillFileURL)
	}

	dep, dirPath, err := fetchBaseSkillDir(ctx, field, skillDirURL, skillFileURL, skillPath, allowedBy, allowlist, opts)
	if err != nil && staleFallback != nil {
		if !isTransientFetchError(err) {
			return Dependency{}, "", err
		}
		staleFallback.Warning = fmt.Sprintf("using stale cached content (re-fetch failed: %s)", err)
		return *staleFallback, staleFallbackPath, nil
	}
	return dep, dirPath, err
}

// isTransientFetchError returns true for errors that indicate a temporary
// network issue where serving stale cached content is appropriate.
func isTransientFetchError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var transient *gitfetch.TransientError
	return errors.As(err, &transient)
}

// fetchBaseSkillDir fetches the full skill directory via git sparse checkout.
func fetchBaseSkillDir(ctx context.Context, field, skillDirURL, skillFileURL, skillPath, allowedBy string, allowlist []string, opts ComposeOpts) (Dependency, string, error) {
	dirPrefix := skillDirURL + "/"
	if ab := matchingAllowedPrefix(dirPrefix, allowlist); ab == "" {
		return Dependency{}, "", fmt.Errorf("base %s: skill directory URL %q is not in allowed_remote_resources", field, dirPrefix)
	}

	forgeInfo, err := forge.ParseRawContentURL(skillDirURL)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: parsing raw URL for skill directory fetch: %w", field, err)
	}

	fetcher := opts.TreeFetcher
	if fetcher == nil {
		fetcher = gitfetch.FetchTree
	}

	files, err := fetcher(ctx, forgeInfo.CloneURL(), forgeInfo.Path, forgeInfo.Ref, opts.GitToken)
	if err != nil {
		if opts.GitToken == "" {
			return Dependency{}, "", fmt.Errorf("base %s: fetching skill directory %s: %w (hint: set GH_TOKEN or GITHUB_TOKEN for private repos)", field, skillPath, err)
		}
		return Dependency{}, "", fmt.Errorf("base %s: fetching skill directory %s: %w", field, skillPath, err)
	}

	if _, ok := files["SKILL.md"]; !ok {
		return Dependency{}, "", fmt.Errorf("base %s: skill directory %s has no SKILL.md", field, skillPath)
	}

	treeHash, err := fetch.CachePutDir(opts.WorkspaceRoot, skillFileURL, files, fetch.DirCachePutOpts{FullListing: true})
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: caching skill directory: %w", field, err)
	}

	treePath, _, err := fetch.CacheGetDir(opts.WorkspaceRoot, treeHash)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: reading cached skill directory: %w", field, err)
	}

	// Create a symlink named after the skill directory so downstream consumers
	// (sandbox upload, logging) see the real skill name instead of "tree".
	skillName := filepath.Base(forgeInfo.Path)
	if skillName == "" || skillName == "." || skillName == ".." || skillName == "metadata.json" {
		skillName = "tree"
	}
	namedPath := filepath.Join(filepath.Dir(treePath), skillName)
	if namedPath != treePath {
		if _, err := os.Lstat(namedPath); os.IsNotExist(err) {
			if err := os.Symlink("tree", namedPath); err != nil && !os.IsExist(err) {
				return Dependency{}, "", fmt.Errorf("base %s: creating named symlink: %w", field, err)
			}
		} else if err != nil {
			return Dependency{}, "", fmt.Errorf("base %s: checking named symlink: %w", field, err)
		}
		treePath = namedPath
	}

	if iErr := urlIndexPut(opts.WorkspaceRoot, skillFileURL, treeHash); iErr != nil {
		return Dependency{}, "", fmt.Errorf("base %s: updating URL index: %w", field, iErr)
	}
	if iErr := urlIndexPut(opts.WorkspaceRoot, "skill:"+skillFileURL, treeHash); iErr != nil {
		return Dependency{}, "", fmt.Errorf("base %s: updating URL index for skill tree: %w", field, iErr)
	}

	fetchedAt := time.Now().UTC()
	if aErr := auditBaseFetch(opts, skillFileURL, treeHash, allowedBy, false, fetchedAt, "skill"); aErr != nil {
		return Dependency{}, "", aErr
	}

	return Dependency{
		Field:     field,
		URL:       skillFileURL,
		LocalPath: treePath,
		SHA256:    treeHash,
		FetchedAt: fetchedAt,
		CacheHit:  false,
		Type:      "directory",
	}, treePath, nil
}

// auditBaseFetch appends a fetch audit log entry for a base composition fetch.
func auditBaseFetch(opts ComposeOpts, fileURL, hash, allowedBy string, cacheHit bool, fetchedAt time.Time, fetchType string) error {
	if opts.AuditLogPath == "" {
		return nil
	}
	return fetch.AppendFetchAudit(opts.AuditLogPath, fetch.FetchAuditEntry{
		TraceID:   opts.TraceID,
		FetchTime: fetchedAt,
		URL:       fileURL,
		SHA256:    hash,
		FetchType: "base_" + fetchType,
		AllowedBy: allowedBy,
		CacheHit:  cacheHit,
	})
}

// urlDirPrefix returns the directory portion of a URL (everything up to and
// including the last "/" before the filename). The integrity hash fragment
// is stripped first. Returns "" if the URL cannot be parsed.
func urlDirPrefix(rawURL string) string {
	cleanURL, _, _ := ParseIntegrityHash(rawURL)
	parsed, err := url.Parse(cleanURL)
	if err != nil {
		return ""
	}
	dir := path.Dir(parsed.Path)
	if dir == "." || dir == "" {
		return ""
	}
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	parsed.Path = dir
	parsed.RawPath = ""
	parsed.Fragment = ""
	return parsed.String()
}

// urlParentDirPrefix returns the parent of the directory containing the URL's
// file. Script paths in harness YAMLs are relative to the scaffold root (the
// parent of the harness/ directory), not the YAML file itself. This matches
// local resolution where ResolveRelativeTo uses absFullsendDir (the workspace
// root), which is the parent of the harness/ directory.
func urlParentDirPrefix(rawURL string) string {
	cleanURL, _, _ := ParseIntegrityHash(rawURL)
	parsed, err := url.Parse(cleanURL)
	if err != nil {
		return ""
	}
	dir := path.Dir(path.Dir(parsed.Path))
	if dir == "." || dir == "" {
		return ""
	}
	if !strings.HasSuffix(dir, "/") {
		dir += "/"
	}
	parsed.Path = dir
	parsed.RawPath = ""
	parsed.Fragment = ""
	return parsed.String()
}

// urlIndexPath returns the path to the URL-to-hash index file.
func urlIndexPath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".fullsend-cache", "url-index.json")
}

// urlIndexLookup reads the URL-to-hash index and returns the SHA256 for the
// given URL. Returns ("", false) on miss or read error.
func urlIndexLookup(workspaceRoot, rawURL string) (string, bool) {
	if workspaceRoot == "" {
		return "", false
	}
	data, err := os.ReadFile(urlIndexPath(workspaceRoot))
	if err != nil {
		return "", false
	}
	var index map[string]string
	if err := json.Unmarshal(data, &index); err != nil {
		return "", false
	}
	hash, ok := index[rawURL]
	return hash, ok
}

// urlIndexPut records a URL→SHA256 mapping in the index file.
func urlIndexPut(workspaceRoot, rawURL, hash string) error {
	if workspaceRoot == "" {
		return nil
	}
	idxPath := urlIndexPath(workspaceRoot)
	if err := os.MkdirAll(filepath.Dir(idxPath), 0o700); err != nil {
		return err
	}

	var index map[string]string
	data, err := os.ReadFile(idxPath)
	if err == nil {
		_ = json.Unmarshal(data, &index)
	}
	if index == nil {
		index = make(map[string]string)
	}
	index[rawURL] = hash

	out, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(idxPath, out, 0o600)
}

// mergeHostFiles concatenates base and child host files, with child entries
// overriding base entries that have the same Dest path.
func mergeHostFiles(base, child []HostFile) []HostFile {
	destIndex := make(map[string]int)
	result := make([]HostFile, 0, len(base)+len(child))

	// Add base entries
	for _, hf := range base {
		destIndex[hf.Dest] = len(result)
		result = append(result, hf)
	}

	// Add/override with child entries
	for _, hf := range child {
		if idx, exists := destIndex[hf.Dest]; exists {
			result[idx] = hf // override
		} else {
			destIndex[hf.Dest] = len(result)
			result = append(result, hf)
		}
	}

	return result
}

// mergeForgeBlocks merges forge maps key-by-key.
// For each platform key present in both, the ForgeConfig fields are merged
// using the same rules as mergeForgeConfig.
func mergeForgeBlocks(base, child map[string]*ForgeConfig) map[string]*ForgeConfig {
	if child == nil {
		child = make(map[string]*ForgeConfig)
	}

	for key, baseFC := range base {
		if childFC, exists := child[key]; exists && childFC != nil {
			// Merge per-platform ForgeConfig
			mergeForgeConfigInto(baseFC, childFC)
		} else if !exists {
			// Child doesn't have this platform — inherit from base
			child[key] = baseFC
		}
		// If child[key] exists but is nil, child explicitly nulls out this platform
	}

	return child
}

// mergeForgeConfigInto merges base ForgeConfig fields into child.
// Similar to mergeForgeConfig in forge.go but prepends base skills
// (base + child order) rather than appending forge skills to harness skills.
func mergeForgeConfigInto(base, child *ForgeConfig) {
	if base == nil {
		return
	}

	// Scalars: child overrides if non-empty
	if child.PreScript == "" {
		child.PreScript = base.PreScript
	}
	if child.PostScript == "" {
		child.PostScript = base.PostScript
	}

	// Skills: concatenate (pre-allocate to avoid mutating base's backing array)
	if base.Skills != nil {
		merged := make([]string, 0, len(base.Skills)+len(child.Skills))
		merged = append(merged, base.Skills...)
		merged = append(merged, child.Skills...)
		child.Skills = merged
	}

	// RunnerEnv: merge, child keys win
	if base.RunnerEnv != nil {
		if child.RunnerEnv == nil {
			child.RunnerEnv = make(map[string]string, len(base.RunnerEnv))
		}
		for k, v := range base.RunnerEnv {
			if _, exists := child.RunnerEnv[k]; !exists {
				child.RunnerEnv[k] = v
			}
		}
	}

	// Env: merge sub-maps, child keys win (ADR 0055)
	if base.Env != nil {
		if child.Env == nil {
			child.Env = &EnvConfig{}
		}
		child.Env.mergeEnvFrom(base.Env, false)
	}

	// ValidationLoop: child replaces if non-nil
	if child.ValidationLoop == nil {
		child.ValidationLoop = base.ValidationLoop
	}
}

// FetchAgentHarness fetches a URL-sourced agent harness using the same
// infrastructure as base composition (ADR 0038). It downloads the content,
// verifies the integrity hash, caches it, and returns the local cache path
// so the caller can pass it to LoadWithBase.
func FetchAgentHarness(ctx context.Context, rawURL string, opts ComposeOpts) (localPath string, dep Dependency, err error) {
	dep, _, err = fetchBaseURL(ctx, rawURL, opts.OrgAllowlist, opts)
	if err != nil {
		return "", Dependency{}, fmt.Errorf("fetching agent harness %s: %w", rawURL, err)
	}
	return dep.LocalPath, dep, nil
}
