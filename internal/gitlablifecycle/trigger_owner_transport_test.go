package gitlablifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// patFixture models the personal access token inventory of the Poller
// service account (user 77) behind the installer credential.
type patFixture struct {
	mu      sync.Mutex
	tokens  []map[string]any
	nextID  int
	created []map[string]any
	revoked []int
	// listStatus fails every listing; relistStatus fails only listings after
	// the first revocation request.
	listStatus, relistStatus, revokeStatus, createStatus int
	// ineffective keeps revoked tokens active.
	ineffective bool
}

func (f *patFixture) register(t *testing.T, mux *http.ServeMux) {
	t.Helper()
	const base = "/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens"
	mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "admin-token", r.Header.Get("PRIVATE-TOKEN"))
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			status := http.StatusOK
			if f.listStatus != 0 {
				status = f.listStatus
			} else if f.relistStatus != 0 && len(f.revoked) > 0 {
				status = f.relistStatus
			}
			writeTestJSON(t, w, status, f.tokens)
		case http.MethodPost:
			if f.createStatus != 0 {
				writeTestJSON(t, w, f.createStatus, map[string]any{"message": "refused glpat-server-echo"})
				return
			}
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			f.created = append(f.created, body)
			f.nextID++
			tok := map[string]any{"id": f.nextID, "name": body["name"], "active": true, "token": "glpat-new-" + strconv.Itoa(f.nextID)}
			f.tokens = append(f.tokens, tok)
			writeTestJSON(t, w, http.StatusCreated, tok)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	mux.HandleFunc(base+"/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		id, err := strconv.Atoi(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		require.NoError(t, err)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.revoked = append(f.revoked, id)
		if f.revokeStatus != 0 {
			w.WriteHeader(f.revokeStatus)
			return
		}
		if !f.ineffective {
			for _, tok := range f.tokens {
				if tok["id"] == id {
					tok["active"] = false
					tok["revoked"] = true
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func fixedNow() time.Time { return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) }

func TestTriggerOwnerBootstrapLifecycle(t *testing.T) {
	ctx := context.Background()
	pats := &patFixture{nextID: 10, tokens: []map[string]any{
		{"id": 1, "name": gitlabroles.PollerTokenName, "active": true},
		{"id": 2, "name": gitlabroles.PollerBootstrapTokenName, "active": true},
		{"id": 3, "name": "unrelated", "active": true},
		{"id": 4, "name": gitlabroles.PollerBootstrapTokenName, "active": false, "revoked": true},
	}}
	mux := http.NewServeMux()
	pats.register(t, mux)
	var triggerAuth []string
	mux.HandleFunc("/api/v4/projects/g%2Fp/triggers", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		triggerAuth = append(triggerAuth, r.Header.Get("PRIVATE-TOKEN"))
		writeTestJSON(t, w, http.StatusCreated, map[string]any{"id": 900, "description": repos.GitLabWebhookTriggerDescription, "token": "glptt-x", "owner": map[string]any{"id": 77}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	to := newTestOwner(t, srv.URL)
	to.Now = fixedNow

	// An orphaned bootstrap token is revoked before a new one is created, and
	// no other token on the account is touched.
	boot, err := to.CreatePollerBootstrap(ctx, "g", "p", 77)
	require.NoError(t, err)
	assert.Equal(t, []int{2}, pats.revoked)
	require.Len(t, pats.created, 1)
	assert.Equal(t, gitlabroles.PollerBootstrapTokenName, pats.created[0]["name"])
	assert.Equal(t, "2026-10-12", pats.created[0]["expires_at"], "the bootstrap credential expires within days")
	assert.Equal(t, 11, boot.ID)
	assert.Equal(t, "glpat-new-11", boot.Token)

	// The trigger is created as the Poller with the bootstrap credential, not
	// the installer's.
	trig, err := to.CreatePipelineTriggerTokenAsPoller(ctx, "g", "p", repos.GitLabWebhookTriggerDescription, boot)
	require.NoError(t, err)
	assert.Equal(t, int64(900), trig.ID)
	assert.Equal(t, []string{"glpat-new-11"}, triggerAuth)
	_, err = to.CreatePipelineTriggerTokenAsPoller(ctx, "g", "p", "d", &repos.PollerBootstrap{ID: 1})
	require.Error(t, err, "an empty bootstrap value never authenticates a trigger creation")

	// The bootstrap credential is revoked and verified by name.
	pats.revoked = nil
	require.NoError(t, to.RevokePollerBootstrap(ctx, "g", "p", 77))
	assert.Equal(t, []int{11}, pats.revoked)
}

func TestTriggerOwnerNewClientErrorDoesNotLeakBootstrap(t *testing.T) {
	to := newTestOwner(t, "http://127.0.0.1:1")
	to.NewClient = func(string, string) (*gitlab.LiveClient, error) { return nil, assert.AnError }
	_, err := to.CreatePipelineTriggerTokenAsPoller(context.Background(), "g", "p", "d", &repos.PollerBootstrap{ID: 1, Token: "glpat-bootstrap"})
	require.ErrorIs(t, err, assert.AnError)
	assert.NotContains(t, err.Error(), "glpat-bootstrap")
}

func TestTriggerOwnerRevokeNamedPATsFailures(t *testing.T) {
	ctx := context.Background()
	active := func() []map[string]any {
		return []map[string]any{
			{"id": 5, "name": gitlabroles.PollerBootstrapTokenName, "active": true},
			{"id": 6, "name": gitlabroles.PollerBootstrapTokenName, "active": true},
		}
	}
	for name, tc := range map[string]struct {
		fixture *patFixture
		want    string
	}{
		"listing fails":              {fixture: &patFixture{listStatus: http.StatusForbidden}, want: "listing Poller service account tokens"},
		"revocation fails":           {fixture: &patFixture{revokeStatus: http.StatusForbidden}, want: "revoking " + strconv.Quote(gitlabroles.PollerBootstrapTokenName) + " token ID 5"},
		"revocation is ineffective":  {fixture: &patFixture{ineffective: true}, want: "still active after revocation"},
		"verification listing fails": {fixture: &patFixture{relistStatus: http.StatusForbidden}, want: "verifying revocation"},
	} {
		t.Run(name, func(t *testing.T) {
			tc.fixture.tokens = active()
			mux := http.NewServeMux()
			tc.fixture.register(t, mux)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			err := newTestOwner(t, srv.URL).RevokePollerBootstrap(ctx, "g", "p", 77)
			require.ErrorContains(t, err, tc.want)
		})
	}

	t.Run("already removed tokens are not a failure", func(t *testing.T) {
		pats := &patFixture{tokens: active(), revokeStatus: http.StatusNotFound}
		mux := http.NewServeMux()
		pats.register(t, mux)
		srv := httptest.NewServer(mux)
		defer srv.Close()
		// GitLab reports the token gone, but the re-listing still shows it
		// active, so the revocation is not confirmed.
		err := newTestOwner(t, srv.URL).RevokePollerBootstrap(ctx, "g", "p", 77)
		require.ErrorContains(t, err, "still active after revocation")
		assert.Equal(t, []int{5, 6}, pats.revoked, "every active bootstrap token is attempted")
	})

	t.Run("bootstrap creation stops when orphan revocation fails", func(t *testing.T) {
		pats := &patFixture{tokens: active(), revokeStatus: http.StatusForbidden}
		mux := http.NewServeMux()
		pats.register(t, mux)
		srv := httptest.NewServer(mux)
		defer srv.Close()
		boot, err := newTestOwner(t, srv.URL).CreatePollerBootstrap(ctx, "g", "p", 77)
		require.Error(t, err)
		assert.Nil(t, boot)
		assert.Empty(t, pats.created)
	})

	t.Run("bootstrap creation failure", func(t *testing.T) {
		pats := &patFixture{tokens: []map[string]any{}, createStatus: http.StatusForbidden}
		mux := http.NewServeMux()
		pats.register(t, mux)
		srv := httptest.NewServer(mux)
		defer srv.Close()
		boot, err := newTestOwner(t, srv.URL).CreatePollerBootstrap(ctx, "g", "p", 77)
		require.Error(t, err)
		assert.Nil(t, boot)
	})
}

func TestTriggerOwnerRuntimeCredentialLifecycle(t *testing.T) {
	ctx := context.Background()
	pats := &patFixture{nextID: 20, tokens: []map[string]any{
		{"id": 1, "name": gitlabroles.PollerTokenName, "active": true},
		{"id": 2, "name": gitlabroles.PollerBootstrapTokenName, "active": true},
		{"id": 3, "name": "unrelated", "active": true},
	}}
	mux := http.NewServeMux()
	pats.register(t, mux)
	var order []string
	deleteStatus := http.StatusNoContent
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		order = append(order, "delete-secret")
		w.WriteHeader(deleteStatus)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	to := newTestOwner(t, srv.URL)

	// The secret is removed before the managed runtime tokens are revoked, and
	// the bootstrap and unmanaged tokens are left alone.
	require.NoError(t, to.RevokePollerRuntimeCredentials(ctx, "g", "p", 77))
	assert.Equal(t, []string{"delete-secret"}, order)
	assert.Equal(t, []int{1}, pats.revoked)

	tok, err := to.CreatePollerRuntimeToken(ctx, "g", "p", 77, "2027-01-01")
	require.NoError(t, err)
	assert.Equal(t, &repos.PollerRuntimeToken{ID: 21, Token: "glpat-new-21"}, tok)
	require.Len(t, pats.created, 1)
	assert.Equal(t, gitlabroles.PollerTokenName, pats.created[0]["name"])
	assert.Equal(t, "2027-01-01", pats.created[0]["expires_at"])

	// A secret that cannot be removed stops before any token is revoked.
	deleteStatus = http.StatusForbidden
	pats.revoked = nil
	err = to.RevokePollerRuntimeCredentials(ctx, "g", "p", 77)
	require.ErrorContains(t, err, forge.SecretGitLabPollerToken)
	assert.Empty(t, pats.revoked)

	pats.createStatus = http.StatusForbidden
	tok, err = to.CreatePollerRuntimeToken(ctx, "g", "p", 77, "2027-01-01")
	require.Error(t, err)
	assert.Nil(t, tok)
}

func TestTriggerOwnerSetProjectMemberAccessLevel(t *testing.T) {
	var got map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/members/77", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "admin-token", r.Header.Get("PRIVATE-TOKEN"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	require.NoError(t, newTestOwner(t, srv.URL).SetProjectMemberAccessLevel(context.Background(), "g", "p", 77, forge.GitLabAccessLevelDeveloper))
	assert.Equal(t, float64(forge.GitLabAccessLevelDeveloper), got["access_level"])
}

// After runtime revocation even managed token names are not accepted.
func TestTriggerOwnerVerifyPollerCredentialsRevoked(t *testing.T) {
	for name, tc := range map[string]struct {
		pats   []map[string]any
		unsafe bool
	}{
		"managed runtime token still active": {pats: []map[string]any{{"id": 5, "name": gitlabroles.PollerTokenName, "active": true}}, unsafe: true},
		"every token revoked":                {pats: []map[string]any{{"id": 5, "name": gitlabroles.PollerTokenName, "active": false, "revoked": true}}},
	} {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, tc.pats)
			})
			for _, path := range []string{"/api/v4/projects/g%2Fp/triggers", "/api/v4/users/77/keys", "/api/v4/projects/g%2Fp/jobs", "/api/v4/projects/g%2Fp/pipeline_schedules"} {
				mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
					writeTestJSON(t, w, http.StatusOK, []map[string]any{})
				})
			}
			srv := httptest.NewServer(mux)
			defer srv.Close()
			err := newTestOwner(t, srv.URL).VerifyPollerCredentialsRevoked(context.Background(), "g", "p", 77)
			var unsafe *repos.PollerElevationUnsafeError
			if tc.unsafe {
				require.ErrorAs(t, err, &unsafe)
				assert.Contains(t, unsafe.Reason, "after revocation")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRoleTokenName(t *testing.T) {
	assert.Equal(t, gitlabroles.PollerTokenName, RoleTokenName(gitlabroles.RolePoller))
	assert.Equal(t, gitlabroles.AnalystTokenName, RoleTokenName(gitlabroles.RoleAnalyst))
	assert.Equal(t, gitlabroles.CoderTokenName, RoleTokenName(gitlabroles.RoleCoder))
	assert.Equal(t, gitlabroles.CustomTokenName("scanner"), RoleTokenName("scanner"))
}

func TestTriggerOwnerSuppliedTokenIDs(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()
	// The analyst and coder credentials share owner 55, but the coder's token
	// ID was never recorded, so every token of that account stands in for the
	// credential and the owner is omitted.
	handleRotationState(t, mux, "g%2Fp", `{"roles":{`+
		`"poller":{"supplied":true,"supplied_user_id":90,"supplied_token_id":11},`+
		`"analyst":{"supplied":true,"supplied_user_id":55,"supplied_token_id":12},`+
		`"coder":{"supplied":true,"supplied_user_id":55},`+
		`"scanner":{"supplied":true,"supplied_user_id":60,"supplied_token_id":13},`+
		`"triage":{"phase":"idle","incoming_id":14}}}`)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := newTestOwner(t, srv.URL).SuppliedTokenIDs(ctx, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, map[int][]repos.SuppliedTokenRef{
		90: {{ID: 11, Name: gitlabroles.PollerTokenName}},
		60: {{ID: 13, Name: gitlabroles.CustomTokenName("scanner")}},
	}, got)

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer failing.Close()
	got, err = newTestOwner(t, failing.URL).SuppliedTokenIDs(ctx, "g", "p")
	require.Error(t, err)
	assert.Nil(t, got)
}

// A supplied non-Poller credential enrolled without a recorded owner is
// attributed by authenticating with the installed secret; anything that
// prevents attribution fails closed.
func TestTriggerOwnerAttributeSuppliedRole(t *testing.T) {
	ctx := context.Background()
	const analystSupplied = `{"roles":{"poller":{"phase":"idle","incoming_id":7},"analyst":{"supplied":true,"phase":"idle","distributed_at":"2026-01-01T00:00:00Z"}}}`
	for name, tc := range map[string]struct {
		state          string
		registryStatus int
		registry       string
		secretStatus   int
		userStatus     int
		newClientErr   bool
		want           string
	}{
		"attributed":             {state: analystSupplied},
		"registry unreadable":    {state: analystSupplied, registryStatus: http.StatusForbidden, want: "reading the role registry"},
		"registry invalid":       {state: analystSupplied, registry: "{not json", want: "parsing the role registry"},
		"secret unreadable":      {state: analystSupplied, secretStatus: http.StatusForbidden, want: "reading \"" + forge.SecretGitLabAnalystToken + "\""},
		"secret not installed":   {state: analystSupplied, secretStatus: http.StatusNotFound, want: "no longer installed"},
		"credential rejected":    {state: analystSupplied, userStatus: http.StatusUnauthorized, want: "cannot be attributed"},
		"client build fails":     {state: analystSupplied, newClientErr: true, want: "building a GitLab client"},
		"role is not registered": {state: `{"roles":{"poller":{"phase":"idle","incoming_id":7},"ghost":{"supplied":true}}}`, want: "not registered"},
	} {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			handleRotationState(t, mux, "g%2Fp", tc.state)
			mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.VarGitLabRoleRegistry, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case tc.registryStatus != 0:
					w.WriteHeader(tc.registryStatus)
				case tc.registry != "":
					writeTestJSON(t, w, http.StatusOK, map[string]any{"value": tc.registry})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabAnalystToken, func(w http.ResponseWriter, r *http.Request) {
				if tc.secretStatus != 0 {
					w.WriteHeader(tc.secretStatus)
					return
				}
				writeTestJSON(t, w, http.StatusOK, map[string]any{"value": "glpat-analyst-supplied"})
			})
			mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "glpat-analyst-supplied", r.Header.Get("PRIVATE-TOKEN"))
				if tc.userStatus != 0 {
					w.WriteHeader(tc.userStatus)
					return
				}
				writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 55})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			to := newTestOwner(t, srv.URL)
			if tc.newClientErr {
				to.NewClient = func(string, string) (*gitlab.LiveClient, error) { return nil, assert.AnError }
			}

			excluded, attributed, err := to.ResolveExcludedAccounts(ctx, "g", "p")
			if tc.want != "" {
				require.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)
				assert.Contains(t, err.Error(), tc.want)
				assert.NotContains(t, err.Error(), "glpat-analyst-supplied")
				assert.Nil(t, excluded)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []int64{55}, excluded)
			assert.Equal(t, map[gitlabroles.Role]int{gitlabroles.RoleAnalyst: 55}, attributed)
			current, err := to.CurrentSuppliedAccountIDs(ctx, "g", "p")
			require.NoError(t, err)
			assert.Equal(t, []int{55}, current)
		})
	}
}
