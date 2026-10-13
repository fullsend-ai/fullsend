package repos

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// GitLabTriggerOwner lets the webhook fast path mint its pipeline
// trigger token as the Poller identity rather than as the install-time
// admin (issue #8083). GitLab binds a trigger token to its creator and
// creating one needs Maintainer access, so the Poller service account is
// raised to Maintainer only for the duration of the create call and then
// restored to Developer before the webhook is enabled or updated.
// Creation and rotation additionally require GitLabPollerQuiescenceVerifier.
// This optional capability must guarantee server-side request draining; owners
// without it can reconcile existing triggers but defer new creation/rotation.
type GitLabTriggerOwner interface {
	// PollerUserID identifies the fullsend-managed Poller service account
	// using the installer's own authority, so identification keeps working
	// after the distributed runtime credential has been revoked or removed.
	// An error wrapping forge.ErrNotFound means there is no managed Poller
	// service account, or the installed Poller credential is an
	// administrator-supplied one, so its membership must not be changed.
	PollerUserID(ctx context.Context, owner, repo string) (int64, error)
	// CreatePollerBootstrap creates the installer-only bootstrap personal
	// access token for the Poller account. Its value is held in memory by the
	// caller only: it is never stored in CI/CD variables, logs, or agent
	// environments.
	CreatePollerBootstrap(ctx context.Context, owner, repo string, userID int64) (*PollerBootstrap, error)
	// CreatePipelineTriggerTokenAsPoller creates a pipeline trigger token
	// authenticated as the Poller identity with the bootstrap credential.
	CreatePipelineTriggerTokenAsPoller(ctx context.Context, owner, repo, description string, bootstrap *PollerBootstrap) (*forge.PipelineTriggerToken, error)
	// RevokePollerBootstrap revokes every active bootstrap personal access
	// token of the Poller account (including orphans left by an interrupted
	// run) and verifies that none remains active.
	RevokePollerBootstrap(ctx context.Context, owner, repo string, userID int64) error
	// SetProjectMemberAccessLevel changes a direct project member's
	// access level using the install-time admin credential.
	SetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64, accessLevel int) error
	// VerifyPollerElevationSafe reports whether the identified Poller may be
	// raised to temporary Maintainer access. It returns an error when the
	// account's credentials cannot be fully accounted for: an active personal
	// access token that is neither the managed runtime credential nor the
	// installer's bootstrap credential, or a pipeline trigger token fullsend
	// does not manage. Failure containment cannot revoke what fullsend does not
	// manage, and the install must not elevate an account that holds such a
	// credential.
	VerifyPollerElevationSafe(ctx context.Context, owner, repo string, userID int64) error
	// VerifyPollerCredentialsRevoked repeats the safety inventory after
	// revocation and requires no active PATs. Managed
	// names are not evidence of revocation. It runs before bootstrap creation.
	VerifyPollerCredentialsRevoked(ctx context.Context, owner, repo string, userID int64) error
	// RevokePollerRuntimeCredentials invalidates the distributed Poller
	// runtime credential before elevation: it removes the installed Poller
	// CI/CD secret, then revokes the account's managed personal access tokens
	// and verifies none remains active. Credentials fullsend does not manage
	// are never touched.
	RevokePollerRuntimeCredentials(ctx context.Context, owner, repo string, userID int64) error
	// CreatePollerRuntimeToken creates a replacement runtime personal access
	// token for the Poller account. The caller stores it.
	CreatePollerRuntimeToken(ctx context.Context, owner, repo string, userID int64, expiresAt string) (*PollerRuntimeToken, error)
	// ContainPoller disables the positively identified managed Poller
	// credential for userID: it revokes the account's managed personal
	// access tokens (runtime and bootstrap) and removes the installed Poller
	// CI/CD secret only when its credential belongs to userID, so a Poller
	// that could not be returned to Developer access stops authenticating
	// while credentials of other identities are preserved. Every step is attempted; failures are joined. It
	// reports an error when an active token that fullsend does not manage
	// remains on the account, since containment is then incomplete.
	ContainPoller(ctx context.Context, owner, repo string, userID int64) error
}

// GitLabPollerQuiescenceVerifier is required before temporary elevation.
// Success must establish a server-side guarantee that all requests accepted
// with revoked credentials have finished, including asynchronous credential
// and job creation. Empty inventories or a fixed delay are insufficient.
// Adapters without this guarantee defer elevation and retain polling.
// Verification runs after revocation and the final safety inventory.
// The guarded orchestration is retained intentionally as the shared contract for
// adapters that can establish this guarantee. It remains tested but disabled in
// the live GitLab adapter; successful inventories never enable it by themselves.
type GitLabPollerQuiescenceVerifier interface {
	VerifyPollerQuiescence(ctx context.Context, owner, repo string, userID int64) error
}

// PollerBootstrap is the installer-only bootstrap credential. Token is the
// secret value; it exists only in memory for one trigger-creation transaction.
type PollerBootstrap struct {
	ID    int
	Token string
}

// PollerRuntimeToken is a newly created runtime credential for the Poller.
type PollerRuntimeToken struct {
	ID    int
	Token string
}

