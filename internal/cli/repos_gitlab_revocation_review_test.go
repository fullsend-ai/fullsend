package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPollerCredentialsRevoked_RejectsActiveManagedNames(t *testing.T) {
	for _, name := range []string{gitlabroles.PollerTokenName, gitlabroles.PollerBootstrapTokenName, "unrelated"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
				calls++
				writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 99, "name": name, "active": true}})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			err = newGitLabPollerTriggerOwner(admin).VerifyPollerCredentialsRevoked(context.Background(), "g", "p", 77)
			var unsafe *repos.PollerElevationUnsafeError
			require.ErrorAs(t, err, &unsafe)
			assert.Contains(t, unsafe.Reason, "after revocation")
			assert.Equal(t, 1, calls)
		})
	}
}

func TestPollerCredentialsRevoked_EmptyInventoryPasses(t *testing.T) {
	calls := map[string]int{}
	mux := http.NewServeMux()
	for _, path := range []string{
		"/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens",
		"/api/v4/projects/g%2Fp/triggers", "/api/v4/users/77/keys",
		"/api/v4/projects/g%2Fp/jobs", "/api/v4/projects/g%2Fp/pipeline_schedules",
	} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			calls[path]++
			writeTestJSON(t, w, http.StatusOK, []map[string]any{})
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	require.NoError(t, newGitLabPollerTriggerOwner(admin).VerifyPollerCredentialsRevoked(context.Background(), "g", "p", 77))
	require.Len(t, calls, 5)
	for _, count := range calls {
		assert.Equal(t, 1, count)
	}
}
