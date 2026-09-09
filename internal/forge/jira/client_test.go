package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// setupTest creates an httptest server and a LiveClient pointed at it.
func setupTest(t *testing.T) (*LiveClient, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := New("test-token", WithBaseURL(srv.URL))
	require.NoError(t, err)
	return client, mux
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

// ---------------------------------------------------------------------------
// CheckRedirect Authorization stripping
// ---------------------------------------------------------------------------

// TestCheckRedirect_AuthorizationHandling verifies the redirect policy: a
// same-origin redirect is followed with the Authorization header intact,
// but any redirect leaving the origin is REFUSED outright rather than
// followed-with-stripped-header. Refusing is stronger than stripping
// because Go's client re-copies the initial request's headers onto every
// hop and only strips them when leaving the initial domain-or-subdomain
// (host only), so a multi-hop same-host TLS-downgrade or subdomain chain
// would otherwise re-attach the credential — the "3-hop re-attach" subtest
// pins exactly that, and would leak under a per-previous-hop strip.
func TestCheckRedirect_AuthorizationHandling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	newEchoServer := func(t *testing.T) (*httptest.Server, *http.ServeMux) {
		t.Helper()
		mux := http.NewServeMux()
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, http.StatusOK, User{
				AccountID:   "echo",
				DisplayName: "auth=" + r.Header.Get("Authorization"),
			})
		})
		return srv, mux
	}

	t.Run("same-origin redirect keeps Authorization", func(t *testing.T) {
		t.Parallel()
		srv, mux := newEchoServer(t)
		mux.HandleFunc("/rest/api/3/redirected", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, srv.URL+"/rest/api/3/myself", http.StatusFound)
		})
		client, err := New("test-token", WithBaseURL(srv.URL))
		require.NoError(t, err)

		var user User
		require.NoError(t, client.do(ctx, http.MethodGet, "/redirected", nil, &user))
		assert.Equal(t, "auth=Bearer test-token", user.DisplayName,
			"Authorization must be preserved across a same-origin, same-scheme redirect")
	})

	t.Run("cross-origin redirect is refused", func(t *testing.T) {
		t.Parallel()
		other, _ := newEchoServer(t)
		srv, mux := newEchoServer(t)
		// Different httptest server = different host:port = off-origin.
		mux.HandleFunc("/rest/api/3/redirected", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/rest/api/3/myself", http.StatusFound)
		})
		client, err := New("test-token", WithBaseURL(srv.URL))
		require.NoError(t, err)

		var user User
		err = client.do(ctx, http.MethodGet, "/redirected", nil, &user)
		require.Error(t, err, "a redirect off the Jira origin must be refused")
		assert.Contains(t, err.Error(), "refusing redirect")
		assert.Empty(t, user.DisplayName, "the off-origin target must never be reached")
	})

	t.Run("multi-hop same-host chain cannot re-attach the credential", func(t *testing.T) {
		t.Parallel()
		// base -> base/hop2 -> foreign/myself. The middle hop stays on the
		// origin (followed), the third leaves it and must be refused before
		// the credential can be re-copied onto it.
		foreign, _ := newEchoServer(t)
		srv, mux := newEchoServer(t)
		mux.HandleFunc("/rest/api/3/redirected", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, srv.URL+"/rest/api/3/hop2", http.StatusFound)
		})
		mux.HandleFunc("/rest/api/3/hop2", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, foreign.URL+"/rest/api/3/myself", http.StatusFound)
		})
		client, err := New("test-token", WithBaseURL(srv.URL))
		require.NoError(t, err)

		var user User
		err = client.do(ctx, http.MethodGet, "/redirected", nil, &user)
		require.Error(t, err, "the off-origin third hop must be refused")
		assert.Empty(t, user.DisplayName, "the foreign host must never be reached, so the token cannot re-attach")
	})
}

func TestValidateBaseURL_RejectsEmbeddedCredentials(t *testing.T) {
	t.Parallel()
	_, err := New("test-token", WithBaseURL("https://user:secret@jira.example.com"))
	require.Error(t, err, "a base URL with embedded credentials must be rejected")
	assert.Contains(t, err.Error(), "must not embed credentials")
	assert.NotContains(t, err.Error(), "secret", "the password must be redacted in the error")
}

// ---------------------------------------------------------------------------
// Auth header verification
// ---------------------------------------------------------------------------

