package forgejo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Branch, pull-request, review, discussion, and commit-status operations
// for the code, review, and fix flows. Each operation that has a matching
// forge.Client method has its signature, so a caller can depend on that
// subset. Forgejo has no counterpart for some forge.Client operations;
// those return an error wrapping forge.ErrNotSupported without making a
// request, and nothing falls back to another forge.

const (
	// pullsPageSize is the page size of every pull-request listing, the
	// Forgejo API's default maximum.
	pullsPageSize = 50
	// maxPullPages bounds every pull-request listing, so a server that
	// ignores the page parameter cannot keep the client looping. A listing
	// that reaches it returns an error wrapping forge.ErrPaginationLimit.
	maxPullPages = 40
	// maxPullDiffBytes bounds the unified diff of one pull request.
	maxPullDiffBytes = 20 << 20
)

// classify adds the forge sentinel matching an API error's status: HTTP 403
// wraps forge.ErrForbidden, and HTTP 409 wraps onConflict when it is not
// nil. Other errors are returned unchanged.
func classify(err error, onConflict error) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch {
	case apiErr.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %w", forge.ErrForbidden, err)
	case apiErr.StatusCode == http.StatusConflict && onConflict != nil:
		return fmt.Errorf("%w: %w", onConflict, err)
	}
	return err
}

