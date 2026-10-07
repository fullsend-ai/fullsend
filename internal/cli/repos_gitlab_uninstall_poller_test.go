package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// memberLevelClient reports effective project access from a map the test
// server updates on membership changes.
type memberLevelClient struct {
	*forge.FakeClient
	mu     *sync.Mutex
	levels map[int64]int
}

func (c memberLevelClient) GetProjectMemberAccessLevel(_ context.Context, _, _ string, uid int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	level, ok := c.levels[uid]
	if !ok {
		return 0, forge.ErrNotFound
	}
	return level, nil
}

// Uninstall reconciles the durably managed Poller: leftover Maintainer access
// is restored and verified and bootstrap credentials are revoked. A same-named
// account without a creation record remains untouched.
func TestGitLabUninstallTokenClient_ReconcilesEveryManagedPoller(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	levels := map[int64]int{10: forge.GitLabAccessLevelMaintainer, 20: forge.GitLabAccessLevelDeveloper}
	revoked := map[int]bool{}
	updated := map[int64]bool{}
	mux := http.NewServeMux()
	handleRotationState(t, mux, "g%2Fp", `{"roles":{"poller":{"managed_user_id":10}}}`)
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 10, "name": "fullsend-poller"}, {"id": 20, "name": "fullsend-poller"}, {"id": 30, "name": "fullsend-coder"}})
	})
	for _, uid := range []int{10, 20} {
		uid := uid
		bootstrapID := uid * 10
		mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/"+strconv.Itoa(uid)+"/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			writeTestJSON(t, w, http.StatusOK, []map[string]any{
				{"id": bootstrapID, "name": "fullsend-poller-bootstrap", "active": !revoked[bootstrapID], "revoked": revoked[bootstrapID]}})
		})
		mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/"+strconv.Itoa(uid)+"/personal_access_tokens/"+strconv.Itoa(bootstrapID), func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			revoked[bootstrapID] = true
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/api/v4/projects/g%2Fp/members/"+strconv.Itoa(uid), func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			mu.Lock()
			defer mu.Unlock()
			levels[int64(uid)] = int(body["access_level"].(float64))
			updated[int64(uid)] = true
			writeTestJSON(t, w, http.StatusOK, map[string]any{"id": uid})
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	u := newGitLabUninstallTokenClient(admin)
	client := memberLevelClient{FakeClient: forge.NewFakeClient(), mu: &mu, levels: levels}
	require.NoError(t, u.ReconcileGitLabPollersForUninstall(ctx, client, "g", "p"))

	assert.Equal(t, forge.GitLabAccessLevelDeveloper, levels[10])
	assert.True(t, updated[10])
	assert.False(t, updated[20], "a Poller already at Developer is not modified")
	assert.True(t, revoked[100])
	assert.False(t, revoked[200], "an unrecorded same-named account is preserved")
}

// A service-account listing that cannot establish absence fails the uninstall.
func TestGitLabUninstallTokenClient_ReconcileListingFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		wantErr bool
	}{
		"not found means no service accounts": {status: http.StatusNotFound},
		"forbidden is not proof of absence":   {status: http.StatusForbidden, wantErr: true},
		"server error":                        {status: http.StatusInternalServerError, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			handlerCalled := false
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				writeTestJSON(t, w, tc.status, map[string]any{"message": "x"})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)

			err = newGitLabUninstallTokenClient(admin).ReconcileGitLabPollersForUninstall(context.Background(), forge.NewFakeClient(), "g", "p")
			assert.True(t, handlerCalled)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// A later page of the legacy project-token listing that answers not found
// after earlier pages returned tokens leaves the destructive inventory
// incomplete: uninstall must fail rather than skip the legacy tokens.
func TestGitLabUninstallTokenClient_StrictInventoryFailsOnLaterPageLegacyNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			writeTestJSON(t, w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
			return
		}
		page := make([]map[string]any, 100)
		for i := range page {
			page[i] = map[string]any{"id": i + 1, "name": "fullsend-coder", "active": true}
		}
		writeTestJSON(t, w, http.StatusOK, page)
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	toks, err := newGitLabUninstallTokenClient(admin).ListProjectAccessTokensStrict(context.Background(), "g", "p")
	require.Error(t, err)
	assert.Nil(t, toks)
	assert.ErrorContains(t, err, "incomplete")
}

