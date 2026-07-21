package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readJSONBody unmarshals the JSON body from a request into v.
func readJSONBody(t *testing.T, r *http.Request, v any) {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, v))
}

// writeJSON writes v as JSON to the response with the given status code.
func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

// ---------------------------------------------------------------------------
// issue.go tests
// ---------------------------------------------------------------------------

func TestCreateIssue(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "test-token", r.Header.Get("PRIVATE-TOKEN"))

		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "Bug report", body["title"])
		assert.Equal(t, "Something broke", body["description"])
		assert.Equal(t, "bug,urgent", body["labels"])

		writeJSON(t, w, http.StatusCreated, map[string]any{
			"iid":         42,
			"title":       "Bug report",
			"description": "Something broke",
			"web_url":     "https://gitlab.com/myorg/myrepo/-/issues/42",
			"labels":      []string{"bug", "urgent"},
		})
	})

	issue, err := client.CreateIssue(ctx, "myorg", "myrepo", "Bug report", "Something broke", "bug", "urgent")
	require.NoError(t, err)
	assert.Equal(t, 42, issue.Number)
	assert.Equal(t, "Bug report", issue.Title)
	assert.Equal(t, "Something broke", issue.Body)
	assert.Equal(t, "https://gitlab.com/myorg/myrepo/-/issues/42", issue.URL)
	assert.Equal(t, []string{"bug", "urgent"}, issue.Labels)
}

func TestCreateIssue_NoLabels(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		readJSONBody(t, r, &body)
		_, hasLabels := body["labels"]
		assert.False(t, hasLabels, "labels should not be sent when none provided")

		writeJSON(t, w, http.StatusCreated, map[string]any{
			"iid":         1,
			"title":       "No labels",
			"description": "",
			"web_url":     "https://gitlab.com/myorg/myrepo/-/issues/1",
			"labels":      []string{},
		})
	})

	issue, err := client.CreateIssue(ctx, "myorg", "myrepo", "No labels", "")
	require.NoError(t, err)
	assert.Equal(t, 1, issue.Number)
}

func TestGetIssue_StringLabels(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/5", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]any{
			"iid":         5,
			"title":       "Test issue",
			"description": "Issue body",
			"web_url":     "https://gitlab.com/myorg/myrepo/-/issues/5",
			"labels":      []string{"feature", "docs"},
		})
	})

	issue, err := client.GetIssue(ctx, "myorg", "myrepo", 5)
	require.NoError(t, err)
	assert.Equal(t, 5, issue.Number)
	assert.Equal(t, "Test issue", issue.Title)
	assert.Equal(t, "Issue body", issue.Body)
	assert.Equal(t, []string{"feature", "docs"}, issue.Labels)
}

func TestGetIssue_ObjectLabels(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/7", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"iid":         7,
			"title":       "Object labels",
			"description": "",
			"web_url":     "https://gitlab.com/myorg/myrepo/-/issues/7",
			"labels": []map[string]any{
				{"title": "alpha"},
				{"title": "beta"},
			},
		})
	})

	issue, err := client.GetIssue(ctx, "myorg", "myrepo", 7)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta"}, issue.Labels)
}

func TestGetIssue_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	})

	_, err := client.GetIssue(ctx, "myorg", "myrepo", 999)
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
}

func TestListOpenIssues(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "opened", r.URL.Query().Get("state"))
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))

		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"iid":         1,
				"title":       "First",
				"description": "First body",
				"web_url":     "https://gitlab.com/myorg/myrepo/-/issues/1",
				"labels":      []string{"bug"},
			},
			{
				"iid":         2,
				"title":       "Second",
				"description": "Second body",
				"web_url":     "https://gitlab.com/myorg/myrepo/-/issues/2",
				"labels":      []string{},
			},
		})
	})

	issues, err := client.ListOpenIssues(ctx, "myorg", "myrepo")
	require.NoError(t, err)
	require.Len(t, issues, 2)
	assert.Equal(t, "First", issues[0].Title)
	assert.Equal(t, "Second", issues[1].Title)
}

func TestListOpenIssues_WithLabelFilter(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "bug,critical", r.URL.Query().Get("labels"))
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"iid":         3,
				"title":       "Critical bug",
				"description": "",
				"web_url":     "https://gitlab.com/myorg/myrepo/-/issues/3",
				"labels":      []string{"bug", "critical"},
			},
		})
	})

	issues, err := client.ListOpenIssues(ctx, "myorg", "myrepo", "bug", "critical")
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "Critical bug", issues[0].Title)
}

func TestCloseIssue(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/10", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]string
		readJSONBody(t, r, &body)
		assert.Equal(t, "close", body["state_event"])
		writeJSON(t, w, http.StatusOK, map[string]any{"iid": 10, "state": "closed"})
	})

	err := client.CloseIssue(ctx, "myorg", "myrepo", 10)
	require.NoError(t, err)
}

func TestAddIssueLabels(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/5", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]string
		readJSONBody(t, r, &body)
		assert.Equal(t, "new-label", body["add_labels"])
		writeJSON(t, w, http.StatusOK, map[string]any{"iid": 5})
	})

	err := client.AddIssueLabels(ctx, "myorg", "myrepo", 5, "new-label")
	require.NoError(t, err)
}

func TestAddIssueLabels_EmptyNewLabels(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	// Should return immediately without making any API calls
	err := client.AddIssueLabels(ctx, "myorg", "myrepo", 5)
	require.NoError(t, err)
}

func TestAddIssueLabels_MultipleLabels(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/5", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]string
		readJSONBody(t, r, &body)
		assert.Equal(t, "bug,new", body["add_labels"])
		writeJSON(t, w, http.StatusOK, map[string]any{"iid": 5})
	})

	err := client.AddIssueLabels(ctx, "myorg", "myrepo", 5, "bug", "new")
	require.NoError(t, err)
}

