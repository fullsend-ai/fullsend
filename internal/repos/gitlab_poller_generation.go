package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// Poller identity generations (#8210, design validated in #8209).
//
// GitLab binds a pipeline trigger token to its creator and requires
// Maintainer access to create one. Re-elevating a Poller identity whose
// runtime credential was ever distributed is unsafe: requests accepted with a
// revoked credential can still complete after revocation, and GitLab exposes
// no drain barrier (#8205). Instead, a fresh project service account (a new
// Poller generation) that has never held a distributed credential is raised
// to Maintainer, creates the trigger with an installer-held bootstrap
// credential, is demoted and independently verified at Developer, and only
// then may receive a runtime credential. The current Poller stays at
// Developer and keeps polling throughout.
//
// The generation document records every account by numeric ID before it is
// acted on, so reconciliation and cleanup never match accounts by name, and it
// records the single trigger-create attempt a pending generation may make. A
// generation whose create result is unknown, or which could not be verified
// at Developer, is quarantined: no further generation starts until an
// operator resolves it, and polling stays in effect.

// PollerGenerationPhase is the handoff phase of a pending Poller generation.
type PollerGenerationPhase string

const (
	// PollerPhaseAccountRequested is recorded before the account-create
	// request. A pending generation still in this phase has no recorded
	// account ID: the response was lost, and an operator must reconcile the
	// account manually rather than risk deleting an unrelated one.
	PollerPhaseAccountRequested PollerGenerationPhase = "account_requested"
	// PollerPhaseAccountRecorded means the fresh account exists at Developer
	// access with its ID recorded and has never been elevated by this
	// generation's trigger attempt. It is safe to resume.
	PollerPhaseAccountRecorded PollerGenerationPhase = "account_recorded"
	// PollerPhaseElevated is recorded before the account is raised to
	// Maintainer and before any trigger-create request.
	PollerPhaseElevated PollerGenerationPhase = "elevated"
	// PollerPhaseTriggerRequested is recorded before the generation's only
	// trigger-create request.
	PollerPhaseTriggerRequested PollerGenerationPhase = "trigger_requested"
	// PollerPhaseVerified means the generation owns a confirmed trigger, its
	// bootstrap credential is revoked, no other personal access token is
	// active, and Developer access was verified independently. It awaits
	// cutover.
	PollerPhaseVerified PollerGenerationPhase = "verified"
	// PollerPhaseQuarantined blocks every further generation until an
	// operator resolves the recorded reason.
	PollerPhaseQuarantined PollerGenerationPhase = "quarantined"
)

func (p PollerGenerationPhase) valid() bool {
	switch p {
	case PollerPhaseAccountRequested, PollerPhaseAccountRecorded, PollerPhaseElevated,
		PollerPhaseTriggerRequested, PollerPhaseVerified, PollerPhaseQuarantined:
		return true
	}
	return false
}

// PendingPollerGeneration is the at most one Poller generation in handoff.
type PendingPollerGeneration struct {
	UserID int64                 `json:"user_id,omitempty"`
	Phase  PollerGenerationPhase `json:"phase"`
	// TriggerAttempted is set before the single trigger-create request and
	// never cleared for this generation: a create request cannot be fenced
	// once sent, so it is never retried.
	TriggerAttempted bool `json:"trigger_attempted,omitempty"`
	// TriggerID is the confirmed trigger owned by this generation. The
	// trigger secret is never persisted; it is held in memory by the run that
	// created it.
	TriggerID int64 `json:"trigger_id,omitempty"`
	// Reason explains a quarantine. It is authored by fullsend and carries no
	// credential material.
	Reason string `json:"reason,omitempty"`
}

// PollerGenerationState is the durable Poller generation document.
type PollerGenerationState struct {
	// CurrentUserID is the Poller account whose runtime credential is
	// published (zero when not yet recorded).
	CurrentUserID int64 `json:"current_user_id,omitempty"`
	// Pending is the generation being handed off, if any.
	Pending *PendingPollerGeneration `json:"pending,omitempty"`
	// RetiringUserID is the superseded Poller account whose schedules,
	// credentials, triggers, and account are still being retired. It stays at
	// Developer until retirement completes.
	RetiringUserID int64 `json:"retiring_user_id,omitempty"`
}

