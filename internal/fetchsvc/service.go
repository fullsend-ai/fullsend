package fetchsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
)

// FetchRequest is a runtime skill fetch request from an in-sandbox agent.
type FetchRequest struct {
	URL string `json:"url"` // full URL including #sha256=<tree-hash>
}

// FetchResponse is returned after a fetch attempt.
type FetchResponse struct {
	LocalPath string `json:"local_path,omitempty"` // sandbox-local skill directory path
	Error     string `json:"error,omitempty"`
}

// Uploader abstracts uploading skill directories into the sandbox.
// Production calls sandbox.UploadDir; tests use a stub.
type Uploader interface {
	UploadSkillDir(sandboxName, localPath, remotePath string) error
}

// ServiceConfig holds configuration for creating a new Service.
type ServiceConfig struct {
	Harness       *harness.Harness
	ForgeClient   forge.Client
	FetchPolicy   fetch.FetchPolicy
	WorkspaceRoot string // host-side root for .fullsend-cache/
	AuditLogPath  string
	TraceID       string
	SandboxName   string
	MaxFetches    int      // 0 → DefaultMaxFetches (10)
	Uploader      Uploader // nil → skip upload step
	SkillDestDir  string   // sandbox skill directory prefix
}

// Service handles runtime skill fetch requests from agents running in sandboxes.
type Service struct {
	harness       *harness.Harness
	forgeClient   forge.Client
	fetchPolicy   fetch.FetchPolicy
	workspaceRoot string
	auditLogPath  string
	traceID       string
	sandboxName   string
	uploader      Uploader
	skillDestDir  string
	limiter       *RateLimiter
	mu            sync.Mutex
}

// New creates a runtime fetch service.
func New(cfg ServiceConfig) *Service {
	skillDest := cfg.SkillDestDir
	if skillDest == "" {
		skillDest = "/sandbox/claude-config/skills"
	}
	return &Service{
		harness:       cfg.Harness,
		forgeClient:   cfg.ForgeClient,
		fetchPolicy:   cfg.FetchPolicy,
		workspaceRoot: cfg.WorkspaceRoot,
		auditLogPath:  cfg.AuditLogPath,
		traceID:       cfg.TraceID,
		sandboxName:   cfg.SandboxName,
		uploader:      cfg.Uploader,
		skillDestDir:  skillDest,
		limiter:       NewRateLimiter(cfg.MaxFetches),
	}
}

