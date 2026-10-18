package repos

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// CreatePipelineSchedule creates a schedule owned by the account that holds
// the bootstrap PAT. The account's access level at creation is counted so
// tests can prove it was never elevated for it. A disabled schedule is
// created disabled; the call is counted so tests can prove the request carried
// that state.
func (b trigBootstrapClient) CreatePipelineSchedule(ctx context.Context, owner, repo, ref, description, cron string, active bool) (int64, error) {
	ok, err := b.f.enter("CreatePipelineScheduleAsAccount")
	if !ok {
		return 0, err
	}
	level, member := b.f.members[b.userID]
	if !member {
		return 0, forge.ErrForbidden
	}
	b.f.calls[fmt.Sprintf("CreatePipelineScheduleAtLevel%d", level)]++
	if !active {
		b.f.calls["CreatePipelineScheduleDisabled"]++
	}
	id, cErr := b.f.state.CreatePipelineScheduleWithActive(ctx, owner, repo, ref, description, cron, nil, active || b.f.createIgnoresActive)
	if cErr != nil {
		return 0, cErr
	}
	for i := range b.f.state.PipelineSchedules[owner+"/"+repo] {
		if b.f.state.PipelineSchedules[owner+"/"+repo][i].ID == id {
			b.f.state.PipelineSchedules[owner+"/"+repo][i].OwnerID = int64(b.userID)
		}
	}
	if b.f.calls["ScheduleOwnerUnreported"] == 0 {
		b.f.schedules[b.userID] = append(b.f.schedules[b.userID], int(id))
	}
	return id, err
}

func (trigBadBootstrap) CreatePipelineSchedule(context.Context, string, string, string, string, string, bool) (int64, error) {
	return 0, nil
}

func (trigCanceledBootstrap) CreatePipelineSchedule(context.Context, string, string, string, string, string, bool) (int64, error) {
	return 0, context.Canceled
}

const (
	trigSlashDesc = "fullsend slash poll"
	trigEventDesc = "fullsend event poll"
)

func trigSpec(t *testing.T, desc string) ScheduleSpec {
	t.Helper()
	for _, s := range PipelineScheduleSpecs() {
		if s.Description == desc {
			return s
		}
	}
	t.Fatalf("no spec %q", desc)
	return ScheduleSpec{}
}

// readyActive converges the trigger account and records the dispatch
// activation, returning the account ID.
func (e *trigEnv) readyActive(t *testing.T) int {
	t.Helper()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	require.True(t, res.Ready)
	require.NoError(t, SetGitLabTriggerDispatchActive(context.Background(), e.opts(), true))
	return res.AccountID
}

// adminSchedule creates an installer-owned schedule.
func (e *trigEnv) adminSchedule(t *testing.T, desc, ref, cron string, active bool) int64 {
	t.Helper()
	id, err := e.state.CreatePipelineSchedule(context.Background(), trigOwner, trigRepo, ref, desc, cron, nil)
	require.NoError(t, err)
	for i := range e.state.PipelineSchedules[trigOwner+"/"+trigRepo] {
		if e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].ID == id {
			e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].OwnerID = 42
		}
	}
	if !active {
		require.NoError(t, e.state.UpdatePipelineSchedule(context.Background(), trigOwner, trigRepo, id, false))
	}
	return id
}

// accountSchedule creates a schedule owned by the account.
func (e *trigEnv) accountSchedule(t *testing.T, acct int, desc, ref, cron string, vars map[string]string) int64 {
	t.Helper()
	id, err := e.state.CreatePipelineSchedule(context.Background(), trigOwner, trigRepo, ref, desc, cron, vars)
	require.NoError(t, err)
	for i := range e.state.PipelineSchedules[trigOwner+"/"+trigRepo] {
		if e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].ID == id {
			e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].OwnerID = int64(acct)
		}
	}
	e.f.schedules[acct] = append(e.f.schedules[acct], int(id))
	return id
}

func (e *trigEnv) liveSchedules(t *testing.T) []forge.PipelineSchedule {
	t.Helper()
	s, err := e.state.ListPipelineSchedules(context.Background(), trigOwner, trigRepo)
	require.NoError(t, err)
	return s
}

func (e *trigEnv) ownedBy(t *testing.T, acct int) map[int64]bool {
	t.Helper()
	ids, err := e.f.AccountScheduleIDs(context.Background(), trigOwner, trigRepo, acct)
	require.NoError(t, err)
	out := map[int64]bool{}
	for _, id := range ids {
		out[int64(id)] = true
	}
	return out
}

// assertAccountOwnsManaged checks that exactly one schedule per spec exists,
// owned by acct, on the branch with the spec's cron and no variables.
func (e *trigEnv) assertAccountOwnsManaged(t *testing.T, acct int) map[string]forge.PipelineSchedule {
	t.Helper()
	owned := e.ownedBy(t, acct)
	byDesc := map[string]forge.PipelineSchedule{}
	for _, s := range e.liveSchedules(t) {
		if !isManagedPipelineSchedule(s.Description) {
			continue
		}
		_, dup := byDesc[s.Description]
		require.False(t, dup, "duplicate %q schedule remains", s.Description)
		byDesc[s.Description] = s
		assert.True(t, owned[s.ID], "schedule %d (%q) is owned by the trigger account", s.ID, s.Description)
		assert.True(t, scheduleRefIsBranch(s.Ref, trigBranch))
		assert.Equal(t, trigSpec(t, s.Description).Cron, s.Cron)
		assert.Empty(t, s.Variables)
	}
	return byDesc
}

func (e *trigEnv) assertNoBootstrapResidue(t *testing.T, acct int) {
	t.Helper()
	assert.Zero(t, e.activePATs(), "schedule PAT revoked")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[acct], "account stays at Developer")
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.Zero(t, st.PATID)
	assert.False(t, st.PATPending)
	assert.False(t, st.Elevated)
	assert.Equal(t, gitLabTriggerPhaseReady, st.Phase)
}

func TestConvergeGitLabTriggerSchedules_AdoptsAdminOwnedSchedules(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	event := trigSpec(t, trigEventDesc)
	adminSlash := e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)
	adminEvent := e.adminSchedule(t, event.Description, trigBranch, event.Cron, false)
	other := e.adminSchedule(t, "nightly build", trigBranch, "0 0 * * *", true)

	res, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Len(t, res.Created, 2)
	assert.ElementsMatch(t, []int64{adminSlash, adminEvent}, res.Removed)

	byDesc := e.assertAccountOwnsManaged(t, acct)
	require.Len(t, byDesc, 2)
	assert.True(t, byDesc[slash.Description].Active)
	assert.False(t, byDesc[event.Description].Active, "a disabled schedule stays disabled under the account")
	assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == other }), "unrelated schedule untouched")
	assert.Equal(t, 2, e.f.calls[fmt.Sprintf("CreatePipelineScheduleAtLevel%d", forge.GitLabAccessLevelDeveloper)], "schedules created while the account is a Developer")
	assert.Zero(t, e.f.calls[fmt.Sprintf("CreatePipelineScheduleAtLevel%d", forge.GitLabAccessLevelMaintainer)])
	e.assertNoBootstrapResidue(t, acct)

	// The dispatch activation stays bound to the ledger generation.
	op, err := loadGitLabTriggerAccountOp(context.Background(), e.opts())
	require.NoError(t, err)
	active, err := op.dispatchActivationRecorded(context.Background())
	require.NoError(t, err)
	assert.True(t, active)

	// Re-running is a no-op: no new PAT, no create, no delete.
	pats := e.f.calls["CreateServiceAccountPAT"]
	res, err = ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Empty(t, res.Created)
	assert.Empty(t, res.Removed)
	assert.Equal(t, pats, e.f.calls["CreateServiceAccountPAT"])

	// A later install accepts the account-owned managed schedules.
	again, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	assert.True(t, again.Ready)
}

