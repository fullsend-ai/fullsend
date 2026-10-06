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

const (
	webhookDeveloperUserID  = 1001
	webhookMaintainerUserID = 2002
	webhookOwnerUserID      = 3003
)

func ensureWebhookRaw(c forge.Client) (GitLabWebhookResult, error) {
	return EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, false)
}

// ownedBy makes the fake mint trigger tokens as the given user, who holds
// the given project access level: GitLab binds a token to its creator.
func ownedBy(c webhookFake, userID int64, level int) {
	c.TriggerTokenOwnerID = userID
	c.ProjectMemberAccess[userID] = level
}

func TestEnsureGitLabWebhookFastPath_DeveloperOwnedTriggerTokenAccepted(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)

	res := ensureWebhook(t, c, false, false)

	assert.Equal(t, "update", res.Action)
	require.Len(t, c.triggers(), 1)
	assert.Equal(t, int64(webhookDeveloperUserID), c.triggers()[0].OwnerID)
	require.Len(t, c.hooks(), 1)
	assert.NotEmpty(t, c.variable(forge.SecretTriggerToken))

	// Reusing the existing managed token re-verifies its owner and is a no-op.
	again := ensureWebhook(t, c, false, false)
	assert.Equal(t, "none", again.Action)
	assert.Len(t, c.CreatedTriggerTokens, 1)
}

func TestEnsureGitLabWebhookFastPath_PrivilegedTriggerOwnerRejectedAndRevoked(t *testing.T) {
	for name, tc := range map[string]struct {
		userID int64
		level  int
	}{
		"maintainer": {webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer},
		"owner":      {webhookOwnerUserID, forge.GitLabAccessLevelOwner},
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			ownedBy(c, tc.userID, tc.level)

			res, err := ensureWebhookRaw(c)

			require.NoError(t, err, "a Maintainer-backed install defers nonfatally once cleanup succeeded")
			assert.Equal(t, "deferred", res.Action)
			require.Len(t, c.CreatedTriggerTokens, 1, "the token is minted only transiently")
			assert.Empty(t, c.triggers(), "the privileged token is revoked")
			assert.Contains(t, c.RevokedTriggerTokenIDs, c.CreatedTriggerTokens[0].ID)
			assert.Empty(t, c.hooks(), "no webhook is enabled")
			assert.Empty(t, c.CreatedProjectHooks)
			assert.Empty(t, c.variable(forge.SecretTriggerToken), "the privileged token is never stored")
			assert.Empty(t, c.variable(forge.SecretWebhookSecret))
			joined := strings.Join(res.Details, "\n")
			assert.Contains(t, joined, "Maintainer or Owner")
			assert.Contains(t, joined, "stays disabled")
			assertNoCredentialLeak(t, c, res)
		})
	}
}

func TestEnsureGitLabWebhookFastPath_TriggerOwnerBelowDeveloperRejected(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, 4004, 20) // Reporter: cannot run trigger pipelines

	res, err := ensureWebhookRaw(c)

	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.Empty(t, c.triggers())
	assert.Empty(t, c.hooks())
}

// A Developer-owned token still needs push or merge access to the protected
// default branch to start pipelines there; a role grant alone, or a grant
// to a different identity such as a poller, does not establish it.
func TestEnsureGitLabWebhookFastPath_DeveloperOwnerWithoutProtectedRefAccessDeferred(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	const pollerUserID = 7007
	for name, rule := range map[string]forge.ProtectedBranchRule{
		"merge granted only to a distinct poller identity": {Name: "main", MergeAccessLevels: []forge.ProtectedBranchAccess{{UserID: pollerUserID}}},
		"maintainers only": {Name: "main", MergeAccessLevels: []forge.ProtectedBranchAccess{{AccessLevel: forge.GitLabAccessLevelMaintainer}}},
		"group grant only": {Name: "main", PushAccessLevels: []forge.ProtectedBranchAccess{{GroupID: 9}}},
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
			delete(c.ProtectedBranches, prefix+"main")
			c.ProtectedBranchRules[prefix+"main"] = &rule

			res, err := ensureWebhookRaw(c)

			require.NoError(t, err)
			assert.Equal(t, "deferred", res.Action)
			assert.Contains(t, strings.Join(res.Details, "\n"), "push or merge access")
			assert.Empty(t, c.triggers(), "the unusable token is revoked")
			assert.Empty(t, c.hooks())
		})
	}

	t.Run("explicit grant to the trigger owner", func(t *testing.T) {
		c := newWebhookFake()
		ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
		delete(c.ProtectedBranches, prefix+"main")
		c.ProtectedBranchRules[prefix+"main"] = &forge.ProtectedBranchRule{Name: "main", MergeAccessLevels: []forge.ProtectedBranchAccess{{UserID: webhookDeveloperUserID}}}

		res, err := ensureWebhookRaw(c)

		require.NoError(t, err)
		assert.Equal(t, "update", res.Action)
		assert.Len(t, c.triggers(), 1)
	})
}

