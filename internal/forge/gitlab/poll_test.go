package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/poll"
)

// Compile-time check that PollClient satisfies poll.GitLabClient.
var _ poll.GitLabClient = (*PollClient)(nil)

func setupPollTest(t *testing.T) (*PollClient, *http.ServeMux) {
	t.Helper()
	client, mux := setupTest(t)
	return NewPollClient(client), mux
}

// ---------------------------------------------------------------------------
// ListIssuesUpdatedSince
// ---------------------------------------------------------------------------

func TestListIssuesUpdatedSince(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		assert.NotEmpty(t, r.URL.Query().Get("updated_after"))

		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"iid":        1,
				"title":      "Bug report",
				"state":      "opened",
				"labels":     []string{"bug"},
				"author":     map[string]any{"id": 10, "username": "alice", "bot": false},
				"updated_at": "2024-06-01T12:00:00Z",
			},
			{
				"iid":        2,
				"title":      "Feature req",
				"state":      "closed",
				"labels":     []string{"feature"},
				"author":     map[string]any{"id": 20, "username": "bob", "bot": false},
				"updated_at": "2024-06-02T12:00:00Z",
			},
		})
	})

	since := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	issues, err := pc.ListIssuesUpdatedSince(ctx, "myorg", "myrepo", since)
	require.NoError(t, err)
	require.Len(t, issues, 2)
	assert.Equal(t, 1, issues[0].IID)
	assert.Equal(t, "Bug report", issues[0].Title)
	assert.Equal(t, "opened", issues[0].State)
	assert.Equal(t, []string{"bug"}, issues[0].Labels)
	assert.Equal(t, "alice", issues[0].Author.Username)
	assert.Equal(t, 2, issues[1].IID)
}

func TestListIssuesUpdatedSince_Pagination(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch page {
		case "1", "":
			items := make([]map[string]any, 100)
			for i := range items {
				items[i] = map[string]any{
					"iid":        i + 1,
					"title":      "Issue",
					"state":      "opened",
					"labels":     []string{},
					"author":     map[string]any{"id": 1, "username": "u"},
					"updated_at": "2024-06-01T12:00:00Z",
				}
			}
			writeJSON(t, w, http.StatusOK, items)
		case "2":
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{
					"iid":        101,
					"title":      "Last",
					"state":      "opened",
					"labels":     []string{},
					"author":     map[string]any{"id": 1, "username": "u"},
					"updated_at": "2024-06-01T12:00:00Z",
				},
			})
		}
	})

	since := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	issues, err := pc.ListIssuesUpdatedSince(ctx, "myorg", "myrepo", since)
	require.NoError(t, err)
	assert.Len(t, issues, 101)
}

// ---------------------------------------------------------------------------
// ListMergeRequestsUpdatedSince
// ---------------------------------------------------------------------------

func TestListMergeRequestsUpdatedSince(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.NotEmpty(t, r.URL.Query().Get("updated_after"))

		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"iid":               10,
				"title":             "Add feature",
				"state":             "merged",
				"labels":            []string{"enhancement"},
				"source_project_id": 100,
				"target_project_id": 100,
				"source_branch":     "feature",
				"target_branch":     "main",
				"author":            map[string]any{"id": 5, "username": "dev"},
				"merge_user":        map[string]any{"id": 6, "username": "merger"},
				"merged_by":         map[string]any{"id": 6, "username": "merger"},
				"merged_at":         "2024-06-01T14:00:00Z",
				"updated_at":        "2024-06-01T14:00:00Z",
			},
		})
	})

	since := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	mrs, err := pc.ListMergeRequestsUpdatedSince(ctx, "myorg", "myrepo", since)
	require.NoError(t, err)
	require.Len(t, mrs, 1)
	assert.Equal(t, 10, mrs[0].IID)
	assert.Equal(t, "Add feature", mrs[0].Title)
	assert.Equal(t, "merged", mrs[0].State)
	assert.Equal(t, "dev", mrs[0].Author.Username)
	assert.Equal(t, "merger", mrs[0].MergeUser.Username)
	assert.Equal(t, "feature", mrs[0].SourceBranch)
	assert.Equal(t, "main", mrs[0].TargetBranch)
}

