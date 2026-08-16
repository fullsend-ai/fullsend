//go:build !js

package mintcore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// NewHandler creates a Handler with the given dependencies.
// Environment variables for handler-level config (ROLE_APP_IDS, ALLOWED_ROLES,
// ALLOWED_ORGS, ALLOWED_WORKFLOW_FILES, PER_REPO_WIF_REPOS) are read once at
// construction time. The OIDCVerifier is injected by the caller so different
// verification strategies can be used (STSVerifier for the Cloud Function,
// JWKSVerifier for devmint). The handler performs authorization (org-allowed,
// workflow-ref) after the verifier authenticates the token.
func NewHandler(pemAccessor PEMAccessor, oidcVerifier OIDCVerifier) (*Handler, error) {
	doer := NewHTTPClientDoer(30 * time.Second)

	perRepoWIFRepos := make(map[string]bool)
	for _, entry := range SplitCSV(os.Getenv("PER_REPO_WIF_REPOS")) {
		perRepoWIFRepos[strings.ToLower(entry)] = true
	}

	workflowHostRepos := make(map[string]bool)
	for _, entry := range SplitCSV(os.Getenv("WORKFLOW_HOST_REPOS")) {
		workflowHostRepos[strings.ToLower(entry)] = true
	}
	if len(workflowHostRepos) == 0 {
		workflowHostRepos["fullsend-ai/fullsend"] = true
	}

	h := &Handler{
		doer:                 doer,
		pemAccessor:          pemAccessor,
		oidcVerifier:         oidcVerifier,
		githubBaseURL:        "https://api.github.com",
		foreignCache:         make(map[string]foreignCacheEntry),
		foreignInflight:      make(map[string]*foreignInflight),
		foreignCacheTTL:      defaultForeignCacheTTL,
		perRepoWIFRepos:      perRepoWIFRepos,
		allowedOrgs:          ParseAllowedOrgs(os.Getenv("ALLOWED_ORGS")),
		allowedWorkflowFiles: SplitCSV(os.Getenv("ALLOWED_WORKFLOW_FILES")),
		workflowHostRepos:    workflowHostRepos,
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

// ServeHTTP handles incoming token mint requests via net/http.
// It delegates to HandleRaw and writes the response to the ResponseWriter.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Method == http.MethodPost {
		defer r.Body.Close()
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			writeHTTPError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
	}

	headers := make(map[string]string, len(r.Header))
	for k := range r.Header {
		headers[k] = r.Header.Get(k)
	}

	status, respHeaders, respBody := h.HandleRaw(r.Context(), r.Method, r.URL.Path, headers, body)
	for k, v := range respHeaders {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	w.Write(respBody)
}

func writeHTTPError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
