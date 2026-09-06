package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// newTestJiraClient constructs a JiraClient for tests, failing immediately
// on a validation error so call sites can stay a single line.
func newTestJiraClient(t *testing.T, jc jiraClient, baseURL string) *JiraClient {
	t.Helper()
	c, err := NewJiraClient(jc, baseURL)
	if err != nil {
		t.Fatalf("NewJiraClient(%q) returned error: %v", baseURL, err)
	}
	return c
}

func TestNewJiraClient_RejectsCredentialBaseURL(t *testing.T) {
	_, err := NewJiraClient(&FakeJiraClient{}, "https://user:token@acme.atlassian.net")
	if err == nil {
		t.Fatal("NewJiraClient with credential-bearing base URL: got nil error, want an error")
	}
}

func TestJiraClient_GetIssue(t *testing.T) {
	fc := &FakeJiraClient{
		Issues: map[string]*jira.Issue{
			"PROJ-42": {
				Key: "PROJ-42",
				Fields: jira.IssueFields{
					Summary: "Widget is broken",
					Description: map[string]any{
						"type":    "doc",
						"version": 1,
						"content": []any{
							map[string]any{
								"type": "paragraph",
								"content": []any{
									map[string]any{"type": "text", "text": "details here"},
								},
							},
						},
					},
					Labels: []string{"bug"},
				},
			},
		},
	}

	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")
	issue, err := c.GetIssue(context.Background(), "PROJ", 42)
	if err != nil {
		t.Fatalf("GetIssue returned error: %v", err)
	}
	if issue.Number != 42 {
		t.Errorf("issue.Number = %d, want 42", issue.Number)
	}
	if issue.Title != "Widget is broken" {
		t.Errorf("issue.Title = %q, want %q", issue.Title, "Widget is broken")
	}
	if issue.Body != "details here" {
		t.Errorf("issue.Body = %q, want %q", issue.Body, "details here")
	}
	if len(issue.Labels) != 1 || issue.Labels[0] != "bug" {
		t.Errorf("issue.Labels = %+v, want [bug]", issue.Labels)
	}
	if issue.URL != "https://acme.atlassian.net/browse/PROJ-42" {
		t.Errorf("issue.URL = %q, want %q", issue.URL, "https://acme.atlassian.net/browse/PROJ-42")
	}
}

func TestJiraClient_GetIssue_NotFound(t *testing.T) {
	fc := &FakeJiraClient{Issues: map[string]*jira.Issue{}}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	_, err := c.GetIssue(context.Background(), "PROJ", 999)
	if !IsNotFound(err) {
		t.Errorf("GetIssue error = %v, want tracker.IsNotFound", err)
	}
}

func TestJiraClient_ListComments(t *testing.T) {
	fc := &FakeJiraClient{
		Comments: map[string][]jira.Comment{
			"PROJ-42": {
				{
					ID:      "1",
					Body:    "plain string body",
					Author:  jira.User{DisplayName: "Jane Doe"},
					Created: "2026-08-06T00:00:00.000+0000",
				},
			},
		},
	}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	comments, err := c.ListComments(context.Background(), "PROJ", 42)
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("len(comments) = %d, want 1", len(comments))
	}
	if comments[0].Body != "plain string body" {
		t.Errorf("comments[0].Body = %q, want %q", comments[0].Body, "plain string body")
	}
	if comments[0].Author != "Jane Doe" {
		t.Errorf("comments[0].Author = %q, want %q", comments[0].Author, "Jane Doe")
	}
}

