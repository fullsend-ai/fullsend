package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

type accountDeletionOrderClient struct {
	*forge.FakeClient
	t *testing.T
}

func (c accountDeletionOrderClient) DeleteProjectServiceAccount(ctx context.Context, owner, repo string, id int) error {
	assert.True(c.t, c.VariablesExist[owner+"/"+repo+"/"+forge.VarGitLabRoleRotation], "ownership must survive until deletion")
	return c.FakeClient.DeleteProjectServiceAccount(ctx, owner, repo, id)
}

func TestGitLabUninstallWrapper_ManagedAccountCleanup(t *testing.T) {
	for _, mode := range []string{"ok", "ssh", "active credential", "resource inventory failure", "delete failure"} {
		t.Run(mode, func(t *testing.T) {
			mux := http.NewServeMux()
			handleRotationState(t, mux, "g%2Fp", `{"roles":{"coder":{"phase":"idle","incoming_id":7,"managed_user_id":77}}}`)
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 77, "name": "fullsend-coder"}})
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
				items := []map[string]any{}
				if mode == "active credential" {
					items = append(items, map[string]any{"id": 9, "name": "administrator-extra", "active": true})
				}
				writeTestJSON(t, w, http.StatusOK, items)
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/access_tokens", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, []map[string]any{})
			})
			resourceReads := 0
			for _, path := range []string{"/api/v4/users/77/keys", "/api/v4/projects/g%2Fp/jobs", "/api/v4/projects/g%2Fp/pipeline_schedules", "/api/v4/projects/g%2Fp/triggers"} {
				mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
					resourceReads++
					if mode == "resource inventory failure" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					items := []map[string]any{}
					if mode == "ssh" && path == "/api/v4/users/77/keys" {
						items = append(items, map[string]any{"id": 1, "usage_type": "auth"})
					}
					writeTestJSON(t, w, http.StatusOK, items)
				})
			}
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("installer-test-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			wrapper := newGitLabUninstallTokenClient(admin)
			// Reconciliation caches project-wide supplied exclusions before cleanup.
			wrapper.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return nil, nil }
			require.NotNil(t, wrapper.VerifyAccountDeletion)
			fc := forge.NewFakeClient()
			key := "g/p/" + forge.VarGitLabRoleRotation
			const state = `{"roles":{"coder":{"phase":"idle","incoming_id":7,"managed_user_id":77}}}`
			fc.VariablesExist[key] = true
			fc.VariableValues[key] = state
			if mode == "delete failure" {
				fc.Errors["DeleteProjectServiceAccount"] = errors.New("deletion refused")
			}
			result, err := repos.CleanupGitLabRoleIdentity(context.Background(), repos.GitLabRoleCleanupConfig{
				Owner: "g", Repo: "p", Client: accountDeletionOrderClient{FakeClient: fc, t: t}, Tokens: wrapper,
			})
			if mode == "ok" {
				require.NoError(t, err)
				assert.Equal(t, 1, result.AccountsDeleted)
				assert.Equal(t, []int{77}, fc.DeletedProjectServiceAccounts)
				assert.False(t, fc.VariablesExist[key], "ownership is retired after account deletion")
			} else {
				require.Error(t, err)
				assert.Zero(t, result.AccountsDeleted)
				assert.Empty(t, fc.DeletedProjectServiceAccounts)
				assert.True(t, fc.VariablesExist[key])
				assert.Equal(t, state, fc.VariableValues[key], "failed cleanup retains complete provenance")
			}
			if mode != "active credential" {
				assert.Equal(t, 4, resourceReads, "the real CLI wrapper verifies remaining account resources")
			}
		})
	}
}
