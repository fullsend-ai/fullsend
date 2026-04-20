// Package github implements forge.Client for the GitHub REST API.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"golang.org/x/crypto/nacl/box"
)

// LiveClient implements forge.Client for the GitHub REST API.
type LiveClient struct {
	http    *http.Client
	token   string
	baseURL string
}

// Compile-time interface check.
var _ forge.Client = (*LiveClient)(nil)

// New creates a new GitHub client with the given personal access token.
func New(token string) *LiveClient {
	return &LiveClient{
		http:    &http.Client{Timeout: 30 * time.Second},
		token:   token,
		baseURL: "https://api.github.com",
	}
}

// WithBaseURL sets a custom base URL (for testing with httptest).
func (c *LiveClient) WithBaseURL(url string) *LiveClient {
	c.baseURL = strings.TrimRight(url, "/")
	return c
}

// APIError represents an error response from the GitHub API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github api: %d %s", e.StatusCode, e.Message)
}

// Unwrap returns forge.ErrNotFound for 404 errors, enabling errors.Is checks.
func (e *APIError) Unwrap() error {
	if e.StatusCode == http.StatusNotFound {
		return forge.ErrNotFound
	}
	return nil
}

const maxRetries = 3

// do performs an HTTP request against the GitHub API with retry on rate limits.
func (c *LiveClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	url := c.baseURL + path

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

		req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("http %s %s: %w", method, path, err)
		}

		if !isRetryable(resp) {
			return resp, nil
		}

		// Drain and close the body before retrying.
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if attempt == maxRetries-1 {
			return nil, &APIError{StatusCode: resp.StatusCode, Message: "rate limited after retries"}
		}

		delay := retryDelay(resp, attempt)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Unreachable, but the compiler needs it.
	return nil, fmt.Errorf("exhausted retries for %s %s", method, path)
}

// isRetryable returns true for responses that should trigger a retry.
// GitHub uses 429 for primary rate limits and 403 with Retry-After for
// secondary rate limits. A plain 403 (e.g., permission denied) is not retried.
func isRetryable(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	// GitHub secondary rate limit: 403 + Retry-After header.
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("Retry-After") != "" {
		return true
	}
	return false
}

// retryDelay calculates how long to wait before retrying.
// It uses the Retry-After header if present, otherwise exponential backoff.
func retryDelay(resp *http.Response, attempt int) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	// Exponential backoff: 1s, 2s, 4s
	return time.Duration(math.Pow(2, float64(attempt))) * time.Second
}

// checkStatus verifies the response has an acceptable status code and returns
// an APIError if not.
func checkStatus(resp *http.Response, acceptable ...int) error {
	for _, code := range acceptable {
		if resp.StatusCode == code {
			return nil
		}
	}

	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var msg struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &msg) == nil && msg.Message != "" {
		return &APIError{StatusCode: resp.StatusCode, Message: msg.Message}
	}
	return &APIError{StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
}

// get performs a GET request and checks for success.
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

// post performs a POST request and checks for success.
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

// put performs a PUT request and checks for success.
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

// patch performs a PATCH request and checks for success.
func (c *LiveClient) patch(ctx context.Context, path string, body any) (*http.Response, error) {
	resp, err := c.do(ctx, http.MethodPatch, path, body)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp, http.StatusOK, http.StatusNoContent); err != nil {
		return nil, err
	}
	return resp, nil
}

// delete_ performs a DELETE request and checks for success.
func (c *LiveClient) delete_(ctx context.Context, path string) error {
	resp, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusNoContent, http.StatusOK)
}

