package gitlablifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// instantAfter replaces the GitLab client's retry delay so tests that exercise
// retryable server errors do not wait for real backoff.
func instantAfter(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

// newTestClient builds a GitLab client for a test server without retry delays.
func newTestClient(token, baseURL string) (*gitlab.LiveClient, error) {
	return gitlab.New(token, gitlab.WithBaseURL(baseURL), gitlab.WithAfterFunc(instantAfter))
}

// newTestOwner is NewTriggerOwner whose admin and per-credential clients skip
// retry delays.
func newTestOwner(t *testing.T, baseURL string) *TriggerOwner {
	t.Helper()
	admin, err := newTestClient("admin-token", baseURL)
	require.NoError(t, err)
	to := NewTriggerOwner(admin)
	to.NewClient = newTestClient
	return to
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

func handleRotationState(t *testing.T, mux *http.ServeMux, project, state string) {
	t.Helper()
	mux.HandleFunc("/api/v4/projects/"+project+"/variables/"+forge.VarGitLabRoleRotation, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"value": state})
	})
}

const managedPollerState = `{"roles":{"poller":{"phase":"idle","incoming_id":5,"managed_user_id":77,"distributed_at":"2026-01-01T00:00:00Z"}}}`

func TestGitLabPollerTriggerOwner_ContainPoller(t *testing.T) {
	ctx := context.Background()
	var revoked []string
	var deletedVars []string
	patsStatus := http.StatusOK
	unmanaged := true // token 3 is an active token fullsend does not manage
	// revokeIneffective models a DELETE that succeeds while the token stays
	// active; otherwise a token listed in revoked is inactive afterwards.
	revokeIneffective := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		active := func(id int) bool {
			if revokeIneffective {
				return true
			}
			return !slices.Contains(revoked, fmt.Sprintf("/api/v4/projects/g/p/service_accounts/77/personal_access_tokens/%d", id))
		}
		toks := []map[string]any{
			{"id": 1, "name": gitlabroles.PollerTokenName, "active": active(1)},
			{"id": 2, "name": gitlabroles.PollerTokenName, "active": false, "revoked": true},
			{"id": 3, "name": "unrelated", "active": unmanaged, "revoked": !unmanaged},
			{"id": 4, "name": gitlabroles.PollerBootstrapTokenName, "active": active(4)},
		}
		writeTestJSON(t, w, patsStatus, toks)
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "admin-token", r.Header.Get("PRIVATE-TOKEN"))
		revoked = append(revoked, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	unmanagedTrigger := false // a Poller-owned trigger that fullsend does not manage
	mux.HandleFunc("/api/v4/projects/g%2Fp/triggers", func(w http.ResponseWriter, r *http.Request) {
		triggers := []map[string]any{
			{"id": 20, "description": repos.GitLabWebhookTriggerDescription, "owner": map[string]any{"id": 77}},
			{"id": 21, "description": "someone else's", "owner": map[string]any{"id": 5}},
		}
		if unmanagedTrigger {
			triggers = append(triggers, map[string]any{"id": 22, "description": "manual", "owner": map[string]any{"id": 77}})
		}
		writeTestJSON(t, w, http.StatusOK, triggers)
	})
	// Authentication paths that revoking tokens and triggers does not end:
	// SSH keys, unfinished jobs, and Poller-owned schedules. Each inventory
	// can survive or fail independently.
	var survivingKeys, survivingJobs, survivingSchedules []map[string]any
	keysStatus, jobsStatus, schedulesStatus := http.StatusOK, http.StatusOK, http.StatusOK
	listOf := func(items []map[string]any) []map[string]any {
		if items == nil {
			return []map[string]any{}
		}
		return items
	}
	mux.HandleFunc("/api/v4/users/77/keys", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, keysStatus, listOf(survivingKeys))
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/jobs", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, jobsStatus, listOf(survivingJobs))
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/pipeline_schedules", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, schedulesStatus, listOf(survivingSchedules))
	})
	// The installed Poller credential authenticates as installedUser; the
	// secret is attributed before any of the account's tokens are revoked.
	installedUser := 77
	secretStatus := http.StatusOK
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "admin-token", r.Header.Get("PRIVATE-TOKEN"))
		if r.Method == http.MethodGet {
			writeTestJSON(t, w, secretStatus, map[string]any{"key": forge.SecretGitLabPollerToken, "value": "glpat-installed"})
			return
		}
		assert.Equal(t, http.MethodDelete, r.Method)
		deletedVars = append(deletedVars, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	userStatus := http.StatusOK
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "glpat-installed", r.Header.Get("PRIVATE-TOKEN"))
		writeTestJSON(t, w, userStatus, map[string]any{"id": installedUser})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	to := newTestOwner(t, srv.URL)

	// An active token that fullsend does not manage is left untouched, but
	// containment is reported as incomplete.
	err := to.ContainPoller(ctx, "g", "p", 77)
	require.ErrorContains(t, err, "containment is incomplete")
	assert.Equal(t, []string{
		"/api/v4/projects/g/p/service_accounts/77/personal_access_tokens/1",
		"/api/v4/projects/g/p/service_accounts/77/personal_access_tokens/4",
	}, revoked, "only the active managed runtime and bootstrap Poller tokens are revoked")
	assert.Len(t, deletedVars, 1, "the installed Poller secret is removed")

	// Without an unmanaged active token containment succeeds.
	unmanaged = false
	revoked, deletedVars = nil, nil
	require.NoError(t, to.ContainPoller(ctx, "g", "p", 77))
	assert.Len(t, revoked, 2)

	// A Poller-owned pipeline trigger that fullsend does not manage keeps the
	// Poller's role, so containment is reported as incomplete. Managed
	// triggers (revoked by the webhook teardown) and other owners' triggers
	// are not reported.
	unmanagedTrigger = true
	revoked, deletedVars = nil, nil
	err = to.ContainPoller(ctx, "g", "p", 77)
	require.ErrorContains(t, err, "containment is incomplete")
	assert.ErrorContains(t, err, "[22]")
	assert.NotContains(t, err.Error(), "20")
	assert.Len(t, deletedVars, 1, "the installed Poller secret is still removed")
	unmanagedTrigger = false

	// Surviving SSH keys, unfinished Poller jobs, and Poller-owned schedules
	// each leave the account usable, so containment is incomplete and names
	// the resource; administrator-owned resources are never deleted, and the
	// installed secret is still removed. An inventory that cannot be read is
	// equally incomplete.
	for name, tc := range map[string]struct {
		set  func()
		want string
	}{
		"ssh key":            {set: func() { survivingKeys = []map[string]any{{"id": 31, "usage_type": "auth"}} }, want: "SSH authentication key(s) [31]"},
		"job":                {set: func() { survivingJobs = []map[string]any{{"id": 41, "user": map[string]any{"id": 77}}} }, want: "job(s) [41]"},
		"schedule":           {set: func() { survivingSchedules = []map[string]any{{"id": 51, "owner": map[string]any{"id": 77}}} }, want: "pipeline schedule(s) [51]"},
		"ssh key inventory":  {set: func() { keysStatus = http.StatusForbidden }, want: "listing Poller SSH keys"},
		"job inventory":      {set: func() { jobsStatus = http.StatusForbidden }, want: "listing Poller jobs"},
		"schedule inventory": {set: func() { schedulesStatus = http.StatusForbidden }, want: "listing Poller schedules"},
	} {
		survivingKeys, survivingJobs, survivingSchedules = nil, nil, nil
		keysStatus, jobsStatus, schedulesStatus = http.StatusOK, http.StatusOK, http.StatusOK
		tc.set()
		revoked, deletedVars = nil, nil
		err = to.ContainPoller(ctx, "g", "p", 77)
		require.ErrorContains(t, err, "containment is incomplete", name)
		assert.ErrorContains(t, err, tc.want, name)
		assert.Len(t, deletedVars, 1, "%s: the installed Poller secret is still removed", name)
	}
	survivingKeys, survivingJobs, survivingSchedules = nil, nil, nil
	keysStatus, jobsStatus, schedulesStatus = http.StatusOK, http.StatusOK, http.StatusOK

	// A revocation that succeeds while the managed tokens stay active is
	// not containment.
	revokeIneffective = true
	revoked, deletedVars = nil, nil
	err = to.ContainPoller(ctx, "g", "p", 77)
	require.ErrorContains(t, err, "containment is incomplete")
	assert.ErrorContains(t, err, "[1 4]")
	assert.ErrorContains(t, err, "still active after revocation")
	revokeIneffective = false

	// Containment runs on its own budgets, so an already-expired caller
	// context does not stop revocation or secret removal.
	revoked, deletedVars = nil, nil
	expired, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, to.ContainPoller(expired, "g", "p", 77))
	assert.Len(t, revoked, 2)
	assert.Len(t, deletedVars, 1)

	// A failed token listing is reported, and the secret is still removed.
	revoked, deletedVars = nil, nil
	patsStatus = http.StatusInternalServerError
	err = to.ContainPoller(ctx, "g", "p", 77)
	require.ErrorContains(t, err, "listing Poller service account tokens")
	assert.Empty(t, revoked)
	assert.Len(t, deletedVars, 1)
	patsStatus = http.StatusOK

	// An installed credential of another identity (the selected account when
	// an unselected duplicate fails containment, or an administrator-supplied
	// credential) is preserved while the contained account's tokens are
	// still revoked.
	revoked, deletedVars = nil, nil
	installedUser = 78
	require.NoError(t, to.ContainPoller(ctx, "g", "p", 77))
	assert.Len(t, revoked, 2, "the contained account's managed tokens are revoked")
	assert.Empty(t, deletedVars, "another identity's installed credential is not removed")

	// A credential GitLab already rejects is inert and is left in place.
	installedUser = 77
	userStatus = http.StatusUnauthorized
	deletedVars = nil
	require.NoError(t, to.ContainPoller(ctx, "g", "p", 77))
	assert.Empty(t, deletedVars)

	// A credential that cannot be attributed is reported and preserved.
	userStatus = http.StatusInternalServerError
	deletedVars = nil
	err = to.ContainPoller(ctx, "g", "p", 77)
	require.ErrorContains(t, err, "attributing")
	assert.Empty(t, deletedVars)
	userStatus = http.StatusOK

	// An unreadable secret is reported and preserved.
	secretStatus = http.StatusForbidden
	deletedVars = nil
	err = to.ContainPoller(ctx, "g", "p", 77)
	require.ErrorContains(t, err, "attributing the installed Poller credential")
	assert.Empty(t, deletedVars)
}

