package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// triggerRemovingTokens is a token client that also removes the trigger
// account, as the CLI's uninstall token client does.
type triggerRemovingTokens struct {
	reconcilingTokens
	removeCalls  int
	removeErr    error
	containCalls int
	containErr   error

	containLeaseHolds       bool
	reconcileCallsAtContain int
	leaseHolds              bool
	leaseUnderMu            bool
	secretsAtRun            int
	client                  *leaseProbeClient
}

func (r *triggerRemovingTokens) ContainGitLabTriggerAccountForUninstall(_ context.Context, _ forge.Client, lease *GitLabProjectLease, owner, repo string) error {
	r.containCalls++
	r.containLeaseHolds = lease.holds(owner, repo)
	r.reconcileCallsAtContain = r.calls
	return r.containErr
}

func (r *triggerRemovingTokens) RemoveGitLabTriggerAccountForUninstall(_ context.Context, _ forge.Client, lease *GitLabProjectLease, owner, repo string) error {
	r.removeCalls++
	r.leaseHolds = lease.holds(owner, repo)
	r.leaseUnderMu = r.client.leaseHeld(owner, repo)
	r.secretsAtRun = len(r.client.secretDeleteHeld)
	return r.removeErr
}

// Uninstall removes the trigger account under the project lease it already
// holds, before any secret is deleted, and only after the Poller identity was
// reconciled.
func TestUninstall_GitLabRemovesTriggerAccountUnderLease(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}
	tokens := &triggerRemovingTokens{client: client}
	tokens.leaseHeld = func() bool { return client.leaseHeld("acme", "api") }

	results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), client, uninstallCommitFn(fake), tokens)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Error)
	assert.True(t, results[0].Success)
	assert.Equal(t, 1, tokens.removeCalls)
	assert.True(t, tokens.leaseHolds, "the lifecycle receives a lease capability that holds")
	assert.True(t, tokens.leaseUnderMu, "the project lease is held during removal")
	assert.Zero(t, tokens.secretsAtRun, "removal precedes secret deletion")
}

// A removal failure stops the uninstall: the scaffold is kept, the manifest
// entry is retained for a retry, and the failure names the kept ledger.
func TestUninstall_GitLabTriggerAccountRemovalFailureStopsUninstall(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}
	tokens := &triggerRemovingTokens{client: client, removeErr: errors.New("deleting the trigger deploy key")}
	tokens.leaseHeld = func() bool { return client.leaseHeld("acme", "api") }

	results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), client, uninstallCommitFn(fake), tokens)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.False(t, results[0].Success)
	assert.False(t, results[0].WorkflowDeleted, "the scaffold stays until the account is gone")
	assert.Contains(t, results[0].Error.Error(), forge.VarGitLabTriggerAccount)
	assert.Contains(t, results[0].Error.Error(), "deleting the trigger deploy key")
	assert.False(t, client.leaseHeld("acme", "api"), "the lease is released on failure")
	for _, d := range fake.DeletedVariables {
		assert.NotEqual(t, forge.VarGitLabTriggerAccount, d.Name, "the ledger is never deleted by generic cleanup")
	}

	tokens.removeErr = nil
	results, err = uninstallGitLabAcmeAPIWithTokens(context.Background(), client, uninstallCommitFn(fake), tokens)
	require.NoError(t, err)
	require.NoError(t, results[0].Error)
	assert.Equal(t, 2, tokens.removeCalls, "the retry resumes removal")
}

// An unverified Poller inventory means removal is skipped, and the ledger
// stays as the durable cleanup obligation.
func TestUninstall_GitLabTriggerAccountSkippedWhenPollerUnreconciled(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}
	tokens := &triggerRemovingTokens{client: client}
	tokens.err = errors.New("listing GitLab project service accounts to reconcile the Poller")
	tokens.leaseHeld = func() bool { return client.leaseHeld("acme", "api") }

	results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), client, uninstallCommitFn(fake), tokens)
	require.NoError(t, err)
	require.Error(t, results[0].Error)
	assert.Zero(t, tokens.removeCalls)
}

const triggerLedgerInterruptedBootstrap = `{"version":1,"phase":"bootstrapping","branch":"main","nonce":"0123456789abcdef","account_id":5,"elevated":true,"pat_pending":true}`

// Generic role-variable cleanup never discovers the trigger ledger or its
// token sink: only the dedicated removal, which proves ownership of each
// resource first, may delete them.
func TestExtraGitLabRoleUninstallVars_ExcludesTriggerAccountState(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	fc.VariableValues["o/r/"+forge.VarGitLabTriggerAccount] = triggerLedgerInterruptedBootstrap
	fc.VariableValues["o/r/"+forge.SecretGitLabTriggerAccountToken] = "x"
	got := extraGitLabRoleUninstallVars(context.Background(), fc, "o", "r", gitLabRoleUninstallVars)
	assert.NotContains(t, got, forge.VarGitLabTriggerAccount)
	assert.NotContains(t, got, forge.SecretGitLabTriggerAccountToken)
	assert.NotContains(t, gitlabRoleIdentityVarNames(context.Background(), fc, "o", "r"), forge.VarGitLabTriggerAccount)
	assert.NotContains(t, gitlabRoleIdentityVarNames(context.Background(), fc, "o", "r"), forge.SecretGitLabTriggerAccountToken)
}

