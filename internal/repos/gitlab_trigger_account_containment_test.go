package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// containWithSinkOpts is containOpts plus the sink remover that the live
// containment options carry.
func (e *trigEnv) containWithSinkOpts() GitLabTriggerAccountOptions {
	return e.unpublishClearsAttributes(e.containOpts())
}

// trigRevokeFailAPI fails the revocation of the listed trigger tokens only.
type trigRevokeFailAPI struct {
	*trigFakeGitLab
	failIDs map[int64]bool
}

func (a trigRevokeFailAPI) RevokePipelineTriggerToken(ctx context.Context, o, r string, id int64) error {
	if a.failIDs[id] {
		return errTrigInjected
	}
	return a.trigFakeGitLab.RevokePipelineTriggerToken(ctx, o, r, id)
}

// Containment-only recovery removes a published credential, and revokes its
// owned trigger, when the override restriction is weakened or unreadable,
// without any convergence, deferral or create path. A healthy project keeps it.
func TestContainGitLabTriggerAccount_ContainsPublishedCredentialWhenOverrideRoleUnsafe(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(e *trigEnv){
		"weakened role": func(e *trigEnv) {
			e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper
		},
		"unreadable role": func(e *trigEnv) {
			e.state.Errors = map[string]error{"GetPipelineVariablesMinimumOverrideRole": errors.New("boom")}
		},
	}
	for name, weaken := range cases {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			e.useRecreatingSink()
			res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			require.NoError(t, err)
			require.True(t, e.sinkPresent())
			require.Contains(t, e.f.triggers, res.TriggerTokenID)

			weaken(e)
			require.NoError(t, ContainGitLabTriggerAccount(ctx, e.containWithSinkOpts()))

			assert.False(t, e.sinkPresent(), "the published credential is removed")
			assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the owned trigger is revoked")
			st, ok := e.ledger(t)
			require.True(t, ok, "the ledger keeps the cleanup obligation")
			assert.True(t, st.PublishStarted)
			assert.False(t, st.TriggerPublished)
		})
	}

	t.Run("healthy project keeps the credential", func(t *testing.T) {
		e := newTrigEnv()
		e.useRecreatingSink()
		res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
		require.NoError(t, err)
		require.NoError(t, ContainGitLabTriggerAccount(ctx, e.containWithSinkOpts()))
		assert.True(t, e.sinkPresent())
		assert.Contains(t, e.f.triggers, res.TriggerTokenID)
	})
}

func TestConvergeGitLabTriggerAccount_ProjectSafetyDriftContainsAndDoesNotRepublish(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*trigEnv)
		wantsError bool
	}{
		{"extra protected ref", func(e *trigEnv) {
			e.state.ProtectedBranches[trigOwner+"/"+trigRepo+"/main"] = true
			e.state.ProtectedBranches[trigOwner+"/"+trigRepo+"/release"] = true
		}, false},
		{"unreadable protected refs", func(e *trigEnv) {
			e.state.Errors = map[string]error{"ListProtectedBranches": errors.New("503 unavailable")}
		}, true},
		{"job-token repository push enabled", func(e *trigEnv) {
			e.state.JobTokenRepositoryPushAllowed[trigOwner+"/"+trigRepo] = true
		}, false},
		{"job-token repository push unreadable", func(e *trigEnv) {
			e.state.Errors = map[string]error{"GetJobTokenRepositoryPushAllowed": errors.New("503 unavailable")}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTrigEnv()
			e.useRecreatingSink()
			opts := e.unpublishClearsAttributes(e.opts())
			res, err := ConvergeGitLabTriggerAccount(context.Background(), opts)
			require.NoError(t, err)
			require.True(t, e.sinkPresent())
			require.Contains(t, e.f.triggers, res.TriggerTokenID)
			tc.mutate(e)
			opts.EnforceWebhookSafety = true

			result, err := ConvergeGitLabTriggerAccount(context.Background(), opts)
			if tc.wantsError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, result.Deferred)
			}
			assert.False(t, e.sinkPresent(), "unsafe published variable is removed")
			assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the owned trigger is revoked")
			st, exists := e.ledger(t)
			require.True(t, exists)
			assert.True(t, st.PublishStarted, "cleanup obligations remain recorded")
			assert.False(t, st.TriggerPublished)
			assert.Len(t, e.f.triggers, 0, "a replacement must not be published while safety is unmet")
		})
	}
}

