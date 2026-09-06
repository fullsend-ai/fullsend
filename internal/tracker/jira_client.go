package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// jiraTimestampFormats are the layouts Jira Cloud has been observed to
// use for created/updated timestamps. Duplicated from
// jirapoll/discover.go's unexported jiraTimestampFormats/
// parseJiraTimestamp rather than shared, for the same reason
// adf.go/jirapoll's ADF walkers stay duplicated for now: consolidating
// onto one shared helper is a reasonable follow-up once the Jira tracker
// integration stabilizes.
var jiraTimestampFormats = []string{
	"2006-01-02T15:04:05.000-0700",
	time.RFC3339,
}

// parseJiraTimestamp parses a Jira timestamp string.
func parseJiraTimestamp(s string) (time.Time, error) {
	for _, format := range jiraTimestampFormats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable Jira timestamp: %q", s)
}

// stickyPropertyKey is the Jira comment entity property key under which
// sticky-comment markers are stored. Using a namespaced key avoids
// collisions with other Jira apps.
const stickyPropertyKey = "fullsend.sticky-marker"

// statusPropertyKey stores run-status bookkeeping outside the visible Jira
// comment body. Jira renders HTML comments as text, unlike GitHub and GitLab.
const statusPropertyKey = "fullsend.agent-status"

// stickyMarkerProperty is object-shaped because Jira rejects top-level JSON
// strings in the properties array of the create-comment request.
type stickyMarkerProperty struct {
	Marker string `json:"marker"`
}

type statusCommentProperty struct {
	Marker   string `json:"marker"`
	Terminal bool   `json:"terminal"`
}

// jiraClient is the Jira API surface this adapter needs. Implemented by
// jira.LiveClient; faked in tests.
type jiraClient interface {
	GetIssue(ctx context.Context, issueIDOrKey string) (*jira.Issue, error)
	ListComments(ctx context.Context, issueIDOrKey string) ([]jira.Comment, error)
	CreateComment(ctx context.Context, issueIDOrKey, body string) (*jira.Comment, error)
	CreateCommentWithProperties(ctx context.Context, issueIDOrKey, body string, properties []jira.CommentProperty) (*jira.Comment, error)
	UpdateComment(ctx context.Context, issueIDOrKey, commentID, body string) error
	SetCommentProperty(ctx context.Context, issueIDOrKey, commentID, propertyKey string, value any) error
	DeleteComment(ctx context.Context, issueIDOrKey, commentID string) error
}

var _ jiraClient = (*jira.LiveClient)(nil)

// JiraClient adapts a Jira client to tracker.Client. project is a Jira
// project key (e.g. "PROJ"); (project, number) maps to the issue key
// "PROJ-123".
type JiraClient struct {
	jira    jiraClient
	baseURL string // for browse URLs: {baseURL}/browse/{key}
}

// NewJiraClient returns a tracker.Client backed by jc. baseURL is the
// Jira instance's base URL (e.g. "https://acme.atlassian.net"), used to
// build issue browse URLs. Returns an error if baseURL embeds credentials
// (https://user:token@host): those would otherwise propagate into every
// Issue.URL this client returns, which are commonly logged or displayed.
func NewJiraClient(jc jiraClient, baseURL string) (*JiraClient, error) {
	trimmed := strings.TrimRight(baseURL, "/")
	if err := jira.ValidateBaseURL(trimmed); err != nil {
		return nil, err
	}
	return &JiraClient{jira: jc, baseURL: trimmed}, nil
}

// issueKey builds a Jira issue key from a project key and issue number,
// e.g. issueKey("PROJ", 123) = "PROJ-123".
func issueKey(project string, number int) string {
	return fmt.Sprintf("%s-%d", project, number)
}

// GetIssue implements Client.
func (c *JiraClient) GetIssue(ctx context.Context, project string, number int) (*Issue, error) {
	key := issueKey(project, number)
	issue, err := c.jira.GetIssue(ctx, key)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	return &Issue{
		Number: number,
		Title:  issue.Fields.Summary,
		Body:   Body(jira.ADFToMarkdown(issue.Fields.Description)),
		URL:    c.baseURL + "/browse/" + key,
		Labels: issue.Fields.Labels,
	}, nil
}

// ListComments implements Client.
func (c *JiraClient) ListComments(ctx context.Context, project string, number int) ([]Comment, error) {
	key := issueKey(project, number)
	comments, err := c.jira.ListComments(ctx, key)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	result := make([]Comment, len(comments))
	for i, comment := range comments {
		result[i] = fromJiraComment(comment)
	}
	return result, nil
}

// ListJiraComments returns the raw jira.Comment objects (including
// entity properties) for use by FindCommentByMarkerProperty. This is
// separate from ListComments because the generic tracker.Comment type
// does not carry Jira-specific property data.
func (c *JiraClient) ListJiraComments(ctx context.Context, project string, number int) ([]jira.Comment, error) {
	key := issueKey(project, number)
	comments, err := c.jira.ListComments(ctx, key)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	return comments, nil
}

