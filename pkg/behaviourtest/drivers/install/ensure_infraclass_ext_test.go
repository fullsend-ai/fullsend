package install_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/failclass"
)

// These tests pin the failure-classification contract of the ensure path
// (tasks 2.1 and 2.2 / design D3 of the behaviour-infra-retry change): the
// two terminal setup-poll exhaustions and a provision-phase GCP WIF quota
// rejection are infrastructure, while the lease races stay scenario. They
// live in the external install_test package because failclass imports
// install — an in-package test file cannot import it (import cycle in
// test) — so they drive the real production paths through the bridges
// exported from ensure_infraclass_test.go.

func TestInfraClassRepoNeverVisible(t *testing.T) {
	err := install.AwaitCreationNeverVisibleForTest(t)
	require.Error(t, err, "a repo that never becomes visible must fail the ensure")

	assert.Equal(t, behaviourfail.Infrastructure, failclass.Classify(err),
		"read-after-create exhaustion is a setup condition, not a scenario assertion")

	var infra *behaviourfail.InfraError
	require.ErrorAs(t, err, &infra)
	assert.Equal(t, "setup: repo never visible", infra.Reason)
	assert.Contains(t, err.Error(), "not visible after", "the terminal message survives wrapping for diagnosis")
}

func TestInfraClassWorkflowNeverSettled(t *testing.T) {
	err := install.AwaitWorkflowNeverSettledForTest(t)
	require.Error(t, err, "a workflow that never settles must fail the ensure")

	assert.Equal(t, behaviourfail.Infrastructure, failclass.Classify(err),
		"workflow-settle exhaustion is a setup condition, not a scenario assertion")

	var infra *behaviourfail.InfraError
	require.ErrorAs(t, err, &infra)
	assert.Equal(t, "setup: workflow never settled", infra.Reason)
	assert.Contains(t, err.Error(), "not visible on", "the terminal message survives wrapping for diagnosis")
}

// TestInfraClassProvisionQuotaErrorThroughEnsurePath covers task 2.2's
// ensure-path gap: provisionUnderGate calls common.RunInferenceProvision
// directly, bypassing ResolveInferenceWIFProvider, so the mark must
// originate in the provision call itself for the spec scenario "GCP
// workload identity federation quota exceeded" to hold on this path.
func TestInfraClassProvisionQuotaErrorThroughEnsurePath(t *testing.T) {
	err := install.ProvisionWIFQuotaRejectedForTest(t)
	require.Error(t, err, "a quota-rejected provision must fail the ensure path's WIF resolution")

	assert.Equal(t, behaviourfail.Infrastructure, failclass.Classify(err),
		"a GCP IAM 429 on the ensure path's provision phase is an environment condition, not a scenario assertion")

	var infra *behaviourfail.InfraError
	require.ErrorAs(t, err, &infra)
	assert.Equal(t, "setup: gcp wif quota", infra.Reason)
	assert.Contains(t, err.Error(), "rate limited (HTTP 429)", "the CLI failure text survives wrapping for diagnosis")
}

func TestInfraClassLeaseLostStaysScenario(t *testing.T) {
	err := install.LeaseLostForTest(t)
	require.Error(t, err)
	require.True(t, install.IsLeaseLostForTest(err), "the bridge must produce the lease-lost race")

	assert.Equal(t, behaviourfail.Scenario, failclass.Classify(err),
		"lease races stay scenario-classified by design (D3): a retry cannot clear a replaced slot")
	assert.False(t, behaviourfail.Is(err), "errLeaseLost is deliberately unmarked")
}

func TestInfraClassForkReplacedStaysScenario(t *testing.T) {
	err := install.ForkReplacedForTest()
	require.Error(t, err)
	require.True(t, install.IsForkReplacedForTest(err), "the bridge must produce the fork-replaced race")

	assert.Equal(t, behaviourfail.Scenario, failclass.Classify(err),
		"lease races stay scenario-classified by design (D3): a retry cannot clear a replaced fork")
	assert.False(t, behaviourfail.Is(err), "errForkReplaced is deliberately unmarked")
}