type pollerGenerationEnvelope struct {
	Version int `json:"version"`
	PollerGenerationState
}

const gitLabPollerGenerationStateVersion = 1

// loadPollerGenerations reads and validates the generation document. A
// missing or empty variable is an empty state.
func loadPollerGenerations(ctx context.Context, client forge.Client, owner, repo string) (PollerGenerationState, error) {
	raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabPollerGenerations)
	if err != nil {
		return PollerGenerationState{}, safeAPIError("reading "+forge.VarGitLabPollerGenerations, err)
	}
	if !exists || strings.TrimSpace(raw) == "" {
		return PollerGenerationState{}, nil
	}
	return decodePollerGenerations(raw)
}

func decodePollerGenerations(raw string) (PollerGenerationState, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var env pollerGenerationEnvelope
	if err := dec.Decode(&env); err != nil {
		return PollerGenerationState{}, fmt.Errorf("decode %s: %w", forge.VarGitLabPollerGenerations, err)
	}
	if env.Version != gitLabPollerGenerationStateVersion {
		return PollerGenerationState{}, fmt.Errorf("unsupported %s version %d; use a compatible CLI", forge.VarGitLabPollerGenerations, env.Version)
	}
	st := env.PollerGenerationState
	if st.CurrentUserID < 0 || st.RetiringUserID < 0 {
		return PollerGenerationState{}, fmt.Errorf("invalid %s: account IDs must not be negative", forge.VarGitLabPollerGenerations)
	}
	if p := st.Pending; p != nil {
		if !p.Phase.valid() {
			return PollerGenerationState{}, fmt.Errorf("invalid %s: unknown pending phase %q", forge.VarGitLabPollerGenerations, p.Phase)
		}
		if p.UserID < 0 || p.TriggerID < 0 {
			return PollerGenerationState{}, fmt.Errorf("invalid %s: pending IDs must not be negative", forge.VarGitLabPollerGenerations)
		}
		if p.UserID == 0 && p.Phase != PollerPhaseAccountRequested && p.Phase != PollerPhaseQuarantined {
			return PollerGenerationState{}, fmt.Errorf("invalid %s: pending phase %q requires a recorded account ID", forge.VarGitLabPollerGenerations, p.Phase)
		}
	}
	return st, nil
}

func writePollerGenerations(ctx context.Context, client forge.Client, owner, repo string, st PollerGenerationState) error {
	raw, err := json.Marshal(pollerGenerationEnvelope{Version: gitLabPollerGenerationStateVersion, PollerGenerationState: st})
	if err != nil {
		return err
	}
	if err := client.UpdateCIVariable(ctx, owner, repo, forge.VarGitLabPollerGenerations, string(raw), true); err != nil {
		return safeAPIError("recording "+forge.VarGitLabPollerGenerations, err)
	}
	return nil
}

// pollerHandoffBlocked returns why no Poller generation may start or resume,
// or "" when one may. Interrupted elevated and trigger-requested generations
// are not blocked here: the handoff first returns them to Developer.
func pollerHandoffBlocked(st PollerGenerationState) string {
	if st.RetiringUserID != 0 {
		return fmt.Sprintf("webhook fast-path blocked: the superseded GitLab Poller identity (user ID %d) has not finished retiring, so no replacement Poller generation is started. The polling schedules remain in effect", st.RetiringUserID)
	}
	p := st.Pending
	if p == nil {
		return ""
	}
	switch p.Phase {
	case PollerPhaseAccountRequested:
		return "webhook fast-path blocked: a replacement GitLab Poller service account was requested but its account ID was never recorded. Identify the fullsend-poller service account created by that request, remove it, and clear " + forge.VarGitLabPollerGenerations + "; fullsend will not delete an account it cannot positively identify. The polling schedules remain in effect"
	case PollerPhaseQuarantined:
		return fmt.Sprintf("webhook fast-path blocked: the replacement GitLab Poller generation (user ID %d) is quarantined: %s. The polling schedules remain in effect", p.UserID, p.Reason)
	case PollerPhaseVerified:
		return fmt.Sprintf("webhook fast-path blocked: the replacement GitLab Poller generation (user ID %d) owns verified pipeline trigger token ID %d but its cutover did not complete. Revoke that trigger, remove the account, and clear %s to start a new generation. The polling schedules remain in effect", p.UserID, p.TriggerID, forge.VarGitLabPollerGenerations)
	}
	return ""
}

