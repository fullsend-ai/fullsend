package install

// Tests for how Claim classifies a failed CreateRepo (#8652): only errors
// that can hide a landed create get an ownership lookup and a pending
// nonce, and throttling errors back the org off on every outcome.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

// lookupCountingClient counts ownership lookups (GetRepo).
type lookupCountingClient struct {
	*orgClient
	lookups atomic.Int32
}

func (c *lookupCountingClient) GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error) {
	c.lookups.Add(1)
	return c.orgClient.GetRepo(ctx, owner, repo)
}

func newLookupCountingAllocator(t *testing.T, layout PoolLayout, p *fakePool) (*slotAllocator, *lookupCountingClient) {
	t.Helper()
	lc := &lookupCountingClient{orgClient: p.clients["halfsend"]}
	a, err := newSlotAllocator(layout, func(context.Context, string) (forge.Client, error) { return lc, nil }, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil
	a.gc = nil
	a.shuffle = func([]Slot) {}
	return a, lc
}

func TestSlotAllocator_DefinitiveCreateErrorSkipsOwnershipLookup(t *testing.T) {
	for name, status := range map[string]int{
		"400": http.StatusBadRequest,
		"404": http.StatusNotFound,
		"451": http.StatusUnavailableForLegalReasons,
	} {
		t.Run(name, func(t *testing.T) {
			layout := StagePoolLayout(2)
			p := newFakePool(layout)
			p.clients["halfsend"].createErr = &gh.APIError{StatusCode: status, Message: "refused"}
			a, lc := newLookupCountingAllocator(t, layout, p)

			_, _, err := a.Claim(context.Background())
			var capErr *CapacityError
			require.ErrorAs(t, err, &capErr)
			assert.Contains(t, capErr.Failed, "halfsend")
			assert.Empty(t, capErr.RateLimited)
			assert.Zero(t, lc.lookups.Load(), "a refused create sends no ownership lookup")
			for _, s := range layout.Slots() {
				assert.Empty(t, a.pendingFor(s), "a refused create leaves no pending nonce")
			}
		})
	}
}

func TestSlotAllocator_NonRateLimit403IsDeniedWithoutLookup(t *testing.T) {
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	p.clients["halfsend"].createErr = &gh.APIError{StatusCode: http.StatusForbidden, Message: "Resource not accessible by integration"}
	a, lc := newLookupCountingAllocator(t, layout, p)

	_, _, err := a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.Denied, "halfsend")
	assert.Zero(t, lc.lookups.Load())
	assert.Equal(t, 1, p.clients["halfsend"].createCalls(), "a denied org is skipped for the rest of the pass")
	assert.Empty(t, a.pendingFor(layout.Slots()[0]))
}

func TestSlotAllocator_Unprocessable422BacksOffOrg(t *testing.T) {
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	c := p.clients["halfsend"]
	c.createErr = &gh.APIError{StatusCode: http.StatusUnprocessableEntity, Message: "Validation Failed"}
	a, lc := newLookupCountingAllocator(t, layout, p)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	_, _, err := a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, now.Add(defaultRateLimitWindow), capErr.RateLimited["halfsend"])
	assert.Empty(t, capErr.Failed)
	assert.Equal(t, 1, c.createCalls(), "the rest of the pass skips the backed-off org")
	assert.Zero(t, lc.lookups.Load())
	assert.Len(t, a.pendingFor(layout.Slots()[0]), 1, "an earlier attempt may have landed")

	// A later pass inside the window still skips it.
	c.createErr = nil
	now = now.Add(defaultRateLimitWindow / 2)
	_, _, err = a.Claim(context.Background())
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.RateLimited, "halfsend")
	assert.Equal(t, 1, c.createCalls())

	// Past the deadline the org is claimable again.
	now = now.Add(defaultRateLimitWindow)
	_, _, err = a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, c.createCalls())
}