func TestEnsureGitLabWebhookFastPath_TriggerOwnerLookupFailureFailsClosed(t *testing.T) {
	t.Run("at provisioning", func(t *testing.T) {
		c := newWebhookFake()
		lookupErr := errors.New("lookup boom")
		c.Errors = map[string]error{"GetProjectMemberAccessLevel": lookupErr}

		res, err := ensureWebhookRaw(c)

		require.Error(t, err)
		assert.ErrorIs(t, err, lookupErr, "the original error is preserved for callers")
		assert.NotContains(t, err.Error(), "lookup boom", "server error text is withheld")
		assert.Equal(t, "deferred", res.Action)
		assert.Empty(t, c.triggers(), "the unverified token is revoked")
		assert.Empty(t, c.hooks())
		assert.Empty(t, c.variable(forge.SecretTriggerToken))
	})

	t.Run("token without a reported owner", func(t *testing.T) {
		c := newWebhookFake()
		c.TriggerTokenOwnerID = 0

		_, err := ensureWebhookRaw(c)

		require.Error(t, err)
		assert.ErrorContains(t, err, "no owner")
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
	})

	t.Run("owner no longer a project member", func(t *testing.T) {
		c := newWebhookFake()
		ownedBy(c, 5005, forge.GitLabAccessLevelDeveloper)
		delete(c.ProjectMemberAccess, 5005)

		_, err := ensureWebhookRaw(c)

		require.Error(t, err)
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
	})

	t.Run("revoking the rejected token fails", func(t *testing.T) {
		c := newWebhookFake()
		ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
		c.Errors = map[string]error{"RevokePipelineTriggerToken": errors.New("revoke boom")}

		_, err := ensureWebhookRaw(c)

		require.Error(t, err)
		assert.ErrorContains(t, err, "revoke boom", "cleanup failures are reported")
		assert.ErrorContains(t, err, "rejected pipeline trigger token")
	})
}

func TestEnsureGitLabWebhookFastPath_UnsafeExistingTriggerCleanedUpOnConverge(t *testing.T) {
	cases := map[string]func(c webhookFake){
		"owner promoted to maintainer": func(c webhookFake) {
			c.ProjectMemberAccess[webhookDeveloperUserID] = forge.GitLabAccessLevelMaintainer
		},
		"owner promoted to owner": func(c webhookFake) {
			c.ProjectMemberAccess[webhookDeveloperUserID] = forge.GitLabAccessLevelOwner
		},
		"owner lookup fails": func(c webhookFake) {
			c.Errors = map[string]error{"GetProjectMemberAccessLevel": errors.New("boom")}
		},
		"administrator-owned token from an earlier install": func(c webhookFake) {
			c.ProjectMemberAccess[webhookMaintainerUserID] = forge.GitLabAccessLevelMaintainer
			key := webhookTestOwner + "/" + webhookTestRepo
			c.PipelineTriggerTokens[key][0].OwnerID = webhookMaintainerUserID
		},
		"token with no recorded owner": func(c webhookFake) {
			key := webhookTestOwner + "/" + webhookTestRepo
			c.PipelineTriggerTokens[key][0].OwnerID = 0
		},
	}
	for name, drift := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			require.Len(t, c.triggers(), 1)
			require.Len(t, c.hooks(), 1)
			drift(c)

			needs, _ := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
			assert.True(t, needs, "an unsafe live credential is convergence work")

			dry, _ := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, true)
			assert.NotEqual(t, "none", dry.Action)
			assert.Len(t, c.triggers(), 1, "dry run must not revoke")
			assert.Len(t, c.hooks(), 1, "dry run must not delete")

			res, err := ensureWebhookRaw(c)
			if name == "owner lookup fails" || name == "token with no recorded owner" {
				require.Error(t, err, "an unverifiable owner is reported")
			} else {
				require.NoError(t, err)
			}
			assert.Empty(t, c.triggers(), "the unsafe trigger token is revoked")
			assert.Empty(t, c.hooks(), "the managed webhook is removed")
			assert.Len(t, c.CreatedTriggerTokens, 1, "nothing new is minted while unsafe")
			assert.Contains(t, strings.Join(res.Details, "\n"), "Revoked 1")
			assertNoCredentialLeak(t, c, res)
		})
	}
}

