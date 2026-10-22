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

// The webhook description is editable and GitLab never returns trigger token
// values on list, so a stored bearer that is an unmanaged Maintainer-owned
// token must not be accepted just because the description names a live,
// Developer-owned managed trigger.
func TestGitLabWebhookFastPath_UnmanagedPrivilegedBearerNotReused(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	ensureWebhook(t, c, false, false)
	require.Len(t, c.triggers(), 1)
	require.Len(t, c.hooks(), 1)
	managedID := c.triggers()[0].ID
	assert.Contains(t, c.hooks()[0].Description, "trigger token ID")

	// Drift: an unmanaged Maintainer-owned token is now both the stored
	// variable and the webhook URL token, while the description still names
	// the Developer-owned managed trigger.
	const unmanagedToken = "glptt-unmanaged-maintainer"
	key := webhookTestOwner + "/" + webhookTestRepo
	c.ProjectMemberAccess[webhookMaintainerUserID] = forge.GitLabAccessLevelMaintainer
	c.PipelineTriggerTokens[key] = append(c.PipelineTriggerTokens[key], forge.PipelineTriggerToken{
		ID: 777, Description: "ci deploy", OwnerID: webhookMaintainerUserID,
	})
	c.VariableValues[key+"/"+forge.SecretTriggerToken] = unmanagedToken
	hooks := c.ProjectHooks[key]
	hooks[0].URL = GitLabWebhookTriggerURL(webhookTestBase, 42, "main", unmanagedToken)
	c.ProjectHooks[key] = hooks

	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.True(t, needs, "the unbound bearer must not count as provisioned")

	// Convergence with a Maintainer-backed installation credential cannot
	// mint a compliant replacement, so the managed fast path is disabled
	// rather than trusting the description.
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.Empty(t, c.hooks(), "the managed webhook is removed")
	assert.Contains(t, c.RevokedTriggerTokenIDs, managedID)
	for _, tok := range c.triggers() {
		assert.NotEqual(t, GitLabWebhookTriggerDescription, tok.Description, "no managed trigger survives")
	}
	assert.Empty(t, c.variable(forge.SecretTriggerToken), "the unbound privileged bearer is removed from runtime credential storage")
	assertUnmanagedTriggerPreserved(t, c)
	assertNoCredentialLeak(t, c, res)
}

// unboundPrivilegedBearerFake returns a provisioned fast path drifted so the
// stored FULLSEND_TRIGGER_TOKEN, the webhook URL token, and an unmanaged
// Maintainer-owned trigger could all be one privileged bearer.
func unboundPrivilegedBearerFake(t *testing.T) (webhookFake, int64) {
	t.Helper()
	c := newWebhookFake()
	ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	ensureWebhook(t, c, false, false)
	require.Len(t, c.triggers(), 1)
	require.Len(t, c.hooks(), 1)
	managedID := c.triggers()[0].ID

	const unmanagedBearer = "unmanaged-privileged-bearer"
	key := webhookTestOwner + "/" + webhookTestRepo
	c.ProjectMemberAccess[webhookMaintainerUserID] = forge.GitLabAccessLevelMaintainer
	c.PipelineTriggerTokens[key] = append(c.PipelineTriggerTokens[key], forge.PipelineTriggerToken{
		ID: 777, Description: "ci deploy", OwnerID: webhookMaintainerUserID,
	})
	c.VariableValues[key+"/"+forge.SecretTriggerToken] = unmanagedBearer
	hooks := c.ProjectHooks[key]
	hooks[0].URL = GitLabWebhookTriggerURL(webhookTestBase, 42, "main", unmanagedBearer)
	c.ProjectHooks[key] = hooks
	return c, managedID
}

// assertUnmanagedTriggerPreserved checks that the unrelated trigger was
// neither revoked nor removed: it is not Fullsend's to revoke.
func assertUnmanagedTriggerPreserved(t *testing.T, c webhookFake) {
	t.Helper()
	assert.NotContains(t, c.RevokedTriggerTokenIDs, int64(777))
	var found bool
	for _, tok := range c.triggers() {
		if tok.ID == 777 {
			found = true
		}
	}
	assert.True(t, found, "the unmanaged trigger is preserved")
}

