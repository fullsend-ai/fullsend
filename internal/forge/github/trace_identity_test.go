package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file are V03 for AISDLC-576: the immutable database IDs
// required by the D01 identity tuple must survive adapter mapping. GitHub's
// per-repo numbers are display values; the REST `id` is the identity.

func TestGetIssue_CapturesImmutableID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/org/repo/issues/7", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       2468913570,
			"number":   7,
			"title":    "Widget is broken",
			"body":     "details",
			"html_url": "https://github.com/org/repo/issues/7",
			"state":    "open",
			"labels":   []any{},
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	issue, err := client.GetIssue(context.Background(), "org", "repo", 7)
	require.NoError(t, err)
	assert.Equal(t, int64(2468913570), issue.ID, "GetIssue must capture the immutable REST issue id")
	assert.Equal(t, 7, issue.Number, "the display number is still addressed by (owner, repo, number)")
}

func TestCreateIssue_CapturesImmutableID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       2468913571,
			"number":   8,
			"title":    "New work",
			"body":     "body",
			"html_url": "https://github.com/org/repo/issues/8",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	issue, err := client.CreateIssue(context.Background(), "org", "repo", "New work", "body")
	require.NoError(t, err)
	assert.Equal(t, int64(2468913571), issue.ID, "CreateIssue must capture the immutable REST issue id")
	assert.Equal(t, 8, issue.Number)
}

func TestCreateChangeProposal_CapturesImmutableID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       1357924680,
			"number":   12,
			"title":    "Add widget",
			"html_url": "https://github.com/org/repo/pull/12",
		})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	cp, err := client.CreateChangeProposal(context.Background(), "org", "repo", "Add widget", "body", "head", "main")
	require.NoError(t, err)
	assert.Equal(t, int64(1357924680), cp.ID, "CreateChangeProposal must capture the immutable REST pull-request id")
	assert.Equal(t, 12, cp.Number)
}

func TestListRepoPullRequests_CapturesImmutableID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":       1357924681,
			"number":   13,
			"title":    "Fix widget",
			"html_url": "https://github.com/org/repo/pull/13",
			"head":     map[string]any{"ref": "fix", "repo": map[string]any{"full_name": "org/repo"}},
			"base":     map[string]any{"ref": "main"},
			"user":     map[string]any{"login": "octocat"},
		}})
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	cps, err := client.ListRepoPullRequests(context.Background(), "org", "repo")
	require.NoError(t, err)
	require.Len(t, cps, 1)
	assert.Equal(t, int64(1357924681), cps[0].ID, "ListRepoPullRequests must capture the immutable REST pull-request id")
	assert.Equal(t, 13, cps[0].Number)
}
