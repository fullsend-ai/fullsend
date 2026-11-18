package install

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

// newOwnershipEnsurer returns an ensurer whose github setup is counted and
// whose hooks are observed, for tests that lose the slot mid-provisioning.
func newOwnershipEnsurer(t *testing.T, sc *stubClient, setups, beforeHooks *atomic.Int32, onSetup func(n int32) error) *repoEnsurer {
	t.Helper()
	speedUpValidateRetries(t)
	speedUpResetRetries(t)
	return &repoEnsurer{
		e2eCfg: e2etest.EnvConfig{MintURL: "https://mint.test"},
		client: sc,
		binary: "/usr/bin/fullsend",
		token:  "tok",
		runCLI: func(_, _ string, args ...string) (string, error) {
			if isGitHubSetupCall(args) {
				n := setups.Add(1)
				if onSetup != nil {
					if err := onSetup(n); err != nil {
						return "", err
					}
				}
				sc.installed = true
			}
			return "", nil
		},
		setupOpts: common.DefaultGitHubSetupOpts(),
		settle:    noopSettle,
		hooks: InstallHooks{
			BeforeInstall: func(context.Context, forge.Client, string, string) (any, error) {
				beforeHooks.Add(1)
				return nil, nil
			},
		},
		logf:    t.Logf,
		ensured: make(map[string]struct{}),
	}
}

func TestEnsureRepo_FailsClosedWhenSlotReplacedBeforeFirstWrite(t *testing.T) {
	var setups, hooks atomic.Int32
	sc := &stubClient{}
	e := newOwnershipEnsurer(t, sc, &setups, &hooks, nil)

	err := e.EnsureRepo(context.Background(), "org", "test-repo-lost", func(context.Context) (bool, error) {
		return false, nil
	})
	require.ErrorIs(t, err, errLeaseLost)
	assert.Zero(t, hooks.Load(), "the install hook must not run for a slot another holder took")
	assert.Zero(t, setups.Load(), "nothing is installed into a slot that is no longer this lease's")

	e.mu.Lock()
	_, cached := e.ensured["org/test-repo-lost"]
	e.mu.Unlock()
	assert.False(t, cached, "a failed ensure is not cached")
}

func TestEnsureRepo_FailsClosedWhenOwnershipUnverifiable(t *testing.T) {
	var setups, hooks atomic.Int32
	e := newOwnershipEnsurer(t, &stubClient{}, &setups, &hooks, nil)

	err := e.EnsureRepo(context.Background(), "org", "test-repo-unverifiable", func(context.Context) (bool, error) {
		return false, assert.AnError
	})
	require.ErrorIs(t, err, errLeaseLost)
	require.ErrorIs(t, err, assert.AnError, "the verification failure is kept in the chain")
	assert.Zero(t, setups.Load())
}

func TestEnsureRepo_DoesNotDeleteForkWhenSlotReplaced(t *testing.T) {
	var setups, hooks atomic.Int32
	sc := &stubClient{forkExists: true}
	e := newOwnershipEnsurer(t, sc, &setups, &hooks, nil)

	err := e.EnsureRepo(context.Background(), "org", "test-repo-fork-lost", func(context.Context) (bool, error) {
		return false, nil
	})
	require.ErrorIs(t, err, errLeaseLost)
	assert.Contains(t, err.Error(), "deleting fork")
	assert.Zero(t, sc.forkDeleteCalled.Load(), "the replacement holder's fork must not be deleted")
	assert.Zero(t, setups.Load())
	assert.Zero(t, hooks.Load())
}

func TestEnsureRepo_ForkOwnershipUnverifiableFailsClosed(t *testing.T) {
	var setups, hooks atomic.Int32
	sc := &stubClient{forkExists: true}
	e := newOwnershipEnsurer(t, sc, &setups, &hooks, nil)

	err := e.EnsureRepo(context.Background(), "org", "test-repo-fork-unverifiable", func(context.Context) (bool, error) {
		return false, assert.AnError
	})
	require.ErrorIs(t, err, errLeaseLost)
	require.ErrorIs(t, err, assert.AnError)
	assert.Zero(t, sc.forkDeleteCalled.Load())
	assert.Zero(t, setups.Load())
}

func TestEnsureRepo_StopsRetryWhenSlotReplacedWhileProvisioningDelayed(t *testing.T) {
	// The first github setup hits the read-after-create 404 and the retry
	// waits; the slot is replaced during that wait. The retry must not
	// install into the replacement.
	var setups, hooks atomic.Int32
	var replaced atomic.Bool
	sc := &stubClient{}
	const target = "org/test-repo-delayed"
	e := newOwnershipEnsurer(t, sc, &setups, &hooks, func(int32) error {
		replaced.Store(true) // happens while provisioning is delayed
		return githubSetupRepoInfo404Err(target)
	})
	var checks atomic.Int32

	err := e.EnsureRepo(context.Background(), "org", "test-repo-delayed", func(context.Context) (bool, error) {
		checks.Add(1)
		return !replaced.Load(), nil
	})
	require.ErrorIs(t, err, errLeaseLost)
	assert.Equal(t, int32(1), setups.Load(), "the setup attempt after the wait must not run")
	assert.GreaterOrEqual(t, checks.Load(), int32(3), "ownership is checked before the hook, before each setup attempt")
}

func TestEnsureRepo_InstallsWhileOwned(t *testing.T) {
	var setups, hooks atomic.Int32
	sc := &stubClient{forkExists: true}
	e := newOwnershipEnsurer(t, sc, &setups, &hooks, nil)
	var checks atomic.Int32

	err := e.EnsureRepo(context.Background(), "org", "test-repo-owned", func(context.Context) (bool, error) {
		checks.Add(1)
		return true, nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), setups.Load())
	assert.Equal(t, int32(1), hooks.Load())
	assert.Equal(t, int32(1), sc.forkDeleteCalled.Load(), "a leftover fork is still removed while the lease is held")
	assert.GreaterOrEqual(t, checks.Load(), int32(3))
}

func TestDeleteLeftoverForkOwned_FailClosedStopsResendWhenBaseReplacedDuringRateLimitWait(t *testing.T) {
	var replaced atomic.Bool
	client, deletes := newRateLimitedDeleteClient(t, &stubClient{forkExists: true}, func() {
		replaced.Store(true)
	})
	e := &repoEnsurer{client: client, logf: t.Logf}

	err := e.deleteLeftoverForkOwned(context.Background(), "org", "repo", func(context.Context) (bool, error) {
		return !replaced.Load(), nil
	}, true)
	require.ErrorIs(t, err, errLeaseLost, "provisioning must stop, not carry on into a replaced slot")
	assert.Equal(t, int32(1), deletes.Load(), "the DELETE is not resent after ownership is lost")
}

func TestDeleteLeftoverForkOwned_FailClosedWhenOwnershipUnverifiableDuringRetry(t *testing.T) {
	client, deletes := newRateLimitedDeleteClient(t, &stubClient{forkExists: true}, func() {})
	e := &repoEnsurer{client: client, logf: t.Logf}

	var checks int
	err := e.deleteLeftoverForkOwned(context.Background(), "org", "repo", func(context.Context) (bool, error) {
		checks++
		if checks == 1 {
			return true, nil
		}
		return false, assert.AnError
	}, true)
	require.ErrorIs(t, err, errLeaseLost)
	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, int32(1), deletes.Load())
}
