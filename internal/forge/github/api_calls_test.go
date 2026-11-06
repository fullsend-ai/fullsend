package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		case "/graphql":
			w.Header().Set("X-RateLimit-Resource", "graphql")
			w.WriteHeader(http.StatusOK)
		case "/core":
			w.Header().Set("X-RateLimit-Resource", "core")
			w.WriteHeader(http.StatusOK)
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
	get(claim, "/core")
	get(claim, "/graphql")
	resp, notModified, err := c.getConditional(claim, "/not-modified", `"etag"`)
	require.NoError(t, err)
	assert.Nil(t, resp)
	assert.True(t, notModified)

	calls := c.APICalls()
	assert.Equal(t, map[string]int64{
		forge.APICallerOther: 1,
		"claim":              3, // the 304 costs no quota, so it is not counted
		"claim/graphql":      1, // charged to another quota
		"lock":               2, // both attempts of the retried request
	}, calls)

	calls["claim"] = 100
	assert.Equal(t, int64(3), c.APICalls()["claim"], "APICalls returns a copy")
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

func TestLiveClient_APIQuotaCounters(t *testing.T) {
	const readReset, writeReset = 1800000000, 1800000009
	remaining := map[string]*atomic.Int32{http.MethodGet: {}, http.MethodPost: {}}
	remaining[http.MethodGet].Store(100)
	remaining[http.MethodPost].Store(50)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/no-headers":
			w.WriteHeader(http.StatusOK)
			return
		case "/graphql":
			w.Header().Set("X-RateLimit-Resource", "graphql")
		case "/not-modified":
			w.Header().Set("X-RateLimit-Resource", "core")
			w.WriteHeader(http.StatusNotModified)
			return
		default:
			w.Header().Set("X-RateLimit-Resource", "core")
		}
		reset := readReset
		if r.Method == http.MethodPost {
			reset = writeReset
		}
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(int(remaining[r.Method].Add(-1))))
		w.Header().Set("X-RateLimit-Reset", strconv.Itoa(reset))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New("token").WithBaseURL(srv.URL)
	assert.Empty(t, c.APIQuotaCounters(), "no counters before the first response")

	ctx := forge.WithAPICaller(context.Background(), "claim")
	call := func(ctx context.Context, method, path string) {
		t.Helper()
		resp, err := c.do(ctx, method, path, nil)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	call(ctx, http.MethodGet, "/repo")
	call(ctx, http.MethodGet, "/repo")
	call(context.Background(), http.MethodGet, "/repo")
	call(ctx, http.MethodPost, "/repo")
	call(ctx, http.MethodGet, "/no-headers")
	call(ctx, http.MethodGet, "/graphql")
	call(ctx, http.MethodGet, "/not-modified")

	counters := c.APIQuotaCounters()
	require.Len(t, counters, 2)
	assert.Equal(t, int64(readReset), counters[0].Reset.Unix())
	assert.Equal(t, 5000, counters[0].Limit)
	assert.Equal(t, 99, counters[0].Highest)
	assert.Equal(t, 97, counters[0].Lowest)
	assert.Equal(t, map[string]int64{"claim/GET": 2, "other/GET": 1}, counters[0].Calls)
	assert.Equal(t, int64(writeReset), counters[1].Reset.Unix())
	assert.Equal(t, map[string]int64{"claim/POST": 1}, counters[1].Calls)

	counters[0].Calls["claim/GET"] = 100
	assert.Equal(t, int64(2), c.APIQuotaCounters()[0].Calls["claim/GET"], "callers get a copy")
}
