package install

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// fakeMintDriver is a test double for mintDriver.
type fakeMintDriver struct {
	mintURL        string
	teardownCalled bool
	teardownErr    error
}

func (f *fakeMintDriver) Install(_ context.Context) (string, error) {
	if f.mintURL != "" {
		return f.mintURL, nil
	}
	return "https://mint.test", nil
}

func (f *fakeMintDriver) Teardown(_ context.Context) error {
	f.teardownCalled = true
	return f.teardownErr
}

// slotEnsurer is an ensurer double bound to one org's client, like the
// real per-org ensurer. EnsureRepo fails unless the slot repo already
// exists (the claim must have created it) and never creates, deletes or
// resets anything.
type slotEnsurer struct {
	org    string
	client forge.Client

	mu       sync.Mutex
	ensured  []string
	released []string
	// releasedOwns records what the stillOwns check reported during each
	// ReleaseRepo call.
	releasedOwns []bool
	failNext     error
	// ownerChecks are the OwnerChecks the driver passed to EnsureRepo.
	ownerChecks []OwnerCheck

	// gate, when set, blocks EnsureRepo until closed after signalling
	// started.
	gate    chan struct{}
	started chan struct{}

	// afterEnsure, when set, runs once EnsureRepo has succeeded, before
	// it returns (to change slot ownership while provisioning continues).
	afterEnsure func()

	// cache, when non-nil, makes EnsureRepo behave like the real ensurer's
	// install cache: a key already cached returns early without
	// installing, and ReleaseRepo clears it.
	cache map[string]bool
}

// recordingTraceSink is a traceSink that records the repos it was asked
// to configure.
type recordingTraceSink struct {
	mu         sync.Mutex
	configured []string
	// beforeWrite, when set, runs at the start of ConfigureRepo and
	// returns its error, standing in for the forge client's guarded writes.
	beforeWrite func(ctx context.Context) error
}

