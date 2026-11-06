package install

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// callerClient records the forge.APICaller label of each call, keyed by
// "method repo".
type callerClient struct {
	*orgClient

	mu      sync.Mutex
	callers map[string]map[string]bool
}

func newCallerClient(c *orgClient) *callerClient {
	return &callerClient{orgClient: c, callers: map[string]map[string]bool{}}
}

func (c *callerClient) record(ctx context.Context, call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.callers[call] == nil {
		c.callers[call] = map[string]bool{}
	}
	c.callers[call][forge.APICaller(ctx)] = true
}

func (c *callerClient) seen(call string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return sortedKeys(c.callers[call])
}

func (c *callerClient) ListOrgRepos(ctx context.Context, org string, includePrivate bool) ([]forge.Repository, error) {
	c.record(ctx, "list")
	return c.orgClient.ListOrgRepos(ctx, org, includePrivate)
}

func (c *callerClient) CreateRepo(ctx context.Context, org, name, description string, private bool) (*forge.Repository, error) {
	c.record(ctx, "create "+repoKind(name))
	return c.orgClient.CreateRepo(ctx, org, name, description, private)
}

func (c *callerClient) GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error) {
	c.record(ctx, "get "+repoKind(repo))
	return c.orgClient.GetRepo(ctx, owner, repo)
}

func (c *callerClient) DeleteRepo(ctx context.Context, owner, repo string) error {
	c.record(ctx, "delete "+repoKind(repo))
	return c.orgClient.DeleteRepo(ctx, owner, repo)
}

func (c *callerClient) UpdateFileIfMatch(ctx context.Context, owner, repo, path, message string, content []byte, expectedSHA string) error {
	c.record(ctx, "update "+repoKind(repo))
	return c.orgClient.UpdateFileIfMatch(ctx, owner, repo, path, message, content, expectedSHA)
}

// repoKind collapses slot names, so calls on any slot share a key.
func repoKind(repo string) string {
	if repo == e2etest.LockRepo {
		return "lock-repo"
	}
	if strings.HasPrefix(repo, "test-repo-") {
		return "slot"
	}
	return repo
}

func TestSlotAllocator_LabelsAPICallsByCaller(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 1, SlotsPerOrg: 2}
	p := newFakePool(layout)
	org := layout.Orgs()[0]
	client := newCallerClient(p.clients[org])
	a := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return client, nil }, nil)
	ctx := context.Background()

	first, _, err := a.Claim(ctx)
	require.NoError(t, err)
	second, _, err := a.Claim(ctx)
	require.NoError(t, err)

	assert.Equal(t, []string{apiCallerClaim}, client.seen("list"))
	assert.Equal(t, []string{apiCallerClaim}, client.seen("create slot"))
	assert.Equal(t, []string{apiCallerLock}, client.seen("create lock-repo"), "the first join creates the lock")
	assert.Equal(t, []string{apiCallerLock}, client.seen("get lock-repo"))
	assert.Equal(t, []string{apiCallerLock}, client.seen("update lock-repo"), "the second join writes holders.json")

	require.NoError(t, a.Release(ctx, first))
	require.NoError(t, a.Release(ctx, second))

	assert.Equal(t, []string{apiCallerRelease}, client.seen("delete slot"))
	assert.Equal(t, []string{apiCallerLock}, client.seen("delete lock-repo"), "the last leave deletes the lock")
	assert.Equal(t, []string{apiCallerLock}, client.seen("update lock-repo"), "leaving is lock traffic, not release traffic")
}

func TestSlotAllocator_LabelsLeaveAtPassEnd(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 1, SlotsPerOrg: 1}
	p := newFakePool(layout)
	org := layout.Orgs()[0]
	client := newCallerClient(p.clients[org])
	a := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return client, nil }, nil)

	m, reason, err := a.orgLock.join(context.Background(), client, org)
	require.NoError(t, err, reason)
	client.mu.Lock()
	client.callers = map[string]map[string]bool{}
	client.mu.Unlock()

	a.leaveOrgLock(forge.WithAPICaller(context.Background(), apiCallerClaim), client, m)
	assert.Equal(t, []string{apiCallerLock}, client.seen("delete lock-repo"))
}

func TestSlotAllocator_LabelsGCCalls(t *testing.T) {
	layout := PoolLayout{FirstOrgIndex: 1, OrgCount: 1, SlotsPerOrg: 1}
	p := newFakePool(layout)
	org := layout.Orgs()[0]
	client := newCallerClient(p.clients[org])
	a := newBridgeAllocator(t, layout, func(context.Context, string) (forge.Client, error) { return client, nil }, nil)

	a.collectDueOrg(forge.WithAPICaller(context.Background(), apiCallerClaim), client, org)

	assert.Equal(t, []string{apiCallerGC}, client.seen("get slot"))
}

// countingClient is a forge.Client that reports fixed API call counts.
type countingClient struct {
	forge.Client
	calls map[string]int64
}

func (c countingClient) APICalls() map[string]int64 { return c.calls }