func TestJiraClient_ListComments_EditedCommentAttributesToEditor(t *testing.T) {
	// Jira sets UpdateAuthor when a comment has been edited, which may
	// differ from Author if someone with Edit-All-Comments rewrote
	// another user's comment. Consumers of tracker.Comment.Author must
	// see the editor's identity, not the original author's, mirroring
	// jirapoll/discover.go's edit-attribution logic (ADR 0054) so a
	// rewritten comment can't be misattributed to whoever originally
	// posted it.
	fc := &FakeJiraClient{
		Comments: map[string][]jira.Comment{
			"PROJ-42": {
				{
					ID:           "1",
					Body:         "edited body",
					Author:       jira.User{DisplayName: "Original Author"},
					UpdateAuthor: jira.User{AccountID: "acct-2", DisplayName: "Editor"},
					Created:      "2026-08-06T00:00:00.000+0000",
					Updated:      "2026-08-06T01:00:00.000+0000",
				},
				{
					ID:      "2",
					Body:    "unedited body",
					Author:  jira.User{DisplayName: "Original Author"},
					Created: "2026-08-06T00:00:00.000+0000",
				},
			},
		},
	}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	comments, err := c.ListComments(context.Background(), "PROJ", 42)
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("len(comments) = %d, want 2", len(comments))
	}
	if comments[0].Author != "Editor" {
		t.Errorf("edited comment Author = %q, want %q (the editor, not the original author)", comments[0].Author, "Editor")
	}
	if comments[1].Author != "Original Author" {
		t.Errorf("unedited comment Author = %q, want %q (UpdateAuthor unset, so Author stands)", comments[1].Author, "Original Author")
	}
}

func TestJiraClient_ListComments_UpdateAuthorIgnoredWithoutLaterUpdatedTimestamp(t *testing.T) {
	// UpdateAuthor.AccountID alone isn't a reliable edit signal — mirror
	// jirapoll/discover.go's defense-in-depth gate of also requiring
	// Updated to parse and be after Created before trusting it.
	fc := &FakeJiraClient{
		Comments: map[string][]jira.Comment{
			"PROJ-42": {
				{
					ID:           "1",
					Body:         "not actually edited",
					Author:       jira.User{DisplayName: "Original Author"},
					UpdateAuthor: jira.User{AccountID: "acct-2", DisplayName: "Editor"},
					Created:      "2026-08-06T00:00:00.000+0000",
					Updated:      "2026-08-06T00:00:00.000+0000",
				},
				{
					ID:           "2",
					Body:         "unparseable updated timestamp",
					Author:       jira.User{DisplayName: "Original Author"},
					UpdateAuthor: jira.User{AccountID: "acct-2", DisplayName: "Editor"},
					Created:      "2026-08-06T00:00:00.000+0000",
					Updated:      "not-a-timestamp",
				},
			},
		},
	}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	comments, err := c.ListComments(context.Background(), "PROJ", 42)
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("len(comments) = %d, want 2", len(comments))
	}
	if comments[0].Author != "Original Author" {
		t.Errorf("Updated == Created comment Author = %q, want %q (not after Created, so not treated as edited)", comments[0].Author, "Original Author")
	}
	if comments[1].Author != "Original Author" {
		t.Errorf("unparseable Updated comment Author = %q, want %q (can't confirm edit, so not treated as edited)", comments[1].Author, "Original Author")
	}
}

func TestJiraClient_CreateComment(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	comment, err := c.CreateComment(context.Background(), "PROJ", 42, "**hello** there")
	if err != nil {
		t.Fatalf("CreateComment returned error: %v", err)
	}
	if fc.CreatedBody != "**hello** there" {
		t.Errorf("underlying jira client received body %q, want %q", fc.CreatedBody, "**hello** there")
	}
	if _, ok := fc.Comments["PROJ-42"]; !ok {
		t.Fatal("expected comment to be created against issue key PROJ-42")
	}
	if comment.Body != "**hello** there" {
		t.Errorf("comment.Body = %q, want %q (Markdown, consistent with ListComments)", comment.Body, "**hello** there")
	}
}

func TestJiraClient_UpdateComment(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	if err := c.UpdateComment(context.Background(), "PROJ", 42, "50001", "updated text"); err != nil {
		t.Fatalf("UpdateComment returned error: %v", err)
	}
	if fc.UpdatedIssueKey != "PROJ-42" {
		t.Errorf("underlying jira client received issue key %q, want %q", fc.UpdatedIssueKey, "PROJ-42")
	}
	if fc.UpdatedCommentID != "50001" {
		t.Errorf("underlying jira client received comment ID %q, want %q", fc.UpdatedCommentID, "50001")
	}
	if fc.UpdatedBody != "updated text" {
		t.Errorf("underlying jira client received body %q, want %q", fc.UpdatedBody, "updated text")
	}
}