func (s *recordingTraceSink) ConfigureRepo(ctx context.Context, _ forge.Client, org, repo string) error {
	if s.beforeWrite != nil {
		if err := s.beforeWrite(ctx); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configured = append(s.configured, org+"/"+repo)
	return nil
}

func (s *recordingTraceSink) Stop(context.Context) error { return nil }

func (e *slotEnsurer) EnsureRepo(ctx context.Context, org, repo string, owner ...OwnerCheck) error {
	e.mu.Lock()
	e.ownerChecks = append(e.ownerChecks, owner...)
	e.mu.Unlock()
	if org != e.org {
		return fmt.Errorf("ensurer for %s asked to ensure %s/%s", e.org, org, repo)
	}
	if _, err := e.client.GetRepo(ctx, org, repo); err != nil {
		return fmt.Errorf("slot repo %s/%s not created by the claim: %w", org, repo, err)
	}
	if e.gate != nil {
		e.started <- struct{}{}
		<-e.gate
	}
	e.mu.Lock()
	if err := e.failNext; err != nil {
		e.failNext = nil
		e.mu.Unlock()
		return err
	}
	if e.cache != nil {
		if e.cache[org+"/"+repo] {
			e.mu.Unlock()
			return nil
		}
		e.cache[org+"/"+repo] = true
	}
	e.ensured = append(e.ensured, org+"/"+repo)
	after := e.afterEnsure
	e.mu.Unlock()
	if after != nil {
		after()
	}
	return nil
}

func (e *slotEnsurer) ReleaseRepo(ctx context.Context, org, repo string, stillOwns OwnerCheck) error {
	ownsSlot := true
	if stillOwns != nil {
		var err error
		if ownsSlot, err = stillOwns(ctx); err != nil {
			ownsSlot = false
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.cache, org+"/"+repo)
	e.released = append(e.released, org+"/"+repo)
	e.releasedOwns = append(e.releasedOwns, ownsSlot)
	return nil
}

// testPoolDriver is a composedDriver over a fake multi-org forge, with a
// fake mint and a slotEnsurer per org.
type testPoolDriver struct {
	*composedDriver
	pool *fakePool

	mu         sync.Mutex
	setups     map[string]int
	mints      map[string]*fakeMintDriver
	ensurers   map[string]*slotEnsurer
	setupErr   error
	tokenCalls map[string]int

	// setupGate, when set, blocks every org setup until it is closed;
	// setupStarted receives a value each time a setup begins.
	setupGate    chan struct{}
	setupStarted chan struct{}

	// ensureGate, when set before the org's setup, blocks every
	// EnsureRepo until it is closed; ensureStarted receives a value each
	// time one begins.
	ensureGate    chan struct{}
	ensureStarted chan struct{}
}

func newTestPoolDriver(t *testing.T, layout PoolLayout) *testPoolDriver {
	t.Helper()
	p := newFakePool(layout)
	td := &testPoolDriver{
		pool:       p,
		setups:     map[string]int{},
		mints:      map[string]*fakeMintDriver{},
		ensurers:   map[string]*slotEnsurer{},
		tokenCalls: map[string]int{},
	}
	cfg := PoolConfig{
		Layout: layout,
		TokenFor: func(_ context.Context, org string) (string, error) {
			td.mu.Lock()
			td.tokenCalls[org]++
			td.mu.Unlock()
			if err, ok := p.noCreds[org]; ok {
				return "", err
			}
			return "tok-" + org, nil
		},
		NewClient: func(token string) forge.Client { return p.clients[strings.TrimPrefix(token, "tok-")] },
		Logf:      t.Logf,
	}
	setup := func(_ context.Context, org string, cred orgCredential) (*orgEnv, error) {
		if td.setupGate != nil {
			td.setupStarted <- struct{}{}
			<-td.setupGate
		}
		td.mu.Lock()
		defer td.mu.Unlock()
		td.setups[org]++
		if err := td.setupErr; err != nil {
			td.setupErr = nil
			return nil, err
		}
		assert.Equal(t, "tok-"+org, cred.token, "org setup uses the org's own token")
		md := &fakeMintDriver{mintURL: "https://mint." + org}
		ens := &slotEnsurer{org: org, client: cred.client, gate: td.ensureGate, started: td.ensureStarted}
		td.mints[org] = md
		td.ensurers[org] = ens
		return &orgEnv{mint: md, mintURL: md.mintURL, ensurer: ens}, nil
	}
	d, err := newComposedDriver(cfg, setup)
	require.NoError(t, err)
	// Leasing tests see only slot repo calls; orglock_test.go and
	// slotgc_test.go cover the shared e2e-lock bridge and GC.
	d.alloc.orgLock = nil
	d.alloc.gc = nil
	td.composedDriver = d
	return td
}

func (td *testPoolDriver) ensurer(org string) *slotEnsurer {
	td.mu.Lock()
	defer td.mu.Unlock()
	return td.ensurers[org]
}

func fastClaimPoll(t *testing.T) {
	t.Helper()
	old := slotClaimPoll
	slotClaimPoll = 5 * time.Millisecond
	t.Cleanup(func() { slotClaimPoll = old })
}

func assertMatchingLease(t *testing.T, td *testPoolDriver, l RepoLease) {
	t.Helper()
	assert.Same(t, td.pool.clients[l.Org], l.Client, "lease client is the leased org's client")
	assert.Equal(t, "tok-"+l.Org, l.Token, "lease token is the leased org's token")
	assert.Equal(t, "https://mint."+l.Org, l.MintURL, "lease mint is the leased org's mint")
	assert.True(t, td.pool.exists(t, Slot{Org: l.Org, Repo: l.Repo}), "slot repo exists while leased")
}

func TestNewComposedDriver_Validation(t *testing.T) {
	setup := func(context.Context, string, orgCredential) (*orgEnv, error) { return nil, nil }
	good := PoolConfig{
		Layout:    testLayout(),
		TokenFor:  func(context.Context, string) (string, error) { return "", nil },
		NewClient: func(string) forge.Client { return nil },
	}
	_, err := newComposedDriver(good, nil)
	require.ErrorContains(t, err, "org setup must not be nil")

	bad := good
	bad.Layout = PoolLayout{}
	_, err = newComposedDriver(bad, setup)
	require.ErrorContains(t, err, "org count must be positive")

	bad = good
	bad.TokenFor = nil
	_, err = newComposedDriver(bad, setup)
	require.ErrorContains(t, err, "TokenFor")

	bad = good
	bad.NewClient = nil
	_, err = newComposedDriver(bad, setup)
	require.ErrorContains(t, err, "NewClient")

	d, err := newComposedDriver(good, setup)
	require.NoError(t, err)
	assert.Equal(t, 12, d.Capacity())
}

func TestComposedDriver_NameOnlyMethodsRequireLeases(t *testing.T) {
	td := newTestPoolDriver(t, testLayout())
	_, err := td.AllocateRepo(context.Background())
	require.ErrorIs(t, err, errLeaseRequired)
	require.ErrorIs(t, td.DeallocateRepo(context.Background(), "test-repo-01"), errLeaseRequired)
}

func TestComposedDriver_ConcurrentLeasesAcrossOrgs(t *testing.T) {
	layout := testLayout() // 3 orgs × 4 slots
	td := newTestPoolDriver(t, layout)
	ctx := context.Background()

	n := layout.OrgCount * layout.SlotsPerOrg
	leases := make([]RepoLease, n)
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leases[i], errs[i] = td.AllocateLease(ctx)
		}()
	}
	wg.Wait()

	seen := map[Slot]bool{}
	orgs := map[string]bool{}
	for i, l := range leases {
		require.NoError(t, errs[i])
		s := Slot{Org: l.Org, Repo: l.Repo}
		assert.False(t, seen[s], "slot %s leased twice", s)
		seen[s] = true
		orgs[l.Org] = true
		assertMatchingLease(t, td, l)
		ens := td.ensurer(l.Org)
		require.NotNil(t, ens)
		assert.Contains(t, ens.ensured, s.String(), "the leased org's ensurer installed the slot")
	}
	assert.Len(t, seen, n)
	assert.Len(t, orgs, layout.OrgCount, "every org was used")
	for org := range orgs {
		assert.Equal(t, 1, td.setups[org], "%s set up once despite concurrent first leases", org)
		assert.Equal(t, 1, td.tokenCalls[org], "%s token minted once", org)
	}

	for _, l := range leases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, td.DeallocateLease(ctx, l.Org, l.Repo))
		}()
	}
	wg.Wait()
	for s := range seen {
		assert.False(t, td.pool.exists(t, s), "released slot repo %s deleted", s)
		assert.Contains(t, td.ensurer(s.Org).released, s.String())
	}
	assert.Empty(t, td.alloc.Held())

	require.NoError(t, td.Finalize(ctx))
	for org, md := range td.mints {
		assert.True(t, md.teardownCalled, "%s mint torn down at Finalize", org)
	}
}