// EnsureGitLabWebhookFastPathWithTriggerOwner is EnsureGitLabWebhookFastPath
// with the trigger token minted as the Poller identity through
// triggerOwner. A nil triggerOwner, or a project without an installed
// Poller credential, mints as the client's own identity, which the
// runtime privilege ceiling then accepts only below Maintainer.
func EnsureGitLabWebhookFastPathWithTriggerOwner(ctx context.Context, client forge.Client, triggerOwner GitLabTriggerOwner, baseURL, owner, repo string, rotate, dryRun bool) (res GitLabWebhookResult, err error) {
	// One installer at a time per project, across processes: the whole
	// transaction (Poller reconciliation, trigger and credential inventory,
	// revocation, elevation, minting, restoration, and webhook publication)
	// holds the same project lease as the role-credential provisioning,
	// rotation, and cleanup.
	release, lockErr := LockGitLabProject(ctx, client, owner, repo, dryRun)
	if lockErr != nil {
		return GitLabWebhookResult{Action: "deferred"}, lockErr
	}
	red := &credentialRedactor{}
	defer func() {
		err = red.redact(err)
		release(&err)
	}()
	return ensureGitLabWebhookFastPath(ctx, client, triggerOwner, baseURL, owner, repo, rotate, dryRun, red)
}

// gitlabPollerRestoreError reports that the Poller's temporary Maintainer
// access could not be restored to, or verified at, Developer. The caller
// must disable the managed fast path: no trigger owned by the Poller may
// stay live while the Poller holds more than Developer access.
type gitlabPollerRestoreError struct {
	userID int64
	err    error
	// bootstrap marks a failure to revoke or verify revocation of the
	// installer-only bootstrap credential, which must also fail closed.
	bootstrap bool
}

func (e *gitlabPollerRestoreError) Error() string {
	if e.bootstrap {
		return fmt.Sprintf("revoking the installer-only bootstrap credential of the GitLab Poller identity (user ID %d) after creating the pipeline trigger token failed: %v. "+
			"The webhook fast path has been disabled and the Poller credential contained; polling and webhook dispatch are unavailable until the bootstrap personal access token (%s) is revoked in the Poller service account's settings and 'fullsend repos install' provisions a replacement credential", e.userID, e.err, gitlabroles.PollerBootstrapTokenName)
	}
	return fmt.Sprintf("restoring the GitLab Poller identity (user ID %d) to Developer access after minting the pipeline trigger token failed: %v. "+
		"The webhook fast path has been disabled. When containment of the Poller credential succeeds, its personal access tokens are revoked and %s is removed, "+
		"so polling and webhook dispatch are unavailable until the member's project role is Developer again and install provisions a replacement credential. "+
		"Set that member's project role to Developer in Project information > Members, then re-run 'fullsend repos install'", e.userID, e.err, forge.SecretGitLabPollerToken)
}

func (e *gitlabPollerRestoreError) Unwrap() error { return e.err }

// GitLabCleanupContext is gitlabCleanupContext for GitLabTriggerOwner
// implementations, which budget each compensating request independently.
// Compensating requests after the Poller was raised to Maintainer must still
// reach GitLab when the operation context is canceled, or the Poller could
// keep standing Maintainer access.
func GitLabCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return gitlabCleanupContext(ctx)
}

// failClosedPoller handles a Poller that could not be restored to, or
// verified at, Developer access. It disables the managed fast path and
// contains the Poller credential itself: its PAT and installed secret would
// otherwise keep the elevated runtime privilege until expiry. Webhook
// teardown and credential containment each run on their own bounded context
// detached from cancellation, so exhausting the teardown budget never stops
// containment. Every cleanup failure is returned; the caller reports the
// restore error itself.
func failClosedPoller(ctx context.Context, client forge.Client, to GitLabTriggerOwner, owner, repo string, res *GitLabWebhookResult, restoreErr *gitlabPollerRestoreError) error {
	teardownCtx, cancelTeardown := gitlabCleanupContext(ctx)
	details, teardownErr := revokeGitLabWebhookFastPath(teardownCtx, client, owner, repo, false)
	cancelTeardown()
	res.Details = append(res.Details, details...)
	containCtx, cancelContain := gitlabCleanupContext(ctx)
	defer cancelContain()
	containErr := to.ContainPoller(containCtx, owner, repo, restoreErr.userID)
	if containErr != nil {
		incomplete := errors.Is(containErr, ErrPollerContainmentIncomplete)
		containErr = safeAPIError(fmt.Sprintf("containing the GitLab Poller credential (user ID %d): revoke its personal access tokens and remove %s manually", restoreErr.userID, forge.SecretGitLabPollerToken), containErr)
		if incomplete {
			containErr = errors.Join(containErr, fmt.Errorf("%w: one or more authentication paths on the Poller service account may remain active (personal access tokens or pipeline trigger tokens that fullsend does not manage, managed tokens that stayed active after revocation, SSH keys, unfinished jobs, pipeline schedules, or an inventory that could not be read); inspect and remove them or have an administrator block the account", ErrPollerContainmentIncomplete))
		}
	} else {
		res.Details = append(res.Details, fmt.Sprintf("Revoked the GitLab Poller identity's (user ID %d) managed personal access tokens and removed %s; polling and webhook dispatch are unavailable until the identity is back at Developer access and 'fullsend repos install' provisions a replacement credential", restoreErr.userID, forge.SecretGitLabPollerToken))
	}
	// Teardown failures wrap server text that the redactor does not cover
	// (the administrative and installed Poller credentials), so only the
	// failure class is kept.
	return errors.Join(safeAPIError("tearing down the managed webhook fast path", teardownErr), containErr)
}

