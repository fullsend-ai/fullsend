package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlablifecycle"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const saTestProject = "/api/v4/projects/group%2Fproject"

// saTestServer is a GitLab API stub for the live role token client. Unserved
// paths answer 404, which the client reads as absent variables and secrets.
type saTestServer struct {
	t    *testing.T
	mux  *http.ServeMux
	srv  *httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func newSATestServer(t *testing.T) *saTestServer {
	t.Helper()
	s := &saTestServer{t: t, mux: http.NewServeMux(), hits: map[string]int{}}
	s.srv = httptest.NewServer(s.mux)
	t.Cleanup(s.srv.Close)
	return s
}

// handle registers a handler whose invocations are counted per route, so a
// test can assert the endpoint it relies on was actually reached rather than
// passing through a 404 branch because a path was mismatched.
func (s *saTestServer) handle(pattern string, fn http.HandlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[pattern]++
		s.mu.Unlock()
		fn(w, r)
	})
}

// assertHit fails unless the route was invoked at least once.
func (s *saTestServer) assertHit(pattern string) {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Positive(s.t, s.hits[pattern], "expected %s to be called", pattern)
}

func (s *saTestServer) client(token string) *gitlab.LiveClient {
	s.t.Helper()
	c, err := gitlab.New(token, gitlab.WithBaseURL(s.srv.URL))
	require.NoError(s.t, err)
	return c
}

func (s *saTestServer) variable(name, value string) {
	s.handle(saTestProject+"/variables/"+name, func(w http.ResponseWriter, r *http.Request) {
		writeSAJSON(w, map[string]any{"value": value})
	})
}

func writeSAJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// The live constructor wires both ownership resolvers and the supplied
// exclusions, so activation never relies on the fail-closed fallback for
// absent resolvers. Each resolver reads the durable provenance recorded in
// rotation state rather than accepting every role-named account.
func TestNewGitLabRoleTokenClient_SetsOwnershipResolvers(t *testing.T) {
	ctx := context.Background()
	s := newSATestServer(t)
	s.variable(forge.VarGitLabRoleRotation, `{"version":2,"roles":{`+
		`"coder":{"phase":"idle","incoming_id":9,"distributed_at":"2026-10-01T00:00:00Z","created_token_ids":[9,10],"managed_user_id":41},`+
		`"poller":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_distributed":true,`+
		`"supplied_user_id":501,"supplied_token_id":13,"excluded_user_ids":[501]}}}`)
	c := newGitLabRoleTokenClient(s.client("admin-token"))

	require.NotNil(t, c.ManagedAccountIDs, "ManagedAccountIDs must be set")
	require.NotNil(t, c.ManagedAccountNames, "ManagedAccountNames must be set")
	require.NotNil(t, c.SuppliedAccountIDs, "SuppliedAccountIDs must be set")
	require.NotNil(t, c.SuppliedTokenIDs, "SuppliedTokenIDs must be set")
	require.NotNil(t, c.CurrentSuppliedAccountIDs, "CurrentSuppliedAccountIDs must be set")
	require.NotNil(t, c.SuppliedCredentialIdentity, "SuppliedCredentialIdentity must be set")
	require.NotNil(t, c.AttributeSuppliedOwners, "AttributeSuppliedOwners must be set")
	assert.NotNil(t, c.ManagedLegacyTokenIDs)
	assert.NotNil(t, c.LegacyTokenCreated)
	assert.NotNil(t, c.AccountCreated)
	assert.NotNil(t, c.VerifyToken)
	assert.NotNil(t, c.VerifyAccountDeletion)
	assert.NotNil(t, c.InstalledRoleCredentialOwner)
	assert.NotNil(t, c.ContainmentClient)
	assert.IsType(t, gitlabServiceAccountAdapter{}, c.SA)
	assert.IsType(t, gitlabTokenAdapter{}, c.Legacy)

	managed, err := c.ManagedAccountIDs(ctx, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, []int{41}, managed)

	names, err := c.ManagedAccountNames(ctx, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, map[int]string{41: gitlabroles.CoderTokenName}, names)

	legacy, err := c.ManagedLegacyTokenIDs(ctx, "group", "project")
	require.NoError(t, err)
	assert.ElementsMatch(t, []int{9, 10}, legacy)

	supplied, err := c.SuppliedAccountIDs(ctx, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, []int{501}, supplied)

	current, err := c.CurrentSuppliedAccountIDs(ctx, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, []int{501}, current)

	tokens, err := c.SuppliedTokenIDs(ctx, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, map[int][]repos.SuppliedTokenRef{501: {{ID: 13, Name: gitlabroles.PollerTokenName}}}, tokens)

	// Durable ownership, not the role name, decides what the live client may
	// manage: a same-named account outside the recorded set is not offered.
	sa, err := c.SuppliedOwnerIDs(ctx, "group", "project")
	require.NoError(t, err)
	assert.NotContains(t, sa, 41)
}