func TestComposedDriver_BehaviourAndPlaybackShareSlots(t *testing.T) {
	fastClaimPoll(t)
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 2}
	td := newTestPoolDriver(t, layout)
	playback := NewPlaybackDriver(td.composedDriver)
	ctx := context.Background()

	// More scenarios than slots: each scenario leases, "runs", and
	// releases; waiters block until a slot frees up.
	const scenarios = 10
	var executed atomic.Int32
	var mu sync.Mutex
	active := map[Slot]bool{}
	var wg sync.WaitGroup
	for i := range scenarios {
		var ld LeaseDriver = td.composedDriver
		if i%2 == 1 {
			ld = playback
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := ld.AllocateLease(ctx)
			if !assert.NoError(t, err) {
				return
			}
			s := Slot{Org: l.Org, Repo: l.Repo}
			mu.Lock()
			assert.False(t, active[s], "slot %s held by two scenarios at once", s)
			active[s] = true
			mu.Unlock()
			assertMatchingLease(t, td, l)
			executed.Add(1)
			time.Sleep(time.Millisecond)
			mu.Lock()
			delete(active, s)
			mu.Unlock()
			assert.NoError(t, ld.DeallocateLease(ctx, l.Org, l.Repo))
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(scenarios), executed.Load(), "each scenario executed once")
	assert.Empty(t, td.alloc.Held())
	require.NoError(t, playback.Finalize(ctx))
}

func TestComposedDriver_NeverResetsOrDeletesOccupiedSlot(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 1, SlotsPerOrg: 2}
	td := newTestPoolDriver(t, layout)
	ctx := context.Background()
	org := layout.Orgs()[0]

	// Another holder (another suite) already holds test-repo-01.
	foreign, err := td.pool.fc.CreateRepo(ctx, org, "test-repo-01", SlotRepoDescription+leaseDescriptionSep+"theirs", false)
	require.NoError(t, err)

	l, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	assert.Equal(t, "test-repo-02", l.Repo, "the occupied slot is skipped")
	require.NoError(t, td.DeallocateLease(ctx, l.Org, l.Repo))

	got, err := td.pool.fc.GetRepo(ctx, org, "test-repo-01")
	require.NoError(t, err, "the other holder's repo is never deleted")
	assert.Equal(t, foreign.Description, got.Description, "the other holder's repo is never reset")
	assert.NotContains(t, td.ensurer(org).ensured, org+"/test-repo-01")
	assert.NotContains(t, td.ensurer(org).released, org+"/test-repo-01")
	require.NoError(t, td.Finalize(ctx))
}