// decodeJSON reads the response body and decodes it into v.
func decodeJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// ListOrgRepos returns all non-archived, non-fork repositories for an org.
func (c *LiveClient) ListOrgRepos(ctx context.Context, org string) ([]forge.Repository, error) {
	var result []forge.Repository

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/orgs/%s/repos?per_page=100&page=%d&type=all", org, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list org repos page %d: %w", page, err)
		}

		var repos []struct {
			ID            int64  `json:"id"`
			Name          string `json:"name"`
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
			Private       bool   `json:"private"`
			Archived      bool   `json:"archived"`
			Fork          bool   `json:"fork"`
		}
		if err := decodeJSON(resp, &repos); err != nil {
			return nil, fmt.Errorf("decode org repos page %d: %w", page, err)
		}

		for _, r := range repos {
			if r.Archived || r.Fork {
				continue
			}
			result = append(result, forge.Repository{
				ID:            r.ID,
				Name:          r.Name,
				FullName:      r.FullName,
				DefaultBranch: r.DefaultBranch,
				Private:       r.Private,
				Archived:      r.Archived,
				Fork:          r.Fork,
			})
		}

		if len(repos) < 100 {
			break
		}
	}

	return result, nil
}

// CreateRepo creates a new repository under an organization.
//
// The repo is created with auto_init: true so that a default branch exists
// immediately. However, GitHub's auto_init is asynchronous — the API returns
// 201 before the initial commit is fully materialized. Callers writing files
// to the new repo via the Contents API should expect transient 404s and
// retry with backoff. See the retry logic in LiveClient.do().
func (c *LiveClient) CreateRepo(ctx context.Context, org, name, description string, private bool) (*forge.Repository, error) {
	payload := map[string]any{
		"name":        name,
		"description": description,
		"private":     private,
		"auto_init":   true,
	}

	resp, err := c.post(ctx, fmt.Sprintf("/orgs/%s/repos", org), payload)
	if err != nil {
		return nil, fmt.Errorf("create repo: %w", err)
	}

	var repo struct {
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
	}
	if err := decodeJSON(resp, &repo); err != nil {
		return nil, fmt.Errorf("decode create repo response: %w", err)
	}

	return &forge.Repository{
		Name:          repo.Name,
		FullName:      repo.FullName,
		DefaultBranch: repo.DefaultBranch,
		Private:       repo.Private,
	}, nil
}

// GetRepo retrieves a single repository by owner and name.
// Returns forge.ErrNotFound (wrapped) if the repo does not exist.
func (c *LiveClient) GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s", owner, repo), nil)
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("get repo %s/%s: %w", owner, repo, err)
	}

	var r struct {
		ID            int64  `json:"id"`
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
		Archived      bool   `json:"archived"`
		Fork          bool   `json:"fork"`
	}
	if err := decodeJSON(resp, &r); err != nil {
		return nil, fmt.Errorf("decode repo: %w", err)
	}

	return &forge.Repository{
		ID:            r.ID,
		Name:          r.Name,
		FullName:      r.FullName,
		DefaultBranch: r.DefaultBranch,
		Private:       r.Private,
		Archived:      r.Archived,
		Fork:          r.Fork,
	}, nil
}

// DeleteRepo deletes a repository.
func (c *LiveClient) DeleteRepo(ctx context.Context, owner, repo string) error {
	return c.delete_(ctx, fmt.Sprintf("/repos/%s/%s", owner, repo))
}

// CreateFile creates a new file on the repository's default branch.
func (c *LiveClient) CreateFile(ctx context.Context, owner, repo, path, message string, content []byte) error {
	return c.CreateFileOnBranch(ctx, owner, repo, "", path, message, content)
}

// CreateFileOnBranch creates a file on a specific branch (or default if empty).
//
// Retries on 404 to handle GitHub's async repo initialization: after
// CreateRepo with auto_init, the default branch may not be materialized
// yet and the Contents API returns 404. Also retries on 409 (conflict)
// which can occur when the branch ref is being updated by a concurrent write.
//
// GitHub quirk: writing to .github/workflows/ paths returns 404 (not 403)
// when the token lacks the "workflow" scope. If you hit persistent 404s
// on workflow file creation, the fix is: gh auth refresh -s workflow
func (c *LiveClient) CreateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error {
	payload := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
	}
	if branch != "" {
		payload["branch"] = branch
	}

	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, path)
	return c.putFileWithRetry(ctx, apiPath, payload, path)
}

