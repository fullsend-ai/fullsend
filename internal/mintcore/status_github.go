//go:build github

package mintcore

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// validateStatusGitHub authenticates a /v1/status request using a
// GitHub user token (OAuth2 login or gh / GH_TOKEN). It:
//
//  1. Extracts the bearer token from the request.
//  2. Calls GET /user on the GitHub API to identify the caller.
//  3. Checks that the caller is a member of the configured
//     StatusGitHubGroup (ORG/TEAM format).
//
// Returns nil on success. Returns errStatusAuthSkip when the
// validator is not configured (group empty).
// Returns an error on all other failures (invalid token, not a
// member, API error). All status-auth failures collapse to 401.
func validateStatusGitHub(ctx context.Context, r *http.Request) error {
	if StatusGitHubGroup == "" {
		return errStatusAuthSkip
	}

	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return errStatusAuthSkip
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	parts := strings.SplitN(StatusGitHubGroup, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid StatusGitHubGroup %q: must be ORG/TEAM", StatusGitHubGroup)
	}
	org, team := parts[0], parts[1]

	// Step 1: verify the token by fetching the authenticated user.
	username, err := githubGetUser(ctx, token)
	if err != nil {
		return fmt.Errorf("github user lookup failed: %w", err)
	}

	// Step 2: check team membership.
	if err := githubCheckTeamMembership(ctx, token, org, team, username); err != nil {
		return fmt.Errorf("github team membership check failed: %w", err)
	}

	log.Printf("status auth: github user %q authenticated via team %s/%s", username, org, team)
	return nil
}

// githubGetUser calls GET /user with the given token and returns the
// login name. Uses mintHTTP for all outbound HTTP.
func githubGetUser(ctx context.Context, token string) (string, error) {
	base, err := githubAPIBaseURL()
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return "", fmt.Errorf("creating /user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return "", fmt.Errorf("calling /user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("invalid or expired token (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d from /user", resp.StatusCode)
	}

	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("decoding /user response: %w", err)
	}
	if user.Login == "" {
		return "", fmt.Errorf("/user returned empty login")
	}
	return user.Login, nil
}

// githubCheckTeamMembership verifies that username is a member of
// org/team. Uses the user's own token for the membership check
// (GET /orgs/{org}/teams/{team}/memberships/{username}).
func githubCheckTeamMembership(ctx context.Context, token, org, team, username string) error {
	base, err := githubAPIBaseURL()
	if err != nil {
		return err
	}
	membershipURL := fmt.Sprintf("%s/orgs/%s/teams/%s/memberships/%s", base,
		url.PathEscape(org), url.PathEscape(team), url.PathEscape(username))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, membershipURL, nil)
	if err != nil {
		return fmt.Errorf("creating team membership request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return fmt.Errorf("calling team membership API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("user %q is not a member of %s/%s", username, org, team)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d from team membership API", resp.StatusCode)
	}

	var membership struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&membership); err != nil {
		return fmt.Errorf("decoding team membership response: %w", err)
	}
	if membership.State != "active" {
		return fmt.Errorf("user %q membership in %s/%s is %q (not active)", username, org, team, membership.State)
	}
	return nil
}