func TestReconcileGitLabWebhookSafety_RevokesPrivilegedTriggerOwner(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	c.ProjectMemberAccess[webhookDeveloperUserID] = forge.GitLabAccessLevelMaintainer

	res, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

	require.NoError(t, err)
	assert.Empty(t, c.triggers())
	assert.Empty(t, c.hooks())
	assert.Contains(t, strings.Join(res.Details, "\n"), "Maintainer or Owner")
}

// The install-time Maintainer credential is used to mint and revoke, but
// nothing derived from it survives as a runtime credential.
func TestEnsureGitLabWebhookFastPath_InstallTimeMaintainerAccessNotRetainedAtRuntime(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)

	res, err := ensureWebhookRaw(c)
	require.NoError(t, err, "the normal Maintainer-backed install defers rather than failing the repository")
	assert.Equal(t, "deferred", res.Action)

	// Every persistent artifact a runtime consumer could use is absent.
	assert.Empty(t, c.triggers())
	assert.Empty(t, c.hooks())
	assert.Empty(t, c.variable(forge.SecretTriggerToken))
	assert.Empty(t, c.variable(forge.SecretWebhookSecret))

	// Reinstalling keeps failing closed and keeps nothing alive, rather
	// than latching onto the Maintainer-owned token.
	_, err = ensureWebhookRaw(c)
	require.NoError(t, err)
	assert.Empty(t, c.triggers())
	assert.Empty(t, c.hooks())
	needs, _ := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	assert.True(t, needs, "provisioning stays pending, never silently satisfied")

	// A Developer-owned token can still be provisioned once one is
	// obtainable; the privileged mint left nothing behind to conflict.
	ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	res = ensureWebhook(t, c, false, false)
	assert.Equal(t, "update", res.Action)
	require.Len(t, c.triggers(), 1)
	assert.Equal(t, int64(webhookDeveloperUserID), c.triggers()[0].OwnerID)
}

// The webhook may be enabled only once the committed typed contract is
// complete end to end; each unmerged piece defers it without error.
func TestEnsureGitLabWebhookFastPath_DefersUntilTypedContractCommitted(t *testing.T) {
	prefix := webhookTestOwner + "/" + webhookTestRepo + "/"
	cases := map[string]struct {
		mutate func(c webhookFake)
		want   string
	}{
		"legacy wrapper": {func(c webhookFake) {
			c.FileContents[prefix+fullsendPipelineInclude] = []byte("include:\n  - local: '" + fullsendDispatcherTemplatePath + "'\n")
		}, "typed pipeline-input contract"},
		"root without the input contract": {func(c webhookFake) {
			c.FileContents[prefix+".gitlab-ci.yml"] = []byte(webhookTestRootCI)
		}, "declares and forwards the pipeline-input contract"},
		"agent template not landed": {func(c webhookFake) {
			delete(c.FileContents, prefix+fullsendAgentTemplatePath)
		}, "compatible agent and poll templates"},
		"poll template not landed": {func(c webhookFake) {
			delete(c.FileContents, prefix+fullsendPollTemplatePath)
		}, "compatible agent and poll templates"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			tc.mutate(c)

			res, err := ensureWebhookRaw(c)

			require.NoError(t, err)
			assert.Equal(t, "deferred", res.Action)
			assert.Contains(t, strings.Join(res.Details, "\n"), tc.want)
			assert.Empty(t, c.CreatedTriggerTokens)
			assert.Empty(t, c.CreatedProjectHooks)
		})
	}
}

