package gitlab

import (
	"context"
	"crypto/sha1" //nolint:gosec // Git's blob hash algorithm, not used for security
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// maxTreePages is the pagination safety bound for tree-listing endpoints
// (getTreeMap, ListDirectoryContents, ListRepositoryFiles) and for walking
// first-parent commit history (resolveRootCommitSHA). Set higher than the
// entity-listing cap (100 pages) because file trees and commit histories can
// have orders of magnitude more entries in monorepos.
const maxTreePages = 1000

type treeEntry struct {
	sha  string
	mode string
}

func blobSHA(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *LiveClient) getDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	r, err := c.GetRepo(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	return r.DefaultBranch, nil
}

func (c *LiveClient) getTreeMap(ctx context.Context, owner, repo, ref string) (map[string]treeEntry, error) {
	proj := projectPath(owner, repo)
	result := make(map[string]treeEntry)

	for page := 1; page <= maxTreePages; page++ {
		apiPath := fmt.Sprintf("/projects/%s/repository/tree?ref=%s&recursive=true&per_page=100&page=%d",
			proj, url.QueryEscape(ref), page)
		resp, err := c.get(ctx, apiPath)
		if err != nil {
			if forge.IsNotFound(err) {
				return result, nil
			}
			return nil, err
		}

		nextPage := resp.Header.Get("X-Next-Page")

		var entries []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
			Type string `json:"type"`
			Mode string `json:"mode"`
		}
		if err := decodeJSON(resp, &entries); err != nil {
			return nil, err
		}

		for _, e := range entries {
			if e.Type == "blob" {
				result[e.Path] = treeEntry{sha: e.ID, mode: e.Mode}
			}
		}

		if nextPage == "" || len(entries) < 100 {
			break
		}
	}

	return result, nil
}

func (c *LiveClient) ListOrgRepos(ctx context.Context, org string, includePrivate bool) ([]forge.Repository, error) {
	var result []forge.Repository

	for page := 1; page <= 100; page++ {
		apiPath := fmt.Sprintf("/groups/%s/projects?per_page=100&page=%d&archived=false&with_shared=false&include_subgroups=true",
			url.PathEscape(org), page)
		resp, err := c.get(ctx, apiPath)
		if err != nil {
			return nil, fmt.Errorf("list group projects page %d: %w", page, err)
		}

		var projects []struct {
			ID                int64  `json:"id"`
			Name              string `json:"name"`
			PathWithNamespace string `json:"path_with_namespace"`
			DefaultBranch     string `json:"default_branch"`
			Visibility        string `json:"visibility"`
			Archived          bool   `json:"archived"`
			ForkedFromProject any    `json:"forked_from_project"`
		}
		if err := decodeJSON(resp, &projects); err != nil {
			return nil, fmt.Errorf("decode group projects page %d: %w", page, err)
		}

		for _, p := range projects {
			if p.Archived || p.ForkedFromProject != nil {
				continue
			}
			private := p.Visibility != "public"
			if private && !includePrivate {
				continue
			}
			result = append(result, forge.Repository{
				ID:            p.ID,
				Name:          p.Name,
				FullName:      p.PathWithNamespace,
				DefaultBranch: p.DefaultBranch,
				Private:       private,
				Archived:      false,
				Fork:          false,
			})
		}

		if len(projects) < 100 {
			break
		}
	}

	return result, nil
}

func (c *LiveClient) GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error) {
	proj := projectPath(owner, repo)
	resp, err := c.get(ctx, fmt.Sprintf("/projects/%s", proj))
	if err != nil {
		return nil, fmt.Errorf("get repo %s/%s: %w", owner, repo, err)
	}

	var p struct {
		ID                int64  `json:"id"`
		Name              string `json:"name"`
		PathWithNamespace string `json:"path_with_namespace"`
		DefaultBranch     string `json:"default_branch"`
		Visibility        string `json:"visibility"`
		Archived          bool   `json:"archived"`
		ForkedFromProject any    `json:"forked_from_project"`
	}
	if err := decodeJSON(resp, &p); err != nil {
		return nil, fmt.Errorf("decode repo: %w", err)
	}

	return &forge.Repository{
		ID:            p.ID,
		Name:          p.Name,
		FullName:      p.PathWithNamespace,
		DefaultBranch: p.DefaultBranch,
		Private:       p.Visibility != "public",
		Archived:      p.Archived,
		Fork:          p.ForkedFromProject != nil,
	}, nil
}