func TestGitLabPollerTriggerOwner_VerifyPollerElevationSafe(t *testing.T) {
	ctx := context.Background()
	safePATs := []map[string]any{
		{"id": 5, "name": gitlabroles.PollerTokenName, "active": true},
		{"id": 6, "name": gitlabroles.PollerTokenName, "active": false, "revoked": true},
	}
	for name, tc := range map[string]struct {
		pats            []map[string]any
		patsStatus      int
		triggers        []map[string]any
		trigStatus      int
		keys            []map[string]any
		keysStatus      int
		jobs            []map[string]any
		jobsStatus      int
		schedules       []map[string]any
		schedulesStatus int
		unsafe          string // expected PollerElevationUnsafeError text; empty means safe or a plain error
		plainErr        bool
	}{
		"safe": {pats: safePATs},
		"the installer's bootstrap credential is recognized": {pats: append([]map[string]any{
			{"id": 7, "name": gitlabroles.PollerBootstrapTokenName, "active": true}}, safePATs...)},
		"another active PAT": {unsafe: "does not manage", pats: append([]map[string]any{
			{"id": 9, "name": "manual", "active": true}}, safePATs...)},
		"PAT listing fails": {patsStatus: http.StatusInternalServerError, plainErr: true},
		"unrelated Poller-owned trigger": {pats: safePATs, unsafe: "[31]", triggers: []map[string]any{
			{"id": 31, "description": "manual", "owner": map[string]any{"id": 77}}}},
		"trigger without a known owner": {pats: safePATs, unsafe: "[32]", triggers: []map[string]any{
			{"id": 32, "description": "legacy", "owner": nil}}},
		"managed and foreign triggers are fine": {pats: safePATs, triggers: []map[string]any{
			{"id": 33, "description": repos.GitLabWebhookTriggerDescription, "owner": map[string]any{"id": 77}},
			{"id": 34, "description": "manual", "owner": map[string]any{"id": 5}}}},
		"trigger listing fails": {pats: safePATs, trigStatus: http.StatusInternalServerError, plainErr: true},
		"an SSH authentication key": {pats: safePATs, unsafe: "SSH authentication key(s) [41]", keys: []map[string]any{
			{"id": 41, "usage_type": "auth_and_signing"}}},
		"an SSH key from an older GitLab without a usage type": {pats: safePATs, unsafe: "[42]", keys: []map[string]any{
			{"id": 42}}},
		"an SSH key that has not expired": {pats: safePATs, unsafe: "[43]", keys: []map[string]any{
			{"id": 43, "usage_type": "auth", "expires_at": "2999-01-01T00:00:00Z"}}},
		"signing-only and expired SSH keys are fine": {pats: safePATs, keys: []map[string]any{
			{"id": 44, "usage_type": "signing"},
			{"id": 45, "usage_type": "auth", "expires_at": "2000-01-01T00:00:00Z"}}},
		"SSH key listing fails": {pats: safePATs, keysStatus: http.StatusForbidden, plainErr: true},
		"a running Poller job": {pats: safePATs, unsafe: "job(s) [51]", jobs: []map[string]any{
			{"id": 51, "status": "running", "user": map[string]any{"id": 77}}}},
		"a job without a reported user": {pats: safePATs, unsafe: "[52]", jobs: []map[string]any{
			{"id": 52, "status": "pending", "user": nil}}},
		"jobs of other users are fine": {pats: safePATs, jobs: []map[string]any{
			{"id": 53, "status": "running", "user": map[string]any{"id": 5}}}},
		"a delayed Poller job": {pats: safePATs, unsafe: "job(s) [54]", jobs: []map[string]any{
			{"id": 54, "status": "scheduled", "user": map[string]any{"id": 77}}}},
		"a Poller job waiting for a callback": {pats: safePATs, unsafe: "job(s) [55]", jobs: []map[string]any{
			{"id": 55, "status": "waiting_for_callback", "user": map[string]any{"id": 77}}}},
		"job listing fails": {pats: safePATs, jobsStatus: http.StatusForbidden, plainErr: true},
		"a Poller-owned pipeline schedule": {pats: safePATs, unsafe: "pipeline schedule(s) [61]", schedules: []map[string]any{
			{"id": 61, "owner": map[string]any{"id": 77}}}},
		"a schedule without a known owner": {pats: safePATs, unsafe: "[62]", schedules: []map[string]any{
			{"id": 62, "owner": nil}}},
		"schedules of other users are fine": {pats: safePATs, schedules: []map[string]any{
			{"id": 63, "owner": map[string]any{"id": 5}}}},
		"schedule listing fails": {pats: safePATs, schedulesStatus: http.StatusForbidden, plainErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
				status := http.StatusOK
				if tc.patsStatus != 0 {
					status = tc.patsStatus
				}
				writeTestJSON(t, w, status, tc.pats)
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/triggers", func(w http.ResponseWriter, r *http.Request) {
				status := http.StatusOK
				if tc.trigStatus != 0 {
					status = tc.trigStatus
				}
				triggers := tc.triggers
				if triggers == nil {
					triggers = []map[string]any{}
				}
				writeTestJSON(t, w, status, triggers)
			})
			mux.HandleFunc("/api/v4/users/77/keys", func(w http.ResponseWriter, r *http.Request) {
				status := http.StatusOK
				if tc.keysStatus != 0 {
					status = tc.keysStatus
				}
				keys := tc.keys
				if keys == nil {
					keys = []map[string]any{}
				}
				writeTestJSON(t, w, status, keys)
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/jobs", func(w http.ResponseWriter, r *http.Request) {
				// GitLab rejects the whole request when any scope value is not
				// one of its job statuses, so the fixture does the same and
				// the complete requested set is pinned: every unfinished
				// state, and nothing finished.
				known := map[string]bool{"created": true, "waiting_for_resource": true, "preparing": true, "waiting_for_callback": true, "pending": true, "running": true, "canceling": true, "success": true, "failed": true, "canceled": true, "skipped": true, "manual": true, "scheduled": true}
				got := r.URL.Query()["scope[]"]
				for _, scope := range got {
					if !known[scope] {
						writeTestJSON(t, w, http.StatusBadRequest, map[string]any{"error": "scope does not have a valid value"})
						return
					}
				}
				assert.ElementsMatch(t, []string{"created", "scheduled", "pending", "preparing", "waiting_for_resource", "waiting_for_callback", "running", "canceling"}, got, "exactly the unfinished job states are listed, including delayed and canceling jobs")
				status := http.StatusOK
				if tc.jobsStatus != 0 {
					status = tc.jobsStatus
				}
				jobs := tc.jobs
				if jobs == nil {
					jobs = []map[string]any{}
				}
				writeTestJSON(t, w, status, jobs)
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/pipeline_schedules", func(w http.ResponseWriter, r *http.Request) {
				status := http.StatusOK
				if tc.schedulesStatus != 0 {
					status = tc.schedulesStatus
				}
				schedules := tc.schedules
				if schedules == nil {
					schedules = []map[string]any{}
				}
				writeTestJSON(t, w, status, schedules)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			err := newTestOwner(t, srv.URL).VerifyPollerElevationSafe(ctx, "g", "p", 77)
			var unsafe *repos.PollerElevationUnsafeError
			switch {
			case tc.unsafe != "":
				require.ErrorAs(t, err, &unsafe)
				assert.Contains(t, unsafe.Reason, tc.unsafe)
			case tc.plainErr:
				require.Error(t, err)
				assert.NotErrorAs(t, err, &unsafe)
			default:
				require.NoError(t, err)
			}
		})
	}
}