// CreateOrUpdateFile creates a file or updates it if it already exists.
// Retries on 404/409 to handle async repo initialization and branch ref races.
func (c *LiveClient) CreateOrUpdateFile(ctx context.Context, owner, repo, path, message string, content []byte) error {
	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, path)

	return c.retryOnTransient(ctx, path, func() error {
		// Try to get existing file for its SHA.
		existingResp, err := c.do(ctx, http.MethodGet, apiPath, nil)
		if err != nil {
			return fmt.Errorf("check existing file: %w", err)
		}

		payload := map[string]any{
			"message": message,
			"content": base64.StdEncoding.EncodeToString(content),
		}

		if existingResp.StatusCode == http.StatusOK {
			var existing struct {
				SHA string `json:"sha"`
			}
			if err := decodeJSON(existingResp, &existing); err != nil {
				return fmt.Errorf("decode existing file: %w", err)
			}
			payload["sha"] = existing.SHA
		} else {
			existingResp.Body.Close()
		}

		resp, err := c.put(ctx, apiPath, payload)
		if err != nil {
			return fmt.Errorf("create or update file %s: %w", path, err)
		}
		resp.Body.Close()
		return nil
	})
}

// CreateOrUpdateFileOnBranch creates or updates a file on a specific branch.
// Like CreateOrUpdateFile, it fetches the existing SHA before updating.
// Retries on 404/409 for async repo init and branch ref races.
func (c *LiveClient) CreateOrUpdateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error {
	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, path)

	return c.retryOnTransient(ctx, path, func() error {
		// Try to get existing file on the branch for its SHA.
		existingResp, err := c.do(ctx, http.MethodGet, apiPath+"?ref="+branch, nil)
		if err != nil {
			return fmt.Errorf("check existing file on branch: %w", err)
		}

		payload := map[string]any{
			"message": message,
			"content": base64.StdEncoding.EncodeToString(content),
			"branch":  branch,
		}

		if existingResp.StatusCode == http.StatusOK {
			var existing struct {
				SHA string `json:"sha"`
			}
			if err := decodeJSON(existingResp, &existing); err != nil {
				return fmt.Errorf("decode existing file: %w", err)
			}
			payload["sha"] = existing.SHA
		} else {
			existingResp.Body.Close()
		}

		resp, err := c.put(ctx, apiPath, payload)
		if err != nil {
			return fmt.Errorf("create or update file %s on branch %s: %w", path, branch, err)
		}
		resp.Body.Close()
		return nil
	})
}

// putFileWithRetry wraps a single PUT to the Contents API with retry on
// transient errors (404 from async repo init, 409 from branch ref races).
func (c *LiveClient) putFileWithRetry(ctx context.Context, apiPath string, payload map[string]any, path string) error {
	return c.retryOnTransient(ctx, path, func() error {
		resp, err := c.put(ctx, apiPath, payload)
		if err != nil {
			return fmt.Errorf("create file %s: %w", path, err)
		}
		resp.Body.Close()
		return nil
	})
}

// retryOnTransient retries an operation that may fail with 404 or 409 due to
// GitHub's async repo initialization or branch ref update races. It uses
// linear backoff (2s between attempts) and up to 5 attempts (~10s total).
func (c *LiveClient) retryOnTransient(ctx context.Context, label string, fn func() error) error {
	const attempts = 5
	const delay = 2 * time.Second

	var lastErr error
	for i := range attempts {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		// Only retry on 404 (repo not ready) or 409 (branch ref conflict).
		var apiErr *APIError
		if !errors.As(lastErr, &apiErr) || (apiErr.StatusCode != 404 && apiErr.StatusCode != 409) {
			return lastErr
		}

		if i < attempts-1 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return fmt.Errorf("%s: %w (after %d attempts)", label, lastErr, attempts)
}

// GetFileContent retrieves the content of a file from a repository.
func (c *LiveClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, path))
	if err != nil {
		return nil, fmt.Errorf("get file content: %w", err)
	}

	var file struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(resp, &file); err != nil {
		return nil, fmt.Errorf("decode file content: %w", err)
	}

	// GitHub's Contents API returns base64 with MIME-style line wrapping.
	cleaned := strings.ReplaceAll(strings.ReplaceAll(file.Content, "\n", ""), "\r", "")
	data, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("decode base64 content: %w", err)
	}
	return data, nil
}