// suppliedPollerServer models a project whose rotation state records the
// installed Poller credential as administrator-supplied, with two service
// accounts sharing the managed name: 90 (the supplied credential's owner when
// userStatus is 200) and 77 (a genuine managed duplicate). It records
// membership changes and token revocations.
func suppliedPollerServer(t *testing.T, userStatus int) (*gitlab.LiveClient, *sync.Mutex, map[int64]bool, map[int]bool) {
	t.Helper()
	var mu sync.Mutex
	updated := map[int64]bool{}
	revoked := map[int]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabPollerToken, "value": "glpat-supplied"})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.VarGitLabRoleRotation, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.VarGitLabRoleRotation, "value": `{"roles":{"poller":{"phase":"idle","managed_user_id":77,"distributed_at":"2026-01-01T00:00:00Z"}}}`})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		if userStatus != http.StatusOK {
			writeTestJSON(t, w, userStatus, map[string]any{"message": "rejected"})
			return
		}
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 90})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 77, "name": "fullsend-poller"}, {"id": 90, "name": "fullsend-poller"}})
	})
	for _, uid := range []int{77, 90} {
		uid := uid
		runtimeID, bootstrapID := uid*10, uid*10+1
		mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/"+strconv.Itoa(uid)+"/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			writeTestJSON(t, w, http.StatusOK, []map[string]any{
				{"id": runtimeID, "name": "fullsend-poller", "active": !revoked[runtimeID], "revoked": revoked[runtimeID]},
				{"id": bootstrapID, "name": "fullsend-poller-bootstrap", "active": !revoked[bootstrapID], "revoked": revoked[bootstrapID]}})
		})
		for _, id := range []int{runtimeID, bootstrapID} {
			id := id
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/"+strconv.Itoa(uid)+"/personal_access_tokens/"+strconv.Itoa(id), func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				revoked[id] = true
				w.WriteHeader(http.StatusNoContent)
			})
		}
		mux.HandleFunc("/api/v4/projects/g%2Fp/members/"+strconv.Itoa(uid), func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			updated[int64(uid)] = true
			writeTestJSON(t, w, http.StatusOK, map[string]any{"id": uid})
		})
	}
	mux.HandleFunc("/api/v4/projects/g%2Fp/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	return admin, &mu, updated, revoked
}

// Uninstall leaves the account that owns an administrator-supplied Poller
// credential, and its same-named tokens, alone while still reconciling and
// revoking a genuine managed duplicate.
func TestGitLabUninstallTokenClient_ExcludesSuppliedPollerAccount(t *testing.T) {
	ctx := context.Background()
	admin, mu, updated, revoked := suppliedPollerServer(t, http.StatusOK)
	var levelMu sync.Mutex
	// The supplied account is left elevated: it is not Fullsend's to demote.
	levels := map[int64]int{77: forge.GitLabAccessLevelDeveloper, 90: forge.GitLabAccessLevelMaintainer}
	client := memberLevelClient{FakeClient: forge.NewFakeClient(), mu: &levelMu, levels: levels}
	u := newGitLabUninstallTokenClient(admin)

	require.NoError(t, u.ReconcileGitLabPollersForUninstall(ctx, client, "g", "p"))
	mu.Lock()
	assert.True(t, revoked[771], "the managed duplicate's bootstrap token is revoked")
	assert.False(t, updated[90], "the supplied account's membership is untouched")
	assert.False(t, revoked[901], "the supplied account's tokens are untouched")
	mu.Unlock()

	toks, err := u.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.NoError(t, err)
	for _, tok := range toks {
		assert.Equal(t, 77, tok.UserID, "token %d of the supplied account must not be offered for revocation", tok.ID)
	}
	assert.NotEmpty(t, toks)
	toks, err = u.ListProjectAccessTokens(ctx, "g", "p")
	require.NoError(t, err)
	for _, tok := range toks {
		assert.Equal(t, 77, tok.UserID)
	}
}

