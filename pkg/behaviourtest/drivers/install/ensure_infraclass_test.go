package install

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// This file bridges unexported ensure-path error producers to the external
// test package (ensure_infraclass_ext_test.go), which classifies them with
// pkg/behaviourtest/failclass. failclass imports this package, so an
// in-package test file cannot import it — the go tool rejects that as an
// "import cycle not allowed in test" — while the external install_test
// package can. The exported bridges below run the real production paths and
// are visible only inside the test build; they never reach a non-test
// compilation.

// speedUpSettlePoll shortens awaitWorkflowReady's poll delay for fast tests
// and restores it on cleanup, mirroring speedUpResetRetries.
func speedUpSettlePoll(t *testing.T) {
	t.Helper()
	orig := settlePoll
	settlePoll = 0
	t.Cleanup(func() { settlePoll = orig })
}

// AwaitCreationNeverVisibleForTest runs the real awaitCreation poll against
// a client whose GetRepo keeps reporting the repo missing, and returns the
// terminal read-after-create exhaustion error the ensurer produces when a
// freshly claimed slot repo never becomes visible.
func AwaitCreationNeverVisibleForTest(t *testing.T) error {
	t.Helper()
	speedUpResetRetries(t)
	client := &countingRepoClient{
		switchAfter: resetMaxAttempts + 1, // never switches
		initialErr:  forge.ErrNotFound,    // never visible
		switchedErr: forge.ErrNotFound,
	}
	e := &repoEnsurer{client: client, logf: t.Logf}
	return e.awaitCreation(context.Background(), "org", "repo", "org/repo")
}

// AwaitWorkflowNeverSettledForTest runs the real awaitWorkflowReady poll
// against a client whose GetWorkflow keeps reporting the workflow missing,
// and returns the terminal settle-exhaustion error the ensurer produces when
// Actions never recognises the dispatch workflow.
func AwaitWorkflowNeverSettledForTest(t *testing.T) error {
	t.Helper()
	speedUpSettlePoll(t)
	sc := &stubClient{installed: false, getWorkflowErr: forge.ErrNotFound}
	return awaitWorkflowReady(context.Background(), sc, "org", "repo", "fullsend.yaml", t.Logf)
}

// ProvisionWIFQuotaRejectedForTest runs the real ensure-path WIF
// resolution (resolveWIFProvider, the entry installFullsend's resolver
// invokes) against a runCLI stub whose status read reports the repo
// unenrolled and whose "inference provision" call fails with GCP's
// IAM-write exhaustion text, and returns the error the ensurer surfaces
// when the provision phase is quota-rejected under provisionGate — the
// path that calls common.RunInferenceProvision directly, bypassing
// ResolveInferenceWIFProvider.
func ProvisionWIFQuotaRejectedForTest(t *testing.T) error {
	t.Helper()
	e := &repoEnsurer{
		runCLI: func(_, _ string, args ...string) (string, error) {
			switch {
			case isInferenceCall(args, "status"):
				return notProvisionedStatus, nil
			case isInferenceCall(args, "provision"):
				return "", fmt.Errorf("[cli] fullsend inference provision org/test-repo-01 --project test-project failed: exit status 1\nError: rate limited (HTTP 429) after 7 attempts")
			}
			return "", nil
		},
		logf: t.Logf,
	}
	_, err := e.resolveWIFProvider(context.Background(), "org/test-repo-01", "test-project")
	return err
}

// LeaseLostForTest runs the provisioning fork cleanup (failClosed, as
// doEnsure invokes it) with an OwnerCheck that reports the slot replaced,
// and returns the errLeaseLost-wrapped error EnsureRepo fails closed with
// before writing to the slot.
func LeaseLostForTest(t *testing.T) error {
	t.Helper()
	e := &repoEnsurer{client: &stubClient{forkExists: true}, logf: t.Logf}
	return e.deleteLeftoverForkOwned(context.Background(), "org", "repo", lostSlot, true)
}

// ForkReplacedForTest returns the errForkReplaced sentinel the fork delete's
// retry guard produces when the fork under the slot's fork name is gone or
// replaced mid-delete.
func ForkReplacedForTest() error {
	return errForkReplaced
}

// IsLeaseLostForTest reports whether err wraps the errLeaseLost sentinel.
func IsLeaseLostForTest(err error) bool { return errors.Is(err, errLeaseLost) }

// IsForkReplacedForTest reports whether err is the errForkReplaced sentinel.
func IsForkReplacedForTest(err error) bool { return errors.Is(err, errForkReplaced) }