// Provisioning and rotation take administrator-supplied credentials as
// ProvidedTokens (role -> value). Enrollment resolves the credential's owner
// and token ID through SuppliedCredentialIdentity while it still authenticates
// and records them, so an expired or revoked credential keeps its owner
// excluded and can be replaced. An enrollment recorded before that, with
// distribution proof only, is attributed by authenticating with the installed
// secret and fails closed when it cannot.
func TestNewGitLabRoleTokenClient_SuppliedCredentialAttribution(t *testing.T) {
	ctx := context.Background()
	const suppliedValue = "supplied-coder-credential-value"
	const selfRoute = "/api/v4/personal_access_tokens/self"
	const userRoute = "/api/v4/user"
	// The enrollment shape written before owners were recorded: distribution
	// proof only, no owner and no token ID.
	enrollment := `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z"}}}`
	rotationRoute := saTestProject + "/variables/" + forge.VarGitLabRoleRotation
	secretRoute := saTestProject + "/variables/" + forge.SecretGitLabCoderToken

	t.Run("an unrecorded owner is attributed from the installed credential", func(t *testing.T) {
		s := newSATestServer(t)
		s.variable(forge.VarGitLabRoleRotation, enrollment)
		s.variable(forge.SecretGitLabCoderToken, suppliedValue)
		s.handle(userRoute, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("PRIVATE-TOKEN") != suppliedValue {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeSAJSON(w, map[string]any{"id": 777})
		})
		c := newGitLabRoleTokenClient(s.client("admin-token"))

		supplied, err := c.SuppliedAccountIDs(ctx, "group", "project")
		require.NoError(t, err)
		assert.Equal(t, []int{777}, supplied)

		current, err := c.CurrentSuppliedAccountIDs(ctx, "group", "project")
		require.NoError(t, err)
		assert.Equal(t, []int{777}, current)

		attributed, err := c.AttributeSuppliedOwners(ctx, "group", "project")
		require.NoError(t, err)
		assert.Equal(t, map[gitlabroles.Role]int{gitlabroles.RoleCoder: 777}, attributed)

		// No token ID was recorded, so the whole account stands in for the
		// credential.
		tokens, err := c.SuppliedTokenIDs(ctx, "group", "project")
		require.NoError(t, err)
		assert.Empty(t, tokens)

		s.assertHit(rotationRoute)
		s.assertHit(secretRoute)
		s.assertHit(userRoute)
	})

	t.Run("an unattributable credential fails closed", func(t *testing.T) {
		s := newSATestServer(t)
		s.variable(forge.VarGitLabRoleRotation, enrollment)
		s.variable(forge.SecretGitLabCoderToken, suppliedValue)
		s.handle(userRoute, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		c := newGitLabRoleTokenClient(s.client("admin-token"))

		_, err := c.SuppliedAccountIDs(ctx, "group", "project")
		require.Error(t, err)
		assert.ErrorIs(t, err, repos.ErrSuppliedCredentialUnresolved)
		assert.NotContains(t, err.Error(), suppliedValue)

		s.assertHit(rotationRoute)
		s.assertHit(secretRoute)
		s.assertHit(userRoute)
	})

	t.Run("enrollment resolves the owner and token of the supplied value", func(t *testing.T) {
		s := newSATestServer(t)
		s.handle(selfRoute, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("PRIVATE-TOKEN") != suppliedValue {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeSAJSON(w, map[string]any{"id": 13, "user_id": 777, "active": true})
		})
		c := newGitLabRoleTokenClient(s.client("admin-token"))

		id, err := c.SuppliedCredentialIdentity(ctx, suppliedValue)
		require.NoError(t, err)
		assert.Equal(t, repos.SuppliedIdentity{UserID: 777, TokenID: 13}, id)

		_, err = c.SuppliedCredentialIdentity(ctx, "rejected-credential-value")
		assert.ErrorIs(t, err, repos.ErrSuppliedCredentialUnresolved, "a credential that does not authenticate has no identity")
		s.assertHit(selfRoute)
	})

	// An enrolled credential that has since expired keeps its recorded owner
	// excluded and still resolves, so a working replacement can be enrolled.
	// Both the account name and a second, longer-lived PAT of the owner are
	// irrelevant: only the enrolled token stands in for the role.
	t.Run("a recorded owner survives expiry of the installed credential", func(t *testing.T) {
		s := newSATestServer(t)
		s.variable(forge.VarGitLabRoleRotation, `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z",`+
			`"supplied":true,"supplied_user_id":777,"supplied_token_id":13,"excluded_user_ids":[777]}}}`)
		s.variable(forge.SecretGitLabCoderToken, suppliedValue)
		s.handle(userRoute, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		s.handle(selfRoute, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		c := newGitLabRoleTokenClient(s.client("admin-token"))

		supplied, err := c.SuppliedAccountIDs(ctx, "group", "project")
		require.NoError(t, err, "an expired credential must not block the project")
		assert.Equal(t, []int{777}, supplied)

		current, err := c.CurrentSuppliedAccountIDs(ctx, "group", "project")
		require.NoError(t, err)
		assert.Equal(t, []int{777}, current)

		tokens, err := c.SuppliedTokenIDs(ctx, "group", "project")
		require.NoError(t, err)
		assert.Equal(t, map[int][]repos.SuppliedTokenRef{777: {{ID: 13, Name: gitlabroles.CoderTokenName}}}, tokens,
			"only the enrolled token is reported, under the role's token name")

		attributed, err := c.AttributeSuppliedOwners(ctx, "group", "project")
		require.NoError(t, err)
		assert.Empty(t, attributed, "nothing is re-attributed once the owner is recorded")

		s.assertHit(rotationRoute)
	})
}

// The live callbacks delegate to the durable recorders, the credential
// verifier and the account-deletion safety check.
func TestNewGitLabRoleTokenClient_Callbacks(t *testing.T) {
	ctx := context.Background()
	const installedValue = "installed-credential-value"

	t.Run("installed credential owner", func(t *testing.T) {
		s := newSATestServer(t)
		s.variable(forge.SecretGitLabCoderToken, installedValue)
		s.handle("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("PRIVATE-TOKEN") != installedValue {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeSAJSON(w, map[string]any{"id": 9, "user_id": 41, "active": true})
		})
		c := newGitLabRoleTokenClient(s.client("admin-token"))

		id, err := c.InstalledRoleCredentialOwner(ctx, "group", "project", forge.SecretGitLabCoderToken)
		require.NoError(t, err)
		assert.Equal(t, 41, id)

		id, err = c.InstalledRoleCredentialOwner(ctx, "group", "project", forge.SecretGitLabAnalystToken)
		require.NoError(t, err)
		assert.Zero(t, id, "an absent secret has no owner")

		s.variable(forge.SecretGitLabPollerToken, "rejected-credential-value")
		_, err = c.InstalledRoleCredentialOwner(ctx, "group", "project", forge.SecretGitLabPollerToken)
		assert.Error(t, err, "an unattributable secret is an error, never a zero owner")
		s.assertHit("/api/v4/personal_access_tokens/self")
	})

	t.Run("recorders refuse creations without identities", func(t *testing.T) {
		s := newSATestServer(t)
		c := newGitLabRoleTokenClient(s.client("admin-token"))
		assert.Error(t, c.LegacyTokenCreated(ctx, "group", "project", nil))
		assert.Error(t, c.AccountCreated(ctx, "group", "project", repos.GitLabServiceAccount{Name: gitlabroles.CoderTokenName}))
		assert.Error(t, c.VerifyToken(ctx, "group", "project", nil))
	})

	t.Run("account deletion checks every owned resource", func(t *testing.T) {
		s := newSATestServer(t)
		var mu sync.Mutex
		triggers := []map[string]any{}
		s.handle("/api/v4/users/41/keys", func(w http.ResponseWriter, r *http.Request) {
			writeSAJSON(w, []map[string]any{})
		})
		s.handle(saTestProject+"/jobs", func(w http.ResponseWriter, r *http.Request) {
			writeSAJSON(w, []map[string]any{})
		})
		s.handle(saTestProject+"/pipeline_schedules", func(w http.ResponseWriter, r *http.Request) {
			writeSAJSON(w, []map[string]any{})
		})
		s.handle(saTestProject+"/triggers", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			writeSAJSON(w, triggers)
		})
		c := newGitLabRoleTokenClient(s.client("admin-token"))

		require.NoError(t, c.VerifyAccountDeletion(ctx, "group", "project", 41))

		mu.Lock()
		triggers = []map[string]any{{"id": 5, "description": "other", "owner": map[string]any{"id": 41}}}
		mu.Unlock()
		err := c.VerifyAccountDeletion(ctx, "group", "project", 41)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not deleted")
		for _, route := range []string{"/api/v4/users/41/keys", saTestProject + "/jobs", saTestProject + "/pipeline_schedules", saTestProject + "/triggers"} {
			s.assertHit(route)
		}
	})

	t.Run("account deletion fails closed on an unreadable inventory", func(t *testing.T) {
		s := newSATestServer(t)
		c := newGitLabRoleTokenClient(s.client("admin-token"))
		assert.Error(t, c.VerifyAccountDeletion(ctx, "group", "project", 41))
	})
}

func TestGitLabServiceAccountAdapter(t *testing.T) {
	ctx := context.Background()
	const newValue = "new-credential-value"
	s := newSATestServer(t)
	var mu sync.Mutex
	var calls []string
	record := func(r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.Method+" "+r.URL.EscapedPath())
	}
	s.mux.HandleFunc(saTestProject+"/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.Method == http.MethodPost {
			writeSAJSON(w, map[string]any{"id": 41, "name": gitlabroles.CoderTokenName, "username": "svc"})
			return
		}
		writeSAJSON(w, []map[string]any{{"id": 41, "name": gitlabroles.CoderTokenName}})
	})
	s.mux.HandleFunc(saTestProject+"/service_accounts/41", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.WriteHeader(http.StatusNoContent)
	})
	s.mux.HandleFunc(saTestProject+"/service_accounts/41/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		if r.Method == http.MethodPost {
			writeSAJSON(w, map[string]any{"id": 9, "name": gitlabroles.CoderTokenName, "token": newValue, "active": true, "expires_at": "2027-01-01", "user_id": 41})
			return
		}
		writeSAJSON(w, []map[string]any{{"id": 9, "name": gitlabroles.CoderTokenName, "active": true, "revoked": false, "expires_at": "2027-01-01", "user_id": 41}})
	})
	s.mux.HandleFunc(saTestProject+"/service_accounts/41/personal_access_tokens/9", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.WriteHeader(http.StatusNoContent)
	})
	s.mux.HandleFunc(saTestProject+"/members", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeSAJSON(w, map[string]any{"id": 41, "access_level": 30})
	})
	s.mux.HandleFunc(saTestProject+"/members/41", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeSAJSON(w, map[string]any{"id": 41, "access_level": 30})
	})
	s.mux.HandleFunc(saTestProject+"/members/all/41", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeSAJSON(w, map[string]any{"id": 41, "access_level": 30})
	})
	a := gitlabServiceAccountAdapter{c: s.client("admin-token")}

	sa, err := a.CreateProjectServiceAccount(ctx, "group", "project", gitlabroles.CoderTokenName)
	require.NoError(t, err)
	assert.Equal(t, repos.GitLabServiceAccount{ID: 41, Name: gitlabroles.CoderTokenName}, sa)

	accounts, err := a.ListProjectServiceAccounts(ctx, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, []repos.GitLabServiceAccount{{ID: 41, Name: gitlabroles.CoderTokenName}}, accounts)

	tok, err := a.CreateServiceAccountPAT(ctx, "group", "project", 41, gitlabroles.CoderTokenName, gitlabroles.TokenScopes(), "2027-01-01")
	require.NoError(t, err)
	assert.Equal(t, &repos.ProjectAccessToken{ID: 9, Name: gitlabroles.CoderTokenName, Token: newValue, Active: true, ExpiresAt: "2027-01-01", UserID: 41}, tok)

	toks, err := a.ListServiceAccountPATs(ctx, "group", "project", 41)
	require.NoError(t, err)
	assert.Equal(t, []repos.ProjectAccessToken{{ID: 9, Name: gitlabroles.CoderTokenName, Active: true, ExpiresAt: "2027-01-01", UserID: 41}}, toks)

	require.NoError(t, a.RevokeServiceAccountPAT(ctx, "group", "project", 41, 9))
	require.NoError(t, a.AddProjectMember(ctx, "group", "project", 41, gitlabroles.DeveloperAccessLevel))
	require.NoError(t, a.UpdateProjectMemberAccessLevel(ctx, "group", "project", 41, gitlabroles.DeveloperAccessLevel))
	level, err := a.GetProjectMemberAccessLevel(ctx, "group", "project", 41)
	require.NoError(t, err)
	assert.Equal(t, gitlabroles.DeveloperAccessLevel, level)
	require.NoError(t, a.DeleteProjectServiceAccount(ctx, "group", "project", 41))

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, calls, "DELETE "+saTestProject+"/service_accounts/41/personal_access_tokens/9")
	assert.Contains(t, calls, "PUT "+saTestProject+"/members/41")
	assert.Contains(t, calls, "DELETE "+saTestProject+"/service_accounts/41")

	t.Run("errors propagate", func(t *testing.T) {
		fail := newSATestServer(t)
		fail.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) })
		bad := gitlabServiceAccountAdapter{c: fail.client("admin-token")}
		_, err := bad.CreateProjectServiceAccount(ctx, "group", "project", gitlabroles.CoderTokenName)
		assert.Error(t, err)
		_, err = bad.ListProjectServiceAccounts(ctx, "group", "project")
		assert.Error(t, err)
		_, err = bad.CreateServiceAccountPAT(ctx, "group", "project", 41, gitlabroles.CoderTokenName, gitlabroles.TokenScopes(), "2027-01-01")
		assert.Error(t, err)
		_, err = bad.ListServiceAccountPATs(ctx, "group", "project", 41)
		assert.Error(t, err)
	})
}