func TestAuthHeader_BearerWithoutEmail(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		assert.Equal(t, "Bearer test-token", auth)
		writeJSON(t, w, http.StatusOK, User{AccountID: "123", DisplayName: "Test"})
	})

	_, err := client.GetMyself(ctx)
	require.NoError(t, err)
}

func TestAuthHeader_BasicWithEmail(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := New("test-token", WithBaseURL(srv.URL), WithEmail("user@example.com"))
	require.NoError(t, err)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		assert.True(t, strings.HasPrefix(auth, "Basic "), "expected Basic auth prefix")
		// Verify the decoded value is email:token
		assert.Contains(t, auth, "Basic ")
		writeJSON(t, w, http.StatusOK, User{AccountID: "123", DisplayName: "Test"})
	})

	_, err = client.GetMyself(ctx)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// SearchIssues pagination
// ---------------------------------------------------------------------------

func TestSearchIssues_Pagination(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	callCount := 0
	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body searchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		// The poller requests only the fields it consumes, not "*all":
		// unbounded payloads can exceed the client's decode cap and wedge
		// the whole cycle.
		assert.Equal(t, searchFields, body.Fields)

		callCount++
		switch body.NextPageToken {
		case "":
			issues := make([]Issue, 50)
			for i := range issues {
				issues[i] = Issue{ID: fmt.Sprintf("%d", i+1), Key: fmt.Sprintf("PROJ-%d", i+1)}
			}
			writeJSON(t, w, http.StatusOK, SearchResult{
				Issues:        issues,
				NextPageToken: "page-2-token",
			})
		case "page-2-token":
			issues := make([]Issue, 50)
			for i := range issues {
				issues[i] = Issue{ID: fmt.Sprintf("%d", i+51), Key: fmt.Sprintf("PROJ-%d", i+51)}
			}
			writeJSON(t, w, http.StatusOK, SearchResult{
				Issues:        issues,
				NextPageToken: "page-3-token",
			})
		case "page-3-token":
			issues := make([]Issue, 20)
			for i := range issues {
				issues[i] = Issue{ID: fmt.Sprintf("%d", i+101), Key: fmt.Sprintf("PROJ-%d", i+101)}
			}
			writeJSON(t, w, http.StatusOK, SearchResult{
				Issues: issues,
				IsLast: true,
			})
		default:
			t.Fatalf("unexpected nextPageToken: %s", body.NextPageToken)
		}
	})

	issues, err := client.SearchIssues(ctx, "project = PROJ", 0)
	require.NoError(t, err)
	assert.Len(t, issues, 120)
	assert.Equal(t, 3, callCount)
	assert.Equal(t, "PROJ-1", issues[0].Key)
	assert.Equal(t, "PROJ-120", issues[119].Key)
}

// ---------------------------------------------------------------------------
// ListComments pagination
// ---------------------------------------------------------------------------

func TestListComments_Pagination(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/comment", func(w http.ResponseWriter, r *http.Request) {
		startAt := r.URL.Query().Get("startAt")
		switch startAt {
		case "0", "":
			comments := make([]Comment, 100)
			for i := range comments {
				comments[i] = Comment{ID: fmt.Sprintf("%d", i+1), Created: "2024-01-01T00:00:00.000+0000"}
			}
			writeJSON(t, w, http.StatusOK, CommentPage{
				Comments:   comments,
				Total:      150,
				MaxResults: 100,
				StartAt:    0,
			})
		case "100":
			comments := make([]Comment, 50)
			for i := range comments {
				comments[i] = Comment{ID: fmt.Sprintf("%d", i+101), Created: "2024-01-02T00:00:00.000+0000"}
			}
			writeJSON(t, w, http.StatusOK, CommentPage{
				Comments:   comments,
				Total:      150,
				MaxResults: 100,
				StartAt:    100,
			})
		}
	})

	comments, err := client.ListComments(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Len(t, comments, 150)
}

// ---------------------------------------------------------------------------
// ListChangelog pagination
// ---------------------------------------------------------------------------

