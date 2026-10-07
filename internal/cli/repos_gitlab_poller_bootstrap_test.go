package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
)

// pollerAPI is a stateful GitLab stand-in for the Poller service account's
// personal access tokens and the installed runtime secret.
type pollerAPI struct {
	t  *testing.T
	mu sync.Mutex
	// pats are the account's tokens; revoking one clears active.
	pats []map[string]any
	// log records "METHOD path" for every mutating request, in order.
	log []string
	// secretPresent reports whether the runtime secret variable exists.
	secretPresent bool
	// secretDeleteStatus overrides the variable DELETE status when set.
	secretDeleteStatus int
	// revokeIgnored makes PAT revocation a no-op, so verification fails.
	revokeIgnored bool
	// created holds the last PAT creation body.
	created map[string]any
}

func newPollerAPI(t *testing.T, pats ...map[string]any) (*pollerAPI, *gitlabPollerTriggerOwner) {
	t.Helper()
	if pats == nil {
		pats = []map[string]any{}
	}
	api := &pollerAPI{t: t, pats: pats, secretPresent: true}
	mux := http.NewServeMux()
	handleRotationState(t, mux, "g%2Fp", managedPollerState)
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		switch r.Method {
		case http.MethodDelete:
			api.log = append(api.log, "DELETE variable")
			if api.secretDeleteStatus != 0 {
				w.WriteHeader(api.secretDeleteStatus)
				return
			}
			api.secretPresent = false
			w.WriteHeader(http.StatusNoContent)
		default:
			if !api.secretPresent {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabPollerToken, "value": "runtime-secret-value"})
		}
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 90, "name": "fullsend-poller"}, {"id": 77, "name": "fullsend-poller"}})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		if r.Method == http.MethodPost {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			api.created = body
			api.log = append(api.log, "POST pat "+body["name"].(string))
			tok := map[string]any{"id": 500, "name": body["name"], "active": true, "token": "created-pat-value"}
			api.pats = append(api.pats, tok)
			writeTestJSON(t, w, http.StatusCreated, tok)
			return
		}
		writeTestJSON(t, w, http.StatusOK, api.pats)
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens/", func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		assert.Equal(t, "admin-token", r.Header.Get("PRIVATE-TOKEN"), "revocation uses installer authority")
		api.log = append(api.log, "DELETE "+r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		if !api.revokeIgnored {
			for _, p := range api.pats {
				if int(p["id"].(int)) == atoiTail(t, r.URL.Path) {
					p["active"] = false
					p["revoked"] = true
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	to := newGitLabPollerTriggerOwner(admin)
	to.Now = func() time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC) }
	return api, to
}

func atoiTail(t *testing.T, path string) int {
	t.Helper()
	tail := path[strings.LastIndex(path, "/")+1:]
	n := 0
	for _, r := range tail {
		require.True(t, r >= '0' && r <= '9', "token ID in %s", path)
		n = n*10 + int(r-'0')
	}
	return n
}

func activeNames(api *pollerAPI) []string {
	api.mu.Lock()
	defer api.mu.Unlock()
	var names []string
	for _, p := range api.pats {
		if p["active"] == true {
			names = append(names, p["name"].(string))
		}
	}
	return names
}

// The runtime credential is invalidated, not merely hidden: the variable is
// removed first (so an interrupted run leaves the secret absent), then every
// managed runtime token is revoked and verified. Bootstrap tokens and
// credentials fullsend does not manage are left alone.
func TestGitLabPollerTriggerOwner_RevokePollerRuntimeCredentials(t *testing.T) {
	ctx := context.Background()
	api, to := newPollerAPI(t,
		map[string]any{"id": 1, "name": "fullsend-poller", "active": true},
		map[string]any{"id": 2, "name": "fullsend-poller", "active": true},
		map[string]any{"id": 3, "name": "fullsend-poller", "active": false, "revoked": true},
		map[string]any{"id": 4, "name": "fullsend-poller-bootstrap", "active": true},
		map[string]any{"id": 5, "name": "unrelated", "active": true},
	)

	require.NoError(t, to.RevokePollerRuntimeCredentials(ctx, "g", "p", 77))

	assert.Equal(t, []string{"DELETE variable", "DELETE 1", "DELETE 2"}, api.log, "the secret is removed first; only active managed runtime tokens are revoked")
	assert.ElementsMatch(t, []string{"fullsend-poller-bootstrap", "unrelated"}, activeNames(api))
}

// Deleting the variable is not enough: a token that stays active after the
// revocation requests fails the transaction.
func TestGitLabPollerTriggerOwner_RevokePollerRuntimeCredentials_Unverified(t *testing.T) {
	ctx := context.Background()
	api, to := newPollerAPI(t, map[string]any{"id": 1, "name": "fullsend-poller", "active": true})
	api.revokeIgnored = true

	err := to.RevokePollerRuntimeCredentials(ctx, "g", "p", 77)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still active after revocation")
}

// A variable that cannot be removed stops the revocation before any token is
// touched, so the state is unchanged.
func TestGitLabPollerTriggerOwner_RevokePollerRuntimeCredentials_VariableDeleteFails(t *testing.T) {
	ctx := context.Background()
	api, to := newPollerAPI(t, map[string]any{"id": 1, "name": "fullsend-poller", "active": true})
	api.secretDeleteStatus = http.StatusBadRequest // not retried by the client

	err := to.RevokePollerRuntimeCredentials(ctx, "g", "p", 77)
	require.Error(t, err)
	assert.Equal(t, []string{"DELETE variable"}, api.log)
	assert.Equal(t, []string{"fullsend-poller"}, activeNames(api))
}

// Orphaned bootstrap credentials are found by name and revoked; no secret
// value is needed.
func TestGitLabPollerTriggerOwner_RevokePollerBootstrap(t *testing.T) {
	ctx := context.Background()
	api, to := newPollerAPI(t,
		map[string]any{"id": 1, "name": "fullsend-poller", "active": true},
		map[string]any{"id": 4, "name": "fullsend-poller-bootstrap", "active": true},
		map[string]any{"id": 6, "name": "fullsend-poller-bootstrap", "active": true},
		map[string]any{"id": 7, "name": "fullsend-poller-bootstrap", "active": false, "revoked": true},
	)

	require.NoError(t, to.RevokePollerBootstrap(ctx, "g", "p", 77))
	assert.Equal(t, []string{"DELETE 4", "DELETE 6"}, api.log)
	assert.Equal(t, []string{"fullsend-poller"}, activeNames(api), "the runtime credential is untouched")

	// Nothing to do the second time.
	api.log = nil
	require.NoError(t, to.RevokePollerBootstrap(ctx, "g", "p", 77))
	assert.Empty(t, api.log)

	// An unverifiable revocation is an error.
	api.pats = append(api.pats, map[string]any{"id": 8, "name": "fullsend-poller-bootstrap", "active": true})
	api.revokeIgnored = true
	assert.ErrorContains(t, to.RevokePollerBootstrap(ctx, "g", "p", 77), "still active after revocation")
}

// The bootstrap credential is created with installer authority, named for
// its purpose, short-lived, and never alongside an orphan.
func TestGitLabPollerTriggerOwner_CreatePollerBootstrap(t *testing.T) {
	ctx := context.Background()
	api, to := newPollerAPI(t, map[string]any{"id": 4, "name": "fullsend-poller-bootstrap", "active": true})

	boot, err := to.CreatePollerBootstrap(ctx, "g", "p", 77)
	require.NoError(t, err)
	assert.Equal(t, 500, boot.ID)
	assert.Equal(t, "created-pat-value", boot.Token)
	assert.Equal(t, []string{"DELETE 4", "POST pat fullsend-poller-bootstrap"}, api.log, "an orphan is revoked before the new credential is created")
	assert.Equal(t, "fullsend-poller-bootstrap", api.created["name"])
	assert.Equal(t, []any{"api"}, api.created["scopes"])
	assert.Equal(t, "2026-03-03", api.created["expires_at"], "the bootstrap credential is short-lived")

	// The runtime variable is never written.
	assert.NotContains(t, strings.Join(api.log, "\n"), "variable")
}

// A bootstrap credential is not created when an orphan cannot be revoked.
func TestGitLabPollerTriggerOwner_CreatePollerBootstrap_OrphanUnrevokable(t *testing.T) {
	api, to := newPollerAPI(t, map[string]any{"id": 4, "name": "fullsend-poller-bootstrap", "active": true})
	api.revokeIgnored = true

	_, err := to.CreatePollerBootstrap(context.Background(), "g", "p", 77)
	require.Error(t, err)
	assert.NotContains(t, strings.Join(api.log, "\n"), "POST")
}

func TestGitLabPollerTriggerOwner_CreatePollerRuntimeToken(t *testing.T) {
	api, to := newPollerAPI(t)

	tok, err := to.CreatePollerRuntimeToken(context.Background(), "g", "p", 77, "2027-03-01")
	require.NoError(t, err)
	assert.Equal(t, 500, tok.ID)
	assert.Equal(t, "created-pat-value", tok.Token)
	assert.Equal(t, "fullsend-poller", api.created["name"])
	assert.Equal(t, "2027-03-01", api.created["expires_at"])
}

// With the distributed credential gone, identification uses installer
// authority alone, so an interrupted run can be recovered.
func TestGitLabPollerTriggerOwner_PollerUserIDWithoutRuntimeSecret(t *testing.T) {
	api, to := newPollerAPI(t)
	api.secretPresent = false

	uid, err := to.PollerUserID(context.Background(), "g", "p")
	require.NoError(t, err)
	assert.Equal(t, int64(77), uid, "the durably recorded Poller service account is used")
}

// An installed credential that authenticates as another identity is an
// administrator-supplied one: it is never replaced or its owner elevated.
func TestGitLabPollerTriggerOwner_PollerUserIDAdministratorSuppliedCredential(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"value": "human-token-value"})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 5})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 77, "name": "fullsend-poller"}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	_, err = newGitLabPollerTriggerOwner(admin).PollerUserID(context.Background(), "g", "p")
	assert.ErrorIs(t, err, forge.ErrNotFound)
}