// ReconcileGitLabPollerElevation returns a managed Poller left above
// Developer (for example by an interrupted rotation) to Developer access, or
// contains its credential when that cannot be verified. Install calls it for
// every GitLab repository, independently of whether any webhook work is
// pending. Dry runs, a nil triggerOwner, and a Poller that is absent or not
// the managed service account change nothing.
func ReconcileGitLabPollerElevation(ctx context.Context, client forge.Client, triggerOwner GitLabTriggerOwner, owner, repo string, dryRun bool) (res GitLabWebhookResult, err error) {
	if dryRun {
		return GitLabWebhookResult{}, nil
	}
	release, lockErr := LockGitLabProject(ctx, client, owner, repo, false)
	if lockErr != nil {
		return GitLabWebhookResult{Action: "deferred"}, lockErr
	}
	red := &credentialRedactor{}
	defer func() {
		err = red.redact(err)
		release(&err)
	}()
	return reconcilePollerElevation(ctx, client, triggerOwner, owner, repo)
}

// reconcilePollerElevation returns a positively identified managed Poller
// that was left above Developer (for example by an interrupted earlier
// run) to Developer access. It runs ahead of the safety and readiness
// early returns so those cannot leave the installed Poller identity
// elevated. A failed restore or verification fails closed through
// failClosedPoller. A nil triggerOwner, a Poller confirmed absent or not
// managed (forge.ErrNotFound), or one that is not a project member changes
// nothing. An unexpected identity lookup failure is returned without
// modifying any account. When a positively identified managed Poller's
// effective access cannot be read, it is treated as possibly elevated: it is
// restored to Developer and verified, with the same fail-closed containment
// when verification fails.
//
// When the owner can list every managed Poller account (ManagedPollerLister),
// each duplicate is reconciled too, independently of which account supplies
// the runtime credential: an interrupted transaction may have elevated an
// account that a later run no longer selects.
func reconcilePollerElevation(ctx context.Context, client forge.Client, to GitLabTriggerOwner, owner, repo string) (res GitLabWebhookResult, err error) {
	if to == nil {
		return GitLabWebhookResult{}, nil
	}
	uid, idErr := to.PollerUserID(ctx, owner, repo)
	// A selected credential that is absent or not the managed Poller (for
	// example an administrator-supplied one) is left alone: its membership
	// must not be changed. That does not account for the independently
	// managed Poller accounts, which are still listed and reconciled below.
	selected := idErr == nil
	if idErr != nil && !forge.IsNotFound(idErr) {
		return GitLabWebhookResult{}, safeAPIError("resolving the GitLab Poller identity", idErr)
	}
	if selected {
		res, err = reconcileOnePoller(ctx, client, to, owner, repo, uid)
	}
	lister, ok := to.(ManagedPollerLister)
	if !ok {
		return res, err
	}
	ids, listErr := lister.ManagedPollerUserIDs(ctx, owner, repo)
	if listErr != nil {
		if forge.IsNotFound(listErr) {
			return res, err
		}
		return res, errors.Join(err, safeAPIError("listing the managed GitLab Poller identities", listErr))
	}
	errs := []error{err}
	for _, id := range ids {
		if selected && id == uid {
			continue
		}
		other, otherErr := reconcileOnePoller(ctx, client, to, owner, repo, id)
		if other.Action != "" {
			res.Action = other.Action
		}
		res.Details = append(res.Details, other.Details...)
		errs = append(errs, otherErr)
	}
	return res, errors.Join(errs...)
}

// ManagedPollerLister is implemented by a GitLabTriggerOwner that can list
// every fullsend-managed Poller service account (duplicates included) with
// installer authority. An error wrapping forge.ErrNotFound means there is
// none.
type ManagedPollerLister interface {
	ManagedPollerUserIDs(ctx context.Context, owner, repo string) ([]int64, error)
}

// reconcileOnePoller reconciles a single positively identified managed
// Poller account; see reconcilePollerElevation.
func reconcileOnePoller(ctx context.Context, client forge.Client, to GitLabTriggerOwner, owner, repo string, uid int64) (res GitLabWebhookResult, err error) {
	level, lvlErr := client.GetProjectMemberAccessLevel(ctx, owner, repo, uid)
	if forge.IsNotFound(lvlErr) || (lvlErr == nil && level <= forge.GitLabAccessLevelDeveloper) {
		return revokeOrphanedPollerBootstrap(ctx, client, to, owner, repo, uid, &res)
	}
	if restoreErr := restorePollerDeveloper(ctx, client, to, owner, repo, uid); restoreErr != nil {
		res.Action = "deferred"
		failure := &gitlabPollerRestoreError{userID: uid, err: restoreErr}
		return res, errors.Join(failure, failClosedPoller(ctx, client, to, owner, repo, &res, failure))
	}
	// An interrupted run that elevated the Poller may also have left the
	// installer-only bootstrap credential behind. It is revoked through
	// installer authority, independently of the (already revoked) previous
	// runtime credential.
	if orphanRes, orphanErr := revokeOrphanedPollerBootstrap(ctx, client, to, owner, repo, uid, &res); orphanErr != nil {
		return orphanRes, orphanErr
	}
	if lvlErr != nil {
		res.Details = []string{fmt.Sprintf("Verified the GitLab Poller identity (user ID %d) at Developer access after its access level could not be read", uid)}
		return res, nil
	}
	res.Details = []string{fmt.Sprintf("Restored the GitLab Poller identity (user ID %d) from access level %d to Developer", uid, level)}
	return res, nil
}

