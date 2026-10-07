package repos

import (
	"context"
	"errors"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// GitLabPollerCredentialInventory supplies read-only account-scoped queries.
// Every inventory must be complete or return an error.
type GitLabPollerCredentialInventory struct {
	Tokens            func(context.Context) ([]ProjectAccessToken, error)
	UnmanagedTriggers func(context.Context) ([]int64, error)
	SSHKeys           func(context.Context) ([]int, error)
	Jobs              func(context.Context) ([]int, error)
	Schedules         func(context.Context) ([]int, error)
}

func (i GitLabPollerCredentialInventory) completeInventory() bool {
	return i.Tokens != nil && i.UnmanagedTriggers != nil && i.SSHKeys != nil && i.Jobs != nil && i.Schedules != nil
}

// GitLabPollerContainmentOperations adds mutations to the read-only inventory
// for containment. Verification accepts only GitLabPollerCredentialInventory.
type GitLabPollerContainmentOperations struct {
	GitLabPollerCredentialInventory
	RevokeToken                  func(context.Context, int) error
	InstalledCredentialBelongsTo func(context.Context) (bool, error)
	DeleteInstalledCredential    func(context.Context) error
}

// VerifyGitLabPollerCredentials refuses elevation when credentials or owned
// resources could inherit temporary Maintainer access.
func VerifyGitLabPollerCredentials(ctx context.Context, userID int64, revoked bool, inventory GitLabPollerCredentialInventory) error {
	if !inventory.completeInventory() {
		return &PollerElevationUnsafeError{Reason: "Poller credential safety inventory is incomplete"}
	}
	toks, err := inventory.Tokens(ctx)
	if err != nil {
		return fmt.Errorf("listing Poller service account tokens: %w", err)
	}
	for _, tok := range toks {
		if tok.Revoked || !tok.Active {
			continue
		}
		if revoked {
			return &PollerElevationUnsafeError{Reason: fmt.Sprintf("the Poller service account (user ID %d) still holds an active personal access token after revocation (token ID %d); elevation was refused", userID, tok.ID)}
		}
		if gitlabroles.IsManagedPollerTokenName(tok.Name) {
			continue
		}
		return &PollerElevationUnsafeError{Reason: fmt.Sprintf("the Poller service account (user ID %d) holds an active personal access token that fullsend does not manage (token ID %d). It cannot be revoked by the install and would gain Maintainer access; revoke it first", userID, tok.ID)}
	}
	// A pipeline trigger token acts with its owner's permissions, so one the
	// Poller owns that fullsend does not manage would run with Maintainer
	// access during the elevation and could not be invalidated afterwards.
	// Managed Poller-owned triggers are revoked by the install itself.
	unmanaged, err := inventory.UnmanagedTriggers(ctx)
	if err != nil {
		return err
	}
	if len(unmanaged) > 0 {
		return &PollerElevationUnsafeError{Reason: fmt.Sprintf("the Poller service account (user ID %d) owns pipeline trigger token(s) %v that fullsend does not manage. They would run with Maintainer access during the elevation and failure containment cannot revoke them; delete them first", userID, unmanaged)}
	}
	// An SSH authentication key is a credential that revoking personal access
	// tokens and triggers does not invalidate: its holder would act with the
	// account's temporary Maintainer permissions. Keys are never deleted by the
	// install, since an administrator may own them; their presence, or an
	// inventory that cannot be read, refuses the elevation.
	ids, err := inventory.SSHKeys(ctx)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		return &PollerElevationUnsafeError{Reason: fmt.Sprintf("the Poller service account (user ID %d) has SSH authentication key(s) %v. The install cannot invalidate them and their holder would gain Maintainer access during the elevation; remove them first", userID, ids)}
	}
	// A running job's CI_JOB_TOKEN stays valid until the job ends and carries
	// its initiating user's permissions, so revoking the account's tokens and
	// triggers does not invalidate it. Jobs that act as the Poller (or whose
	// user GitLab did not report) would inherit the temporary Maintainer
	// access; defer the elevation until they finish. An inventory that cannot
	// be read fails closed.
	jobIDs, err := inventory.Jobs(ctx)
	if err != nil {
		return err
	}
	if len(jobIDs) > 0 {
		return &PollerElevationUnsafeError{Reason: fmt.Sprintf("job(s) %v are still unfinished and run as the Poller service account (or as a user GitLab did not report). Their job tokens stay valid until the jobs end and would gain Maintainer access during the elevation; wait for them to finish or cancel them first", jobIDs)}
	}
	// A pipeline schedule runs on GitLab's clock as its owner, so a
	// Poller-owned one can start work after the inventories above and obtain a
	// job token that carries the temporary Maintainer access. The install
	// cannot quiesce it without touching a resource it does not manage, so its
	// presence refuses the elevation. An inventory that cannot be read fails
	// closed.
	scheduleIDs, err := inventory.Schedules(ctx)
	if err != nil {
		return err
	}
	if len(scheduleIDs) > 0 {
		return &PollerElevationUnsafeError{Reason: fmt.Sprintf("pipeline schedule(s) %v are owned by the Poller service account (or by a user GitLab did not report). They could start pipelines whose job tokens would gain Maintainer access during the elevation; delete them or transfer their ownership first", scheduleIDs)}
	}
	return nil
}