func TestGitLabPollerTriggerOwner_Errors(t *testing.T) {
	ctx := context.Background()

	t.Run("poller token missing is not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)
		to := NewTriggerOwner(admin)
		_, err = to.PollerUserID(ctx, "g", "p")
		assert.ErrorIs(t, err, forge.ErrNotFound)
		_, err = to.CreatePipelineTriggerTokenAsPoller(ctx, "g", "p", "d", nil)
		assert.Error(t, err, "a trigger is never created without the bootstrap credential")
	})

	t.Run("variable read error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)
		_, err = NewTriggerOwner(admin).PollerUserID(ctx, "g", "p")
		require.Error(t, err)
		assert.False(t, forge.IsNotFound(err))
	})

	selfToken := map[string]any{"id": 5, "name": gitlabroles.PollerTokenName, "active": true, "user_id": 77}
	selfStatus := http.StatusOK
	pollerOwnerWithAccounts := func(t *testing.T, status int, accounts any) *TriggerOwner {
		t.Helper()
		mux := http.NewServeMux()
		handleRotationState(t, mux, "g%2Fp", managedPollerState)
		mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, selfStatus, selfToken)
		})
		mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, http.StatusOK, map[string]any{"value": "glpat-poller"})
		})
		mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77})
		})
		mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, status, accounts)
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return newTestOwner(t, srv.URL)
	}

	t.Run("human or bot poller is never elevated", func(t *testing.T) {
		to := pollerOwnerWithAccounts(t, http.StatusOK, []map[string]any{{"id": 78, "name": gitlabroles.PollerTokenName}})
		_, err := to.PollerUserID(ctx, "g", "p")
		assert.ErrorIs(t, err, forge.ErrNotFound)
	})

	t.Run("differently named poller token is never elevated", func(t *testing.T) {
		selfToken = map[string]any{"id": 6, "name": "ci-helper", "active": true, "user_id": 77}
		selfStatus = http.StatusOK
		t.Cleanup(func() {
			selfToken = map[string]any{"id": 5, "name": gitlabroles.PollerTokenName, "active": true, "user_id": 77}
		})
		to := pollerOwnerWithAccounts(t, http.StatusOK, []map[string]any{{"id": 77, "name": gitlabroles.PollerTokenName}})
		_, err := to.PollerUserID(ctx, "g", "p")
		assert.ErrorIs(t, err, forge.ErrNotFound)
	})

	t.Run("revoked poller token is never elevated", func(t *testing.T) {
		selfToken = map[string]any{"id": 5, "name": gitlabroles.PollerTokenName, "active": false, "revoked": true, "user_id": 77}
		t.Cleanup(func() {
			selfToken = map[string]any{"id": 5, "name": gitlabroles.PollerTokenName, "active": true, "user_id": 77}
		})
		to := pollerOwnerWithAccounts(t, http.StatusOK, []map[string]any{{"id": 77, "name": gitlabroles.PollerTokenName}})
		_, err := to.PollerUserID(ctx, "g", "p")
		assert.ErrorIs(t, err, forge.ErrNotFound)
	})

	t.Run("poller token lookup error is not a confirmed absence", func(t *testing.T) {
		selfStatus = http.StatusInternalServerError
		t.Cleanup(func() { selfStatus = http.StatusOK })
		to := pollerOwnerWithAccounts(t, http.StatusOK, []map[string]any{{"id": 77, "name": gitlabroles.PollerTokenName}})
		_, err := to.PollerUserID(ctx, "g", "p")
		require.Error(t, err)
		assert.False(t, forge.IsNotFound(err))
	})

	t.Run("service accounts absent", func(t *testing.T) {
		to := pollerOwnerWithAccounts(t, http.StatusNotFound, map[string]any{"message": "404"})
		_, err := to.PollerUserID(ctx, "g", "p")
		assert.ErrorIs(t, err, forge.ErrNotFound)
		_, err = to.ManagedPollerUserIDs(ctx, "g", "p")
		assert.ErrorIs(t, err, forge.ErrNotFound)
	})

	// A forbidden inventory cannot establish that no managed Poller exists,
	// so it must not read as a confirmed absence that skips reconciliation.
	t.Run("service accounts forbidden is not a confirmed absence", func(t *testing.T) {
		to := pollerOwnerWithAccounts(t, http.StatusForbidden, map[string]any{"message": "403"})
		_, err := to.PollerUserID(ctx, "g", "p")
		require.Error(t, err)
		assert.False(t, forge.IsNotFound(err))
		_, err = to.ManagedPollerUserIDs(ctx, "g", "p")
		require.Error(t, err)
		assert.False(t, forge.IsNotFound(err))
	})

	// GitLab returns 403 for plan gating as well as missing permissions, so
	// even an Owner installer's refused inventory is a confirmed absence only
	// when the complete project membership shows no managed Poller account. An
	// account an interrupted install left elevated is still a member, and an
	// unreadable membership proves nothing, so both keep the fail-closed
	// handling.
	for _, tc := range []struct {
		name          string
		membersStatus int
		members       []map[string]any
		wantNotFound  bool
	}{
		{"no poller member", http.StatusOK, []map[string]any{{"id": 5, "name": "installer", "access_level": forge.GitLabAccessLevelOwner}}, true},
		{"elevated poller member", http.StatusOK, []map[string]any{
			{"id": 5, "name": "installer", "access_level": forge.GitLabAccessLevelOwner},
			{"id": 9, "name": gitlabroles.PollerTokenName, "access_level": forge.GitLabAccessLevelMaintainer},
		}, false},
		{"membership unreadable", http.StatusForbidden, nil, false},
	} {
		t.Run("service accounts forbidden with "+tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/projects/g%2Fp/members/all", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, tc.membersStatus, tc.members)
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			to := NewTriggerOwner(admin)
			_, err = to.ManagedPollerUserIDs(ctx, "g", "p")
			require.Error(t, err)
			assert.Equal(t, tc.wantNotFound, forge.IsNotFound(err))
		})
	}

	// The 403 fallback creates a project access token bot named like the
	// Poller. On reinstall that Developer-level bot is a member with the
	// managed name, but the project's token inventory shows it owns a token, so
	// it is positively a legacy bot and not an unresolved service account.
	for _, tc := range []struct {
		name         string
		tokens       []map[string]any
		tokensStatus int
		wantNotFound bool
	}{
		{"legacy bot owning a project access token", []map[string]any{{"id": 3, "name": gitlabroles.PollerTokenName, "active": true, "user_id": 9}}, http.StatusOK, true},
		{"token owned by another user", []map[string]any{{"id": 3, "name": gitlabroles.PollerTokenName, "active": true, "user_id": 10}}, http.StatusOK, false},
		{"unreadable token inventory", nil, http.StatusForbidden, false},
	} {
		t.Run("service accounts forbidden with existing developer poller member and "+tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/projects/g%2Fp/members/all", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, []map[string]any{
					{"id": 5, "name": "installer", "access_level": forge.GitLabAccessLevelOwner},
					{"id": 9, "name": gitlabroles.PollerTokenName, "access_level": forge.GitLabAccessLevelDeveloper},
				})
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/access_tokens", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, tc.tokensStatus, tc.tokens)
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			_, err = NewTriggerOwner(admin).ManagedPollerUserIDs(ctx, "g", "p")
			require.Error(t, err)
			assert.Equal(t, tc.wantNotFound, forge.IsNotFound(err))
		})
	}

	t.Run("service account listing error", func(t *testing.T) {
		to := pollerOwnerWithAccounts(t, http.StatusBadRequest, map[string]any{"message": "400"})
		_, err := to.PollerUserID(ctx, "g", "p")
		require.Error(t, err)
		assert.False(t, forge.IsNotFound(err))
	})

	t.Run("poller user lookup error", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, http.StatusOK, map[string]any{"value": "glpat-poller"})
		})
		mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		_, err := newTestOwner(t, srv.URL).PollerUserID(ctx, "g", "p")
		require.Error(t, err)
		assert.False(t, forge.IsNotFound(err))
	})

	// An expired or revoked installed token cannot authenticate, but the
	// managed account is still identified through the administrator's
	// inventory so its membership can be reconciled and the credential
	// replaced.
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("rejected poller token (%d) falls back to the admin inventory", status), func(t *testing.T) {
			var adminTokens []string
			mux := http.NewServeMux()
			handleRotationState(t, mux, "g%2Fp", `{"roles":{"poller":{"phase":"idle","incoming_id":5,"managed_user_id":80,"distributed_at":"2026-01-01T00:00:00Z"}}}`)
			mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, map[string]any{"value": "glpat-dead"})
			})
			mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			})
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				adminTokens = append(adminTokens, r.Header.Get("PRIVATE-TOKEN"))
				writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 90, "name": gitlabroles.PollerTokenName}, {"id": 80, "name": gitlabroles.PollerTokenName}, {"id": 12, "name": gitlabroles.CoderTokenName}})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			uid, err := NewTriggerOwner(admin).PollerUserID(ctx, "g", "p")
			require.NoError(t, err)
			assert.Equal(t, int64(80), uid)
			assert.Equal(t, []string{"admin-token"}, adminTokens)
		})
	}

	t.Run("rejected poller token without a managed account is not found", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, http.StatusOK, map[string]any{"value": "glpat-dead"})
		})
		mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 12, "name": gitlabroles.CoderTokenName}})
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)
		_, err = NewTriggerOwner(admin).PollerUserID(ctx, "g", "p")
		assert.ErrorIs(t, err, forge.ErrNotFound)
	})

	for _, tc := range []struct {
		name         string
		status       int
		wantNotFound bool
	}{
		{"rejected poller token with absent service accounts is not found", http.StatusNotFound, true},
		{"rejected poller token with forbidden service accounts is not a confirmed absence", http.StatusForbidden, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, map[string]any{"value": "glpat-dead"})
			})
			mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			})
			serviceAccountsCalled := false
			mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				serviceAccountsCalled = true
				w.WriteHeader(tc.status)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			_, err = NewTriggerOwner(admin).PollerUserID(ctx, "g", "p")
			require.Error(t, err)
			assert.True(t, serviceAccountsCalled, "service_accounts handler must be invoked")
			assert.Equal(t, tc.wantNotFound, forge.IsNotFound(err))
		})
	}

	t.Run("rejected poller token with inventory failure is not a confirmed absence", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, http.StatusOK, map[string]any{"value": "glpat-dead"})
		})
		mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()
		admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)
		_, err = NewTriggerOwner(admin).PollerUserID(ctx, "g", "p")
		require.Error(t, err)
		assert.False(t, forge.IsNotFound(err))
	})

	t.Run("client build error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, http.StatusOK, map[string]any{"value": "glpat-poller"})
		}))
		defer srv.Close()
		admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
		require.NoError(t, err)
		to := NewTriggerOwner(admin)
		to.NewClient = func(string, string) (*gitlab.LiveClient, error) { return nil, assert.AnError }
		_, err = to.PollerUserID(ctx, "g", "p")
		require.ErrorIs(t, err, assert.AnError)
		assert.NotContains(t, err.Error(), "glpat-poller")
	})
}

