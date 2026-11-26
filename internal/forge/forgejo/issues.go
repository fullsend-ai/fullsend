package forgejo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Forgejo issue, comment, and label operations. They have the signatures
// of the matching forge.Client methods, so the triage and polling flows
// can read and update issues on a Forgejo instance. Every request goes to
// the client's configured instance; there is no fallback to another forge.

const (
	// issuePageSize is the page size requested for every issue, label, and
	// timeline listing, the Forgejo API's default maximum. A server may
	// clamp it lower (its [api] MAX_RESPONSE_ITEMS setting), so a short
	// page does not mark the last page; a listing ends at an empty page.
	issuePageSize = 50
	// maxIssuePages bounds the open-issue and timeline listings, so a
	// server that ignores the page parameter cannot keep the client
	// looping.
	maxIssuePages = 100
	// maxLabelPages bounds the repository label listing.
	maxLabelPages = 20
	// newLabelColor is the color given to a label the client creates, the
	// same default the GitHub adapter uses.
	newLabelColor = "#ededed"
)

// issueLabel is the subset of Forgejo's Label the client reads.
type issueLabel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// issueResponse is the subset of Forgejo's Issue the client reads.
type issueResponse struct {
	ID          int64        `json:"id"`
	Number      int          `json:"number"`
	Title       string       `json:"title"`
	Body        string       `json:"body"`
	HTMLURL     string       `json:"html_url"`
	State       string       `json:"state"`
	Labels      []issueLabel `json:"labels"`
	PullRequest *struct{}    `json:"pull_request"`
}

func (r issueResponse) toIssue() forge.Issue {
	names := make([]string, 0, len(r.Labels))
	for _, l := range r.Labels {
		names = append(names, l.Name)
	}
	return forge.Issue{
		ID:     r.ID,
		Number: r.Number,
		Title:  r.Title,
		Body:   r.Body,
		URL:    r.HTMLURL,
		Labels: names,
	}
}