func TestListIssueComments(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/3/notes", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "asc", r.URL.Query().Get("sort"))

		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"id":         100,
				"body":       "First comment",
				"created_at": "2024-01-01T00:00:00Z",
				"author":     map[string]string{"username": "alice"},
			},
			{
				"id":         101,
				"body":       "Second comment",
				"created_at": "2024-01-02T00:00:00Z",
				"author":     map[string]string{"username": "bob"},
			},
		})
	})

	comments, err := client.ListIssueComments(ctx, "myorg", "myrepo", 3)
	require.NoError(t, err)
	require.Len(t, comments, 2)

	assert.Equal(t, 100, comments[0].ID)
	assert.Equal(t, "First comment", comments[0].Body)
	assert.Equal(t, "alice", comments[0].Author)
	assert.Equal(t, "2024-01-01T00:00:00Z", comments[0].CreatedAt)
	// Verify HTMLURL construction
	assert.Contains(t, comments[0].HTMLURL, "/-/issues/3#note_100")

	assert.Equal(t, 101, comments[1].ID)
	assert.Equal(t, "bob", comments[1].Author)
}

func TestCreateIssueComment(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/3/notes", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]string
		readJSONBody(t, r, &body)
		assert.Equal(t, "Hello world", body["body"])

		writeJSON(t, w, http.StatusCreated, map[string]any{
			"id":         200,
			"body":       "Hello world",
			"created_at": "2024-03-01T12:00:00Z",
			"author":     map[string]string{"username": "botuser"},
		})
	})

	comment, err := client.CreateIssueComment(ctx, "myorg", "myrepo", 3, "Hello world")
	require.NoError(t, err)
	assert.Equal(t, 200, comment.ID)
	assert.Equal(t, "Hello world", comment.Body)
	assert.Equal(t, "botuser", comment.Author)
	assert.Contains(t, comment.HTMLURL, "/-/issues/3#note_200")
}

func TestUpdateIssueComment(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	// Register handler for listing open issues (the note-scan approach)
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"iid": 1},
			{"iid": 2},
		})
	})

	// Note 500 is on issue 2 -- issue 1 returns 404, issue 2 returns 200
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/1/notes/500", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	})

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/2/notes/500", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]string
		readJSONBody(t, r, &body)
		assert.Equal(t, "Updated body", body["body"])
		writeJSON(t, w, http.StatusOK, map[string]any{"id": 500, "body": "Updated body"})
	})

	err := client.UpdateIssueComment(ctx, "myorg", "myrepo", 500, "Updated body")
	require.NoError(t, err)
}

func TestDeleteIssueComment(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	// Listing open issues returns one issue
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"iid": 10},
		})
	})

	// The note exists on issue 10
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/10/notes/600", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeleteIssueComment(ctx, "myorg", "myrepo", 600)
	require.NoError(t, err)
}

func TestMinimizeComment(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	err := client.MinimizeComment(ctx, "myorg", "myrepo")
	require.ErrorIs(t, err, forge.ErrNotSupported)
}

// ---------------------------------------------------------------------------
// mr.go tests
// ---------------------------------------------------------------------------

func TestCreateChangeProposal(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]string
		readJSONBody(t, r, &body)
		assert.Equal(t, "feature-branch", body["source_branch"])
		assert.Equal(t, "main", body["target_branch"])
		assert.Equal(t, "New feature", body["title"])
		assert.Equal(t, "Feature description", body["description"])

		writeJSON(t, w, http.StatusCreated, map[string]any{
			"iid":           99,
			"title":         "New feature",
			"web_url":       "https://gitlab.com/myorg/myrepo/-/merge_requests/99",
			"source_branch": "feature-branch",
			"target_branch": "main",
		})
	})

	cp, err := client.CreateChangeProposal(ctx, "myorg", "myrepo", "New feature", "Feature description", "feature-branch", "main")
	require.NoError(t, err)
	assert.Equal(t, 99, cp.Number)
	assert.Equal(t, "New feature", cp.Title)
	assert.Equal(t, "https://gitlab.com/myorg/myrepo/-/merge_requests/99", cp.URL)
	assert.Equal(t, "feature-branch", cp.Head)
	assert.Equal(t, "main", cp.Base)
}

func TestListRepoPullRequests(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "opened", r.URL.Query().Get("state"))
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))

		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"iid":           1,
				"title":         "MR One",
				"web_url":       "https://gitlab.com/myorg/myrepo/-/merge_requests/1",
				"source_branch": "branch-1",
				"target_branch": "main",
			},
			{
				"iid":           2,
				"title":         "MR Two",
				"web_url":       "https://gitlab.com/myorg/myrepo/-/merge_requests/2",
				"source_branch": "branch-2",
				"target_branch": "main",
			},
		})
	})

	mrs, err := client.ListRepoPullRequests(ctx, "myorg", "myrepo")
	require.NoError(t, err)
	require.Len(t, mrs, 2)
	assert.Equal(t, "MR One", mrs[0].Title)
	assert.Equal(t, "branch-1", mrs[0].Head)
	assert.Equal(t, "MR Two", mrs[1].Title)
}

func TestGetPullRequestHeadSHA(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]any{
			"sha": "abc123def456",
		})
	})

	sha, err := client.GetPullRequestHeadSHA(ctx, "myorg", "myrepo", 10)
	require.NoError(t, err)
	assert.Equal(t, "abc123def456", sha)
}

func TestListPullRequestFiles(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/5/diffs", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"old_path": "old.go", "new_path": "new.go"},
			{"old_path": "same.go", "new_path": "same.go"},
		})
	})

	files, err := client.ListPullRequestFiles(ctx, "myorg", "myrepo", 5)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "new.go", files[0])
	assert.Equal(t, "same.go", files[1])
}

func TestListPullRequestFileDiffs(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/5/diffs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"new_path": "file.go",
				"diff":     "@@ -1,3 +1,4 @@\n+new line\n",
			},
			{
				"new_path": "other.go",
				"diff":     "@@ -10,2 +10,3 @@\n+another\n",
			},
		})
	})

	diffs, err := client.ListPullRequestFileDiffs(ctx, "myorg", "myrepo", 5)
	require.NoError(t, err)
	require.Len(t, diffs, 2)
	assert.Equal(t, "file.go", diffs[0].Path)
	assert.Contains(t, diffs[0].Patch, "+new line")
	assert.Equal(t, "other.go", diffs[1].Path)
}