func TestConvergeGitLabTriggerSchedules_LeavesMissingSpecToConvergeSchedules(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)

	res, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Len(t, res.Created, 1)
	byDesc := e.assertAccountOwnsManaged(t, acct)
	assert.Contains(t, byDesc, slash.Description)
	assert.NotContains(t, byDesc, trigEventDesc, "no schedule is created for a spec with no live schedule")

	// With no managed schedule at all, no PAT is minted.
	e2 := newTrigEnv()
	e2.readyActive(t)
	pats := e2.f.calls["CreateServiceAccountPAT"]
	res, err = ConvergeGitLabTriggerSchedules(context.Background(), e2.opts())
	require.NoError(t, err)
	assert.Empty(t, res.Created)
	assert.Equal(t, pats, e2.f.calls["CreateServiceAccountPAT"])
}

func TestConvergeGitLabTriggerSchedules_RepairsDrift(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	event := trigSpec(t, trigEventDesc)
	wrongCron := e.accountSchedule(t, acct, slash.Description, trigBranch, "0 * * * *", nil)
	withVars := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, map[string]string{forge.VarPollMode: "events"})
	wrongRef := e.accountSchedule(t, acct, event.Description, "feature", event.Cron, nil)

	res, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Len(t, res.Created, 2)
	assert.ElementsMatch(t, []int64{wrongCron, withVars, wrongRef}, res.Removed)
	e.assertAccountOwnsManaged(t, acct)
	e.assertNoBootstrapResidue(t, acct)
}

func TestConvergeGitLabTriggerSchedules_RemovesDuplicates(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	// An interrupted move left both the account's schedule and the original,
	// plus a duplicate account-owned schedule.
	keep := e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)
	dup := e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)
	orig := e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)

	pats := e.f.calls["CreateServiceAccountPAT"]
	res, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Empty(t, res.Created, "the in-sync account schedule is kept")
	assert.ElementsMatch(t, []int64{dup, orig}, res.Removed)
	assert.Equal(t, pats, e.f.calls["CreateServiceAccountPAT"], "no PAT when nothing is created")
	byDesc := e.assertAccountOwnsManaged(t, acct)
	assert.Equal(t, keep, byDesc[slash.Description].ID)
}

func TestConvergeGitLabTriggerSchedules_Refusals(t *testing.T) {
	ctx := context.Background()

	t.Run("lease", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.Lease = nil
		_, err := ConvergeGitLabTriggerSchedules(ctx, o)
		require.ErrorIs(t, err, errTriggerAccountUnleased)
		_, err = ReleaseGitLabTriggerSchedules(ctx, o)
		require.ErrorIs(t, err, errTriggerAccountUnleased)
	})
	t.Run("no bootstrap factory", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.Bootstrap = nil
		_, err := ConvergeGitLabTriggerSchedules(ctx, o)
		require.ErrorContains(t, err, "bootstrap client factory")
	})
	t.Run("unverified inventory", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.PollerInventoryVerified = false
		_, err := ConvergeGitLabTriggerSchedules(ctx, o)
		require.ErrorContains(t, err, "verified Poller identity inventory")
	})
	t.Run("no ledger", func(t *testing.T) {
		e := newTrigEnv()
		_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
		require.ErrorContains(t, err, "ready trigger account")
	})
	t.Run("branch change", func(t *testing.T) {
		e := newTrigEnv()
		e.readyActive(t)
		o := e.opts()
		o.Branch = "release"
		_, err := ConvergeGitLabTriggerSchedules(ctx, o)
		require.ErrorContains(t, err, "does not move grants between branches")
	})
	t.Run("account is a poller", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		e.pollers = []int{acct}
		_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
		require.ErrorContains(t, err, "also a Poller role identity")
	})
	t.Run("activation missing or stale", func(t *testing.T) {
		e := newTrigEnv()
		e.readyActive(t)
		slash := trigSpec(t, trigSlashDesc)
		admin := e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)
		require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), false))
		_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
		require.ErrorIs(t, err, errGitLabTriggerDispatchInactive)

		stale, err := json.Marshal(gitLabDispatchActivation{Version: gitLabDispatchActivationVersion, Nonce: "0000000000000000", AccountID: 1})
		require.NoError(t, err)
		e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.VarGitLabDispatchActivation] = string(stale)
		_, err = ConvergeGitLabTriggerSchedules(ctx, e.opts())
		require.ErrorIs(t, err, errGitLabTriggerDispatchInactive)

		e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.VarGitLabDispatchActivation] = "not json"
		_, err = ConvergeGitLabTriggerSchedules(ctx, e.opts())
		require.ErrorIs(t, err, errGitLabTriggerDispatchInactive)
		assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == admin }), "installer schedule untouched")
	})
	t.Run("foreign account schedule", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		e.accountSchedule(t, acct, "someone's schedule", trigBranch, "0 0 * * *", nil)
		_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
		require.ErrorContains(t, err, "that fullsend did not create")
	})
	t.Run("account deleted", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		delete(e.f.accounts, acct)
		_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
		require.ErrorContains(t, err, "no longer exists")
	})
}

func TestConvergeGitLabTriggerSchedules_FailureKeepsInstallerSchedules(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		setup func(e *trigEnv, o *GitLabTriggerAccountOptions)
		want  string
	}{
		{"pat create fails", func(e *trigEnv, _ *GitLabTriggerAccountOptions) {
			e.f.fail["CreateServiceAccountPAT"] = true
		}, "creating the GitLab trigger schedule token"},
		{"bootstrap auth fails", func(_ *trigEnv, o *GitLabTriggerAccountOptions) {
			o.Bootstrap = func(string) (GitLabTriggerBootstrapAPI, error) { return nil, errTrigInjected }
		}, "authenticating as the GitLab trigger account"},
		{"schedule create fails", func(e *trigEnv, _ *GitLabTriggerAccountOptions) {
			e.f.fail["CreatePipelineScheduleAsAccount"] = true
		}, "schedule as the GitLab trigger account"},
		{"schedule create returns no id", func(_ *trigEnv, o *GitLabTriggerAccountOptions) {
			o.Bootstrap = func(string) (GitLabTriggerBootstrapAPI, error) { return trigBadBootstrap{}, nil }
		}, "response carried no schedule ID"},
		{"schedule create canceled", func(_ *trigEnv, o *GitLabTriggerAccountOptions) {
			o.Bootstrap = func(string) (GitLabTriggerBootstrapAPI, error) { return trigCanceledBootstrap{}, nil }
		}, "schedule as the GitLab trigger account"},
		{"owner not reported", func(e *trigEnv, _ *GitLabTriggerAccountOptions) {
			e.f.calls["ScheduleOwnerUnreported"] = 1
		}, "is not reported as owned by GitLab trigger account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTrigEnv()
			acct := e.readyActive(t)
			slash := trigSpec(t, trigSlashDesc)
			event := trigSpec(t, trigEventDesc)
			adminSlash := e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)
			adminEvent := e.adminSchedule(t, event.Description, trigBranch, event.Cron, false)
			o := e.opts()
			tc.setup(e, &o)
			_, err := ConvergeGitLabTriggerSchedules(ctx, o)
			require.ErrorContains(t, err, tc.want)
			live := e.liveSchedules(t)
			for _, id := range []int64{adminSlash, adminEvent} {
				assert.True(t, slices.ContainsFunc(live, func(s forge.PipelineSchedule) bool { return s.ID == id }), "installer schedule %d kept", id)
			}
			e.assertNoBootstrapResidue(t, acct)

			// The next run converges.
			if tc.name == "owner not reported" {
				e.f.calls["ScheduleOwnerUnreported"] = 0
			}
			_, err = ConvergeGitLabTriggerSchedules(ctx, e.opts())
			require.NoError(t, err)
			byDesc := e.assertAccountOwnsManaged(t, acct)
			assert.True(t, byDesc[slash.Description].Active)
			assert.False(t, byDesc[event.Description].Active, "a disabled schedule is never left polling after a retry")
		})
	}
}

