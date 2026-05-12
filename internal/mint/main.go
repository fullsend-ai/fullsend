// Package function implements a Cloud Function token mint that issues
// GitHub App installation tokens to OIDC-authenticated .fullsend workflows.
//
// Callers present a GitHub OIDC JWT. The mint validates it via GCP STS
// (Workload Identity Federation), looks up the requested role's PEM from
// Secret Manager, and returns a scoped installation token.
package function

import (
	"bytes"
	"context"
	"crypto"
	"errors"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
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

	httpClient := &http.Client{Timeout: 30 * time.Second}
	handler := NewHandler(
		&smPEMAccessor{gcpProjectNum: os.Getenv("GCP_PROJECT_NUMBER")},
		&stsTokenValidator{
			httpClient:      httpClient,
			stsBaseURL:      "https://sts.googleapis.com",
			gcpProjectNum:   os.Getenv("GCP_PROJECT_NUMBER"),
			wifPoolName:     os.Getenv("WIF_POOL_NAME"),
			wifProviderName: os.Getenv("WIF_PROVIDER_NAME"),
		},
	)
	functions.HTTP("ServeHTTP", handler.ServeHTTP)
}

var internalClient = &http.Client{Timeout: 10 * time.Second}

// stsTokenValidator validates OIDC tokens by exchanging them with GCP STS
// (Workload Identity Federation). The STS exchange verifies the token was
// minted by a repo in the configured org via CEL attribute conditions.
type stsTokenValidator struct {
	httpClient      HTTPDoer
	stsBaseURL      string
	gcpProjectNum   string
	wifPoolName     string
	wifProviderName string
}

func (v *stsTokenValidator) Validate(ctx context.Context, oidcToken string) error {
	aud := fmt.Sprintf("//iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		v.gcpProjectNum, v.wifPoolName, v.wifProviderName)

	formValues := url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"audience":             {aud},
		"scope":                {"https://www.googleapis.com/auth/cloud-platform"},
		"requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"subject_token_type":   {"urn:ietf:params:oauth:token-type:jwt"},
		"subject_token":        {oidcToken},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		v.stsBaseURL+"/v1/token",
		strings.NewReader(formValues.Encode()))
	if err != nil {
		return fmt.Errorf("creating STS request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("STS request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Log only the status code — the response body may contain GCP project
		// details, WIF pool names, or CEL condition text useful for reconnaissance.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("STS returned status %d", resp.StatusCode)
	}

	var stsResp stsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stsResp); err != nil {
		return fmt.Errorf("decoding STS response: %w", err)
	}

	if stsResp.AccessToken == "" {
		return fmt.Errorf("STS returned empty access token")
	}

	return nil
}

// smPEMAccessor reads agent PEMs from GCP Secret Manager via REST API,
// authenticating with the metadata server token (available in Cloud Functions).
// Secret naming convention: projects/{num}/secrets/fullsend-{org}--{role}-app-pem/versions/latest
type smPEMAccessor struct {
	gcpProjectNum string
}