func TestSlotAllocator_RateLimitedCreateAdoptsOwnRepoAndBacksOff(t *testing.T) {
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	c := p.clients["halfsend"]
	// An earlier attempt landed; the client's retries then hit a 429.
	c.createLostErr = &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "too many", RetryAfter: 2 * time.Minute}
	a, lc := newLookupCountingAllocator(t, layout, p)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err, "the landed create is adopted")
	assert.Equal(t, layout.Slots()[0], s)
	assert.Equal(t, int32(1), lc.lookups.Load())
	until, blocked := a.orgBlockedUntil("halfsend")
	require.True(t, blocked, "adopting does not drop the rate-limit backoff")
	assert.Equal(t, now.Add(2*time.Minute), until)

	c.createLostErr = nil
	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.RateLimited, "halfsend")
	assert.Equal(t, 1, c.createCalls(), "the next claim skips the org until the deadline")
}

// TestSlotAllocator_LiveClientRateLimitAfterLandedCreate drives the real
// LiveClient against a server whose first create attempt lands but answers
// 502, and whose retries are then rate-limited.
func TestSlotAllocator_LiveClientRateLimitAfterLandedCreate(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"secondary rate-limit 403": {status: http.StatusForbidden, body: `{"message":"You have exceeded a secondary rate limit"}`},
		"429":                      {status: http.StatusTooManyRequests, body: `{"message":"too many requests"}`},
	} {
		t.Run(name, func(t *testing.T) {
			var (
				mu      sync.Mutex
				created string
				posts   atomic.Int32
				lookups atomic.Int32
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/orgs/halfsend/repos":
					_, _ = w.Write([]byte("[]"))
				case r.Method == http.MethodPost && r.URL.Path == "/orgs/halfsend/repos":
					if posts.Add(1) == 1 {
						var body struct {
							Description string `json:"description"`
						}
						_ = json.NewDecoder(r.Body).Decode(&body)
						mu.Lock()
						created = body.Description
						mu.Unlock()
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					w.Header().Set("Retry-After", "120")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				case r.Method == http.MethodGet && r.URL.Path == "/repos/halfsend/test-repo-01":
					lookups.Add(1)
					mu.Lock()
					desc := created
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(map[string]any{
						"id": 42, "name": "test-repo-01", "full_name": "halfsend/test-repo-01", "description": desc,
					})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			client := gh.New("test-token").WithBaseURL(srv.URL).WithAfterFunc(func(time.Duration) <-chan time.Time {
				ch := make(chan time.Time, 1)
				ch <- time.Now()
				return ch
			})
			a, err := newSlotAllocator(StagePoolLayout(1), func(context.Context, string) (forge.Client, error) { return client, nil }, t.Logf)
			require.NoError(t, err)
			a.orgLock = nil
			a.gc = nil
			now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
			a.now = func() time.Time { return now }

			s, _, err := a.Claim(context.Background())
			require.NoError(t, err, "the repo the first attempt created is adopted")
			assert.Greater(t, posts.Load(), int32(1), "the client retried after the 502")
			assert.Equal(t, int32(1), lookups.Load())
			a.mu.Lock()
			l := a.held[s]
			a.mu.Unlock()
			assert.Equal(t, int64(42), l.repoID)
			until, blocked := a.orgBlockedUntil("halfsend")
			require.True(t, blocked, "the rate limit backs the org off despite the adoption")
			assert.Equal(t, now.Add(120*time.Second), until)
		})
	}
}

// TestSlotAllocator_LiveClientUnprocessableCreateBacksOff drives the real
// LiveClient against a server that answers the create with a 422 that is
// not "already exists".
func TestSlotAllocator_LiveClientUnprocessableCreateBacksOff(t *testing.T) {
	var posts, lookups atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/halfsend/repos":
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/halfsend/repos":
			posts.Add(1)
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"Repository","code":"custom","message":"try again later"}]}`))
		case r.Method == http.MethodGet && r.URL.Path != "/orgs/halfsend/repos":
			lookups.Add(1)
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := gh.New("test-token").WithBaseURL(srv.URL)
	a, err := newSlotAllocator(StagePoolLayout(2), func(context.Context, string) (forge.Client, error) { return client, nil }, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil
	a.gc = nil

	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.RateLimited, "halfsend")
	assert.Equal(t, int32(1), posts.Load(), "the backed-off org gets no second create in the pass")
	assert.Zero(t, lookups.Load())
}

// TestSlotAllocator_LiveClientUnprocessableAfterLandedCreate covers a 502
// that hid a landed create followed by a 422 on the client's retry: the org
// backs off without a lookup, and a later pass adopts the repo through the
// pending nonce.
func TestSlotAllocator_LiveClientUnprocessableAfterLandedCreate(t *testing.T) {
	var (
		mu      sync.Mutex
		created string
		posts   atomic.Int32
		lookups atomic.Int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		desc := created
		mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/halfsend/repos":
			if desc == "" {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 42, "name": "test-repo-01", "full_name": "halfsend/test-repo-01"}})
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/halfsend/repos":
			if posts.Add(1) == 1 {
				var body struct {
					Description string `json:"description"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				created = body.Description
				mu.Unlock()
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"Repository","code":"custom","message":"try again later"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/halfsend/test-repo-01":
			lookups.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "name": "test-repo-01", "full_name": "halfsend/test-repo-01", "description": desc,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := gh.New("test-token").WithBaseURL(srv.URL).WithAfterFunc(func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	})
	a, err := newSlotAllocator(StagePoolLayout(1), func(context.Context, string) (forge.Client, error) { return client, nil }, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil
	a.gc = nil
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.RateLimited, "halfsend")
	assert.Equal(t, int32(2), posts.Load(), "the client retried after the 502")
	assert.Zero(t, lookups.Load())

	now = now.Add(2 * defaultRateLimitWindow)
	s, _, err := a.Claim(context.Background())
	require.NoError(t, err, "the repo the first attempt created is adopted")
	assert.Equal(t, int32(2), posts.Load(), "the listed name is re-checked, not created again")
	assert.Equal(t, int32(1), lookups.Load())
	a.mu.Lock()
	l := a.held[s]
	a.mu.Unlock()
	assert.Equal(t, int64(42), l.repoID)
}

