package cli

import (
	"context"
	"encoding/json"
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

func writeTestJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

func TestGitLabRoleTokenClient_ProvisionsServiceAccount(t *testing.T) {
	ctx := context.Background()
	member := 0
	mux := http.NewServeMux()
	selfCalled := false
	mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
		selfCalled = true
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 9, "user_id": 77, "active": true, "scopes": []string{"api"}})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeTestJSON(t, w, http.StatusCreated, map[string]any{"id": 77, "username": "sa", "name": "fullsend-poller"})
			return
		}
		writeTestJSON(t, w, http.StatusOK, []map[string]any{})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/members", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		member = int(body["access_level"].(float64))
		writeTestJSON(t, w, http.StatusConflict, map[string]any{"message": "Member already exists"})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/members/77", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		member = int(body["access_level"].(float64))
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/members/all/77", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77, "access_level": member})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeTestJSON(t, w, http.StatusCreated, map[string]any{"id": 9, "name": "fullsend-poller", "token": "glpat-sa", "active": true})
			return
		}
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 9, "name": "fullsend-poller", "active": true}})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/service_accounts/77/personal_access_tokens/9", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	c := newGitLabRoleTokenClient(glClient).(repos.ServiceAccountTokenClient)
	// Durable creation ownership is exercised separately from this API adapter.
	c.AccountCreated = nil
	tok, err := c.CreateProjectAccessToken(ctx, "group", "project", "fullsend-poller", []string{"api"}, 30, "2027-01-01")
	require.NoError(t, err)
	assert.Equal(t, 9, tok.ID)
	assert.Equal(t, 77, tok.UserID)
	assert.Equal(t, 30, member)
	assert.True(t, selfCalled)

	// The listing below still reports the created account, so mux the
	// list to include it.
	mux2 := http.NewServeMux()
	handleRotationState(t, mux2, "group%2Fproject", `{"roles":{"poller":{"managed_user_id":77}}}`)
	mux2.HandleFunc("/api/v4/projects/group%2Fproject/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 77, "username": "sa", "name": "fullsend-poller"}})
	})
	mux2.Handle("/", mux)
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	gl2, err := gitlab.New("test-token", gitlab.WithBaseURL(srv2.URL))
	require.NoError(t, err)
	c2 := newGitLabRoleTokenClient(gl2).(repos.ServiceAccountTokenClient)
	toks, err := c2.ListProjectAccessTokens(ctx, "group", "project")
	require.NoError(t, err)
	require.Len(t, toks, 1)
	assert.Equal(t, 77, toks[0].UserID)
	require.NoError(t, c2.RevokeProjectAccessToken(ctx, "group", "project", 9))
}

func TestGitLabServiceAccountAdapter_Errors(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	glClient, err := gitlab.New("test-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	a := gitlabServiceAccountAdapter{c: glClient}

	_, err = a.CreateProjectServiceAccount(ctx, "g", "p", "fullsend-poller")
	assert.Error(t, err)
	_, err = a.ListProjectServiceAccounts(ctx, "g", "p")
	assert.Error(t, err)
	_, err = a.CreateServiceAccountPAT(ctx, "g", "p", 1, "fullsend-poller", nil, "2027-01-01")
	assert.Error(t, err)
	_, err = a.ListServiceAccountPATs(ctx, "g", "p", 1)
	assert.Error(t, err)
	assert.Error(t, a.RevokeServiceAccountPAT(ctx, "g", "p", 1, 2))
	assert.Error(t, a.AddProjectMember(ctx, "g", "p", 1, 30))
	assert.Error(t, a.UpdateProjectMemberAccessLevel(ctx, "g", "p", 1, 30))
	_, err = a.GetProjectMemberAccessLevel(ctx, "g", "p", 1)
	assert.Error(t, err)
}

// handleRotationState serves the project's rotation-state variable.
func handleRotationState(t *testing.T, mux *http.ServeMux, project, state string) {
	t.Helper()
	mux.HandleFunc("/api/v4/projects/"+project+"/variables/"+forge.VarGitLabRoleRotation, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.VarGitLabRoleRotation, "value": state})
	})
}

// managedPollerState records a fullsend-minted Poller credential.
const managedPollerState = `{"roles":{"poller":{"phase":"idle","incoming_id":5,"managed_user_id":77,"distributed_at":"2026-01-01T00:00:00Z"}}}`