func TestComposedDriver_ReleaseUnblocksWaiter(t *testing.T) {
	fastClaimPoll(t)
	td := newTestPoolDriver(t, StagePoolLayout(1))
	ctx := context.Background()

	first, err := td.AllocateLease(ctx)
	require.NoError(t, err)

	got := make(chan RepoLease, 1)
	go func() {
		l, err := td.AllocateLease(ctx)
		assert.NoError(t, err)
		got <- l
	}()
	select {
	case <-got:
		t.Fatal("second lease succeeded while the only slot was held")
	case <-time.After(30 * time.Millisecond):
	}
	require.NoError(t, td.DeallocateLease(ctx, first.Org, first.Repo))
	select {
	case second := <-got:
		assert.Equal(t, first.Org, second.Org)
		assert.Equal(t, first.Repo, second.Repo)
		require.NoError(t, td.DeallocateLease(ctx, second.Org, second.Repo))
	case <-time.After(5 * time.Second):
		t.Fatal("waiter not unblocked by release")
	}
	require.NoError(t, td.Finalize(ctx))
}

func TestComposedDriver_CancellationWhileWaiting(t *testing.T) {
	fastClaimPoll(t)
	td := newTestPoolDriver(t, StagePoolLayout(1))
	held, err := td.AllocateLease(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = td.AllocateLease(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorIs(t, err, ErrPoolCapacityExhausted, "the last capacity error is kept")
	assert.Equal(t, []Slot{{Org: held.Org, Repo: held.Repo}}, td.alloc.Held(), "a cancelled lease holds nothing")
	assert.True(t, td.pool.exists(t, Slot{Org: held.Org, Repo: held.Repo}), "the held repo is untouched")
	require.NoError(t, td.DeallocateLease(context.Background(), held.Org, held.Repo))
	require.NoError(t, td.Finalize(context.Background()))
}

func TestComposedDriver_CancelledFollowerDoesNotWaitForSharedSetup(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(2))
	td.setupGate = make(chan struct{})
	td.setupStarted = make(chan struct{}, 2)

	type result struct {
		lease RepoLease
		err   error
	}
	leaderDone := make(chan result, 1)
	go func() {
		l, err := td.AllocateLease(context.Background())
		leaderDone <- result{l, err}
	}()
	<-td.setupStarted // the leader is inside the org's setup

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	followerDone := make(chan error, 1)
	go func() {
		_, err := td.AllocateLease(ctx)
		followerDone <- err
	}()
	select {
	case err := <-followerDone:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		close(td.setupGate)
		t.Fatal("cancelled follower is still blocked on the leader's setup")
	}
	assert.Len(t, td.alloc.Held(), 1, "the cancelled follower released its slot while setup was still running")

	close(td.setupGate)
	res := <-leaderDone
	require.NoError(t, res.err, "the leader's setup is unaffected by the follower's cancellation")
	assert.Equal(t, 1, td.setups[StageOrg], "the org's mint is deployed once")
	require.NoError(t, td.DeallocateLease(context.Background(), res.lease.Org, res.lease.Repo))
	require.NoError(t, td.Finalize(context.Background()))
}

func TestComposedDriver_FinalizeWaitsForSetupLeftByCancelledWaiter(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(2))
	td.setupGate = make(chan struct{})
	td.setupStarted = make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	leaseDone := make(chan error, 1)
	go func() {
		_, err := td.AllocateLease(ctx)
		leaseDone <- err
	}()
	<-td.setupStarted // the only waiter is inside the org's setup
	cancel()
	select {
	case err := <-leaseDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		close(td.setupGate)
		t.Fatal("cancelled lease is still blocked on the setup")
	}

	finalizeDone := make(chan error, 1)
	go func() { finalizeDone <- td.Finalize(context.Background()) }()
	select {
	case err := <-finalizeDone:
		close(td.setupGate)
		t.Fatalf("Finalize returned while an org setup was still in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(td.setupGate)
	select {
	case err := <-finalizeDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Finalize did not return after the setup finished")
	}

	td.mu.Lock()
	md := td.mints[StageOrg]
	td.mu.Unlock()
	require.NotNil(t, md, "the detached setup deployed the mint")
	assert.True(t, md.teardownCalled, "Finalize tore down the mint deployed by the detached setup")
}

func TestComposedDriver_FinalizeHonorsContextWhileSetupBlocked(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(2))
	td.setupGate = make(chan struct{})
	td.setupStarted = make(chan struct{}, 1)

	leaseCtx, cancelLease := context.WithCancel(context.Background())
	leaseDone := make(chan error, 1)
	go func() {
		_, err := td.AllocateLease(leaseCtx)
		leaseDone <- err
	}()
	<-td.setupStarted // the only waiter is inside the org's setup
	cancelLease()
	select {
	case err := <-leaseDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		close(td.setupGate)
		t.Fatal("cancelled lease is still blocked on the setup")
	}

	finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelFinalize()
	finalizeDone := make(chan error, 1)
	go func() { finalizeDone <- td.Finalize(finalizeCtx) }()
	select {
	case err := <-finalizeDone:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		close(td.setupGate)
		t.Fatal("Finalize did not return after its context expired")
	}

	// The late setup result is not cached after Finalize; its mint is
	// torn down by the setup itself.
	close(td.setupGate)
	td.composedDriver.setups.Wait()
	td.mu.Lock()
	md := td.mints[StageOrg]
	td.mu.Unlock()
	require.NotNil(t, md, "the detached setup deployed the mint")
	assert.True(t, md.teardownCalled, "the late setup tore down its own mint")
	td.composedDriver.envMu.Lock()
	assert.Empty(t, td.composedDriver.envs, "no mint cached after Finalize")
	td.composedDriver.envMu.Unlock()
}

func TestComposedDriver_NoOrgSetupStartsAfterFinalize(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(2))
	require.NoError(t, td.Finalize(context.Background()))

	_, err := td.AllocateLease(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finalized")
	assert.Empty(t, td.setups, "no mint was deployed after Finalize")
	assert.Empty(t, td.alloc.Held(), "the failed lease released its slot")
}

func TestComposedDriver_QuotaSwitchOver(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 3}
	td := newTestPoolDriver(t, layout)
	ctx := context.Background()
	exhausted, healthy := layout.Orgs()[0], layout.Orgs()[1]
	td.pool.clients[exhausted].setRateLimit(forge.RateLimit{Limit: 5000, Remaining: 0, Reset: time.Now().Add(time.Hour)})

	for range layout.SlotsPerOrg {
		l, err := td.AllocateLease(ctx)
		require.NoError(t, err)
		assert.Equal(t, healthy, l.Org, "leases move to the org with quota left")
		assertMatchingLease(t, td, l)
	}
	_, _, err := td.alloc.Claim(ctx)
	var capErr *CapacityError
	require.ErrorAs(t, err, &capErr)
	assert.Contains(t, capErr.RateLimited, exhausted, "capacity diagnostics name the rate-limited org")
	assert.Zero(t, td.setups[exhausted], "no mint deployed in the exhausted org")
	assert.Contains(t, td.alloc.QuotaDiagnostics(), exhausted+":")
}

