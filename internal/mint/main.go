// Package function implements a Cloud Function token mint that issues
// GitHub App installation tokens to OIDC-authenticated .fullsend workflows.
//
// Callers present a GitHub OIDC JWT. The mint validates it via GCP STS
// (Workload Identity Federation), looks up the requested role's PEM from
// Secret Manager, and returns a scoped installation token.
package function

import (
	"context"
	"encoding/base64"
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

	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

var requiredEnvVars = []string{
	"ALLOWED_ORGS",
	"GCP_PROJECT_NUMBER",
	"WIF_POOL_NAME",
	"WIF_PROVIDER_NAME",
	"ROLE_APP_IDS",
	"OIDC_AUDIENCE",
}

func init() {
	if strings.HasSuffix(os.Args[0], ".test") || strings.HasSuffix(os.Args[0], ".test.exe") {
		return
	}

	var missing []string
	for _, v := range requiredEnvVars {
		if os.Getenv(v) == "" {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		log.Fatalf("required environment variables not set: %s", strings.Join(missing, ", "))
	}

	handler := NewHandler(
		&smPEMAccessor{gcpProjectNum: os.Getenv("GCP_PROJECT_NUMBER")},
	)
	functions.HTTP("ServeHTTP", handler.ServeHTTP)
}

var internalClient = &http.Client{Timeout: 10 * time.Second}

// smPEMAccessor reads agent PEMs from GCP Secret Manager via REST API,
// authenticating with the metadata server token (available in Cloud Functions).
// Secret naming convention: projects/{num}/secrets/fullsend-{org}--{role}-app-pem/versions/latest
type smPEMAccessor struct {
	gcpProjectNum string
}

func (s *smPEMAccessor) AccessPEM(ctx context.Context, org, role string) ([]byte, error) {
	if !mintcore.GitHubOrgPattern.MatchString(org) || strings.Contains(org, "--") {
		return nil, fmt.Errorf("invalid org name %q", org)
	}
	if !mintcore.RolePattern.MatchString(role) || strings.Contains(role, "--") {
		return nil, fmt.Errorf("invalid role name %q", role)
	}
	name := fmt.Sprintf("projects/%s/secrets/fullsend-%s--%s-app-pem/versions/latest",
		s.gcpProjectNum, org, role)
	token, err := metadataToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting metadata token: %w", err)
	}

	url := fmt.Sprintf("https://secretmanager.googleapis.com/v1/%s:access", name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating secret request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := internalClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("accessing secret: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("secret access returned status %d", resp.StatusCode)
	}

	var result struct {
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding secret response: %w", err)
	}

	data, err := base64.StdEncoding.DecodeString(result.Payload.Data)
	if err != nil {
		return nil, fmt.Errorf("decoding secret data: %w", err)
	}
	return data, nil
}

func metadataToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := internalClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata token request returned %d", resp.StatusCode)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("metadata returned empty access token")
	}
	return tok.AccessToken, nil
}

const maxRepos = 500

// mintRequest is the JSON body sent by .fullsend agent workflows.
type mintRequest struct {
	Role  string   `json:"role"`
	Repos []string `json:"repos,omitempty"`
}

// mintResponse is returned on success.
type mintResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// Handler holds dependencies for the Cloud Function.
type Handler struct {
	httpClient   mintcore.HTTPDoer
	pemAccessor  mintcore.PEMAccessor
	oidcVerifier mintcore.OIDCVerifier

	githubBaseURL string

	roleAppIDs   map[string]string
	allowedOrgs  []string
	allowedRoles []string
	oidcAudience string
}

// NewHandler creates a Handler with production defaults.
// All environment variables are read once at construction time.
func NewHandler(pemAccessor mintcore.PEMAccessor) *Handler {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	oidcAudience := os.Getenv("OIDC_AUDIENCE")

	// Parse allowed orgs early so we can pass them to the STS verifier.
	var allowedOrgs []string
	for _, entry := range strings.Split(os.Getenv("ALLOWED_ORGS"), ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			allowedOrgs = append(allowedOrgs, trimmed)
		}
	}

	var allowedWorkflows []string
	if wf := os.Getenv("ALLOWED_WORKFLOW_FILES"); wf != "" {
		for _, entry := range strings.Split(wf, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				allowedWorkflows = append(allowedWorkflows, trimmed)
			}
		}
	}

	perRepoWIFRepos := make(map[string]bool)
	if raw := os.Getenv("PER_REPO_WIF_REPOS"); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				perRepoWIFRepos[strings.ToLower(trimmed)] = true
			}
		}
	}

	h := &Handler{
		httpClient:  httpClient,
		pemAccessor: pemAccessor,
		oidcVerifier: mintcore.NewSTSVerifier(mintcore.STSVerifierOptions{
			HTTPClient:         httpClient,
			GCPProjectNum:      os.Getenv("GCP_PROJECT_NUMBER"),
			WIFPoolName:        os.Getenv("WIF_POOL_NAME"),
			DefaultWIFProvider: os.Getenv("WIF_PROVIDER_NAME"),
			AllowedOrgs:        allowedOrgs,
			AllowedWorkflows:   allowedWorkflows,
			PerRepoWIFRepos:    perRepoWIFRepos,
			OIDCAudience:       oidcAudience,
		}),
		githubBaseURL: "https://api.github.com",
		allowedOrgs:   allowedOrgs,
		oidcAudience:  oidcAudience,
	}

	if raw := os.Getenv("ROLE_APP_IDS"); raw != "" {
		var ids map[string]string
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			log.Fatalf("failed to parse ROLE_APP_IDS: %v", err)
		}
		h.roleAppIDs = ids
	}

	roleSet := make(map[string]bool)
	for key := range h.roleAppIDs {
		if idx := strings.Index(key, "/"); idx >= 0 {
			roleSet[key[idx+1:]] = true
		}
	}

	if raw := os.Getenv("ALLOWED_ROLES"); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				if !mintcore.RolePattern.MatchString(trimmed) {
					log.Fatalf("ALLOWED_ROLES contains invalid entry %q: must match %s", trimmed, mintcore.RolePattern.String())
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
		if _, ok := mintcore.RolePermissions[role]; !ok {
			log.Fatalf("ALLOWED_ROLES contains %q but RolePermissions has no entry for it", role)
		}
		if !roleSet[role] {
			log.Fatalf("ALLOWED_ROLES contains %q but ROLE_APP_IDS has no org-scoped entry for it", role)
		}
	}

	return h
}