// branchURL returns the API URL of branch in owner/repo. Each segment of a
// branch name containing slashes is escaped separately, so the name cannot
// address another API route.
func (c *LiveClient) branchURL(owner, repo, branch string) string {
	segs := strings.Split(branch, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return c.repoURL(owner, repo, "/branches/"+strings.Join(segs, "/"))
}

// pullURL returns the API URL of pull request number followed by suffix.
func (c *LiveClient) pullURL(owner, repo string, number int, suffix string) string {
	return c.repoURL(owner, repo, "/pulls/"+strconv.Itoa(number)+suffix)
}

// GetBranchRef returns the commit SHA at the head of branch, or an error
// wrapping forge.ErrNotFound when the branch does not exist.
func (c *LiveClient) GetBranchRef(ctx context.Context, owner, repo, branch string) (string, error) {
	var out struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := c.do(ctx, http.MethodGet, c.branchURL(owner, repo, branch), nil, &out); err != nil {
		return "", classify(err, nil)
	}
	if out.Commit.ID == "" {
		return "", fmt.Errorf("branch %q in %q/%q has no commit SHA", branch, owner, repo)
	}
	return out.Commit.ID, nil
}

// CreateBranch creates branchName from the head of the repository's
// default branch. It returns an error wrapping forge.ErrAlreadyExists when
// the branch exists and forge.ErrForbidden on insufficient permissions.
func (c *LiveClient) CreateBranch(ctx context.Context, owner, repo, branchName string) error {
	body := map[string]string{"new_branch_name": branchName}
	if err := c.do(ctx, http.MethodPost, c.repoURL(owner, repo, "/branches"), body, nil); err != nil {
		return fmt.Errorf("creating branch %q: %w", branchName, classify(err, forge.ErrAlreadyExists))
	}
	return nil
}

// CreateBranchFromSHA creates branchName pointing at commit sha. It returns
// an error wrapping forge.ErrAlreadyExists when the branch exists,
// forge.ErrForbidden on insufficient permissions, and forge.ErrNotFound
// when the repository or the commit does not exist.
func (c *LiveClient) CreateBranchFromSHA(ctx context.Context, owner, repo, branchName, sha string) error {
	if sha == "" {
		return fmt.Errorf("creating branch %q: no commit SHA", branchName)
	}
	body := map[string]string{"new_branch_name": branchName, "old_ref_name": sha}
	if err := c.do(ctx, http.MethodPost, c.repoURL(owner, repo, "/branches"), body, nil); err != nil {
		return fmt.Errorf("creating branch %q from SHA: %w", branchName, classify(err, forge.ErrAlreadyExists))
	}
	return nil
}

// DeleteBranch deletes branchName. It returns an error wrapping
// forge.ErrNotFound when the branch does not exist and forge.ErrForbidden
// when it is protected or the token lacks permission. Callers must verify
// ownership before deleting a branch with a predictable name.
func (c *LiveClient) DeleteBranch(ctx context.Context, owner, repo, branchName string) error {
	if err := c.do(ctx, http.MethodDelete, c.branchURL(owner, repo, branchName), nil, nil); err != nil {
		return fmt.Errorf("deleting branch %q: %w", branchName, classify(err, nil))
	}
	return nil
}

// pullBranch is the subset of Forgejo's PRBranchInfo the client reads. Repo
// is empty when the head repository was deleted.
type pullBranch struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

// pullRequest is the subset of Forgejo's PullRequest the client reads.
type pullRequest struct {
	ID      int64  `json:"id"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	UpdatedAt    string     `json:"updated_at"`
	ChangedFiles int        `json:"changed_files"`
	Head         pullBranch `json:"head"`
	Base         pullBranch `json:"base"`
}

func (p pullRequest) toChangeProposal() forge.ChangeProposal {
	return forge.ChangeProposal{
		URL:      p.HTMLURL,
		Title:    p.Title,
		Number:   p.Number,
		ID:       p.ID,
		Head:     p.Head.Ref,
		HeadRepo: p.Head.Repo.FullName,
		Base:     p.Base.Ref,
		Author:   p.User.Login,
	}
}

// createPull opens a pull request with the given head specification.
func (c *LiveClient) createPull(ctx context.Context, owner, repo, title, body, head, base string) (*pullRequest, error) {
	req := map[string]string{"title": title, "body": body, "head": head, "base": base}
	var out pullRequest
	if err := c.do(ctx, http.MethodPost, c.repoURL(owner, repo, "/pulls"), req, &out); err != nil {
		return nil, fmt.Errorf("creating pull request from %q into %q: %w", head, base, classify(err, forge.ErrAlreadyExists))
	}
	if out.Number == 0 {
		return nil, fmt.Errorf("created pull request in %q/%q has no number", owner, repo)
	}
	return &out, nil
}

// CreateChangeProposal opens a pull request from head into base, both
// branches of owner/repo. It returns an error wrapping
// forge.ErrAlreadyExists when an open pull request already joins the two
// branches and forge.ErrForbidden on insufficient permissions.
func (c *LiveClient) CreateChangeProposal(ctx context.Context, owner, repo, title, body, head, base string) (*forge.ChangeProposal, error) {
	pr, err := c.createPull(ctx, owner, repo, title, body, head, base)
	if err != nil {
		return nil, err
	}
	cp := pr.toChangeProposal()
	return &cp, nil
}

// checkHeadFork verifies, before a pull request is opened, that
// headOwner/headRepo exists and is a fork of baseOwner/baseRepo.
func (c *LiveClient) checkHeadFork(ctx context.Context, baseOwner, baseRepo, headOwner, headRepo string) error {
	var out struct {
		Fork   bool `json:"fork"`
		Parent *struct {
			FullName string `json:"full_name"`
		} `json:"parent"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoURL(headOwner, headRepo, ""), nil, &out); err != nil {
		return fmt.Errorf("looking up head repository %q/%q: %w", headOwner, headRepo, classify(err, nil))
	}
	base := baseOwner + "/" + baseRepo
	if !out.Fork || out.Parent == nil || !strings.EqualFold(out.Parent.FullName, base) {
		return fmt.Errorf("head repository %q/%q is not a fork of %q", headOwner, headRepo, base)
	}
	return nil
}