func TestComposedDriver_StageUsesUnsuffixedOrg(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(2))
	ctx := context.Background()
	for range 2 {
		l, err := td.AllocateLease(ctx)
		require.NoError(t, err)
		assert.Equal(t, StageOrg, l.Org)
		assert.Equal(t, "https://mint."+StageOrg, l.MintURL)
	}
	assert.Equal(t, map[string]int{StageOrg: 1}, td.setups)
	err := td.Finalize(ctx)
	require.ErrorContains(t, err, "2 outstanding lease(s)")
}

func TestComposedDriver_ProvisioningFailureReleasesSlot(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	ctx := context.Background()

	// First lease sets the org up; make the next install fail.
	l, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, l.Org, l.Repo))
	td.ensurer(StageOrg).failNext = errors.New("install boom")

	_, err = td.AllocateLease(ctx)
	require.ErrorContains(t, err, "install boom")
	assert.Empty(t, td.alloc.Held(), "a failed install releases its slot")
	assert.False(t, td.pool.exists(t, Slot{Org: StageOrg, Repo: "test-repo-01"}), "the failed lease's own repo is deleted")

	l, err = td.AllocateLease(ctx)
	require.NoError(t, err, "the slot is claimable again")
	require.NoError(t, td.DeallocateLease(ctx, l.Org, l.Repo))
}

func TestComposedDriver_OrgSetupFailureIsRetried(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	ctx := context.Background()
	td.setupErr = errors.New("deploy boom")

	_, err := td.AllocateLease(ctx)
	require.ErrorContains(t, err, "deploy boom")
	assert.Empty(t, td.alloc.Held())

	l, err := td.AllocateLease(ctx)
	require.NoError(t, err, "a failed org setup is not cached")
	assert.Equal(t, 2, td.setups[StageOrg])
	require.NoError(t, td.DeallocateLease(ctx, l.Org, l.Repo))
}

func TestComposedDriver_CredentialFailureSkipsOrg(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 2, SlotsPerOrg: 2}
	td := newTestPoolDriver(t, layout)
	denied := layout.Orgs()[0]
	td.pool.noCreds[denied] = errors.New("no installation")
	td.alloc.shuffle = func([]Slot) {} // layout order: the denied org is tried first

	l, err := td.AllocateLease(context.Background())
	require.NoError(t, err)
	assert.Equal(t, layout.Orgs()[1], l.Org)
	assert.GreaterOrEqual(t, td.tokenCalls[denied], 1)
}

