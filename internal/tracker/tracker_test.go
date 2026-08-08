package tracker

import (
	"context"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestSplitProject(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{input: "org/project", wantOwner: "org", wantRepo: "project"},
		{input: "group/subgroup/project", wantOwner: "group/subgroup", wantRepo: "project"},
		{input: "project", wantErr: true},
		{input: "/repo", wantErr: true},
		{input: "owner/", wantErr: true},
		{input: "", wantErr: true},
	}
	for _, tc := range tests {
		owner, repo, err := splitProject(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("splitProject(%q) = (%q, %q, <nil>), want error", tc.input, owner, repo)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitProject(%q) returned unexpected error: %v", tc.input, err)
			continue
		}
		if owner != tc.wantOwner || repo != tc.wantRepo {
			t.Errorf("splitProject(%q) = (%q, %q), want (%q, %q)",
				tc.input, owner, repo, tc.wantOwner, tc.wantRepo)
		}
	}
}

func TestForgeClient_GetIssue(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.OpenIssues = map[string][]forge.Issue{
		"acme/widgets": {
			{Number: 42, Title: "Widget is broken", Body: "details", URL: "https://example.com/42", Labels: []string{"bug"}},
		},
	}

	c := NewForgeClient(fc)
	issue, err := c.GetIssue(context.Background(), "acme/widgets", 42)
	if err != nil {
		t.Fatalf("GetIssue returned error: %v", err)
	}
	if issue.Number != 42 || issue.Title != "Widget is broken" || issue.Body != "details" || issue.URL != "https://example.com/42" {
		t.Errorf("GetIssue returned unexpected issue: %+v", issue)
	}
	if len(issue.Labels) != 1 || issue.Labels[0] != "bug" {
		t.Errorf("GetIssue returned unexpected labels: %+v", issue.Labels)
	}
}

func TestForgeClient_GetIssue_NestedNamespace(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.OpenIssues = map[string][]forge.Issue{
		"group/subgroup/project": {
			{Number: 1, Title: "Nested"},
		},
	}

	c := NewForgeClient(fc)
	issue, err := c.GetIssue(context.Background(), "group/subgroup/project", 1)
	if err != nil {
		t.Fatalf("GetIssue returned error: %v", err)
	}
	if issue.Title != "Nested" {
		t.Errorf("GetIssue returned unexpected issue: %+v", issue)
	}
}

func TestForgeClient_GetIssue_NotFound(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)
	_, err := c.GetIssue(context.Background(), "acme/widgets", 99)
	if !IsNotFound(err) {
		t.Errorf("GetIssue error = %v, want tracker.ErrNotFound", err)
	}
	if !forge.IsNotFound(err) {
		t.Errorf("GetIssue error = %v, want it to still satisfy forge.ErrNotFound", err)
	}
}

func TestForgeClient_CreateAndListComments(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "fullsend-bot"

	c := NewForgeClient(fc)
	ctx := context.Background()

	created, err := c.CreateComment(ctx, "acme/widgets", 42, "hello there")
	if err != nil {
		t.Fatalf("CreateComment returned error: %v", err)
	}
	if created.Body != "hello there" || created.Author != "fullsend-bot" || created.ID == "" {
		t.Errorf("CreateComment returned unexpected comment: %+v", created)
	}

	comments, err := c.ListComments(ctx, "acme/widgets", 42)
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if len(comments) != 1 || comments[0].ID != created.ID || comments[0].Body != "hello there" {
		t.Errorf("ListComments returned unexpected comments: %+v", comments)
	}
}

func TestForgeClient_UpdateComment(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)
	ctx := context.Background()

	created, err := c.CreateComment(ctx, "acme/widgets", 42, "original")
	if err != nil {
		t.Fatalf("CreateComment returned error: %v", err)
	}

	if err := c.UpdateComment(ctx, "acme/widgets", 42, created.ID, "updated"); err != nil {
		t.Fatalf("UpdateComment returned error: %v", err)
	}

	comments, err := c.ListComments(ctx, "acme/widgets", 42)
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if len(comments) != 1 || comments[0].Body != "updated" {
		t.Errorf("ListComments after update returned unexpected comments: %+v", comments)
	}
}

func TestForgeClient_UpdateComment_InvalidID(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)

	err := c.UpdateComment(context.Background(), "acme/widgets", 42, "not-a-number", "updated")
	if err == nil {
		t.Fatal("UpdateComment with non-numeric ID should return an error")
	}
}

// staticClient is a minimal tracker.Client implementation used to verify
// the interface shape independent of the forge adapter.
type staticClient struct{}

func (staticClient) GetIssue(_ context.Context, _ string, _ int) (*Issue, error) { return nil, nil }
func (staticClient) ListComments(_ context.Context, _ string, _ int) ([]Comment, error) {
	return nil, nil
}
func (staticClient) CreateComment(_ context.Context, _ string, _ int, _ string) (*Comment, error) {
	return nil, nil
}
func (staticClient) UpdateComment(_ context.Context, _ string, _ int, _ string, _ string) error {
	return nil
}

var _ Client = staticClient{}
var _ Client = (*ForgeClient)(nil)
