package repos

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func (e *trigEnv) diagnose(api GitLabTriggerAccountAPI, branch string) GitLabTriggerAccountStatus {
	return DiagnoseGitLabTriggerAccount(context.Background(), e.state, api, trigOwner, trigRepo, branch)
}

func TestDiagnoseGitLabTriggerAccount_NotInstalledAndDeferred(t *testing.T) {
	e := newTrigEnv()
	got := e.diagnose(nil, "")
	assert.Equal(t, GitLabTriggerStateNotInstalled, got.State)

	got = e.diagnose(e.f, trigBranch)
	assert.Equal(t, GitLabTriggerStateNotInstalled, got.State)

	e.f.exactRule = false
	got = e.diagnose(e.f, trigBranch)
	assert.Equal(t, GitLabTriggerStateDeferred, got.State)
	require.Len(t, got.Reasons, 1)
	assert.Contains(t, got.Reasons[0], "exact-name protected-branch rule")

	e.f.exactRule = true
	e.f.saDisabled = true
	got = e.diagnose(e.f, trigBranch)
	assert.Equal(t, GitLabTriggerStateDeferred, got.State)
	assert.Contains(t, got.Reasons[0], "service accounts are unavailable")

	e.f.saDisabled = false
	e.f.fail["HasExactProtectedBranchRule"] = true
	got = e.diagnose(e.f, trigBranch)
	assert.Equal(t, GitLabTriggerStateNotInstalled, got.State)
	assert.Contains(t, got.Reasons[len(got.Reasons)-1], "could not check")
}

// With the dispatch activation recorded, a managed schedule the trigger
// account does not own runs with its owner's permissions, so status reports it
// instead of a ready account. The dispatch gate must not, because the
// schedules move only after it passes (#8602).
func TestDiagnoseGitLabTriggerAccount_ScheduleOwnershipAfterActivation(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.readyActive(t)
	spec := trigSpec(t, trigSlashDesc)
	id := e.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)

	got := e.diagnose(e.f, trigBranch)
	assert.Equal(t, GitLabTriggerStateDrifted, got.State)
	require.Len(t, got.Reasons, 1)
	assert.Contains(t, got.Reasons[0], fmt.Sprintf("managed polling schedule %d", id))
	assert.Contains(t, got.Reasons[0], "not the trigger account")
	assert.Contains(t, got.Reasons[0], "re-run repos install")
	assert.Equal(t, GitLabTriggerStateReady, diagnoseGitLabTriggerAccount(ctx, e.state, e.f, trigOwner, trigRepo, trigBranch, true).State, "the dispatch gate ignores schedule ownership")
	assert.Equal(t, GitLabTriggerStateReady, e.diagnose(nil, "").State, "the ledger-only diagnosis makes no live reads")

	e.state.Errors = map[string]error{"ListPipelineSchedules": errTrigInjected}
	got = e.diagnose(e.f, trigBranch)
	assert.Equal(t, GitLabTriggerStateDrifted, got.State)
	assert.Contains(t, strings.Join(got.Reasons, "\n"), "owner of the managed polling schedules is unverified")
	e.state.Errors = nil

	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Equal(t, GitLabTriggerStateReady, e.diagnose(e.f, trigBranch).State)

	// Without a recorded activation the schedules belong to the installer.
	e2 := newTrigEnv()
	e2.readyActive(t)
	e2.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)
	require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e2.opts(), false))
	assert.Equal(t, GitLabTriggerStateReady, e2.diagnose(e2.f, trigBranch).State)
}

// Install refuses to adopt a managed schedule whose owner is not a verified
// installer Maintainer, so status must not tell the operator that re-running it
// repairs the drift.
func TestDiagnoseGitLabTriggerAccount_ScheduleOwnershipGuidance(t *testing.T) {
	ctx := context.Background()
	spec := trigSpec(t, trigSlashDesc)
	setup := func(owner int64) (*trigEnv, int64) {
		e := newTrigEnv()
		e.readyActive(t)
		id := e.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)
		for i := range e.state.PipelineSchedules[trigOwner+"/"+trigRepo] {
			if e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].ID == id {
				e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].OwnerID = owner
			}
		}
		return e, id
	}

	e, _ := setup(77)
	e.f.members[77] = forge.GitLabAccessLevelDeveloper
	got := e.diagnose(e.f, trigBranch)
	assert.Equal(t, GitLabTriggerStateDrifted, got.State)
	require.Len(t, got.Reasons, 1)
	assert.Contains(t, got.Reasons[0], "owned by user 77")
	assert.Contains(t, got.Reasons[0], "install refuses to adopt it")
	assert.Contains(t, got.Reasons[0], "administrator must remove or reassign")
	assert.NotContains(t, got.Reasons[0], "move it to the trigger account")
	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.ErrorContains(t, err, "refusing adoption", "status guidance agrees with convergence")

	// A Maintainer other than the installer is adoptable.
	e, _ = setup(78)
	e.f.members[78] = forge.GitLabAccessLevelMaintainer
	got = e.diagnose(e.f, trigBranch)
	require.Len(t, got.Reasons, 1)
	assert.Contains(t, got.Reasons[0], "re-run repos install to move it")

	// A failed installer lookup is reported as unverified, not as adoptable.
	e, _ = setup(79)
	e.f.fail["GetAuthenticatedUserID"] = true
	got = e.diagnose(e.f, trigBranch)
	require.Len(t, got.Reasons, 1)
	assert.Contains(t, got.Reasons[0], "could not verify the installer")
}

