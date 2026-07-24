package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// CreateIssue creates a new issue on a GitLab project.
// Labels are sent as a comma-separated string per the GitLab API convention.
func (c *LiveClient) CreateIssue(ctx context.Context, owner, repo, title, body string, labels ...string) (*forge.Issue, error) {
	path := fmt.Sprintf("/projects/%s/issues", projectPath(owner, repo))

	payload := map[string]any{
		"title":       title,
		"description": body,
	}
	if len(labels) > 0 {
		payload["labels"] = strings.Join(labels, ",")
	}

	resp, err := c.post(ctx, path, payload)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}

	var result struct {
		IID    int          `json:"iid"`
		Title  string       `json:"title"`
		Desc   string       `json:"description"`
		WebURL string       `json:"web_url"`
		Labels gitlabLabels `json:"labels"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode issue: %w", err)
	}

	return &forge.Issue{
		Number: result.IID,
		Title:  result.Title,
		Body:   result.Desc,
		URL:    result.WebURL,
		Labels: result.Labels.strings(),
	}, nil
}

// GetIssue returns an issue by its project-scoped IID.
func (c *LiveClient) GetIssue(ctx context.Context, owner, repo string, number int) (*forge.Issue, error) {
	path := fmt.Sprintf("/projects/%s/issues/%d", projectPath(owner, repo), number)

	resp, err := c.get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("get issue #%d: %w", number, err)
	}

	var result struct {
		IID    int          `json:"iid"`
		Title  string       `json:"title"`
		Desc   string       `json:"description"`
		WebURL string       `json:"web_url"`
		Labels gitlabLabels `json:"labels"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode issue #%d: %w", number, err)
	}

	return &forge.Issue{
		Number: result.IID,
		Title:  result.Title,
		Body:   result.Desc,
		URL:    result.WebURL,
		Labels: result.Labels.strings(),
	}, nil
}

// ListOpenIssues returns open issues for a project, optionally filtered by labels.
// Paginates automatically until all matching issues are retrieved.
func (c *LiveClient) ListOpenIssues(ctx context.Context, owner, repo string, labelFilter ...string) ([]forge.Issue, error) {
	var result []forge.Issue

	proj := projectPath(owner, repo)
	for page := 1; page <= 100; page++ {
		params := url.Values{}
		params.Set("state", "opened")
		params.Set("per_page", "100")
		params.Set("page", fmt.Sprintf("%d", page))
		if len(labelFilter) > 0 {
			params.Set("labels", strings.Join(labelFilter, ","))
		}
		path := fmt.Sprintf("/projects/%s/issues?%s", proj, params.Encode())

		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list open issues page %d: %w", page, err)
		}

		var raw []struct {
			IID    int          `json:"iid"`
			Title  string       `json:"title"`
			Desc   string       `json:"description"`
			WebURL string       `json:"web_url"`
			Labels gitlabLabels `json:"labels"`
		}
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decode open issues page %d: %w", page, err)
		}

		for _, item := range raw {
			result = append(result, forge.Issue{
				Number: item.IID,
				Title:  item.Title,
				Body:   item.Desc,
				URL:    item.WebURL,
				Labels: item.Labels.strings(),
			})
		}

		if len(raw) < 100 {
			break
		}
	}

	return result, nil
}

// CloseIssue closes an issue by setting its state_event to "close".
func (c *LiveClient) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	path := fmt.Sprintf("/projects/%s/issues/%d", projectPath(owner, repo), number)
	resp, err := c.put(ctx, path, map[string]string{"state_event": "close"})
	if err != nil {
		return fmt.Errorf("close issue #%d: %w", number, err)
	}
	resp.Body.Close()
	return nil
}

// AddIssueLabels atomically appends labels to an existing issue using
// GitLab's add_labels parameter, avoiding the read-modify-write race of
// replacing the full label set.
func (c *LiveClient) AddIssueLabels(ctx context.Context, owner, repo string, number int, newLabels ...string) error {
	if len(newLabels) == 0 {
		return nil
	}

	path := fmt.Sprintf("/projects/%s/issues/%d", projectPath(owner, repo), number)
	resp, err := c.put(ctx, path, map[string]string{
		"add_labels": strings.Join(newLabels, ","),
	})
	if err != nil {
		return fmt.Errorf("add labels to issue #%d: %w", number, err)
	}
	resp.Body.Close()
	return nil
}