// Standalone role cleanup leaves an interrupted trigger bootstrap's ledger
// and sink in place.
func TestCleanupGitLabRoleIdentity_PreservesTriggerAccountLedger(t *testing.T) {
	t.Parallel()
	fc := forge.NewFakeClient()
	fc.VariableValues["group/project/"+forge.VarGitLabTriggerAccount] = triggerLedgerInterruptedBootstrap
	fc.Secrets["group/project/"+forge.SecretGitLabTriggerAccountToken] = true
	_, err := CleanupGitLabRoleIdentity(context.Background(), GitLabRoleCleanupConfig{Owner: "group", Repo: "project", Client: fc})
	require.NoError(t, err)
	assert.Contains(t, fc.VariableValues, "group/project/"+forge.VarGitLabTriggerAccount)
	assert.True(t, fc.Secrets["group/project/"+forge.SecretGitLabTriggerAccountToken])
	for _, d := range fc.DeletedVariables {
		assert.NotEqual(t, forge.VarGitLabTriggerAccount, d.Name)
	}
}

// With a trigger ledger, an uninstall client without the dedicated removal
// capability stops, keeps the scaffold and the ledger, and names the blocker.
func TestUninstall_GitLabTriggerLedgerRequiresRemovalCapability(t *testing.T) {
	for name, ledger := range map[string]string{
		"ready":                 `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","account_id":5}`,
		"interrupted bootstrap": triggerLedgerInterruptedBootstrap,
	} {
		t.Run(name, func(t *testing.T) {
			fake := newInstalledFakeGitLabClient("acme/api")
			fake.VariableValues["acme/api/"+forge.VarGitLabTriggerAccount] = ledger
			client := &leaseProbeClient{FakeClient: fake}
			tokens := &reconcilingTokens{}
			tokens.leaseHeld = func() bool { return client.leaseHeld("acme", "api") }

			results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), client, uninstallCommitFn(fake), tokens)
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.Error(t, results[0].Error)
			assert.False(t, results[0].Success)
			assert.False(t, results[0].WorkflowDeleted, "the scaffold stays until the account is removed")
			assert.Contains(t, results[0].Error.Error(), "cannot remove the GitLab trigger account")
			assert.Contains(t, fake.VariableValues, "acme/api/"+forge.VarGitLabTriggerAccount, "the ledger is preserved")
			for _, d := range fake.DeletedVariables {
				assert.NotEqual(t, forge.VarGitLabTriggerAccount, d.Name)
			}
			assert.False(t, client.leaseHeld("acme", "api"), "the lease is released")
		})
	}
}

// Without a ledger, a client lacking the capability uninstalls as before.
func TestUninstall_GitLabWithoutTriggerLedgerNeedsNoCapability(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}
	tokens := &reconcilingTokens{}
	tokens.leaseHeld = func() bool { return client.leaseHeld("acme", "api") }
	results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), client, uninstallCommitFn(fake), tokens)
	require.NoError(t, err)
	require.NoError(t, results[0].Error)
}

// Containment of an interrupted trigger-account bootstrap runs under the
// lease before the Poller is reconciled, so a Poller failure that skips
// removal still leaves the account contained.
func TestUninstall_GitLabContainsTriggerAccountBeforePollerReconciliation(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}
	tokens := &triggerRemovingTokens{client: client}
	tokens.leaseHeld = func() bool { return client.leaseHeld("acme", "api") }
	tokens.err = errors.New("Poller inventory unreadable")

	results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), client, uninstallCommitFn(fake), tokens)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error, "the unreconciled Poller still fails the uninstall")
	assert.Equal(t, 1, tokens.containCalls, "containment runs although the Poller could not be reconciled")
	assert.True(t, tokens.containLeaseHolds, "containment receives a lease capability that holds")
	assert.Zero(t, tokens.reconcileCallsAtContain, "containment precedes Poller reconciliation")
	assert.Zero(t, tokens.removeCalls, "removal still waits for a reconciled Poller")
}

// A containment failure stops the uninstall before the Poller is reconciled
// and before anything is torn down.
func TestUninstall_GitLabTriggerAccountContainmentFailureStopsUninstall(t *testing.T) {
	fake := newInstalledFakeGitLabClient("acme/api")
	client := &leaseProbeClient{FakeClient: fake}
	tokens := &triggerRemovingTokens{client: client, containErr: errors.New("restoring Developer access")}
	tokens.leaseHeld = func() bool { return client.leaseHeld("acme", "api") }

	results, err := uninstallGitLabAcmeAPIWithTokens(context.Background(), client, uninstallCommitFn(fake), tokens)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "containing the GitLab trigger account")
	assert.False(t, results[0].Success)
	assert.Zero(t, tokens.calls, "the Poller is not reconciled after a failed containment")
	assert.Zero(t, tokens.removeCalls)
}
