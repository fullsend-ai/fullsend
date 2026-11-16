package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// validReactionContent mirrors GitHub/Gitea reaction content values.
var validReactionContent = map[string]struct{}{
	"+1": {}, "-1": {}, "laugh": {}, "confused": {}, "heart": {},
	"hooray": {}, "rocket": {}, "eyes": {},
}

func syntheticReactionID(content, user string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(content))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(user))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

type apiReaction struct {
	Content   string  `json:"content"`
	CreatedAt string  `json:"created_at"`
	User      apiUser `json:"user"`
}

func mapReaction(r apiReaction) forge.Reaction {
	return forge.Reaction{
		ID:      syntheticReactionID(r.Content, r.User.Login),
		Content: r.Content,
		User:    r.User.Login,
	}
}

// AddIssueReaction implements forge.Client.
func (c *LiveClient) AddIssueReaction(ctx context.Context, owner, repo string, number int, content string) (int64, error) {
	if _, ok := validReactionContent[content]; !ok {
		return 0, fmt.Errorf("add issue reaction: invalid content %q", content)
	}
	var raw apiReaction
	p := "/repos/" + escapePath(owner, repo) + "/issues/" + strconv.Itoa(number) + "/reactions"
	if _, err := c.do(ctx, http.MethodPost, p, map[string]any{"content": content}, &raw); err != nil {
		return 0, err
	}
	return mapReaction(raw).ID, nil
}

// DeleteIssueReaction implements forge.Client. Forgejo deletes by content for
// the authenticated user; reactionID is matched via synthetic IDs from List.
func (c *LiveClient) DeleteIssueReaction(ctx context.Context, owner, repo string, number int, reactionID int64) error {
	reactions, err := c.ListIssueReactions(ctx, owner, repo, number)
	if err != nil {
		return err
	}
	var content string
	for _, r := range reactions {
		if r.ID == reactionID {
			content = r.Content
			break
		}
	}
	if content == "" {
		return fmt.Errorf("delete issue reaction %d: %w", reactionID, forge.ErrNotFound)
	}
	p := "/repos/" + escapePath(owner, repo) + "/issues/" + strconv.Itoa(number) + "/reactions"
	_, err = c.do(ctx, http.MethodDelete, p, map[string]any{"content": content}, nil)
	return err
}

// AddIssueCommentReaction implements forge.Client.
func (c *LiveClient) AddIssueCommentReaction(ctx context.Context, owner, repo string, commentID int, content string) (int64, error) {
	if _, ok := validReactionContent[content]; !ok {
		return 0, fmt.Errorf("add issue comment reaction: invalid content %q", content)
	}
	var raw apiReaction
	p := "/repos/" + escapePath(owner, repo) + "/issues/comments/" + strconv.Itoa(commentID) + "/reactions"
	if _, err := c.do(ctx, http.MethodPost, p, map[string]any{"content": content}, &raw); err != nil {
		return 0, err
	}
	return mapReaction(raw).ID, nil
}

// DeleteIssueCommentReaction implements forge.Client.
func (c *LiveClient) DeleteIssueCommentReaction(ctx context.Context, owner, repo string, commentID int, reactionID int64) error {
	var raw []apiReaction
	p := "/repos/" + escapePath(owner, repo) + "/issues/comments/" + strconv.Itoa(commentID) + "/reactions"
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return err
	}
	var content string
	for _, r := range raw {
		if mapReaction(r).ID == reactionID {
			content = r.Content
			break
		}
	}
	if content == "" {
		return fmt.Errorf("delete issue comment reaction %d: %w", reactionID, forge.ErrNotFound)
	}
	_, err := c.do(ctx, http.MethodDelete, p, map[string]any{"content": content}, nil)
	return err
}

// ListIssueReactions implements forge.Client.
func (c *LiveClient) ListIssueReactions(ctx context.Context, owner, repo string, number int) ([]forge.Reaction, error) {
	var all []forge.Reaction
	for page := 1; page <= 20; page++ {
		var raw []apiReaction
		p := fmt.Sprintf("/repos/%s/issues/%d/reactions?limit=50&page=%d", escapePath(owner, repo), number, page)
		if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
			return nil, err
		}
		for _, r := range raw {
			all = append(all, mapReaction(r))
		}
		if len(raw) < 50 {
			break
		}
	}
	return all, nil
}