// CreateLabel creates a project label. If a label with the same name already
// exists, the call succeeds without modification (idempotent).
func (c *LiveClient) CreateLabel(ctx context.Context, owner, repo, name, color, description string) error {
	path := fmt.Sprintf("/projects/%s/labels", projectPath(owner, repo))
	body := map[string]string{
		"name":        name,
		"color":       "#" + color, // GitLab requires a leading '#' on color hex codes
		"description": description,
	}
	resp, err := c.post(ctx, path, body)
	if err != nil {
		// GitLab returns 409 Conflict when the label already exists.
		if forge.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create label %q: %w", name, err)
	}
	resp.Body.Close()
	return nil
}

// ListIssueComments returns all notes on an issue, sorted ascending.
// GitLab calls issue comments "notes".
func (c *LiveClient) ListIssueComments(ctx context.Context, owner, repo string, number int) ([]forge.IssueComment, error) {
	var result []forge.IssueComment

	proj := projectPath(owner, repo)
	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/projects/%s/issues/%d/notes?sort=asc&per_page=100&page=%d", proj, number, page)

		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list issue comments page %d: %w", page, err)
		}

		var raw []struct {
			ID        int    `json:"id"`
			Body      string `json:"body"`
			CreatedAt string `json:"created_at"`
			Author    struct {
				Username string `json:"username"`
			} `json:"author"`
		}
		if err := decodeJSON(resp, &raw); err != nil {
			return nil, fmt.Errorf("decode issue comments page %d: %w", page, err)
		}

		for _, r := range raw {
			htmlURL := fmt.Sprintf("%s/-/issues/%d#note_%d",
				c.projectWebURL(owner, repo), number, r.ID)
			result = append(result, forge.IssueComment{
				ID:        r.ID,
				HTMLURL:   htmlURL,
				Body:      r.Body,
				Author:    r.Author.Username,
				CreatedAt: r.CreatedAt,
			})
		}

		if len(raw) < 100 {
			break
		}
	}

	return result, nil
}