// A failed replacement leaves the retired generation's live token in the sink
// while the current generation never published. Ordinary convergence on a
// healthy project must contain that sink when it becomes unsafe, before the
// replacement is created, even when replacement creation fails again.
func TestConvergeGitLabTriggerAccount_UnsafeSinkOwnedByRetiredGenerationIsContainedBeforeReplacement(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.useRecreatingSink()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)

	e.f.fail["CreateProjectServiceAccount"] = true
	_, err = RotateGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.Error(t, err)

	st, ok := e.ledger(t)
	require.True(t, ok)
	require.False(t, st.PublishStarted, "the replacement never published")
	require.Len(t, st.Retired, 1)
	require.True(t, st.Retired[0].PublishStarted)
	require.True(t, e.sinkPresent())
	require.Contains(t, e.f.triggers, res.TriggerTokenID)

	e.state.SecretProtections[trigSinkKey] = forge.SecretProtection{Exists: true, Protected: true} // unmasked
	e.f.fail["CreateProjectServiceAccount"] = true                                                 // injected failures are one-shot
	_, err = ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.Error(t, err, "the replacement still fails")

	assert.False(t, e.sinkPresent(), "the retired generation's unsafe sink is removed")
	assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the retired generation's trigger is revoked")
	st, ok = e.ledger(t)
	require.True(t, ok)
	require.Len(t, st.Retired, 1, "retirement stays recorded")
	assert.False(t, st.Retired[0].TriggerPublished)
}

// One generation's failed revocation does not suppress the others, and the
// ledger records exactly the revocations that completed.
func TestContainGitLabTriggerAccount_RevocationFailureInOneGenerationDoesNotSuppressOthers(t *testing.T) {
	ctx := context.Background()
	// Two publishing generations: the rotation's retirement fails, so the
	// previous generation and its live trigger stay recorded.
	setup := func(t *testing.T) (e *trigEnv, previous, current GitLabTriggerAccountResult) {
		e = newTrigEnv()
		e.useRecreatingSink()
		var err error
		previous, err = ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
		require.NoError(t, err)
		e.f.fail["RevokePipelineTriggerToken"] = true
		_, err = RotateGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
		require.Error(t, err)
		delete(e.f.fail, "RevokePipelineTriggerToken")
		st, ok := e.ledger(t)
		require.True(t, ok)
		require.Len(t, st.Retired, 1)
		require.True(t, st.PublishStarted)
		require.True(t, st.Retired[0].PublishStarted)
		current = GitLabTriggerAccountResult{TriggerTokenID: st.TriggerTokenID}
		require.Contains(t, e.f.triggers, previous.TriggerTokenID)
		require.Contains(t, e.f.triggers, current.TriggerTokenID)
		e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper
		return e, previous, current
	}

	t.Run("current generation fails", func(t *testing.T) {
		e, previous, current := setup(t)
		o := e.containWithSinkOpts()
		o.API = trigRevokeFailAPI{trigFakeGitLab: e.f, failIDs: map[int64]bool{current.TriggerTokenID: true}}
		err := ContainGitLabTriggerAccount(ctx, o)
		require.ErrorIs(t, err, errTrigInjected)

		assert.False(t, e.sinkPresent())
		assert.Contains(t, e.f.triggers, current.TriggerTokenID, "the failed revocation stays live")
		assert.NotContains(t, e.f.triggers, previous.TriggerTokenID, "the retired generation is still revoked")
		st, _ := e.ledger(t)
		assert.True(t, st.TriggerPublished, "the unresolved obligation is persisted")
		assert.False(t, st.Retired[0].TriggerPublished, "the completed revocation is persisted")

		require.NoError(t, ContainGitLabTriggerAccount(ctx, e.containWithSinkOpts()))
		assert.NotContains(t, e.f.triggers, current.TriggerTokenID, "a retry finishes the revocation")
	})

	t.Run("retired generation fails", func(t *testing.T) {
		e, previous, current := setup(t)
		o := e.containWithSinkOpts()
		o.API = trigRevokeFailAPI{trigFakeGitLab: e.f, failIDs: map[int64]bool{previous.TriggerTokenID: true}}
		err := ContainGitLabTriggerAccount(ctx, o)
		require.ErrorIs(t, err, errTrigInjected)

		assert.NotContains(t, e.f.triggers, current.TriggerTokenID, "the current generation is still revoked")
		assert.Contains(t, e.f.triggers, previous.TriggerTokenID)
		st, _ := e.ledger(t)
		assert.False(t, st.TriggerPublished)
		assert.True(t, st.Retired[0].TriggerPublished, "the unresolved obligation is persisted")
	})
}

