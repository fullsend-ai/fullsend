package cli

import (
	"context"
	"github.com/fullsend-ai/fullsend/internal/gitlablifecycle"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
)

// Exercise the real cleanup deadline rather than introducing a production
// timeout override solely for tests. Attribution succeeds before inventory
// spends its complete budget; deleting that credential must remain independent.
func TestContainPoller_InventoryDeadlineStillDeletesAttributedSecret(t *testing.T) {
	mux := http.NewServeMux()
	var deleted, timedOut atomic.Bool
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeTestJSON(t, w, http.StatusOK, map[string]any{"value": "runtime-test-token"})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		timedOut.Store(true)
	})
	for _, path := range []string{"/api/v4/projects/g%2Fp/triggers", "/api/v4/users/77/keys", "/api/v4/projects/g%2Fp/jobs", "/api/v4/projects/g%2Fp/pipeline_schedules"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, http.StatusOK, []map[string]any{})
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("installer-test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	err = gitlablifecycle.NewTriggerOwner(admin).ContainPoller(context.Background(), "g", "p", 77)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.True(t, timedOut.Load(), "inventory spent its deadline")
	assert.True(t, deleted.Load(), "secret deletion receives a fresh live context")
}