func TestMergeChangeProposal(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/15/merge", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]any{"iid": 15, "state": "merged"})
	})

	err := client.MergeChangeProposal(ctx, "myorg", "myrepo", 15)
	require.NoError(t, err)
}

func TestUpdatePullRequestBranch(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/20/rebase", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		writeJSON(t, w, http.StatusAccepted, map[string]any{"rebase_in_progress": true})
	})

	err := client.UpdatePullRequestBranch(ctx, "myorg", "myrepo", 20)
	require.NoError(t, err)
}

func TestCreatePullRequestReview_Approve(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	approved := false
	var approveBody map[string]string
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/30/approve", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		readJSONBody(t, r, &approveBody)
		approved = true
		writeJSON(t, w, http.StatusOK, map[string]any{"iid": 30})
	})

	err := client.CreatePullRequestReview(ctx, "myorg", "myrepo", 30, "APPROVE", "", "sha123", nil)
	require.NoError(t, err)
	assert.True(t, approved, "approve endpoint should have been called")
	assert.Equal(t, "sha123", approveBody["sha"], "commitSHA should be passed as sha parameter")
}

func TestCreatePullRequestReview_ApproveWithBody(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	var approvedCalled, noteCalled bool
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/30/approve", func(w http.ResponseWriter, r *http.Request) {
		approvedCalled = true
		writeJSON(t, w, http.StatusOK, map[string]any{"iid": 30})
	})
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/30/notes", func(w http.ResponseWriter, r *http.Request) {
		noteCalled = true
		var body map[string]string
		readJSONBody(t, r, &body)
		assert.Equal(t, "LGTM!", body["body"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": 1, "body": "LGTM!"})
	})

	err := client.CreatePullRequestReview(ctx, "myorg", "myrepo", 30, "APPROVE", "LGTM!", "sha123", nil)
	require.NoError(t, err)
	assert.True(t, approvedCalled)
	assert.True(t, noteCalled)
}

func TestCreatePullRequestReview_Comment(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	var notes []string
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/30/notes", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]string
		readJSONBody(t, r, &body)
		notes = append(notes, body["body"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": len(notes), "body": body["body"]})
	})

	err := client.CreatePullRequestReview(ctx, "myorg", "myrepo", 30, "COMMENT", "Review body", "sha123", []forge.ReviewComment{
		{Path: "main.go", Line: 10, Body: "Fix this"},
	})
	require.NoError(t, err)
	require.Len(t, notes, 2)
	assert.Equal(t, "Review body", notes[0])
	assert.Contains(t, notes[1], "`main.go:10`")
	assert.Contains(t, notes[1], "Fix this")
}

func TestCreatePullRequestReview_CommentWithFileLevel(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	var notes []string
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/30/notes", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		readJSONBody(t, r, &body)
		notes = append(notes, body["body"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": len(notes), "body": body["body"]})
	})

	err := client.CreatePullRequestReview(ctx, "myorg", "myrepo", 30, "COMMENT", "", "sha123", []forge.ReviewComment{
		{Path: "readme.md", Line: 0, Body: "File-level comment"},
	})
	require.NoError(t, err)
	// No main body, so only the inline comment
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], "`readme.md`")
	assert.Contains(t, notes[0], "File-level comment")
	// Should NOT contain a line number
	assert.NotContains(t, notes[0], "readme.md:0")
}

func TestCreatePullRequestReview_InvalidEvent(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	err := client.CreatePullRequestReview(ctx, "myorg", "myrepo", 30, "INVALID", "", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid event")
}

func TestListPullRequestReviews(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"approved_by": []map[string]any{
				{"user": map[string]any{"id": 42, "username": "approver1"}},
				{"user": map[string]any{"id": 99, "username": "approver2"}},
			},
		})
	})

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/notes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"id":         300,
				"body":       "Looks good",
				"system":     false,
				"created_at": "2024-02-01T00:00:00Z",
				"author":     map[string]any{"id": 50, "username": "commenter"},
			},
			{
				"id":         301,
				"body":       "assigned to @someone",
				"system":     true,
				"created_at": "2024-02-02T00:00:00Z",
				"author":     map[string]any{"id": 0, "username": "system"},
			},
		})
	})

	reviews, err := client.ListPullRequestReviews(ctx, "myorg", "myrepo", 10)
	require.NoError(t, err)

	// 2 approvers + 1 non-system note = 3 reviews
	require.Len(t, reviews, 3)

	// Approvals come first (IDs are negated user IDs to avoid collision with note IDs)
	assert.Equal(t, "APPROVED", reviews[0].State)
	assert.Equal(t, "approver1", reviews[0].User)
	assert.Equal(t, -42, reviews[0].ID)
	assert.Equal(t, "APPROVED", reviews[1].State)
	assert.Equal(t, "approver2", reviews[1].User)
	assert.Equal(t, -99, reviews[1].ID)

	// Then comments (system notes are skipped)
	assert.Equal(t, "COMMENTED", reviews[2].State)
	assert.Equal(t, "commenter", reviews[2].User)
	assert.Equal(t, "Looks good", reviews[2].Body)
}

func TestListPullRequestReviews_ChangesRequested(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"approved_by": []map[string]any{}})
	})

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/notes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"id":         400,
				"body":       requestChangesMarker + "\n\nPlease fix the error handling",
				"system":     false,
				"created_at": "2024-03-01T00:00:00Z",
				"author":     map[string]any{"id": 50, "username": "reviewer"},
			},
			{
				"id":         401,
				"body":       "Regular comment",
				"system":     false,
				"created_at": "2024-03-02T00:00:00Z",
				"author":     map[string]any{"id": 51, "username": "commenter"},
			},
		})
	})

	reviews, err := client.ListPullRequestReviews(ctx, "myorg", "myrepo", 10)
	require.NoError(t, err)
	require.Len(t, reviews, 2)
	assert.Equal(t, "CHANGES_REQUESTED", reviews[0].State)
	assert.Equal(t, "COMMENTED", reviews[1].State)
}