// CreateIssueComment creates a new note on an issue.
func (c *LiveClient) CreateIssueComment(ctx context.Context, owner, repo string, number int, body string) (*forge.IssueComment, error) {
	path := fmt.Sprintf("/projects/%s/issues/%d/notes", projectPath(owner, repo), number)

	resp, err := c.post(ctx, path, map[string]string{"body": body})
	if err != nil {
		return nil, fmt.Errorf("create issue comment on #%d: %w", number, err)
	}

	var result struct {
		ID        int    `json:"id"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
		Author    struct {
			Username string `json:"username"`
		} `json:"author"`
	}
	if err := decodeJSON(resp, &result); err != nil {
		return nil, fmt.Errorf("decode issue comment: %w", err)
	}

	htmlURL := fmt.Sprintf("%s/-/issues/%d#note_%d",
		c.projectWebURL(owner, repo), number, result.ID)

	return &forge.IssueComment{
		ID:        result.ID,
		HTMLURL:   htmlURL,
		Body:      result.Body,
		Author:    result.Author.Username,
		CreatedAt: result.CreatedAt,
	}, nil
}

// UpdateIssueComment updates the body of an existing note on an issue.
// GitLab's note API requires the issue IID in the URL path, but the
// forge.Client interface only provides the note ID. Since GitLab has no
// endpoint to look up a note by ID alone, we scan recent issues (open
// then closed) to locate the note. In practice, this is always called
// shortly after ListIssueComments or CreateIssueComment on the same issue.
func (c *LiveClient) UpdateIssueComment(ctx context.Context, owner, repo string, commentID int, body string) error {
	return c.updateOrDeleteNote(ctx, owner, repo, commentID, &body)
}

// DeleteIssueComment deletes a note on an issue.
// See UpdateIssueComment for the note-lookup strategy.
func (c *LiveClient) DeleteIssueComment(ctx context.Context, owner, repo string, commentID int) error {
	return c.updateOrDeleteNote(ctx, owner, repo, commentID, nil)
}

// updateOrDeleteNote finds the issue containing the given note and either
// updates its body (when body is non-nil) or deletes it. It scans recent
// issues ordered by update time to locate the note efficiently.
//
// Known limitation: GitLab's Notes API requires the issue IID to address a
// note, but the forge.Client interface only passes a bare commentID. This
// method must scan up to 1500 issues (10 pages open + 5 pages closed) to
// locate the parent issue. On projects with more issues, the note may not
// be found even though it exists.
func (c *LiveClient) updateOrDeleteNote(ctx context.Context, owner, repo string, noteID int, body *string) error {
	proj := projectPath(owner, repo)

	// Scan open issues first (most common case).
	for page := 1; page <= 10; page++ {
		path := fmt.Sprintf("/projects/%s/issues?state=opened&per_page=100&page=%d&order_by=updated_at&sort=desc", proj, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return fmt.Errorf("list issues to find note %d: %w", noteID, err)
		}

		var issues []struct {
			IID int `json:"iid"`
		}
		if err := decodeJSON(resp, &issues); err != nil {
			return fmt.Errorf("decode issues: %w", err)
		}

		for _, issue := range issues {
			err := c.tryNoteOperation(ctx, proj, issue.IID, noteID, body)
			if err == nil {
				return nil
			}
			if !forge.IsNotFound(err) {
				return err
			}
		}

		if len(issues) < 100 {
			break
		}
	}

	// Try closed issues (the issue may have been closed after the comment was created).
	for page := 1; page <= 5; page++ {
		path := fmt.Sprintf("/projects/%s/issues?state=closed&per_page=100&page=%d&order_by=updated_at&sort=desc", proj, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return fmt.Errorf("list closed issues to find note %d: %w", noteID, err)
		}

		var issues []struct {
			IID int `json:"iid"`
		}
		if err := decodeJSON(resp, &issues); err != nil {
			return fmt.Errorf("decode closed issues: %w", err)
		}

		for _, issue := range issues {
			err := c.tryNoteOperation(ctx, proj, issue.IID, noteID, body)
			if err == nil {
				return nil
			}
			if !forge.IsNotFound(err) {
				return err
			}
		}

		if len(issues) < 100 {
			break
		}
	}

	op := "update"
	if body == nil {
		op = "delete"
	}
	return fmt.Errorf("%s note %d: could not find issue containing this note", op, noteID)
}

// tryNoteOperation attempts to update or delete a note on the given issue.
// Returns nil on success, or an error wrapping forge.ErrNotFound if the
// note doesn't exist on this issue.
func (c *LiveClient) tryNoteOperation(ctx context.Context, proj string, issueIID, noteID int, body *string) error {
	notePath := fmt.Sprintf("/projects/%s/issues/%d/notes/%d", proj, issueIID, noteID)

	if body == nil {
		return c.delete_(ctx, notePath)
	}

	resp, err := c.put(ctx, notePath, map[string]string{"body": *body})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// MinimizeComment is not supported on GitLab -- there is no equivalent
// concept of hiding/minimizing individual comments.
func (c *LiveClient) MinimizeComment(_ context.Context, _, _ string) error {
	return forge.ErrNotSupported
}

// projectWebURL returns the web URL for a project (without trailing slash).
func (c *LiveClient) projectWebURL(owner, repo string) string {
	return c.baseURL + "/" + owner + "/" + repo
}

// gitlabLabels handles GitLab's inconsistent label format -- labels may be
// returned as a JSON array of strings (modern API) or as an array of
// objects with a "title" field (older API or some endpoints).
type gitlabLabels []string

func (l *gitlabLabels) UnmarshalJSON(data []byte) error {
	// Try array of strings first (modern format).
	var strLabels []string
	if err := json.Unmarshal(data, &strLabels); err == nil {
		*l = strLabels
		return nil
	}

	// Fall back to array of objects with "title" field.
	var objLabels []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(data, &objLabels); err == nil {
		result := make([]string, 0, len(objLabels))
		for _, ol := range objLabels {
			result = append(result, ol.Title)
		}
		*l = result
		return nil
	}

	return fmt.Errorf("labels: unexpected JSON format: %s", string(data))
}

func (l gitlabLabels) strings() []string {
	if l == nil {
		return nil
	}
	return []string(l)
}
