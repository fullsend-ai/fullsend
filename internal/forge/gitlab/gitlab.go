// Package gitlab implements forge.Client for the GitLab REST API v4.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// LiveClient implements forge.Client for the GitLab REST API v4.
type LiveClient struct {
	http    *http.Client
	token   string
	baseURL string
}

// Compile-time interface check.
var _ forge.Client = (*LiveClient)(nil)

// Option configures the GitLab client.
type Option func(*LiveClient)

// WithBaseURL sets a custom base URL for self-hosted GitLab instances.
// Non-https schemes are only allowed for loopback addresses (localhost,
// 127.0.0.1) to support test servers; other http:// URLs are rejected
// to prevent sending the PRIVATE-TOKEN header in cleartext.
func WithBaseURL(rawURL string) Option {
	return func(c *LiveClient) {
		c.baseURL = strings.TrimRight(rawURL, "/")
	}
}

// validateBaseURL checks that the base URL uses https, unless it points to a
// loopback address (for httptest servers).
func validateBaseURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid base URL: %w", err)
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return fmt.Errorf("base URL %q uses insecure scheme %q; only https is allowed for non-loopback hosts", rawURL, u.Scheme)
}

// New creates a new GitLab client with the given project access token.
// Returns an error if the configured base URL uses an insecure scheme
// for a non-loopback host.
func New(token string, opts ...Option) (*LiveClient, error) {
	if token == "" {
		return nil, fmt.Errorf("gitlab: token must not be empty")
	}
	c := &LiveClient{
		http: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				if len(via) > 0 {
					crossOrigin := req.URL.Host != via[0].URL.Host
					tlsDowngrade := via[0].URL.Scheme == "https" && req.URL.Scheme != "https"
					if crossOrigin || tlsDowngrade {
						req.Header.Del("PRIVATE-TOKEN")
					}
				}
				return nil
			},
		},
		token:   token,
		baseURL: "https://gitlab.com",
	}
	for _, o := range opts {
		o(c)
	}
	if err := validateBaseURL(c.baseURL); err != nil {
		return nil, err
	}
	return c, nil
}

// APIError represents an error response from the GitLab API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gitlab api: %d %s", e.StatusCode, e.Message)
}

func (e *APIError) Unwrap() error {
	if e.StatusCode == http.StatusNotFound {
		return forge.ErrNotFound
	}
	if e.StatusCode == http.StatusConflict {
		return forge.ErrAlreadyExists
	}
	if e.StatusCode == http.StatusForbidden {
		return forge.ErrForbidden
	}
	return nil
}

const maxRetries = 5

func (c *LiveClient) apiURL(path string) string {
	return c.baseURL + "/api/v4" + path
}

// projectPath URL-encodes a "owner/repo" or nested group path for the
// GitLab API, which expects namespace/project as a URL-encoded slug.
func projectPath(owner, repo string) string {
	return url.PathEscape(owner + "/" + repo)
}

func (c *LiveClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	reqURL := c.apiURL(path)

	var bodyData []byte
	if body != nil {
		var err error
		bodyData, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
	}

	for attempt := range maxRetries {
		var reqBody io.Reader
		if bodyData != nil {
			reqBody = bytes.NewReader(bodyData)
		}

		req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("PRIVATE-TOKEN", c.token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if isTransientError(err) && isIdempotent(method) && attempt < maxRetries-1 {
				delay := retryDelay(nil, attempt)
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}
			return nil, fmt.Errorf("http %s %s: %w", method, path, err)
		}

		if isRetryable(resp, method) {
			resp.Body.Close()
			delay := retryDelay(resp, attempt)
			if attempt == maxRetries-1 {
				return nil, &APIError{
					StatusCode: resp.StatusCode,
					Message:    fmt.Sprintf("retryable error after %d attempts on %s %s", maxRetries, method, path),
				}
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("exhausted retries for %s %s", method, path)
}

func isRetryable(resp *http.Response, method string) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if resp.StatusCode >= 500 && resp.StatusCode <= 504 && isIdempotent(method) {
		return true
	}
	return false
}

func isTransientError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return false
}

func isIdempotent(method string) bool {
	return method == http.MethodGet || method == http.MethodHead ||
		method == http.MethodPut || method == http.MethodDelete
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
	const maxRetryAfterSecs = 300
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
				if secs > maxRetryAfterSecs {
					secs = maxRetryAfterSecs
				}
				return time.Duration(secs) * time.Second
			}
		}
	}
	base := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	half := base / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

func checkStatus(resp *http.Response, acceptable ...int) error {
	for _, code := range acceptable {
		if resp.StatusCode == code {
			return nil
		}
	}

	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	var errResp struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(data, &errResp) == nil {
		msg := extractMessage(errResp.Message, errResp.Error)
		if msg != "" {
			return &APIError{StatusCode: resp.StatusCode, Message: msg}
		}
	}
	return &APIError{StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
}

// extractMessage handles GitLab's inconsistent error format — "message"
// can be a string, a map, or an array depending on the endpoint.
func extractMessage(message any, fallback string) string {
	switch v := message.(type) {
	case string:
		if v != "" {
			return v
		}
	case map[string]any:
		parts := make([]string, 0, len(v))
		for k, val := range v {
			parts = append(parts, fmt.Sprintf("%s: %v", k, val))
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	case []any:
		parts := make([]string, 0, len(v))
		for _, val := range v {
			parts = append(parts, fmt.Sprintf("%v", val))
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	return fallback
}

// extractConflictMessage extracts a human-readable message from a 409 response
// body. GitLab returns JSON with a "message" field; if parsing fails, the raw
// body is returned as-is.
func extractConflictMessage(data []byte) string {
	var errResp struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(data, &errResp) == nil {
		if msg := extractMessage(errResp.Message, errResp.Error); msg != "" {
			return msg
		}
	}
	return string(data)
}

func (c *LiveClient) get(ctx context.Context, path string) (*http.Response, error) {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *LiveClient) post(ctx context.Context, path string, body any) (*http.Response, error) {
	resp, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp, http.StatusOK, http.StatusCreated); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *LiveClient) put(ctx context.Context, path string, body any) (*http.Response, error) {
	resp, err := c.do(ctx, http.MethodPut, path, body)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp, http.StatusOK, http.StatusCreated, http.StatusNoContent); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *LiveClient) delete_(ctx context.Context, path string) error {
	resp, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusOK, http.StatusAccepted, http.StatusNoContent)
}

const maxResponseBody = 10 << 20 // 10 MB

func decodeJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(v)
}