// DeleteFile deletes a file from a repository. Idempotent: returns nil if
// the file does not exist.
func (c *LiveClient) DeleteFile(ctx context.Context, owner, repo, path, message string) error {
	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, path)

	// Get current SHA (required by the Contents API for deletes).
	existingResp, err := c.do(ctx, http.MethodGet, apiPath, nil)
	if err != nil {
		return fmt.Errorf("check existing file for delete: %w", err)
	}
	if existingResp.StatusCode == http.StatusNotFound {
		existingResp.Body.Close()
		return nil // already gone
	}
	if err := checkStatus(existingResp, http.StatusOK); err != nil {
		return fmt.Errorf("get file for delete: %w", err)
	}

	var existing struct {
		SHA string `json:"sha"`
	}
	if err := decodeJSON(existingResp, &existing); err != nil {
		return fmt.Errorf("decode existing file for delete: %w", err)
	}

	payload := map[string]any{
		"message": message,
		"sha":     existing.SHA,
	}
	resp, err := c.do(ctx, http.MethodDelete, apiPath, payload)
	if err != nil {
		return fmt.Errorf("delete file %s: %w", path, err)
	}
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return fmt.Errorf("delete file %s: %w", path, err)
	}
	resp.Body.Close()
	return nil
}

// CreateBranch creates a new branch from the repository's default branch.
func (c *LiveClient) CreateBranch(ctx context.Context, owner, repo, branchName string) error {
	// Step 1: Get the default branch name.
	repoResp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s", owner, repo))
	if err != nil {
		return fmt.Errorf("get repo for default branch: %w", err)
	}
	var repoInfo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := decodeJSON(repoResp, &repoInfo); err != nil {
		return fmt.Errorf("decode repo info: %w", err)
	}

	// Step 2: Get the SHA of the default branch.
	refResp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, repoInfo.DefaultBranch))
	if err != nil {
		return fmt.Errorf("get ref for default branch: %w", err)
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := decodeJSON(refResp, &ref); err != nil {
		return fmt.Errorf("decode ref: %w", err)
	}

	// Step 3: Create the new branch ref.
	payload := map[string]string{
		"ref": "refs/heads/" + branchName,
		"sha": ref.Object.SHA,
	}
	resp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo), payload)
	if err != nil {
		return fmt.Errorf("create branch %s: %w", branchName, err)
	}
	resp.Body.Close()
	return nil
}

// CreateChangeProposal creates a pull request.
func (c *LiveClient) CreateChangeProposal(ctx context.Context, owner, repo, title, body, head, base string) (*forge.ChangeProposal, error) {
	payload := map[string]string{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}

	resp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/pulls", owner, repo), payload)
	if err != nil {
		return nil, fmt.Errorf("create pull request: %w", err)
	}

	var pr struct {
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Number  int    `json:"number"`
	}
	if err := decodeJSON(resp, &pr); err != nil {
		return nil, fmt.Errorf("decode pull request: %w", err)
	}

	return &forge.ChangeProposal{
		URL:    pr.HTMLURL,
		Title:  pr.Title,
		Number: pr.Number,
	}, nil
}

// ListRepoPullRequests lists open pull requests for a repository with pagination.
func (c *LiveClient) ListRepoPullRequests(ctx context.Context, owner, repo string) ([]forge.ChangeProposal, error) {
	var result []forge.ChangeProposal

	for page := 1; page <= 100; page++ {
		resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=100&page=%d", owner, repo, page))
		if err != nil {
			return nil, fmt.Errorf("list pull requests page %d: %w", page, err)
		}

		var prs []struct {
			HTMLURL string `json:"html_url"`
			Title   string `json:"title"`
			Number  int    `json:"number"`
		}
		if err := decodeJSON(resp, &prs); err != nil {
			return nil, fmt.Errorf("decode pull requests page %d: %w", page, err)
		}

		for _, pr := range prs {
			result = append(result, forge.ChangeProposal{
				URL:    pr.HTMLURL,
				Title:  pr.Title,
				Number: pr.Number,
			})
		}

		if len(prs) < 100 {
			break
		}
	}

	return result, nil
}