var _ repos.GitLabTriggerOwner = (*TriggerOwner)(nil)

// An installed Poller credential that rotation state records as
// administrator-supplied is never treated as managed, even when its service
// account and PAT carry the managed names: identification reports not-found
// and the account is excluded from managed reconciliation, so trigger
// provisioning cannot revoke it or change its membership. A genuine managed
// duplicate stays recoverable.
func TestGitLabPollerTriggerOwner_SuppliedSameNamedCredentialIsNotManaged(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		state       string
		wantManaged []int64
		wantUID     int64
	}{
		{name: "supplied", state: `{"roles":{"poller":{"phase":"idle","managed_user_id":90,"distributed_at":"2026-01-01T00:00:00Z"}}}`, wantManaged: []int64{90}},
		{name: "managed", state: `{"roles":{"poller":{"phase":"idle","incoming_id":5,"managed_user_id":77,"distributed_at":"2026-01-01T00:00:00Z"}}}`, wantManaged: []int64{77}, wantUID: 77},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/projects/group%2Fproject/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabPollerToken, "value": "glpat-supplied"})
			})
			mux.HandleFunc("/api/v4/projects/group%2Fproject/variables/"+forge.VarGitLabRoleRotation, func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.VarGitLabRoleRotation, "value": tc.state})
			})
			mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77})
			})
			mux.HandleFunc("/api/v4/projects/group%2Fproject/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 77, "name": gitlabroles.PollerTokenName}, {"id": 90, "name": gitlabroles.PollerTokenName}})
			})
			mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 5, "name": gitlabroles.PollerTokenName, "active": true, "user_id": 77})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			to := NewTriggerOwner(admin)

			ids, err := to.ManagedPollerUserIDs(ctx, "group", "project")
			require.NoError(t, err)
			assert.Equal(t, tc.wantManaged, ids)

			uid, err := to.PollerUserID(ctx, "group", "project")
			if tc.wantUID == 0 {
				require.ErrorIs(t, err, forge.ErrNotFound, "a supplied credential is not a managed Poller")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantUID, uid)
		})
	}
}

