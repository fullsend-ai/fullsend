package repos

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	leaseTestForeignHolder = "installer-on-another-host"
	leaseTestKey           = webhookTestOwner + "/" + webhookTestRepo + "/" + GitLabProjectLeaseVar
)

func TestLockGitLabProject_NilErrorPointerStillReleasesLocalLock(t *testing.T) {
	c := newWebhookFake()
	c.Errors["ReleaseProjectLease"] = errors.New("release failed")
	release, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)
	require.NoError(t, err)
	assert.NotPanics(t, func() { release(nil) })
	localRelease, err := LockGitLabProject(context.Background(), newWebhookFake(), webhookTestOwner, webhookTestRepo, true)
	require.NoError(t, err)
	localRelease(nil)
}

func TestLockGitLabProject_AlreadyCanceledNeverAcquires(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, dryRun := range []bool{false, true} {
		for range 100 {
			release, err := LockGitLabProject(ctx, newWebhookFake(), webhookTestOwner, webhookTestRepo, dryRun)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, release)
		}
	}
}

// shortLease shrinks the wait budget so contention tests finish quickly.
func shortLease(t *testing.T, wait time.Duration) {
	t.Helper()
	oldWait, oldPoll := gitlabLeaseWait, gitlabLeasePoll
	gitlabLeaseWait, gitlabLeasePoll = wait, 5*time.Millisecond
	t.Cleanup(func() { gitlabLeaseWait, gitlabLeasePoll = oldWait, oldPoll })
}

// holdsLease reports whether the fake project currently carries a lease.
func holdsLease(c webhookFake) bool {
	ok, err := c.AcquireProjectLease(context.Background(), webhookTestOwner, webhookTestRepo, GitLabProjectLeaseVar, "probe")
	if err == nil && ok {
		_ = c.ReleaseProjectLease(context.Background(), webhookTestOwner, webhookTestRepo, GitLabProjectLeaseVar, "probe")
		return false
	}
	return true
}

// raisingTriggerOwner models a second installer process racing the first one
// inside its Maintainer window: when the Poller is raised it records whether
// the other installer could take the project lease.
type raisingTriggerOwner struct {
	*fakeTriggerOwner
	otherAcquired []bool
}

func (r *raisingTriggerOwner) SetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64, level int) error {
	if level == forge.GitLabAccessLevelMaintainer {
		ok, err := r.c.AcquireProjectLease(ctx, owner, repo, GitLabProjectLeaseVar, "installer-b")
		if err != nil {
			return err
		}
		r.otherAcquired = append(r.otherAcquired, ok)
	}
	return r.fakeTriggerOwner.SetProjectMemberAccessLevel(ctx, owner, repo, userID, level)
}

// The lease is held for the whole elevate/mint/restore/publish transaction, so
// an installer in another process cannot start its inventory, revocation, or
// publication inside another's Maintainer window, and the lease is freed at
// the end.
func TestEnsureGitLabWebhookFastPath_LeaseCoversElevationWindow(t *testing.T) {
	c := newWebhookFake()
	po := &raisingTriggerOwner{fakeTriggerOwner: newPollerOwner(c)}

	_, err := ensureWebhookAsPoller(c, po, false)

	require.NoError(t, err)
	assert.Equal(t, []bool{false}, po.otherAcquired, "the second installer is refused while the Poller is raised")
	assert.False(t, holdsLease(c), "the lease is released when the transaction ends")
	assert.Len(t, c.triggers(), 1)
}

// An installer in another process holding the lease stops this one before it
// inventories, revokes, elevates, or publishes anything.
func TestEnsureGitLabWebhookFastPath_HeldLeaseRefusesElevation(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	c := newWebhookFake()
	po := newPollerOwner(c)
	c.ProjectLeases = map[string]string{leaseTestKey: leaseTestForeignHolder}

	res, err := ensureWebhookAsPoller(c, po, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), GitLabProjectLeaseVar)
	assert.Equal(t, "deferred", res.Action)
	assert.Empty(t, po.sets, "the Poller is never raised without exclusivity")
	assert.Empty(t, c.CreatedTriggerTokens)
	assert.Empty(t, c.hooks())
	assert.Equal(t, leaseTestForeignHolder, c.ProjectLeases[leaseTestKey], "another installer's lease is left alone")
}

