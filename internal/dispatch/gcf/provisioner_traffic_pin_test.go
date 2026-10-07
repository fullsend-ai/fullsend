package gcf

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A source deploy must reconcile and pin the new revision BEFORE org
// registration. Registration patches copy the traffic-serving revision's env
// into a new revision, so running it first against the stale serving revision
// would overwrite the freshly deployed ROLE_APP_IDS and FULLSEND_SOURCE_HASH.
func TestProvisioner_Provision_CodeChanged_ReconcilesBeforeRegistration(t *testing.T) {
	fake := newFakeGCFClient()
	fake.applyEnvUpdatesToTraffic = true
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"GCP_PROJECT_NUMBER":     "123456789",
			"WIF_POOL_NAME":          "fullsend-pool",
			"WIF_PROVIDER_NAME":      "github-oidc",
			"ALLOWED_ORGS":           "test-org",
			"ALLOWED_ROLES":          "coder",
			"ROLE_APP_IDS":           `{"coder":"11111"}`,
			"FULLSEND_SOURCE_HASH":   "old-hash-that-wont-match",
			"ALLOWED_WORKFLOW_FILES": "*",
		},
	}
	// Serving revision: old role ID, old hash. Template (latest) revision:
	// the newly configured role ID and deployment metadata, plus a revoked
	// org that only the stale template still carries.
	servingEnv := map[string]string{
		"ALLOWED_ORGS":         "test-org",
		"ALLOWED_ROLES":        "coder",
		"ROLE_APP_IDS":         `{"coder":"11111"}`,
		"FULLSEND_SOURCE_HASH": "old-serving-hash",
	}
	fake.trafficEnvVars = servingEnv
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort: "fullsend-mint-00115-qp5",
		TemplateMatchesTraffic:   false,
		TrafficPercent:           100,
		TrafficEnvVars:           servingEnv,
		TemplateEnvVars: map[string]string{
			"ALLOWED_ORGS":         "revoked-org,test-org",
			"ALLOWED_ROLES":        "coder",
			"ROLE_APP_IDS":         `{"coder":"12345"}`,
			"FULLSEND_SOURCE_HASH": "new-template-hash",
		},
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"new-org"}, // registration requires a patch
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	require.Len(t, fake.updateServiceEnvVarsHistory, 2,
		"one reconciliation patch, then one registration patch")
	reconcile, register := fake.updateServiceEnvVarsHistory[0], fake.updateServiceEnvVarsHistory[1]

	// Reconciliation ran first, against the serving revision's allow-list.
	assert.Equal(t, "test-org", reconcile["ALLOWED_ORGS"])
	assert.JSONEq(t, `{"coder":"12345"}`, reconcile["ROLE_APP_IDS"])
	assert.Equal(t, "new-template-hash", reconcile["FULLSEND_SOURCE_HASH"])

	// Registration then patched the reconciled revision: the new org is
	// added, and the newly configured role ID and deployment metadata survive.
	assert.Equal(t, "new-org,test-org", register["ALLOWED_ORGS"])
	assert.JSONEq(t, `{"coder":"12345"}`, register["ROLE_APP_IDS"],
		"newly configured role IDs must survive the registration patch")
	assert.Equal(t, "new-template-hash", register["FULLSEND_SOURCE_HASH"],
		"deployment metadata must survive the registration patch")
	assert.Equal(t, "coder", register["ALLOWED_ROLES"])
}

func TestEnsureTrafficOnLatestRevision_SplitTraffic_PinsWhenLatestIsMajorityTarget(t *testing.T) {
	// The latest revision receives 60% and an older revision 40%: the older
	// revision's code is still serving, so the pin must not be skipped.
	fake := newFakeGCFClient()
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:       "fullsend-mint-00115-qp5",
		TrafficPercent:             60,
		LatestCreatedRevisionShort: "fullsend-mint-00115-qp5",
		LatestReadyRevisionShort:   "fullsend-mint-00115-qp5",
		TemplateMatchesTraffic:     false, // live client reports false for a split
		TrafficEnvVars:             map[string]string{"ALLOWED_ORGS": "test-org"},
		TemplateEnvVars:            map[string]string{"ALLOWED_ORGS": "test-org"},
	}
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

	require.NoError(t, p.ensureTrafficOnLatestRevision(context.Background(), false))
	assert.Contains(t, fake.calls, "PinServiceTraffic")
	assert.Equal(t, "fullsend-mint-00115-qp5", fake.lastPinnedRevision)
}

func TestEnsureTrafficOnLatestRevision_FullTraffic_SkipsPinWhenTargetServing(t *testing.T) {
	fake := newFakeGCFClient()
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:       "fullsend-mint-00115-qp5",
		TrafficPercent:             100,
		LatestCreatedRevisionShort: "fullsend-mint-00115-qp5",
		TemplateMatchesTraffic:     false, // force the later target==traffic early return
		TrafficEnvVars:             map[string]string{"ALLOWED_ORGS": "test-org"},
		TemplateEnvVars:            map[string]string{"ALLOWED_ORGS": "test-org"},
	}
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

	require.NoError(t, p.ensureTrafficOnLatestRevision(context.Background(), false))
	assert.NotContains(t, fake.calls, "PinServiceTraffic")
}