// A supplied Poller credential that GitLab rejects (expired, revoked, or
// forbidden) cannot be attributed to its owner. Identification and managed
// reconciliation fail closed instead of adopting a same-named
// administrator-owned service account as managed.
func TestGitLabPollerTriggerOwner_RejectedSuppliedCredentialFailsClosed(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/projects/group%2Fproject/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabPollerToken, "value": "glpat-supplied"})
			})
			mux.HandleFunc("/api/v4/projects/group%2Fproject/variables/"+forge.VarGitLabRoleRotation, func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.VarGitLabRoleRotation, "value": `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z"}}}`})
			})
			mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, status, map[string]any{"message": "rejected"})
			})
			mux.HandleFunc("/api/v4/projects/group%2Fproject/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 90, "name": gitlabroles.PollerTokenName}})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			to := NewTriggerOwner(admin)

			ids, err := to.ManagedPollerUserIDs(ctx, "group", "project")
			require.Error(t, err)
			assert.NotErrorIs(t, err, forge.ErrNotFound)
			assert.Nil(t, ids)

			uid, err := to.PollerUserID(ctx, "group", "project")
			require.Error(t, err)
			assert.NotErrorIs(t, err, forge.ErrNotFound)
			assert.Zero(t, uid)
		})
	}
}

// A 404 while attributing a supplied Poller credential with no recorded owner
// is unresolved ownership, not confirmed absence: the remote error's
// capability classification must not survive, or elevated-Poller recovery
// could treat it as "no managed Poller exists".
func TestGitLabPollerTriggerOwner_SuppliedAttributionNotFoundIsNotAbsence(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()
	handleRotationState(t, mux, "g%2Fp", `{"roles":{"poller":{"phase":"idle","supplied":true,"distributed_at":"2026-01-01T00:00:00Z"}}}`)
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabPollerToken, "value": "glpat-supplied"})
	})
	userCalled := false
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		userCalled = true
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 90, "name": gitlabroles.PollerTokenName}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	ids, err := NewTriggerOwner(admin).ManagedPollerUserIDs(ctx, "g", "p")
	require.Error(t, err)
	assert.True(t, userCalled, "the supplied credential must be authenticated to attribute it")
	assert.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)
	assert.False(t, forge.IsNotFound(err))
	assert.Nil(t, ids)
}