func TestDiagnoseGitLabTriggerAccount_ReadyAndDrift(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res)
	require.True(t, e.sinkPresent())

	assert.Equal(t, GitLabTriggerStateReady, e.diagnose(e.f, trigBranch).State)
	assert.Equal(t, GitLabTriggerStateReady, e.diagnose(nil, "").State)

	// A different default branch is a deferral with the recovery path.
	got := e.diagnose(e.f, "develop")
	assert.Equal(t, GitLabTriggerStateDeferred, got.State)
	assert.Contains(t, got.Reasons[0], "Run repos uninstall")

	drifts := map[string]struct {
		mutate func(e *trigEnv, res GitLabTriggerAccountResult)
		want   string
	}{
		"sink missing": {func(e *trigEnv, _ GitLabTriggerAccountResult) { e.setSink(false) }, forge.SecretGitLabTriggerAccountToken + " is missing"},
		"sink value copied": {func(e *trigEnv, _ GitLabTriggerAccountResult) {
			e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.SecretGitLabTriggerAccountToken] = "another-trigger-bearer"
		}, "does not match its recorded fingerprint"},
		"legacy digest missing": {func(e *trigEnv, _ GitLabTriggerAccountResult) {
			st, _ := e.ledger(t)
			st.TriggerTokenDigest = ""
			e.setLedger(t, st)
		}, "predates value binding"},
		"key missing":     {func(e *trigEnv, res GitLabTriggerAccountResult) { delete(e.f.keys, res.DeployKeyID) }, "deploy key"},
		"trigger missing": {func(e *trigEnv, res GitLabTriggerAccountResult) { delete(e.f.triggers, res.TriggerTokenID) }, "trigger token"},
		"account elevated": {func(e *trigEnv, res GitLabTriggerAccountResult) {
			e.f.members[res.AccountID] = forge.GitLabAccessLevelMaintainer
		}, "access level 40"},
		"grant missing":   {func(e *trigEnv, _ GitLabTriggerAccountResult) { e.f.pushes = nil }, "push entry"},
		"rule not exact":  {func(e *trigEnv, _ GitLabTriggerAccountResult) { e.f.exactRule = false }, "exact-name protected-branch rule"},
		"account deleted": {func(e *trigEnv, res GitLabTriggerAccountResult) { delete(e.f.accounts, res.AccountID) }, "no longer exists"},
	}
	for name, tc := range drifts {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			tc.mutate(e, res)
			got := e.diagnose(e.f, trigBranch)
			assert.Equal(t, GitLabTriggerStateDrifted, got.State)
			assert.Contains(t, got.Reasons[0], tc.want)
		})
	}
}

func TestDiagnoseGitLabTriggerAccount_Obligations(t *testing.T) {
	e := newTrigEnv()
	e.setLedger(t, gitLabTriggerAccountState{
		Version: 1, Phase: gitLabTriggerPhaseRemoving, Branch: trigBranch, Nonce: "00112233445566aa",
		AccountID: 5, Elevated: true, PATPending: true, DeployKeyID: 6, DeployKeyFingerprint: "SHA256:x", GrantEntryID: 7, TriggerTokenID: 8, TriggerPublished: true, PublishStarted: true,
	})
	got := e.diagnose(nil, "")
	assert.Equal(t, GitLabTriggerStateCleanup, got.State)
	joined := ""
	for _, r := range got.Reasons {
		joined += r + "\n"
	}
	for _, want := range []string{"service account 5", "Maintainer", "bootstrap personal access token", "deploy key 6", "push entry 7", "trigger token 8", "re-run repos uninstall"} {
		assert.Contains(t, joined, want)
	}

	st, _ := e.ledger(t)
	st.Phase = gitLabTriggerPhaseRotating
	e.setLedger(t, st)
	assert.Equal(t, GitLabTriggerStateInProgress, e.diagnose(nil, "").State)

	st.Phase = gitLabTriggerPhaseReady
	e.setLedger(t, st)
	got = e.diagnose(nil, "")
	assert.Equal(t, GitLabTriggerStateDrifted, got.State)
	assert.Contains(t, got.Reasons[0], "bootstrap elevation obligation")

	e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.VarGitLabTriggerAccount] = " "
	assert.Equal(t, GitLabTriggerStateUnreadable, e.diagnose(nil, "").State)
	e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.VarGitLabTriggerAccount] = "{"
	assert.Equal(t, GitLabTriggerStateUnreadable, e.diagnose(nil, "").State)
	e.state.Errors = map[string]error{"GetRepoVariable": errTrigInjected}
	assert.Equal(t, GitLabTriggerStateUnreadable, e.diagnose(nil, "").State)
}