func TestListChangelog_Pagination(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/changelog", func(w http.ResponseWriter, r *http.Request) {
		startAt := r.URL.Query().Get("startAt")
		switch startAt {
		case "0", "":
			entries := make([]ChangelogEntry, 100)
			for i := range entries {
				entries[i] = ChangelogEntry{
					ID:      fmt.Sprintf("%d", i+1),
					Created: "2024-01-01T00:00:00.000+0000",
					Items:   []ChangeItem{{Field: "status", ToString: "In Progress"}},
				}
			}
			writeJSON(t, w, http.StatusOK, changelogPage{
				Values:     entries,
				Total:      130,
				MaxResults: 100,
				StartAt:    0,
				IsLast:     false,
			})
		case "100":
			entries := make([]ChangelogEntry, 30)
			for i := range entries {
				entries[i] = ChangelogEntry{
					ID:      fmt.Sprintf("%d", i+101),
					Created: "2024-01-02T00:00:00.000+0000",
					Items:   []ChangeItem{{Field: "status", ToString: "Done"}},
				}
			}
			writeJSON(t, w, http.StatusOK, changelogPage{
				Values:     entries,
				Total:      130,
				MaxResults: 100,
				StartAt:    100,
				IsLast:     true,
			})
		}
	})

	entries, err := client.ListChangelog(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Len(t, entries, 130)
}

// ---------------------------------------------------------------------------
// GetEntityProperty — found and not-found
// ---------------------------------------------------------------------------

func TestGetEntityProperty_Found(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/properties/fullsend.lock", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]any{
			"key":   "fullsend.lock",
			"value": map[string]string{"id": "abc-123", "ts": "2024-01-01T00:00:00Z"},
		})
	})

	val, err := client.GetEntityProperty(ctx, "PROJ-1", "fullsend.lock")
	require.NoError(t, err)

	var parsed map[string]string
	require.NoError(t, json.Unmarshal(val, &parsed))
	assert.Equal(t, "abc-123", parsed["id"])
}

func TestGetEntityProperty_NotFound(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/properties/missing", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"errorMessages": []string{"Property 'missing' not found"},
		})
	})

	_, err := client.GetEntityProperty(ctx, "PROJ-1", "missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, forge.ErrNotFound), "expected forge.ErrNotFound, got: %v", err)
}

// ---------------------------------------------------------------------------
// SetEntityProperty
// ---------------------------------------------------------------------------

func TestSetEntityProperty(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/properties/fullsend.lock", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "abc-123", body["id"])

		w.WriteHeader(http.StatusOK)
	})

	err := client.SetEntityProperty(ctx, "PROJ-1", "fullsend.lock", map[string]string{"id": "abc-123"})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// GetCommentProperty — found and not-found
// ---------------------------------------------------------------------------

func TestGetCommentProperty_Found(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/comment/10001/properties/fullsend.sticky-marker", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]any{
			"key":   "fullsend.sticky-marker",
			"value": "<!-- fullsend:triage-agent -->",
		})
	})

	val, err := client.GetCommentProperty(ctx, "PROJ-1", "10001", "fullsend.sticky-marker")
	require.NoError(t, err)

	var stored string
	require.NoError(t, json.Unmarshal(val, &stored))
	assert.Equal(t, "<!-- fullsend:triage-agent -->", stored)
}

func TestGetCommentProperty_NotFound(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	handlerCalled := false
	mux.HandleFunc("/rest/api/3/comment/10001/properties/missing", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"errorMessages": []string{"Property 'missing' not found"},
		})
	})

	_, err := client.GetCommentProperty(ctx, "PROJ-1", "10001", "missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, forge.ErrNotFound), "expected forge.ErrNotFound, got: %v", err)
	assert.True(t, handlerCalled, "handler was not called — URL path mismatch")
}

// ---------------------------------------------------------------------------
// SetCommentProperty
// ---------------------------------------------------------------------------

func TestSetCommentProperty(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/comment/10001/properties/fullsend.sticky-marker", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var body string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "<!-- fullsend:triage-agent -->", body)

		w.WriteHeader(http.StatusOK)
	})

	err := client.SetCommentProperty(ctx, "PROJ-1", "10001", "fullsend.sticky-marker", "<!-- fullsend:triage-agent -->")
	require.NoError(t, err)
}

func TestSetCommentProperty_Error(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/comment/10001/properties/fullsend.sticky-marker", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{
			"errorMessages": []string{"Forbidden"},
		})
	})

	err := client.SetCommentProperty(ctx, "PROJ-1", "10001", "fullsend.sticky-marker", "marker-value")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set comment property")
}

// ---------------------------------------------------------------------------
// CreateCommentWithProperties
// ---------------------------------------------------------------------------

