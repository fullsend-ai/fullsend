package mintcore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// errOIDCNotAuthenticated is returned by verifyOIDCRequest when the
// request cannot be authenticated via OIDC — either the Bearer header
// is missing or token verification failed. Distinct from "valid OIDC
// token that fails authorization", which is a hard policy denial.
var errOIDCNotAuthenticated = errors.New("OIDC: not authenticated")

// errOIDCKeysUnavailable marks a verifier error where the mint could not
// obtain what it needs to check the token: the issuer's discovery
// document or signing keys did not arrive (transport failure, timeout,
// 5xx or 429), or, for the STS verifier, the exchange that checks the
// token against the issuer's keys did not complete. The token was never
// judged, so the request is answered 503 rather than 401 and the client
// retries. Verifiers wrap it only for these cases; every other failure,
// including ones the mint cannot classify, stays a plain error and
// produces 401.
var errOIDCKeysUnavailable = errors.New("OIDC signing keys unavailable")

// oidcKeysUnavailableMessage is the 503 response body's error message
// for a request whose token could not be checked (errOIDCKeysUnavailable).
const oidcKeysUnavailableMessage = "OIDC signing key fetch failed; the token was not checked, retry later"

// transientUpstreamStatus reports whether an HTTP status from a key or
// verification endpoint means the upstream could not answer right now,
// as opposed to answering with a definite result.
func transientUpstreamStatus(status int) bool {
	return status >= http.StatusInternalServerError || status == http.StatusTooManyRequests
}

// writeOIDCAuthError answers a request that failed OIDC authentication:
// 503 when the mint could not obtain the keys to check the token
// (errOIDCKeysUnavailable), 401 "authentication failed" otherwise. A
// request is never accepted in either case.
func writeOIDCAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, errOIDCKeysUnavailable) {
		writeError(w, http.StatusServiceUnavailable, oidcKeysUnavailableMessage)
		return
	}
	writeError(w, http.StatusUnauthorized, "authentication failed")
}

// verifyOIDCRequest extracts the Bearer token, verifies it via OIDC,
// and runs the full authorization pipeline (AuthorizeToken,
// ValidateWorkflowRef). Used by both the /v1/token path and the
// /v1/status auth pipeline.
//
// Returns errOIDCNotAuthenticated (via errors.Is) when the Bearer
// header is missing or OIDC verification fails — callers that support
// fallback auth can check this. When verification failed because the
// signing keys could not be obtained, the error also matches
// errOIDCKeysUnavailable. Any other error means the token was valid but
// denied by policy (hard 401, no fallback).
func (h *Handler) verifyOIDCRequest(ctx context.Context, r *http.Request) (*Claims, error) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, errOIDCNotAuthenticated
	}
	oidcToken := strings.TrimPrefix(authHeader, "Bearer ")

	claims, err := h.oidcVerifier.Verify(ctx, oidcToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errOIDCNotAuthenticated, err)
	}
	if claims == nil {
		return nil, errOIDCNotAuthenticated
	}

	if err := AuthorizeToken(claims, h.perRepoWIFRepos); err != nil {
		return nil, fmt.Errorf("token authorization failed: %w", err)
	}

	if err := ValidateWorkflowRef(claims.JobWorkflowRef, h.workflowHostRepos, h.allowedWorkflowFiles); err != nil {
		return nil, fmt.Errorf("workflow ref validation failed: %w", err)
	}
	return claims, nil
}
