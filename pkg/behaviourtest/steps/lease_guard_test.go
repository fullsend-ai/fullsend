package steps

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// slotHolder models who holds a pool slot's name: the scenario under
// test, or a replacement holder that took the name over (a lessee that
// outlived the pool's GC deadline, for example). It backs
// World.LeaseOwned deterministically, with no clocks or goroutines.
type slotHolder struct {
	current atomic.Value // string
	checks  atomic.Int32
	err     error // when set, ownership cannot be verified
}

func newSlotHolder(owner string) *slotHolder {
	h := &slotHolder{}
	h.current.Store(owner)
	return h
}

func (h *slotHolder) replaceWith(owner string) { h.current.Store(owner) }

func (h *slotHolder) leaseOwnedBy(scenario string) func(context.Context) (bool, error) {
	return func(context.Context) (bool, error) {
		h.checks.Add(1)
		if h.err != nil {
			return false, h.err
		}
		return h.current.Load().(string) == scenario, nil
	}
}

// leaseGuardWorld returns a world with state for every kind of
// name-based cleanup mutation: issue, fork PR, namespace PR, branches,
// fork repo, harness-hosting repo, mint-probe host repo, dummy script,
// kill switch, and inference-gateway config. It also has local state
// (a Jira config dir) that cleanup removes regardless of the lease.
func leaseGuardWorld(t *testing.T, scm *fakeCleanupSCM) (*world.World, *[]string) {
	t.Helper()
	var logs []string
	scm.fileContent = []byte("version: \"1\"\nkill_switch: true\nroles:\n  - triage\n")
	return &world.World{
		Org:                 "org",
		RepoOwner:           "org",
		RepoName:            "test-repo-01",
		SCM:                 scm,
		Logf:                func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		IssueNumber:         7,
		ForkPRNumber:        8,
		CreatedPRNumbers:    []int{9},
		CreatedBranches:     []string{"agent/7-fix"},
		ForkOwner:           "org",
		ForkRepo:            "test-repo-01-fork",
		ForkPRBranch:        "agent/7-fix",
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "test-repo-01-harness",
		MintProbe:           &world.MintProbe{CreatedRepos: []string{"test-repo-01-mint-host"}},
		DummyOps:            []runtime.BehaviourOperation{{Op: "echo", Args: "hello"}},
		KillSwitchActivated: true,

		GatewayConfigOverridden: true,
		GatewayConfigOriginal:   []byte("runtime: dummy\n"),
		JiraConfigDir:           t.TempDir(),
	}, &logs
}

func assertNoMutations(t *testing.T, scm *fakeCleanupSCM) {
	t.Helper()
	assert.Empty(t, scm.closedIssues, "no issue or PR may be closed on the replacement holder's repo")
	assert.Empty(t, scm.deletedBranches, "no branch may be deleted from the replacement holder's repo")
	assert.Empty(t, scm.deletedRepos, "no fork, hosting or probe repo named after the slot may be deleted")
	assert.Empty(t, scm.commits, "nothing may be committed to the replacement holder's repo")
	assert.False(t, scm.commitFileCalled)
}

func TestCleanupScenario_ReplacementHolderBeforeAfterHook_NothingMutated(t *testing.T) {
	scm := &fakeCleanupSCM{}
	w, logs := leaseGuardWorld(t, scm)
	holder := newSlotHolder("scenario")
	w.LeaseOwned = holder.leaseOwnedBy("scenario")

	// Another holder replaces the slot before the After hook runs.
	holder.replaceWith("replacement")
	jiraDir := w.JiraConfigDir
	CleanupScenario(w)

	assertNoMutations(t, scm)
	assert.True(t, w.GatewayConfigOverridden, "the gateway restore was skipped, not recorded as done")
	assert.NoFileExists(t, jiraDir, "local cleanup still runs")
	assert.Contains(t, *logs, "behaviour cleanup: close issue #7: "+errLeaseLost.Error())
	assert.Contains(t, *logs, "behaviour cleanup: delete fork repo org/test-repo-01-fork: "+errLeaseLost.Error())
}

