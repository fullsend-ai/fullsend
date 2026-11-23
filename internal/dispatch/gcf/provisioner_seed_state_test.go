package gcf

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failedFunctionFixture returns a provisioner for an existing function whose
// Cloud Functions state is FAILED (so a code deploy is forced) and whose
// metadata carries stale grants that the serving Cloud Run revision revoked.
func failedFunctionFixture(t *testing.T, fake *fakeGCFClient) *Provisioner {
	t.Helper()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "FAILED",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"GCP_PROJECT_NUMBER":     "123456789",
			"ALLOWED_ORGS":           "stale-org,test-org",
			"ALLOWED_ROLES":          "coder",
			"ROLE_APP_IDS":           `{"coder":"11111"}`,
			"ALLOWED_WORKFLOW_FILES": "*",
			"FULLSEND_SOURCE_HASH":   "old-hash-that-wont-match",
		},
	}
	return newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)
}

// A non-ACTIVE Cloud Functions state does not prove the backing Cloud Run
// service has no serving revision: when it has one, the deploy environment is
// seeded from it and traffic is pinned before the source deploy, exactly as for
// an ACTIVE function.
func TestProvisioner_Provision_FailedFunctionWithServingRevisionIsSeededAndPinned(t *testing.T) {
	fake := newFakeGCFClient()
	p := failedFunctionFixture(t, fake)
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort: "fullsend-mint-00114-fm9",
		TrafficAllocType:         "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST",
		TemplateMatchesTraffic:   true,
		TrafficPercent:           100,
		Etag:                     "etag-seed",
		TrafficEnvVars: map[string]string{
			"ALLOWED_ORGS":  "test-org",
			"ALLOWED_ROLES": "coder",
			"ROLE_APP_IDS":  `{"coder":"11111"}`,
		},
	}

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	deployed := fake.lastCreateFunctionEnvVars
	require.NotNil(t, deployed)
	assert.Equal(t, "test-org", deployed["ALLOWED_ORGS"], "stale grant must not be deployed")
	pin := indexOfCall(fake.calls, "PinServiceTraffic")
	require.GreaterOrEqual(t, pin, 0)
	assert.Less(t, pin, indexOfCall(fake.calls, "UpdateFunction"))
	assert.Equal(t, "etag-seed", fake.pinEtags[0])
}

// When the Cloud Run read fails for any reason other than a positive
// "service does not exist", or the service has a latest-ready revision but no
// resolvable serving revision, the deploy is refused regardless of the Cloud
// Functions state.
func TestProvisioner_Provision_FailedFunctionUnverifiedServingStateRefusesDeploy(t *testing.T) {
	cases := map[string]struct {
		err  error
		info *ServiceRevisionInfo
	}{
		"read failure": {err: fmt.Errorf("unexpected status 503 getting Cloud Run service")},
		"ready revision without traffic revision": {
			info: &ServiceRevisionInfo{LatestReadyRevisionShort: "fullsend-mint-00114-fm9"},
		},
		"unsettled routing": {
			info: &ServiceRevisionInfo{RoutingUnsettled: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGCFClient()
			p := failedFunctionFixture(t, fake)
			if tc.err != nil {
				fake.errs["GetServiceRevisionInfo"] = tc.err
			}
			fake.revisionInfo = tc.info

			_, err := p.Provision(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), retryDeployAdvice)
			assert.NotContains(t, fake.calls, "UploadFunctionSource")
			assert.NotContains(t, fake.calls, "UpdateFunction")
		})
	}
}