func credentialsFor(clients map[string]forge.Client) *orgCredentials {
	creds := newOrgCredentials(nil, nil)
	for org, client := range clients {
		creds.byOrg[org] = orgCredential{client: client}
	}
	return creds
}

func TestOrgCredentials_APICallDiagnostics(t *testing.T) {
	creds := credentialsFor(map[string]forge.Client{
		"halfsend-02":    countingClient{calls: map[string]int64{"lock": 5, "claim": 2, "provision/graphql": 4}},
		"halfsend-01":    countingClient{calls: map[string]int64{"claim": 1, forge.APICallerOther: 10}},
		"no-calls-yet":   countingClient{},
		"does-not-count": forge.NewFakeClient(),
	})

	assert.Equal(t, strings.Join([]string{
		"halfsend-01: total=11 claim=1 other=10",
		"halfsend-02: total=7 claim=2 lock=5 provision/graphql=4",
		"all orgs: total=18 claim=3 lock=5 other=10 provision/graphql=4",
	}, "\n"), creds.apiCallDiagnostics())
}

// quotaCountingClient is a forge.Client that reports fixed quota counters.
type quotaCountingClient struct {
	forge.Client
	counters []forge.APIQuotaCounter
}

func (c quotaCountingClient) APIQuotaCounters() []forge.APIQuotaCounter { return c.counters }

func TestOrgCredentials_APIQuotaDiagnostics(t *testing.T) {
	reset := time.Date(2026, 10, 25, 18, 0, 0, 0, time.UTC)
	creds := credentialsFor(map[string]forge.Client{
		"halfsend-02": quotaCountingClient{counters: []forge.APIQuotaCounter{{
			Reset: reset, Limit: 5000, Highest: 4000, Lowest: 3000,
			Calls: map[string]int64{"other/POST": 2, "claim/GET": 5},
		}}},
		"halfsend-01": quotaCountingClient{counters: []forge.APIQuotaCounter{
			{Reset: reset, Limit: 5000, Highest: 10, Lowest: 10, Calls: map[string]int64{"lock/GET": 1}},
			{Reset: reset.Add(9 * time.Second), Limit: 5000, Highest: 20, Lowest: 15, Calls: map[string]int64{"lock/POST": 1}},
		}},
		"not-a-reporter": countingClient{},
	})
	assert.Equal(t, strings.Join([]string{
		"halfsend-01: reset=2026-10-25T18:00:00Z drop=0 remaining=10/5000 calls=1 lock/GET=1",
		"halfsend-01: reset=2026-10-25T18:00:09Z drop=5 remaining=15/5000 calls=1 lock/POST=1",
		"halfsend-02: reset=2026-10-25T18:00:00Z drop=1000 remaining=3000/5000 calls=7 claim/GET=5 other/POST=2",
	}, "\n"), creds.apiQuotaDiagnostics())
}

func TestComposedDriver_LogsQuotaCounters(t *testing.T) {
	var lines []string
	d := &composedDriver{
		creds: credentialsFor(map[string]forge.Client{"org": quotaCountingClient{counters: []forge.APIQuotaCounter{{
			Reset: time.Date(2026, 10, 25, 18, 0, 0, 0, time.UTC), Limit: 5000, Calls: map[string]int64{"claim/GET": 1},
		}}}}),
		logf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}
	d.logAPICalls("so far")
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "[driver] per-org quota counters so far:\norg: reset=")
}

func TestOrgCredentials_APICallDiagnosticsEmpty(t *testing.T) {
	assert.Empty(t, credentialsFor(nil).apiCallDiagnostics())
	assert.Empty(t, credentialsFor(map[string]forge.Client{"org": forge.NewFakeClient()}).apiCallDiagnostics())
}