// A supplied Poller credential GitLab rejects cannot be attributed, so
// uninstall fails closed: nothing is reconciled and no token inventory is
// offered for revocation.
func TestGitLabUninstallTokenClient_UnattributableSuppliedCredentialFailsClosed(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			ctx := context.Background()
			admin, mu, updated, revoked := suppliedPollerServer(t, status)
			u := newGitLabUninstallTokenClient(admin)

			err := u.ReconcileGitLabPollersForUninstall(ctx, forge.NewFakeClient(), "g", "p")
			require.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)
			mu.Lock()
			assert.Empty(t, updated)
			assert.Empty(t, revoked)
			mu.Unlock()

			toks, err := u.ListProjectAccessTokensStrict(ctx, "g", "p")
			require.Error(t, err)
			assert.Nil(t, toks)
		})
	}
}

// An uninstall that attributes a supplied Poller credential by authenticating
// durably records the owner on the role, so a retry after the credential's
// secret is deleted resolves the owner from rotation state instead of failing
// closed.
func TestGitLabUninstallTokenClient_AttributedSuppliedOwnerSurvivesSecretDeletion(t *testing.T) {
	ctx := context.Background()
	rotationKey := "g/p/" + forge.VarGitLabRoleRotation
	state := `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z"}}}`
	fc := forge.NewFakeClient()
	fc.VariableValues[rotationKey] = state
	fc.VariablesExist[rotationKey] = true

	// First attempt: the owner is not recorded, so it is attributed by
	// authenticating with the installed credential and then persisted.
	admin, _, _, _ := suppliedPollerServer(t, http.StatusOK)
	var levelMu sync.Mutex
	levels := map[int64]int{77: forge.GitLabAccessLevelDeveloper, 90: forge.GitLabAccessLevelMaintainer}
	client := memberLevelClient{FakeClient: fc, mu: &levelMu, levels: levels}
	require.NoError(t, newGitLabUninstallTokenClient(admin).ReconcileGitLabPollersForUninstall(ctx, client, "g", "p"))

	persisted := fc.VariableValues[rotationKey]
	assert.Contains(t, persisted, `"supplied_user_id":90`)
	assert.Contains(t, persisted, `"excluded_user_ids":[90]`)

	// Retry after cleanup deleted the secret: the persisted state alone
	// resolves the owner, with no installed secret and no authentication.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusNotFound, map[string]any{"message": "404 Variable Not Found"})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.VarGitLabRoleRotation, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.VarGitLabRoleRotation, "value": persisted})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 77, "name": "fullsend-poller"}, {"id": 90, "name": "fullsend-poller"}})
	})
	var mu sync.Mutex
	touched90 := false
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/90/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		touched90 = true
		mu.Unlock()
		writeTestJSON(t, w, http.StatusOK, []map[string]any{})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	retryAdmin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	err = newGitLabUninstallTokenClient(retryAdmin).ReconcileGitLabPollersForUninstall(ctx, client, "g", "p")
	require.NoError(t, err)
	mu.Lock()
	assert.False(t, touched90, "the supplied account stays untouched on retry")
	mu.Unlock()
}

// absentServiceAccountAPIServer models a GitLab without the service-account API
// (the listing answers not found) whose project has an installed Poller
// credential and a role-named legacy project access token owned by user 90.
func absentServiceAccountAPIServer(t *testing.T, rotationState string) *gitlab.LiveClient {
	t.Helper()
	return serviceAccountListStatusServer(t, rotationState, http.StatusNotFound)
}

// serviceAccountListStatusServer is absentServiceAccountAPIServer with the
// service-account listing answering listStatus.
func serviceAccountListStatusServer(t *testing.T, rotationState string, listStatus int) *gitlab.LiveClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabPollerToken, "value": "glpat-supplied"})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.VarGitLabRoleRotation, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.VarGitLabRoleRotation, "value": rotationState})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 90})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, listStatus, map[string]any{"message": "service account listing"})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 5, "name": "fullsend-poller", "active": true, "user_id": 90},
			{"id": 6, "name": "fullsend-poller", "active": true, "user_id": 91},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	return admin
}

