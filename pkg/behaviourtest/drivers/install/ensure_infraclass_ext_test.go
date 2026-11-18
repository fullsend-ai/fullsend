package install_test

import (
	"errors"
	"fmt"
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
	assert.Contains(t, err.Error(), "workflow not active", "the terminal message survives wrapping for diagnosis")
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
// classification at exhaustion (PR #8801 review): a poll that ends on the
// expected not-found lag is infra-marked, and any other last error is
// returned unmarked with its cause, so the classifier decides from the
// real error.
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
			wantMsg:    "workflow not active",
		},
		"rate limit 429 stays unmarked and classifies infra": {
			err:        &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "too many requests"},
			wantClass:  behaviourfail.Infrastructure,
			wantMarked: false,
			wantMsg:    "polling fullsend.yaml on org/repo failed",
		},
		"permission 403 stays unmarked and classifies scenario": {
			err:        &gh.APIError{StatusCode: http.StatusForbidden, Message: "resource not accessible by integration"},
			wantClass:  behaviourfail.Scenario,
			wantMarked: false,
			wantMsg:    "polling fullsend.yaml on org/repo failed",
		},
		"server 500 stays unmarked and classifies infra": {
			err:        &gh.APIError{StatusCode: http.StatusInternalServerError, Message: "boom"},
			wantClass:  behaviourfail.Infrastructure,
			wantMarked: false,
			wantMsg:    "polling fullsend.yaml on org/repo failed",
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

// TestInfraClassSetupGitHubTransient pins #9497: a `github setup`
// subprocess that fails on a GitHub 5xx the client gave up on, or on an
// HTTP/2 CANCEL stream reset, is infrastructure, in a single attempt.
// Any other setup failure stays scenario, and the same texts outside
// setup — or beside a scenario cause in a join — stay scenario too.
func TestInfraClassSetupGitHubTransient(t *testing.T) {
	const cliPrefix = "[cli] fullsend github setup org/repo --direct --mint-url https://mint.test --runtime dummy --vendor failed: exit status 1\n"
	const streamReset = "Error: committing scaffold files: create blob for .fullsend/bin/fullsend: decode blob: stream error: stream ID 23; CANCEL; received from peer"
	const refs500 = "Error: committing scaffold files: update ref: github api: 500 retryable error after 5 attempts on PATCH /repos/halfsend-07/test-repo-05/git/refs/heads/main (last delay: 16s)"

	for name, tc := range map[string]struct {
		cliOutput  string
		wantClass  behaviourfail.Class
		wantReason string
	}{
		"stream reset on create blob": {
			cliOutput:  streamReset,
			wantClass:  behaviourfail.Infrastructure,
			wantReason: "setup: github stream reset",
		},
		"500 on PATCH git/refs after the client's retries": {
			cliOutput:  refs500,
			wantClass:  behaviourfail.Infrastructure,
			wantReason: "setup: github server error",
		},
		"404": {
			cliOutput: "Error: committing scaffold files: create tree: github api: 404 Not Found",
			wantClass: behaviourfail.Scenario,
		},
		"422 validation": {
			cliOutput: "Error: committing scaffold files: update ref: github api: 422 Validation Failed (Reference update failed)",
			wantClass: behaviourfail.Scenario,
		},
		"non-github error": {
			cliOutput: "Error: loading config preset: open /tmp/preset.yaml: no such file or directory",
			wantClass: behaviourfail.Scenario,
		},
		"panic": {
			cliOutput: "panic: runtime error: invalid memory address or nil pointer dereference\n\ngoroutine 1 [running]:",
			wantClass: behaviourfail.Scenario,
		},
	} {
		t.Run(name, func(t *testing.T) {
			attempts, err := install.InstallFullsendSetupCLIErrorForTest(t, cliPrefix+tc.cliOutput)
			require.Error(t, err)
			assert.Equal(t, 1, attempts, "only the read-after-create 404 re-runs setup in the driver")
			assert.Equal(t, tc.wantClass, failclass.Classify(err))
			if tc.wantReason == "" {
				assert.False(t, behaviourfail.Is(err), "unrecognized setup failures stay unmarked")
				return
			}
			var infra *behaviourfail.InfraError
			require.ErrorAs(t, err, &infra)
			assert.Equal(t, tc.wantReason, infra.Reason)
			assert.Contains(t, err.Error(), "github setup org/repo", "the setup call stays named for diagnosis")
		})
	}

	t.Run("same texts outside setup stay scenario", func(t *testing.T) {
		for _, out := range []string{streamReset, refs500} {
			err := fmt.Errorf("[cli] fullsend admin status org/repo failed: exit status 1\n%s", out)
			assert.Equal(t, behaviourfail.Scenario, failclass.Classify(err),
				"only the setup path marks CLI text; a scenario step's CLI failure has no typed error to match")
		}
	})

	t.Run("joined with a scenario branch stays scenario", func(t *testing.T) {
		_, setupErr := install.InstallFullsendSetupCLIErrorForTest(t, cliPrefix+streamReset)
		require.Equal(t, behaviourfail.Infrastructure, failclass.Classify(setupErr))
		joined := errors.Join(setupErr, errors.New("expected label ready-to-code, got none"))
		assert.Equal(t, behaviourfail.Scenario, failclass.Classify(joined),
			"a scenario cause beside the setup failure keeps the whole error scenario")
	})
}
