package mintcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
type statusResponse struct {
	Org               string   `json:"org"`
	Roles             []string `json:"roles"`
	WorkflowHostRepos []string `json:"workflow_host_repos,omitempty"`
	Version           string   `json:"version,omitempty"`
	Commit            string   `json:"commit,omitempty"`
}

// Handler holds dependencies for the token mint HTTP server.
type Handler struct {
	doer         Doer
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

// jsonHeaders returns standard JSON response headers.
func jsonHeaders() map[string]string {
	return map[string]string{
		"Content-Type":  "application/json",
		"Cache-Control": "no-store",
	}
}

// errorResponse returns a JSON error body.
func errorResponse(msg string) []byte {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return b
}

// HandleRaw processes a request using simple types (no net/http dependency).
// This is the primary entry point for WASM builds and is also used by
// ServeHTTP on non-WASM platforms.
func (h *Handler) HandleRaw(ctx context.Context, method, path string, headers map[string]string, body []byte) (int, map[string]string, []byte) {
	if method == "GET" && path == "/health" {
		return h.handleHealthRaw()
	}

	if path != "/v1/token" && path != "/" && path != "/v1/status" {
		return statusNotFound, jsonHeaders(), errorResponse("not found")
	}

	if path == "/v1/status" && method != "GET" {
		return statusMethodNotAllowed, jsonHeaders(), errorResponse("method not allowed")
	}
	if path != "/v1/status" && method != "POST" {
		return statusMethodNotAllowed, jsonHeaders(), errorResponse("method not allowed")
	}

	authHeader := headers["Authorization"]
	if authHeader == "" {
		// Also check lowercase (HTTP/2 normalizes to lowercase).
		authHeader = headers["authorization"]
	}
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return statusUnauthorized, jsonHeaders(), errorResponse("missing or invalid Authorization header")
	}
	oidcToken := strings.TrimPrefix(authHeader, "Bearer ")

	if path == "/v1/status" {
		claims, err := h.oidcVerifier.Verify(ctx, oidcToken)
		if err != nil {
			log.Printf("OIDC verification failed for /v1/status: %v", err)
			return statusUnauthorized, jsonHeaders(), errorResponse("authentication failed")
		}
		if err := AuthorizeToken(claims, h.allowedOrgs, h.perRepoWIFRepos); err != nil {
			log.Printf("token authorization failed for /v1/status: %v", err)
			return statusUnauthorized, jsonHeaders(), errorResponse("authentication failed")
		}
		isPerRepo := IsPerRepoMode(claims.Repository, h.perRepoWIFRepos)
		isDualEnrolled := false
		if isPerRepo && !IsPublicMintRepos(h.perRepoWIFRepos) &&
			ValidateOrgAllowed(claims.RepositoryOwner, h.allowedOrgs) == nil {
			isDualEnrolled = true
			isPerRepo = false
		}
		wfErr := ValidateWorkflowRef(claims.JobWorkflowRef, claims.Repository, isPerRepo, h.workflowHostRepos, h.allowedWorkflowFiles)
		if wfErr != nil && isDualEnrolled {
			wfErr = ValidateWorkflowRef(claims.JobWorkflowRef, claims.Repository, true, h.workflowHostRepos, h.allowedWorkflowFiles)
		}
		if wfErr != nil {
			log.Printf("workflow ref validation failed for /v1/status: %v", wfErr)
			return statusUnauthorized, jsonHeaders(), errorResponse("authentication failed")
		}
		return h.handleStatusRaw(claims)
	}

	var req mintRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return statusBadRequest, jsonHeaders(), errorResponse("invalid JSON body")
	}

	if req.Role == "" {
		return statusBadRequest, jsonHeaders(), errorResponse("role is required")
	}

	if !RolePattern.MatchString(req.Role) {
		return statusBadRequest, jsonHeaders(), errorResponse("invalid role format")
	}

	if !h.checkAllowedRole(req.Role) {
		return statusForbidden, jsonHeaders(), errorResponse("role not allowed")
	}

	if len(req.Repos) == 0 {
		return statusBadRequest, jsonHeaders(), errorResponse("repos is required")
	}

	req.Repos = normalizeMintRepos(req.Repos)

	if len(req.Repos) > maxRepos {
		return statusBadRequest, jsonHeaders(), errorResponse(fmt.Sprintf("too many repos (max %d)", maxRepos))
	}
	for _, repo := range req.Repos {
		if !RepoNamePattern.MatchString(repo) || strings.Contains(repo, "..") {
			return statusBadRequest, jsonHeaders(), errorResponse("invalid repo name")
		}
	}

	if req.TargetOrg != "" {
		if err := validateTargetOrg(req.TargetOrg); err != nil {
			return statusBadRequest, jsonHeaders(), errorResponse("invalid target_org")
		}
	}

	claims, err := h.oidcVerifier.Verify(ctx, oidcToken)
	if err != nil {
		log.Printf("OIDC verification failed: %v", err)
		return statusUnauthorized, jsonHeaders(), errorResponse("authentication failed")
	}

	if err := AuthorizeToken(claims, h.allowedOrgs, h.perRepoWIFRepos); err != nil {
		log.Printf("token authorization failed: %v", err)
		return statusUnauthorized, jsonHeaders(), errorResponse("authentication failed")
	}
	callerOrg := strings.ToLower(claims.RepositoryOwner)
	targetOrg := strings.ToLower(strings.TrimSpace(req.TargetOrg))
	if targetOrg == "" {
		targetOrg = callerOrg
	}

	isTargetForeign := !strings.EqualFold(targetOrg, callerOrg)
	isPerRepo := IsPerRepoMode(claims.Repository, h.perRepoWIFRepos)
	isDualEnrolled := false
	if isPerRepo && !IsPublicMintRepos(h.perRepoWIFRepos) &&
		ValidateOrgAllowed(claims.RepositoryOwner, h.allowedOrgs) == nil {
		isDualEnrolled = true
		log.Printf("dual-enrollment: %s matches both per-repo and per-org — accepting workflow refs from either mode", claims.Repository)
		isPerRepo = false
	}
	wfErr := ValidateWorkflowRef(claims.JobWorkflowRef, claims.Repository, isPerRepo, h.workflowHostRepos, h.allowedWorkflowFiles)
	if wfErr != nil && isDualEnrolled {
		wfErr = ValidateWorkflowRef(claims.JobWorkflowRef, claims.Repository, true, h.workflowHostRepos, h.allowedWorkflowFiles)
	}
	if wfErr != nil {
		log.Printf("workflow ref validation failed: %v", wfErr)
		return statusUnauthorized, jsonHeaders(), errorResponse("authentication failed")
	}
	shape, scopeErr := validateReposScope(isTargetForeign, claims.Repository, req.Repos, isPerRepo)
	if scopeErr != nil && !isTargetForeign {
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
		return statusForbidden, jsonHeaders(), errorResponse(scopeErr.Error())
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
			if me.userMsg != "" {
				msg = me.userMsg
			}
			return me.status, jsonHeaders(), errorResponse(msg)
		}
		return statusInternalServerError, jsonHeaders(), errorResponse("internal error")
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

	respBody, _ := json.Marshal(resp)
	return statusOK, jsonHeaders(), respBody
}

