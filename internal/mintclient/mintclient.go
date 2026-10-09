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
)

var httpClient HTTPDoer = &http.Client{Timeout: 30 * time.Second}

// MaxMintDuration is a practical upper bound on how long a single
// MintToken call can take, informed by its retry schedule: fetchOIDCJWT
// retries within oidcRetry (up to 10 attempts with exponential backoff
// capped at 8s, and no new attempt started more than 60s after the first —
// see oidcRetry), and callMint retries up to 5 times (1s+2s+4s+8s
// backoff). Each HTTP round trip is individually bounded by httpClient's
// 30s timeout (see doWithRetryPolicy, fetchOIDCJWT, callMint).
//
// The OIDC phase therefore ends within about 60s plus one attempt (at most
// 90s even if that final attempt hangs for the full client timeout), and
// callMint adds 15s of backoff on top. The literal worst case — every
// callMint attempt also hanging for the full 30s client timeout — is far
// longer, unrealistically long for a caller to wait out during teardown.
// In practice an outage produces fast failing responses dominated by the
// backoff schedule, not attempts that each hang the full client timeout,
// so MaxMintDuration is a documented practical ceiling rather than that
// literal worst case: comfortably above the ~75s backoff-dominated
// estimate (a minute-long OIDC endpoint outage followed by a few mint
// retries), while remaining well inside a CI job's own timeout.
//
// Callers that bound MintToken with a context deadline (e.g. the
// post-script remint in internal/cli, #7231) should use at least this
// value: a shorter bound routinely cuts off retries the client itself
// would have completed.
const MaxMintDuration = 120 * time.Second

// HTTPDoer abstracts http.Client for testability.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// defaultAudience is the canonical OIDC audience for the fullsend
// token mint. Duplicated from mintconsts.OIDCAudience so this package
// does not import the nested mintcore module (mintconsts lives inside
// that module even though it has no mintcore imports of its own).
const defaultAudience = "fullsend-mint"

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
	err = doWithRetryPolicy(ctx, oidcRetry, func() error {
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
	err = doWithRetry(ctx, callMintMaxAttempts, func() error {
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

// Unwrap exposes the wrapped error to errors.Is/errors.As so callers can
// detect e.g. a context.DeadlineExceeded that occurred mid-request (wrapped
// here to mark it retryable) rather than only one that fired between
// retries (returned unwrapped by doWithRetry's ctx.Done() case below).
// Without this, a caller bounding MintToken with a context deadline can't
// distinguish a truncated retry from a genuine rejection when the deadline
// happens to land while an HTTP request is in flight.
func (e *retryableError) Unwrap() error { return e.error }

var retryBaseDelay = time.Second

// callMintMaxAttempts is how many times callMint tries the mint service
// before giving up on retryable (5xx or transport) failures.
const callMintMaxAttempts = 5

// retryPolicy bounds how long doWithRetryPolicy keeps retrying retryable
// failures. Backoff doubles from retryBaseDelay after each failed attempt.
type retryPolicy struct {
	// maxAttempts is the most attempts made, including the first.
	maxAttempts int
	// maxDelay caps a single backoff delay. Zero leaves it uncapped.
	maxDelay time.Duration
	// window, when positive, stops retrying once the next attempt would
	// start more than window after the first attempt began, so the total
	// retry time stays bounded even when each attempt is slow (e.g. an
	// upstream proxy that takes ~10s to report a connection timeout).
	window time.Duration
}

// oidcRetry is the retry policy for fetching the GitHub Actions OIDC JWT.
// The token endpoint has been seen returning 503s for about a minute at a
// time (#8276), so retries span roughly that long instead of the few
// seconds callMint's schedule gives. With fast failures this is about 10
// attempts over ~55s; with attempts that each take ~10s it is about 5
// attempts over ~65s. A variable so tests can shorten the window.
var oidcRetry = retryPolicy{
	maxAttempts: 10,
	maxDelay:    8 * time.Second,
	window:      60 * time.Second,
}

func doWithRetry(ctx context.Context, maxAttempts int, fn func() error) error {
	return doWithRetryPolicy(ctx, retryPolicy{maxAttempts: maxAttempts}, fn)
}

// doWithRetryPolicy calls fn until it succeeds, returns a non-retryable
// error, or p is exhausted. When retries run out on a retryable error, the
// returned error wraps it and states how many attempts were made.
func doWithRetryPolicy(ctx context.Context, p retryPolicy, fn func() error) error {
	start := time.Now()
	var lastErr error
	attempts := 0
	for attempts < p.maxAttempts {
		lastErr = fn()
		attempts++
		if lastErr == nil {
			return nil
		}
		if _, ok := lastErr.(*retryableError); !ok {
			return lastErr
		}
		if attempts >= p.maxAttempts {
			break
		}
		delay := time.Duration(1<<uint(attempts-1)) * retryBaseDelay
		if p.maxDelay > 0 && delay > p.maxDelay {
			delay = p.maxDelay
		}
		if p.window > 0 && time.Since(start)+delay > p.window {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return fmt.Errorf("giving up after %d attempts: %w", attempts, lastErr)
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
	StatusAuthOIDC StatusAuthMethod = "OIDC"
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
			if errors.Is(oidcErr, context.Canceled) || errors.Is(oidcErr, context.DeadlineExceeded) {
				return nil, "", fmt.Errorf("fetching OIDC JWT: %w", oidcErr)
			}
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

	return nil, "", fmt.Errorf("%w for /v1/status; attempted: %s", ErrAuthenticationFailed, strings.Join(attempted, ", "))
}

// ErrAuthenticationFailed indicates that every available authentication
// mechanism was attempted and rejected (401) by the mint's /v1/status
// endpoint. Callers can use errors.Is to distinguish this from other
// terminal QueryStatus errors (e.g. network failures, non-2xx/non-401
// responses, malformed URLs) that are not auth-related.
var ErrAuthenticationFailed = errors.New("authentication failed")

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