func TestVerifyGitLabRoleToken(t *testing.T) {
	ctx := context.Background()
	const value = "replacement-credential-value"
	newServer := func(t *testing.T, self map[string]any, level int) *saTestServer {
		s := newSATestServer(t)
		s.handle("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("PRIVATE-TOKEN") != value {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeSAJSON(w, self)
		})
		s.handle(saTestProject+"/members/all/41", func(w http.ResponseWriter, r *http.Request) {
			writeSAJSON(w, map[string]any{"id": 41, "access_level": level})
		})
		return s
	}
	good := map[string]any{"id": 9, "user_id": 41, "active": true, "scopes": []string{"api"}, "name": gitlabroles.CoderTokenName}
	tok := &repos.ProjectAccessToken{ID: 9, UserID: 41, Name: gitlabroles.CoderTokenName, Token: value}

	t.Run("accepts the minted identity at Developer", func(t *testing.T) {
		s := newServer(t, good, gitlabroles.DeveloperAccessLevel)
		require.NoError(t, verifyGitLabRoleToken(ctx, s.client("admin-token"), "group", "project", tok))
		s.assertHit("/api/v4/personal_access_tokens/self")
		s.assertHit(saTestProject + "/members/all/41")
	})

	t.Run("rejects a missing token", func(t *testing.T) {
		s := newServer(t, good, gitlabroles.DeveloperAccessLevel)
		assert.Error(t, verifyGitLabRoleToken(ctx, s.client("admin-token"), "group", "project", nil))
	})

	mismatches := map[string]map[string]any{
		"other token":   {"id": 8, "user_id": 41, "active": true, "scopes": []string{"api"}},
		"other user":    {"id": 9, "user_id": 42, "active": true, "scopes": []string{"api"}},
		"inactive":      {"id": 9, "user_id": 41, "active": false, "scopes": []string{"api"}},
		"revoked":       {"id": 9, "user_id": 41, "active": true, "revoked": true, "scopes": []string{"api"}},
		"missing scope": {"id": 9, "user_id": 41, "active": true, "scopes": []string{"read_api"}},
	}
	for name, self := range mismatches {
		t.Run("rejects "+name, func(t *testing.T) {
			s := newServer(t, self, gitlabroles.DeveloperAccessLevel)
			err := verifyGitLabRoleToken(ctx, s.client("admin-token"), "group", "project", tok)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unexpected identity, scope or active state")
			s.assertHit("/api/v4/personal_access_tokens/self")
		})
	}

	t.Run("rejects effective access above Developer", func(t *testing.T) {
		s := newServer(t, good, 40)
		err := verifyGitLabRoleToken(ctx, s.client("admin-token"), "group", "project", tok)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "effective Developer access")
		s.assertHit(saTestProject + "/members/all/41")
	})

	t.Run("rejects a credential that does not authenticate", func(t *testing.T) {
		s := newServer(t, good, gitlabroles.DeveloperAccessLevel)
		other := *tok
		other.Token = "other-credential-value"
		err := verifyGitLabRoleToken(ctx, s.client("admin-token"), "group", "project", &other)
		require.Error(t, err)
		// The self endpoint was reached and rejected the credential, rather than
		// the error coming from an unmatched route.
		s.assertHit("/api/v4/personal_access_tokens/self")
	})

	t.Run("a replacement Poller must hold protected-ref pipeline access", func(t *testing.T) {
		poller := map[string]any{"id": 9, "user_id": 41, "active": true, "scopes": []string{"api"}, "name": gitlabroles.PollerTokenName}
		s := newServer(t, poller, gitlabroles.DeveloperAccessLevel)
		// The project lookup fails, so pipeline access cannot be established.
		ptok := *tok
		ptok.Name = gitlabroles.PollerTokenName
		assert.Error(t, verifyGitLabRoleToken(ctx, s.client("admin-token"), "group", "project", &ptok))
		s.assertHit("/api/v4/personal_access_tokens/self")
		s.assertHit(saTestProject + "/members/all/41")
	})
}

