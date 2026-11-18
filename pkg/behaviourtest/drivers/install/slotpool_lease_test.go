package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

func TestPoolLayoutForEnvironment_HonoursPoolSize(t *testing.T) {
	t.Setenv("BEHAVIOUR_POOL_SIZE", "5")
	dev := PoolLayoutForEnvironment("dev", nil)
	assert.Equal(t, 5, dev.SlotsPerOrg)
	assert.Len(t, dev.Slots(), DevPoolOrgCount*5)
	stage := PoolLayoutForEnvironment("stage", nil)
	assert.Equal(t, 5, stage.SlotsPerOrg)
	assert.Len(t, stage.Slots(), 5)

	// An unparsable override falls back to the default and warns.
	t.Setenv("BEHAVIOUR_POOL_SIZE", "many")
	var warned []string
	l := PoolLayoutForEnvironment("stage", func(format string, args ...any) {
		warned = append(warned, fmt.Sprintf(format, args...))
	})
	assert.Equal(t, DefaultPoolSize, l.SlotsPerOrg)
	require.Len(t, warned, 1)
	assert.Contains(t, warned[0], "BEHAVIOUR_POOL_SIZE")
}

func TestSlotAllocator_AdoptsOwnRepoAfterLostCreateResponse(t *testing.T) {
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	a.shuffle = func([]Slot) {}

	// The first POST created the repo but its response was lost; the
	// client's retry then reported "already exists".
	p.clients["halfsend"].createLostErr = fmt.Errorf("create repo: %w", forge.ErrAlreadyExists)
	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Slot{Org: "halfsend", Repo: "test-repo-01"}, s)
	assert.Equal(t, []Slot{s}, a.Held())

	repo, err := p.fc.GetRepo(context.Background(), s.Org, s.Repo)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(repo.Description, SlotRepoDescription+leaseDescriptionSep))

	// The adopted lease releases normally.
	require.NoError(t, a.Release(context.Background(), s))
	assert.False(t, p.exists(t, s))
}

func TestSlotAllocator_ForeignRepoOnAlreadyExistsIsNotAdopted(t *testing.T) {
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	// Not visible to the listing hint, so the create is what finds it.
	p.fc.CreatedRepos = append(p.fc.CreatedRepos, forge.Repository{
		ID: 7, Name: "test-repo-01", FullName: "halfsend/test-repo-01", Description: SlotRepoDescription + leaseDescriptionSep + "someone-else",
	})
	a := newTestAllocator(t, layout, p)

	_, _, err := a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, 1, capErr.Occupied)
	assert.Empty(t, a.Held())
	assert.Empty(t, p.fc.DeletedRepos)
}

func TestSlotAllocator_ReleaseNeverDeletesRecreatedRepo(t *testing.T) {
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	ctx := context.Background()

	s, _, err := a.Claim(ctx)
	require.NoError(t, err)

	// A's earlier delete landed with a lost response; B recreated the name.
	require.NoError(t, p.fc.DeleteRepo(ctx, s.Org, s.Repo))
	_, err = p.fc.CreateRepo(ctx, s.Org, s.Repo, newLeaseDescription(), false)
	require.NoError(t, err)

	require.NoError(t, a.Release(ctx, s))
	assert.Equal(t, []string{s.String()}, p.fc.DeletedRepos, "only the simulated first delete; B's repo is untouched")
	assert.True(t, p.exists(t, s))
}

func TestSlotAllocator_ReleaseAfterLostDeleteResponse(t *testing.T) {
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	ctx := context.Background()

	s, _, err := a.Claim(ctx)
	require.NoError(t, err)

	p.clients["halfsend"].deleteLostErr = errors.New("response timeout")
	require.NoError(t, a.Release(ctx, s), "a re-check shows the repo is gone, so the delete is not re-sent or failed")
	assert.Equal(t, []string{s.String()}, p.fc.DeletedRepos)
	assert.False(t, p.exists(t, s))
}

func TestSlotAllocator_ReleaseVerifyFailureDeletesNothing(t *testing.T) {
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	ctx := context.Background()

	s, _, err := a.Claim(ctx)
	require.NoError(t, err)

	p.fc.Errors["GetRepo"] = errors.New("api down")
	err = a.Release(ctx, s)
	require.ErrorContains(t, err, "api down")
	assert.Empty(t, p.fc.DeletedRepos)
}

// expiredTokenClient models a claim-time client whose installation token
// has since expired: reads and deletes are refused.
type expiredTokenClient struct{ *orgClient }

