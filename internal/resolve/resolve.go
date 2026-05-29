package resolve

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
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
}

// ResolveHarness resolves URL-referenced declarative fields (Agent, Policy,
// Skills) in the harness to local cache paths. Local paths are left unchanged.
// The harness is modified in place. Returns the list of resolved dependencies.
//
// Phase 1: single-level resolution only (no transitive deps).
func ResolveHarness(ctx context.Context, h *harness.Harness, opts ResolveOpts) ([]Dependency, error) {
	var deps []Dependency

	if h.Agent != "" && harness.IsURL(h.Agent) {
		dep, localPath, err := resolveURL(ctx, "agent", h.Agent, h, opts)
		if err != nil {
			return nil, fmt.Errorf("resolving agent: %w", err)
		}
		h.Agent = localPath
		deps = append(deps, dep)
	}

	if h.Policy != "" && harness.IsURL(h.Policy) {
		dep, localPath, err := resolveURL(ctx, "policy", h.Policy, h, opts)
		if err != nil {
			return nil, fmt.Errorf("resolving policy: %w", err)
		}
		h.Policy = localPath
		deps = append(deps, dep)
	}

	for i, s := range h.Skills {
		if harness.IsURL(s) {
			dep, localPath, err := resolveURL(ctx, fmt.Sprintf("skills[%d]", i), s, h, opts)
			if err != nil {
				return nil, fmt.Errorf("resolving skills[%d]: %w", i, err)
			}
			h.Skills[i] = localPath
			deps = append(deps, dep)
		}
	}

	return deps, nil
}

func resolveURL(ctx context.Context, field, rawURL string, h *harness.Harness, opts ResolveOpts) (Dependency, string, error) {
	cleanURL, expectedHash, hasHash := harness.ParseIntegrityHash(rawURL)
	if !hasHash {
		return Dependency{}, "", fmt.Errorf("%s URL must include #sha256=... integrity hash", field)
	}

	allowedBy := h.MatchingAllowedPrefix(cleanURL)
	if allowedBy == "" {
		return Dependency{}, "", fmt.Errorf("%s URL %q is not in allowed_remote_resources", field, cleanURL)
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
			return Dependency{}, "", fmt.Errorf("%s integrity check failed: expected %s, got %s", field, expectedHash, actualHash)
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

	return Dependency{
		URL:       cleanURL,
		LocalPath: localPath,
		SHA256:    expectedHash,
		FetchedAt: fetchedAt,
		CacheHit:  cacheHit,
	}, localPath, nil
}