// CreateCrossRepoChangeProposal opens a pull request into
// baseOwner/baseRepo from headBranch of headOwner/headRepo. Forgejo names
// the head as "owner:branch" and resolves it to that owner's fork of the
// base repository, so a fork owned by the base repository's owner cannot
// be named: that case returns an error wrapping forge.ErrNotSupported
// without making a request. Before opening the pull request, it checks
// that headOwner/headRepo is a fork of the base repository and returns an
// error without creating anything when it is not. If Forgejo still
// resolves the head to a repository other than headOwner/headRepo (for
// example, the owner has several forks), the pull request it opened is
// reported in an error rather than returned.
func (c *LiveClient) CreateCrossRepoChangeProposal(ctx context.Context, baseOwner, baseRepo, headOwner, headRepo, title, body, headBranch, baseBranch string) (*forge.ChangeProposal, error) {
	if strings.EqualFold(baseOwner, headOwner) {
		if strings.EqualFold(baseRepo, headRepo) {
			return c.CreateChangeProposal(ctx, baseOwner, baseRepo, title, body, headBranch, baseBranch)
		}
		return nil, fmt.Errorf("creating pull request from %q/%q into %q/%q: Forgejo cannot name a head fork owned by the base repository's owner: %w",
			headOwner, headRepo, baseOwner, baseRepo, forge.ErrNotSupported)
	}
	if err := c.checkHeadFork(ctx, baseOwner, baseRepo, headOwner, headRepo); err != nil {
		return nil, err
	}
	pr, err := c.createPull(ctx, baseOwner, baseRepo, title, body, headOwner+":"+headBranch, baseBranch)
	if err != nil {
		return nil, err
	}
	want := headOwner + "/" + headRepo
	if !strings.EqualFold(pr.Head.Repo.FullName, want) {
		return nil, fmt.Errorf("pull request %s was opened from %q, not the requested head repository %q", sanitizeMessage(pr.HTMLURL), pr.Head.Repo.FullName, want)
	}
	cp := pr.toChangeProposal()
	return &cp, nil
}

// ListRepoPullRequests returns the open pull requests of owner/repo.
func (c *LiveClient) ListRepoPullRequests(ctx context.Context, owner, repo string) ([]forge.ChangeProposal, error) {
	var out []forge.ChangeProposal
	for page := 1; page <= maxPullPages; page++ {
		var prs []pullRequest
		q := "?state=open&page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(pullsPageSize)
		if err := c.do(ctx, http.MethodGet, c.repoURL(owner, repo, "/pulls")+q, nil, &prs); err != nil {
			return nil, classify(err, nil)
		}
		for _, pr := range prs {
			out = append(out, pr.toChangeProposal())
		}
		if len(prs) < pullsPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("listing pull requests of %q/%q: %w", owner, repo, forge.ErrPaginationLimit)
}

// getPull reads pull request number.
func (c *LiveClient) getPull(ctx context.Context, owner, repo string, number int) (*pullRequest, error) {
	var out pullRequest
	if err := c.do(ctx, http.MethodGet, c.pullURL(owner, repo, number, ""), nil, &out); err != nil {
		return nil, classify(err, nil)
	}
	return &out, nil
}

// GetPullRequestInfo returns the branch and repository context of pull
// request number. IsFork is set when the head repository differs from the
// base repository or is unknown.
func (c *LiveClient) GetPullRequestInfo(ctx context.Context, owner, repo string, number int) (*forge.PullRequestInfo, error) {
	pr, err := c.getPull(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	headRepo, baseRepo := pr.Head.Repo.FullName, pr.Base.Repo.FullName
	return &forge.PullRequestInfo{
		Number:       pr.Number,
		HTMLURL:      pr.HTMLURL,
		HeadRepo:     headRepo,
		BaseRepo:     baseRepo,
		HeadRef:      pr.Head.Ref,
		BaseRef:      pr.Base.Ref,
		HeadSHA:      pr.Head.SHA,
		BaseSHA:      pr.Base.SHA,
		ChangedFiles: pr.ChangedFiles,
		AuthorID:     pr.User.Login,
		IsFork:       headRepo == "" || baseRepo == "" || !strings.EqualFold(headRepo, baseRepo),
	}, nil
}

// GetPullRequestAuthorizationContext returns the author login, last update
// time, and label names of pull request number.
func (c *LiveClient) GetPullRequestAuthorizationContext(ctx context.Context, owner, repo string, number int) (*forge.PullRequestAuthorizationContext, error) {
	pr, err := c.getPull(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(pr.Labels))
	for _, l := range pr.Labels {
		labels = append(labels, l.Name)
	}
	return &forge.PullRequestAuthorizationContext{AuthorLogin: pr.User.Login, UpdatedAt: pr.UpdatedAt, Labels: labels}, nil
}

// GetPullRequestHeadSHA returns the commit SHA at the head of pull request
// number.
func (c *LiveClient) GetPullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error) {
	pr, err := c.getPull(ctx, owner, repo, number)
	if err != nil {
		return "", err
	}
	if pr.Head.SHA == "" {
		return "", fmt.Errorf("pull request %d in %q/%q has no head SHA", number, owner, repo)
	}
	return pr.Head.SHA, nil
}

// changedFile is the subset of Forgejo's ChangedFile the client reads.
type changedFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
}

