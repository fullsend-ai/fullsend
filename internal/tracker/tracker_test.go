package tracker

import (
	"context"
	"strings"
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

func TestForgeClient_GetIssue_NotFound_NoStutteredMessage(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)
	_, err := c.GetIssue(context.Background(), "acme/widgets", 99)
	if got := err.Error(); strings.Count(got, "not found") != 1 {
		t.Errorf("GetIssue error = %q, want \"not found\" to appear exactly once", got)
	}
}

func TestForgeClient_UpdateComment_NotFound_NoStutteredMessage(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)
	err := c.UpdateComment(context.Background(), "acme/widgets", 42, "99", "updated")
	if got := err.Error(); strings.Count(got, "not found") != 1 {
		t.Errorf("UpdateComment error = %q, want \"not found\" to appear exactly once", got)
	}
}

func TestForgeClient_ListComments_NotFound(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"ListIssueComments": forge.ErrNotFound}
	c := NewForgeClient(fc)
	_, err := c.ListComments(context.Background(), "acme/widgets", 42)
	if !IsNotFound(err) {
		t.Errorf("ListComments error = %v, want tracker.ErrNotFound", err)
	}
	if !forge.IsNotFound(err) {
		t.Errorf("ListComments error = %v, want it to still satisfy forge.ErrNotFound", err)
	}
}

func TestForgeClient_CreateComment_NotFound(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"CreateIssueComment": forge.ErrNotFound}
	c := NewForgeClient(fc)
	_, err := c.CreateComment(context.Background(), "acme/widgets", 42, "hello")
	if !IsNotFound(err) {
		t.Errorf("CreateComment error = %v, want tracker.ErrNotFound", err)
	}
	if !forge.IsNotFound(err) {
		t.Errorf("CreateComment error = %v, want it to still satisfy forge.ErrNotFound", err)
	}
}

func TestForgeClient_UpdateComment_NotFound(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)
	err := c.UpdateComment(context.Background(), "acme/widgets", 42, "99", "updated")
	if !IsNotFound(err) {
		t.Errorf("UpdateComment error = %v, want tracker.ErrNotFound", err)
	}
	if !forge.IsNotFound(err) {
		t.Errorf("UpdateComment error = %v, want it to still satisfy forge.ErrNotFound", err)
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

func TestForgeClient_DeleteComment(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = "bot"
	c := NewForgeClient(fc)
	ctx := context.Background()

	created, err := c.CreateComment(ctx, "acme/widgets", 42, "to be deleted")
	if err != nil {
		t.Fatalf("CreateComment returned error: %v", err)
	}

	if err := c.DeleteComment(ctx, "acme/widgets", 42, created.ID); err != nil {
		t.Fatalf("DeleteComment returned error: %v", err)
	}

	comments, err := c.ListComments(ctx, "acme/widgets", 42)
	if err != nil {
		t.Fatalf("ListComments returned error: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("expected 0 comments after delete, got %d", len(comments))
	}
}

func TestForgeClient_DeleteComment_InvalidID(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)

	err := c.DeleteComment(context.Background(), "acme/widgets", 42, "not-a-number")
	if err == nil {
		t.Fatal("DeleteComment with non-numeric ID should return an error")
	}
}

func TestForgeClient_DeleteComment_InvalidProject(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)

	err := c.DeleteComment(context.Background(), "invalid", 42, "1")
	if err == nil {
		t.Fatal("DeleteComment with invalid project should return an error")
	}
}

func TestForgeClient_AddIssueReaction(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)
	ctx := context.Background()

	id, err := c.AddIssueReaction(ctx, "acme/widgets", 42, "eyes")
	if err != nil {
		t.Fatalf("AddIssueReaction returned error: %v", err)
	}
	if id == 0 {
		t.Error("AddIssueReaction returned zero ID")
	}
	if len(fc.AddedReactions) != 1 || fc.AddedReactions[0].Content != "eyes" {
		t.Errorf("unexpected reactions: %+v", fc.AddedReactions)
	}
}