// TestSlotAllocator_LiveClientCreateDeadlineCutsRateLimitWait covers a 502
// that hid a landed create followed by a secondary-limit 403 whose
// Retry-After wait outlasts the create deadline: the repo is adopted and
// the org stays blocked until the server's deadline.
func TestSlotAllocator_LiveClientCreateDeadlineCutsRateLimitWait(t *testing.T) {
	shortCreateTimeout(t, 200*time.Millisecond)
	var (
		mu      sync.Mutex
		created string
		posts   atomic.Int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/halfsend/repos":
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/halfsend/repos":
			if posts.Add(1) == 1 {
				var body struct {
					Description string `json:"description"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				created = body.Description
				mu.Unlock()
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/halfsend/test-repo-01":
			mu.Lock()
			desc := created
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "name": "test-repo-01", "full_name": "halfsend/test-repo-01", "description": desc,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// The 5xx backoff fires at once; the Retry-After wait never does, so
	// the create deadline cuts it off.
	client := gh.New("test-token").WithBaseURL(srv.URL).WithAfterFunc(func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		if d < time.Minute {
			ch <- time.Now()
		}
		return ch
	})
	a, err := newSlotAllocator(StagePoolLayout(1), func(context.Context, string) (forge.Client, error) { return client, nil }, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil
	a.gc = nil
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	_, _, err = a.Claim(context.Background())
	require.NoError(t, err, "the repo the first attempt created is adopted")
	assert.Equal(t, int32(2), posts.Load())
	until, blocked := a.orgBlockedUntil("halfsend")
	require.True(t, blocked, "the rate limit backs the org off despite the adoption")
	assert.Equal(t, now.Add(120*time.Second), until)
}