func (s *smPEMAccessor) AccessPEM(ctx context.Context, org, role string) ([]byte, error) {
	if !githubOrgPattern.MatchString(org) || strings.Contains(org, "--") {
		return nil, fmt.Errorf("invalid org name %q", org)
	}
	if !rolePattern.MatchString(role) || strings.Contains(role, "--") {
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
		// Log only the status code — the response body may expose full secret
		// resource names containing project number, org name, and role.
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


// rolePattern restricts role to safe lowercase identifiers.
var rolePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// githubOrgPattern validates GitHub org/user names: alphanumeric or single
// hyphens, cannot start or end with a hyphen, max 39 characters.
var githubOrgPattern = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)

// repoNamePattern validates individual repo names (no org prefix).
// GitHub allows repos starting with dot (e.g., .fullsend, .github).
var repoNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.][a-zA-Z0-9._-]{0,99}$`)

const maxRepos = 100

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

// stsResponse is the relevant fields from the STS token exchange response.
type stsResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// audience handles the OIDC aud claim which can be a string or array of strings.
type audience []string

func (a *audience) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*a = []string{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("aud must be a string or array of strings")
	}
	if len(arr) == 0 {
		return fmt.Errorf("aud must not be empty")
	}
	*a = arr
	return nil
}

func (a audience) contains(aud string) bool {
	for _, v := range a {
		if v == aud {
			return true
		}
	}
	return false
}

// oidcClaims holds the subset of OIDC JWT claims we validate.
type oidcClaims struct {
	Issuer          string   `json:"iss"`
	Audience        audience `json:"aud"`
	IssuedAt        int64    `json:"iat"`
	Expiry          int64    `json:"exp"`
	RepositoryOwner string   `json:"repository_owner"`
	JobWorkflowRef  string   `json:"job_workflow_ref"`
}

// installationResponse is the response from GET /orgs/{org}/installation.
type installationResponse struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
}

// installationTokenResponse is the response from POST /app/installations/{id}/access_tokens.
type installationTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// PEMAccessor retrieves agent PEM keys by org and role.
// Implementations encapsulate the storage backend (GCP Secret Manager, etc.).
type PEMAccessor interface {
	AccessPEM(ctx context.Context, org, role string) ([]byte, error)
}

// TokenValidator validates an OIDC token and returns an error if invalid.
// Implementations encapsulate the validation backend (GCP STS, etc.).
type TokenValidator interface {
	Validate(ctx context.Context, oidcToken string) error
}

// HTTPDoer abstracts http.Client for testability.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Handler holds dependencies for the Cloud Function.
type Handler struct {
	httpClient     HTTPDoer
	pemAccessor    PEMAccessor
	tokenValidator TokenValidator

	githubBaseURL string

	roleAppIDs       map[string]string
	allowedOrgs      []string
	allowedRoles     []string
	allowedWorkflows []string
	oidcAudience     string
}

// NewHandler creates a Handler with production defaults.
// All environment variables are read once at construction time.
func NewHandler(pemAccessor PEMAccessor, tokenValidator TokenValidator) *Handler {
	h := &Handler{
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		pemAccessor:    pemAccessor,
		tokenValidator: tokenValidator,
		githubBaseURL:  "https://api.github.com",
		oidcAudience:   os.Getenv("OIDC_AUDIENCE"),
	}

	if raw := os.Getenv("ROLE_APP_IDS"); raw != "" {
		var ids map[string]string
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			log.Fatalf("failed to parse ROLE_APP_IDS: %v", err)
		}
		h.roleAppIDs = ids
	}

	for _, entry := range strings.Split(os.Getenv("ALLOWED_ORGS"), ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			h.allowedOrgs = append(h.allowedOrgs, trimmed)
		}
	}

	// Derive allowed roles from ROLE_APP_IDS keys (format: "org/role").
	// If ALLOWED_ROLES env var is set, use it as an explicit allowlist
	// (still validated against ROLE_APP_IDS).
	roleSet := make(map[string]bool)
	for key := range h.roleAppIDs {
		if idx := strings.Index(key, "/"); idx >= 0 {
			roleSet[key[idx+1:]] = true
		}
	}

	if raw := os.Getenv("ALLOWED_ROLES"); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				if !rolePattern.MatchString(trimmed) {
					log.Fatalf("ALLOWED_ROLES contains invalid entry %q: must match %s", trimmed, rolePattern.String())
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
		if _, ok := rolePermissions[role]; !ok {
			log.Fatalf("ALLOWED_ROLES contains %q but rolePermissions has no entry for it", role)
		}
		if !roleSet[role] {
			log.Fatalf("ALLOWED_ROLES contains %q but ROLE_APP_IDS has no org-scoped entry for it", role)
		}
	}

	if wf := os.Getenv("ALLOWED_WORKFLOW_FILES"); wf != "" {
		for _, entry := range strings.Split(wf, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				h.allowedWorkflows = append(h.allowedWorkflows, trimmed)
			}
		}
	}

	return h
}

// ServeHTTP handles incoming token mint requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && (r.URL.Path == "/health") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok"}`)
		return
	}

	if r.URL.Path != "/v1/token" && r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
		return
	}
	oidcToken := strings.TrimPrefix(authHeader, "Bearer ")

	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10)) // 64KB limit
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

	if !rolePattern.MatchString(req.Role) {
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
		writeError(w, http.StatusBadRequest, "too many repos (max 100)")
		return
	}
	for _, repo := range req.Repos {
		if !repoNamePattern.MatchString(repo) || strings.Contains(repo, "..") {
			writeError(w, http.StatusBadRequest, "invalid repo name")
			return
		}
	}

	ctx := r.Context()

	claims, err := h.prevalidateOIDCToken(oidcToken)
	if err != nil {
		log.Printf("OIDC pre-validation failed: %v", err)
		writeError(w, http.StatusForbidden, "authentication failed")
		return
	}

	if err := h.tokenValidator.Validate(ctx, oidcToken); err != nil {
		log.Printf("OIDC validation failed: %v", err)
		writeError(w, http.StatusForbidden, "authentication failed")
		return
	}

	// Derive org from the validated JWT claim, normalized to lowercase to match
	// WIF CEL conditions.
	// Safe because prevalidateOIDCToken checked ALLOWED_ORGS, WIF STS validated
	// the token via CEL, and the JWT is cryptographically signed by GitHub.
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