// --- Actions variables / secret protection ---------------------------------

// GetRepoSecretProtection implements forge.Client.
func (c *LiveClient) GetRepoSecretProtection(ctx context.Context, owner, repo, name string) (forge.SecretProtection, error) {
	exists, err := c.RepoSecretExists(ctx, owner, repo, name)
	if err != nil || !exists {
		return forge.SecretProtection{}, err
	}
	return forge.SecretProtection{Exists: true, Masked: true, Protected: true}, nil
}

// CreateOrUpdateRepoVariable implements forge.Client.
func (c *LiveClient) CreateOrUpdateRepoVariable(ctx context.Context, owner, repo, name, value string) error {
	p := "/repos/" + escapePath(owner, repo) + "/actions/variables/" + url.PathEscape(name)
	_, err := c.do(ctx, http.MethodPut, p, map[string]any{"value": value}, nil)
	if err == nil {
		return nil
	}
	if !forge.IsNotFound(err) {
		// Try create when update fails for other reasons (e.g. 404-shaped messages).
		_, err2 := c.do(ctx, http.MethodPost, p, map[string]any{"value": value}, nil)
		if err2 == nil {
			return nil
		}
		return fmt.Errorf("update variable %s: %v; create: %w", name, err, err2)
	}
	_, err = c.do(ctx, http.MethodPost, p, map[string]any{"value": value}, nil)
	return err
}

// RepoVariableExists implements forge.Client.
func (c *LiveClient) RepoVariableExists(ctx context.Context, owner, repo, name string) (bool, error) {
	_, ok, err := c.GetRepoVariable(ctx, owner, repo, name)
	return ok, err
}

