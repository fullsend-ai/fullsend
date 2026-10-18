package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// A foreign credential or resource attached after a successful install blocks
// the next install, so status reports it as drift instead of ready.
func TestDiagnoseGitLabTriggerAccount_ForeignResourcesAreDrift(t *testing.T) {
	for name, tc := range foreignAccountResources {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			require.Equal(t, GitLabTriggerStateReady, e.diagnose(e.f, trigBranch).State)

			tc.seed(e, res.AccountID)
			got := e.diagnose(e.f, trigBranch)
			assert.Equal(t, GitLabTriggerStateDrifted, got.State)
			require.NotEmpty(t, got.Reasons)
			assert.Contains(t, got.Reasons[0], "install would refuse to continue")
			assert.Contains(t, got.Reasons[0], tc.want)
		})
	}
}

// An inventory that cannot be read is unverified, never ready.
func TestDiagnoseGitLabTriggerAccount_UnreadableForeignInventoryIsUnverified(t *testing.T) {
	for _, method := range []string{"ListServiceAccountPATs", "AccountSSHKeyIDs", "AccountScheduleIDs", "UnknownOwnerScheduleIDs", "ListPipelineTriggerTokens"} {
		t.Run(method, func(t *testing.T) {
			e := newTrigEnv()
			_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)

			e.f.fail[method] = true
			got := e.diagnose(e.f, trigBranch)
			assert.Equal(t, GitLabTriggerStateDrifted, got.State)
			joined := ""
			for _, r := range got.Reasons {
				joined += r + "\n"
			}
			assert.Contains(t, joined, "could not verify")
		})
	}
}

// A recorded bootstrap obligation is reported once; its own PAT is not
// counted as a foreign credential.
func TestDiagnoseGitLabTriggerAccount_BootstrapPATIsNotForeign(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	st, ok := e.ledger(t)
	require.True(t, ok)
	st.PATID = 9
	e.setLedger(t, st)
	e.f.pats[9] = &ProjectAccessToken{ID: 9, Name: st.patName(), Active: true, UserID: res.AccountID}

	got := e.diagnose(e.f, trigBranch)
	assert.Equal(t, GitLabTriggerStateDrifted, got.State)
	for _, r := range got.Reasons {
		assert.NotContains(t, r, "install would refuse to continue")
	}
}

// publishUnsafeThenFail makes the publisher write the variable with unsafe
// attributes and then fail, as a store that rejects a variable after creating it.
func (e *trigEnv) publishUnsafeThenFail() {
	e.publish = func(_ context.Context, id int64, token string) error {
		e.f.published[id] = token
		e.setSink(true)
		if e.state.SecretProtections == nil {
			e.state.SecretProtections = map[string]forge.SecretProtection{}
		}
		e.state.SecretProtections[trigSinkKey] = forge.SecretProtection{Exists: true}
		return errTrigInjected
	}
}

// A failed publication revokes the new trigger in the same run, even when the
// publisher also cannot delete the variable, and keeps the ledger obligation.
func TestConvergeGitLabTriggerAccount_FailedPublicationRevokesTrigger(t *testing.T) {
	t.Run("variable deletion fails", func(t *testing.T) {
		e := newTrigEnv()
		e.publishUnsafeThenFail()
		unpublishCalls := 0
		deleteFails := true
		o := e.opts()
		o.UnpublishTriggerToken = func(context.Context) error {
			unpublishCalls++
			// The pre-publication clear succeeds; later deletions fail.
			if unpublishCalls > 1 && deleteFails {
				return errors.New("delete rejected")
			}
			e.setSink(false)
			return nil
		}

		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.ErrorContains(t, err, "publishing the pipeline trigger token")
		require.ErrorContains(t, err, "removing the unsafe pipeline trigger token variable")
		assert.Zero(t, e.managedTriggers(), "the trigger is revoked although the variable stays")
		assert.True(t, e.sinkPresent(), "the undeletable variable is reported, not hidden")
		st, ok := e.ledger(t)
		require.True(t, ok)
		assert.True(t, st.PublishStarted, "the publication obligation stays recorded")
		assert.False(t, st.TriggerPublished)

		// A later install replaces the generation once deletion works.
		deleteFails = false
		e.useRecreatingSink()
		o = e.unpublishClearsAttributes(e.opts())
		got, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.NoError(t, err)
		e.assertConverged(t, got)
	})

	t.Run("variable deletion succeeds", func(t *testing.T) {
		e := newTrigEnv()
		e.publishUnsafeThenFail()
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.ErrorContains(t, err, "publishing the pipeline trigger token")
		assert.NotContains(t, err.Error(), "removing the unsafe")
		assert.Zero(t, e.managedTriggers())
		assert.False(t, e.sinkPresent())
	})

	t.Run("canceled context", func(t *testing.T) {
		e := newTrigEnv()
		ctx, cancel := context.WithCancel(context.Background())
		e.publish = func(context.Context, int64, string) error {
			cancel()
			return context.Canceled
		}
		_, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
		require.Error(t, err)
		assert.Zero(t, e.managedTriggers(), "containment runs detached from the canceled context")
	})
}

// A failed token-variable delete during uninstall does not stop trigger
// revocation; the ledger stays until both obligations are satisfied.
func TestRemoveGitLabTriggerAccount_FailedSinkDeleteStillRevokesTriggers(t *testing.T) {
	e := newTrigEnv()
	_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	require.Equal(t, 1, e.managedTriggers())

	e.f.fail["Unpublish"] = true
	err = RemoveGitLabTriggerAccount(context.Background(), e.opts())
	require.ErrorContains(t, err, "removing the published pipeline trigger token")
	assert.Zero(t, e.managedTriggers(), "the trigger is revoked although the variable could not be deleted")
	assert.True(t, e.sinkPresent())
	st, ok := e.ledger(t)
	require.True(t, ok, "the ledger stays")
	assert.Equal(t, gitLabTriggerPhaseRemoving, st.Phase)

	require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
	e.assertRemoved(t)
	assert.False(t, e.sinkPresent())
}

// The revocation failure and the deletion failure are both reported, and the
// retired generations' triggers are revoked too.
func TestRemoveGitLabTriggerAccount_FailedSinkDeleteRevokesRetiredGenerations(t *testing.T) {
	e := newTrigEnv()
	e.useRecreatingSink()
	_, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)
	st, ok := e.ledger(t)
	require.True(t, ok)
	// Leave the current generation retired, as an interrupted replacement does.
	old := st
	old.Retired = nil
	st = gitLabTriggerAccountState{
		Version: 1, Phase: gitLabTriggerPhaseReady, Branch: old.Branch, Nonce: "00112233445566aa",
		Retired: []gitLabTriggerAccountState{old},
	}
	e.setLedger(t, st)

	e.f.fail["Unpublish"] = true
	err = RemoveGitLabTriggerAccount(context.Background(), e.opts())
	require.Error(t, err)
	assert.Zero(t, e.managedTriggers(), "the retired generation's trigger is revoked")
	_, ok = e.ledger(t)
	assert.True(t, ok, "the ledger stays")
}