// GetAuthenticatedUser returns the login of the authenticated user.
func (c *LiveClient) GetAuthenticatedUser(ctx context.Context) (string, error) {
	resp, err := c.get(ctx, "/user")
	if err != nil {
		return "", fmt.Errorf("get authenticated user: %w", err)
	}

	var user struct {
		Login string `json:"login"`
	}
	if err := decodeJSON(resp, &user); err != nil {
		return "", fmt.Errorf("decode user: %w", err)
	}
	return user.Login, nil
}

// GetTokenScopes returns the OAuth scopes granted to the current token
// by inspecting the X-OAuth-Scopes header from a lightweight API call.
//
// GitHub only populates X-OAuth-Scopes for classic PATs and OAuth tokens.
// Fine-grained PATs and GitHub App installation tokens return an empty
// header, making scope introspection impossible for those token types.
// There is no alternative API to query fine-grained PAT permissions.
// See: https://docs.github.com/en/rest/using-the-rest-api/troubleshooting-the-rest-api#missing-or-incorrect-x-oauth-scopes-header
func (c *LiveClient) GetTokenScopes(ctx context.Context) ([]string, error) {
	resp, err := c.do(ctx, http.MethodHead, "/user", nil)
	if err != nil {
		return nil, fmt.Errorf("checking token scopes: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Message: "token validation failed"}
	}

	header := resp.Header.Get("X-OAuth-Scopes")
	if header == "" {
		// Fine-grained tokens and GitHub App tokens don't populate this header.
		// Return nil to indicate scope introspection isn't available.
		return nil, nil
	}

	var scopes []string
	for _, s := range strings.Split(header, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			scopes = append(scopes, s)
		}
	}
	return scopes, nil
}

// CreateRepoSecret creates or updates an encrypted repository secret.
func (c *LiveClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	value = strings.TrimSpace(value)
	// Step 1: Get the repo's public key for secret encryption.
	keyResp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/actions/secrets/public-key", owner, repo))
	if err != nil {
		return fmt.Errorf("get public key: %w", err)
	}

	var pubKey struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := decodeJSON(keyResp, &pubKey); err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}

	// Step 2: Decode the public key and encrypt the secret value.
	keyBytes, err := base64.StdEncoding.DecodeString(pubKey.Key)
	if err != nil {
		return fmt.Errorf("decode public key base64: %w", err)
	}

	var recipientKey [32]byte
	copy(recipientKey[:], keyBytes)

	encrypted, err := box.SealAnonymous(nil, []byte(value), &recipientKey, nil)
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}

	// Step 3: Upload the encrypted secret.
	payload := map[string]string{
		"encrypted_value": base64.StdEncoding.EncodeToString(encrypted),
		"key_id":          pubKey.KeyID,
	}

	resp, err := c.put(ctx, fmt.Sprintf("/repos/%s/%s/actions/secrets/%s", owner, repo, name), payload)
	if err != nil {
		return fmt.Errorf("create secret %s: %w", name, err)
	}
	resp.Body.Close()
	return nil
}

