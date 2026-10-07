package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

func TestGitLabRoleProvenanceDiagnostics_WithholdUnderlyingSensitiveText(t *testing.T) {
	const sensitive = "sensitive diagnostic fixture value"
	for _, parseFailure := range []bool{true, false} {
		t.Run(fmt.Sprintf("parse failure=%t", parseFailure), func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.VarGitLabRoleRegistry, func(w http.ResponseWriter, r *http.Request) {
				raw := ""
				if parseFailure {
					raw = fmt.Sprintf(`{"roles":[{"name":%q}]}`, sensitive)
				}
				writeTestJSON(t, w, http.StatusOK, map[string]any{"value": raw})
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabAnalystToken, func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, map[string]any{"value": sensitive})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			o := newGitLabPollerTriggerOwner(admin)
			o.NewClient = func(string, string) (*gitlab.LiveClient, error) {
				return nil, fmt.Errorf("%w: %s", forge.ErrForbidden, sensitive)
			}
			if parseFailure {
				err = o.RefuseUnknownRoleProvenance(context.Background(), "g", "p", nil)
				require.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)
				assert.Contains(t, err.Error(), "server error text withheld")
				assert.NotContains(t, err.Error(), sensitive)
			}
			_, err = o.AttributeSuppliedRole(context.Background(), "g", "p", gitlabroles.RoleAnalyst)
			require.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)
			assert.Contains(t, err.Error(), "server error text withheld")
			assert.NotContains(t, err.Error(), sensitive)
		})
	}
}