func TestProvisioner_Provision_SameHash_PinsWhenTrafficSplit(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	sourceZip, err := bundleFunctionSource(srcDir, "", "", StatusGitHubAuth{})
	require.NoError(t, err)

	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":           "test-org",
			"ALLOWED_ROLES":          "coder",
			"ROLE_APP_IDS":           `{"coder":"12345"}`,
			"FULLSEND_SOURCE_HASH":   sha256Hex(sourceZip),
			"ALLOWED_WORKFLOW_FILES": "*",
		},
	}
	env := map[string]string{"ALLOWED_ORGS": "test-org", "ROLE_APP_IDS": `{"coder":"12345"}`, "ALLOWED_ROLES": "coder"}
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:       "fullsend-mint-00115-qp5",
		TrafficPercent:             60,
		LatestCreatedRevisionShort: "fullsend-mint-00115-qp5",
		LatestReadyRevisionShort:   "fullsend-mint-00115-qp5",
		TemplateMatchesTraffic:     false,
		TrafficEnvVars:             env,
		TemplateEnvVars:            env,
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: srcDir,
	}, fake)

	_, err = p.Provision(context.Background())
	require.NoError(t, err)
	assert.NotContains(t, fake.calls, "UpdateFunction")
	assert.Equal(t, "fullsend-mint-00115-qp5", fake.lastPinnedRevision)
}

func TestReconcileTargetEnvVars_AllowedRoles(t *testing.T) {
	roleIDs := `{"coder":"1","reviewer":"2","triage":"3"}`

	parse := func(t *testing.T, v string) map[string]string {
		t.Helper()
		var m map[string]string
		require.NoError(t, json.Unmarshal([]byte(v), &m))
		return m
	}

	t.Run("identical_role_ids_different_allow_lists_restores_serving_restriction", func(t *testing.T) {
		traffic := map[string]string{"ROLE_APP_IDS": roleIDs, "ALLOWED_ROLES": "coder"}
		target := map[string]string{"ROLE_APP_IDS": roleIDs, "ALLOWED_ROLES": "coder,reviewer,triage"}

		got, err := reconcileTargetEnvVars(traffic, target, false, nil)
		require.NoError(t, err)
		require.NotNil(t, got, "differing allow-lists must trigger reconciliation")
		assert.Equal(t, "coder", got["ALLOWED_ROLES"])
		assert.JSONEq(t, roleIDs, got["ROLE_APP_IDS"])
	})

	t.Run("identical_role_ids_and_allow_lists_is_noop", func(t *testing.T) {
		traffic := map[string]string{"ROLE_APP_IDS": roleIDs, "ALLOWED_ROLES": "coder,reviewer"}
		target := map[string]string{"ROLE_APP_IDS": roleIDs, "ALLOWED_ROLES": "reviewer,coder"}

		got, err := reconcileTargetEnvVars(traffic, target, false, nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("empty_serving_allow_list_means_all_roles", func(t *testing.T) {
		traffic := map[string]string{"ROLE_APP_IDS": roleIDs}
		target := map[string]string{"ROLE_APP_IDS": roleIDs, "ALLOWED_ROLES": "coder,reviewer,triage"}

		got, err := reconcileTargetEnvVars(traffic, target, false, nil)
		require.NoError(t, err)
		assert.Nil(t, got, "an empty ALLOWED_ROLES allows every registered role, same as the target's explicit full list")
	})

	t.Run("role_id_update_preserves_restricted_serving_allow_list", func(t *testing.T) {
		// Serving revision restricts to "coder". The template has different
		// role IDs (so ROLE_APP_IDS is reconciled) and a derived all-roles
		// allow-list that must not be adopted.
		traffic := map[string]string{"ROLE_APP_IDS": `{"coder":"1","reviewer":"2"}`, "ALLOWED_ROLES": "coder"}
		target := map[string]string{"ROLE_APP_IDS": `{"coder":"1"}`, "ALLOWED_ROLES": "coder"}

		got, err := reconcileTargetEnvVars(traffic, target, false, nil)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, map[string]string{"coder": "1", "reviewer": "2"}, parse(t, got["ROLE_APP_IDS"]))
		assert.Equal(t, "coder", got["ALLOWED_ROLES"],
			"reviewer is registered but was not allowed on the serving revision, so it must stay disallowed")
	})

	t.Run("role_newly_registered_by_this_deploy_is_allowed", func(t *testing.T) {
		traffic := map[string]string{"ROLE_APP_IDS": `{"coder":"1"}`, "ALLOWED_ROLES": "coder"}
		target := map[string]string{"ROLE_APP_IDS": `{"coder":"1","fix":"9"}`, "ALLOWED_ROLES": "coder,fix"}

		got, err := reconcileTargetEnvVars(traffic, target, false, map[string]string{"fix": "9"})
		require.NoError(t, err)
		// Template already matches the desired state: nothing to patch.
		assert.Nil(t, got)

		// Same, but the target lacks the new role entirely.
		target = map[string]string{"ROLE_APP_IDS": `{"coder":"1"}`, "ALLOWED_ROLES": "coder"}
		got, err = reconcileTargetEnvVars(traffic, target, false, map[string]string{"fix": "9"})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, map[string]string{"coder": "1", "fix": "9"}, parse(t, got["ROLE_APP_IDS"]))
		assert.Equal(t, "coder,fix", got["ALLOWED_ROLES"])
	})

	t.Run("role_already_registered_but_restricted_on_serving_stays_restricted", func(t *testing.T) {
		traffic := map[string]string{"ROLE_APP_IDS": `{"coder":"1","reviewer":"2"}`, "ALLOWED_ROLES": "coder"}
		target := map[string]string{"ROLE_APP_IDS": `{"coder":"1","reviewer":"2"}`, "ALLOWED_ROLES": "coder,reviewer"}

		got, err := reconcileTargetEnvVars(traffic, target, false, map[string]string{"reviewer": "2"})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "coder", got["ALLOWED_ROLES"])
	})

	t.Run("refuses_empty_allow_list_that_mint_would_treat_as_all_roles", func(t *testing.T) {
		// Serving allows only "ghost", which is no longer registered. The
		// intersection with the reconciled roles is empty, and writing an
		// empty ALLOWED_ROLES would widen access to every role.
		traffic := map[string]string{"ROLE_APP_IDS": `{"coder":"1"}`, "ALLOWED_ROLES": "ghost"}
		target := map[string]string{"ROLE_APP_IDS": `{"coder":"1"}`, "ALLOWED_ROLES": "coder"}

		got, err := reconcileTargetEnvVars(traffic, target, false, nil)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.Contains(t, err.Error(), "empty allow-list")
	})
}

