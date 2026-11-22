// Package forgejo is the Forgejo API adapter. It implements the
// repository-content operations that the repos lifecycle needs for
// repositories declared under forgejo.instances in repos.yaml, using the
// Forgejo v1 REST API (Forgejo 17 and later).
//
// The method signatures match the corresponding forge.Client methods so
// callers can depend on the narrow subset they need; operations Forgejo
// support does not yet cover are not part of this adapter.
package forgejo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// maxResponseBytes bounds every API response body the client reads.
const maxResponseBytes = 10 << 20

// Client is a Forgejo API client for one instance. It is safe for
// concurrent use.
type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

// New returns a client for the Forgejo instance at baseURL. baseURL may
// include a path prefix (https://example.com/forgejo), which is preserved
// in every API request. token is sent as the API credential; it is never
// included in returned errors.
func New(baseURL, token string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing forgejo base URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("forgejo base URL must use https or http, got scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("forgejo base URL has no host")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{baseURL: u, token: token, httpClient: httpClient}, nil
}

// contentsResponse is the subset of Forgejo's ContentsResponse the client
// reads.
type contentsResponse struct {
	Type     string `json:"type"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
	SHA      string `json:"sha"`
}

// contentsURL returns the API URL of path in owner/repo. Each segment is
// escaped so a repository path cannot address another API route.
func (c *Client) contentsURL(owner, repo, filePath string) string {
	segs := strings.Split(strings.Trim(filePath, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return c.baseURL.String() + "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) +
		"/contents/" + strings.Join(segs, "/")
}

// do sends an API request and decodes a JSON response into out when out
// is non-nil. A 404 maps to forge.ErrNotFound.
func (c *Client) do(ctx context.Context, method, rawURL string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, redactURL(req.URL), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%s %s: reading response: %w", method, redactURL(req.URL), err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s %s: %w", method, redactURL(req.URL), forge.ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Method: method, URL: redactURL(req.URL), StatusCode: resp.StatusCode, Message: apiMessage(data)}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: decoding response: %w", method, redactURL(req.URL), err)
		}
	}
	return nil
}

// APIError is a non-2xx, non-404 Forgejo API response.
type APIError struct {
	Method     string
	URL        string
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s %s: Forgejo API returned HTTP %d", e.Method, e.URL, e.StatusCode)
	}
	return fmt.Sprintf("%s %s: Forgejo API returned HTTP %d: %s", e.Method, e.URL, e.StatusCode, e.Message)
}

// apiMessage extracts Forgejo's error message, bounded so an unexpected
// body cannot flood the terminal.
func apiMessage(data []byte) string {
	var body struct {
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(data, &body) == nil {
		msg = body.Message
	}
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return msg
}

// redactURL drops user info and query parameters from u for error messages.
func redactURL(u *url.URL) string {
	c := *u
	c.User = nil
	c.RawQuery = ""
	return c.String()
}

// getContents reads path on the default branch.
func (c *Client) getContents(ctx context.Context, owner, repo, filePath string) (*contentsResponse, error) {
	var out contentsResponse
	if err := c.do(ctx, http.MethodGet, c.contentsURL(owner, repo, filePath), nil, &out); err != nil {
		return nil, err
	}
	if out.Type != "file" {
		return nil, fmt.Errorf("%s in %s/%s is a %s, not a file", filePath, owner, repo, out.Type)
	}
	return &out, nil
}

// GetFileContent returns the content of path on the default branch, or an
// error wrapping forge.ErrNotFound when it does not exist.
func (c *Client) GetFileContent(ctx context.Context, owner, repo, filePath string) ([]byte, error) {
	out, err := c.getContents(ctx, owner, repo, filePath)
	if err != nil {
		return nil, err
	}
	if out.Encoding != "base64" {
		return nil, fmt.Errorf("%s in %s/%s: unsupported content encoding %q", filePath, owner, repo, out.Encoding)
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("%s in %s/%s: decoding content: %w", filePath, owner, repo, err)
	}
	return data, nil
}

// CreateOrUpdateFile writes content to path on the default branch,
// creating the file or replacing it when it exists.
func (c *Client) CreateOrUpdateFile(ctx context.Context, owner, repo, filePath, message string, content []byte) error {
	body := map[string]string{
		"content": base64.StdEncoding.EncodeToString(content),
		"message": message,
	}
	existing, err := c.getContents(ctx, owner, repo, filePath)
	switch {
	case err == nil:
		body["sha"] = existing.SHA
		return c.do(ctx, http.MethodPut, c.contentsURL(owner, repo, filePath), body, nil)
	case forge.IsNotFound(err):
		return c.do(ctx, http.MethodPost, c.contentsURL(owner, repo, filePath), body, nil)
	default:
		return err
	}
}

// DeleteFile removes path from the default branch. It returns an error
// wrapping forge.ErrNotFound when the file does not exist.
func (c *Client) DeleteFile(ctx context.Context, owner, repo, filePath, message string) error {
	existing, err := c.getContents(ctx, owner, repo, filePath)
	if err != nil {
		return err
	}
	body := map[string]string{"message": message, "sha": existing.SHA}
	return c.do(ctx, http.MethodDelete, c.contentsURL(owner, repo, filePath), body, nil)
}
