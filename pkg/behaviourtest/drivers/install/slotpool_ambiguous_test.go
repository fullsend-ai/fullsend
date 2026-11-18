package install

// Tests for recovering our own slot repo after an ambiguous create in
// Claim (#8593): the create may have landed even though the claim saw an
// error, so the claimer looks for its lease nonce before giving up and
// re-checks unresolved nonces on later passes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

func TestSlotAllocator_AdoptsOwnRepoAfterConnectionReset(t *testing.T) {
	layout := StagePoolLayout(2)
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	a.shuffle = func([]Slot) {}
	ctx := context.Background()

	// The POST landed but the connection was reset before the response.
	p.clients["halfsend"].createLostErr = fmt.Errorf("create repo: read tcp: %w", syscall.ECONNRESET)
	s, _, err := a.Claim(ctx)
	require.NoError(t, err)
	assert.Equal(t, Slot{Org: "halfsend", Repo: "test-repo-01"}, s)
	assert.Equal(t, []Slot{s}, a.Held())
	assert.Equal(t, 1, p.clients["halfsend"].createCalls(), "the landed create is adopted, not abandoned for another slot")
	assert.Empty(t, a.pendingFor(s))

	require.NoError(t, a.Release(ctx, s))
	assert.False(t, p.exists(t, s))
}

// TestSlotAllocator_LiveClientAdoptsOwnRepoAfterUndecodableCreateResponse
// drives the real LiveClient against a server that answers the create
// with 201 and a body that cannot be decoded.
func TestSlotAllocator_LiveClientAdoptsOwnRepoAfterUndecodableCreateResponse(t *testing.T) {
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
			posts.Add(1)
			var body struct {
				Description string `json:"description"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			created = body.Description
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":42,"name":`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/halfsend/test-repo-01":
			mu.Lock()
			desc := created
			mu.Unlock()
			if desc == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "name": "test-repo-01", "full_name": "halfsend/test-repo-01", "description": desc,
			})
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
	require.NoError(t, err, "the repo our create made is adopted despite the undecodable response")
	assert.Equal(t, int32(1), posts.Load())
	a.mu.Lock()
	l := a.held[s]
	a.mu.Unlock()
	assert.Equal(t, int64(42), l.repoID, "the adopted lease carries the ID from the lookup")
}

func TestSlotAllocator_AdoptsCreateLandingAfterTimeoutLookup(t *testing.T) {
	for name, listed := range map[string]bool{"visible to listing": true, "hidden from listing": false} {
		t.Run(name, func(t *testing.T) {
			shortCreateTimeout(t, 20*time.Millisecond)
			layout := StagePoolLayout(1)
			slot := layout.Slots()[0]
			p := newFakePool(layout)
			a := newTestAllocator(t, layout, p)
			now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
			a.now = func() time.Time { return now }
			ctx := context.Background()
			c := p.clients["halfsend"]
			c.createLandsLate = true

			_, _, err := a.Claim(ctx)
			var capErr *CapacityError
			require.ErrorAs(t, err, &capErr)
			assert.Contains(t, capErr.RateLimited, "halfsend")
			require.NotEmpty(t, c.lateDesc)
			assert.Equal(t, []string{c.lateDesc}, a.pendingFor(slot), "the unresolved create is remembered")

			// The create lands after the timeout-path lookup found nothing.
			c.createLandsLate = false
			late := forge.Repository{ID: 77, Name: slot.Repo, FullName: slot.String(), Description: c.lateDesc}
			if listed {
				p.fc.Repos = append(p.fc.Repos, late)
			} else {
				p.fc.CreatedRepos = append(p.fc.CreatedRepos, late)
			}

			now = now.Add(defaultRateLimitWindow)
			s, _, err := a.Claim(ctx)
			require.NoError(t, err, "a later pass adopts the late-landing repo instead of treating it as occupied")
			assert.Equal(t, slot, s)
			assert.Empty(t, a.pendingFor(slot))
			wantCreates := 2
			if listed {
				wantCreates = 1
			}
			assert.Equal(t, wantCreates, c.createCalls())

			require.NoError(t, a.Release(ctx, s))
			assert.False(t, p.exists(t, s))
		})
	}
}

