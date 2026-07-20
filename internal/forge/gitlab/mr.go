package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const requestChangesMarker = "<!-- fullsend:request-changes -->"

// CreateChangeProposal creates a merge request on GitLab.
func (c *LiveClient) CreateChangeProposal(ctx context.Context, owner, repo, title, body, head, base string) (*forge.ChangeProposal, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests", projectPath(owner, repo))
	payload := map[string]string{
		"source_branch": head,
		"target_branch": base,
		"title":         title,
		"description":   body,
	}

	resp, err := c.post(ctx, path, payload)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			msg := strings.ToLower(apiErr.Message)
			if strings.Contains(msg, "no commits") || strings.Contains(msg, "no changes") {
				return nil, fmt.Errorf("create merge request: %w: %w", forge.ErrNoChanges, err)
			}
		}
		return nil, fmt.Errorf("create merge request: %w", err)
	}

	var mr struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		WebURL       string `json:"web_url"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
	}
	if err := decodeJSON(resp, &mr); err != nil {
		return nil, fmt.Errorf("decode merge request: %w", err)
	}

	return &forge.ChangeProposal{
		Number: mr.IID,
		URL:    mr.WebURL,
		Title:  mr.Title,
		Head:   mr.SourceBranch,
		Base:   mr.TargetBranch,
	}, nil
}

// ListRepoPullRequests lists open merge requests for a project with pagination.
func (c *LiveClient) ListRepoPullRequests(ctx context.Context, owner, repo string) ([]forge.ChangeProposal, error) {
	var result []forge.ChangeProposal

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/merge_requests?state=opened&per_page=100&page=%d",
			projectPath(owner, repo), page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list merge requests page %d: %w", page, err)
		}

		var mrs []struct {
			IID          int    `json:"iid"`
			Title        string `json:"title"`
			WebURL       string `json:"web_url"`
			SourceBranch string `json:"source_branch"`
			TargetBranch string `json:"target_branch"`
		}
		if err := decodeJSON(resp, &mrs); err != nil {
			return nil, fmt.Errorf("decode merge requests page %d: %w", page, err)
		}

		for _, mr := range mrs {
			result = append(result, forge.ChangeProposal{
				Number: mr.IID,
				URL:    mr.WebURL,
				Title:  mr.Title,
				Head:   mr.SourceBranch,
				Base:   mr.TargetBranch,
			})
		}

		if len(mrs) < 100 {
			break
		}
	}

	return result, nil
}

// GetPullRequestInfo returns branch and repo context for a merge request.
func (c *LiveClient) GetPullRequestInfo(ctx context.Context, owner, repo string, number int) (*forge.PullRequestInfo, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d", projectPath(owner, repo), number)
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("get merge request !%d: %w", number, err)
	}

	var mr struct {
		IID          int    `json:"iid"`
		WebURL       string `json:"web_url"`
		SHA          string `json:"sha"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		Author       struct {
			ID       int    `json:"id"`
			Username string `json:"username"`
		} `json:"author"`
		SourceProjectID int `json:"source_project_id"`
		TargetProjectID int `json:"target_project_id"`
	}
	if err := decodeJSON(resp, &mr); err != nil {
		return nil, fmt.Errorf("decode merge request !%d: %w", number, err)
	}

	headRepo := owner + "/" + repo
	isFork := mr.SourceProjectID != mr.TargetProjectID
	if isFork {
		srcPath := fmt.Sprintf("/projects/%d", mr.SourceProjectID)
		srcResp, err := c.get(ctx, srcPath)
		if err != nil {
			return nil, fmt.Errorf("get source project for !%d: %w", number, err)
		}
		var srcProj struct {
			PathWithNamespace string `json:"path_with_namespace"`
		}
		if err := decodeJSON(srcResp, &srcProj); err != nil {
			return nil, fmt.Errorf("decode source project for !%d: %w", number, err)
		}
		headRepo = srcProj.PathWithNamespace
	}

	return &forge.PullRequestInfo{
		Number:   mr.IID,
		HTMLURL:  mr.WebURL,
		HeadRepo: headRepo,
		BaseRepo: owner + "/" + repo,
		HeadRef:  mr.SourceBranch,
		BaseRef:  mr.TargetBranch,
		HeadSHA:  mr.SHA,
		AuthorID: mr.Author.Username,
		IsFork:   isFork,
	}, nil
}

// GetPullRequestHeadSHA returns the current HEAD commit SHA of a merge request.
func (c *LiveClient) GetPullRequestHeadSHA(ctx context.Context, owner, repo string, number int) (string, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d", projectPath(owner, repo), number)
	resp, err := c.get(ctx, path)
	if err != nil {
		return "", fmt.Errorf("get merge request !%d: %w", number, err)
	}

	var mr struct {
		SHA string `json:"sha"`
	}
	if err := decodeJSON(resp, &mr); err != nil {
		return "", fmt.Errorf("decode merge request !%d: %w", number, err)
	}
	return mr.SHA, nil
}