// A supplied Poller credential's owner is recorded at enrollment, so an
// expired or revoked credential keeps its owner's account excluded without
// having to authenticate, and installation can proceed to enroll a replacement.
func TestGitLabPollerTriggerOwner_RecordedSuppliedOwnerSurvivesRejectedCredential(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()
	handleRotationState(t, mux, "g%2Fp", `{"roles":{"poller":{"phase":"idle","managed_user_id":91,"distributed_at":"2026-01-01T00:00:00Z","supplied_user_id":90}}}`)
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabPollerToken, "value": "glpat-dead"})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		t.Error("the recorded owner must not require authenticating the rejected credential")
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 90, "name": gitlabroles.PollerTokenName}, {"id": 91, "name": gitlabroles.PollerTokenName}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	to := NewTriggerOwner(admin)

	ids, err := to.ManagedPollerUserIDs(ctx, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int64{91}, ids, "the supplied owner's account is excluded")
	excluded, err := to.SuppliedAccountIDs(ctx, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int{90}, excluded)
}

// A supplied owner stays excluded after fullsend mints a managed replacement,
// and an empty Poller entry is unknown rather than managed.
func TestGitLabPollerTriggerOwner_SuppliedExclusionSurvivesManagedRotation(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()
	handleRotationState(t, mux, "g%2Fp", `{"roles":{"poller":{"phase":"idle","incoming_id":7,"managed_user_id":91,"distributed_at":"2026-01-01T00:00:00Z","excluded_user_ids":[90]}}}`)
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 90, "name": gitlabroles.PollerTokenName}, {"id": 91, "name": gitlabroles.PollerTokenName}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	ids, err := NewTriggerOwner(admin).ManagedPollerUserIDs(ctx, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int64{91}, ids, "the former supplied owner is never managed")
}

