package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type loggedRequest struct {
	method, path string
	status       int
}

func recordRequests(ctx context.Context) (context.Context, func() []loggedRequest) {
	var mu sync.Mutex
	var got []loggedRequest
	ctx = WithRequestLogger(ctx, func(method, path string, status int) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, loggedRequest{method, path, status})
	})
	return ctx, func() []loggedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]loggedRequest(nil), got...)
	}
}

func TestRequestLoggerReportsMethodPathAndStatus(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"403 Forbidden"}`)
	})

	ctx, logged := recordRequests(context.Background())
	_, err := client.GetAuthenticatedUserID(ctx)
	require.Error(t, err)

	assert.Equal(t, []loggedRequest{{http.MethodGet, "/api/v4/user", http.StatusForbidden}}, logged())
}

func TestRequestLoggerOmitsQueryString(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("GET /api/v4/projects/{id}/variables/{key}", func(w http.ResponseWriter, r *http.Request) {
		assert.NotEmpty(t, r.URL.RawQuery, "the variable read is expected to carry a query string")
		w.WriteHeader(http.StatusNotFound)
	})

	ctx, logged := recordRequests(context.Background())
	_, found, err := client.GetRepoVariable(ctx, "acme", "api", "FULLSEND_TOKEN")
	require.NoError(t, err)
	assert.False(t, found)

	got := logged()
	require.Len(t, got, 1)
	assert.Equal(t, http.MethodGet, got[0].method)
	assert.Equal(t, "/api/v4/projects/acme%2Fapi/variables/FULLSEND_TOKEN", got[0].path)
	assert.Equal(t, http.StatusNotFound, got[0].status)
}

func TestRequestLoggerReportsEveryAttemptAndTransportFailures(t *testing.T) {
	client, mux := setupTest(t)
	calls := 0
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"id":42}`)
	})

	ctx, logged := recordRequests(context.Background())
	id, err := client.GetAuthenticatedUserID(ctx)
	require.NoError(t, err)
	assert.Equal(t, 42, id)
	assert.Equal(t, []loggedRequest{
		{http.MethodGet, "/api/v4/user", http.StatusServiceUnavailable},
		{http.MethodGet, "/api/v4/user", http.StatusOK},
	}, logged())

	unreachable, err := New("test-token", WithBaseURL("http://127.0.0.1:1"), WithAfterFunc(noWaitAfter))
	require.NoError(t, err)
	ctx, logged = recordRequests(context.Background())
	_, err = unreachable.doAttempts(ctx, http.MethodPost, "/projects?private_token=x", nil, 1)
	require.Error(t, err)
	assert.Equal(t, []loggedRequest{{http.MethodPost, "/api/v4/projects", 0}}, logged())
}

func TestRequestLoggerDisabledByDefault(t *testing.T) {
	client, mux := setupTest(t)
	mux.HandleFunc("GET /api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":42}`)
	})
	_, err := client.GetAuthenticatedUserID(WithRequestLogger(context.Background(), nil))
	require.NoError(t, err)
	_, err = client.GetAuthenticatedUserID(context.Background())
	require.NoError(t, err)
}