func TestConvergeGitLabTriggerSchedules_DisabledReplacementIsNeverCreatedActive(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	adminEvent := e.adminSchedule(t, event.Description, trigBranch, event.Cron, false)
	// Even if every later request failed, nothing could leave the replacement
	// active: it is created disabled and edited by no further request.
	e.state.Errors = map[string]error{"UpdatePipelineSchedule": errTrigInjected}
	before := len(e.state.CreatedSchedules)
	e.state.UpdatedScheduleIDs = nil

	res, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Equal(t, 1, e.f.calls["CreatePipelineScheduleDisabled"], "the create request carried the disabled state")
	assert.Equal(t, []int64{adminEvent}, res.Removed)
	require.Len(t, res.Created, 1)
	require.Len(t, e.state.CreatedSchedules[before:], 1)
	assert.False(t, e.state.CreatedSchedules[before].Active, "the replacement was never created active")
	assert.Empty(t, e.state.UpdatedScheduleIDs, "no separate request edits the replacement")
	byDesc := e.assertAccountOwnsManaged(t, acct)
	assert.False(t, byDesc[event.Description].Active)
}

func TestConvergeGitLabTriggerSchedules_RejectsMalformedActivation(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	admin := e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)
	key := trigOwner + "/" + trigRepo + "/" + forge.VarGitLabDispatchActivation
	valid := e.state.VariableValues[key]
	require.NotEmpty(t, valid)
	pats := e.f.calls["CreateServiceAccountPAT"]

	for name, raw := range map[string]string{
		"trailing content": valid + ` {"version":1}`,
		"duplicate key":    `{"version":1,` + valid[1:],
	} {
		t.Run(name, func(t *testing.T) {
			e.state.VariableValues[key] = raw
			_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
			require.ErrorIs(t, err, errGitLabTriggerDispatchInactive)
			assert.Equal(t, pats, e.f.calls["CreateServiceAccountPAT"], "no PAT is minted")
			live := e.liveSchedules(t)
			require.Len(t, live, 1)
			assert.Equal(t, admin, live[0].ID, "installer schedule untouched")
		})
	}
}

func TestConvergeGitLabTriggerSchedules_QualifiedRefIsInSync(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	for _, spec := range PipelineScheduleSpecs() {
		e.accountSchedule(t, acct, spec.Description, "refs/heads/"+trigBranch, spec.Cron, nil)
	}
	res, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Empty(t, res.Created)
	assert.Empty(t, res.Removed)
}

func TestConvergeGitLabTriggerSchedules_ReactivatesAccountOwnedSchedules(t *testing.T) {
	ctx := context.Background()
	slash := trigSpec(t, trigSlashDesc)
	setup := func(t *testing.T) (*trigEnv, int, int64) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		id := e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)
		require.NoError(t, e.state.UpdatePipelineSchedule(ctx, trigOwner, trigRepo, id, false))
		return e, acct, id
	}

	t.Run("left disabled by default", func(t *testing.T) {
		e, _, id := setup(t)
		res, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assert.Empty(t, res.Created)
		live := e.liveSchedules(t)
		require.Len(t, live, 1)
		assert.Equal(t, id, live[0].ID)
		assert.False(t, live[0].Active)
	})
	t.Run("replaced by an active schedule", func(t *testing.T) {
		e, acct, id := setup(t)
		o := e.opts()
		o.ReactivateSchedules = true
		res, err := ConvergeGitLabTriggerSchedules(ctx, o)
		require.NoError(t, err)
		assert.Len(t, res.Created, 1)
		assert.Equal(t, []int64{id}, res.Removed)
		byDesc := e.assertAccountOwnsManaged(t, acct)
		assert.True(t, byDesc[slash.Description].Active)
		e.assertNoBootstrapResidue(t, acct)
	})
	t.Run("hand-back keeps it active", func(t *testing.T) {
		e, acct, _ := setup(t)
		o := e.opts()
		o.ReactivateSchedules = true
		_, err := ReleaseGitLabTriggerSchedules(ctx, o)
		require.NoError(t, err)
		assert.Empty(t, e.ownedBy(t, acct))
		live := e.liveSchedules(t)
		require.Len(t, live, 1)
		assert.True(t, live[0].Active)
	})
}

func TestReleaseGitLabTriggerSchedules_DisabledCopyIsNeverCreatedActive(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	mine := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, nil)
	require.NoError(t, e.state.UpdatePipelineSchedule(ctx, trigOwner, trigRepo, mine, false))
	// Even if every later request failed, nothing could leave the copy active:
	// it is created disabled and edited by no further request.
	e.state.Errors["UpdatePipelineSchedule"] = errTrigInjected
	before := len(e.state.CreatedSchedules)
	e.state.UpdatedScheduleIDs = nil

	res, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	require.Len(t, res.Created, 1)
	require.Len(t, e.state.CreatedSchedules[before:], 1)
	assert.False(t, e.state.CreatedSchedules[before].Active, "the installer copy was never created active")
	assert.Empty(t, e.state.UpdatedScheduleIDs, "no separate request edits the copy")
	live := e.liveSchedules(t)
	require.Len(t, live, 1)
	assert.False(t, live[0].Active)
	assert.Empty(t, e.ownedBy(t, acct))
}

func TestConvergeGitLabTriggerSchedules_InterruptedDisableIsRecoveredOnRetry(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	adminEvent := e.adminSchedule(t, event.Description, trigBranch, event.Cron, false)

	// GitLab ignores the requested disabled state and the cleanup does not
	// complete (a lost response or a cancelled run), so the replacement stays
	// active beside the disabled original.
	e.ignoreActiveOnCreate(true)
	e.state.Errors = map[string]error{"DeletePipelineSchedule": errTrigInjected}
	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.Error(t, err)
	live := e.liveSchedules(t)
	require.Len(t, live, 2)
	replacement := live[1].ID
	require.NotEqual(t, adminEvent, replacement)
	require.True(t, live[1].Active)

	// The retry does not take the active replacement for in sync: the original
	// records that polling was disabled.
	e.ignoreActiveOnCreate(false)
	e.state.Errors = nil
	res, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Len(t, res.Created, 1)
	assert.ElementsMatch(t, []int64{adminEvent, replacement}, res.Removed)
	byDesc := e.assertAccountOwnsManaged(t, acct)
	require.Len(t, byDesc, 1)
	assert.False(t, byDesc[event.Description].Active, "a disabled schedule is never left polling after a retry")
	e.assertNoBootstrapResidue(t, acct)
}

func TestReleaseGitLabTriggerSchedules_InterruptedAdoptionKeepsDisabledState(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	adminEvent := e.adminSchedule(t, event.Description, trigBranch, event.Cron, false)

	// Adoption is interrupted after the replacement exists, and GitLab ignored
	// its disabled state: it stays active beside the disabled installer-owned
	// original.
	e.ignoreActiveOnCreate(true)
	e.state.Errors = map[string]error{"DeletePipelineSchedule": errTrigInjected}
	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.Error(t, err)
	e.ignoreActiveOnCreate(false)
	e.state.Errors = nil
	live := e.liveSchedules(t)
	require.Len(t, live, 2)
	replacement := live[1].ID
	require.True(t, live[1].Active)

	// Deferring activation hands the schedule back. The original records that
	// polling was disabled, so it stays disabled without --reactivate-schedules.
	res, err := ReconcileGitLabTriggerScheduleOwnership(ctx, e.opts(), false)
	require.NoError(t, err)
	assert.Empty(t, res.Created, "the disabled original is already a complete copy")
	assert.Equal(t, []int64{replacement}, res.Removed)
	assert.Empty(t, e.ownedBy(t, acct))
	live = e.liveSchedules(t)
	require.Len(t, live, 1)
	assert.Equal(t, adminEvent, live[0].ID)
	assert.False(t, live[0].Active, "polling stays disabled")
}