// Supplied credentials of non-Poller roles keep their owners out of management.
func TestGitLabPollerTriggerOwner_SuppliedAnalystOwnerIsExcluded(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()
	handleRotationState(t, mux, "g%2Fp", `{"roles":{"analyst":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z","supplied":true,"supplied_user_id":55},"poller":{"phase":"idle","incoming_id":7}}}`)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	excluded, err := NewTriggerOwner(admin).SuppliedAccountIDs(ctx, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int{55}, excluded)
}

func TestGitLabPollerTriggerOwner_InvalidSuppliedOwnerFailsClosed(t *testing.T) {
	for _, role := range []string{"poller", "analyst", "coder", "scanner"} {
		t.Run(role, func(t *testing.T) {
			mux := http.NewServeMux()
			called := false
			mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.VarGitLabRoleRotation, func(w http.ResponseWriter, r *http.Request) {
				called = true
				writeTestJSON(t, w, http.StatusOK, map[string]any{"value": fmt.Sprintf(`{"roles":{%q:{"supplied":true,"supplied_user_id":-1}}}`, role)})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			ids, err := NewTriggerOwner(admin).SuppliedAccountIDs(context.Background(), "g", "p")
			require.Error(t, err)
			assert.Empty(t, ids)
			assert.True(t, called)
		})
	}
}

// With no same-named service account there is nothing to attribute, so a
// rejected supplied credential does not block reconciliation.
func TestGitLabPollerTriggerOwner_NoMatchingAccountSkipsAttribution(t *testing.T) {
	mux := http.NewServeMux()
	handleRotationState(t, mux, "g%2Fp", `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z"}}}`)
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 12, "name": gitlabroles.CoderTokenName}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	_, err = NewTriggerOwner(admin).ManagedPollerUserIDs(context.Background(), "g", "p")
	assert.ErrorIs(t, err, forge.ErrNotFound)
}

