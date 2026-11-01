package common_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/failclass"
)

// These tests pin the failure-classification contract of the inference WIF
// provisioning path (task 2.2 / design D3 of the behaviour-infra-retry
// change): a GCP quota or rate-limit rejection surfaced by the `inference
// provision`/status CLI calls is infrastructure, while every other
// inference failure — an unrelated CLI failure or a provider that never
// reports healthy — stays scenario. They live in this external common_test
// package because failclass imports drivers/install, which imports common:
// an in-package test file cannot import failclass ("import cycle not
// allowed in test"). The signature list itself is unit-tested in-package in
// setup_test.go (TestIsGCPWIFQuotaError); everything driven here is the
// package's exported surface.

// isInference reports whether the captured CLI args invoke the given
// `fullsend inference <sub>` command. Same helper as setup_test.go, restated
// because that file's in-package symbols are not visible here.
func isInference(args []string, sub string) bool {
	return len(args) >= 2 && args[0] == "inference" && args[1] == sub
}

// skipPostProvisionBackoff replaces common.PostProvisionSleep with a no-op
// so the wait-loop tests run without the real 60s backoff, restoring it on
// cleanup. The in-package stubPostProvisionSleep is not reachable from this
// external package; tests in one package run sequentially, so mutating the
// exported var is safe.
func skipPostProvisionBackoff(t *testing.T) {
	t.Helper()
	orig := common.PostProvisionSleep
	common.PostProvisionSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { common.PostProvisionSleep = orig })
}

func TestInfraClassProvisionQuotaError(t *testing.T) {
	// The status read reports the repo unenrolled, so resolution falls
	// through to provision; the provision CLI call fails with the real
	// IAM-write exhaustion text (doWIFRequestWithRetry gives up after its
	// backoff), embedded in the runner's combined-output error like
	// e2etest.tryRunCLIFromDir produces. The mark originates in
	// RunInferenceProvision; ResolveInferenceWIFProvider's own
	// markGCPWIFQuota must pass the already-marked error through
	// unwrapped.
	runner := func(_, _ string, args ...string) (string, error) {
		switch {
		case isInference(args, "status"):
			return `{"status":"not_provisioned"}`, nil
		case isInference(args, "provision"):
			return "", fmt.Errorf("[cli] fullsend inference provision org/repo --project proj failed: exit status 1\nError: rate limited (HTTP 429) after 7 attempts")
		}
		return "", nil
	}

	_, err := common.ResolveInferenceWIFProvider("/bin/fullsend", "tok", "org/repo", "proj", runner, t.Logf)
	require.Error(t, err, "a quota-rejected provision must fail the resolution")

	assert.Equal(t, behaviourfail.Infrastructure, failclass.Classify(err),
		"a GCP IAM 429 on the WIF provisioning path is an environment condition, not a scenario assertion")

	var infra *behaviourfail.InfraError
	require.ErrorAs(t, err, &infra)
	assert.Equal(t, "setup: gcp wif quota", infra.Reason)
	assert.Contains(t, err.Error(), "rate limited (HTTP 429)", "the CLI failure text survives wrapping for diagnosis")
	assert.Equal(t, 1, strings.Count(err.Error(), "setup: gcp wif quota"),
		"the mark is applied once: RunInferenceProvision's marked error is not re-wrapped by ResolveInferenceWIFProvider")
}

func TestInfraClassStatusQuotaError(t *testing.T) {
	// Provision succeeds, but every post-provision status read is
	// quota-rejected, so WaitInferenceHealthy's terminal error carries the
	// quota signature and is marked there; ResolveInferenceWIFProvider must
	// pass that already-marked error through without wrapping it again.
	skipPostProvisionBackoff(t)
	runner := func(_, _ string, args ...string) (string, error) {
		switch {
		case isInference(args, "status"):
			return "", fmt.Errorf("[cli] fullsend inference status org/repo --project proj --format json failed: exit status 1\nError: getting WIF provider returned 429: Quota exceeded for quota project '123456789'")
		case isInference(args, "provision"):
			return "", nil
		}
		return "", nil
	}

	_, err := common.ResolveInferenceWIFProvider("/bin/fullsend", "tok", "org/repo", "proj", runner, t.Logf)
	require.Error(t, err, "status reads that never clear the quota rejection must fail the resolution")
	assert.Contains(t, err.Error(), "not healthy after provision", "the drive reaches WaitInferenceHealthy's terminal error")

	assert.Equal(t, behaviourfail.Infrastructure, failclass.Classify(err),
		"a GCP quota rejection on the status reads is an environment condition, not a scenario assertion")

	var infra *behaviourfail.InfraError
	require.ErrorAs(t, err, &infra)
	assert.Equal(t, "setup: gcp wif quota", infra.Reason)
	assert.Equal(t, 1, strings.Count(err.Error(), "setup: gcp wif quota"),
		"the mark is applied once: WaitInferenceHealthy's marked error is not re-wrapped by ResolveInferenceWIFProvider")
}

func TestInfraClassUnrelatedInferenceFailureStaysScenario(t *testing.T) {
	// The provider exists but never reports healthy: the terminal error is
	// the task's named unrelated failure ("expected healthy status"), which
	// carries no quota signature and must stay scenario — a retry cannot
	// make an unhealthy provider healthy.
	skipPostProvisionBackoff(t)
	runner := func(_, _ string, args ...string) (string, error) {
		if isInference(args, "status") {
			return `{"status":"unhealthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/providers/wif"}`, nil
		}
		return "", nil // provision succeeds
	}

	_, err := common.ResolveInferenceWIFProvider("/bin/fullsend", "tok", "org/repo", "proj", runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `expected healthy status, got "unhealthy"`,
		"the drive must reach the unrelated terminal failure")

	assert.Equal(t, behaviourfail.Scenario, failclass.Classify(err),
		"a provider that never reports healthy is not a quota rejection")
	assert.False(t, behaviourfail.Is(err), "unrelated inference failures stay unmarked")
}

func TestInfraClassUnrelatedProvisionCLIFailureStaysScenario(t *testing.T) {
	// A CLI failure without a quota signature (here an IAM permission
	// denial) must stay scenario: the allowlist is deliberately narrow, and
	// mislabeling a product or configuration defect would spend the single
	// automatic retry on it.
	runner := func(_, _ string, args ...string) (string, error) {
		switch {
		case isInference(args, "status"):
			return `{"status":"not_provisioned"}`, nil
		case isInference(args, "provision"):
			return "", fmt.Errorf("[cli] fullsend inference provision org/repo --project proj failed: exit status 1\nError: creating WIF pool returned 403: Permission 'iam.workloadIdentityPools.create' denied")
		}
		return "", nil
	}

	_, err := common.ResolveInferenceWIFProvider("/bin/fullsend", "tok", "org/repo", "proj", runner, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Permission 'iam.workloadIdentityPools.create' denied",
		"the drive must fail on the unrelated CLI failure")

	assert.Equal(t, behaviourfail.Scenario, failclass.Classify(err),
		"an unrelated CLI failure carries no quota signature and stays scenario")
	assert.False(t, behaviourfail.Is(err))
}
