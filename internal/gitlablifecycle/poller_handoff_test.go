package gitlablifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// handoffServer models the GitLab endpoints the replacement Poller handoff
// uses. The replacement service account is user 77, so patFixture models its
// personal access tokens.
type handoffServer struct {
	pats *patFixture

	createStatus    int
	createBody      map[string]any
	memberStatus    int
	updateStatus    int
	accessLevel     int
	accessStatus    int
	protectedStatus int
	protectedRule   map[string]any
	jobs            []map[string]any

	createdNames []string
	members      []map[string]any
	updates      []map[string]any
	triggerAuth  []string
	branchWrites int
	// createCalls and protectedReads count handler invocations, so a 404
	// outcome is proven to come from the handler rather than an unmatched route.
	createCalls    int
	protectedReads int
}

func newHandoffServer() *handoffServer {
	return &handoffServer{
		pats:            &patFixture{nextID: 10, tokens: []map[string]any{}},
		createBody:      map[string]any{"id": 77, "name": gitlabroles.PollerTokenName},
		accessLevel:     forge.GitLabAccessLevelDeveloper,
		protectedStatus: http.StatusNotFound,
	}
}

func (s *handoffServer) start(t *testing.T) *TriggerOwner {
	t.Helper()
	mux := http.NewServeMux()
	s.pats.register(t, mux)
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "admin-token", r.Header.Get("PRIVATE-TOKEN"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		s.createCalls++
		s.createdNames = append(s.createdNames, body["name"].(string))
		if s.createStatus != 0 {
			writeTestJSON(t, w, s.createStatus, map[string]any{"message": "refused glpat-server-echo"})
			return
		}
		writeTestJSON(t, w, http.StatusCreated, s.createBody)
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/members", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		s.members = append(s.members, body)
		if s.memberStatus != 0 {
			writeTestJSON(t, w, s.memberStatus, map[string]any{"message": "member"})
			return
		}
		writeTestJSON(t, w, http.StatusCreated, map[string]any{"id": body["user_id"]})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/members/77", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		s.updates = append(s.updates, body)
		if s.updateStatus != 0 {
			writeTestJSON(t, w, s.updateStatus, map[string]any{"message": "update"})
			return
		}
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/members/all/77", func(w http.ResponseWriter, r *http.Request) {
		if s.accessStatus != 0 {
			writeTestJSON(t, w, s.accessStatus, map[string]any{"message": "access"})
			return
		}
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 77, "access_level": s.accessLevel})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, map[string]any{"id": 1, "name": "p", "path_with_namespace": "g/p", "default_branch": "main"})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/protected_branches/main", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			s.protectedReads++
		} else {
			s.branchWrites++
		}
		if s.protectedStatus != http.StatusOK {
			writeTestJSON(t, w, s.protectedStatus, map[string]any{"message": "404 Not found"})
			return
		}
		writeTestJSON(t, w, http.StatusOK, s.protectedRule)
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/protected_branches", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/triggers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeTestJSON(t, w, http.StatusOK, []map[string]any{})
			return
		}
		s.triggerAuth = append(s.triggerAuth, r.Header.Get("PRIVATE-TOKEN"))
		writeTestJSON(t, w, http.StatusCreated, map[string]any{"id": 900, "description": repos.GitLabWebhookTriggerDescription, "token": "glptt-x", "owner": map[string]any{"id": 77}})
	})
	mux.HandleFunc("/api/v4/users/77/keys", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{})
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/jobs", func(w http.ResponseWriter, r *http.Request) {
		jobs := s.jobs
		if jobs == nil {
			jobs = []map[string]any{}
		}
		writeTestJSON(t, w, http.StatusOK, jobs)
	})
	mux.HandleFunc("/api/v4/projects/g%2Fp/pipeline_schedules", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, http.StatusOK, []map[string]any{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	to := newTestOwner(t, srv.URL)
	to.Now = fixedNow
	return to
}

func TestCreateReplacementPoller_CreatesDeveloperServiceAccount(t *testing.T) {
	s := newHandoffServer()
	to := s.start(t)

	uid, err := to.CreateReplacementPoller(context.Background(), "g", "p")
	require.NoError(t, err)
	assert.Equal(t, int64(77), uid)
	assert.Equal(t, []string{gitlabroles.PollerTokenName}, s.createdNames)
	require.Len(t, s.members, 1)
	assert.EqualValues(t, forge.GitLabAccessLevelDeveloper, s.members[0]["access_level"])
	assert.Empty(t, s.pats.created, "no personal access token is created with the account")
	assert.Zero(t, s.branchWrites, "an unprotected branch needs no grant")
}

func TestCreateReplacementPoller_ExistingMemberIsSetToDeveloper(t *testing.T) {
	s := newHandoffServer()
	s.memberStatus = http.StatusConflict
	to := s.start(t)

	uid, err := to.CreateReplacementPoller(context.Background(), "g", "p")
	require.NoError(t, err)
	assert.Equal(t, int64(77), uid)
	require.Len(t, s.updates, 1)
	assert.EqualValues(t, forge.GitLabAccessLevelDeveloper, s.updates[0]["access_level"])
}

func TestCreateReplacementPoller_Failures(t *testing.T) {
	ctx := context.Background()
	t.Run("unavailable", func(t *testing.T) {
		for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
			s := newHandoffServer()
			s.createStatus = status
			uid, err := s.start(t).CreateReplacementPoller(ctx, "g", "p")
			require.Error(t, err)
			assert.Zero(t, uid)
			assert.Equal(t, 1, s.createCalls, "the create handler produced the refusal")
			assert.True(t, forge.IsNotFound(err), "a refused create made no account, so the handoff defers")
			assert.NotContains(t, err.Error(), "glpat-server-echo")
		}
	})
	t.Run("server error", func(t *testing.T) {
		s := newHandoffServer()
		s.createStatus = http.StatusInternalServerError
		uid, err := s.start(t).CreateReplacementPoller(ctx, "g", "p")
		require.Error(t, err)
		assert.Zero(t, uid)
		assert.False(t, forge.IsNotFound(err), "an unknown create result is never mistaken for an absent capability")
	})
	t.Run("no ID", func(t *testing.T) {
		s := newHandoffServer()
		s.createBody = map[string]any{"name": gitlabroles.PollerTokenName}
		uid, err := s.start(t).CreateReplacementPoller(ctx, "g", "p")
		require.Error(t, err)
		assert.Zero(t, uid)
	})
	t.Run("membership", func(t *testing.T) {
		s := newHandoffServer()
		s.memberStatus = http.StatusInternalServerError
		uid, err := s.start(t).CreateReplacementPoller(ctx, "g", "p")
		require.Error(t, err)
		assert.Equal(t, int64(77), uid, "the created account is reported so it is recorded")
	})
	t.Run("membership update", func(t *testing.T) {
		s := newHandoffServer()
		s.memberStatus = http.StatusConflict
		s.updateStatus = http.StatusInternalServerError
		uid, err := s.start(t).CreateReplacementPoller(ctx, "g", "p")
		require.Error(t, err)
		assert.Equal(t, int64(77), uid)
	})
}