// revokeOrphanedPollerBootstrap revokes any installer-only bootstrap
// credential left on the managed Poller by an interrupted run and verifies
// the revocation. The credential's value is never persisted, so it is
// accounted for by name through installer authority. A failure fails closed
// like a failed restore: the Poller credential is contained.
func revokeOrphanedPollerBootstrap(ctx context.Context, client forge.Client, to GitLabTriggerOwner, owner, repo string, uid int64, res *GitLabWebhookResult) (GitLabWebhookResult, error) {
	cleanupCtx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	if err := to.RevokePollerBootstrap(cleanupCtx, owner, repo, uid); err != nil {
		res.Action = "deferred"
		failure := &gitlabPollerRestoreError{userID: uid, err: safeAPIError("revoking leftover bootstrap credentials", err), bootstrap: true}
		return *res, errors.Join(failure, failClosedPoller(ctx, client, to, owner, repo, res, failure))
	}
	return *res, nil
}

// PollerElevationUnsafeError is returned by
// GitLabTriggerOwner.VerifyPollerElevationSafe when elevating the Poller
// would be unsafe. Reason is authored by fullsend and carries no credential
// material, so it may be shown to the operator verbatim.
type PollerElevationUnsafeError struct {
	Reason string
}

func (e *PollerElevationUnsafeError) Error() string { return e.Reason }

// ErrPollerContainmentIncomplete marks a ContainPoller failure that leaves a
// credential usable on the Poller account. Its text is authored by fullsend
// and is kept when failClosedPoller sanitizes API failures.
var ErrPollerContainmentIncomplete = errors.New("containment is incomplete")

func pollerSafetyReason(err error) string {
	var unsafe *PollerElevationUnsafeError
	if errors.As(err, &unsafe) {
		return unsafe.Reason
	}
	return safeAPIError("verifying the Poller credential", err).Error()
}

// pollerMint is the outcome of mintPollerOwnedTrigger.
type pollerMint struct {
	minted  *forge.PipelineTriggerToken
	details []string
	// deferReason, when set, means no token was minted and the fast path
	// must stay deferred for that reason.
	deferReason string
	// revokedIDs lists the Poller-owned managed trigger tokens revoked before
	// elevation. When the webhook's active trigger is among them the webhook
	// is inert, so a compliant fast path can no longer be preserved; revoked
	// superseded triggers leave an unrelated compliant fast path intact.
	revokedIDs []int64
}