func (c *LiveClient) CreateRepo(ctx context.Context, org, name, description string, private bool) (*forge.Repository, error) {
	groupResp, err := c.get(ctx, fmt.Sprintf("/groups/%s", url.PathEscape(org)))
	if err != nil {
		return nil, fmt.Errorf("get group %s: %w", org, err)
	}
	var group struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(groupResp, &group); err != nil {
		return nil, fmt.Errorf("decode group: %w", err)
	}

	visibility := "public"
	if private {
		visibility = "private"
	}

	payload := map[string]any{
		"name":                   name,
		"namespace_id":           group.ID,
		"description":            description,
		"visibility":             visibility,
		"initialize_with_readme": true,
	}

	resp, err := c.post(ctx, "/projects", payload)
	if err != nil {
		return nil, fmt.Errorf("create repo: %w", err)
	}

	var p struct {
		ID                int64  `json:"id"`
		Name              string `json:"name"`
		PathWithNamespace string `json:"path_with_namespace"`
		DefaultBranch     string `json:"default_branch"`
		Visibility        string `json:"visibility"`
	}
	if err := decodeJSON(resp, &p); err != nil {
		return nil, fmt.Errorf("decode create repo response: %w", err)
	}

	return &forge.Repository{
		ID:            p.ID,
		Name:          p.Name,
		FullName:      p.PathWithNamespace,
		DefaultBranch: p.DefaultBranch,
		Private:       p.Visibility != "public",
	}, nil
}

func (c *LiveClient) UpdateRepoVisibility(ctx context.Context, owner, repo string, private bool) error {
	visibility := "public"
	if private {
		visibility = "private"
	}
	body := map[string]string{"visibility": visibility}
	_, err := c.put(ctx, fmt.Sprintf("/projects/%s", projectPath(owner, repo)), body)
	return err
}

func (c *LiveClient) DeleteRepo(ctx context.Context, owner, repo string) error {
	return c.delete_(ctx, fmt.Sprintf("/projects/%s", projectPath(owner, repo)))
}

func (c *LiveClient) FindExistingFork(ctx context.Context, owner, repo string) (string, string, error) {
	proj := projectPath(owner, repo)
	resp, err := c.get(ctx, fmt.Sprintf("/projects/%s/forks?owned=true&per_page=1", proj))
	if err != nil {
		return "", "", fmt.Errorf("find existing fork of %s/%s: %w", owner, repo, err)
	}

	var forks []struct {
		Path      string `json:"path"`
		Namespace struct {
			FullPath string `json:"full_path"`
		} `json:"namespace"`
	}
	if err := decodeJSON(resp, &forks); err != nil {
		return "", "", fmt.Errorf("decode forks: %w", err)
	}

	if len(forks) == 0 {
		return "", "", nil
	}
	return forks[0].Namespace.FullPath, forks[0].Path, nil
}

// CreateFork is idempotent: if a fork already exists (409 Conflict),
// it returns the existing fork's metadata.
func (c *LiveClient) CreateFork(ctx context.Context, owner, repo string) (string, string, error) {
	proj := projectPath(owner, repo)
	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%s/fork", proj), map[string]any{})
	if err != nil {
		return "", "", fmt.Errorf("create fork of %s/%s: %w", owner, repo, err)
	}

	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		forkOwner, forkRepo, err := c.FindExistingFork(ctx, owner, repo)
		if err != nil {
			return "", "", err
		}
		if forkOwner == "" {
			return "", "", fmt.Errorf("create fork of %s/%s: 409 Conflict but no existing fork found", owner, repo)
		}
		return forkOwner, forkRepo, nil
	}

	if err := checkStatus(resp, http.StatusOK, http.StatusCreated); err != nil {
		return "", "", fmt.Errorf("create fork of %s/%s: %w", owner, repo, err)
	}

	var fork struct {
		Path      string `json:"path"`
		Namespace struct {
			FullPath string `json:"full_path"`
		} `json:"namespace"`
	}
	if err := decodeJSON(resp, &fork); err != nil {
		return "", "", fmt.Errorf("decode fork response: %w", err)
	}
	return fork.Namespace.FullPath, fork.Path, nil
}