func TestCleanupScenario_OwnedLease_CleansEverything(t *testing.T) {
	// Control: with the lease intact the same world is fully cleaned, so
	// the no-mutation result above is the guard's doing.
	scm := &fakeCleanupSCM{}
	w, _ := leaseGuardWorld(t, scm)
	holder := newSlotHolder("scenario")
	w.LeaseOwned = holder.leaseOwnedBy("scenario")

	CleanupScenario(w)

	assert.NotEmpty(t, scm.closedIssues)
	assert.NotEmpty(t, scm.deletedBranches)
	assert.Len(t, scm.deletedRepos, 3, "fork, harness-hosting and probe host repos")
	assert.NotEmpty(t, scm.commits)
	assert.Greater(t, int(holder.checks.Load()), 1, "ownership is verified per mutation, not once")
}

func TestCleanupScenario_ReplacementDuringCleanup_StopsAtTheNextMutation(t *testing.T) {
	scm := &fakeCleanupSCM{}
	w, _ := leaseGuardWorld(t, scm)
	holder := newSlotHolder("scenario")
	w.LeaseOwned = holder.leaseOwnedBy("scenario")
	w.SCM = &replaceAfterFirstClose{fakeCleanupSCM: scm, holder: holder}
	w.GatewayConfigOverridden = false
	w.GatewayConfigOriginal = nil

	CleanupScenario(w)

	assert.Len(t, scm.closedIssues, 1, "only the close issued while the lease was still owned")
	assert.Empty(t, scm.deletedBranches)
	assert.Empty(t, scm.deletedRepos)
	assert.Empty(t, scm.commits)
}

// replaceAfterFirstClose hands the slot to a replacement holder as soon
// as the scenario's first mutation has landed.
type replaceAfterFirstClose struct {
	*fakeCleanupSCM
	holder *slotHolder
}

func (r *replaceAfterFirstClose) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	err := r.fakeCleanupSCM.CloseIssue(ctx, owner, repo, number)
	r.holder.replaceWith("replacement")
	return err
}

func TestCleanupScenario_UnverifiableLease_FailsClosed(t *testing.T) {
	scm := &fakeCleanupSCM{}
	w, logs := leaseGuardWorld(t, scm)
	holder := newSlotHolder("scenario")
	holder.err = errors.New("lookup unavailable")
	w.LeaseOwned = holder.leaseOwnedBy("scenario")

	CleanupScenario(w)

	assertNoMutations(t, scm)
	assert.Contains(t, *logs, "behaviour cleanup: close issue #7: cannot verify the leased slot is still this scenario's; refusing to mutate by name: lookup unavailable")
}

func TestCleanupScenario_TransientVerificationFailureIsRetried(t *testing.T) {
	speedUpCleanupRetries(t)
	scm := &fakeCleanupSCM{}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "test-repo-01", IssueNumber: 7, SCM: scm}
	var checks int
	w.LeaseOwned = func(context.Context) (bool, error) {
		checks++
		if checks == 1 {
			return false, &fakeTransientError{msg: "rate limited"}
		}
		return true, nil
	}

	CleanupScenario(w)

	require.Len(t, scm.closedIssues, 1, "the retry verified the lease and closed the issue")
	assert.GreaterOrEqual(t, checks, 2, "the failed verification was retried")
}

func TestCleanupScenario_RetryAfterTransientErrorReverifiesLease(t *testing.T) {
	speedUpCleanupRetries(t)
	holder := newSlotHolder("scenario")
	var closes int
	scm := &fakeRetryCleanupSCM{closeIssueFn: func(context.Context, string, string, int) error {
		closes++
		// The first close fails transiently, and the slot changes hands
		// before cleanupRetry tries again.
		holder.replaceWith("replacement")
		return &fakeTransientError{msg: "502 bad gateway"}
	}}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "test-repo-01", IssueNumber: 7, SCM: scm}
	w.LeaseOwned = holder.leaseOwnedBy("scenario")

	CleanupScenario(w)

	assert.Equal(t, 1, closes, "the retry must not be sent once the lease is lost")
}

func TestCleanupScenario_NoLeaseTracking_Unguarded(t *testing.T) {
	scm := &fakeCleanupSCM{}
	w, _ := leaseGuardWorld(t, scm)

	CleanupScenario(w)

	assert.NotEmpty(t, scm.closedIssues, "a world without a lease check cleans as before")
	assert.NoError(t, verifyLease(w))
}

// rateLimitedDeleteSCM models a forge client whose DELETE is rate-limited
// once: it sends the request, waits (during which the slot changes
// hands) and then runs the retry guard from the request context before
// it would resend, as the GitHub client does. The client's own handling
// of the guard is covered by the internal/forge/github tests.
type rateLimitedDeleteSCM struct {
	*fakeCleanupSCM
	holder *slotHolder

	deletes          atomic.Int32
	ambiguousRetryOn atomic.Bool
}