func TestJiraClient_DeleteComment(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")
	ctx := context.Background()

	// Create a comment first so we have something to delete.
	_, err := c.CreateComment(ctx, "PROJ", 42, "to be deleted")
	if err != nil {
		t.Fatalf("CreateComment returned error: %v", err)
	}

	comments, err := c.ListComments(ctx, "PROJ", 42)
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment before delete, got %d", len(comments))
	}

	if err := c.DeleteComment(ctx, "PROJ", 42, comments[0].ID); err != nil {
		t.Fatalf("DeleteComment returned error: %v", err)
	}

	comments, err = c.ListComments(ctx, "PROJ", 42)
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("expected 0 comments after delete, got %d", len(comments))
	}
}

func TestJiraClient_DeleteComment_NotFound(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	err := c.DeleteComment(context.Background(), "PROJ", 42, "nonexistent")
	if err == nil {
		t.Fatal("DeleteComment on nonexistent comment: got nil error, want error")
	}
	if !IsNotFound(err) {
		t.Errorf("DeleteComment error does not satisfy tracker.IsNotFound: %v", err)
	}
}

func TestJiraClient_NotFoundWrapping(t *testing.T) {
	// JiraClient must wrap forge.ErrNotFound into tracker.ErrNotFound so
	// callers using tracker.IsNotFound get the expected result. Verify
	// that the wrapper also preserves the underlying forge.ErrNotFound.
	fc := &FakeJiraClient{Issues: map[string]*jira.Issue{}}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	_, err := c.GetIssue(context.Background(), "PROJ", 999)
	if err == nil {
		t.Fatal("GetIssue on missing issue: got nil error, want not-found")
	}
	if !IsNotFound(err) {
		t.Errorf("GetIssue error does not satisfy tracker.IsNotFound: %v", err)
	}
	// The underlying forge.ErrNotFound should still be reachable for debug.
	if !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("GetIssue error does not unwrap to forge.ErrNotFound: %v", err)
	}
}

func TestJiraClient_CreateCommentWithMarker(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	marker := "<!-- fullsend:triage-agent -->"
	comment, err := c.CreateCommentWithMarker(context.Background(), "PROJ", 42, "hello", marker)
	if err != nil {
		t.Fatalf("CreateCommentWithMarker returned error: %v", err)
	}
	if comment.Body != "hello" {
		t.Errorf("comment.Body = %q, want %q", comment.Body, "hello")
	}

	// Verify property was set.
	propKey := "PROJ-42/" + comment.ID
	props, ok := fc.CommentProperties[propKey]
	if !ok {
		t.Fatal("expected comment properties to be set")
	}
	var stored stickyMarkerProperty
	if err := json.Unmarshal(props[stickyPropertyKey], &stored); err != nil {
		t.Fatalf("unmarshal property value: %v", err)
	}
	if stored.Marker != marker {
		t.Errorf("stored marker = %q, want %q", stored.Marker, marker)
	}
}