// ListPullRequestFiles returns the file paths changed by a merge request.
// GitLab returns diffs with old_path and new_path; we use new_path as the
// canonical path (matching rename destinations).
func (c *LiveClient) ListPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]string, error) {
	var files []string

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/merge_requests/%d/diffs?per_page=100&page=%d",
			projectPath(owner, repo), number, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list merge request diffs page %d: %w", page, err)
		}

		var diffs []struct {
			OldPath string `json:"old_path"`
			NewPath string `json:"new_path"`
		}
		if err := decodeJSON(resp, &diffs); err != nil {
			return nil, fmt.Errorf("decode merge request diffs page %d: %w", page, err)
		}

		for _, d := range diffs {
			files = append(files, d.NewPath)
		}

		if len(diffs) < 100 {
			break
		}
	}

	return files, nil
}

// ListPullRequestFileDiffs returns the files changed by a merge request
// along with their unified diff patches.
func (c *LiveClient) ListPullRequestFileDiffs(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestFileDiff, error) {
	var files []forge.PullRequestFileDiff

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/merge_requests/%d/diffs?per_page=100&page=%d",
			projectPath(owner, repo), number, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list merge request file diffs page %d: %w", page, err)
		}

		var diffs []struct {
			NewPath string `json:"new_path"`
			Diff    string `json:"diff"`
		}
		if err := decodeJSON(resp, &diffs); err != nil {
			return nil, fmt.Errorf("decode merge request file diffs page %d: %w", page, err)
		}

		for _, d := range diffs {
			files = append(files, forge.PullRequestFileDiff{
				Path:  d.NewPath,
				Patch: d.Diff,
			})
		}

		if len(diffs) < 100 {
			break
		}
	}

	return files, nil
}

// MergeChangeProposal merges a merge request by its IID.
func (c *LiveClient) MergeChangeProposal(ctx context.Context, owner, repo string, number int) error {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/merge", projectPath(owner, repo), number)
	resp, err := c.put(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("merge merge request !%d: %w", number, err)
	}
	resp.Body.Close()
	return nil
}

// UpdatePullRequestBranch rebases a merge request's source branch onto
// the target branch. GitLab uses rebase rather than merge-base-into-head.
// The rebase may be asynchronous; we fire the request and return without
// waiting for completion.
func (c *LiveClient) UpdatePullRequestBranch(ctx context.Context, owner, repo string, number int) error {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/rebase", projectPath(owner, repo), number)
	resp, err := c.do(ctx, http.MethodPut, path, nil)
	if err != nil {
		return fmt.Errorf("rebase merge request !%d: %w", number, err)
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK, http.StatusAccepted); err != nil {
		return fmt.Errorf("rebase merge request !%d: %w", number, err)
	}
	return nil
}

