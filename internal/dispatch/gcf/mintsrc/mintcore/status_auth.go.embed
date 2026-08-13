package mintcore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// statusAuthError carries an HTTP status code and message for status auth failures.
type statusAuthError struct {
	status  int
	message string
}

// StatusAuthConfig bundles the status auth configuration fields for
// NewHandlerFromConfig and ParseWorkerConfig.
type StatusAuthConfig struct {
	StatusAuth               string
	StatusGithubGroup        string
	StatusGithubClientID     string
	StatusGithubClientSecret string
}

// ParseStatusAuthModes parses the STATUS_AUTH CSV into a list of enabled modes.
// Default: ["oidc"].
func ParseStatusAuthModes(raw string) []string {
	if raw == "" {
		return []string{"oidc"}
	}
	var modes []string
	for _, entry := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(strings.ToLower(entry)); trimmed != "" {
			modes = append(modes, trimmed)
		}
	}
	if len(modes) == 0 {
		return []string{"oidc"}
	}
	return modes
}

// ValidateStatusAuthConfig validates the status auth configuration at startup.
// When "github" mode is enabled, STATUS_GITHUB_GROUP, STATUS_GITHUB_CLIENT_ID,
// and STATUS_GITHUB_CLIENT_SECRET are required.
func ValidateStatusAuthConfig(modes []string, group, clientID, clientSecret string) error {
	for _, mode := range modes {
		switch mode {
		case "oidc":
			// No additional config required.
		case "github":
			if group == "" {
				return fmt.Errorf("STATUS_GITHUB_GROUP is required when github status auth mode is enabled")
			}
			if !strings.Contains(group, "/") {
				return fmt.Errorf("STATUS_GITHUB_GROUP must be in ORG/TEAM format, got %q", group)
			}
			if clientID == "" {
				return fmt.Errorf("STATUS_GITHUB_CLIENT_ID is required when github status auth mode is enabled")
			}
			if clientSecret == "" {
				return fmt.Errorf("STATUS_GITHUB_CLIENT_SECRET is required when github status auth mode is enabled")
			}
		case "access":
			// Future mode — accepted without error for forward compatibility.
		default:
			return fmt.Errorf("STATUS_AUTH contains unknown mode %q", mode)
		}
	}
	return nil
}

// statusAuthModeEnabled reports whether a mode is in the enabled list.
func statusAuthModeEnabled(modes []string, mode string) bool {
	for _, m := range modes {
		if m == mode {
			return true
		}
	}
	return false
}

// authenticateStatus dispatches /v1/status authentication across the configured
// modes. It tries OIDC first (if enabled), then GitHub user token (if enabled).
// On success it returns the caller's org. On failure it returns a statusAuthError.
func (h *Handler) authenticateStatus(ctx context.Context, bearerToken string) (string, *statusAuthError) {
	if statusAuthModeEnabled(h.statusAuthModes, "oidc") {
		org, err := h.authenticateStatusOIDC(ctx, bearerToken)
		if err == nil {
			return org, nil
		}
		// OIDC failed; if github mode is also enabled, fall through.
		if !statusAuthModeEnabled(h.statusAuthModes, "github") {
			return "", err
		}
		log.Printf("OIDC auth failed for /v1/status, trying github mode: %v", err.message)
	}

	if statusAuthModeEnabled(h.statusAuthModes, "github") {
		return h.authenticateStatusGitHub(ctx, bearerToken)
	}

	return "", &statusAuthError{
		status:  http.StatusUnauthorized,
		message: "authentication failed",
	}
}

// authenticateStatusOIDC verifies the bearer token as an OIDC JWT and performs
// the same authorization checks as the /v1/token path. Returns the caller's org.
func (h *Handler) authenticateStatusOIDC(ctx context.Context, oidcToken string) (string, *statusAuthError) {
	claims, err := h.oidcVerifier.Verify(ctx, oidcToken)
	if err != nil {
		log.Printf("OIDC verification failed for /v1/status: %v", err)
		return "", &statusAuthError{
			status:  http.StatusUnauthorized,
			message: "authentication failed",
		}
	}
	if err := AuthorizeToken(claims, h.allowedOrgs, h.perRepoWIFRepos); err != nil {
		log.Printf("token authorization failed for /v1/status: %v", err)
		return "", &statusAuthError{
			status:  http.StatusUnauthorized,
			message: "authentication failed",
		}
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
		return "", &statusAuthError{
			status:  http.StatusUnauthorized,
			message: "authentication failed",
		}
	}
	return claims.RepositoryOwner, nil
}

// authenticateStatusGitHub validates the bearer token as a GitHub user token
// and checks org/team membership against the configured group.
func (h *Handler) authenticateStatusGitHub(ctx context.Context, token string) (string, *statusAuthError) {
	username, err := GitHubUserFromToken(ctx, h.httpClient, h.githubBaseURL, token)
	if err != nil {
		log.Printf("GitHub user token validation failed for /v1/status: %v", err)
		return "", &statusAuthError{
			status:  http.StatusUnauthorized,
			message: "authentication failed",
		}
	}

	org, team := parseGitHubGroup(h.statusGithubGroup)

	isMember, err := CheckTeamMembership(ctx, h.httpClient, h.githubBaseURL, token, org, team, username)
	if err != nil {
		log.Printf("GitHub team membership check failed for /v1/status: user=%s group=%s err=%v", username, h.statusGithubGroup, err)
		return "", &statusAuthError{
			status:  http.StatusForbidden,
			message: "membership check failed",
		}
	}
	if !isMember {
		log.Printf("GitHub user %s is not a member of %s", username, h.statusGithubGroup)
		return "", &statusAuthError{
			status:  http.StatusForbidden,
			message: "not a member of the required group",
		}
	}

	return org, nil
}

// parseGitHubGroup splits an "ORG/TEAM" string into its components.
func parseGitHubGroup(group string) (org, team string) {
	parts := strings.SplitN(group, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return group, ""
}

// githubUserResponse is the relevant subset of the GET /user response.
type githubUserResponse struct {
	Login string `json:"login"`
}

// GitHubUserFromToken validates a GitHub user token by calling GET /user
// and returns the authenticated user's login.
func GitHubUserFromToken(ctx context.Context, httpClient HTTPDoer, githubBaseURL, token string) (string, error) {
	reqURL := fmt.Sprintf("%s/user", githubBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating /user request: %w", err)
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling /user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("/user returned status %d", resp.StatusCode)
	}

	var user githubUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("decoding /user response: %w", err)
	}
	if user.Login == "" {
		return "", fmt.Errorf("/user returned empty login")
	}
	return user.Login, nil
}

// teamMembershipResponse is the relevant subset of the GitHub team membership API.
type teamMembershipResponse struct {
	State string `json:"state"`
}

// CheckTeamMembership checks whether a user is an active member of the given
// org/team by calling GET /orgs/{org}/teams/{team}/memberships/{username}.
func CheckTeamMembership(ctx context.Context, httpClient HTTPDoer, githubBaseURL, token, org, team, username string) (bool, error) {
	reqURL := fmt.Sprintf("%s/orgs/%s/teams/%s/memberships/%s", githubBaseURL, org, team, username)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return false, fmt.Errorf("creating team membership request: %w", err)
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("checking team membership: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return false, fmt.Errorf("team membership check returned status %d", resp.StatusCode)
	}

	var membership teamMembershipResponse
	if err := json.NewDecoder(resp.Body).Decode(&membership); err != nil {
		return false, fmt.Errorf("decoding team membership response: %w", err)
	}
	return membership.State == "active", nil
}
