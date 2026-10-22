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

const trigOverrideKey = trigOwner + "/" + trigRepo

// A weakened or unverifiable override role makes a published credential
// unsafe whatever the sink's attributes: install and rotation remove the sink
// and revoke the owned trigger before they return.
func TestGitLabTriggerAccount_WeakOrUnreadableOverrideRoleContainsPublishedCredential(t *testing.T) {
	ctx := context.Background()
	weaken := map[string]func(e *trigEnv){
		"weakened role": func(e *trigEnv) {
			e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper
		},
		"unreadable role": func(e *trigEnv) {
			e.state.Errors = map[string]error{"GetPipelineVariablesMinimumOverrideRole": errors.New("boom")}
		},
	}
	actions := map[string]func(e *trigEnv) error{
		"install": func(e *trigEnv) error {
			_, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			return err
		},
		"rotation": func(e *trigEnv) error {
			_, err := RotateGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			return err
		},
	}
	for wName, weakenFn := range weaken {
		for aName, act := range actions {
			t.Run(wName+"/"+aName, func(t *testing.T) {
				e := newTrigEnv()
				e.useRecreatingSink()
				res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
				require.NoError(t, err)
				require.Contains(t, e.f.triggers, res.TriggerTokenID)
				require.True(t, e.sinkPresent(), "the sink is healthy before the role changes")

				weakenFn(e)
				err = act(e)
				if wName == "unreadable role" {
					require.ErrorContains(t, err, "override role")
				} else {
					require.NoError(t, err)
				}

				assert.False(t, e.sinkPresent(), "the published credential is removed")
				assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the owned trigger is revoked")
				st, ok := e.ledger(t)
				require.True(t, ok, "the cleanup ledger is retained")
				assert.True(t, st.PublishStarted, "the publication marker stays as the cleanup obligation")
				assert.False(t, st.TriggerPublished)
			})
		}
	}
}

// A failed replacement leaves the retired generation's live token in the
// sink while the current generation never published. A later override-role
// deferral must still remove that sink and revoke the retired generation's
// trigger.
func TestGitLabTriggerAccount_OverrideRoleDeferralContainsSinkOwnedByRetiredGeneration(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.useRecreatingSink()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)

	e.f.fail["CreateProjectServiceAccount"] = true // the replacement fails
	_, err = RotateGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.Error(t, err)
	delete(e.f.fail, "CreateProjectServiceAccount")

	st, ok := e.ledger(t)
	require.True(t, ok)
	require.False(t, st.PublishStarted, "the replacement never published")
	require.Len(t, st.Retired, 1)
	require.True(t, st.Retired[0].PublishStarted)
	require.True(t, e.sinkPresent(), "the sink still holds the retired generation's token")
	require.Contains(t, e.f.triggers, res.TriggerTokenID)

	e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper
	got, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)
	assert.Contains(t, got.Deferred, "not no_one_allowed")

	assert.False(t, e.sinkPresent(), "the retired generation's token is removed from the sink")
	assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the retired generation's trigger is revoked")
	st, ok = e.ledger(t)
	require.True(t, ok)
	require.Len(t, st.Retired, 1, "retirement stays recorded")
	assert.True(t, st.Retired[0].PublishStarted)
	assert.False(t, st.Retired[0].TriggerPublished)
}

// repos status does not report an installed account ready while the live
// override restriction is weakened or cannot be verified.
func TestDiagnoseGitLabTriggerAccount_ReportsLiveOverrideRole(t *testing.T) {
	cases := map[string]struct {
		mutate func(e *trigEnv)
		want   string
	}{
		"weakened": {func(e *trigEnv) {
			e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper
		}, "\"developer\""},
		"empty": {func(e *trigEnv) {
			e.state.PipelineVarOverrideRoles[trigOverrideKey] = ""
		}, "(unset)"},
		"unreadable": {func(e *trigEnv) {
			e.state.Errors = map[string]error{"GetPipelineVariablesMinimumOverrideRole": errors.New("boom")}
		}, "unverified"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			e.assertConverged(t, res)
			require.Equal(t, GitLabTriggerStateReady, e.diagnose(e.f, trigBranch).State)

			tc.mutate(e)
			got := e.diagnose(e.f, trigBranch)
			assert.Equal(t, GitLabTriggerStateDrifted, got.State)
			assert.Contains(t, strings.Join(got.Reasons, "; "), tc.want)
		})
	}
}