func TestGitlabTriggerOwnerFor(t *testing.T) {
	assert.Nil(t, gitlabTriggerOwnerFor(forge.NewFakeClient()))
	s := newSATestServer(t)
	to := gitlabTriggerOwnerFor(s.client("admin-token"))
	require.NotNil(t, to)
	assert.IsType(t, &gitlablifecycle.TriggerOwner{}, to)
	// The live owner cannot guarantee request draining, so the webhook fast
	// path keeps deferring trigger creation and rotation.
	_, verifies := to.(repos.GitLabPollerQuiescenceVerifier)
	assert.False(t, verifies, "the live trigger owner must not enable elevation")
}

// fakeTriggerOwner identifies one managed Poller and applies membership
// changes to the fake forge client.
type fakeTriggerOwner struct {
	fc            *forge.FakeClient
	uid           int64
	idErr         error
	bootstrapRevs []int64
}

func (o *fakeTriggerOwner) PollerUserID(context.Context, string, string) (int64, error) {
	return o.uid, o.idErr
}

func (o *fakeTriggerOwner) CreatePollerBootstrap(context.Context, string, string, int64) (*repos.PollerBootstrap, error) {
	return nil, fmt.Errorf("not used")
}

func (o *fakeTriggerOwner) CreatePipelineTriggerTokenAsPoller(context.Context, string, string, string, *repos.PollerBootstrap) (*forge.PipelineTriggerToken, error) {
	return nil, fmt.Errorf("not used")
}