// A deny-all workflow policy (absent ALLOWED_WORKFLOW_FILES) on the serving
// revision must survive the whole deploy flow — reconciliation AND the
// placeholder/org registration that follows it — on both the hash-skip and
// the source-deploy paths.
func TestProvisioner_Provision_ExistingMint_PreservesDenyAllWorkflowFilesThroughRegistration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sameHash   bool
		realOrgs   []string
		wantUpdate int
	}{
		{name: "hash_skip_placeholder_org", sameHash: true, realOrgs: []string{PlaceholderOrg}},
		{name: "source_deploy_placeholder_org", sameHash: false, realOrgs: []string{PlaceholderOrg}},
		{name: "hash_skip_real_org", sameHash: true, realOrgs: []string{"new-org"}},
		{name: "source_deploy_real_org", sameHash: false, realOrgs: []string{"new-org"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srcDir := fakeFunctionSourceDir(t)
			sourceZip, err := bundleFunctionSource(srcDir, "", "", StatusGitHubAuth{})
			require.NoError(t, err)
			hash := "old-hash"
			if tc.sameHash {
				hash = sha256Hex(sourceZip)
			}

			fake := newFakeGCFClient()
			fake.applyEnvUpdatesToTraffic = true
			fake.functionInfo = &FunctionInfo{
				State: "ACTIVE",
				URI:   "https://fullsend-mint-abc123.run.app",
				EnvVars: map[string]string{
					"ALLOWED_ORGS":         "test-org",
					"ALLOWED_ROLES":        "coder",
					"ROLE_APP_IDS":         `{"coder":"12345"}`,
					"FULLSEND_SOURCE_HASH": hash,
				},
			}
			// Serving revision: no ALLOWED_WORKFLOW_FILES (deny-all). The
			// template carries a stale wildcard.
			servingEnv := map[string]string{
				"ALLOWED_ORGS":  "test-org",
				"ALLOWED_ROLES": "coder",
				"ROLE_APP_IDS":  `{"coder":"12345"}`,
			}
			fake.trafficEnvVars = servingEnv
			fake.revisionInfo = &ServiceRevisionInfo{
				TrafficRevisionShort:     "fullsend-mint-00114-fm9",
				LatestReadyRevisionShort: "fullsend-mint-00115-qp5",
				TrafficPercent:           100,
				Etag:                     "etag-1",
				TrafficEnvVars:           servingEnv,
				TemplateEnvVars: map[string]string{
					"ALLOWED_ORGS":           "test-org",
					"ALLOWED_ROLES":          "coder",
					"ROLE_APP_IDS":           `{"coder":"12345"}`,
					"ALLOWED_WORKFLOW_FILES": "*",
				},
			}

			p := newTestProvisioner(Config{
				ProjectID:         "my-project",
				GitHubOrgs:        tc.realOrgs,
				AgentPEMs:         singleRolePEMs(),
				AgentAppIDs:       singleRoleAppIDs(),
				FunctionSourceDir: srcDir,
			}, fake)

			_, err = p.Provision(context.Background())
			require.NoError(t, err)

			require.NotEmpty(t, fake.updateServiceEnvVarsHistory, "stale wildcard must have been reconciled away")
			for i, written := range fake.updateServiceEnvVarsHistory {
				assert.NotContains(t, written, "ALLOWED_WORKFLOW_FILES",
					"env write %d must not grant or widen workflow files", i)
			}
			assert.NotContains(t, fake.trafficEnvVars, "ALLOWED_WORKFLOW_FILES",
				"the serving revision must remain deny-all after the full deploy flow")
		})
	}
}

// On an existing mint the placeholder org must not trigger a registration
// write at all.
func TestProvisioner_Provision_HashSkip_SkipsPlaceholderOrgRegistration(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	sourceZip, err := bundleFunctionSource(srcDir, "", "", StatusGitHubAuth{})
	require.NoError(t, err)

	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":         "test-org",
			"ROLE_APP_IDS":         `{"coder":"12345"}`,
			"FULLSEND_SOURCE_HASH": sha256Hex(sourceZip),
		},
	}
	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{PlaceholderOrg},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: srcDir,
	}, fake)

	_, err = p.Provision(context.Background())
	require.NoError(t, err)
	assert.NotContains(t, fake.calls, "UpdateServiceEnvVars")
}

