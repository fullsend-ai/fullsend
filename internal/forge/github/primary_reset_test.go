package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingAfter returns an afterFunc that is immediately ready and records
// every requested delay.
func recordingAfter() (func(time.Duration) <-chan time.Time, func() []time.Duration) {
	var (
		mu     sync.Mutex
		delays []time.Duration
	)
	after := func(d time.Duration) <-chan time.Time {
		mu.Lock()
		delays = append(delays, d)
		mu.Unlock()
		return noWaitAfter(d)
	}
	get := func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Duration(nil), delays...)
	}
	return after, get
}

func primaryLimitHandler(t *testing.T, calls *int, resetIn time.Duration, succeedAfter int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if *calls > succeedAfter {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(resetIn).Unix(), 10))
		w.Header().Set("X-RateLimit-Resource", "core")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for installation ID 1."}`))
	}
}

func TestPrimaryResetWait(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	resp := func(status int, kv ...string) *http.Response {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return &http.Response{StatusCode: status, Header: h}
	}
	reset := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) }

	tests := []struct {
		name string
		resp *http.Response
		want time.Duration
		ok   bool
	}{
		{"403 exhausted waits for reset plus a second", resp(403, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset(10*time.Minute)), 10*time.Minute + time.Second, true},
		{"429 exhausted", resp(429, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset(30*time.Second)), 31 * time.Second, true},
		{"reset already passed waits one second", resp(403, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset(-time.Minute)), time.Second, true},
		{"retry-after wins", resp(403, "Retry-After", "5", "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset(10*time.Minute)), 0, false},
		{"budget left is a secondary limit", resp(403, "X-RateLimit-Remaining", "12", "X-RateLimit-Reset", reset(10*time.Minute)), 0, false},
		{"no reset header", resp(403, "X-RateLimit-Remaining", "0"), 0, false},
		{"bad reset header", resp(403, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", "soon"), 0, false},
		{"absurd reset is ignored", resp(403, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset(24*time.Hour)), 0, false},
		{"far-future reset is ignored, not wrapped", resp(403, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", strconv.FormatInt(1<<62, 10)), 0, false},
		{"reset at the bound is accepted", resp(403, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset(maxPrimaryResetWait-time.Second)), maxPrimaryResetWait, true},
		{"reset just past the bound is ignored", resp(403, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset(maxPrimaryResetWait)), 0, false},
		{"other status", resp(502, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset", reset(time.Minute)), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := primaryResetWait(tt.resp, now)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDo_PrimaryRateLimitWaitsForReset(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(primaryLimitHandler(t, &calls, 9*time.Minute, 1))
	defer srv.Close()

	after, delays := recordingAfter()
	client := New("test-token").WithBaseURL(srv.URL).WithAfterFunc(after)

	err := client.DeleteRepo(context.Background(), "org", "repo")
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "one rate-limited call, then the retry after the reset")
	got := delays()
	require.Len(t, got, 1)
	assert.InDelta(t, (9*time.Minute + time.Second).Seconds(), got[0].Seconds(), 2,
		"must wait for X-RateLimit-Reset, not the short secondary-limit backoff")
}

func TestDo_PrimaryRateLimitBeyondDeadlineFailsFast(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(primaryLimitHandler(t, &calls, 9*time.Minute, 100))
	defer srv.Close()

	after, delays := recordingAfter()
	client := New("test-token").WithBaseURL(srv.URL).WithAfterFunc(after)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := client.DeleteRepo(ctx, "org", "repo")
	require.Error(t, err)
	assert.True(t, IsRateLimitError(err), "the error must name the rate limit, not a deadline: %v", err)
	assert.Contains(t, err.Error(), "beyond the context deadline")
	assert.Contains(t, err.Error(), "remaining=0/5000")
	assert.Equal(t, 1, calls, "no point retrying inside the exhausted window")
	assert.Empty(t, delays())
}

func TestDo_PrimaryRateLimitWithoutResetKeepsBackoff(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit."}`))
	}))
	defer srv.Close()

	orig := secondaryRateLimitBackoff
	defer func() { secondaryRateLimitBackoff = orig }()
	secondaryRateLimitBackoff = 10 * time.Millisecond

	after, delays := recordingAfter()
	client := New("test-token").WithBaseURL(srv.URL).WithAfterFunc(after)
	require.NoError(t, client.DeleteRepo(context.Background(), "org", "repo"))
	got := delays()
	require.Len(t, got, 1)
	assert.Less(t, got[0], time.Second, "a secondary limit keeps the configured backoff")
}
