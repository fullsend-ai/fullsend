package install

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func groupOrgs(groups []mintGroup) [][]string {
	out := make([][]string, len(groups))
	for i, g := range groups {
		out[i] = g.orgs
	}
	return out
}

// assertPacked checks that groups cover layout's orgs once, in order,
// with 1-based indices and each serialized list within maxBytes.
func assertPacked(t *testing.T, layout PoolLayout, groups []mintGroup, maxBytes int) {
	t.Helper()
	var all []string
	for i, g := range groups {
		assert.Equal(t, i+1, g.index)
		assert.Equal(t, groupWorkflowHostRepos(g.orgs, layout.SlotsPerOrg), g.workflowHostRepos)
		assert.LessOrEqual(t, len(g.workflowHostRepos), maxBytes, "%s", g)
		all = append(all, g.orgs...)
	}
	assert.Equal(t, layout.Orgs(), all)
}

func TestPlanMintGroups_DevPoolTwoMints(t *testing.T) {
	layout := DevPoolLayout(DefaultPoolSize) // 12 orgs × 12 slots
	groups, err := planMintGroups(layout, maxWorkerVarBytes)
	require.NoError(t, err)
	require.Len(t, groups, 2)
	assertPacked(t, layout, groups, maxWorkerVarBytes)
	assert.Equal(t, layout.Orgs()[:6], groups[0].orgs)
	assert.Equal(t, layout.Orgs()[6:], groups[1].orgs)
	// The serialized sizes cited in #8681: six orgs fit, seven do not.
	assert.Len(t, groups[0].workflowHostRepos, 4319)
	assert.Len(t, groupWorkflowHostRepos(layout.Orgs()[:7], DefaultPoolSize), 5039)
}

func TestPlanMintGroups_NearByteLimit(t *testing.T) {
	layout := DevPoolLayout(DefaultPoolSize)
	six := len(groupWorkflowHostRepos(layout.Orgs()[:6], DefaultPoolSize))

	groups, err := planMintGroups(layout, six)
	require.NoError(t, err)
	assert.Equal(t, []int{6, 6}, groupSizes(groups), "a list exactly at the limit fits")
	assertPacked(t, layout, groups, six)

	groups, err = planMintGroups(layout, six-1)
	require.NoError(t, err)
	assert.Equal(t, []int{5, 5, 2}, groupSizes(groups), "one byte over moves an org to the next mint")
	assertPacked(t, layout, groups, six-1)
}

func groupSizes(groups []mintGroup) []int {
	sizes := make([]int, len(groups))
	for i, g := range groups {
		sizes[i] = len(g.orgs)
	}
	return sizes
}

func TestPlanMintGroups_RecomputedForPoolChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		layout PoolLayout
		sizes  []int
	}{
		{"fewer slots per org", DevPoolLayout(6), []int{12}},
		{"more slots per org", DevPoolLayout(20), []int{4, 4, 4}},
		{"fewer orgs", PoolLayout{FirstOrgIndex: 1, OrgCount: 4, SlotsPerOrg: DefaultPoolSize}, []int{4}},
		{"more orgs", PoolLayout{FirstOrgIndex: 1, OrgCount: 13, SlotsPerOrg: DefaultPoolSize}, []int{6, 6, 1}},
		{"stage", StagePoolLayout(DefaultPoolSize), []int{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups, err := planMintGroups(tc.layout, maxWorkerVarBytes)
			require.NoError(t, err)
			assert.Equal(t, tc.sizes, groupSizes(groups))
			assertPacked(t, tc.layout, groups, maxWorkerVarBytes)
		})
	}
}

func TestPlanMintGroups_OrgTooLargeForOneMint(t *testing.T) {
	_, err := planMintGroups(DevPoolLayout(100), maxWorkerVarBytes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "org halfsend-01 alone needs")
	assert.Contains(t, err.Error(), "100 slots")
	assert.Contains(t, err.Error(), "5000-byte limit")
}

func TestPlanCFMints_StatusGroupPickedOnce(t *testing.T) {
	old := pickStatusOrg
	t.Cleanup(func() { pickStatusOrg = old })
	var calls int
	var offered []string
	pickStatusOrg = func(orgs []string) string {
		calls++
		offered = orgs
		return orgs[9]
	}

	plan, err := planCFMints(DevPoolLayout(DefaultPoolSize))
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "one status team per job")
	assert.Equal(t, DevPoolLayout(DefaultPoolSize).Orgs(), offered, "eligible teams are the pool orgs'")
	assert.Equal(t, "halfsend-10/mint-status", plan.statusGroup)
	assert.Equal(t, [][]string{DevPoolLayout(DefaultPoolSize).Orgs()[:6], DevPoolLayout(DefaultPoolSize).Orgs()[6:]}, groupOrgs(plan.groups))
	assert.Equal(t, 0, plan.groupOf["halfsend-01"])
	assert.Equal(t, 1, plan.groupOf["halfsend-12"])

	_, err = planCFMints(DevPoolLayout(100))
	require.Error(t, err)
}

