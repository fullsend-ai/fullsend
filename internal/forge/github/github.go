// Package github implements the forge.Client interface for GitHub.
package github

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"golang.org/x/crypto/nacl/box"
)

const (
	// maxResponseBytes caps the size of GitHub API responses we'll read (10 MB).
	maxResponseBytes = 10 * 1024 * 1024

	// maxPages caps pagination to prevent runaway loops.
	maxPages = 100
)

// LiveClient implements forge.Client using the GitHub REST API.
type LiveClient struct {
	http    *http.Client
	token   string
	baseURL string
}

// NewLiveClient creates a forge.Client that talks to the GitHub API.
// The token must have repo, admin:org, and workflow scopes for full
// install functionality.
func NewLiveClient(token string) *LiveClient {
	return &LiveClient{
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		token:   token,
		baseURL: "https://api.github.com",
	}
}

// ListOrgRepos returns all non-archived, non-fork repositories in the org.
func (c *LiveClient) ListOrgRepos(ctx context.Context, org string) ([]forge.Repository, error) {
	var all []forge.Repository

	for page := 1; page <= maxPages; page++ {
		reqURL := fmt.Sprintf("%s/orgs/%s/repos?per_page=100&page=%d&type=all", c.baseURL, url.PathEscape(org), page)

		var repos []forge.Repository
		if err := c.get(ctx, reqURL, &repos); err != nil {
			return nil, fmt.Errorf("listing repos page %d: %w", page, err)
		}

		if len(repos) == 0 {
			break
		}

		for _, r := range repos {
			if !r.Archived && !r.Fork {
				all = append(all, r)
			}
		}

	}

	return all, nil
}

// CreateRepo creates a new repository in the organization.
func (c *LiveClient) CreateRepo(ctx context.Context, org, name, description string, private bool) (*forge.Repository, error) {
	body := map[string]any{
		"name":        name,
		"description": description,
		"private":     private,
		"auto_init":   true, // Create with a README so there's a default branch
	}

	reqURL := fmt.Sprintf("%s/orgs/%s/repos", c.baseURL, url.PathEscape(org))

	var repo forge.Repository
	if err := c.post(ctx, reqURL, body, &repo); err != nil {
		return nil, err
	}

	return &repo, nil
}

// CreateFile creates a file in a repository on the default branch.
func (c *LiveClient) CreateFile(ctx context.Context, owner, repo, path, message string, content []byte) error {
	return c.CreateFileOnBranch(ctx, owner, repo, "", path, message, content)
}

// CreateFileOnBranch creates a file on a specific branch.
// If branch is empty, the file is created on the default branch.
func (c *LiveClient) CreateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error {
	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
	}
	if branch != "" {
		body["branch"] = branch
	}

	reqURL := fmt.Sprintf("%s/repos/%s/%s/contents/%s", c.baseURL, url.PathEscape(owner), url.PathEscape(repo), path)

	var result json.RawMessage
	return c.put(ctx, reqURL, body, &result)
}

// CreateOrUpdateFile creates a file if it doesn't exist, or updates it if it does.
// For updates, it fetches the current file SHA first (required by the Contents API).
func (c *LiveClient) CreateOrUpdateFile(ctx context.Context, owner, repo, path, message string, content []byte) error {
	reqURL := fmt.Sprintf("%s/repos/%s/%s/contents/%s", c.baseURL, url.PathEscape(owner), url.PathEscape(repo), path)

	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
	}

	// Try to get the existing file's SHA
	var existing struct {
		SHA string `json:"sha"`
	}
	if err := c.get(ctx, reqURL, &existing); err == nil && existing.SHA != "" {
		body["sha"] = existing.SHA
	}
	// If get fails (404 = file doesn't exist), proceed without SHA to create it

	var result json.RawMessage
	return c.put(ctx, reqURL, body, &result)
}

// GetAuthenticatedUser returns the login of the currently authenticated user.
func (c *LiveClient) GetAuthenticatedUser(ctx context.Context) (string, error) {
	reqURL := fmt.Sprintf("%s/user", c.baseURL)

	var user struct {
		Login string `json:"login"`
	}
	if err := c.get(ctx, reqURL, &user); err != nil {
		return "", err
	}

	return user.Login, nil
}