// beginPollerCutover makes the verified pending generation newUserID the
// current Poller and moves the previous current Poller to retiring. It
// refuses when the pending generation is not that verified account or when a
// previous retirement is unresolved, so at most one old/new pair exists.
func beginPollerCutover(st PollerGenerationState, newUserID int64) (PollerGenerationState, error) {
	p := st.Pending
	if p == nil || p.Phase != PollerPhaseVerified || p.UserID != newUserID || newUserID <= 0 {
		return st, fmt.Errorf("GitLab Poller cutover refused: user ID %d is not the verified pending generation", newUserID)
	}
	if st.RetiringUserID != 0 {
		return st, fmt.Errorf("GitLab Poller cutover refused: the superseded Poller (user ID %d) has not finished retiring", st.RetiringUserID)
	}
	return PollerGenerationState{CurrentUserID: newUserID, RetiringUserID: st.CurrentUserID}, nil
}

// finishPollerRetirement clears the retiring Poller once its schedules,
// credentials, triggers, and account were removed. retiredUserID must match
// the recorded retiring account.
func finishPollerRetirement(st PollerGenerationState, retiredUserID int64) (PollerGenerationState, error) {
	if retiredUserID <= 0 || st.RetiringUserID != retiredUserID {
		return st, fmt.Errorf("GitLab Poller retirement refused: user ID %d is not the recorded retiring Poller", retiredUserID)
	}
	st.RetiringUserID = 0
	return st, nil
}

// GitLabPollerHandoff is the optional GitLabTriggerOwner capability that
// creates replacement Poller generations. The handoff uses the owner's
// bootstrap, membership, inventory, and trigger methods for the new account.
type GitLabPollerHandoff interface {
	// CreateReplacementPoller creates a fresh fullsend-managed project
	// service account for the Poller role with no personal access token and
	// adds it to the project at Developer access. It returns the account ID
	// whenever the account was created, even when a later step failed. An
	// error wrapping forge.ErrNotFound means project service accounts are not
	// supported and no account was created.
	CreateReplacementPoller(ctx context.Context, owner, repo string) (int64, error)
	// VerifyTriggerStartsProtectedPipeline proves, after demotion, that the
	// trigger starts a pipeline on the project's protected default branch
	// under the existing branch policy. It must not change that policy.
	VerifyTriggerStartsProtectedPipeline(ctx context.Context, owner, repo string, trigger *forge.PipelineTriggerToken) error
}

// PollerHandoffResult is the outcome of handoffPollerGeneration. Trigger
// holds the secret of the verified generation's trigger in memory only.
type PollerHandoffResult struct {
	NewUserID   int64
	Trigger     *forge.PipelineTriggerToken
	Details     []string
	DeferReason string
}

