package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// requestChangesMarker is the hidden HTML comment posted on GitLab
// REQUEST_CHANGES notes. It must match the poller/router marker so a
// review-bot request-changes note can dispatch the fix agent.
const requestChangesMarker = forge.ChangesRequestedMarker

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

// CreateCrossRepoChangeProposal is not supported on GitLab. GitLab merge
// requests between forks use the same source_branch / target_branch API as
// same-repo MRs (the fork relationship is implicit in the project), so the
// cross-repo variant is unnecessary. Returns forge.ErrNotSupported.
func (c *LiveClient) CreateCrossRepoChangeProposal(_ context.Context, _, _, _, _, _, _, _, _ string) (*forge.ChangeProposal, error) {
	return nil, fmt.Errorf("create cross-repo change proposal: %w", forge.ErrNotSupported)
}

// ListRepoPullRequests lists open merge requests for a project with pagination.
func (c *LiveClient) ListRepoPullRequests(ctx context.Context, owner, repo string) ([]forge.ChangeProposal, error) {
	var result []forge.ChangeProposal
	// Cache source_project_id -> path_with_namespace within this call so
	// multiple MRs from the same fork only trigger one lookup.
	forkPaths := make(map[int]string)

	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/merge_requests?state=opened&per_page=100&page=%d",
			projectPath(owner, repo), page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list merge requests page %d: %w", page, err)
		}

		var mrs []struct {
			IID             int    `json:"iid"`
			Title           string `json:"title"`
			WebURL          string `json:"web_url"`
			SourceBranch    string `json:"source_branch"`
			TargetBranch    string `json:"target_branch"`
			SourceProjectID int    `json:"source_project_id"`
			TargetProjectID int    `json:"target_project_id"`
			Author          struct {
				Username string `json:"username"`
			} `json:"author"`
		}
		if err := decodeJSON(resp, &mrs); err != nil {
			return nil, fmt.Errorf("decode merge requests page %d: %w", page, err)
		}

		for _, mr := range mrs {
			// This endpoint is scoped to owner/repo as the target project,
			// so target_project_id always identifies it. When
			// source_project_id matches, the head branch lives in the same
			// project. Otherwise it's a fork: the list endpoint only gives
			// us the source project's numeric ID, so resolve it to the
			// "owner/repo"-shaped path_with_namespace (the same shape
			// GitHub uses, and what callers compare HeadRepo against) via
			// an extra lookup — mirroring the pattern GetPullRequestInfo
			// already uses to resolve a fork's source project.
			headRepo := owner + "/" + repo
			if mr.SourceProjectID != mr.TargetProjectID {
				resolved, ok := forkPaths[mr.SourceProjectID]
				if !ok {
					resolved, err = c.resolveProjectPath(ctx, mr.SourceProjectID)
					if err != nil {
						return nil, fmt.Errorf("resolve source project %d for merge request !%d: %w",
							mr.SourceProjectID, mr.IID, err)
					}
					forkPaths[mr.SourceProjectID] = resolved
				}
				headRepo = resolved
			}
			result = append(result, forge.ChangeProposal{
				Number:   mr.IID,
				URL:      mr.WebURL,
				Title:    mr.Title,
				Head:     mr.SourceBranch,
				HeadRepo: headRepo,
				Base:     mr.TargetBranch,
				Author:   mr.Author.Username,
			})
		}

		if len(mrs) < 100 {
			break
		}
	}

	return result, nil
}

