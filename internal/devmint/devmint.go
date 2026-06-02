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

type diskConfig struct {
	Org   string                    `json:"org"`
	Roles map[string]diskRoleConfig `json:"roles"`
}

type diskRoleConfig struct {
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
		allowedWorkflows: opts.AllowedWorkflows,
		pems:             make(map[string][]byte),
		appIDs:           make(map[string]string),
	}
	if opts.PerRepoWIFRepos != nil {
		s.perRepoWIFRepos = opts.PerRepoWIFRepos
	} else {
		s.perRepoWIFRepos = make(map[string]bool)
	}
	if len(s.allowedWorkflows) == 0 {
		s.allowedWorkflows = []string{"*"}
	}
	if !opts.InsecureNoAuth {
		s.oidcVerifier = mintcore.NewJWKSVerifier(
			"https://token.actions.githubusercontent.com",
			opts.OIDCAudience,
			nil,
		)
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

	var cfg diskConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	newPems := make(map[string][]byte, len(cfg.Roles))
	newAppIDs := make(map[string]string, len(cfg.Roles))

	for role, rc := range cfg.Roles {
		newAppIDs[role] = rc.AppID
		pemPath := filepath.Join(s.dataDir, "pems", role+".pem")
		pemData, err := os.ReadFile(pemPath)
		if err != nil {
			return fmt.Errorf("reading PEM for role %s: %w", role, err)
		}
		newPems[role] = pemData
	}

	s.org = cfg.Org
	s.pems = newPems
	s.appIDs = newAppIDs

	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Fullsend-Dev-Mint", "true")

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

		s.mu.RLock()
		org := s.org
		s.mu.RUnlock()

		if err := mintcore.ValidateOrgAllowed(claims.RepositoryOwner, []string{org}); err != nil {
			s.logger.Printf("[MINT] org validation failed: %v", err)
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "authentication failed"})
			return
		}

		if err := mintcore.ValidateWorkflowRef(claims.JobWorkflowRef, claims.Repository, []string{org}, s.perRepoWIFRepos, s.allowedWorkflows); err != nil {
			s.logger.Printf("[MINT] workflow ref validation failed: %v", err)
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
	if !mintcore.RolePattern.MatchString(req.Role) {
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

	token, expiresAt, err := mintcore.CreateInstallationToken(r.Context(), s.httpClient, s.githubBaseURL, jwt, installationID, req.Role, req.Repos)
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

	s.mu.RLock()
	defer s.mu.RUnlock()

	resp := statusResponse{Org: s.org}
	for role, appID := range s.appIDs {
		resp.Roles = append(resp.Roles, roleStatus{Role: role, AppID: appID})
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) Start(ctx context.Context) error {
	if err := s.loadFromDisk(); err != nil {
		return fmt.Errorf("loading persisted state: %w", err)
	}

	if s.org != "" {
		s.logger.Printf("Loaded %d role(s) for org %s from %s", len(s.pems), s.org, s.dataDir)
	}

	srv := &http.Server{
		Addr:    s.addr,
		Handler: s,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	s.logger.Printf("WARNING: Standalone token mint. Do not use in production.")
	s.logger.Printf("Mint listening on http://%s", s.addr)
	if s.insecureNoAuth {
		s.logger.Printf("WARNING: OIDC verification disabled (--insecure-no-auth)")
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