func (h *Handler) handleHealthRaw() (int, map[string]string, []byte) {
	hdrs := jsonHeaders()
	if h.legacyAppIDsOnly {
		body, _ := json.Marshal(map[string]string{
			"status": "unhealthy",
			"reason": "ROLE_APP_IDS contains legacy org/role keys but no role-only keys; migration required",
		})
		return statusServiceUnavailable, hdrs, body
	}
	resp := map[string]string{"status": "ok"}
	if Version != "" {
		resp["version"] = Version
	}
	if Commit != "" {
		resp["commit"] = Commit
	}
	body, _ := json.Marshal(resp)
	return statusOK, hdrs, body
}

func (h *Handler) handleStatusRaw(claims *Claims) (int, map[string]string, []byte) {
	org := strings.ToLower(claims.RepositoryOwner)
	roles := append([]string(nil), h.allowedRoles...)

	// Build sorted workflow host repos list for the status response.
	var hostRepos []string
	for repo := range h.workflowHostRepos {
		hostRepos = append(hostRepos, repo)
	}
	sort.Strings(hostRepos)

	body, err := json.Marshal(statusResponse{
		Org:               org,
		Roles:             roles,
		WorkflowHostRepos: hostRepos,
		Version:           Version,
		Commit:            Commit,
	})
	if err != nil {
		log.Printf("encoding status response: %v", err)
	}

	return statusOK, jsonHeaders(), body
}