// handoffPollerGeneration creates (or resumes) a replacement Poller
// generation and its trigger. The caller holds the project lease
// (LockGitLabProject) for the whole call, and publishes the runtime
// credential and trigger secret and enables the webhook only after a result
// with a Trigger is returned. The current Poller is never elevated or
// modified.
//
// Order: record the pending account before and after creating it, verify it
// holds no personal access token, create the installer-held bootstrap
// credential, record the elevation, raise to Maintainer, record the single
// trigger attempt, create the trigger, then revoke the bootstrap credential,
// inventory every personal access token, demote, and verify Developer through
// an independent read. Any failure after the trigger request publishes
// nothing and quarantines the generation.
func handoffPollerGeneration(ctx context.Context, client forge.Client, to GitLabTriggerOwner, h GitLabPollerHandoff, owner, repo string, red *credentialRedactor) (PollerHandoffResult, error) {
	st, err := loadPollerGenerations(ctx, client, owner, repo)
	if err != nil {
		return PollerHandoffResult{}, err
	}
	if reason := pollerHandoffBlocked(st); reason != "" {
		return PollerHandoffResult{DeferReason: reason}, nil
	}
	g := &pollerGenerationRun{ctx: ctx, client: client, to: to, h: h, owner: owner, repo: repo, red: red, st: st}
	if st.Pending != nil {
		switch st.Pending.Phase {
		case PollerPhaseElevated, PollerPhaseTriggerRequested:
			if res, stop, err := g.recoverInterrupted(); stop {
				return res, err
			}
		}
	}
	if g.st.Pending == nil {
		if res, stop, err := g.createAccount(); stop {
			return res, err
		}
	}
	return g.attemptTrigger()
}

type pollerGenerationRun struct {
	ctx     context.Context
	client  forge.Client
	to      GitLabTriggerOwner
	h       GitLabPollerHandoff
	owner   string
	repo    string
	red     *credentialRedactor
	st      PollerGenerationState
	details []string
}

// setPending records the pending generation durably. The write runs on a
// bounded context detached from cancellation so a canceled operation still
// records what it did.
func (g *pollerGenerationRun) setPending(p PendingPollerGeneration) error {
	ctx, cancel := gitlabCleanupContext(g.ctx)
	defer cancel()
	next := g.st
	next.Pending = &p
	if err := writePollerGenerations(ctx, g.client, g.owner, g.repo, next); err != nil {
		return err
	}
	g.st = next
	return nil
}

func (g *pollerGenerationRun) clearPending() error {
	ctx, cancel := gitlabCleanupContext(g.ctx)
	defer cancel()
	next := g.st
	next.Pending = nil
	if err := writePollerGenerations(ctx, g.client, g.owner, g.repo, next); err != nil {
		return err
	}
	g.st = next
	return nil
}

// quarantine records reason against the pending generation and returns the
// operator-facing deferral. A failed write is returned as well; the earlier
// recorded phase then quarantines the generation on the next run.
func (g *pollerGenerationRun) quarantine(reason string) (PollerHandoffResult, error) {
	p := *g.st.Pending
	p.Phase = PollerPhaseQuarantined
	p.Reason = reason
	writeErr := g.setPending(p)
	return PollerHandoffResult{
		Details:     g.details,
		DeferReason: fmt.Sprintf("webhook fast-path blocked: the replacement GitLab Poller generation (user ID %d) is quarantined: %s. The polling schedules remain in effect", p.UserID, reason),
	}, writeErr
}

// demote revokes the bootstrap credential, verifies that the account holds no
// active personal access token, returns it to Developer, and verifies the
// effective access level. Every step runs, on contexts detached from
// cancellation, and failures are joined.
func (g *pollerGenerationRun) demote(uid int64) error {
	revErr := revokePollerBootstrapDetached(g.ctx, g.to, g.owner, g.repo, uid)
	invCtx, cancel := gitlabCleanupContext(g.ctx)
	invErr := g.to.VerifyPollerCredentialsRevoked(invCtx, g.owner, g.repo, uid)
	cancel()
	if invErr != nil {
		invErr = errors.New("verifying that no personal access token remains active: " + pollerSafetyReason(invErr))
	}
	restoreErr := restorePollerDeveloper(g.ctx, g.client, g.to, g.owner, g.repo, uid)
	if restoreErr != nil {
		restoreErr = fmt.Errorf("restoring Developer access: %w", restoreErr)
	}
	return errors.Join(revErr, invErr, restoreErr)
}