// A positive "no such service" from Cloud Run on a non-ACTIVE function means
// there is no previous serving service, so the unseeded deploy may proceed. The
// same signal on an ACTIVE function is contradictory and is still refused.
func TestProvisioner_Provision_FailedFunctionWithoutServiceMayDeploy(t *testing.T) {
	notFound := fmt.Errorf("unexpected status 404 getting Cloud Run service: %w", ErrServiceNotFound)

	t.Run("non-active proceeds", func(t *testing.T) {
		fake := newFakeGCFClient()
		p := failedFunctionFixture(t, fake)
		fake.errs["GetServiceRevisionInfo"] = notFound

		// The post-build reconciliation re-reads the service and sees the same
		// error, so Provision fails later; the seed step itself must not have
		// refused the deploy.
		_, _ = p.Provision(context.Background())
		assert.Contains(t, fake.calls, "UpdateFunction")
		assert.NotContains(t, fake.calls, "PinServiceTrafficIfMatch")
	})

	t.Run("active is refused", func(t *testing.T) {
		fake := newFakeGCFClient()
		p := failedFunctionFixture(t, fake)
		fake.functionInfo.State = "ACTIVE"
		fake.errs["GetServiceRevisionInfo"] = notFound

		_, err := p.Provision(context.Background())
		require.Error(t, err)
		assert.NotContains(t, fake.calls, "UpdateFunction")
	})
}

// A service that exists but has never produced a ready revision has no
// previous serving revision, so a non-ACTIVE function may deploy unseeded.
func TestProvisioner_Provision_FailedFunctionWithNeverReadyServiceMayDeploy(t *testing.T) {
	fake := newFakeGCFClient()
	p := failedFunctionFixture(t, fake)
	seedRead := &ServiceRevisionInfo{Etag: "etag-seed"}
	postBuild := &ServiceRevisionInfo{
		TrafficRevisionShort:       "fullsend-mint-00001-abc",
		LatestCreatedRevisionShort: "fullsend-mint-00001-abc",
		LatestReadyRevisionShort:   "fullsend-mint-00001-abc",
		TemplateMatchesTraffic:     true,
		TrafficPercent:             100,
		TrafficEnvVars:             map[string]string{"ALLOWED_ORGS": "test-org"},
	}
	fake.revisionInfoSequence = []*ServiceRevisionInfo{seedRead}
	fake.revisionInfo = postBuild

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "UpdateFunction")
	assert.NotContains(t, fake.calls, "PinServiceTrafficIfMatch", "nothing is serving, so there is nothing to pin")
}

// A configured role that is still registered at the post-build read but with a
// different app ID was replaced while the source built; reconciliation would
// overwrite the new ID with the deploy's configured one, so the deploy is
// refused with retry advice and nothing is written or pinned afterwards.
func TestProvisioner_Provision_CodeChanged_RoleReplacedDuringSourceDeployIsNotOverwritten(t *testing.T) {
	fake := newFakeGCFClient()
	p := sourceDeployFixture(t, fake)
	seedRead := &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort: "fullsend-mint-00114-fm9",
		TrafficAllocType:         "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST",
		TemplateMatchesTraffic:   true,
		TrafficPercent:           100,
		Etag:                     "etag-seed",
		TrafficEnvVars:           map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "coder", "ROLE_APP_IDS": `{"coder":"11111"}`},
	}
	// A concurrent admin rotated coder's app ID on the serving revision.
	postBuild := &ServiceRevisionInfo{
		TrafficRevisionShort:       "fullsend-mint-00115-rv1",
		LatestCreatedRevisionShort: "fullsend-mint-00116-qp5",
		LatestReadyRevisionShort:   "fullsend-mint-00116-qp5",
		TrafficAllocType:           "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION",
		TrafficPercent:             100,
		Etag:                       "etag-post",
		TrafficEnvVars:             map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "coder", "ROLE_APP_IDS": `{"coder":"33333"}`},
		TemplateEnvVars:            map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "coder", "ROLE_APP_IDS": `{"coder":"12345"}`},
	}
	fake.revisionInfoSequence = []*ServiceRevisionInfo{seedRead}
	fake.revisionInfo = postBuild

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replaced while the source was building")
	assert.Contains(t, err.Error(), "coder")
	assert.Contains(t, err.Error(), retryDeployAdvice)
	assert.Nil(t, fake.lastUpdateServiceEnvVars, "the replaced role's ID must not be overwritten")
	assert.Equal(t, 1, countCalls(fake.calls, "PinServiceTraffic"), "only the pre-deploy pin may run")
}