// A lease released while this installer waits lets it proceed.
func TestEnsureGitLabWebhookFastPath_WaitsForLeaseRelease(t *testing.T) {
	shortLease(t, 5*time.Second)
	c := newWebhookFake()
	po := newPollerOwner(c)
	c.ProjectLeases = map[string]string{leaseTestKey: leaseTestForeignHolder}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = c.ReleaseProjectLease(context.Background(), webhookTestOwner, webhookTestRepo, GitLabProjectLeaseVar, leaseTestForeignHolder)
	}()

	_, err := ensureWebhookAsPoller(c, po, false)

	require.NoError(t, err)
	assert.Len(t, c.triggers(), 1)
	assert.False(t, holdsLease(c))
}

// A canceled operation stops waiting without changing anything.
func TestLockGitLabProject_CanceledWhileWaiting(t *testing.T) {
	shortLease(t, 5*time.Second)
	c := newWebhookFake()
	c.ProjectLeases = map[string]string{leaseTestKey: leaseTestForeignHolder}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := LockGitLabProject(ctx, c, webhookTestOwner, webhookTestRepo, false)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	// The process-local mutex was freed too.
	unlock, err := LockGitLabProject(context.Background(), newWebhookFake(), webhookTestOwner, webhookTestRepo, true)
	require.NoError(t, err)
	var releaseErr error
	unlock(&releaseErr)
}

// A second operation in the same process, blocked on the process-local lock,
// observes cancellation instead of waiting for the first to release it.
func TestLockGitLabProject_LocalContentionHonorsCancellation(t *testing.T) {
	shortLease(t, 5*time.Second)
	c := newWebhookFake()
	release, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)
	require.NoError(t, err)
	var releaseErr error
	defer release(&releaseErr)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = LockGitLabProject(ctx, c, webhookTestOwner, webhookTestRepo, false)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second, "cancellation is observed while the local lock is held")
}

// The acquisition budget applies to the process-local lock too.
func TestLockGitLabProject_LocalContentionHonorsBudget(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	c := newWebhookFake()
	release, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)
	require.NoError(t, err)
	var releaseErr error
	defer release(&releaseErr)

	_, err = LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "holds the project lock")
}

// blockingLeaseClient models a live client waiting out a long Retry-After:
// the lease request returns only when its context ends.
type blockingLeaseClient struct {
	webhookFake
	// grantAfterBlock makes the request report a granted lease once its
	// context expired, as a response that arrives after the budget.
	grantAfterBlock bool
	// releaseErr is returned by the compensating release.
	releaseErr error
	released   []string
}

func (b *blockingLeaseClient) AcquireProjectLease(ctx context.Context, _, _, _, _ string) (bool, error) {
	<-ctx.Done()
	if b.grantAfterBlock {
		return true, nil
	}
	return false, ctx.Err()
}

func (b *blockingLeaseClient) ReleaseProjectLease(_ context.Context, _, _, _, holder string) error {
	b.released = append(b.released, holder)
	return b.releaseErr
}

// A lease request that blocks is bounded by the acquisition budget even when
// the caller's context never ends.
func TestLockGitLabProject_BlockedAcquisitionHonorsBudget(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	c := &blockingLeaseClient{webhookFake: newWebhookFake()}

	start := time.Now()
	_, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second, "the blocked request does not outlive the budget")
	assert.ErrorContains(t, err, "holds the project lease")
	// The process-local mutex was freed too.
	unlock, err := LockGitLabProject(context.Background(), newWebhookFake(), webhookTestOwner, webhookTestRepo, true)
	require.NoError(t, err)
	var releaseErr error
	unlock(&releaseErr)
}