// CreateForkInOrg creates a fork of the given repository under the specified
// GitLab group (namespace) with the given name.
func (c *LiveClient) CreateForkInOrg(ctx context.Context, owner, repo, org, forkName string) (string, error) {
	proj := projectPath(owner, repo)
	body := map[string]string{
		"namespace_path": org,
		"name":           forkName,
		"path":           forkName,
	}
	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%s/fork", proj), body)
	if err != nil {
		return "", fmt.Errorf("create fork of %s/%s in %s: %w", owner, repo, org, err)
	}

	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		// Check if the existing repo is actually a fork of the source.
		existingPath := fmt.Sprintf("/projects/%s", projectPath(org, forkName))
		existingResp, err := c.get(ctx, existingPath)
		if err != nil {
			return "", fmt.Errorf("check existing repo %s/%s: %w", org, forkName, err)
		}
		var existing struct {
			Path              string `json:"path"`
			ForkedFromProject *struct {
				PathWithNamespace string `json:"path_with_namespace"`
			} `json:"forked_from_project"`
		}
		if err := decodeJSON(existingResp, &existing); err != nil {
			return "", fmt.Errorf("decode existing repo: %w", err)
		}
		sourcePath := owner + "/" + repo
		if existing.ForkedFromProject == nil || !strings.EqualFold(existing.ForkedFromProject.PathWithNamespace, sourcePath) {
			return "", forge.ErrNotFork
		}
		return existing.Path, nil
	}

	if err := checkStatus(resp, http.StatusOK, http.StatusCreated); err != nil {
		return "", fmt.Errorf("create fork of %s/%s in %s: %w", owner, repo, org, err)
	}

	var fork struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(resp, &fork); err != nil {
		return "", fmt.Errorf("decode fork response: %w", err)
	}
	return fork.Path, nil
}

func (c *LiveClient) GetBranchRef(ctx context.Context, owner, repo, branch string) (string, error) {
	proj := projectPath(owner, repo)
	resp, err := c.get(ctx, fmt.Sprintf("/projects/%s/repository/branches/%s", proj, url.PathEscape(branch)))
	if err != nil {
		return "", fmt.Errorf("get branch ref %s/%s@%s: %w", owner, repo, branch, err)
	}
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := decodeJSON(resp, &b); err != nil {
		return "", fmt.Errorf("decode branch: %w", err)
	}
	return b.Commit.ID, nil
}

func (c *LiveClient) CreateBranch(ctx context.Context, owner, repo, branchName string) error {
	defaultBranch, err := c.getDefaultBranch(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("get default branch: %w", err)
	}

	proj := projectPath(owner, repo)
	payload := map[string]string{
		"branch": branchName,
		"ref":    defaultBranch,
	}
	resp, err := c.post(ctx, fmt.Sprintf("/projects/%s/repository/branches", proj), payload)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest &&
			strings.Contains(strings.ToLower(apiErr.Message), "already exists") {
			return fmt.Errorf("create branch %s: %w: %w", branchName, forge.ErrAlreadyExists, err)
		}
		return fmt.Errorf("create branch %s: %w", branchName, err)
	}
	resp.Body.Close()
	return nil
}