func (o *fakeTriggerOwner) RevokePollerBootstrap(_ context.Context, _, _ string, uid int64) error {
	o.bootstrapRevs = append(o.bootstrapRevs, uid)
	return nil
}

func (o *fakeTriggerOwner) SetProjectMemberAccessLevel(_ context.Context, _, _ string, uid int64, level int) error {
	o.fc.ProjectMemberAccess[uid] = level
	return nil
}

func (o *fakeTriggerOwner) VerifyPollerElevationSafe(context.Context, string, string, int64) error {
	return nil
}

func (o *fakeTriggerOwner) VerifyPollerCredentialsRevoked(context.Context, string, string, int64) error {
	return nil
}

func (o *fakeTriggerOwner) RevokePollerRuntimeCredentials(context.Context, string, string, int64) error {
	return nil
}

func (o *fakeTriggerOwner) CreatePollerRuntimeToken(context.Context, string, string, int64, string) (*repos.PollerRuntimeToken, error) {
	return nil, fmt.Errorf("not used")
}

func (o *fakeTriggerOwner) ContainPoller(context.Context, string, string, int64) error {
	return nil
}

func TestReconcileGitLabPollerElevation(t *testing.T) {
	ctx := context.Background()

	t.Run("restores an elevated managed Poller to Developer", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.ProjectMemberAccess[7] = 40
		to := &fakeTriggerOwner{fc: fc, uid: 7}
		var out bytes.Buffer
		err := reconcileGitLabPollerElevation(ctx, &reposInstallConfig{testGitLabTriggerOwner: to}, fc, ui.New(&out), "group", "project")
		require.NoError(t, err)
		assert.Equal(t, gitlabroles.DeveloperAccessLevel, fc.ProjectMemberAccess[7])
		assert.Contains(t, out.String(), "Restored the GitLab Poller identity (user ID 7) from access level 40 to Developer")
		assert.Equal(t, []int64{7}, to.bootstrapRevs)
	})

	t.Run("dry run changes nothing", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.ProjectMemberAccess[7] = 40
		to := &fakeTriggerOwner{fc: fc, uid: 7}
		err := reconcileGitLabPollerElevation(ctx, &reposInstallConfig{testGitLabTriggerOwner: to, dryRun: true}, fc, ui.New(&bytes.Buffer{}), "group", "project")
		require.NoError(t, err)
		assert.Equal(t, 40, fc.ProjectMemberAccess[7])
		assert.Empty(t, to.bootstrapRevs)
	})

	t.Run("an identity lookup failure is returned", func(t *testing.T) {
		fc := forge.NewFakeClient()
		to := &fakeTriggerOwner{fc: fc, idErr: fmt.Errorf("inventory unavailable")}
		err := reconcileGitLabPollerElevation(ctx, &reposInstallConfig{testGitLabTriggerOwner: to}, fc, ui.New(&bytes.Buffer{}), "group", "project")
		assert.Error(t, err)
	})

	t.Run("no live client means no trigger owner", func(t *testing.T) {
		fc := forge.NewFakeClient()
		require.NoError(t, reconcileGitLabPollerElevation(ctx, nil, fc, ui.New(&bytes.Buffer{}), "group", "project"))
	})
}