// mintPollerOwnedTrigger mints the managed pipeline trigger token as the
// Poller identity through a bootstrap-credential lifecycle (#8083). The
// caller holds the project lease for the whole transaction. The Poller must
// hold exactly Developer access, and the transaction runs in this order:
//
//  1. Account for every credential of the Poller account; refuse when one
//     cannot be accounted for.
//  2. Remove and revoke the distributed runtime credential and revoke the
//     Poller-owned managed triggers, so nothing distributed stays valid while
//     the Poller is elevated.
//  3. Create an installer-only bootstrap credential held only in memory.
//  4. Raise the Poller to Maintainer, create the trigger with the bootstrap
//     credential, then restore and verify Developer.
//  5. Revoke the bootstrap credential and only then publish a replacement
//     runtime credential.
//
// A restore or bootstrap-revocation failure revokes the new token and returns
// a *gitlabPollerRestoreError, and no runtime credential is published. Every
// other path returns with Developer access verified and publishes the
// replacement runtime credential if the original was revoked.
func mintPollerOwnedTrigger(ctx context.Context, client forge.Client, to GitLabTriggerOwner, owner, repo string, red *credentialRedactor) (pm pollerMint, err error) {
	// runtimeRevoked is set once the distributed runtime credential has been
	// (or may have been) invalidated; failClosed is set when the Poller may
	// exceed Developer or the bootstrap credential could not be accounted for,
	// in which case no runtime credential may be published.
	// revocationConfirmed is set only when that revocation succeeded; until
	// then the previous runtime PAT may survive and its cleanup tracking must
	// not be lost when a replacement is recorded.
	var runtimeRevoked, revocationConfirmed, failClosed bool
	var uid int64
	defer func() {
		if !runtimeRevoked || failClosed {
			return
		}
		if pubErr := publishPollerRuntimeCredential(ctx, client, to, owner, repo, uid, red, revocationConfirmed); pubErr != nil {
			err = errors.Join(err, pubErr)
			return
		}
		pm.details = append(pm.details, fmt.Sprintf("Published a replacement GitLab Poller runtime credential (%s) after its project role was verified at Developer access", forge.SecretGitLabPollerToken))
	}()
	uid, err = to.PollerUserID(ctx, owner, repo)
	if err != nil {
		if forge.IsNotFound(err) {
			return pollerMint{}, nil
		}
		return pollerMint{}, safeAPIError("resolving the GitLab Poller identity", err)
	}

	level, err := client.GetProjectMemberAccessLevel(ctx, owner, repo, uid)
	switch {
	case forge.IsNotFound(err):
		return pollerMint{deferReason: fmt.Sprintf("webhook fast-path blocked: the Poller identity (user ID %d) is not a member of the project, so it cannot own the pipeline trigger token. The polling schedules remain in effect", uid)}, nil
	case err != nil:
		return pollerMint{}, safeAPIError(fmt.Sprintf("reading the Poller identity (user ID %d) project access", uid), err)
	case level < forge.GitLabAccessLevelDeveloper:
		return pollerMint{deferReason: fmt.Sprintf("webhook fast-path blocked: the Poller identity (user ID %d) has less than Developer project access, so a trigger it owns could not start pipelines. The polling schedules remain in effect", uid)}, nil
	case level > forge.GitLabAccessLevelDeveloper:
		// A Poller left above Developer (for example by an interrupted
		// earlier run) is corrected before anything else.
		if restoreErr := restorePollerDeveloper(ctx, client, to, owner, repo, uid); restoreErr != nil {
			return pollerMint{}, &gitlabPollerRestoreError{userID: uid, err: restoreErr}
		}
	}

	// Refuse elevation unless every credential of the account can be
	// accounted for: one that fullsend does not manage could not be revoked
	// and would gain Maintainer access during the create window.
	if safeErr := to.VerifyPollerElevationSafe(ctx, owner, repo, uid); safeErr != nil {
		return pollerMint{deferReason: fmt.Sprintf("webhook fast-path blocked: the Poller identity (user ID %d) was not raised to temporary Maintainer access because %s. The polling schedules remain in effect", uid, pollerSafetyReason(safeErr))}, nil
	}

	quiescence, ok := to.(GitLabPollerQuiescenceVerifier)
	if !ok {
		return pollerMint{deferReason: "webhook fast-path blocked: GitLab cannot verify that previously authenticated Poller requests and asynchronous jobs have drained before temporary Maintainer access; polling schedules remain in effect"}, nil
	}

	// The distributed runtime credential would gain the elevated role as well
	// and is available to running jobs, so it is invalidated before the
	// elevation (deleting the CI/CD variable alone would not revoke it). The
	// replacement is published only after Developer access is restored and the
	// bootstrap credential is revoked. This interrupts polling and in-flight
	// jobs that use the Poller credential until then.
	runtimeRevoked = true
	if revErr := to.RevokePollerRuntimeCredentials(ctx, owner, repo, uid); revErr != nil {
		return pollerMint{}, safeAPIError("revoking the distributed GitLab Poller runtime credential before raising the Poller", revErr)
	}
	revocationConfirmed = true
	runtimeDetails := []string{fmt.Sprintf("Revoked the distributed GitLab Poller runtime credential and removed %s before raising the Poller to temporary Maintainer access; polling and in-flight jobs that use it are interrupted until the replacement is published", forge.SecretGitLabPollerToken)}

	// A trigger token acts with its owner's permissions, so any managed
	// trigger the Poller already owns (and the webhook URL that embeds it)
	// would run with Maintainer access while the Poller is elevated.
	// Revoke those before the elevation; the replacement is minted below and
	// dispatch resumes only once Developer access is restored and verified.
	revoked, revokeErr := revokePollerOwnedManagedTriggers(ctx, client, owner, repo, uid)
	revokedDetails := runtimeDetails
	if len(revoked) > 0 {
		revokedDetails = append(revokedDetails, fmt.Sprintf("Revoked %d Poller-owned pipeline trigger token(s) before raising the Poller to temporary Maintainer access; webhook dispatch resumes once Developer access is restored", len(revoked)))
	}
	if revokeErr != nil {
		return pollerMint{details: revokedDetails, revokedIDs: revoked}, revokeErr
	}

	// The first safety check ran while the distributed runtime credential
	// was still valid, and a holder of it could have registered a credential
	// (such as an SSH key) that the revocations above do not invalidate. With
	// the runtime credential gone, account for the credentials again before
	// the elevation.
	if safeErr := to.VerifyPollerCredentialsRevoked(ctx, owner, repo, uid); safeErr != nil {
		return pollerMint{details: revokedDetails, revokedIDs: revoked, deferReason: fmt.Sprintf("webhook fast-path blocked: the Poller identity (user ID %d) was not raised to temporary Maintainer access because %s after its runtime credential was revoked. The polling schedules remain in effect", uid, pollerSafetyReason(safeErr))}, nil
	}

	if quietErr := quiescence.VerifyPollerQuiescence(ctx, owner, repo, uid); quietErr != nil {
		return pollerMint{details: revokedDetails, revokedIDs: revoked, deferReason: "webhook fast-path blocked: Poller requests accepted before revocation may still create credentials or jobs; temporary Maintainer access was deferred and polling schedules remain in effect"}, nil
	}

	// The installer-only bootstrap credential authenticates the trigger
	// creation. Its value lives only in this function; it is never published.
	// An ambiguous or failed creation is still followed by a revocation by
	// name so no orphan survives.
	bootstrap, bootErr := to.CreatePollerBootstrap(ctx, owner, repo, uid)
	if bootstrap != nil {
		red.add(bootstrap.Token)
	}
	if bootErr == nil && (bootstrap == nil || bootstrap.Token == "") {
		bootErr = errors.New("GitLab returned no token value")
	}
	if bootErr != nil {
		bootErr = safeAPIError("creating the installer-only bootstrap credential for the Poller", bootErr)
		if revErr := revokePollerBootstrapDetached(ctx, to, owner, repo, uid); revErr != nil {
			// The bootstrap credential could not be accounted for: do not
			// publish a runtime credential next to it. The typed failure makes
			// the caller contain the Poller and tear down the managed fast
			// path immediately, under the lease already held.
			failClosed = true
			return pollerMint{details: revokedDetails, revokedIDs: revoked}, &gitlabPollerRestoreError{userID: uid, err: errors.Join(bootErr, revErr), bootstrap: true}
		}
		return pollerMint{details: revokedDetails, revokedIDs: revoked}, bootErr
	}

	if err := to.SetProjectMemberAccessLevel(ctx, owner, repo, uid, forge.GitLabAccessLevelMaintainer); err != nil {
		// A failed elevation may still have changed the role.
		if restoreErr := restorePollerDeveloper(ctx, client, to, owner, repo, uid); restoreErr != nil {
			failClosed = true
			return pollerMint{details: revokedDetails, revokedIDs: revoked}, &gitlabPollerRestoreError{userID: uid, err: restoreErr}
		}
		if revErr := revokePollerBootstrapDetached(ctx, to, owner, repo, uid); revErr != nil {
			failClosed = true
			return pollerMint{details: revokedDetails, revokedIDs: revoked}, &gitlabPollerRestoreError{userID: uid, err: revErr, bootstrap: true}
		}
		if forge.IsForbidden(err) || forge.IsNotFound(err) {
			return pollerMint{details: revokedDetails, revokedIDs: revoked, deferReason: fmt.Sprintf("webhook fast-path blocked: the Poller identity (user ID %d) could not be granted temporary Maintainer access to create the pipeline trigger token (%v). Project access token bots cannot change role; re-run install on an instance with project service accounts so the Poller is a service account. The polling schedules remain in effect", uid, safeAPIError("updating the Poller membership", err))}, nil
		}
		return pollerMint{details: revokedDetails, revokedIDs: revoked}, safeAPIError(fmt.Sprintf("granting the Poller identity (user ID %d) temporary Maintainer access", uid), err)
	}

	minted, mintErr := to.CreatePipelineTriggerTokenAsPoller(ctx, owner, repo, GitLabWebhookTriggerDescription, bootstrap)
	if minted != nil {
		red.add(minted.Token)
	}
	restoreErr := restorePollerDeveloper(ctx, client, to, owner, repo, uid)
	// Developer access is restored (or not) first; the bootstrap credential
	// is revoked either way and its revocation verified.
	var bootRevokeErr error
	if restoreErr == nil {
		bootRevokeErr = revokePollerBootstrapDetached(ctx, to, owner, repo, uid)
	}
	if restoreErr != nil || bootRevokeErr != nil {
		failClosed = true
		var errs []error
		if mintErr == nil && minted != nil && minted.ID != 0 {
			cleanupCtx, cancel := gitlabCleanupContext(ctx)
			defer cancel()
			if revokeErr := client.RevokePipelineTriggerToken(cleanupCtx, owner, repo, minted.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
				errs = append(errs, safeAPIError(fmt.Sprintf("revoking pipeline trigger token ID %d minted before the failed restore", minted.ID), revokeErr))
			}
		}
		failure := &gitlabPollerRestoreError{userID: uid, err: restoreErr}
		if restoreErr == nil {
			failure = &gitlabPollerRestoreError{userID: uid, err: bootRevokeErr, bootstrap: true}
		}
		return pollerMint{details: revokedDetails, revokedIDs: revoked}, errors.Join(append([]error{failure}, errs...)...)
	}
	if mintErr != nil {
		return pollerMint{details: revokedDetails, revokedIDs: revoked}, safeAPIError("creating pipeline trigger token as the Poller identity", mintErr)
	}
	if minted == nil || minted.Token == "" {
		return pollerMint{details: revokedDetails, revokedIDs: revoked}, errors.New("creating pipeline trigger token as the Poller identity: GitLab returned no token value")
	}
	if minted.OwnerID != uid {
		var revokeErr error
		cleanupCtx, cancel := gitlabCleanupContext(ctx)
		defer cancel()
		if err := client.RevokePipelineTriggerToken(cleanupCtx, owner, repo, minted.ID); err != nil && !forge.IsNotFound(err) {
			revokeErr = safeAPIError(fmt.Sprintf("revoking pipeline trigger token ID %d", minted.ID), err)
		}
		return pollerMint{details: revokedDetails, revokedIDs: revoked}, errors.Join(fmt.Errorf("pipeline trigger token ID %d minted as the Poller identity reports owner user ID %d, not %d", minted.ID, minted.OwnerID, uid), revokeErr)
	}
	return pollerMint{
		minted:     minted,
		details:    append(revokedDetails, fmt.Sprintf("Created pipeline trigger token as the Poller identity (user ID %d) with temporary Maintainer access using an installer-only bootstrap credential; restored and verified Developer access and revoked the bootstrap credential", uid)),
		revokedIDs: revoked,
	}, nil
}

