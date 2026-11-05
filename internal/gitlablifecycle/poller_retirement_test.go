package gitlablifecycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// retirementServer models the GitLab endpoints that retire the superseded
// Poller service account 77 (the account patFixture models) while account 99
// is the current Poller whose credential is installed.
type retirementServer struct {
	pats *patFixture

	state          string
	accounts       []map[string]any
	accountsStatus int
	jobs           []map[string]any
	jobsStatus     int
	keys           []map[string]any
	schedules      []map[string]any
	triggers       []map[string]any
	deleteStatus   int
	// deleteAsync models GitLab accepting the deletion (202) while the account
	// stays listed; accountsStatusAfterDelete makes the confirming re-list fail.
	deleteAsync               bool
	accountsStatusAfterDelete int

	deleted     int
	deletedVars int
}

func newRetirementServer() *retirementServer {
	return &retirementServer{
		pats: &patFixture{nextID: 10, tokens: []map[string]any{
			{"id": 1, "name": gitlabroles.PollerTokenName, "active": true},
			{"id": 2, "name": gitlabroles.PollerTokenName, "active": false, "revoked": true},
		}},
		state:    `{"version":2,"roles":{"poller":{"phase":"idle","incoming_id":5,"managed_user_id":99,"distributed_at":"2026-01-01T00:00:00Z"}}}`,
		accounts: []map[string]any{{"id": 77, "name": gitlabroles.PollerTokenName}, {"id": 99, "name": gitlabroles.PollerTokenName}},
	}
}

func (s *retirementServer) start(t *testing.T) *TriggerOwner {
	t.Helper()
	list := func(items []map[string]any) []map[string]any {
		if items == nil {
			return []map[string]any{}
		}
		return items
	}
	status := func(code int) int {
		if code == 0 {
			return http.StatusOK
		}
		return code
	}
	mux := http.NewServeMux()
	s.pats.register(t, mux)
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.VarGitLabRoleRotation, func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"value": s.state})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, status(s.accountsStatus), list(s.accounts))
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts/77", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "admin-token", r.Header.Get("PRIVATE-TOKEN"))
		s.deleted++
		if s.deleteStatus != 0 {
			writeTestJSON(t, w, s.deleteStatus, map[string]any{"message": "glpat-server-echo"})
			if s.deleteStatus >= http.StatusMultipleChoices {
				return
			}
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		if s.deleteAsync {
			return
		}
		kept := []map[string]any{}
		for _, a := range s.accounts {
			if a["id"] != 77 {
				kept = append(kept, a)
			}
		}
		s.accounts = kept
		if s.accountsStatusAfterDelete != 0 {
			s.accountsStatus = s.accountsStatusAfterDelete
		}
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/jobs", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, status(s.jobsStatus), list(s.jobs))
	})
	mux.HandleFunc("/api/v4/users/77/keys", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, list(s.keys))
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/pipeline_schedules", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, list(s.schedules))
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/triggers", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, list(s.triggers))
	})
	// The installed Poller credential belongs to the current Poller (99), so
	// retiring 77 must leave it in place.
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/"+forge.SecretGitLabPollerToken, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			s.deletedVars++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeTestJSON(t, w, http.StatusOK, map[string]any{"key": forge.SecretGitLabPollerToken, "value": "glpat-installed"})
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 99})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return newTestOwner(t, srv.URL)
}

