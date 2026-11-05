package repos

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// foreignAccountResources are credentials and resources an administrator may
// attach to the trigger account. Deleting the account would invalidate each.
var foreignAccountResources = map[string]struct {
	seed func(e *trigEnv, acct int)
	want string
}{
	"active PAT": {func(e *trigEnv, acct int) {
		e.f.pats[9] = &ProjectAccessToken{ID: 9, Name: "someone", Active: true, UserID: acct}
	}, "did not create"},
	"ssh key":  {func(e *trigEnv, acct int) { e.f.sshKeys[acct] = []int{7} }, "SSH authentication key"},
	"schedule": {func(e *trigEnv, acct int) { e.f.schedules[acct] = []int{8} }, "pipeline schedule"},
	"foreign trigger": {func(e *trigEnv, acct int) {
		e.f.triggers[9] = forge.PipelineTriggerToken{ID: 9, Description: "someone-else", OwnerID: int64(acct)}
	}, "refusing to treat it as fullsend-owned"},
}

// Uninstall refuses to delete the account while a credential or resource that
// fullsend did not create remains, keeps the ledger, and finishes once the
// administrator removes it.
func TestRemoveGitLabTriggerAccount_ForeignResourcesBlockAccountDeletion(t *testing.T) {
	for name, tc := range foreignAccountResources {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			tc.seed(e, res.AccountID)

			err = RemoveGitLabTriggerAccount(context.Background(), e.opts())
			require.ErrorContains(t, err, tc.want)
			assert.Contains(t, e.f.accounts, res.AccountID, "the account owning a foreign resource is kept")
			st, ok := e.ledger(t)
			require.True(t, ok, "obligation kept")
			assert.Equal(t, res.AccountID, st.AccountID)
			if name == "active PAT" {
				assert.True(t, e.f.pats[9].Active, "a foreign PAT is not revoked by inference")
			}

			delete(e.f.pats, 9)
			delete(e.f.triggers, 9)
			clear(e.f.sshKeys)
			clear(e.f.schedules)
			require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
			e.assertRemoved(t)
		})
	}
}

// An inventory that cannot be read is never taken as "nothing remains".
func TestRemoveGitLabTriggerAccount_InventoryFailureBlocksAccountDeletion(t *testing.T) {
	for _, method := range []string{"AccountSSHKeyIDs", "AccountScheduleIDs", "UnknownOwnerScheduleIDs"} {
		t.Run(method, func(t *testing.T) {
			e := newTrigEnv()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			e.f.fail[method] = true

			require.Error(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
			assert.Contains(t, e.f.accounts, res.AccountID)
			_, ok := e.ledger(t)
			assert.True(t, ok, "obligation kept")

			require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
			e.assertRemoved(t)
		})
	}
}

// Retiring a previous generation applies the same check, so rotation never
// deletes an identity that owns an administrator-attached credential.
func TestGitLabTriggerAccount_RetirementKeepsAccountWithForeignResources(t *testing.T) {
	for name, tc := range foreignAccountResources {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			e.useRecreatingSink()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
			require.NoError(t, err)
			tc.seed(e, res.AccountID)

			_, err = RotateGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
			require.ErrorContains(t, err, "retiring a previous GitLab trigger account generation")
			require.ErrorContains(t, err, tc.want)
			assert.Contains(t, e.f.accounts, res.AccountID, "the retired account owning a foreign resource is kept")
			st, ok := e.ledger(t)
			require.True(t, ok)
			require.Len(t, st.Retired, 1, "the retirement obligation stays in the ledger")
		})
	}
}

// loseAccountCreate leaves the state of an interrupted create: the GitLab
// account exists under the nonce name, but the ledger records no account ID.
func loseAccountCreate(t *testing.T, e *trigEnv) int {
	t.Helper()
	e.f.lose["CreateProjectServiceAccount"] = true
	_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.Error(t, err)
	clear(e.f.lose)
	st, ok := e.ledger(t)
	require.True(t, ok)
	require.Zero(t, st.AccountID, "the create response was lost before the ID was recorded")
	accts := e.managedAccounts()
	require.Len(t, accts, 1)
	return accts[0].ID
}

// An account recovered by its nonce name is checked for foreign resources the
// same way as one whose ID was recorded.
func TestRemoveGitLabTriggerAccount_NonceRecoveredAccountKeepsForeignResources(t *testing.T) {
	for name, tc := range foreignAccountResources {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			acct := loseAccountCreate(t, e)
			tc.seed(e, acct)

			err := RemoveGitLabTriggerAccount(context.Background(), e.opts())
			require.ErrorContains(t, err, tc.want)
			assert.Contains(t, e.f.accounts, acct, "the account owning a foreign resource is kept")
			st, ok := e.ledger(t)
			require.True(t, ok, "obligation kept")
			assert.Equal(t, acct, st.AccountID, "the recovered account is recorded")

			delete(e.f.pats, 9)
			delete(e.f.triggers, 9)
			clear(e.f.sshKeys)
			clear(e.f.schedules)
			require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
			e.assertRemoved(t)
		})
	}
}