// Both exported reconciliation entry points, and a readiness deferral, must
// remove the unbound privileged bearer without provisioning anything.
func TestGitLabWebhookSafety_UnboundPrivilegedBearerRemoved(t *testing.T) {
	ctx := context.Background()
	t.Run("ReconcileGitLabWebhookSafety", func(t *testing.T) {
		c, managedID := unboundPrivilegedBearerFake(t)
		res, err := ReconcileGitLabWebhookSafety(ctx, c, webhookTestOwner, webhookTestRepo, false)
		require.NoError(t, err)
		assert.Equal(t, "update", res.Action)
		assert.Empty(t, c.variable(forge.SecretTriggerToken))
		assert.Empty(t, c.hooks())
		assert.Contains(t, c.RevokedTriggerTokenIDs, managedID)
		assertUnmanagedTriggerPreserved(t, c)
		assert.Len(t, c.CreatedTriggerTokens, 1, "reconciliation never provisions a replacement")
		assertNoCredentialLeak(t, c, res)
	})
	t.Run("ReconcileGitLabWebhookSafety dry run", func(t *testing.T) {
		c, _ := unboundPrivilegedBearerFake(t)
		before := c.variable(forge.SecretTriggerToken)
		res, err := ReconcileGitLabWebhookSafety(ctx, c, webhookTestOwner, webhookTestRepo, true)
		require.NoError(t, err)
		assert.Contains(t, strings.Join(res.Details, "\n"), "Would delete the "+forge.SecretTriggerToken+" variable")
		assert.Equal(t, before, c.variable(forge.SecretTriggerToken), "a dry run changes nothing")
		assert.Len(t, c.hooks(), 1)
	})
	t.Run("EnsureGitLabWebhookFastPath readiness deferral", func(t *testing.T) {
		c, managedID := unboundPrivilegedBearerFake(t)
		// The project stops being ready, so provisioning defers before it
		// reaches the binding probe.
		delete(c.FileContents, webhookTestOwner+"/"+webhookTestRepo+"/"+fullsendDispatcherTemplatePath)
		res, err := EnsureGitLabWebhookFastPath(ctx, c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
		require.NoError(t, err)
		assert.Equal(t, "deferred", res.Action)
		assert.Empty(t, c.variable(forge.SecretTriggerToken), "the bearer is removed even though readiness defers")
		assert.Empty(t, c.hooks())
		assert.Contains(t, c.RevokedTriggerTokenIDs, managedID)
		assertUnmanagedTriggerPreserved(t, c)
		assert.Len(t, c.CreatedTriggerTokens, 1, "no replacement is minted while deferred")
		assertNoCredentialLeak(t, c, res)
	})
	t.Run("variable deletion failure is reported after the webhook is removed", func(t *testing.T) {
		c, _ := unboundPrivilegedBearerFake(t)
		c.Errors = map[string]error{"DeleteRepoSecret": errors.New("forbidden")}
		_, err := ReconcileGitLabWebhookSafety(ctx, c, webhookTestOwner, webhookTestRepo, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deleting "+forge.SecretTriggerToken)
		assert.Empty(t, c.hooks(), "the managed webhook is removed before the variable deletion is attempted")
	})
	t.Run("unverifiable trigger list fails closed", func(t *testing.T) {
		c, _ := unboundPrivilegedBearerFake(t)
		c.Errors = map[string]error{"ListPipelineTriggerTokens": errors.New("boom")}
		_, err := ReconcileGitLabWebhookSafety(ctx, c, webhookTestOwner, webhookTestRepo, false)
		require.Error(t, err)
		assert.Empty(t, c.variable(forge.SecretTriggerToken), "an unbounded bearer is removed when its privilege cannot be verified")
		assert.Empty(t, c.hooks())
	})
	// Binding is evaluated independently of the variable's protection: an
	// unprotected, unmasked, or unverifiable variable that may hold an unmanaged
	// Maintainer-owned bearer must be removed, not just have the managed
	// triggers revoked.
	key := webhookTestOwner + "/" + webhookTestRepo + "/" + forge.SecretTriggerToken
	for _, tc := range []struct {
		name  string
		setup func(c webhookFake)
	}{
		{"unprotected variable", func(c webhookFake) {
			c.SecretProtections[key] = forge.SecretProtection{Exists: true, Masked: true, Protected: false}
		}},
		{"unmasked variable", func(c webhookFake) {
			c.SecretProtections[key] = forge.SecretProtection{Exists: true, Masked: false, Protected: true}
		}},
		{"protection lookup failure", func(c webhookFake) {
			c.Errors = map[string]error{"GetRepoSecretProtection": errors.New("boom")}
		}},
	} {
		t.Run("unbound bearer removed with "+tc.name, func(t *testing.T) {
			c, managedID := unboundPrivilegedBearerFake(t)
			tc.setup(c)
			_, err := ReconcileGitLabWebhookSafety(ctx, c, webhookTestOwner, webhookTestRepo, false)
			if tc.name == "protection lookup failure" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Empty(t, c.variable(forge.SecretTriggerToken), "the stored bearer is removed")
			assert.Empty(t, c.hooks())
			assert.Contains(t, c.RevokedTriggerTokenIDs, managedID)
			assertUnmanagedTriggerPreserved(t, c)
		})
		t.Run("unbound bearer removed with "+tc.name+" and readiness deferral", func(t *testing.T) {
			c, _ := unboundPrivilegedBearerFake(t)
			tc.setup(c)
			delete(c.FileContents, webhookTestOwner+"/"+webhookTestRepo+"/"+fullsendDispatcherTemplatePath)
			res, _ := EnsureGitLabWebhookFastPath(ctx, c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
			assert.Equal(t, "deferred", res.Action)
			assert.Empty(t, c.variable(forge.SecretTriggerToken), "the bearer is removed even though readiness defers")
			assertUnmanagedTriggerPreserved(t, c)
		})
	}
	t.Run("no unmanaged privileged trigger keeps a compliant bearer", func(t *testing.T) {
		c := newWebhookFake()
		ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
		ensureWebhook(t, c, false, false)
		before := c.variable(forge.SecretTriggerToken)
		res, err := ReconcileGitLabWebhookSafety(ctx, c, webhookTestOwner, webhookTestRepo, false)
		require.NoError(t, err)
		assert.Equal(t, "none", res.Action)
		assert.Equal(t, before, c.variable(forge.SecretTriggerToken))
		assert.Len(t, c.hooks(), 1)
	})
}

// A stale duplicate managed hook ahead of the correctly configured one must
// not hide the corroborating hook: the matching hook is kept, the duplicate
// deleted, and no replacement is minted.
func TestEnsureGitLabWebhookFastPath_StaleDuplicateHookBeforeValidHook(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	ensureWebhook(t, c, false, false)
	require.Len(t, c.hooks(), 1)
	valid := c.hooks()[0]
	existingToken := c.variable(forge.SecretTriggerToken)
	activeID := c.triggers()[0].ID

	key := webhookTestOwner + "/" + webhookTestRepo
	stale := valid
	stale.ID = valid.ID + 100
	stale.URL = GitLabWebhookTriggerURL(webhookTestBase, 42, "main", "glptt-stale")
	stale.Description = gitlabWebhookDescription
	c.ProjectHooks[key] = []forge.ProjectHook{stale, valid}

	// The installation credential is Maintainer-backed: a replacement mint
	// would be rejected and tear down the valid fast path.
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

	require.NoError(t, err)
	assert.Equal(t, "update", res.Action)
	assert.Len(t, c.CreatedTriggerTokens, 1, "no replacement token is minted")
	assert.Empty(t, c.RevokedTriggerTokenIDs, "the valid trigger is not revoked")
	assert.Equal(t, []int64{stale.ID}, c.DeletedProjectHookIDs)
	require.Len(t, c.hooks(), 1)
	assert.Equal(t, valid.ID, c.hooks()[0].ID)
	assert.Equal(t, existingToken, c.variable(forge.SecretTriggerToken))
	require.Len(t, c.triggers(), 1)
	assert.Equal(t, activeID, c.triggers()[0].ID)
	assertNoCredentialLeak(t, c, res)
}

func TestTriggerDispatcherExecProblem(t *testing.T) {
	const head = "\"" + gitlabDispatcherJobName + "\":\n  stage: dispatch\n  script: [echo]\n"
	for _, tc := range []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"clean job", head, ""},
		{"job-level after_script", head + "  after_script: [deploy]\n", "after_script"},
		{"job-level services", head + "  services: [postgres]\n", "services"},
		{"job-level hooks", head + "  hooks:\n    pre_get_sources_script: [deploy]\n", "hooks"},
		{"after_script with inherit default false", head + "  inherit:\n    default: false\n  after_script: [deploy]\n", "after_script"},
		{"explicitly empty after_script", head + "  after_script: []\n", ""},
		{"merge-derived after_script", ".base: &base\n  after_script: [deploy]\n" + "\"" + gitlabDispatcherJobName + "\":\n  <<: *base\n  stage: dispatch\n  script: [echo]\n", "after_script"},
		{"aliased after_script", ".steps: &steps [deploy]\n" + head + "  after_script: *steps\n", "after_script"},
		{"extends", head + "  extends: .other\n", "extends"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := triggerDispatcherExecProblem([]byte(tc.yaml), "the committed dispatcher")
			if tc.wantErr == "" {
				assert.Empty(t, got)
				return
			}
			assert.Contains(t, got, tc.wantErr)
		})
	}
}