func TestComposedDriver_DoubleRelease(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	ctx := context.Background()
	l, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, l.Org, l.Repo))
	require.ErrorContains(t, td.DeallocateLease(ctx, l.Org, l.Repo), "double-release")
	require.ErrorContains(t, td.DeallocateLease(ctx, "other-org", l.Repo), "not an outstanding lease")
}

func TestComposedDriver_ReleaseVerifiesOwnershipBeforeForkCleanup(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(2))
	ctx := context.Background()

	own, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	replaced, err := td.AllocateLease(ctx)
	require.NoError(t, err)

	// Another holder deletes and recreates the second lease's base repo.
	require.NoError(t, td.pool.fc.DeleteRepo(ctx, replaced.Org, replaced.Repo))
	_, err = td.pool.fc.CreateRepo(ctx, replaced.Org, replaced.Repo, "another holder", false)
	require.NoError(t, err)

	require.NoError(t, td.DeallocateLease(ctx, own.Org, own.Repo))
	require.NoError(t, td.DeallocateLease(ctx, replaced.Org, replaced.Repo))

	ens := td.ensurer(own.Org)
	require.Equal(t, []string{own.Org + "/" + own.Repo, replaced.Org + "/" + replaced.Repo}, ens.released)
	assert.Equal(t, []bool{true, false}, ens.releasedOwns,
		"the fork is cleaned only for the slot repo that is still this lease's")
	assert.True(t, td.pool.exists(t, Slot{Org: replaced.Org, Repo: replaced.Repo}),
		"the replacement repo is never deleted")
}

func TestComposedDriver_TraceConfigurationRequiresOwnership(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	ctx := context.Background()

	// First lease sets the org up so its ensurer exists.
	first, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, first.Org, first.Repo))

	sink := &recordingTraceSink{}
	td.traces = sink

	t.Run("owned", func(t *testing.T) {
		l, err := td.AllocateLease(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{l.Org + "/" + l.Repo}, sink.configured)
		require.NoError(t, td.DeallocateLease(ctx, l.Org, l.Repo))
	})

	t.Run("slot replaced after install", func(t *testing.T) {
		sink.configured = nil
		td.ensurer(first.Org).afterEnsure = func() {
			// Another holder deletes and recreates the slot's repo while
			// provisioning is between install and trace configuration.
			require.NoError(t, td.pool.fc.DeleteRepo(ctx, first.Org, first.Repo))
			_, err := td.pool.fc.CreateRepo(ctx, first.Org, first.Repo, "another holder", false)
			require.NoError(t, err)
		}

		_, err := td.AllocateLease(ctx)
		require.ErrorIs(t, err, errLeaseLost)
		assert.Empty(t, sink.configured, "no trace variable or secret is written to the replacement repo")
	})
}

func TestComposedDriver_TraceConfigurationLostLeaseFailsAllocation(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	ctx := context.Background()

	// First lease sets the org up so its ensurer exists.
	first, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, first.Org, first.Repo))

	sink := &recordingTraceSink{}
	sink.beforeWrite = func(ctx context.Context) error {
		// Another holder replaces the slot's repo after the pre-write
		// check; the guard the forge client runs before its next write
		// then reports the lost lease.
		require.NoError(t, td.pool.fc.DeleteRepo(ctx, first.Org, first.Repo))
		_, err := td.pool.fc.CreateRepo(ctx, first.Org, first.Repo, "another holder", false)
		require.NoError(t, err)
		guard := forge.RetryGuard(ctx)
		require.NotNil(t, guard, "trace configuration runs under an ownership guard")
		return guard(ctx)
	}
	td.traces = sink

	_, err = td.AllocateLease(ctx)
	require.ErrorIs(t, err, errLeaseLost, "a lost lease is not a best-effort trace failure")
	assert.Empty(t, sink.configured)
	td.mu.Lock()
	defer td.mu.Unlock()
	assert.Empty(t, td.outstanding, "no lease is recorded for a slot the driver no longer owns")
}

