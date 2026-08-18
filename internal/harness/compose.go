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

	// Event is the normalized event data for CEL overlay resolution (ADR 0088).
	// If nil, ResolveOverlays is a no-op.
	Event map[string]any

	// Config is the per-repo config (from config.yaml) exposed to overlay
	// CEL when expressions as the config variable (ADR 0088).
	Config map[string]any

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
//  2. If base absent: resolve URL-sourced resources → ResolveForge → ResolveOverlays → Validate → return
//  3. If base present: loadBaseChain recursively, then mergeBaseIntoChild
//  4. Resolve remaining URL-sourced resources and scripts (child's own relative paths)
//  5. ResolveForge and ResolveOverlays once on final merged result
//  6. Validate
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

			profileDeps, err := resolveBaseProfiles(ctx, child, opts.SourceURL, opts.OrgAllowlist, opts)
			if err != nil {
				return nil, nil, fmt.Errorf("resolving URL-sourced profiles: %w", err)
			}
			deps = append(deps, profileDeps...)

			providerDeps, err := resolveBaseProviders(ctx, child, opts.SourceURL, opts.OrgAllowlist, opts)
			if err != nil {
				return nil, nil, fmt.Errorf("resolving URL-sourced providers: %w", err)
			}
			deps = append(deps, providerDeps...)

			pluginDeps, err := resolveBasePlugins(ctx, child, opts.SourceURL, opts.OrgAllowlist, opts)
			if err != nil {
				return nil, nil, fmt.Errorf("resolving URL-sourced plugins: %w", err)
			}
			deps = append(deps, pluginDeps...)
		}

		if err := child.validateForge(); err != nil {
			return nil, nil, fmt.Errorf("invalid harness: %w", err)
		}
		if err := child.validateOverlays(); err != nil {
			return nil, nil, fmt.Errorf("invalid harness: %w", err)
		}
		if err := child.ResolveForge(opts.ForgePlatform); err != nil {
			return nil, nil, fmt.Errorf("resolving forge config: %w", err)
		}
		if err := child.ResolveOverlays(opts.Event, opts.ForgePlatform, opts.Config); err != nil {
			return nil, nil, fmt.Errorf("resolving overlays: %w", err)
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

	// When the harness was fetched from a URL, the child may still have
	// relative resource paths (including scripts, agent, policy, skills,
	// host_files, profiles, providers, and plugins)
	// that originated in the child harness — not the base. After merge,
	// base resources are absolute cache paths but the child's own relative
	// paths remain unresolved. Resolve them against the SourceURL now,
	// the same way the no-base path does.
	//
	// Without this, relative paths like "skills/pr-review" or
	// "scripts/pre.sh" would be resolved against the local workspace by
	// ResolveRelativeTo, missing companion files (sub-agents/,
	// meta-prompt.md) that only exist at the source URL. See #5305.
	//
	// All resolve functions skip cache paths and URLs, so they are safe
	// to call on the merged harness where base-inherited fields are
	// already resolved to cache paths.
	if opts.SourceURL != "" {
		scriptDeps, err := resolveBaseScripts(ctx, child, opts.SourceURL, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving URL-sourced scripts after base composition: %w", err)
		}
		deps = append(deps, scriptDeps...)

		resourceDeps, err := resolveBaseResources(ctx, child, opts.SourceURL, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving URL-sourced resources after base composition: %w", err)
		}
		deps = append(deps, resourceDeps...)

		hostFileDeps, err := resolveBaseHostFiles(ctx, child, opts.SourceURL, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving URL-sourced host_files after base composition: %w", err)
		}
		deps = append(deps, hostFileDeps...)

		profileDeps, err := resolveBaseProfiles(ctx, child, opts.SourceURL, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving URL-sourced profiles after base composition: %w", err)
		}
		deps = append(deps, profileDeps...)

		providerDeps, err := resolveBaseProviders(ctx, child, opts.SourceURL, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving URL-sourced providers after base composition: %w", err)
		}
		deps = append(deps, providerDeps...)

		pluginDeps, err := resolveBasePlugins(ctx, child, opts.SourceURL, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving URL-sourced plugins after base composition: %w", err)
		}
		deps = append(deps, pluginDeps...)
	}

	// ResolveForge and ResolveOverlays once on the merged result
	if err := child.validateForge(); err != nil {
		return nil, nil, fmt.Errorf("invalid harness: %w", err)
	}
	if err := child.validateOverlays(); err != nil {
		return nil, nil, fmt.Errorf("invalid harness: %w", err)
	}
	if err := child.ResolveForge(opts.ForgePlatform); err != nil {
		return nil, nil, fmt.Errorf("resolving forge config: %w", err)
	}
	if err := child.ResolveOverlays(opts.Event, opts.ForgePlatform, opts.Config); err != nil {
		return nil, nil, fmt.Errorf("resolving overlays: %w", err)
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

		profileDeps, err := resolveBaseProfiles(ctx, base, baseRef, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving base profiles from %s: %w", cleanURL, err)
		}
		deps = append(deps, profileDeps...)

		providerDeps, err := resolveBaseProviders(ctx, base, baseRef, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving base providers from %s: %w", cleanURL, err)
		}
		deps = append(deps, providerDeps...)

		pluginDeps, err := resolveBasePlugins(ctx, base, baseRef, allowlist, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving base plugins from %s: %w", cleanURL, err)
		}
		deps = append(deps, pluginDeps...)

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
	if child.Effort == "" {
		child.Effort = base.Effort
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

	// Skills: base + child with child-overrides-base-by-basename.
	// A child skill whose directory basename matches a base skill replaces
	// the base entry (same as host_files' override-by-dest). This allows
	// child harnesses to override built-in skills via base: composition.
	if base.Skills != nil || child.Skills != nil {
		child.Skills = mergeSkills(base.Skills, child.Skills)
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
	if base.OpenShell != nil && len(base.OpenShell.Profiles) > 0 {
		if child.OpenShell == nil {
			child.OpenShell = &OpenShellConfig{}
		}
		merged := make([]string, 0, len(base.OpenShell.Profiles)+len(child.OpenShell.Profiles))
		merged = append(merged, base.OpenShell.Profiles...)
		merged = append(merged, child.OpenShell.Profiles...)
		child.OpenShell.Profiles = merged
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

	// Pointer structs: child replaces if non-nil, but carry forward
	// PreflightCheck when the child overrides validation_loop without
	// setting its own preflight_check (avoids silently dropping inherited
	// preflight checks — see #5074).
	if child.ValidationLoop == nil {
		child.ValidationLoop = base.ValidationLoop
	} else if child.ValidationLoop.PreflightCheck == "" && base.ValidationLoop != nil {
		child.ValidationLoop.PreflightCheck = base.ValidationLoop.PreflightCheck
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

	// Overlays: concatenated (base first, child appended) — same as plugins,
	// providers, api_servers. Declaration order matters: ResolveOverlays uses
	// first-match-wins semantics, so base entries (placed first) take
	// precedence over child entries with the same when condition.
	if base.Overlays != nil {
		merged := make([]OverlayEntry, 0, len(base.Overlays)+len(child.Overlays))
		merged = append(merged, base.Overlays...)
		merged = append(merged, child.Overlays...)
		child.Overlays = merged
	}
}

// isFullsendCachePath reports whether p is a path already inside fullsend's
// own content-addressed cache (<workspaceRoot>/.fullsend-cache/...), as
// opposed to a path written directly into untrusted harness content. Used by
// resolveBaseScripts, resolveBaseResources, and resolveBaseHostFiles to
// decide which values are safe to skip (already resolved by an earlier step)
// versus which must still be rejected by validateBaseRelPath: an arbitrary
// host path in an exec field runs on the host via exec.Command, and one in
// agent/policy/host_files is read on the host and becomes the literal agent
// definition, sandbox policy, or an uploaded sandbox file — letting either
// come from an untrusted path is a code-execution or disclosure risk, not
// just a resolution bug.
//
// Both p and workspaceRoot are resolved to absolute paths before comparison
// so that cache paths are recognized regardless of whether WorkspaceRoot was
// absolute or relative. When WorkspaceRoot is relative (e.g., "." in dispatch),
// CachePath returns a relative path like ".fullsend-cache/resources/sha256/
// <hash>/content"; without absolute resolution the old filepath.IsAbs guard
// would reject it, causing a spurious re-fetch attempt against the SourceURL.
func isFullsendCachePath(p, workspaceRoot string) bool {
	if p == "" || workspaceRoot == "" {
		return false
	}
	absP, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	absCache, err := filepath.Abs(filepath.Join(workspaceRoot, ".fullsend-cache"))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absCache, absP)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveBaseScripts fetches script fields from a URL-referenced base harness.
// For each script field (pre_script, post_script, validation_loop.script) that
// is a non-empty relative path, the script's containing directory is fetched
// (via git sparse checkout) so that companion files are co-located at the
// BASH_SOURCE-relative path. When the URL cannot be parsed for directory-level
// fetching (e.g., non-raw.githubusercontent.com URLs), the script is fetched
// as a single file via fetchBaseFile as a fallback.
// Forge-level scripts are also resolved. agent_input is excluded because
// runtime treats it as a directory (uploaded recursively).
//
// A field is skipped (left unchanged) when it's a URL or already resolved to
// a path inside fullsend's own cache (see isFullsendCachePath) — both cases
// mean an earlier step already handled it. Any other absolute path is
// untrusted and is rejected by validateBaseRelPath, since these fields are
// executed on the host, not just read as data.
//
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
		if *f.ptr == "" || IsURL(*f.ptr) || isFullsendCachePath(*f.ptr, opts.WorkspaceRoot) {
			continue
		}
		if err := validateBaseRelPath(f.name, *f.ptr); err != nil {
			return nil, err
		}
		dep, cachePath, err := fetchBaseScriptOrDir(ctx, f.name, baseURLDir, *f.ptr, allowlist, opts)
		if err != nil {
			return nil, err
		}
		*f.ptr = cachePath
		deps = append(deps, dep)
	}

	if base.ValidationLoop != nil && base.ValidationLoop.Script != "" && !IsURL(base.ValidationLoop.Script) && !isFullsendCachePath(base.ValidationLoop.Script, opts.WorkspaceRoot) {
		if err := validateBaseRelPath("validation_loop.script", base.ValidationLoop.Script); err != nil {
			return nil, err
		}
		dep, cachePath, err := fetchBaseScriptOrDir(ctx, "validation_loop.script", baseURLDir, base.ValidationLoop.Script, allowlist, opts)
		if err != nil {
			return nil, err
		}
		base.ValidationLoop.Script = cachePath
		deps = append(deps, dep)
	}
	if base.ValidationLoop != nil && base.ValidationLoop.Schema != "" && !IsURL(base.ValidationLoop.Schema) && !isFullsendCachePath(base.ValidationLoop.Schema, opts.WorkspaceRoot) {
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

	if fc := base.Forge[opts.ForgePlatform]; fc != nil {
		platform := opts.ForgePlatform
		forgeScripts := []struct {
			name string
			ptr  *string
		}{
			{fmt.Sprintf("forge.%s.pre_script", platform), &fc.PreScript},
			{fmt.Sprintf("forge.%s.post_script", platform), &fc.PostScript},
		}
		for _, f := range forgeScripts {
			if *f.ptr == "" || IsURL(*f.ptr) || isFullsendCachePath(*f.ptr, opts.WorkspaceRoot) {
				continue
			}
			if err := validateBaseRelPath(f.name, *f.ptr); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseScriptOrDir(ctx, f.name, baseURLDir, *f.ptr, allowlist, opts)
			if err != nil {
				return nil, err
			}
			*f.ptr = cachePath
			deps = append(deps, dep)
		}
		if fc.Policy != "" && !IsURL(fc.Policy) && !isFullsendCachePath(fc.Policy, opts.WorkspaceRoot) {
			fieldName := fmt.Sprintf("forge.%s.policy", platform)
			if err := validateBaseRelPath(fieldName, fc.Policy); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, fc.Policy, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			fc.Policy = cachePath
			deps = append(deps, dep)
		}
		if fc.ValidationLoop != nil && fc.ValidationLoop.Script != "" && !IsURL(fc.ValidationLoop.Script) && !isFullsendCachePath(fc.ValidationLoop.Script, opts.WorkspaceRoot) {
			fieldName := fmt.Sprintf("forge.%s.validation_loop.script", platform)
			if err := validateBaseRelPath(fieldName, fc.ValidationLoop.Script); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseScriptOrDir(ctx, fieldName, baseURLDir, fc.ValidationLoop.Script, allowlist, opts)
			if err != nil {
				return nil, err
			}
			fc.ValidationLoop.Script = cachePath
			deps = append(deps, dep)
		}
		if fc.ValidationLoop != nil && fc.ValidationLoop.Schema != "" && !IsURL(fc.ValidationLoop.Schema) && !isFullsendCachePath(fc.ValidationLoop.Schema, opts.WorkspaceRoot) {
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

	// Overlay-level scripts: same fetch treatment as forge-level scripts.
	// Each overlay entry embeds a ForgeConfig whose script, policy, and
	// validation_loop fields may contain relative paths from the base URL.
	for i := range base.Overlays {
		oc := &base.Overlays[i].ForgeConfig
		overlayScripts := []struct {
			name string
			ptr  *string
		}{
			{fmt.Sprintf("overlays[%d].pre_script", i), &oc.PreScript},
			{fmt.Sprintf("overlays[%d].post_script", i), &oc.PostScript},
		}
		for _, f := range overlayScripts {
			if *f.ptr == "" || IsURL(*f.ptr) || isFullsendCachePath(*f.ptr, opts.WorkspaceRoot) {
				continue
			}
			if err := validateBaseRelPath(f.name, *f.ptr); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseScriptOrDir(ctx, f.name, baseURLDir, *f.ptr, allowlist, opts)
			if err != nil {
				return nil, err
			}
			*f.ptr = cachePath
			deps = append(deps, dep)
		}
		if oc.Policy != "" && !IsURL(oc.Policy) && !isFullsendCachePath(oc.Policy, opts.WorkspaceRoot) {
			fieldName := fmt.Sprintf("overlays[%d].policy", i)
			if err := validateBaseRelPath(fieldName, oc.Policy); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, oc.Policy, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			oc.Policy = cachePath
			deps = append(deps, dep)
		}
		if oc.ValidationLoop != nil && oc.ValidationLoop.Script != "" && !IsURL(oc.ValidationLoop.Script) && !isFullsendCachePath(oc.ValidationLoop.Script, opts.WorkspaceRoot) {
			fieldName := fmt.Sprintf("overlays[%d].validation_loop.script", i)
			if err := validateBaseRelPath(fieldName, oc.ValidationLoop.Script); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseScriptOrDir(ctx, fieldName, baseURLDir, oc.ValidationLoop.Script, allowlist, opts)
			if err != nil {
				return nil, err
			}
			oc.ValidationLoop.Script = cachePath
			deps = append(deps, dep)
		}
		if oc.ValidationLoop != nil && oc.ValidationLoop.Schema != "" && !IsURL(oc.ValidationLoop.Schema) && !isFullsendCachePath(oc.ValidationLoop.Schema, opts.WorkspaceRoot) {
			fieldName := fmt.Sprintf("overlays[%d].validation_loop.schema", i)
			if err := validateBaseRelPath(fieldName, oc.ValidationLoop.Schema); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, oc.ValidationLoop.Schema, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			oc.ValidationLoop.Schema = cachePath
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
// already resolved to a path inside fullsend's own cache (see
// isFullsendCachePath) are left unchanged — an earlier step already handled
// them. Any other absolute path is untrusted and is rejected by
// validateBaseRelPath: agent/policy content is read on the host and used as
// the literal agent definition / sandbox policy, so an arbitrary host path
// here is a disclosure risk, not just a resolution bug.
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
		if *f.ptr == "" || IsURL(*f.ptr) || isFullsendCachePath(*f.ptr, opts.WorkspaceRoot) {
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
		if skill.Source != "" && !IsURL(skill.Source) && !isFullsendCachePath(skill.Source, opts.WorkspaceRoot) {
			fieldName := fmt.Sprintf("skills[%d]", i)
			if err := validateBaseRelPath(fieldName, skill.Source); err != nil {
				return nil, err
			}
			dep, localDir, err := fetchBaseSkill(ctx, fieldName, baseURLDir, skill.Source, allowlist, opts)
			if err != nil {
				return nil, err
			}
			base.Skills[i].Source = localDir
			deps = append(deps, dep)
		}

		for key, val := range skill.Overrides {
			if val == nil || *val == "" || IsURL(*val) || isFullsendCachePath(*val, opts.WorkspaceRoot) {
				continue
			}
			overrideField := fmt.Sprintf("skills[%d].overrides[%s]", i, key)
			if err := validateBaseRelPath(overrideField, *val); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, overrideField, baseURLDir, *val, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			resolved := cachePath
			base.Skills[i].Overrides[key] = &resolved
			deps = append(deps, dep)
		}
	}

	// Forge-specific skills: same fetch treatment as top-level skills.
	// resolveBaseScripts already handles forge scripts and forge policy;
	// skills are directories (fetched via fetchBaseSkill) and belong here.
	if fc := base.Forge[opts.ForgePlatform]; fc != nil {
		platform := opts.ForgePlatform
		for i, skill := range fc.Skills {
			if skill.Source != "" && !IsURL(skill.Source) && !isFullsendCachePath(skill.Source, opts.WorkspaceRoot) {
				fieldName := fmt.Sprintf("forge.%s.skills[%d]", platform, i)
				if err := validateBaseRelPath(fieldName, skill.Source); err != nil {
					return nil, err
				}
				dep, localDir, err := fetchBaseSkill(ctx, fieldName, baseURLDir, skill.Source, allowlist, opts)
				if err != nil {
					return nil, err
				}
				fc.Skills[i].Source = localDir
				deps = append(deps, dep)
			}

			for key, val := range skill.Overrides {
				if val == nil || *val == "" || IsURL(*val) || isFullsendCachePath(*val, opts.WorkspaceRoot) {
					continue
				}
				overrideField := fmt.Sprintf("forge.%s.skills[%d].overrides[%s]", platform, i, key)
				if err := validateBaseRelPath(overrideField, *val); err != nil {
					return nil, err
				}
				dep, cachePath, err := fetchBaseFile(ctx, overrideField, baseURLDir, *val, allowlist, opts, "resource", false)
				if err != nil {
					return nil, err
				}
				resolved := cachePath
				fc.Skills[i].Overrides[key] = &resolved
				deps = append(deps, dep)
			}
		}
	}

	// Overlay-specific skills: same fetch treatment as forge-specific skills.
	for i := range base.Overlays {
		oc := &base.Overlays[i].ForgeConfig
		for j, skill := range oc.Skills {
			if skill.Source != "" && !IsURL(skill.Source) && !isFullsendCachePath(skill.Source, opts.WorkspaceRoot) {
				fieldName := fmt.Sprintf("overlays[%d].skills[%d]", i, j)
				if err := validateBaseRelPath(fieldName, skill.Source); err != nil {
					return nil, err
				}
				dep, localDir, err := fetchBaseSkill(ctx, fieldName, baseURLDir, skill.Source, allowlist, opts)
				if err != nil {
					return nil, err
				}
				oc.Skills[j].Source = localDir
				deps = append(deps, dep)
			}

			for key, val := range skill.Overrides {
				if val == nil || *val == "" || IsURL(*val) || isFullsendCachePath(*val, opts.WorkspaceRoot) {
					continue
				}
				overrideField := fmt.Sprintf("overlays[%d].skills[%d].overrides[%s]", i, j, key)
				if err := validateBaseRelPath(overrideField, *val); err != nil {
					return nil, err
				}
				dep, cachePath, err := fetchBaseFile(ctx, overrideField, baseURLDir, *val, allowlist, opts, "resource", false)
				if err != nil {
					return nil, err
				}
				resolved := cachePath
				oc.Skills[j].Overrides[key] = &resolved
				deps = append(deps, dep)
			}
		}
	}

	return deps, nil
}

// resolveBaseHostFiles fetches host_files with relative src paths from a
// URL-referenced base harness. For each host_files entry whose src is a
// non-empty relative path (not a ${VAR} reference, URL, or a path already
// resolved into fullsend's own cache — see isFullsendCachePath), the file is
// fetched from the base URL's directory, cached content-addressed, and the
// src field is rewritten to the local cache path. This ensures host_files
// inherited through base: composition resolve correctly at sandbox setup
// time, the same way scripts and resources do. Any other absolute src is
// untrusted and rejected: host_files are read on the host and uploaded into
// the sandbox, so an arbitrary host path here is a disclosure risk.
func resolveBaseHostFiles(ctx context.Context, base *Harness, baseURL string, allowlist []string, opts ComposeOpts) ([]Dependency, error) {
	baseURLDir := urlParentDirPrefix(baseURL)
	if baseURLDir == "" {
		return nil, fmt.Errorf("cannot determine directory from base URL")
	}

	var deps []Dependency

	for i := range base.HostFiles {
		src := base.HostFiles[i].Src
		if src == "" || strings.Contains(src, "${") || IsURL(src) || isFullsendCachePath(src, opts.WorkspaceRoot) {
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

	// Forge-specific host_files: same fetch treatment as top-level
	// host_files. Entries with ${VAR} expansion are left unchanged
	// (resolved at bootstrap time on the host).
	if fc := base.Forge[opts.ForgePlatform]; fc != nil {
		platform := opts.ForgePlatform
		for i := range fc.HostFiles {
			src := fc.HostFiles[i].Src
			if src == "" || strings.Contains(src, "${") || IsURL(src) || isFullsendCachePath(src, opts.WorkspaceRoot) {
				continue
			}
			fieldName := fmt.Sprintf("forge.%s.host_files[%d].src", platform, i)
			if err := validateBaseRelPath(fieldName, src); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, src, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			fc.HostFiles[i].Src = cachePath
			deps = append(deps, dep)
		}
	}

	// Overlay-specific host_files: same fetch treatment as forge-specific
	// host_files. Entries with ${VAR} expansion are left unchanged.
	for i := range base.Overlays {
		oc := &base.Overlays[i].ForgeConfig
		for j := range oc.HostFiles {
			src := oc.HostFiles[j].Src
			if src == "" || strings.Contains(src, "${") || IsURL(src) || isFullsendCachePath(src, opts.WorkspaceRoot) {
				continue
			}
			fieldName := fmt.Sprintf("overlays[%d].host_files[%d].src", i, j)
			if err := validateBaseRelPath(fieldName, src); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, src, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			oc.HostFiles[j].Src = cachePath
			deps = append(deps, dep)
		}
	}

	return deps, nil
}

// resolveBaseProfiles fetches profile files with relative paths from a
// URL-referenced base harness. For each openshell.profiles entry that is a
// non-empty relative path (not a URL or already-cached path), the file is
// fetched from the base URL's directory, cached content-addressed, and the
// entry is rewritten to the local cache path.
func resolveBaseProfiles(ctx context.Context, base *Harness, baseURL string, allowlist []string, opts ComposeOpts) ([]Dependency, error) {
	hasTopLevel := base.OpenShell != nil && len(base.OpenShell.Profiles) > 0
	hasForge := false
	if fc := base.Forge[opts.ForgePlatform]; fc != nil && fc.OpenShell != nil && len(fc.OpenShell.Profiles) > 0 {
		hasForge = true
	}
	hasOverlay := false
	for _, oe := range base.Overlays {
		if oe.OpenShell != nil && len(oe.OpenShell.Profiles) > 0 {
			hasOverlay = true
			break
		}
	}
	if !hasTopLevel && !hasForge && !hasOverlay {
		return nil, nil
	}

	baseURLDir := urlParentDirPrefix(baseURL)
	if baseURLDir == "" {
		return nil, fmt.Errorf("cannot determine directory from base URL")
	}

	var deps []Dependency

	if base.OpenShell != nil {
		for i, p := range base.OpenShell.Profiles {
			if p == "" || IsURL(p) || isFullsendCachePath(p, opts.WorkspaceRoot) {
				continue
			}
			fieldName := fmt.Sprintf("openshell.profiles[%d]", i)
			if err := validateBaseRelPath(fieldName, p); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, p, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			base.OpenShell.Profiles[i] = cachePath
			deps = append(deps, dep)
		}
	}

	// Forge-specific profiles: same fetch treatment as top-level profiles.
	if fc := base.Forge[opts.ForgePlatform]; fc != nil && fc.OpenShell != nil {
		platform := opts.ForgePlatform
		for i, p := range fc.OpenShell.Profiles {
			if p == "" || IsURL(p) || isFullsendCachePath(p, opts.WorkspaceRoot) {
				continue
			}
			fieldName := fmt.Sprintf("forge.%s.openshell.profiles[%d]", platform, i)
			if err := validateBaseRelPath(fieldName, p); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, p, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			fc.OpenShell.Profiles[i] = cachePath
			deps = append(deps, dep)
		}
	}

	// Overlay-specific profiles: same fetch treatment as forge-specific profiles.
	for i := range base.Overlays {
		oc := &base.Overlays[i].ForgeConfig
		if oc.OpenShell == nil {
			continue
		}
		for j, p := range oc.OpenShell.Profiles {
			if p == "" || IsURL(p) || isFullsendCachePath(p, opts.WorkspaceRoot) {
				continue
			}
			fieldName := fmt.Sprintf("overlays[%d].openshell.profiles[%d]", i, j)
			if err := validateBaseRelPath(fieldName, p); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, p, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			oc.OpenShell.Profiles[j] = cachePath
			deps = append(deps, dep)
		}
	}

	return deps, nil
}

// resolveBaseProviders fetches provider files with relative paths from a
// URL-referenced base harness. For each providers entry that is a non-empty
// relative path (not a URL, already-cached path, or bare provider name),
// the file is fetched from the base URL's directory, cached
// content-addressed, and the entry is rewritten to the local cache path.
func resolveBaseProviders(ctx context.Context, base *Harness, baseURL string, allowlist []string, opts ComposeOpts) ([]Dependency, error) {
	hasTopLevel := len(base.Providers) > 0
	hasForge := false
	if fc := base.Forge[opts.ForgePlatform]; fc != nil && len(fc.Providers) > 0 {
		hasForge = true
	}
	hasOverlay := false
	for _, oe := range base.Overlays {
		if len(oe.Providers) > 0 {
			hasOverlay = true
			break
		}
	}
	if !hasTopLevel && !hasForge && !hasOverlay {
		return nil, nil
	}

	baseURLDir := urlParentDirPrefix(baseURL)
	if baseURLDir == "" {
		return nil, fmt.Errorf("cannot determine directory from base URL")
	}

	var deps []Dependency

	for i, p := range base.Providers {
		if p == "" || IsURL(p) || isFullsendCachePath(p, opts.WorkspaceRoot) {
			continue
		}
		if !IsProviderPath(p) {
			continue
		}
		fieldName := fmt.Sprintf("providers[%d]", i)
		if err := validateBaseRelPath(fieldName, p); err != nil {
			return nil, err
		}
		dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, p, allowlist, opts, "resource", false)
		if err != nil {
			return nil, err
		}
		base.Providers[i] = cachePath
		deps = append(deps, dep)
	}

	// Forge-specific providers: same fetch treatment as top-level providers.
	if fc := base.Forge[opts.ForgePlatform]; fc != nil {
		platform := opts.ForgePlatform
		for i, p := range fc.Providers {
			if p == "" || IsURL(p) || isFullsendCachePath(p, opts.WorkspaceRoot) {
				continue
			}
			if !IsProviderPath(p) {
				continue
			}
			fieldName := fmt.Sprintf("forge.%s.providers[%d]", platform, i)
			if err := validateBaseRelPath(fieldName, p); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, p, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			fc.Providers[i] = cachePath
			deps = append(deps, dep)
		}
	}

	// Overlay-specific providers: same fetch treatment as forge-specific providers.
	for i := range base.Overlays {
		oc := &base.Overlays[i].ForgeConfig
		for j, p := range oc.Providers {
			if p == "" || IsURL(p) || isFullsendCachePath(p, opts.WorkspaceRoot) {
				continue
			}
			if !IsProviderPath(p) {
				continue
			}
			fieldName := fmt.Sprintf("overlays[%d].providers[%d]", i, j)
			if err := validateBaseRelPath(fieldName, p); err != nil {
				return nil, err
			}
			dep, cachePath, err := fetchBaseFile(ctx, fieldName, baseURLDir, p, allowlist, opts, "resource", false)
			if err != nil {
				return nil, err
			}
			oc.Providers[j] = cachePath
			deps = append(deps, dep)
		}
	}

	return deps, nil
}

// resolveBasePlugins fetches plugin directories with relative paths from a
// URL-referenced base harness, following the same pattern as
// resolveBaseResources. Plugins are directories (fetched via fetchBasePlugin)
// that use plugin.json as their marker file instead of SKILL.md.
func resolveBasePlugins(ctx context.Context, base *Harness, baseURL string, allowlist []string, opts ComposeOpts) ([]Dependency, error) {
	if len(base.Plugins) == 0 {
		return nil, nil
	}

	baseURLDir := urlParentDirPrefix(baseURL)
	if baseURLDir == "" {
		return nil, fmt.Errorf("cannot determine directory from base URL")
	}

	var deps []Dependency

	for i, p := range base.Plugins {
		if p == "" || IsURL(p) || isFullsendCachePath(p, opts.WorkspaceRoot) {
			continue
		}
		fieldName := fmt.Sprintf("plugins[%d]", i)
		if err := validateBaseRelPath(fieldName, p); err != nil {
			return nil, err
		}
		if baseName := filepath.Base(p); !ValidPluginBasename(baseName) {
			return nil, fmt.Errorf("base %s path %q does not end in a valid plugin basename (allowed: a-z, A-Z, 0-9, _, -)", fieldName, p)
		}
		dep, localDir, err := fetchBasePlugin(ctx, fieldName, baseURLDir, p, allowlist, opts)
		if err != nil {
			return nil, err
		}
		base.Plugins[i] = localDir
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

			// Preserve the original file extension in the cache path so
			// downstream validators (e.g., Validate for openshell.profiles)
			// see the expected extension instead of bare "content".
			if ext := path.Ext(relPath); ext != "" {
				named, symErr := fetch.CacheNamedSymlink(contentPath, "content"+ext)
				if symErr != nil {
					return Dependency{}, "", fmt.Errorf("base %s: creating extension symlink: %w", field, symErr)
				}
				contentPath = named
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

	// Preserve the original file extension (see cache-hit path above).
	if ext := path.Ext(relPath); ext != "" {
		named, symErr := fetch.CacheNamedSymlink(contentPath, "content"+ext)
		if symErr != nil {
			return Dependency{}, "", fmt.Errorf("base %s: creating extension symlink: %w", field, symErr)
		}
		contentPath = named
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

// fetchBaseScriptOrDir attempts directory-level fetching for a script from a
// URL base so that companion files (helper scripts, Python tools, etc.) are
// co-located at the BASH_SOURCE-relative path. When the URL is a
// raw.githubusercontent.com URL, the script's containing directory is fetched
// via git sparse checkout. When the URL cannot be parsed for tree-level
// fetching, the function falls back to single-file fetching via fetchBaseFile.
func fetchBaseScriptOrDir(ctx context.Context, field, baseURLDir, relPath string, allowlist []string, opts ComposeOpts) (Dependency, string, error) {
	fileURL := baseURLDir + relPath
	scriptDir := path.Dir(relPath)
	scriptName := path.Base(relPath)

	// Only attempt directory-level fetch when the script has a directory
	// component (e.g., "scripts/pre-code.sh", not just "pre-code.sh").
	if scriptDir != "." && scriptDir != "" {
		scriptDirURL := baseURLDir + scriptDir

		// Check directory cache — first via the directory key, then via the
		// file URL key (which also maps to the tree hash when directory-fetched).
		// The second lookup handles offline mode when the scriptdir: key was
		// evicted but the per-file URL index entry persists.
		for _, cacheKey := range []string{"scriptdir:" + scriptDirURL, fileURL} {
			treeHash, indexHit := urlIndexLookup(opts.WorkspaceRoot, cacheKey)
			if !indexHit {
				continue
			}
			treePath, entry, err := fetch.CacheGetDir(opts.WorkspaceRoot, treeHash)
			if err != nil || treePath == "" {
				continue
			}
			treePath, err = fetch.CacheNamedSymlink(treePath, filepath.Base(scriptDir))
			if err != nil {
				return Dependency{}, "", fmt.Errorf("base %s: %w", field, err)
			}
			contentPath := filepath.Join(treePath, scriptName)
			if _, statErr := os.Stat(contentPath); statErr != nil {
				continue
			}
			allowedBy := matchingAllowedPrefix(fileURL, allowlist)
			if allowedBy == "" {
				return Dependency{}, "", fmt.Errorf("base %s: URL %q is not in allowed_remote_resources", field, fileURL)
			}
			if chErr := os.Chmod(contentPath, 0o755); chErr != nil {
				return Dependency{}, "", fmt.Errorf("base %s: setting executable permission on cached file: %w", field, chErr)
			}
			if aErr := auditBaseFetch(opts, fileURL, treeHash, allowedBy, true, entry.FetchTime, "script"); aErr != nil {
				return Dependency{}, "", aErr
			}
			return Dependency{
				Field:     field,
				URL:       fileURL,
				LocalPath: contentPath,
				SHA256:    treeHash,
				FetchedAt: entry.FetchTime,
				CacheHit:  true,
				Type:      "directory",
			}, contentPath, nil
		}

		if !opts.FetchPolicy.Offline {
			// Probe whether the URL is parseable for directory-level fetching.
			if _, parseErr := forge.ParseRawContentURL(scriptDirURL); parseErr == nil {
				allowedBy := matchingAllowedPrefix(fileURL, allowlist)
				if allowedBy == "" {
					return Dependency{}, "", fmt.Errorf("base %s: URL %q is not in allowed_remote_resources", field, fileURL)
				}
				dep, contentPath, err := fetchBaseScriptDirTree(ctx, field, scriptDirURL, fileURL, scriptName, allowedBy, allowlist, opts)
				if err == nil {
					return dep, contentPath, nil
				}
				if !isTransientFetchError(err) {
					return Dependency{}, "", err
				}
				// Transient tree-fetch failure — fall through to single-file fetch.
			}
			// ParseRawContentURL failed — not a raw.githubusercontent.com URL.
			// Fall through to single-file fetch.
		}
	}

	// Fall back to single-file fetch for scripts without a directory
	// component, for non-raw.githubusercontent.com URLs, for offline mode
	// when the directory cache missed, and for transient tree-fetch errors.
	return fetchBaseFile(ctx, field, baseURLDir, relPath, allowlist, opts, "script", true)
}

// fetchBaseScriptDirTree fetches the full directory containing a script via git
// sparse checkout, analogous to fetchBaseSkillDir for skills. All files in the
// directory are cached together so that companion files (helper scripts, Python
// tools, etc.) are available at the BASH_SOURCE-relative path. Only the target
// script is made executable (chmod 0o755), matching fetchBaseFile behavior.
func fetchBaseScriptDirTree(ctx context.Context, field, scriptDirURL, scriptFileURL, scriptName, allowedBy string, allowlist []string, opts ComposeOpts) (Dependency, string, error) {
	dirPrefix := scriptDirURL + "/"
	if ab := matchingAllowedPrefix(dirPrefix, allowlist); ab == "" {
		return Dependency{}, "", fmt.Errorf("base %s: script directory URL %q is not in allowed_remote_resources", field, dirPrefix)
	}

	forgeInfo, err := forge.ParseRawContentURL(scriptDirURL)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: parsing raw URL for script directory fetch: %w", field, err)
	}

	fetcher := opts.TreeFetcher
	if fetcher == nil {
		fetcher = gitfetch.FetchTree
	}

	files, err := fetcher(ctx, forgeInfo.CloneURL(), forgeInfo.Path, forgeInfo.Ref, opts.GitToken)
	if err != nil {
		if opts.GitToken == "" {
			return Dependency{}, "", fmt.Errorf("base %s: fetching script directory %s: %w (hint: set GH_TOKEN or GITHUB_TOKEN for private repos)", field, forgeInfo.Path, err)
		}
		return Dependency{}, "", fmt.Errorf("base %s: fetching script directory %s: %w", field, forgeInfo.Path, err)
	}

	if _, ok := files[scriptName]; !ok {
		return Dependency{}, "", fmt.Errorf("base %s: script %s not found in directory %s", field, scriptName, forgeInfo.Path)
	}

	treeHash, err := fetch.CachePutDir(opts.WorkspaceRoot, scriptFileURL, files, fetch.DirCachePutOpts{FullListing: true})
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: caching script directory %s: %w", field, forgeInfo.Path, err)
	}

	treePath, _, err := fetch.CacheGetDir(opts.WorkspaceRoot, treeHash)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: reading cached script directory %s: %w", field, forgeInfo.Path, err)
	}

	// Create a symlink named after the script directory so downstream
	// consumers see the real directory name instead of "tree".
	treePath, err = fetch.CacheNamedSymlink(treePath, filepath.Base(forgeInfo.Path))
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: %w", field, err)
	}

	contentPath := filepath.Join(treePath, scriptName)
	if chErr := os.Chmod(contentPath, 0o755); chErr != nil {
		return Dependency{}, "", fmt.Errorf("base %s: setting executable permission on %s: %w", field, scriptName, chErr)
	}

	// Update URL index: both per-file and per-directory keys.
	if iErr := urlIndexPut(opts.WorkspaceRoot, scriptFileURL, treeHash); iErr != nil {
		return Dependency{}, "", fmt.Errorf("base %s: updating URL index: %w", field, iErr)
	}
	if iErr := urlIndexPut(opts.WorkspaceRoot, "scriptdir:"+scriptDirURL, treeHash); iErr != nil {
		return Dependency{}, "", fmt.Errorf("base %s: updating URL index for script dir: %w", field, iErr)
	}

	fetchedAt := time.Now().UTC()
	if aErr := auditBaseFetch(opts, scriptFileURL, treeHash, allowedBy, false, fetchedAt, "script"); aErr != nil {
		return Dependency{}, "", aErr
	}

	return Dependency{
		Field:     field,
		URL:       scriptFileURL,
		LocalPath: contentPath,
		SHA256:    treeHash,
		FetchedAt: fetchedAt,
		CacheHit:  false,
		Type:      "directory",
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
				// Apply the same symlink renaming as fetchBaseSkillDir so
				// cache-hit paths return the real skill name, not "tree".
				treePath, err = fetch.CacheNamedSymlink(treePath, filepath.Base(skillPath))
				if err != nil {
					return Dependency{}, "", fmt.Errorf("base %s: %w", field, err)
				}
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
	treePath, err = fetch.CacheNamedSymlink(treePath, filepath.Base(forgeInfo.Path))
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: %w", field, err)
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

// fetchBasePlugin fetches a plugin directory from a URL-referenced base harness.
// It mirrors fetchBaseSkill but uses plugin.json as the marker file instead of
// SKILL.md, and uses "plugin:" as the cache index prefix.
func fetchBasePlugin(ctx context.Context, field, baseURLDir, pluginPath string, allowlist []string, opts ComposeOpts) (Dependency, string, error) {
	pluginDirURL := baseURLDir + pluginPath
	pluginFileURL := pluginDirURL + "/plugin.json"

	allowedBy := matchingAllowedPrefix(pluginFileURL, allowlist)
	if allowedBy == "" {
		return Dependency{}, "", fmt.Errorf("base %s: URL %q is not in allowed_remote_resources", field, pluginFileURL)
	}

	hash, indexHit := urlIndexLookup(opts.WorkspaceRoot, pluginFileURL)
	var staleFallback *Dependency
	var staleFallbackPath string
	if indexHit {
		treeHash, ok := urlIndexLookup(opts.WorkspaceRoot, "plugin:"+pluginFileURL)
		if ok {
			treePath, entry, err := fetch.CacheGetDir(opts.WorkspaceRoot, treeHash)
			if err == nil && treePath != "" {
				treePath, err = fetch.CacheNamedSymlink(treePath, filepath.Base(pluginPath))
				if err != nil {
					return Dependency{}, "", fmt.Errorf("base %s: %w", field, err)
				}
				cachedDep := Dependency{
					Field:     field,
					URL:       pluginFileURL,
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
					if aErr := auditBaseFetch(opts, pluginFileURL, treeHash, allowedBy, true, entry.FetchTime, "plugin"); aErr != nil {
						return Dependency{}, "", aErr
					}
					if cErr := ChmodPluginDir(treePath); cErr != nil {
						return Dependency{}, "", fmt.Errorf("base %s: setting plugin permissions: %w", field, cErr)
					}
					return cachedDep, treePath, nil
				}
			}
		}
		_ = hash
	}

	if opts.FetchPolicy.Offline {
		// staleFallback is only set when Offline=false (line above), so it
		// is always nil here; skip the nil guard and go straight to the
		// cache-miss error.
		return Dependency{}, "", fmt.Errorf("base %s: URL %s not in cache and offline mode is enabled (run 'fullsend lock' first)", field, pluginFileURL)
	}

	dep, dirPath, err := fetchBasePluginDir(ctx, field, pluginDirURL, pluginFileURL, pluginPath, allowedBy, allowlist, opts)
	if err != nil && staleFallback != nil {
		if !isTransientFetchError(err) {
			return Dependency{}, "", err
		}
		staleFallback.Warning = fmt.Sprintf("using stale cached content (re-fetch failed: %s)", err)
		if cErr := ChmodPluginDir(staleFallbackPath); cErr != nil {
			return Dependency{}, "", fmt.Errorf("base %s: setting plugin permissions: %w", field, cErr)
		}
		return *staleFallback, staleFallbackPath, nil
	}
	return dep, dirPath, err
}

// fetchBasePluginDir fetches the full plugin directory via git sparse checkout.
func fetchBasePluginDir(ctx context.Context, field, pluginDirURL, pluginFileURL, pluginPath, allowedBy string, allowlist []string, opts ComposeOpts) (Dependency, string, error) {
	dirPrefix := pluginDirURL + "/"
	if ab := matchingAllowedPrefix(dirPrefix, allowlist); ab == "" {
		return Dependency{}, "", fmt.Errorf("base %s: plugin directory URL %q is not in allowed_remote_resources", field, dirPrefix)
	}

	forgeInfo, err := forge.ParseRawContentURL(pluginDirURL)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: parsing raw URL for plugin directory fetch: %w", field, err)
	}

	fetcher := opts.TreeFetcher
	if fetcher == nil {
		fetcher = gitfetch.FetchTree
	}

	files, err := fetcher(ctx, forgeInfo.CloneURL(), forgeInfo.Path, forgeInfo.Ref, opts.GitToken)
	if err != nil {
		if opts.GitToken == "" {
			return Dependency{}, "", fmt.Errorf("base %s: fetching plugin directory %s: %w (hint: set GH_TOKEN or GITHUB_TOKEN for private repos)", field, pluginPath, err)
		}
		return Dependency{}, "", fmt.Errorf("base %s: fetching plugin directory %s: %w", field, pluginPath, err)
	}

	if _, ok := files["plugin.json"]; !ok {
		return Dependency{}, "", fmt.Errorf("base %s: plugin directory %s has no plugin.json", field, pluginPath)
	}

	treeHash, err := fetch.CachePutDir(opts.WorkspaceRoot, pluginFileURL, files, fetch.DirCachePutOpts{FullListing: true})
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: caching plugin directory: %w", field, err)
	}

	treePath, _, err := fetch.CacheGetDir(opts.WorkspaceRoot, treeHash)
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: reading cached plugin directory: %w", field, err)
	}

	treePath, err = fetch.CacheNamedSymlink(treePath, filepath.Base(forgeInfo.Path))
	if err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: %w", field, err)
	}

	if iErr := urlIndexPut(opts.WorkspaceRoot, pluginFileURL, treeHash); iErr != nil {
		return Dependency{}, "", fmt.Errorf("base %s: updating URL index: %w", field, iErr)
	}
	if iErr := urlIndexPut(opts.WorkspaceRoot, "plugin:"+pluginFileURL, treeHash); iErr != nil {
		return Dependency{}, "", fmt.Errorf("base %s: updating URL index for plugin tree: %w", field, iErr)
	}

	fetchedAt := time.Now().UTC()
	if aErr := auditBaseFetch(opts, pluginFileURL, treeHash, allowedBy, false, fetchedAt, "plugin"); aErr != nil {
		return Dependency{}, "", aErr
	}

	if err := ChmodPluginDir(treePath); err != nil {
		return Dependency{}, "", fmt.Errorf("base %s: setting plugin permissions: %w", field, err)
	}

	return Dependency{
		Field:     field,
		URL:       pluginFileURL,
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

// mergeSkills concatenates base and child skill entries, with child entries
// overriding base entries that resolve to the same sandbox directory name
// (filepath.Base). When both base and child define the same basename, the
// child's source replaces the base's, and their Overrides maps are merged
// (child keys win). This mirrors mergeHostFiles' override-by-dest behavior
// and allows a child harness to replace a built-in skill via base:
// composition (see #5408).
//
// Known limitation: if the base slice itself contains two entries with the
// same basename (e.g., /cache/a/skill-x and /cache/b/skill-x), the second
// entry silently overwrites the first in baseIndex. In practice this is
// benign because duplicateDestinationNameError at bootstrap time catches
// duplicate basenames within a single harness.
func mergeSkills(base, child []SkillEntry) []SkillEntry {
	baseIndex := make(map[string]int, len(base))
	result := make([]SkillEntry, 0, len(base)+len(child))

	// Add base entries
	for _, s := range base {
		name := filepath.Base(s.Source)
		baseIndex[name] = len(result)
		result = append(result, s)
	}

	// Add/override with child entries
	for _, s := range child {
		name := filepath.Base(s.Source)
		if idx, exists := baseIndex[name]; exists {
			// Merge overrides: child values win per key
			merged := SkillEntry{
				Source:    s.Source,
				Overrides: mergeOverrides(result[idx].Overrides, s.Overrides),
			}
			result[idx] = merged
		} else {
			baseIndex[name] = len(result)
			result = append(result, s)
		}
	}

	return result
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
// Similar to mergeForgeConfig in forge.go but prepends base skills and host files
// (base + child order) rather than appending forge values to harness values.
func mergeForgeConfigInto(base, child *ForgeConfig) {
	if base == nil {
		return
	}

	// Scalars: child overrides if non-empty
	if child.Policy == "" {
		child.Policy = base.Policy
	}
	if child.PreScript == "" {
		child.PreScript = base.PreScript
	}
	if child.PostScript == "" {
		child.PostScript = base.PostScript
	}

	// Skills: base + child with child-overrides-base-by-basename
	if base.Skills != nil || child.Skills != nil {
		child.Skills = mergeSkills(base.Skills, child.Skills)
	}

	// Providers: base + child (concatenated)
	if base.Providers != nil {
		merged := make([]string, 0, len(base.Providers)+len(child.Providers))
		merged = append(merged, base.Providers...)
		merged = append(merged, child.Providers...)
		child.Providers = merged
	}

	// OpenShell.Profiles: base + child (concatenated)
	if base.OpenShell != nil && len(base.OpenShell.Profiles) > 0 {
		if child.OpenShell == nil {
			child.OpenShell = &OpenShellConfig{}
		}
		merged := make([]string, 0, len(base.OpenShell.Profiles)+len(child.OpenShell.Profiles))
		merged = append(merged, base.OpenShell.Profiles...)
		merged = append(merged, child.OpenShell.Profiles...)
		child.OpenShell.Profiles = merged
	}

	// HostFiles: concatenated with last-writer-wins dedup by Dest
	if base.HostFiles != nil {
		child.HostFiles = mergeHostFiles(base.HostFiles, child.HostFiles)
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

	// ValidationLoop: child replaces if non-nil, but carry forward
	// PreflightCheck when the child overrides validation_loop without
	// setting its own preflight_check (see #5074).
	if child.ValidationLoop == nil {
		child.ValidationLoop = base.ValidationLoop
	} else if child.ValidationLoop.PreflightCheck == "" && base.ValidationLoop != nil {
		child.ValidationLoop.PreflightCheck = base.ValidationLoop.PreflightCheck
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
