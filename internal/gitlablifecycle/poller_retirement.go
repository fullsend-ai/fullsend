package gitlablifecycle

import (
	"context"
	"fmt"
	"slices"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

var _ repos.GitLabPollerRetirement = (*TriggerOwner)(nil)

// PollerRetirementBlocked reports why the superseded Poller account userID
// must not be retired yet: it owns an administrator-supplied credential, which
// is not fullsend's to delete, or unfinished project jobs still run as it (or
// as a user GitLab did not report). It returns "" when retirement may proceed.
func (o *TriggerOwner) PollerRetirementBlocked(ctx context.Context, owner, repo string, userID int64) (string, error) {
	excluded, err := o.excludedAccountIDs(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	if slices.Contains(excluded, userID) {
		return fmt.Sprintf("user ID %d owns an administrator-supplied credential, so fullsend does not delete it; reconcile that account, then remove retiring_user_id from %s", userID, forge.VarGitLabPollerGenerations), nil
	}
	jobs, err := o.PollerActiveJobIDs(ctx, owner, repo, userID)
	if err != nil {
		return "", repos.SafeAPIError("listing unfinished jobs of the superseded Poller", err)
	}
	if len(jobs) > 0 {
		return fmt.Sprintf("%d unfinished job(s) still run as it", len(jobs)), nil
	}
	return "", nil
}

// RetirePollerAccount revokes the superseded Poller account's managed personal
// access tokens, verifies that no active personal access token, usable SSH key,
// unfinished job, pipeline schedule, or pipeline trigger remains, and deletes
// the project service account, then re-lists the inventory to confirm it is
// gone. An account that is no longer listed was already retired. An inventory that cannot be read fails closed.
func (o *TriggerOwner) RetirePollerAccount(ctx context.Context, owner, repo string, userID int64) error {
	accounts, err := o.Admin.ListProjectServiceAccounts(ctx, owner, repo)
	if err != nil {
		return repos.SafeAPIError("listing project service accounts before retiring the superseded Poller", err)
	}
	if !slices.ContainsFunc(accounts, func(sa gitlab.ServiceAccount) bool { return int64(sa.ID) == userID }) {
		return nil
	}
	if err := o.ContainPoller(ctx, owner, repo, userID); err != nil {
		return fmt.Errorf("revoking the superseded Poller's credentials: %w", err)
	}
	pats, err := o.Admin.ListServiceAccountPATs(ctx, owner, repo, int(userID))
	if err != nil {
		return repos.SafeAPIError("listing the superseded Poller's personal access tokens before deletion", err)
	}
	for _, p := range pats {
		if p.Active && !p.Revoked {
			return fmt.Errorf("service account %d still has active personal access token ID %d; not deleted", userID, p.ID)
		}
	}
	if err := repos.VerifyGitLabAccountDeletion(ctx, int(userID), o.accountDeletionInventory(owner, repo, userID)); err != nil {
		return err
	}
	if err := o.Admin.DeleteProjectServiceAccount(ctx, owner, repo, int(userID)); err != nil {
		return repos.SafeAPIError(fmt.Sprintf("deleting the superseded Poller service account %d", userID), err)
	}
	// GitLab may accept the deletion (202) and complete it later, so success is
	// only reported once the account is no longer listed. Until then the caller
	// keeps retiring_user_id and the next install retries.
	remaining, err := o.Admin.ListProjectServiceAccounts(ctx, owner, repo)
	if err != nil {
		return repos.SafeAPIError("confirming the superseded Poller service account was deleted", err)
	}
	if slices.ContainsFunc(remaining, func(sa gitlab.ServiceAccount) bool { return int64(sa.ID) == userID }) {
		return fmt.Errorf("service account %d is still listed after the deletion request was accepted; it stays recorded as retiring until it is gone", userID)
	}
	return nil
}

// accountDeletionInventory lists the resources that must be gone before
// userID is deleted, including those whose owner GitLab did not report.
func (o *TriggerOwner) accountDeletionInventory(owner, repo string, userID int64) repos.GitLabAccountDeletionInventory {
	return repos.GitLabAccountDeletionInventory{
		SSHKeys: func(ctx context.Context) ([]int, error) {
			ids, err := o.PollerSSHKeyIDs(ctx, userID)
			return ids, repos.SafeAPIError("listing service-account SSH keys before deletion", err)
		},
		Jobs: func(ctx context.Context) ([]int, error) {
			ids, err := o.PollerActiveJobIDs(ctx, owner, repo, userID)
			return ids, repos.SafeAPIError("listing service-account unfinished jobs before deletion", err)
		},
		Schedules: func(ctx context.Context) ([]int, error) {
			ids, err := o.PollerScheduleIDs(ctx, owner, repo, userID)
			return ids, repos.SafeAPIError("listing service-account pipeline schedules before deletion", err)
		},
		Triggers: func(ctx context.Context) ([]int, error) {
			ids, err := o.PollerTriggerIDs(ctx, owner, repo, userID)
			return ids, repos.SafeAPIError("listing service-account pipeline triggers before deletion", err)
		},
	}
}