// A failed variable deletion does not suppress revocation.
func TestContainGitLabTriggerAccount_VariableDeletionFailureDoesNotSuppressRevocation(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.useRecreatingSink()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)
	e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper

	o := e.containOpts()
	o.UnpublishTriggerToken = func(context.Context) error { return errTrigInjected }
	require.ErrorIs(t, ContainGitLabTriggerAccount(ctx, o), errTrigInjected)

	assert.True(t, e.sinkPresent(), "the variable that could not be removed stays")
	assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the trigger is revoked anyway")
}

// A published trigger is contained against the account's live access before any
// deferral or unrelated readiness failure can return. An account raised out of
// band is demoted while a healthy sink and role keep the credential in place;
// when Developer access cannot be established the credential is removed and
// the owned trigger revoked, with the cleanup obligation kept.
func TestConvergeGitLabTriggerAccount_ElevatedPublishedAccountIsContainedBeforeDeferral(t *testing.T) {
	ctx := context.Background()
	elevate := func(t *testing.T, e *trigEnv, accountID int) {
		t.Helper()
		e.f.members[accountID] = forge.GitLabAccessLevelMaintainer
	}
	deferrals := map[string]func(e *trigEnv, o *GitLabTriggerAccountOptions){
		"protected-branch rule removed": func(e *trigEnv, _ *GitLabTriggerAccountOptions) { e.f.exactRule = false },
		"default branch changed":        func(_ *trigEnv, o *GitLabTriggerAccountOptions) { o.Branch = "release" },
	}

	for name, defer_ := range deferrals {
		t.Run("demoted before "+name+" defers", func(t *testing.T) {
			e := newTrigEnv()
			e.useRecreatingSink()
			res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			require.NoError(t, err)
			elevate(t, e, res.AccountID)

			o := e.unpublishClearsAttributes(e.opts())
			defer_(e, &o)
			got, err := ConvergeGitLabTriggerAccount(ctx, o)
			require.NoError(t, err)
			assert.NotEmpty(t, got.Deferred)

			assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[res.AccountID], "the published account is back at Developer")
			assert.True(t, e.sinkPresent(), "a verified credential is left in place")
			assert.Contains(t, e.f.triggers, res.TriggerTokenID)
		})
	}

	// Access that cannot be restored, or verified because the inventory or
	// the membership cannot be read, contains the credential instead.
	failures := map[string]func(e *trigEnv){
		"membership update fails": func(e *trigEnv) { e.f.fail["UpdateProjectMemberAccessLevel"] = true },
		"inventory unreadable":    func(e *trigEnv) { e.f.fail["ListProjectServiceAccounts"] = true },
		"membership unreadable":   func(e *trigEnv) { e.f.fail["GetProjectMemberAccessLevel"] = true },
	}
	for name, breakIt := range failures {
		for dName, defer_ := range deferrals {
			t.Run(name+" with "+dName, func(t *testing.T) {
				e := newTrigEnv()
				e.useRecreatingSink()
				res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
				require.NoError(t, err)
				elevate(t, e, res.AccountID)

				o := e.unpublishClearsAttributes(e.opts())
				defer_(e, &o)
				breakIt(e)
				_, err = ConvergeGitLabTriggerAccount(ctx, o)
				require.ErrorIs(t, err, errTrigInjected)

				assert.False(t, e.sinkPresent(), "the credential is removed")
				assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the owned trigger is revoked")
				st, ok := e.ledger(t)
				require.True(t, ok, "the ledger keeps the cleanup obligation")
				assert.True(t, st.PublishStarted)
				assert.False(t, st.TriggerPublished)
				assert.Equal(t, res.TriggerTokenID, st.TriggerTokenID)
			})
		}
	}

	t.Run("a retired publishing generation is demoted too", func(t *testing.T) {
		e := newTrigEnv()
		e.useRecreatingSink()
		res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
		require.NoError(t, err)
		e.f.fail["CreateProjectServiceAccount"] = true
		_, err = RotateGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
		require.Error(t, err)
		st, ok := e.ledger(t)
		require.True(t, ok)
		require.Len(t, st.Retired, 1)
		require.True(t, st.Retired[0].PublishStarted)
		elevate(t, e, res.AccountID)

		o := e.unpublishClearsAttributes(e.opts())
		o.Branch = "release"
		got, err := ConvergeGitLabTriggerAccount(ctx, o)
		require.NoError(t, err)
		assert.NotEmpty(t, got.Deferred)
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[res.AccountID], "the retired generation's account is demoted")
	})
}
