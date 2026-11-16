package forgejo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestCommitFilesToBranchAndReview(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "r", "full_name": "o/r", "default_branch": "main", "owner": map[string]any{"login": "o"}})
	})
	mux.HandleFunc("/api/v1/repos/o/r/contents/a.txt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"content": map[string]any{"path": "a.txt"}})
	})
	mux.HandleFunc("/api/v1/repos/o/r/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 9})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 9, "state": "APPROVED", "user": map[string]any{"login": "u"}, "body": "ok"}})
	})
	mux.HandleFunc("/api/v1/repos/o/r/branch_protections/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"branch_name": "main", "enable_push": false})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New("t", WithBaseURL(srv.URL+"/api/v1"))
	ok, err := c.CommitFilesToBranch(context.Background(), "o", "r", "main", "msg", []forge.TreeFile{{Path: "a.txt", Content: []byte("hi")}})
	if err != nil || !ok {
		t.Fatalf("commit: ok=%v err=%v", ok, err)
	}
	if err := c.CreatePullRequestReview(context.Background(), "o", "r", 1, "APPROVE", "lgtm", "", nil); err != nil {
		t.Fatal(err)
	}
	revs, err := c.ListPullRequestReviews(context.Background(), "o", "r", 1)
	if err != nil || len(revs) != 1 {
		t.Fatalf("reviews=%v err=%v", revs, err)
	}
	prot, err := c.IsProtectedBranch(context.Background(), "o", "r", "main")
	if err != nil || !prot {
		t.Fatalf("prot=%v err=%v", prot, err)
	}
}