func TestSlotAllocator_OwnershipLookupFailureOnAlreadyExistsIsUnknown(t *testing.T) {
	for name, tc := range map[string]struct {
		lookupErr error
		check     func(t *testing.T, capErr *CapacityError)
	}{
		"lookup failed": {
			lookupErr: errors.New("api down"),
			check: func(t *testing.T, capErr *CapacityError) {
				require.Contains(t, capErr.Failed, "halfsend")
				assert.ErrorContains(t, capErr.Failed["halfsend"], "api down")
			},
		},
		"lookup throttled": {
			lookupErr: &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "too many"},
			check: func(t *testing.T, capErr *CapacityError) {
				assert.Contains(t, capErr.RateLimited, "halfsend")
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			layout := StagePoolLayout(1)
			p := newFakePool(layout)
			a := newTestAllocator(t, layout, p)
			now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
			a.now = func() time.Time { return now }
			ctx := context.Background()
			c := p.clients["halfsend"]

			// Our create landed, the client's retry reported "already
			// exists", and the ownership lookup then failed.
			c.createLostErr = fmt.Errorf("create repo: %w", forge.ErrAlreadyExists)
			p.fc.Errors["GetRepo"] = tc.lookupErr

			_, _, err := a.Claim(ctx)
			var capErr *CapacityError
			require.ErrorAs(t, err, &capErr)
			assert.Zero(t, capErr.Occupied, "a repo of unknown ownership is not counted as occupied")
			tc.check(t, capErr)
			assert.Empty(t, a.Held())

			// Once the lookup works, a later pass recognises the repo as ours.
			delete(p.fc.Errors, "GetRepo")
			c.createLostErr = nil
			now = now.Add(defaultRateLimitWindow)
			s, _, err := a.Claim(ctx)
			require.NoError(t, err)
			assert.Equal(t, []Slot{s}, a.Held())
		})
	}
}

func TestSlotAllocator_PendingRecheckOfListedSlot(t *testing.T) {
	layout := StagePoolLayout(1)
	slot := layout.Slots()[0]
	p := newFakePool(layout)
	a := newTestAllocator(t, layout, p)
	ctx := context.Background()
	a.addPending(slot, newLeaseDescription())
	p.fc.Repos = append(p.fc.Repos, forge.Repository{
		ID: 9, Name: slot.Repo, FullName: slot.String(), Description: newLeaseDescription(),
	})

	// A failed re-check leaves ownership unknown.
	p.fc.Errors["GetRepo"] = errors.New("api down")
	_, _, err := a.Claim(ctx)
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Zero(t, capErr.Occupied)
	assert.Contains(t, capErr.Failed, "halfsend")
	assert.Len(t, a.pendingFor(slot), 1)

	// Another lease's repo holds the name: occupied, and our pending
	// nonce is dropped since it can no longer land.
	delete(p.fc.Errors, "GetRepo")
	_, _, err = a.Claim(ctx)
	require.ErrorAs(t, err, &capErr)
	assert.Equal(t, 1, capErr.Occupied)
	assert.Empty(t, a.pendingFor(slot))
	assert.Zero(t, p.clients["halfsend"].createCalls())
	assert.Empty(t, p.fc.DeletedRepos)
}

func TestSlotAllocator_CreateErrorWithoutOwnRepo(t *testing.T) {
	t.Run("foreign repo", func(t *testing.T) {
		layout := StagePoolLayout(1)
		slot := layout.Slots()[0]
		p := newFakePool(layout)
		// Not visible to the listing hint, so the create is what runs into it.
		p.fc.CreatedRepos = append(p.fc.CreatedRepos, forge.Repository{
			ID: 7, Name: slot.Repo, FullName: slot.String(), Description: newLeaseDescription(),
		})
		p.clients["halfsend"].createErr = fmt.Errorf("create repo: read tcp: %w", syscall.ECONNRESET)
		a := newTestAllocator(t, layout, p)

		_, _, err := a.Claim(context.Background())
		var capErr *CapacityError
		require.ErrorAs(t, err, &capErr)
		assert.Equal(t, 1, capErr.Occupied)
		assert.Empty(t, capErr.Failed)
		assert.Empty(t, a.pendingFor(slot))
		assert.Empty(t, a.Held())
	})

	t.Run("lookup throttled", func(t *testing.T) {
		layout := StagePoolLayout(2)
		p := newFakePool(layout)
		p.clients["halfsend"].createErr = &gh.APIError{StatusCode: http.StatusBadGateway, Message: "bad gateway"}
		p.fc.Errors["GetRepo"] = &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "too many"}
		a := newTestAllocator(t, layout, p)
		a.shuffle = func([]Slot) {}

		_, _, err := a.Claim(context.Background())
		var capErr *CapacityError
		require.ErrorAs(t, err, &capErr)
		assert.Contains(t, capErr.Failed, "halfsend")
		assert.Contains(t, capErr.RateLimited, "halfsend", "a throttled lookup backs the org off")
		assert.Equal(t, 1, p.clients["halfsend"].createCalls())
		assert.Len(t, a.pendingFor(layout.Slots()[0]), 1, "the create of unknown outcome is re-checked later")
	})
}

