package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// --- forks -----------------------------------------------------------------

// FindExistingFork implements forge.Client.
func (c *LiveClient) FindExistingFork(ctx context.Context, owner, repo string) (string, string, error) {
	user, err := c.GetAuthenticatedUser(ctx)
	if err != nil {
		return "", "", err
	}
	var r apiRepo
	code, err := c.do(ctx, http.MethodGet, "/repos/"+escapePath(user, repo), nil, &r)
	if err != nil {
		if code == http.StatusNotFound || forge.IsNotFound(err) {
			return "", "", nil
		}
		return "", "", err
	}
	if r.Fork {
		// Parent may be present on Forgejo fork payloads.
		parent := ""
		var full struct {
			Fork   bool `json:"fork"`
			Parent *struct {
				FullName string `json:"full_name"`
			} `json:"parent"`
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		}
		if _, err := c.do(ctx, http.MethodGet, "/repos/"+escapePath(user, repo), nil, &full); err == nil {
			if full.Parent != nil {
				parent = full.Parent.FullName
			}
			if full.Fork && parent == owner+"/"+repo {
				return full.Owner.Login, full.Name, nil
			}
		}
	}
	return "", "", nil
}

// CreateFork implements forge.Client.
func (c *LiveClient) CreateFork(ctx context.Context, owner, repo string) (string, string, error) {
	var fork apiRepo
	if _, err := c.do(ctx, http.MethodPost, "/repos/"+escapePath(owner, repo)+"/forks", map[string]any{}, &fork); err != nil {
		return "", "", err
	}
	return fork.Owner.Login, fork.Name, nil
}

// CreateForkInOrg implements forge.Client.
func (c *LiveClient) CreateForkInOrg(ctx context.Context, owner, repo, org, forkName string) (string, error) {
	source := owner + "/" + repo
	var existing apiRepo
	code, err := c.do(ctx, http.MethodGet, "/repos/"+escapePath(org, forkName), nil, &existing)
	if err == nil && code == http.StatusOK {
		var full struct {
			Fork   bool `json:"fork"`
			Name   string `json:"name"`
			Parent *struct {
				FullName string `json:"full_name"`
			} `json:"parent"`
		}
		if _, e2 := c.do(ctx, http.MethodGet, "/repos/"+escapePath(org, forkName), nil, &full); e2 == nil {
			if !full.Fork {
				return "", fmt.Errorf("repo %s/%s already exists and is not a fork: %w", org, forkName, forge.ErrNotFork)
			}
			if full.Parent != nil && full.Parent.FullName != source {
				return "", fmt.Errorf("repo %s/%s is a fork of %s, not %s: %w", org, forkName, full.Parent.FullName, source, forge.ErrNotFork)
			}
			return full.Name, nil
		}
	}
	payload := map[string]any{"organization": org}
	if forkName != "" {
		payload["name"] = forkName
	}
	var fork apiRepo
	if _, err := c.do(ctx, http.MethodPost, "/repos/"+escapePath(owner, repo)+"/forks", payload, &fork); err != nil {
		return "", err
	}
	return fork.Name, nil
}

// --- file commits ----------------------------------------------------------

// UpdateFileIfMatch implements forge.Client.
func (c *LiveClient) UpdateFileIfMatch(ctx context.Context, owner, repo, path, message string, content []byte, expectedSHA string) error {
	ct, err := c.getContents(ctx, owner, repo, path, "")
	if err != nil {
		return err
	}
	if ct.SHA != expectedSHA {
		return fmt.Errorf("update %s: sha mismatch want %s got %s: %w", path, expectedSHA, ct.SHA, forge.ErrConflict)
	}
	return c.putFile(ctx, owner, repo, path, message, "", content, expectedSHA)
}

// DeleteFiles implements forge.Client.
func (c *LiveClient) DeleteFiles(ctx context.Context, owner, repo, message string, paths []string) (int, error) {
	n := 0
	for _, p := range paths {
		if err := c.DeleteFile(ctx, owner, repo, p, message); err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return n, err
		}
		n++
	}
	return n, nil
}

