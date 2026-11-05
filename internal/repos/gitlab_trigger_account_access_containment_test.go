package repos

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// A published account raised out of band is demoted to Developer whatever state
// the sink and the override role are in, including when the sink cannot be
// read: containment restores access before any sink or role check can return.
func TestContainGitLabTriggerAccount_RestoresElevatedAccountWhateverTheSinkState(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		setup   func(e *trigEnv)
		wantErr bool
	}{
		"sink attributes unreadable": {
			setup: func(e *trigEnv) {
				e.state.Errors = map[string]error{"GetRepoSecretProtection": errors.New("boom")}
			},
			wantErr: true,
		},
		"sink missing": {
			setup: func(e *trigEnv) { e.setSink(false) },
		},
		"sink unprotected": {
			setup: func(e *trigEnv) {
				e.state.SecretProtections[trigSinkKey] = forge.SecretProtection{Exists: true, Masked: true}
			},
		},
		"override role weakened": {
			setup: func(e *trigEnv) {
				e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper
			},
		},
		"override role unreadable": {
			setup: func(e *trigEnv) {
				e.state.Errors = map[string]error{"GetPipelineVariablesMinimumOverrideRole": errors.New("boom")}
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			e.useRecreatingSink()
			res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
			require.NoError(t, err)

			e.f.members[res.AccountID] = forge.GitLabAccessLevelMaintainer // out of band
			tc.setup(e)

			err = ContainGitLabTriggerAccount(ctx, e.containWithSinkOpts())
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[res.AccountID],
				"the published account is back at Developer")
			if name == "sink attributes unreadable" {
				assert.False(t, e.sinkPresent(), "an unreadable protection record cannot retain the published bearer")
				assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the owned trigger is revoked")
			}
		})
	}
}

// When Developer access cannot be established and the sink cannot be read, the
// credential is still removed and the owned trigger revoked, and both failures
// are reported.
func TestContainGitLabTriggerAccount_UnverifiableAccessStillContainsWhenSinkUnreadable(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.useRecreatingSink()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)
	require.Contains(t, e.f.triggers, res.TriggerTokenID)

	e.f.members[res.AccountID] = forge.GitLabAccessLevelMaintainer
	e.f.fail["UpdateProjectMemberAccessLevel"] = true
	e.state.Errors = map[string]error{"GetRepoSecretProtection": errors.New("boom")}

	err = ContainGitLabTriggerAccount(ctx, e.containWithSinkOpts())
	require.ErrorContains(t, err, "could not be verified")
	require.ErrorContains(t, err, "GitLab trigger account")
	assert.False(t, e.sinkPresent(), "the credential is removed")
	assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "the owned trigger is revoked")
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.True(t, st.PublishStarted, "the cleanup obligation stays recorded")
	assert.False(t, st.TriggerPublished)
}

// A sink removal that exhausts its own deadline does not starve the trigger
// revocation or the ledger write that follow it.
func TestContainGitLabTriggerAccount_SinkRemovalDeadlineDoesNotStarveRevocation(t *testing.T) {
	old := gitlabCleanupTimeout
	gitlabCleanupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { gitlabCleanupTimeout = old })

	ctx := context.Background()
	e := newTrigEnv()
	e.useRecreatingSink()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)
	require.Contains(t, e.f.triggers, res.TriggerTokenID)

	e.state.PipelineVarOverrideRoles[trigOverrideKey] = forge.PipelineVarOverrideDeveloper

	o := e.containOpts()
	o.UnpublishTriggerToken = func(c context.Context) error {
		<-c.Done()
		return c.Err()
	}
	err = ContainGitLabTriggerAccount(ctx, o)
	require.Error(t, err, "the failed sink removal is reported")
	assert.NotContains(t, e.f.triggers, res.TriggerTokenID, "revocation got a live context")
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.False(t, st.TriggerPublished, "the ledger write got a live context")
	assert.True(t, st.PublishStarted)
}