// barrierClient holds every GetRepo until want callers are inside it, so
// concurrent ownership lookups for one slot are deterministically
// interleaved.
type barrierClient struct {
	*orgClient
	arrived chan struct{}
	release chan struct{}
	want    int
	// passthrough lets later GetRepo calls skip the barrier.
	passthrough atomic.Bool
}

func (c *barrierClient) GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error) {
	r, err := c.orgClient.GetRepo(ctx, owner, repo)
	if c.passthrough.Load() {
		return r, err
	}
	c.arrived <- struct{}{}
	select {
	case <-c.release:
	case <-time.After(5 * time.Second):
		return nil, errors.New("barrier timed out")
	}
	return r, err
}

func TestSlotAllocator_ConcurrentClaimsAdoptPendingLeaseOnce(t *testing.T) {
	layout := StagePoolLayout(1)
	slot := layout.Slots()[0]
	p := newFakePool(layout)
	desc := newLeaseDescription()
	p.fc.Repos = append(p.fc.Repos, forge.Repository{
		ID: 5, Name: slot.Repo, FullName: slot.String(), Description: desc,
	})
	bc := &barrierClient{
		orgClient: p.clients["halfsend"],
		arrived:   make(chan struct{}, 2),
		release:   make(chan struct{}),
		want:      2,
	}
	a, err := newSlotAllocator(layout, func(context.Context, string) (forge.Client, error) { return bc, nil }, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil
	a.addPending(slot, desc)

	type result struct {
		slot Slot
		err  error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			s, _, err := a.Claim(context.Background())
			results <- result{s, err}
		}()
	}
	// Both claims are inside their ownership lookup before either records.
	for range bc.want {
		select {
		case <-bc.arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("both claims should reach the ownership lookup")
		}
	}
	close(bc.release)

	var won, lost int
	for range 2 {
		r := <-results
		if r.err == nil {
			won++
			assert.Equal(t, slot, r.slot)
			continue
		}
		lost++
		var capErr *CapacityError
		require.ErrorAs(t, r.err, &capErr)
		assert.Equal(t, 1, capErr.Occupied, "the loser sees the slot as taken")
	}
	assert.Equal(t, 1, won, "exactly one claim adopts the pending lease")
	assert.Equal(t, 1, lost)
	assert.Equal(t, []Slot{slot}, a.Held())
	assert.Zero(t, bc.createCalls())
}

// TestSlotAllocator_DelayedAdoptionFailsAfterWinnerReleased pauses the
// second adoption of a pending lease until the first claim has been
// released: the slot is no longer held then, but the pending nonce was
// consumed, so the late adoption must not return a lease for the repo
// the winner already deleted.
func TestSlotAllocator_DelayedAdoptionFailsAfterWinnerReleased(t *testing.T) {
	layout := StagePoolLayout(1)
	slot := layout.Slots()[0]
	p := newFakePool(layout)
	desc := newLeaseDescription()
	p.fc.Repos = append(p.fc.Repos, forge.Repository{
		ID: 5, Name: slot.Repo, FullName: slot.String(), Description: desc,
	})
	bc := &barrierClient{
		orgClient: p.clients["halfsend"],
		arrived:   make(chan struct{}, 2),
		release:   make(chan struct{}),
		want:      2,
	}
	a, err := newSlotAllocator(layout, func(context.Context, string) (forge.Client, error) { return bc, nil }, t.Logf)
	require.NoError(t, err)
	a.orgLock = nil
	a.addPending(slot, desc)

	type result struct {
		slot Slot
		err  error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			s, _, err := a.Claim(context.Background())
			results <- result{s, err}
		}()
	}
	for range bc.want {
		select {
		case <-bc.arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("both claims should reach the ownership lookup")
		}
	}

	// Let one lookup finish: its claim adopts the lease and releases it.
	bc.release <- struct{}{}
	first := <-results
	require.NoError(t, first.err)
	require.Equal(t, slot, first.slot)
	bc.passthrough.Store(true) // Release looks the repo up through the same client.
	require.NoError(t, a.Release(context.Background(), slot))
	require.Empty(t, a.Held())

	// Only now does the second lookup finish.
	bc.release <- struct{}{}
	second := <-results
	var capErr *CapacityError
	require.ErrorAs(t, second.err, &capErr, "a delayed adoption must not claim a released slot")
	assert.Equal(t, 1, capErr.Occupied)
	assert.Empty(t, a.Held())
	assert.Zero(t, bc.createCalls())
}

