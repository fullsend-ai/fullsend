package mintcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const maxRepos = 500

// mintRequest is the JSON body sent by .fullsend agent workflows.
type mintRequest struct {
	Role  string   `json:"role"`
	Repos []string `json:"repos,omitempty"`
}

// mintResponse is returned on success.
type mintResponse struct {
	Token         string            `json:"token"`
	ExpiresAt     string            `json:"expires_at"`
	GrantedRepos  []string          `json:"granted_repos,omitempty"`
	GrantedPerms  map[string]string `json:"granted_permissions,omitempty"`
	RepoSelection string            `json:"repository_selection,omitempty"`
}

// statusResponse is returned by the /v1/status diagnostic endpoint.
type statusResponse struct {
	Org   string   `json:"org"`
	Roles []string `json:"roles"`
}

// Handler holds dependencies for the token mint HTTP server.
type Handler struct {
	httpClient   HTTPDoer
	pemAccessor  PEMAccessor
	oidcVerifier OIDCVerifier

	githubBaseURL string

	roleAppIDs       map[string]string
	allowedRoles     []string
	legacyAppIDsOnly bool // ROLE_APP_IDS has org/role keys but no role-only keys
}

// NewHandler creates a Handler with the given dependencies.
// Environment variables for handler-level config (ROLE_APP_IDS, ALLOWED_ROLES)
// are read once at construction time. The OIDCVerifier is injected by the caller
// so different verification strategies can be used (STSVerifier for the Cloud
// Function, JWKSVerifier for devmint). Org validation is the OIDCVerifier's
// responsibility.
func NewHandler(pemAccessor PEMAccessor, oidcVerifier OIDCVerifier) (*Handler, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}

	h := &Handler{
		httpClient:    httpClient,
		pemAccessor:   pemAccessor,
		oidcVerifier:  oidcVerifier,
		githubBaseURL: "https://api.github.com",
	}

	if raw := os.Getenv("ROLE_APP_IDS"); raw != "" {
		var ids map[string]string
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return nil, fmt.Errorf("failed to parse ROLE_APP_IDS: %w", err)
		}
		h.roleAppIDs = RoleOnlyAppIDs(ids)
		h.legacyAppIDsOnly = legacyAppIDsOnly(ids)
	}

	roleSet := make(map[string]bool, len(h.roleAppIDs))
	for role := range h.roleAppIDs {
		roleSet[role] = true
	}

	if raw := os.Getenv("ALLOWED_ROLES"); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				if !RolePattern.MatchString(trimmed) {
					return nil, fmt.Errorf("ALLOWED_ROLES contains invalid entry %q: must match %s", trimmed, RolePattern.String())
				}
				h.allowedRoles = append(h.allowedRoles, trimmed)
			}
		}
	} else {
		for role := range roleSet {
			h.allowedRoles = append(h.allowedRoles, role)
		}
		sort.Strings(h.allowedRoles)
	}

	for _, role := range h.allowedRoles {
		if !HasRole(role) {
			return nil, fmt.Errorf("ALLOWED_ROLES contains %q but RolePermissions has no entry for it", role)
		}
		if !roleSet[role] {
			return nil, fmt.Errorf("ALLOWED_ROLES contains %q but ROLE_APP_IDS has no entry for it", role)
		}
	}

	return h, nil
}