// CreatePullRequestReview creates a review on a merge request.
//
// GitLab has no native review object. This method synthesizes reviews:
//   - APPROVE: POST /projects/:id/merge_requests/:iid/approve
//     When commitSHA is non-empty, it is passed as the "sha" parameter
//     so GitLab rejects the approval if HEAD has advanced (409 Conflict).
//   - REQUEST_CHANGES or COMMENT: POST a note with the review body,
//     plus individual notes for each inline comment.
//     GitLab's Notes API has no commit-pinning parameter, so commitSHA
//     cannot be enforced for these events.
//
// Inline comments are posted as plain MR notes with file:line in the body
// text, not as positioned diff comments. GitLab's Discussions API supports
// positioned comments but requires base/head/start SHAs that are not
// available through the forge.Client interface.
func (c *LiveClient) CreatePullRequestReview(ctx context.Context, owner, repo string, number int, event, body, commitSHA string, comments []forge.ReviewComment) error {
	proj := projectPath(owner, repo)

	switch event {
	case "APPROVE":
		approvePath := fmt.Sprintf("/projects/%s/merge_requests/%d/approve", proj, number)
		approveBody := map[string]string{}
		if commitSHA != "" {
			approveBody["sha"] = commitSHA
		}
		resp, err := c.do(ctx, http.MethodPost, approvePath, approveBody)
		if err != nil {
			return fmt.Errorf("approve merge request !%d: %w", number, err)
		}
		if resp.StatusCode == http.StatusConflict {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			msg := extractConflictMessage(data)
			if strings.Contains(strings.ToLower(msg), "already approved") {
				// Idempotent: already approved (undocumented 409 variant).
			} else {
				return fmt.Errorf("approve merge request !%d: 409 Conflict: %s", number, msg)
			}
		} else if err := checkStatus(resp, http.StatusOK, http.StatusCreated); err != nil {
			return fmt.Errorf("approve merge request !%d: %w", number, err)
		} else {
			resp.Body.Close()
		}

		// If there is also a body, post it as a note.
		if body != "" {
			notePath := fmt.Sprintf("/projects/%s/merge_requests/%d/notes", proj, number)
			noteResp, err := c.post(ctx, notePath, map[string]string{"body": body})
			if err != nil {
				return fmt.Errorf("post approval comment on !%d: %w", number, err)
			}
			noteResp.Body.Close()
		}

	case "REQUEST_CHANGES", "COMMENT":
		noteBody := body
		if event == "REQUEST_CHANGES" {
			if noteBody != "" {
				noteBody = requestChangesMarker + "\n\n" + noteBody
			} else {
				noteBody = requestChangesMarker
			}
		}
		if noteBody != "" {
			notePath := fmt.Sprintf("/projects/%s/merge_requests/%d/notes", proj, number)
			resp, err := c.post(ctx, notePath, map[string]string{"body": noteBody})
			if err != nil {
				return fmt.Errorf("post review comment on !%d: %w", number, err)
			}
			resp.Body.Close()
		}

	default:
		return fmt.Errorf("create review on !%d: invalid event %q", number, event)
	}

	// Post inline comments as individual notes referencing file and line.
	for _, rc := range comments {
		notePath := fmt.Sprintf("/projects/%s/merge_requests/%d/notes", proj, number)
		noteBody := rc.Body
		if rc.Line > 0 {
			noteBody = fmt.Sprintf("`%s:%d`\n\n%s", rc.Path, rc.Line, rc.Body)
		} else {
			noteBody = fmt.Sprintf("`%s`\n\n%s", rc.Path, rc.Body)
		}
		resp, err := c.post(ctx, notePath, map[string]string{"body": noteBody})
		if err != nil {
			return fmt.Errorf("post inline comment on !%d (%s:%d): %w", number, rc.Path, rc.Line, err)
		}
		resp.Body.Close()
	}

	return nil
}

// ListPullRequestReviews synthesizes reviews from GitLab's approval state
// and MR notes.
//
// GitLab has no native review object. Approvals are mapped to APPROVED
// reviews (ID = approver's user ID), and MR notes are mapped to COMMENTED
// reviews (ID = note ID). DismissPullRequestReview relies on this convention
// to distinguish approvals from comments.
func (c *LiveClient) ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestReview, error) {
	proj := projectPath(owner, repo)
	var result []forge.PullRequestReview

	// 1. Get approvals via the approvals endpoint (works on all tiers,
	// unlike approval_state which requires Premium for rules).
	approvalPath := fmt.Sprintf("/projects/%s/merge_requests/%d/approvals", proj, number)
	approvalResp, err := c.get(ctx, approvalPath)
	if err != nil {
		return nil, fmt.Errorf("get approvals for !%d: %w", number, err)
	}

	var approvals struct {
		ApprovedBy []struct {
			User struct {
				ID       int    `json:"id"`
				Username string `json:"username"`
			} `json:"user"`
		} `json:"approved_by"`
	}
	if err := decodeJSON(approvalResp, &approvals); err != nil {
		return nil, fmt.Errorf("decode approvals for !%d: %w", number, err)
	}

	for _, entry := range approvals.ApprovedBy {
		result = append(result, forge.PullRequestReview{
			ID:    -entry.User.ID,
			User:  entry.User.Username,
			State: "APPROVED",
		})
	}

	// 2. Get notes (comments) on the MR.
	for page := 1; page <= 100; page++ {
		notesPath := fmt.Sprintf("/projects/%s/merge_requests/%d/notes?per_page=100&page=%d&sort=asc",
			proj, number, page)
		notesResp, err := c.get(ctx, notesPath)
		if err != nil {
			return nil, fmt.Errorf("list notes for !%d page %d: %w", number, page, err)
		}

		var notes []struct {
			ID        int    `json:"id"`
			Body      string `json:"body"`
			System    bool   `json:"system"`
			CreatedAt string `json:"created_at"`
			Author    struct {
				ID       int    `json:"id"`
				Username string `json:"username"`
			} `json:"author"`
		}
		if err := decodeJSON(notesResp, &notes); err != nil {
			return nil, fmt.Errorf("decode notes for !%d page %d: %w", number, page, err)
		}

		for _, note := range notes {
			if note.System {
				continue
			}
			state := "COMMENTED"
			if strings.Contains(note.Body, requestChangesMarker) {
				state = "CHANGES_REQUESTED"
			}
			result = append(result, forge.PullRequestReview{
				ID:          note.ID,
				User:        note.Author.Username,
				State:       state,
				Body:        note.Body,
				SubmittedAt: note.CreatedAt,
			})
		}

		if len(notes) < 100 {
			break
		}
	}

	return result, nil
}