// With the service-account API absent, uninstall still resolves and durably
// records the owner of a supplied credential before any credential is deleted,
// and the legacy token it owns is never offered for revocation.
func TestGitLabUninstallTokenClient_AbsentServiceAccountAPIStillRecordsSuppliedOwner(t *testing.T) {
	ctx := context.Background()
	rotationKey := "g/p/" + forge.VarGitLabRoleRotation
	fc := forge.NewFakeClient()
	fc.VariableValues[rotationKey] = `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z"}}}`
	fc.VariablesExist[rotationKey] = true
	u := newGitLabUninstallTokenClient(absentServiceAccountAPIServer(t, fc.VariableValues[rotationKey]))

	require.NoError(t, u.ReconcileGitLabPollersForUninstall(ctx, fc, "g", "p"))
	persisted := fc.VariableValues[rotationKey]
	assert.Contains(t, persisted, `"supplied_user_id":90`)
	assert.Contains(t, persisted, `"excluded_user_ids":[90]`)

	toks, err := u.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, toks, 1)
	assert.Equal(t, 6, toks[0].ID, "the supplied owner's legacy token is not offered for revocation")
}

// A service-account listing that fails for a reason other than absence still
// resolves and durably records the owner of an ownerless supplied credential
// before returning the failure. Uninstall goes on to role cleanup, which
// deletes the installed secret the owner is attributed with, so a retry must
// resolve the owner from rotation state alone.
func TestGitLabUninstallTokenClient_FailedListingStillRecordsSuppliedOwner(t *testing.T) {
	ctx := context.Background()
	rotationKey := "g/p/" + forge.VarGitLabRoleRotation
	fc := forge.NewFakeClient()
	fc.VariableValues[rotationKey] = `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z"}}}`
	fc.VariablesExist[rotationKey] = true
	u := newGitLabUninstallTokenClient(serviceAccountListStatusServer(t, fc.VariableValues[rotationKey], http.StatusInternalServerError))

	err := u.ReconcileGitLabPollersForUninstall(ctx, fc, "g", "p")
	require.Error(t, err)
	persisted := fc.VariableValues[rotationKey]
	assert.Contains(t, persisted, `"supplied_user_id":90`, "the owner is recorded despite the failed listing")
	assert.Contains(t, persisted, `"excluded_user_ids":[90]`)

	// Retry after role cleanup deleted the installed secret: the persisted state
	// alone resolves the owner and the supplied account stays untouched.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusNotFound, map[string]any{"message": "404 Variable Not Found"})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.VarGitLabRoleRotation, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.VarGitLabRoleRotation, "value": persisted})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 90, "name": "fullsend-poller"}})
	})
	var mu sync.Mutex
	touched90 := false
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/90/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		touched90 = true
		mu.Unlock()
		writeTestJSON(t, w, http.StatusOK, []map[string]any{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	retryAdmin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	require.NoError(t, newGitLabUninstallTokenClient(retryAdmin).ReconcileGitLabPollersForUninstall(ctx, fc, "g", "p"))
	mu.Lock()
	assert.False(t, touched90, "the supplied account stays untouched on retry")
	mu.Unlock()
}

// With the service-account API absent and no provenance for the installed
// Poller credential, uninstall fails closed instead of offering role-named
// legacy tokens for revocation.
func TestGitLabUninstallTokenClient_AbsentServiceAccountAPIUnknownProvenanceFailsClosed(t *testing.T) {
	ctx := context.Background()
	u := newGitLabUninstallTokenClient(absentServiceAccountAPIServer(t, `{"roles":{}}`))

	err := u.ReconcileGitLabPollersForUninstall(ctx, forge.NewFakeClient(), "g", "p")
	require.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)
	toks, err := u.ListProjectAccessTokensStrict(ctx, "g", "p")
	require.Error(t, err)
	assert.Nil(t, toks)
}
