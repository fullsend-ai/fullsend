package install

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// idleTraceCollector returns a started-looking collector with no
// containers, so Stop runs without podman.
func idleTraceCollector(t *testing.T) *traceCollector {
	t.Helper()
	return &traceCollector{
		logf:     t.Logf,
		token:    strings.Repeat("t", 64),
		endpoint: "https://example-tunnel.trycloudflare.com/v1/traces",
	}
}

func TestTraceStart_MintTracing(t *testing.T) {
	t.Run("no collector", func(t *testing.T) {
		endpoint, file, cleanup, err := traceStart{}.mintTracing()
		require.NoError(t, err)
		cleanup()
		assert.Empty(t, endpoint)
		assert.Empty(t, file)
	})
	t.Run("running collector", func(t *testing.T) {
		tc := idleTraceCollector(t)
		endpoint, file, cleanup, err := traceStart{started: tc}.mintTracing()
		require.NoError(t, err)
		assert.Equal(t, tc.endpoint, endpoint)

		data, err := os.ReadFile(file)
		require.NoError(t, err)
		assert.Equal(t, "Authorization=Bearer%20"+tc.token+"\n", string(data),
			"the mint gets the same bearer token the workflows use")
		if runtime.GOOS != "windows" {
			info, err := os.Stat(file)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the CLI requires a private headers file")
		}

		cleanup()
		_, err = os.Stat(file)
		assert.True(t, os.IsNotExist(err), "cleanup removes the token file")
	})
}

func TestTraceStart_Attach(t *testing.T) {
	t.Run("composed driver takes ownership", func(t *testing.T) {
		tc := idleTraceCollector(t)
		pending := &fakeTraceSink{}
		d := newTestPoolDriver(t, StagePoolLayout(1)).composedDriver

		got := traceStart{started: tc, pending: pending}.attach(d)
		cd := got.(*composedDriver)
		assert.Same(t, tc, cd.traces)
		assert.Same(t, pending, cd.pendingTraces)
		assert.Equal(t, 0, pending.stopCalls)
	})
	t.Run("non-composed driver stops the collector", func(t *testing.T) {
		pending := &fakeTraceSink{}
		pd := NewPlaybackDriver(newTestPoolDriver(t, StagePoolLayout(1)).composedDriver)

		assert.Same(t, pd, traceStart{pending: pending}.attach(pd))
		assert.Equal(t, 1, pending.stopCalls)
	})
	t.Run("nothing started", func(t *testing.T) {
		d := newTestPoolDriver(t, StagePoolLayout(1)).composedDriver
		cd := traceStart{}.attach(d).(*composedDriver)
		assert.Nil(t, cd.traces)
		assert.Nil(t, cd.pendingTraces)
	})
}

func TestTraceStart_Abort(t *testing.T) {
	tc := idleTraceCollector(t)
	pending := &fakeTraceSink{}
	traceStart{started: tc, pending: pending}.abort()
	assert.False(t, tc.cleanupPending(), "started collector is stopped")
	assert.Equal(t, 1, pending.stopCalls)
}

func TestBuildPoolDriver_TraceCollector(t *testing.T) {
	newMint := func(mintGroup, orgCredential) (mintDriver, func(), error) {
		return &testCFMintMintDriver{installMintURL: "https://mint.test"}, func() {}, nil
	}
	t.Run("attached on success", func(t *testing.T) {
		tc := idleTraceCollector(t)
		d, err := buildPoolDriver(testPoolConfig(t, StagePoolLayout(1)), nil, newMint, playbackSetupOpts(), traceStart{started: tc})
		require.NoError(t, err)
		assert.Same(t, tc, d.(*composedDriver).traces)
		assert.True(t, tc.cleanupPending(), "an attached collector runs until Finalize")
	})
	t.Run("stopped when construction fails", func(t *testing.T) {
		tc := idleTraceCollector(t)
		pending := &fakeTraceSink{}
		bad := testPoolConfig(t, PoolLayout{})
		_, err := buildPoolDriver(bad, nil, newMint, playbackSetupOpts(), traceStart{started: tc, pending: pending})
		require.Error(t, err)
		assert.False(t, tc.cleanupPending(), "collector must not outlive a failed factory")
		assert.Equal(t, 1, pending.stopCalls)
	})
}

