package repos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const dispatcherJobHeader = "\"" + gitlabDispatcherJobName + "\":\n"

// Global execution configuration is inherited by the dispatcher job, so a
// project's global after_script, services or default hooks would run inside
// a bearer-token-triggered dispatcher job. Provisioning defers and an
// existing fast path is revoked, unless the dispatcher disables default
// inheritance.
func TestEnsureGitLabWebhookFastPath_InheritedGlobalExecutionIsNotProvisioned(t *testing.T) {
	cases := map[string]string{
		"global after_script":           "\nafter_script:\n  - ./deploy.sh\n",
		"global services":               "\nservices:\n  - docker:dind\n",
		"default after_script":          "\ndefault:\n  after_script:\n    - ./deploy.sh\n",
		"default services":              "\ndefault:\n  services: [docker:dind]\n",
		"default hooks":                 "\ndefault:\n  hooks:\n    pre_get_sources_script: [./x.sh]\n",
		"default with unresolved merge": "\n.shared: &shared\n  retry: 1\ndefault:\n  <<: *shared\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"] = append(c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"], []byte(extra)...)

			res := ensureWebhook(t, c, false, false)

			assert.Equal(t, "deferred", res.Action)
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Empty(t, c.CreatedProjectHooks)
			assert.Contains(t, strings.Join(res.Details, "\n"), "inherit")
		})
	}

	t.Run("existing fast path is revoked when a global after_script appears", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"] = append(c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"], []byte("\nafter_script:\n  - ./deploy.sh\n")...)

		res, err := ensureWebhookRaw(c)

		require.NoError(t, err)
		assert.Equal(t, "update", res.Action)
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
	})

	t.Run("a global default in the wrapper is also inherited", func(t *testing.T) {
		c := newWebhookFake()
		c.FileContents[triggerJobsPrefix+fullsendPipelineInclude] = append(c.FileContents[triggerJobsPrefix+fullsendPipelineInclude], []byte("\nafter_script:\n  - ./deploy.sh\n")...)

		res := ensureWebhook(t, c, false, false)

		assert.Empty(t, c.CreatedTriggerTokens)
		assert.Contains(t, strings.Join(res.Details, "\n"), "inherit")
	})

	t.Run("dispatcher disabling default inheritance is allowed", func(t *testing.T) {
		c := newWebhookFake()
		path := triggerJobsPrefix + fullsendDispatcherTemplatePath
		template := string(c.FileContents[path])
		require.Contains(t, template, dispatcherJobHeader)
		c.FileContents[path] = []byte(strings.Replace(template, dispatcherJobHeader, dispatcherJobHeader+"  inherit:\n    default: false\n", 1))
		c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"] = append(c.FileContents[triggerJobsPrefix+".gitlab-ci.yml"], []byte("\nafter_script:\n  - ./cleanup.sh\ndefault:\n  services: [docker:dind]\n")...)

		ensureWebhook(t, c, false, false)

		assert.Len(t, c.triggers(), 1)
		assert.Len(t, c.hooks(), 1)
	})
}

// GitLab runs the project's selected CI configuration, which may not be the
// committed .gitlab-ci.yml that every check here reads. A custom path
// defers provisioning and revokes an existing fast path, including after
// the path drifts post-provisioning.
func TestGitLabWebhook_CustomCIConfigPathIsNotTrusted(t *testing.T) {
	setPath := func(c webhookFake, path string) {
		c.Repos[0].CIConfigPath = path
	}

	t.Run("custom path defers provisioning", func(t *testing.T) {
		c := newWebhookFake()
		setPath(c, "ci/other.yml@other/group")

		res := ensureWebhook(t, c, false, false)

		assert.Equal(t, "deferred", res.Action)
		assert.Contains(t, strings.Join(res.Details, "\n"), "custom CI configuration path")
		assert.Empty(t, c.CreatedTriggerTokens)
		assert.Empty(t, c.CreatedProjectHooks)
	})

	t.Run("the standard path is accepted", func(t *testing.T) {
		c := newWebhookFake()
		setPath(c, ".gitlab-ci.yml")

		ensureWebhook(t, c, false, false)

		assert.Len(t, c.triggers(), 1)
	})

	t.Run("drift after provisioning revokes through install and reconciliation", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		setPath(c, "other/ci.yml")

		needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
		require.NoError(t, err)
		assert.True(t, needs)

		res, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)
		require.NoError(t, err)
		assert.Equal(t, "update", res.Action)
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
	})

	t.Run("an unreadable project fails closed", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.Errors = map[string]error{"GetRepo": errors.New("boom")}

		_, _, err := reconcileGitLabTriggerSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, "main", false)

		require.Error(t, err)
		assert.Empty(t, c.triggers())
	})
}

