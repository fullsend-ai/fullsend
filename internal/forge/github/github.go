// Package github implements forge.Client for the GitHub REST API.
package github

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git's blob hash algorithm, not used for security
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	Errors     []APIErrorDetail
}

// APIErrorDetail is one validation error entry returned by GitHub.
type APIErrorDetail struct {
	Resource string `json:"resource"`
	Field    string `json:"field"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("github api: %d %s", e.StatusCode, e.Message)
	for _, d := range e.Errors {
		if d.Message != "" {
			s += fmt.Sprintf(" (%s)", d.Message)
		}
	}
	return s
}

// Unwrap returns sentinel errors for well-known API responses.
//
// ErrBranchProtected is intentionally NOT mapped here. Branch protection
// 422s are context-dependent: the word "protected" in a validation error
// only signals a branch-protection failure when it comes from a ref update
// (PATCH /git/refs). Other 422s may coincidentally mention "protected" in
// unrelated contexts. The wrapping happens in commitFilesTo where the
// operation context is known.
func (e *APIError) Unwrap() error {
	if e.StatusCode == http.StatusNotFound {
		return forge.ErrNotFound
	}
	if e.StatusCode == http.StatusUnprocessableEntity && isAlreadyExistsError(e) {
		return forge.ErrAlreadyExists
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

		retryable, respBody := isRetryable(resp)
		if !retryable {
			if respBody != nil {
				// We read the body to check for secondary rate limit;
				// replace it so callers can still read it.
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(respBody))
			}
			return resp, nil
		}

		// Body already read or drained by isRetryable.
		resp.Body.Close()

		delay := retryDelay(resp, attempt)
		retryAfter := resp.Header.Get("Retry-After")

		if attempt == maxRetries-1 {
			msg := fmt.Sprintf("retryable error after %d attempts on %s %s (last delay: %s", maxRetries, method, path, delay)
			if retryAfter != "" {
				msg += fmt.Sprintf(", Retry-After: %s", retryAfter)
			}
			msg += ")"
			return nil, &APIError{StatusCode: resp.StatusCode, Message: msg}
		}
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
// GitHub uses 429 for primary rate limits and 403 for secondary rate limits.
// Secondary rate limits may include a Retry-After header, or may only be
// identifiable by the response body containing "secondary rate limit".
// Server errors (500, 502, 503, 504) are also retried as transient failures.
func isRetryable(resp *http.Response) (bool, []byte) {
	if resp.StatusCode == http.StatusTooManyRequests {
		io.Copy(io.Discard, resp.Body)
		return true, nil
	}
	// Transient server errors.
	if resp.StatusCode >= 500 && resp.StatusCode <= 504 {
		io.Copy(io.Discard, resp.Body)
		return true, nil
	}
	if resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("Retry-After") != "" {
			io.Copy(io.Discard, resp.Body)
			return true, nil
		}
		// Check body for secondary rate limit without Retry-After header.
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<16)) // 64KB max
		if readErr != nil {
			return false, nil
		}
		if strings.Contains(strings.ToLower(string(data)), "secondary rate limit") {
			return true, nil
		}
		// Not a rate limit — return the body so the caller can still use it.
		return false, data
	}
	return false, nil
}

// secondaryRateLimitBackoff is the minimum backoff for secondary rate limits
// when no Retry-After header is present. GitHub's secondary rate limits
// typically require waiting at least 60 seconds.
var secondaryRateLimitBackoff = 60 * time.Second

// retryDelay calculates how long to wait before retrying.
// It uses the Retry-After header if present, otherwise exponential backoff.
func retryDelay(resp *http.Response, attempt int) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 && secs <= 300 {
			return time.Duration(secs) * time.Second
		}
	}
	// For secondary rate limits (403), use a longer backoff.
	if resp.StatusCode == http.StatusForbidden {
		return secondaryRateLimitBackoff + time.Duration(math.Pow(2, float64(attempt)))*time.Second
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
		Message string           `json:"message"`
		Errors  []APIErrorDetail `json:"errors"`
	}
	if json.Unmarshal(data, &msg) == nil && msg.Message != "" {
		return &APIError{StatusCode: resp.StatusCode, Message: msg.Message, Errors: msg.Errors}
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

// ListOrgRepos returns public, non-archived, non-fork repositories for an org.
//
// Private repos are excluded because the default .fullsend config repo is
// public and agent workflow logs are visible to anyone. Enrolling a private
// repo would expose its code in those public logs.
//
// Forks are excluded because fullsend's trust model assumes org-owned repos
// where CODEOWNERS governance and org-level permissions control agent
// autonomy. Fork repos may have different ownership and CODEOWNERS configs,
// which could bypass human-approval gates. Archived repos are excluded
// because they represent inactive targets where agent work would be wasted.
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
			if r.Archived || r.Fork || r.Private {
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

	return c.retryOnRepoRace(ctx, path, func() error {
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

	return c.retryOnRepoRace(ctx, path, func() error {
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
// repo race conditions (404 from async repo init, 409 from branch ref races).
func (c *LiveClient) putFileWithRetry(ctx context.Context, apiPath string, payload map[string]any, path string) error {
	return c.retryOnRepoRace(ctx, path, func() error {
		resp, err := c.put(ctx, apiPath, payload)
		if err != nil {
			return fmt.Errorf("create file %s: %w", path, err)
		}
		resp.Body.Close()
		return nil
	})
}

// retryOnRepoRace retries an operation that may fail due to GitHub
// repository initialization races. It handles 404 (async repo/branch
// creation where the ref is not yet materialized) and 409 (branch ref
// update conflicts). Server-side 5xx errors are handled at a lower level
// by do(). It uses linear backoff (2s between attempts) and up to 5
// attempts (~10s total).
func (c *LiveClient) retryOnRepoRace(ctx context.Context, label string, fn func() error) error {
	const attempts = 5
	const delay = 2 * time.Second

	var lastErr error
	for i := range attempts {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		// Retry on transient errors:
		// - 404: repo not ready (async init)
		// - 409: branch ref conflict
		// - 500/502/503/504: transient server-side errors
		var apiErr *APIError
		if !errors.As(lastErr, &apiErr) || !isTransientStatus(apiErr.StatusCode) {
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

// isTransientStatus returns true for HTTP status codes that indicate a
// repo/branch race condition worth retrying: 404 (async repo init) and
// 409 (branch ref conflict). Server-side 5xx errors are retried at a
// lower level by do().
func isTransientStatus(code int) bool {
	switch code {
	case http.StatusNotFound,
		http.StatusConflict:
		return true
	default:
		return false
	}
}

// CommitFiles atomically commits multiple files to the default branch
// using the Git Trees/Blobs/Commits API. Returns (false, nil) when
// all files already match the current tree (idempotent).
// Text files are embedded as UTF-8 tree content. Binary files (e.g.
// vendored ELF) are uploaded via the Git Blob API and referenced by SHA.
//
// Returns forge.ErrBranchProtected (wrapped) when the ref update fails
// with a 422, which indicates branch protection rules prevent direct pushes.
func (c *LiveClient) CommitFiles(ctx context.Context, owner, repo, message string, files []forge.TreeFile) (bool, error) {
	if len(files) == 0 {
		return false, nil
	}

	// Get default branch name.
	repoResp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s", owner, repo))
	if err != nil {
		return false, fmt.Errorf("get repo: %w", err)
	}
	var repoInfo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := decodeJSON(repoResp, &repoInfo); err != nil {
		return false, fmt.Errorf("decode repo info: %w", err)
	}

	return c.commitFilesTo(ctx, owner, repo, repoInfo.DefaultBranch, message, files)
}

// CommitFilesToBranch atomically commits multiple files to a specific
// branch. Like CommitFiles, it is idempotent.
func (c *LiveClient) CommitFilesToBranch(ctx context.Context, owner, repo, branch, message string, files []forge.TreeFile) (bool, error) {
	if len(files) == 0 {
		return false, nil
	}
	return c.commitFilesTo(ctx, owner, repo, branch, message, files)
}

// commitFilesTo is the shared implementation for CommitFiles and
// CommitFilesToBranch. It commits files to the specified branch using
// the Git Trees/Blobs/Commits API.
func (c *LiveClient) commitFilesTo(ctx context.Context, owner, repo, branch, message string, files []forge.TreeFile) (bool, error) {
	// 1. Get current commit SHA from the branch ref.
	// Wrapped in retryOnRepoRace for freshly-created repos/branches where
	// the ref may not be materialized yet (async auto_init).
	var commitSHA string
	if err := c.retryOnRepoRace(ctx, "get branch ref", func() error {
		refResp, refErr := c.get(ctx, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, branch))
		if refErr != nil {
			return fmt.Errorf("get branch ref: %w", refErr)
		}
		var ref struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if decErr := decodeJSON(refResp, &ref); decErr != nil {
			return fmt.Errorf("decode ref: %w", decErr)
		}
		commitSHA = ref.Object.SHA
		return nil
	}); err != nil {
		return false, err
	}

	// 2. Get the current commit to find its tree SHA.
	cResp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/git/commits/%s", owner, repo, commitSHA))
	if err != nil {
		return false, fmt.Errorf("get commit: %w", err)
	}
	var commitObj struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := decodeJSON(cResp, &commitObj); err != nil {
		return false, fmt.Errorf("decode commit: %w", err)
	}
	baseTreeSHA := commitObj.Tree.SHA

	// 3. Get the full recursive tree to compare existing blobs.
	treeResp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1", owner, repo, baseTreeSHA))
	if err != nil {
		return false, fmt.Errorf("get tree: %w", err)
	}
	var existingTree struct {
		Tree []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			SHA  string `json:"sha"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := decodeJSON(treeResp, &existingTree); err != nil {
		return false, fmt.Errorf("decode tree: %w", err)
	}
	if existingTree.Truncated {
		return false, fmt.Errorf("tree too large (truncated); cannot diff")
	}

	type blobInfo struct {
		sha  string
		mode string
	}
	existing := make(map[string]blobInfo, len(existingTree.Tree))
	for _, entry := range existingTree.Tree {
		existing[entry.Path] = blobInfo{sha: entry.SHA, mode: entry.Mode}
	}

	// 4. Compute expected blob SHAs and filter to changed files.
	var changedEntries []map[string]any
	for _, f := range files {
		expectedSHA := blobSHA(f.Content)
		info, exists := existing[f.Path]
		if exists && info.sha == expectedSHA && info.mode == f.Mode {
			continue
		}

		entry := map[string]any{
			"path": f.Path,
			"mode": f.Mode,
			"type": "blob",
		}
		if utf8.Valid(f.Content) {
			entry["content"] = string(f.Content)
		} else {
			blobSHAValue := expectedSHA
			if exists && info.sha == expectedSHA {
				blobSHAValue = info.sha
			} else {
				createdSHA, err := c.createBlob(ctx, owner, repo, f.Content)
				if err != nil {
					return false, fmt.Errorf("create blob for %s: %w", f.Path, err)
				}
				blobSHAValue = createdSHA
			}
			entry["sha"] = blobSHAValue
		}
		changedEntries = append(changedEntries, entry)
	}

	if len(changedEntries) == 0 {
		return false, nil
	}

	// 5. Create new tree with base_tree + changed entries.
	treePayload := map[string]any{
		"base_tree": baseTreeSHA,
		"tree":      changedEntries,
	}
	newTreeResp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/git/trees", owner, repo), treePayload)
	if err != nil {
		return false, fmt.Errorf("create tree: %w", err)
	}
	var newTree struct {
		SHA string `json:"sha"`
	}
	if err := decodeJSON(newTreeResp, &newTree); err != nil {
		return false, fmt.Errorf("decode new tree: %w", err)
	}

	// 6. Create commit with new tree and old commit as parent.
	commitPayload := map[string]any{
		"message": message,
		"tree":    newTree.SHA,
		"parents": []string{commitSHA},
	}
	newCommitResp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/git/commits", owner, repo), commitPayload)
	if err != nil {
		return false, fmt.Errorf("create commit: %w", err)
	}
	var newCommit struct {
		SHA string `json:"sha"`
	}
	if err := decodeJSON(newCommitResp, &newCommit); err != nil {
		return false, fmt.Errorf("decode new commit: %w", err)
	}

	// 7. Update branch ref to point to new commit.
	// A 422 here typically means branch protection rules prevent the push.
	refPayload := map[string]string{
		"sha": newCommit.SHA,
	}
	refUpdateResp, err := c.patch(ctx, fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, branch), refPayload)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnprocessableEntity && isBranchProtectionError(apiErr) {
			return false, fmt.Errorf("%w: %w", forge.ErrBranchProtected, err)
		}
		return false, fmt.Errorf("update ref: %w", err)
	}
	refUpdateResp.Body.Close()

	return true, nil
}