// repos status reports managed service accounts and treats a managed account
// above Developer as drift that clears role readiness.
func TestAnnotateGitLabRoleLifecycleReportsServiceAccountDrift(t *testing.T) {
	ctx := context.Background()
	s := newSATestServer(t)
	s.variable(forge.VarGitLabRoleRotation, `{"version":2,"roles":{"coder":{"phase":"idle","incoming_id":9,`+
		`"distributed_at":"2026-10-01T00:00:00Z","created_token_ids":[9],"managed_user_id":41}}}`)
	s.handle(saTestProject+"/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeSAJSON(w, []map[string]any{{"id": 41, "name": gitlabroles.CoderTokenName}})
	})
	s.handle(saTestProject+"/service_accounts/41/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		writeSAJSON(w, []map[string]any{{"id": 9, "name": gitlabroles.CoderTokenName, "active": true, "expires_at": "2027-01-01", "user_id": 41}})
	})
	s.handle(saTestProject+"/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		writeSAJSON(w, []map[string]any{})
	})
	s.handle(saTestProject+"/members/all/41", func(w http.ResponseWriter, r *http.Request) {
		writeSAJSON(w, map[string]any{"id": 41, "access_level": 40})
	})
	result := &repos.StatusResult{Repos: []repos.RepoStatus{{Owner: "group", Repo: "project"}}}
	annotateGitLabRoleLifecycle(ctx, newSingleClientFactory(s.client("admin-token")), result)

	for _, route := range []string{saTestProject + "/service_accounts", saTestProject + "/service_accounts/41/personal_access_tokens", saTestProject + "/access_tokens", saTestProject + "/members/all/41"} {
		s.assertHit(route)
	}
	st := result.Repos[0]
	require.Len(t, st.GitLabServiceAccounts, 1)
	assert.Equal(t, repos.GitLabServiceAccountStatus{ID: 41, Name: gitlabroles.CoderTokenName, AccessLevel: 40, Managed: true}, st.GitLabServiceAccounts[0])
	assert.True(t, slices.ContainsFunc(st.Drifts, func(d repos.Drift) bool { return d.Field == "gitlab-service-account:41" }),
		"expected service-account drift, got %v", st.Drifts)
	assert.False(t, st.GitLabRolesReady)
	assert.Equal(t, 1, result.Summary.Drifted)
}