func (expiredTokenClient) GetRepo(context.Context, string, string) (*forge.Repository, error) {
	return nil, &gh.APIError{StatusCode: http.StatusUnauthorized, Message: "Bad credentials"}
}

func (expiredTokenClient) DeleteRepo(context.Context, string, string) error {
	return &gh.APIError{StatusCode: http.StatusUnauthorized, Message: "Bad credentials"}
}

func TestSlotAllocator_ReleaseReResolvesClient(t *testing.T) {
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	calls := 0
	a, err := newSlotAllocator(layout, func(_ context.Context, org string) (forge.Client, error) {
		calls++
		if calls == 1 {
			return expiredTokenClient{p.clients[org]}, nil
		}
		return p.clients[org], nil
	}, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	require.NoError(t, a.Release(context.Background(), s), "release uses the freshly resolved client")
	assert.Equal(t, 2, calls)
	assert.False(t, p.exists(t, s))
}

func TestSlotAllocator_ReleaseFallsBackToClaimClient(t *testing.T) {
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	calls := 0
	a, err := newSlotAllocator(layout, func(_ context.Context, org string) (forge.Client, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("mint unavailable")
		}
		return p.clients[org], nil
	}, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	require.NoError(t, a.Release(context.Background(), s))
	assert.False(t, p.exists(t, s))
}

func TestSlotAllocator_ListingAvoidsCreateProbes(t *testing.T) {
	layout := StagePoolLayout(4)
	p := newFakePool(layout)
	for _, s := range layout.Slots()[:3] {
		p.fc.Repos = append(p.fc.Repos, forge.Repository{Name: s.Repo, FullName: s.String()})
	}
	a := newTestAllocator(t, layout, p)
	a.shuffle = func([]Slot) {}

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-repo-04", s.Repo)
	assert.Equal(t, 1, p.clients["halfsend"].createCalls(), "occupied slots are skipped from the listing, not probed")

	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, 4, capErr.Occupied)
	assert.Equal(t, 1, p.clients["halfsend"].createCalls(), "a full pass makes no create calls")
}

func TestSlotAllocator_ListingFailureFallsBackToProbing(t *testing.T) {
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	p.fc.Repos = append(p.fc.Repos, forge.Repository{Name: "test-repo-01", FullName: "halfsend/test-repo-01"})
	p.fc.Errors["ListOrgRepos"] = errors.New("listing unavailable")
	a := newTestAllocator(t, layout, p)
	a.shuffle = func([]Slot) {}

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-repo-02", s.Repo)
	assert.Equal(t, 2, p.clients["halfsend"].createCalls(), "without a listing every slot is probed by create")
}

func TestSlotAllocator_ListingRateLimitClosesOrg(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 2}
	p := newFakePool(layout)
	p.fc.Errors["ListOrgRepos"] = &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "rate limit: too many"}
	a := newTestAllocator(t, layout, p)

	_, _, err := a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.RateLimited, "halfsend-01")
	assert.Contains(t, capErr.RateLimited, "halfsend-02")
	assert.Zero(t, p.clients["halfsend-01"].createCalls())
	assert.Zero(t, p.clients["halfsend-02"].createCalls())
}

func shortCreateTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := claimCreateTimeout
	claimCreateTimeout = d
	t.Cleanup(func() { claimCreateTimeout = old })
}

func TestSlotAllocator_CreateTimeoutBacksOffOrg(t *testing.T) {
	shortCreateTimeout(t, 20*time.Millisecond)
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 1}
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	a.shuffle = func([]Slot) {}
	p.clients["halfsend-01"].createBlocks = true

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "halfsend-02", s.Org, "a create stuck in backoff moves the claim to another org")
	assert.Equal(t, 1, p.clients["halfsend-01"].createCalls())

	// The stuck org stays closed for the backoff window.
	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.RateLimited, "halfsend-01")
	assert.Equal(t, 1, p.clients["halfsend-01"].createCalls())
}

func TestSlotAllocator_CreateTimeoutAdoptsLandedRepo(t *testing.T) {
	shortCreateTimeout(t, 20*time.Millisecond)
	layout := StagePoolLayout(1)
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	p.clients["halfsend"].createLandsThenBlocks = true

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []Slot{s}, a.Held())
	require.NoError(t, a.Release(context.Background(), s))
	assert.False(t, p.exists(t, s))
}