// CreateComment implements Client. The returned Comment's Body is
// ADFToMarkdown(comment.Body), the same representation ListComments uses,
// so tracker.Comment.Body has a stable meaning regardless of which method
// produced it.
func (c *JiraClient) CreateComment(ctx context.Context, project string, number int, body Body) (*Comment, error) {
	key := issueKey(project, number)
	comment, err := c.jira.CreateComment(ctx, key, string(body))
	if err != nil {
		return nil, wrapNotFound(err)
	}
	result := fromJiraComment(*comment)
	return &result, nil
}

// CreateStatusComment implements StatusCommentClient by storing run identity
// and terminal state in a Jira comment entity property.
func (c *JiraClient) CreateStatusComment(ctx context.Context, project string, number int, body Body, marker string, terminal bool) (*Comment, error) {
	key := issueKey(project, number)
	// statusCommentProperty contains only a string and bool, so marshaling
	// cannot fail.
	value, _ := json.Marshal(statusCommentProperty{Marker: marker, Terminal: terminal})
	comment, err := c.jira.CreateCommentWithProperties(ctx, key, string(body), []jira.CommentProperty{{
		Key: statusPropertyKey, Value: value,
	}})
	if err != nil {
		return nil, wrapNotFound(err)
	}
	result := fromJiraComment(*comment)
	return &result, nil
}

// UpdateStatusComment implements StatusCommentClient. Writing the property
// first preserves discoverability if the subsequent body update fails.
func (c *JiraClient) UpdateStatusComment(ctx context.Context, project string, number int, commentID string, body Body, marker string, terminal bool) error {
	key := issueKey(project, number)
	property := statusCommentProperty{Marker: marker, Terminal: terminal}
	if err := c.jira.SetCommentProperty(ctx, key, commentID, statusPropertyKey, property); err != nil {
		return fmt.Errorf("setting status property on comment %s of %s: %w", commentID, key, err)
	}
	return wrapNotFound(c.jira.UpdateComment(ctx, key, commentID, string(body)))
}

// FindStatusComment implements StatusCommentClient. Property lookup is the
// primary path; visible-body scanning keeps pre-property comments compatible.
func (c *JiraClient) FindStatusComment(ctx context.Context, project string, number int, marker string) (*Comment, bool, error) {
	key := issueKey(project, number)
	comments, err := c.jira.ListComments(ctx, key)
	if err != nil {
		return nil, false, wrapNotFound(err)
	}
	for i := range comments {
		for _, prop := range comments[i].Properties {
			if prop.Key != statusPropertyKey {
				continue
			}
			var stored statusCommentProperty
			if json.Unmarshal(prop.Value, &stored) == nil && stored.Marker == marker {
				result := fromJiraComment(comments[i])
				return &result, stored.Terminal, nil
			}
		}
	}
	for i := range comments {
		body := jira.ADFToMarkdown(comments[i].Body)
		if strings.Contains(body, marker) {
			result := fromJiraComment(comments[i])
			return &result, strings.Contains(body, "fullsend:status:terminal"), nil
		}
	}
	return nil, false, nil
}

// IsStatusComment implements StatusCommentClient. The legacy body check keeps
// timeline analysis correct while older marker-bearing comments are present.
func (c *JiraClient) IsStatusComment(ctx context.Context, project string, number int, commentID string) (bool, error) {
	key := issueKey(project, number)
	comments, err := c.jira.ListComments(ctx, key)
	if err != nil {
		return false, wrapNotFound(err)
	}
	for i := range comments {
		if comments[i].ID != commentID {
			continue
		}
		for _, prop := range comments[i].Properties {
			if prop.Key == statusPropertyKey {
				return true, nil
			}
		}
		return strings.Contains(jira.ADFToMarkdown(comments[i].Body), "fullsend:agent-status:"), nil
	}
	return false, nil
}

// CreateCommentWithMarker creates a comment with the sticky marker
// stored as a Jira comment entity property instead of embedded in the
// visible ADF body. This keeps implementation bookkeeping out of the
// user-visible conversation on Jira (which lacks HTML comments and
// would render the marker as plain text).
func (c *JiraClient) CreateCommentWithMarker(ctx context.Context, project string, number int, body Body, marker string) (*Comment, error) {
	key := issueKey(project, number)
	markerValue, err := json.Marshal(stickyMarkerProperty{Marker: marker})
	if err != nil {
		return nil, fmt.Errorf("marshal sticky marker: %w", err)
	}
	props := []jira.CommentProperty{
		{Key: stickyPropertyKey, Value: markerValue},
	}
	comment, err := c.jira.CreateCommentWithProperties(ctx, key, string(body), props)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	result := fromJiraComment(*comment)
	return &result, nil
}