// revokePollerBootstrapDetached revokes the bootstrap credential on a bounded
// context detached from ctx's cancellation, so a canceled operation cannot
// leave it valid. The failure keeps only its class: GitLab's response may echo
// credentials the redactor does not know.
func revokePollerBootstrapDetached(ctx context.Context, to GitLabTriggerOwner, owner, repo string, uid int64) error {
	cleanupCtx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	if err := to.RevokePollerBootstrap(cleanupCtx, owner, repo, uid); err != nil {
		return safeAPIError("revoking the installer-only Poller bootstrap credential", err)
	}
	return nil
}

// publishPollerRuntimeCredential creates and stores a replacement runtime
// credential for the Poller. It must be called only when the Poller is
// verified at Developer access and no bootstrap credential remains. It runs on
// a bounded context detached from ctx's cancellation so a canceled install
// does not leave the Poller without a credential. A failed store revokes the
// new credential; the next install provisions a replacement because the
// secret is absent.
func publishPollerRuntimeCredential(ctx context.Context, client forge.Client, to GitLabTriggerOwner, owner, repo string, uid int64, red *credentialRedactor, revocationConfirmed bool) error {
	ctx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	now := time.Now()
	expiresAt := GitLabPATExpiresAt(now)
	tok, err := to.CreatePollerRuntimeToken(ctx, owner, repo, uid, expiresAt)
	if tok != nil {
		red.add(tok.Token)
	}
	if err != nil {
		return safeAPIError("creating the replacement GitLab Poller runtime credential; re-run 'fullsend repos install' to provision it", err)
	}
	if tok == nil || tok.Token == "" {
		return errors.New("creating the replacement GitLab Poller runtime credential: GitLab returned no token value; re-run 'fullsend repos install' to provision it")
	}
	if !canMaskGitLabValue(tok.Token) {
		// A value GitLab cannot mask must not be stored: it would reach
		// protected job logs in the clear. Revoke it on a fresh detached
		// context; the next install provisions a replacement.
		var revokeErr error
		revokeCtx, cancelRevoke := gitlabCleanupContext(ctx)
		if err := to.RevokePollerRuntimeCredentials(revokeCtx, owner, repo, uid); err != nil {
			revokeErr = safeAPIError("revoking the replacement GitLab Poller runtime credential that cannot be masked", err)
		}
		cancelRevoke()
		return errors.Join(errors.New("the replacement GitLab Poller runtime credential cannot be masked (it must be a single line of at least 8 characters using GitLab's allowed charset) and was not stored; re-run 'fullsend repos install' to provision it"), revokeErr)
	}
	if storeErr := client.CreateRepoSecret(ctx, owner, repo, forge.SecretGitLabPollerToken, tok.Token); storeErr != nil {
		// The publication budget may be exhausted (a store that times out
		// leaves nothing of ctx), so the compensating revocation runs on its
		// own fresh bounded context detached from ctx.
		var revokeErr error
		revokeCtx, cancelRevoke := gitlabCleanupContext(ctx)
		if err := to.RevokePollerRuntimeCredentials(revokeCtx, owner, repo, uid); err != nil {
			revokeErr = safeAPIError("revoking the replacement GitLab Poller runtime credential that could not be stored", err)
		}
		cancelRevoke()
		return errors.Join(safeAPIError(fmt.Sprintf("storing the replacement %s; re-run 'fullsend repos install' to provision it", forge.SecretGitLabPollerToken), storeErr), revokeErr)
	}
	// Rotation-state proof is advisory: the credential itself is live, and a
	// missing proof only makes a later rotation run replace it. The role's
	// previous entry names the credential revoked before elevation, so it is
	// replaced, not preserved (the caller holds the project lease), unless
	// that revocation was not confirmed: then the previous credential may
	// still be live and stays tracked for cleanup.
	_ = recordPollerReplacementDistribution(ctx, client, owner, repo, tok.ID, expiresAt, now, !revocationConfirmed)
	return nil
}