// The dispatcher job's own after_script, services and hooks run outside the
// dispatcher's authorization and secret unsets, so provisioning defers and an
// existing managed fast path is revoked, including with inherit: default:
// false, which does not govern job-level keys.
func TestGitLabWebhookSafety_DispatcherJobLevelExecution(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	for _, extra := range []string{
		"  after_script: [deploy]\n",
		"  services: [postgres]\n",
		"  hooks:\n    pre_get_sources_script: [deploy]\n",
	} {
		key := strings.Fields(extra)[0]
		template := "\"" + gitlabDispatcherJobName + "\":\n  stage: dispatch\n  inherit:\n    default: false\n  script: [echo]\n" + extra

		t.Run("provisioning defers "+key, func(t *testing.T) {
			c := newWebhookFake()
			c.FileContents[prefix+fullsendDispatcherTemplatePath] = []byte(template)

			res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)

			require.NoError(t, err)
			assert.Equal(t, "deferred", res.Action)
			assert.Contains(t, strings.Join(res.Details, "\n"), strings.TrimSuffix(key, ":"))
			assert.Empty(t, c.hooks())
			assert.Empty(t, c.CreatedTriggerTokens)
		})

		t.Run("existing fast path revoked "+key, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			require.Len(t, c.triggers(), 1)
			c.FileContents[prefix+fullsendDispatcherTemplatePath] = []byte(template)

			_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

			require.NoError(t, err)
			assert.Empty(t, c.triggers())
			assert.Empty(t, c.hooks())
		})
	}
}

