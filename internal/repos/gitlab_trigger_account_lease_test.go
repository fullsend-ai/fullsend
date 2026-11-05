package repos

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// A missing, wrong-project or released lease refuses every public entry point
// before any GitLab or ledger call.
func TestGitLabTriggerAccount_RequiresProjectLease(t *testing.T) {
	released := trigTestLease(trigOwner, trigRepo)
	released.held.Store(false)
	leases := map[string]*GitLabProjectLease{
		"missing":       nil,
		"wrong project": trigTestLease(trigOwner, "other"),
		"released":      released,
	}
	for name, lease := range leases {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			o := e.opts()
			o.Lease = lease

			res, err := ConvergeGitLabTriggerAccount(context.Background(), o)
			require.ErrorIs(t, err, errTriggerAccountUnleased)
			assert.False(t, res.Ready)
			_, err = RotateGitLabTriggerAccount(context.Background(), o)
			require.ErrorIs(t, err, errTriggerAccountUnleased)
			require.ErrorIs(t, RemoveGitLabTriggerAccount(context.Background(), o), errTriggerAccountUnleased)

			assert.Empty(t, e.f.calls, "no GitLab call is made without the lease")
			assert.Empty(t, e.state.VariableValues, "the ledger is not written without the lease")
		})
	}
}

// Rotation takes the lease check once and reuses it for the converge step.
func TestRotateGitLabTriggerAccount_LeasedRotationConverges(t *testing.T) {
	e := newTrigEnv()
	_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	res, err := RotateGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res)
}

// A trigger request accepted before revocation can still become a job after
// any snapshot of unfinished jobs. The distributed identity is therefore never
// raised again: repair elevates a fresh generation, and a job that appears
// later for the old account only ever holds Developer authority.
func TestConvergeGitLabTriggerAccount_DelayedJobNeverGainsMaintainer(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res)
	acct := res.AccountID

	// Drift: the deploy key disappears, so repair needs a Maintainer window.
	delete(e.f.keys, res.DeployKeyID)
	e.f.activeJobs[acct] = []int{900}
	var elevated []int64
	o := e.opts()
	o.API = trigElevationProbeAPI{trigFakeGitLab: e.f, onElevate: func(id int64) { elevated = append(elevated, id) }}

	got, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.NoError(t, err)
	e.assertConverged(t, got)
	assert.Empty(t, got.Deferred)
	assert.NotEqual(t, acct, got.AccountID, "repair uses a fresh generation")
	assert.NotContains(t, elevated, int64(acct), "the distributed account is never raised again")
	assert.Equal(t, []int64{int64(got.AccountID)}, elevated)
	assert.NotContains(t, e.f.accounts, acct, "the previous generation is retired after publication")
}

// Rotation never raises the distributed identity either: a job still running
// as it is irrelevant because only the fresh generation is elevated.
func TestRotateGitLabTriggerAccount_DelayedJobNeverGainsMaintainer(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.f.activeJobs[res.AccountID] = []int{901}
	var elevated []int64
	o := e.opts()
	o.API = trigElevationProbeAPI{trigFakeGitLab: e.f, onElevate: func(id int64) { elevated = append(elevated, id) }}

	got, err := RotateGitLabTriggerAccount(context.Background(), o)
	require.NoError(t, err)
	e.assertConverged(t, got)
	assert.NotEqual(t, res.AccountID, got.AccountID)
	assert.Equal(t, []int64{int64(got.AccountID)}, elevated)
}

// Foreign resources owned by the trigger account fail closed.
func TestConvergeGitLabTriggerAccount_ForeignAccountResourcesFailClosed(t *testing.T) {
	cases := map[string]struct {
		seed func(e *trigEnv, acct int)
		want string
	}{
		"ssh key":  {func(e *trigEnv, acct int) { e.f.sshKeys[acct] = []int{7} }, "SSH authentication key"},
		"schedule": {func(e *trigEnv, acct int) { e.f.schedules[acct] = []int{8} }, "pipeline schedule"},
		"foreign trigger": {func(e *trigEnv, acct int) {
			e.f.triggers[9] = forge.PipelineTriggerToken{ID: 9, Description: "someone-else", OwnerID: int64(acct)}
		}, "refusing to treat it as fullsend-owned"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			tc.seed(e, res.AccountID)
			delete(e.f.keys, res.DeployKeyID)
			elevations := 0
			o := e.opts()
			o.API = trigElevationProbeAPI{trigFakeGitLab: e.f, onElevate: func(int64) { elevations++ }}

			_, err = ConvergeGitLabTriggerAccount(context.Background(), o)
			require.ErrorContains(t, err, tc.want)
			assert.Zero(t, elevations, "no elevation while foreign credentials exist")
			assert.Zero(t, e.activePATs())
			if name == "foreign trigger" {
				assert.Contains(t, e.f.triggers, int64(9), "a foreign trigger is never revoked")
			}
		})
	}
}

// Inventory failures are never read as "nothing in flight".
func TestConvergeGitLabTriggerAccount_InventoryFailuresFailClosed(t *testing.T) {
	for _, method := range []string{"AccountSSHKeyIDs", "AccountScheduleIDs", "UnknownOwnerScheduleIDs", "AccountActiveJobIDs"} {
		t.Run(method, func(t *testing.T) {
			e := newTrigEnv()
			e.f.fail[method] = true
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.Error(t, err)
			assert.False(t, res.Ready)
			assert.Empty(t, e.f.published)
			assert.Zero(t, e.activePATs())
		})
	}
}
