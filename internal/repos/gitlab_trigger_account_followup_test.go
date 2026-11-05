package repos

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// containOpts is what containment-only recovery receives: no Poller
// inventory, no bootstrap client and no publisher.
func (e *trigEnv) containOpts() GitLabTriggerAccountOptions {
	return GitLabTriggerAccountOptions{
		Owner: trigOwner, Repo: trigRepo,
		Lease: trigTestLease(trigOwner, trigRepo),
		API:   e.f,
		State: e.state,
	}
}

// interruptBootstrap leaves the account elevated with a live bootstrap PAT,
// like an install killed between elevation and demotion.
func (e *trigEnv) interruptBootstrap(t *testing.T, acct int) {
	t.Helper()
	st, ok := e.ledger(t)
	require.True(t, ok)
	pat, err := e.f.CreateServiceAccountPAT(context.Background(), trigOwner, trigRepo, acct, st.patName(), nil, "")
	require.NoError(t, err)
	e.f.members[acct] = forge.GitLabAccessLevelMaintainer
	st.Elevated, st.PATID = true, pat.ID
	e.setLedger(t, st)
}

func TestContainGitLabTriggerAccount_RestoresDeveloperWithoutCreating(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.interruptBootstrap(t, res.AccountID)
	before := map[string]int{}
	for k, v := range e.f.calls {
		before[k] = v
	}
	triggersBefore, keysBefore := len(e.f.triggers), len(e.f.keys)

	// Containment needs neither the Poller inventory nor any create path.
	require.NoError(t, ContainGitLabTriggerAccount(context.Background(), e.containOpts()))
	e.assertContained(t)
	for _, create := range []string{"CreateProjectServiceAccount", "CreateServiceAccountPAT", "CreateProjectDeployKey", "CreatePipelineTriggerToken", "GrantProtectedBranchPushDeployKey", "Publish"} {
		assert.Equal(t, before[create], e.f.calls[create], "%s must not run during containment", create)
	}
	assert.Len(t, e.f.triggers, triggersBefore)
	assert.Len(t, e.f.keys, keysBefore)
	assert.NotEmpty(t, e.f.published, "the published trigger value is left alone")
}

func TestContainGitLabTriggerAccount_ContainsRetiredGenerationsAndNoLedger(t *testing.T) {
	e := newTrigEnv()
	require.NoError(t, ContainGitLabTriggerAccount(context.Background(), e.containOpts()), "no ledger is a no-op")
	assert.Empty(t, e.f.calls, "no GitLab call without a ledger")

	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	// Make a retired generation that was left elevated.
	st, ok := e.ledger(t)
	require.True(t, ok)
	old := st
	st = gitLabTriggerAccountState{Version: 1, Phase: gitLabTriggerPhaseRotating, Branch: old.Branch, Nonce: "0123456789abcdef", Retired: []gitLabTriggerAccountState{old}}
	e.setLedger(t, st)
	pat, err := e.f.CreateServiceAccountPAT(context.Background(), trigOwner, trigRepo, res.AccountID, old.patName(), nil, "")
	require.NoError(t, err)
	e.f.members[res.AccountID] = forge.GitLabAccessLevelMaintainer
	st.Retired[0].Elevated, st.Retired[0].PATID = true, pat.ID
	e.setLedger(t, st)

	require.NoError(t, ContainGitLabTriggerAccount(context.Background(), e.containOpts()))
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[res.AccountID])
	assert.Zero(t, e.activePATs())
	got, _ := e.ledger(t)
	require.Len(t, got.Retired, 1)
	assert.False(t, got.Retired[0].Elevated)
	assert.Zero(t, got.Retired[0].PATID)
}

func TestContainGitLabTriggerAccount_FailuresKeepObligationsAndLeaseIsRequired(t *testing.T) {
	for _, failing := range []string{"ListProjectServiceAccounts", "UpdateProjectMemberAccessLevel", "RevokeServiceAccountPAT"} {
		t.Run(failing, func(t *testing.T) {
			e := newTrigEnv()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			e.interruptBootstrap(t, res.AccountID)

			e.f.fail[failing] = true
			require.Error(t, ContainGitLabTriggerAccount(context.Background(), e.containOpts()))
			st, _ := e.ledger(t)
			assert.True(t, st.Elevated || st.PATID != 0, "an unfinished obligation stays recorded")

			require.NoError(t, ContainGitLabTriggerAccount(context.Background(), e.containOpts()))
			e.assertContained(t)
		})
	}

	e := newTrigEnv()
	_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	o := e.containOpts()
	o.Lease = nil
	require.ErrorIs(t, ContainGitLabTriggerAccount(context.Background(), o), errTriggerAccountUnleased)
}