func TestDismissPullRequestReview(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"approved_by": []map[string]any{
				{"user": map[string]any{"id": 42, "username": "botuser"}},
			},
		})
	})

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"username": "botuser"})
	})

	unapproved := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/unapprove", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		unapproved = true
		writeJSON(t, w, http.StatusCreated, map[string]any{})
	})

	notePosted := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/notes", func(w http.ResponseWriter, r *http.Request) {
		notePosted = true
		var body map[string]string
		readJSONBody(t, r, &body)
		assert.Contains(t, body["body"], "Review dismissed:")
		assert.Contains(t, body["body"], "outdated")
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": 1})
	})

	err := client.DismissPullRequestReview(ctx, "myorg", "myrepo", 10, -42, "outdated")
	require.NoError(t, err)
	assert.True(t, unapproved)
	assert.True(t, notePosted)
}

func TestDismissPullRequestReview_NonApproval_NoMarker(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"approved_by": []map[string]any{
				{"user": map[string]any{"id": 99, "username": "someone"}},
			},
		})
	})

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/notes/42", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, map[string]string{"body": "just a comment"})
			return
		}
		t.Fatal("should not update a note without the marker")
	})

	err := client.DismissPullRequestReview(ctx, "myorg", "myrepo", 10, 42, "")
	require.NoError(t, err)
}

func TestDismissPullRequestReview_RequestChangesNote(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"approved_by": []map[string]any{}})
	})

	var updatedBody string
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/notes/42", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(t, w, http.StatusOK, map[string]string{
				"body": requestChangesMarker + "\n\nPlease fix this",
			})
			return
		}
		var body map[string]string
		readJSONBody(t, r, &body)
		updatedBody = body["body"]
		writeJSON(t, w, http.StatusOK, map[string]any{"id": 42})
	})

	err := client.DismissPullRequestReview(ctx, "myorg", "myrepo", 10, 42, "superseded")
	require.NoError(t, err)
	assert.Contains(t, updatedBody, dismissedMarker)
	assert.NotContains(t, updatedBody, requestChangesMarker)
	assert.Contains(t, updatedBody, "superseded")
}

// ---------------------------------------------------------------------------
// ci.go tests
// ---------------------------------------------------------------------------

func TestGetAuthenticatedUser(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "test-token", r.Header.Get("PRIVATE-TOKEN"))
		writeJSON(t, w, http.StatusOK, map[string]string{"username": "testbot"})
	})

	username, err := client.GetAuthenticatedUser(ctx)
	require.NoError(t, err)
	assert.Equal(t, "testbot", username)
}

func TestGetAuthenticatedUserIdentity(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]string{
			"name":  "Test Bot",
			"email": "bot@example.com",
		})
	})

	identity, err := client.GetAuthenticatedUserIdentity(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Test Bot", identity.Name)
	assert.Equal(t, "bot@example.com", identity.Email)
}

func TestGetTokenScopes(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	scopes, err := client.GetTokenScopes(ctx)
	require.NoError(t, err)
	assert.Nil(t, scopes)
}

func TestIsInstallationToken(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	isInstall, err := client.IsInstallationToken(ctx)
	require.NoError(t, err)
	assert.False(t, isInstall)
}

func TestCreateRepoSecret(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "MY_SECRET", body["key"])
		assert.Equal(t, "s3cr3t", body["value"])
		assert.Equal(t, true, body["protected"])
		assert.Equal(t, true, body["masked"])
		assert.Equal(t, "env_var", body["variable_type"])

		writeJSON(t, w, http.StatusCreated, map[string]any{"key": "MY_SECRET"})
	})

	err := client.CreateRepoSecret(ctx, "myorg", "myrepo", "MY_SECRET", "s3cr3t")
	require.NoError(t, err)
}

func TestRepoSecretExists_True(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/MY_SECRET", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]string{"key": "MY_SECRET"})
	})

	exists, err := client.RepoSecretExists(ctx, "myorg", "myrepo", "MY_SECRET")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestRepoSecretExists_False(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/MISSING", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	exists, err := client.RepoSecretExists(ctx, "myorg", "myrepo", "MISSING")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestRepoSecretExists_UnexpectedStatus(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/BAD", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := client.RepoSecretExists(ctx, "myorg", "myrepo", "BAD")
	require.Error(t, err)
}

func TestDeleteRepoSecret(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/MY_SECRET", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeleteRepoSecret(ctx, "myorg", "myrepo", "MY_SECRET")
	require.NoError(t, err)
}

func TestDeleteRepoSecret_AlreadyGone(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/GONE", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	// Should be idempotent: 404 is not an error
	err := client.DeleteRepoSecret(ctx, "myorg", "myrepo", "GONE")
	require.NoError(t, err)
}

func TestCreateOrUpdateRepoVariable_Create(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "MY_VAR", body["key"])
		assert.Equal(t, "my-value", body["value"])
		assert.Equal(t, "env_var", body["variable_type"])

		writeJSON(t, w, http.StatusCreated, map[string]any{"key": "MY_VAR"})
	})

	err := client.CreateOrUpdateRepoVariable(ctx, "myorg", "myrepo", "MY_VAR", "my-value")
	require.NoError(t, err)
}

func TestCreateOrUpdateRepoVariable_UpdateOnConflict(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	// POST returns 409 Conflict (variable already exists)
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeJSON(t, w, http.StatusConflict, map[string]string{"message": "MY_VAR has already been taken"})
			return
		}
	})

	// Then it falls back to PUT
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/MY_VAR", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "new-value", body["value"])
		writeJSON(t, w, http.StatusOK, map[string]any{"key": "MY_VAR", "value": "new-value"})
	})

	err := client.CreateOrUpdateRepoVariable(ctx, "myorg", "myrepo", "MY_VAR", "new-value")
	require.NoError(t, err)
}

func TestGetRepoVariable_Found(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/MY_VAR", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]string{"key": "MY_VAR", "value": "hello"})
	})

	value, found, err := client.GetRepoVariable(ctx, "myorg", "myrepo", "MY_VAR")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "hello", value)
}

func TestGetRepoVariable_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/MISSING", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	value, found, err := client.GetRepoVariable(ctx, "myorg", "myrepo", "MISSING")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, "", value)
}

