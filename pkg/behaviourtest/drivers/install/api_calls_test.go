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
		"halfsend-02":    countingClient{calls: map[string]int64{"lock": 5, "claim": 2}},
		"halfsend-01":    countingClient{calls: map[string]int64{"claim": 1, forge.APICallerOther: 10}},
		"no-calls-yet":   countingClient{},
		"does-not-count": forge.NewFakeClient(),
	})

	assert.Equal(t, strings.Join([]string{
		"halfsend-01: total=11 claim=1 other=10",
		"halfsend-02: total=7 claim=2 lock=5",
		"all orgs: total=18 claim=3 lock=5 other=10",
	}, "\n"), creds.apiCallDiagnostics())
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

func TestComposedDriver_LabelsProvisionAndReleaseCalls(t *testing.T) {
	td := newTestPoolDriver(t, StagePoolLayout(1))
	inner := td.setup
	var ens *callerEnsurer
	td.setup = func(ctx context.Context, org string, cred orgCredential) (*orgEnv, error) {
		env, err := inner(ctx, org, cred)
		if err != nil {
			return nil, err
		}
		ens = &callerEnsurer{slotEnsurer: env.ensurer.(*slotEnsurer)}
		env.ensurer = ens
		return env, nil
	}
	ctx := context.Background()

	lease, err := td.AllocateLease(ctx)
	require.NoError(t, err)
	require.NoError(t, td.DeallocateLease(ctx, lease.Org, lease.Repo))
	require.NoError(t, td.Finalize(ctx))

	require.NotNil(t, ens)
	assert.Equal(t, apiCallerProvision, ens.ensure)
	assert.Equal(t, apiCallerRelease, ens.release)
}