// TestSlotAllocator_LiveClientRateLimitDoesNotPinClaim drives the real
// LiveClient, whose retry loop would otherwise sleep out the server's
// Retry-After inside CreateRepo.
func TestSlotAllocator_LiveClientRateLimitDoesNotPinClaim(t *testing.T) {
	shortCreateTimeout(t, 100*time.Millisecond)
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/halfsend/repos":
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/halfsend/repos":
			posts.Add(1)
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	layout := StagePoolLayout(3)
	client := gh.New("test-token").WithBaseURL(srv.URL)
	a, err := newSlotAllocator(layout, func(context.Context, string) (forge.Client, error) { return client, nil }, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil

	start := time.Now()
	_, _, err = a.Claim(context.Background())
	elapsed := time.Since(start)

	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.RateLimited, "halfsend")
	assert.Less(t, elapsed, 10*time.Second, "the claim must not wait out the client's retry backoff")
	assert.Equal(t, int32(1), posts.Load(), "the limited org is not hit again after the first create")
}

// TestSlotAllocator_LiveClientTimeoutKeepsRetryAfter checks that cutting a
// create off during the client's rate-limit backoff still blocks the org
// for the Retry-After wait the client was honouring, not the default
// window.
func TestSlotAllocator_LiveClientTimeoutKeepsRetryAfter(t *testing.T) {
	shortCreateTimeout(t, 100*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/halfsend/repos":
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/halfsend/repos":
			w.Header().Set("Retry-After", "300")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := gh.New("test-token").WithBaseURL(srv.URL)
	a, err := newSlotAllocator(StagePoolLayout(1), func(context.Context, string) (forge.Client, error) { return client, nil }, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil

	start := time.Now()
	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	until, ok := capErr.RateLimited["halfsend"]
	require.True(t, ok)
	assert.Greater(t, until.Sub(start), 4*time.Minute, "the org stays closed for the Retry-After wait, not the one-minute default")
}

// TestSlotAllocator_LiveClientThrottledListingMovesToNextOrg checks that a
// throttled org listing is bounded and does not pin the claim before
// another org with capacity is tried.
func TestSlotAllocator_LiveClientThrottledListingMovesToNextOrg(t *testing.T) {
	shortCreateTimeout(t, 100*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/halfsend-01/repos":
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/halfsend-02/repos":
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/halfsend-02/repos":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":7,"name":"test-repo-01","full_name":"halfsend-02/test-repo-01"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 1}
	client := gh.New("test-token").WithBaseURL(srv.URL)
	a, err := newSlotAllocator(layout, func(context.Context, string) (forge.Client, error) { return client, nil }, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil
	a.shuffle = func([]Slot) {}

	start := time.Now()
	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "halfsend-02", s.Org)
	assert.Less(t, time.Since(start), 10*time.Second, "the throttled listing must not wait out the client's backoff")
	until, blocked := a.orgBlockedUntil("halfsend-01")
	require.True(t, blocked)
	assert.Greater(t, time.Until(until), time.Minute, "the throttled org stays closed for its Retry-After wait")
}

// TestSlotAllocator_LiveClientDeleteNotReplayedAfterServerError checks
// that Release does not let the live client replay a DELETE after an
// ambiguous 5xx: the first attempt landed and the name was recreated by
// another allocator, so a replay would delete the new lease's repository.
func TestSlotAllocator_LiveClientDeleteNotReplayedAfterServerError(t *testing.T) {
	var deletes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/halfsend/repos":
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/halfsend/repos":
			var body struct {
				Description string `json:"description"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "name": "test-repo-01", "full_name": "halfsend/test-repo-01", "description": body.Description,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/halfsend/test-repo-01":
			id, desc := 1, ""
			if deletes.Load() > 0 {
				// The first DELETE landed and another allocator
				// recreated the name.
				id, desc = 2, "someone else's lease"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": id, "name": "test-repo-01", "full_name": "halfsend/test-repo-01", "description": desc,
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/halfsend/test-repo-01":
			deletes.Add(1)
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := gh.New("test-token").WithBaseURL(srv.URL)
	a, err := newSlotAllocator(StagePoolLayout(1), func(context.Context, string) (forge.Client, error) { return client, nil }, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	require.NoError(t, a.Release(context.Background(), s))
	assert.Equal(t, int32(1), deletes.Load(), "the ambiguous DELETE must not be replayed")
}

// rateLimitedDeleteServer serves one slot repo for the live client. Every
// DELETE is rejected with the given rate-limit status (unless
// okAfterFirst). replaced switches GET to another holder's repo.
type rateLimitedDeleteServer struct {
	status   int
	body     string
	deletes  atomic.Int32
	replaced atomic.Bool
	// okAfterFirst makes DELETEs after the first one succeed.
	okAfterFirst bool
}

func (s *rateLimitedDeleteServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/orgs/halfsend/repos":
		_, _ = w.Write([]byte("[]"))
	case r.Method == http.MethodPost && r.URL.Path == "/orgs/halfsend/repos":
		var body struct {
			Description string `json:"description"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 1, "name": "test-repo-01", "full_name": "halfsend/test-repo-01", "description": body.Description,
		})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/halfsend/test-repo-01":
		id, desc := 1, ""
		if s.replaced.Load() {
			id, desc = 2, SlotRepoDescription+leaseDescriptionSep+"another-holder"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "name": "test-repo-01", "full_name": "halfsend/test-repo-01", "description": desc,
		})
	case r.Method == http.MethodDelete && r.URL.Path == "/repos/halfsend/test-repo-01":
		if s.deletes.Add(1) > 1 && s.okAfterFirst {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// TestSlotAllocator_LiveClientRateLimitedDeleteRechecksOwnership checks
// that when the live client waits out a 429 or rate-limit 403 on Release's
// DELETE, and another holder's repo replaces the slot during the wait, the
// DELETE is not resent.
func TestSlotAllocator_LiveClientRateLimitedDeleteRechecksOwnership(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "429", status: http.StatusTooManyRequests, body: `{"message":"API rate limit exceeded"}`},
		{name: "rate-limit 403", status: http.StatusForbidden, body: `{"message":"You have exceeded a secondary rate limit"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srvState := &rateLimitedDeleteServer{status: tt.status, body: tt.body}
			srv := httptest.NewServer(srvState)
			defer srv.Close()

			// The client's rate-limit wait is where GC deletes and another
			// holder recreates the slot.
			client := gh.New("test-token").WithBaseURL(srv.URL).WithAfterFunc(func(time.Duration) <-chan time.Time {
				srvState.replaced.Store(true)
				ch := make(chan time.Time, 1)
				ch <- time.Now()
				return ch
			})
			a, err := newSlotAllocator(StagePoolLayout(1), func(context.Context, string) (forge.Client, error) { return client, nil }, t.Logf)
			require.NoError(t, err)
			a.orgLock = nil

			s, _, err := a.Claim(context.Background())
			require.NoError(t, err)
			require.NoError(t, a.Release(context.Background(), s), "a replaced repo counts as already released")
			assert.Equal(t, int32(1), srvState.deletes.Load(), "the DELETE must not be resent against another holder's repo")
			assert.Empty(t, a.Held())
		})
	}
}

// TestSlotAllocator_LiveClientRateLimitedDeleteRetriesWhenStillOurs checks
// that the ownership re-check lets the rate-limited DELETE be resent when
// the slot repo is still this lease's.
func TestSlotAllocator_LiveClientRateLimitedDeleteRetriesWhenStillOurs(t *testing.T) {
	srvState := &rateLimitedDeleteServer{
		status:       http.StatusTooManyRequests,
		body:         `{"message":"API rate limit exceeded"}`,
		okAfterFirst: true,
	}
	srv := httptest.NewServer(srvState)
	defer srv.Close()

	client := gh.New("test-token").WithBaseURL(srv.URL).WithAfterFunc(func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	})
	a, err := newSlotAllocator(StagePoolLayout(1), func(context.Context, string) (forge.Client, error) { return client, nil }, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	require.NoError(t, a.Release(context.Background(), s))
	assert.Equal(t, int32(2), srvState.deletes.Load(), "the DELETE is resent once ownership is confirmed")
}

// TestSlotAllocator_ReleaseGuardLookupFailureDeletesNothing checks that a
// failed ownership re-check after a rate-limit wait does not resend the
// DELETE and reports the failure.
func TestSlotAllocator_ReleaseGuardLookupFailureDeletesNothing(t *testing.T) {
	var deletes, gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/halfsend/repos":
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/halfsend/repos":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"name":"test-repo-01","full_name":"halfsend/test-repo-01"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/halfsend/test-repo-01":
			if gets.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"id":1,"name":"test-repo-01","full_name":"halfsend/test-repo-01"}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/halfsend/test-repo-01":
			deletes.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
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

	s, _, err := a.Claim(context.Background())
	require.NoError(t, err)
	err = a.Release(context.Background(), s)
	require.ErrorContains(t, err, "re-verifying repo after retry wait")
	assert.Equal(t, int32(1), deletes.Load())
}