func TestComposedDriver_FailedProvisioningInvalidatesEnsureCache(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	ctx := context.Background()

	// First lease sets the org up so its ensurer exists.
	first, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, first.Org, first.Repo))

	ens := td.ensurer(first.Org)
	ens.mu.Lock()
	ens.cache = map[string]bool{}
	installsBefore := len(ens.ensured)
	ens.mu.Unlock()

	// Trace configuration fails fatally after EnsureRepo has succeeded
	// (and cached the install).
	td.traces = &recordingTraceSink{beforeWrite: func(context.Context) error {
		return fmt.Errorf("slot replaced: %w", errLeaseLost)
	}}
	_, err = td.AllocateLease(ctx)
	require.ErrorIs(t, err, errLeaseLost)
	ens.mu.Lock()
	assert.Equal(t, []string{first.Org + "/" + first.Repo}, ens.released[len(ens.released)-1:],
		"a failed allocation forgets the install its slot was cached with")
	ens.mu.Unlock()

	// The next lease of the slot gets a fresh repo and must be installed.
	td.traces = nil
	second, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	assert.Equal(t, first.Repo, second.Repo, "single-slot pool reuses the slot")
	ens.mu.Lock()
	defer ens.mu.Unlock()
	assert.Len(t, ens.ensured, installsBefore+2,
		"installation runs again for the reallocated slot instead of hitting a stale cache")
}

func TestComposedDriver_TraceConfigurationCancellationFailsAllocation(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	bg := context.Background()

	first, err := td.AllocateLease(bg)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(bg, first.Org, first.Repo))

	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	sink := &recordingTraceSink{}
	sink.beforeWrite = func(ctx context.Context) error {
		// The caller gives up while a guarded write is in flight.
		cancel()
		return ctx.Err()
	}
	td.traces = sink

	_, err = td.AllocateLease(ctx)
	require.ErrorIs(t, err, context.Canceled, "caller cancellation is not a best-effort trace failure")
	assert.Empty(t, sink.configured)
	assert.Empty(t, td.alloc.Held(), "the cancelled allocation releases its claim")
	td.mu.Lock()
	defer td.mu.Unlock()
	assert.Empty(t, td.outstanding, "no lease is recorded for a cancelled allocation")
}

func TestComposedDriver_NoAllocationAfterFinalizeForInitializedOrg(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(2))
	ctx := context.Background()
	l, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, l.Org, l.Repo))
	require.NoError(t, td.Finalize(ctx))

	_, err = td.AllocateLease(ctx)
	require.ErrorContains(t, err, "finalized", "a cached org environment must not bypass the finalized guard")
	assert.Empty(t, td.alloc.Held())
}

func TestComposedDriver_FinalizeWaitsForAllocationInFlight(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(2))
	ctx := context.Background()
	// Initialize the org first so the blocked allocation is on the cached path.
	first, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, first.Org, first.Repo))

	td.ensurer(first.Org).gate = make(chan struct{})
	td.ensurer(first.Org).started = make(chan struct{}, 1)
	gate := td.ensurer(first.Org).gate

	allocDone := make(chan error, 1)
	go func() {
		_, allocErr := td.AllocateLease(ctx)
		allocDone <- allocErr
	}()
	<-td.ensurer(first.Org).started

	finalizeDone := make(chan error, 1)
	go func() { finalizeDone <- td.Finalize(ctx) }()
	select {
	case err := <-finalizeDone:
		close(gate)
		t.Fatalf("Finalize returned while an allocation was still provisioning: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(gate)
	require.NoError(t, <-allocDone)
	err = <-finalizeDone
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 outstanding lease(s)", "Finalize reclaimed the lease that finished provisioning")
	assert.Empty(t, td.alloc.Held())
}

func TestComposedDriver_AllocationFinishingAfterFinalizeGaveUpReleasesItself(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(2))
	ctx := context.Background()
	first, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, first.Org, first.Repo))

	td.ensurer(first.Org).gate = make(chan struct{})
	td.ensurer(first.Org).started = make(chan struct{}, 1)
	gate := td.ensurer(first.Org).gate

	allocDone := make(chan error, 1)
	go func() {
		_, allocErr := td.AllocateLease(ctx)
		allocDone <- allocErr
	}()
	<-td.ensurer(first.Org).started

	fctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err = td.Finalize(fctx)
	require.ErrorContains(t, err, "allocation still in flight")

	close(gate)
	require.ErrorContains(t, <-allocDone, "finalized")
	assert.Empty(t, td.alloc.Held(), "the late allocation released its own slot")
}

func TestComposedDriver_FinalizeReclaimsLeaksAndJoinsTeardownErrors(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 1, SlotsPerOrg: 2}
	td := newTestPoolDriver(t, layout)
	ctx := context.Background()
	l, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	td.mints[l.Org].teardownErr = errors.New("teardown boom")

	err = td.Finalize(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 outstanding lease(s)")
	assert.Contains(t, err.Error(), "teardown boom")
	assert.Empty(t, td.alloc.Held(), "leaked slot released")
	assert.False(t, td.pool.exists(t, Slot{Org: l.Org, Repo: l.Repo}), "leaked slot repo deleted")
	assert.Contains(t, td.ensurer(l.Org).released, l.Org+"/"+l.Repo)
}