// GetRepoVariable implements forge.Client.
func (c *LiveClient) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	var raw struct {
		Name string `json:"name"`
		Data string `json:"data"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/actions/variables/" + url.PathEscape(name)
	code, err := c.do(ctx, http.MethodGet, p, nil, &raw)
	if err != nil {
		if code == http.StatusNotFound || forge.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return raw.Data, true, nil
}

// GetRepoVariableInfo implements forge.Client.
func (c *LiveClient) GetRepoVariableInfo(ctx context.Context, owner, repo, name string) (forge.RepoVariable, bool, error) {
	value, exists, err := c.GetRepoVariable(ctx, owner, repo, name)
	return forge.RepoVariable{Value: value}, exists, err
}

// ListRepoVariables implements forge.Client.
func (c *LiveClient) ListRepoVariables(ctx context.Context, owner, repo string) (map[string]string, error) {
	out := map[string]string{}
	for page := 1; page <= 20; page++ {
		var raw []struct {
			Name string `json:"name"`
			Data string `json:"data"`
		}
		p := fmt.Sprintf("/repos/%s/actions/variables?limit=50&page=%d", escapePath(owner, repo), page)
		if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
			return nil, err
		}
		for _, v := range raw {
			out[v.Name] = v.Data
		}
		if len(raw) < 50 {
			break
		}
	}
	return out, nil
}

// DeleteRepoVariable implements forge.Client.
func (c *LiveClient) DeleteRepoVariable(ctx context.Context, owner, repo, name string) error {
	p := "/repos/" + escapePath(owner, repo) + "/actions/variables/" + url.PathEscape(name)
	_, err := c.do(ctx, http.MethodDelete, p, nil, nil)
	return err
}

// --- Actions workflows / runs ----------------------------------------------

type apiActionRun struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Event      string `json:"event"`
	TriggerEvt string `json:"trigger_event"`
	Status     string `json:"status"`
	HTMLURL    string `json:"html_url"`
	Created    string `json:"created"`
	WorkflowID string `json:"workflow_id"`
	PrettyRef  string `json:"prettyref"`
}

func mapActionRun(r apiActionRun) forge.WorkflowRun {
	status, conclusion := mapActionRunStatus(r.Status)
	event := r.Event
	if event == "" {
		event = r.TriggerEvt
	}
	return forge.WorkflowRun{
		ID:         int(r.ID),
		Name:       r.Title,
		Event:      event,
		Status:     status,
		Conclusion: conclusion,
		HTMLURL:    r.HTMLURL,
		CreatedAt:  r.Created,
		Path:       r.WorkflowID,
	}
}

func mapActionRunStatus(s string) (status, conclusion string) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "success", "completed":
		return "completed", "success"
	case "failure", "failed":
		return "completed", "failure"
	case "cancelled", "canceled":
		return "completed", "cancelled"
	case "skipped":
		return "completed", "skipped"
	case "running", "in_progress":
		return "in_progress", ""
	case "waiting", "queued", "pending", "blocked":
		return "queued", ""
	default:
		return s, ""
	}
}

func workflowFileMatches(workflowFile, workflowID, path string) bool {
	if workflowFile == "" {
		return true
	}
	wf := strings.TrimPrefix(workflowFile, "/")
	for _, cand := range []string{workflowID, path} {
		cand = strings.TrimPrefix(cand, "/")
		if cand == "" {
			continue
		}
		if cand == wf || strings.HasSuffix(cand, "/"+wf) || strings.HasSuffix(cand, wf) {
			return true
		}
		if strings.EqualFold(cand, wf) {
			return true
		}
	}
	return false
}

// GetWorkflow implements forge.Client best-effort via recent runs (no dedicated GET).
func (c *LiveClient) GetWorkflow(ctx context.Context, owner, repo, workflowFile string) (*forge.Workflow, error) {
	runs, err := c.ListRecentWorkflowRuns(ctx, owner, repo, 50)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		if workflowFileMatches(workflowFile, r.Path, r.Name) {
			return &forge.Workflow{
				ID:    r.ID,
				Name:  r.Name,
				Path:  firstNonEmpty(r.Path, workflowFile),
				State: "active",
			}, nil
		}
	}
	// Forgejo has no workflow catalog endpoint; treat unknown files as active
	// so DispatchWorkflow can still be attempted by filename.
	return &forge.Workflow{Name: workflowFile, Path: workflowFile, State: "active"}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// GetLatestWorkflowRun implements forge.Client.
func (c *LiveClient) GetLatestWorkflowRun(ctx context.Context, owner, repo, workflowFile string) (*forge.WorkflowRun, error) {
	runs, err := c.ListWorkflowRuns(ctx, owner, repo, workflowFile)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("no workflow runs found for %s", workflowFile)
	}
	return &runs[0], nil
}

// GetWorkflowRun implements forge.Client.
func (c *LiveClient) GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*forge.WorkflowRun, error) {
	var raw apiActionRun
	p := "/repos/" + escapePath(owner, repo) + "/actions/runs/" + strconv.Itoa(runID)
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	m := mapActionRun(raw)
	return &m, nil
}

// DispatchWorkflow implements forge.Client.
func (c *LiveClient) DispatchWorkflow(ctx context.Context, owner, repo, workflowFile, ref string, inputs map[string]string) error {
	payload := map[string]any{"ref": ref}
	if len(inputs) > 0 {
		payload["inputs"] = inputs
	}
	p := "/repos/" + escapePath(owner, repo) + "/actions/workflows/" + url.PathEscape(workflowFile) + "/dispatches"
	_, err := c.do(ctx, http.MethodPost, p, payload, nil)
	return err
}

// ListWorkflowRuns implements forge.Client.
func (c *LiveClient) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]forge.WorkflowRun, error) {
	runs, err := c.listActionRunsPage(ctx, owner, repo, 1, 30)
	if err != nil {
		return nil, err
	}
	out := make([]forge.WorkflowRun, 0, len(runs))
	for _, r := range runs {
		m := mapActionRun(r)
		if workflowFileMatches(workflowFile, r.WorkflowID, m.Path) {
			out = append(out, m)
		}
	}
	return out, nil
}

// ListWorkflowRunsSince implements forge.Client.
func (c *LiveClient) ListWorkflowRunsSince(ctx context.Context, owner, repo, workflowFile string, since time.Time) ([]forge.WorkflowRun, error) {
	const maxPages = 50
	var all []forge.WorkflowRun
	for page := 1; page <= maxPages; page++ {
		runs, err := c.listActionRunsPage(ctx, owner, repo, page, 50)
		if err != nil {
			return nil, err
		}
		if len(runs) == 0 {
			return all, nil
		}
		reachedBoundary := false
		for _, r := range runs {
			if !since.IsZero() {
				if t, err := time.Parse(time.RFC3339, r.Created); err == nil && t.Before(since) {
					reachedBoundary = true
					break
				}
			}
			if workflowFileMatches(workflowFile, r.WorkflowID, r.WorkflowID) {
				all = append(all, mapActionRun(r))
			}
		}
		if reachedBoundary || len(runs) < 50 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("list workflow runs since: pagination exceeded %d pages", maxPages)
}

// ListRecentWorkflowRuns implements forge.Client.
func (c *LiveClient) ListRecentWorkflowRuns(ctx context.Context, owner, repo string, perPage int) ([]forge.WorkflowRun, error) {
	if perPage <= 0 {
		perPage = 30
	}
	if perPage > 100 {
		perPage = 100
	}
	runs, err := c.listActionRunsPage(ctx, owner, repo, 1, perPage)
	if err != nil {
		return nil, err
	}
	out := make([]forge.WorkflowRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, mapActionRun(r))
	}
	return out, nil
}

func (c *LiveClient) listActionRunsPage(ctx context.Context, owner, repo string, page, limit int) ([]apiActionRun, error) {
	p := fmt.Sprintf("/repos/%s/actions/runs?page=%d&limit=%d", escapePath(owner, repo), page, limit)
	var raw json.RawMessage
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	var wrapped struct {
		WorkflowRuns []apiActionRun `json:"workflow_runs"`
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] == '{' {
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, fmt.Errorf("decode action runs object: %w", err)
		}
		return wrapped.WorkflowRuns, nil
	}
	var bare []apiActionRun
	if err := json.Unmarshal(raw, &bare); err != nil {
		return nil, fmt.Errorf("decode action runs: %w", err)
	}
	return bare, nil
}

// ListWorkflowRunJobs implements forge.Client.
func (c *LiveClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	var raw []struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Status  string `json:"status"`
		Attempt int    `json:"attempt"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/actions/runs/" + strconv.Itoa(runID) + "/jobs"
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]forge.WorkflowJob, 0, len(raw))
	for _, j := range raw {
		st, concl := mapActionRunStatus(j.Status)
		out = append(out, forge.WorkflowJob{
			ID:         int(j.ID),
			RunAttempt: j.Attempt,
			Name:       j.Name,
			Status:     st,
			Conclusion: concl,
		})
	}
	return out, nil
}