// Creating the account never reads or writes the branch rule: admission is
// checked read-only by EnsureReplacementPollerAccess before elevation.
func TestCreateReplacementPoller_NeverTouchesBranchProtection(t *testing.T) {
	s := newHandoffServer()
	s.protectedStatus = http.StatusOK
	s.protectedRule = map[string]any{"name": "main", "merge_access_levels": []map[string]any{{"access_level": 40}}, "push_access_levels": []map[string]any{{"access_level": 40}}}

	uid, err := s.start(t).CreateReplacementPoller(context.Background(), "g", "p")
	require.NoError(t, err)
	assert.Equal(t, int64(77), uid)
	assert.Zero(t, s.protectedReads)
	assert.Zero(t, s.branchWrites)
}

func TestEnsureReplacementPollerAccess(t *testing.T) {
	ctx := context.Background()
	maintainerOnly := map[string]any{"name": "main", "merge_access_levels": []map[string]any{{"access_level": 40}}, "push_access_levels": []map[string]any{{"access_level": 40}}}

	t.Run("adds a missing membership", func(t *testing.T) {
		s := newHandoffServer()
		require.NoError(t, s.start(t).EnsureReplacementPollerAccess(ctx, "g", "p", 77))
		require.Len(t, s.members, 1)
		assert.EqualValues(t, forge.GitLabAccessLevelDeveloper, s.members[0]["access_level"])
		assert.Zero(t, s.branchWrites)
	})
	t.Run("existing membership is set to Developer", func(t *testing.T) {
		s := newHandoffServer()
		s.memberStatus = http.StatusConflict
		require.NoError(t, s.start(t).EnsureReplacementPollerAccess(ctx, "g", "p", 77))
		require.Len(t, s.updates, 1)
		assert.EqualValues(t, forge.GitLabAccessLevelDeveloper, s.updates[0]["access_level"])
	})
	t.Run("membership failure is retried by the next call", func(t *testing.T) {
		s := newHandoffServer()
		s.memberStatus = http.StatusInternalServerError
		to := s.start(t)
		require.Error(t, to.EnsureReplacementPollerAccess(ctx, "g", "p", 77))
		s.memberStatus = 0
		require.NoError(t, to.EnsureReplacementPollerAccess(ctx, "g", "p", 77))
		assert.Len(t, s.members, 2)
	})
	t.Run("membership update failure", func(t *testing.T) {
		s := newHandoffServer()
		s.memberStatus = http.StatusConflict
		s.updateStatus = http.StatusInternalServerError
		require.Error(t, s.start(t).EnsureReplacementPollerAccess(ctx, "g", "p", 77))
	})
	t.Run("branch policy that excludes the account defers without a grant", func(t *testing.T) {
		s := newHandoffServer()
		s.protectedStatus = http.StatusOK
		s.protectedRule = maintainerOnly
		err := s.start(t).EnsureReplacementPollerAccess(ctx, "g", "p", 77)
		var unsafe *repos.PollerGenerationUnsafeError
		require.ErrorAs(t, err, &unsafe)
		assert.Contains(t, unsafe.Reason, "cannot start pipelines on protected branch")
		assert.Contains(t, unsafe.Reason, "does not broaden branch protection")
		assert.Zero(t, s.branchWrites, "a replacement is never granted merge access")
	})
	t.Run("unreadable branch policy is an error, not a deferral", func(t *testing.T) {
		s := newHandoffServer()
		s.protectedStatus = http.StatusInternalServerError
		err := s.start(t).EnsureReplacementPollerAccess(ctx, "g", "p", 77)
		require.Error(t, err)
		var unsafe *repos.PollerGenerationUnsafeError
		assert.False(t, errors.As(err, &unsafe))
		assert.Zero(t, s.branchWrites)
	})
}