// ---------------------------------------------------------------------------
// ListProjectEvents
// ---------------------------------------------------------------------------

func TestListProjectEvents(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/events", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "note", r.URL.Query().Get("target_type"))

		// Verify the date parameter was widened by one day:
		// since = 2024-06-15, so after should be 2024-06-14
		assert.Equal(t, "2024-06-14", r.URL.Query().Get("after"))

		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"id":         1,
				"author":     map[string]any{"id": 10, "username": "commenter"},
				"created_at": "2024-06-15T10:00:00Z",
				"note": map[string]any{
					"id":            100,
					"noteable_type": "Issue",
					"noteable_iid":  5,
					"body":          "LGTM",
				},
			},
			{
				"id":         2,
				"author":     map[string]any{"id": 20, "username": "old"},
				"created_at": "2024-06-14T08:00:00Z",
				"note": map[string]any{
					"id":            99,
					"noteable_type": "MergeRequest",
					"noteable_iid":  3,
					"body":          "old comment",
				},
			},
		})
	})

	since := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	events, err := pc.ListProjectEvents(ctx, "myorg", "myrepo", "note", since)
	require.NoError(t, err)

	// Only the event at 2024-06-15T10:00:00Z should pass the
	// client-side filter (>= since). The old event from 2024-06-14
	// should be excluded.
	require.Len(t, events, 1)
	assert.Equal(t, 1, events[0].ID)
	assert.Equal(t, "commenter", events[0].Author.Username)
	assert.Equal(t, "Issue", events[0].Note.NoteableType)
	assert.Equal(t, 5, events[0].Note.NoteableIID)
	assert.Equal(t, "LGTM", events[0].Note.Body)
}

// ---------------------------------------------------------------------------
// ListIssueNotes
// ---------------------------------------------------------------------------

func TestListIssueNotes(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/5/notes", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "asc", r.URL.Query().Get("sort"))

		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"id":         100,
				"body":       "First note",
				"author":     map[string]any{"id": 10, "username": "alice", "bot": false},
				"created_at": "2024-06-01T12:00:00Z",
			},
			{
				"id":         101,
				"body":       "Bot note",
				"author":     map[string]any{"id": 20, "username": "bot-user", "bot": true},
				"created_at": "2024-06-01T12:05:00Z",
			},
		})
	})

	notes, err := pc.ListIssueNotes(ctx, "myorg", "myrepo", 5)
	require.NoError(t, err)
	require.Len(t, notes, 2)
	assert.Equal(t, 100, notes[0].ID)
	assert.Equal(t, "First note", notes[0].Body)
	assert.Equal(t, "alice", notes[0].Author.Username)
	assert.False(t, notes[0].Author.Bot)
	assert.Equal(t, 101, notes[1].ID)
	assert.True(t, notes[1].Author.Bot)
}

// ---------------------------------------------------------------------------
// ListMergeRequestNotes
// ---------------------------------------------------------------------------

func TestListMergeRequestNotes(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/notes", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "asc", r.URL.Query().Get("sort"))

		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"id":         200,
				"body":       "Review comment",
				"author":     map[string]any{"id": 30, "username": "reviewer"},
				"created_at": "2024-06-01T15:00:00Z",
			},
		})
	})

	notes, err := pc.ListMergeRequestNotes(ctx, "myorg", "myrepo", 10)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Equal(t, 200, notes[0].ID)
	assert.Equal(t, "Review comment", notes[0].Body)
	assert.Equal(t, "reviewer", notes[0].Author.Username)
}

// ---------------------------------------------------------------------------
// ListResourceLabelEvents
// ---------------------------------------------------------------------------