// recordPollerReplacementDistribution records rotation-state proof for a
// replacement Poller runtime credential published by
// publishPollerRuntimeCredential. The caller revoked only the managed
// runtime PATs on the selected service account, so outgoing IDs recorded
// earlier (for example a legacy project access token queued by role
// rotation) are not confirmed revoked and stay scheduled for grace cleanup
// under an overlapping phase. When the previous revocation was not confirmed,
// the previous incoming credential may still be live and stays tracked too.
// The exclusion of any administrator-owned account the previous entry
// recorded survives the replacement.
func recordPollerReplacementDistribution(ctx context.Context, client forge.Client, owner, repo string, tokenID int, expiresAt string, now time.Time, previousRevocationUnconfirmed bool) error {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before replacement distribution proof: %w", err)
	}
	previous := state.Roles[string(gitlabroles.RolePoller)]
	var outgoing []int
	for _, id := range previous.OutgoingIDs {
		if id != tokenID && !containsInt(outgoing, id) {
			outgoing = append(outgoing, id)
		}
	}
	if previousRevocationUnconfirmed && previous.IncomingID != 0 && previous.IncomingID != tokenID && !containsInt(outgoing, previous.IncomingID) {
		outgoing = append(outgoing, previous.IncomingID)
	}
	phase := rotationPhaseIdle
	if len(outgoing) > 0 {
		phase = rotationPhaseOverlapping
	}
	excluded := append([]int(nil), previous.ExcludedUserIDs...)
	if previous.SuppliedUserID > 0 && !containsInt(excluded, previous.SuppliedUserID) {
		excluded = append(excluded, previous.SuppliedUserID)
		sort.Ints(excluded)
	}
	state.Roles[string(gitlabroles.RolePoller)] = rotationRoleState{
		CreatedTokenIDs: uniqueInts(append(append([]int(nil), previous.CreatedTokenIDs...), tokenID)),
		ManagedUserID:   previous.ManagedUserID,
		Phase:           phase,
		IncomingID:      tokenID,
		OutgoingIDs:     outgoing,
		DistributedAt:   now.UTC().Format(time.RFC3339),
		ExpiresAt:       expiresAt,
		ExcludedUserIDs: excluded,
	}
	return writeRotationState(ctx, client, owner, repo, state)
}

// restorePollerDeveloper sets the Poller back to Developer, retrying the
// update once, and verifies the effective access level. It runs on a
// bounded context detached from ctx's cancellation so a canceled operation
// cannot leave the Poller above Developer.
func restorePollerDeveloper(ctx context.Context, client forge.Client, to GitLabTriggerOwner, owner, repo string, uid int64) error {
	ctx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	// The membership and access responses may echo credentials that the
	// redactor does not know (the administrative token or the installed
	// Poller token), so only the failure class is kept.
	setErr := to.SetProjectMemberAccessLevel(ctx, owner, repo, uid, forge.GitLabAccessLevelDeveloper)
	if setErr != nil {
		setErr = to.SetProjectMemberAccessLevel(ctx, owner, repo, uid, forge.GitLabAccessLevelDeveloper)
	}
	if setErr != nil {
		setErr = safeAPIError("updating the Poller membership to Developer", setErr)
	}
	level, err := client.GetProjectMemberAccessLevel(ctx, owner, repo, uid)
	if err != nil {
		return errors.Join(setErr, safeAPIError("verifying effective access", err))
	}
	if level != forge.GitLabAccessLevelDeveloper {
		return errors.Join(setErr, fmt.Errorf("effective project access level is %d, want %d (Developer)", level, forge.GitLabAccessLevelDeveloper))
	}
	return nil
}

