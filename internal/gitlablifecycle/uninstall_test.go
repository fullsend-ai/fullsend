package gitlablifecycle

import (
	"context"
	"encoding/json"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type uninstallLegacyInventory struct{ tokens []repos.ProjectAccessToken }

func (a uninstallLegacyInventory) ListProjectAccessTokens(context.Context, string, string) ([]repos.ProjectAccessToken, error) {
	return a.tokens, nil
}
func (a uninstallLegacyInventory) CreateProjectAccessToken(context.Context, string, string, string, []string, int, string) (*repos.ProjectAccessToken, error) {
	panic("unexpected creation")
}
func (a uninstallLegacyInventory) RevokeProjectAccessToken(context.Context, string, string, int) error {
	panic("unexpected revocation")
}

func TestUninstallCachesResolvedExclusionsBeforeCredentialRemoval(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "resolved", true: "unresolved"}[failed], func(t *testing.T) {
			ctx := context.Background()
			var reads atomic.Int64
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
			mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.VarGitLabRoleRotation, func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				if failed {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"value": `{"roles":{"poller":{"supplied":true,"supplied_user_id":501,"supplied_token_id":1,"phase":"idle"}}}`})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("test-installer", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			role := repos.ServiceAccountTokenClient{Legacy: uninstallLegacyInventory{tokens: []repos.ProjectAccessToken{{ID: 1, Name: "fullsend-poller", UserID: 501, Active: true}, {ID: 2, Name: "fullsend-coder", UserID: 502, Active: true}}}, ManagedLegacyTokenIDs: func(context.Context, string, string) ([]int, error) { return []int{1, 2}, nil }}
			u := NewUninstallTokenClient(admin, role)
			fc := forge.NewFakeClient()
			err = u.ReconcileGitLabPollersForUninstall(ctx, fc, "g", "p")
			if failed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				value, _, err := fc.GetRepoVariable(ctx, "g", "p", forge.VarGitLabRoleRotation)
				require.NoError(t, err)
				assert.Contains(t, value, "501")
			}
			initialReads := reads.Load()
			for _, strict := range []bool{false, true} {
				var tokens []repos.ProjectAccessToken
				var err error
				if strict {
					tokens, err = u.ListProjectAccessTokensStrict(ctx, "g", "p")
				} else {
					tokens, err = u.ListProjectAccessTokens(ctx, "g", "p")
				}
				if failed {
					require.Error(t, err)
					assert.Empty(t, tokens)
				} else {
					require.NoError(t, err)
					require.Len(t, tokens, 1)
					assert.Equal(t, 2, tokens[0].ID)
				}
			}
			assert.Equal(t, initialReads, reads.Load(), "inventories must use cached ownership after credential removal")
			if err != nil {
				assert.NotContains(t, err.Error(), "test-installer")
			}
		})
	}
}
