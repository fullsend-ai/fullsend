package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListPullRequestReviewThreads(t *testing.T) {
	line := 17
	originalLine := 16
	var request map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			require.Equal(t, "/repos/owner/repo/collaborators/reviewer/permission", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]any{"role_name": "write"})
			return
		}
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/graphql", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"repository": map[string]any{
					"pullRequest": map[string]any{
						"reviewThreads": map[string]any{
							"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
							"nodes": []any{
								map[string]any{
									"id": "PRRT_1", "isResolved": true, "path": "main.go",
									"diffSide": "RIGHT", "line": line, "originalLine": originalLine,
									"startDiffSide": nil, "startLine": nil, "originalStartLine": nil, "isOutdated": false,
									"resolvedBy": map[string]any{"login": "reviewer", "__typename": "User"},
									"comments": map[string]any{
										"pageInfo": map[string]any{"hasNextPage": true},
										"nodes": []any{map[string]any{
											"author": map[string]any{"login": "reviewer", "__typename": "User"},
											"body":   "please update this", "createdAt": "2026-10-04T10:00:00Z",
										}},
									},
								},
								map[string]any{
									"id": "PRRT_2", "isResolved": false, "path": "README.md",
									"line": nil, "originalLine": nil, "resolvedBy": nil,
									"comments": map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": []any{}},
								},
							},
						},
					},
				},
			},
		})
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv).ListPullRequestReviewThreads(context.Background(), "owner", "repo", 42)
	require.NoError(t, err)
	assert.Equal(t, "owner", request["variables"].(map[string]any)["owner"])
	assert.Equal(t, "repo", request["variables"].(map[string]any)["name"])
	assert.Equal(t, float64(42), request["variables"].(map[string]any)["number"])
	assert.Len(t, got.Threads, 2)
	query := request["query"].(string)
	for _, field := range []string{"diffSide", "startDiffSide", "startLine", "originalStartLine", "isOutdated"} {
		assert.Contains(t, query, field)
	}
	right, notOutdated := forge.DiffSideRight, false
	assert.Equal(t, forge.ReviewThread{
		ID: "PRRT_1", IsResolved: true, Path: "main.go", Side: &right, Line: &line,
		OriginalLine: &originalLine, NewLine: &line, IsOutdated: &notOutdated,
		ResolvedBy: "reviewer", ResolvedByType: "User",
		ResolvedByRole: normevent.RoleWrite, ResolvedByRoleVerified: true,
		Comments: []forge.ReviewThreadComment{{
			Author: "reviewer", AuthorType: "User", AuthorRole: normevent.RoleWrite, AuthorRoleVerified: true,
			Body: "please update this", CreatedAt: "2026-10-04T10:00:00Z",
		}}, CommentsTruncated: true,
	}, got.Threads[0])
	assert.Nil(t, got.Threads[1].Line)
	assert.Nil(t, got.Threads[1].Side, "a missing diffSide is unknown")
	assert.Empty(t, got.Threads[1].ResolvedBy)
	assert.Equal(t, normevent.RoleNone, got.Threads[1].ResolvedByRole)
}

func TestListPullRequestReviewThreads_PositionContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		thread := func(fields map[string]any) map[string]any {
			fields["comments"] = map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": []any{}}
			return fields
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
			"pullRequest": map[string]any{"reviewThreads": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes": []any{
					thread(map[string]any{
						"id": "old-side", "path": "main.go", "diffSide": "LEFT",
						"line": 8, "originalLine": 8, "isOutdated": false,
					}),
					thread(map[string]any{
						"id": "outdated", "path": "main.go", "diffSide": "RIGHT",
						"line": nil, "originalLine": 21, "isOutdated": true,
					}),
					thread(map[string]any{
						"id": "multi-line", "path": "main.go", "diffSide": "RIGHT",
						"line": 14, "originalLine": 13, "startDiffSide": "LEFT",
						"startLine": 10, "originalStartLine": 9, "isOutdated": false,
					}),
					thread(map[string]any{
						"id": "outdated-multi-line", "path": "main.go", "diffSide": "RIGHT",
						"line": nil, "originalLine": 25, "startDiffSide": "LEFT",
						"startLine": nil, "originalStartLine": 22, "isOutdated": true,
					}),
				},
			}},
		}}})
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv).ListPullRequestReviewThreads(context.Background(), "owner", "repo", 1)
	require.NoError(t, err)
	require.Len(t, got.Threads, 4)

	oldSide := got.Threads[0]
	require.NotNil(t, oldSide.Side)
	assert.Equal(t, forge.DiffSideLeft, *oldSide.Side)
	assert.Equal(t, 8, *oldSide.Line)
	assert.Equal(t, 8, *oldSide.OldLine, "a LEFT-side line is an old-file line")
	assert.Nil(t, oldSide.NewLine)
	require.NotNil(t, oldSide.IsOutdated)
	assert.False(t, *oldSide.IsOutdated)
	assert.Nil(t, oldSide.StartSide)
	assert.Nil(t, oldSide.StartLine)

	outdated := got.Threads[1]
	assert.Equal(t, forge.DiffSideRight, *outdated.Side)
	assert.Nil(t, outdated.Line)
	assert.Nil(t, outdated.OldLine)
	assert.Nil(t, outdated.NewLine)
	assert.Equal(t, 21, *outdated.OriginalLine)
	require.NotNil(t, outdated.IsOutdated)
	assert.True(t, *outdated.IsOutdated)

	multi := got.Threads[2]
	assert.Equal(t, forge.DiffSideRight, *multi.Side)
	assert.Equal(t, 14, *multi.Line)
	assert.Equal(t, 14, *multi.NewLine)
	require.NotNil(t, multi.StartSide)
	assert.Equal(t, forge.DiffSideLeft, *multi.StartSide)
	assert.Equal(t, 10, *multi.StartLine)
	assert.Equal(t, 13, *multi.OriginalLine)
	assert.Equal(t, 9, *multi.OriginalStartLine)

	outdatedMulti := got.Threads[3]
	assert.Nil(t, outdatedMulti.Line)
	assert.Nil(t, outdatedMulti.StartLine)
	require.NotNil(t, outdatedMulti.StartSide, "start side survives a null startLine on outdated threads")
	assert.Equal(t, forge.DiffSideLeft, *outdatedMulti.StartSide)
	assert.Equal(t, 25, *outdatedMulti.OriginalLine)
	assert.Equal(t, 22, *outdatedMulti.OriginalStartLine)
}

