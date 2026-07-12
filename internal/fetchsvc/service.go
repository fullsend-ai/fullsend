// Package fetchsvc provides a runtime skill fetch service for agents running
// in sandboxes. It validates, fetches, caches, and uploads skill directories
// on behalf of in-sandbox agent processes.
package fetchsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
)

const maxRequestBytes = 1 << 20 // 1 MB

// fetchError carries an HTTP status code alongside the error message,
// eliminating string-based error classification for status mapping.
type fetchError struct {
	msg    string
	status int
}

func (e *fetchError) Error() string { return e.msg }

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
type Uploader interface {
	UploadSkillDir(sandboxName, localPath, remotePath string) error
}

// ServiceConfig holds configuration for creating a new Service.
type ServiceConfig struct {
	Harness       *harness.Harness
	TreeFetcher   gitfetch.TreeFetchFunc
	GitToken      string
	FetchPolicy   fetch.FetchPolicy
	WorkspaceRoot string // host-side root for .fullsend-cache/
	AuditLogPath  string
	TraceID       string
	SandboxName   string
	MaxFetches    int      // 0 → DefaultMaxFetches (10)
	Uploader      Uploader // nil → skip upload step
	SkillDestDir  string   // "" → /sandbox/claude-config/skills
}

// Service handles runtime skill fetch requests from agents running in sandboxes.
type Service struct {
	harness       *harness.Harness
	treeFetcher   gitfetch.TreeFetchFunc
	gitToken      string
	fetchPolicy   fetch.FetchPolicy
	workspaceRoot string
	auditLogPath  string
	traceID       string
	sandboxName   string
	uploader      Uploader
	skillDestDir  string
	limiter       *RateLimiter
}

// New creates a runtime fetch service.
func New(cfg ServiceConfig) *Service {
	skillDest := cfg.SkillDestDir
	if skillDest == "" {
		skillDest = "/sandbox/claude-config/skills"
	}
	fetcher := cfg.TreeFetcher
	if fetcher == nil {
		fetcher = gitfetch.FetchTree
	}
	return &Service{
		harness:       cfg.Harness,
		treeFetcher:   fetcher,
		gitToken:      cfg.GitToken,
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
// On success it returns a FetchResponse with LocalPath set and a nil error.
// On failure it returns a *fetchError with an appropriate HTTP status code.
func (s *Service) HandleFetch(ctx context.Context, req FetchRequest) (FetchResponse, error) {
	if req.URL == "" {
		return FetchResponse{}, &fetchError{"url is required", http.StatusBadRequest}
	}

	if !harness.IsURL(req.URL) {
		return FetchResponse{}, &fetchError{"url must be a valid HTTPS URL", http.StatusBadRequest}
	}

	cleanURL, expectedHash, hasHash := harness.ParseIntegrityHash(req.URL)
	if !hasHash {
		return FetchResponse{}, &fetchError{"url must include #sha256=... integrity hash", http.StatusBadRequest}
	}

	allowedBy := s.harness.MatchingAllowedPrefix(cleanURL)
	if allowedBy == "" {
		return FetchResponse{}, &fetchError{
			fmt.Sprintf("url %q is not in allowed_remote_resources", cleanURL),
			http.StatusForbidden,
		}
	}

	forgeInfo, err := forge.ParseForgeURL(cleanURL)
	if err != nil {
		return FetchResponse{}, &fetchError{"skill URLs must be hosted on a supported forge", http.StatusBadRequest}
	}
	if forgeInfo.Forge != "github" {
		return FetchResponse{}, &fetchError{fmt.Sprintf("forge %q is recognized but fetch support has not landed yet", forgeInfo.Forge), http.StatusBadRequest}
	}

	if forgeInfo.Path == "" {
		return FetchResponse{}, &fetchError{"skill URL must include a path to a directory", http.StatusBadRequest}
	}

	if !s.limiter.Allow() {
		return FetchResponse{}, &fetchError{
			"runtime fetch rate limit exceeded",
			http.StatusTooManyRequests,
		}
	}
	committed := false
	defer func() {
		if !committed {
			s.limiter.Release()
		}
	}()

	treePath, dirEntry, err := fetch.CacheGetDir(s.workspaceRoot, expectedHash)
	if err != nil {
		return FetchResponse{}, &fetchError{"internal error during cache lookup", http.StatusInternalServerError}
	}

	cacheHit := treePath != ""
	fetchedAt := time.Now().UTC()

	if !cacheHit {
		if s.fetchPolicy.Offline {
			return FetchResponse{}, &fetchError{"skill not in cache and offline mode is enabled", http.StatusServiceUnavailable}
		}

		files, err := s.treeFetcher(ctx, forgeInfo.CloneURL(), forgeInfo.Path, forgeInfo.Ref, s.gitToken)
		if err != nil {
			return FetchResponse{}, &fetchError{"failed to fetch skill directory", http.StatusBadGateway}
		}

		if len(files) == 0 {
			return FetchResponse{}, &fetchError{"skill directory contains no files", http.StatusUnprocessableEntity}
		}

		actualHash := fetch.ComputeTreeHash(files)
		if actualHash != expectedHash {
			return FetchResponse{}, &fetchError{
				"integrity check failed",
				http.StatusUnprocessableEntity,
			}
		}

		treeHash, err := fetch.CachePutDir(s.workspaceRoot, cleanURL, files)
		if err != nil {
			return FetchResponse{}, &fetchError{"failed to cache skill directory", http.StatusInternalServerError}
		}

		cachePath, err := fetch.CachePath(s.workspaceRoot, treeHash)
		if err != nil {
			return FetchResponse{}, &fetchError{"internal error computing cache path", http.StatusInternalServerError}
		}
		treePath = filepath.Join(cachePath, "tree")
	} else if dirEntry != nil {
		fetchedAt = dirEntry.FetchTime
	}

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
			return FetchResponse{}, &fetchError{"failed to upload skill to sandbox", http.StatusInternalServerError}
		}
	}

	committed = true

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
			log.Printf("fetchsvc: audit log write failed: %v", err)
		}
	}

	return FetchResponse{LocalPath: remotePath}, nil
}

// ServeHTTP implements http.Handler for HTTP-based transports.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)

	var req FetchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, FetchResponse{Error: "request body too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, FetchResponse{Error: "invalid request body"})
		return
	}

	resp, err := s.HandleFetch(r.Context(), req)
	if err != nil {
		var fe *fetchError
		status := http.StatusInternalServerError
		if errors.As(err, &fe) {
			status = fe.status
			resp.Error = fe.msg
		} else {
			resp.Error = "internal error"
		}
		writeJSON(w, status, resp)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"response encoding failed"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n"))
}