// CreateRepoSecret creates or updates an Actions secret on a repository.
// It fetches the repo's public key, encrypts the value with libsodium's
// sealed box, and PUTs the encrypted value to the Actions secrets API.
func (c *LiveClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	// Step 1: Get the repo's public key for secret encryption
	keyURL := fmt.Sprintf("%s/repos/%s/%s/actions/secrets/public-key",
		c.baseURL, url.PathEscape(owner), url.PathEscape(repo))

	var pubKey struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if err := c.get(ctx, keyURL, &pubKey); err != nil {
		return fmt.Errorf("getting repo public key: %w", err)
	}

	// Step 2: Decode the public key from base64
	keyBytes, err := base64.StdEncoding.DecodeString(pubKey.Key)
	if err != nil {
		return fmt.Errorf("decoding public key: %w", err)
	}
	if len(keyBytes) != 32 {
		return fmt.Errorf("unexpected public key length: %d (expected 32)", len(keyBytes))
	}

	var recipientKey [32]byte
	copy(recipientKey[:], keyBytes)

	// Step 3: Encrypt the value using libsodium sealed box
	encrypted, err := box.SealAnonymous(nil, []byte(value), &recipientKey, rand.Reader)
	if err != nil {
		return fmt.Errorf("encrypting secret: %w", err)
	}

	// Step 4: PUT the encrypted secret
	secretURL := fmt.Sprintf("%s/repos/%s/%s/actions/secrets/%s",
		c.baseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(name))

	body := map[string]string{
		"encrypted_value": base64.StdEncoding.EncodeToString(encrypted),
		"key_id":          pubKey.KeyID,
	}

	var result json.RawMessage
	return c.put(ctx, secretURL, body, &result)
}

// RepoSecretExists checks whether an Actions secret exists on a repository.
func (c *LiveClient) RepoSecretExists(ctx context.Context, owner, repo, name string) (bool, error) {
	reqURL := fmt.Sprintf("%s/repos/%s/%s/actions/secrets/%s",
		c.baseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(name))

	var result json.RawMessage
	if err := c.get(ctx, reqURL, &result); err != nil {
		// 404 means the secret doesn't exist
		if apiErr, ok := err.(*apiError); ok && apiErr.StatusCode == 404 {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// CreateBranch creates a new branch from the default branch's HEAD.
func (c *LiveClient) CreateBranch(ctx context.Context, owner, repo, branchName string) error {
	// First, get the default branch SHA
	sha, err := c.getDefaultBranchSHA(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("getting default branch SHA: %w", err)
	}

	body := map[string]any{
		"ref": "refs/heads/" + branchName,
		"sha": sha,
	}

	reqURL := fmt.Sprintf("%s/repos/%s/%s/git/refs", c.baseURL, url.PathEscape(owner), url.PathEscape(repo))

	var result json.RawMessage
	return c.post(ctx, reqURL, body, &result)
}

// CreateChangeProposal creates a PR from head branch to base branch.
func (c *LiveClient) CreateChangeProposal(ctx context.Context, owner, repo, title, body, head, base string) (*forge.ChangeProposal, error) {
	reqBody := map[string]any{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}

	reqURL := fmt.Sprintf("%s/repos/%s/%s/pulls", c.baseURL, url.PathEscape(owner), url.PathEscape(repo))

	var resp struct {
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Number  int    `json:"number"`
	}
	if err := c.post(ctx, reqURL, reqBody, &resp); err != nil {
		return nil, err
	}

	return &forge.ChangeProposal{
		URL:    resp.HTMLURL,
		Title:  resp.Title,
		Number: resp.Number,
	}, nil
}

// GetFileContent retrieves the content of a file from a repository.
func (c *LiveClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	reqURL := fmt.Sprintf("%s/repos/%s/%s/contents/%s",
		c.baseURL, url.PathEscape(owner), url.PathEscape(repo), path)

	var result struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := c.get(ctx, reqURL, &result); err != nil {
		return nil, err
	}

	if result.Encoding != "base64" {
		return nil, fmt.Errorf("unexpected encoding %q", result.Encoding)
	}

	decoded, err := base64.StdEncoding.DecodeString(result.Content)
	if err != nil {
		return nil, fmt.Errorf("decoding base64 content: %w", err)
	}

	return decoded, nil
}

// DeleteRepo deletes a repository. This is irreversible.
func (c *LiveClient) DeleteRepo(ctx context.Context, owner, repo string) error {
	reqURL := fmt.Sprintf("%s/repos/%s/%s",
		c.baseURL, url.PathEscape(owner), url.PathEscape(repo))

	return c.delete(ctx, reqURL)
}

// GetLatestWorkflowRun returns the most recent run of a workflow file.
func (c *LiveClient) GetLatestWorkflowRun(ctx context.Context, owner, repo, workflowFile string) (*forge.WorkflowRun, error) {
	reqURL := fmt.Sprintf("%s/repos/%s/%s/actions/workflows/%s/runs?per_page=1",
		c.baseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(workflowFile))

	var result struct {
		WorkflowRuns []forge.WorkflowRun `json:"workflow_runs"`
	}
	if err := c.get(ctx, reqURL, &result); err != nil {
		return nil, err
	}

	if len(result.WorkflowRuns) == 0 {
		return nil, nil
	}

	return &result.WorkflowRuns[0], nil
}

// GetWorkflowRun returns a specific workflow run by ID.
func (c *LiveClient) GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*forge.WorkflowRun, error) {
	reqURL := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d",
		c.baseURL, url.PathEscape(owner), url.PathEscape(repo), runID)

	var run forge.WorkflowRun
	if err := c.get(ctx, reqURL, &run); err != nil {
		return nil, err
	}

	return &run, nil
}

// ListRepoPullRequests lists open pull requests in a repository.
func (c *LiveClient) ListRepoPullRequests(ctx context.Context, owner, repo string) ([]forge.ChangeProposal, error) {
	reqURL := fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&per_page=100",
		c.baseURL, url.PathEscape(owner), url.PathEscape(repo))

	var prs []struct {
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Number  int    `json:"number"`
	}
	if err := c.get(ctx, reqURL, &prs); err != nil {
		return nil, err
	}

	proposals := make([]forge.ChangeProposal, len(prs))
	for i, pr := range prs {
		proposals[i] = forge.ChangeProposal{
			Number: pr.Number,
			URL:    pr.HTMLURL,
			Title:  pr.Title,
		}
	}

	return proposals, nil
}