func TestReleaseGitLabTriggerSchedules_InterruptedCleanupStrandsNoInstallerOriginal(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	// An interrupted adoption: the installer-owned original (older, on a
	// noncanonical cron) beside the account's replacement.
	stale := e.adminSchedule(t, event.Description, trigBranch, "0 * * * *", true)
	mine := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, nil)

	// Hand-back creates a canonical copy, then its cleanup is cut short after
	// one deletion. The stale original goes first, so the account still owns a
	// schedule and the next run can finish.
	e.state.failDeleteAfter = 1
	_, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
	require.Error(t, err)
	live := e.liveSchedules(t)
	require.Len(t, live, 2)
	ids := []int64{live[0].ID, live[1].ID}
	assert.NotContains(t, ids, stale, "the stale installer original is deleted before the account's schedule")
	assert.Contains(t, ids, mine, "the account keeps a schedule for the retry to find")

	e.state.failDeleteAfter = 0
	res, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Equal(t, []int64{mine}, res.Removed)
	assert.Empty(t, e.ownedBy(t, acct))
	live = e.liveSchedules(t)
	require.Len(t, live, 1)
	assert.Equal(t, event.Cron, live[0].Cron)
	assert.True(t, live[0].Active)
}

func TestReleaseGitLabTriggerSchedules_InterruptedCleanupOfDisabledStaleOriginalKeepsDisabledState(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	// An interrupted adoption: a disabled installer-owned original on a
	// noncanonical cron beside the account's active replacement.
	stale := e.adminSchedule(t, event.Description, trigBranch, "0 * * * *", false)
	mine := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, nil)

	// Hand-back creates a disabled copy, then its cleanup is cut short after the
	// stale original, the record of the disabled state, is deleted.
	e.state.failDeleteAfter = 1
	_, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
	require.Error(t, err)
	live := e.liveSchedules(t)
	require.Len(t, live, 2)
	assert.Equal(t, mine, live[0].ID)
	assert.True(t, live[0].Active)
	assert.NotEqual(t, stale, live[1].ID)
	assert.False(t, live[1].Active)
	copyID := live[1].ID

	// The retry keeps the disabled copy instead of re-enabling polling.
	e.state.failDeleteAfter = 0
	res, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Empty(t, res.Created, "the disabled copy is already complete")
	assert.Equal(t, []int64{mine}, res.Removed)
	assert.Empty(t, e.ownedBy(t, acct))
	live = e.liveSchedules(t)
	require.Len(t, live, 1)
	assert.Equal(t, copyID, live[0].ID)
	assert.False(t, live[0].Active, "polling stays disabled")
}

func TestConvergeGitLabTriggerSchedules_InterruptedHandBackKeepsDisabledState(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	// A disabled schedule the account owns.
	mine := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, nil)
	require.NoError(t, e.state.UpdatePipelineSchedule(ctx, trigOwner, trigRepo, mine, false))

	// Hand-back is interrupted after its copy exists: GitLab ignored the copy's
	// disabled state and the cleanup fails, leaving an active installer copy.
	e.ignoreActiveOnCreate(true)
	e.state.Errors = map[string]error{"DeletePipelineSchedule": errTrigInjected}
	_, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
	require.Error(t, err)
	e.ignoreActiveOnCreate(false)
	e.state.Errors = nil
	live := e.liveSchedules(t)
	require.Len(t, live, 2)
	copyID := live[1].ID
	require.True(t, live[1].Active)

	// Moving the schedule back to the account must not take the newer, active
	// copy for the record of the intended state.
	res, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Empty(t, res.Created, "the disabled account-owned schedule is already in sync")
	assert.Equal(t, []int64{copyID}, res.Removed)
	byDesc := e.assertAccountOwnsManaged(t, acct)
	require.Len(t, byDesc, 1)
	assert.Equal(t, mine, byDesc[event.Description].ID)
	assert.False(t, byDesc[event.Description].Active, "a disabled schedule is never re-enabled by an interrupted hand-back")
	e.assertNoBootstrapResidue(t, acct)
}

func TestReleaseGitLabTriggerSchedules_DriftedRefIsNotCopied(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	// The account's schedule was pointed at another ref.
	mine := e.accountSchedule(t, acct, event.Description, "feature", event.Cron, nil)

	res, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Len(t, res.Created, 1)
	assert.Equal(t, []int64{mine}, res.Removed)
	assert.Empty(t, e.ownedBy(t, acct))
	live := e.liveSchedules(t)
	require.Len(t, live, 1)
	assert.True(t, scheduleRefIsBranch(live[0].Ref, trigBranch), "the installer's copy runs on the recorded branch")
	assert.Equal(t, event.Cron, live[0].Cron)
}

func TestConvergeGitLabTriggerSchedules_RepairsTimezoneDrift(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	spec := trigSpec(t, trigSlashDesc)
	mine := e.accountSchedule(t, acct, spec.Description, trigBranch, spec.Cron, nil)
	key := trigOwner + "/" + trigRepo
	for i := range e.state.PipelineSchedules[key] {
		if e.state.PipelineSchedules[key][i].ID == mine {
			e.state.PipelineSchedules[key][i].CronTimezone = "America/New_York"
		}
	}
	res, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Len(t, res.Created, 1)
	assert.Equal(t, []int64{mine}, res.Removed)
	byDesc := e.assertAccountOwnsManaged(t, acct)
	assert.Equal(t, "UTC", byDesc[spec.Description].CronTimezone)
}

func TestReleaseGitLabTriggerSchedules_RepairsCronAndTimezoneDrift(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	spec := trigSpec(t, trigEventDesc)
	mine := e.accountSchedule(t, acct, spec.Description, trigBranch, "0 1 * * *", nil)
	copyID := e.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)
	key := trigOwner + "/" + trigRepo
	for i := range e.state.PipelineSchedules[key] {
		s := &e.state.PipelineSchedules[key][i]
		if s.ID == mine || s.ID == copyID {
			s.CronTimezone = "America/New_York"
		}
	}
	res, err := ReleaseGitLabTriggerSchedules(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Len(t, res.Created, 1)
	assert.ElementsMatch(t, []int64{mine, copyID}, res.Removed)
	live := e.liveSchedules(t)
	require.Len(t, live, 1)
	assert.Equal(t, spec.Cron, live[0].Cron)
	assert.Equal(t, "UTC", live[0].CronTimezone)
}

func TestConvergeGitLabTriggerSchedules_InterruptedReplacementOfAccountOwnedOriginal(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	// A disabled, drifted schedule the account already owns.
	orig := e.accountSchedule(t, acct, event.Description, trigBranch, "0 0 1 1 *", nil)
	require.NoError(t, e.state.UpdatePipelineSchedule(ctx, trigOwner, trigRepo, orig, false))

	// The replacement is created, but GitLab ignored its disabled state and the
	// cleanup does not complete, so it stays active beside the disabled
	// original.
	e.ignoreActiveOnCreate(true)
	e.state.Errors = map[string]error{"DeletePipelineSchedule": errTrigInjected}
	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.Error(t, err)
	live := e.liveSchedules(t)
	require.Len(t, live, 2)
	replacement := live[1].ID
	require.NotEqual(t, orig, replacement)
	require.True(t, live[1].Active)

	// The retry does not take the active replacement for in sync: the original
	// records that polling was disabled.
	e.ignoreActiveOnCreate(false)
	e.state.Errors = nil
	res, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Len(t, res.Created, 1)
	assert.ElementsMatch(t, []int64{orig, replacement}, res.Removed)
	byDesc := e.assertAccountOwnsManaged(t, acct)
	require.Len(t, byDesc, 1)
	assert.False(t, byDesc[event.Description].Active, "a disabled schedule is never left polling after a retry")
	e.assertNoBootstrapResidue(t, acct)
}

