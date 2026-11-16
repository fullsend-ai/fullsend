package forgejo

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func mapRepo(r apiRepo) forge.Repository {
	return forge.Repository{
		ID:            r.ID,
		Name:          r.Name,
		FullName:      r.FullName,
		Description:   r.Description,
		Private:       r.Private,
		Fork:          r.Fork,
		Archived:      r.Archived,
		DefaultBranch: r.DefaultBranch,
	}
}

// GetAuthenticatedUserIdentity implements forge.Client.
func (c *LiveClient) GetAuthenticatedUserIdentity(ctx context.Context) (*forge.UserIdentity, error) {
	var u apiUser
	if _, err := c.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return nil, err
	}
	name := u.FullName
	if name == "" {
		name = u.Login
	}
	email := u.Email
	if email == "" {
		email = u.Login + "@users.noreply.local"
	}
	return &forge.UserIdentity{Name: name, Email: email}, nil
}

type apiContents struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	SHA      string `json:"sha"`
	Type     string `json:"type"`
	Path     string `json:"path"`
	Name     string `json:"name"`
}

func (c *LiveClient) getContents(ctx context.Context, owner, repo, path, ref string) (*apiContents, error) {
	p := "/repos/" + escapePath(owner, repo) + "/contents/" + strings.TrimPrefix(path, "/")
	if ref != "" {
		p += "?ref=" + url.QueryEscape(ref)
	}
	var out apiContents
	if _, err := c.do(ctx, http.MethodGet, p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func decodeContent(c *apiContents) ([]byte, error) {
	if c.Encoding == "base64" || c.Encoding == "" {
		// Forgejo returns base64, possibly wrapped with newlines.
		raw := strings.ReplaceAll(c.Content, "\n", "")
		return base64.StdEncoding.DecodeString(raw)
	}
	return []byte(c.Content), nil
}

// GetFileContent implements forge.Client.
func (c *LiveClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	ct, err := c.getContents(ctx, owner, repo, path, "")
	if err != nil {
		return nil, err
	}
	return decodeContent(ct)
}

// GetFileContentAtRef implements forge.Client.
func (c *LiveClient) GetFileContentAtRef(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	ct, err := c.getContents(ctx, owner, repo, path, ref)
	if err != nil {
		return nil, err
	}
	return decodeContent(ct)
}

func (c *LiveClient) putFile(ctx context.Context, owner, repo, path, message, branch string, content []byte, sha string) error {
	payload := map[string]any{
		"content": base64.StdEncoding.EncodeToString(content),
		"message": message,
	}
	if branch != "" {
		payload["branch"] = branch
	}
	if sha != "" {
		payload["sha"] = sha
	}
	p := "/repos/" + escapePath(owner, repo) + "/contents/" + strings.TrimPrefix(path, "/")
	_, err := c.do(ctx, http.MethodPut, p, payload, nil)
	return err
}

// CreateFile implements forge.Client.
func (c *LiveClient) CreateFile(ctx context.Context, owner, repo, path, message string, content []byte) error {
	return c.putFile(ctx, owner, repo, path, message, "", content, "")
}

// CreateOrUpdateFile implements forge.Client.
func (c *LiveClient) CreateOrUpdateFile(ctx context.Context, owner, repo, path, message string, content []byte) error {
	sha := ""
	if ct, err := c.getContents(ctx, owner, repo, path, ""); err == nil {
		sha = ct.SHA
	}
	return c.putFile(ctx, owner, repo, path, message, "", content, sha)
}

// CreateFileOnBranch implements forge.Client.
func (c *LiveClient) CreateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error {
	return c.putFile(ctx, owner, repo, path, message, branch, content, "")
}

// CreateOrUpdateFileOnBranch implements forge.Client.
func (c *LiveClient) CreateOrUpdateFileOnBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error {
	sha := ""
	if ct, err := c.getContents(ctx, owner, repo, path, branch); err == nil {
		sha = ct.SHA
	}
	return c.putFile(ctx, owner, repo, path, message, branch, content, sha)
}

// DeleteFile implements forge.Client.
func (c *LiveClient) DeleteFile(ctx context.Context, owner, repo, path, message string) error {
	ct, err := c.getContents(ctx, owner, repo, path, "")
	if err != nil {
		return err
	}
	payload := map[string]any{"message": message, "sha": ct.SHA}
	p := "/repos/" + escapePath(owner, repo) + "/contents/" + strings.TrimPrefix(path, "/")
	_, err = c.do(ctx, http.MethodDelete, p, payload, nil)
	return err
}

// ListDirectoryContents implements forge.Client.
func (c *LiveClient) ListDirectoryContents(ctx context.Context, owner, repo, path, ref string, recursive bool) ([]forge.DirectoryEntry, error) {
	_ = recursive // Forgejo contents API is non-recursive; callers may walk.
	p := "/repos/" + escapePath(owner, repo) + "/contents/" + strings.TrimPrefix(path, "/")
	if ref != "" {
		p += "?ref=" + url.QueryEscape(ref)
	}
	var raw []apiContents
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]forge.DirectoryEntry, 0, len(raw))
	for _, e := range raw {
		typ := e.Type
		if typ == "blob" {
			typ = "file"
		}
		if typ == "tree" {
			typ = "dir"
		}
		out = append(out, forge.DirectoryEntry{Path: e.Path, Type: typ})
	}
	return out, nil
}