// FindCommentByMarkerProperty returns the first comment that carries
// the sticky marker as a comment entity property, or nil if none is
// found. This is the property-based equivalent of the body-scanning
// approach used for GitHub/GitLab.
//
// If no property match is found, a legacy fallback scans comment bodies
// for the marker string (the old behavior before property-based storage).
// This allows transparent migration: the first update to a legacy
// comment will set the property and strip the marker from the body.
func (c *JiraClient) FindCommentByMarkerProperty(comments []jira.Comment, marker string) *jira.Comment {
	// Primary: match by entity property.
	for i := range comments {
		for _, prop := range comments[i].Properties {
			if prop.Key == stickyPropertyKey {
				var stored stickyMarkerProperty
				if json.Unmarshal(prop.Value, &stored) == nil && stored.Marker == marker {
					return &comments[i]
				}
			}
		}
	}
	// Legacy fallback: match by body text for pre-property comments.
	for i := range comments {
		md := jira.ADFToMarkdown(comments[i].Body)
		if strings.Contains(md, marker) {
			return &comments[i]
		}
	}
	return nil
}

// MigrateAndUpdateComment updates a comment's body and ensures the
// sticky marker is stored as a comment entity property. If the comment
// was found via the legacy body-text path (no property set), this also
// sets the property — transparently migrating the comment to the
// property-based scheme.
//
// The body written to Jira has the marker stripped (unlike
// GitHub/GitLab where the marker is an invisible HTML comment). The
// caller should pass the body without the marker prefix.
func (c *JiraClient) MigrateAndUpdateComment(ctx context.Context, project string, number int, commentID string, body Body, marker string) error {
	key := issueKey(project, number)
	// Set the property first — idempotent PUT. If it was already set
	// (new-style comment), this is a no-op from Jira's perspective.
	// If it was absent (legacy comment), this completes migration.
	//
	// Property-before-body ordering is intentional: if the property
	// write succeeds but the body update fails, the next run will
	// still find the comment via its property and can retry. The
	// reverse order risks losing the marker from both the body (which
	// was just stripped) and the property (which was never set).
	if err := c.jira.SetCommentProperty(ctx, key, commentID, stickyPropertyKey, stickyMarkerProperty{Marker: marker}); err != nil {
		return fmt.Errorf("setting sticky marker property on comment %s of %s: %w", commentID, key, err)
	}
	if err := c.jira.UpdateComment(ctx, key, commentID, string(body)); err != nil {
		return wrapNotFound(err)
	}
	return nil
}

// UpdateComment implements Client. Unlike GitHub/GitLab, Jira's
// update-comment endpoint needs the issue key, which is why number is
// part of the signature here.
func (c *JiraClient) UpdateComment(ctx context.Context, project string, number int, commentID string, body Body) error {
	key := issueKey(project, number)
	return wrapNotFound(c.jira.UpdateComment(ctx, key, commentID, string(body)))
}

// DeleteComment implements Client.
func (c *JiraClient) DeleteComment(ctx context.Context, project string, number int, commentID string) error {
	key := issueKey(project, number)
	return wrapNotFound(c.jira.DeleteComment(ctx, key, commentID))
}

// fromJiraComment converts a jira.Comment to a tracker.Comment. HTMLURL is
// deliberately left empty: Jira's comment permalink format isn't confirmed
// against real Jira Cloud behavior, so rather than guess at a URL shape
// that might be wrong, it's left unset instead of risking a broken link.
//
// Author is UpdateAuthor when the comment has been edited, not Author:
// someone with Edit-All-Comments can rewrite another user's comment, and
// this is adapted from jirapoll/discover.go's attribute-to-the-editor
// logic (ADR 0054) so edited content isn't misattributed to the original
// author. Note that tracker.Comment.Author is a display name only —
// unlike jirapoll, which authorizes on the stable AccountID, nothing here
// makes Author a trustworthy identifier for authorization decisions; a
// future consumer needing that would have to add one.
func fromJiraComment(c jira.Comment) Comment {
	author := c.Author.DisplayName
	if commentEdited(c) {
		author = c.UpdateAuthor.DisplayName
	}
	return Comment{
		ID:        c.ID,
		Body:      Body(jira.ADFToMarkdown(c.Body)),
		Author:    author,
		CreatedAt: c.Created,
	}
}

// commentEdited reports whether c has genuinely been edited since
// creation. UpdateAuthor.AccountID alone is Jira's signal for this
// (observed Cloud behavior, not a documented contract — see
// jirapoll/discover.go's identical hedge), so this additionally requires
// Updated to parse and be after Created, mirroring jirapoll's own
// edit-detection gate for defense in depth against that signal alone
// being wrong.
func commentEdited(c jira.Comment) bool {
	if c.UpdateAuthor.AccountID == "" {
		return false
	}
	created, err := parseJiraTimestamp(c.Created)
	if err != nil {
		return false
	}
	updated, err := parseJiraTimestamp(c.Updated)
	if err != nil {
		return false
	}
	return updated.After(created)
}