func TestOrgCredentials_MintOncePerOrgAndRetryFailures(t *testing.T) {
	calls := map[string]int{}
	var mu sync.Mutex
	fail := true
	c := newOrgCredentials(func(_ context.Context, org string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls[org]++
		if org == "b" && fail {
			fail = false
			return "", errors.New("mint down")
		}
		return "tok-" + org, nil
	}, func(token string) forge.Client { return forge.NewFakeClient() })

	ctx := context.Background()
	a1, err := c.get(ctx, "a")
	require.NoError(t, err)
	a2, err := c.clientFor(ctx, "a")
	require.NoError(t, err)
	assert.Same(t, a1.client, a2, "one client per org")
	assert.Equal(t, 1, calls["a"])

	_, err = c.clientFor(ctx, "b")
	require.ErrorContains(t, err, "minting token for b")
	b, err := c.get(ctx, "b")
	require.NoError(t, err, "failures are not cached")
	assert.Equal(t, "tok-b", b.token)
	assert.Equal(t, 2, calls["b"])
}

func TestComposedDriver_StillOwnsLease(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 1, SlotsPerOrg: 1}
	td := newTestPoolDriver(t, layout)
	ctx := context.Background()

	l, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	owns, err := td.StillOwnsLease(ctx, l.Org, l.Repo)
	require.NoError(t, err)
	assert.True(t, owns, "the lease owns the repo its claim created")

	// Another holder takes over the name: the repo is deleted and
	// recreated under a different lease.
	require.NoError(t, td.pool.fc.DeleteRepo(ctx, l.Org, l.Repo))
	_, err = td.pool.fc.CreateRepo(ctx, l.Org, l.Repo, SlotRepoDescription+leaseDescriptionSep+"theirs", false)
	require.NoError(t, err)
	owns, err = td.StillOwnsLease(ctx, l.Org, l.Repo)
	require.NoError(t, err)
	assert.False(t, owns, "a replacement holder's repo is not this lease's")

	owns, err = td.StillOwnsLease(ctx, l.Org, "test-repo-99")
	require.NoError(t, err)
	assert.False(t, owns, "a slot this driver does not hold is not owned")
}

func TestComposedDriver_FinalizeCancelsAllocationWaitingForCapacity(t *testing.T) {
	fastClaimPoll(t)
	td := newTestPoolDriver(t, StagePoolLayout(1))
	held, err := td.AllocateLease(context.Background())
	require.NoError(t, err)

	// The waiter's context is never cancelled, as with a runner that
	// finalizes under a context nothing else ends.
	waiterDone := make(chan error, 1)
	go func() {
		_, allocErr := td.AllocateLease(context.Background())
		waiterDone <- allocErr
	}()
	select {
	case err := <-waiterDone:
		t.Fatalf("allocation returned while the only slot was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	finalizeDone := make(chan error, 1)
	go func() { finalizeDone <- td.Finalize(context.Background()) }()
	select {
	case err := <-finalizeDone:
		require.ErrorContains(t, err, "1 outstanding lease(s)", "Finalize reclaimed the held lease")
	case <-time.After(5 * time.Second):
		t.Fatal("Finalize blocked on an allocation waiting for pool capacity")
	}
	select {
	case err := <-waiterDone:
		require.ErrorIs(t, err, context.Canceled, "the capacity wait was cancelled by Finalize")
	case <-time.After(5 * time.Second):
		t.Fatal("capacity waiter not released by Finalize")
	}
	assert.Empty(t, td.alloc.Held())
	assert.False(t, td.pool.exists(t, Slot{Org: held.Org, Repo: held.Repo}), "held slot repo reclaimed")
}

func TestComposedDriver_PassesAllocatorOwnershipCheckToEnsurer(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	ctx := context.Background()

	l, err := td.AllocateLease(ctx)
	require.NoError(t, err)

	en := td.ensurer(StageOrg)
	en.mu.Lock()
	checks := append([]OwnerCheck(nil), en.ownerChecks...)
	en.mu.Unlock()
	require.Len(t, checks, 1, "provisioning hands the ensurer the allocator's ownership check")

	owns, err := checks[0](ctx)
	require.NoError(t, err)
	assert.True(t, owns, "the lease holds its slot while provisioned")

	require.NoError(t, td.DeallocateLease(ctx, l.Org, l.Repo))
	owns, err = checks[0](ctx)
	require.NoError(t, err)
	assert.False(t, owns, "after release the same check reports the slot as no longer held")
}