// prevalidateOIDCToken performs lightweight validation of the OIDC JWT before
// sending it to STS. This is defense-in-depth — STS performs the authoritative
// validation.
func (h *Handler) prevalidateOIDCToken(token string) (*oidcClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format: expected 3 segments, got %d", len(parts))
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT claims: %w", err)
	}

	var claims oidcClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("parsing JWT claims: %w", err)
	}

	if claims.Issuer != "https://token.actions.githubusercontent.com" {
		return nil, fmt.Errorf("unexpected issuer: %s", claims.Issuer)
	}

	if h.oidcAudience == "" {
		return nil, fmt.Errorf("OIDC_AUDIENCE must be configured")
	}
	if !claims.Audience.contains(h.oidcAudience) {
		return nil, fmt.Errorf("audience mismatch")
	}

	// Allow 30s clock skew between GitHub's token issuer and this host.
	const clockSkewSeconds = 30
	now := time.Now().Unix()
	if claims.Expiry <= now-clockSkewSeconds {
		return nil, fmt.Errorf("token expired")
	}
	if claims.IssuedAt == 0 {
		return nil, fmt.Errorf("missing iat claim")
	}
	if claims.IssuedAt > now+clockSkewSeconds {
		return nil, fmt.Errorf("token issued in the future")
	}

	if !h.checkAllowedOrg(claims.RepositoryOwner) {
		return nil, fmt.Errorf("repository_owner not in allowed orgs")
	}

	// Validate job_workflow_ref — only .fullsend workflows can request tokens.
	// Use claims.RepositoryOwner (just validated against ALLOWED_ORGS) for the
	// prefix check to ensure the workflow belongs to the caller's own .fullsend repo.
	if claims.JobWorkflowRef == "" {
		return nil, fmt.Errorf("missing job_workflow_ref claim")
	}
	ref := strings.ToLower(claims.JobWorkflowRef)
	expectedPrefix := strings.ToLower(claims.RepositoryOwner) + "/.fullsend/"
	if !strings.HasPrefix(ref, expectedPrefix) {
		return nil, fmt.Errorf("job_workflow_ref does not reference .fullsend repo")
	}

	// Extract the workflow file path relative to .fullsend/ (before @ref).
	relPath := strings.TrimPrefix(ref, expectedPrefix)
	if atIdx := strings.Index(relPath, "@"); atIdx > 0 {
		relPath = relPath[:atIdx]
	}

	if !strings.HasPrefix(relPath, ".github/workflows/") {
		return nil, fmt.Errorf("job_workflow_ref does not reference a workflow file")
	}

	// If ALLOWED_WORKFLOW_FILES is set, restrict to those specific files.
	if len(h.allowedWorkflows) > 0 {
		workflowFile := strings.TrimPrefix(relPath, ".github/workflows/")
		allowed := false
		for _, wf := range h.allowedWorkflows {
			if strings.EqualFold(wf, workflowFile) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("workflow file %q not in allowed list", workflowFile)
		}
	}

	return &claims, nil
}

// mintToken looks up the PEM for (org, role), generates a GitHub App JWT,
// finds the installation, and creates an installation token.
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

	jwt, err := generateAppJWT(appID, pemData)
	if err != nil {
		return "", "", &mintError{status: http.StatusInternalServerError, msg: fmt.Sprintf("generating app JWT: %v", err)}
	}

	installationID, err := h.findInstallation(ctx, jwt, org, repos[0])
	if err != nil {
		return "", "", &mintError{status: http.StatusBadGateway, msg: err.Error()}
	}

	token, expiresAt, err := h.createInstallationToken(ctx, jwt, installationID, role, repos)
	if err != nil {
		return "", "", &mintError{status: http.StatusBadGateway, msg: err.Error()}
	}

	return token, expiresAt, nil
}