// ListRepositoryFiles implements forge.Client via git trees when available.
func (c *LiveClient) ListRepositoryFiles(ctx context.Context, owner, repo string) ([]string, error) {
	r, err := c.GetRepo(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	ref := r.DefaultBranch
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/git/trees/" + url.PathEscape(ref) + "?recursive=true"
	if _, err := c.do(ctx, http.MethodGet, p, nil, &tree); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tree.Tree))
	for _, t := range tree.Tree {
		if t.Type == "blob" {
			out = append(out, t.Path)
		}
	}
	return out, nil
}

type apiBranch struct {
	Name   string `json:"name"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

// GetBranchRef implements forge.Client.
func (c *LiveClient) GetBranchRef(ctx context.Context, owner, repo, branch string) (string, error) {
	var b apiBranch
	p := "/repos/" + escapePath(owner, repo) + "/branches/" + url.PathEscape(branch)
	if _, err := c.do(ctx, http.MethodGet, p, nil, &b); err != nil {
		return "", err
	}
	return b.Commit.ID, nil
}

// GetRef implements forge.Client.
func (c *LiveClient) GetRef(ctx context.Context, owner, repo, ref string) (string, error) {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	return c.GetBranchRef(ctx, owner, repo, ref)
}

// CreateBranch implements forge.Client.
func (c *LiveClient) CreateBranch(ctx context.Context, owner, repo, branchName string) error {
	r, err := c.GetRepo(ctx, owner, repo)
	if err != nil {
		return err
	}
	sha, err := c.GetBranchRef(ctx, owner, repo, r.DefaultBranch)
	if err != nil {
		return err
	}
	return c.CreateBranchFromSHA(ctx, owner, repo, branchName, sha)
}

// CreateBranchFromSHA implements forge.Client.
func (c *LiveClient) CreateBranchFromSHA(ctx context.Context, owner, repo, branch, sha string) error {
	payload2 := map[string]any{
		"ref": "refs/heads/" + branch,
		"sha": sha,
	}
	_, err := c.do(ctx, http.MethodPost, "/repos/"+escapePath(owner, repo)+"/git/refs", payload2, nil)
	if err == nil {
		return nil
	}
	payload := map[string]any{"new_branch_name": branch, "old_ref_name": sha}
	_, err2 := c.do(ctx, http.MethodPost, "/repos/"+escapePath(owner, repo)+"/branches", payload, nil)
	if err2 != nil {
		return fmt.Errorf("create branch: refs=%v branches=%v", err, err2)
	}
	return nil
}

// DeleteBranch implements forge.Client.
func (c *LiveClient) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	_, err := c.do(ctx, http.MethodDelete, "/repos/"+escapePath(owner, repo)+"/branches/"+url.PathEscape(branch), nil, nil)
	return err
}

// DeleteRef implements forge.Client.
func (c *LiveClient) DeleteRef(ctx context.Context, owner, repo, ref string) error {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	return c.DeleteBranch(ctx, owner, repo, ref)
}

type apiPR struct {
	ID     int64  `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"html_url"`
	State  string `json:"state"`
	Head   struct {
		Ref  string  `json:"ref"`
		SHA  string  `json:"sha"`
		Repo apiRepo `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string  `json:"ref"`
		Repo apiRepo `json:"repo"`
	} `json:"base"`
	User apiUser `json:"user"`
}

func mapPR(p apiPR) forge.ChangeProposal {
	return forge.ChangeProposal{
		URL:      p.URL,
		Title:    p.Title,
		Number:   p.Number,
		ID:       p.ID,
		Head:     p.Head.Ref,
		HeadRepo: p.Head.Repo.FullName,
		Base:     p.Base.Ref,
		Author:   p.User.Login,
	}
}