func (h *Handler) mintToken(ctx context.Context, org, role string, repos []string) (string, string, *GrantedScope, error) {
	appID, err := h.lookupRoleAppID(role)
	if err != nil {
		return "", "", nil, &mintError{status: statusForbidden, msg: fmt.Sprintf("looking up app ID for role %s: %v", role, err)}
	}

	pemData, err := h.pemAccessor.AccessPEM(ctx, role)
	if err != nil {
		return "", "", nil, &mintError{status: statusForbidden, msg: fmt.Sprintf("reading PEM secret for role %s: %v", role, err)}
	}
	defer func() {
		for i := range pemData {
			pemData[i] = 0
		}
	}()

	jwt, err := GenerateAppJWT(appID, pemData)
	if err != nil {
		return "", "", nil, &mintError{status: statusInternalServerError, msg: fmt.Sprintf("generating app JWT: %v", err)}
	}

	var installationID int64
	if len(repos) == 0 {
		installationID, err = FindOrgInstallation(ctx, h.doer, h.githubBaseURL, jwt, org)
	} else {
		installationID, err = FindInstallation(ctx, h.doer, h.githubBaseURL, jwt, org, repos[0])
	}
	if err != nil {
		if len(repos) > 0 && errors.Is(err, ErrInstallationNotFound) {
			umsg := fmt.Sprintf("repository %s/%s is not covered by the GitHub App installation", org, repos[0])
			return "", "", nil, &mintError{
				status:  statusUnprocessableEntity,
				msg:     umsg,
				userMsg: umsg,
			}
		}
		return "", "", nil, &mintError{status: statusBadGateway, msg: err.Error()}
	}

	if len(repos) > 1 {
		for _, repo := range repos[1:] {
			otherID, otherErr := FindInstallation(ctx, h.doer, h.githubBaseURL, jwt, org, repo)
			if otherErr != nil {
				if errors.Is(otherErr, ErrInstallationNotFound) {
					umsg := fmt.Sprintf("repository %s/%s is not covered by the GitHub App installation", org, repo)
					return "", "", nil, &mintError{
						status:  statusUnprocessableEntity,
						msg:     umsg,
						userMsg: umsg,
					}
				}
				return "", "", nil, &mintError{status: statusBadGateway, msg: otherErr.Error()}
			}
			if otherID != installationID {
				umsg := fmt.Sprintf("repository %s/%s uses a different GitHub App installation than %s", org, repo, repos[0])
				return "", "", nil, &mintError{
					status:  statusUnprocessableEntity,
					msg:     umsg,
					userMsg: umsg,
				}
			}
		}
	}

	token, expiresAt, granted, err := CreateInstallationToken(ctx, h.doer, h.githubBaseURL, jwt, installationID, role, repos)
	if err != nil {
		return "", "", nil, &mintError{status: statusBadGateway, msg: err.Error()}
	}

	if granted != nil {
		granted.AppID = appID
		granted.InstallationID = installationID
	}

	return token, expiresAt, granted, nil
}

func (h *Handler) mintTokenCrossOrg(ctx context.Context, claims *Claims, targetOrg, role string, repos []string) (string, string, *GrantedScope, error) {
	if len(repos) > 0 {
		if err := h.checkRepoForeignGrants(ctx, claims, targetOrg, role, repos); err != nil {
			log.Printf("repo-level foreign grant check failed: %v", err)
			return "", "", nil, &mintError{status: statusForbidden, msg: "foreign caller not authorized for target repos"}
		}
		log.Printf("repo-level foreign grant: caller=%s target_org=%s repos=%v role=%s",
			claims.Repository, targetOrg, repos, role)
		return h.mintToken(ctx, targetOrg, role, repos)
	}

	allowlist, err := h.loadForeignAllowlist(ctx, targetOrg, role)
	if err != nil {
		return "", "", nil, &mintError{status: statusBadGateway, msg: err.Error()}
	}
	if CallerAllowed(allowlist, claims.Repository, claims.RepositoryOwner) {
		return h.mintToken(ctx, targetOrg, role, repos)
	}

	return "", "", nil, &mintError{status: statusForbidden, msg: "foreign caller not authorized for target org"}
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

	installationID, err := FindOrgInstallation(ctx, h.doer, h.githubBaseURL, jwt, targetOrg)
	if err != nil {
		return nil, fmt.Errorf("finding org installation on %s: %v", targetOrg, err)
	}

	allowlist, err := ReadForeignAllowlist(ctx, h.doer, h.githubBaseURL, jwt, installationID, targetOrg, role)
	if err != nil {
		return nil, err
	}

	return allowlist, nil
}

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

	installationID, err := FindInstallation(ctx, h.doer, h.githubBaseURL, jwt, targetOrg, targetRepo)
	if err != nil {
		return nil, fmt.Errorf("finding repo installation on %s/%s: %v", targetOrg, targetRepo, err)
	}

	allowlist, err := ReadForeignAllowlistFromRepo(ctx, h.doer, h.githubBaseURL, jwt, installationID, targetOrg, targetRepo, role)
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
type mintError struct {
	status  int
	msg     string
	userMsg string
}

func (e *mintError) Error() string { return e.msg }