func TestSlotAllocator_ThrottledCreateWithForeignRepoStillBacksOffOrg(t *testing.T) {
	layout := StagePoolLayout(2)
	slots := layout.Slots()
	p := newFakePool(layout)
	// Not visible to the listing hint (as an archived repo), so the create
	// is what runs into it.
	p.fc.CreatedRepos = append(p.fc.CreatedRepos, forge.Repository{
		ID: 7, Name: slots[0].Repo, FullName: slots[0].String(), Description: newLeaseDescription(),
	})
	p.clients["halfsend"].createErr = &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "too many"}
	a := newTestAllocator(t, layout, p)
	a.shuffle = func([]Slot) {}

	_, _, err := a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.RateLimited, "halfsend", "the create's rate limit is not hidden by the foreign repo")
	assert.Equal(t, 1, capErr.Occupied)
	assert.Equal(t, 1, p.clients["halfsend"].createCalls(), "later slots in the throttled org are skipped")
	assert.Empty(t, a.Held())
}

// TestSlotAllocator_ClearPendingKeepsNoncesRegisteredAfterSnapshot
// registers a new pending nonce while another Claim's ownership lookup
// is in flight: the lookup finds a foreign repo and clears only the
// nonces it classified, so the late one stays pending.
func TestSlotAllocator_ClearPendingKeepsNoncesRegisteredAfterSnapshot(t *testing.T) {
	layout := StagePoolLayout(1)
	slot := layout.Slots()[0]
	p := newFakePool(layout)
	p.fc.Repos = append(p.fc.Repos, forge.Repository{
		ID: 5, Name: slot.Repo, FullName: slot.String(), Description: newLeaseDescription(),
	})
	bc := &barrierClient{
		orgClient: p.clients["halfsend"],
		arrived:   make(chan struct{}, 1),
		release:   make(chan struct{}),
		want:      1,
	}
	a, err := newSlotAllocator(layout, func(context.Context, string) (forge.Client, error) { return bc, nil }, t.Logf)
	require.NoError(t, err)
	first, late := newLeaseDescription(), newLeaseDescription()
	a.addPending(slot, first)

	errs := make(chan error, 1)
	go func() {
		_, _, err := a.Claim(context.Background())
		errs <- err
	}()
	select {
	case <-bc.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the claim should reach the ownership lookup")
	}
	a.addPending(slot, late) // lands between the lookup's snapshot and clearPending
	close(bc.release)

	var capErr *CapacityError
	require.ErrorAs(t, <-errs, &capErr)
	assert.Equal(t, 1, capErr.Occupied)
	assert.Equal(t, []string{late}, a.pendingFor(slot), "only the classified nonce is cleared")
}

// heldOnCreateClient marks the slot held under another lease while its
// create is in flight, as a concurrent adoption of a stale entry would.
type heldOnCreateClient struct {
	*orgClient
	a    *slotAllocator
	slot Slot
}

func (c *heldOnCreateClient) CreateRepo(ctx context.Context, org, name, description string, private bool) (*forge.Repository, error) {
	c.a.mu.Lock()
	c.a.held[c.slot] = lease{client: c.orgClient, description: "stale-lease"}
	c.a.mu.Unlock()
	return c.orgClient.CreateRepo(ctx, org, name, description, private)
}

// TestSlotAllocator_CreateSuccessRejectedByHeldGuardIsNotAClaim covers a
// successful create whose lease the held-slot guard rejects: Claim must
// not report the slot as claimed, and must keep the existing lease.
func TestSlotAllocator_CreateSuccessRejectedByHeldGuardIsNotAClaim(t *testing.T) {
	layout := StagePoolLayout(1)
	slot := layout.Slots()[0]
	p := newFakePool(layout)
	hc := &heldOnCreateClient{orgClient: p.clients["halfsend"], slot: slot}
	a, err := newSlotAllocator(layout, func(context.Context, string) (forge.Client, error) { return hc, nil }, t.Logf)
	require.NoError(t, err)
	hc.a = a

	_, _, err = a.Claim(context.Background())
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr, "a rejected lease must not be returned as a successful claim")
	assert.Equal(t, 1, capErr.Occupied)
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, "stale-lease", a.held[slot].description, "the existing lease is left untouched")
}