// CreateBranchFromSHA creates a new branch pointing at the given commit SHA.
// GitLab's branch creation API accepts a commit SHA as the ref parameter.
// Returns forge.ErrAlreadyExists if the branch already exists.
func (c *LiveClient) CreateBranchFromSHA(ctx context.Context, owner, repo, branchName, sha string) error {
	proj := projectPath(owner, repo)
	payload := map[string]string{
		"branch": branchName,
		"ref":    sha,
	}
	resp, err := c.post(ctx, fmt.Sprintf("/projects/%s/repository/branches", proj), payload)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest &&
			strings.Contains(strings.ToLower(apiErr.Message), "already exists") {
			return fmt.Errorf("create branch %s from SHA: %w: %w", branchName, forge.ErrAlreadyExists, err)
		}
		return fmt.Errorf("create branch %s from SHA: %w", branchName, err)
	}
	resp.Body.Close()
	return nil
}

// DeleteBranch deletes a git branch. Returns forge.ErrNotFound (wrapped)
// if the branch does not exist.
func (c *LiveClient) DeleteBranch(ctx context.Context, owner, repo, branchName string) error {
	return c.DeleteRef(ctx, owner, repo, "heads/"+branchName)
}

// DeleteRef deletes a git ref via the GitLab Branches or Tags API.
// refPath must be in the form "heads/<branch>" or "tags/<tag>".
// Returns forge.ErrNotFound (wrapped) if the ref does not exist.
func (c *LiveClient) DeleteRef(ctx context.Context, owner, repo, refPath string) error {
	proj := projectPath(owner, repo)

	if after, ok := strings.CutPrefix(refPath, "heads/"); ok {
		apiPath := fmt.Sprintf("/projects/%s/repository/branches/%s", proj, url.PathEscape(after))
		return c.delete_(ctx, apiPath)
	}
	if after, ok := strings.CutPrefix(refPath, "tags/"); ok {
		apiPath := fmt.Sprintf("/projects/%s/repository/tags/%s", proj, url.PathEscape(after))
		return c.delete_(ctx, apiPath)
	}

	return fmt.Errorf("delete ref %s in %s/%s: unsupported ref path format (expected heads/ or tags/ prefix)", refPath, owner, repo)
}

// GetRef maps forge-style ref paths ("heads/main", "tags/v1") to GitLab
// commit lookups. GitLab's commits endpoint accepts branch names, tag
// names, and SHAs directly.
func (c *LiveClient) GetRef(ctx context.Context, owner, repo, refPath string) (string, error) {
	ref := refPath
	if after, ok := strings.CutPrefix(refPath, "heads/"); ok {
		ref = after
	} else if after, ok := strings.CutPrefix(refPath, "tags/"); ok {
		ref = after
	}

	proj := projectPath(owner, repo)
	resp, err := c.get(ctx, fmt.Sprintf("/projects/%s/repository/commits/%s", proj, url.PathEscape(ref)))
	if err != nil {
		return "", fmt.Errorf("get ref %s/%s@%s: %w", owner, repo, refPath, err)
	}
	var commit struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(resp, &commit); err != nil {
		return "", fmt.Errorf("decode commit: %w", err)
	}
	return commit.ID, nil
}

// CompareCommits compares two commits and returns their relationship status:
// "ahead" (head is ahead of base), "behind" (head is behind base),
// "identical" (same commit), or "diverged" (no linear relationship).
//
// GitLab's compare endpoint is not symmetric — it only reports commits
// reachable from "to" but not from "from". A single forward call cannot
// distinguish "ahead" from "diverged" because both cases return non-empty
// commits. To resolve the ambiguity, a reverse comparison (from=head,
// to=base) is always made when the forward call returns any diffs or
// commits. When both directions have commits, the history has diverged.
func (c *LiveClient) CompareCommits(ctx context.Context, owner, repo, base, head string) (string, error) {
	proj := projectPath(owner, repo)

	fwd, err := c.compareOnce(ctx, proj, owner, repo, base, head)
	if err != nil {
		return "", err
	}

	// No diffs and no commits → same commit.
	if len(fwd.Commits) == 0 && len(fwd.Diffs) == 0 {
		return "identical", nil
	}

	// Forward returned something — need the reverse direction to
	// distinguish ahead / behind / diverged.
	rev, err := c.compareOnce(ctx, proj, owner, repo, head, base)
	if err != nil {
		// If the reverse call fails, degrade to "diverged" rather than
		// blocking the caller.
		return "diverged", nil
	}

	hasFwd := len(fwd.Commits) > 0
	hasRev := len(rev.Commits) > 0

	switch {
	case hasFwd && hasRev:
		return "diverged", nil
	case hasFwd:
		return "ahead", nil
	case hasRev:
		return "behind", nil
	default:
		// Diffs exist but neither direction has commits — degrade to
		// diverged.
		return "diverged", nil
	}
}