func (g *pollerGenerationRun) revokeTrigger(id int64) error {
	if id == 0 {
		return nil
	}
	ctx, cancel := gitlabCleanupContext(g.ctx)
	defer cancel()
	if err := g.client.RevokePipelineTriggerToken(ctx, g.owner, g.repo, id); err != nil && !forge.IsNotFound(err) {
		return safeAPIError(fmt.Sprintf("revoking pipeline trigger token ID %d of the replacement Poller generation", id), err)
	}
	return nil
}

// recoverInterrupted returns a generation left elevated by an interrupted run
// to Developer. A generation that never sent its trigger request resumes;
// one whose request may be in flight is quarantined.
func (g *pollerGenerationRun) recoverInterrupted() (res PollerHandoffResult, stop bool, err error) {
	p := *g.st.Pending
	if demoteErr := g.demote(p.UserID); demoteErr != nil {
		res, writeErr := g.quarantine(fmt.Sprintf("an interrupted handoff left it possibly above Developer access and it could not be verified at Developer; an administrator must revoke its personal access tokens and set its project role to Developer, or remove the account, then clear %s", forge.VarGitLabPollerGenerations))
		return res, true, errors.Join(fmt.Errorf("recovering the replacement GitLab Poller generation (user ID %d): %w", p.UserID, demoteErr), writeErr)
	}
	g.details = append(g.details, fmt.Sprintf("Returned the interrupted replacement GitLab Poller generation (user ID %d) to verified Developer access", p.UserID))
	if p.TriggerAttempted || p.Phase == PollerPhaseTriggerRequested {
		res, err := g.quarantine(fmt.Sprintf("its single pipeline trigger creation request has an unknown result and is never retried; delete any %q pipeline trigger owned by user ID %d, remove the account, then clear %s", GitLabWebhookTriggerDescription, p.UserID, forge.VarGitLabPollerGenerations))
		return res, true, err
	}
	p.Phase = PollerPhaseAccountRecorded
	if err := g.setPending(p); err != nil {
		return PollerHandoffResult{Details: g.details}, true, err
	}
	return PollerHandoffResult{}, false, nil
}

// createAccount records the account request, creates the fresh account, and
// records its ID.
func (g *pollerGenerationRun) createAccount() (res PollerHandoffResult, stop bool, err error) {
	if err := g.setPending(PendingPollerGeneration{Phase: PollerPhaseAccountRequested}); err != nil {
		return PollerHandoffResult{}, true, err
	}
	uid, createErr := g.h.CreateReplacementPoller(g.ctx, g.owner, g.repo)
	if uid <= 0 {
		if forge.IsNotFound(createErr) {
			// No account was created: the capability is absent.
			clearErr := g.clearPending()
			return PollerHandoffResult{DeferReason: "webhook fast-path blocked: this GitLab instance does not support project service accounts, so no replacement Poller identity can own the pipeline trigger token. The polling schedules remain in effect"}, true, clearErr
		}
		if createErr == nil {
			createErr = errors.New("GitLab returned no account ID")
		}
		return PollerHandoffResult{}, true, fmt.Errorf("%w; the request is recorded in %s and must be reconciled manually before another Poller generation can start", safeAPIError("creating the replacement GitLab Poller service account", createErr), forge.VarGitLabPollerGenerations)
	}
	if err := g.setPending(PendingPollerGeneration{UserID: uid, Phase: PollerPhaseAccountRecorded}); err != nil {
		return PollerHandoffResult{}, true, fmt.Errorf("replacement GitLab Poller service account (user ID %d) was created but not recorded; record or remove it manually: %w", uid, err)
	}
	g.details = append(g.details, fmt.Sprintf("Created replacement GitLab Poller service account (user ID %d) at Developer access with no credentials", uid))
	if createErr != nil {
		return PollerHandoffResult{Details: g.details}, true, safeAPIError(fmt.Sprintf("preparing the replacement GitLab Poller service account (user ID %d)", uid), createErr)
	}
	return PollerHandoffResult{}, false, nil
}