func TestListResourceLabelEvents(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/5/resource_label_events", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))

		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"id":     1,
				"action": "add",
				"label":  map[string]any{"name": "ready-to-code"},
				"user":   map[string]any{"id": 10, "username": "alice"},
			},
			{
				"id":     2,
				"action": "remove",
				"label":  map[string]any{"name": "wip"},
				"user":   map[string]any{"id": 10, "username": "alice"},
			},
		})
	})

	events, err := pc.ListResourceLabelEvents(ctx, "myorg", "myrepo", 5)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, 1, events[0].ID)
	assert.Equal(t, "add", events[0].Action)
	assert.Equal(t, "ready-to-code", events[0].Label.Name)
	assert.Equal(t, 2, events[1].ID)
	assert.Equal(t, "remove", events[1].Action)
}

// ---------------------------------------------------------------------------
// GetCIVariable
// ---------------------------------------------------------------------------

func TestPollGetCIVariable(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/MY_VAR", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]string{
			"key":   "MY_VAR",
			"value": "cursor-value",
		})
	})

	val, err := pc.GetCIVariable(ctx, "myorg", "myrepo", "MY_VAR")
	require.NoError(t, err)
	assert.Equal(t, "cursor-value", val)
}

func TestPollGetCIVariable_NotFound(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/MISSING", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	})

	_, err := pc.GetCIVariable(ctx, "myorg", "myrepo", "MISSING")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// CreateNoteAwardEmoji
// ---------------------------------------------------------------------------

func TestCreateNoteAwardEmoji_Issue(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/5/notes/100/award_emoji", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]string
		readJSONBody(t, r, &body)
		assert.Equal(t, "eyes", body["name"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": 1, "name": "eyes"})
	})

	err := pc.CreateNoteAwardEmoji(ctx, "myorg", "myrepo", "Issue", 5, 100, "eyes")
	require.NoError(t, err)
}

func TestCreateNoteAwardEmoji_MergeRequest(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/notes/200/award_emoji", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]string
		readJSONBody(t, r, &body)
		assert.Equal(t, "thumbsup", body["name"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": 2, "name": "thumbsup"})
	})

	err := pc.CreateNoteAwardEmoji(ctx, "myorg", "myrepo", "MergeRequest", 10, 200, "thumbsup")
	require.NoError(t, err)
}

func TestCreateNoteAwardEmoji_InvalidType(t *testing.T) {
	pc, _ := setupPollTest(t)
	ctx := context.Background()

	err := pc.CreateNoteAwardEmoji(ctx, "myorg", "myrepo", "Unknown", 1, 1, "eyes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported noteable type")
}

// ---------------------------------------------------------------------------
// GetIssue (poll version)
// ---------------------------------------------------------------------------

func TestPollGetIssue(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/42", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]any{
			"iid":        42,
			"title":      "Poll issue",
			"state":      "opened",
			"labels":     []string{"bug", "critical"},
			"author":     map[string]any{"id": 10, "username": "alice", "bot": false},
			"updated_at": "2024-06-01T12:00:00Z",
		})
	})

	issue, err := pc.GetIssue(ctx, "myorg", "myrepo", 42)
	require.NoError(t, err)
	assert.Equal(t, 42, issue.IID)
	assert.Equal(t, "Poll issue", issue.Title)
	assert.Equal(t, "opened", issue.State)
	assert.Equal(t, []string{"bug", "critical"}, issue.Labels)
	assert.Equal(t, "alice", issue.Author.Username)
	assert.Equal(t, 10, issue.Author.ID)
}

func TestPollGetIssue_NotFound(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	})

	_, err := pc.GetIssue(ctx, "myorg", "myrepo", 999)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// GetMergeRequest
// ---------------------------------------------------------------------------

func TestPollGetMergeRequest(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]any{
			"iid":               10,
			"title":             "Add feature",
			"state":             "opened",
			"labels":            []string{"review"},
			"source_project_id": 100,
			"target_project_id": 100,
			"source_branch":     "feature",
			"target_branch":     "main",
			"author":            map[string]any{"id": 55, "username": "bob", "bot": false},
			"updated_at":        "2024-06-01T12:00:00Z",
		})
	})

	mr, err := pc.GetMergeRequest(ctx, "myorg", "myrepo", 10)
	require.NoError(t, err)
	assert.Equal(t, 10, mr.IID)
	assert.Equal(t, "Add feature", mr.Title)
	assert.Equal(t, 100, mr.SourceProjectID)
	assert.Equal(t, 100, mr.TargetProjectID)
	assert.Equal(t, "feature", mr.SourceBranch)
	assert.Equal(t, "main", mr.TargetBranch)
	assert.Equal(t, "bob", mr.Author.Username)
}