func TestComposedDriver_LogsAPICallsWhileRunning(t *testing.T) {
	logged := make(chan string, 16)
	d := &composedDriver{
		creds: credentialsFor(map[string]forge.Client{"org": countingClient{calls: map[string]int64{"claim": 3}}}),
		logf: func(format string, args ...any) {
			select {
			case logged <- fmt.Sprintf(format, args...):
			default:
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.logAPICallsEvery(ctx, time.Millisecond)
		close(done)
	}()

	select {
	case line := <-logged:
		assert.Equal(t, "[driver] per-org API calls so far:\norg: total=3 claim=3\nall orgs: total=3 claim=3", line)
	case <-time.After(5 * time.Second):
		t.Fatal("no API call log before the deadline")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the logger did not stop when its context ended")
	}
}

func TestComposedDriver_FinalizeLogsAPICalls(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	td.creds = credentialsFor(map[string]forge.Client{StageOrg: countingClient{calls: map[string]int64{"claim": 3}}})
	var logs []string
	td.logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	require.NoError(t, td.Finalize(context.Background()))

	assert.Contains(t, strings.Join(logs, "\n"), "per-org API calls at finalize")
}

// callerEnsurer records the labels its driver calls it with.
type callerEnsurer struct {
	*slotEnsurer

	mu      sync.Mutex
	ensure  string
	release string
}

func (e *callerEnsurer) EnsureRepo(ctx context.Context, org, repo string, owner ...OwnerCheck) error {
	e.mu.Lock()
	e.ensure = forge.APICaller(ctx)
	e.mu.Unlock()
	return e.slotEnsurer.EnsureRepo(ctx, org, repo, owner...)
}

func (e *callerEnsurer) ReleaseRepo(ctx context.Context, org, repo string, stillOwns OwnerCheck) error {
	e.mu.Lock()
	e.release = forge.APICaller(ctx)
	e.mu.Unlock()
	return e.slotEnsurer.ReleaseRepo(ctx, org, repo, stillOwns)
}

// withCallerEnsurer makes td's org setup wrap each ensurer in a
// callerEnsurer, returned through the pointer it yields.
func withCallerEnsurer(td *testPoolDriver) **callerEnsurer {
	inner := td.setup
	ens := new(*callerEnsurer)
	td.setup = func(ctx context.Context, org string, cred orgCredential) (*orgEnv, error) {
		env, err := inner(ctx, org, cred)
		if err != nil {
			return nil, err
		}
		*ens = &callerEnsurer{slotEnsurer: env.ensurer.(*slotEnsurer)}
		env.ensurer = *ens
		return env, nil
	}
	return ens
}

func TestComposedDriver_LabelsProvisionAndReleaseCalls(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	ens := withCallerEnsurer(td)
	ctx := context.Background()

	lease, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, lease.Org, lease.Repo))
	require.NoError(t, td.Finalize(ctx))

	require.NotNil(t, *ens)
	assert.Equal(t, apiCallerProvision, (*ens).ensure)
	assert.Equal(t, apiCallerRelease, (*ens).release)
}

func TestComposedDriver_LabelsProvisioningRollbackAsRelease(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	ens := withCallerEnsurer(td)
	ctx := context.Background()

	first, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, first.Org, first.Repo))
	(*ens).release = ""

	sink := &recordingTraceSink{}
	sink.beforeWrite = func(ctx context.Context) error {
		require.NoError(t, td.pool.fc.DeleteRepo(ctx, first.Org, first.Repo))
		_, err := td.pool.fc.CreateRepo(ctx, first.Org, first.Repo, "another holder", false)
		require.NoError(t, err)
		return forge.RetryGuard(ctx)(ctx)
	}
	td.traces = sink

	_, err = td.AllocateLease(ctx)
	require.ErrorIs(t, err, errLeaseLost)
	assert.Equal(t, apiCallerProvision, (*ens).ensure)
	assert.Equal(t, apiCallerRelease, (*ens).release, "the rollback after EnsureRepo is release traffic")
}

// apiCallLines collects the driver's API call log lines.
type apiCallLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *apiCallLines) logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if !strings.Contains(line, "per-org API calls") {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *apiCallLines) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func (l *apiCallLines) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return ""
	}
	return l.lines[len(l.lines)-1]
}

// countCalls makes td report API calls for org, and sends its API call log
// lines to the returned collector.
func countCalls(td *testPoolDriver, org string) *apiCallLines {
	lines := &apiCallLines{}
	td.logf = lines.logf
	td.creds.byOrg[org] = orgCredential{
		token:  "tok-" + org,
		client: countingClient{Client: td.pool.clients[org], calls: map[string]int64{"claim": 3}},
	}
	return lines
}

func TestComposedDriver_LogsAPICallsUntilFinalizeEnds(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(2))
	lines := countCalls(td, StageOrg)
	ctx := context.Background()
	first, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, first.Org, first.Repo))

	// Swap the 5-minute log for a fast one.
	td.endAPICallLog()
	td.envMu.Lock()
	td.startAPICallLog(time.Millisecond)
	td.envMu.Unlock()

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
	require.Eventually(t, func() bool {
		td.envMu.Lock()
		defer td.envMu.Unlock()
		return td.closed
	}, 5*time.Second, time.Millisecond, "Finalize began")

	before := lines.count()
	logged := assert.Eventually(t, func() bool { return lines.count() > before },
		5*time.Second, time.Millisecond, "the log keeps running while Finalize is blocked")
	close(gate)
	require.True(t, logged)
	require.NoError(t, <-allocDone)
	require.ErrorContains(t, <-finalizeDone, "1 outstanding lease(s)")

	final := lines.count()
	assert.Contains(t, lines.last(), "per-org API calls at finalize:")
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, final, lines.count(), "nothing logs after the final line")
}

func TestComposedDriver_FinalizeMarksIncompleteAPICalls(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(2))
	lines := countCalls(td, StageOrg)
	td.setupGate = make(chan struct{})
	td.setupStarted = make(chan struct{}, 1)

	leaseCtx, cancelLease := context.WithCancel(context.Background())
	leaseDone := make(chan error, 1)
	go func() {
		_, err := td.AllocateLease(leaseCtx)
		leaseDone <- err
	}()
	<-td.setupStarted
	cancelLease()
	require.ErrorIs(t, <-leaseDone, context.Canceled)

	finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelFinalize()
	err := td.Finalize(finalizeCtx)
	close(td.setupGate)
	td.composedDriver.setups.Wait()

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, lines.last(), "at finalize (incomplete: work still in flight):")
}