func TestListRepoVariables(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, []map[string]string{
			{"key": "VAR1", "value": "val1"},
			{"key": "VAR2", "value": "val2"},
		})
	})

	vars, err := client.ListRepoVariables(ctx, "myorg", "myrepo")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"VAR1": "val1", "VAR2": "val2"}, vars)
}

func TestListRepoVariables_Pagination(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	pageCount := 0
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables", func(w http.ResponseWriter, r *http.Request) {
		pageCount++
		page := r.URL.Query().Get("page")

		if page == "1" {
			// Return exactly 100 items to trigger next page
			vars := make([]map[string]string, 100)
			for i := range vars {
				vars[i] = map[string]string{
					"key":   "VAR_" + page + "_" + string(rune('A'+i%26)),
					"value": "val",
				}
			}
			writeJSON(t, w, http.StatusOK, vars)
			return
		}
		// Page 2 returns fewer than 100, ending pagination
		writeJSON(t, w, http.StatusOK, []map[string]string{
			{"key": "LAST_VAR", "value": "last"},
		})
	})

	vars, err := client.ListRepoVariables(ctx, "myorg", "myrepo")
	require.NoError(t, err)
	assert.Equal(t, 2, pageCount)
	assert.Contains(t, vars, "LAST_VAR")
}

func TestCreatePipelineSchedule(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]string
			readJSONBody(t, r, &body)
			assert.Equal(t, "main", body["ref"])
			assert.Equal(t, "Nightly build", body["description"])
			assert.Equal(t, "0 2 * * *", body["cron"])
			assert.Equal(t, "UTC", body["cron_timezone"])

			writeJSON(t, w, http.StatusCreated, map[string]any{"id": 123})
			return
		}
	})

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules/123/variables", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]string
		readJSONBody(t, r, &body)
		assert.NotEmpty(t, body["key"])
		assert.NotEmpty(t, body["value"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"key": body["key"]})
	})

	id, err := client.CreatePipelineSchedule(ctx, "myorg", "myrepo", "main", "Nightly build", "0 2 * * *", map[string]string{
		"ENV": "production",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(123), id)
}

func TestCreatePipelineSchedule_NoVariables(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": 456})
	})

	id, err := client.CreatePipelineSchedule(ctx, "myorg", "myrepo", "main", "Weekly", "0 0 * * 0", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(456), id)
}

func TestDeletePipelineSchedule(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules/123", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeletePipelineSchedule(ctx, "myorg", "myrepo", 123)
	require.NoError(t, err)
}

func TestListPipelineSchedules(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"id":            int64(1),
				"description":   "Nightly",
				"ref":           "main",
				"cron":          "0 0 * * *",
				"cron_timezone": "UTC",
				"active":        true,
			},
			{
				"id":            int64(2),
				"description":   "Weekly",
				"ref":           "develop",
				"cron":          "0 0 * * 0",
				"cron_timezone": "US/Pacific",
				"active":        false,
			},
		})
	})

	schedules, err := client.ListPipelineSchedules(ctx, "myorg", "myrepo")
	require.NoError(t, err)
	require.Len(t, schedules, 2)

	assert.Equal(t, int64(1), schedules[0].ID)
	assert.Equal(t, "Nightly", schedules[0].Description)
	assert.Equal(t, "main", schedules[0].Ref)
	assert.Equal(t, "0 0 * * *", schedules[0].Cron)
	assert.Equal(t, "UTC", schedules[0].CronTimezone)
	assert.True(t, schedules[0].Active)

	assert.Equal(t, int64(2), schedules[1].ID)
	assert.False(t, schedules[1].Active)
}

func TestIsProtectedBranch_True(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]string{"name": "main"})
	})

	protected, err := client.IsProtectedBranch(ctx, "myorg", "myrepo", "main")
	require.NoError(t, err)
	assert.True(t, protected)
}

func TestIsProtectedBranch_False(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/protected_branches/feature", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	protected, err := client.IsProtectedBranch(ctx, "myorg", "myrepo", "feature")
	require.NoError(t, err)
	assert.False(t, protected)
}

func TestIsProtectedBranch_UnexpectedStatus(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := client.IsProtectedBranch(ctx, "myorg", "myrepo", "main")
	require.Error(t, err)
}

func TestGetOrgPlan_WithPlan(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/namespaces/myorg", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]string{"plan": "premium"})
	})

	plan, err := client.GetOrgPlan(ctx, "myorg")
	require.NoError(t, err)
	assert.Equal(t, "premium", plan)
}

func TestGetOrgPlan_NoPlanField(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/namespaces/myorg", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]string{"plan": ""})
	})

	plan, err := client.GetOrgPlan(ctx, "myorg")
	require.NoError(t, err)
	assert.Equal(t, "free", plan)
}

func TestUpdateCIVariable(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/CI_VAR", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "new-val", body["value"])
		assert.Equal(t, true, body["protected"])
		writeJSON(t, w, http.StatusOK, map[string]any{"key": "CI_VAR", "value": "new-val"})
	})

	err := client.UpdateCIVariable(ctx, "myorg", "myrepo", "CI_VAR", "new-val", true)
	require.NoError(t, err)
}

func TestCreateProtectedCIVariable(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "MY_CI_VAR", body["key"])
		assert.Equal(t, "ci-value", body["value"])
		assert.Equal(t, true, body["protected"])
		assert.Equal(t, false, body["masked"])
		assert.Equal(t, "env_var", body["variable_type"])

		writeJSON(t, w, http.StatusCreated, map[string]any{"key": "MY_CI_VAR"})
	})

	err := client.CreateProtectedCIVariable(ctx, "myorg", "myrepo", "MY_CI_VAR", "ci-value")
	require.NoError(t, err)
}

