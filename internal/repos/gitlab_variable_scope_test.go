package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// An instance-level CI/CD variable can redefine a predefined variable the
// evaluator treats as fixed, so provisioning defers and an existing fast
// path is revoked.
func TestEnsureGitLabWebhookFastPath_InstanceVariableOverride(t *testing.T) {
	t.Run("fresh install defers", func(t *testing.T) {
		c := newWebhookFake()
		c.InstanceVariables = []string{"UNRELATED", "CI_PIPELINE_SOURCE"}

		res := ensureWebhook(t, c, false, false)

		assert.Equal(t, "deferred", res.Action)
		assert.Empty(t, c.CreatedTriggerTokens)
		assert.Empty(t, c.CreatedProjectHooks)
		assert.Contains(t, strings.Join(res.Details, "\n"), "predefined instance variable override: CI_PIPELINE_SOURCE")
	})

	t.Run("existing fast path is revoked", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		c.InstanceVariables = []string{"CI_COMMIT_REF_PROTECTED"}

		res, err := ensureWebhookRaw(c)

		require.NoError(t, err)
		assert.Equal(t, "update", res.Action)
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
	})

	t.Run("unrelated instance variables do not defer", func(t *testing.T) {
		c := newWebhookFake()
		c.InstanceVariables = []string{"UNRELATED"}

		ensureWebhook(t, c, false, false)

		assert.Len(t, c.triggers(), 1)
	})

	t.Run("uninspectable instance variables defer and revoke", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		c.Errors["ListInstanceVariables"] = fmt.Errorf("list instance variables: %w", forge.ErrForbidden)

		res, err := ensureWebhookRaw(c)

		require.NoError(t, err)
		assert.Equal(t, "update", res.Action)
		assert.Contains(t, strings.Join(res.Details, "\n"), "instance-level CI/CD variables cannot be inspected")
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
	})
}

// A project Maintainer cannot list group variables (group Owner access is
// required). That defers the fast path rather than failing the install,
// and still revokes an existing managed credential.
func TestEnsureGitLabWebhookFastPath_GroupVariablesForbiddenDefers(t *testing.T) {
	t.Run("fresh install defers without an error", func(t *testing.T) {
		c := newWebhookFake()
		c.Errors["ListOrgVariables"] = fmt.Errorf("listing group variables: %w", forge.ErrForbidden)

		res, err := ensureWebhookRaw(c)

		require.NoError(t, err)
		assert.Equal(t, "deferred", res.Action)
		assert.Empty(t, c.CreatedTriggerTokens)
		assert.Contains(t, strings.Join(res.Details, "\n"), "group Owner access")
	})

	t.Run("existing fast path is revoked", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		c.Errors["ListOrgVariables"] = fmt.Errorf("listing group variables: %w", forge.ErrForbidden)

		res, err := ensureWebhookRaw(c)

		require.NoError(t, err)
		assert.Equal(t, "update", res.Action)
		assert.Empty(t, c.triggers())
	})

	t.Run("other group errors still fail", func(t *testing.T) {
		c := newWebhookFake()
		c.Errors["ListOrgVariables"] = errors.New("boom")

		_, err := ensureWebhookRaw(c)

		require.Error(t, err)
	})
}

// A same-named dispatcher job in the root or wrapper is merged over the
// included template's job, so the template-only readiness check cannot
// speak for the job that runs.
func TestGitLabWebhookReadiness_DispatcherJobOverride(t *testing.T) {
	override := "\n" + dispatcherJobHeader + "  rules:\n    - when: never\n"
	for name, path := range map[string]string{
		"root":    ".gitlab-ci.yml",
		"wrapper": fullsendPipelineInclude,
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			key := triggerJobsPrefix + path
			c.FileContents[key] = append(c.FileContents[key], []byte(override)...)

			res := ensureWebhook(t, c, false, false)

			assert.Equal(t, "deferred", res.Action)
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Contains(t, strings.Join(res.Details, "\n"), "override the dispatcher template")
		})
	}

	t.Run("convergence reports work after the override appears", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		key := triggerJobsPrefix + ".gitlab-ci.yml"
		c.FileContents[key] = append(c.FileContents[key], []byte(override)...)

		needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)

		require.NoError(t, err)
		assert.True(t, needs)
	})
}

// Early safety and project-lookup errors never echo server error text,
// which could contain a stored credential.
func TestGitLabTriggerSafety_EarlyErrorsWithholdServerText(t *testing.T) {
	const echoed = "echoed-credential-value"
	cases := map[string]struct {
		method string
		run    func(c webhookFake) error
	}{
		"override role on install": {"GetPipelineVariablesMinimumOverrideRole", func(c webhookFake) error {
			_, err := ensureWebhookRaw(c)
			return err
		}},
		"override role on reconcile": {"GetPipelineVariablesMinimumOverrideRole", func(c webhookFake) error {
			_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)
			return err
		}},
		"project lookup on install": {"GetRepo", func(c webhookFake) error {
			_, err := ensureWebhookRaw(c)
			return err
		}},
		"project lookup on reconcile": {"GetRepo", func(c webhookFake) error {
			_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)
			return err
		}},
		"trigger owner lookup on reconcile": {"GetProjectMemberAccessLevel", func(c webhookFake) error {
			_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)
			return err
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			injected := errors.New("server said " + echoed)
			c.Errors[tc.method] = injected

			err := tc.run(c)

			require.Error(t, err)
			assert.NotContains(t, err.Error(), echoed)
			assert.ErrorIs(t, err, injected, "the original error stays reachable for errors.Is")
		})
	}
}