// DeleteFiles atomically removes paths from the repository default branch.
func (c *LiveClient) DeleteFiles(ctx context.Context, owner, repo, message string, paths []string) (int, error) {
	if len(paths) == 0 {
		return 0, nil
	}

	repoResp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s", owner, repo))
	if err != nil {
		return 0, fmt.Errorf("get repo: %w", err)
	}
	var repoInfo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := decodeJSON(repoResp, &repoInfo); err != nil {
		return 0, fmt.Errorf("decode repo info: %w", err)
	}

	var commitSHA string
	if err := c.retryOnRepoRace(ctx, "get branch ref", func() error {
		refResp, refErr := c.get(ctx, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, repoInfo.DefaultBranch))
		if refErr != nil {
			return fmt.Errorf("get branch ref: %w", refErr)
		}
		var ref struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if decErr := decodeJSON(refResp, &ref); decErr != nil {
			return fmt.Errorf("decode ref: %w", decErr)
		}
		commitSHA = ref.Object.SHA
		return nil
	}); err != nil {
		return 0, err
	}

	cResp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/git/commits/%s", owner, repo, commitSHA))
	if err != nil {
		return 0, fmt.Errorf("get commit: %w", err)
	}
	var commitObj struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := decodeJSON(cResp, &commitObj); err != nil {
		return 0, fmt.Errorf("decode commit: %w", err)
	}
	baseTreeSHA := commitObj.Tree.SHA

	treeResp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1", owner, repo, baseTreeSHA))
	if err != nil {
		return 0, fmt.Errorf("get tree: %w", err)
	}
	var existingTree struct {
		Tree []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := decodeJSON(treeResp, &existingTree); err != nil {
		return 0, fmt.Errorf("decode tree: %w", err)
	}
	if existingTree.Truncated {
		return 0, fmt.Errorf("tree too large (truncated); cannot delete")
	}

	existing := make(map[string]string, len(existingTree.Tree))
	for _, entry := range existingTree.Tree {
		existing[entry.Path] = entry.Mode
	}

	var deleteEntries []map[string]any
	for _, path := range paths {
		mode, ok := existing[path]
		if !ok {
			continue
		}
		if mode == "" {
			mode = "100644"
		}
		deleteEntries = append(deleteEntries, map[string]any{
			"path": path,
			"mode": mode,
			"type": "blob",
			"sha":  nil,
		})
	}
	if len(deleteEntries) == 0 {
		return 0, nil
	}

	treePayload := map[string]any{
		"base_tree": baseTreeSHA,
		"tree":      deleteEntries,
	}
	newTreeResp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/git/trees", owner, repo), treePayload)
	if err != nil {
		return 0, fmt.Errorf("create tree: %w", err)
	}
	var newTree struct {
		SHA string `json:"sha"`
	}
	if err := decodeJSON(newTreeResp, &newTree); err != nil {
		return 0, fmt.Errorf("decode new tree: %w", err)
	}

	commitPayload := map[string]any{
		"message": message,
		"tree":    newTree.SHA,
		"parents": []string{commitSHA},
	}
	newCommitResp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/git/commits", owner, repo), commitPayload)
	if err != nil {
		return 0, fmt.Errorf("create commit: %w", err)
	}
	var newCommit struct {
		SHA string `json:"sha"`
	}
	if err := decodeJSON(newCommitResp, &newCommit); err != nil {
		return 0, fmt.Errorf("decode new commit: %w", err)
	}

	refPayload := map[string]string{"sha": newCommit.SHA}
	if err := c.retryOnRepoRace(ctx, "update ref", func() error {
		refUpdateResp, patchErr := c.patch(ctx, fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, repoInfo.DefaultBranch), refPayload)
		if patchErr != nil {
			return fmt.Errorf("update ref: %w", patchErr)
		}
		refUpdateResp.Body.Close()
		return nil
	}); err != nil {
		return 0, err
	}

	return len(deleteEntries), nil
}

