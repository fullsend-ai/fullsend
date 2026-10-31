package install_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
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

// TestInfraClassWorkflowGetErrorSurfaced pins awaitWorkflowReady's
// poll-through boundary (PR #8801 review): only the expected not-found
// lag is polled through to the infra-marked exhaustion; every other
// GetWorkflow failure is returned unmarked, preserving the cause, so
// the classifier decides from the real error.
func TestInfraClassWorkflowGetErrorSurfaced(t *testing.T) {
	tests := map[string]struct {
		err        error
		wantClass  behaviourfail.Class
		wantMarked bool
		wantMsg    string
	}{
		"api 404 polls through to infra exhaustion": {
			err:        &gh.APIError{StatusCode: http.StatusNotFound, Message: "Not Found"},
			wantClass:  behaviourfail.Infrastructure,
			wantMarked: true,
			wantMsg:    "not visible on",
		},
		"rate limit 429 surfaces unmarked and classifies infra": {
			err:        &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "too many requests"},
			wantClass:  behaviourfail.Infrastructure,
			wantMarked: false,
			wantMsg:    "getting workflow fullsend.yaml on org/repo",
		},
		"permission 403 surfaces unmarked and classifies scenario": {
			err:        &gh.APIError{StatusCode: http.StatusForbidden, Message: "resource not accessible by integration"},
			wantClass:  behaviourfail.Scenario,
			wantMarked: false,
			wantMsg:    "getting workflow fullsend.yaml on org/repo",
		},
		"server 500 surfaces unmarked and classifies scenario": {
			err:        &gh.APIError{StatusCode: http.StatusInternalServerError, Message: "boom"},
			wantClass:  behaviourfail.Scenario,
			wantMarked: false,
			wantMsg:    "getting workflow fullsend.yaml on org/repo",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := install.AwaitWorkflowGetErrorForTest(t, tc.err)
			require.Error(t, err, "an unexpected GetWorkflow failure must fail the ensure")
			assert.Equal(t, tc.wantClass, failclass.Classify(err))
			assert.Equal(t, tc.wantMarked, behaviourfail.Is(err))
			assert.Contains(t, err.Error(), tc.wantMsg,
				"the surfaced error names the failing call and preserves the cause")
		})
	}
}

// TestInfraClassSetup404Exhaustion pins installFullsend's terminal
// read-after-create 404 (PR #8801 review): the narrowly recognized
// setup-404 exhaustion is marked infrastructure so the known mode gets
// the single retry, while other setup failures stay unmarked scenario.
func TestInfraClassSetup404Exhaustion(t *testing.T) {
	t.Run("404 exhaustion is marked infra", func(t *testing.T) {
		err := install.InstallFullsendSetup404ExhaustedForTest(t)
		require.Error(t, err)
		assert.Equal(t, behaviourfail.Infrastructure, failclass.Classify(err),
			"the known read-after-create setup-404 mode is an environment condition (#8036 acceptance)")
		var infra *behaviourfail.InfraError
		require.ErrorAs(t, err, &infra)
		assert.Equal(t, "setup: read-after-create 404", infra.Reason)
		assert.Contains(t, err.Error(), "still hit the read-after-create 404",
			"the terminal message survives wrapping for diagnosis")
		assert.Contains(t, err.Error(), "404 Not Found",
			"the CLI subprocess text stays reachable through the wrap chain")
	})
	t.Run("non-404 setup failure stays scenario", func(t *testing.T) {
		err := install.InstallFullsendSetupFailureForTest(t)
		require.Error(t, err)
		assert.Equal(t, behaviourfail.Scenario, failclass.Classify(err),
			"unrecognized setup failures stay scenario so a retry cannot mask them")
		assert.False(t, behaviourfail.Is(err), "non-404 setup failures are not marked")
	})
}