func TestErrNotSupported_OrgMethods(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	t.Run("CreateOrgSecret", func(t *testing.T) {
		err := client.CreateOrgSecret(ctx, "org", "secret", "val", nil)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("OrgSecretExists", func(t *testing.T) {
		_, err := client.OrgSecretExists(ctx, "org", "secret")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("DeleteOrgSecret", func(t *testing.T) {
		err := client.DeleteOrgSecret(ctx, "org", "secret")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("SetOrgSecretRepos", func(t *testing.T) {
		err := client.SetOrgSecretRepos(ctx, "org", "secret", nil)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("GetOrgSecretRepos", func(t *testing.T) {
		_, err := client.GetOrgSecretRepos(ctx, "org", "secret")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("CreateOrUpdateOrgVariable", func(t *testing.T) {
		err := client.CreateOrUpdateOrgVariable(ctx, "org", "var", "val", nil)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("CreateOrUpdateOrgVariableAll", func(t *testing.T) {
		err := client.CreateOrUpdateOrgVariableAll(ctx, "org", "var", "val")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("OrgVariableExists", func(t *testing.T) {
		_, err := client.OrgVariableExists(ctx, "org", "var")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("GetOrgVariable", func(t *testing.T) {
		_, _, err := client.GetOrgVariable(ctx, "org", "var")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("ListOrgVariables", func(t *testing.T) {
		_, err := client.ListOrgVariables(ctx, "org")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("DeleteOrgVariable", func(t *testing.T) {
		err := client.DeleteOrgVariable(ctx, "org", "var")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("SetOrgVariableRepos", func(t *testing.T) {
		err := client.SetOrgVariableRepos(ctx, "org", "var", nil)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("GetOrgVariableRepos", func(t *testing.T) {
		_, err := client.GetOrgVariableRepos(ctx, "org", "var")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})
}

func TestErrNotSupported_WorkflowMethods(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	t.Run("GetWorkflow", func(t *testing.T) {
		_, err := client.GetWorkflow(ctx, "o", "r", "w")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("GetLatestWorkflowRun", func(t *testing.T) {
		_, err := client.GetLatestWorkflowRun(ctx, "o", "r", "w")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("GetWorkflowRun", func(t *testing.T) {
		_, err := client.GetWorkflowRun(ctx, "o", "r", 1)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("DispatchWorkflow", func(t *testing.T) {
		err := client.DispatchWorkflow(ctx, "o", "r", "w", "main", nil)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("ListWorkflowRuns", func(t *testing.T) {
		_, err := client.ListWorkflowRuns(ctx, "o", "r", "w")
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("ListRecentWorkflowRuns", func(t *testing.T) {
		_, err := client.ListRecentWorkflowRuns(ctx, "o", "r", 10)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("ListWorkflowRunArtifacts", func(t *testing.T) {
		_, err := client.ListWorkflowRunArtifacts(ctx, "o", "r", 1)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("DownloadWorkflowRunArtifact", func(t *testing.T) {
		_, err := client.DownloadWorkflowRunArtifact(ctx, "o", "r", 1)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("ListRepositoryArtifacts", func(t *testing.T) {
		_, err := client.ListRepositoryArtifacts(ctx, "o", "r", 1)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("GetWorkflowRunLogs", func(t *testing.T) {
		_, err := client.GetWorkflowRunLogs(ctx, "o", "r", 1)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})

	t.Run("GetWorkflowRunAnnotations", func(t *testing.T) {
		_, err := client.GetWorkflowRunAnnotations(ctx, "o", "r", 1)
		require.ErrorIs(t, err, forge.ErrNotSupported)
	})
}

// ---------------------------------------------------------------------------
// Additional edge-case and error-path tests
// ---------------------------------------------------------------------------

func TestGitlabLabels_UnmarshalJSON(t *testing.T) {
	t.Run("string array", func(t *testing.T) {
		var l gitlabLabels
		err := json.Unmarshal([]byte(`["bug","feature"]`), &l)
		require.NoError(t, err)
		assert.Equal(t, gitlabLabels{"bug", "feature"}, l)
	})

	t.Run("object array", func(t *testing.T) {
		var l gitlabLabels
		err := json.Unmarshal([]byte(`[{"title":"alpha"},{"title":"beta"}]`), &l)
		require.NoError(t, err)
		assert.Equal(t, gitlabLabels{"alpha", "beta"}, l)
	})

	t.Run("invalid format", func(t *testing.T) {
		var l gitlabLabels
		err := json.Unmarshal([]byte(`"not-an-array"`), &l)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected JSON format")
	})

	t.Run("empty array", func(t *testing.T) {
		var l gitlabLabels
		err := json.Unmarshal([]byte(`[]`), &l)
		require.NoError(t, err)
		assert.Equal(t, gitlabLabels{}, l)
	})
}

func TestGitlabLabels_Strings(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		var l gitlabLabels
		assert.Nil(t, l.strings())
	})

	t.Run("non-nil", func(t *testing.T) {
		l := gitlabLabels{"a", "b"}
		assert.Equal(t, []string{"a", "b"}, l.strings())
	})
}

func TestDeleteRepoVariable(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/MY_VAR", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeleteRepoVariable(ctx, "myorg", "myrepo", "MY_VAR")
	require.NoError(t, err)
}

func TestDeleteRepoVariable_Idempotent(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/GONE", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	err := client.DeleteRepoVariable(ctx, "myorg", "myrepo", "GONE")
	require.NoError(t, err)
}

func TestRepoVariableExists_True(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/EXISTS", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]string{"key": "EXISTS"})
	})

	exists, err := client.RepoVariableExists(ctx, "myorg", "myrepo", "EXISTS")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestRepoVariableExists_False(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/MISSING", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	exists, err := client.RepoVariableExists(ctx, "myorg", "myrepo", "MISSING")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestDismissPullRequestReview_WithoutMessage(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"approved_by": []map[string]any{
				{"user": map[string]any{"id": 42, "username": "botuser"}},
			},
		})
	})

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"username": "botuser"})
	})

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/unapprove", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusCreated, map[string]any{})
	})

	// No notes endpoint registered -- if message is empty, no note should be posted
	err := client.DismissPullRequestReview(ctx, "myorg", "myrepo", 10, -42, "")
	require.NoError(t, err)
}

func TestCreatePullRequestReview_RequestChanges(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	var notes []string
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/30/notes", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		readJSONBody(t, r, &body)
		notes = append(notes, body["body"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": len(notes)})
	})

	err := client.CreatePullRequestReview(ctx, "myorg", "myrepo", 30, "REQUEST_CHANGES", "Please fix", "sha", nil)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], "Please fix")
	assert.Contains(t, notes[0], requestChangesMarker)
}

func TestCreatePullRequestReview_RequestChangesEmptyBody(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	var notes []string
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/31/notes", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		readJSONBody(t, r, &body)
		notes = append(notes, body["body"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": len(notes)})
	})

	err := client.CreatePullRequestReview(ctx, "myorg", "myrepo", 31, "REQUEST_CHANGES", "", "sha", nil)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Equal(t, requestChangesMarker, notes[0])
}

func TestGetPullRequestInfo(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"iid":               5,
			"web_url":           "https://gitlab.com/myorg/myrepo/-/merge_requests/5",
			"sha":               "abc123",
			"source_branch":     "feature",
			"target_branch":     "main",
			"author":            map[string]any{"id": 42, "username": "contributor"},
			"source_project_id": 100,
			"target_project_id": 100,
		})
	})

	info, err := client.GetPullRequestInfo(ctx, "myorg", "myrepo", 5)
	require.NoError(t, err)
	assert.Equal(t, "myorg/myrepo", info.HeadRepo)
	assert.Equal(t, "myorg/myrepo", info.BaseRepo)
	assert.Equal(t, "contributor", info.AuthorID)
	assert.False(t, info.IsFork)
}

func TestGetPullRequestInfo_Fork(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"iid":               5,
			"web_url":           "https://gitlab.com/myorg/myrepo/-/merge_requests/5",
			"sha":               "abc123",
			"source_branch":     "feature",
			"target_branch":     "main",
			"author":            map[string]any{"id": 42, "username": "contributor"},
			"source_project_id": 200,
			"target_project_id": 100,
		})
	})

	mux.HandleFunc("/api/v4/projects/200", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"path_with_namespace": "contributor/myrepo",
		})
	})

	info, err := client.GetPullRequestInfo(ctx, "myorg", "myrepo", 5)
	require.NoError(t, err)
	assert.Equal(t, "contributor/myrepo", info.HeadRepo)
	assert.Equal(t, "myorg/myrepo", info.BaseRepo)
	assert.True(t, info.IsFork)
}

func TestDismissPullRequestReview_OtherApprover(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/approvals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"approved_by": []map[string]any{
				{"user": map[string]any{"id": 42, "username": "otherapprover"}},
			},
		})
	})

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"username": "botuser"})
	})

	err := client.DismissPullRequestReview(ctx, "myorg", "myrepo", 10, -42, "outdated")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotSupported)
}

func TestCreatePullRequestReview_Approve_409SHAMismatch(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/30/approve", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"message":"The SHA does not match"}`)
	})

	err := client.CreatePullRequestReview(ctx, "myorg", "myrepo", 30, "APPROVE", "", "stale-sha", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409 Conflict")
}

func TestCreatePullRequestReview_Approve_409AlreadyApproved(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/30/approve", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"message":"You have already approved this merge request"}`)
	})

	err := client.CreatePullRequestReview(ctx, "myorg", "myrepo", 30, "APPROVE", "", "sha123", nil)
	require.NoError(t, err)
}

func TestCreatePullRequestReview_Approve_409AlreadyMerged(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/30/approve", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"message":"MR has already been merged"}`)
	})

	err := client.CreatePullRequestReview(ctx, "myorg", "myrepo", 30, "APPROVE", "", "sha123", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409 Conflict")
}

func TestCreateRepoSecret_MaskedFallback(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	callCount := 0
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var body map[string]any
		readJSONBody(t, r, &body)
		if body["masked"] == true {
			writeJSON(t, w, http.StatusBadRequest, map[string]string{
				"message": "This variable can not be masked",
			})
			return
		}
		assert.Equal(t, false, body["masked"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"key": "SHORT"})
	})

	err := client.CreateRepoSecret(ctx, "myorg", "myrepo", "SHORT", "ab")
	require.NoError(t, err)
	assert.Equal(t, 2, callCount, "should retry with masked:false after 400")
}