func TestBuildPoolDriver_OrgSetupUsesOrgCredentialAndMint(t *testing.T) {
	t.Setenv("TEST_ACTOR_OUTSIDER_PAT", "")
	var gotGroup mintGroup
	cleaned := false
	mint := &testCFMintMintDriver{installMintURL: "https://mint.halfsend-03"}
	newMint := func(group mintGroup, cred orgCredential) (mintDriver, func(), error) {
		gotGroup = group
		assert.Equal(t, "tok-halfsend-03", cred.token, "the deploy uses the triggering org's credential")
		return mint, func() { cleaned = true }, nil
	}
	pool := testPoolConfig(t, DevPoolLayout(2))
	pool.Binary = "/bin/fullsend"
	pool.GCPProjectID = "proj"
	groups, err := planMintGroups(pool.Layout, maxWorkerVarBytes)
	require.NoError(t, err)
	plan := newMintPlan(groups, "halfsend-07/mint-status")
	d, err := buildPoolDriver(pool, plan, newMint, playbackSetupOpts(), traceStart{})
	require.NoError(t, err)
	cd := d.(*composedDriver)

	client := forge.NewFakeClient()
	env, err := cd.setup(context.Background(), "halfsend-03", orgCredential{token: "tok-halfsend-03", client: client})
	require.NoError(t, err)
	assert.Contains(t, gotGroup.orgs, "halfsend-03", "the org's group mint is deployed")
	assert.True(t, cleaned, "deploy-only material removed after Install")
	assert.Equal(t, "https://mint.halfsend-03", env.mintURL)
	ref, ok := env.mint.(*sharedMintRef)
	require.True(t, ok, "orgs hold a reference to their group's shared mint")
	assert.Same(t, mint, ref.shared.md)
	assert.Equal(t, "halfsend-07/mint-status", env.statusGroup, "the job's status group, not the org's")
	assert.Contains(t, env.workflowHostRepos, "halfsend-03/test-repo-01")
	assert.Contains(t, env.workflowHostRepos, "halfsend-01/test-repo-02-mint-host", "the whole group's hosts")
	ens, ok := env.ensurer.(*repoEnsurer)
	require.True(t, ok)
	assert.Same(t, client, ens.client, "the ensurer uses the org's client")
	assert.Equal(t, "tok-halfsend-03", ens.token, "the ensurer uses the org's token")
	assert.Equal(t, "https://mint.halfsend-03", ens.e2eCfg.MintURL, "the ensurer installs against the org's mint")
	assert.Equal(t, "proj", ens.e2eCfg.GCPProjectID)
	assert.Equal(t, "/bin/fullsend", ens.binary)
}

func TestBuildPoolDriver_FailedDeployTearsDown(t *testing.T) {
	mint := &testCFMintMintDriver{installErr: fmt.Errorf("deploy boom")}
	torn := &tearDownRecorder{testCFMintMintDriver: mint}
	newMint := func(mintGroup, orgCredential) (mintDriver, func(), error) { return torn, func() {}, nil }
	d, err := buildPoolDriver(testPoolConfig(t, StagePoolLayout(1)), nil, newMint, playbackSetupOpts(), traceStart{})
	require.NoError(t, err)
	_, err = d.(*composedDriver).setup(context.Background(), StageOrg, orgCredential{token: "t", client: forge.NewFakeClient()})
	require.ErrorContains(t, err, "deploying mint: deploy boom")
	assert.True(t, torn.tornDown, "a failed deploy leaves nothing behind")

	newMintErr := func(mintGroup, orgCredential) (mintDriver, func(), error) { return nil, nil, fmt.Errorf("no pems") }
	d, err = buildPoolDriver(testPoolConfig(t, StagePoolLayout(1)), nil, newMintErr, playbackSetupOpts(), traceStart{})
	require.NoError(t, err)
	_, err = d.(*composedDriver).setup(context.Background(), StageOrg, orgCredential{})
	require.ErrorContains(t, err, "no pems")
}

// tearDownRecorder records Teardown on a test mint.
type tearDownRecorder struct {
	*testCFMintMintDriver
	tornDown bool
}

func (r *tearDownRecorder) Teardown(ctx context.Context) error {
	r.tornDown = true
	return r.testCFMintMintDriver.Teardown(ctx)
}

// testPoolConfig is a PoolConfig over layout with "tok-<org>" tokens
// and fake clients.
func testPoolConfig(t *testing.T, layout PoolLayout) PoolConfig {
	t.Helper()
	return PoolConfig{
		Layout:    layout,
		TokenFor:  func(_ context.Context, org string) (string, error) { return "tok-" + org, nil },
		NewClient: func(string) forge.Client { return forge.NewFakeClient() },
		Logf:      t.Logf,
	}
}

func TestCFMintDeployArgs_TraceCollector(t *testing.T) {
	base := cfmintConfig{pemDir: "/tmp/pems", suiteName: "bt"}

	args := CFMintDeployArgs("bt-abc12345", "bt-mint", base)
	assert.NotContains(t, args, "--otlp-traces-endpoint", "no collector, no tracing flags")
	assert.NotContains(t, args, "--otlp-headers-file")

	traced := base
	traced.traceEndpoint = "https://example-tunnel.trycloudflare.com/v1/traces"
	traced.traceHeadersFile = "/tmp/bt-otlp-headers-1"
	args = CFMintDeployArgs("bt-abc12345", "bt-mint", traced)
	assert.Equal(t, []string{
		"--otlp-traces-endpoint", "https://example-tunnel.trycloudflare.com/v1/traces",
		"--otlp-headers-file", "/tmp/bt-otlp-headers-1",
	}, args[len(args)-4:], "the token travels in a file, never on the command line")
}
