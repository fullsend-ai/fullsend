package resolve

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/skill"
)

const (
	DefaultMaxDepth     = 10
	DefaultMaxResources = 50
)

// Dependency records a single URL that was resolved to a local cache path.
type Dependency struct {
	URL       string
	LocalPath string
	SHA256    string
	FetchedAt time.Time
	CacheHit  bool
}

// ResolveOpts controls how URL-referenced resources are resolved.
type ResolveOpts struct {
	WorkspaceRoot string
	FetchPolicy   fetch.FetchPolicy
	TraceID       string
	AuditLogPath  string

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
	maxDepth      int
	maxResources  int
}

// ResolveHarness resolves URL-referenced declarative fields (Agent, Policy,
// Skills) in the harness to local cache paths. Local paths are left unchanged.
// The harness is modified in place: URL fields are replaced with cache paths,
// and h.Skills may grow to include transitively resolved skill dependencies.
// Returns the deduplicated list of resolved dependencies.
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
func ResolveHarness(ctx context.Context, h *harness.Harness, opts ResolveOpts) ([]Dependency, error) {
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
		dep, localPath, err := resolveURL(ctx, "agent", h.Agent, h, opts, state, false, 0)
		if err != nil {
			return nil, fmt.Errorf("resolving agent: %w", err)
		}
		h.Agent = localPath
		state.appendDependency(dep)
	}

	if h.Policy != "" && harness.IsURL(h.Policy) {
		dep, localPath, err := resolveURL(ctx, "policy", h.Policy, h, opts, state, false, 0)
		if err != nil {
			return nil, fmt.Errorf("resolving policy: %w", err)
		}
		h.Policy = localPath
		state.appendDependency(dep)
	}

	for i, s := range h.Skills {
		if harness.IsURL(s) {
			dep, localPath, err := resolveURL(ctx, fmt.Sprintf("skills[%d]", i), s, h, opts, state, recurse, 0)
			if err != nil {
				return nil, fmt.Errorf("resolving skills[%d]: %w", i, err)
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

	return state.deps, nil
}

func (s *resolveState) appendDependency(dep Dependency) {
	if s.inDeps[dep.URL] {
		return
	}
	s.inDeps[dep.URL] = true
	s.deps = append(s.deps, dep)
}

func resolveURL(ctx context.Context, field, rawURL string, h *harness.Harness,
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
		return dep, dep.LocalPath, nil
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

	if recurse {
		if err := resolveTransitiveDeps(ctx, cleanURL, content, h, opts, state, depth+1); err != nil {
			return Dependency{}, "", fmt.Errorf("resolving transitive deps for %s (%s): %w", field, cleanURL, err)
		}
	}

	dep := Dependency{
		URL:       cleanURL,
		LocalPath: localPath,
		SHA256:    expectedHash,
		FetchedAt: fetchedAt,
		CacheHit:  cacheHit,
	}

	state.resolved[cleanURL] = dep

	return dep, localPath, nil
}

// resolveTransitiveDeps parses skill frontmatter and recursively resolves
// declared dependencies. Policy references are fetched as leaf nodes.
// depth is the current nesting level (1 for first-level transitive deps).
func resolveTransitiveDeps(ctx context.Context, parentURL string, content []byte,
	h *harness.Harness, opts ResolveOpts, state *resolveState, depth int,
) error {
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
		dep, localPath, err := resolveURL(ctx, field, resolved, h, opts, state, true, depth)
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
		dep, _, err := resolveURL(ctx, field, resolved, h, opts, state, false, depth)
		if err != nil {
			return err
		}

		state.appendDependency(dep)
	}

	return nil
}