// An inventory that cannot be read blocks deleting a nonce-recovered account.
func TestRemoveGitLabTriggerAccount_NonceRecoveredInventoryFailureBlocksDeletion(t *testing.T) {
	for _, method := range []string{"AccountSSHKeyIDs", "AccountScheduleIDs", "UnknownOwnerScheduleIDs"} {
		t.Run(method, func(t *testing.T) {
			e := newTrigEnv()
			acct := loseAccountCreate(t, e)
			e.f.fail[method] = true

			require.Error(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
			assert.Contains(t, e.f.accounts, acct)
			_, ok := e.ledger(t)
			assert.True(t, ok, "obligation kept")

			clear(e.f.fail)
			require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
			e.assertRemoved(t)
		})
	}
}

// A nonce-recovered account has no ledger proof of a bootstrap PAT, so a PAT
// that merely carries the bootstrap name is not revoked by inference: it
// reaches the foreign-PAT check and blocks deleting the account.
func TestRemoveGitLabTriggerAccount_NonceRecoveredAccountDoesNotRevokePATByName(t *testing.T) {
	e := newTrigEnv()
	acct := loseAccountCreate(t, e)
	st, ok := e.ledger(t)
	require.True(t, ok)
	e.f.pats[9] = &ProjectAccessToken{ID: 9, Name: st.patName(), Active: true, UserID: acct}

	err := RemoveGitLabTriggerAccount(context.Background(), e.opts())
	require.ErrorContains(t, err, "did not create")
	assert.Contains(t, e.f.accounts, acct, "the account holding an unproven PAT is kept")
	assert.True(t, e.f.pats[9].Active, "the PAT is not revoked by name alone")
	_, ok = e.ledger(t)
	assert.True(t, ok, "obligation kept")
}

// Repeating the removal of a nonce-recovered account keeps the PAT that merely
// carries the bootstrap name: the first attempt recorded the account ID, but
// the ledger still holds no bootstrap obligation.
func TestRemoveGitLabTriggerAccount_RepeatedRemovalDoesNotRevokePATByName(t *testing.T) {
	e := newTrigEnv()
	acct := loseAccountCreate(t, e)
	st, ok := e.ledger(t)
	require.True(t, ok)
	e.f.pats[9] = &ProjectAccessToken{ID: 9, Name: st.patName(), Active: true, UserID: acct}

	for attempt := 1; attempt <= 2; attempt++ {
		err := RemoveGitLabTriggerAccount(context.Background(), e.opts())
		require.ErrorContains(t, err, "did not create", "attempt %d", attempt)
		assert.Contains(t, e.f.accounts, acct, "attempt %d keeps the account", attempt)
		assert.True(t, e.f.pats[9].Active, "attempt %d does not revoke the PAT by name", attempt)
	}
}

// A ready account has no bootstrap obligation, so an administrator-created PAT
// that carries the bootstrap name is not revoked by uninstall.
func TestRemoveGitLabTriggerAccount_ReadyAccountKeepsPATWithBootstrapName(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	st, ok := e.ledger(t)
	require.True(t, ok)
	require.Zero(t, st.PATID)
	require.False(t, st.PATPending)
	e.f.pats[9] = &ProjectAccessToken{ID: 9, Name: st.patName(), Active: true, UserID: res.AccountID}

	err = RemoveGitLabTriggerAccount(context.Background(), e.opts())
	require.ErrorContains(t, err, "did not create")
	assert.Contains(t, e.f.accounts, res.AccountID)
	assert.True(t, e.f.pats[9].Active, "the PAT is not revoked by name alone")
}

// Retiring a previous generation applies the same rule.
func TestGitLabTriggerAccount_RetirementKeepsPATWithBootstrapName(t *testing.T) {
	e := newTrigEnv()
	e.useRecreatingSink()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
	require.NoError(t, err)
	st, ok := e.ledger(t)
	require.True(t, ok)
	e.f.pats[9] = &ProjectAccessToken{ID: 9, Name: st.patName(), Active: true, UserID: res.AccountID}

	_, err = RotateGitLabTriggerAccount(context.Background(), e.unpublishClearsAttributes(e.opts()))
	require.ErrorContains(t, err, "retiring a previous GitLab trigger account generation")
	require.ErrorContains(t, err, "did not create")
	assert.Contains(t, e.f.accounts, res.AccountID)
	assert.True(t, e.f.pats[9].Active, "the PAT is not revoked by name alone")
}
