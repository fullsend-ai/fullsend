package mintcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxRepos = 500

const defaultForeignCacheTTL = 60 * time.Second

type foreignCacheEntry struct {
	allowlist []string
	fetchedAt time.Time
}

// mintRequest is the JSON body sent by .fullsend agent workflows.
type mintRequest struct {
	Role      string   `json:"role"`
	TargetOrg string   `json:"target_org,omitempty"`
	Repos     []string `json:"repos"`
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
// When authenticated via OIDC, Org is set to the caller's org.
// When authenticated via an optional validator (e.g. GitHub user
// token), AllowedOrgs lists all configured orgs instead.
type statusResponse struct {
	Org               string   `json:"org,omitempty"`
	AllowedOrgs       []string `json:"allowed_orgs,omitempty"`
	Roles             []string `json:"roles"`
	WorkflowHostRepos []string `json:"workflow_host_repos,omitempty"`
	Version           string   `json:"version,omitempty"`
	Commit            string   `json:"commit,omitempty"`
}

// Handler holds dependencies for the token mint HTTP server.
type Handler struct {
	pemAccessor  PEMAccessor
	oidcVerifier OIDCVerifier

	githubBaseURL string

	roleAppIDs       map[string]string
	allowedRoles     []string
	legacyAppIDsOnly bool // ROLE_APP_IDS has org/role keys but no role-only keys

	foreignCache    map[string]foreignCacheEntry
	foreignInflight map[string]*foreignInflight
	foreignCacheTTL time.Duration
	foreignCacheMu  sync.Mutex

	// perRepoWIFRepos is the set of repositories with per-repo WIF treatment.
	// The handler uses this to decide repos scope policy (per-repo vs per-org).
	perRepoWIFRepos map[string]bool

	// allowedOrgs lists the orgs permitted to use the mint (per-org callers).
	allowedOrgs []string

	// allowedWorkflowFiles lists the workflow basenames permitted to call the mint.
	allowedWorkflowFiles []string

	// workflowHostRepos lists the repos whose workflows are trusted to
	// call the mint in per-repo mode. Defaults to fullsend-ai/fullsend.
	// Per-org callers hard-wire to {org}/.fullsend and upstream instead.
	workflowHostRepos map[string]bool
}

type foreignInflight struct {
	wg        sync.WaitGroup
	allowlist []string
	err       error
}

// NewHandler creates a Handler with the given dependencies.
// Configuration variables (ROLE_APP_IDS, ALLOWED_ROLES, ALLOWED_ORGS,
// ALLOWED_WORKFLOW_FILES, PER_REPO_WIF_REPOS, WORKFLOW_HOST_REPOS)
// are read once at construction time via the package-internal mintEnv
// accessor. On native platforms mintEnv delegates to os.Getenv; on WASM
// the CF Worker calls RegisterEnv before constructing the handler.
//
// The HTTP client for GitHub API calls is obtained from the
// package-internal mintHTTP accessor. On native platforms this is a
// cached *http.Client; on WASM the Worker calls RegisterHTTP first.
//
// The OIDC audience is the compile-time constant
// mintconsts.OIDCAudience — it is not read from the environment.
//
// Load sites construct the appropriate OIDCVerifier (STSVerifier for
// the Cloud Function, JWKSVerifier for devmint/standalone/Worker) and
// pass it in. The handler only performs authorization (org-allowed,
// workflow-ref) after the verifier authenticates the token.
func NewHandler(pemAccessor PEMAccessor, oidcVerifier OIDCVerifier) (*Handler, error) {
	if oidcVerifier == nil {
		return nil, errors.New("oidcVerifier must not be nil")
	}

	// Register custom role permissions before processing ALLOWED_ROLES
	// so that HasRole sees them during validation.
	if raw := mintEnv("CUSTOM_ROLE_PERMISSIONS"); raw != "" {
		var perms map[string]map[string]string
		if err := json.Unmarshal([]byte(raw), &perms); err != nil {
			return nil, fmt.Errorf("failed to parse CUSTOM_ROLE_PERMISSIONS: %w", err)
		}
		if err := RegisterCustomRolePermissions(perms); err != nil {
			return nil, fmt.Errorf("registering custom role permissions: %w", err)
		}
	}

	perRepoWIFRepos := make(map[string]bool)
	for _, entry := range SplitCSV(mintEnv("PER_REPO_WIF_REPOS")) {
		perRepoWIFRepos[strings.ToLower(entry)] = true
	}

	workflowHostRepos := make(map[string]bool)
	for _, entry := range SplitCSV(mintEnv("WORKFLOW_HOST_REPOS")) {
		workflowHostRepos[strings.ToLower(entry)] = true
	}
	if len(workflowHostRepos) == 0 {
		workflowHostRepos["fullsend-ai/fullsend"] = true
	}

	h := &Handler{
		pemAccessor:          pemAccessor,
		oidcVerifier:         oidcVerifier,
		githubBaseURL:        "https://api.github.com",
		foreignCache:         make(map[string]foreignCacheEntry),
		foreignInflight:      make(map[string]*foreignInflight),
		foreignCacheTTL:      defaultForeignCacheTTL,
		perRepoWIFRepos:      perRepoWIFRepos,
		allowedOrgs:          ParseAllowedOrgs(mintEnv("ALLOWED_ORGS")),
		allowedWorkflowFiles: SplitCSV(mintEnv("ALLOWED_WORKFLOW_FILES")),
		workflowHostRepos:    workflowHostRepos,
	}

	if raw := mintEnv("ROLE_APP_IDS"); raw != "" {
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

	if raw := mintEnv("ALLOWED_ROLES"); raw != "" {
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

	// --- /v1/status auth pipeline ---
	if r.URL.Path == "/v1/status" {
		auth, err := h.authenticateStatus(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication failed")
			return
		}
		h.handleStatusWithAuth(w, auth)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
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
		writeError(w, http.StatusBadRequest, "repos is required")
		return
	}

	req.Repos = normalizeMintRepos(req.Repos)

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

	if req.TargetOrg != "" {
		if err := validateTargetOrg(req.TargetOrg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid target_org")
			return
		}
	}

	// --- /v1/token auth: OIDC only (shared helper) ---
	claims, isPerRepo, oidcErr := h.verifyOIDCRequest(r.Context(), r)
	if oidcErr != nil {
		log.Printf("authentication failed: %v", oidcErr)
		writeError(w, http.StatusUnauthorized, "authentication failed")
		return
	}

	ctx := r.Context()
	callerOrg := strings.ToLower(claims.RepositoryOwner)
	targetOrg := strings.ToLower(strings.TrimSpace(req.TargetOrg))
	if targetOrg == "" {
		targetOrg = callerOrg
	}

	isTargetForeign := !strings.EqualFold(targetOrg, callerOrg)
	shape, scopeErr := validateReposScope(isTargetForeign, claims.Repository, req.Repos, isPerRepo)
	if scopeErr != nil && !isTargetForeign {
		// Same-org scope denied. For per-repo callers requesting repos
		// beyond their own (specific per-repo denial), check repo-level
		// FOREIGN grants. Only override the per-repo cross-repo denial;
		// other denial reasons (empty repos, org-mode shape violations)
		// must not be overridden.
		if isPerRepo && len(req.Repos) > 0 && errors.Is(scopeErr, errPerRepoCrossRepo) {
			if fErr := h.checkRepoForeignGrants(ctx, claims, callerOrg, req.Role, req.Repos); fErr == nil {
				log.Printf("intra-org repo-level foreign grant: caller=%s target_org=%s repos=%v role=%s",
					claims.Repository, callerOrg, req.Repos, req.Role)
				scopeErr = nil
			} else {
				log.Printf("intra-org repo-level foreign grant check failed: %v", fErr)
			}
		}
	}
	if scopeErr != nil {
		writeError(w, http.StatusForbidden, scopeErr.Error())
		return
	}
	if shape != "" {
		log.Printf("repos scope shape=%s requested_repos=%v source_repo=%s target_org=%s role=%s",
			shape, req.Repos, claims.Repository, targetOrg, req.Role)
	}

	if len(req.Repos) == 0 {
		log.Printf("WARNING: repos=[\"*\"] normalized to installation-wide token for target_org=%s role=%s caller_org=%s source_repo=%s",
			targetOrg, req.Role, callerOrg, claims.Repository)
	}

	var token, expiresAt string
	var granted *GrantedScope

	if !isTargetForeign {
		token, expiresAt, granted, err = h.mintToken(ctx, callerOrg, req.Role, req.Repos)
	} else {
		token, expiresAt, granted, err = h.mintTokenCrossOrg(ctx, claims, targetOrg, req.Role, req.Repos)
	}
	if err != nil {
		log.Printf("failed to mint token: org=%s target_org=%s role=%s err=%v", callerOrg, targetOrg, req.Role, err)
		var me *mintError
		if errors.As(err, &me) {
			msg := "mint failed"
			// Surface the user-facing message when the error explicitly
			// provides one. Only errors that set userMsg opt into this;
			// all others keep the generic message to avoid leaking
			// internal details.
			if me.userMsg != "" {
				msg = me.userMsg
			}
			writeError(w, me.status, msg)
		} else {
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	if granted != nil {
		log.Printf("minted: org=%s target_org=%s role=%s app_id=%s installation_id=%d requested_repos=%v source_repo=%s workflow_ref=%s",
			callerOrg, targetOrg, req.Role, granted.AppID, granted.InstallationID, req.Repos, claims.Repository, claims.JobWorkflowRef)
		log.Printf("granted scope: repos=%v permissions=%v repo_selection=%s",
			granted.Repos, granted.Permissions, granted.RepoSelection)
		if len(req.Repos) == 0 {
			log.Printf("WARNING: repos=[\"*\"] installation-wide token granted for target_org=%s role=%s repo_selection=%s",
				targetOrg, req.Role, granted.RepoSelection)
		} else if granted.RepoSelection == "all" {
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
	resp := map[string]string{"status": "ok"}
	if Version != "" {
		resp["version"] = Version
	}
	if Commit != "" {
		resp["commit"] = Commit
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// handleStatus is a legacy wrapper for OIDC-only status auth. It
// delegates to handleStatusWithAuth with an OIDC result. Retained for
// backward compatibility with tests that call it directly.
func (h *Handler) handleStatus(w http.ResponseWriter, claims *Claims) {
	h.handleStatusWithAuth(w, &statusAuthResult{oidcClaims: claims})
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

	var installation installationResponse
	if len(repos) == 0 {
		installation, err = findOrgInstallationDetails(ctx, h.githubBaseURL, jwt, org)
	} else {
		installation, err = findInstallationDetails(ctx, h.githubBaseURL, jwt, org, repos[0])
	}
	if err != nil {
		// A 404 from FindInstallation means the repo is not covered by
		// the GitHub App installation. Surface a clear 422 so callers
		// can diagnose misconfigured installations. Transient errors
		// (500, 503, 429, network) propagate as 502.
		if len(repos) > 0 && errors.Is(err, ErrInstallationNotFound) {
			umsg := fmt.Sprintf("repository %s/%s is not covered by the GitHub App installation", org, repos[0])
			return "", "", nil, &mintError{
				status:  http.StatusUnprocessableEntity,
				msg:     umsg,
				userMsg: umsg,
			}
		}
		return "", "", nil, &mintError{status: http.StatusBadGateway, msg: err.Error()}
	}
	installationID := installation.ID

	// Verify all requested repos are covered by the same installation.
	// If the GitHub App uses selected-repository installation mode,
	// repos not in the selection return 404 from the installation
	// lookup. Detecting this upfront produces a clear error instead
	// of a confusing 422 from CreateInstallationToken.
	//
	// Only 404 responses indicate a genuinely uncovered repo (→ 422).
	// Transient failures (500, 503, 429, network errors) are propagated
	// as 502, matching the repos[0] error path above.
	if len(repos) > 1 {
		for _, repo := range repos[1:] {
			otherID, otherErr := FindInstallation(ctx, h.githubBaseURL, jwt, org, repo)
			if otherErr != nil {
				if errors.Is(otherErr, ErrInstallationNotFound) {
					umsg := fmt.Sprintf("repository %s/%s is not covered by the GitHub App installation", org, repo)
					return "", "", nil, &mintError{
						status:  http.StatusUnprocessableEntity,
						msg:     umsg,
						userMsg: umsg,
					}
				}
				return "", "", nil, &mintError{status: http.StatusBadGateway, msg: otherErr.Error()}
			}
			if otherID != installationID {
				umsg := fmt.Sprintf("repository %s/%s uses a different GitHub App installation than %s", org, repo, repos[0])
				return "", "", nil, &mintError{
					status:  http.StatusUnprocessableEntity,
					msg:     umsg,
					userMsg: umsg,
				}
			}
		}
	}

	token, expiresAt, granted, err := CreateInstallationTokenWithGrantedPermissions(ctx, h.githubBaseURL, jwt, installationID, org, role, repos, installation.Permissions)
	if err != nil {
		if errors.Is(err, ErrRequiredPermissionsMissing) {
			return "", "", nil, &mintError{
				status:  http.StatusUnprocessableEntity,
				msg:     err.Error(),
				userMsg: err.Error(),
			}
		}
		return "", "", nil, &mintError{status: http.StatusBadGateway, msg: err.Error()}
	}

	if granted != nil {
		granted.AppID = appID
		granted.InstallationID = installationID
	}

	return token, expiresAt, granted, nil
}

func (h *Handler) mintTokenCrossOrg(ctx context.Context, claims *Claims, targetOrg, role string, repos []string) (string, string, *GrantedScope, error) {
	// Specific repos requested → authorize exclusively via per-repo
	// FOREIGN grants. Org-level FOREIGN is not consulted for repo-scoped
	// requests; it authorizes only installation-wide tokens.
	if len(repos) > 0 {
		if err := h.checkRepoForeignGrants(ctx, claims, targetOrg, role, repos); err != nil {
			log.Printf("repo-level foreign grant check failed: %v", err)
			return "", "", nil, &mintError{status: http.StatusForbidden, msg: "foreign caller not authorized for target repos"}
		}
		log.Printf("repo-level foreign grant: caller=%s target_org=%s repos=%v role=%s",
			claims.Repository, targetOrg, repos, role)
		return h.mintToken(ctx, targetOrg, role, repos)
	}

	// Installation-wide (empty repos) → org-level FOREIGN check only.
	allowlist, err := h.loadForeignAllowlist(ctx, targetOrg, role)
	if err != nil {
		return "", "", nil, &mintError{status: http.StatusBadGateway, msg: err.Error()}
	}
	if CallerAllowed(allowlist, claims.Repository, claims.RepositoryOwner) {
		return h.mintToken(ctx, targetOrg, role, repos)
	}

	return "", "", nil, &mintError{status: http.StatusForbidden, msg: "foreign caller not authorized for target org"}
}

func (h *Handler) loadForeignAllowlist(ctx context.Context, targetOrg, role string) ([]string, error) {
	key := foreignCacheKey(targetOrg, role)

	h.foreignCacheMu.Lock()
	if entry, ok := h.foreignCache[key]; ok && time.Since(entry.fetchedAt) < h.foreignCacheTTL {
		allowlist := append([]string(nil), entry.allowlist...)
		h.foreignCacheMu.Unlock()
		return allowlist, nil
	}
	if inflight, ok := h.foreignInflight[key]; ok {
		h.foreignCacheMu.Unlock()
		inflight.wg.Wait()
		if inflight.err != nil {
			return nil, inflight.err
		}
		return append([]string(nil), inflight.allowlist...), nil
	}
	inflight := &foreignInflight{}
	inflight.wg.Add(1)
	h.foreignInflight[key] = inflight
	h.foreignCacheMu.Unlock()

	allowlist, err := h.fetchForeignAllowlist(ctx, targetOrg, role)

	h.foreignCacheMu.Lock()
	delete(h.foreignInflight, key)
	if err == nil {
		h.foreignCache[key] = foreignCacheEntry{
			allowlist: append([]string(nil), allowlist...),
			fetchedAt: time.Now(),
		}
	}
	inflight.allowlist = allowlist
	inflight.err = err
	inflight.wg.Done()
	h.foreignCacheMu.Unlock()

	if err != nil {
		return nil, err
	}
	return allowlist, nil
}

func (h *Handler) fetchForeignAllowlist(ctx context.Context, targetOrg, role string) ([]string, error) {
	appID, err := h.lookupRoleAppID(role)
	if err != nil {
		return nil, fmt.Errorf("looking up app ID for role %s: %v", role, err)
	}

	pemData, err := h.pemAccessor.AccessPEM(ctx, role)
	if err != nil {
		return nil, fmt.Errorf("reading PEM secret for role %s: %v", role, err)
	}
	defer func() {
		for i := range pemData {
			pemData[i] = 0
		}
	}()

	jwt, err := GenerateAppJWT(appID, pemData)
	if err != nil {
		return nil, fmt.Errorf("generating app JWT: %v", err)
	}

	installationID, err := FindOrgInstallation(ctx, h.githubBaseURL, jwt, targetOrg)
	if err != nil {
		return nil, fmt.Errorf("finding org installation on %s: %v", targetOrg, err)
	}

	allowlist, err := ReadForeignAllowlist(ctx, h.githubBaseURL, jwt, installationID, targetOrg, role)
	if err != nil {
		return nil, err
	}

	return allowlist, nil
}

// checkRepoForeignGrants verifies that every repo in repos has a repo-level
// FULLSEND_FOREIGN_<role>_REPOS variable that authorizes the caller.
//
// This function serves two distinct authorization paths:
//   - Cross-org primary authorization: called from mintTokenCrossOrg when
//     a foreign request carries specific repos (repo-scoped FOREIGN grant).
//   - Intra-org fallback: called from the main handler when a per-repo
//     caller requests repos beyond its own repository within the same org
//     (errPerRepoCrossRepo), allowing cross-repo access via repo-level grants.
func (h *Handler) checkRepoForeignGrants(ctx context.Context, claims *Claims, targetOrg, role string, repos []string) error {
	for _, repo := range repos {
		allowlist, err := h.loadRepoForeignAllowlist(ctx, targetOrg, repo, role)
		if err != nil {
			return fmt.Errorf("checking repo-level foreign grant on %s/%s: %v", targetOrg, repo, err)
		}
		if !CallerAllowed(allowlist, claims.Repository, claims.RepositoryOwner) {
			return fmt.Errorf("caller %s not authorized by repo-level foreign grant on %s/%s", claims.Repository, targetOrg, repo)
		}
	}
	return nil
}

// loadRepoForeignAllowlist loads the repo-level FOREIGN allowlist for a
// specific target repo, with in-memory caching and inflight dedup (same
// pattern as loadForeignAllowlist for org-level).
func (h *Handler) loadRepoForeignAllowlist(ctx context.Context, targetOrg, targetRepo, role string) ([]string, error) {
	key := repoForeignCacheKey(targetOrg, targetRepo, role)

	h.foreignCacheMu.Lock()
	if entry, ok := h.foreignCache[key]; ok && time.Since(entry.fetchedAt) < h.foreignCacheTTL {
		allowlist := append([]string(nil), entry.allowlist...)
		h.foreignCacheMu.Unlock()
		return allowlist, nil
	}
	if inflight, ok := h.foreignInflight[key]; ok {
		h.foreignCacheMu.Unlock()
		inflight.wg.Wait()
		if inflight.err != nil {
			return nil, inflight.err
		}
		return append([]string(nil), inflight.allowlist...), nil
	}
	inflight := &foreignInflight{}
	inflight.wg.Add(1)
	h.foreignInflight[key] = inflight
	h.foreignCacheMu.Unlock()

	allowlist, err := h.fetchRepoForeignAllowlist(ctx, targetOrg, targetRepo, role)

	h.foreignCacheMu.Lock()
	delete(h.foreignInflight, key)
	if err == nil {
		h.foreignCache[key] = foreignCacheEntry{
			allowlist: append([]string(nil), allowlist...),
			fetchedAt: time.Now(),
		}
	}
	inflight.allowlist = allowlist
	inflight.err = err
	inflight.wg.Done()
	h.foreignCacheMu.Unlock()

	if err != nil {
		return nil, err
	}
	return allowlist, nil
}

// fetchRepoForeignAllowlist reads FULLSEND_FOREIGN_<role>_REPOS from a
// specific target repo's repo-level Actions variables.
func (h *Handler) fetchRepoForeignAllowlist(ctx context.Context, targetOrg, targetRepo, role string) ([]string, error) {
	appID, err := h.lookupRoleAppID(role)
	if err != nil {
		return nil, fmt.Errorf("looking up app ID for role %s: %v", role, err)
	}

	pemData, err := h.pemAccessor.AccessPEM(ctx, role)
	if err != nil {
		return nil, fmt.Errorf("reading PEM secret for role %s: %v", role, err)
	}
	defer func() {
		for i := range pemData {
			pemData[i] = 0
		}
	}()

	jwt, err := GenerateAppJWT(appID, pemData)
	if err != nil {
		return nil, fmt.Errorf("generating app JWT: %v", err)
	}

	installationID, err := FindInstallation(ctx, h.githubBaseURL, jwt, targetOrg, targetRepo)
	if err != nil {
		return nil, fmt.Errorf("finding repo installation on %s/%s: %v", targetOrg, targetRepo, err)
	}

	allowlist, err := ReadForeignAllowlistFromRepo(ctx, h.githubBaseURL, jwt, installationID, targetOrg, targetRepo, role)
	if err != nil {
		return nil, err
	}

	return allowlist, nil
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
// userMsg, when non-empty, is a client-safe message that the response
// boundary surfaces instead of the generic "mint failed". Errors that
// do not set userMsg keep the generic message, preventing accidental
// disclosure of internal details.
type mintError struct {
	status  int
	msg     string
	userMsg string
}

func (e *mintError) Error() string { return e.msg }

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
