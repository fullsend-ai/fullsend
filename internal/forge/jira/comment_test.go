package jira

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestCreateComment(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/comment", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		adfBody, ok := body["body"].(map[string]any)
		require.True(t, ok, "request body's \"body\" field should be an ADF doc, got: %+v", body["body"])
		assert.Equal(t, "doc", adfBody["type"])

		writeJSON(t, w, http.StatusCreated, Comment{
			ID:      "10001",
			Body:    adfBody,
			Author:  User{DisplayName: "fullsend-bot"},
			Created: "2026-08-06T00:00:00.000+0000",
		})
	})

	comment, err := client.CreateComment(ctx, "PROJ-1", "hello there")
	require.NoError(t, err)
	assert.Equal(t, "10001", comment.ID)
	assert.Equal(t, "fullsend-bot", comment.Author.DisplayName)
}

func TestCreateComment_NotFound(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-999/comment", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"errorMessages": []string{"Issue does not exist or you do not have permission to see it."},
		})
	})

	_, err := client.CreateComment(ctx, "PROJ-999", "hello there")
	require.Error(t, err)
	assert.True(t, errors.Is(err, forge.ErrNotFound))
}

func TestUpdateComment(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/comment/10001", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		adfBody, ok := body["body"].(map[string]any)
		require.True(t, ok, "request body's \"body\" field should be an ADF doc, got: %+v", body["body"])
		assert.Equal(t, "doc", adfBody["type"])

		w.WriteHeader(http.StatusOK)
	})

	err := client.UpdateComment(ctx, "PROJ-1", "10001", "updated text")
	require.NoError(t, err)
}

func TestUpdateComment_NotFound(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/comment/99999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"errorMessages": []string{"The comment does not exist."},
		})
	})

	err := client.UpdateComment(ctx, "PROJ-1", "99999", "updated text")
	require.Error(t, err)
	assert.True(t, errors.Is(err, forge.ErrNotFound))
}
