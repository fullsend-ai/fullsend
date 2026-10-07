package cli

import (
	"context"
	"encoding/json"
	"github.com/fullsend-ai/fullsend/internal/gitlablifecycle"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

func TestRenamedPollerRecovery_UsesOwnershipPreservesSuppliedExclusions(t *testing.T) {
	for _, operation := range []string{"install", "uninstall"} {
		for _, ownership := range []string{"managed", "excluded", "unrecorded"} {
			t.Run(operation+"/"+ownership, func(t *testing.T) {
				state := `{"roles":{"poller":{"managed_user_id":77,"incoming_id":7}}}`
				if ownership == "excluded" {
					state = `{"roles":{"poller":{"managed_user_id":77,"incoming_id":7,"excluded_user_ids":[77]}}}`
				} else if ownership == "unrecorded" {
					state = `{"roles":{}}`
				}
				var mu sync.Mutex
				levels := map[int64]int{77: forge.GitLabAccessLevelMaintainer}
				var revoked, updated bool
				mux := http.NewServeMux()
				handleRotationState(t, mux, "g%2Fp", state)
				mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
					writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 77, "name": "renamed-poller"}})
				})
				mux.HandleFunc("/api/v4/projects/g%2Fp/members/77", func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						AccessLevel int `json:"access_level"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					mu.Lock()
					defer mu.Unlock()
					levels[77] = body.AccessLevel
					updated = true
					writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77})
				})
				mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 8, "name": "fullsend-poller-bootstrap", "active": !revoked, "revoked": revoked}})
				})
				mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens/8", func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					revoked = true
					w.WriteHeader(http.StatusNoContent)
				})
				srv := httptest.NewServer(mux)
				defer srv.Close()
				admin, err := gitlab.New("installer-test-token", gitlab.WithBaseURL(srv.URL))
				require.NoError(t, err)
				client := memberLevelClient{FakeClient: forge.NewFakeClient(), mu: &mu, levels: levels}
				if operation == "install" {
					_, err = repos.ReconcileGitLabPollerElevation(context.Background(), client, gitlablifecycle.NewTriggerOwner(admin), "g", "p", false)
				} else {
					err = newGitLabUninstallTokenClient(admin).ReconcileGitLabPollersForUninstall(context.Background(), client, "g", "p")
				}
				require.NoError(t, err)
				mu.Lock()
				defer mu.Unlock()
				assert.Equal(t, ownership == "managed", updated)
				assert.Equal(t, ownership == "managed", revoked)
				if ownership == "managed" {
					assert.Equal(t, forge.GitLabAccessLevelDeveloper, levels[77])
				} else {
					assert.Equal(t, forge.GitLabAccessLevelMaintainer, levels[77])
				}
			})
		}
	}
}

func TestRenamedPollerRecovery_ForbiddenInventoryCannotEstablishAbsence(t *testing.T) {
	mux := http.NewServeMux()
	handleRotationState(t, mux, "g%2Fp", `{"roles":{"poller":{"managed_user_id":77,"incoming_id":7}}}`)
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusForbidden, map[string]any{"message": "denied"})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/members/all", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 77, "name": "renamed-poller", "access_level": 40}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("installer-test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	_, err = gitlablifecycle.NewTriggerOwner(admin).ManagedPollerUserIDs(context.Background(), "g", "p")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrForbidden)
	assert.NotErrorIs(t, err, forge.ErrNotFound)
}
