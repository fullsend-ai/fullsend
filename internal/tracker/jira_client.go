package tracker

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// jiraClient is the Jira API surface this adapter needs. Implemented by
// jira.LiveClient; faked in tests.
type jiraClient interface {
	GetIssue(ctx context.Context, issueIDOrKey string) (*jira.Issue, error)
	ListComments(ctx context.Context, issueIDOrKey string) ([]jira.Comment, error)
	CreateComment(ctx context.Context, issueIDOrKey, body string) (*jira.Comment, error)
	UpdateComment(ctx context.Context, issueIDOrKey, commentID, body string) error
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
		return nil, err
	}
	return &Issue{
		Number: number,
		Title:  issue.Fields.Summary,
		Body:   jira.ADFToPlainText(issue.Fields.Description),
		URL:    c.baseURL + "/browse/" + key,
		Labels: issue.Fields.Labels,
	}, nil
}

// ListComments implements Client.
func (c *JiraClient) ListComments(ctx context.Context, project string, number int) ([]Comment, error) {
	key := issueKey(project, number)
	comments, err := c.jira.ListComments(ctx, key)
	if err != nil {
		return nil, err
	}
	result := make([]Comment, len(comments))
	for i, comment := range comments {
		result[i] = fromJiraComment(comment)
	}
	return result, nil
}

// CreateComment implements Client. The returned Comment's Body is
// ADFToPlainText(comment.Body), the same representation ListComments
// uses, so tracker.Comment.Body has a stable meaning regardless of which
// method produced it.
func (c *JiraClient) CreateComment(ctx context.Context, project string, number int, body string) (*Comment, error) {
	key := issueKey(project, number)
	comment, err := c.jira.CreateComment(ctx, key, body)
	if err != nil {
		return nil, err
	}
	result := fromJiraComment(*comment)
	return &result, nil
}

// UpdateComment implements Client. Unlike GitHub/GitLab, Jira's
// update-comment endpoint needs the issue key, which is why number is
// part of the signature here.
func (c *JiraClient) UpdateComment(ctx context.Context, project string, number int, commentID string, body string) error {
	key := issueKey(project, number)
	return c.jira.UpdateComment(ctx, key, commentID, body)
}

// fromJiraComment converts a jira.Comment to a tracker.Comment. HTMLURL is
// deliberately left empty: Jira's comment permalink format isn't confirmed
// against real Jira Cloud behavior, so rather than guess at a URL shape
// that might be wrong, it's left unset instead of risking a broken link.
func fromJiraComment(c jira.Comment) Comment {
	return Comment{
		ID:        c.ID,
		Body:      jira.ADFToPlainText(c.Body),
		Author:    c.Author.DisplayName,
		CreatedAt: c.Created,
	}
}
