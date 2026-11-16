package forgejo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestDetectCompileTimeInterface(t *testing.T) {
	var _ forge.Client = New("tok")
}

func TestGetRepoAndUser(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "login": "mrgeek", "full_name": "Mr Geek", "email": "a@b.c"})
	})
	mux.HandleFunc("/api/v1/repos/forgeops-ai/forge-platform", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 9, "name": "forge-platform", "full_name": "forgeops-ai/forge-platform",
			"private": true, "default_branch": "main",
			"owner": map[string]any{"login": "forgeops-ai"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New("test-token", WithBaseURL(srv.URL+"/api/v1"))
	login, err := c.GetAuthenticatedUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if login != "mrgeek" {
		t.Fatalf("login=%q", login)
	}
	repo, err := c.GetRepo(context.Background(), "forgeops-ai", "forge-platform")
	if err != nil {
		t.Fatal(err)
	}
	if repo.Name != "forge-platform" || !repo.Private {
		t.Fatalf("repo=%+v", repo)
	}
}

func TestUnsupportedStub(t *testing.T) {
	c := New("x", WithBaseURL("http://127.0.0.1:9/api/v1"))
	_, err := c.CreatePipeline(context.Background(), "o", "r", "main", nil)
	if !forge.IsNotSupported(err) {
		t.Fatalf("want ErrNotSupported, got %v", err)
	}
}
