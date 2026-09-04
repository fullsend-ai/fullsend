package tracker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// FakeJiraClient is an in-memory fake implementing the jiraClient
// interface, exported (unlike its ecosystem forge.FakeClient
// counterpart, which lives alongside it) so tests in other packages can
// exercise tracker.Client's Jira path — including its real
// jira.MarkdownToADF/ADFToMarkdown round-trip — without a live Jira
// instance. CreateComment/UpdateComment store the body as real ADF
// (mirroring Jira echoing back what it stored), so ListComments/GetIssue
// read it back through the same escaping/decoding logic a live Jira
// instance would apply.
type FakeJiraClient struct {
	Issues   map[string]*jira.Issue
	Comments map[string][]jira.Comment

	// CommentProperties maps "issueKey/commentID" to a map of
	// propertyKey -> value, mirroring Jira's per-comment entity
	// property store.
	CommentProperties map[string]map[string]json.RawMessage

	CreatedBody      string
	UpdatedIssueKey  string
	UpdatedCommentID string
	UpdatedBody      string

	// PropertyError, when non-nil, is returned by SetCommentProperty
	// to simulate permission failures.
	PropertyError error

	// UpdateError, when non-nil, is returned by UpdateComment to
	// simulate update failures.
	UpdateError error
}

func (f *FakeJiraClient) GetIssue(_ context.Context, issueIDOrKey string) (*jira.Issue, error) {
	issue, ok := f.Issues[issueIDOrKey]
	if !ok {
		return nil, fmt.Errorf("get issue %s: %w", issueIDOrKey, forge.ErrNotFound)
	}
	return issue, nil
}

func (f *FakeJiraClient) ListComments(_ context.Context, issueIDOrKey string) ([]jira.Comment, error) {
	comments := f.Comments[issueIDOrKey]
	// Attach properties to each comment, mirroring the
	// ?expand=properties behavior of the real API.
	result := make([]jira.Comment, len(comments))
	copy(result, comments)
	for i := range result {
		propKey := issueIDOrKey + "/" + result[i].ID
		if props, ok := f.CommentProperties[propKey]; ok {
			for k, v := range props {
				result[i].Properties = append(result[i].Properties, jira.CommentProperty{
					Key:   k,
					Value: v,
				})
			}
		}
	}
	return result, nil
}

func (f *FakeJiraClient) CreateComment(ctx context.Context, issueIDOrKey, body string) (*jira.Comment, error) {
	return f.CreateCommentWithProperties(ctx, issueIDOrKey, body, nil)
}

func (f *FakeJiraClient) CreateCommentWithProperties(_ context.Context, issueIDOrKey, body string, properties []jira.CommentProperty) (*jira.Comment, error) {
	f.CreatedBody = body
	adf, err := jira.MarkdownToADF(body) // mirrors Jira echoing back the ADF it stored
	if err != nil {
		return nil, err
	}
	comment := jira.Comment{
		ID:      fmt.Sprintf("%d", len(f.Comments[issueIDOrKey])+1),
		Body:    adf,
		Author:  jira.User{DisplayName: "fullsend-bot"},
		Created: "2026-08-06T00:00:00.000+0000",
	}
	if f.Comments == nil {
		f.Comments = make(map[string][]jira.Comment)
	}
	f.Comments[issueIDOrKey] = append(f.Comments[issueIDOrKey], comment)

	// Store properties if provided.
	if len(properties) > 0 {
		if f.CommentProperties == nil {
			f.CommentProperties = make(map[string]map[string]json.RawMessage)
		}
		propKey := issueIDOrKey + "/" + comment.ID
		if f.CommentProperties[propKey] == nil {
			f.CommentProperties[propKey] = make(map[string]json.RawMessage)
		}
		for _, p := range properties {
			f.CommentProperties[propKey][p.Key] = p.Value
		}
	}

	return &comment, nil
}

func (f *FakeJiraClient) UpdateComment(_ context.Context, issueIDOrKey, commentID, body string) error {
	if f.UpdateError != nil {
		return f.UpdateError
	}
	f.UpdatedIssueKey = issueIDOrKey
	f.UpdatedCommentID = commentID
	f.UpdatedBody = body
	adf, err := jira.MarkdownToADF(body) // mirrors Jira echoing back the ADF it stored
	if err != nil {
		return err
	}
	for i, c := range f.Comments[issueIDOrKey] {
		if c.ID == commentID {
			f.Comments[issueIDOrKey][i].Body = adf
			break
		}
	}
	return nil
}

func (f *FakeJiraClient) SetCommentProperty(_ context.Context, issueIDOrKey, commentID, propertyKey string, value any) error {
	if f.PropertyError != nil {
		return f.PropertyError
	}
	if f.CommentProperties == nil {
		f.CommentProperties = make(map[string]map[string]json.RawMessage)
	}
	propKey := issueIDOrKey + "/" + commentID
	if f.CommentProperties[propKey] == nil {
		f.CommentProperties[propKey] = make(map[string]json.RawMessage)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f.CommentProperties[propKey][propertyKey] = raw
	return nil
}

func (f *FakeJiraClient) DeleteComment(_ context.Context, issueIDOrKey, commentID string) error {
	comments := f.Comments[issueIDOrKey]
	for i, c := range comments {
		if c.ID == commentID {
			f.Comments[issueIDOrKey] = append(comments[:i], comments[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("delete comment %s on %s: %w", commentID, issueIDOrKey, forge.ErrNotFound)
}

var _ jiraClient = (*FakeJiraClient)(nil)

// NewFakeJiraClient returns a tracker.Client backed by an in-memory fake
// that round-trips comment bodies through the real
// jira.MarkdownToADF/ADFToMarkdown conversions, for tests that need to
// exercise Jira-specific markdown quirks (e.g. mdEscaper) without a live
// Jira instance.
func NewFakeJiraClient(baseURL string) (*JiraClient, error) {
	return NewJiraClient(&FakeJiraClient{}, baseURL)
}

// NewFakeJiraClientWithFake returns both the tracker.Client and the
// underlying FakeJiraClient, giving tests access to the fake's control
// fields (e.g. PropertyError for permission failure simulation).
func NewFakeJiraClientWithFake(baseURL string) (*JiraClient, *FakeJiraClient, error) {
	fc := &FakeJiraClient{}
	tc, err := NewJiraClient(fc, baseURL)
	if err != nil {
		return nil, nil, err
	}
	return tc, fc, nil
}