// compareResult holds the subset of GitLab's compare response we need.
type compareResult struct {
	Commits []struct{ ID string }      `json:"commits"`
	Diffs   []struct{ NewPath string } `json:"diffs"`
}

// compareOnce performs a single GitLab repository compare API call.
func (c *LiveClient) compareOnce(ctx context.Context, proj, owner, repo, from, to string) (*compareResult, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/projects/%s/repository/compare?from=%s&to=%s",
		proj, url.QueryEscape(from), url.QueryEscape(to)))
	if err != nil {
		return nil, fmt.Errorf("compare commits %s/%s %s...%s: %w", owner, repo, from, to, err)
	}
	var cmp compareResult
	if err := decodeJSON(resp, &cmp); err != nil {
		return nil, fmt.Errorf("decode compare response: %w", err)
	}
	return &cmp, nil
}

func (c *LiveClient) CreateFile(ctx context.Context, owner, repo, path, message string, content []byte) error {
	branch, err := c.getDefaultBranch(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("get default branch: %w", err)
	}
	return c.CreateFileOnBranch(ctx, owner, repo, branch, path, message, content)
}

func (c *LiveClient) CreateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error {
	proj := projectPath(owner, repo)
	apiPath := fmt.Sprintf("/projects/%s/repository/files/%s", proj, url.PathEscape(path))
	payload := map[string]string{
		"branch":         branch,
		"content":        base64.StdEncoding.EncodeToString(content),
		"encoding":       "base64",
		"commit_message": message,
	}
	resp, err := c.post(ctx, apiPath, payload)
	if err != nil {
		return fmt.Errorf("create file %s: %w", path, err)
	}
	resp.Body.Close()
	return nil
}

// CreateOrUpdateFile tries a POST (create); on 400 "already exists" it
// falls back to PUT (update). GitLab's file API does not require a SHA
// for updates, unlike GitHub.
func (c *LiveClient) CreateOrUpdateFile(ctx context.Context, owner, repo, path, message string, content []byte) error {
	branch, err := c.getDefaultBranch(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("get default branch: %w", err)
	}
	return c.CreateOrUpdateFileOnBranch(ctx, owner, repo, branch, path, message, content)
}

func (c *LiveClient) CreateOrUpdateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error {
	proj := projectPath(owner, repo)
	apiPath := fmt.Sprintf("/projects/%s/repository/files/%s", proj, url.PathEscape(path))
	payload := map[string]string{
		"branch":         branch,
		"content":        base64.StdEncoding.EncodeToString(content),
		"encoding":       "base64",
		"commit_message": message,
	}

	resp, err := c.do(ctx, http.MethodPost, apiPath, payload)
	if err != nil {
		return fmt.Errorf("create file %s: %w", path, err)
	}
	if err := checkStatus(resp, http.StatusCreated); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest &&
			strings.Contains(strings.ToLower(apiErr.Message), "already exists") {
			updateResp, updateErr := c.put(ctx, apiPath, payload)
			if updateErr != nil {
				return fmt.Errorf("update file %s: %w", path, updateErr)
			}
			updateResp.Body.Close()
			return nil
		}
		return fmt.Errorf("create file %s: %w", path, err)
	}
	resp.Body.Close()
	return nil
}

func (c *LiveClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	return c.GetFileContentAtRef(ctx, owner, repo, path, "HEAD")
}

