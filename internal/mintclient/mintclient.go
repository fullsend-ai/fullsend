package mintclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var httpClient HTTPDoer = &http.Client{Timeout: 30 * time.Second}

// HTTPDoer abstracts http.Client for testability.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

const defaultAudience = "fullsend-mint"

// MintRequest holds the parameters for minting a token via the fullsend mint service.
type MintRequest struct {
	MintURL  string
	Role     string
	Repos    []string
	Audience string
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
		return nil, fmt.Errorf("at least one repo is required")
	}

	audience := req.Audience
	if audience == "" {
		audience = defaultAudience
	}

	oidcJWT, err := fetchOIDCJWT(ctx, audience)
	if err != nil {
		return nil, fmt.Errorf("fetching OIDC JWT: %w", err)
	}

	result, err := callMint(ctx, req.MintURL, oidcJWT, req.Role, req.Repos)
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
	Role  string   `json:"role"`
	Repos []string `json:"repos"`
}

func callMint(ctx context.Context, mintURL, oidcJWT, role string, repos []string) (*MintResult, error) {
	reqBody := mintRequestBody{
		Role:  role,
		Repos: repos,
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
