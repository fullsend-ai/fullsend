package mintclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/mintcore/mintconsts"
)

var httpClient HTTPDoer = &http.Client{Timeout: 30 * time.Second}

// HTTPDoer abstracts http.Client for testability.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// defaultAudience is the canonical OIDC audience for the fullsend
// token mint, sourced from the shared mintconsts package.
const defaultAudience = mintconsts.OIDCAudience

// MintRequest holds the parameters for minting a token via the fullsend mint service.
type MintRequest struct {
	MintURL   string
	Role      string
	Level     string   // optional: privilege level ("read" or "write"); server defaults to "write" when empty
	Repos     []string // required: specific repo names, or ["*"] for installation-wide token
	TargetOrg string   // optional: cross-org mint when set and differs from caller org
	Audience  string
}

// MintResult holds the minted token and its expiry.
type MintResult struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// envLookup is overridden in tests.
var envLookup = os.Getenv

// MintToken obtains a fresh GitHub App installation token by exchanging a
// GitHub Actions OIDC JWT with the fullsend token mint service.
//
// It reads ACTIONS_ID_TOKEN_REQUEST_URL and ACTIONS_ID_TOKEN_REQUEST_TOKEN
// from the environment. These are set automatically by GitHub Actions when
// the job declares id-token: write permission.
func MintToken(ctx context.Context, req MintRequest) (*MintResult, error) {
	if req.MintURL == "" {
		return nil, fmt.Errorf("mint URL is required")
	}
	parsed, err := url.Parse(req.MintURL)
	if err != nil {
		return nil, fmt.Errorf("invalid mint URL: %w", err)
	}
	isLocalhost := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"
	if parsed.Scheme != "https" && !isLocalhost {
		return nil, fmt.Errorf("mint URL must use HTTPS")
	}
	if req.Role == "" {
		return nil, fmt.Errorf("role is required")
	}
	if len(req.Repos) == 0 {
		return nil, fmt.Errorf("repos is required")
	}
	audience := req.Audience
	if audience == "" {
		audience = defaultAudience
	}

	oidcJWT, err := fetchOIDCJWT(ctx, audience)
	if err != nil {
		return nil, fmt.Errorf("fetching OIDC JWT: %w", err)
	}

	result, err := callMint(ctx, req.MintURL, oidcJWT, req)
	if err != nil {
		return nil, fmt.Errorf("calling mint service: %w", err)
	}

	return result, nil
}

type oidcTokenResponse struct {
	Value string `json:"value"`
}

