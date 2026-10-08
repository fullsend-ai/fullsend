package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateIssue(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)

		var body struct {
			Fields map[string]any `json:"fields"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, map[string]any{"key": "PROJ"}, body.Fields["project"])
		assert.Equal(t, map[string]any{"id": "10003"}, body.Fields["issuetype"])
		assert.Equal(t, map[string]any{"key": "PROJ-42"}, body.Fields["parent"])
		assert.Equal(t, "Sub-task title", body.Fields["summary"])
		desc, ok := body.Fields["description"].(map[string]any)
		require.True(t, ok, "description should be an ADF doc, got: %+v", body.Fields["description"])
		assert.Equal(t, "doc", desc["type"])

		writeJSON(t, w, http.StatusCreated, CreatedIssue{ID: "10100", Key: "PROJ-43", Self: "https://example/rest/api/3/issue/10100"})
	})

	created, err := client.CreateIssue(ctx, CreateIssueInput{
		ProjectKey:  "PROJ",
		IssueType:   "10003",
		ParentKey:   "PROJ-42",
		Summary:     "Sub-task title",
		Description: "some *markdown*",
	})
	require.NoError(t, err)
	assert.Equal(t, "10100", created.ID)
	assert.Equal(t, "PROJ-43", created.Key)
}

func TestCreateIssue_TypeNameNoParentNoDescription(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Fields map[string]any `json:"fields"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, map[string]any{"name": "Task"}, body.Fields["issuetype"])
		assert.NotContains(t, body.Fields, "parent")
		assert.NotContains(t, body.Fields, "description")

		writeJSON(t, w, http.StatusCreated, CreatedIssue{ID: "10101", Key: "PROJ-44"})
	})

	created, err := client.CreateIssue(ctx, CreateIssueInput{ProjectKey: "PROJ", IssueType: "Task", Summary: "t"})
	require.NoError(t, err)
	assert.Equal(t, "PROJ-44", created.Key)
}

func TestCreateIssue_NotRetriedOnServerError(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	calls := 0
	mux.HandleFunc("/rest/api/3/issue", func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(t, w, http.StatusInternalServerError, map[string]any{"errorMessages": []string{"oops"}})
	})

	_, err := client.CreateIssue(ctx, CreateIssueInput{ProjectKey: "PROJ", IssueType: "10003", Summary: "t"})
	require.Error(t, err)
	assert.Equal(t, 1, calls, "a non-idempotent create must not be retried on 5xx")
}

func TestCreateIssue_BadRequest(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]any{"errors": map[string]string{"parent": "Given parent work item does not belong to appropriate hierarchy."}})
	})

	_, err := client.CreateIssue(ctx, CreateIssueInput{ProjectKey: "PROJ", IssueType: "10003", ParentKey: "PROJ-1", Summary: "t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent")
}

func TestCreateIssue_DescriptionTooLarge(t *testing.T) {
	t.Parallel()
	client, _ := setupTest(t)
	_, err := client.CreateIssue(context.Background(), CreateIssueInput{
		ProjectKey:  "PROJ",
		IssueType:   "10003",
		Summary:     "t",
		Description: strings.Repeat("a", MaxMarkdownBytes+1),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ADF")
}