func TestCreateRepoSecret_Upsert(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	callCount := 0
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		writeJSON(t, w, http.StatusConflict, map[string]string{
			"message": "MY_SECRET has already been taken",
		})
	})

	updated := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/MY_SECRET", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "newvalue", body["value"])
		assert.Equal(t, true, body["protected"])
		updated = true
		writeJSON(t, w, http.StatusOK, map[string]any{"key": "MY_SECRET"})
	})

	err := client.CreateRepoSecret(ctx, "myorg", "myrepo", "MY_SECRET", "newvalue")
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)
	assert.True(t, updated)
}

func TestCreateRepoSecret_MaskedFallbackNotOnNonMaskError(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]string{
			"message": "key is invalid",
		})
	})

	err := client.CreateRepoSecret(ctx, "myorg", "myrepo", "BAD_KEY!", "value")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key is invalid")
}

func TestCreatePipelineSchedule_CleansUpOnVariableFailure(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	scheduleDeleted := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeJSON(t, w, http.StatusCreated, map[string]any{"id": 77})
			return
		}
	})
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules/77/variables", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]string{"message": "invalid"})
	})
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules/77", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			scheduleDeleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	})

	id, err := client.CreatePipelineSchedule(ctx, "myorg", "myrepo", "main", "test", "0 * * * *", map[string]string{"VAR": "val"})
	require.Error(t, err)
	assert.Equal(t, int64(0), id)
	assert.True(t, scheduleDeleted, "orphaned schedule should be cleaned up")
}

func TestListOrgRepos_ExcludesInternal(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/groups/myorg/projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 1, "name": "public-proj", "path_with_namespace": "myorg/public-proj",
				"default_branch": "main", "visibility": "public", "archived": false},
			{"id": 2, "name": "internal-proj", "path_with_namespace": "myorg/internal-proj",
				"default_branch": "main", "visibility": "internal", "archived": false},
			{"id": 3, "name": "private-proj", "path_with_namespace": "myorg/private-proj",
				"default_branch": "main", "visibility": "private", "archived": false},
		})
	})

	repos, err := client.ListOrgRepos(ctx, "myorg", false)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "myorg/public-proj", repos[0].FullName)
}

