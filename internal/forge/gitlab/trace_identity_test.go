package gitlab

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// The tests in this file cover ADR 0154: the immutable database IDs
// required by the identity tuple must survive adapter mapping. GitLab's
// per-project IIDs are display values; the database `id` is the identity,
// and issue and merge-request id spaces are independent.

func TestGetIssue_CapturesDatabaseID(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()
	handlerCalled := false

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues/5", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id":          987654321,
			"iid":         5,
			"title":       "Widget is broken",
			"description": "details",
			"web_url":     "https://gitlab.com/owner/repo/-/issues/5",
			"state":       "opened",
		})
	})

	issue, err := client.GetIssue(ctx, "owner", "repo", 5)
	require.NoError(t, err)
	assert.True(t, handlerCalled, "handler was not called — URL path mismatch")
	assert.Equal(t, int64(987654321), issue.ID, "GetIssue must capture the immutable database id, not the IID")
	assert.Equal(t, 5, issue.Number, "the display IID is still addressed by (owner, repo, iid)")
}

func TestCreateIssue_CapturesDatabaseID(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()
	handlerCalled := false

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"id":          987654322,
			"iid":         6,
			"title":       "New work",
			"description": "body",
			"web_url":     "https://gitlab.com/owner/repo/-/issues/6",
		})
	})

	issue, err := client.CreateIssue(ctx, "owner", "repo", "New work", "body")
	assert.True(t, handlerCalled)
	require.NoError(t, err)
	assert.Equal(t, int64(987654322), issue.ID, "CreateIssue must capture the immutable database id")
	assert.Equal(t, 6, issue.Number)
}

// TestGetIssue_NestedNamespace exercises a project path under nested groups:
// the identity fields must survive the same escaped-path mapping.
func TestGetIssue_NestedNamespace(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()
	handlerCalled := false

	mux.HandleFunc("/api/v4/projects/group%2Fsubgroup%2Frepo/issues/7", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id":          987654323,
			"iid":         7,
			"title":       "Nested",
			"description": "details",
			"web_url":     "https://gitlab.com/group/subgroup/repo/-/issues/7",
			"state":       "opened",
		})
	})

	issue, err := client.GetIssue(ctx, "group/subgroup", "repo", 7)
	require.NoError(t, err)
	assert.True(t, handlerCalled, "handler was not called — URL path mismatch")
	assert.Equal(t, int64(987654323), issue.ID)
	assert.Equal(t, 7, issue.Number)
}

func TestCreateChangeProposal_CapturesDatabaseID(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()
	handlerCalled := false

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"id":            555444333,
			"iid":           9,
			"title":         "Add widget",
			"web_url":       "https://gitlab.com/owner/repo/-/merge_requests/9",
			"source_branch": "feature",
			"target_branch": "main",
		})
	})

	cp, err := client.CreateChangeProposal(ctx, "owner", "repo", "Add widget", "body", "feature", "main")
	assert.True(t, handlerCalled)
	require.NoError(t, err)
	assert.Equal(t, int64(555444333), cp.ID, "CreateChangeProposal must capture the immutable database MR id, not the IID")
	assert.Equal(t, 9, cp.Number)
}

func TestListRepoPullRequests_CapturesDatabaseID(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()
	handlerCalled := false

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusOK, []map[string]any{{
			"id":            555444334,
			"iid":           10,
			"title":         "Fix widget",
			"web_url":       "https://gitlab.com/owner/repo/-/merge_requests/10",
			"source_branch": "fix",
			"target_branch": "main",
			"author":        map[string]any{"username": "octocat"},
		}})
	})

	cps, err := client.ListRepoPullRequests(ctx, "owner", "repo")
	assert.True(t, handlerCalled)
	require.NoError(t, err)
	require.Len(t, cps, 1)
	assert.Equal(t, int64(555444334), cps[0].ID, "ListRepoPullRequests must capture the immutable database MR id")
	assert.Equal(t, 10, cps[0].Number)
}

// TestListOpenIssues_CapturesDatabaseID keeps the display and identity
// fields distinguishable on the listing path used by polling.
func TestListOpenIssues_CapturesDatabaseID(t *testing.T) {
	client, mux := setupTest(t)
	ctx := context.Background()
	handlerCalled := false

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusOK, []map[string]any{{
			"id":          987654324,
			"iid":         11,
			"title":       "Listed",
			"description": "details",
			"web_url":     "https://gitlab.com/owner/repo/-/issues/11",
		}})
	})

	issues, err := client.ListOpenIssues(ctx, "owner", "repo")
	assert.True(t, handlerCalled)
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, int64(987654324), issues[0].ID)
	assert.Equal(t, 11, issues[0].Number)
}

// Compile-time guard: the fixture values above must stay forge.Issue /
// forge.ChangeProposal fields.
var (
	_ = forge.Issue{ID: 1}
	_ = forge.ChangeProposal{ID: 1}
)