func fetchOIDCJWT(ctx context.Context, audience string) (string, error) {
	requestURL := envLookup("ACTIONS_ID_TOKEN_REQUEST_URL")
	if requestURL == "" {
		return "", fmt.Errorf("ACTIONS_ID_TOKEN_REQUEST_URL not set (is this running in GitHub Actions with id-token: write?)")
	}

	requestToken := envLookup("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if requestToken == "" {
		return "", fmt.Errorf("ACTIONS_ID_TOKEN_REQUEST_TOKEN not set")
	}

	parsedOIDC, err := url.Parse(requestURL)
	if err != nil {
		return "", fmt.Errorf("parsing OIDC URL: %w", err)
	}
	q := parsedOIDC.Query()
	q.Set("audience", audience)
	parsedOIDC.RawQuery = q.Encode()
	oidcURL := parsedOIDC.String()

	var body []byte
	var statusCode int
	err = doWithRetry(ctx, 3, func() error {
		httpReq, rerr := http.NewRequestWithContext(ctx, http.MethodGet, oidcURL, nil)
		if rerr != nil {
			return fmt.Errorf("creating request: %w", rerr)
		}
		httpReq.Header.Set("Authorization", "bearer "+requestToken)

		resp, rerr := httpClient.Do(httpReq)
		if rerr != nil {
			return &retryableError{fmt.Errorf("requesting OIDC token: %w", rerr)}
		}
		defer resp.Body.Close()

		body, rerr = io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if rerr != nil {
			return fmt.Errorf("reading response: %w", rerr)
		}
		statusCode = resp.StatusCode

		if statusCode >= 500 {
			return &retryableError{fmt.Errorf("OIDC endpoint returned HTTP %d: %s", statusCode, truncateBody(body, 200))}
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if statusCode != http.StatusOK {
		excerpt := truncateBody(body, 200)
		return "", fmt.Errorf("OIDC endpoint returned HTTP %d: %s", statusCode, excerpt)
	}

	var tokenResp oidcTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}

	if tokenResp.Value == "" {
		return "", fmt.Errorf("OIDC endpoint returned empty token")
	}

	return tokenResp.Value, nil
}

type mintRequestBody struct {
	Role      string   `json:"role"`
	Level     string   `json:"level,omitempty"`
	TargetOrg string   `json:"target_org,omitempty"`
	Repos     []string `json:"repos"`
}

func callMint(ctx context.Context, mintURL, oidcJWT string, req MintRequest) (*MintResult, error) {
	reqBody := mintRequestBody{
		Role:      req.Role,
		Level:     req.Level,
		TargetOrg: req.TargetOrg,
		Repos:     req.Repos,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	mintEndpoint, err := url.JoinPath(mintURL, "/v1/token")
	if err != nil {
		return nil, fmt.Errorf("constructing mint URL: %w", err)
	}

	var body []byte
	var statusCode int
	err = doWithRetry(ctx, 5, func() error {
		httpReq, rerr := http.NewRequestWithContext(ctx, http.MethodPost, mintEndpoint, bytes.NewReader(bodyBytes))
		if rerr != nil {
			return fmt.Errorf("creating request: %w", rerr)
		}
		httpReq.Header.Set("Authorization", "Bearer "+oidcJWT)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, rerr := httpClient.Do(httpReq)
		if rerr != nil {
			return &retryableError{fmt.Errorf("requesting token: %w", rerr)}
		}
		defer resp.Body.Close()

		body, rerr = io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if rerr != nil {
			return fmt.Errorf("reading response: %w", rerr)
		}
		statusCode = resp.StatusCode

		if statusCode >= 500 {
			return &retryableError{fmt.Errorf("mint returned HTTP %d: %s", statusCode, truncateBody(body, 200))}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if statusCode != http.StatusOK {
		excerpt := truncateBody(body, 200)
		return nil, fmt.Errorf("mint returned HTTP %d: %s", statusCode, excerpt)
	}

	var result MintResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if result.Token == "" {
		return nil, fmt.Errorf("mint returned empty token")
	}

	return &result, nil
}

type retryableError struct{ error }

var retryBaseDelay = time.Second

func doWithRetry(ctx context.Context, maxAttempts int, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if _, ok := lastErr.(*retryableError); !ok {
			return lastErr
		}
		if attempt < maxAttempts-1 {
			delay := time.Duration(1<<uint(attempt)) * retryBaseDelay
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return lastErr
}

func truncateBody(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "..."
}

// StatusResult holds the response from GET /v1/status.
type StatusResult struct {
	Org               string   `json:"org,omitempty"`
	AllowedOrgs       []string `json:"allowed_orgs,omitempty"`
	Roles             []string `json:"roles,omitempty"`
	WorkflowHostRepos []string `json:"workflow_host_repos,omitempty"`
	Version           string   `json:"version,omitempty"`
	Commit            string   `json:"commit,omitempty"`
}

// StatusRequest holds the parameters for querying GET /v1/status.
type StatusRequest struct {
	MintURL  string
	Audience string // OIDC audience; defaults to the standard mint audience when empty.
}

// StatusAuthMethod describes which mechanism authenticated a
// successful /v1/status call.
type StatusAuthMethod string

const (
	// StatusAuthOIDC indicates authentication via GitHub Actions OIDC.
	StatusAuthOIDC StatusAuthMethod = "oidc"
	// StatusAuthGitHub indicates authentication via a GitHub user token
	// (GH_TOKEN, GITHUB_TOKEN, or gh auth token).
	StatusAuthGitHub StatusAuthMethod = "GitHub"
)

// hasOIDCEnv reports whether the GitHub Actions OIDC environment
// variables are present. QueryStatus uses this to decide whether to
// attempt the OIDC auth path.
func hasOIDCEnv() bool {
	return envLookup("ACTIONS_ID_TOKEN_REQUEST_URL") != "" &&
		envLookup("ACTIONS_ID_TOKEN_REQUEST_TOKEN") != ""
}

// QueryStatus calls GET /v1/status with auto-discovered authentication.
//
// Discovery order (per #5879):
//  1. GitHub Actions OIDC — attempted when ACTIONS_ID_TOKEN_REQUEST_URL
//     and ACTIONS_ID_TOKEN_REQUEST_TOKEN are set.
//  2. GitHub user token — GH_TOKEN, GITHUB_TOKEN, or the output of
//     `gh auth token`.
//
// The first mechanism that yields a 200 response wins. A 401 from the
// first mechanism triggers fallback to the next. If all mechanisms
// fail, the error lists what was attempted.
//
// resolveGitHubToken is a callback that resolves a GitHub user token.
// The CLI passes its resolveToken function; tests supply a stub.
func QueryStatus(ctx context.Context, req StatusRequest, resolveGitHubToken func() (string, error)) (*StatusResult, StatusAuthMethod, error) {
	if req.MintURL == "" {
		return nil, "", fmt.Errorf("mint URL is required")
	}
	parsed, err := url.Parse(req.MintURL)
	if err != nil {
		return nil, "", fmt.Errorf("invalid mint URL: %w", err)
	}
	isLocalhost := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"
	if parsed.Scheme != "https" && !isLocalhost {
		return nil, "", fmt.Errorf("mint URL must use HTTPS")
	}

	statusEndpoint, err := url.JoinPath(req.MintURL, "/v1/status")
	if err != nil {
		return nil, "", fmt.Errorf("constructing status URL: %w", err)
	}

	audience := req.Audience
	if audience == "" {
		audience = defaultAudience
	}

	var attempted []string

	// Attempt 1: OIDC
	if hasOIDCEnv() {
		oidcJWT, oidcErr := fetchOIDCJWT(ctx, audience)
		if oidcErr == nil {
			result, callErr := callStatus(ctx, statusEndpoint, oidcJWT)
			if callErr == nil {
				return result, StatusAuthOIDC, nil
			}
			// Only fall through on 401; other errors are terminal.
			if !isUnauthorizedErr(callErr) {
				return nil, "", fmt.Errorf("calling /v1/status with OIDC: %w", callErr)
			}
			attempted = append(attempted, "OIDC (rejected)")
		} else {
			attempted = append(attempted, fmt.Sprintf("OIDC (%s)", oidcErr))
		}
	} else {
		attempted = append(attempted, "OIDC (env vars not set)")
	}

	// Attempt 2: GitHub user token
	if resolveGitHubToken != nil {
		ghToken, ghErr := resolveGitHubToken()
		if ghErr == nil && ghToken != "" {
			result, callErr := callStatus(ctx, statusEndpoint, ghToken)
			if callErr == nil {
				return result, StatusAuthGitHub, nil
			}
			if !isUnauthorizedErr(callErr) {
				return nil, "", fmt.Errorf("calling /v1/status with GitHub token: %w", callErr)
			}
			attempted = append(attempted, "GitHub token (rejected)")
		} else {
			if ghErr != nil {
				attempted = append(attempted, fmt.Sprintf("GitHub token (%s)", ghErr))
			} else {
				attempted = append(attempted, "GitHub token (empty)")
			}
		}
	}

	return nil, "", fmt.Errorf("authentication failed for /v1/status; attempted: %s", strings.Join(attempted, ", "))
}

// errUnauthorized is returned by callStatus when the server responds
// with 401. QueryStatus uses this to decide whether to fall through
// to the next auth mechanism.
var errUnauthorized = errors.New("unauthorized")

// isUnauthorizedErr reports whether err wraps errUnauthorized.
func isUnauthorizedErr(err error) bool {
	return errors.Is(err, errUnauthorized)
}

// callStatus performs GET /v1/status with the given bearer token and
// decodes the response into a StatusResult.
func callStatus(ctx context.Context, statusURL, bearerToken string) (*StatusResult, error) {
	var body []byte
	var statusCode int
	err := doWithRetry(ctx, 3, func() error {
		httpReq, rerr := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
		if rerr != nil {
			return fmt.Errorf("creating request: %w", rerr)
		}
		httpReq.Header.Set("Authorization", "Bearer "+bearerToken)

		resp, rerr := httpClient.Do(httpReq)
		if rerr != nil {
			return &retryableError{fmt.Errorf("requesting status: %w", rerr)}
		}
		defer resp.Body.Close()

		body, rerr = io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if rerr != nil {
			return fmt.Errorf("reading response: %w", rerr)
		}
		statusCode = resp.StatusCode

		if statusCode >= 500 {
			return &retryableError{fmt.Errorf("status endpoint returned HTTP %d: %s", statusCode, truncateBody(body, 200))}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if statusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("status endpoint returned HTTP 401: %w", errUnauthorized)
	}

	if statusCode != http.StatusOK {
		excerpt := truncateBody(body, 200)
		return nil, fmt.Errorf("status endpoint returned HTTP %d: %s", statusCode, excerpt)
	}

	var result StatusResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing status response: %w", err)
	}

	return &result, nil
}