// findInstallation looks up the app's installation ID via the repo-based API.
// Using GET /repos/{owner}/{repo}/installation instead of /orgs/{org}/installation
// enables per-repo app installations in the future.
func (h *Handler) findInstallation(ctx context.Context, jwt, org, repo string) (int64, error) {
	reqURL := fmt.Sprintf("%s/repos/%s/%s/installation", h.githubBaseURL, org, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, fmt.Errorf("creating installation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("getting installation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("getting installation for %s/%s returned status %d", org, repo, resp.StatusCode)
	}

	var inst installationResponse
	if err := json.NewDecoder(resp.Body).Decode(&inst); err != nil {
		return 0, fmt.Errorf("decoding installation: %w", err)
	}

	if inst.ID == 0 {
		return 0, fmt.Errorf("no installation found for %s/%s", org, repo)
	}

	return inst.ID, nil
}

// rolePermissions defines the minimum GitHub App permissions per agent role.
// Tokens are always downscoped to these permissions regardless of what the
// App itself has configured.
var rolePermissions = map[string]map[string]string{
	"triage":   {"contents": "read", "issues": "write", "metadata": "read"},
	"coder":    {"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
	"review":   {"contents": "read", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
	"fix":      {"contents": "write", "pull_requests": "write", "issues": "write", "metadata": "read"},
	"retro":       {"actions": "read", "contents": "read", "pull_requests": "read", "issues": "write", "metadata": "read"},
	"prioritize":  {"contents": "read", "issues": "write", "organization_projects": "write", "metadata": "read"},
	"fullsend":    {"actions": "write", "contents": "write", "pull_requests": "write", "workflows": "write", "metadata": "read"},
}

// createInstallationToken exchanges a JWT for an installation access token,
// scoped to the given repos and role-specific permissions.
func (h *Handler) createInstallationToken(ctx context.Context, jwt string, installationID int64, role string, repos []string) (string, string, error) {
	perms, ok := rolePermissions[role]
	if !ok {
		return "", "", fmt.Errorf("no permissions defined for role %q", role)
	}
	tokenReqBody := map[string]interface{}{
		"repositories": repos,
		"permissions":  perms,
	}

	tokenReqBytes, err := json.Marshal(tokenReqBody)
	if err != nil {
		return "", "", fmt.Errorf("marshaling token request: %w", err)
	}

	reqURL := fmt.Sprintf("%s/app/installations/%d/access_tokens", h.githubBaseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(tokenReqBytes))
	if err != nil {
		return "", "", fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("creating installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("creating installation token returned status %d", resp.StatusCode)
	}

	var tokenResp installationTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", "", fmt.Errorf("decoding token response: %w", err)
	}

	if tokenResp.Token == "" {
		return "", "", fmt.Errorf("empty installation token returned")
	}

	return tokenResp.Token, tokenResp.ExpiresAt, nil
}

// generateAppJWT creates a signed JWT for GitHub App authentication.
func generateAppJWT(appID string, pemData []byte) (string, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		pkcs8Key, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if pkcs8Err != nil {
			return "", fmt.Errorf("failed to parse private key (PKCS1: %v, PKCS8: %v)", err, pkcs8Err)
		}
		var ok bool
		key, ok = pkcs8Key.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("PKCS8 key is not RSA")
		}
	}

	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]interface{}{
		"iss": appID,
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshaling JWT header: %w", err)
	}

	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshaling JWT claims: %w", err)
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signingInput := headerB64 + "." + claimsB64

	hashed := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}

	signatureB64 := base64.RawURLEncoding.EncodeToString(signature)

	return signingInput + "." + signatureB64, nil
}

// checkAllowedOrg returns true if org is in the allowedOrgs list (case-insensitive).
func (h *Handler) checkAllowedOrg(org string) bool {
	for _, entry := range h.allowedOrgs {
		if strings.EqualFold(entry, org) {
			return true
		}
	}
	return false
}

// checkAllowedRole returns true if role is in the allowedRoles list.
func (h *Handler) checkAllowedRole(role string) bool {
	for _, entry := range h.allowedRoles {
		if entry == role {
			return true
		}
	}
	return false
}

// lookupRoleAppID returns the GitHub App ID for the given org/role from the
// cached ROLE_APP_IDS map (parsed once at init). Keys are "org/role" format.
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

// mintError is returned by mintToken to carry an appropriate HTTP status code.
type mintError struct {
	status int
	msg    string
}

func (e *mintError) Error() string { return e.msg }

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