// isBranchProtectionError checks whether a 422 APIError indicates branch
// protection rather than another validation failure (e.g. non-fast-forward).
// It matches both legacy branch protection rules and newer repository rulesets.
func isBranchProtectionError(apiErr *APIError) bool {
	msg := strings.ToLower(apiErr.Message)
	for _, d := range apiErr.Errors {
		msg += " " + strings.ToLower(d.Message)
	}
	return strings.Contains(msg, "protected") ||
		strings.Contains(msg, "required status") ||
		strings.Contains(msg, "required review") ||
		strings.Contains(msg, "rule violation")
}

func isAlreadyExistsError(apiErr *APIError) bool {
	msg := strings.ToLower(apiErr.Message)
	for _, d := range apiErr.Errors {
		msg += " " + strings.ToLower(d.Message)
	}
	return strings.Contains(msg, "already exists")
}

// blobSHA computes the Git blob object SHA-1 for the given content.
func blobSHA(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *LiveClient) createBlob(ctx context.Context, owner, repo string, content []byte) (string, error) {
	payload := map[string]string{
		"content":  base64.StdEncoding.EncodeToString(content),
		"encoding": "base64",
	}
	resp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/git/blobs", owner, repo), payload)
	if err != nil {
		return "", fmt.Errorf("create blob: %w", err)
	}
	var blob struct {
		SHA string `json:"sha"`
	}
	if err := decodeJSON(resp, &blob); err != nil {
		return "", fmt.Errorf("decode blob: %w", err)
	}
	return blob.SHA, nil
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

// escapePathSegments URL-escapes each segment of a slash-separated path
// individually, preserving the / separators.
func escapePathSegments(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

// GetFileContentAtRef retrieves the content of a file at a specific ref
// (commit SHA, branch, or tag). Unlike GetFileContent which reads from
// the default branch, this reads from the specified ref.
func (c *LiveClient) GetFileContentAtRef(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s",
		url.PathEscape(owner), url.PathEscape(repo), escapePathSegments(path), url.QueryEscape(ref)))
	if err != nil {
		return nil, fmt.Errorf("get file content at ref: %w", err)
	}

	var file struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(resp, &file); err != nil {
		return nil, fmt.Errorf("decode file content: %w", err)
	}

	cleaned := strings.ReplaceAll(strings.ReplaceAll(file.Content, "\n", ""), "\r", "")
	data, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("decode base64 content: %w", err)
	}
	return data, nil
}