// A violated safety invariant must revoke every managed trigger even when
// variable, token-scope, or webhook discovery fails: triggers are
// identified by description alone, and an installation error does not
// invalidate their bearer credentials.
func TestReconcileGitLabWebhookSafety_RevokesDespiteIndependentDiscoveryFailure(t *testing.T) {
	for _, failing := range []string{"ListRepoVariables", "GetRepoSecretProtection", "ListProjectHooks"} {
		t.Run(failing, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			require.Len(t, c.triggers(), 1)
			token := c.variable(forge.SecretTriggerToken)
			require.NotEmpty(t, token)
			c.PipelineVarOverrideRoles[webhookTestOwner+"/"+webhookTestRepo] = "developer"
			c.Errors = map[string]error{failing: errors.New("lookup failed: " + token)}

			res, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

			require.Error(t, err, "the discovery failure is reported")
			assert.NotContains(t, err.Error(), token, "credential-bearing server text is redacted")
			assert.Empty(t, c.triggers(), "the managed trigger is revoked regardless")
			// Either the unsafe-credential step or the teardown reports it.
			assert.Regexp(t, `Revoked (1 Fullsend-managed|pipeline trigger token \(ID 1\))`, strings.Join(res.Details, "\n"))
		})
	}
}

// When the project cannot be read the safety invariants cannot be verified,
// but managed credentials are identified independently of the project, so both
// exported entry points still tear them down and join the lookup error.
func TestGitLabWebhookSafety_ProjectLookupFailureStillTearsDown(t *testing.T) {
	entryPoints := map[string]func(c webhookFake, dryRun bool) (GitLabWebhookResult, error){
		"ReconcileGitLabWebhookSafety": func(c webhookFake, dryRun bool) (GitLabWebhookResult, error) {
			return ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, dryRun)
		},
		"EnsureGitLabWebhookFastPath": func(c webhookFake, dryRun bool) (GitLabWebhookResult, error) {
			return EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, false, dryRun)
		},
	}
	for name, call := range entryPoints {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			ensureWebhook(t, c, false, false)
			require.Len(t, c.triggers(), 1)
			require.Len(t, c.hooks(), 1)
			lookupErr := errors.New("lookup boom")
			c.Errors = map[string]error{"GetRepo": lookupErr}

			dry, dryErr := call(c, true)
			require.Error(t, dryErr)
			assert.ErrorIs(t, dryErr, lookupErr)
			assert.NotContains(t, dryErr.Error(), "lookup boom", "server error text is withheld")
			assert.Contains(t, strings.Join(dry.Details, "\n"), "Would revoke")
			assert.Len(t, c.triggers(), 1, "dry run must not revoke")
			assert.Len(t, c.hooks(), 1, "dry run must not delete")

			res, err := call(c, false)

			require.Error(t, err, "the lookup failure is reported")
			assert.ErrorContains(t, err, "reading project")
			assert.ErrorIs(t, err, lookupErr)
			assert.NotContains(t, err.Error(), "lookup boom", "server error text is withheld")
			assert.Equal(t, "update", res.Action)
			assert.Empty(t, c.triggers(), "the managed trigger is revoked despite the lookup failure")
			assert.Empty(t, c.hooks(), "the managed webhook is removed despite the lookup failure")
			assert.Contains(t, strings.Join(res.Details, "\n"), "Revoked 1")
		})
	}

	t.Run("cleanup errors are joined with the lookup error", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		lookupErr := errors.New("lookup boom")
		c.Errors = map[string]error{"GetRepo": lookupErr, "RevokePipelineTriggerToken": errors.New("revoke boom")}

		_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

		require.Error(t, err)
		assert.ErrorIs(t, err, lookupErr)
		assert.ErrorContains(t, err, "reading project")
		assert.ErrorContains(t, err, "revoke boom")
	})
}