// listPullFiles returns every file changed by pull request number. A
// listing that reaches the page cap returns the files read so far with
// truncated set.
func (c *LiveClient) listPullFiles(ctx context.Context, owner, repo string, number int) (files []changedFile, truncated bool, err error) {
	for page := 1; page <= maxPullPages; page++ {
		var batch []changedFile
		q := "?page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(pullsPageSize)
		if err := c.do(ctx, http.MethodGet, c.pullURL(owner, repo, number, "/files")+q, nil, &batch); err != nil {
			return nil, false, classify(err, nil)
		}
		files = append(files, batch...)
		if len(batch) < pullsPageSize {
			return files, false, nil
		}
	}
	return files, true, nil
}

// ListPullRequestFiles returns the paths changed by pull request number.
func (c *LiveClient) ListPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]string, error) {
	files, truncated, err := c.listPullFiles(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("listing files of pull request %d in %q/%q: %w", number, owner, repo, forge.ErrPaginationLimit)
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Filename)
	}
	return out, nil
}

// ListPullRequestCommits returns the commit SHAs of pull request number,
// oldest first. Forgejo lists them newest first, as git log does, so the
// listing is reversed.
func (c *LiveClient) ListPullRequestCommits(ctx context.Context, owner, repo string, number int) ([]string, error) {
	var newestFirst []string
	complete := false
	for page := 1; page <= maxPullPages && !complete; page++ {
		var batch []struct {
			SHA string `json:"sha"`
		}
		q := "?page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(pullsPageSize) + "&verification=false&files=false"
		if err := c.do(ctx, http.MethodGet, c.pullURL(owner, repo, number, "/commits")+q, nil, &batch); err != nil {
			return nil, classify(err, nil)
		}
		for _, cm := range batch {
			newestFirst = append(newestFirst, cm.SHA)
		}
		complete = len(batch) < pullsPageSize
	}
	if !complete {
		return nil, fmt.Errorf("listing commits of pull request %d in %q/%q: %w", number, owner, repo, forge.ErrPaginationLimit)
	}
	out := make([]string, 0, len(newestFirst))
	for i := len(newestFirst) - 1; i >= 0; i-- {
		out = append(out, newestFirst[i])
	}
	return out, nil
}

// normalizeFileStatus maps a Forgejo changed-file status to the
// forge.PullRequestFileDiff vocabulary: added, removed, renamed, or
// modified. A copy is a new file, so it reads as added.
func normalizeFileStatus(status string) string {
	switch status {
	case "added", "copied":
		return "added"
	case "deleted", "removed":
		return "removed"
	case "renamed":
		return "renamed"
	default:
		return "modified"
	}
}

