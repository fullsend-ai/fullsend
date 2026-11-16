package forgejo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestReactionsAndWorkflowRuns(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/o/r/issues/1/reactions", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": "+1", "user": map[string]any{"login": "alice"},
			})
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"content": "+1", "user": map[string]any{"login": "alice"},
			}})
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/repos/o/r/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": 1,
			"workflow_runs": []map[string]any{{
				"id": 7, "title": "CI", "event": "push", "status": "success",
				"html_url": "https://example/run/7", "created": time.Now().UTC().Format(time.RFC3339),
				"workflow_id": "ci.yaml",
			}},
		})
	})
	mux.HandleFunc("/api/v1/repos/o/r/actions/workflows/ci.yaml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/repos/o/r/pulls/3", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"updated_at": "2026-01-02T03:04:05Z",
			"user":       map[string]any{"login": "bob"},
			"labels":     []map[string]any{{"name": "ok"}},
		})
	})
	mux.HandleFunc("/api/v1/repos/o/r/pulls/3/reviews", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": 9, "body": "LGTM", "state": "APPROVED",
			"user": map[string]any{"login": "rev"}, "submitted_at": "2026-01-02T03:04:05Z",
		}})
	})
	mux.HandleFunc("/api/v1/repos/o/r/pulls/3/reviews/9/comments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": 11, "body": "nit", "path": "a.go", "position": 4,
			"created_at": "2026-01-02T03:04:05Z",
			"user":       map[string]any{"login": "rev"},
		}})
	})
	mux.HandleFunc("/api/v1/repos/o/r/actions/variables/FOO", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "FOO", "data": "bar"})
		case http.MethodPut, http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("/api/v1/repos/o/r/actions/variables", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "FOO", "data": "bar"}})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New("tok", WithBaseURL(srv.URL))

	id, err := c.AddIssueReaction(context.Background(), "o", "r", 1, "+1")
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected synthetic reaction id")
	}
	rs, err := c.ListIssueReactions(context.Background(), "o", "r", 1)
	if err != nil || len(rs) != 1 || rs[0].Content != "+1" {
		t.Fatalf("reactions=%v err=%v", rs, err)
	}
	if err := c.DeleteIssueReaction(context.Background(), "o", "r", 1, id); err != nil {
		t.Fatal(err)
	}

	runs, err := c.ListWorkflowRuns(context.Background(), "o", "r", "ci.yaml")
	if err != nil || len(runs) != 1 || runs[0].Conclusion != "success" {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
	if err := c.DispatchWorkflow(context.Background(), "o", "r", "ci.yaml", "main", nil); err != nil {
		t.Fatal(err)
	}

	auth, err := c.GetPullRequestAuthorizationContext(context.Background(), "o", "r", 3)
	if err != nil || auth.AuthorLogin != "bob" || len(auth.Labels) != 1 {
		t.Fatalf("auth=%+v err=%v", auth, err)
	}
	page, err := c.ListPullRequestReviewThreads(context.Background(), "o", "r", 3)
	if err != nil || len(page.Threads) != 1 || page.Threads[0].Path != "a.go" {
		t.Fatalf("threads=%+v err=%v", page, err)
	}

	if err := c.CreateOrUpdateRepoVariable(context.Background(), "o", "r", "FOO", "bar"); err != nil {
		t.Fatal(err)
	}
	val, ok, err := c.GetRepoVariable(context.Background(), "o", "r", "FOO")
	if err != nil || !ok || val != "bar" {
		t.Fatalf("var=%q ok=%v err=%v", val, ok, err)
	}
	vars, err := c.ListRepoVariables(context.Background(), "o", "r")
	if err != nil || vars["FOO"] != "bar" {
		t.Fatalf("vars=%v err=%v", vars, err)
	}
}

func TestGetTokenScopesNil(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "u"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New("tok", WithBaseURL(srv.URL))
	scopes, err := c.GetTokenScopes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scopes != nil {
		t.Fatalf("want nil scopes, got %v", scopes)
	}
	_ = forge.ErrNotSupported // keep import used if trimmed later
}
