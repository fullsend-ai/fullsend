// Package devmint implements a standalone token mint server for local development.
// It serves GitHub App installation tokens over HTTP using OIDC verification,
// storing PEM keys on disk instead of in GCP Secret Manager. Use fullsend mint run
// to start the server; see docs/guides/infrastructure/dev-mint.md for setup.
package devmint

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sort"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

const githubAPIBaseURL = "https://api.github.com"

type tokenRequest struct {
	Role  string   `json:"role"`
	Repos []string `json:"repos"`
}

type tokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

type statusResponse struct {
	Org   string       `json:"org"`
	Roles []roleStatus `json:"roles"`
}

type roleStatus struct {
	Role  string `json:"role"`
	AppID string `json:"app_id"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// DiskConfig is the on-disk format for the dev mint's config.json.
type DiskConfig struct {
	Org   string                    `json:"org"`
	Roles map[string]DiskRoleConfig `json:"roles"`
}

// DiskRoleConfig is a single role entry within DiskConfig.
type DiskRoleConfig struct {
	AppID string `json:"app_id"`
}

const maxRequestBodyLen = 64 << 10 // 64 KiB
const maxRepos = 500

type Server struct {
	dataDir        string
	addr           string
	logger         *log.Logger
	githubBaseURL  string
	httpClient     *http.Client
	insecureNoAuth bool
	oidcAudience   string
	oidcVerifier   mintcore.OIDCVerifier

	allowedWorkflows []string
	perRepoWIFRepos  map[string]bool

	mu     sync.RWMutex
	org    string
	pems   map[string][]byte
	appIDs map[string]string
}

type Options struct {
	DataDir          string
	Bind             string
	Port             int
	Logger           *log.Logger
	InsecureNoAuth   bool
	OIDCAudience     string
	AllowedWorkflows []string
	PerRepoWIFRepos  map[string]bool
}

func New(opts Options) *Server {
	s := &Server{
		dataDir:          opts.DataDir,
		addr:             net.JoinHostPort(opts.Bind, fmt.Sprintf("%d", opts.Port)),
		logger:           opts.Logger,
		githubBaseURL:    githubAPIBaseURL,
		httpClient:       &http.Client{Timeout: 30 * time.Second},
		insecureNoAuth:   opts.InsecureNoAuth,
		oidcAudience:     opts.OIDCAudience,
		allowedWorkflows: opts.AllowedWorkflows,
		pems:             make(map[string][]byte),
		appIDs:           make(map[string]string),
	}
	if opts.PerRepoWIFRepos != nil {
		s.perRepoWIFRepos = opts.PerRepoWIFRepos
	} else {
		s.perRepoWIFRepos = make(map[string]bool)
	}
	return s
}

func (s *Server) loadFromDisk() error {
	configPath := filepath.Join(s.dataDir, "config.json")
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}

	var cfg DiskConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Org != "" {
		if err := mintcore.ValidateOrgName(cfg.Org); err != nil {
			return fmt.Errorf("invalid org in config.json: %w", err)
		}
	}

	newPems := make(map[string][]byte, len(cfg.Roles))
	newAppIDs := make(map[string]string, len(cfg.Roles))

	for role, rc := range cfg.Roles {
		if err := mintcore.ValidateRoleName(role); err != nil {
			return err
		}
		newAppIDs[role] = rc.AppID
		pemPath := filepath.Join(s.dataDir, "pems", role+".pem")
		pemData, err := os.ReadFile(pemPath)
		if err != nil {
			return fmt.Errorf("reading PEM for role %s: %w", role, err)
		}
		newPems[role] = pemData
	}

	s.mu.Lock()
	s.org = cfg.Org
	s.pems = newPems
	s.appIDs = newAppIDs
	s.mu.Unlock()

	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		s.handleHealth(w, r)
	case "/v1/token", "/":
		s.handleToken(w, r)
	case "/v1/status":
		s.handleStatus(w, r)
	default:
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "not found"})
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	if !s.insecureNoAuth {
		if s.oidcVerifier == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "OIDC verifier not initialized — call Start() first"})
			return
		}
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "missing or invalid Authorization header"})
			return
		}
		oidcToken := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := s.oidcVerifier.Verify(r.Context(), oidcToken)
		if err != nil {
			s.logger.Printf("[MINT] OIDC verification failed: %v", err)
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "authentication failed"})
			return
		}
		if claims == nil {
			// Fail-closed: treat (nil, nil) as an auth failure rather than skip checks.
			s.logger.Printf("[MINT] OIDC verifier returned nil claims without error")
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "authentication failed"})
			return
		}
		s.logger.Printf("[MINT] token request from repo=%s workflow=%s", claims.Repository, claims.JobWorkflowRef)
		// Defense-in-depth: JWKSVerifier already enforces AllowedOrgs, but
		// check explicitly here to match the production handler pattern.
		s.mu.RLock()
		org := s.org
		s.mu.RUnlock()
		if claims.RepositoryOwner != org {
			s.logger.Printf("[MINT] OIDC org mismatch: got %q, want %q", claims.RepositoryOwner, org)
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "authentication failed"})
			return
		}
	}

	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyLen))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "failed to read request body"})
		return
	}

	var req tokenRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body"})
		return
	}

	if req.Role == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "role is required"})
		return
	}
	if err := mintcore.ValidateRoleName(req.Role); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid role format"})
		return
	}
	if len(req.Repos) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "repos is required"})
		return
	}
	if len(req.Repos) > maxRepos {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("too many repos (max %d)", maxRepos)})
		return
	}
	for _, repo := range req.Repos {
		if !mintcore.RepoNamePattern.MatchString(repo) || strings.Contains(repo, "..") {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid repo name"})
			return
		}
	}

	s.mu.RLock()
	pemData := s.pems[req.Role]
	appID := s.appIDs[req.Role]
	org := s.org
	s.mu.RUnlock()

	if len(pemData) == 0 || appID == "" {
		s.logger.Printf("[MINT] no PEM configured for role %q", req.Role)
		writeJSON(w, http.StatusInternalServerError, errorResponse{
			Error: fmt.Sprintf("no PEM configured for role %q", req.Role),
		})
		return
	}

	s.logger.Printf("[MINT] token request: role=%s repos=%v", req.Role, req.Repos)

	jwt, err := mintcore.GenerateAppJWT(appID, pemData)
	if err != nil {
		s.logger.Printf("[MINT] JWT generation failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to generate app JWT"})
		return
	}

	repo := req.Repos[0]
	installationID, err := mintcore.FindInstallation(r.Context(), s.httpClient, s.githubBaseURL, jwt, org, repo)
	if err != nil {
		s.logger.Printf("[MINT] find installation failed: %v", err)
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: fmt.Sprintf("failed to find installation: %v", err)})
		return
	}

	token, expiresAt, _, err := mintcore.CreateInstallationToken(r.Context(), s.httpClient, s.githubBaseURL, jwt, installationID, req.Role, req.Repos)
	if err != nil {
		s.logger.Printf("[MINT] create installation token failed: %v", err)
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: fmt.Sprintf("failed to create installation token: %v", err)})
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		Token:     token,
		ExpiresAt: expiresAt,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	if !s.insecureNoAuth {
		if s.oidcVerifier == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "OIDC verifier not initialized — call Start() first"})
			return
		}
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "missing or invalid Authorization header"})
			return
		}
		oidcToken := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := s.oidcVerifier.Verify(r.Context(), oidcToken)
		if err != nil {
			s.logger.Printf("[MINT] OIDC verification failed for /v1/status: %v", err)
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "authentication failed"})
			return
		}
		if claims == nil {
			// Fail-closed: treat (nil, nil) as an auth failure rather than skip checks.
			s.logger.Printf("[MINT] OIDC verifier returned nil claims without error on /v1/status")
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "authentication failed"})
			return
		}
		// Defense-in-depth: match handleToken's explicit org check.
		s.mu.RLock()
		org := s.org
		s.mu.RUnlock()
		if claims.RepositoryOwner != org {
			s.logger.Printf("[MINT] OIDC org mismatch on /v1/status: got %q, want %q", claims.RepositoryOwner, org)
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "authentication failed"})
			return
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	resp := statusResponse{Org: s.org}
	for role, appID := range s.appIDs {
		resp.Roles = append(resp.Roles, roleStatus{Role: role, AppID: appID})
	}
	sort.Slice(resp.Roles, func(i, j int) bool {
		return resp.Roles[i].Role < resp.Roles[j].Role
	})

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) Start(ctx context.Context) error {
	if err := s.loadFromDisk(); err != nil {
		return fmt.Errorf("loading persisted state: %w", err)
	}

	if s.org != "" {
		s.logger.Printf("Loaded %d role(s) for org %s from %s", len(s.pems), s.org, s.dataDir)
	}

	if !s.insecureNoAuth {
		if s.org == "" {
			return fmt.Errorf("OIDC verification requires a populated data directory (no org in config.json); use --insecure-no-auth for first-time setup")
		}
		// Resolve nil allowedWorkflows to wildcard at verifier construction time.
		// nil means "caller did not specify" — the loopback-only default is
		// documented as an accepted trade-off for development use.
		verifierWorkflows := s.allowedWorkflows
		if len(verifierWorkflows) == 0 {
			verifierWorkflows = []string{"*"}
		}
		s.oidcVerifier = mintcore.NewJWKSVerifier(mintcore.JWKSVerifierConfig{
			IssuerURL:            "https://token.actions.githubusercontent.com",
			Audience:             s.oidcAudience,
			AllowedOrgs:          []string{s.org},
			AllowedWorkflowFiles: verifierWorkflows,
			PerRepoWIFRepos:      s.perRepoWIFRepos,
		})
	}

	srv := &http.Server{
		Addr:         s.addr,
		Handler:      s,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	s.logger.Printf("WARNING: Dev mint. Do not use in production.")
	s.logger.Printf("Mint listening on http://%s", s.addr)
	if s.insecureNoAuth {
		s.logger.Printf("WARNING: OIDC verification disabled (--insecure-no-auth)")
	} else if len(s.allowedWorkflows) == 0 || (len(s.allowedWorkflows) == 1 && s.allowedWorkflows[0] == "*") {
		s.logger.Printf("WARNING: All workflows are allowed to mint tokens (default wildcard). Use --allowed-workflows for tighter controls.")
	}
	s.logger.Printf("PEMs are loaded from %s/pems/<role>.pem at startup; restart to pick up changes", s.dataDir)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("mint server: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