func TestListPullRequestReviewThreads_GHESGraphQLEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/graphql", r.URL.Path)
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`))
	}))
	defer srv.Close()

	client := New("test-token").WithBaseURL(srv.URL + "/api/v3").WithAfterFunc(noWaitAfter)
	_, err := client.ListPullRequestReviewThreads(context.Background(), "owner", "repo", 1)
	require.NoError(t, err)
}

func TestListPullRequestReviewThreads_RoleLookupFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			switch r.URL.Path {
			case "/repos/owner/repo/collaborators/missing/permission":
				w.WriteHeader(http.StatusNotFound)
			case "/repos/owner/repo/collaborators/failing/permission":
				w.WriteHeader(http.StatusInternalServerError)
			default:
				t.Fatalf("unexpected permission path %s", r.URL.Path)
			}
			return
		}

		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"thread-1","isResolved":true,"resolvedBy":{"login":"missing","__typename":"User"},"comments":{"pageInfo":{"hasNextPage":false},"nodes":[{"author":{"login":"failing","__typename":"User"},"body":"comment","createdAt":"2026-10-06T10:00:00Z"},{"author":{"login":"review-bot","__typename":"Bot"},"body":"bot comment","createdAt":"2026-10-06T10:00:00Z"}]}}]}}}}}`))
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv).ListPullRequestReviewThreads(context.Background(), "owner", "repo", 1)
	require.NoError(t, err)
	require.Len(t, got.Threads, 1)
	assert.Equal(t, normevent.RoleNone, got.Threads[0].ResolvedByRole)
	assert.True(t, got.Threads[0].ResolvedByRoleVerified, "a confirmed non-member is authoritative RoleNone")
	assert.Equal(t, normevent.RoleNone, got.Threads[0].Comments[0].AuthorRole)
	assert.False(t, got.Threads[0].Comments[0].AuthorRoleVerified, "a failed lookup is not authoritative")
	assert.Equal(t, normevent.RoleNone, got.Threads[0].Comments[1].AuthorRole)
	assert.False(t, got.Threads[0].Comments[1].AuthorRoleVerified, "bots do not use human permission roles")
}

func TestListPullRequestReviewThreads_PaginatesAndCaps(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Variables struct {
				Cursor *string `json:"cursor"`
			} `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if calls == 1 {
			assert.Nil(t, body.Variables.Cursor)
		} else {
			require.NotNil(t, body.Variables.Cursor)
			assert.Equal(t, fmt.Sprintf("cursor-%d", calls-1), *body.Variables.Cursor)
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{
			"pullRequest": map[string]any{"reviewThreads": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": true, "endCursor": fmt.Sprintf("cursor-%d", calls)},
				"nodes":    []any{map[string]any{"id": fmt.Sprintf("thread-%d", calls), "comments": map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": []any{}}}},
			}},
		}}})
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv).ListPullRequestReviewThreads(context.Background(), "owner", "repo", 1)
	require.NoError(t, err)
	assert.Equal(t, 20, calls)
	assert.True(t, got.Truncated)
	assert.Len(t, got.Threads, 20)
}

func TestListPullRequestReviewThreads_Errors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "graphql error", status: http.StatusOK, body: `{"errors":[{"message":"forbidden"}]}`, wantErr: "forbidden"},
		{name: "invalid json", status: http.StatusOK, body: `{`, wantErr: "decode pull request review threads"},
		{name: "missing cursor", status: http.StatusOK, body: `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":true,"endCursor":""},"nodes":[]}}}}}`, wantErr: "missing pagination cursor"},
		{name: "http error", status: http.StatusForbidden, body: `{"message":"denied"}`, wantErr: "page 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			_, err := newTestClient(t, srv).ListPullRequestReviewThreads(context.Background(), "o", "r", 1)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