// The stored-bearer cleanup must not depend on the project lookup or on the
// variable listing succeeding: a drifted unmanaged privileged bearer is
// removed (or, for a dry run, reported) through both exported reconciliation
// entry points even when GetRepo or ListRepoVariables fails.
func TestGitLabWebhookSafety_UnboundPrivilegedBearerRemovedOnDiscoveryFailure(t *testing.T) {
	ctx := context.Background()
	for _, failing := range []string{"GetRepo", "ListRepoVariables"} {
		entries := []struct {
			name string
			run  func(c webhookFake, dryRun bool) (GitLabWebhookResult, error)
		}{
			{"ReconcileGitLabWebhookSafety", func(c webhookFake, dryRun bool) (GitLabWebhookResult, error) {
				return ReconcileGitLabWebhookSafety(ctx, c, webhookTestOwner, webhookTestRepo, dryRun)
			}},
			{"EnsureGitLabWebhookFastPath", func(c webhookFake, dryRun bool) (GitLabWebhookResult, error) {
				return EnsureGitLabWebhookFastPath(ctx, c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, dryRun)
			}},
		}
		for _, entry := range entries {
			t.Run(failing+" failure via "+entry.name, func(t *testing.T) {
				c, managedID := unboundPrivilegedBearerFake(t)
				c.Errors = map[string]error{failing: errors.New("boom")}
				res, err := entry.run(c, false)
				require.Error(t, err, "the discovery failure is still reported")
				assert.Empty(t, c.variable(forge.SecretTriggerToken), "the stored bearer is removed")
				assert.Empty(t, c.hooks(), "the managed webhook is removed")
				assert.Contains(t, c.RevokedTriggerTokenIDs, managedID)
				assertUnmanagedTriggerPreserved(t, c)
				assertNoCredentialLeak(t, c, res)
			})
			t.Run(failing+" failure via "+entry.name+" dry run", func(t *testing.T) {
				c, _ := unboundPrivilegedBearerFake(t)
				before := c.variable(forge.SecretTriggerToken)
				c.Errors = map[string]error{failing: errors.New("boom")}
				res, err := entry.run(c, true)
				require.Error(t, err)
				assert.Contains(t, strings.Join(res.Details, "\n"), "Would delete the "+forge.SecretTriggerToken+" variable")
				assert.Equal(t, before, c.variable(forge.SecretTriggerToken), "a dry run changes nothing")
				assert.Len(t, c.hooks(), 1)
				assert.Empty(t, c.RevokedTriggerTokenIDs)
			})
		}
	}
}