func (c *LiveClient) getDefaultBranchSHA(ctx context.Context, owner, repo string) (string, error) {
	repoURL := fmt.Sprintf("%s/repos/%s/%s", c.baseURL, url.PathEscape(owner), url.PathEscape(repo))

	var repoInfo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.get(ctx, repoURL, &repoInfo); err != nil {
		return "", err
	}

	branchURL := fmt.Sprintf("%s/repos/%s/%s/git/ref/heads/%s",
		c.baseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(repoInfo.DefaultBranch))

	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.get(ctx, branchURL, &ref); err != nil {
		return "", err
	}

	return ref.Object.SHA, nil
}

// apiError represents a GitHub API error response.
type apiError struct {
	Message string `json:"message"`
	Errors  []struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"errors"`
	StatusCode int
}

func (e *apiError) Error() string {
	msg := fmt.Sprintf("GitHub API %d: %s", e.StatusCode, e.Message)
	for _, detail := range e.Errors {
		msg += fmt.Sprintf(" (%s: %s)", detail.Code, detail.Message)
	}
	return msg
}

func (c *LiveClient) do(ctx context.Context, method, url string, reqBody any) (*http.Response, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("marshalling request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.http.Do(req)
}

func (c *LiveClient) get(ctx context.Context, url string, result any) error {
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return c.handleResponse(resp, result)
}

func (c *LiveClient) post(ctx context.Context, url string, body any, result any) error {
	resp, err := c.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return c.handleResponse(resp, result)
}

func (c *LiveClient) delete(ctx context.Context, url string) error {
	resp, err := c.do(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return c.handleResponse(resp, nil)
}

func (c *LiveClient) put(ctx context.Context, url string, body any, result any) error {
	resp, err := c.do(ctx, http.MethodPut, url, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	return c.handleResponse(resp, result)
}

func (c *LiveClient) handleResponse(resp *http.Response, result any) error {
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		apiErr := &apiError{StatusCode: resp.StatusCode}
		if json.Unmarshal(respBody, apiErr) != nil {
			apiErr.Message = string(respBody)
		}
		return apiErr
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}

	return nil
}