// If role secret preparation fails on a hash-skip deploy with divergent
// traffic, nothing may have been published to the live mint yet.
func TestProvisioner_Provision_SameHash_SecretFailureHappensBeforeReconciliation(t *testing.T) {
	for _, failing := range []string{"AddSecretVersion", "GetSecret"} {
		t.Run(failing, func(t *testing.T) {
			srcDir := fakeFunctionSourceDir(t)
			sourceZip, err := bundleFunctionSource(srcDir, "", "", StatusGitHubAuth{})
			require.NoError(t, err)

			fake := newFakeGCFClient()
			fake.functionInfo = &FunctionInfo{
				State: "ACTIVE",
				URI:   "https://fullsend-mint-abc123.run.app",
				EnvVars: map[string]string{
					"ALLOWED_ORGS":         "test-org",
					"ROLE_APP_IDS":         `{"coder":"11111"}`,
					"FULLSEND_SOURCE_HASH": sha256Hex(sourceZip),
				},
			}
			servingEnv := map[string]string{
				"ALLOWED_ORGS":  "test-org",
				"ALLOWED_ROLES": "coder",
				"ROLE_APP_IDS":  `{"coder":"11111"}`,
			}
			fake.trafficEnvVars = servingEnv
			// Reconciliation would publish the replacement app ID (12345).
			fake.revisionInfo = &ServiceRevisionInfo{
				TrafficRevisionShort:     "fullsend-mint-00114-fm9",
				LatestReadyRevisionShort: "fullsend-mint-00115-qp5",
				TrafficPercent:           100,
				Etag:                     "etag-1",
				TrafficEnvVars:           servingEnv,
				TemplateEnvVars:          servingEnv,
			}
			cfg := Config{
				ProjectID:         "my-project",
				GitHubOrgs:        []string{"test-org"},
				AgentAppIDs:       singleRoleAppIDs(),
				FunctionSourceDir: srcDir,
			}
			if failing == "AddSecretVersion" {
				cfg.AgentPEMs = singleRolePEMs()
			} // else: no fresh PEM, so the secret is verified via GetSecret
			fake.errs[failing] = fmt.Errorf("secret manager unavailable")
			p := newTestProvisioner(cfg, fake)

			_, err = p.Provision(context.Background())
			require.Error(t, err)
			assert.NotContains(t, fake.calls, "UpdateServiceEnvVars",
				"app IDs must not be published before secrets are prepared")
			assert.NotContains(t, fake.calls, "PinServiceTraffic",
				"traffic must not move before secrets are prepared")
			assert.NotContains(t, fake.calls, "GetServiceRevisionInfo",
				"traffic reconciliation must not start before secrets are prepared")
		})
	}
}

// The hash-skip path must not store PEMs twice now that secrets are prepared
// ahead of reconciliation.
func TestProvisioner_Provision_SameHash_StoresPEMOnce(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	sourceZip, err := bundleFunctionSource(srcDir, "", "", StatusGitHubAuth{})
	require.NoError(t, err)

	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":         "test-org",
			"ROLE_APP_IDS":         `{"coder":"12345"}`,
			"FULLSEND_SOURCE_HASH": sha256Hex(sourceZip),
		},
	}
	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: srcDir,
	}, fake)

	_, err = p.Provision(context.Background())
	require.NoError(t, err)
	assert.Len(t, fake.secretVersionNames, 1)
}

func divergedRevisionInfo(etag string, trafficEnv map[string]string) *ServiceRevisionInfo {
	return &ServiceRevisionInfo{
		TrafficRevisionShort:       "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort:   "fullsend-mint-00114-fm9",
		LatestCreatedRevisionShort: "fullsend-mint-00115-qp5",
		TrafficPercent:             100,
		Etag:                       etag,
		RecentRevisions: []RevisionSummary{
			{Name: "fullsend-mint-00115-qp5", Active: false},
			{Name: "fullsend-mint-00114-fm9", Active: true},
		},
		TrafficEnvVars:  trafficEnv,
		TemplateEnvVars: map[string]string{"ALLOWED_ORGS": "test-org", "ROLE_APP_IDS": `{"coder":"1","triage":"2"}`, "ALLOWED_ROLES": "coder,triage"},
	}
}

func readyRevisionInfo(base *ServiceRevisionInfo, etag string, trafficRev string, trafficEnv map[string]string) *ServiceRevisionInfo {
	info := *base
	info.Etag = etag
	info.TrafficRevisionShort = trafficRev
	info.LatestReadyRevisionShort = "fullsend-mint-00115-qp5"
	info.TrafficEnvVars = trafficEnv
	info.RecentRevisions = []RevisionSummary{{Name: "fullsend-mint-00115-qp5", Active: true}}
	return &info
}