// ambiguousAcquireClient models a lease request that GitLab committed but
// whose response was lost: the lease is recorded for the caller's holder and
// the request reports an error.
type ambiguousAcquireClient struct {
	webhookFake
	acquireErr error
	releaseErr error
}

func (a *ambiguousAcquireClient) AcquireProjectLease(ctx context.Context, owner, repo, name, holder string) (bool, error) {
	if _, err := a.webhookFake.AcquireProjectLease(ctx, owner, repo, name, holder); err != nil {
		return false, err
	}
	return false, a.acquireErr
}

func (a *ambiguousAcquireClient) ReleaseProjectLease(ctx context.Context, owner, repo, name, holder string) error {
	if a.releaseErr != nil {
		return a.releaseErr
	}
	return a.webhookFake.ReleaseProjectLease(ctx, owner, repo, name, holder)
}

// A failed acquisition request may have created the lease server-side; it is
// released by holder so it does not stay held until manual recovery.
func TestLockGitLabProject_AmbiguousAcquisitionFailureReleasesLease(t *testing.T) {
	c := &ambiguousAcquireClient{webhookFake: newWebhookFake(), acquireErr: errors.New("connection reset")}

	_, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

	require.Error(t, err)
	assert.False(t, holdsLease(c.webhookFake), "the lease the failed request created is released")
	// The process-local mutex was freed too.
	unlock, err := LockGitLabProject(context.Background(), newWebhookFake(), webhookTestOwner, webhookTestRepo, true)
	require.NoError(t, err)
	var releaseErr error
	unlock(&releaseErr)
}

// The holder-checked release never removes another installer's lease.
func TestLockGitLabProject_AmbiguousAcquisitionFailureKeepsForeignLease(t *testing.T) {
	c := &ambiguousAcquireClient{webhookFake: newWebhookFake(), acquireErr: errors.New("connection reset")}
	c.ProjectLeases = map[string]string{leaseTestKey: leaseTestForeignHolder}

	_, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

	require.Error(t, err)
	assert.Equal(t, leaseTestForeignHolder, c.ProjectLeases[leaseTestKey])
}

// A cleanup failure after an ambiguous acquisition is reported with the
// acquisition failure.
func TestLockGitLabProject_AmbiguousAcquisitionCleanupFailureIsReported(t *testing.T) {
	c := &ambiguousAcquireClient{webhookFake: newWebhookFake(), acquireErr: errors.New("connection reset"), releaseErr: errors.New("release failed")}

	_, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

	require.Error(t, err)
	assert.ErrorContains(t, err, "taking the project lease")
	assert.ErrorContains(t, err, "releasing the project lease after a failed acquisition")
}

// A lease granted after the budget expired is released, not used.
func TestLockGitLabProject_LeaseGrantedAfterBudgetIsReleased(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	c := &blockingLeaseClient{webhookFake: newWebhookFake(), grantAfterBlock: true}

	_, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

	require.Error(t, err)
	assert.Len(t, c.released, 1, "the late lease is released on a detached context")
}

// A failed release of a lease granted after the budget expired is reported with
// the budget error, not discarded.
func TestLockGitLabProject_LateLeaseReleaseFailureIsReported(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	c := &blockingLeaseClient{webhookFake: newWebhookFake(), grantAfterBlock: true, releaseErr: errors.New("release failed")}

	_, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

	require.Error(t, err)
	assert.ErrorContains(t, err, "another fullsend installer holds the project lease")
	assert.ErrorContains(t, err, "releasing the project lease granted after the wait budget expired")
	assert.Len(t, c.released, 1)
}

// The same cleanup failure is reported alongside a caller cancellation.
func TestLockGitLabProject_LateLeaseReleaseFailureReportedOnCancel(t *testing.T) {
	c := &blockingLeaseClient{webhookFake: newWebhookFake(), grantAfterBlock: true, releaseErr: errors.New("release failed")}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	_, err := LockGitLabProject(ctx, c, webhookTestOwner, webhookTestRepo, false)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "releasing the project lease granted after the wait budget expired")
}