// resolveProjectPath resolves a GitLab numeric project ID to its
// "owner/repo"-shaped path_with_namespace.
func (c *LiveClient) resolveProjectPath(ctx context.Context, projectID int) (string, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/projects/%d", projectID))
	if err != nil {
		return "", fmt.Errorf("get project %d: %w", projectID, err)
	}
	var proj struct {
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if err := decodeJSON(resp, &proj); err != nil {
		return "", fmt.Errorf("decode project %d: %w", projectID, err)
	}
	if proj.PathWithNamespace == "" {
		return "", fmt.Errorf("get project %d: response missing path_with_namespace", projectID)
	}
	return proj.PathWithNamespace, nil
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

// CloseChangeProposal closes an open merge request without merging it.
func (c *LiveClient) CloseChangeProposal(ctx context.Context, owner, repo string, number int) error {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d", projectPath(owner, repo), number)
	resp, err := c.put(ctx, path, map[string]string{"state_event": "close"})
	if err != nil {
		return fmt.Errorf("close merge request !%d: %w", number, err)
	}
	resp.Body.Close()
	return nil
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
//     GitLab rejects the approval outright when the authenticated user is
//     the MR author (self-approval is blocked by default:
//     merge_requests_author_approval=false), and fullsend's code and
//     review agents share one bot PAT on GitLab, so that rejection is a
//     certainty on bot-authored MRs, not a possible-but-rare outcome.
//     The identity is checked before the call, and the call is skipped
//     entirely in favor of posting a note that records the approve
//     verdict. A post-hoc 401 handler remains as a safety net for edge
//     cases (e.g., a future per-role PAT where the pre-call identity
//     check doesn't apply but project settings still block approval).
//     The sticky review comment remains the authoritative record either
//     way.
//   - REQUEST_CHANGES or COMMENT: POST a note with the review body,
//     plus inline comments (see below).
//     GitLab's Notes API has no commit-pinning parameter, so commitSHA
//     cannot be enforced for the review-body note.
//
// Inline comments with a line number are posted as positioned diff
// discussions (POST .../discussions) using the MR's current diff_refs.
// When commitSHA is non-empty, the discussion is only positioned if
// diff_refs.head_sha matches, so findings from an older revision are
// not attached to a newer diff. File-level comments (Line == 0),
// comments whose position cannot be resolved, and comments rejected
// by the Discussions API fall back to general MR notes with a
// `path:line` prefix — the same non-inline path used previously.
func (c *LiveClient) CreatePullRequestReview(ctx context.Context, owner, repo string, number int, event, body, commitSHA string, comments []forge.ReviewComment) error {
	proj := projectPath(owner, repo)

	switch event {
	case "APPROVE":
		// The bot PAT is always the MR author on bot-authored MRs, and
		// GitLab's default merge_requests_author_approval=false always
		// rejects self-approval. When the pre-call check confirms that,
		// skip the API call that is known to fail and record the verdict
		// as a note directly. If the check itself errors, or resolves to
		// a different identity, fall through to the normal approve call;
		// the 401 handling below still catches genuine self-approval as
		// a safety net.
		if isAuthor, err := c.isAuthenticatedUserMRAuthor(ctx, owner, repo, number); err == nil && isAuthor {
			if err := c.postApprovalFallbackNote(ctx, proj, number, body); err != nil {
				return err
			}
			// Inline comments still need posting; the body was already
			// folded into the fallback note.
			body = ""
			break
		}

		approvePath := fmt.Sprintf("/projects/%s/merge_requests/%d/approve", proj, number)
		approveBody := map[string]string{}
		if commitSHA != "" {
			approveBody["sha"] = commitSHA
		}
		resp, err := c.do(ctx, http.MethodPost, approvePath, approveBody)
		if err != nil {
			return fmt.Errorf("approve merge request !%d: %w", number, err)
		}
		switch resp.StatusCode {
		case http.StatusOK, http.StatusCreated:
			resp.Body.Close()
		case http.StatusConflict:
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			msg := extractConflictMessage(data)
			if !strings.Contains(strings.ToLower(msg), "already approved") {
				return fmt.Errorf("approve merge request !%d: 409 Conflict: %s", number, msg)
			}
			// Idempotent: already approved (undocumented 409 variant).
		case http.StatusUnauthorized:
			// Safety net: the pre-call identity check above either
			// errored or found the bot isn't the author, so this 401 was
			// not predicted. GitLab returns the same generic 401 body
			// for self-approval and for other approval-ineligibility
			// reasons, so genuine credential failures are recognized by
			// known phrasing and always treated as a hard error, and
			// every other 401 is re-verified against the authenticated
			// identity before falling back to a note — a 401 for a
			// reason other than self-approval must still surface as an
			// error, not a false-positive "approved" note.
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			msg := extractConflictMessage(data)
			if isCredentialFailure(msg) {
				return fmt.Errorf("approve merge request !%d: %w", number, &APIError{
					StatusCode: http.StatusUnauthorized,
					Message:    msg,
				})
			}
			isAuthor, err := c.isAuthenticatedUserMRAuthor(ctx, owner, repo, number)
			if err != nil {
				return fmt.Errorf("approve merge request !%d: verify self-approval: %w", number, err)
			}
			if !isAuthor {
				return fmt.Errorf("approve merge request !%d: %w", number, &APIError{
					StatusCode: http.StatusUnauthorized,
					Message:    msg,
				})
			}
			if err := c.postApprovalFallbackNote(ctx, proj, number, body); err != nil {
				return err
			}
			// Inline comments still need posting; skip the success-path
			// body note because the fallback already included it.
			body = ""
		default:
			return fmt.Errorf("approve merge request !%d: %w", number, checkStatus(resp))
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

	return c.postInlineComments(ctx, proj, number, commitSHA, comments)
}

// mrDiffRefs is the GitLab diff_refs object that identifies a specific
// merge-request diff version. The Discussions API requires all three
// SHAs to attach a comment to a diff line.
type mrDiffRefs struct {
	BaseSHA  string
	StartSHA string
	HeadSHA  string
}

// usable reports whether refs can be used to position a discussion on
// the reviewed revision. All three SHAs must be present. When
// commitSHA is non-empty it must match HeadSHA so a finding based on
// an older HEAD is not pinned to a newer diff.
func (r *mrDiffRefs) usable(commitSHA string) bool {
	if r == nil || r.BaseSHA == "" || r.StartSHA == "" || r.HeadSHA == "" {
		return false
	}
	if commitSHA != "" && !strings.EqualFold(r.HeadSHA, commitSHA) {
		return false
	}
	return true
}

// getMergeRequestDiffRefs fetches the current diff_refs for an MR.
func (c *LiveClient) getMergeRequestDiffRefs(ctx context.Context, proj string, number int) (*mrDiffRefs, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d", proj, number)
	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("get merge request !%d diff refs: %w", number, err)
	}
	var mr struct {
		DiffRefs struct {
			BaseSHA  string `json:"base_sha"`
			StartSHA string `json:"start_sha"`
			HeadSHA  string `json:"head_sha"`
		} `json:"diff_refs"`
	}
	if err := decodeJSON(resp, &mr); err != nil {
		return nil, fmt.Errorf("decode merge request !%d diff refs: %w", number, err)
	}
	return &mrDiffRefs{
		BaseSHA:  mr.DiffRefs.BaseSHA,
		StartSHA: mr.DiffRefs.StartSHA,
		HeadSHA:  mr.DiffRefs.HeadSHA,
	}, nil
}

// postInlineComments publishes review comments. Line-level comments are
// posted as positioned discussions when the MR's current diff_refs
// match the reviewed commit; otherwise they fall back to general notes.
// File-level comments (Line == 0) always use notes.
func (c *LiveClient) postInlineComments(ctx context.Context, proj string, number int, commitSHA string, comments []forge.ReviewComment) error {
	var refs *mrDiffRefs
	for _, rc := range comments {
		if rc.Line > 0 {
			fetched, err := c.getMergeRequestDiffRefs(ctx, proj, number)
			if err == nil {
				refs = fetched
			} else if forge.IsTransient(err) {
				// A transient failure here (5xx, rate limit, network) would
				// otherwise silently downgrade every inline discussion in
				// this batch to a note with no diagnostic trail. Non-transient
				// errors (e.g. 404 in test stubs) continue to fall back
				// silently, matching postDiffDiscussion's own error handling
				// below.
				log.Printf("get merge request !%d diff refs failed, falling back to notes: %v", number, err)
			}
			break
		}
	}

	for _, rc := range comments {
		if rc.Line > 0 && refs.usable(commitSHA) {
			if err := c.postDiffDiscussion(ctx, proj, number, refs, rc); err == nil {
				continue
			} else if forge.IsTransient(err) {
				// A 4xx here means GitLab rejected the diff position,
				// which is an expected, silent fallback case handled by
				// the note path below. forge.IsTransient(err) instead
				// means a 5xx, rate limit, or network failure — that may
				// indicate a systemic Discussions API problem, so log it
				// rather than letting it degrade unnoticed to a note.
				log.Printf("post diff discussion on !%d (%s:%d) failed, falling back to note: %v", number, rc.Path, rc.Line, err)
			}
		}
		if err := c.postInlineNote(ctx, proj, number, rc); err != nil {
			return err
		}
	}
	return nil
}

// discussionPosition is the GitLab Discussions API position object for
// a single-line text comment on the new (right-hand) side of a diff.
type discussionPosition struct {
	BaseSHA      string `json:"base_sha"`
	StartSHA     string `json:"start_sha"`
	HeadSHA      string `json:"head_sha"`
	PositionType string `json:"position_type"`
	NewPath      string `json:"new_path"`
	OldPath      string `json:"old_path"`
	NewLine      int    `json:"new_line,omitempty"`
}

type discussionRequest struct {
	Body     string             `json:"body"`
	Position discussionPosition `json:"position"`
}

// postDiffDiscussion posts rc as a positioned Discussions API comment.
//
// forge.ReviewComment carries a single Path, so NewPath and OldPath are
// both set to rc.Path below. For a renamed file this sends
// old_path == new_path (the destination name), which GitLab's
// diff-position matching may reject; the caller falls back to a plain
// note in that case. This is a pre-existing ReviewComment limitation,
// not specific to positioned discussions — extend ReviewComment with
// separate old/new path fields if positioning comments on renamed
// files is needed.
func (c *LiveClient) postDiffDiscussion(ctx context.Context, proj string, number int, refs *mrDiffRefs, rc forge.ReviewComment) error {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/discussions", proj, number)
	payload := discussionRequest{
		Body: rc.Body,
		Position: discussionPosition{
			BaseSHA:      refs.BaseSHA,
			StartSHA:     refs.StartSHA,
			HeadSHA:      refs.HeadSHA,
			PositionType: "text",
			NewPath:      rc.Path,
			OldPath:      rc.Path,
			NewLine:      rc.Line,
		},
	}
	resp, err := c.post(ctx, path, payload)
	if err != nil {
		return fmt.Errorf("post diff discussion on !%d (%s:%d): %w", number, rc.Path, rc.Line, err)
	}
	resp.Body.Close()
	return nil
}

func (c *LiveClient) postInlineNote(ctx context.Context, proj string, number int, rc forge.ReviewComment) error {
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

const approvalFallbackNote = "Formal API approval skipped (MR author matches the review bot identity); recording the verdict as a note instead."

// isCredentialFailure reports whether a GitLab 401 body from POST
// /approve indicates a genuine credential problem (invalid, expired, or
// revoked token) rather than an authorization refusal such as the
// self-approval block. It is only consulted by the post-hoc 401 safety
// net in CreatePullRequestReview — the primary path checks identity
// before making the call and never sees this 401 for the common
// same-identity case. GitLab returns the same generic "401
// Unauthorized" body (or an empty body) for both cases, so message text
// alone cannot distinguish "self-approval" from other approval
// ineligibility reasons — only known credential-failure phrasing is
// treated as a hard error here. Every other 401 is verified against the
// authenticated identity (isAuthenticatedUserMRAuthor) before the
// caller assumes self-approval and falls back to a note.
func isCredentialFailure(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	switch {
	case strings.Contains(lower, "invalid token"),
		strings.Contains(lower, "bad credentials"),
		strings.Contains(lower, "access token"),
		strings.Contains(lower, "expired"),
		strings.Contains(lower, "revoked"),
		strings.Contains(lower, "insufficient_scope"),
		strings.Contains(lower, "token is invalid"),
		strings.Contains(lower, "not authenticated"):
		return true
	}
	return false
}

// isAuthenticatedUserMRAuthor reports whether the authenticated bot
// identity is the author of the given merge request. CreatePullRequestReview
// calls this before attempting POST /approve: on GitLab,
// merge_requests_author_approval is false by default, so an authenticated
// user who is also the MR author can never have their approval accepted.
// When this reports true, the approve call is skipped entirely in favor
// of posting a note. It is also consulted a second time, as a safety
// net, if a 401 arrives despite that pre-check (e.g., the check errored,
// or resolved to a different identity than the one project settings end
// up rejecting). An error here means the check could not be performed;
// callers must fail closed (return the error, or fall through to the
// normal approve call before the fact) rather than assume self-approval.
// An empty username from either lookup is treated the same way — GitLab
// always populates it on a 200 response, so an empty string signals a
// lookup that didn't actually resolve an identity, and comparing two
// empty strings as equal would otherwise be a false positive match.
func (c *LiveClient) isAuthenticatedUserMRAuthor(ctx context.Context, owner, repo string, number int) (bool, error) {
	authUser, err := c.GetAuthenticatedUser(ctx)
	if err != nil {
		return false, fmt.Errorf("check authenticated user: %w", err)
	}
	if authUser == "" {
		return false, fmt.Errorf("get authenticated user: empty username")
	}
	info, err := c.GetPullRequestInfo(ctx, owner, repo, number)
	if err != nil {
		return false, fmt.Errorf("get merge request !%d author: %w", number, err)
	}
	if info.AuthorID == "" {
		return false, fmt.Errorf("get merge request !%d author: empty author username", number)
	}
	return info.AuthorID == authUser, nil
}

// postApprovalFallbackNote records an approve verdict as an MR note in
// place of a formal /approve call — either because the call was skipped
// upfront (the authenticated user is known to be the MR author) or,
// as a safety net, because GitLab rejected the call for that reason.
func (c *LiveClient) postApprovalFallbackNote(ctx context.Context, proj string, number int, body string) error {
	fallbackBody := approvalFallbackNote
	if body != "" {
		fallbackBody = body + "\n\n" + approvalFallbackNote
	}
	notePath := fmt.Sprintf("/projects/%s/merge_requests/%d/notes", proj, number)
	noteResp, err := c.post(ctx, notePath, map[string]string{"body": fallbackBody})
	if err != nil {
		return fmt.Errorf("post approval fallback comment on !%d: %w", number, err)
	}
	noteResp.Body.Close()
	return nil
}