// CreateChangeProposal implements forge.Client.
func (c *LiveClient) CreateChangeProposal(ctx context.Context, owner, repo, title, body, head, base string) (*forge.ChangeProposal, error) {
	payload := map[string]any{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}
	var pr apiPR
	if _, err := c.do(ctx, http.MethodPost, "/repos/"+escapePath(owner, repo)+"/pulls", payload, &pr); err != nil {
		return nil, err
	}
	m := mapPR(pr)
	return &m, nil
}

// ListRepoPullRequests implements forge.Client.
func (c *LiveClient) ListRepoPullRequests(ctx context.Context, owner, repo string) ([]forge.ChangeProposal, error) {
	var raw []apiPR
	if _, err := c.do(ctx, http.MethodGet, "/repos/"+escapePath(owner, repo)+"/pulls?state=open&limit=50", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]forge.ChangeProposal, 0, len(raw))
	for _, p := range raw {
		out = append(out, mapPR(p))
	}
	return out, nil
}

// CloseChangeProposal implements forge.Client.
func (c *LiveClient) CloseChangeProposal(ctx context.Context, owner, repo string, number int) error {
	payload := map[string]any{"state": "closed"}
	_, err := c.do(ctx, http.MethodPatch, "/repos/"+escapePath(owner, repo)+"/pulls/"+strconv.Itoa(number), payload, nil)
	return err
}

// GetPullRequestInfo implements forge.Client.
func (c *LiveClient) GetPullRequestInfo(ctx context.Context, owner, repo string, number int) (*forge.PullRequestInfo, error) {
	var pr apiPR
	if _, err := c.do(ctx, http.MethodGet, "/repos/"+escapePath(owner, repo)+"/pulls/"+strconv.Itoa(number), nil, &pr); err != nil {
		return nil, err
	}
	return &forge.PullRequestInfo{
		Number:   pr.Number,
		HTMLURL:  pr.URL,
		HeadRepo: pr.Head.Repo.FullName,
		BaseRepo: pr.Base.Repo.FullName,
		HeadRef:  pr.Head.Ref,
		BaseRef:  pr.Base.Ref,
		HeadSHA:  pr.Head.SHA,
		AuthorID: strconv.FormatInt(pr.User.ID, 10),
		IsFork:   pr.Head.Repo.FullName != pr.Base.Repo.FullName,
	}, nil
}

// GetPullRequestHeadSHA implements forge.Client.
func (c *LiveClient) GetPullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error) {
	info, err := c.GetPullRequestInfo(ctx, owner, repo, number)
	if err != nil {
		return "", err
	}
	return info.HeadSHA, nil
}