const (
	maxDirDepth    = 10
	maxDirAPIcalls = 100
	maxDirFiles    = 1000
)

// ListDirectoryContents returns all files and subdirectories at the given
// path in a repository at the specified ref. When path points to a directory,
// the GitHub Contents API returns a JSON array of entries.
func (c *LiveClient) ListDirectoryContents(ctx context.Context, owner, repo, path, ref string, recursive bool) ([]forge.DirectoryEntry, error) {
	apiCalls := 0
	fileCount := 0
	return c.listDirContents(ctx, owner, repo, path, ref, recursive, 0, &apiCalls, &fileCount)
}

func (c *LiveClient) listDirContents(ctx context.Context, owner, repo, path, ref string, recursive bool, depth int, apiCalls *int, fileCount *int) ([]forge.DirectoryEntry, error) {
	if depth > maxDirDepth {
		return nil, fmt.Errorf("directory listing exceeded maximum depth of %d at %s", maxDirDepth, path)
	}
	if *apiCalls >= maxDirAPIcalls {
		return nil, fmt.Errorf("directory listing exceeded maximum of %d API calls", maxDirAPIcalls)
	}
	*apiCalls++

	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s",
		url.PathEscape(owner), url.PathEscape(repo), escapePathSegments(path), url.QueryEscape(ref))

	resp, err := c.get(ctx, apiPath)
	if err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}

	var entries []struct {
		Name string `json:"name"`
		Path string `json:"path"` // full path from repo root
		Type string `json:"type"` // "file" or "dir"
		Size int    `json:"size"`
	}
	if err := decodeJSON(resp, &entries); err != nil {
		return nil, fmt.Errorf("decode directory listing: %w", err)
	}

	var result []forge.DirectoryEntry
	for _, e := range entries {
		var relPath string
		if path == "" {
			relPath = e.Path
		} else {
			relPath = strings.TrimPrefix(e.Path, path+"/")
		}

		if e.Type == "file" {
			if *fileCount >= maxDirFiles {
				return nil, fmt.Errorf("directory listing exceeded maximum of %d files", maxDirFiles)
			}
			*fileCount++
			result = append(result, forge.DirectoryEntry{
				Path: relPath,
				Type: "file",
				Size: e.Size,
			})
		} else if e.Type == "dir" && recursive {
			subEntries, err := c.listDirContents(ctx, owner, repo, e.Path, ref, true, depth+1, apiCalls, fileCount)
			if err != nil {
				return nil, fmt.Errorf("listing subdirectory %s: %w", e.Path, err)
			}
			for _, sub := range subEntries {
				sub.Path = relPath + "/" + sub.Path
				result = append(result, sub)
			}
		} else if e.Type == "dir" {
			result = append(result, forge.DirectoryEntry{
				Path: relPath,
				Type: "dir",
				Size: 0,
			})
		}
	}

	return result, nil
}

// DeleteFile deletes a file from the repository's default branch.
// It first fetches the file to obtain its SHA (required by the GitHub Contents
// API), then issues the DELETE. Retries on transient 404/409 errors.
func (c *LiveClient) DeleteFile(ctx context.Context, owner, repo, path, message string) error {
	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, path)

	return c.retryOnRepoRace(ctx, path, func() error {
		// GET the file to obtain its SHA.
		existingResp, err := c.do(ctx, http.MethodGet, apiPath, nil)
		if err != nil {
			return fmt.Errorf("get file for delete: %w", err)
		}
		if err := checkStatus(existingResp, http.StatusOK); err != nil {
			return fmt.Errorf("get file %s for delete: %w", path, err)
		}

		var existing struct {
			SHA string `json:"sha"`
		}
		if err := decodeJSON(existingResp, &existing); err != nil {
			return fmt.Errorf("decode file sha: %w", err)
		}

		payload := map[string]string{
			"message": message,
			"sha":     existing.SHA,
		}

		resp, err := c.do(ctx, http.MethodDelete, apiPath, payload)
		if err != nil {
			return fmt.Errorf("delete file %s: %w", path, err)
		}
		defer resp.Body.Close()
		if err := checkStatus(resp, http.StatusOK); err != nil {
			return fmt.Errorf("delete file %s: %w", path, err)
		}
		return nil
	})
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

// GetOrgPlan returns the billing plan name for the org (e.g. "free", "team", "enterprise").
func (c *LiveClient) GetOrgPlan(ctx context.Context, org string) (string, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/orgs/%s", org))
	if err != nil {
		return "", fmt.Errorf("get org plan: %w", err)
	}
	var orgResp struct {
		Plan struct {
			Name string `json:"name"`
		} `json:"plan"`
	}
	if err := decodeJSON(resp, &orgResp); err != nil {
		return "", fmt.Errorf("decode org plan: %w", err)
	}
	return orgResp.Plan.Name, nil
}

