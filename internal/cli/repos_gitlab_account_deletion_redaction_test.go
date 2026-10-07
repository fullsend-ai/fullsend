package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

func TestGitLabAccountDeletionInventory_WithholdsRemoteErrorsPreservesPolicy(t *testing.T) {
	const installerToken = "installer redaction fixture credential"
	paths := []string{
		"/api/v4/users/77/keys",
		"/api/v4/projects/g%2Fp/jobs",
		"/api/v4/projects/g%2Fp/pipeline_schedules",
		"/api/v4/projects/g%2Fp/triggers",
	}
	for _, failedPath := range append(paths, "") {
		name := failedPath
		if name == "" {
			name = "local policy diagnostic"
		}
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			for _, path := range paths {
				mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
					if path == failedPath {
						writeTestJSON(t, w, http.StatusForbidden, map[string]any{"message": installerToken})
						return
					}
					if failedPath == "" && path == paths[0] {
						writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 9, "usage_type": "auth"}})
						return
					}
					writeTestJSON(t, w, http.StatusOK, []map[string]any{})
				})
			}
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New(installerToken, gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			// Uninstall reuses the same deletion callback as install's client.
			callbacks := map[string]func(context.Context, string, string, int) error{
				"install":   newGitLabRoleTokenClient(admin).(repos.ServiceAccountTokenClient).VerifyAccountDeletion,
				"uninstall": newGitLabUninstallTokenClient(admin).VerifyAccountDeletion,
			}
			for clientName, callback := range callbacks {
				t.Run(clientName, func(t *testing.T) {
					err := callback(context.Background(), "g", "p", 77)
					require.Error(t, err)
					assert.NotContains(t, err.Error(), installerToken)
					if failedPath == "" {
						assert.EqualError(t, err, "service account 77 has SSH credentials, unfinished jobs or owned schedules/triggers; not deleted")
						return
					}
					assert.ErrorIs(t, err, forge.ErrForbidden)
					var apiErr *gitlab.APIError
					assert.ErrorAs(t, err, &apiErr)
					assert.Contains(t, err.Error(), "server error text withheld")
				})
			}
		})
	}
}
