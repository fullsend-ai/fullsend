package repos

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GitLab applies the last occurrence of a duplicated mapping key, while the
// readiness and safety lookups would select the first. A configuration with
// duplicate keys is therefore never trusted: provisioning defers and an
// existing fast path is revoked.
func TestEnsureGitLabWebhookFastPath_DuplicateMappingKeysAreNotTrusted(t *testing.T) {
	rootPath := triggerJobsPrefix + ".gitlab-ci.yml"
	cases := map[string]string{
		"duplicate job rules":        "\nextra:\n  script: [true]\n  rules:\n    - when: never\n  rules:\n    - when: on_success\n",
		"duplicate rule when":        "\nextra:\n  script: [true]\n  rules:\n    - if: '" + `$CI_PIPELINE_SOURCE == "trigger"` + "'\n      when: never\n      when: on_success\n",
		"duplicate rule if":          "\nextra:\n  script: [true]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"trigger\"'\n      if: '$CI_PIPELINE_SOURCE == \"push\"'\n      when: never\n",
		"duplicate include location": "\nextra:\n  script: [true]\n  rules:\n    - when: never\n.tpl:\n  include:\n    local: a.yml\n    local: b.yml\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			t.Run("provisioning defers", func(t *testing.T) {
				c := newWebhookFake()
				c.FileContents[rootPath] = append(c.FileContents[rootPath], []byte(extra)...)

				res := ensureWebhook(t, c, false, false)

				assert.Equal(t, "deferred", res.Action)
				assert.Empty(t, c.CreatedTriggerTokens)
				assert.Empty(t, c.CreatedProjectHooks)
				assert.Contains(t, strings.Join(res.Details, "\n"), "duplicate key")
			})

			t.Run("existing fast path is revoked", func(t *testing.T) {
				c := newWebhookFake()
				ensureWebhook(t, c, false, false)
				require.Len(t, c.triggers(), 1)
				c.FileContents[rootPath] = append(c.FileContents[rootPath], []byte(extra)...)

				needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
				require.NoError(t, err)
				assert.True(t, needs)

				res, err := ensureWebhookRaw(c)

				require.NoError(t, err)
				assert.Equal(t, "update", res.Action)
				assert.Empty(t, c.triggers())
				assert.Empty(t, c.hooks())
			})
		})
	}
}

func TestDuplicateMappingKeyProblem_ReadinessDiagnostics(t *testing.T) {
	assert.Contains(t, gitlabRootCIDispatcherProblem([]byte("stages: [a]\nstages: [dispatch]\n"), "main"), "duplicate")
	assert.Contains(t, gitlabWrapperDispatcherProblem([]byte("include: []\ninclude: []\n"), "main"), "duplicate")
	assert.Contains(t, gitlabDispatcherTemplateProblem([]byte("a: 1\na: 2\n"), "main"), "duplicate")
}

// A variable whose effective name cannot be established from its key (an
// interpolated or aliased name) could resolve to a predefined variable such
// as CI_PIPELINE_SOURCE, which changes how trigger exclusion is evaluated.
func TestEnsureGitLabWebhookFastPath_UnverifiableVariableNamesAreNotTrusted(t *testing.T) {
	rootPath := triggerJobsPrefix + ".gitlab-ci.yml"
	cases := map[string]string{
		"interpolated variable name": "\nextra:\n  script: [true]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n  variables:\n    $[[ inputs.variable_name ]]: push\n",
		"aliased variable name":      "\n.name: &name CI_PIPELINE_SOURCE\nextra:\n  script: [true]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n  variables:\n    *name : push\n",
		"tagged variable name":       "\nextra:\n  script: [true]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n  variables:\n    !custom CI_PIPELINE_SOURCE: push\n",
		"interpolated section key":   "\nextra:\n  script: [true]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n  $[[ inputs.section ]]:\n    CI_PIPELINE_SOURCE: push\n",
		"reserved variable name":     "\nextra:\n  script: [true]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n  variables:\n    CI_PIPELINE_SOURCE: push\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			t.Run("provisioning defers", func(t *testing.T) {
				c := newWebhookFake()
				c.FileContents[rootPath] = append(c.FileContents[rootPath], []byte(extra)...)

				res := ensureWebhook(t, c, false, false)

				assert.Equal(t, "deferred", res.Action)
				assert.Empty(t, c.CreatedTriggerTokens)
				assert.Empty(t, c.CreatedProjectHooks)
			})

			t.Run("existing fast path is revoked", func(t *testing.T) {
				c := newWebhookFake()
				ensureWebhook(t, c, false, false)
				require.Len(t, c.triggers(), 1)
				c.FileContents[rootPath] = append(c.FileContents[rootPath], []byte(extra)...)

				res, err := ensureWebhookRaw(c)

				require.NoError(t, err)
				assert.Equal(t, "update", res.Action)
				assert.Empty(t, c.triggers())
				assert.Empty(t, c.hooks())
			})
		})
	}
}

// GitLab permanently disables a webhook after repeated delivery failures.
// The hook stays correctly configured but never fires, so convergence must
// report work and repair it by recreating the owned hook.
func TestEnsureGitLabWebhookFastPath_PermanentlyDisabledHookIsRepaired(t *testing.T) {
	key := webhookTestOwner + "/" + webhookTestRepo
	disable := func(c webhookFake) {
		c.ProjectHooks[key][0].AlertStatus = "disabled"
	}

	t.Run("detected by convergence", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		disable(c)

		needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)

		require.NoError(t, err)
		assert.True(t, needs)
	})

	t.Run("repaired by recreating the hook", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		oldID := c.hooks()[0].ID
		triggerID := c.triggers()[0].ID
		disable(c)

		res := ensureWebhook(t, c, false, false)

		assert.Equal(t, "update", res.Action)
		assert.Equal(t, []int64{oldID}, c.DeletedProjectHookIDs)
		require.Len(t, c.hooks(), 1)
		assert.NotEqual(t, oldID, c.hooks()[0].ID)
		assert.False(t, c.hooks()[0].HookDeliveryDisabled())
		assert.Contains(t, strings.Join(res.Details, "\n"), "Recreated project webhook")
		// The working trigger token is kept; only the hook is replaced.
		require.Len(t, c.triggers(), 1)
		assert.Equal(t, triggerID, c.triggers()[0].ID)
		assert.Empty(t, c.RevokedTriggerTokenIDs)
		assertNoCredentialLeak(t, c, res)

		needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
		require.NoError(t, err)
		assert.False(t, needs)
	})

	t.Run("dry run makes no changes", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		disable(c)

		res := ensureWebhook(t, c, false, true)

		assert.Equal(t, "would-update", res.Action)
		assert.Contains(t, strings.Join(res.Details, "\n"), "Would recreate project webhook")
		assert.Empty(t, c.DeletedProjectHookIDs)
		assert.Len(t, c.CreatedProjectHooks, 1)
	})

	t.Run("a temporarily disabled hook is left alone", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.ProjectHooks[key][0].AlertStatus = "temporarily_disabled"
		c.ProjectHooks[key][0].DisabledUntil = "2099-01-01T00:00:00Z"

		needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)

		require.NoError(t, err)
		assert.False(t, needs)
	})
}