// A replacement token rejected during rotation is revoked on its own: the
// existing compliant token and the webhook that uses it keep working.
func TestEnsureGitLabWebhookFastPath_RotationRejectionPreservesCompliantFastPath(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	ensureWebhook(t, c, false, false)
	require.Len(t, c.triggers(), 1)
	existingID := c.triggers()[0].ID
	existingToken := c.variable(forge.SecretTriggerToken)
	existingHook := c.hooks()[0]

	// Rotation mints the replacement with the Maintainer installation credential.
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)

	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	require.Len(t, c.CreatedTriggerTokens, 2)
	rejectedID := c.CreatedTriggerTokens[1].ID
	assert.Equal(t, []int64{rejectedID}, c.RevokedTriggerTokenIDs, "only the rejected replacement is revoked")
	require.Len(t, c.triggers(), 1)
	assert.Equal(t, existingID, c.triggers()[0].ID, "the compliant token survives")
	assert.Equal(t, existingToken, c.variable(forge.SecretTriggerToken), "the stored token is unchanged")
	require.Len(t, c.hooks(), 1, "the working webhook survives")
	assert.Equal(t, existingHook.URL, c.hooks()[0].URL)
	assert.Empty(t, c.DeletedProjectHookIDs)
	assertNoCredentialLeak(t, c, res)
}

// Removing default-branch protection after provisioning is convergence
// work: protected variables are unavailable to the dispatcher without it.
func TestGitLabWebhookNeedsWork_DefaultBranchUnprotectedAfterProvisioning(t *testing.T) {
	c := newWebhookFake()
	ensureWebhook(t, c, false, false)
	require.False(t, gitlabWebhookNeedsWork(context.Background(), c, webhookTestOwner, webhookTestRepo))

	delete(c.ProtectedBranches, webhookTestOwner+"/"+webhookTestRepo+"/main")

	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	assert.True(t, needs)
	assert.True(t, gitlabWebhookNeedsWork(context.Background(), c, webhookTestOwner, webhookTestRepo))

	// The follow-up install defers and tears the managed fast path down:
	// without protection the dispatcher would run without its variables, and
	// leaving the trigger and webhook live contradicts the revocation promise.
	res, err := ensureWebhookRaw(c)
	require.NoError(t, err)
	assert.Equal(t, "update", res.Action)
	details := strings.Join(res.Details, "\n")
	assert.Contains(t, details, "no protected-branch rule")
	assert.Contains(t, details, "Revoked 1")
	assert.Contains(t, details, "Deleted 1")
	assert.Empty(t, c.triggers())
	assert.Empty(t, c.hooks())
}

// Revocation-only reconciliation (used when installation failed) must treat
// missing default-branch protection as a safety failure too, and an
// unverifiable protection listing must fail closed.
func TestReconcileGitLabWebhookSafety_DefaultBranchProtectionGone(t *testing.T) {
	key := webhookTestOwner + "/" + webhookTestRepo

	t.Run("unprotected default branch revokes", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		delete(c.ProtectedBranches, key+"/main")

		res, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

		require.NoError(t, err)
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
		assert.Contains(t, strings.Join(res.Details, "\n"), "Revoked 1")
	})

	t.Run("unverifiable protection revokes and reports", func(t *testing.T) {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		c.Errors = map[string]error{"ListProtectedBranches": errors.New("boom")}

		_, err := ReconcileGitLabWebhookSafety(context.Background(), c, webhookTestOwner, webhookTestRepo, false)

		require.Error(t, err)
		assert.Empty(t, c.triggers())
		assert.Empty(t, c.hooks())
	})
}

// interleaveFake runs hook once, just before the first UpdateProjectHook,
// to model a second installer process acting between this run's steps.
type interleaveFake struct {
	webhookFake
	before func()
	after  func()
	done   bool
}