// GetAuthenticatedUser returns the login of the authenticated user.
//
// For classic PATs and OAuth tokens the identity comes from GET /user.
// GitHub App installation tokens cannot call /user, so when that call
// fails the method falls back to GET /app and constructs the
// conventional bot login "{slug}[bot]".
func (c *LiveClient) GetAuthenticatedUser(ctx context.Context) (string, error) {
	resp, err := c.get(ctx, "/user")
	if err == nil {
		var user struct {
			Login string `json:"login"`
		}
		if err := decodeJSON(resp, &user); err != nil {
			return "", fmt.Errorf("decode user: %w", err)
		}
		return user.Login, nil
	}

	// /user is not available for GitHub App installation tokens.
	// Fall back to /app which returns the app's metadata including
	// its slug, from which we derive the bot login.
	appResp, appErr := c.get(ctx, "/app")
	if appErr != nil {
		// Neither endpoint worked — return the original /user error
		// because that is the more common path.
		return "", fmt.Errorf("get authenticated user: %w (app fallback: %v)", err, appErr)
	}

	var app struct {
		Slug string `json:"slug"`
	}
	if appErr := decodeJSON(appResp, &app); appErr != nil {
		return "", fmt.Errorf("decode app: %w", appErr)
	}
	if app.Slug == "" {
		return "", fmt.Errorf("get authenticated user: /app returned empty slug")
	}
	return app.Slug + "[bot]", nil
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

// GetRepoVariable returns the value of a repository Actions variable.
// Returns ("", false, nil) if the variable does not exist.
func (c *LiveClient) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/actions/variables/%s", owner, repo, name), nil)
	if err != nil {
		return "", false, fmt.Errorf("get variable %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, &APIError{StatusCode: resp.StatusCode, Message: "unexpected status getting variable"}
	}

	var result struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", false, fmt.Errorf("decode variable %s: %w", name, err)
	}
	return result.Value, true, nil
}

// GetWorkflow returns a workflow definition by filename (e.g. repo-maintenance.yml).
func (c *LiveClient) GetWorkflow(ctx context.Context, owner, repo, workflowFile string) (*forge.Workflow, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/actions/workflows/%s", owner, repo, workflowFile))
	if err != nil {
		return nil, fmt.Errorf("get workflow %s: %w", workflowFile, err)
	}

	var wf struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Path  string `json:"path"`
		State string `json:"state"`
	}
	if err := decodeJSON(resp, &wf); err != nil {
		return nil, fmt.Errorf("decode workflow %s: %w", workflowFile, err)
	}

	return &forge.Workflow{
		ID:    wf.ID,
		Name:  wf.Name,
		Path:  wf.Path,
		State: wf.State,
	}, nil
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

// CreateIssue creates a new issue on a repository. Labels are best-effort:
// if GitHub rejects the create because a label is unavailable in the target
// repo, the request is retried without labels so issue creation still succeeds.
func (c *LiveClient) CreateIssue(ctx context.Context, owner, repo, title, body string, labels ...string) (*forge.Issue, error) {
	payload := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	resp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/issues", owner, repo), payload)
	if err != nil {
		var apiErr *APIError
		if len(labels) == 0 || !errors.As(err, &apiErr) || !isValidationErrorForField(apiErr, "labels") {
			return nil, fmt.Errorf("create issue: %w", err)
		}
		resp, err = c.post(ctx, fmt.Sprintf("/repos/%s/%s/issues", owner, repo), map[string]any{"title": title, "body": body})
		if err != nil {
			return nil, fmt.Errorf("create issue without labels after label rejection: %w", err)
		}
	}
	var result struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Labels  []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode issue: %w", err)
	}
	return &forge.Issue{
		Number: result.Number,
		Title:  result.Title,
		Body:   result.Body,
		URL:    result.HTMLURL,
		Labels: labelNames(result.Labels),
	}, nil
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

func labelNames(labels []struct {
	Name string `json:"name"`
}) []string {
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		names = append(names, label.Name)
	}
	return names
}

func isValidationErrorForField(err *APIError, field string) bool {
	if err == nil || err.StatusCode != http.StatusUnprocessableEntity {
		return false
	}
	for _, detail := range err.Errors {
		if detail.Field == field {
			return true
		}
	}
	return false
}