func TestGitLabPollerTriggerOwner(t *testing.T) {
	ctx := context.Background()
	var gotTokens []string
	var levels []int
	mux := http.NewServeMux()
	handleRotationState(t, mux, "group%2Fproject", managedPollerState)
	mux.HandleFunc("/api/v4/projects/group%2Fproject/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		// GitLab rejects a bare key as ambiguous when the same key also exists
		// for specific environments, so the lookup must name the wildcard scope.
		if r.URL.Query().Get("filter[environment_scope]") != "*" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabPollerToken, "value": "glpat-poller"})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		gotTokens = append(gotTokens, r.Header.Get("PRIVATE-TOKEN"))
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "admin-token", r.Header.Get("PRIVATE-TOKEN"))
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 12, "name": "fullsend-coder"}, {"id": 77, "name": "fullsend-poller"}})
	})
	mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "glpat-poller", r.Header.Get("PRIVATE-TOKEN"))
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 5, "name": "fullsend-poller", "active": true, "user_id": 77})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/triggers", func(w http.ResponseWriter, r *http.Request) {
		gotTokens = append(gotTokens, r.Header.Get("PRIVATE-TOKEN"))
		writeTestJSON(t, w, http.StatusCreated, map[string]any{"id": 3, "description": "d", "token": "glptt-x", "owner": map[string]any{"id": 77}})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "admin-token", r.Header.Get("PRIVATE-TOKEN"), "the bootstrap credential is created with installer authority")
		if r.Method == http.MethodGet {
			writeTestJSON(t, w, http.StatusOK, []map[string]any{})
			return
		}
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "fullsend-poller-bootstrap", body["name"])
		writeTestJSON(t, w, http.StatusCreated, map[string]any{"id": 8, "name": "fullsend-poller-bootstrap", "token": "glpat-bootstrap-test", "active": true})
	})
	mux.HandleFunc("/api/v4/projects/group%2Fproject/members/77", func(w http.ResponseWriter, r *http.Request) {
		gotTokens = append(gotTokens, r.Header.Get("PRIVATE-TOKEN"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		levels = append(levels, int(body["access_level"].(float64)))
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	to := gitlabTriggerOwnerFor(admin)
	require.NotNil(t, to)
	uid, err := to.PollerUserID(ctx, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, int64(77), uid)
	boot, err := to.CreatePollerBootstrap(ctx, "group", "project", 77)
	require.NoError(t, err)
	assert.Equal(t, 8, boot.ID)
	tok, err := to.CreatePipelineTriggerTokenAsPoller(ctx, "group", "project", "d", boot)
	require.NoError(t, err)
	assert.Equal(t, int64(77), tok.OwnerID)
	require.NoError(t, to.SetProjectMemberAccessLevel(ctx, "group", "project", 77, 40))
	assert.Equal(t, []int{40}, levels)
	assert.Equal(t, []string{"glpat-poller", "glpat-bootstrap-test", "admin-token"}, gotTokens, "the trigger is created with the bootstrap credential, never the runtime one; membership changes use the admin credential")

	assert.Nil(t, gitlabTriggerOwnerFor(forge.NewFakeClient()))
}

func TestResolveProvidedTokenIDs_OnlyInventoryCoveredTokens(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		userID int
		want   map[gitlabroles.Role]int
	}{
		{name: "role service account", userID: 90, want: map[gitlabroles.Role]int{gitlabroles.RolePoller: 7}},
		{name: "ordinary user", userID: 55, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 7, "name": "ci", "active": true, "user_id": tc.userID})
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 90, "name": "fullsend-poller"}, {"id": 91, "name": "unrelated"}})
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/access_tokens", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, []map[string]any{})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)

			got := resolveProvidedTokenIDs(ctx, admin, "g", "p", map[gitlabroles.Role]string{gitlabroles.RolePoller: "glpat-supplied"})
			assert.Equal(t, tc.want, got)
		})
	}
}

// An administrator-supplied Poller owned by an ordinary user is in no token
// inventory, so protected-ref access is evaluated for its recorded owner.
func TestPollerPipelineUserIDs_IncludesRecordedSuppliedOwner(t *testing.T) {
	ctx := context.Background()
	fake := forge.NewFakeClient()
	fake.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z","supplied":true,"supplied_user_id":55}}}`
	fake.VariablesExist["g/p/"+forge.VarGitLabRoleRotation] = true

	assert.Equal(t, []int{55}, pollerPipelineUserIDs(ctx, fake, "g", "p", nil))
	// An owner already derived from the inventory is not duplicated.
	assert.Equal(t, []int{55}, pollerPipelineUserIDs(ctx, fake, "g", "p", []repos.ProjectAccessToken{
		{Name: "fullsend-poller", Active: true, UserID: 55},
	}))
	// Managed provenance adds nothing.
	fake.VariableValues["g/p/"+forge.VarGitLabRoleRotation] = `{"roles":{"poller":{"phase":"idle","incoming_id":5}}}`
	assert.Empty(t, pollerPipelineUserIDs(ctx, fake, "g", "p", nil))
}

// Failures reading the installed Poller credential while attributing a supplied
// credential never forward the server's response text, which could echo a
// credential.