func (i *interleaveFake) UpdateProjectHook(ctx context.Context, owner, repo string, id int64, hook forge.ProjectHook) (*forge.ProjectHook, error) {
	if !i.done {
		i.done = true
		if i.before != nil {
			i.before()
		}
		out, err := i.webhookFake.UpdateProjectHook(ctx, owner, repo, id, hook)
		if i.after != nil {
			i.after()
		}
		return out, err
	}
	return i.webhookFake.UpdateProjectHook(ctx, owner, repo, id, hook)
}

// Concurrent rotations must not report success with a webhook pointing at a
// revoked token, nor revoke another run's active token.
func TestEnsureGitLabWebhookFastPath_ConcurrentRotationDetected(t *testing.T) {
	setup := func() *interleaveFake {
		c := newWebhookFake()
		ensureWebhook(t, c, false, false)
		require.Len(t, c.triggers(), 1)
		return &interleaveFake{webhookFake: c}
	}

	t.Run("other run finishes before this run writes the webhook", func(t *testing.T) {
		c := setup()
		c.before = func() {
			_, err := EnsureGitLabWebhookFastPath(context.Background(), c.webhookFake, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)
			require.NoError(t, err)
		}

		_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)

		require.Error(t, err, "the lost race is reported, not success")
		assert.ErrorContains(t, err, "concurrent")

		// The next run converges to one managed token and one hook.
		res, err := ensureWebhookRaw(c.webhookFake)
		require.NoError(t, err)
		assert.NotEqual(t, "", res.Action)
		assert.Len(t, c.triggers(), 1)
		assert.Len(t, c.hooks(), 1)
		needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
		require.NoError(t, err)
		assert.False(t, needs)
	})

	t.Run("other run replaces the webhook after this run writes it", func(t *testing.T) {
		c := setup()
		c.after = func() {
			_, err := EnsureGitLabWebhookFastPath(context.Background(), c.webhookFake, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)
			require.NoError(t, err)
		}

		_, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)

		require.Error(t, err)
		assert.ErrorContains(t, err, "concurrent")
		// This run did not revoke the other run's active token.
		require.Len(t, c.triggers(), 1)
		needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
		require.NoError(t, err)
		assert.False(t, needs, "the winning run's state is a complete fast path")
	})
}

// A compliant active token and webhook survive a rejected rotation even
// when a safe superseded trigger still awaits cleanup; the superseded
// trigger is revoked on its own.
func TestEnsureGitLabWebhookFastPath_RotationRejectionPreservesFastPathWithSupersededTrigger(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookDeveloperUserID, forge.GitLabAccessLevelDeveloper)
	ensureWebhook(t, c, false, false)
	require.Len(t, c.triggers(), 1)
	activeID := c.triggers()[0].ID
	existingToken := c.variable(forge.SecretTriggerToken)
	existingHook := c.hooks()[0]

	// A developer-owned trigger left over from an earlier rotation.
	const supersededID = 9001
	key := webhookTestOwner + "/" + webhookTestRepo
	c.PipelineTriggerTokens[key] = append(c.PipelineTriggerTokens[key], forge.PipelineTriggerToken{
		ID: supersededID, Description: GitLabWebhookTriggerDescription, OwnerID: webhookDeveloperUserID,
	})

	// Rotation mints the replacement with the Maintainer installation credential.
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	res, err := EnsureGitLabWebhookFastPath(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo, true, false)

	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	require.Len(t, c.CreatedTriggerTokens, 2)
	rejectedID := c.CreatedTriggerTokens[1].ID
	assert.ElementsMatch(t, []int64{rejectedID, supersededID}, c.RevokedTriggerTokenIDs)
	require.Len(t, c.triggers(), 1)
	assert.Equal(t, activeID, c.triggers()[0].ID, "the active token survives")
	assert.Equal(t, existingToken, c.variable(forge.SecretTriggerToken), "the stored token is unchanged")
	require.Len(t, c.hooks(), 1, "the working webhook survives")
	assert.Equal(t, existingHook.URL, c.hooks()[0].URL)
	assert.Empty(t, c.DeletedProjectHookIDs)
	assertNoCredentialLeak(t, c, res)
}