// A restrictive revision that becomes serving while waiting for the target
// to become ready must not be overwritten by a pin based on the earlier
// authorization snapshot.
func TestEnsureTrafficOnLatestRevision_RestrictiveRevisionServingDuringWaitIsNotOverwritten(t *testing.T) {
	servingBefore := map[string]string{"ALLOWED_ORGS": "test-org", "ROLE_APP_IDS": `{"coder":"1","triage":"2"}`, "ALLOWED_ROLES": "coder,triage"}
	// During the wait a role removal publishes a restrictive revision that
	// now serves traffic (triage revoked).
	servingAfter := map[string]string{"ALLOWED_ORGS": "test-org", "ROLE_APP_IDS": `{"coder":"1"}`, "ALLOWED_ROLES": "coder"}

	fake := newFakeGCFClient()
	before := divergedRevisionInfo("etag-1", servingBefore)
	after := readyRevisionInfo(before, "etag-2", "fullsend-mint-00116-new", servingAfter)
	// Initial read, then the poll that observes the target Ready.
	fake.revisionInfoSequence = []*ServiceRevisionInfo{before, after}
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

	err := p.ensureTrafficOnLatestRevision(context.Background(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "changed while waiting")
	assert.NotContains(t, fake.calls, "PinServiceTraffic")
	assert.NotContains(t, fake.calls, "UpdateServiceEnvVars")
	assert.NotContains(t, err.Error(), "gcloud run services update-traffic")
}

func TestEnsureTrafficOnLatestRevision_LatestRevisionChangedDuringWaitIsNotPinned(t *testing.T) {
	env := map[string]string{"ALLOWED_ORGS": "test-org", "ROLE_APP_IDS": `{"coder":"1","triage":"2"}`, "ALLOWED_ROLES": "coder,triage"}
	fake := newFakeGCFClient()
	before := divergedRevisionInfo("etag-1", env)
	after := readyRevisionInfo(before, "etag-2", "fullsend-mint-00114-fm9", env)
	after.LatestCreatedRevisionShort = "fullsend-mint-00116-new"
	fake.revisionInfoSequence = []*ServiceRevisionInfo{before, after}
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

	err := p.ensureTrafficOnLatestRevision(context.Background(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "changed from fullsend-mint-00115-qp5 to fullsend-mint-00116-new")
	assert.NotContains(t, fake.calls, "PinServiceTraffic")
}

func TestEnsureTrafficOnLatestRevision_TrafficRevisionUnknownAfterWaitIsNotPinned(t *testing.T) {
	env := map[string]string{"ALLOWED_ORGS": "test-org", "ROLE_APP_IDS": `{"coder":"1","triage":"2"}`, "ALLOWED_ROLES": "coder,triage"}
	fake := newFakeGCFClient()
	before := divergedRevisionInfo("etag-1", env)
	after := readyRevisionInfo(before, "etag-2", "", env)
	fake.revisionInfoSequence = []*ServiceRevisionInfo{before, after}
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

	err := p.ensureTrafficOnLatestRevision(context.Background(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer reported")
	assert.NotContains(t, fake.calls, "PinServiceTraffic")
}

// With unchanged serving state after the wait, the pin goes through and is
// conditioned on the etag of the fresh (post-wait) read, not the stale one.
func TestEnsureTrafficOnLatestRevision_PinIsConditionedOnFreshEtagAfterWait(t *testing.T) {
	env := map[string]string{"ALLOWED_ORGS": "test-org", "ROLE_APP_IDS": `{"coder":"1","triage":"2"}`, "ALLOWED_ROLES": "coder,triage"}
	fake := newFakeGCFClient()
	before := divergedRevisionInfo("etag-stale", env)
	after := readyRevisionInfo(before, "etag-fresh", "fullsend-mint-00114-fm9", env)
	fake.revisionInfoSequence = []*ServiceRevisionInfo{before, after}
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

	require.NoError(t, p.ensureTrafficOnLatestRevision(context.Background(), false))
	assert.Equal(t, "fullsend-mint-00115-qp5", fake.lastPinnedRevision)
	assert.Equal(t, []string{"etag-fresh"}, fake.pinEtags)
}

func TestEnsureTrafficOnLatestRevision_PinWithoutWaitUsesVerifiedEtag(t *testing.T) {
	env := map[string]string{"ALLOWED_ORGS": "test-org"}
	fake := newFakeGCFClient()
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort: "fullsend-mint-00115-qp5",
		TrafficPercent:           100,
		Etag:                     "etag-1",
		TrafficEnvVars:           env,
		TemplateEnvVars:          env,
	}
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

	require.NoError(t, p.ensureTrafficOnLatestRevision(context.Background(), false))
	assert.Equal(t, []string{"etag-1"}, fake.pinEtags)
}

// A rejected etag precondition (service changed after verification) fails the
// deploy with retry advice rather than a manual traffic-shift command.
func TestEnsureTrafficOnLatestRevision_EtagPreconditionFailureFailsWithRetryAdvice(t *testing.T) {
	env := map[string]string{"ALLOWED_ORGS": "test-org"}
	fake := newFakeGCFClient()
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort: "fullsend-mint-00115-qp5",
		TrafficPercent:           100,
		Etag:                     "etag-1",
		TrafficEnvVars:           env,
		TemplateEnvVars:          env,
	}
	fake.errs["PinServiceTrafficIfMatch"] = fmt.Errorf("409 etag mismatch")
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

	err := p.ensureTrafficOnLatestRevision(context.Background(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "etag mismatch")
	assert.Contains(t, err.Error(), retryDeployAdvice)
	assert.NotContains(t, err.Error(), "gcloud run services update-traffic")
}

// A restrictive traffic-only rollback that is accepted but not yet observed
// must not be overwritten using the older revision's authorization: unsettled
// routing is refused before any reconciliation or pin.
func TestEnsureTrafficOnLatestRevision_UnsettledRoutingIsRefused(t *testing.T) {
	env := map[string]string{"ALLOWED_ORGS": "test-org"}
	fake := newFakeGCFClient()
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort: "fullsend-mint-00115-qp5",
		TrafficPercent:           100,
		Etag:                     "etag-1",
		TrafficEnvVars:           env,
		TemplateEnvVars:          map[string]string{"ALLOWED_ORGS": "test-org,other-org"},
		RoutingUnsettled:         true,
	}
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

	err := p.ensureTrafficOnLatestRevision(context.Background(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has not settled")
	assert.Contains(t, err.Error(), retryDeployAdvice)
	assert.NotContains(t, fake.calls, "PinServiceTraffic")
	assert.NotContains(t, fake.calls, "PinServiceTrafficIfMatch")
	assert.NotContains(t, fake.calls, "UpdateServiceEnvVarsIfMatch")
}

// TemplateMatchesTraffic and RoutingUnsettled can both be true (100% observed
// on the latest revision while an accepted rollback is pending). The deploy
// must not report success on that unsettled snapshot.
func TestEnsureTrafficOnLatestRevision_UnsettledRoutingRefusedEvenWhenTemplateMatchesTraffic(t *testing.T) {
	fake := newFakeGCFClient()
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00115-qp5",
		LatestReadyRevisionShort: "fullsend-mint-00115-qp5",
		TrafficPercent:           100,
		TemplateMatchesTraffic:   true,
		RoutingUnsettled:         true,
		Etag:                     "etag-1",
	}
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

	err := p.ensureTrafficOnLatestRevision(context.Background(), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has not settled")
	assert.Contains(t, err.Error(), retryDeployAdvice)
	assert.NotContains(t, fake.calls, "PinServiceTraffic")
	assert.NotContains(t, fake.calls, "UpdateServiceEnvVarsIfMatch")
}

// A first-deploy pin is conditioned on the etag of the verified read, and a
// rejected precondition withholds the manual traffic-shift command.
func TestEnsureTrafficOnLatestRevision_FirstDeployPinIsConditionedOnEtag(t *testing.T) {
	fake := newFakeGCFClient()
	fake.revisionInfo = &ServiceRevisionInfo{
		LatestReadyRevisionShort: "fullsend-mint-00115-qp5",
		Etag:                     "etag-first",
	}
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

	require.NoError(t, p.ensureTrafficOnLatestRevision(context.Background(), true))
	assert.Equal(t, "etag-first", fake.lastPinEtag)
	assert.Equal(t, "fullsend-mint-00115-qp5", fake.lastPinnedRevision)

	fake.errs["PinServiceTrafficIfMatch"] = fmt.Errorf("409 etag mismatch")
	err := p.ensureTrafficOnLatestRevision(context.Background(), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "etag mismatch")
	assert.Contains(t, err.Error(), retryDeployAdvice)
	assert.NotContains(t, err.Error(), "gcloud run services update-traffic")
}

func TestSafeRevisionName(t *testing.T) {
	assert.Equal(t, "fullsend-mint-00115-qp5", SafeRevisionName("fullsend-mint-00115-qp5"))
	for _, bad := range []string{"", "rev\x1b[31m", "rev\nname", "Rev-Upper"} {
		assert.Equal(t, "(invalid revision name)", SafeRevisionName(bad), "name %q", bad)
	}
}

// Under LATEST traffic allocation the revision built by a source deploy can
// serve as soon as UpdateFunction completes, so the deploy environment must
// already carry the serving revision's registrations and authorization policy
// even when Cloud Functions metadata is stale.
func TestProvisioner_Provision_CodeChanged_LatestAllocationSeedsDeployEnvFromServing(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"GCP_PROJECT_NUMBER":     "123456789",
			"ALLOWED_ORGS":           "stale-org,test-org",
			"ALLOWED_ROLES":          "coder,triage",
			"ROLE_APP_IDS":           `{"coder":"11111","triage":"22222"}`,
			"ALLOWED_WORKFLOW_FILES": "*",
			"FULLSEND_SOURCE_HASH":   "old-hash-that-wont-match",
		},
	}
	// The serving revision (patched directly on Cloud Run) revoked triage and
	// the stale org, and narrowed the workflow policy.
	servingEnv := map[string]string{
		"ALLOWED_ORGS":           "test-org",
		"ALLOWED_ROLES":          "coder",
		"ROLE_APP_IDS":           `{"coder":"11111"}`,
		"ALLOWED_WORKFLOW_FILES": "ci.yml",
		"FULLSEND_SOURCE_HASH":   "old-serving-hash",
	}
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort: "fullsend-mint-00114-fm9",
		TemplateMatchesTraffic:   true, // LATEST: the new revision serves immediately
		TrafficPercent:           100,
		TrafficEnvVars:           servingEnv,
	}
	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	deployed := fake.lastCreateFunctionEnvVars
	require.NotNil(t, deployed)
	assert.Equal(t, "test-org", deployed["ALLOWED_ORGS"], "revoked org must not be restored")
	assert.Equal(t, "coder", deployed["ALLOWED_ROLES"], "serving role restriction must be preserved")
	assert.JSONEq(t, `{"coder":"12345"}`, deployed["ROLE_APP_IDS"])
	assert.Equal(t, "ci.yml", deployed["ALLOWED_WORKFLOW_FILES"], "serving workflow policy must not be widened")
}

func countCalls(calls []string, name string) int {
	n := 0
	for _, c := range calls {
		if c == name {
			n++
		}
	}
	return n
}

func indexOfCall(calls []string, name string) int {
	for i, c := range calls {
		if c == name {
			return i
		}
	}
	return -1
}

func sourceDeployFixture(t *testing.T, fake *fakeGCFClient) *Provisioner {
	t.Helper()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"GCP_PROJECT_NUMBER":     "123456789",
			"ALLOWED_ORGS":           "test-org",
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

// Under LATEST allocation the source-built revision would serve the seeded
// environment as soon as it exists, so traffic must be pinned to the verified
// serving revision, conditioned on the seed read's etag, before the source is
// uploaded or UpdateFunction runs.
func TestProvisioner_Provision_CodeChanged_PinsServingRevisionBeforeSourceDeploy(t *testing.T) {
	fake := newFakeGCFClient()
	p := sourceDeployFixture(t, fake)
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort: "fullsend-mint-00114-fm9",
		TrafficAllocType:         "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST",
		TemplateMatchesTraffic:   true,
		TrafficPercent:           100,
		Etag:                     "etag-seed",
		TrafficEnvVars:           map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "coder", "ROLE_APP_IDS": `{"coder":"11111"}`},
	}

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	pin := indexOfCall(fake.calls, "PinServiceTraffic")
	require.GreaterOrEqual(t, pin, 0)
	assert.Less(t, pin, indexOfCall(fake.calls, "UploadFunctionSource"))
	assert.Less(t, pin, indexOfCall(fake.calls, "UpdateFunction"))
	assert.Equal(t, "etag-seed", fake.pinEtags[0])
	assert.Equal(t, "fullsend-mint-00114-fm9", fake.lastPinnedRevision)
}

