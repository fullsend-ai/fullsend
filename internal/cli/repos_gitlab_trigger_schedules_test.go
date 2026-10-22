package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// scheduleOwnerAPI implements the trigger-account API calls the schedule
// hand-back makes; any other call panics through the nil embedded interface.
type scheduleOwnerAPI struct {
	repos.GitLabTriggerAccountAPI
	accountID int
	owned     []int
}

func (a scheduleOwnerAPI) ListProjectServiceAccounts(context.Context, string, string) ([]repos.GitLabServiceAccount, error) {
	return []repos.GitLabServiceAccount{{ID: a.accountID, Name: "fullsend-trigger-0123456789abcdef"}}, nil
}

func (a scheduleOwnerAPI) AccountScheduleIDs(context.Context, string, string, int) ([]int, error) {
	return a.owned, nil
}

func (a scheduleOwnerAPI) GetAuthenticatedUserID(context.Context) (int, error) {
	return 42, nil
}

type scheduleOwnerState struct{ *forge.FakeClient }

func (s *scheduleOwnerState) CreatePipelineScheduleWithActive(ctx context.Context, owner, repo, ref, description, cron string, variables map[string]string, active bool) (int64, error) {
	id, err := s.FakeClient.CreatePipelineScheduleWithActive(ctx, owner, repo, ref, description, cron, variables, active)
	if err == nil {
		for i := range s.PipelineSchedules[owner+"/"+repo] {
			if s.PipelineSchedules[owner+"/"+repo][i].ID == id {
				s.PipelineSchedules[owner+"/"+repo][i].OwnerID = 42
			}
		}
	}
	return id, err
}

// A failed activation write has withdrawn the activation, so the account-owned
// schedules go back to the installer and the original error is still returned.
func TestRecordGitLabTriggerDispatchActivation_FailureHandsSchedulesBack(t *testing.T) {
	ctx := context.Background()
	client := &scheduleOwnerState{forge.NewFakeClient()}
	// No trigger token is recorded, so the activation cannot be written.
	client.VariableValues["g/p/"+forge.VarGitLabTriggerAccount] = `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","account_id":7}`
	id, err := client.CreatePipelineSchedule(ctx, "g", "p", "main", "fullsend slash poll", "*/5 * * * *", nil)
	require.NoError(t, err)
	client.PipelineSchedules["g/p"][0].OwnerID = 7
	lease, release, err := repos.LockGitLabProjectLease(ctx, client, "g", "p")
	require.NoError(t, err)
	defer release(&err)

	opts := repos.GitLabTriggerAccountOptions{
		Owner: "g", Repo: "p", Branch: "main",
		Lease: lease,
		API:   scheduleOwnerAPI{accountID: 7, owned: []int{int(id)}},
		State: client,
	}
	var buf bytes.Buffer
	err = recordGitLabTriggerDispatchActivation(ctx, opts, ui.New(&buf), "g/p", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dispatch cannot be activated")
	assert.Contains(t, buf.String(), "Recording the GitLab trigger dispatch activation failed")
	assert.Contains(t, buf.String(), "GitLab polling schedules are owned by the installer (1 created, 1 removed)")
	assert.Contains(t, client.DeletedScheduleIDs, id)

	// Success records nothing to hand back.
	buf.Reset()
	require.NoError(t, recordGitLabTriggerDispatchActivation(ctx, opts, ui.New(&buf), "g/p", false))
	assert.Empty(t, buf.String())
	err = nil
}

func TestReconcileGitLabTriggerScheduleOwnership_HandsBackAndReports(t *testing.T) {
	ctx := context.Background()
	client := &scheduleOwnerState{forge.NewFakeClient()}
	client.VariableValues["g/p/"+forge.VarGitLabTriggerAccount] = `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","account_id":7}`
	id, err := client.CreatePipelineSchedule(ctx, "g", "p", "main", "fullsend slash poll", "*/5 * * * *", nil)
	require.NoError(t, err)
	client.PipelineSchedules["g/p"][0].OwnerID = 7
	lease, release, err := repos.LockGitLabProjectLease(ctx, client, "g", "p")
	require.NoError(t, err)
	defer release(&err)

	opts := repos.GitLabTriggerAccountOptions{
		Owner: "g", Repo: "p", Branch: "main",
		Lease: lease,
		API:   scheduleOwnerAPI{accountID: 7, owned: []int{int(id)}},
		State: client,
	}
	var buf bytes.Buffer
	require.NoError(t, reconcileGitLabTriggerScheduleOwnership(ctx, opts, ui.New(&buf), "g/p", false))
	assert.Contains(t, buf.String(), "GitLab polling schedules are owned by the installer (1 created, 1 removed)")
	assert.Contains(t, client.DeletedScheduleIDs, id)

	// Nothing left to move: nothing is reported.
	buf.Reset()
	opts.API = scheduleOwnerAPI{accountID: 7}
	require.NoError(t, reconcileGitLabTriggerScheduleOwnership(ctx, opts, ui.New(&buf), "g/p", false))
	assert.Empty(t, buf.String())

	// Without the lease nothing runs and the failure is reported.
	buf.Reset()
	opts.Lease = nil
	err = reconcileGitLabTriggerScheduleOwnership(ctx, opts, ui.New(&buf), "g/p", true)
	require.Error(t, err)
	assert.Contains(t, buf.String(), "Reconciling the owner of the GitLab polling schedules failed")
	err = nil
}

// A transfer interrupted after the account's schedules were created (the
// installer-owned originals were not deleted) still warns, because a cleanup
// retry creates nothing and would not warn (#8543).
func TestReportGitLabTriggerScheduleReconcile_InterruptedTransferWarns(t *testing.T) {
	deleteErr := errors.New("deleting the installer-owned schedule failed")
	var buf bytes.Buffer
	err := reportGitLabTriggerScheduleReconcile(ui.New(&buf), "g/p", repos.GitLabTriggerSchedulesResult{Created: []int64{11, 12}}, deleteErr, true)
	require.ErrorIs(t, err, deleteErr)
	out := buf.String()
	assert.Contains(t, out, "Reconciling the owner of the GitLab polling schedules failed")
	assert.Contains(t, out, "[g/p] repos install and repos uninstall for this project now require "+gitlabTriggerScheduleMinInstallerCLI)

	// Nothing was created, or the run was a hand-back: no warning.
	for name, tc := range map[string]struct {
		res    repos.GitLabTriggerSchedulesResult
		active bool
	}{
		"nothing created":       {active: true},
		"hand-back interrupted": {res: repos.GitLabTriggerSchedulesResult{Created: []int64{11}}},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			require.ErrorIs(t, reportGitLabTriggerScheduleReconcile(ui.New(&buf), "g/p", tc.res, deleteErr, tc.active), deleteErr)
			assert.NotContains(t, buf.String(), "now require")
		})
	}

	// The cleanup-only retry succeeds without created schedules and stays quiet
	// about the warning the interrupted run already printed.
	buf.Reset()
	require.NoError(t, reportGitLabTriggerScheduleReconcile(ui.New(&buf), "g/p", repos.GitLabTriggerSchedulesResult{Removed: []int64{1, 2}}, nil, true))
	assert.Contains(t, buf.String(), "owned by the trigger account (0 created, 2 removed)")
	assert.NotContains(t, buf.String(), "now require")
}