func TestJiraClient_StatusCommentProperties(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")
	ctx := context.Background()
	marker := "<!-- fullsend:agent-status:run-42 -->"

	created, err := c.CreateStatusComment(ctx, "PROJ", 42, "started", marker, false)
	if err != nil {
		t.Fatalf("CreateStatusComment: %v", err)
	}
	if created.Body != "started" {
		t.Errorf("created body = %q, want %q", created.Body, "started")
	}

	found, terminal, err := c.FindStatusComment(ctx, "PROJ", 42, marker)
	if err != nil {
		t.Fatalf("FindStatusComment: %v", err)
	}
	if found == nil {
		t.Fatal("FindStatusComment returned nil")
	}
	if found.ID != created.ID {
		t.Errorf("found ID = %q, want %q", found.ID, created.ID)
	}
	if terminal {
		t.Error("new start comment unexpectedly terminal")
	}
	isStatus, err := c.IsStatusComment(ctx, "PROJ", 42, created.ID)
	if err != nil {
		t.Fatalf("IsStatusComment: %v", err)
	}
	if !isStatus {
		t.Error("property-backed comment was not recognized as a status comment")
	}
	normal, err := c.CreateComment(ctx, "PROJ", 42, "ordinary comment")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	isStatus, err = c.IsStatusComment(ctx, "PROJ", 42, normal.ID)
	if err != nil {
		t.Fatalf("IsStatusComment for ordinary comment: %v", err)
	}
	if isStatus {
		t.Error("ordinary comment was recognized as a status comment")
	}

	if err := c.UpdateStatusComment(ctx, "PROJ", 42, created.ID, "finished", marker, true); err != nil {
		t.Fatalf("UpdateStatusComment: %v", err)
	}
	found, terminal, err = c.FindStatusComment(ctx, "PROJ", 42, marker)
	if err != nil {
		t.Fatalf("FindStatusComment after update: %v", err)
	}
	if found == nil {
		t.Fatal("FindStatusComment after update returned nil")
	}
	if found.Body != "finished" {
		t.Errorf("updated body = %q, want %q", found.Body, "finished")
	}
	if !terminal {
		t.Error("updated completion comment is not terminal")
	}
}

func TestJiraClient_FindStatusComment_LegacyFallback(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")
	ctx := context.Background()
	marker := "<!-- fullsend:agent-status:legacy-run -->"

	legacy, err := fc.CreateComment(ctx, "PROJ-42", marker+"\n<!-- fullsend:status:terminal -->\nfinished")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	found, terminal, err := c.FindStatusComment(ctx, "PROJ", 42, marker)
	if err != nil {
		t.Fatalf("FindStatusComment: %v", err)
	}
	if found == nil || found.ID != legacy.ID {
		t.Fatalf("legacy status comment not found: %+v", found)
	}
	if !terminal {
		t.Error("legacy terminal marker was not recognized")
	}

	found, terminal, err = c.FindStatusComment(ctx, "PROJ", 42, "<!-- fullsend:agent-status:missing -->")
	if err != nil {
		t.Fatalf("FindStatusComment missing marker: %v", err)
	}
	if found != nil || terminal {
		t.Errorf("missing marker returned comment=%+v terminal=%t", found, terminal)
	}
}

func TestJiraClient_UpdateStatusComment_PropertyError(t *testing.T) {
	propertyErr := errors.New("property denied")
	fc := &FakeJiraClient{PropertyError: propertyErr}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	err := c.UpdateStatusComment(context.Background(), "PROJ", 42, "1", "finished", "marker", true)
	if !errors.Is(err, propertyErr) {
		t.Errorf("UpdateStatusComment error = %v, want wrapped property error", err)
	}
}

func TestJiraClient_FindCommentByMarkerProperty(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")
	ctx := context.Background()
	marker := "<!-- fullsend:triage-agent -->"

	// Create a comment with a property-based marker.
	_, err := c.CreateCommentWithMarker(ctx, "PROJ", 42, "content", marker)
	if err != nil {
		t.Fatalf("CreateCommentWithMarker: %v", err)
	}

	// List raw Jira comments and find by property.
	jiraComments, err := c.ListJiraComments(ctx, "PROJ", 42)
	if err != nil {
		t.Fatalf("ListJiraComments: %v", err)
	}

	found := c.FindCommentByMarkerProperty(jiraComments, marker)
	if found == nil {
		t.Fatal("FindCommentByMarkerProperty returned nil, want a match")
	}
	if found.ID != "1" {
		t.Errorf("found.ID = %q, want %q", found.ID, "1")
	}

	// Search for a different marker: should not find.
	notFound := c.FindCommentByMarkerProperty(jiraComments, "<!-- other -->")
	if notFound != nil {
		t.Errorf("FindCommentByMarkerProperty should return nil for non-matching marker, got %+v", notFound)
	}
}