// DismissPullRequestReview dismisses a review on a merge request.
//
// On GitLab, if the review was an approval, this unapproves the MR.
// For non-approval reviews backed by a request-changes note, this edits
// the note to replace the request-changes marker with a dismissed marker
// so ListPullRequestReviews stops reporting it as CHANGES_REQUESTED.
func (c *LiveClient) DismissPullRequestReview(ctx context.Context, owner, repo string, number, reviewID int, message string) error {
	// Check if this reviewID corresponds to an approver by looking up
	// the approval state. The reviewID for approvals is the user ID.
	proj := projectPath(owner, repo)

	approvalPath := fmt.Sprintf("/projects/%s/merge_requests/%d/approvals", proj, number)
	approvalResp, err := c.get(ctx, approvalPath)
	if err != nil {
		return fmt.Errorf("get approvals for !%d: %w", number, err)
	}

	var approvals struct {
		ApprovedBy []struct {
			User struct {
				ID       int    `json:"id"`
				Username string `json:"username"`
			} `json:"user"`
		} `json:"approved_by"`
	}
	if err := decodeJSON(approvalResp, &approvals); err != nil {
		return fmt.Errorf("decode approvals for !%d: %w", number, err)
	}

	isApproval := false
	var approverUsername string
	if reviewID < 0 {
		userID := -reviewID
		for _, entry := range approvals.ApprovedBy {
			if entry.User.ID == userID {
				isApproval = true
				approverUsername = entry.User.Username
				break
			}
		}
	}

	if !isApproval {
		// The reviewID may be a note ID for a CHANGES_REQUESTED review.
		// Edit the note to remove the request-changes marker so
		// ListPullRequestReviews stops reporting it as CHANGES_REQUESTED.
		return c.dismissRequestChangesNote(ctx, proj, number, reviewID, message)
	}

	// GitLab's /unapprove always removes the authenticated user's approval,
	// not a specific reviewer's. Verify the reviewID matches our user.
	authUser, err := c.GetAuthenticatedUser(ctx)
	if err != nil {
		return fmt.Errorf("get authenticated user for dismiss: %w", err)
	}
	if approverUsername != authUser {
		return fmt.Errorf("dismiss approval on !%d: %w: can only unapprove the authenticated user's own approval", number, forge.ErrNotSupported)
	}

	unapprovePath := fmt.Sprintf("/projects/%s/merge_requests/%d/unapprove", proj, number)
	resp, err := c.post(ctx, unapprovePath, nil)
	if err != nil {
		return fmt.Errorf("unapprove merge request !%d: %w", number, err)
	}
	resp.Body.Close()

	// If a dismiss message was provided, post it as a note.
	if message != "" {
		notePath := fmt.Sprintf("/projects/%s/merge_requests/%d/notes", proj, number)
		noteResp, err := c.post(ctx, notePath, map[string]string{
			"body": "Review dismissed: " + message,
		})
		if err != nil {
			return fmt.Errorf("post dismiss message on !%d: %w", number, err)
		}
		noteResp.Body.Close()
	}

	return nil
}

const dismissedMarker = "<!-- fullsend:dismissed -->"

// dismissRequestChangesNote edits a request-changes note to replace the
// marker with a dismissed marker so ListPullRequestReviews stops reporting
// it as CHANGES_REQUESTED.
func (c *LiveClient) dismissRequestChangesNote(ctx context.Context, proj string, mrIID, noteID int, message string) error {
	notePath := fmt.Sprintf("/projects/%s/merge_requests/%d/notes/%d", proj, mrIID, noteID)
	resp, err := c.get(ctx, notePath)
	if err != nil {
		return fmt.Errorf("get note %d on !%d: %w", noteID, mrIID, err)
	}
	var note struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(resp, &note); err != nil {
		return fmt.Errorf("decode note %d on !%d: %w", noteID, mrIID, err)
	}

	if !strings.Contains(note.Body, requestChangesMarker) {
		return nil
	}

	newBody := strings.Replace(note.Body, requestChangesMarker, dismissedMarker, 1)
	if message != "" {
		newBody += "\n\n_Dismissed: " + message + "_"
	}
	putResp, err := c.put(ctx, notePath, map[string]string{"body": newBody})
	if err != nil {
		return fmt.Errorf("dismiss note %d on !%d: %w", noteID, mrIID, err)
	}
	putResp.Body.Close()
	return nil
}