// A rejected etag precondition on the pre-deploy pin means the service changed
// since the seed read: the deploy fails with retry advice before any source is
// uploaded or built.
func TestProvisioner_Provision_CodeChanged_PrePinPreconditionFailureRefusesDeploy(t *testing.T) {
	fake := newFakeGCFClient()
	p := sourceDeployFixture(t, fake)
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort: "fullsend-mint-00114-fm9",
		TrafficAllocType:     "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST",
		TrafficPercent:       100,
		Etag:                 "etag-seed",
		TrafficEnvVars:       map[string]string{"ALLOWED_ORGS": "test-org"},
	}
	fake.errs["PinServiceTrafficIfMatch"] = fmt.Errorf("409 etag mismatch")

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "etag mismatch")
	assert.Contains(t, err.Error(), retryDeployAdvice)
	assert.NotContains(t, err.Error(), "gcloud run services update-traffic")
	assert.NotContains(t, fake.calls, "UploadFunctionSource")
	assert.NotContains(t, fake.calls, "UpdateFunction")
}

// Traffic already explicitly pinned to the serving revision at 100% needs no
// pre-deploy pin.
func TestProvisioner_Provision_CodeChanged_AlreadyRevisionPinnedSkipsPrePin(t *testing.T) {
	fake := newFakeGCFClient()
	p := sourceDeployFixture(t, fake)
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:   "fullsend-mint-00114-fm9",
		TrafficAllocType:       "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION",
		TemplateMatchesTraffic: true,
		TrafficPercent:         100,
		Etag:                   "etag-seed",
		TrafficEnvVars:         map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "coder", "ROLE_APP_IDS": `{"coder":"11111"}`},
	}

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "UpdateFunction")
	assert.NotContains(t, fake.calls, "PinServiceTraffic")
}