// RepoSecretExists checks if a secret exists in a repository.
func (c *LiveClient) RepoSecretExists(ctx context.Context, owner, repo, name string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/actions/secrets/%s", owner, repo, name), nil)
	if err != nil {
		return false, fmt.Errorf("check secret %s: %w", name, err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, &APIError{StatusCode: resp.StatusCode, Message: "unexpected status checking secret"}
}

// CreateOrUpdateRepoVariable creates or updates a repository Actions variable.
func (c *LiveClient) CreateOrUpdateRepoVariable(ctx context.Context, owner, repo, name, value string) error {
	payload := map[string]string{
		"value": value,
	}

	// Try PATCH first (update existing).
	resp, err := c.patch(ctx, fmt.Sprintf("/repos/%s/%s/actions/variables/%s", owner, repo, name), payload)
	if err == nil {
		resp.Body.Close()
		return nil
	}

	// If the variable doesn't exist (404), create it.
	if !isNotFound(err) {
		return fmt.Errorf("update variable %s: %w", name, err)
	}

	createPayload := map[string]string{
		"name":  name,
		"value": value,
	}
	resp2, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/actions/variables", owner, repo), createPayload)
	if err != nil {
		return fmt.Errorf("create variable %s: %w", name, err)
	}
	resp2.Body.Close()
	return nil
}

// RepoVariableExists checks if a variable exists in a repository.
func (c *LiveClient) RepoVariableExists(ctx context.Context, owner, repo, name string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/actions/variables/%s", owner, repo, name), nil)
	if err != nil {
		return false, fmt.Errorf("check variable %s: %w", name, err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, &APIError{StatusCode: resp.StatusCode, Message: "unexpected status checking variable"}
}

// GetLatestWorkflowRun returns the most recent workflow run for a workflow file.
func (c *LiveClient) GetLatestWorkflowRun(ctx context.Context, owner, repo, workflowFile string) (*forge.WorkflowRun, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/runs?per_page=1", owner, repo, workflowFile))
	if err != nil {
		return nil, fmt.Errorf("get latest workflow run: %w", err)
	}

	var result struct {
		WorkflowRuns []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
			CreatedAt  string `json:"created_at"`
		} `json:"workflow_runs"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode workflow runs: %w", err)
	}

	if len(result.WorkflowRuns) == 0 {
		return nil, fmt.Errorf("no workflow runs found for %s", workflowFile)
	}

	run := result.WorkflowRuns[0]
	return &forge.WorkflowRun{
		ID:         run.ID,
		Name:       run.Name,
		Status:     run.Status,
		Conclusion: run.Conclusion,
		HTMLURL:    run.HTMLURL,
		CreatedAt:  run.CreatedAt,
	}, nil
}

// GetWorkflowRun returns a specific workflow run by ID.
func (c *LiveClient) GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*forge.WorkflowRun, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/actions/runs/%d", owner, repo, runID))
	if err != nil {
		return nil, fmt.Errorf("get workflow run %d: %w", runID, err)
	}

	var run struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		HTMLURL    string `json:"html_url"`
		CreatedAt  string `json:"created_at"`
	}
	if err := decodeJSON(resp, &run); err != nil {
		return nil, fmt.Errorf("decode workflow run: %w", err)
	}

	return &forge.WorkflowRun{
		ID:         run.ID,
		Name:       run.Name,
		Status:     run.Status,
		Conclusion: run.Conclusion,
		HTMLURL:    run.HTMLURL,
		CreatedAt:  run.CreatedAt,
	}, nil
}

// DispatchWorkflow triggers a workflow_dispatch event on a workflow file.
// GitHub returns 204 No Content on success (not 200 or 201).
func (c *LiveClient) DispatchWorkflow(ctx context.Context, owner, repo, workflowFile, ref string, inputs map[string]string) error {
	dispatchInputs := make(map[string]string)
	for k, v := range inputs {
		dispatchInputs[k] = v
	}
	payload := map[string]any{
		"ref":    ref,
		"inputs": dispatchInputs,
	}
	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/dispatches", owner, repo, workflowFile), payload)
	if err != nil {
		return fmt.Errorf("dispatch workflow %s: %w", workflowFile, err)
	}
	if err := checkStatus(resp, http.StatusNoContent); err != nil {
		return fmt.Errorf("dispatch workflow %s: %w", workflowFile, err)
	}
	resp.Body.Close()
	return nil
}

// CreateIssue creates a new issue on a repository.
func (c *LiveClient) CreateIssue(ctx context.Context, owner, repo, title, body string) (*forge.Issue, error) {
	payload := map[string]string{"title": title, "body": body}
	resp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/issues", owner, repo), payload)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}
	var result struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode issue: %w", err)
	}
	return &forge.Issue{Number: result.Number, Title: result.Title, URL: result.HTMLURL}, nil
}

// CloseIssue closes an issue by number.
func (c *LiveClient) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	resp, err := c.patch(ctx, fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number), map[string]string{"state": "closed"})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// MergeChangeProposal squash-merges a pull request by number.
func (c *LiveClient) MergeChangeProposal(ctx context.Context, owner, repo string, number int) error {
	resp, err := c.put(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, repo, number), map[string]string{"merge_method": "squash"})
	if err != nil {
		return fmt.Errorf("merge pull request #%d: %w", number, err)
	}
	resp.Body.Close()
	return nil
}

// ListWorkflowRuns returns recent workflow runs for a workflow file.
func (c *LiveClient) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]forge.WorkflowRun, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/runs?per_page=10", owner, repo, workflowFile))
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	var result struct {
		WorkflowRuns []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
			CreatedAt  string `json:"created_at"`
		} `json:"workflow_runs"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode workflow runs: %w", err)
	}
	runs := make([]forge.WorkflowRun, len(result.WorkflowRuns))
	for i, r := range result.WorkflowRuns {
		runs[i] = forge.WorkflowRun{
			ID:         r.ID,
			Name:       r.Name,
			Status:     r.Status,
			Conclusion: r.Conclusion,
			HTMLURL:    r.HTMLURL,
			CreatedAt:  r.CreatedAt,
		}
	}
	return runs, nil
}

// GetWorkflowRunLogs downloads the logs for a workflow run.
// It fetches the job list for the run and concatenates each job's log output.
func (c *LiveClient) GetWorkflowRunLogs(ctx context.Context, owner, repo string, runID int) (string, error) {
	// List jobs for this run.
	resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs", owner, repo, runID))
	if err != nil {
		return "", fmt.Errorf("list jobs for run %d: %w", runID, err)
	}
	var jobsResult struct {
		Jobs []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"jobs"`
	}
	if err := decodeJSON(resp, &jobsResult); err != nil {
		return "", fmt.Errorf("decode jobs: %w", err)
	}

	var buf strings.Builder
	for _, job := range jobsResult.Jobs {
		// Download logs for each job (returns plain text, 302 redirect to download URL).
		jobResp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/actions/jobs/%d/logs", owner, repo, job.ID), nil)
		if err != nil {
			fmt.Fprintf(&buf, "=== %s (job %d) ===\n[failed to fetch logs: %v]\n\n", job.Name, job.ID, err)
			continue
		}
		if jobResp.StatusCode < 200 || jobResp.StatusCode >= 300 {
			jobResp.Body.Close()
			fmt.Fprintf(&buf, "=== %s (job %d) ===\n[logs unavailable: HTTP %d]\n\n", job.Name, job.ID, jobResp.StatusCode)
			continue
		}
		logData, readErr := io.ReadAll(io.LimitReader(jobResp.Body, 1<<20)) // 1 MB per job
		jobResp.Body.Close()
		if readErr != nil {
			fmt.Fprintf(&buf, "=== %s (job %d) ===\n[failed to read logs: %v]\n\n", job.Name, job.ID, readErr)
			continue
		}
		fmt.Fprintf(&buf, "=== %s (job %d) ===\n%s\n", job.Name, job.ID, string(logData))
	}
	return buf.String(), nil
}

// ListOrgInstallations lists app installations for an organization.
func (c *LiveClient) ListOrgInstallations(ctx context.Context, org string) ([]forge.Installation, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/orgs/%s/installations?per_page=100", org))
	if err != nil {
		return nil, fmt.Errorf("list org installations: %w", err)
	}

	var result struct {
		Installations []struct {
			ID      int    `json:"id"`
			AppID   int    `json:"app_id"`
			AppSlug string `json:"app_slug"`
		} `json:"installations"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode installations: %w", err)
	}

	installs := make([]forge.Installation, len(result.Installations))
	for i, inst := range result.Installations {
		installs[i] = forge.Installation{
			ID:      inst.ID,
			AppID:   inst.AppID,
			AppSlug: inst.AppSlug,
		}
	}
	return installs, nil
}

// CreateOrgSecret creates or updates an encrypted organization-level secret
// scoped to the given repository IDs.
// The value is trimmed of whitespace before encryption to prevent corruption
// from stray newlines or carriage returns in pasted input.
func (c *LiveClient) CreateOrgSecret(ctx context.Context, org, name, value string, selectedRepoIDs []int64) error {
	value = strings.TrimSpace(value)
	// Step 1: Get the org's public key for secret encryption.
	keyResp, err := c.get(ctx, fmt.Sprintf("/orgs/%s/actions/secrets/public-key", org))
	if err != nil {
		return fmt.Errorf("get org public key: %w", err)
	}

	var pubKey struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := decodeJSON(keyResp, &pubKey); err != nil {
		return fmt.Errorf("decode org public key: %w", err)
	}

	// Step 2: Decode the public key and encrypt the secret value.
	keyBytes, err := base64.StdEncoding.DecodeString(pubKey.Key)
	if err != nil {
		return fmt.Errorf("decode org public key base64: %w", err)
	}

	var recipientKey [32]byte
	copy(recipientKey[:], keyBytes)

	encrypted, err := box.SealAnonymous(nil, []byte(value), &recipientKey, nil)
	if err != nil {
		return fmt.Errorf("encrypt org secret: %w", err)
	}

	// Step 3: Upload the encrypted secret.
	// Always use visibility "selected" so that SetOrgSecretRepos can later
	// update the repo access list without a 409 Conflict (which GitHub
	// returns when trying to set selected repos on a visibility "all" secret).
	if selectedRepoIDs == nil {
		selectedRepoIDs = []int64{}
	}
	payload := map[string]any{
		"encrypted_value":       base64.StdEncoding.EncodeToString(encrypted),
		"key_id":                pubKey.KeyID,
		"visibility":            "selected",
		"selected_repository_ids": selectedRepoIDs,
	}

	resp, err := c.put(ctx, fmt.Sprintf("/orgs/%s/actions/secrets/%s", org, name), payload)
	if err != nil {
		return fmt.Errorf("create org secret %s: %w", name, err)
	}
	resp.Body.Close()
	return nil
}

// OrgSecretExists checks if an org-level secret exists.
func (c *LiveClient) OrgSecretExists(ctx context.Context, org, name string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/orgs/%s/actions/secrets/%s", org, name), nil)
	if err != nil {
		return false, fmt.Errorf("check org secret %s: %w", name, err)
	}
	resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	case http.StatusForbidden:
		// 403 means the token doesn't have permission to check org secrets.
		// Return false with an error so callers can distinguish "not found"
		// from "can't tell due to permissions".
		return false, &APIError{StatusCode: http.StatusForbidden, Message: "insufficient permissions to check org secret (missing admin:org scope?)"}
	default:
		return false, &APIError{StatusCode: resp.StatusCode, Message: "unexpected status checking org secret"}
	}
}

// DeleteOrgSecret deletes an org-level secret. It is idempotent: a 404
// (secret already gone) is not treated as an error.
func (c *LiveClient) DeleteOrgSecret(ctx context.Context, org, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/orgs/%s/actions/secrets/%s", org, name), nil)
	if err != nil {
		return fmt.Errorf("delete org secret %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return &APIError{StatusCode: resp.StatusCode, Message: "unexpected status deleting org secret"}
}

// SetOrgSecretRepos sets the list of repositories that can access an org secret.
func (c *LiveClient) SetOrgSecretRepos(ctx context.Context, org, name string, repoIDs []int64) error {
	if repoIDs == nil {
		repoIDs = []int64{}
	}
	payload := map[string]any{
		"selected_repository_ids": repoIDs,
	}

	resp, err := c.put(ctx, fmt.Sprintf("/orgs/%s/actions/secrets/%s/repositories", org, name), payload)
	if err != nil {
		return fmt.Errorf("set org secret repos for %s: %w", name, err)
	}
	resp.Body.Close()
	return nil
}

// isNotFound checks whether an error is a 404 API error.
func isNotFound(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return errors.Is(err, forge.ErrNotFound)
}
