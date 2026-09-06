package mintcore

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
)

// errStatusAuthSkip is returned by the stub validateStatusGitHub and
// by the real validator when the request does not carry credentials it
// recognises. authenticateStatus treats this as "not applicable" and
// falls through to 401.
var errStatusAuthSkip = errors.New("status auth: skip")

// statusAuthResult describes how a /v1/status request was
// authenticated, so handleStatusWithAuth can choose the right payload shape.
type statusAuthResult struct {
	// oidcClaims is set when OIDC authentication succeeded.
	// When non-nil, the status response is scoped to the
	// authenticating workflow's org. When nil, a non-OIDC validator
	// authenticated the request and the status response reports all
	// configured allowed orgs.
	oidcClaims *Claims
}

// authenticateStatus runs the /v1/status auth pipeline:
//
//  1. OIDC (always first, always compiled in).
//  2. validateStatusGitHub — compile-time selected via build tags:
//     real validator (github tag) or stub returning errStatusAuthSkip.
//  3. validateStatusCFAccess — Cloudflare Access Managed OAuth.
//  4. If everything fails → error.
//
// Non-skip errors from validateStatusGitHub or validateStatusCFAccess
// produce an immediate 401 with no fall-through — the validator
// positively rejected the request.
func (h *Handler) authenticateStatus(ctx context.Context, r *http.Request) (*statusAuthResult, error) {
	// --- OIDC (always tried first) ---
	claims, _, oidcErr := h.verifyOIDCRequest(ctx, r)
	if oidcErr == nil {
		return &statusAuthResult{oidcClaims: claims}, nil
	}
	if !errors.Is(oidcErr, errOIDCNotAuthenticated) {
		// OIDC token is valid but not authorized — policy denial,
		// do NOT fall through to other validators.
		log.Printf("OIDC authorization failed for /v1/status: %v", oidcErr)
		return nil, errors.New("authentication failed")
	}
	log.Printf("OIDC verification failed for /v1/status: %v", oidcErr)

	// --- GitHub validator (compile-time selected) ---
	ghErr := validateStatusGitHub(ctx, r)
	if ghErr == nil {
		return &statusAuthResult{}, nil
	}
	if !errors.Is(ghErr, errStatusAuthSkip) {
		// Real rejection — 401 immediately, no fall-through.
		log.Printf("GitHub status validator rejected request: %v", ghErr)
		return nil, errors.New("authentication failed")
	}

	// --- Cloudflare Access validator ---
	cfErr := validateStatusCFAccess(ctx, r)
	if cfErr == nil {
		return &statusAuthResult{}, nil
	}
	if !errors.Is(cfErr, errStatusAuthSkip) {
		// Real rejection — 401 immediately, no fall-through.
		log.Printf("CF Access status validator rejected request: %v", cfErr)
	}

	return nil, errors.New("authentication failed")
}

// handleStatusWithAuth serves the /v1/status response using the
// authentication result to determine payload shape.
func (h *Handler) handleStatusWithAuth(w http.ResponseWriter, auth *statusAuthResult) {
	roles := append([]string(nil), h.allowedRoles...)

	var hostRepos []string
	for repo := range h.workflowHostRepos {
		hostRepos = append(hostRepos, repo)
	}
	sort.Strings(hostRepos)

	resp := statusResponse{
		Roles:             roles,
		WorkflowHostRepos: hostRepos,
		Version:           Version,
		Commit:            Commit,
	}

	if auth.oidcClaims != nil {
		// OIDC success: scope to the authenticating workflow's org.
		resp.Org = strings.ToLower(auth.oidcClaims.RepositoryOwner)
	} else {
		// Non-OIDC validator: report all configured allowed orgs.
		resp.AllowedOrgs = h.allowedOrgs
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("encoding status response: %v", err)
	}
}
