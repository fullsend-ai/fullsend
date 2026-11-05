package gitlablifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// TriggerOwner implements the fresh-identity Poller generation handoff
// (#8243): a new project service account that never held a distributed
// credential mints the webhook trigger, so the current Poller is never
// re-elevated. The repository policy (repos.handoffPollerGeneration) owns the
// ordering, the durable generation record, and every safety decision; these
// methods are installer-authorized GitLab transport.
var _ repos.GitLabPollerHandoff = (*TriggerOwner)(nil)

// CreateReplacementPoller creates a fresh fullsend-poller project service
// account with no personal access token and adds it to the project at
// Developer access. It never changes the protected-branch rule: whether the
// account may start pipelines on the default branch is checked read-only by
// EnsureReplacementPollerAccess before the account is raised. The account ID
// is returned whenever the account was created, even when a later step
// failed. A refused or unsupported create means no account was created and is
// reported as forge.ErrNotFound, so the handoff defers and polling continues.
func (o *TriggerOwner) CreateReplacementPoller(ctx context.Context, owner, repo string) (int64, error) {
	sa, err := o.Admin.CreateProjectServiceAccount(ctx, owner, repo, gitlabroles.PollerTokenName)
	if err != nil {
		if forge.IsNotFound(err) || forge.IsNotSupported(err) || forge.IsForbidden(err) {
			return 0, fmt.Errorf("project service accounts are unavailable to the installer (%v): %w", repos.SafeAPIError("creating the replacement Poller service account", err), forge.ErrNotFound)
		}
		return 0, err
	}
	if sa == nil || sa.ID <= 0 {
		return 0, errors.New("GitLab returned no service account ID")
	}
	uid := int64(sa.ID)
	if err := o.ensureReplacementMember(ctx, owner, repo, uid); err != nil {
		return uid, err
	}
	return uid, nil
}

// EnsureReplacementPollerAccess idempotently establishes the replacement
// account's Developer membership and verifies, read-only, that the existing
// protected-branch policy lets it start pipelines on the default branch. The
// handoff runs it before every elevation attempt, so a membership or policy
// failure in an earlier run is retried rather than left to quarantine the
// generation after a trigger was minted. A policy that does not admit the
// account is reported as a *repos.PollerGenerationUnsafeError, which defers
// the handoff; fullsend never writes the branch rule for a replacement.
func (o *TriggerOwner) EnsureReplacementPollerAccess(ctx context.Context, owner, repo string, userID int64) error {
	if err := o.ensureReplacementMember(ctx, owner, repo, userID); err != nil {
		return err
	}
	if err := repos.VerifyGitLabPollerProtectedPipelineAccess(ctx, o.Admin, owner, repo, int(userID)); err != nil {
		var denied *repos.PollerPipelineAccessDeniedError
		if errors.As(err, &denied) {
			return &repos.PollerGenerationUnsafeError{Reason: denied.Error() + "; fullsend does not broaden branch protection, so allow the Poller to start pipelines on that branch"}
		}
		return fmt.Errorf("reading protected default-branch pipeline access for the replacement Poller service account (user ID %d): %w", userID, err)
	}
	return nil
}

// ensureReplacementMember makes the account a direct project member at
// Developer access, creating or lowering the membership as needed.
func (o *TriggerOwner) ensureReplacementMember(ctx context.Context, owner, repo string, uid int64) error {
	if err := o.Admin.AddProjectMember(ctx, owner, repo, int(uid), forge.GitLabAccessLevelDeveloper); err != nil {
		if !forge.IsAlreadyExists(err) {
			return fmt.Errorf("adding the replacement Poller service account (user ID %d) as a Developer: %w", uid, err)
		}
		if err := o.Admin.UpdateProjectMemberAccessLevel(ctx, owner, repo, int(uid), forge.GitLabAccessLevelDeveloper); err != nil {
			return fmt.Errorf("setting the replacement Poller service account (user ID %d) to Developer: %w", uid, err)
		}
	}
	return nil
}

// VerifyReplacementPollerCredentialsRevoked requires that the replacement
// account holds no active personal access token, no unmanaged pipeline
// trigger, no usable SSH key, no unfinished job, and no pipeline schedule.
// Managed token names are not evidence of revocation.
func (o *TriggerOwner) VerifyReplacementPollerCredentialsRevoked(ctx context.Context, owner, repo string, userID int64) error {
	err := o.verifyPollerCredentials(ctx, owner, repo, userID, true)
	var unsafe *repos.PollerElevationUnsafeError
	if errors.As(err, &unsafe) {
		return &repos.PollerGenerationUnsafeError{Reason: unsafe.Reason}
	}
	return err
}

// CreateReplacementPollerBootstrap creates the installer-only bootstrap
// personal access token for the replacement account. Any bootstrap token left
// by an interrupted run is revoked first. The value is returned to the caller
// and never stored.
func (o *TriggerOwner) CreateReplacementPollerBootstrap(ctx context.Context, owner, repo string, userID int64) (*repos.ReplacementPollerBootstrap, error) {
	tok, err := o.CreatePollerBootstrap(ctx, owner, repo, userID)
	if err != nil || tok == nil {
		return nil, err
	}
	return &repos.ReplacementPollerBootstrap{ID: tok.ID, Token: tok.Token}, nil
}

// RevokeReplacementPollerBootstrap revokes every active bootstrap personal
// access token of the replacement account and verifies none remains active.
func (o *TriggerOwner) RevokeReplacementPollerBootstrap(ctx context.Context, owner, repo string, userID int64) error {
	return o.RevokePollerBootstrap(ctx, owner, repo, userID)
}

// CreatePipelineTriggerTokenAsReplacementPoller creates the trigger token as
// the replacement account by authenticating with its bootstrap credential.
func (o *TriggerOwner) CreatePipelineTriggerTokenAsReplacementPoller(ctx context.Context, owner, repo, description string, bootstrap *repos.ReplacementPollerBootstrap) (*forge.PipelineTriggerToken, error) {
	if bootstrap == nil || bootstrap.Token == "" {
		return nil, errors.New("no bootstrap credential")
	}
	return o.CreatePipelineTriggerTokenAsPoller(ctx, owner, repo, description, &repos.PollerBootstrap{ID: bootstrap.ID, Token: bootstrap.Token})
}

// VerifyTriggerStartsProtectedPipeline proves, after demotion, that the
// trigger's owner is at exactly Developer access and that the existing
// protected-branch policy lets it start pipelines on the default branch. It
// reads the policy and never changes it, and it does not start a pipeline, so
// no dispatcher job runs as a side effect of the probe.
func (o *TriggerOwner) VerifyTriggerStartsProtectedPipeline(ctx context.Context, owner, repo string, trigger *forge.PipelineTriggerToken) error {
	if trigger == nil || trigger.OwnerID <= 0 {
		return errors.New("the pipeline trigger token has no reported owner")
	}
	level, err := o.Admin.GetProjectMemberAccessLevel(ctx, owner, repo, trigger.OwnerID)
	if err != nil {
		return fmt.Errorf("reading the trigger owner's project access: %w", err)
	}
	if level != forge.GitLabAccessLevelDeveloper {
		return fmt.Errorf("the trigger owner (user ID %d) has project access level %d, not Developer", trigger.OwnerID, level)
	}
	return repos.VerifyGitLabPollerProtectedPipelineAccess(ctx, o.Admin, owner, repo, int(trigger.OwnerID))
}