// The lease is freed on a failing transaction and after a canceled context.
func TestEnsureGitLabWebhookFastPath_LeaseReleasedOnFailure(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	po.restoreErrs = 2

	_, err := ensureWebhookAsPoller(c, po, false)

	require.Error(t, err)
	assert.False(t, holdsLease(c), "a failed install does not strand the lease")

	// A canceled operation still releases on a detached context.
	c2 := newWebhookFake()
	ctx, cancel := context.WithCancel(context.Background())
	unlock, lockErr := LockGitLabProject(ctx, c2, webhookTestOwner, webhookTestRepo, false)
	require.NoError(t, lockErr)
	cancel()
	var releaseErr error
	unlock(&releaseErr)
	require.NoError(t, releaseErr)
	assert.False(t, holdsLease(c2))
}

// A lease that cannot be freed is reported with manual recovery steps.
func TestLockGitLabProject_ReleaseFailureReported(t *testing.T) {
	c := newWebhookFake()
	c.Errors["ReleaseProjectLease"] = errors.New("503 unavailable")
	unlock, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)
	require.NoError(t, err)

	opErr := errors.New("operation failed")
	unlock(&opErr)

	require.ErrorContains(t, opErr, "operation failed")
	assert.ErrorContains(t, opErr, "releasing the project lease")
	assert.ErrorContains(t, opErr, GitLabProjectLeaseVar)
}

// A failed lease request fails closed.
func TestLockGitLabProject_AcquireFailure(t *testing.T) {
	c := newWebhookFake()
	c.Errors["AcquireProjectLease"] = errors.New("503 unavailable")

	_, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

	require.ErrorContains(t, err, "taking the project lease")
}

// noLeaseClient hides the optional lease capability of the embedded client.
type noLeaseClient struct{ forge.Client }

// A client that cannot take the lease is refused rather than run unserialized,
// except for dry runs, which write nothing and so need no lease.
func TestLockGitLabProject_ClientWithoutLeaseRefused(t *testing.T) {
	c := noLeaseClient{newWebhookFake()}

	_, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, false)
	require.ErrorContains(t, err, "cannot take the project lease")

	unlock, err := LockGitLabProject(context.Background(), c, webhookTestOwner, webhookTestRepo, true)
	require.NoError(t, err)
	var releaseErr error
	unlock(&releaseErr)
	require.NoError(t, releaseErr)
}

// A dry run never writes the lease, so another installer's lease does not
// block it.
func TestEnsureGitLabWebhookFastPath_DryRunTakesNoLease(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	c := newWebhookFake()
	c.ProjectLeases = map[string]string{leaseTestKey: leaseTestForeignHolder}

	_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, true)

	require.NoError(t, err)
	assert.Equal(t, leaseTestForeignHolder, c.ProjectLeases[leaseTestKey])
}

// Poller reconciliation and failure-path safety reconciliation participate in
// the same lease, and are refused while another installer holds it.
func TestReconcileGitLabPollerElevation_HeldLeaseRefusesRestore(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	c := newWebhookFake()
	po := newPollerOwner(c)
	c.ProjectMemberAccess[webhookPollerUserID] = forge.GitLabAccessLevelMaintainer
	c.ProjectLeases = map[string]string{leaseTestKey: leaseTestForeignHolder}

	_, err := ReconcileGitLabPollerElevation(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)
	require.ErrorContains(t, err, GitLabProjectLeaseVar)
	_, err = ReconcileGitLabWebhookSafetyWithTriggerOwner(context.Background(), c, po, webhookTestOwner, webhookTestRepo, false)
	require.ErrorContains(t, err, GitLabProjectLeaseVar)

	assert.Empty(t, po.sets, "membership is not changed while another installer holds the lease")
}