func (s *rateLimitedDeleteSCM) DeleteRepo(ctx context.Context, owner, repo string) error {
	s.deletes.Add(1)
	s.ambiguousRetryOn.Store(forge.AmbiguousRetryDisabled(ctx))
	// The slot changes hands while the client waits to resend.
	s.holder.replaceWith("replacement")
	guard := forge.RetryGuard(ctx)
	if guard == nil {
		s.deletes.Add(1) // resent with no ownership check
		return nil
	}
	if err := guard(ctx); err != nil {
		return fmt.Errorf("DELETE %s/%s: not retried: %w", owner, repo, err)
	}
	s.deletes.Add(1)
	return nil
}

func TestCleanupScenario_RetryGuardStopsRateLimitedDeleteForReplacedSlot(t *testing.T) {
	holder := newSlotHolder("scenario")
	scm := &rateLimitedDeleteSCM{fakeCleanupSCM: &fakeCleanupSCM{}, holder: holder}
	w := &world.World{
		Org: "org", RepoOwner: "org", RepoName: "test-repo-01",
		ForkOwner: "org", ForkRepo: "test-repo-01-fork",
		SCM: scm,
	}
	w.LeaseOwned = holder.leaseOwnedBy("scenario")
	var logs []string
	w.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	CleanupScenario(w)

	assert.Equal(t, int32(1), scm.deletes.Load(), "the DELETE sent while the lease was owned is not resent after the wait")
	assert.True(t, scm.ambiguousRetryOn.Load(), "the DELETE must not be replayed after an ambiguous failure")
	require.NotEmpty(t, logs)
	assert.Contains(t, logs[len(logs)-1], "not retried")
	assert.Contains(t, logs[len(logs)-1], errLeaseLost.Error())
}

func TestCleanupScenario_ReactionNotificationsNotDisabledOnReplacedSlot(t *testing.T) {
	cfg := []byte("version: \"1\"\nstatus_notifications:\n  reaction:\n    start: eyes\n")
	scm := &fakeCleanupSCM{fileContent: cfg}
	var logs []string
	w := &world.World{
		Org: "org", RepoOwner: "org", RepoName: "test-repo-01", SCM: scm,
		Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
	}
	require.True(t, reactionsEnabledInConfig(w), "precondition: the config enables reactions")
	w.LeaseOwned = func(context.Context) (bool, error) { return false, nil }

	CleanupScenario(w)

	assert.Empty(t, scm.commits)
	assert.Contains(t, logs, "behaviour cleanup: disable reaction notifications: "+errLeaseLost.Error())
}

func TestCleanupScenario_OwnersAuthCleanupGuarded(t *testing.T) {
	scm := &fakeCleanupSCM{fileContent: []byte("version: \"1\"\n")}
	w := &world.World{
		Org: "org", RepoOwner: "org", RepoName: "test-repo-01", SCM: scm,
		OwnersAuthActivated: true,
	}
	w.LeaseOwned = func(context.Context) (bool, error) { return false, nil }

	CleanupScenario(w)

	assert.Empty(t, scm.commits, "OWNERS files of a replacement holder's repo are not cleared")
}

// replaceDuringRead hands the slot to a replacement holder while the
// scenario's preparatory config read is in flight.
type replaceDuringRead struct {
	*fakeCleanupSCM
	holder *slotHolder
}

func (r *replaceDuringRead) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	data, err := r.fakeCleanupSCM.GetFileContent(ctx, owner, repo, path)
	r.holder.replaceWith("replacement")
	return data, err
}

func TestCleanupScenario_LeaseLostDuringConfigRead_NoCommit(t *testing.T) {
	scm := &fakeCleanupSCM{fileContent: []byte("version: \"1\"\nkill_switch: true\n")}
	var logs []string
	w := &world.World{
		Org: "org", RepoOwner: "org", RepoName: "test-repo-01",
		Logf:                func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		KillSwitchActivated: true,
	}
	holder := newSlotHolder("scenario")
	w.SCM = &replaceDuringRead{fakeCleanupSCM: scm, holder: holder}
	w.LeaseOwned = holder.leaseOwnedBy("scenario")

	CleanupScenario(w)

	assert.Empty(t, scm.commits, "ownership is re-verified after the read, before the first commit")
	assert.False(t, scm.commitFileCalled)
	assert.Contains(t, logs, "behaviour cleanup: deactivate kill switch: updating config: "+errLeaseLost.Error())
}