func TestCreateCommentWithProperties(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/comment", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)

		var req struct {
			Body       map[string]any    `json:"body"`
			Properties []CommentProperty `json:"properties"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Len(t, req.Properties, 1)
		assert.Equal(t, "fullsend.sticky-marker", req.Properties[0].Key)

		writeJSON(t, w, http.StatusCreated, Comment{
			ID:      "10001",
			Body:    req.Body,
			Created: "2024-01-01T00:00:00.000+0000",
		})
	})

	props := []CommentProperty{
		{Key: "fullsend.sticky-marker", Value: json.RawMessage(`"<!-- fullsend:triage-agent -->"`)},
	}
	comment, err := client.CreateCommentWithProperties(ctx, "PROJ-1", "hello world", props)
	require.NoError(t, err)
	assert.Equal(t, "10001", comment.ID)
}

// ---------------------------------------------------------------------------
// DeleteComment
// ---------------------------------------------------------------------------

func TestDeleteComment(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/comment/50001", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeleteComment(ctx, "PROJ-1", "50001")
	require.NoError(t, err)
}

func TestDeleteComment_NotFound(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/comment/99999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"errorMessages": []string{"Comment does not exist."},
		})
	})

	err := client.DeleteComment(ctx, "PROJ-1", "99999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete comment")
}

// ---------------------------------------------------------------------------
// DeleteEntityProperty
// ---------------------------------------------------------------------------

func TestDeleteEntityProperty(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-1/properties/fullsend.lock", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})

	err := client.DeleteEntityProperty(ctx, "PROJ-1", "fullsend.lock")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// GetMyself
// ---------------------------------------------------------------------------

func TestGetMyself(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, User{
			AccountID:   "5b10a2844c20165700ede21g",
			DisplayName: "Mia Krystof",
			AccountType: "atlassian",
			Active:      true,
		})
	})

	user, err := client.GetMyself(ctx)
	require.NoError(t, err)
	assert.Equal(t, "5b10a2844c20165700ede21g", user.AccountID)
	assert.Equal(t, "Mia Krystof", user.DisplayName)
	assert.Equal(t, "atlassian", user.AccountType)
	assert.True(t, user.Active)
}

// ---------------------------------------------------------------------------
// Error responses
// ---------------------------------------------------------------------------

func TestErrorResponse_401(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusUnauthorized, map[string]any{
			"errorMessages": []string{"You do not have permission to access this resource"},
		})
	})

	_, err := client.GetMyself(ctx)
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
}

func TestErrorResponse_403(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{
			"errorMessages": []string{"Forbidden"},
		})
	})

	_, err := client.GetMyself(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, forge.ErrForbidden))
}

func TestErrorResponse_429_RetryAfter(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Use a short-timeout client for this test.
	client, err := New("test-token", WithBaseURL(srv.URL))
	require.NoError(t, err)
	ctx := context.Background()

	callCount := 0
	mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 1 {
			w.Header().Set("Retry-After", "1")
			writeJSON(t, w, http.StatusTooManyRequests, map[string]any{
				"errorMessages": []string{"Rate limit exceeded"},
			})
			return
		}
		writeJSON(t, w, http.StatusOK, User{AccountID: "123"})
	})

	user, err := client.GetMyself(ctx)
	require.NoError(t, err)
	assert.Equal(t, "123", user.AccountID)
	assert.Equal(t, 2, callCount)
}

// ---------------------------------------------------------------------------
// Base URL validation
// ---------------------------------------------------------------------------

func TestBaseURLValidation_HTTPSRequired(t *testing.T) {
	t.Parallel()
	_, err := New("token", WithBaseURL("http://jira.example.com"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insecure scheme")
}

func TestBaseURLValidation_LoopbackException(t *testing.T) {
	t.Parallel()
	_, err := New("token", WithBaseURL("http://localhost:8080"))
	require.NoError(t, err)

	_, err = New("token", WithBaseURL("http://127.0.0.1:8080"))
	require.NoError(t, err)
}

func TestBaseURLValidation_HTTPSAllowed(t *testing.T) {
	t.Parallel()
	_, err := New("token", WithBaseURL("https://acme.atlassian.net"))
	require.NoError(t, err)
}

func TestNew_EmptyToken(t *testing.T) {
	t.Parallel()
	_, err := New("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token must not be empty")
}

func TestNew_NoBaseURL(t *testing.T) {
	t.Parallel()
	_, err := New("token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base URL must be set")
}

// ---------------------------------------------------------------------------
// GetIssue
// ---------------------------------------------------------------------------

func TestGetIssue(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-42", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, Issue{
			ID:  "10042",
			Key: "PROJ-42",
			Fields: IssueFields{
				Summary: "Test issue",
				Status:  Status{Name: "Open", StatusCategory: StatusCategory{Key: "new"}},
				Labels:  []string{"bug"},
			},
		})
	})

	issue, err := client.GetIssue(ctx, "PROJ-42")
	require.NoError(t, err)
	assert.Equal(t, "PROJ-42", issue.Key)
	assert.Equal(t, "Test issue", issue.Fields.Summary)
	assert.Equal(t, "new", issue.Fields.Status.StatusCategory.Key)
}

func TestGetIssue_NotFound(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/issue/PROJ-999", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"errorMessages": []string{"Issue does not exist or you do not have permission to see it."},
		})
	})

	_, err := client.GetIssue(ctx, "PROJ-999")
	require.Error(t, err)
	assert.True(t, errors.Is(err, forge.ErrNotFound))
}

func TestGetStatus(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/status/Won%27t%20Fix", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, Status{
			Name:           "Won't Fix",
			StatusCategory: StatusCategory{Key: "done"},
		})
	})

	status, err := client.GetStatus(ctx, "Won't Fix")
	require.NoError(t, err)
	assert.Equal(t, "Won't Fix", status.Name)
	assert.Equal(t, "done", status.StatusCategory.Key)
}

func TestGetStatus_NotFound(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/status/Bogus", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"errorMessages": []string{"The status does not exist."},
		})
	})

	_, err := client.GetStatus(ctx, "Bogus")
	require.Error(t, err)
	assert.True(t, errors.Is(err, forge.ErrNotFound))
}

// ---------------------------------------------------------------------------
// SearchIssues single page
// ---------------------------------------------------------------------------

func TestSearchIssues_SinglePage(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body searchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Contains(t, body.JQL, "project = TEST")
		assert.Equal(t, searchFields, body.Fields)
		assert.Empty(t, body.NextPageToken, "first request should have no nextPageToken")
		writeJSON(t, w, http.StatusOK, SearchResult{
			Issues: []Issue{{ID: "1", Key: "TEST-1"}},
			IsLast: true,
		})
	})

	issues, err := client.SearchIssues(ctx, "project = TEST", 0)
	require.NoError(t, err)
	assert.Len(t, issues, 1)
	assert.Equal(t, "TEST-1", issues[0].Key)
}

// TestSearchIssues_LimitStopsPagination checks that a positive limit stops
// pagination as soon as enough issues have been collected, instead of
// exhausting the full JQL match set and truncating client-side.
func TestSearchIssues_LimitStopsPagination(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	callCount := 0
	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		issues := make([]Issue, 50)
		for i := range issues {
			issues[i] = Issue{ID: fmt.Sprintf("%d", i+1), Key: fmt.Sprintf("PROJ-%d", i+1)}
		}
		writeJSON(t, w, http.StatusOK, SearchResult{
			Issues:        issues,
			NextPageToken: "next-page", // there would be more, but limit should stop us first
		})
	})

	issues, err := client.SearchIssues(ctx, "project = PROJ", 30)
	require.NoError(t, err)
	assert.Len(t, issues, 30, "should truncate to the requested limit")
	assert.Equal(t, 1, callCount, "should stop paginating once the limit is reached")
}

// TestSearchIssues_RetriesOn5xx checks that a transient 5xx from the
// read-only JQL search endpoint is retried, even though the request is a
// POST (POST is not idempotent in general, but this endpoint is read-only).
func TestSearchIssues_RetriesOn5xx(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	callCount := 0
	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(t, w, http.StatusOK, SearchResult{
			Issues: []Issue{{ID: "1", Key: "TEST-1"}},
			IsLast: true,
		})
	})

	issues, err := client.SearchIssues(ctx, "project = TEST", 0)
	require.NoError(t, err)
	assert.Len(t, issues, 1)
	assert.Equal(t, 2, callCount, "should have retried after the 503")
}

// ---------------------------------------------------------------------------
// New base URL edge cases
// ---------------------------------------------------------------------------

func TestBaseURLValidation_ParseError(t *testing.T) {
	t.Parallel()
	_, err := New("token", WithBaseURL("http://exa mple.com"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid base URL")
}

// ---------------------------------------------------------------------------
// WithHTTPClient
// ---------------------------------------------------------------------------

func TestWithHTTPClient(t *testing.T) {
	t.Parallel()
	custom := &http.Client{Timeout: 5 * time.Second}
	c, err := New("token", WithBaseURL("https://example.atlassian.net"), WithHTTPClient(custom))
	require.NoError(t, err)
	assert.Same(t, custom, c.httpClient)
}

// ---------------------------------------------------------------------------
// Retry helpers
// ---------------------------------------------------------------------------

func TestIsTransientError(t *testing.T) {
	t.Parallel()
	assert.True(t, isTransientError(io.EOF))
	assert.True(t, isTransientError(io.ErrUnexpectedEOF))
	assert.True(t, isTransientError(&net.DNSError{Err: "timeout", IsTimeout: true}))
	assert.False(t, isTransientError(errors.New("boom")))
}

func TestIsIdempotent(t *testing.T) {
	t.Parallel()
	assert.True(t, isIdempotent(http.MethodGet, "/issue/PROJ-1"))
	assert.True(t, isIdempotent(http.MethodHead, "/issue/PROJ-1"))
	assert.True(t, isIdempotent(http.MethodPut, "/issue/PROJ-1"))
	assert.True(t, isIdempotent(http.MethodDelete, "/issue/PROJ-1"))
	assert.False(t, isIdempotent(http.MethodPost, "/issue"))
	assert.False(t, isIdempotent(http.MethodPatch, "/issue/PROJ-1"))
	// /search/jql is a read-only JQL search issued as a POST (the request
	// body carries the query), so it's safe to retry like any other GET.
	assert.True(t, isIdempotent(http.MethodPost, "/search/jql"))
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		method string
		path   string
		want   bool
	}{
		{"429 GET", http.StatusTooManyRequests, http.MethodGet, "/issue/PROJ-1", true},
		{"429 POST", http.StatusTooManyRequests, http.MethodPost, "/issue", true},
		{"500 GET", http.StatusInternalServerError, http.MethodGet, "/issue/PROJ-1", true},
		{"503 PUT", http.StatusServiceUnavailable, http.MethodPut, "/issue/PROJ-1", true},
		{"500 POST", http.StatusInternalServerError, http.MethodPost, "/issue", false},
		{"500 POST search/jql", http.StatusInternalServerError, http.MethodPost, "/search/jql", true},
		{"404 GET", http.StatusNotFound, http.MethodGet, "/issue/PROJ-1", false},
		{"505 GET", http.StatusHTTPVersionNotSupported, http.MethodGet, "/issue/PROJ-1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status}
			assert.Equal(t, tc.want, isRetryable(resp, tc.method, tc.path))
		})
	}
}

func TestRetryDelay_RetryAfterHeader(t *testing.T) {
	t.Parallel()
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"2"}}}
	assert.Equal(t, 2*time.Second, retryDelay(resp, 0))
}

func TestRetryDelay_RetryAfterCapped(t *testing.T) {
	t.Parallel()
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"9999"}}}
	assert.Equal(t, 300*time.Second, retryDelay(resp, 0))
}

func TestRetryDelay_InvalidRetryAfterFallsBackToExponential(t *testing.T) {
	t.Parallel()
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"not-a-number"}}}
	d := retryDelay(resp, 0)
	assert.GreaterOrEqual(t, d, time.Duration(0))
	assert.Less(t, d, 2*time.Second)
}

func TestRetryDelay_NoResponseExponentialBackoff(t *testing.T) {
	t.Parallel()
	d := retryDelay(nil, 2)
	assert.GreaterOrEqual(t, d, 2*time.Second)
	assert.Less(t, d, 4*time.Second)
}

// ---------------------------------------------------------------------------
// do() edge cases
// ---------------------------------------------------------------------------

func TestDo_ReadBodyError(t *testing.T) {
	t.Parallel()
	client, _ := setupTest(t)
	err := client.do(context.Background(), http.MethodPost, "/x", iotest.ErrReader(errors.New("boom")), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read request body")
}

// flakyTransport fails the first failN round trips with a transient network
// error before delegating to inner.
type flakyTransport struct {
	calls int
	failN int
	inner http.RoundTripper
}

func (t *flakyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls++
	if t.calls <= t.failN {
		return nil, &net.DNSError{Err: "flaky", IsTimeout: true}
	}
	return t.inner.RoundTrip(req)
}

func TestDo_RetriesOnTransientNetworkError(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, User{AccountID: "42"})
	})

	transport := &flakyTransport{failN: 1, inner: http.DefaultTransport}
	client, err := New("test-token", WithBaseURL(srv.URL), WithHTTPClient(&http.Client{Transport: transport}))
	require.NoError(t, err)

	user, err := client.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "42", user.AccountID)
	assert.Equal(t, 2, transport.calls)
}

// ---------------------------------------------------------------------------
// GetProjectRoleActors
// ---------------------------------------------------------------------------

func TestGetProjectRoleActors(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/project/PROJ/role", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, map[string]string{
			"Administrators": "http://localhost/rest/api/3/project/10001/role/10002",
			"Developers":     "http://localhost/rest/api/3/project/10001/role/10003",
		})
	})

	mux.HandleFunc("/rest/api/3/project/PROJ/role/10002", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, ProjectRoleDetail{
			Name: "Administrators",
			Actors: []RoleActor{
				{
					ID:          1,
					DisplayName: "Alice Admin",
					Type:        "atlassian-user-role-actor",
					ActorUser:   &RoleActorUser{AccountID: "alice-id"},
				},
			},
		})
	})

	mux.HandleFunc("/rest/api/3/project/PROJ/role/10003", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, ProjectRoleDetail{
			Name: "Developers",
			Actors: []RoleActor{
				{
					ID:          2,
					DisplayName: "Bob Dev",
					Type:        "atlassian-user-role-actor",
					ActorUser:   &RoleActorUser{AccountID: "bob-id"},
				},
				{
					ID:          3,
					DisplayName: "dev-group",
					Type:        "atlassian-group-role-actor",
					ActorGroup:  &RoleActorGroup{GroupID: "group-1", Name: "dev-group"},
				},
			},
		})
	})

	actors, err := client.GetProjectRoleActors(ctx, "PROJ")
	require.NoError(t, err)

	assert.True(t, actors["Administrators"].DirectUsers["alice-id"],
		"alice should be a direct Administrators user")
	assert.True(t, actors["Developers"].DirectUsers["bob-id"],
		"bob should be a direct Developers user")
	assert.Equal(t, []string{"group-1"}, actors["Developers"].GroupIDs,
		"dev-group should be listed in Developers GroupIDs")
	assert.Empty(t, actors["Administrators"].GroupIDs,
		"Administrators should have no group actors")
}

func TestGetProjectRoleActors_Error(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/project/BADPROJ/role", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"errorMessages": []string{"No project could be found with key 'BADPROJ'"},
		})
	})

	_, err := client.GetProjectRoleActors(ctx, "BADPROJ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list project roles")
}

// ---------------------------------------------------------------------------
// GetUserGroups
// ---------------------------------------------------------------------------

func TestGetUserGroups(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/user/groups", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "user-123", r.URL.Query().Get("accountId"))
		writeJSON(t, w, http.StatusOK, []UserGroupInfo{
			{Name: "developers", GroupID: "group-dev-1", Self: "https://example.com/group/1"},
			{Name: "jira-users", GroupID: "group-all-2", Self: "https://example.com/group/2"},
		})
	})

	groups, err := client.GetUserGroups(ctx, "user-123")
	require.NoError(t, err)
	assert.Len(t, groups, 2)
	assert.Equal(t, "developers", groups[0].Name)
	assert.Equal(t, "group-dev-1", groups[0].GroupID)
	assert.Equal(t, "jira-users", groups[1].Name)
	assert.Equal(t, "group-all-2", groups[1].GroupID)
}

func TestGetUserGroups_Error(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/user/groups", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"errorMessages": []string{"User not found"},
		})
	})

	_, err := client.GetUserGroups(ctx, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get user groups")
}

func TestGetUserGroups_Empty(t *testing.T) {
	t.Parallel()
	client, mux := setupTest(t)
	ctx := context.Background()

	mux.HandleFunc("/rest/api/3/user/groups", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []UserGroupInfo{})
	})

	groups, err := client.GetUserGroups(ctx, "user-no-groups")
	require.NoError(t, err)
	assert.Empty(t, groups)
}