func TestConvergeGitLabTriggerSchedules_InterruptedCleanupKeepsDisabledState(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	// A disabled, drifted schedule the account already owns.
	orig := e.accountSchedule(t, acct, event.Description, trigBranch, "0 0 1 1 *", nil)
	require.NoError(t, e.state.UpdatePipelineSchedule(ctx, trigOwner, trigRepo, orig, false))

	// An earlier run, against a GitLab that ignored the disabled state, left an
	// active replacement beside the disabled original.
	e.ignoreActiveOnCreate(true)
	e.state.Errors = map[string]error{"DeletePipelineSchedule": errTrigInjected}
	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.Error(t, err)
	e.ignoreActiveOnCreate(false)
	e.state.Errors = nil
	live := e.liveSchedules(t)
	require.Len(t, live, 2)
	require.True(t, live[1].Active)

	// This run replaces both, and its cleanup is cut short after one deletion.
	// The original, which records the disabled state, must outlive the active
	// replacement.
	e.state.failDeleteAfter = 1
	_, err = ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.Error(t, err)
	live = e.liveSchedules(t)
	require.Len(t, live, 2)
	assert.Equal(t, orig, live[0].ID, "the disabled original is deleted last")
	assert.False(t, live[0].Active)
	assert.False(t, live[1].Active)

	// The retry keeps polling disabled.
	e.state.failDeleteAfter = 0
	_, err = ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	byDesc := e.assertAccountOwnsManaged(t, acct)
	require.Len(t, byDesc, 1)
	assert.False(t, byDesc[event.Description].Active, "a disabled schedule is never left polling after an interrupted cleanup")
	e.assertNoBootstrapResidue(t, acct)
}

func TestConvergeGitLabTriggerSchedules_ActiveStateFollowsSurvivingOriginal(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	// A disabled account-owned replacement beside an active original is not the
	// state the original asked for.
	orig := e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)
	mine := e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)
	require.NoError(t, e.state.UpdatePipelineSchedule(ctx, trigOwner, trigRepo, mine, false))

	res, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Len(t, res.Created, 1)
	assert.ElementsMatch(t, []int64{mine, orig}, res.Removed)
	byDesc := e.assertAccountOwnsManaged(t, acct)
	assert.True(t, byDesc[slash.Description].Active)
}

func TestReleaseGitLabTriggerSchedules_ReplacesIncompleteInstallerCopies(t *testing.T) {
	ctx := context.Background()
	event := trigSpec(t, trigEventDesc)

	for _, tc := range []struct {
		name        string
		accountOff  bool
		installer   func(e *trigEnv) int64
		wantActive  bool
		wantRemoved int
	}{
		{"active copy of a disabled schedule", true, func(e *trigEnv) int64 {
			return e.adminSchedule(t, event.Description, trigBranch, event.Cron, true)
		}, false, 2},
		{"copy on another ref", false, func(e *trigEnv) int64 {
			return e.adminSchedule(t, event.Description, "feature", event.Cron, true)
		}, true, 2},
		{"copy with another cron", false, func(e *trigEnv) int64 {
			return e.adminSchedule(t, event.Description, trigBranch, "0 * * * *", true)
		}, true, 2},
		{"copy with variables", false, func(e *trigEnv) int64 {
			id, err := e.state.CreatePipelineSchedule(ctx, trigOwner, trigRepo, trigBranch, event.Description, event.Cron, map[string]string{forge.VarPollMode: "events"})
			require.NoError(t, err)
			for i := range e.state.PipelineSchedules[trigOwner+"/"+trigRepo] {
				if e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].ID == id {
					e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].OwnerID = 42
				}
			}
			return id
		}, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTrigEnv()
			acct := e.readyActive(t)
			mine := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, nil)
			if tc.accountOff {
				require.NoError(t, e.state.UpdatePipelineSchedule(ctx, trigOwner, trigRepo, mine, false))
			}
			stale := tc.installer(e)

			res, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
			require.NoError(t, err)
			assert.Len(t, res.Created, 1, "a complete copy is created")
			assert.ElementsMatch(t, []int64{mine, stale}, res.Removed)
			assert.Len(t, res.Removed, tc.wantRemoved)
			assert.Empty(t, e.ownedBy(t, acct))
			live := e.liveSchedules(t)
			require.Len(t, live, 1)
			assert.Equal(t, tc.wantActive, live[0].Active)
			assert.True(t, scheduleRefIsBranch(live[0].Ref, trigBranch))
			assert.Equal(t, event.Cron, live[0].Cron)
			assert.Empty(t, live[0].Variables)
		})
	}

	t.Run("a failed copy keeps the stale schedule and the account's", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		mine := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, nil)
		stale := e.adminSchedule(t, event.Description, "feature", event.Cron, true)
		e.state.Errors = map[string]error{"CreatePipelineSchedule": errTrigInjected}
		_, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
		require.ErrorContains(t, err, "handing the fullsend event poll schedule back")
		live := e.liveSchedules(t)
		require.Len(t, live, 2)
		assert.ElementsMatch(t, []int64{mine, stale}, []int64{live[0].ID, live[1].ID})
	})
}

func TestConvergeGitLabTriggerSchedules_ContainsInterruptedPAT(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)

	// A previous run recorded the PAT obligation and lost the create response.
	st, _ := e.ledger(t)
	st.PATPending = true
	e.setLedger(t, st)
	e.f.pats[999] = &ProjectAccessToken{ID: 999, Name: st.patName(), Active: true, UserID: acct}

	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.False(t, e.f.pats[999].Active, "interrupted schedule PAT revoked")
	e.assertAccountOwnsManaged(t, acct)
	e.assertNoBootstrapResidue(t, acct)
}

func TestConvergeGitLabTriggerSchedules_LedgerWriteFailure(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	admin := e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)
	pats := e.f.calls["CreateServiceAccountPAT"]
	e.state.failWhen = func(st gitLabTriggerAccountState) bool { return st.PATPending }
	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.Error(t, err)
	assert.Equal(t, pats, e.f.calls["CreateServiceAccountPAT"], "no PAT minted before its obligation is recorded")
	assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == admin }))
	e.assertNoBootstrapResidue(t, acct)
}

func TestCheckForeignResources_ManagedAccountSchedules(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)

	op, err := newGitLabTriggerAccountOp(ctx, e.opts())
	require.NoError(t, err)
	require.NoError(t, op.checkForeignResources(ctx), "a managed schedule the account owns is fullsend's")

	foreign := e.accountSchedule(t, acct, "deploy nightly", trigBranch, "0 0 * * *", nil)
	err = op.checkForeignResources(ctx)
	require.ErrorContains(t, err, "owns 1 pipeline schedule(s) that fullsend did not create")
	require.ErrorContains(t, err, fmt.Sprint(foreign))

	// A schedule ID the project list does not report is not proven managed.
	e.f.schedules[acct] = []int{4242}
	require.ErrorContains(t, op.checkForeignResources(ctx), "that fullsend did not create")

	e.f.fail["AccountScheduleIDs"] = true
	require.ErrorContains(t, op.checkForeignResources(ctx), "listing the GitLab trigger account's pipeline schedules")
	e.f.schedules[acct] = []int{4242}
	e.state.Errors = map[string]error{"ListPipelineSchedules": errTrigInjected}
	require.ErrorContains(t, op.checkForeignResources(ctx), "listing pipeline schedules")
}

func TestReleaseGitLabTriggerSchedules(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	event := trigSpec(t, trigEventDesc)
	e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)
	e.adminSchedule(t, event.Description, trigBranch, event.Cron, false)
	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	e.assertAccountOwnsManaged(t, acct)

	res, err := ReconcileGitLabTriggerScheduleOwnership(ctx, e.opts(), false)
	require.NoError(t, err)
	assert.Len(t, res.Created, 2)
	assert.Len(t, res.Removed, 2)
	assert.Empty(t, e.ownedBy(t, acct), "the account owns no schedule after the hand-back")
	byDesc := map[string]forge.PipelineSchedule{}
	for _, s := range e.liveSchedules(t) {
		byDesc[s.Description] = s
	}
	require.Len(t, byDesc, 2)
	assert.True(t, byDesc[slash.Description].Active)
	assert.False(t, byDesc[event.Description].Active, "a disabled schedule stays disabled after the hand-back")
	assert.True(t, scheduleRefIsBranch(byDesc[slash.Description].Ref, trigBranch))
	assert.Equal(t, slash.Cron, byDesc[slash.Description].Cron)
	assert.Empty(t, byDesc[slash.Description].Variables)

	// Nothing left to release.
	res, err = ReleaseGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Empty(t, res.Created)
	assert.Empty(t, res.Removed)

	// Moving back again goes through the dispatch-active branch.
	res, err = ReconcileGitLabTriggerScheduleOwnership(ctx, e.opts(), true)
	require.NoError(t, err)
	assert.Len(t, res.Created, 2)
	e.assertAccountOwnsManaged(t, acct)
}

