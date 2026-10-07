package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServiceAccountCredentialValidation(t *testing.T) {
	for _, mode := range []string{"ok", "scope", "identity", "inactive", "auth", "membership", "level"} {
		t.Run(mode, func(t *testing.T) {
			calls := map[string]int{}
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
				calls["self"]++
				assert.Equal(t, "new-role-token", r.Header.Get("PRIVATE-TOKEN"))
				if mode == "auth" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				uid, scopes, active := 77, []string{"api"}, true
				if mode == "scope" {
					scopes = []string{"read_api"}
				}
				if mode == "identity" {
					uid = 88
				}
				if mode == "inactive" {
					active = false
				}
				writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 9, "user_id": uid, "active": active, "scopes": scopes})
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/members/all/77", func(w http.ResponseWriter, r *http.Request) {
				calls["member"]++
				if mode == "membership" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				level := 30
				if mode == "level" {
					level = 40
				}
				writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77, "access_level": level})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			c := newGitLabRoleTokenClient(admin)
			err = c.VerifyToken(context.Background(), "g", "p", &repos.ProjectAccessToken{ID: 9, UserID: 77, Token: "new-role-token"})
			assert.Equal(t, mode != "ok", err != nil)
			assert.Equal(t, 1, calls["self"])
			if mode == "ok" || mode == "membership" || mode == "level" {
				assert.Equal(t, 1, calls["member"])
			}
		})
	}
}

func TestServiceAccountDeletionResourceSafety(t *testing.T) {
	paths := []string{"/api/v4/users/77/keys", "/api/v4/projects/g%2Fp/jobs", "/api/v4/projects/g%2Fp/pipeline_schedules", "/api/v4/projects/g%2Fp/triggers"}
	for _, mode := range []string{"ok", "keys", "jobs", "schedules", "triggers", "inventory"} {
		t.Run(mode, func(t *testing.T) {
			calls := map[string]int{}
			mux := http.NewServeMux()
			for i, path := range paths {
				mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
					calls[path]++
					if mode == "inventory" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					items := []map[string]any{}
					if i == 0 && mode == "keys" {
						items = []map[string]any{{"id": 1, "usage_type": "auth"}}
					}
					if i == 1 && mode == "jobs" {
						items = []map[string]any{{"id": 1, "user": map[string]any{"id": 77}}}
					}
					if i == 2 && mode == "schedules" {
						items = []map[string]any{{"id": 1, "owner": map[string]any{"id": 77}}}
					}
					if i == 3 && mode == "triggers" {
						items = []map[string]any{{"id": 1, "owner": map[string]any{"id": 77}}}
					}
					writeTestJSON(t, w, http.StatusOK, items)
				})
			}
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			c := newGitLabRoleTokenClient(admin)
			err = c.VerifyAccountDeletion(context.Background(), "g", "p", 77)
			assert.Equal(t, mode != "ok", err != nil)
			for _, path := range paths {
				assert.Equal(t, 1, calls[path])
			}
		})
	}
}