func TestForgeClient_AddIssueReaction_InvalidProject(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)

	_, err := c.AddIssueReaction(context.Background(), "invalid", 42, "eyes")
	if err == nil {
		t.Fatal("AddIssueReaction with invalid project should return an error")
	}
}

func TestForgeClient_DeleteIssueReaction(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)
	ctx := context.Background()

	id, err := c.AddIssueReaction(ctx, "acme/widgets", 42, "eyes")
	if err != nil {
		t.Fatalf("AddIssueReaction returned error: %v", err)
	}

	if err := c.DeleteIssueReaction(ctx, "acme/widgets", 42, id); err != nil {
		t.Fatalf("DeleteIssueReaction returned error: %v", err)
	}
}

func TestForgeClient_DeleteIssueReaction_InvalidProject(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)

	err := c.DeleteIssueReaction(context.Background(), "invalid", 42, 1)
	if err == nil {
		t.Fatal("DeleteIssueReaction with invalid project should return an error")
	}
}

func TestForgeClient_AddCommentReaction(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)
	ctx := context.Background()

	id, err := c.AddCommentReaction(ctx, "acme/widgets", 42, "100", "+1")
	if err != nil {
		t.Fatalf("AddCommentReaction returned error: %v", err)
	}
	if id == 0 {
		t.Error("AddCommentReaction returned zero ID")
	}
	if len(fc.AddedCommentReactions) != 1 || fc.AddedCommentReactions[0].Content != "+1" {
		t.Errorf("unexpected comment reactions: %+v", fc.AddedCommentReactions)
	}
}

func TestForgeClient_AddCommentReaction_InvalidID(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)

	_, err := c.AddCommentReaction(context.Background(), "acme/widgets", 42, "not-a-number", "+1")
	if err == nil {
		t.Fatal("AddCommentReaction with non-numeric comment ID should return an error")
	}
}

func TestForgeClient_AddCommentReaction_InvalidProject(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)

	_, err := c.AddCommentReaction(context.Background(), "invalid", 42, "100", "+1")
	if err == nil {
		t.Fatal("AddCommentReaction with invalid project should return an error")
	}
}

func TestForgeClient_DeleteCommentReaction(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)
	ctx := context.Background()

	id, err := c.AddCommentReaction(ctx, "acme/widgets", 42, "100", "+1")
	if err != nil {
		t.Fatalf("AddCommentReaction returned error: %v", err)
	}

	if err := c.DeleteCommentReaction(ctx, "acme/widgets", 42, "100", id); err != nil {
		t.Fatalf("DeleteCommentReaction returned error: %v", err)
	}
}

func TestForgeClient_DeleteCommentReaction_InvalidID(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)

	err := c.DeleteCommentReaction(context.Background(), "acme/widgets", 42, "not-a-number", 1)
	if err == nil {
		t.Fatal("DeleteCommentReaction with non-numeric comment ID should return an error")
	}
}

func TestForgeClient_DeleteCommentReaction_InvalidProject(t *testing.T) {
	fc := forge.NewFakeClient()
	c := NewForgeClient(fc)

	err := c.DeleteCommentReaction(context.Background(), "invalid", 42, "100", 1)
	if err == nil {
		t.Fatal("DeleteCommentReaction with invalid project should return an error")
	}
}

// staticClient is a minimal tracker.Client implementation used to verify
// the interface shape independent of the forge adapter.
type staticClient struct{}

func (staticClient) GetIssue(_ context.Context, _ string, _ int) (*Issue, error) { return nil, nil }
func (staticClient) ListComments(_ context.Context, _ string, _ int) ([]Comment, error) {
	return nil, nil
}
func (staticClient) CreateComment(_ context.Context, _ string, _ int, _ Body) (*Comment, error) {
	return nil, nil
}
func (staticClient) UpdateComment(_ context.Context, _ string, _ int, _ string, _ Body) error {
	return nil
}
func (staticClient) DeleteComment(_ context.Context, _ string, _ int, _ string) error {
	return nil
}

var _ Client = staticClient{}
var _ Client = (*ForgeClient)(nil)
var _ Reactor = (*ForgeClient)(nil)
