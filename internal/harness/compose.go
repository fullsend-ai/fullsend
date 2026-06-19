package harness

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/fetch"
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

	// OrgAllowlist is the org-level allowed_remote_resources from config.yaml.
	// Base URLs must match a prefix in this list.
	OrgAllowlist []string

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
		// No base — same as LoadWithOpts
		if err := child.validateForge(); err != nil {
			return nil, nil, fmt.Errorf("invalid harness: %w", err)
		}
		if err := child.ResolveForge(opts.ForgePlatform); err != nil {
			return nil, nil, fmt.Errorf("resolving forge config: %w", err)
		}
		if err := child.Validate(); err != nil {
			return nil, nil, fmt.Errorf("invalid harness: %w", err)
		}
		return child, nil, nil
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

		// For URL bases, relative paths in the base resolve against the child's directory
		// (scripts are always local per ADR-0038's "no remote executables" rule)
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

	// ValidationLoop: child replaces if non-nil
	if child.ValidationLoop == nil {
		child.ValidationLoop = base.ValidationLoop
	}
}