// CommitFiles implements forge.Client via sequential contents API (not atomic tree).
func (c *LiveClient) CommitFiles(ctx context.Context, owner, repo, message string, files []forge.TreeFile) (bool, error) {
	r, err := c.GetRepo(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	return c.CommitFilesToBranch(ctx, owner, repo, r.DefaultBranch, message, files)
}

// CommitFilesToBranch implements forge.Client via sequential contents API.
func (c *LiveClient) CommitFilesToBranch(ctx context.Context, owner, repo, branch, message string, files []forge.TreeFile) (bool, error) {
	if len(files) == 0 {
		return false, nil
	}
	changed := false
	for _, f := range files {
		if f.Delete {
			ct, err := c.getContents(ctx, owner, repo, f.Path, branch)
			if err != nil {
				if forge.IsNotFound(err) {
					continue
				}
				return changed, err
			}
			payload := map[string]any{"message": message, "sha": ct.SHA, "branch": branch}
			p := "/repos/" + escapePath(owner, repo) + "/contents/" + strings.TrimPrefix(f.Path, "/")
			if _, err := c.do(ctx, http.MethodDelete, p, payload, nil); err != nil {
				return changed, err
			}
			changed = true
			continue
		}
		content, err := f.Bytes()
		if err != nil {
			return changed, err
		}
		if err := c.CreateOrUpdateFileOnBranch(ctx, owner, repo, branch, f.Path, message, content); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// ForceCommitFileToBranch implements forge.Client.
func (c *LiveClient) ForceCommitFileToBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error {
	return c.CreateOrUpdateFileOnBranch(ctx, owner, repo, branch, path, message, content)
}

// CreateCrossRepoChangeProposal implements forge.Client.
func (c *LiveClient) CreateCrossRepoChangeProposal(ctx context.Context, baseOwner, baseRepo, headOwner, headRepo, title, body, headBranch, baseBranch string) (*forge.ChangeProposal, error) {
	_ = headRepo // Forgejo head uses owner:branch; head repo name must match fork naming.
	head := headOwner + ":" + headBranch
	return c.CreateChangeProposal(ctx, baseOwner, baseRepo, title, body, head, baseBranch)
}

// IsInstallationToken implements forge.Client — Forgejo personal tokens are not GH App installs.
func (c *LiveClient) IsInstallationToken(ctx context.Context) (bool, error) {
	if _, err := c.GetAuthenticatedUser(ctx); err != nil {
		return false, err
	}
	return false, nil
}

// --- comments / labels -----------------------------------------------------

// GetIssueComment implements forge.Client.
func (c *LiveClient) GetIssueComment(ctx context.Context, owner, repo string, commentID int) (*forge.IssueComment, error) {
	var raw struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Created string `json:"created_at"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/issues/comments/" + fmt.Sprintf("%d", commentID)
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	return &forge.IssueComment{ID: raw.ID, Body: raw.Body, Author: raw.User.Login, CreatedAt: raw.Created}, nil
}

// UpdateIssueComment implements forge.Client.
func (c *LiveClient) UpdateIssueComment(ctx context.Context, owner, repo string, commentID int, body string) error {
	p := "/repos/" + escapePath(owner, repo) + "/issues/comments/" + fmt.Sprintf("%d", commentID)
	_, err := c.do(ctx, http.MethodPatch, p, map[string]any{"body": body}, nil)
	return err
}

// DeleteIssueComment implements forge.Client.
func (c *LiveClient) DeleteIssueComment(ctx context.Context, owner, repo string, commentID int) error {
	p := "/repos/" + escapePath(owner, repo) + "/issues/comments/" + fmt.Sprintf("%d", commentID)
	_, err := c.do(ctx, http.MethodDelete, p, nil, nil)
	return err
}

// RemoveIssueLabel implements forge.Client.
func (c *LiveClient) RemoveIssueLabel(ctx context.Context, owner, repo string, number int, label string) error {
	p := "/repos/" + escapePath(owner, repo) + "/issues/" + fmt.Sprintf("%d", number) + "/labels/" + url.PathEscape(label)
	_, err := c.do(ctx, http.MethodDelete, p, nil, nil)
	return err
}

// --- pull request review ---------------------------------------------------

// ListPullRequestCommits implements forge.Client.
func (c *LiveClient) ListPullRequestCommits(ctx context.Context, owner, repo string, number int) ([]string, error) {
	var raw []struct {
		SHA string `json:"sha"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/pulls/" + fmt.Sprintf("%d", number) + "/commits?limit=100"
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw))
	for _, cmt := range raw {
		out = append(out, cmt.SHA)
	}
	return out, nil
}

// ListPullRequestFileDiffs implements forge.Client.
func (c *LiveClient) ListPullRequestFileDiffs(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestFileDiff, error) {
	var raw []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Patch     string `json:"patch"`
		Previous  string `json:"previous_filename"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/pulls/" + fmt.Sprintf("%d", number) + "/files?limit=100"
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]forge.PullRequestFileDiff, 0, len(raw))
	for _, f := range raw {
		st := f.Status
		switch st {
		case "added", "removed", "renamed", "modified", "copied":
		default:
			st = "modified"
		}
		out = append(out, forge.PullRequestFileDiff{
			Path:         f.Filename,
			PreviousPath: f.Previous,
			Status:       st,
			Patch:        f.Patch,
		})
	}
	return out, nil
}

// CreatePullRequestReview implements forge.Client.
func (c *LiveClient) CreatePullRequestReview(ctx context.Context, owner, repo string, number int, event, body, commitSHA string, comments []forge.ReviewComment) error {
	switch event {
	case "APPROVE", "REQUEST_CHANGES", "COMMENT":
	default:
		return fmt.Errorf("create review on #%d: invalid event %q", number, event)
	}
	payload := map[string]any{
		"event":  event,
		"body":   body,
	}
	if commitSHA != "" {
		payload["commit_id"] = commitSHA
	}
	if len(comments) > 0 {
		cms := make([]map[string]any, 0, len(comments))
		for _, rc := range comments {
			cm := map[string]any{"path": rc.Path, "body": rc.Body}
			if rc.Line > 0 {
				cm["new_position"] = rc.Line
			}
			cms = append(cms, cm)
		}
		payload["comments"] = cms
	}
	p := "/repos/" + escapePath(owner, repo) + "/pulls/" + fmt.Sprintf("%d", number) + "/reviews"
	_, err := c.do(ctx, http.MethodPost, p, payload, nil)
	return err
}

// ListPullRequestReviews implements forge.Client.
func (c *LiveClient) ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestReview, error) {
	var raw []struct {
		ID    int    `json:"id"`
		Body  string `json:"body"`
		State string `json:"state"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
		SubmittedAt string `json:"submitted_at"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/pulls/" + fmt.Sprintf("%d", number) + "/reviews"
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]forge.PullRequestReview, 0, len(raw))
	for _, r := range raw {
		out = append(out, forge.PullRequestReview{
			ID:          r.ID,
			User:        r.User.Login,
			State:       r.State,
			Body:        r.Body,
			SubmittedAt: r.SubmittedAt,
		})
	}
	return out, nil
}

// DismissPullRequestReview implements forge.Client.
func (c *LiveClient) DismissPullRequestReview(ctx context.Context, owner, repo string, number, reviewID int, message string) error {
	p := "/repos/" + escapePath(owner, repo) + "/pulls/" + fmt.Sprintf("%d", number) + "/reviews/" + fmt.Sprintf("%d", reviewID) + "/dismissals"
	_, err := c.do(ctx, http.MethodPost, p, map[string]any{"message": message}, nil)
	return err
}

// UpdatePullRequestBranch implements forge.Client.
func (c *LiveClient) UpdatePullRequestBranch(ctx context.Context, owner, repo string, number int) error {
	p := "/repos/" + escapePath(owner, repo) + "/pulls/" + fmt.Sprintf("%d", number) + "/update"
	_, err := c.do(ctx, http.MethodPost, p, nil, nil)
	return err
}

// --- branch protection -----------------------------------------------------

// IsProtectedBranch implements forge.Client.
func (c *LiveClient) IsProtectedBranch(ctx context.Context, owner, repo, branch string) (bool, error) {
	rule, err := c.GetProtectedBranch(ctx, owner, repo, branch)
	if err != nil {
		return false, err
	}
	return rule != nil, nil
}

// GetProtectedBranch implements forge.Client.
func (c *LiveClient) GetProtectedBranch(ctx context.Context, owner, repo, branch string) (*forge.ProtectedBranchRule, error) {
	var raw struct {
		EnablePush           bool   `json:"enable_push"`
		EnableMergeWhitelist bool   `json:"enable_merge_whitelist"`
		BranchName           string `json:"branch_name"`
		Protected            bool   `json:"protected"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/branch_protections/" + url.PathEscape(branch)
	code, err := c.do(ctx, http.MethodGet, p, nil, &raw)
	if err != nil {
		if code == http.StatusNotFound || forge.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	name := raw.BranchName
	if name == "" {
		name = branch
	}
	return &forge.ProtectedBranchRule{Name: name}, nil
}

// ListProtectedBranches implements forge.Client.
func (c *LiveClient) ListProtectedBranches(ctx context.Context, owner, repo string) ([]forge.ProtectedBranchRule, error) {
	var raw []struct {
		BranchName string `json:"branch_name"`
		RuleName   string `json:"rule_name"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/branch_protections"
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]forge.ProtectedBranchRule, 0, len(raw))
	for _, r := range raw {
		name := r.BranchName
		if name == "" {
			name = r.RuleName
		}
		out = append(out, forge.ProtectedBranchRule{Name: name})
	}
	return out, nil
}

// --- actions secrets (best-effort Forgejo/Gitea API) -----------------------

// CreateRepoSecret implements forge.Client.
func (c *LiveClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	p := "/repos/" + escapePath(owner, repo) + "/actions/secrets/" + url.PathEscape(name)
	_, err := c.do(ctx, http.MethodPut, p, map[string]any{"data": value}, nil)
	return err
}

// RepoSecretExists implements forge.Client.
func (c *LiveClient) RepoSecretExists(ctx context.Context, owner, repo, name string) (bool, error) {
	var raw []struct {
		Name string `json:"name"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/actions/secrets"
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return false, err
	}
	for _, s := range raw {
		if s.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// DeleteRepoSecret implements forge.Client.
func (c *LiveClient) DeleteRepoSecret(ctx context.Context, owner, repo, name string) error {
	p := "/repos/" + escapePath(owner, repo) + "/actions/secrets/" + url.PathEscape(name)
	_, err := c.do(ctx, http.MethodDelete, p, nil, nil)
	return err
}