func TestApplyGitLabTriggerAccountStatus(t *testing.T) {
	st := &RepoStatus{Drifts: []Drift{{Field: "gitlab-trigger-account", Expected: "ready", Actual: "stale"}}}
	ApplyGitLabTriggerAccountStatus(st, GitLabTriggerAccountStatus{State: GitLabTriggerStateReady})
	assert.Empty(t, st.Drifts, "a stale trigger-account drift is replaced")
	for _, state := range []string{GitLabTriggerStateNotInstalled, GitLabTriggerStateDeferred} {
		ApplyGitLabTriggerAccountStatus(st, GitLabTriggerAccountStatus{State: state, Reasons: []string{"why"}})
		assert.Empty(t, st.Drifts, "%s is informational", state)
		require.NotNil(t, st.GitLabTriggerAccount)
	}
	ApplyGitLabTriggerAccountStatus(st, GitLabTriggerAccountStatus{State: GitLabTriggerStateCleanup, Reasons: []string{"a", "b"}})
	require.Len(t, st.Drifts, 1)
	assert.Equal(t, "cleanup-pending: a; b", st.Drifts[0].Actual)
}

// A default-branch change explains the deferral but does not hide unfinished
// retirement, missing or unsafe resources or excess account access on the
// recorded branch: those keep the drift classification.
func TestDiagnoseGitLabTriggerAccount_BranchChangeKeepsIndependentProblems(t *testing.T) {
	ctx := context.Background()
	const newBranch = "develop"

	problems := map[string]struct {
		mutate func(t *testing.T, e *trigEnv, res GitLabTriggerAccountResult)
		want   string
	}{
		"unfinished retirement": {func(t *testing.T, e *trigEnv, _ GitLabTriggerAccountResult) {
			st, ok := e.ledger(t)
			require.True(t, ok)
			st.Retired = []gitLabTriggerAccountState{{Phase: gitLabTriggerPhaseReady, Branch: trigBranch, Nonce: "00112233445566bb", AccountID: 77}}
			e.setLedger(t, st)
		}, "retirement of the previous trigger account generation"},
		"sink unsafe": {func(_ *testing.T, e *trigEnv, _ GitLabTriggerAccountResult) {
			e.state.SecretProtections = map[string]forge.SecretProtection{trigSinkKey: {Exists: true, Protected: true}} // unmasked
		}, "is not masked"},
		"sink missing": {func(_ *testing.T, e *trigEnv, _ GitLabTriggerAccountResult) {
			e.setSink(false)
		}, forge.SecretGitLabTriggerAccountToken + " is missing"},
		"trigger missing": {func(_ *testing.T, e *trigEnv, res GitLabTriggerAccountResult) {
			delete(e.f.triggers, res.TriggerTokenID)
		}, "trigger token"},
		"account elevated": {func(_ *testing.T, e *trigEnv, res GitLabTriggerAccountResult) {
			e.f.members[res.AccountID] = forge.GitLabAccessLevelMaintainer
		}, "access level 40"},
		"grant missing": {func(_ *testing.T, e *trigEnv, _ GitLabTriggerAccountResult) { e.f.pushes = nil }, "push entry"},
	}
	for name, tc := range problems {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			res, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
			require.NoError(t, err)
			tc.mutate(t, e, res)

			got := e.diagnose(e.f, newBranch)
			assert.Equal(t, GitLabTriggerStateDrifted, got.State)
			require.NotEmpty(t, got.Reasons)
			assert.Contains(t, got.Reasons[0], "Run repos uninstall", "the branch-change explanation is kept")
			assert.Contains(t, strings.Join(got.Reasons, "; "), tc.want)
		})
	}

	t.Run("nothing else wrong stays deferred", func(t *testing.T) {
		e := newTrigEnv()
		_, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
		require.NoError(t, err)
		got := e.diagnose(e.f, newBranch)
		assert.Equal(t, GitLabTriggerStateDeferred, got.State)
		assert.Len(t, got.Reasons, 1)
	})
}

// The published value is removed before its token is revoked, in rotation and
// in uninstall, so the sink never holds a value for a dead token.
func TestGitLabTriggerAccount_SinkClearedWithTrigger(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	require.True(t, e.sinkPresent())
	oldValue := e.f.published[res.TriggerTokenID]

	res2, err := RotateGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res2)
	assert.NotEqual(t, res.TriggerTokenID, res2.TriggerTokenID)
	assert.NotEqual(t, oldValue, e.f.published[res2.TriggerTokenID])
	assert.True(t, e.sinkPresent())

	require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
	e.assertRemoved(t)
	assert.False(t, e.sinkPresent(), "uninstall removes the published token")

	// A failing unpublish keeps the ledger obligation but still revokes the
	// trigger, so a persistent delete failure leaves no live credential.
	e = newTrigEnv()
	_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.f.fail["Unpublish"] = true
	require.Error(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
	_, ok := e.ledger(t)
	assert.True(t, ok, "the ledger stays")
	assert.Equal(t, 0, e.managedTriggers(), "the trigger is revoked even though its value could not be removed")
	require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
	e.assertRemoved(t)
}