func (c *LiveClient) GetFileContentAtRef(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	proj := projectPath(owner, repo)
	apiPath := fmt.Sprintf("/projects/%s/repository/files/%s?ref=%s",
		proj, url.PathEscape(path), url.QueryEscape(ref))
	resp, err := c.get(ctx, apiPath)
	if err != nil {
		// GitLab 404 unwraps to forge.ErrNotFound via APIError.Unwrap, so
		// a missing state file is distinguishable from other failures.
		return nil, fmt.Errorf("get file content: %w", err)
	}

	var file struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := decodeJSON(resp, &file); err != nil {
		return nil, fmt.Errorf("decode file content: %w", err)
	}

	if file.Encoding != "base64" {
		return []byte(file.Content), nil
	}
	data, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return nil, fmt.Errorf("decode base64 content: %w", err)
	}
	return data, nil
}

func (c *LiveClient) DeleteFile(ctx context.Context, owner, repo, path, message string) error {
	branch, err := c.getDefaultBranch(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("get default branch: %w", err)
	}
	return c.deleteFileOnBranch(ctx, owner, repo, branch, path, message)
}

func (c *LiveClient) deleteFileOnBranch(ctx context.Context, owner, repo, branch, path, message string) error {
	proj := projectPath(owner, repo)
	apiPath := fmt.Sprintf("/projects/%s/repository/files/%s", proj, url.PathEscape(path))
	payload := map[string]string{
		"branch":         branch,
		"commit_message": message,
	}
	resp, err := c.do(ctx, http.MethodDelete, apiPath, payload)
	if err != nil {
		return fmt.Errorf("delete file %s: %w", path, err)
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusNoContent)
}

func (c *LiveClient) DeleteFiles(ctx context.Context, owner, repo, message string, paths []string) (int, error) {
	if len(paths) == 0 {
		return 0, nil
	}

	branch, err := c.getDefaultBranch(ctx, owner, repo)
	if err != nil {
		return 0, fmt.Errorf("get default branch: %w", err)
	}

	existing, err := c.getTreeMap(ctx, owner, repo, branch)
	if err != nil {
		return 0, fmt.Errorf("get tree for delete: %w", err)
	}

	var actions []map[string]any
	for _, p := range paths {
		if _, ok := existing[p]; !ok {
			continue
		}
		actions = append(actions, map[string]any{
			"action":    "delete",
			"file_path": p,
		})
	}

	if len(actions) == 0 {
		return 0, nil
	}

	proj := projectPath(owner, repo)
	payload := map[string]any{
		"branch":         branch,
		"commit_message": message,
		"actions":        actions,
	}
	resp, err := c.post(ctx, fmt.Sprintf("/projects/%s/repository/commits", proj), payload)
	if err != nil {
		return 0, fmt.Errorf("delete files commit: %w", err)
	}
	resp.Body.Close()

	return len(actions), nil
}

func (c *LiveClient) ListDirectoryContents(ctx context.Context, owner, repo, path, ref string, recursive bool) ([]forge.DirectoryEntry, error) {
	proj := projectPath(owner, repo)

	params := url.Values{}
	if path != "" {
		params.Set("path", path)
	}
	params.Set("ref", ref)
	if recursive {
		params.Set("recursive", "true")
	}
	params.Set("per_page", "100")

	var result []forge.DirectoryEntry
	for page := 1; page <= maxTreePages; page++ {
		params.Set("page", fmt.Sprintf("%d", page))
		apiPath := fmt.Sprintf("/projects/%s/repository/tree?%s", proj, params.Encode())

		resp, err := c.get(ctx, apiPath)
		if err != nil {
			return nil, fmt.Errorf("list directory: %w", err)
		}

		nextPage := resp.Header.Get("X-Next-Page")

		var entries []struct {
			Name string `json:"name"`
			Path string `json:"path"`
			Type string `json:"type"`
		}
		if err := decodeJSON(resp, &entries); err != nil {
			return nil, fmt.Errorf("decode directory listing: %w", err)
		}

		for _, e := range entries {
			if e.Type != "blob" && e.Type != "tree" {
				continue
			}

			relPath := e.Path
			if path != "" {
				relPath = strings.TrimPrefix(e.Path, path+"/")
			}

			entryType := "file"
			if e.Type == "tree" {
				entryType = "dir"
			}

			result = append(result, forge.DirectoryEntry{
				Path: relPath,
				Type: entryType,
			})
		}

		if nextPage == "" || len(entries) < 100 {
			break
		}
	}

	return result, nil
}