// A trigger token whose owner GitLab did not report fails closed before any
// elevation, is reported by ID, and is never revoked by inference.
func TestConvergeGitLabTriggerAccount_UnknownOwnerTriggerFailsClosedBeforeElevation(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.f.triggers[9] = forge.PipelineTriggerToken{ID: 9, Description: res.stateDescription(t, e), OwnerID: 0}
	e.f.triggers[10] = forge.PipelineTriggerToken{ID: 10, Description: "admin", OwnerID: 0}
	delete(e.f.keys, res.DeployKeyID)
	elevations := 0
	o := e.opts()
	o.API = trigElevationProbeAPI{trigFakeGitLab: e.f, onElevate: func(int64) { elevations++ }}

	_, err = ConvergeGitLabTriggerAccount(context.Background(), o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown owner")
	assert.Contains(t, err.Error(), "9")
	assert.Contains(t, err.Error(), "10")
	assert.Zero(t, elevations)
	assert.Contains(t, e.f.triggers, int64(9))
	assert.Contains(t, e.f.triggers, int64(10))
	assert.Zero(t, e.activePATs())
}

func (r GitLabTriggerAccountResult) stateDescription(t *testing.T, e *trigEnv) string {
	t.Helper()
	st, ok := e.ledger(t)
	require.True(t, ok)
	return st.triggerDescription()
}

// Ownerless schedules are not attributed to the trigger account: they are
// reported as unknown ownership, and a schedule with another owner is ignored.
func TestConvergeGitLabTriggerAccount_OwnerlessSchedulesAreReportedAsUnknown(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)

	e.f.schedules[res.AccountID+1000] = []int{5}
	got, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err, "a schedule owned by someone else is not this account's")
	assert.True(t, got.Ready)

	e.f.ownerlessSchedules = []int{77}
	delete(e.f.keys, got.DeployKeyID)
	elevations := 0
	o := e.opts()
	o.API = trigElevationProbeAPI{trigFakeGitLab: e.f, onElevate: func(int64) { elevations++ }}
	_, err = ConvergeGitLabTriggerAccount(context.Background(), o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no reported owner")
	assert.Contains(t, err.Error(), "77")
	assert.NotContains(t, err.Error(), "owns 1 pipeline schedule", "an ownerless schedule is not claimed as the account's")
	assert.Zero(t, elevations)
}

// An unsafe sink is removed and its ownership-verified trigger revoked before
// the replacement generation exists; replacement failures keep the cleanup.
func TestConvergeGitLabTriggerAccount_UnsafeSinkIsContainedBeforeReplacement(t *testing.T) {
	unsafe := forge.SecretProtection{Exists: true, Protected: true} // unmasked
	setup := func(t *testing.T) (*trigEnv, GitLabTriggerAccountResult) {
		e := newTrigEnv()
		e.useRecreatingSink()
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
		require.NoError(t, err)
		e.state.SecretProtections[trigSinkKey] = unsafe
		return e, res
	}

	t.Run("replacement creation fails", func(t *testing.T) {
		e, res := setup(t)
		e.f.fail["CreatePipelineTriggerToken"] = true
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
		require.Error(t, err)
		assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the exposed generation's trigger is revoked")
		assert.False(t, e.sinkPresent(), "the unsafe variable is removed")
		assert.Zero(t, e.activePATs())
		st, ok := e.ledger(t)
		require.True(t, ok)
		require.Len(t, st.Retired, 1, "the old generation's cleanup obligation is recorded")
		assert.Equal(t, res.AccountID, st.Retired[0].AccountID)
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[res.AccountID])

		got, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
		require.NoError(t, err)
		e.assertConverged(t, got)
		assert.NotEqual(t, res.AccountID, got.AccountID)
		final, _ := e.ledger(t)
		assert.Empty(t, final.Retired)
	})

	t.Run("variable removal fails", func(t *testing.T) {
		e, res := setup(t)
		o := e.opts()
		o.UnpublishTriggerToken = func(context.Context) error { return errTrigInjected }
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.Error(t, err)
		assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "a failed variable removal does not suppress the revocation")
		assert.True(t, e.sinkPresent(), "the variable that could not be removed stays")
		st, _ := e.ledger(t)
		assert.Empty(t, st.Retired, "no replacement generation starts")
		assert.Equal(t, res.AccountID, st.AccountID)
		assert.True(t, st.PublishStarted, "the publication obligation stays recorded for the retry")
		assert.False(t, st.TriggerPublished, "the completed revocation is recorded")

		got, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
		require.NoError(t, err)
		e.assertConverged(t, got)
	})

	t.Run("trigger revocation fails", func(t *testing.T) {
		e, res := setup(t)
		e.f.fail["RevokePipelineTriggerToken"] = true
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
		require.Error(t, err)
		assert.False(t, e.sinkPresent(), "the unsafe variable is already removed")
		st, _ := e.ledger(t)
		assert.Empty(t, st.Retired, "no replacement starts while the old trigger is live")
		assert.Equal(t, res.TriggerTokenID, st.TriggerTokenID, "the trigger obligation stays recorded")

		got, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
		require.NoError(t, err)
		e.assertConverged(t, got)
		assert.NotContains(t, e.f.triggers, res.TriggerTokenID)
	})
}

// A deploy key that cannot push never admits the trigger account on the
// branch, so it is neither ready nor repaired by inference; uninstall still
// removes it.
func TestGitLabTriggerAccount_NonPushDeployKeyIsNeverReady(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Equal(t, GitLabTriggerStateReady, e.diagnose(e.f, trigBranch).State)

	e.f.noPushKeys = map[int]bool{res.DeployKeyID: true}
	got := e.diagnose(e.f, trigBranch)
	assert.Equal(t, GitLabTriggerStateDrifted, got.State)
	assert.Contains(t, got.Reasons[len(got.Reasons)-1], "cannot push")

	out, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.ErrorContains(t, err, "cannot push")
	assert.False(t, out.Ready)

	require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
	e.assertRemoved(t)
}

// Direct membership already at Developer does not prove effective access:
// inherited excess access is rejected on the unchanged path and in status.
func TestGitLabTriggerAccount_InheritedExcessAccessRejectedWhenDirectIsDeveloper(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	require.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[res.AccountID])

	api := trigInheritedAPI{trigFakeGitLab: e.f, inherited: forge.GitLabAccessLevelMaintainer}
	got := e.diagnose(api, trigBranch)
	assert.Equal(t, GitLabTriggerStateDrifted, got.State)
	assert.Contains(t, got.Reasons[0], "access level 40")

	o := e.opts()
	o.API = api
	out, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.ErrorContains(t, err, "effective access level")
	assert.False(t, out.Ready)
}