// HandleFetch processes a single runtime skill fetch request.
func (s *Service) HandleFetch(ctx context.Context, req FetchRequest) FetchResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.URL == "" {
		return FetchResponse{Error: "url is required"}
	}

	if !harness.IsURL(req.URL) {
		return FetchResponse{Error: "url must be a valid HTTPS URL"}
	}

	cleanURL, expectedHash, hasHash := harness.ParseIntegrityHash(req.URL)
	if !hasHash {
		return FetchResponse{Error: fmt.Sprintf("url must include #sha256=... integrity hash: %s", cleanURL)}
	}

	allowedBy := s.harness.MatchingAllowedPrefix(cleanURL)
	if allowedBy == "" {
		return FetchResponse{Error: fmt.Sprintf("url %q is not in allowed_remote_resources", cleanURL)}
	}

	if !s.limiter.Allow() {
		return FetchResponse{Error: fmt.Sprintf("runtime fetch rate limit exceeded (max %d per run)", s.limiter.max)}
	}

	forgeInfo, err := forge.ParseForgeURL(cleanURL)
	if err != nil {
		return FetchResponse{Error: fmt.Sprintf("skill URLs must be hosted on a supported forge: %v", err)}
	}

	treePath, dirEntry, err := fetch.CacheGetDir(s.workspaceRoot, expectedHash)
	if err != nil {
		return FetchResponse{Error: fmt.Sprintf("cache lookup failed: %v", err)}
	}

	cacheHit := treePath != ""
	fetchedAt := time.Now().UTC()

	if !cacheHit {
		if s.forgeClient == nil {
			return FetchResponse{Error: fmt.Sprintf("forge client is required to fetch skill %s (not cached)", cleanURL)}
		}
		if s.fetchPolicy.Offline {
			return FetchResponse{Error: fmt.Sprintf("skill %s not in cache and offline mode is enabled", cleanURL)}
		}

		entries, err := s.forgeClient.ListDirectoryContents(ctx, forgeInfo.Owner, forgeInfo.Repo, forgeInfo.Path, forgeInfo.Ref, true)
		if err != nil {
			return FetchResponse{Error: fmt.Sprintf("listing directory for %s: %v", cleanURL, err)}
		}

		files := make(map[string][]byte)
		for _, e := range entries {
			if e.Type != "file" {
				continue
			}
			var fullPath string
			if forgeInfo.Path == "" {
				fullPath = e.Path
			} else {
				fullPath = forgeInfo.Path + "/" + e.Path
			}
			content, err := s.forgeClient.GetFileContentAtRef(ctx, forgeInfo.Owner, forgeInfo.Repo, fullPath, forgeInfo.Ref)
			if err != nil {
				return FetchResponse{Error: fmt.Sprintf("fetching file %s: %v", e.Path, err)}
			}
			files[e.Path] = content
		}

		actualHash := fetch.ComputeTreeHash(files)
		if actualHash != expectedHash {
			return FetchResponse{Error: fmt.Sprintf("integrity check failed for %s: expected %s, got %s", cleanURL, expectedHash, actualHash)}
		}

		if _, err := fetch.CachePutDir(s.workspaceRoot, cleanURL, files); err != nil {
			return FetchResponse{Error: fmt.Sprintf("caching skill directory: %v", err)}
		}

		cachePath, err := fetch.CachePath(s.workspaceRoot, expectedHash)
		if err != nil {
			return FetchResponse{Error: fmt.Sprintf("computing cache path: %v", err)}
		}
		treePath = filepath.Join(cachePath, "tree")
	} else if dirEntry != nil {
		fetchedAt = dirEntry.FetchTime
	}

	// Derive sandbox destination path from hash prefix + skill basename.
	basename := filepath.Base(forgeInfo.Path)
	if basename == "" || basename == "." {
		basename = "skill"
	}
	hashPrefix := expectedHash
	if len(hashPrefix) > 8 {
		hashPrefix = hashPrefix[:8]
	}
	remotePath := filepath.Join(s.skillDestDir, hashPrefix+"-"+basename)

	if s.uploader != nil {
		if err := s.uploader.UploadSkillDir(s.sandboxName, treePath, remotePath); err != nil {
			return FetchResponse{Error: fmt.Sprintf("uploading skill to sandbox: %v", err)}
		}
	}

	if s.auditLogPath != "" {
		if err := fetch.AppendFetchAudit(s.auditLogPath, fetch.FetchAuditEntry{
			TraceID:   s.traceID,
			FetchTime: fetchedAt,
			URL:       cleanURL,
			SHA256:    expectedHash,
			FetchType: "runtime",
			AllowedBy: allowedBy,
			CacheHit:  cacheHit,
		}); err != nil {
			return FetchResponse{Error: fmt.Sprintf("writing audit log: %v", err)}
		}
	}

	return FetchResponse{LocalPath: remotePath}
}

// ServeHTTP implements http.Handler for HTTP-based transports.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req FetchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, FetchResponse{Error: "invalid request body"})
		return
	}

	resp := s.HandleFetch(r.Context(), req)

	status := http.StatusOK
	if resp.Error != "" {
		status = classifyError(resp.Error)
	}
	writeJSON(w, status, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func classifyError(errMsg string) int {
	switch {
	case strings.Contains(errMsg, "rate limit exceeded"):
		return http.StatusTooManyRequests
	case strings.Contains(errMsg, "not in allowed_remote_resources"):
		return http.StatusForbidden
	case strings.Contains(errMsg, "url is required"),
		strings.Contains(errMsg, "url must be"),
		strings.Contains(errMsg, "url must include"),
		strings.Contains(errMsg, "invalid request body"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