// ContainGitLabPoller invalidates managed authentication paths and reports
// every remaining path without deleting administrator-owned resources.
func ContainGitLabPoller(ctx context.Context, userID int64, inventory GitLabPollerContainmentOperations) error {
	if !inventory.completeInventory() || inventory.RevokeToken == nil || inventory.InstalledCredentialBelongsTo == nil || inventory.DeleteInstalledCredential == nil {
		return fmt.Errorf("%w: Poller containment operations are incomplete", ErrPollerContainmentIncomplete)
	}
	var errs []error
	// The installed secret is removed only when its credential belongs to
	// this account. It must be attributed before the account's tokens are
	// revoked, since afterwards the credential can no longer say who it is.
	secretCtx, cancelSecret := GitLabCleanupContext(ctx)
	defer cancelSecret()
	removeSecret, attributeErr := inventory.InstalledCredentialBelongsTo(secretCtx)
	if attributeErr != nil {
		errs = append(errs, attributeErr)
	}
	revokeCtx, cancelRevoke := GitLabCleanupContext(ctx)
	defer cancelRevoke()
	toks, err := inventory.Tokens(revokeCtx)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing Poller service account tokens: %w", err))
	}
	unmanaged := 0
	for _, tok := range toks {
		if tok.Revoked || !tok.Active {
			continue
		}
		if !gitlabroles.IsManagedPollerTokenName(tok.Name) {
			// Fullsend never revokes credentials it does not manage, but one
			// that stays active keeps whatever role the account holds.
			unmanaged++
			continue
		}
		if err := inventory.RevokeToken(revokeCtx, tok.ID); err != nil && !forge.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("revoking Poller token ID %d: %w", tok.ID, err))
		}
	}
	if err == nil {
		// A successful DELETE is not proof the token became inactive. Re-list
		// on a fresh bounded context and report any managed runtime or
		// bootstrap token that is still active, or an inventory that cannot
		// be read, as incomplete containment.
		verifyCtx, cancelVerify := GitLabCleanupContext(ctx)
		defer cancelVerify()
		after, verifyErr := inventory.Tokens(verifyCtx)
		if verifyErr != nil {
			errs = append(errs, fmt.Errorf("%w: verifying revocation of the Poller service account tokens: %w", ErrPollerContainmentIncomplete, verifyErr))
		} else {
			var stillActive []int
			for _, tok := range after {
				if !tok.Revoked && tok.Active && gitlabroles.IsManagedPollerTokenName(tok.Name) {
					stillActive = append(stillActive, tok.ID)
				}
			}
			if len(stillActive) > 0 {
				errs = append(errs, fmt.Errorf("%w: managed Poller personal access token(s) %v are still active after revocation", ErrPollerContainmentIncomplete, stillActive))
			}
		}
	}
	if unmanaged > 0 {
		errs = append(errs, fmt.Errorf("%w: %d active personal access token(s) on the Poller service account are not managed by fullsend and were not revoked; revoke them or have an administrator block the account", ErrPollerContainmentIncomplete, unmanaged))
	}
	// Pipeline triggers the Poller owns keep its role too. Managed ones are
	// revoked with the webhook teardown; others are reported, never deleted.
	triggerCtx, cancelTriggers := GitLabCleanupContext(ctx)
	defer cancelTriggers()
	if ids, err := inventory.UnmanagedTriggers(triggerCtx); err != nil {
		errs = append(errs, err)
	} else if len(ids) > 0 {
		errs = append(errs, fmt.Errorf("%w: pipeline trigger token(s) %v owned by the Poller service account are not managed by fullsend and were not revoked; delete them or have an administrator block the account", ErrPollerContainmentIncomplete, ids))
	}
	// Revoking tokens and triggers does not end every authentication path:
	// SSH keys, unfinished jobs (their CI_JOB_TOKEN outlives the initiating
	// credential), and Poller-owned schedules keep acting with the account's
	// role. Each is inventoried on its own bounded detached context and
	// reported, never deleted, since an administrator may own them. An
	// inventory that cannot be read cannot show the path is closed.
	pathCtx, cancelPath := GitLabCleanupContext(ctx)
	defer cancelPath()
	if ids, err := inventory.SSHKeys(pathCtx); err != nil {
		errs = append(errs, fmt.Errorf("%w: %w", ErrPollerContainmentIncomplete, err))
	} else if len(ids) > 0 {
		errs = append(errs, fmt.Errorf("%w: SSH authentication key(s) %v on the Poller service account were not removed; remove them or have an administrator block the account", ErrPollerContainmentIncomplete, ids))
	}
	jobCtx, cancelJobs := GitLabCleanupContext(ctx)
	defer cancelJobs()
	if ids, err := inventory.Jobs(jobCtx); err != nil {
		errs = append(errs, fmt.Errorf("%w: %w", ErrPollerContainmentIncomplete, err))
	} else if len(ids) > 0 {
		errs = append(errs, fmt.Errorf("%w: job(s) %v are unfinished and run as the Poller service account (or an unreported user); their job tokens stay valid until the jobs end, so cancel them or have an administrator block the account", ErrPollerContainmentIncomplete, ids))
	}
	scheduleCtx, cancelSchedules := GitLabCleanupContext(ctx)
	defer cancelSchedules()
	if ids, err := inventory.Schedules(scheduleCtx); err != nil {
		errs = append(errs, fmt.Errorf("%w: %w", ErrPollerContainmentIncomplete, err))
	} else if len(ids) > 0 {
		errs = append(errs, fmt.Errorf("%w: pipeline schedule(s) %v are owned by the Poller service account (or an unreported user) and were not deleted; delete them, transfer their ownership, or have an administrator block the account", ErrPollerContainmentIncomplete, ids))
	}
	if removeSecret {
		// The attribution budget may be spent by now; deletion gets a fresh
		// detached context so a slow inventory step cannot block it.
		deleteCtx, cancelDelete := GitLabCleanupContext(ctx)
		defer cancelDelete()
		if err := inventory.DeleteInstalledCredential(deleteCtx); err != nil {
			errs = append(errs, fmt.Errorf("removing %s: %w", forge.SecretGitLabPollerToken, err))
		}
	}
	return errors.Join(errs...)
}