func TestPollGetMergeRequest_NotFound(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	})

	_, err := pc.GetMergeRequest(ctx, "myorg", "myrepo", 999)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// GetMemberAccessLevel
// ---------------------------------------------------------------------------

func TestGetMemberAccessLevel(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/members/all/42", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id":           42,
			"username":     "alice",
			"access_level": 30,
		})
	})

	level, err := pc.GetMemberAccessLevel(ctx, "myorg", "myrepo", 42)
	require.NoError(t, err)
	assert.Equal(t, 30, level)
}

func TestGetMemberAccessLevel_NotFound(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/members/all/999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	})

	_, err := pc.GetMemberAccessLevel(ctx, "myorg", "myrepo", 999)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// GetProjectPath
// ---------------------------------------------------------------------------

func TestGetProjectPath(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/12345", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id":                  12345,
			"path_with_namespace": "mygroup/myrepo",
		})
	})

	path, err := pc.GetProjectPath(ctx, 12345)
	require.NoError(t, err)
	assert.Equal(t, "mygroup/myrepo", path)
}

func TestGetProjectPath_NotFound(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/99999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]string{"message": "404 Not Found"})
	})

	_, err := pc.GetProjectPath(ctx, 99999)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Error paths — API failures
// ---------------------------------------------------------------------------

func TestListIssuesUpdatedSince_APIError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]string{"message": "internal error"})
	})

	_, err := pc.ListIssuesUpdatedSince(ctx, "myorg", "myrepo", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list issues updated since page 1")
}

func TestListMergeRequestsUpdatedSince_APIError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]string{"message": "internal error"})
	})

	_, err := pc.ListMergeRequestsUpdatedSince(ctx, "myorg", "myrepo", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list merge requests updated since page 1")
}

func TestListMergeRequestsUpdatedSince_Pagination(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch page {
		case "1", "":
			items := make([]map[string]any, 100)
			for i := range items {
				items[i] = map[string]any{
					"iid":               i + 1,
					"title":             "MR",
					"state":             "opened",
					"labels":            []string{},
					"source_project_id": 1,
					"target_project_id": 1,
					"source_branch":     "feature",
					"target_branch":     "main",
					"author":            map[string]any{"id": 1, "username": "u"},
					"updated_at":        "2024-06-01T12:00:00Z",
				}
			}
			writeJSON(t, w, http.StatusOK, items)
		case "2":
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{
					"iid":               101,
					"title":             "Last MR",
					"state":             "opened",
					"labels":            []string{},
					"source_project_id": 1,
					"target_project_id": 1,
					"source_branch":     "feature",
					"target_branch":     "main",
					"author":            map[string]any{"id": 1, "username": "u"},
					"updated_at":        "2024-06-01T12:00:00Z",
				},
			})
		}
	})

	since := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	mrs, err := pc.ListMergeRequestsUpdatedSince(ctx, "myorg", "myrepo", since)
	require.NoError(t, err)
	assert.Len(t, mrs, 101)
}

func TestListProjectEvents_APIError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/events", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]string{"message": "internal error"})
	})

	_, err := pc.ListProjectEvents(ctx, "myorg", "myrepo", "note", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list project events page 1")
}

func TestListProjectEvents_DecodeError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	})

	_, err := pc.ListProjectEvents(ctx, "myorg", "myrepo", "note", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode project events page 1")
}

func TestListIssueNotes_APIError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/5/notes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]string{"message": "internal error"})
	})

	_, err := pc.ListIssueNotes(ctx, "myorg", "myrepo", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list issue notes page 1")
}

func TestListMergeRequestNotes_APIError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/notes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]string{"message": "internal error"})
	})

	_, err := pc.ListMergeRequestNotes(ctx, "myorg", "myrepo", 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list merge request notes page 1")
}

