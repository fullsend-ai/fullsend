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

// verifyOIDCRequest extracts the Bearer token, verifies it via OIDC,
// and runs the full authorization pipeline (AuthorizeToken,
// ValidateWorkflowRef). Used by both the /v1/token path and the
// /v1/status auth pipeline.
//
// Returns errOIDCNotAuthenticated (via errors.Is) when the Bearer
// header is missing or OIDC verification fails — callers that support
// fallback auth can check this. Any other error means the token was
// valid but denied by policy (hard 401, no fallback).
func (h *Handler) verifyOIDCRequest(ctx context.Context, r *http.Request) (*Claims, error) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return nil, errOIDCNotAuthenticated
	}
	oidcToken := strings.TrimPrefix(authHeader, "Bearer ")

	claims, err := h.oidcVerifier.Verify(ctx, oidcToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errOIDCNotAuthenticated, err)
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