func TestPollerRetirementBlocked(t *testing.T) {
	ctx := context.Background()

	s := newRetirementServer()
	reason, err := s.start(t).PollerRetirementBlocked(ctx, "g", "p", 77)
	require.NoError(t, err)
	assert.Empty(t, reason, "an idle managed account may be retired")

	s = newRetirementServer()
	s.jobs = []map[string]any{{"id": 5, "user": map[string]any{"id": 77}}, {"id": 6, "user": map[string]any{"id": 12}}}
	reason, err = s.start(t).PollerRetirementBlocked(ctx, "g", "p", 77)
	require.NoError(t, err)
	assert.Equal(t, "1 unfinished job(s) still run as it", reason)

	s = newRetirementServer()
	s.state = `{"version":2,"roles":{"poller":{"phase":"idle","incoming_id":5,"managed_user_id":99,"distributed_at":"2026-01-01T00:00:00Z","excluded_user_ids":[77]}}}`
	reason, err = s.start(t).PollerRetirementBlocked(ctx, "g", "p", 77)
	require.NoError(t, err)
	assert.Contains(t, reason, "owns an administrator-supplied credential")

	s = newRetirementServer()
	s.state = `{"version":2,"roles":{"poller":{"supplied":true,"supplied_user_id":-1}}}`
	_, err = s.start(t).PollerRetirementBlocked(ctx, "g", "p", 77)
	require.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)

	s = newRetirementServer()
	s.jobsStatus = http.StatusInternalServerError
	_, err = s.start(t).PollerRetirementBlocked(ctx, "g", "p", 77)
	require.ErrorContains(t, err, "listing unfinished jobs of the superseded Poller")
}

func TestRetirePollerAccount_RevokesVerifiesAndDeletes(t *testing.T) {
	s := newRetirementServer()
	to := s.start(t)

	require.NoError(t, to.RetirePollerAccount(context.Background(), "g", "p", 77))
	assert.Equal(t, []int{1}, s.pats.revoked, "the managed runtime credential is revoked")
	assert.Equal(t, 1, s.deleted)
	assert.Zero(t, s.deletedVars, "the current Poller's installed credential is preserved")
}

func TestRetirePollerAccount_AlreadyDeleted(t *testing.T) {
	s := newRetirementServer()
	s.accounts = []map[string]any{{"id": 99, "name": gitlabroles.PollerTokenName}}
	require.NoError(t, s.start(t).RetirePollerAccount(context.Background(), "g", "p", 77))
	assert.Empty(t, s.pats.revoked)
	assert.Zero(t, s.deleted)
}

func TestRetirePollerAccount_FailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(s *retirementServer)
		want  string
	}{
		"account listing": {
			setup: func(s *retirementServer) { s.accountsStatus = http.StatusInternalServerError },
			want:  "listing project service accounts before retiring the superseded Poller",
		},
		"unmanaged active token": {
			setup: func(s *retirementServer) {
				s.pats.tokens = append(s.pats.tokens, map[string]any{"id": 3, "name": "unrelated", "active": true})
			},
			want: "revoking the superseded Poller's credentials",
		},
		"revocation does not stick": {
			setup: func(s *retirementServer) {
				s.pats.ineffective = true
			},
			want: "revoking the superseded Poller's credentials",
		},
		"token relisting": {
			setup: func(s *retirementServer) { s.pats.relistStatus = http.StatusInternalServerError },
			want:  "revoking the superseded Poller's credentials",
		},
		"owned trigger": {
			setup: func(s *retirementServer) {
				s.triggers = []map[string]any{{"id": 20, "description": repos.GitLabWebhookTriggerDescription, "owner": map[string]any{"id": 77}}}
			},
			want: "not deleted",
		},
		"unfinished job": {
			setup: func(s *retirementServer) {
				s.jobs = []map[string]any{{"id": 5, "user": map[string]any{"id": 77}}}
			},
			want: "are unfinished and run as the Poller service account",
		},
		"deletion": {
			setup: func(s *retirementServer) { s.deleteStatus = http.StatusInternalServerError },
			want:  "deleting the superseded Poller service account 77",
		},
		"deletion accepted but account remains": {
			setup: func(s *retirementServer) {
				s.deleteStatus = http.StatusAccepted
				s.deleteAsync = true
			},
			want: "still listed after the deletion request was accepted",
		},
		"deletion confirmation listing": {
			setup: func(s *retirementServer) { s.accountsStatusAfterDelete = http.StatusInternalServerError },
			want:  "confirming the superseded Poller service account was deleted",
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newRetirementServer()
			tc.setup(s)
			err := s.start(t).RetirePollerAccount(context.Background(), "g", "p", 77)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), "glpat-server-echo")
			if !strings.HasPrefix(name, "deletion") {
				assert.Zero(t, s.deleted, "the account is not deleted")
			}
		})
	}
}
