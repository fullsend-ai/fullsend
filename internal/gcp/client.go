// Package gcp provides authenticated HTTP access to GCP APIs using
// Application Default Credentials. It is a shared foundation used by
// the Vertex AI inference provider and the GCF dispatch provisioner.
package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Retry parameters for transient errors in DoRequest.
const (
	// maxRetries is the number of retry attempts after the initial request.
	// Total attempts = maxRetries + 1 = 4.
	maxRetries = 3
)

// Client provides authenticated HTTP access to GCP APIs using
// Application Default Credentials.
type Client struct {
	httpClient *http.Client
	// tokenFunc is the function used to obtain access tokens.
	// It defaults to ADC but can be overridden for testing.
	tokenFunc    func(ctx context.Context) (string, error)
	QuotaProject string

	// retryDelayFn returns the backoff duration for a retry attempt.
	// Override in tests to avoid real sleeps. Defaults to
	// defaultRetryDelay (exponential backoff with jitter).
	retryDelayFn func(attempt int) time.Duration

	// adcOnce guards lazy initialization of adcTS.
	adcOnce sync.Once
	// adcTS is the cached TokenSource from FindDefaultCredentials.
	// It handles token refresh internally.
	adcTS oauth2.TokenSource
	// adcErr records any error from credential discovery so it
	// can be returned on every subsequent call.
	adcErr error
}

// NewClient creates a new Client with default settings.
func NewClient() *Client {
	c := &Client{
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: http.DefaultTransport.(*http.Transport).Clone(),
		},
		retryDelayFn: defaultRetryDelay,
	}
	c.tokenFunc = c.adcToken
	return c
}

// NewClientWithHTTP creates a Client that uses the given HTTP client and a
// static "test-token" for auth. Intended for unit tests that redirect GCP
// API calls to httptest servers.
func NewClientWithHTTP(httpClient *http.Client) *Client {
	return &Client{
		httpClient: httpClient,
		tokenFunc: func(_ context.Context) (string, error) {
			return "test-token", nil
		},
		retryDelayFn: func(_ int) time.Duration { return 0 },
	}
}

// AccessToken obtains a GCP access token using Application Default Credentials.
func (c *Client) AccessToken(ctx context.Context) (string, error) {
	return c.tokenFunc(ctx)
}

// adcToken obtains a token via Application Default Credentials.
// Credential discovery (FindDefaultCredentials) runs once; the
// returned TokenSource handles refresh internally.
func (c *Client) adcToken(ctx context.Context) (string, error) {
	c.adcOnce.Do(func() {
		// Use WithoutCancel so credential discovery is not tied to a
		// single request's lifecycle — a cancelled first-caller context
		// must not permanently poison the cached result.
		creds, err := google.FindDefaultCredentials(context.WithoutCancel(ctx), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			c.adcErr = fmt.Errorf("finding GCP credentials: %w (ensure 'gcloud auth application-default login' has been run or GOOGLE_APPLICATION_CREDENTIALS is set)", err)
			return
		}
		c.adcTS = creds.TokenSource
	})
	if c.adcErr != nil {
		return "", c.adcErr
	}
	tok, err := c.adcTS.Token()
	if err != nil {
		return "", fmt.Errorf("obtaining GCP access token: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("GCP credentials returned empty access token")
	}
	return tok.AccessToken, nil
}

// DoRequest creates and executes an authenticated HTTP request.
// It retries transient failures using exponential backoff with jitter,
// but only for idempotent HTTP methods (GET, HEAD, PUT, DELETE) to
// avoid duplicating side effects for non-idempotent requests.
//
// Retryable conditions (idempotent methods only):
//   - HTTP status codes 500, 502, 503, 504
//   - Transport errors: connection resets, timeouts, unexpected EOF
//
// Non-idempotent methods (POST, PATCH) are never retried because the
// server may have processed the request before returning the error or
// dropping the connection. For example, retrying AddSecretVersion (POST)
// after a 500 could create a duplicate secret version.
//
// HTTP 429 is not retried here; callers that need 429 retry (e.g.
// doWIFRequestWithRetry) handle it at a higher level with their own
// backoff strategy.
func (c *Client) DoRequest(ctx context.Context, method, url, body string) (*http.Response, error) {
	token, err := c.AccessToken(ctx)
	if err != nil {
		return nil, err
	}

	const totalAttempts = maxRetries + 1 // 4
	for attempt := range totalAttempts {
		var bodyReader io.Reader
		if body != "" {
			bodyReader = strings.NewReader(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.QuotaProject != "" {
			req.Header.Set("x-goog-user-project", c.QuotaProject)
		}

		resp, doErr := c.httpClient.Do(req)

		// Transport-level error (connection reset, timeout, EOF, etc.).
		// Only retry idempotent methods — for non-idempotent requests the
		// server may have processed the request before the connection was
		// lost, and retrying could duplicate side effects (e.g. creating
		// an extra secret version via AddSecretVersion).
		if doErr != nil {
			if attempt < maxRetries && isIdempotentMethod(method) && isRetryableTransportError(ctx, doErr) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(c.getRetryDelay(attempt)):
				}
				continue
			}
			return nil, doErr
		}

		// Retryable HTTP status code (500, 502, 503, 504).
		// Like transport errors, only retry idempotent methods to avoid
		// duplicating side effects (e.g. AddSecretVersion POST → 500
		// after the version was created would create a duplicate on retry).
		if attempt < maxRetries && isIdempotentMethod(method) && isRetryableStatusCode(resp.StatusCode) {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.getRetryDelay(attempt)):
			}
			continue
		}

		return resp, nil
	}

	// Unreachable: the final iteration always returns above.
	return nil, fmt.Errorf("exhausted %d retry attempts", maxRetries)
}