// attemptTrigger elevates the recorded never-exposed account, makes the
// generation's single trigger-create request, and demotes and verifies it.
func (g *pollerGenerationRun) attemptTrigger() (PollerHandoffResult, error) {
	p := *g.st.Pending
	uid := p.UserID
	level, err := g.client.GetProjectMemberAccessLevel(g.ctx, g.owner, g.repo, uid)
	if err != nil {
		return PollerHandoffResult{Details: g.details}, safeAPIError(fmt.Sprintf("reading the replacement GitLab Poller (user ID %d) project access", uid), err)
	}
	if level != forge.GitLabAccessLevelDeveloper {
		return PollerHandoffResult{Details: g.details, DeferReason: fmt.Sprintf("webhook fast-path blocked: the replacement GitLab Poller (user ID %d) has project access level %d, not Developer, so it was not raised. The polling schedules remain in effect", uid, level)}, nil
	}
	if safeErr := g.to.VerifyPollerCredentialsRevoked(g.ctx, g.owner, g.repo, uid); safeErr != nil {
		return PollerHandoffResult{Details: g.details, DeferReason: fmt.Sprintf("webhook fast-path blocked: the replacement GitLab Poller (user ID %d) was not raised to temporary Maintainer access because %s. The polling schedules remain in effect", uid, pollerSafetyReason(safeErr))}, nil
	}

	bootstrap, bootErr := g.to.CreatePollerBootstrap(g.ctx, g.owner, g.repo, uid)
	if bootstrap != nil {
		g.red.add(bootstrap.Token)
	}
	if bootErr == nil && (bootstrap == nil || bootstrap.Token == "") {
		bootErr = errors.New("GitLab returned no token value")
	}
	if bootErr != nil {
		bootErr = safeAPIError("creating the installer-only bootstrap credential for the replacement Poller", bootErr)
		if revErr := revokePollerBootstrapDetached(g.ctx, g.to, g.owner, g.repo, uid); revErr != nil {
			res, writeErr := g.quarantine(fmt.Sprintf("its installer-only bootstrap credential (%s) could not be accounted for; revoke it, then clear %s", gitlabroles.PollerBootstrapTokenName, forge.VarGitLabPollerGenerations))
			return res, errors.Join(bootErr, revErr, writeErr)
		}
		return PollerHandoffResult{Details: g.details}, bootErr
	}

	p.Phase = PollerPhaseElevated
	if err := g.setPending(p); err != nil {
		return PollerHandoffResult{Details: g.details}, errors.Join(err, revokePollerBootstrapDetached(g.ctx, g.to, g.owner, g.repo, uid))
	}
	if err := g.to.SetProjectMemberAccessLevel(g.ctx, g.owner, g.repo, uid, forge.GitLabAccessLevelMaintainer); err != nil {
		if demoteErr := g.demote(uid); demoteErr != nil {
			res, writeErr := g.quarantine(fmt.Sprintf("a failed elevation could not be verified as reverted to Developer access; an administrator must set its project role to Developer and revoke its personal access tokens, then clear %s", forge.VarGitLabPollerGenerations))
			return res, errors.Join(fmt.Errorf("reverting the replacement GitLab Poller (user ID %d) elevation: %w", uid, demoteErr), writeErr)
		}
		p.Phase = PollerPhaseAccountRecorded
		if writeErr := g.setPending(p); writeErr != nil {
			return PollerHandoffResult{Details: g.details}, writeErr
		}
		if forge.IsForbidden(err) || forge.IsNotFound(err) {
			return PollerHandoffResult{Details: g.details, DeferReason: fmt.Sprintf("webhook fast-path blocked: the replacement GitLab Poller (user ID %d) could not be granted temporary Maintainer access to create the pipeline trigger token (%v). The polling schedules remain in effect", uid, safeAPIError("updating the Poller membership", err))}, nil
		}
		return PollerHandoffResult{Details: g.details}, safeAPIError(fmt.Sprintf("granting the replacement GitLab Poller (user ID %d) temporary Maintainer access", uid), err)
	}

	// The single create attempt is recorded before it is sent: once sent it
	// cannot be fenced, so it must never be repeated for this generation.
	p.Phase = PollerPhaseTriggerRequested
	p.TriggerAttempted = true
	if err := g.setPending(p); err != nil {
		demoteErr := g.demote(uid)
		if demoteErr == nil {
			p.Phase = PollerPhaseAccountRecorded
			p.TriggerAttempted = false
			if writeErr := g.setPending(p); writeErr != nil {
				err = errors.Join(err, writeErr)
			}
		}
		if demoteErr != nil {
			demoteErr = fmt.Errorf("returning the replacement GitLab Poller (user ID %d) to Developer access: %w", uid, demoteErr)
		}
		return PollerHandoffResult{Details: g.details}, errors.Join(err, demoteErr)
	}
	minted, mintErr := g.to.CreatePipelineTriggerTokenAsPoller(g.ctx, g.owner, g.repo, GitLabWebhookTriggerDescription, bootstrap)
	if minted != nil {
		g.red.add(minted.Token)
	}

	if demoteErr := g.demote(uid); demoteErr != nil {
		var revokeErr error
		if minted != nil {
			revokeErr = g.revokeTrigger(minted.ID)
		}
		res, writeErr := g.quarantine(fmt.Sprintf("its bootstrap credential revocation, personal access token inventory, or demotion could not be verified, so nothing was published; an administrator must revoke its personal access tokens, set its project role to Developer, delete any %q pipeline trigger it owns, and remove the account, then clear %s", GitLabWebhookTriggerDescription, forge.VarGitLabPollerGenerations))
		return res, errors.Join(fmt.Errorf("demoting the replacement GitLab Poller (user ID %d): %w", uid, demoteErr), revokeErr, writeErr)
	}
	if mintErr != nil || minted == nil || minted.Token == "" || minted.ID == 0 {
		var revokeErr error
		if minted != nil {
			revokeErr = g.revokeTrigger(minted.ID)
		}
		res, writeErr := g.quarantine(fmt.Sprintf("its single pipeline trigger creation request returned no confirmed result and is never retried; delete any %q pipeline trigger owned by user ID %d, remove the account, then clear %s", GitLabWebhookTriggerDescription, uid, forge.VarGitLabPollerGenerations))
		return res, errors.Join(revokeErr, writeErr)
	}
	if minted.OwnerID != uid {
		revokeErr := g.revokeTrigger(minted.ID)
		res, writeErr := g.quarantine(fmt.Sprintf("its pipeline trigger token ID %d reports owner user ID %d and was revoked; remove the account, then clear %s", minted.ID, minted.OwnerID, forge.VarGitLabPollerGenerations))
		return res, errors.Join(revokeErr, writeErr)
	}

	p.Phase = PollerPhaseVerified
	p.TriggerID = minted.ID
	if err := g.setPending(p); err != nil {
		return PollerHandoffResult{Details: g.details}, errors.Join(err, g.revokeTrigger(minted.ID))
	}
	g.details = append(g.details, fmt.Sprintf("Created pipeline trigger token ID %d as the replacement GitLab Poller (user ID %d) with temporary Maintainer access; revoked its bootstrap credential, verified no personal access token remains, and verified Developer access", minted.ID, uid))

	if probeErr := g.h.VerifyTriggerStartsProtectedPipeline(g.ctx, g.owner, g.repo, minted); probeErr != nil {
		revokeErr := g.revokeTrigger(minted.ID)
		res, writeErr := g.quarantine(fmt.Sprintf("its pipeline trigger could not start a pipeline on the protected default branch at Developer access (%v); fullsend does not broaden branch protection. Allow the Poller to start pipelines on that branch, remove the account, then clear %s", safeAPIError("protected-branch trigger probe", probeErr), forge.VarGitLabPollerGenerations))
		return res, errors.Join(revokeErr, writeErr)
	}
	return PollerHandoffResult{NewUserID: uid, Trigger: minted, Details: g.details}, nil
}