func (c *LiveClient) ListRepositoryFiles(ctx context.Context, owner, repo string) ([]string, error) {
	proj := projectPath(owner, repo)
	var paths []string

	for page := 1; page <= maxTreePages; page++ {
		apiPath := fmt.Sprintf("/projects/%s/repository/tree?recursive=true&per_page=100&page=%d", proj, page)
		resp, err := c.get(ctx, apiPath)
		if err != nil {
			return nil, fmt.Errorf("list repository files: %w", err)
		}

		nextPage := resp.Header.Get("X-Next-Page")

		var entries []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		}
		if err := decodeJSON(resp, &entries); err != nil {
			return nil, fmt.Errorf("decode tree: %w", err)
		}

		for _, e := range entries {
			if e.Type == "blob" {
				paths = append(paths, e.Path)
			}
		}

		if nextPage == "" || len(entries) < 100 {
			break
		}
	}

	return paths, nil
}

const skipCIMarker = "[skip ci]"

func withSkipCI(message string) string {
	if strings.Contains(message, skipCIMarker) {
		return message
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return skipCIMarker
	}
	return message + " " + skipCIMarker
}

// commitOptions controls optional GitLab Commits API fields used by
// force-re-root writes. Zero value preserves the existing non-force path.
type commitOptions struct {
	force    bool
	startSHA string
}

// ForceCommitFileToBranch force-updates branch to a single-file commit
// re-rooted on the repository's root commit. The branch is created if it
// does not exist. Each call leaves the branch at base+1 commit (prior
// force-commits become unreachable). The commit message is suffixed with
// [skip ci] when not already present.
func (c *LiveClient) ForceCommitFileToBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error {
	if branch == "" || path == "" {
		return fmt.Errorf("force commit: branch and path are required")
	}
	startSHA, err := c.resolveRootCommitSHA(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("resolve force-commit base: %w", err)
	}
	_, err = c.commitFilesImpl(ctx, owner, repo, branch, withSkipCI(message), []forge.TreeFile{
		{Path: path, Content: content, Mode: "100644"},
	}, commitOptions{force: true, startSHA: startSHA})
	if err != nil {
		return fmt.Errorf("force commit %s to %s: %w", path, branch, err)
	}
	return nil
}

// resolveRootCommitSHA finds the repository's DAG root commit (the fixed
// base for force-re-root writes). The commits-list endpoint does not
// support order_by/sort (those belong to other GitLab endpoints); it only
// supports first_parent plus standard offset pagination. So the root is
// found by walking first-parent history to its last page and taking that
// page's last entry.
func (c *LiveClient) resolveRootCommitSHA(ctx context.Context, owner, repo string) (string, error) {
	branch, err := c.getDefaultBranch(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("get default branch: %w", err)
	}
	proj := projectPath(owner, repo)

	var root string
	for page := 1; page <= maxTreePages; page++ {
		params := url.Values{}
		if branch != "" {
			params.Set("ref_name", branch)
		}
		params.Set("first_parent", "true")
		params.Set("per_page", "100")
		params.Set("page", fmt.Sprintf("%d", page))
		resp, err := c.get(ctx, fmt.Sprintf("/projects/%s/repository/commits?%s", proj, params.Encode()))
		if err != nil {
			return "", fmt.Errorf("list root commits: %w", err)
		}

		nextPage := resp.Header.Get("X-Next-Page")

		var commits []struct {
			ID string `json:"id"`
		}
		if err := decodeJSON(resp, &commits); err != nil {
			return "", fmt.Errorf("decode commits: %w", err)
		}
		if len(commits) == 0 {
			break
		}
		root = commits[len(commits)-1].ID

		if nextPage == "" || len(commits) < 100 {
			break
		}
	}

	if root == "" {
		return "", fmt.Errorf("%w: no root commit for %s/%s", forge.ErrNotFound, owner, repo)
	}
	return root, nil
}

