package gitlablifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// sinkFixture models the trigger-token CI/CD variable.
type sinkFixture struct {
	mu        sync.Mutex
	exists    bool
	value     string
	masked    bool
	protected bool
	varType   string
	envScope  string
	deletes   int
	// creates counts requests that reached the create handler, so a test can
	// tell a refusal the handler produced from a routing mismatch.
	creates   int
	createErr int
}

func (f *sinkFixture) register(t *testing.T, mux *http.ServeMux) {
	t.Helper()
	const base = "/api/v4/projects/g%2Fp/variables"
	mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		// The handler runs on the httptest server's goroutine, so it uses assert
		// (t.Errorf), never require (t.FailNow).
		f.creates++
		if !assert.Equal(t, http.MethodPost, r.Method) {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if f.createErr != 0 {
			writeTestJSON(t, w, f.createErr, map[string]any{"message": "refused"})
			return
		}
		var body map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, forge.SecretGitLabTriggerAccountToken, body["key"])
		assert.Equal(t, true, body["masked"], "the sink is created masked")
		assert.Equal(t, true, body["protected"], "the sink is created protected")
		value, _ := body["value"].(string)
		f.exists, f.value = true, value
		writeTestJSON(t, w, http.StatusCreated, map[string]any{"key": body["key"]})
	})
	mux.HandleFunc(base+"/"+forge.SecretGitLabTriggerAccountToken, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if !f.exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			vt := f.varType
			if vt == "" {
				vt = "env_var"
			}
			scope := f.envScope
			if scope == "" {
				scope = "*"
			}
			writeTestJSON(t, w, http.StatusOK, map[string]any{"masked": f.masked, "protected": f.protected, "variable_type": vt, "environment_scope": scope})
		case http.MethodDelete:
			f.deletes++
			f.exists, f.value = false, ""
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
}

func newSinkOwner(t *testing.T, f *sinkFixture) *TriggerOwner {
	t.Helper()
	mux := http.NewServeMux()
	f.register(t, mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return newTestOwner(t, srv.URL)
}

// The trigger token is accepted only once GitLab reports the variable masked,
// protected and not a file; the value is never kept in an unprotected sink.
func TestTriggerAccountOptions_SinkRequiresProtectedMaskedVariable(t *testing.T) {
	tests := map[string]struct {
		fixture *sinkFixture
		wantErr bool
	}{
		"protected and masked": {fixture: &sinkFixture{masked: true, protected: true}},
		"unmasked":             {fixture: &sinkFixture{masked: false, protected: true}, wantErr: true},
		"unprotected":          {fixture: &sinkFixture{masked: true, protected: false}, wantErr: true},
		"file type":            {fixture: &sinkFixture{masked: true, protected: true, varType: "file"}, wantErr: true},
		"environment scoped":   {fixture: &sinkFixture{masked: true, protected: true, envScope: "production"}, wantErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := tc.fixture
			o := newSinkOwner(t, f)
			err := o.publishTriggerToken(context.Background(), "g", "p", "glptt-secret-value")
			assert.Equal(t, 1, f.creates, "the create handler was invoked exactly once")
			if tc.wantErr {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "glptt-secret-value", "the token never appears in errors")
				assert.False(t, f.exists, "an unprotected sink is deleted")
				return
			}
			require.NoError(t, err)
			assert.True(t, f.exists)
			assert.Equal(t, "glptt-secret-value", f.value)
		})
	}
}

func TestTriggerAccountOptions_SinkCreateFailureAndUnpublish(t *testing.T) {
	f := &sinkFixture{createErr: http.StatusForbidden}
	o := newSinkOwner(t, f)
	err := o.publishTriggerToken(context.Background(), "g", "p", "glptt-secret-value")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "glptt-secret-value")
	assert.Equal(t, 1, f.creates, "the create handler refused the request, not a routing mismatch")
	assert.False(t, f.exists, "no variable is stored after a refused create")

	// Unpublishing removes the variable and is idempotent when it is absent.
	f = &sinkFixture{exists: true, value: "x", masked: true, protected: true}
	o = newSinkOwner(t, f)
	opts := o.triggerAccountOptions("g", "p", "main", []int{}, nil)
	assert.True(t, opts.PollerInventoryVerified)
	assert.True(t, opts.EnforceWebhookSafety, "install and rotation enforce the webhook's project-wide trigger invariants")
	assert.Nil(t, opts.Lease)
	require.NoError(t, opts.UnpublishTriggerToken(context.Background()))
	assert.False(t, f.exists)
	require.NoError(t, opts.UnpublishTriggerToken(context.Background()))
	assert.Equal(t, 2, f.deletes)
	contain := o.ContainmentOptions("g", "p", nil)
	assert.True(t, contain.EnforceWebhookSafety, "early containment checks the same project-wide invariants")
}