func TestListOrgRepos_IncludesSubgroups(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/groups/myorg/projects", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "true", r.URL.Query().Get("include_subgroups"))
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"id": 1, "name": "sub-project", "path_with_namespace": "myorg/sub/sub-project",
				"default_branch": "main", "visibility": "public", "archived": false,
			},
		})
	})

	repos, err := client.ListOrgRepos(ctx, "myorg", false)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "myorg/sub/sub-project", repos[0].FullName)
}

func TestCreateChangeProposal_Error(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusUnprocessableEntity, map[string]string{
			"message": "Validation failed",
		})
	})

	_, err := client.CreateChangeProposal(ctx, "myorg", "myrepo", "Title", "Body", "head", "base")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Validation failed")
}

func TestGetAuthenticatedUser_Error(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusUnauthorized, map[string]string{
			"message": "401 Unauthorized",
		})
	})

	_, err := client.GetAuthenticatedUser(ctx)
	require.Error(t, err)
}

func TestGetAuthenticatedUserIdentity_Fallbacks(t *testing.T) {
	t.Run("name and email present", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{
				"id": 42, "username": "jdoe", "name": "Jane Doe", "email": "jane@example.com",
			})
		})
		id, err := client.GetAuthenticatedUserIdentity(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "Jane Doe", id.Name)
		assert.Equal(t, "jane@example.com", id.Email)
	})

	t.Run("empty name falls back to username", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{
				"id": 42, "username": "jdoe", "name": "", "email": "jane@example.com",
			})
		})
		id, err := client.GetAuthenticatedUserIdentity(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "jdoe", id.Name)
	})

	t.Run("empty email falls back to noreply using baseURL host", func(t *testing.T) {
		client, mux := setupTest(t)
		mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{
				"id": 42, "username": "jdoe", "name": "Jane Doe", "email": "",
			})
		})
		id, err := client.GetAuthenticatedUserIdentity(context.Background())
		require.NoError(t, err)
		assert.Contains(t, id.Email, "42+jdoe@users.noreply.")
	})

	t.Run("empty email uses gitlab.com domain for default baseURL", func(t *testing.T) {
		client, err := New("test-token")
		require.NoError(t, err)
		// Can't call the API on real gitlab.com, but verify the baseURL hostname logic
		u, _ := url.Parse(client.baseURL)
		assert.Equal(t, "gitlab.com", u.Hostname())
	})
}

func TestCreateChangeProposal_NoChanges(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]string{
			"message": "No commits between base and head",
		})
	})

	_, err := client.CreateChangeProposal(ctx, "owner", "repo", "Title", "Body", "head", "base")
	require.Error(t, err)
	assert.True(t, forge.IsNoChanges(err), "expected ErrNoChanges, got: %v", err)
}

func TestCreateBranch_AlreadyExists400(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/owner%2Frepo", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id": 1, "name": "repo", "path_with_namespace": "owner/repo",
			"default_branch": "main", "visibility": "public",
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/branches", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]string{
			"message": "Branch already exists",
		})
	})

	err := client.CreateBranch(ctx, "owner", "repo", "feature")
	require.Error(t, err)
	assert.True(t, forge.IsAlreadyExists(err), "expected ErrAlreadyExists, got: %v", err)
}

func TestCommitFilesImpl_BranchProtected(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]string{
			"message": "You are not allowed to push into this branch. Branch is protected.",
		})
	})

	_, err := client.CommitFilesToBranch(ctx, "owner", "repo", "main", "test", []forge.TreeFile{
		{Path: "new.txt", Content: []byte("hello"), Mode: "100644"},
	})
	require.Error(t, err)
	assert.True(t, forge.IsBranchProtected(err), "expected ErrBranchProtected, got: %v", err)
}

func TestCommitFilesImpl_NonFastForward(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]string{
			"message": "Could not update refs/heads/main. Please refresh and try again.",
		})
	})

	_, err := client.CommitFilesToBranch(ctx, "owner", "repo", "main", "test", []forge.TreeFile{
		{Path: "new.txt", Content: []byte("hello"), Mode: "100644"},
	})
	require.Error(t, err)
	assert.True(t, forge.IsNonFastForward(err), "expected ErrNonFastForward, got: %v", err)
}

func TestListOpenIssues_LabelEncoding(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues", func(w http.ResponseWriter, r *http.Request) {
		labels := r.URL.Query().Get("labels")
		assert.Equal(t, "bug fix,feature&request", labels)
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	})

	_, err := client.ListOpenIssues(ctx, "owner", "repo", "bug fix", "feature&request")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// DeleteRef tests
// ---------------------------------------------------------------------------

func TestDeleteRef_Branch(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	called := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/repository/branches/feature-branch", func(w http.ResponseWriter, r *http.Request) {
		called = true
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeleteRef(ctx, "myorg", "myrepo", "heads/feature-branch")
	require.NoError(t, err)
	assert.True(t, called)
}

func TestDeleteRef_Tag(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	called := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/repository/tags/v1.0", func(w http.ResponseWriter, r *http.Request) {
		called = true
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeleteRef(ctx, "myorg", "myrepo", "tags/v1.0")
	require.NoError(t, err)
	assert.True(t, called)
}

func TestDeleteRef_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/repository/branches/gone", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	err := client.DeleteRef(ctx, "myorg", "myrepo", "heads/gone")
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err))
}

func TestDeleteRef_UnsupportedPrefix(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	err := client.DeleteRef(ctx, "myorg", "myrepo", "pull/123/head")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported ref path format")
}

// ---------------------------------------------------------------------------
// CreateCrossRepoChangeProposal tests
// ---------------------------------------------------------------------------

func TestCreateCrossRepoChangeProposal_NotSupported(t *testing.T) {
	client, _ := setupTest(t)
	ctx := context.Background()

	cp, err := client.CreateCrossRepoChangeProposal(ctx, "base-owner", "base-repo", "head-owner", "head-repo", "title", "body", "feature", "main")
	require.Error(t, err)
	assert.Nil(t, cp)
	assert.ErrorIs(t, err, forge.ErrNotSupported)
}