func TestJiraClient_FindCommentByMarkerProperty_LegacyFallback(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")
	ctx := context.Background()
	marker := "<!-- fullsend:triage-agent -->"

	// Create a legacy comment: marker in body, no property.
	legacyBody := marker + "\nlegacy content"
	_, err := fc.CreateComment(ctx, "PROJ-42", legacyBody)
	if err != nil {
		t.Fatalf("CreateComment (legacy): %v", err)
	}

	jiraComments, err := c.ListJiraComments(ctx, "PROJ", 42)
	if err != nil {
		t.Fatalf("ListJiraComments: %v", err)
	}

	// Should find via body-text fallback.
	found := c.FindCommentByMarkerProperty(jiraComments, marker)
	if found == nil {
		t.Fatal("FindCommentByMarkerProperty should find legacy comment via body fallback")
	}
}

func TestJiraClient_MigrateAndUpdateComment(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")
	ctx := context.Background()
	marker := "<!-- fullsend:triage-agent -->"

	// Create a legacy comment: marker in body, no property.
	legacyBody := marker + "\nlegacy content"
	_, err := fc.CreateComment(ctx, "PROJ-42", legacyBody)
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	// Migrate: update body and set property.
	err = c.MigrateAndUpdateComment(ctx, "PROJ", 42, "1", "migrated content", marker)
	if err != nil {
		t.Fatalf("MigrateAndUpdateComment: %v", err)
	}

	// Verify body was updated.
	if fc.UpdatedBody != "migrated content" {
		t.Errorf("UpdatedBody = %q, want %q", fc.UpdatedBody, "migrated content")
	}

	// Verify property was set.
	propKey := "PROJ-42/1"
	props, ok := fc.CommentProperties[propKey]
	if !ok {
		t.Fatal("expected comment properties to be set after migration")
	}
	var stored stickyMarkerProperty
	if err := json.Unmarshal(props[stickyPropertyKey], &stored); err != nil {
		t.Fatalf("unmarshal property value: %v", err)
	}
	if stored.Marker != marker {
		t.Errorf("stored marker = %q, want %q", stored.Marker, marker)
	}
}

func TestJiraClient_MigrateAndUpdateComment_SetPropertyError(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")
	ctx := context.Background()
	marker := "<!-- fullsend:triage-agent -->"

	// Create a comment to update.
	_, err := fc.CreateComment(ctx, "PROJ-42", "content")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	// Simulate a property write failure.
	fc.PropertyError = errors.New("403 forbidden")

	err = c.MigrateAndUpdateComment(ctx, "PROJ", 42, "1", "updated", marker)
	if err == nil {
		t.Fatal("expected error from MigrateAndUpdateComment when SetCommentProperty fails")
	}
	if !errors.Is(err, fc.PropertyError) {
		t.Errorf("error should wrap PropertyError, got: %v", err)
	}
}

func TestJiraClient_MigrateAndUpdateComment_UpdateCommentError(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")
	ctx := context.Background()
	marker := "<!-- fullsend:triage-agent -->"

	// Create a comment to update.
	_, err := fc.CreateComment(ctx, "PROJ-42", "content")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	// Simulate an update failure (property write succeeds, body update fails).
	fc.UpdateError = forge.ErrNotFound

	err = c.MigrateAndUpdateComment(ctx, "PROJ", 42, "1", "updated", marker)
	if err == nil {
		t.Fatal("expected error from MigrateAndUpdateComment when UpdateComment fails")
	}
	if !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("error should wrap forge.ErrNotFound, got: %v", err)
	}
}

func TestNewFakeJiraClient(t *testing.T) {
	c, err := NewFakeJiraClient("https://acme.atlassian.net")
	if err != nil {
		t.Fatalf("NewFakeJiraClient returned error: %v", err)
	}

	ctx := context.Background()
	created, err := c.CreateComment(ctx, "PROJ", 42, "test comment")
	if err != nil {
		t.Fatalf("CreateComment returned error: %v", err)
	}
	if created.ID == "" {
		t.Error("CreateComment returned empty ID")
	}
	if created.Body != "test comment" {
		t.Errorf("CreateComment body = %q, want %q", created.Body, "test comment")
	}
}

var _ Client = (*JiraClient)(nil)
var _ StatusCommentClient = (*JiraClient)(nil)