// revokePollerOwnedManagedTriggers revokes every Fullsend-managed pipeline
// trigger token owned by the Poller (or whose owner GitLab did not report)
// and returns the IDs that were revoked. Trigger tokens act with their owner's
// permissions, so these must not outlive the start of a temporary
// elevation. Revocation runs on a bounded context detached from ctx.
func revokePollerOwnedManagedTriggers(ctx context.Context, client forge.Client, owner, repo string, uid int64) ([]int64, error) {
	cleanupCtx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	triggers, err := client.ListPipelineTriggerTokens(cleanupCtx, owner, repo)
	if err != nil {
		return nil, safeAPIError("listing pipeline trigger tokens before raising the Poller", err)
	}
	var revoked []int64
	var errs []error
	for _, t := range triggers {
		if t.Description != GitLabWebhookTriggerDescription || (t.OwnerID != uid && t.OwnerID != 0) {
			continue
		}
		if revokeErr := client.RevokePipelineTriggerToken(cleanupCtx, owner, repo, t.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
			errs = append(errs, safeAPIError(fmt.Sprintf("revoking Poller-owned pipeline trigger token ID %d before raising the Poller", t.ID), revokeErr))
			continue
		}
		revoked = append(revoked, t.ID)
	}
	if len(errs) > 0 {
		return revoked, errors.Join(errs...)
	}
	// A successful DELETE is not proof of absence. Re-list the triggers and
	// refuse to continue while any managed trigger the Poller owns (or whose
	// owner GitLab did not report) is still listed, because the elevation
	// inventory excludes managed triggers. An unreadable inventory fails
	// closed.
	remaining, err := client.ListPipelineTriggerTokens(cleanupCtx, owner, repo)
	if err != nil {
		return revoked, safeAPIError("verifying revocation of the Poller-owned pipeline trigger tokens before raising the Poller", err)
	}
	var stillListed []int64
	for _, t := range remaining {
		if t.Description == GitLabWebhookTriggerDescription && (t.OwnerID == uid || t.OwnerID == 0) {
			stillListed = append(stillListed, t.ID)
		}
	}
	if len(stillListed) > 0 {
		return revoked, fmt.Errorf("Poller-owned pipeline trigger token(s) %v are still listed after revocation; the Poller was not raised", stillListed)
	}
	return revoked, nil
}

// GitLabPollerUninstallReconciler is implemented by the GitLab role token
// client the CLI hands to uninstall. Uninstall calls it under the project
// lease, before role credential cleanup, so a Poller account left elevated or
// holding an installer-only bootstrap credential by an interrupted
// trigger-creation transaction is not left behind by a successful uninstall.
//
// An error wrapping ErrPollerSuppliedUnresolved means the installed Poller
// credential is recorded as administrator-supplied but its owner could not be
// attributed. Uninstall then leaves the installation state (including that
// provenance) in place and revokes nothing, so a retry can still exclude the
// supplied account and its tokens.
type GitLabPollerUninstallReconciler interface {
	ReconcileGitLabPollersForUninstall(ctx context.Context, client forge.Client, owner, repo string) error
}

// ReconcileManagedPollersForUninstall restores every listed managed Poller
// account (duplicates included) to at most Developer access, verifying the
// effective level, and revokes and verifies revocation of its bootstrap
// credentials. Every account is attempted and the failures are joined: a
// failure means the cleanup could not be confirmed, and uninstall must fail
// so the manifest entry is retained for retry. Server text is withheld from
// errors because it may echo credentials the redactor does not know.
func ReconcileManagedPollersForUninstall(ctx context.Context, client forge.Client, to GitLabTriggerOwner, owner, repo string, userIDs []int64) error {
	var errs []error
	for _, uid := range userIDs {
		lvlCtx, cancel := gitlabCleanupContext(ctx)
		level, lvlErr := client.GetProjectMemberAccessLevel(lvlCtx, owner, repo, uid)
		cancel()
		// A Poller that is not a member (or is at most Developer) holds no
		// elevated access; any other outcome is restored and verified.
		if !forge.IsNotFound(lvlErr) && (lvlErr != nil || level > forge.GitLabAccessLevelDeveloper) {
			if err := restorePollerDeveloper(ctx, client, to, owner, repo, uid); err != nil {
				errs = append(errs, fmt.Errorf("restoring the GitLab Poller identity (user ID %d) to Developer access before uninstall: %w", uid, err))
				// The positively identified managed Poller may still be
				// elevated and hold a valid runtime credential, and the
				// failed uninstall skips role-credential cleanup. Contain it
				// as install does, on its own bounded context detached from
				// cancellation. The rotation provenance and manifest entry stay
				// in place for the retry.
				containCtx, cancelContain := gitlabCleanupContext(ctx)
				containErr := to.ContainPoller(containCtx, owner, repo, uid)
				cancelContain()
				if containErr != nil {
					errs = append(errs, safeAPIError(fmt.Sprintf("containing the GitLab Poller credential (user ID %d) after the restore failed: revoke its personal access tokens and remove %s manually", uid, forge.SecretGitLabPollerToken), containErr))
				}
			}
		}
		if err := revokePollerBootstrapDetached(ctx, to, owner, repo, uid); err != nil {
			errs = append(errs, fmt.Errorf("revoking the bootstrap credential of the GitLab Poller identity (user ID %d) before uninstall: %w", uid, err))
		}
	}
	return errors.Join(errs...)
}
