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

// groupScopedFake answers ListOrgVariables per group: forbidden for the
// groups in forbidden, and the listed variable names for the groups in vars.
type groupScopedFake struct {
	webhookFake
	forbidden map[string]bool
	vars      map[string][]string
}

func (c groupScopedFake) ListOrgVariables(_ context.Context, group string) ([]forge.OrgVariable, error) {
	if c.forbidden[group] {
		return nil, fmt.Errorf("listing group variables: %w", forge.ErrForbidden)
	}
	var out []forge.OrgVariable
	for _, name := range c.vars[group] {
		out = append(out, forge.OrgVariable{Name: name})
	}
	return out, nil
}

// forbidInheritedScopes makes the instance and every group variable listing
// answer forbidden, as they do for a project Maintainer without
// administrator or group Owner access.
func forbidInheritedScopes(c webhookFake) {
	c.Errors["ListInstanceVariables"] = fmt.Errorf("list instance variables: %w", forge.ErrForbidden)
	c.Errors["ListOrgVariables"] = fmt.Errorf("listing group variables: %w", forge.ErrForbidden)
}

// A project Maintainer without administrator or group Owner access cannot
// list instance or group variables. Those scopes are unverified, not
// unsafe: the fast path is activated and retained when the project-level
// and committed-CI checks pass, with no acceptance setting.
func TestEnsureGitLabWebhookFastPath_UnreadableInheritedScopes(t *testing.T) {
	t.Run("fresh install activates the fast path", func(t *testing.T) {
		c := newWebhookFake()
		forbidInheritedScopes(c)

		res := ensureWebhook(t, c, false, false)

		assert.Equal(t, "update", res.Action)
		assert.Len(t, c.triggers(), 1)
		assert.Len(t, c.hooks(), 1)
	})

	t.Run("status reports no drift", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		forbidInheritedScopes(c)

		needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)

		require.NoError(t, err)
		assert.False(t, needs, "an unreadable inherited scope is not drift")
	})

	t.Run("existing fast path is retained on reinstall", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		before := c.triggers()
		require.Len(t, before, 1)
		forbidInheritedScopes(c)

		_, err := ensureWebhookRaw(c)

		require.NoError(t, err)
		assert.Equal(t, before, c.triggers(), "the managed trigger token is not revoked or replaced")
		assert.Len(t, c.hooks(), 1)
	})

	t.Run("safety reconciliation does not revoke", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		before := c.triggers()
		require.Len(t, before, 1)
		forbidInheritedScopes(c)

		res, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

		require.NoError(t, err)
		assert.NotEqual(t, "deferred", res.Action)
		assert.Equal(t, before, c.triggers())
		assert.Len(t, c.hooks(), 1)
	})

	t.Run("project variable collision still defers", func(t *testing.T) {
		c := newWebhookFake()
		forbidInheritedScopes(c)
		c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/CI_PIPELINE_SOURCE"] = "push"

		res := ensureWebhook(t, c, false, false)

		assert.Equal(t, "deferred", res.Action)
		assert.Empty(t, c.CreatedTriggerTokens)
		assert.Contains(t, strings.Join(res.Details, "\n"), "predefined project variable override: CI_PIPELINE_SOURCE")
	})

	t.Run("readable group override defers when instance is unreadable", func(t *testing.T) {
		c := newWebhookFake()
		c.Errors["ListInstanceVariables"] = fmt.Errorf("list instance variables: %w", forge.ErrForbidden)
		c.OrgVariables = map[string]bool{webhookTestOwner + "/CI_PIPELINE_SOURCE": true}

		res := ensureWebhook(t, c, false, false)

		assert.Equal(t, "deferred", res.Action)
		assert.Empty(t, c.CreatedTriggerTokens)
		assert.Contains(t, strings.Join(res.Details, "\n"), "predefined group variable override: CI_PIPELINE_SOURCE")
	})

	t.Run("readable instance override revokes when groups are unreadable", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		c.Errors["ListOrgVariables"] = fmt.Errorf("listing group variables: %w", forge.ErrForbidden)
		c.InstanceVariables = []string{"CI_COMMIT_REF_PROTECTED"}

		res, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

		require.NoError(t, err)
		assert.Equal(t, "update", res.Action)
		assert.Contains(t, strings.Join(res.Details, "\n"), "predefined instance variable override: CI_COMMIT_REF_PROTECTED")
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
	})

	t.Run("readable override in a later group is found past an unreadable one", func(t *testing.T) {
		c := groupScopedFake{
			webhookFake: newWebhookFake(),
			forbidden:   map[string]bool{webhookTestOwner: true},
			vars:        map[string][]string{webhookTestOwner + "/sub": {"CI_COMMIT_REF_PROTECTED"}},
		}

		problem, err := gitlabInheritedVariableOverrideProblem(context.Background(), c, webhookTestOwner, "sub/"+webhookTestRepo, triggerPipelineEnv("main"))

		require.NoError(t, err)
		assert.Equal(t, "webhook fast-path deferred: predefined group variable override: CI_COMMIT_REF_PROTECTED", problem)
	})

	t.Run("unreadable nested group is skipped", func(t *testing.T) {
		c := groupScopedFake{
			webhookFake: newWebhookFake(),
			forbidden:   map[string]bool{webhookTestOwner + "/sub": true},
			vars:        map[string][]string{webhookTestOwner: {"UNRELATED"}},
		}

		problem, err := gitlabInheritedVariableOverrideProblem(context.Background(), c, webhookTestOwner, "sub/"+webhookTestRepo, triggerPipelineEnv("main"))

		require.NoError(t, err)
		assert.Empty(t, problem)
	})
}

// Errors other than forbidden mean the inherited scopes could not be
// checked at all: they fail rather than counting as verified or as
// unverified, and never echo server error text.
func TestGitLabInheritedVariableOverride_NonForbiddenErrorsFail(t *testing.T) {
	const echoed = "echoed-credential-value"
	for _, method := range []string{"ListInstanceVariables", "ListOrgVariables"} {
		t.Run(method+" on install", func(t *testing.T) {
			c := newWebhookFake()
			c.Errors[method] = errors.New("server said " + echoed)

			res, err := ensureWebhookRaw(c)

			require.Error(t, err)
			assert.NotContains(t, err.Error(), echoed)
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.NotEqual(t, "update", res.Action)
		})

		t.Run(method+" on status", func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			c.Errors[method] = errors.New("server said " + echoed)

			needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)

			require.Error(t, err)
			assert.True(t, needs)
			assert.NotContains(t, err.Error(), echoed)
		})

		t.Run(method+" on reconcile revokes", func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			require.Len(t, c.triggers(), 1)
			c.Errors[method] = errors.New("server said " + echoed)

			_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

			require.Error(t, err)
			assert.NotContains(t, err.Error(), echoed)
			assert.Empty(t, c.triggers())
		})
	}
}