// A transfer to the trigger account warns about the minimum installer CLI and
// the upgrade order; a hand-back or a run that created nothing does not.
func TestPrintGitLabTriggerScheduleOwnership_TransferWarnsAboutMinimumCLI(t *testing.T) {
	var buf bytes.Buffer
	printGitLabTriggerScheduleOwnership(ui.New(&buf), "g/p", repos.GitLabTriggerSchedulesResult{Created: []int64{11, 12}, Removed: []int64{1, 2}}, true)
	out := buf.String()
	assert.Contains(t, out, "GitLab polling schedules are owned by the trigger account (2 created, 2 removed)")
	assert.Contains(t, out, "[g/p] repos install and repos uninstall for this project now require "+gitlabTriggerScheduleMinInstallerCLI)
	assert.Contains(t, out, "(this CLI, "+version+", qualifies)")
	assert.Contains(t, out, "earlier CLIs treat the trigger-account-owned schedules as foreign")
	assert.Contains(t, out, "Upgrade every CLI that installs or uninstalls this project before its next run")
	assert.Contains(t, out, "first use a current CLI to hand the schedules back to the installer")

	for name, tc := range map[string]struct {
		res    repos.GitLabTriggerSchedulesResult
		active bool
		want   string
	}{
		"hand-back":            {res: repos.GitLabTriggerSchedulesResult{Created: []int64{11}, Removed: []int64{1}}, want: "owned by the installer (1 created, 1 removed)"},
		"cleanup only":         {res: repos.GitLabTriggerSchedulesResult{Removed: []int64{1}}, active: true, want: "owned by the trigger account (0 created, 1 removed)"},
		"nothing moved active": {active: true},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			printGitLabTriggerScheduleOwnership(ui.New(&buf), "g/p", tc.res, tc.active)
			if tc.want == "" {
				assert.Empty(t, buf.String())
			} else {
				assert.Contains(t, buf.String(), tc.want)
			}
			assert.NotContains(t, buf.String(), "now require")
		})
	}
}