func TestReleaseGitLabTriggerSchedules_ExistingInstallerCopyAndEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("installer copy already exists", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		slash := trigSpec(t, trigSlashDesc)
		mine := e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)
		e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)
		res, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assert.Empty(t, res.Created, "an interrupted hand-back is finished without a second copy")
		assert.Equal(t, []int64{mine}, res.Removed)
	})
	t.Run("no ledger", func(t *testing.T) {
		e := newTrigEnv()
		res, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assert.Empty(t, res.Created)
	})
	t.Run("requires API and state", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.API = nil
		_, err := ReleaseGitLabTriggerSchedules(ctx, o)
		require.ErrorContains(t, err, "API and state store are required")
	})
	t.Run("invalid ledger", func(t *testing.T) {
		e := newTrigEnv()
		e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.VarGitLabTriggerAccount] = "{"
		_, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
		require.Error(t, err)
	})
	t.Run("account gone or misnamed", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		slash := trigSpec(t, trigSlashDesc)
		e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)
		a := e.f.accounts[acct]
		a.Name = "renamed"
		e.f.accounts[acct] = a
		_, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
		require.ErrorContains(t, err, "does not carry the recorded trigger account name")
		delete(e.f.accounts, acct)
		res, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assert.Empty(t, res.Removed, "a deleted account's schedules are not attributed to it")
	})
	t.Run("create and delete failures", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		slash := trigSpec(t, trigSlashDesc)
		mine := e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)
		e.state.Errors = map[string]error{"CreatePipelineSchedule": errTrigInjected}
		_, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
		require.ErrorContains(t, err, "handing the fullsend slash poll schedule back")
		assert.True(t, e.ownedBy(t, acct)[mine], "the account's schedule is kept until a copy exists")

		e.state.Errors = map[string]error{"DeletePipelineSchedule": errTrigInjected}
		_, err = ReleaseGitLabTriggerSchedules(ctx, e.opts())
		require.ErrorContains(t, err, fmt.Sprintf("deleting pipeline schedule %d", mine))
	})
}

func TestRotateGitLabTriggerAccount_HandsSchedulesToNewAccount(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	oldAcct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	event := trigSpec(t, trigEventDesc)
	e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)
	e.adminSchedule(t, event.Description, trigBranch, event.Cron, true)
	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	e.assertAccountOwnsManaged(t, oldAcct)

	res, err := RotateGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	require.True(t, res.Ready)
	require.NotEqual(t, oldAcct, res.AccountID)
	assert.NotContains(t, e.f.accounts, oldAcct, "the retired account is deleted")
	// Retirement handed the schedules back, so polling never stopped.
	managed := 0
	for _, s := range e.liveSchedules(t) {
		if isManagedPipelineSchedule(s.Description) {
			managed++
		}
	}
	assert.Equal(t, 2, managed)
	assert.Empty(t, e.ownedBy(t, res.AccountID))

	require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true))
	_, err = ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	e.assertAccountOwnsManaged(t, res.AccountID)
}

func TestReleaseGitLabTriggerSchedules_RetiredGeneration(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	mine := e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)
	// An interrupted rotation left the schedule-owning generation retired.
	st, _ := e.ledger(t)
	cur := gitLabTriggerAccountState{Version: gitLabTriggerAccountStateVersion, Phase: gitLabTriggerPhaseRotating, Branch: trigBranch, Nonce: "abcdefabcdefabcd", Retired: []gitLabTriggerAccountState{st}}
	cur.Retired[0].Retired = nil
	e.setLedger(t, cur)
	res, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Equal(t, []int64{mine}, res.Removed)
	assert.Len(t, res.Created, 1)
}

func TestRemoveGitLabTriggerAccount_DeletesAccountSchedules(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	event := trigSpec(t, trigEventDesc)
	e.adminSchedule(t, slash.Description, trigBranch, slash.Cron, true)
	e.adminSchedule(t, event.Description, trigBranch, event.Cron, true)
	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	created := len(e.state.CreatedSchedules)

	require.NoError(t, RemoveGitLabTriggerAccount(ctx, e.opts()))
	e.assertRemoved(t)
	assert.NotContains(t, e.f.accounts, acct)
	for _, s := range e.liveSchedules(t) {
		assert.False(t, isManagedPipelineSchedule(s.Description), "uninstall leaves no managed schedule (%d)", s.ID)
	}
	assert.Equal(t, created, len(e.state.CreatedSchedules), "uninstall creates no installer copy")
}

func TestCreateWithBootstrap_HandsSchedulesBackBeforeElevation(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	mine := e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)

	op, err := newGitLabTriggerAccountOp(ctx, e.opts())
	require.NoError(t, err)
	_, err = op.releaseSchedules(ctx, true)
	require.NoError(t, err)
	_, err = op.createWithBootstrap(ctx, true, true)
	require.NoError(t, err)
	assert.Equal(t, forge.GitLabAccessLevelMaintainer, e.f.members[acct])
	assert.False(t, e.ownedBy(t, acct)[mine], "the account owns no schedule while elevated")
	assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.Description == slash.Description }), "an installer copy keeps polling")
	require.NoError(t, op.endBootstrap(ctx))

	// A failed hand-back refuses the elevation.
	e2 := newTrigEnv()
	acct2 := e2.readyActive(t)
	e2.accountSchedule(t, acct2, slash.Description, trigBranch, slash.Cron, nil)
	op2, err := newGitLabTriggerAccountOp(ctx, e2.opts())
	require.NoError(t, err)
	e2.state.Errors = map[string]error{"CreatePipelineSchedule": errTrigInjected}
	_, err = op2.releaseSchedules(ctx, true)
	require.ErrorContains(t, err, "handing the fullsend slash poll schedule back")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, e2.f.members[acct2])
}

func TestBootstrap_HandBackPrecedesFinalActiveJobCheck(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	slash := trigSpec(t, trigSlashDesc)
	e.accountSchedule(t, acct, slash.Description, trigBranch, slash.Cron, nil)
	e.state.afterDelete = func() { e.f.activeJobs[acct] = []int{987} }
	op, err := newGitLabTriggerAccountOp(ctx, e.opts())
	require.NoError(t, err)
	err = op.bootstrap(ctx, false, false)
	require.ErrorContains(t, err, "unfinished job")
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[acct], "the job that started during hand-back must not gain Maintainer authority")
	assert.Empty(t, e.ownedBy(t, acct), "the account's scheduled source is removed before checking active jobs")
}

func TestReleaseGitLabTriggerSchedules_RejectsCreateWithoutID(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	spec := trigSpec(t, trigSlashDesc)
	original := e.accountSchedule(t, acct, spec.Description, trigBranch, spec.Cron, nil)
	e.state.createWithoutID = true

	_, err := ReleaseGitLabTriggerSchedules(context.Background(), e.opts())
	require.ErrorContains(t, err, "no schedule ID")
	assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == original }), "the account-owned original must survive an unusable create response")
}

func TestReleaseGitLabTriggerSchedules_RejectsForeignHandBackCopy(t *testing.T) {
	for _, ownerID := range []int64{0, 99} {
		t.Run(fmt.Sprint(ownerID), func(t *testing.T) {
			e := newTrigEnv()
			acct := e.readyActive(t)
			spec := trigSpec(t, trigSlashDesc)
			original := e.accountSchedule(t, acct, spec.Description, trigBranch, spec.Cron, nil)
			copyID := e.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)
			for i := range e.state.PipelineSchedules[trigOwner+"/"+trigRepo] {
				if e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].ID == copyID {
					e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].OwnerID = ownerID
				}
			}
			_, err := ReleaseGitLabTriggerSchedules(context.Background(), e.opts())
			require.ErrorContains(t, err, "not owned by a verified installer Maintainer")
			assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == original }), "the account-owned original must survive")
		})
	}
}