// Missing rotation state is unknown provenance, not proof of a managed
// credential: with a Poller credential installed, the same-named account is not
// adopted.
func TestGitLabPollerTriggerOwner_UnknownProvenanceFailsClosed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabPollerToken, "value": "glpat-x"})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{{"id": 90, "name": gitlabroles.PollerTokenName}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	ids, err := NewTriggerOwner(admin).ManagedPollerUserIDs(context.Background(), "g", "p")
	require.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)
	assert.Nil(t, ids)
}

// An installed non-Poller credential whose provenance rotation state does not
// record is refused, even when rotation state has no entry for the role at all:
// an interrupted supplied enrollment stores the secret before its provenance,
// and the same-named account must not then be treated as managed.
func TestGitLabPollerTriggerOwner_UnknownNonPollerProvenanceFailsClosed(t *testing.T) {
	for name, state := range map[string]string{
		"no entry for the role": `{"roles":{"poller":{"phase":"idle","incoming_id":7}}}`,
		"empty entry":           `{"roles":{"poller":{"phase":"idle","incoming_id":7},"analyst":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			handleRotationState(t, mux, "g%2Fp", state)
			mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabAnalystToken, func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabAnalystToken, "value": "glpat-x"})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)

			excluded, err := NewTriggerOwner(admin).SuppliedAccountIDs(context.Background(), "g", "p")
			require.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)
			assert.Contains(t, err.Error(), "--gitlab-role-token")
			assert.Nil(t, excluded)
		})
	}
}

// A role with recorded managed provenance, or with no installed credential,
// does not block management.
func TestGitLabPollerTriggerOwner_KnownOrUninstalledNonPollerProvenanceProceeds(t *testing.T) {
	mux := http.NewServeMux()
	handleRotationState(t, mux, "g%2Fp", `{"roles":{"poller":{"phase":"idle","incoming_id":7},"analyst":{"phase":"idle","incoming_id":8}}}`)
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabAnalystToken, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabAnalystToken, "value": "glpat-x"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	excluded, err := NewTriggerOwner(admin).SuppliedAccountIDs(context.Background(), "g", "p")
	require.NoError(t, err)
	assert.Empty(t, excluded)
}

// An account whose supplied credential was replaced by a managed one stays an
// exclusion (never managed, never revoked) but no longer owns an installed
// supplied credential, so it is absent from the current set the operational
// inventory uses.
func TestGitLabPollerTriggerOwner_CurrentSuppliedAccountsExcludeHistoricalOwners(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		state       string
		wantExclude []int
		wantCurrent []int
	}{
		{
			name:        "replaced by managed",
			state:       `{"roles":{"poller":{"phase":"idle","incoming_id":5,"distributed_at":"2026-01-01T00:00:00Z","excluded_user_ids":[90]}}}`,
			wantExclude: []int{90},
		},
		{
			name:        "supplied replaced by another supplied",
			state:       `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z","supplied":true,"supplied_user_id":91,"excluded_user_ids":[90,91]}}}`,
			wantExclude: []int{90, 91},
			wantCurrent: []int{91},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			handleRotationState(t, mux, "g%2Fp", tc.state)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
			require.NoError(t, err)
			to := NewTriggerOwner(admin)

			excluded, err := to.SuppliedAccountIDs(ctx, "g", "p")
			require.NoError(t, err)
			assert.Equal(t, tc.wantExclude, excluded)
			current, err := to.CurrentSuppliedAccountIDs(ctx, "g", "p")
			require.NoError(t, err)
			assert.Equal(t, tc.wantCurrent, current)
		})
	}
}

// A supplied token is recorded as the role's lifecycle token only when the
// project inventory can enumerate it: an ordinary user's personal access token
// is outside the inventory, so recording its ID would make it look missing.
func TestGitLabPollerTriggerOwner_SuppliedAttributionReadFailureWithholdsServerText(t *testing.T) {
	mux := http.NewServeMux()
	handleRotationState(t, mux, "g%2Fp", `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-09-20T00:00:00Z","supplied":true}}}`)
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusBadRequest, map[string]any{"message": "bad value glpat-leaked-admin-secret"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	admin, err := gitlab.New("admin-token", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)

	_, _, err = NewTriggerOwner(admin).SuppliedPollerUserID(context.Background(), "g", "p")
	require.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)
	assert.NotContains(t, err.Error(), "glpat-leaked-admin-secret")
}