// ListPullRequestFileDiffs returns the files changed by pull request number
// with the hunks of each file's unified diff. Forgejo's file listing has no
// patches, so they are taken from the pull request's unified diff. Git tree
// modes are not reported (ModesVerified is false). When the file listing
// reaches its page cap or the diff exceeds its size bound, the files are
// returned with an error wrapping forge.ErrDiffListingTruncated; an
// oversized diff leaves every patch empty.
func (c *LiveClient) ListPullRequestFileDiffs(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestFileDiff, error) {
	files, truncated, err := c.listPullFiles(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	out := make([]forge.PullRequestFileDiff, 0, len(files))
	for _, f := range files {
		d := forge.PullRequestFileDiff{Path: f.Filename, Status: normalizeFileStatus(f.Status)}
		if d.Status == "renamed" {
			d.PreviousPath = f.PreviousFilename
		}
		out = append(out, d)
	}
	diff, err := c.send(ctx, http.MethodGet, c.pullURL(owner, repo, number, ".diff"), nil, "text/plain", maxPullDiffBytes+1, false)
	if err != nil {
		return nil, classify(err, nil)
	}
	if len(diff) > maxPullDiffBytes {
		return out, fmt.Errorf("%w: diff of pull request %d exceeds %d bytes", forge.ErrDiffListingTruncated, number, maxPullDiffBytes)
	}
	patches := splitUnifiedDiff(string(diff))
	for i := range out {
		out[i].Patch = patches[out[i].Path]
	}
	if truncated {
		return out, fmt.Errorf("%w at %d files", forge.ErrDiffListingTruncated, len(out))
	}
	return out, nil
}

// splitUnifiedDiff splits a git unified diff into the hunks of each file,
// keyed by the file's new path, or its old path when the file was deleted.
// A patch starts at its first hunk header, as forge.PullRequestFileDiff
// patches do; a file with no hunks (binary, mode-only, or a pure rename)
// has no entry.
func splitUnifiedDiff(diff string) map[string]string {
	patches := map[string]string{}
	var oldPath, newPath string
	var hunks []string
	flush := func() {
		key := newPath
		if key == "" {
			key = oldPath
		}
		if key != "" && len(hunks) > 0 {
			patches[key] = strings.Join(hunks, "\n")
		}
		oldPath, newPath, hunks = "", "", nil
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
		case hunks != nil:
			hunks = append(hunks, line)
		case strings.HasPrefix(line, "@@"):
			hunks = []string{line}
		case strings.HasPrefix(line, "--- "):
			oldPath = diffPath(strings.TrimPrefix(line, "--- "), "a/")
		case strings.HasPrefix(line, "+++ "):
			newPath = diffPath(strings.TrimPrefix(line, "+++ "), "b/")
		}
	}
	flush()
	return patches
}

// diffPath returns the repository path of a ---/+++ line's file name, or
// "" for /dev/null. Git appends a tab to a name that contains a space and
// C-quotes a name with special characters; both are undone.
func diffPath(name, prefix string) string {
	name = strings.TrimSuffix(name, "\t")
	if name == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(name, `"`) {
		if unq, err := strconv.Unquote(name); err == nil {
			name = unq
		}
	}
	return strings.TrimPrefix(name, prefix)
}

// reviewEvents maps the forge.Client review events to Forgejo's.
var reviewEvents = map[string]string{
	"APPROVE":         "APPROVED",
	"REQUEST_CHANGES": "REQUEST_CHANGES",
	"COMMENT":         "COMMENT",
}

// CreatePullRequestReview submits a review on pull request number. event is
// APPROVE, REQUEST_CHANGES, or COMMENT. A non-empty commitSHA pins the
// review to that commit. Each comment with a line is attached to that line
// of the file's new version; Forgejo has no file-level review comment, so a
// comment without a line is appended to the review body under its path.
func (c *LiveClient) CreatePullRequestReview(ctx context.Context, owner, repo string, number int, event, body, commitSHA string, comments []forge.ReviewComment) error {
	fjEvent, ok := reviewEvents[event]
	if !ok {
		return fmt.Errorf("creating review on pull request %d: invalid event %q", number, event)
	}
	type reviewComment struct {
		Path        string `json:"path"`
		Body        string `json:"body"`
		NewPosition int    `json:"new_position"`
	}
	var inline []reviewComment
	var fileLevel strings.Builder
	for _, rc := range comments {
		if rc.Line > 0 {
			inline = append(inline, reviewComment{Path: rc.Path, Body: rc.Body, NewPosition: rc.Line})
			continue
		}
		fmt.Fprintf(&fileLevel, "\n\n**%s**\n\n%s", rc.Path, rc.Body)
	}
	if fileLevel.Len() > 0 {
		body = strings.TrimLeft(body+fileLevel.String(), "\n")
	}
	req := struct {
		Event    string          `json:"event"`
		Body     string          `json:"body"`
		CommitID string          `json:"commit_id,omitempty"`
		Comments []reviewComment `json:"comments,omitempty"`
	}{Event: fjEvent, Body: body, CommitID: commitSHA, Comments: inline}
	if err := c.do(ctx, http.MethodPost, c.pullURL(owner, repo, number, "/reviews"), req, nil); err != nil {
		return fmt.Errorf("creating review on pull request %d: %w", number, classify(err, nil))
	}
	return nil
}