// commentResponse is the subset of Forgejo's Comment the client reads.
type commentResponse struct {
	ID      int    `json:"id"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
}

// toIssueComment converts a Forgejo comment. Forgejo has no GraphQL node
// IDs and no app identities, so NodeID and AppClientID stay empty.
func (r commentResponse) toIssueComment() forge.IssueComment {
	return forge.IssueComment{
		ID:        r.ID,
		HTMLURL:   r.HTMLURL,
		Body:      r.Body,
		Author:    r.User.Login,
		CreatedAt: r.CreatedAt,
	}
}

// issueURL returns the API URL of issue number in owner/repo followed by
// suffix.
func (c *LiveClient) issueURL(owner, repo string, number int, suffix string) string {
	return c.repoURL(owner, repo, "/issues/"+strconv.Itoa(number)+suffix)
}

// commentURL returns the API URL of comment commentID in owner/repo.
func (c *LiveClient) commentURL(owner, repo string, commentID int) string {
	return c.repoURL(owner, repo, "/issues/comments/"+strconv.Itoa(commentID))
}

// pageQuery returns the page and limit query of a listing page.
func pageQuery(page int) url.Values {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("limit", strconv.Itoa(issuePageSize))
	return q
}

// listLabels returns every label defined in owner/repo.
func (c *LiveClient) listLabels(ctx context.Context, owner, repo string) ([]issueLabel, error) {
	var out []issueLabel
	for page := 1; page <= maxLabelPages; page++ {
		var labels []issueLabel
		if err := c.do(ctx, http.MethodGet, c.repoURL(owner, repo, "/labels")+"?"+pageQuery(page).Encode(), nil, &labels); err != nil {
			return nil, fmt.Errorf("listing labels of %q/%q: %w", owner, repo, err)
		}
		out = append(out, labels...)
		if len(labels) == 0 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("listing labels of %q/%q: %w", owner, repo, forge.ErrPaginationLimit)
}

// findLabel returns the label named name, preferring an exact match over
// a case-insensitive one (forge label names are compared without case).
// Forgejo allows several labels with the same name; the first listed wins.
func findLabel(labels []issueLabel, name string) (issueLabel, bool) {
	for _, l := range labels {
		if l.Name == name {
			return l, true
		}
	}
	for _, l := range labels {
		if strings.EqualFold(l.Name, name) {
			return l, true
		}
	}
	return issueLabel{}, false
}

// createLabel creates a repository label and returns it.
func (c *LiveClient) createLabel(ctx context.Context, owner, repo, name string) (issueLabel, error) {
	var out issueLabel
	body := map[string]string{"name": name, "color": newLabelColor}
	if err := c.do(ctx, http.MethodPost, c.repoURL(owner, repo, "/labels"), body, &out); err != nil {
		return issueLabel{}, fmt.Errorf("creating label %q in %q/%q: %w", name, owner, repo, err)
	}
	if out.ID == 0 {
		return issueLabel{}, fmt.Errorf("created label %q in %q/%q has no ID", name, owner, repo)
	}
	return out, nil
}

// labelIDs resolves label names to the IDs Forgejo's issue endpoints take.
// A name with no repository label is created first, as GitHub and GitLab
// do when a label is applied, so the label is never silently dropped.
func (c *LiveClient) labelIDs(ctx context.Context, owner, repo string, names []string) ([]int64, error) {
	existing, err := c.listLabels(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		l, ok := findLabel(existing, name)
		if !ok {
			if l, err = c.createLabel(ctx, owner, repo, name); err != nil {
				return nil, err
			}
			existing = append(existing, l)
		}
		ids = append(ids, l.ID)
	}
	return ids, nil
}

// CreateIssue creates an issue carrying labels. A label the repository
// does not define yet is created first.
func (c *LiveClient) CreateIssue(ctx context.Context, owner, repo, title, body string, labels ...string) (*forge.Issue, error) {
	payload := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		ids, err := c.labelIDs(ctx, owner, repo, labels)
		if err != nil {
			return nil, fmt.Errorf("creating issue in %q/%q: %w", owner, repo, err)
		}
		payload["labels"] = ids
	}
	var out issueResponse
	if err := c.do(ctx, http.MethodPost, c.repoURL(owner, repo, "/issues"), payload, &out); err != nil {
		return nil, fmt.Errorf("creating issue in %q/%q: %w", owner, repo, err)
	}
	if out.Number == 0 {
		return nil, fmt.Errorf("created issue in %q/%q has no number", owner, repo)
	}
	issue := out.toIssue()
	return &issue, nil
}

// AddIssueLabels adds labels to an issue or pull request. A label the
// repository does not define yet is created first.
func (c *LiveClient) AddIssueLabels(ctx context.Context, owner, repo string, number int, labels ...string) error {
	if len(labels) == 0 {
		return nil
	}
	ids, err := c.labelIDs(ctx, owner, repo, labels)
	if err != nil {
		return fmt.Errorf("adding labels to #%d in %q/%q: %w", number, owner, repo, err)
	}
	if err := c.do(ctx, http.MethodPost, c.issueURL(owner, repo, number, "/labels"), map[string]any{"labels": ids}, nil); err != nil {
		return fmt.Errorf("adding labels to #%d in %q/%q: %w", number, owner, repo, err)
	}
	return nil
}

// EnsureLabel creates a repository label named name unless one exists.
// Forgejo accepts duplicate label names, so the labels are listed first
// rather than relying on the create being rejected.
func (c *LiveClient) EnsureLabel(ctx context.Context, owner, repo, name string) error {
	existing, err := c.listLabels(ctx, owner, repo)
	if err != nil {
		return err
	}
	if _, ok := findLabel(existing, name); ok {
		return nil
	}
	_, err = c.createLabel(ctx, owner, repo, name)
	return err
}

// GetIssue returns an issue or pull request by number, or an error
// wrapping forge.ErrNotFound when it does not exist. Forgejo reports no
// state reason, so StateReason stays empty.
func (c *LiveClient) GetIssue(ctx context.Context, owner, repo string, number int) (*forge.Issue, error) {
	var out issueResponse
	if err := c.do(ctx, http.MethodGet, c.issueURL(owner, repo, number, ""), nil, &out); err != nil {
		return nil, fmt.Errorf("getting issue #%d in %q/%q: %w", number, owner, repo, err)
	}
	issue := out.toIssue()
	issue.State = out.State
	issue.IsPullRequest = out.PullRequest != nil
	return &issue, nil
}

// CloseIssue closes an issue by number.
func (c *LiveClient) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	if err := c.do(ctx, http.MethodPatch, c.issueURL(owner, repo, number, ""), map[string]string{"state": "closed"}, nil); err != nil {
		return fmt.Errorf("closing issue #%d in %q/%q: %w", number, owner, repo, err)
	}
	return nil
}

// ListOpenIssues returns the open issues of owner/repo, excluding pull
// requests. When labels are given, only issues carrying every one of them
// are returned. The filter is also applied to the response, because
// Forgejo discards label names it does not know rather than matching
// nothing. It returns an error wrapping forge.ErrPaginationLimit rather
// than a partial listing when the page cap is reached.
func (c *LiveClient) ListOpenIssues(ctx context.Context, owner, repo string, labels ...string) ([]forge.Issue, error) {
	var out []forge.Issue
	for page := 1; page <= maxIssuePages; page++ {
		q := pageQuery(page)
		q.Set("state", "open")
		q.Set("type", "issues")
		if len(labels) > 0 {
			q.Set("labels", strings.Join(labels, ","))
		}
		var issues []issueResponse
		if err := c.do(ctx, http.MethodGet, c.repoURL(owner, repo, "/issues")+"?"+q.Encode(), nil, &issues); err != nil {
			return nil, fmt.Errorf("listing open issues in %q/%q: %w", owner, repo, err)
		}
		for _, r := range issues {
			if r.PullRequest != nil || !hasAllLabels(r.Labels, labels) {
				continue
			}
			out = append(out, r.toIssue())
		}
		if len(issues) == 0 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("listing open issues in %q/%q: %w", owner, repo, forge.ErrPaginationLimit)
}

// hasAllLabels reports whether have holds a label named like each of want.
func hasAllLabels(have []issueLabel, want []string) bool {
	for _, name := range want {
		if _, ok := findLabel(have, name); !ok {
			return false
		}
	}
	return true
}

// timelineEvent is the subset of Forgejo's TimelineComment the client
// reads. A "label" event's body is "1" when the label was added and empty
// when it was removed.
type timelineEvent struct {
	Type      string      `json:"type"`
	Body      string      `json:"body"`
	CreatedAt string      `json:"created_at"`
	Label     *issueLabel `json:"label"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

// ListIssueLabelEvents returns the events that added a label to an issue
// or pull request, read from its timeline. It returns an error wrapping
// forge.ErrPaginationLimit rather than a partial listing when the page cap
// is reached.
func (c *LiveClient) ListIssueLabelEvents(ctx context.Context, owner, repo string, number int) ([]forge.IssueLabelEvent, error) {
	var out []forge.IssueLabelEvent
	for page := 1; page <= maxIssuePages; page++ {
		var events []timelineEvent
		if err := c.do(ctx, http.MethodGet, c.issueURL(owner, repo, number, "/timeline")+"?"+pageQuery(page).Encode(), nil, &events); err != nil {
			return nil, fmt.Errorf("listing label events of #%d in %q/%q: %w", number, owner, repo, err)
		}
		for _, e := range events {
			if e.Type != "label" || e.Body != "1" || e.Label == nil {
				continue
			}
			out = append(out, forge.IssueLabelEvent{
				LabelName:  e.Label.Name,
				ActorLogin: e.User.Login,
				CreatedAt:  e.CreatedAt,
			})
		}
		if len(events) == 0 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("listing label events of #%d in %q/%q: %w", number, owner, repo, forge.ErrPaginationLimit)
}

// RemoveIssueLabel removes a label from an issue or pull request. The
// label is looked up among the issue's own labels, so an organization
// label resolves too. It returns an error wrapping forge.ErrNotFound when
// the issue does not carry the label.
func (c *LiveClient) RemoveIssueLabel(ctx context.Context, owner, repo string, number int, label string) error {
	var current []issueLabel
	if err := c.do(ctx, http.MethodGet, c.issueURL(owner, repo, number, "/labels"), nil, &current); err != nil {
		return fmt.Errorf("removing label %q from #%d in %q/%q: %w", label, number, owner, repo, err)
	}
	l, ok := findLabel(current, label)
	if !ok {
		return fmt.Errorf("removing label %q from #%d in %q/%q: %w", label, number, owner, repo, forge.ErrNotFound)
	}
	if err := c.do(ctx, http.MethodDelete, c.issueURL(owner, repo, number, "/labels/"+strconv.FormatInt(l.ID, 10)), nil, nil); err != nil {
		return fmt.Errorf("removing label %q from #%d in %q/%q: %w", label, number, owner, repo, err)
	}
	return nil
}

// ListIssueComments returns every comment on an issue or pull request.
// Forgejo returns them in one unpaginated response.
func (c *LiveClient) ListIssueComments(ctx context.Context, owner, repo string, number int) ([]forge.IssueComment, error) {
	var comments []commentResponse
	if err := c.do(ctx, http.MethodGet, c.issueURL(owner, repo, number, "/comments"), nil, &comments); err != nil {
		return nil, fmt.Errorf("listing comments of #%d in %q/%q: %w", number, owner, repo, err)
	}
	out := make([]forge.IssueComment, 0, len(comments))
	for _, r := range comments {
		out = append(out, r.toIssueComment())
	}
	return out, nil
}

// CreateIssueComment creates a comment on an issue or pull request.
func (c *LiveClient) CreateIssueComment(ctx context.Context, owner, repo string, number int, body string) (*forge.IssueComment, error) {
	var out commentResponse
	if err := c.do(ctx, http.MethodPost, c.issueURL(owner, repo, number, "/comments"), map[string]string{"body": body}, &out); err != nil {
		return nil, fmt.Errorf("commenting on #%d in %q/%q: %w", number, owner, repo, err)
	}
	if out.ID == 0 {
		return nil, fmt.Errorf("created comment on #%d in %q/%q has no ID", number, owner, repo)
	}
	comment := out.toIssueComment()
	return &comment, nil
}

// GetIssueComment returns a comment by ID, or an error wrapping
// forge.ErrNotFound when owner/repo has no such comment. Forgejo answers
// an ID that names a timeline event rather than a comment with an empty
// response, which is also reported as not found.
func (c *LiveClient) GetIssueComment(ctx context.Context, owner, repo string, commentID int) (*forge.IssueComment, error) {
	var out commentResponse
	if err := c.do(ctx, http.MethodGet, c.commentURL(owner, repo, commentID), nil, &out); err != nil {
		return nil, fmt.Errorf("getting comment %d in %q/%q: %w", commentID, owner, repo, err)
	}
	if out.ID == 0 {
		return nil, fmt.Errorf("getting comment %d in %q/%q: %w", commentID, owner, repo, forge.ErrNotFound)
	}
	comment := out.toIssueComment()
	return &comment, nil
}

// UpdateIssueComment replaces the body of a comment.
func (c *LiveClient) UpdateIssueComment(ctx context.Context, owner, repo string, commentID int, body string) error {
	if err := c.do(ctx, http.MethodPatch, c.commentURL(owner, repo, commentID), map[string]string{"body": body}, nil); err != nil {
		return fmt.Errorf("updating comment %d in %q/%q: %w", commentID, owner, repo, err)
	}
	return nil
}

// DeleteIssueComment deletes a comment.
func (c *LiveClient) DeleteIssueComment(ctx context.Context, owner, repo string, commentID int) error {
	if err := c.do(ctx, http.MethodDelete, c.commentURL(owner, repo, commentID), nil, nil); err != nil {
		return fmt.Errorf("deleting comment %d in %q/%q: %w", commentID, owner, repo, err)
	}
	return nil
}

// MinimizeComment is not supported: Forgejo has no way to hide a comment.
// It returns an error wrapping forge.ErrNotSupported.
func (c *LiveClient) MinimizeComment(_ context.Context, nodeID, _ string) error {
	return fmt.Errorf("minimizing comment %q: %w", nodeID, forge.ErrNotSupported)
}