func TestReplacementPollerBootstrapAndTrigger(t *testing.T) {
	ctx := context.Background()
	s := newHandoffServer()
	to := s.start(t)

	boot, err := to.CreateReplacementPollerBootstrap(ctx, "g", "p", 77)
	require.NoError(t, err)
	require.NotNil(t, boot)
	require.Len(t, s.pats.created, 1)
	assert.Equal(t, gitlabroles.PollerBootstrapTokenName, s.pats.created[0]["name"])

	trig, err := to.CreatePipelineTriggerTokenAsReplacementPoller(ctx, "g", "p", repos.GitLabWebhookTriggerDescription, boot)
	require.NoError(t, err)
	assert.Equal(t, int64(77), trig.OwnerID)
	assert.Equal(t, []string{boot.Token}, s.triggerAuth, "the trigger is created with the bootstrap credential, not the installer's")

	require.NoError(t, to.RevokeReplacementPollerBootstrap(ctx, "g", "p", 77))
	assert.Equal(t, []int{boot.ID}, s.pats.revoked)

	_, err = to.CreatePipelineTriggerTokenAsReplacementPoller(ctx, "g", "p", repos.GitLabWebhookTriggerDescription, nil)
	require.Error(t, err)

	s.pats.createStatus = http.StatusInternalServerError
	boot, err = to.CreateReplacementPollerBootstrap(ctx, "g", "p", 77)
	require.Error(t, err)
	assert.Nil(t, boot)
}