// ServeHTTP handles incoming token mint requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/health" {
		h.handleHealth(w)
		return
	}

	if r.URL.Path != "/v1/token" && r.URL.Path != "/" && r.URL.Path != "/v1/status" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	if r.URL.Path == "/v1/status" && r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.URL.Path != "/v1/status" && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
		return
	}
	oidcToken := strings.TrimPrefix(authHeader, "Bearer ")

	if r.URL.Path == "/v1/status" {
		claims, err := h.oidcVerifier.Verify(r.Context(), oidcToken)
		if err != nil {
			log.Printf("OIDC verification failed for /v1/status: %v", err)
			writeError(w, http.StatusUnauthorized, "authentication failed")
			return
		}
		h.handleStatus(w, claims)
		return
	}

	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req mintRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Role == "" {
		writeError(w, http.StatusBadRequest, "role is required")
		return
	}

	if !RolePattern.MatchString(req.Role) {
		writeError(w, http.StatusBadRequest, "invalid role format")
		return
	}

	if !h.checkAllowedRole(req.Role) {
		writeError(w, http.StatusForbidden, "role not allowed")
		return
	}

	if len(req.Repos) == 0 {
		writeError(w, http.StatusBadRequest, "repos is required (at least one repo must be specified)")
		return
	}

	if len(req.Repos) > maxRepos {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("too many repos (max %d)", maxRepos))
		return
	}
	for _, repo := range req.Repos {
		if !RepoNamePattern.MatchString(repo) || strings.Contains(repo, "..") {
			writeError(w, http.StatusBadRequest, "invalid repo name")
			return
		}
	}

	ctx := r.Context()

	claims, err := h.oidcVerifier.Verify(ctx, oidcToken)
	if err != nil {
		log.Printf("OIDC verification failed: %v", err)
		writeError(w, http.StatusUnauthorized, "authentication failed")
		return
	}

	org := strings.ToLower(claims.RepositoryOwner)

	token, expiresAt, granted, err := h.mintToken(ctx, org, req.Role, req.Repos)
	if err != nil {
		log.Printf("failed to mint token: org=%s role=%s err=%v", org, req.Role, err)
		var me *mintError
		if errors.As(err, &me) {
			writeError(w, me.status, "mint failed")
		} else {
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	if granted != nil {
		log.Printf("minted: org=%s role=%s app_id=%s installation_id=%d requested_repos=%v source_repo=%s workflow_ref=%s",
			org, req.Role, granted.AppID, granted.InstallationID, req.Repos, claims.Repository, claims.JobWorkflowRef)
		log.Printf("granted scope: repos=%v permissions=%v repo_selection=%s",
			granted.Repos, granted.Permissions, granted.RepoSelection)
		if granted.RepoSelection == "all" {
			log.Printf("WARNING: token granted with repository_selection=all (requested specific repos: %v)", req.Repos)
		}
		requested := RolePermissionsFor(req.Role)
		for perm, level := range granted.Permissions {
			if reqLevel, ok := requested[perm]; !ok {
				log.Printf("WARNING: extra permission granted: %s=%s (not requested)", perm, level)
			} else if level != reqLevel {
				log.Printf("WARNING: permission level mismatch: %s requested=%s granted=%s", perm, reqLevel, level)
			}
		}
		for perm, reqLevel := range requested {
			if _, ok := granted.Permissions[perm]; !ok {
				log.Printf("WARNING: requested permission not granted: %s=%s", perm, reqLevel)
			}
		}
	}

	resp := mintResponse{
		Token:     token,
		ExpiresAt: expiresAt,
	}
	if granted != nil {
		resp.GrantedRepos = granted.Repos
		resp.GrantedPerms = granted.Permissions
		resp.RepoSelection = granted.RepoSelection
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleHealth(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if h.legacyAppIDsOnly {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "unhealthy",
			"reason": "ROLE_APP_IDS contains legacy org/role keys but no role-only keys; migration required",
		})
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, `{"status":"ok"}`)
}

func (h *Handler) handleStatus(w http.ResponseWriter, claims *Claims) {
	org := strings.ToLower(claims.RepositoryOwner)
	roles := append([]string(nil), h.allowedRoles...)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(statusResponse{
		Org:   org,
		Roles: roles,
	}); err != nil {
		log.Printf("encoding status response: %v", err)
	}
}

func (h *Handler) mintToken(ctx context.Context, org, role string, repos []string) (string, string, *GrantedScope, error) {
	appID, err := h.lookupRoleAppID(role)
	if err != nil {
		return "", "", nil, &mintError{status: http.StatusForbidden, msg: fmt.Sprintf("looking up app ID for role %s: %v", role, err)}
	}

	pemData, err := h.pemAccessor.AccessPEM(ctx, role)
	if err != nil {
		return "", "", nil, &mintError{status: http.StatusForbidden, msg: fmt.Sprintf("reading PEM secret for role %s: %v", role, err)}
	}
	defer func() {
		for i := range pemData {
			pemData[i] = 0
		}
	}()

	jwt, err := GenerateAppJWT(appID, pemData)
	if err != nil {
		return "", "", nil, &mintError{status: http.StatusInternalServerError, msg: fmt.Sprintf("generating app JWT: %v", err)}
	}

	installationID, err := FindInstallation(ctx, h.httpClient, h.githubBaseURL, jwt, org, repos[0])
	if err != nil {
		return "", "", nil, &mintError{status: http.StatusBadGateway, msg: err.Error()}
	}

	token, expiresAt, granted, err := CreateInstallationToken(ctx, h.httpClient, h.githubBaseURL, jwt, installationID, role, repos)
	if err != nil {
		return "", "", nil, &mintError{status: http.StatusBadGateway, msg: err.Error()}
	}

	if granted != nil {
		granted.AppID = appID
		granted.InstallationID = installationID
	}

	return token, expiresAt, granted, nil
}

func (h *Handler) checkAllowedRole(role string) bool {
	for _, entry := range h.allowedRoles {
		if entry == role {
			return true
		}
	}
	return false
}

// legacyAppIDsOnly reports whether ids contains org/role keys but no role-only
// keys. An empty map or unset ROLE_APP_IDS is not a migration failure.
func legacyAppIDsOnly(ids map[string]string) bool {
	if len(ids) == 0 || len(RoleOnlyAppIDs(ids)) > 0 {
		return false
	}
	for key := range ids {
		if strings.Contains(key, "/") {
			return true
		}
	}
	return false
}

// RoleOnlyAppIDs extracts role-keyed entries from ROLE_APP_IDS, ignoring
// legacy org/role keys left over during migration.
func RoleOnlyAppIDs(ids map[string]string) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]string, len(ids))
	for key, appID := range ids {
		if strings.Contains(key, "/") {
			continue
		}
		out[key] = appID
	}
	return out
}

func (h *Handler) lookupRoleAppID(role string) (string, error) {
	if h.roleAppIDs == nil {
		return "", fmt.Errorf("ROLE_APP_IDS not set or invalid")
	}

	lookupRole := PemSecretRole(role)
	appID, ok := h.roleAppIDs[lookupRole]
	if !ok {
		for key, id := range h.roleAppIDs {
			if strings.EqualFold(key, lookupRole) {
				appID = id
				ok = true
				break
			}
		}
	}
	if !ok {
		return "", fmt.Errorf("no app ID configured for role %q", role)
	}
	if appID == "" {
		return "", fmt.Errorf("no app ID configured for role %q", role)
	}
	return appID, nil
}

// mintError is an HTTP-aware error carrying a status code for the response.
type mintError struct {
	status int
	msg    string
}

func (e *mintError) Error() string { return e.msg }

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