// A grant revoked between the seed read and the end of the source deploy,
// while LATEST routing is kept, must not be restored by the new revision: the
// post-build reconciliation re-reads the serving state (traffic stayed on the
// old revision because of the pre-deploy pin) and narrows the new revision's
// environment before it is pinned.
func TestProvisioner_Provision_CodeChanged_RevocationDuringSourceDeployIsReconciled(t *testing.T) {
	fake := newFakeGCFClient()
	p := sourceDeployFixture(t, fake)
	seedRead := &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort: "fullsend-mint-00114-fm9",
		TrafficAllocType:         "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST",
		TemplateMatchesTraffic:   true,
		TrafficPercent:           100,
		Etag:                     "etag-seed",
		TrafficEnvVars:           map[string]string{"ALLOWED_ORGS": "test-org,other-org", "ALLOWED_ROLES": "coder", "ROLE_APP_IDS": `{"coder":"11111"}`},
	}
	// After the build: other-org was revoked on the serving revision (which
	// still receives traffic), while the new revision was built from the
	// earlier seed.
	postBuild := &ServiceRevisionInfo{
		TrafficRevisionShort:       "fullsend-mint-00114-fm9",
		LatestCreatedRevisionShort: "fullsend-mint-00115-qp5",
		LatestReadyRevisionShort:   "fullsend-mint-00115-qp5",
		TrafficAllocType:           "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION",
		TrafficPercent:             100,
		Etag:                       "etag-post",
		TrafficEnvVars:             map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "coder", "ROLE_APP_IDS": `{"coder":"11111"}`},
		TemplateEnvVars:            map[string]string{"ALLOWED_ORGS": "test-org,other-org", "ALLOWED_ROLES": "coder", "ROLE_APP_IDS": `{"coder":"11111"}`},
	}
	fake.revisionInfoSequence = []*ServiceRevisionInfo{seedRead}
	fake.revisionInfo = postBuild

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	require.NotNil(t, fake.lastUpdateServiceEnvVars, "post-build reconciliation must rewrite the new revision's env")
	assert.Equal(t, "test-org", fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "revoked org must not be restored")
	assert.Equal(t, []string{"etag-post"}, fake.updateEtags[:1])
}

// A role that is both configured for this deploy and registered at seed time,
// then revoked on the serving revision while the source builds, must not be
// re-added by the post-build reconciliation: the deploy is refused with retry
// advice and nothing is written or pinned.
func TestProvisioner_Provision_CodeChanged_RoleRevokedDuringSourceDeployIsNotRestored(t *testing.T) {
	fake := newFakeGCFClient()
	p := sourceDeployFixture(t, fake)
	seedRead := &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort: "fullsend-mint-00114-fm9",
		TrafficAllocType:         "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST",
		TemplateMatchesTraffic:   true,
		TrafficPercent:           100,
		Etag:                     "etag-seed",
		TrafficEnvVars:           map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "coder,triage", "ROLE_APP_IDS": `{"coder":"11111","triage":"22222"}`},
	}
	// After the build, a concurrent RemoveRoleFromMint revoked coder: the
	// revocation's own revision is now serving with a fresh etag, while the
	// source-built revision carries the seeded environment including coder.
	postBuild := &ServiceRevisionInfo{
		TrafficRevisionShort:       "fullsend-mint-00115-rv1",
		LatestCreatedRevisionShort: "fullsend-mint-00116-qp5",
		LatestReadyRevisionShort:   "fullsend-mint-00116-qp5",
		TrafficAllocType:           "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION",
		TrafficPercent:             100,
		Etag:                       "etag-post",
		TrafficEnvVars:             map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "triage", "ROLE_APP_IDS": `{"triage":"22222"}`},
		TemplateEnvVars:            map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "coder,triage", "ROLE_APP_IDS": `{"coder":"12345","triage":"22222"}`},
	}
	fake.revisionInfoSequence = []*ServiceRevisionInfo{seedRead}
	fake.revisionInfo = postBuild

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoked while the source was building")
	assert.Contains(t, err.Error(), "coder")
	assert.Contains(t, err.Error(), retryDeployAdvice)
	assert.Nil(t, fake.lastUpdateServiceEnvVars, "the revoked role must not be written back")
	assert.Equal(t, 1, countCalls(fake.calls, "PinServiceTraffic"), "only the pre-deploy pin may run")
}