func TestPickStatusOrg_FromPool(t *testing.T) {
	orgs := DevPoolLayout(1).Orgs()
	for range 50 {
		assert.Contains(t, orgs, pickStatusOrg(orgs))
	}
	assert.Equal(t, StageOrg, pickStatusOrg([]string{StageOrg}))
}

func TestSingleMintPlan(t *testing.T) {
	plan := singleMintPlan(DevPoolLayout(2))
	require.Len(t, plan.groups, 1)
	assert.Equal(t, DevPoolLayout(2).Orgs(), plan.groups[0].orgs)
	assert.Empty(t, plan.groups[0].workflowHostRepos)
	assert.Empty(t, plan.statusGroup)
}

func TestMintGroup_String(t *testing.T) {
	assert.Equal(t, "mint group 2", mintGroup{index: 2}.String())
	assert.Equal(t, "mint group 1 (halfsend)", mintGroup{index: 1, orgs: []string{"halfsend"}}.String())
	assert.Equal(t, "mint group 1 (halfsend-01…halfsend-06)",
		mintGroup{index: 1, orgs: DevPoolLayout(1).Orgs()[:6]}.String())
}

// countingMint is a mintDriver that counts teardowns.
type countingMint struct {
	url       string
	teardowns atomic.Int32
}

func (m *countingMint) Install(context.Context) (string, error) { return m.url, nil }
func (m *countingMint) Teardown(context.Context) error {
	m.teardowns.Add(1)
	return nil
}

