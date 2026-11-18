package tracker

import (
	"context"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file cover ADR 0154: the immutable database IDs
// required by the identity tuple must survive tracker mapping, and the
// Jira immutable issue ID must stay distinct from the display key.

func TestForgeClient_GetIssue_MapsImmutableID(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.OpenIssues = map[string][]forge.Issue{
		"acme/widgets": {
			{ID: 2468913570, Number: 42, Title: "Widget is broken", Body: "details", URL: "https://example.com/42"},
		},
	}

	c := NewForgeClient(fc)
	issue, err := c.GetIssue(context.Background(), "acme/widgets", 42)
	require.NoError(t, err)
	assert.Equal(t, int64(2468913570), issue.ID, "immutable forge issue id")
	assert.Equal(t, 42, issue.Number, "display number")
}

func TestForgeClient_CreateIssue_MapsImmutableID(t *testing.T) {
	fc := forge.NewFakeClient()

	c := NewForgeClient(fc)
	issue, err := c.CreateIssue(context.Background(), "acme/widgets", "New work", "body", CreateIssueOptions{})
	require.NoError(t, err)
	assert.NotZero(t, issue.ID, "the fake's synthesized immutable issue id")
	assert.NotZero(t, issue.Number, "display number")
	assert.NotEqual(t, int64(issue.Number), issue.ID, "identity and display number use distinct id spaces")
}

// TestJiraClient_GetIssue_IDAndKeyDistinct covers the Jira ID/key
// distinction: the immutable numeric issue id and the display key must both
// survive mapping without being conflated.
func TestJiraClient_GetIssue_IDAndKeyDistinct(t *testing.T) {
	fc := &FakeJiraClient{
		Issues: map[string]*jira.Issue{
			"PROJ-42": {
				ID:  "10100",
				Key: "PROJ-42",
				Fields: jira.IssueFields{
					Summary: "Widget is broken",
					Project: &jira.ProjectRef{ID: "10000", Key: "PROJ"},
				},
			},
		},
	}

	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")
	issue, err := c.GetIssue(context.Background(), "PROJ", 42)
	require.NoError(t, err)
	assert.Equal(t, int64(10100), issue.ID, "immutable Jira issue id")
	assert.Equal(t, "PROJ-42", issue.Key, "display key")
	assert.Equal(t, 42, issue.Number, "numeric key suffix")
	assert.Equal(t, int64(10000), issue.ContainerID)
}

func TestJiraClient_CreateIssue_MapsImmutableID(t *testing.T) {
	fc := &FakeJiraClient{ProjectIDs: map[string]string{"PROJ": "20000"}}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	issue, err := c.CreateIssue(context.Background(), "PROJ", "New work", "body", CreateIssueOptions{IssueType: "Task"})
	require.NoError(t, err)
	assert.NotZero(t, issue.ID, "immutable id of the created issue")
	assert.NotEmpty(t, issue.Key, "display key of the created issue")
	assert.Zero(t, issue.ContainerID, "Jira create response has no project ID")
	read, err := c.GetIssue(context.Background(), "PROJ", issue.Number)
	require.NoError(t, err)
	assert.Equal(t, issue.ID, read.ID)
	assert.Equal(t, int64(20000), read.ContainerID, "trusted read resolves the container")
}

func TestJiraClient_GetIssue_UnresolvedProject(t *testing.T) {
	for _, project := range []*jira.ProjectRef{nil, {}, {ID: "bad"}, {ID: "0"}, {ID: "-1"}, {ID: "9223372036854775808"}} {
		fc := &FakeJiraClient{Issues: map[string]*jira.Issue{
			"PROJ-42": {ID: "10100", Key: "PROJ-42", Fields: jira.IssueFields{Project: project}},
		}}
		c := newTestJiraClient(t, fc, "https://acme.atlassian.net")
		issue, err := c.GetIssue(context.Background(), "PROJ", 42)
		require.NoError(t, err)
		assert.Zero(t, issue.ContainerID, "project %#v must remain unresolved", project)
	}
}