// getRetryDelay returns the backoff delay for the given attempt number.
func (c *Client) getRetryDelay(attempt int) time.Duration {
	if c.retryDelayFn != nil {
		return c.retryDelayFn(attempt)
	}
	return defaultRetryDelay(attempt)
}

// defaultRetryDelay returns the backoff duration for a retry attempt.
// Uses exponential backoff with jitter: 1s base, doubling each
// attempt, capped at 10s. Jitter randomises 50-100% of the computed
// delay to desynchronise concurrent callers.
func defaultRetryDelay(attempt int) time.Duration {
	const (
		baseDelay = 1 * time.Second
		maxDelay  = 10 * time.Second
	)
	delay := min(baseDelay<<uint(attempt), maxDelay) // 1s, 2s, 4s, 10s
	half := delay / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// isRetryableTransportError reports whether a client.Do error is
// transient and worth retrying. It identifies TCP connection resets,
// TLS handshake timeouts, unexpected connection closures, and network
// timeouts.
//
// We check ctx.Err() rather than errors.Is(err, context.DeadlineExceeded)
// because http.Client.Timeout wraps context.DeadlineExceeded from an
// internal context even when the caller's context is still active. Such
// per-request timeouts on slow servers are transient and worth retrying.
// See internal/fetch/fetch.go isTransientRequestError for the same
// rationale.
func isRetryableTransportError(ctx context.Context, err error) bool {
	// If the caller's context is done, this is intentional
	// cancellation — not a transient failure.
	if ctx.Err() != nil {
		return false
	}
	// HTTP client timeout (e.g. net/http.Client.Timeout exceeded)
	// and i/o timeouts — both surface via the Timeout() interface.
	var te interface{ Timeout() bool }
	if errors.As(err, &te) && te.Timeout() {
		return true
	}
	// Unexpected connection closure — the server dropped the
	// connection before a full response was read.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// Transient connection errors. Use typed syscall sentinels
	// instead of string matching — these are stable across
	// platforms (Go maps OS-specific codes to syscall.Errno on
	// all supported targets).
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	return false
}

// isIdempotentMethod reports whether an HTTP method is idempotent —
// safe to retry without risk of duplicating side effects. Follows the
// same convention as internal/forge/gitlab and internal/forge/jira.
func isIdempotentMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead ||
		method == http.MethodPut || method == http.MethodDelete
}

// isRetryableStatusCode reports whether an HTTP status code represents
// a transient server-side failure that may succeed on retry.
//
// HTTP 429 (Too Many Requests) is intentionally excluded because the
// GCF provisioner's doWIFRequestWithRetry already handles 429 with
// its own backoff strategy. Retrying 429 here would cause double-retry
// for WIF provider operations.
func isRetryableStatusCode(code int) bool {
	switch code {
	case http.StatusInternalServerError, // 500
		http.StatusBadGateway,         // 502
		http.StatusServiceUnavailable, // 503
		http.StatusGatewayTimeout:     // 504
		return true
	}
	return false
}

// ExtractErrorMessage parses a GCP API error response and returns only
// the error message, avoiding leakage of sensitive metadata.
func ExtractErrorMessage(body []byte) string {
	var gcpErr struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &gcpErr) == nil && gcpErr.Error.Message != "" {
		return gcpErr.Error.Message
	}
	return "(error details unavailable)"
}
