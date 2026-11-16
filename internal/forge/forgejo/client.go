// Package forgejo implements forge.Client for the Forgejo / Gitea API v1.
//
// ForgeOps FACTORY-FULLSEND-FORGEJO-CLIENT-001: MVP client for git.forge.internal.
// Methods not yet mapped return forge.ErrNotSupported (see stubs.go).
package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Compile-time interface check.
var _ forge.Client = (*LiveClient)(nil)

// LiveClient talks to a Forgejo (or Gitea-compatible) instance.
type LiveClient struct {
	http    *http.Client
	token   string
	baseURL string // e.g. https://git.forge.internal/api/v1
}

// Option configures LiveClient.
type Option func(*LiveClient)

// WithBaseURL sets the API root (must include /api/v1 or will be appended).
func WithBaseURL(raw string) Option {
	return func(c *LiveClient) {
		c.baseURL = normalizeAPIBase(raw)
	}
}

func normalizeAPIBase(raw string) string {
	raw = strings.TrimRight(raw, "/")
	if strings.HasSuffix(raw, "/api/v1") {
		return raw
	}
	return raw + "/api/v1"
}

// New returns a Forgejo client. baseURL defaults to https://git.forge.internal.
func New(token string, opts ...Option) *LiveClient {
	c := &LiveClient{
		http:    &http.Client{Timeout: 60 * time.Second},
		token:   token,
		baseURL: normalizeAPIBase("https://git.forge.internal"),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// BaseURL returns the configured API base.
func (c *LiveClient) BaseURL() string { return c.baseURL }

func (c *LiveClient) do(ctx context.Context, method, path string, in any, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	u := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return res.StatusCode, err
	}
	if res.StatusCode == http.StatusNotFound {
		return res.StatusCode, fmt.Errorf("%s %s: %w", method, path, forge.ErrNotFound)
	}
	if res.StatusCode == http.StatusConflict {
		return res.StatusCode, fmt.Errorf("%s %s: %w", method, path, forge.ErrConflict)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("%s %s: HTTP %d: %s", method, path, res.StatusCode, truncate(string(data), 300))
	}
	if out != nil && len(data) > 0 && res.StatusCode != http.StatusNoContent {
		if err := json.Unmarshal(data, out); err != nil {
			return res.StatusCode, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return res.StatusCode, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func escapePath(parts ...string) string {
	enc := make([]string, len(parts))
	for i, p := range parts {
		enc[i] = url.PathEscape(p)
	}
	return strings.Join(enc, "/")
}

type apiUser struct {
	ID       int64  `json:"id"`
	Login    string `json:"login"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
}

type apiRepo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	Fork          bool   `json:"fork"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	CloneURL string `json:"clone_url"`
	HTMLURL  string `json:"html_url"`
}

// GetAuthenticatedUser implements forge.Client.
func (c *LiveClient) GetAuthenticatedUser(ctx context.Context) (string, error) {
	var u apiUser
	if _, err := c.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

// GetTokenScopes implements forge.Client (Forgejo does not expose classic GitHub scopes).
func (c *LiveClient) GetTokenScopes(ctx context.Context) ([]string, error) {
	// Probe auth works; scopes are not enumerated on Forgejo the same way.
	if _, err := c.GetAuthenticatedUser(ctx); err != nil {
		return nil, err
	}
	return []string{"all"}, nil
}

// ListOrgRepos implements forge.Client.
func (c *LiveClient) ListOrgRepos(ctx context.Context, org string, includePrivate bool) ([]forge.Repository, error) {
	var raw []apiRepo
	path := "/orgs/" + url.PathEscape(org) + "/repos?limit=100"
	if _, err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]forge.Repository, 0, len(raw))
	for _, r := range raw {
		if r.Archived || r.Fork {
			continue
		}
		if !includePrivate && r.Private {
			continue
		}
		out = append(out, mapRepo(r))
	}
	return out, nil
}

// GetRepo implements forge.Client.
func (c *LiveClient) GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error) {
	var r apiRepo
	path := "/repos/" + escapePath(owner, repo)
	if _, err := c.do(ctx, http.MethodGet, path, nil, &r); err != nil {
		return nil, err
	}
	m := mapRepo(r)
	return &m, nil
}

// CreateRepo implements forge.Client.
func (c *LiveClient) CreateRepo(ctx context.Context, org, name, description string, private bool) (*forge.Repository, error) {
	payload := map[string]any{
		"name":        name,
		"description": description,
		"private":     private,
		"auto_init":   true,
	}
	var r apiRepo
	path := "/orgs/" + url.PathEscape(org) + "/repos"
	if _, err := c.do(ctx, http.MethodPost, path, payload, &r); err != nil {
		return nil, err
	}
	m := mapRepo(r)
	return &m, nil
}

// UpdateRepoVisibility implements forge.Client.
func (c *LiveClient) UpdateRepoVisibility(ctx context.Context, owner, repo string, private bool) error {
	payload := map[string]any{"private": private}
	_, err := c.do(ctx, http.MethodPatch, "/repos/"+escapePath(owner, repo), payload, nil)
	return err
}

// DeleteRepo implements forge.Client.
func (c *LiveClient) DeleteRepo(ctx context.Context, owner, repo string) error {
	_, err := c.do(ctx, http.MethodDelete, "/repos/"+escapePath(owner, repo), nil, nil)
	return err
}
