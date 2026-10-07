package gitlablifecycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTriggerOwnerDoesNotAdoptUnrecordedAccount(t *testing.T) {
	var listed, mutated bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		listed = true
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`[{"id":77,"name":"fullsend-poller"}]`))
		assert.NoError(t, err)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutated = true
		}
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client, err := gitlab.New("installer fixture credential", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	owner := NewTriggerOwner(client)
	ids, err := owner.ManagedPollerUserIDs(context.Background(), "g", "p")
	require.ErrorIs(t, err, forge.ErrNotFound)
	assert.Empty(t, ids)
	assert.True(t, listed)
	assert.False(t, mutated)
}

func TestPollerCredentialRejected(t *testing.T) {
	assert.True(t, PollerCredentialRejected(&gitlab.APIError{StatusCode: http.StatusUnauthorized}))
	assert.True(t, PollerCredentialRejected(&gitlab.APIError{StatusCode: http.StatusForbidden}))
	assert.False(t, PollerCredentialRejected(&gitlab.APIError{StatusCode: http.StatusInternalServerError}))
	assert.False(t, PollerCredentialRejected(nil))
}

func TestSuppliedProvenanceReadErrorsAreSanitized(t *testing.T) {
	const sensitive = "opaque credential echoed by remote server"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/g%2Fp/variables/FULLSEND_GITLAB_ROLE_ROTATION", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, err := w.Write([]byte(`{"message":"` + sensitive + `"}`))
		assert.NoError(t, err)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client, err := gitlab.New("installer fixture credential", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	owner := NewTriggerOwner(client)
	_, _, err = owner.SuppliedPollerUserID(context.Background(), "g", "p")
	require.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)
	assert.ErrorIs(t, err, forge.ErrForbidden)
	assert.NotContains(t, err.Error(), sensitive)
	assert.Contains(t, err.Error(), "server error text withheld")
	_, _, err = owner.ResolveExcludedAccounts(context.Background(), "g", "p")
	require.ErrorIs(t, err, repos.ErrPollerSuppliedUnresolved)
	assert.ErrorIs(t, err, forge.ErrForbidden)
	assert.NotContains(t, err.Error(), sensitive)
	assert.Contains(t, err.Error(), "server error text withheld")
}

// The live adapter intentionally lacks a server-side drain capability. Empty
// inventories must never be mistaken for proof that accepted requests finished.
func TestLiveTriggerOwnerDoesNotClaimQuiescence(t *testing.T) {
	var owner any = NewTriggerOwner(nil)
	_, ok := owner.(repos.GitLabPollerQuiescenceVerifier)
	assert.False(t, ok)
}

func TestPollerTriggerIDsIncludesManagedAndUnknownOwners(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`[{"id":1,"description":"fullsend-webhook","owner":{"id":501}},{"id":2,"owner":{"id":999}},{"id":3}]`))
		assert.NoError(t, err)
	}))
	defer srv.Close()
	client, err := gitlab.New("fixture", gitlab.WithBaseURL(srv.URL))
	require.NoError(t, err)
	ids, err := NewTriggerOwner(client).PollerTriggerIDs(context.Background(), "g", "p", 501)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 3}, ids)
}
