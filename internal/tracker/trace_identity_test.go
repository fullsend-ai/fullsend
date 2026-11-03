package tracker

import (
	"context"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// The tests in this file are V03 for AISDLC-576: the immutable database IDs
// required by the D01 identity tuple must survive tracker mapping, and the
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
	if err != nil {
		t.Fatalf("GetIssue returned error: %v", err)
	}
	if issue.ID != 2468913570 {
		t.Errorf("issue.ID = %d, want 2468913570 (immutable forge issue id)", issue.ID)
	}
	if issue.Number != 42 {
		t.Errorf("issue.Number = %d, want 42 (display number)", issue.Number)
	}
}

func TestForgeClient_CreateIssue_MapsImmutableID(t *testing.T) {
	fc := forge.NewFakeClient()

	c := NewForgeClient(fc)
	issue, err := c.CreateIssue(context.Background(), "acme/widgets", "New work", "body", CreateIssueOptions{})
	if err != nil {
		t.Fatalf("CreateIssue returned error: %v", err)
	}
	if issue.ID == 0 {
		t.Errorf("issue.ID = 0, want the fake's synthesized immutable issue id")
	}
	if issue.Number == 0 {
		t.Errorf("issue.Number = 0, want a display number")
	}
	if issue.ID == int64(issue.Number) {
		t.Errorf("issue.ID (%d) must be a distinct id space from the display number (%d)", issue.ID, issue.Number)
	}
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
	if err != nil {
		t.Fatalf("GetIssue returned error: %v", err)
	}
	if issue.ID != 10100 {
		t.Errorf("issue.ID = %d, want 10100 (immutable Jira issue id)", issue.ID)
	}
	if issue.Key != "PROJ-42" {
		t.Errorf("issue.Key = %q, want %q (display key)", issue.Key, "PROJ-42")
	}
	if issue.Number != 42 {
		t.Errorf("issue.Number = %d, want 42 (numeric key suffix)", issue.Number)
	}
}

func TestJiraClient_CreateIssue_MapsImmutableID(t *testing.T) {
	fc := &FakeJiraClient{}
	c := newTestJiraClient(t, fc, "https://acme.atlassian.net")

	issue, err := c.CreateIssue(context.Background(), "PROJ", "New work", "body", CreateIssueOptions{IssueType: "Task"})
	if err != nil {
		t.Fatalf("CreateIssue returned error: %v", err)
	}
	if issue.ID == 0 {
		t.Errorf("issue.ID = 0, want the immutable id of the created issue")
	}
	if issue.Key == "" {
		t.Errorf("issue.Key is empty, want the display key of the created issue")
	}
}
