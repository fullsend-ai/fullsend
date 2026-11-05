package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestLiveClient_APICalls(t *testing.T) {
	var flaky atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/not-modified":
			w.WriteHeader(http.StatusNotModified)
		case "/flaky":
			if flaky.Add(1) == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := New("token").WithBaseURL(srv.URL)
	c.afterFunc = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	assert.Empty(t, c.APICalls(), "no calls before the first response")

	get := func(ctx context.Context, path string) {
		t.Helper()
		resp, err := c.get(ctx, path)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	ctx := context.Background()
	claim := forge.WithAPICaller(ctx, "claim")

	get(ctx, "/ok")
	get(claim, "/ok")
	_, err := c.get(claim, "/missing")
	require.True(t, forge.IsNotFound(err), "unexpected error: %v", err)
	get(forge.WithAPICaller(ctx, "lock"), "/flaky")
	resp, notModified, err := c.getConditional(claim, "/not-modified", `"etag"`)
	require.NoError(t, err)
	assert.Nil(t, resp)
	assert.True(t, notModified)

	calls := c.APICalls()
	assert.Equal(t, map[string]int64{
		forge.APICallerOther: 1,
		"claim":              2, // the 304 costs no quota, so it is not counted
		"lock":               2, // both attempts of the retried request
	}, calls)

	calls["claim"] = 100
	assert.Equal(t, int64(2), c.APICalls()["claim"], "APICalls returns a copy")
}

func TestLiveClient_APICallsIgnoresRequestsWithoutAResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := New("token").WithBaseURL(url)
	_, err := c.get(context.Background(), "/gone")
	require.Error(t, err)
	assert.Empty(t, c.APICalls())
}