// A dispatcher that GitLab creates as a manual action never dispatches the
// webhook event on its own; readiness must not accept it.
func TestGitLabDispatcherTemplateProblem_ManualWhen(t *testing.T) {
	const trigger = `$CI_PIPELINE_SOURCE == "trigger"`
	base := dispatcherJobHeader + "  stage: dispatch\n  script: [./run.sh]\n"
	cases := map[string]struct {
		body    string
		problem bool
	}{
		"job-level manual":                    {"  when: manual\n", true},
		"job-level on_failure":                {"  when: on_failure\n", true},
		"rule-level on_failure":               {"  rules:\n    - if: '" + trigger + "'\n      when: on_failure\n", true},
		"inherited job on_failure":            {"  when: on_failure\n  rules:\n    - if: '" + trigger + "'\n", true},
		"rule-level manual":                   {"  rules:\n    - if: '" + trigger + "'\n      when: manual\n", true},
		"job-level manual inherited by rule":  {"  when: manual\n  rules:\n    - if: '" + trigger + "'\n", true},
		"rule-level always overrides manual":  {"  when: manual\n  rules:\n    - if: '" + trigger + "'\n      when: always\n", false},
		"rule-level never":                    {"  rules:\n    - if: '" + trigger + "'\n      when: never\n", true},
		"rule-level on_success":               {"  rules:\n    - if: '" + trigger + "'\n      when: on_success\n", false},
		"rule-level non-literal when":         {"  rules:\n    - if: '" + trigger + "'\n      when: $WHEN\n", true},
		"job-level when never, rule always":   {"  when: never\n  rules:\n    - if: '" + trigger + "'\n      when: always\n", false},
		"plain job-level always without rule": {"  when: always\n", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			problem := gitlabDispatcherTemplateProblem([]byte(base+tc.body), "main")
			if tc.problem {
				assert.NotEmpty(t, problem)
			} else {
				assert.Empty(t, problem)
			}
		})
	}

	t.Run("manual dispatcher defers provisioning", func(t *testing.T) {
		c := newWebhookFake()
		c.FileContents[triggerJobsPrefix+fullsendDispatcherTemplatePath] = []byte(base + "  when: manual\n")

		res := ensureWebhook(t, c, false, false)

		assert.Equal(t, "deferred", res.Action)
		assert.Contains(t, strings.Join(res.Details, "\n"), "when: manual")
		assert.Empty(t, c.CreatedTriggerTokens)
	})
}

// After provisioning, the dispatcher can stop admitting trigger pipelines
// while credentials and hook configuration stay intact. The convergence
// probe must report that so the CLI runs the post-install step and surfaces
// the deferral.
func TestGitLabWebhookNeedsWork_DispatcherAdmissionDriftAfterProvisioning(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	require.False(t, gitlabWebhookNeedsWork(context.Background(), c, webhookTestOwner, webhookTestRepo))

	c.FileContents[triggerJobsPrefix+fullsendDispatcherTemplatePath] = []byte(dispatcherJobHeader +
		"  stage: dispatch\n  script: [./run.sh]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"schedule\"'\n")

	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.True(t, needs)
	assert.True(t, gitlabWebhookNeedsWork(context.Background(), c, webhookTestOwner, webhookTestRepo))

	res, err := ensureWebhookRaw(c)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.Contains(t, strings.Join(res.Details, "\n"), "dispatcher template")
}

// A probe failure while re-checking committed readiness also reports work.
func TestGitLabWebhookNeedsWork_ReadinessProbeFailure(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	c.Errors = map[string]error{"GetFileContent": errors.New("read failed")}

	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)

	assert.True(t, needs)
	assert.Error(t, err)
}

// A managed trigger whose stored credential is unmasked, unprotected or
// unverifiable is revoked before any readiness deferral or probe failure can
// return early, independently of whether replacement provisioning can run.
func TestEnsureGitLabWebhookFastPath_UnsafeStoredTokenRevokedBeforeReadinessDeferral(t *testing.T) {
	key := webhookTestOwner + "/" + webhookTestRepo
	missingScript := func(c webhookFake) {
		delete(c.FileContents, triggerJobsPrefix+gitlabDispatcherJobScriptPath)
	}

	t.Run("unprotected token with a missing dispatcher script", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		c.SecretProtections[key+"/"+forge.SecretTriggerToken] = forge.SecretProtection{Exists: true, Masked: true}
		missingScript(c)

		res, err := ensureWebhookRaw(c)

		require.NoError(t, err)
		assert.Equal(t, "deferred", res.Action)
		assert.Empty(t, c.triggers(), "the exposed bearer token is revoked despite the deferral")
		joined := strings.Join(res.Details, "\n")
		assert.Contains(t, joined, "Revoked pipeline trigger token (ID 1)")
		assert.Contains(t, joined, gitlabDispatcherJobScriptPath)
	})

	t.Run("unmasked token with a missing dispatcher script", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.SecretProtections[key+"/"+forge.SecretTriggerToken] = forge.SecretProtection{Exists: true, Protected: true}
		missingScript(c)

		_, err := ensureWebhookRaw(c)

		require.NoError(t, err)
		assert.Empty(t, c.triggers())
	})

	t.Run("protection lookup failure with a missing dispatcher script", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		token := c.variable(forge.SecretTriggerToken)
		missingScript(c)
		c.Errors = map[string]error{"GetRepoSecretProtection": errors.New("lookup failed: " + token)}

		_, err := ensureWebhookRaw(c)

		require.Error(t, err)
		assert.NotContains(t, err.Error(), token)
		assert.Empty(t, c.triggers(), "an unverifiable credential is revoked")
	})

	t.Run("revocation-only reconciliation revokes an unsafe token", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		c.SecretProtections[key+"/"+forge.SecretTriggerToken] = forge.SecretProtection{Exists: true, Masked: true}

		res, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

		require.NoError(t, err)
		assert.Equal(t, "update", res.Action)
		assert.Empty(t, c.triggers())
		assert.Contains(t, strings.Join(res.Details, "\n"), "Revoked pipeline trigger token (ID 1)")
	})

	t.Run("dry run reports without revoking", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.SecretProtections[key+"/"+forge.SecretTriggerToken] = forge.SecretProtection{Exists: true, Masked: true}
		missingScript(c)

		res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, true)

		require.NoError(t, err)
		assert.Len(t, c.triggers(), 1)
		assert.Contains(t, strings.Join(res.Details, "\n"), "Would revoke pipeline trigger token (ID 1)")
	})

	t.Run("a safe token is left alone", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		missingScript(c)

		res, err := ensureWebhookRaw(c)

		require.NoError(t, err)
		assert.Equal(t, "deferred", res.Action)
		assert.Len(t, c.triggers(), 1)
	})
}