func TestReleaseGitLabTriggerSchedules_RejectsAccountOwnerDrift(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	spec := trigSpec(t, trigSlashDesc)
	id := e.accountSchedule(t, acct, spec.Description, trigBranch, spec.Cron, nil)
	e.state.PipelineSchedules[trigOwner+"/"+trigRepo][0].OwnerID = 99

	_, err := ReleaseGitLabTriggerSchedules(context.Background(), e.opts())
	require.ErrorContains(t, err, "changed owner")
	assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == id }), "taken-over schedule is not deleted")
}

func TestReleaseGitLabTriggerSchedules_RejectsWrongOwnerOnNewCopy(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	spec := trigSpec(t, trigSlashDesc)
	original := e.accountSchedule(t, acct, spec.Description, trigBranch, spec.Cron, nil)
	e.state.createOwnerOverride = 99

	_, err := ReleaseGitLabTriggerSchedules(context.Background(), e.opts())
	require.ErrorContains(t, err, "required owner")
	live := e.liveSchedules(t)
	require.Len(t, live, 2, "an unexpected-owner copy must not be deleted")
	assert.True(t, slices.ContainsFunc(live, func(s forge.PipelineSchedule) bool { return s.ID == original }), "the account-owned original remains")
}

func TestConvergeGitLabTriggerSchedules_RejectsForeignManagedSchedule(t *testing.T) {
	e := newTrigEnv()
	e.readyActive(t)
	spec := trigSpec(t, trigSlashDesc)
	id := e.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)
	e.state.PipelineSchedules[trigOwner+"/"+trigRepo][0].OwnerID = 99

	_, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
	require.ErrorContains(t, err, "not owned by a verified installer Maintainer")
	assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == id }), "foreign schedule is untouched")
}

func TestConvergeGitLabTriggerSchedules_StopsIfPlannedRemovalChangesOwner(t *testing.T) {
	e := newTrigEnv()
	e.readyActive(t)
	spec := trigSpec(t, trigSlashDesc)
	id := e.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)
	e.state.detailOwnerOverride = map[int64]int64{id: 99}

	_, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
	require.ErrorContains(t, err, "changed owner or description before deletion")
	assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == id }), "taken-over original is not deleted")
}

func TestConvergeGitLabTriggerSchedules_StopsIfPlannedRemovalChangesDescription(t *testing.T) {
	e := newTrigEnv()
	e.readyActive(t)
	spec := trigSpec(t, trigSlashDesc)
	id := e.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)
	e.state.detailDescriptionOverride = map[int64]string{id: "user nightly build"}

	_, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
	require.ErrorContains(t, err, "changed owner or description before deletion")
	assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == id }), "renamed schedule is not deleted")
}

func TestGitLabTriggerScheduleDeletion_RequiresOwnerAndToleratesAlreadyGone(t *testing.T) {
	e := newTrigEnv()
	e.readyActive(t)
	op, err := newGitLabTriggerAccountOp(context.Background(), e.opts())
	require.NoError(t, err)
	_, err = op.deleteSchedules(context.Background(), []int64{91}, nil)
	require.ErrorContains(t, err, "without a verified owner")
	removed, err := op.deleteSchedules(context.Background(), []int64{91}, map[int64]scheduleDeleteTarget{91: {ownerID: 42, description: trigSlashDesc}})
	require.NoError(t, err)
	assert.Equal(t, []int64{91}, removed)
}

func TestGitLabTriggerSchedules_AcceptsPriorInstallerMaintainer(t *testing.T) {
	t.Run("adoption", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		spec := trigSpec(t, trigSlashDesc)
		original := e.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)
		e.state.PipelineSchedules[trigOwner+"/"+trigRepo][0].OwnerID = 77
		e.f.members[77] = forge.GitLabAccessLevelMaintainer
		_, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
		require.NoError(t, err)
		assert.False(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == original }))
		assert.NotEmpty(t, e.ownedBy(t, acct))
	})
	t.Run("hand-back", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		spec := trigSpec(t, trigSlashDesc)
		mine := e.accountSchedule(t, acct, spec.Description, trigBranch, spec.Cron, nil)
		copyID := e.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)
		e.state.PipelineSchedules[trigOwner+"/"+trigRepo][1].OwnerID = 77
		e.f.members[77] = forge.GitLabAccessLevelMaintainer
		_, err := ReleaseGitLabTriggerSchedules(context.Background(), e.opts())
		require.NoError(t, err)
		live := e.liveSchedules(t)
		require.Len(t, live, 1)
		assert.Equal(t, copyID, live[0].ID)
		assert.NotEqual(t, mine, live[0].ID)
	})
}

func TestReleaseSchedules_IgnoresRetiredGenerationAndUninstallDoesNotRequireInstaller(t *testing.T) {
	e := newTrigEnv()
	acct := e.readyActive(t)
	spec := trigSpec(t, trigSlashDesc)
	e.accountSchedule(t, acct, spec.Description, trigBranch, spec.Cron, nil)
	retiredID := 777
	e.accountSchedule(t, retiredID, spec.Description, trigBranch, spec.Cron, nil)
	op, err := newGitLabTriggerAccountOp(context.Background(), e.opts())
	require.NoError(t, err)
	op.st.Retired = []gitLabTriggerAccountState{{AccountID: retiredID}}
	_, err = op.releaseSchedules(context.Background(), true)
	require.NoError(t, err, "a retired generation's schedule is not an installer copy")

	e2 := newTrigEnv()
	acct2 := e2.readyActive(t)
	e2.accountSchedule(t, acct2, spec.Description, trigBranch, spec.Cron, nil)
	foreign := e2.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)
	e2.state.PipelineSchedules[trigOwner+"/"+trigRepo][1].OwnerID = 99
	e2.f.fail["GetAuthenticatedUserID"] = true
	op2, err := newGitLabTriggerAccountOp(context.Background(), e2.opts())
	require.NoError(t, err)
	_, err = op2.releaseSchedules(context.Background(), false)
	require.NoError(t, err, "uninstall deletes only account-owned schedules")
	assert.True(t, slices.ContainsFunc(e2.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == foreign }), "foreign schedule remains")
}

func TestGitLabTriggerSchedules_CreateUsesQualifiedBranchRef(t *testing.T) {
	e := newTrigEnv()
	e.readyActive(t)
	spec := trigSpec(t, trigSlashDesc)
	e.adminSchedule(t, spec.Description, trigBranch, spec.Cron, true)

	adopted, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
	require.NoError(t, err)
	require.Len(t, adopted.Created, 1)
	assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool {
		return s.ID == adopted.Created[0] && s.Ref == "refs/heads/"+trigBranch
	}), "account adoption must identify the branch even when a tag has the same name")

	handedBack, err := ReleaseGitLabTriggerSchedules(context.Background(), e.opts())
	require.NoError(t, err)
	require.Len(t, handedBack.Created, 1)
	assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool {
		return s.ID == handedBack.Created[0] && s.Ref == "refs/heads/"+trigBranch
	}), "installer hand-back must identify the branch even when a tag has the same name")
}