func TestSharedMint_DeployOnceTeardownByLastRef(t *testing.T) {
	md := &countingMint{url: "https://mint.g1"}
	var deploys int
	s := &sharedMint{deploy: func(context.Context, orgCredential) (mintDriver, string, error) {
		deploys++
		return md, md.url, nil
	}}
	a := &sharedMintRef{shared: s}
	b := &sharedMintRef{shared: s}
	ctx := context.Background()

	url, err := a.Install(ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://mint.g1", url)
	url, err = a.Install(ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://mint.g1", url, "a held reference returns the URL again")
	url, err = b.Install(ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://mint.g1", url)
	assert.Equal(t, 1, deploys)
	assert.Equal(t, 2, s.refs)

	require.NoError(t, a.Teardown(ctx))
	require.NoError(t, a.Teardown(ctx), "a released reference releases nothing")
	assert.Zero(t, md.teardowns.Load(), "another org still uses the mint")
	require.NoError(t, b.Teardown(ctx))
	assert.Equal(t, int32(1), md.teardowns.Load(), "the last reference tears the mint down")
	require.NoError(t, s.release(ctx), "release without references is a no-op")
	assert.Equal(t, int32(1), md.teardowns.Load())
}

func TestSharedMint_FailedDeployRetried(t *testing.T) {
	md := &countingMint{url: "https://mint.g1"}
	fail := true
	var deploys int
	s := &sharedMint{deploy: func(context.Context, orgCredential) (mintDriver, string, error) {
		deploys++
		if fail {
			return nil, "", errors.New("deploy boom")
		}
		return md, md.url, nil
	}}
	ref := &sharedMintRef{shared: s}
	_, err := ref.Install(context.Background())
	require.ErrorContains(t, err, "deploy boom")
	require.NoError(t, ref.Teardown(context.Background()), "a failed install holds nothing")
	assert.Zero(t, s.refs)

	fail = false
	url, err := (&sharedMintRef{shared: s}).Install(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://mint.g1", url)
	assert.Equal(t, 2, deploys, "a later lease retries the deploy")
}

// groupPoolDriver builds the CF-style pool driver over the DEV layout
// with a fake group mint per deploy. deployErr, when non-nil, is
// returned by the next deploy of any group.
type groupPoolDriver struct {
	*composedDriver
	plan *mintPlan

	mu        sync.Mutex
	deploys   map[int]int
	mints     map[int][]*countingMint
	deployErr error
	creds     map[int][]string
	cleanups  atomic.Int32
}

func newGroupPoolDriver(t *testing.T, layout PoolLayout) *groupPoolDriver {
	t.Helper()
	t.Setenv("TEST_ACTOR_OUTSIDER_PAT", "")
	groups, err := planMintGroups(layout, maxWorkerVarBytes)
	require.NoError(t, err)
	gp := &groupPoolDriver{
		plan:    newMintPlan(groups, "halfsend-05/mint-status"),
		deploys: map[int]int{},
		mints:   map[int][]*countingMint{},
		creds:   map[int][]string{},
	}
	newMint := func(group mintGroup, cred orgCredential) (mintDriver, func(), error) {
		gp.mu.Lock()
		defer gp.mu.Unlock()
		gp.deploys[group.index]++
		gp.creds[group.index] = append(gp.creds[group.index], cred.token)
		if err := gp.deployErr; err != nil {
			gp.deployErr = nil
			return &testCFMintMintDriver{installErr: err}, func() { gp.cleanups.Add(1) }, nil
		}
		md := &countingMint{url: fmt.Sprintf("https://mint-group-%d.test", group.index)}
		gp.mints[group.index] = append(gp.mints[group.index], md)
		return md, func() { gp.cleanups.Add(1) }, nil
	}
	d, err := buildPoolDriver(testPoolConfig(t, layout), gp.plan, newMint, playbackSetupOpts(), traceStart{})
	require.NoError(t, err)
	gp.composedDriver = d.(*composedDriver)
	return gp
}

// setupOrgs runs the driver's org setup for orgs concurrently, as their
// first leases would, and caches the results as the driver does.
func (gp *groupPoolDriver) setupOrgs(t *testing.T, orgs ...string) map[string]*orgEnv {
	t.Helper()
	envs := make([]*orgEnv, len(orgs))
	errs := make([]error, len(orgs))
	var wg sync.WaitGroup
	for i, org := range orgs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			envs[i], errs[i] = gp.setup(context.Background(), org,
				orgCredential{token: "tok-" + org, client: forge.NewFakeClient()})
		}()
	}
	wg.Wait()
	out := map[string]*orgEnv{}
	for i, org := range orgs {
		require.NoError(t, errs[i], org)
		out[org] = envs[i]
		gp.envMu.Lock()
		gp.envs[org] = envs[i]
		gp.envMu.Unlock()
	}
	return out
}

func TestGroupPoolDriver_ConcurrentFirstLeasesShareOneDeploy(t *testing.T) {
	gp := newGroupPoolDriver(t, DevPoolLayout(DefaultPoolSize))
	orgs := DevPoolLayout(DefaultPoolSize).Orgs()[:6] // all of group 1

	envs := gp.setupOrgs(t, orgs...)
	assert.Equal(t, map[int]int{1: 1}, gp.deploys, "one deployment for the group")
	assert.Equal(t, int32(1), gp.cleanups.Load(), "deploy-only material removed once")
	require.Len(t, gp.creds[1], 1)
	assert.True(t, strings.HasPrefix(gp.creds[1][0], "tok-halfsend-0"), "deployed with a group org's credential")

	wantHosts := strings.Split(gp.plan.groups[0].workflowHostRepos, ",")
	for _, org := range orgs {
		env := envs[org]
		assert.Equal(t, "https://mint-group-1.test", env.mintURL, org)
		assert.Equal(t, "halfsend-05/mint-status", env.statusGroup, org)
		assert.Equal(t, wantHosts, env.workflowHostRepos, org)
		ens, ok := env.ensurer.(*repoEnsurer)
		require.True(t, ok)
		assert.Equal(t, "tok-"+org, ens.token, "each org installs with its own credential")
		assert.Equal(t, "https://mint-group-1.test", ens.e2eCfg.MintURL)
	}
}

func TestGroupPoolDriver_FinalizeTearsDownEachGroupOnce(t *testing.T) {
	gp := newGroupPoolDriver(t, DevPoolLayout(DefaultPoolSize))
	envs := gp.setupOrgs(t, "halfsend-01", "halfsend-02", "halfsend-07", "halfsend-12")
	assert.Equal(t, map[int]int{1: 1, 2: 1}, gp.deploys)
	assert.Equal(t, "https://mint-group-1.test", envs["halfsend-02"].mintURL)
	assert.Equal(t, "https://mint-group-2.test", envs["halfsend-07"].mintURL)
	assert.Equal(t, envs["halfsend-01"].statusGroup, envs["halfsend-12"].statusGroup,
		"every mint shares the job's status group")

	require.NoError(t, gp.Finalize(context.Background()))
	assert.Equal(t, int32(1), gp.mints[1][0].teardowns.Load())
	assert.Equal(t, int32(1), gp.mints[2][0].teardowns.Load())
}

func TestGroupPoolDriver_FailedDeployTornDownAndRetried(t *testing.T) {
	gp := newGroupPoolDriver(t, DevPoolLayout(DefaultPoolSize))
	gp.deployErr = errors.New("wrangler boom")

	_, err := gp.setup(context.Background(), "halfsend-03",
		orgCredential{token: "tok-halfsend-03", client: forge.NewFakeClient()})
	require.ErrorContains(t, err, "deploying mint: wrangler boom")

	// The next lease in any org of the group deploys again.
	envs := gp.setupOrgs(t, "halfsend-04")
	assert.Equal(t, 2, gp.deploys[1])
	assert.Equal(t, "https://mint-group-1.test", envs["halfsend-04"].mintURL)

	require.NoError(t, gp.Finalize(context.Background()))
	require.Len(t, gp.mints[1], 1)
	assert.Equal(t, int32(1), gp.mints[1][0].teardowns.Load())
}

func TestGroupPoolDriver_UnknownOrg(t *testing.T) {
	gp := newGroupPoolDriver(t, DevPoolLayout(DefaultPoolSize))
	_, err := gp.setup(context.Background(), "someone-else", orgCredential{})
	require.ErrorContains(t, err, "not in any mint group")
}