func TestListResourceLabelEvents_APIError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/5/resource_label_events", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]string{"message": "internal error"})
	})

	_, err := pc.ListResourceLabelEvents(ctx, "myorg", "myrepo", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list resource label events page 1")
}

func TestCreateNoteAwardEmoji_APIError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/5/notes/100/award_emoji", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]string{"message": "internal error"})
	})

	err := pc.CreateNoteAwardEmoji(ctx, "myorg", "myrepo", "Issue", 5, 100, "eyes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create award emoji on Issue #5 note 100")
}

func TestCreateNoteAwardEmoji_MRError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/notes/200/award_emoji", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]string{"message": "internal error"})
	})

	err := pc.CreateNoteAwardEmoji(ctx, "myorg", "myrepo", "MergeRequest", 10, 200, "eyes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create award emoji on MergeRequest !10 note 200")
}

func TestGetCIVariable_DecodeError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/BAD", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	})

	_, err := pc.GetCIVariable(ctx, "myorg", "myrepo", "BAD")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode CI variable BAD")
}

func TestPollGetIssue_DecodeError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	})

	_, err := pc.GetIssue(ctx, "myorg", "myrepo", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode issue #42")
}

func TestGetMemberAccessLevel_DecodeError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/members/all/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	})

	_, err := pc.GetMemberAccessLevel(ctx, "myorg", "myrepo", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode member access level for user 42")
}

func TestGetProjectPath_DecodeError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/12345", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	})

	_, err := pc.GetProjectPath(ctx, 12345)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode project path for ID 12345")
}

func TestListIssuesUpdatedSince_DecodeError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	})

	_, err := pc.ListIssuesUpdatedSince(ctx, "myorg", "myrepo", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode issues page 1")
}

func TestListMergeRequestsUpdatedSince_DecodeError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	})

	_, err := pc.ListMergeRequestsUpdatedSince(ctx, "myorg", "myrepo", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode merge requests page 1")
}

func TestListIssueNotes_DecodeError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/5/notes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	})

	_, err := pc.ListIssueNotes(ctx, "myorg", "myrepo", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode issue notes page 1")
}

func TestListMergeRequestNotes_DecodeError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/merge_requests/10/notes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	})

	_, err := pc.ListMergeRequestNotes(ctx, "myorg", "myrepo", 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode merge request notes page 1")
}

func TestListResourceLabelEvents_DecodeError(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/issues/5/resource_label_events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not valid json`))
	})

	_, err := pc.ListResourceLabelEvents(ctx, "myorg", "myrepo", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode resource label events page 1")
}

// ---------------------------------------------------------------------------
// NewPollClient
// ---------------------------------------------------------------------------

func TestNewPollClient(t *testing.T) {
	client, err := New("test-token")
	require.NoError(t, err)

	pc := NewPollClient(client)
	assert.NotNil(t, pc)
	assert.Equal(t, client, pc.LiveClient)
}

// ---------------------------------------------------------------------------
// Inherited methods from LiveClient
// ---------------------------------------------------------------------------

func TestPollClient_UpdateCIVariable(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/variables/CURSOR", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "new-cursor", body["value"])
		writeJSON(t, w, http.StatusOK, map[string]any{"key": "CURSOR"})
	})

	err := pc.UpdateCIVariable(ctx, "myorg", "myrepo", "CURSOR", "new-cursor", false)
	require.NoError(t, err)
}

func TestPollClient_GetAuthenticatedUser(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]string{"username": "pollbot"})
	})

	username, err := pc.GetAuthenticatedUser(ctx)
	require.NoError(t, err)
	assert.Equal(t, "pollbot", username)
}

func TestPollClient_GetAuthenticatedUserID(t *testing.T) {
	pc, mux := setupPollTest(t)
	ctx := context.Background()

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]int{"id": 42})
	})

	id, err := pc.GetAuthenticatedUserID(ctx)
	require.NoError(t, err)
	assert.Equal(t, 42, id)
}