// ListPullRequestFiles implements forge.Client.
func (c *LiveClient) ListPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]string, error) {
	var files []struct {
		Filename string `json:"filename"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/repos/"+escapePath(owner, repo)+"/pulls/"+strconv.Itoa(number)+"/files", nil, &files); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Filename)
	}
	return out, nil
}

// MergeChangeProposal implements forge.Client.
func (c *LiveClient) MergeChangeProposal(ctx context.Context, owner, repo string, number int) error {
	payload := map[string]any{"Do": "merge"}
	_, err := c.do(ctx, http.MethodPost, "/repos/"+escapePath(owner, repo)+"/pulls/"+strconv.Itoa(number)+"/merge", payload, nil)
	return err
}

type apiIssue struct {
	ID     int64   `json:"id"`
	Number int     `json:"number"`
	Title  string  `json:"title"`
	Body   string  `json:"body"`
	State  string  `json:"state"`
	Pull   bool    `json:"pull_request"`
	User   apiUser `json:"user"`
}

// CreateIssue implements forge.Client. Labels are applied best-effort after
// create so a missing label cannot block issue creation.
func (c *LiveClient) CreateIssue(ctx context.Context, owner, repo, title, body string, labels ...string) (*forge.Issue, error) {
	payload := map[string]any{"title": title, "body": body}
	var iss apiIssue
	if _, err := c.do(ctx, http.MethodPost, "/repos/"+escapePath(owner, repo)+"/issues", payload, &iss); err != nil {
		return nil, err
	}
	if len(labels) > 0 {
		_ = c.AddIssueLabels(ctx, owner, repo, iss.Number, labels...)
	}
	return &forge.Issue{ID: iss.ID, Number: iss.Number, Title: iss.Title, Body: iss.Body}, nil
}

// GetIssue implements forge.Client.
func (c *LiveClient) GetIssue(ctx context.Context, owner, repo string, number int) (*forge.Issue, error) {
	var iss apiIssue
	if _, err := c.do(ctx, http.MethodGet, "/repos/"+escapePath(owner, repo)+"/issues/"+strconv.Itoa(number), nil, &iss); err != nil {
		return nil, err
	}
	return &forge.Issue{ID: iss.ID, Number: iss.Number, Title: iss.Title, Body: iss.Body, IsPullRequest: iss.Pull}, nil
}

// CloseIssue implements forge.Client.
func (c *LiveClient) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	payload := map[string]any{"state": "closed"}
	_, err := c.do(ctx, http.MethodPatch, "/repos/"+escapePath(owner, repo)+"/issues/"+strconv.Itoa(number), payload, nil)
	return err
}

// ListOpenIssues implements forge.Client.
func (c *LiveClient) ListOpenIssues(ctx context.Context, owner, repo string, labels ...string) ([]forge.Issue, error) {
	p := "/repos/" + escapePath(owner, repo) + "/issues?state=open&type=issues&limit=50"
	if len(labels) > 0 {
		p += "&labels=" + url.QueryEscape(strings.Join(labels, ","))
	}
	var raw []apiIssue
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]forge.Issue, 0, len(raw))
	for _, iss := range raw {
		if iss.Pull {
			continue
		}
		out = append(out, forge.Issue{ID: iss.ID, Number: iss.Number, Title: iss.Title, Body: iss.Body})
	}
	return out, nil
}

// EnsureLabel implements forge.Client.
func (c *LiveClient) EnsureLabel(ctx context.Context, owner, repo, name string) error {
	payload := map[string]any{"name": name, "color": "#ededed"}
	_, err := c.do(ctx, http.MethodPost, "/repos/"+escapePath(owner, repo)+"/labels", payload, nil)
	if err == nil || forge.IsConflict(err) {
		return nil
	}
	// already exists often returns 422
	if strings.Contains(err.Error(), "422") || strings.Contains(strings.ToLower(err.Error()), "already") {
		return nil
	}
	return err
}

// AddIssueLabels implements forge.Client.
func (c *LiveClient) AddIssueLabels(ctx context.Context, owner, repo string, number int, labels ...string) error {
	for _, l := range labels {
		_ = c.EnsureLabel(ctx, owner, repo, l)
	}
	payload := map[string]any{"labels": labels}
	_, err := c.do(ctx, http.MethodPost, "/repos/"+escapePath(owner, repo)+"/issues/"+strconv.Itoa(number)+"/labels", payload, nil)
	return err
}

type apiComment struct {
	ID   int64   `json:"id"`
	Body string  `json:"body"`
	User apiUser `json:"user"`
}

// CreateIssueComment implements forge.Client.
func (c *LiveClient) CreateIssueComment(ctx context.Context, owner, repo string, number int, body string) (*forge.IssueComment, error) {
	payload := map[string]any{"body": body}
	var cm apiComment
	if _, err := c.do(ctx, http.MethodPost, "/repos/"+escapePath(owner, repo)+"/issues/"+strconv.Itoa(number)+"/comments", payload, &cm); err != nil {
		return nil, err
	}
	return &forge.IssueComment{ID: int(cm.ID), Body: cm.Body, Author: cm.User.Login}, nil
}

// ListIssueComments implements forge.Client.
func (c *LiveClient) ListIssueComments(ctx context.Context, owner, repo string, number int) ([]forge.IssueComment, error) {
	var raw []apiComment
	if _, err := c.do(ctx, http.MethodGet, "/repos/"+escapePath(owner, repo)+"/issues/"+strconv.Itoa(number)+"/comments", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]forge.IssueComment, 0, len(raw))
	for _, cm := range raw {
		out = append(out, forge.IssueComment{ID: int(cm.ID), Body: cm.Body, Author: cm.User.Login})
	}
	return out, nil
}

// CompareCommits implements forge.Client.
func (c *LiveClient) CompareCommits(ctx context.Context, owner, repo, base, head string) (status string, err error) {
	var cmp struct {
		Status       string `json:"status"`
		TotalCommits int    `json:"total_commits"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/compare/" + url.PathEscape(base) + "..." + url.PathEscape(head)
	if _, err := c.do(ctx, http.MethodGet, p, nil, &cmp); err != nil {
		return "", err
	}
	if cmp.Status != "" {
		return cmp.Status, nil
	}
	// Forgejo/Gitea often omit GitHub-style status; derive a coarse signal.
	if cmp.TotalCommits == 0 {
		return "identical", nil
	}
	return "ahead", nil
}