// ServeHTTP handles incoming token mint requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/health" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok"}`)
		return
	}

	if r.URL.Path != "/v1/token" && r.URL.Path != "/" && r.URL.Path != "/v1/status" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
		return
	}
	oidcToken := strings.TrimPrefix(authHeader, "Bearer ")

	if r.Method == http.MethodGet && r.URL.Path == "/v1/status" {
		if _, err := h.oidcVerifier.Verify(r.Context(), oidcToken); err != nil {
			writeError(w, http.StatusUnauthorized, fmt.Sprintf("OIDC verification failed: %v", err))
			return
		}
		h.handleStatus(w)
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
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

	if !mintcore.RolePattern.MatchString(req.Role) {
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
		if !mintcore.RepoNamePattern.MatchString(repo) || strings.Contains(repo, "..") {
			writeError(w, http.StatusBadRequest, "invalid repo name")
			return
		}
	}

	ctx := r.Context()

	claims, err := h.oidcVerifier.Verify(ctx, oidcToken)
	if err != nil {
		log.Printf("OIDC verification failed: %v", err)
		writeError(w, http.StatusForbidden, "authentication failed")
		return
	}

	org := strings.ToLower(claims.RepositoryOwner)

	token, expiresAt, err := h.mintToken(ctx, org, req.Role, req.Repos)
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

	log.Printf("minted: org=%s role=%s repo_count=%d", org, req.Role, len(req.Repos))

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(mintResponse{
		Token:     token,
		ExpiresAt: expiresAt,
	})
}

type statusResponse struct {
	Orgs  []string     `json:"orgs"`
	Roles []statusRole `json:"roles"`
}

type statusRole struct {
	Key   string `json:"key"`
	AppID string `json:"app_id"`
}

func (h *Handler) handleStatus(w http.ResponseWriter) {
	var roles []statusRole
	for key, appID := range h.roleAppIDs {
		roles = append(roles, statusRole{Key: key, AppID: appID})
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].Key < roles[j].Key })

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(statusResponse{
		Orgs:  h.allowedOrgs,
		Roles: roles,
	})
}

func (h *Handler) mintToken(ctx context.Context, org, role string, repos []string) (string, string, error) {
	appID, err := h.lookupRoleAppID(org, role)
	if err != nil {
		return "", "", &mintError{status: http.StatusForbidden, msg: fmt.Sprintf("looking up app ID for role %s: %v", role, err)}
	}

	pemData, err := h.pemAccessor.AccessPEM(ctx, org, role)
	if err != nil {
		return "", "", &mintError{status: http.StatusForbidden, msg: fmt.Sprintf("reading PEM secret for role %s: %v", role, err)}
	}
	defer func() {
		for i := range pemData {
			pemData[i] = 0
		}
	}()

	jwt, err := mintcore.GenerateAppJWT(appID, pemData)
	if err != nil {
		return "", "", &mintError{status: http.StatusInternalServerError, msg: fmt.Sprintf("generating app JWT: %v", err)}
	}

	installationID, err := mintcore.FindInstallation(ctx, h.httpClient, h.githubBaseURL, jwt, org, repos[0])
	if err != nil {
		return "", "", &mintError{status: http.StatusBadGateway, msg: err.Error()}
	}

	token, expiresAt, err := mintcore.CreateInstallationToken(ctx, h.httpClient, h.githubBaseURL, jwt, installationID, role, repos)
	if err != nil {
		return "", "", &mintError{status: http.StatusBadGateway, msg: err.Error()}
	}

	return token, expiresAt, nil
}

func (h *Handler) checkAllowedRole(role string) bool {
	for _, entry := range h.allowedRoles {
		if entry == role {
			return true
		}
	}
	return false
}

func (h *Handler) lookupRoleAppID(org, role string) (string, error) {
	if h.roleAppIDs == nil {
		return "", fmt.Errorf("ROLE_APP_IDS not set or invalid")
	}

	key := org + "/" + role
	appID, ok := h.roleAppIDs[key]
	if !ok || appID == "" {
		return "", fmt.Errorf("no app ID configured for role %q (org %q)", role, org)
	}

	return appID, nil
}

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