func TestVerifyReplacementPollerCredentialsRevoked(t *testing.T) {
	ctx := context.Background()
	s := newHandoffServer()
	to := s.start(t)
	require.NoError(t, to.VerifyReplacementPollerCredentialsRevoked(ctx, "g", "p", 77))

	// Any active token, managed name or not, is an operator-safe refusal.
	s.pats.tokens = []map[string]any{{"id": 3, "name": gitlabroles.PollerTokenName, "active": true}}
	err := to.VerifyReplacementPollerCredentialsRevoked(ctx, "g", "p", 77)
	var unsafe *repos.PollerGenerationUnsafeError
	require.True(t, errors.As(err, &unsafe))
	assert.Contains(t, unsafe.Reason, "token ID 3")

	s.pats.listStatus = http.StatusInternalServerError
	err = to.VerifyReplacementPollerCredentialsRevoked(ctx, "g", "p", 77)
	require.Error(t, err)
}

func TestVerifyTriggerStartsProtectedPipeline(t *testing.T) {
	ctx := context.Background()
	trigger := &forge.PipelineTriggerToken{ID: 900, OwnerID: 77}

	t.Run("unprotected branch", func(t *testing.T) {
		s := newHandoffServer()
		require.NoError(t, s.start(t).VerifyTriggerStartsProtectedPipeline(ctx, "g", "p", trigger))
		assert.Positive(t, s.protectedReads, "the 404 came from the protected-branch handler")
	})
	t.Run("developer merge grant", func(t *testing.T) {
		s := newHandoffServer()
		s.protectedStatus = http.StatusOK
		s.protectedRule = map[string]any{"name": "main", "merge_access_levels": []map[string]any{{"access_level": 30}}, "push_access_levels": []map[string]any{{"access_level": 40}}}
		require.NoError(t, s.start(t).VerifyTriggerStartsProtectedPipeline(ctx, "g", "p", trigger))
		assert.Zero(t, s.branchWrites)
	})
	t.Run("maintainers only", func(t *testing.T) {
		s := newHandoffServer()
		s.protectedStatus = http.StatusOK
		s.protectedRule = map[string]any{"name": "main", "merge_access_levels": []map[string]any{{"access_level": 40}}, "push_access_levels": []map[string]any{{"access_level": 40}}}
		err := s.start(t).VerifyTriggerStartsProtectedPipeline(ctx, "g", "p", trigger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot start pipelines on protected branch")
		assert.Zero(t, s.branchWrites, "the probe never broadens branch protection")
	})
	t.Run("owner above developer", func(t *testing.T) {
		s := newHandoffServer()
		s.accessLevel = forge.GitLabAccessLevelMaintainer
		err := s.start(t).VerifyTriggerStartsProtectedPipeline(ctx, "g", "p", trigger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not Developer")
	})
	t.Run("owner access unreadable", func(t *testing.T) {
		s := newHandoffServer()
		s.accessStatus = http.StatusInternalServerError
		require.Error(t, s.start(t).VerifyTriggerStartsProtectedPipeline(ctx, "g", "p", trigger))
	})
	t.Run("no owner", func(t *testing.T) {
		s := newHandoffServer()
		require.Error(t, s.start(t).VerifyTriggerStartsProtectedPipeline(ctx, "g", "p", &forge.PipelineTriggerToken{ID: 900}))
		require.Error(t, s.start(t).VerifyTriggerStartsProtectedPipeline(ctx, "g", "p", nil))
	})
}
