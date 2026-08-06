package tracker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// fakeJiraClient is a hand-written fake implementing the jiraClient
// interface, mirroring how internal/jirapoll's tests mock JiraClient.
type fakeJiraClient struct {
	issues   map[string]*jira.Issue
	comments map[string][]jira.Comment

	createdBody      string
	updatedIssueKey  string
	updatedCommentID string
	updatedBody      string
}

func (f *fakeJiraClient) GetIssue(_ context.Context, issueIDOrKey string) (*jira.Issue, error) {
	issue, ok := f.issues[issueIDOrKey]
	if !ok {
		return nil, fmt.Errorf("get issue %s: %w", issueIDOrKey, forge.ErrNotFound)
	}
	return issue, nil
}

func (f *fakeJiraClient) ListComments(_ context.Context, issueIDOrKey string) ([]jira.Comment, error) {
	return f.comments[issueIDOrKey], nil
}

func (f *fakeJiraClient) CreateComment(_ context.Context, issueIDOrKey, body string) (*jira.Comment, error) {
	f.createdBody = body
	comment := jira.Comment{
		ID:      "50001",
		Body:    map[string]any{"type": "doc", "version": 1, "content": []any{}}, // deliberately not body's markdown
		Author:  jira.User{DisplayName: "fullsend-bot"},
		Created: "2026-08-06T00:00:00.000+0000",
	}
	if f.comments == nil {
		f.comments = make(map[string][]jira.Comment)
	}
	f.comments[issueIDOrKey] = append(f.comments[issueIDOrKey], comment)
	return &comment, nil
}

func (f *fakeJiraClient) UpdateComment(_ context.Context, issueIDOrKey, commentID, body string) error {
	f.updatedIssueKey = issueIDOrKey
	f.updatedCommentID = commentID
	f.updatedBody = body
	return nil
}

var _ jiraClient = (*fakeJiraClient)(nil)

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
	_, err := NewJiraClient(&fakeJiraClient{}, "https://user:token@acme.atlassian.net")
	if err == nil {
		t.Fatal("NewJiraClient with credential-bearing base URL: got nil error, want an error")
	}
}

func TestJiraClient_GetIssue(t *testing.T) {
	fc := &fakeJiraClient{
		issues: map[string]*jira.Issue{
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
	fc := &fakeJiraClient{issues: map[string]*jira.Issue{}}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	_, err := c.GetIssue(context.Background(), "PROJ", 999)
	if !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("GetIssue error = %v, want forge.ErrNotFound", err)
	}
}

func TestJiraClient_ListComments(t *testing.T) {
	fc := &fakeJiraClient{
		comments: map[string][]jira.Comment{
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

func TestJiraClient_CreateComment(t *testing.T) {
	fc := &fakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	comment, err := c.CreateComment(context.Background(), "PROJ", 42, "**hello** there")
	if err != nil {
		t.Fatalf("CreateComment returned error: %v", err)
	}
	if fc.createdBody != "**hello** there" {
		t.Errorf("underlying jira client received body %q, want %q", fc.createdBody, "**hello** there")
	}
	if _, ok := fc.comments["PROJ-42"]; !ok {
		t.Fatal("expected comment to be created against issue key PROJ-42")
	}
	if comment.Body != "**hello** there" {
		t.Errorf("comment.Body = %q, want the original markdown %q, not a round-tripped ADF value", comment.Body, "**hello** there")
	}
}

func TestJiraClient_UpdateComment(t *testing.T) {
	fc := &fakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	if err := c.UpdateComment(context.Background(), "PROJ", 42, "50001", "updated text"); err != nil {
		t.Fatalf("UpdateComment returned error: %v", err)
	}
	if fc.updatedIssueKey != "PROJ-42" {
		t.Errorf("underlying jira client received issue key %q, want %q", fc.updatedIssueKey, "PROJ-42")
	}
	if fc.updatedCommentID != "50001" {
		t.Errorf("underlying jira client received comment ID %q, want %q", fc.updatedCommentID, "50001")
	}
	if fc.updatedBody != "updated text" {
		t.Errorf("underlying jira client received body %q, want %q", fc.updatedBody, "updated text")
	}
}

var _ Client = (*JiraClient)(nil)