func TestGitLabTriggerSchedules_RejectsIgnoredDisabledState(t *testing.T) {
	t.Run("adoption", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		spec := trigSpec(t, trigSlashDesc)
		original := e.adminSchedule(t, spec.Description, trigBranch, spec.Cron, false)
		e.ignoreActiveOnCreate(true)

		_, err := ConvergeGitLabTriggerSchedules(context.Background(), e.opts())
		require.ErrorContains(t, err, "does not match")
		assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == original && !s.Active }), "the disabled installer original must survive")
		assert.Empty(t, e.ownedBy(t, acct), "the mismatched active replacement is removed")
	})
	t.Run("hand-back", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		spec := trigSpec(t, trigEventDesc)
		original := e.accountSchedule(t, acct, spec.Description, trigBranch, spec.Cron, nil)
		for i := range e.state.PipelineSchedules[trigOwner+"/"+trigRepo] {
			if e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].ID == original {
				e.state.PipelineSchedules[trigOwner+"/"+trigRepo][i].Active = false
			}
		}
		e.ignoreActiveOnCreate(true)

		_, err := ReleaseGitLabTriggerSchedules(context.Background(), e.opts())
		require.ErrorContains(t, err, "does not match")
		assert.True(t, slices.ContainsFunc(e.liveSchedules(t), func(s forge.PipelineSchedule) bool { return s.ID == original && !s.Active }), "the disabled account original must survive")
	})
}

// The removals keep the order the planner chose: a global re-sort by ID would
// delete the disabled installer copy, which holds the intended state, before
// the older active account schedule it overrides.
func TestConvergeGitLabTriggerSchedules_InterruptedCleanupKeepsDisabledInstallerCopy(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	// An interrupted hand-back left the account's active schedule beside a
	// newer, disabled installer copy.
	mine := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, nil)
	copyID := e.adminSchedule(t, event.Description, trigBranch, event.Cron, false)

	// Adoption plans a disabled account schedule, and its cleanup is cut short
	// after one deletion.
	e.state.failDeleteAfter = 1
	_, err := ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.Error(t, err)
	live := e.liveSchedules(t)
	require.Len(t, live, 2)
	for _, s := range live {
		assert.False(t, s.Active, "schedule %d must not be left active", s.ID)
		assert.NotEqual(t, mine, s.ID, "the active schedule is deleted first")
	}
	assert.Equal(t, copyID, live[0].ID, "the disabled installer copy outlives the active schedule")

	// The retry keeps polling disabled.
	e.state.failDeleteAfter = 0
	_, err = ConvergeGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	byDesc := e.assertAccountOwnsManaged(t, acct)
	require.Len(t, byDesc, 1)
	assert.False(t, byDesc[event.Description].Active, "polling is not re-enabled by an interrupted cleanup")
	e.assertNoBootstrapResidue(t, acct)
}

// Hand-back keeps exactly one complete installer copy: once the account owns
// nothing a later run returns early and would never remove a duplicate.
func TestReleaseGitLabTriggerSchedules_KeepsOneOfSeveralCompleteCopies(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	acct := e.readyActive(t)
	event := trigSpec(t, trigEventDesc)
	mine := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, nil)
	first := e.adminSchedule(t, event.Description, trigBranch, event.Cron, true)
	second := e.adminSchedule(t, event.Description, trigBranch, event.Cron, true)

	res, err := ReleaseGitLabTriggerSchedules(ctx, e.opts())
	require.NoError(t, err)
	assert.Empty(t, res.Created, "a complete copy already exists")
	assert.ElementsMatch(t, []int64{mine, second}, res.Removed)
	assert.Empty(t, e.ownedBy(t, acct))
	live := e.liveSchedules(t)
	require.Len(t, live, 1)
	assert.Equal(t, first, live[0].ID, "the oldest complete copy is kept")
}

// Install hands an account-owned schedule that drifted to carry variables back
// to the installer before the override-role preflight, which cannot edit it.
// Early containment has already withdrawn the activation by then, but the
// repair does not depend on that, on the current generation being ready or
// published, or on the schedule belonging to the current generation.
func TestRepairGitLabTriggerSchedules(t *testing.T) {
	ctx := context.Background()
	event := trigSpec(t, trigEventDesc)
	drift := map[string]string{"X": "y"}

	assertHandedBack := func(t *testing.T, e *trigEnv, acct int, res GitLabTriggerSchedulesResult, drifted int64) {
		t.Helper()
		assert.Len(t, res.Created, 1)
		assert.Equal(t, []int64{drifted}, res.Removed)
		assert.Empty(t, e.ownedBy(t, acct))
		live := e.liveSchedules(t)
		require.Len(t, live, 1)
		assert.Equal(t, res.Created[0], live[0].ID, "the installer-owned copy carries no variables")
		assert.Zero(t, e.activePATs(), "no bootstrap token is left")
	}

	t.Run("hands a schedule that carries variables back while the activation is recorded", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		drifted := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, drift)
		res, err := RepairGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assertHandedBack(t, e, acct, res, drifted)
	})

	t.Run("does nothing without a ledger", func(t *testing.T) {
		e := newTrigEnv()
		res, err := RepairGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assert.Empty(t, res.Created)
		assert.Empty(t, res.Removed)
	})

	t.Run("does nothing while the account owns no schedule", func(t *testing.T) {
		e := newTrigEnv()
		e.readyActive(t)
		admin := e.adminSchedule(t, event.Description, trigBranch, event.Cron, true)
		res, err := RepairGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assert.Empty(t, res.Created)
		assert.Empty(t, res.Removed)
		live := e.liveSchedules(t)
		require.Len(t, live, 1)
		assert.Equal(t, admin, live[0].ID)
	})

	t.Run("hands a schedule that carries variables back without a recorded activation", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), false))
		drifted := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, drift)
		res, err := RepairGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assertHandedBack(t, e, acct, res, drifted)
	})

	// Containment clears TriggerPublished when the token sink is missing or
	// unsafe; the account-owned schedule still blocks the preflight.
	t.Run("hands a schedule back when the current generation is no longer published", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		drifted := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, drift)
		st, _ := e.ledger(t)
		st.TriggerPublished = false
		e.setLedger(t, st)
		res, err := RepairGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assertHandedBack(t, e, acct, res, drifted)
	})

	t.Run("hands a schedule back when the current generation is not ready", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		drifted := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, drift)
		st, _ := e.ledger(t)
		st.Phase = gitLabTriggerPhaseRotating
		e.setLedger(t, st)
		res, err := RepairGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assertHandedBack(t, e, acct, res, drifted)
	})

	// An interrupted rotation leaves the schedule-owning generation retired
	// while the current one is still bootstrapping.
	t.Run("hands a retired generation's schedule back", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		drifted := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, drift)
		st, _ := e.ledger(t)
		cur := gitLabTriggerAccountState{Version: gitLabTriggerAccountStateVersion, Phase: gitLabTriggerPhaseRotating, Branch: trigBranch, Nonce: "abcdefabcdefabcd", Retired: []gitLabTriggerAccountState{st}}
		cur.Retired[0].Retired = nil
		e.setLedger(t, cur)
		res, err := RepairGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assertHandedBack(t, e, acct, res, drifted)
	})

	// Install runs the repair before the typed-wrapper, input-contract and
	// template gates that convergeSchedules checks, so it must not resume
	// polling: the install command does not forward --reactivate-schedules to it.
	t.Run("keeps a disabled schedule that carries variables disabled", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		drifted := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, drift)
		require.NoError(t, e.state.UpdatePipelineSchedule(ctx, trigOwner, trigRepo, drifted, false))
		res, err := RepairGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assertHandedBack(t, e, acct, res, drifted)
		assert.False(t, e.liveSchedules(t)[0].Active, "the installer-owned copy stays disabled")
	})

	t.Run("leaves an account-owned schedule without variables", func(t *testing.T) {
		e := newTrigEnv()
		acct := e.readyActive(t)
		clean := e.accountSchedule(t, acct, event.Description, trigBranch, event.Cron, nil)
		res, err := RepairGitLabTriggerSchedules(ctx, e.opts())
		require.NoError(t, err)
		assert.Empty(t, res.Created)
		assert.Empty(t, res.Removed)
		live := e.liveSchedules(t)
		require.Len(t, live, 1)
		assert.Equal(t, clean, live[0].ID)
		assert.True(t, e.ownedBy(t, acct)[clean])
	})
}