// ListWorkflowRunArtifacts implements forge.Client.
func (c *LiveClient) ListWorkflowRunArtifacts(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowArtifact, error) {
	var raw []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/actions/runs/" + strconv.Itoa(runID) + "/artifacts"
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]forge.WorkflowArtifact, 0, len(raw))
	for _, a := range raw {
		out = append(out, forge.WorkflowArtifact{ID: int(a.ID), Name: a.Name})
	}
	return out, nil
}

// DownloadWorkflowRunArtifact implements forge.Client.
func (c *LiveClient) DownloadWorkflowRunArtifact(ctx context.Context, owner, repo string, artifactID int) ([]byte, error) {
	// Prefer zip endpoint; fall back to artifact metadata URL is not needed.
	p := "/repos/" + escapePath(owner, repo) + "/actions/artifacts/" + strconv.Itoa(artifactID) + "/zip"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+p, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, forge.ErrNotFound
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("download artifact %d: HTTP %d", artifactID, res.StatusCode)
	}
	return io.ReadAll(res.Body)
}

// ListRepositoryArtifacts implements forge.Client.
func (c *LiveClient) ListRepositoryArtifacts(ctx context.Context, owner, repo string, perPage int) ([]forge.RepositoryArtifact, error) {
	if perPage <= 0 {
		perPage = 30
	}
	var raw []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		CreatedAt string `json:"created_at"`
		RunID     int64  `json:"run_id"`
	}
	p := fmt.Sprintf("/repos/%s/actions/artifacts?limit=%d", escapePath(owner, repo), perPage)
	if _, err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]forge.RepositoryArtifact, 0, len(raw))
	for _, a := range raw {
		out = append(out, forge.RepositoryArtifact{
			ID:            int(a.ID),
			Name:          a.Name,
			CreatedAt:     a.CreatedAt,
			WorkflowRunID: int(a.RunID),
		})
	}
	return out, nil
}

