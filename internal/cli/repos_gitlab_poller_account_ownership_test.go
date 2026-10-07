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
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// A same-named administrator account is not adopted when neither a secret nor
// durable creation provenance exists. No membership or PAT endpoint may be
// used, even when reconciliation is recovering interrupted installations.
func TestGitLabPollerOwnership_AbsentSecretPreservesUnrecordedAccount(t *testing.T) {
	for name, state := range map[string]string{
		"empty state":                `{"roles":{}}`,
		"token evidence alone":       `{"roles":{"poller":{"phase":"idle","incoming_id":5}}}`,
		"another recorded account":   `{"roles":{"poller":{"managed_user_id":90}}}`,
		"installed unowned identity": `{"roles":{"poller":{"managed_user_id":90,"incoming_id":5}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var destructiveCalls atomic.Int32
			mux := http.NewServeMux()
			handleRotationState(t, mux, "g%2Fp", state)
			if name == "installed unowned identity" {
				mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
					writeTestJSON(t, w, http.StatusOK, map[string]any{"value": "glpat-administrator-token"})
				})
				mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
					writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77})
				})
			}
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 77, "name": "fullsend-poller"}})
			})
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					destructiveCalls.Add(1)
				}
				w.WriteHeader(http.StatusNotFound)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			owner := gitlablifecycle.NewTriggerOwner(admin)
			ctx := context.Background()
			ids, err := owner.ManagedPollerUserIDs(ctx, "g", "p")
			require.ErrorIs(t, err, forge.ErrNotFound)
			assert.Empty(t, ids)
			_, err = owner.PollerUserID(ctx, "g", "p")
			require.ErrorIs(t, err, forge.ErrNotFound)
			fc := forge.NewFakeClient()
			fc.ProjectMemberAccess[77] = forge.GitLabAccessLevelMaintainer
			_, err = repos.ReconcileGitLabPollerElevation(ctx, fc, owner, "g", "p", false)
			require.NoError(t, err)
			require.NoError(t, newGitLabUninstallTokenClient(admin).ReconcileGitLabPollersForUninstall(ctx, fc, "g", "p"))
			assert.Equal(t, forge.GitLabAccessLevelMaintainer, fc.ProjectMemberAccess[77])
			assert.Zero(t, destructiveCalls.Load(), "administrator membership and PATs must remain untouched")
		})
	}
}

func TestGitLabPollerOwnership_SelectsOnlyRecordedAccount(t *testing.T) {
	for name, state := range map[string]string{
		"recorded creation":       `{"roles":{"poller":{"managed_user_id":77}}}`,
		"supplied exclusion wins": `{"roles":{"poller":{"managed_user_id":77,"excluded_user_ids":[77]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			handleRotationState(t, mux, "g%2Fp", state)
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 77, "name": "fullsend-poller"}, {"id": 90, "name": "fullsend-poller"}})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			ids, err := gitlablifecycle.NewTriggerOwner(admin).ManagedPollerUserIDs(context.Background(), "g", "p")
			if name == "supplied exclusion wins" {
				require.ErrorIs(t, err, forge.ErrNotFound)
				assert.Empty(t, ids)
			} else {
				require.NoError(t, err)
				assert.Equal(t, []int64{77}, ids)
			}
		})
	}
}