// A configured role that was not registered at seed time is a genuine new
// registration and must still be added by the post-build reconciliation.
func TestProvisioner_Provision_CodeChanged_NewlyConfiguredRoleIsStillRegistered(t *testing.T) {
	fake := newFakeGCFClient()
	p := sourceDeployFixture(t, fake)
	seedRead := &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		LatestReadyRevisionShort: "fullsend-mint-00114-fm9",
		TrafficAllocType:         "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST",
		TemplateMatchesTraffic:   true,
		TrafficPercent:           100,
		Etag:                     "etag-seed",
		TrafficEnvVars:           map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "triage", "ROLE_APP_IDS": `{"triage":"22222"}`},
	}
	postBuild := &ServiceRevisionInfo{
		TrafficRevisionShort:       "fullsend-mint-00114-fm9",
		LatestCreatedRevisionShort: "fullsend-mint-00115-qp5",
		LatestReadyRevisionShort:   "fullsend-mint-00115-qp5",
		TrafficAllocType:           "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION",
		TrafficPercent:             100,
		Etag:                       "etag-post",
		TrafficEnvVars:             map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "triage", "ROLE_APP_IDS": `{"triage":"22222"}`},
		TemplateEnvVars:            map[string]string{"ALLOWED_ORGS": "test-org", "ALLOWED_ROLES": "coder,triage", "ROLE_APP_IDS": `{"coder":"12345","triage":"22222"}`},
	}
	fake.revisionInfoSequence = []*ServiceRevisionInfo{seedRead}
	fake.revisionInfo = postBuild

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	// The template already carries the newly configured role, so the new
	// revision is pinned as-is.
	assert.Equal(t, "fullsend-mint-00115-qp5", fake.lastPinnedRevision)
}

func TestProvisioner_Provision_CodeChanged_UnsettledServingRoutingRefusesDeploy(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:    "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State:   "ACTIVE",
		URI:     "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{"FULLSEND_SOURCE_HASH": "old-hash-that-wont-match"},
	}
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort: "fullsend-mint-00114-fm9",
		TrafficEnvVars:       map[string]string{"ALLOWED_ORGS": "test-org"},
		RoutingUnsettled:     true,
	}
	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has not settled")
	assert.Contains(t, err.Error(), retryDeployAdvice)
	assert.NotContains(t, fake.calls, "UpdateFunction")
}

// An ACTIVE function whose service reports no resolvable serving revision
// cannot be verified, so the source deploy must be refused rather than
// proceeding with the possibly stale Cloud Functions env.
func TestProvisioner_Provision_CodeChanged_NoResolvableServingRevisionRefusesDeploy(t *testing.T) {
	cases := map[string]*ServiceRevisionInfo{
		"empty traffic revision": {TemplateMatchesTraffic: true},
		"unsettled and empty":    {RoutingUnsettled: true},
	}
	for name, info := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGCFClient()
			fake.functionInfo = &FunctionInfo{
				Name:    "projects/my-project/locations/us-central1/functions/fullsend-mint",
				State:   "ACTIVE",
				URI:     "https://fullsend-mint-abc123.run.app",
				EnvVars: map[string]string{"FULLSEND_SOURCE_HASH": "old-hash-that-wont-match"},
			}
			fake.revisionInfo = info
			p := newTestProvisioner(Config{
				ProjectID:         "my-project",
				GitHubOrgs:        []string{"test-org"},
				AgentPEMs:         singleRolePEMs(),
				AgentAppIDs:       singleRoleAppIDs(),
				FunctionSourceDir: fakeFunctionSourceDir(t),
			}, fake)

			_, err := p.Provision(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), retryDeployAdvice)
			assert.NotContains(t, fake.calls, "UploadFunctionSource")
			assert.NotContains(t, fake.calls, "UpdateFunction")
		})
	}
}

func TestProvisioner_Provision_CodeChanged_UnreliableServingEnvRefusesDeploy(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:    "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State:   "ACTIVE",
		URI:     "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{"FULLSEND_SOURCE_HASH": "old-hash-that-wont-match"},
	}
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevisionShort:     "fullsend-mint-00114-fm9",
		TrafficEnvVarsUnreliable: true,
	}
	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before deploying source")
	assert.NotContains(t, fake.calls, "UpdateFunction")
}

// A first-deploy readiness-wait failure must not offer a manual traffic shift:
// another admin may have published a more restrictive revision during the wait.
func TestEnsureTrafficOnLatestRevision_FirstDeployReadinessWaitFailureUsesRetryAdvice(t *testing.T) {
	fake := newFakeGCFClient()
	fake.revisionInfo = &ServiceRevisionInfo{
		LatestCreatedRevisionShort: "fullsend-mint-00115-qp5",
		Etag:                       "etag-first",
	}
	p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)
	p.revisionReadyTimeout = 5 * time.Millisecond

	err := p.ensureTrafficOnLatestRevision(context.Background(), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not become ready")
	assert.Contains(t, err.Error(), retryDeployAdvice)
	assert.NotContains(t, err.Error(), "gcloud run services update-traffic")
	assert.NotContains(t, fake.calls, "PinServiceTraffic")
}

// API-derived revision names are validated before any log or error output.
func TestEnsureTrafficOnLatestRevision_MalformedRevisionNamesAreRejected(t *testing.T) {
	bad := "rev\x1b[31mevil"
	for name, info := range map[string]*ServiceRevisionInfo{
		"target": {
			TrafficRevisionShort:       "fullsend-mint-00114-fm9",
			LatestCreatedRevisionShort: bad,
		},
		"serving": {
			TrafficRevisionShort:     bad,
			LatestReadyRevisionShort: "fullsend-mint-00115-qp5",
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGCFClient()
			fake.revisionInfo = info
			p := newTestProvisioner(Config{ProjectID: "my-project", Region: "us-central1"}, fake)

			err := p.ensureTrafficOnLatestRevision(context.Background(), false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "malformed revision name")
			assert.NotContains(t, err.Error(), "\x1b")
			assert.NotContains(t, fake.calls, "PinServiceTraffic")
			assert.NotContains(t, fake.calls, "PinServiceTrafficIfMatch")
		})
	}
}