// CommitFiles atomically commits multiple files to the default branch
// via GitLab's Commits API. Returns (false, nil) when all files already
// match the current tree (idempotent).
func (c *LiveClient) CommitFiles(ctx context.Context, owner, repo, message string, files []forge.TreeFile) (bool, error) {
	if len(files) == 0 {
		return false, nil
	}
	branch, err := c.getDefaultBranch(ctx, owner, repo)
	if err != nil {
		return false, fmt.Errorf("get default branch: %w", err)
	}
	return c.commitFilesImpl(ctx, owner, repo, branch, message, files, commitOptions{})
}

func (c *LiveClient) CommitFilesToBranch(ctx context.Context, owner, repo, branch, message string, files []forge.TreeFile) (bool, error) {
	if len(files) == 0 {
		return false, nil
	}
	return c.commitFilesImpl(ctx, owner, repo, branch, message, files, commitOptions{})
}

// commitFilesImpl reads the tree, computes a diff, and POSTs a commit.
// This is a non-atomic read-modify-write; concurrent branch updates may
// cause a 409 Conflict (mapped to ErrNonFastForward). The GitHub client
// shares this structural pattern.
//
// When opts.force is set with opts.startSHA, the commit is re-rooted on
// that SHA and the target branch is force-updated (created if absent).
// Actions are applied to the start SHA's tree, not the target branch.
func (c *LiveClient) commitFilesImpl(ctx context.Context, owner, repo, branch, message string, files []forge.TreeFile, opts commitOptions) (bool, error) {
	var existing map[string]treeEntry
	if opts.force && opts.startSHA != "" {
		// Actions apply to start_sha's tree (the fixed base), not the
		// target branch. The base typically does not contain the file,
		// so treating the tree as empty yields "create" actions — the
		// correct force-re-root shape. Reading the target branch would
		// send "update" and GitLab would 400 because the file is absent
		// from start_sha.
		existing = map[string]treeEntry{}
	} else {
		var err error
		existing, err = c.getTreeMap(ctx, owner, repo, branch)
		if err != nil {
			return false, fmt.Errorf("get tree: %w", err)
		}
	}

	var actions []map[string]any
	for _, f := range files {
		if f.Delete {
			if _, ok := existing[f.Path]; !ok {
				continue
			}
			actions = append(actions, map[string]any{
				"action":    "delete",
				"file_path": f.Path,
			})
			continue
		}

		expectedSHA := blobSHA(f.Content)
		info, exists := existing[f.Path]
		if exists && info.sha == expectedSHA && info.mode == f.Mode {
			continue
		}

		action := "create"
		if exists {
			action = "update"
		}

		entry := map[string]any{
			"action":    action,
			"file_path": f.Path,
			"content":   base64.StdEncoding.EncodeToString(f.Content),
			"encoding":  "base64",
		}
		if f.Mode == "100755" {
			entry["execute_filemode"] = true
		} else if exists && info.mode == "100755" {
			entry["execute_filemode"] = false
		}

		actions = append(actions, entry)
	}

	if len(actions) == 0 {
		return false, nil
	}

	proj := projectPath(owner, repo)
	payload := map[string]any{
		"branch":         branch,
		"commit_message": message,
		"actions":        actions,
	}
	if opts.force {
		payload["force"] = true
	}
	if opts.startSHA != "" {
		payload["start_sha"] = opts.startSHA
	}

	resp, err := c.post(ctx, fmt.Sprintf("/projects/%s/repository/commits", proj), payload)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			msg := strings.ToLower(apiErr.Message)
			if apiErr.StatusCode == http.StatusForbidden &&
				(strings.Contains(msg, "protected") || strings.Contains(msg, "not allowed to push")) {
				return false, fmt.Errorf("%w: %w", forge.ErrBranchProtected, err)
			}
			if apiErr.StatusCode == http.StatusConflict &&
				!strings.Contains(msg, "already exists") {
				return false, fmt.Errorf("%w: %w", forge.ErrNonFastForward, err)
			}
		}
		return false, fmt.Errorf("create commit: %w", err)
	}
	resp.Body.Close()

	return true, nil
}