// GetWorkflowRunLogs implements forge.Client.
func (c *LiveClient) GetWorkflowRunLogs(ctx context.Context, owner, repo string, runID int) (string, error) {
	p := "/repos/" + escapePath(owner, repo) + "/actions/runs/" + strconv.Itoa(runID) + "/logs"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+p, nil)
	if err != nil {
		return "", err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return "", forge.ErrNotFound
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("get workflow run logs %d: HTTP %d", runID, res.StatusCode)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// --- PR authorization + review threads -------------------------------------

// GetPullRequestAuthorizationContext implements forge.Client.
func (c *LiveClient) GetPullRequestAuthorizationContext(ctx context.Context, owner, repo string, number int) (*forge.PullRequestAuthorizationContext, error) {
	var pr struct {
		UpdatedAt string  `json:"updated_at"`
		User      apiUser `json:"user"`
		Labels    []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	p := "/repos/" + escapePath(owner, repo) + "/pulls/" + strconv.Itoa(number)
	if _, err := c.do(ctx, http.MethodGet, p, nil, &pr); err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(pr.Labels))
	for _, l := range pr.Labels {
		labels = append(labels, l.Name)
	}
	return &forge.PullRequestAuthorizationContext{
		AuthorLogin: pr.User.Login,
		UpdatedAt:   pr.UpdatedAt,
		Labels:      labels,
	}, nil
}

// ListPullRequestReviewThreads maps Forgejo review comments to ReviewThread
// entries (one thread per comment; no GraphQL thread grouping).
func (c *LiveClient) ListPullRequestReviewThreads(ctx context.Context, owner, repo string, number int) (forge.ReviewThreadPage, error) {
	reviews, err := c.ListPullRequestReviews(ctx, owner, repo, number)
	if err != nil {
		return forge.ReviewThreadPage{}, err
	}
	page := forge.ReviewThreadPage{}
	const capThreads = 500
	for _, rev := range reviews {
		var comments []struct {
			ID        int      `json:"id"`
			Body      string   `json:"body"`
			Path      string   `json:"path"`
			Position  int      `json:"position"`
			CreatedAt string   `json:"created_at"`
			User      apiUser  `json:"user"`
			Resolver  *apiUser `json:"resolver"`
		}
		p := "/repos/" + escapePath(owner, repo) + "/pulls/" + strconv.Itoa(number) + "/reviews/" + strconv.Itoa(rev.ID) + "/comments"
		if _, err := c.do(ctx, http.MethodGet, p, nil, &comments); err != nil {
			return forge.ReviewThreadPage{}, err
		}
		for _, cm := range comments {
			if len(page.Threads) >= capThreads {
				page.Truncated = true
				return page, nil
			}
			side := forge.DiffSideRight
			line := cm.Position
			thread := forge.ReviewThread{
				ID:             strconv.Itoa(cm.ID),
				IsResolved:     cm.Resolver != nil && cm.Resolver.Login != "",
				Path:           cm.Path,
				Side:           &side,
				ResolvedByRole: normevent.RoleNone,
				Comments: []forge.ReviewThreadComment{{
					Author:     cm.User.Login,
					AuthorType: "User",
					Body:       cm.Body,
					CreatedAt:  cm.CreatedAt,
				}},
			}
			if line > 0 {
				thread.Line = &line
				thread.NewLine = &line
			}
			if cm.Resolver != nil {
				thread.ResolvedBy = cm.Resolver.Login
				thread.ResolvedByType = "User"
			}
			page.Threads = append(page.Threads, thread)
		}
	}
	return page, nil
}