// ListOpenIssues returns open issues on a repository, excluding pull requests.
// When labels are provided, GitHub filters to issues carrying those labels.
func (c *LiveClient) ListOpenIssues(ctx context.Context, owner, repo string, labels ...string) ([]forge.Issue, error) {
	var result []forge.Issue

	for page := 1; page <= 100; page++ {
		query := url.Values{}
		query.Set("state", "open")
		query.Set("per_page", "100")
		query.Set("page", strconv.Itoa(page))
		if len(labels) > 0 {
			query.Set("labels", strings.Join(labels, ","))
		}
		resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/issues?%s", owner, repo, query.Encode()))
		if err != nil {
			return nil, fmt.Errorf("list open issues page %d: %w", page, err)
		}
		var raw []struct {
			Number      int    `json:"number"`
			Title       string `json:"title"`
			Body        string `json:"body"`
			HTMLURL     string `json:"html_url"`
			PullRequest *struct {
				URL string `json:"url"`
			} `json:"pull_request"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		}
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decode open issues page %d: %w", page, err)
		}
		for _, item := range raw {
			if item.PullRequest != nil {
				continue
			}
			result = append(result, forge.Issue{
				Number: item.Number,
				Title:  item.Title,
				Body:   item.Body,
				URL:    item.HTMLURL,
				Labels: labelNames(item.Labels),
			})
		}
		if len(raw) < 100 {
			break
		}
	}
	return result, nil
}

// ListIssueComments returns all comments on an issue, paginating automatically.
func (c *LiveClient) ListIssueComments(ctx context.Context, owner, repo string, number int) ([]forge.IssueComment, error) {
	var result []forge.IssueComment

	for page := 1; page <= 100; page++ {
		resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100&page=%d", owner, repo, number, page))
		if err != nil {
			return nil, fmt.Errorf("list issue comments page %d: %w", page, err)
		}
		var raw []struct {
			ID      int    `json:"id"`
			NodeID  string `json:"node_id"`
			HTMLURL string `json:"html_url"`
			Body    string `json:"body"`
			User    struct {
				Login string `json:"login"`
			} `json:"user"`
			CreatedAt string `json:"created_at"`
		}
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decoding issue comments page %d: %w", page, err)
		}

		for _, r := range raw {
			result = append(result, forge.IssueComment{
				ID:        r.ID,
				NodeID:    r.NodeID,
				HTMLURL:   r.HTMLURL,
				Body:      r.Body,
				Author:    r.User.Login,
				CreatedAt: r.CreatedAt,
			})
		}

		if len(raw) < 100 {
			break
		}
	}
	return result, nil
}

// CreateIssueComment creates a new comment on an issue or pull request.
func (c *LiveClient) CreateIssueComment(ctx context.Context, owner, repo string, number int, body string) (*forge.IssueComment, error) {
	payload := map[string]string{"body": body}
	resp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number), payload)
	if err != nil {
		return nil, fmt.Errorf("create issue comment on #%d: %w", number, err)
	}
	var result struct {
		ID      int    `json:"id"`
		NodeID  string `json:"node_id"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		CreatedAt string `json:"created_at"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode issue comment: %w", err)
	}
	return &forge.IssueComment{
		ID:        result.ID,
		NodeID:    result.NodeID,
		HTMLURL:   result.HTMLURL,
		Body:      result.Body,
		Author:    result.User.Login,
		CreatedAt: result.CreatedAt,
	}, nil
}

// UpdateIssueComment updates the body of an existing issue comment.
func (c *LiveClient) UpdateIssueComment(ctx context.Context, owner, repo string, commentID int, body string) error {
	payload := map[string]string{"body": body}
	resp, err := c.patch(ctx, fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, repo, commentID), payload)
	if err != nil {
		return fmt.Errorf("update issue comment %d: %w", commentID, err)
	}
	resp.Body.Close()
	return nil
}

// DeleteIssueComment deletes an issue comment by its numeric ID.
func (c *LiveClient) DeleteIssueComment(ctx context.Context, owner, repo string, commentID int) error {
	return c.delete_(ctx, fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, repo, commentID))
}

// MinimizeComment minimizes (hides) an issue or review comment via the
// GitHub GraphQL API. The caller provides the GraphQL node ID directly
// (available in IssueComment.NodeID and PullRequestReview.NodeID).
// The reason must be one of: ABUSE, OFF_TOPIC, OUTDATED, RESOLVED,
// DUPLICATE, SPAM.
func (c *LiveClient) MinimizeComment(ctx context.Context, nodeID, reason string) error {
	switch reason {
	case "ABUSE", "OFF_TOPIC", "OUTDATED", "RESOLVED", "DUPLICATE", "SPAM":
	default:
		return fmt.Errorf("minimize comment %s: invalid reason %q", nodeID, reason)
	}

	query := `mutation($id: ID!, $reason: ReportedContentClassifiers!) {
		minimizeComment(input: {subjectId: $id, classifier: $reason}) {
			minimizedComment { isMinimized }
		}
	}`
	gqlPayload := map[string]any{
		"query": query,
		"variables": map[string]string{
			"id":     nodeID,
			"reason": reason,
		},
	}
	gqlResp, err := c.post(ctx, "/graphql", gqlPayload)
	if err != nil {
		return fmt.Errorf("minimize comment %s: %w", nodeID, err)
	}
	var gqlResult struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := decodeJSON(gqlResp, &gqlResult); err != nil {
		return fmt.Errorf("decode minimize response: %w", err)
	}
	if len(gqlResult.Errors) > 0 {
		return fmt.Errorf("minimize comment %s: %s", nodeID, gqlResult.Errors[0].Message)
	}
	return nil
}

// GetPullRequestHeadSHA returns the current HEAD commit SHA of a pull request.
func (c *LiveClient) GetPullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number))
	if err != nil {
		return "", fmt.Errorf("get pull request #%d: %w", number, err)
	}

	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := decodeJSON(resp, &pr); err != nil {
		return "", fmt.Errorf("decode pull request #%d: %w", number, err)
	}
	return pr.Head.SHA, nil
}

// ListPullRequestFiles returns the file paths changed by a pull request.
// GitHub caps PR file lists at 3000 files total regardless of pagination.
func (c *LiveClient) ListPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]string, error) {
	var files []string
	for page := 1; page <= 100; page++ {
		resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100&page=%d", owner, repo, number, page))
		if err != nil {
			return nil, fmt.Errorf("list pull request files page %d: %w", page, err)
		}
		var raw []struct {
			Filename string `json:"filename"`
		}
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decoding pull request files page %d: %w", page, err)
		}
		for _, f := range raw {
			files = append(files, f.Filename)
		}
		if len(raw) < 100 {
			break
		}
	}
	return files, nil
}

// ListPullRequestFileDiffs returns the files changed by a pull request
// along with their unified diff patches. Same API endpoint as
// ListPullRequestFiles but also extracts the patch field.
func (c *LiveClient) ListPullRequestFileDiffs(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestFileDiff, error) {
	var files []forge.PullRequestFileDiff
	for page := 1; page <= 100; page++ {
		resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100&page=%d", owner, repo, number, page))
		if err != nil {
			return nil, fmt.Errorf("list pull request file diffs page %d: %w", page, err)
		}
		var raw []struct {
			Filename string `json:"filename"`
			Patch    string `json:"patch"`
		}
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decoding pull request file diffs page %d: %w", page, err)
		}
		for _, f := range raw {
			files = append(files, forge.PullRequestFileDiff{
				Path:  f.Filename,
				Patch: f.Patch,
			})
		}
		if len(raw) < 100 {
			break
		}
	}
	return files, nil
}

// CreatePullRequestReview submits a formal review on a pull request.
// The event must be one of: APPROVE, REQUEST_CHANGES, COMMENT.
// When commitSHA is non-empty it is sent as commit_id, pinning the
// review to that commit. GitHub rejects the request if the commit is
// not the PR's current HEAD, closing the TOCTOU gap between the
// stale-head check and review submission.
// When comments is non-nil, inline diff comments are attached to the
// review via the GitHub "comments" field.
func (c *LiveClient) CreatePullRequestReview(ctx context.Context, owner, repo string, number int, event, body, commitSHA string, comments []forge.ReviewComment) error {
	switch event {
	case "APPROVE", "REQUEST_CHANGES", "COMMENT":
	default:
		return fmt.Errorf("create review on #%d: invalid event %q", number, event)
	}

	type reviewComment struct {
		Path        string `json:"path"`
		Line        int    `json:"line,omitempty"`
		Body        string `json:"body"`
		SubjectType string `json:"subject_type,omitempty"`
	}

	// GitHub's subject_type: "file" is inferred from Line==0 so forge
	// callers don't need to know about this GitHub-specific field.

	type reviewPayload struct {
		Event    string          `json:"event"`
		Body     string          `json:"body"`
		CommitID string          `json:"commit_id,omitempty"`
		Comments []reviewComment `json:"comments,omitempty"`
	}

	payload := reviewPayload{
		Event:    event,
		Body:     body,
		CommitID: commitSHA,
	}
	for _, rc := range comments {
		c := reviewComment{
			Path: rc.Path,
			Line: rc.Line,
			Body: rc.Body,
		}
		if rc.Line == 0 {
			c.SubjectType = "file"
		}
		payload.Comments = append(payload.Comments, c)
	}

	resp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number), payload)
	if err != nil {
		return fmt.Errorf("create pull request review on #%d: %w", number, err)
	}
	resp.Body.Close()
	return nil
}

// ListPullRequestReviews returns all reviews on a pull request, paginating automatically.
func (c *LiveClient) ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestReview, error) {
	var result []forge.PullRequestReview

	for page := 1; page <= 100; page++ {
		resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100&page=%d", owner, repo, number, page))
		if err != nil {
			return nil, fmt.Errorf("list pull request reviews page %d: %w", page, err)
		}
		var raw []struct {
			ID     int    `json:"id"`
			NodeID string `json:"node_id"`
			User   struct {
				Login string `json:"login"`
			} `json:"user"`
			State       string `json:"state"`
			Body        string `json:"body"`
			SubmittedAt string `json:"submitted_at"`
		}
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decoding pull request reviews page %d: %w", page, err)
		}

		for _, r := range raw {
			result = append(result, forge.PullRequestReview{
				ID:          r.ID,
				NodeID:      r.NodeID,
				User:        r.User.Login,
				State:       r.State,
				Body:        r.Body,
				SubmittedAt: r.SubmittedAt,
			})
		}

		if len(raw) < 100 {
			break
		}
	}
	return result, nil
}

// DismissPullRequestReview dismisses a review, changing its state to DISMISSED.
func (c *LiveClient) DismissPullRequestReview(ctx context.Context, owner, repo string, number, reviewID int, message string) error {
	payload := map[string]string{
		"message": message,
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews/%d/dismissals", owner, repo, number, reviewID)
	resp, err := c.put(ctx, path, payload)
	if err != nil {
		return fmt.Errorf("dismiss review %d on #%d: %w", reviewID, number, err)
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

// UpdatePullRequestBranch updates a PR's head branch by merging the base
// branch into it (GitHub's PUT /repos/{owner}/{repo}/pulls/{number}/update-branch).
// The GitHub API returns 202 Accepted for this endpoint.
func (c *LiveClient) UpdatePullRequestBranch(ctx context.Context, owner, repo string, number int) error {
	resp, err := c.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/%s/pulls/%d/update-branch", owner, repo, number), nil)
	if err != nil {
		return fmt.Errorf("update pull request branch #%d: %w", number, err)
	}
	if err := checkStatus(resp, http.StatusAccepted); err != nil {
		return fmt.Errorf("update pull request branch #%d: %w", number, err)
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
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			Steps      []struct {
				Name       string `json:"name"`
				Number     int    `json:"number"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := decodeJSON(resp, &jobsResult); err != nil {
		return "", fmt.Errorf("decode jobs: %w", err)
	}

	var buf strings.Builder
	for _, job := range jobsResult.Jobs {
		fmt.Fprintf(&buf, "=== %s (job %d) [%s/%s] ===\n", job.Name, job.ID, job.Status, job.Conclusion)
		// Print step-level summary first.
		for _, step := range job.Steps {
			marker := "✓"
			if step.Conclusion == "failure" {
				marker = "✗"
			} else if step.Conclusion == "skipped" {
				marker = "⊘"
			} else if step.Status != "completed" {
				marker = "…"
			}
			fmt.Fprintf(&buf, "  %s Step %d: %s [%s/%s]\n", marker, step.Number, step.Name, step.Status, step.Conclusion)
		}
		fmt.Fprintln(&buf)

		// Download logs for each job (returns plain text, 302 redirect to download URL).
		jobResp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/actions/jobs/%d/logs", owner, repo, job.ID), nil)
		if err != nil {
			fmt.Fprintf(&buf, "[failed to fetch logs: %v]\n\n", err)
			continue
		}
		if jobResp.StatusCode < 200 || jobResp.StatusCode >= 300 {
			jobResp.Body.Close()
			fmt.Fprintf(&buf, "[logs unavailable: HTTP %d]\n\n", jobResp.StatusCode)
			continue
		}
		logData, readErr := io.ReadAll(io.LimitReader(jobResp.Body, 1<<20)) // 1 MB per job
		jobResp.Body.Close()
		if readErr != nil {
			fmt.Fprintf(&buf, "[failed to read logs: %v]\n\n", readErr)
			continue
		}
		fmt.Fprintf(&buf, "%s\n", string(logData))
	}
	return buf.String(), nil
}

// GetWorkflowRunAnnotations returns annotations from all jobs in a workflow run.
// GitHub workflow commands (::notice::, ::warning::) produce check-run
// annotations that are accessible via the check-runs API.
func (c *LiveClient) GetWorkflowRunAnnotations(ctx context.Context, owner, repo string, runID int) ([]forge.Annotation, error) {
	// List jobs for this run.
	resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs", owner, repo, runID))
	if err != nil {
		return nil, fmt.Errorf("list jobs for run %d: %w", runID, err)
	}
	var jobsResult struct {
		Jobs []struct {
			ID int `json:"id"`
		} `json:"jobs"`
	}
	if err := decodeJSON(resp, &jobsResult); err != nil {
		return nil, fmt.Errorf("decode jobs: %w", err)
	}

	var annotations []forge.Annotation
	for _, job := range jobsResult.Jobs {
		resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/check-runs/%d/annotations", owner, repo, job.ID))
		if err != nil {
			continue // best-effort
		}
		var anns []struct {
			Level   string `json:"annotation_level"`
			Message string `json:"message"`
		}
		if err := decodeJSON(resp, &anns); err != nil {
			continue
		}
		for _, a := range anns {
			annotations = append(annotations, forge.Annotation{
				Level:   a.Level,
				Message: a.Message,
			})
		}
	}
	return annotations, nil
}

// ListOrgInstallations lists app installations for an organization.
func (c *LiveClient) ListOrgInstallations(ctx context.Context, org string) ([]forge.Installation, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/orgs/%s/installations?per_page=100", org))
	if err != nil {
		return nil, fmt.Errorf("list org installations: %w", err)
	}

	var result struct {
		Installations []struct {
			ID          int               `json:"id"`
			AppID       int               `json:"app_id"`
			AppSlug     string            `json:"app_slug"`
			Permissions map[string]string `json:"permissions"`
			App         struct {
				Owner struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"app"`
		} `json:"installations"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode installations: %w", err)
	}

	installs := make([]forge.Installation, len(result.Installations))
	for i, inst := range result.Installations {
		installs[i] = forge.Installation{
			ID:            inst.ID,
			AppID:         inst.AppID,
			AppSlug:       inst.AppSlug,
			AppOwnerLogin: inst.App.Owner.Login,
			Permissions:   inst.Permissions,
		}
	}
	return installs, nil
}

func (c *LiveClient) GetAppClientID(ctx context.Context, slug string) (string, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/apps/%s", slug))
	if err != nil {
		return "", fmt.Errorf("get app %s: %w", slug, err)
	}
	var app struct {
		ClientID string `json:"client_id"`
	}
	if err := decodeJSON(resp, &app); err != nil {
		return "", fmt.Errorf("decode app %s: %w", slug, err)
	}
	if app.ClientID == "" {
		return "", fmt.Errorf("app %s has no client_id", slug)
	}
	return app.ClientID, nil
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
		"encrypted_value":         base64.StdEncoding.EncodeToString(encrypted),
		"key_id":                  pubKey.KeyID,
		"visibility":              "selected",
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

// GetOrgSecretRepos returns the repository IDs that have access to an org secret.
func (c *LiveClient) GetOrgSecretRepos(ctx context.Context, org, name string) ([]int64, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/orgs/%s/actions/secrets/%s/repositories", org, name))
	if err != nil {
		return nil, fmt.Errorf("get org secret repos for %s: %w", name, err)
	}
	defer resp.Body.Close()

	var result struct {
		Repositories []struct {
			ID int64 `json:"id"`
		} `json:"repositories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode org secret repos for %s: %w", name, err)
	}

	ids := make([]int64, len(result.Repositories))
	for i, r := range result.Repositories {
		ids[i] = r.ID
	}
	return ids, nil
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

// CreateOrUpdateOrgVariable creates or updates an org-level Actions variable
// scoped to the given repository IDs.
func (c *LiveClient) CreateOrUpdateOrgVariable(ctx context.Context, org, name, value string, selectedRepoIDs []int64) error {
	if selectedRepoIDs == nil {
		selectedRepoIDs = []int64{}
	}

	// Try PATCH first (update existing).
	patchPayload := map[string]any{
		"value":                   value,
		"visibility":              "selected",
		"selected_repository_ids": selectedRepoIDs,
	}

	resp, err := c.patch(ctx, fmt.Sprintf("/orgs/%s/actions/variables/%s", org, name), patchPayload)
	if err == nil {
		resp.Body.Close()
		return nil
	}

	// If the variable doesn't exist (404), create it.
	if !isNotFound(err) {
		return fmt.Errorf("update org variable %s: %w", name, err)
	}

	createPayload := map[string]any{
		"name":                    name,
		"value":                   value,
		"visibility":              "selected",
		"selected_repository_ids": selectedRepoIDs,
	}
	resp2, err := c.post(ctx, fmt.Sprintf("/orgs/%s/actions/variables", org), createPayload)
	if err != nil {
		return fmt.Errorf("create org variable %s: %w", name, err)
	}
	resp2.Body.Close()
	return nil
}

// OrgVariableExists checks if an org-level variable exists.
func (c *LiveClient) OrgVariableExists(ctx context.Context, org, name string) (bool, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/orgs/%s/actions/variables/%s", org, name), nil)
	if err != nil {
		return false, fmt.Errorf("check org variable %s: %w", name, err)
	}
	resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	case http.StatusForbidden:
		return false, &APIError{StatusCode: http.StatusForbidden, Message: "insufficient permissions to check org variable (missing admin:org scope?)"}
	default:
		return false, &APIError{StatusCode: resp.StatusCode, Message: "unexpected status checking org variable"}
	}
}

// DeleteOrgVariable deletes an org-level variable. It is idempotent: a 404
// (variable already gone) is not treated as an error.
func (c *LiveClient) DeleteOrgVariable(ctx context.Context, org, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/orgs/%s/actions/variables/%s", org, name), nil)
	if err != nil {
		return fmt.Errorf("delete org variable %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return &APIError{StatusCode: resp.StatusCode, Message: "unexpected status deleting org variable"}
}

// SetOrgVariableRepos sets the list of repositories that can access an org variable.
func (c *LiveClient) SetOrgVariableRepos(ctx context.Context, org, name string, repoIDs []int64) error {
	if repoIDs == nil {
		repoIDs = []int64{}
	}
	payload := map[string]any{
		"selected_repository_ids": repoIDs,
	}

	resp, err := c.put(ctx, fmt.Sprintf("/orgs/%s/actions/variables/%s/repositories", org, name), payload)
	if err != nil {
		return fmt.Errorf("set org variable repos for %s: %w", name, err)
	}
	resp.Body.Close()
	return nil
}

// GetOrgVariableRepos returns the repository IDs that have access to an org variable.
func (c *LiveClient) GetOrgVariableRepos(ctx context.Context, org, name string) ([]int64, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/orgs/%s/actions/variables/%s/repositories", org, name))
	if err != nil {
		return nil, fmt.Errorf("get org variable repos for %s: %w", name, err)
	}
	defer resp.Body.Close()

	var result struct {
		Repositories []struct {
			ID int64 `json:"id"`
		} `json:"repositories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode org variable repos for %s: %w", name, err)
	}

	ids := make([]int64, len(result.Repositories))
	for i, r := range result.Repositories {
		ids[i] = r.ID
	}
	return ids, nil
}

// isNotFound checks whether an error is a 404 API error.
func isNotFound(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return errors.Is(err, forge.ErrNotFound)
}