// reviewStates maps Forgejo review states to the forge.PullRequestReview
// vocabulary.
var reviewStates = map[string]string{
	"APPROVED":        "APPROVED",
	"REQUEST_CHANGES": "CHANGES_REQUESTED",
	"COMMENT":         "COMMENTED",
	"PENDING":         "PENDING",
}

// ListPullRequestReviews returns the reviews of pull request number. A
// dismissed review has state DISMISSED. Forgejo also lists review requests
// as reviews; they are not reviews and are left out.
func (c *LiveClient) ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestReview, error) {
	var out []forge.PullRequestReview
	for page := 1; page <= maxPullPages; page++ {
		var batch []struct {
			ID   int `json:"id"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
			State       string `json:"state"`
			Body        string `json:"body"`
			CommitID    string `json:"commit_id"`
			SubmittedAt string `json:"submitted_at"`
			Dismissed   bool   `json:"dismissed"`
		}
		q := "?page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(pullsPageSize)
		if err := c.do(ctx, http.MethodGet, c.pullURL(owner, repo, number, "/reviews")+q, nil, &batch); err != nil {
			return nil, classify(err, nil)
		}
		for _, r := range batch {
			state, ok := reviewStates[r.State]
			if !ok {
				continue
			}
			if r.Dismissed {
				state = "DISMISSED"
			}
			out = append(out, forge.PullRequestReview{
				ID:          r.ID,
				User:        r.User.Login,
				State:       state,
				Body:        r.Body,
				SubmittedAt: r.SubmittedAt,
				CommitID:    r.CommitID,
			})
		}
		if len(batch) < pullsPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("listing reviews of pull request %d in %q/%q: %w", number, owner, repo, forge.ErrPaginationLimit)
}

// ListPullRequestReviewThreads returns an error wrapping
// forge.ErrNotSupported without making a request: Forgejo has no
// review-thread API that reports thread positions and resolution together.
func (c *LiveClient) ListPullRequestReviewThreads(_ context.Context, owner, repo string, number int) (forge.ReviewThreadPage, error) {
	return forge.ReviewThreadPage{}, fmt.Errorf("listing review threads of pull request %d in %q/%q: Forgejo has no review-thread API: %w",
		number, owner, repo, forge.ErrNotSupported)
}

// DismissPullRequestReview dismisses review reviewID of pull request number
// with message.
func (c *LiveClient) DismissPullRequestReview(ctx context.Context, owner, repo string, number, reviewID int, message string) error {
	body := map[string]string{"message": message}
	if err := c.do(ctx, http.MethodPost, c.pullURL(owner, repo, number, "/reviews/"+strconv.Itoa(reviewID)+"/dismissals"), body, nil); err != nil {
		return fmt.Errorf("dismissing review %d on pull request %d: %w", reviewID, number, classify(err, nil))
	}
	return nil
}

// CloseChangeProposal closes pull request number without merging it.
// Callers must verify ownership before closing a pull request.
func (c *LiveClient) CloseChangeProposal(ctx context.Context, owner, repo string, number int) error {
	body := map[string]string{"state": "closed"}
	if err := c.do(ctx, http.MethodPatch, c.pullURL(owner, repo, number, ""), body, nil); err != nil {
		return fmt.Errorf("closing pull request %d: %w", number, classify(err, nil))
	}
	return nil
}

// MergeChangeProposal squash-merges pull request number. A pull request
// that cannot be merged yields the API error; the head branch is not
// updated and the merge is not retried.
func (c *LiveClient) MergeChangeProposal(ctx context.Context, owner, repo string, number int) error {
	body := map[string]string{"Do": "squash"}
	if err := c.do(ctx, http.MethodPost, c.pullURL(owner, repo, number, "/merge"), body, nil); err != nil {
		return fmt.Errorf("merging pull request %d: %w", number, classify(err, forge.ErrConflict))
	}
	return nil
}

// UpdatePullRequestBranch merges the base branch into the head branch of
// pull request number.
func (c *LiveClient) UpdatePullRequestBranch(ctx context.Context, owner, repo string, number int) error {
	if err := c.do(ctx, http.MethodPost, c.pullURL(owner, repo, number, "/update")+"?style=merge", nil, nil); err != nil {
		return fmt.Errorf("updating branch of pull request %d: %w", number, classify(err, forge.ErrConflict))
	}
	return nil
}

// CommitStatus is one CI context's latest status on a commit.
type CommitStatus struct {
	// Context names the check, e.g. "ci / test (pull_request)".
	Context string
	// State is pending, success, error, failure, or warning.
	State       string
	Description string
	TargetURL   string
	UpdatedAt   string
}

// CombinedStatus is the CI status of a commit across every context.
type CombinedStatus struct {
	SHA string
	// State is the worst state of Statuses: error, then failure, warning,
	// pending, and success. It is pending when no context has reported.
	State    string
	Statuses []CommitStatus
}

// statusRank orders commit-status states from worst to best, the order in
// which Forgejo combines them.
var statusRank = map[string]int{"error": 0, "failure": 1, "warning": 2, "pending": 3, "success": 4}

// combineStatuses returns the worst state of statuses, or pending when
// there are none. An unrecognized state ranks as pending, so it never
// reads as success.
func combineStatuses(statuses []CommitStatus) string {
	if len(statuses) == 0 {
		return "pending"
	}
	state, best := "", len(statusRank)
	for _, s := range statuses {
		st := s.State
		if _, ok := statusRank[st]; !ok {
			st = "pending"
		}
		if statusRank[st] < best {
			best, state = statusRank[st], st
		}
	}
	return state
}

// GetCombinedStatus returns the latest status of every CI context on ref
// (a commit SHA, branch, or tag), which includes the statuses Forgejo
// Actions reports for its runs. The combined state is computed from every
// context rather than taken from the response, because Forgejo combines
// only the page it returns. A commit with no statuses yields state pending
// and no statuses, not an error. Pass a pull request's head SHA to read its
// CI status.
func (c *LiveClient) GetCombinedStatus(ctx context.Context, owner, repo, ref string) (*CombinedStatus, error) {
	out := &CombinedStatus{}
	base := c.repoURL(owner, repo, "/commits/"+url.PathEscape(ref)+"/status")
	complete := false
	for page := 1; page <= maxPullPages && !complete; page++ {
		var resp struct {
			SHA        string `json:"sha"`
			TotalCount int    `json:"total_count"`
			Statuses   []struct {
				Context     string `json:"context"`
				Status      string `json:"status"`
				Description string `json:"description"`
				TargetURL   string `json:"target_url"`
				UpdatedAt   string `json:"updated_at"`
			} `json:"statuses"`
		}
		q := "?page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(pullsPageSize)
		if err := c.do(ctx, http.MethodGet, base+q, nil, &resp); err != nil {
			return nil, classify(err, nil)
		}
		if out.SHA == "" {
			out.SHA = resp.SHA
		}
		for _, s := range resp.Statuses {
			out.Statuses = append(out.Statuses, CommitStatus{
				Context: s.Context, State: s.Status, Description: s.Description, TargetURL: s.TargetURL, UpdatedAt: s.UpdatedAt,
			})
		}
		complete = len(resp.Statuses) < pullsPageSize || (resp.TotalCount > 0 && len(out.Statuses) >= resp.TotalCount)
	}
	if !complete {
		return nil, fmt.Errorf("reading commit status of %q in %q/%q: %w", ref, owner, repo, forge.ErrPaginationLimit)
	}
	out.State = combineStatuses(out.Statuses)
	return out, nil
}
