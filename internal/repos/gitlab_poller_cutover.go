package repos

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// Poller generation cutover (#8243).
//
// A trigger owner that implements GitLabPollerHandoff never re-elevates the
// current Poller. Instead handoffPollerOwnedTrigger runs the generation
// handoff (handoffPollerGeneration): a fresh service account that never held
// a distributed credential mints the trigger, is demoted to Developer, and is
// verified. Only then is it cut over: its runtime credential is created and
// published, rotation state records it as the managed Poller account, and the
// generation document makes it current and records the superseded Poller as
// retiring. The superseded Poller stays at Developer and keeps its schedules
// until it is retired. Any ineligible or ambiguous state defers the fast path
// and leaves polling in effect.

// prepareHandoffPollerOwnedTrigger obtains a pipeline trigger token owned by a
// fresh, verified Poller generation. The returned pollerMint carries a cutover
// func that cuts the runtime Poller credential over to that generation; the
// caller runs it only after publishing the trigger secret and the webhook, so
// a failed publication leaves the current Poller in service and nothing
// recorded as retiring. The caller holds the project lease (lease) for the
// whole call and wires the returned trigger into the webhook. A returned
// trigger with an error was not wired in: the caller revokes it.
//
// A Poller that is absent or not a managed service account (forge.ErrNotFound
// from PollerUserID) returns an empty result, as mintPollerOwnedTrigger does,
// so the caller's owner check keeps the fast path deferred.
func prepareHandoffPollerOwnedTrigger(ctx context.Context, client forge.Client, to GitLabTriggerOwner, h GitLabPollerHandoff, lease *GitLabProjectLease, owner, repo string, wiredTriggerID int64, red *credentialRedactor) (pollerMint, error) {
	if !lease.holds(owner, repo) {
		return pollerMint{}, errPollerHandoffUnleased
	}
	uid, err := to.PollerUserID(ctx, owner, repo)
	if err != nil {
		if forge.IsNotFound(err) {
			return pollerMint{}, nil
		}
		return pollerMint{}, safeAPIError("resolving the GitLab Poller identity", err)
	}
	st, err := loadPollerGenerations(ctx, client, owner, repo)
	if err != nil {
		return pollerMint{}, err
	}
	var details []string
	if next, resumed, ok, resetErr := resetUnwiredVerifiedPollerGeneration(ctx, client, h, owner, repo, st, uid, wiredTriggerID); resetErr != nil {
		return pollerMint{}, resetErr
	} else if ok {
		st, details = next, append(details, resumed...)
	}
	if p := st.Pending; p != nil && p.Phase == PollerPhaseVerified && p.UserID == uid && st.RetiringUserID == 0 {
		// An earlier run published the verified generation's runtime
		// credential and recorded it as the managed Poller, but did not record
		// the cutover. Record it now; the superseded Poller is then retiring.
		next, cutErr := beginPollerCutover(st, uid)
		if cutErr == nil {
			cutErr = writePollerGenerationsDetached(ctx, client, owner, repo, next)
		}
		if cutErr != nil {
			return pollerMint{}, fmt.Errorf("recording the interrupted GitLab Poller cutover to user ID %d: %w", uid, cutErr)
		}
		details = append(details, fmt.Sprintf("Recorded the interrupted cutover to the GitLab Poller generation (user ID %d); the superseded Poller (user ID %d) stays at Developer access until it is retired", uid, next.RetiringUserID))
		st = next
	}
	if reason := pollerCurrentMismatch(st, uid); reason != "" {
		return pollerMint{details: details, deferReason: reason}, nil
	}
	if st.CurrentUserID == 0 {
		// Record the running Poller before a replacement exists, so a later
		// cutover records it as retiring rather than forgetting it.
		st.CurrentUserID = uid
		if err := writePollerGenerationsDetached(ctx, client, owner, repo, st); err != nil {
			return pollerMint{details: details}, err
		}
	}

	res, err := handoffPollerGeneration(ctx, client, h, lease, owner, repo, red)
	pm := pollerMint{details: append(details, res.Details...), deferReason: res.DeferReason}
	if err != nil {
		if pm.deferReason != "" {
			pm.details = append(pm.details, pm.deferReason)
			pm.deferReason = ""
		}
		return pm, err
	}
	if res.Trigger == nil || pm.deferReason != "" {
		pm.minted = nil
		return pm, nil
	}
	pm.minted = res.Trigger
	newUID := res.NewUserID
	pm.beginPublish = func(markCtx context.Context) error {
		return markPollerTriggerDistributed(markCtx, client, owner, repo, newUID)
	}
	pm.cutover = func(cutCtx context.Context) ([]string, error) {
		return cutOverPollerGeneration(cutCtx, client, to, owner, repo, uid, newUID, red)
	}
	return pm, nil
}

// handoffPollerOwnedTrigger runs prepareHandoffPollerOwnedTrigger and then the
// cutover at once, for callers that do not publish the trigger themselves.
func handoffPollerOwnedTrigger(ctx context.Context, client forge.Client, to GitLabTriggerOwner, h GitLabPollerHandoff, lease *GitLabProjectLease, owner, repo string, red *credentialRedactor) (pollerMint, error) {
	pm, err := prepareHandoffPollerOwnedTrigger(ctx, client, to, h, lease, owner, repo, 0, red)
	if err != nil || pm.cutover == nil {
		return pm, err
	}
	cutDetails, cutErr := pm.cutover(ctx)
	pm.details = append(pm.details, cutDetails...)
	return pm, cutErr
}

// resetUnwiredVerifiedPollerGeneration recovers a verified pending generation
// whose trigger is gone or was never wired in (wiredTriggerID is the trigger
// the webhook uses, if any). The generation's triggers are revoked and
// confirmed absent by listing again first. When the trigger credential was
// never distributed (the failure came before the trigger secret store was
// attempted), the account is demoted, verified, and returned to a fresh
// account_recorded generation. When it may have been distributed (a secret
// store or webhook failure, including a write with an unknown outcome), the
// account is never raised again: it is demoted, recorded as abandoned, and the
// pending slot is cleared so a genuinely fresh account replaces it. It returns
// false when the generation is not in that state and changes nothing.
func resetUnwiredVerifiedPollerGeneration(ctx context.Context, client forge.Client, h GitLabPollerHandoff, owner, repo string, st PollerGenerationState, managedUID, wiredTriggerID int64) (PollerGenerationState, []string, bool, error) {
	p := st.Pending
	if p == nil || p.Phase != PollerPhaseVerified || p.UserID == managedUID || st.RetiringUserID != 0 || p.TriggerID <= 0 {
		return st, nil, false, nil
	}
	if p.TriggerID == wiredTriggerID {
		// The webhook uses this trigger and only the cutover is missing: the
		// cutover finalization completes it (finalizePendingPollerCutover), and
		// until then the verified generation stays blocked.
		return st, nil, false, nil
	}
	g := &pollerGenerationRun{ctx: ctx, client: client, h: h, owner: owner, repo: repo, red: &credentialRedactor{}, st: st}
	// Revoke the generation's triggers and confirm by listing again that none
	// remains: a DELETE that reports success proves nothing, and the credential
	// inventory excludes managed triggers, so demotion cannot show it.
	if goneErr := g.revokeAndConfirmTriggersGone(p); goneErr != nil {
		return st, nil, false, goneErr
	}
	if p.TriggerDistributed {
		// The trigger secret may have reached protected jobs (a write with an
		// unknown outcome counts), and requests they submitted may still be
		// accepted after revocation: never raise this account again. It is
		// verified at Developer without credentials, recorded as abandoned (it
		// stays owned for uninstall), and a fresh account replaces it.
		if demoteErr := g.demote(p.UserID); demoteErr != nil {
			return st, nil, false, fmt.Errorf("abandoning the replacement GitLab Poller generation (user ID %d) whose trigger may have been distributed: %w", p.UserID, demoteErr)
		}
		next := g.st
		next.Pending = nil
		next.AbandonedUserIDs = append(slices.Clone(next.AbandonedUserIDs), p.UserID)
		if err := g.write(next); err != nil {
			return st, nil, false, err
		}
		return g.st, []string{fmt.Sprintf("Abandoned the replacement GitLab Poller generation (user ID %d): its trigger may have been distributed, so it was revoked, the account stays at Developer access without credentials, and a fresh account replaces it", p.UserID)}, true, nil
	}
	if demoteErr := g.demote(p.UserID); demoteErr != nil {
		return st, nil, false, fmt.Errorf("resuming the unwired replacement GitLab Poller generation (user ID %d): %w", p.UserID, demoteErr)
	}
	next := *p
	next.Phase = PollerPhaseAccountRecorded
	next.TriggerAttempted = false
	next.TriggerID = 0
	if err := g.setPending(next); err != nil {
		return st, nil, false, err
	}
	return g.st, []string{fmt.Sprintf("Resumed the replacement GitLab Poller generation (user ID %d): its earlier trigger was never wired in and no longer exists", p.UserID)}, true, nil
}

// revokeAndConfirmTriggersGone revokes every pipeline trigger token owned by
// the pending generation's account (and its recorded trigger) and confirms by
// listing again that none remains. An unreadable inventory or a trigger that
// is still listed after revocation fails closed.
func (g *pollerGenerationRun) revokeAndConfirmTriggersGone(p *PendingPollerGeneration) error {
	owned := func(t forge.PipelineTriggerToken) bool {
		return t.ID == p.TriggerID || (p.UserID > 0 && t.OwnerID == p.UserID)
	}
	triggers, err := g.client.ListPipelineTriggerTokens(g.ctx, g.owner, g.repo)
	if err != nil {
		return safeAPIError("listing pipeline trigger tokens before resuming the unwired GitLab Poller generation", err)
	}
	var revoked bool
	for _, t := range triggers {
		if !owned(t) {
			continue
		}
		if revokeErr := g.revokeTrigger(t.ID); revokeErr != nil {
			return revokeErr
		}
		revoked = true
	}
	if !revoked {
		return nil
	}
	ctx, cancel := gitlabCleanupContext(g.ctx)
	defer cancel()
	after, err := g.client.ListPipelineTriggerTokens(ctx, g.owner, g.repo)
	if err != nil {
		return safeAPIError("confirming the pipeline trigger tokens of the unwired GitLab Poller generation are revoked", err)
	}
	for _, t := range after {
		if owned(t) {
			return fmt.Errorf("pipeline trigger token ID %d of the replacement GitLab Poller generation (user ID %d) is still listed after revocation, so the generation was neither reset nor re-elevated; revoke it manually and rerun install", t.ID, p.UserID)
		}
	}
	return nil
}

// markPollerTriggerDistributed durably records, before the trigger secret is
// stored, that the verified pending generation newUID's trigger credential may
// be distributed. The marker is never cleared.
func markPollerTriggerDistributed(ctx context.Context, client forge.Client, owner, repo string, newUID int64) error {
	st, err := loadPollerGenerations(ctx, client, owner, repo)
	if err != nil {
		return err
	}
	p := st.Pending
	if p == nil || p.Phase != PollerPhaseVerified || p.UserID != newUID {
		return fmt.Errorf("recording the trigger publication of GitLab Poller user ID %d refused: it is not the verified pending generation", newUID)
	}
	if p.TriggerDistributed {
		return nil
	}
	next := *p
	next.TriggerDistributed = true
	st.Pending = &next
	if err := writePollerGenerationsDetached(ctx, client, owner, repo, st); err != nil {
		return fmt.Errorf("recording the trigger publication of GitLab Poller user ID %d: %w", newUID, err)
	}
	return nil
}

// finalizePendingPollerCutover finishes an interrupted final cutover on an
// ordinary lease-protected install, with no minting, elevation, or new
// generation. It handles a verified pending generation that either already
// owns the published runtime credential (only the generation document is
// missing the cutover) or owns the trigger the webhook uses while the cutover
// itself was contained before the credential was stored (the cutover is
// retried). activeTriggerID and triggers describe the corroborated webhook
// trigger. It returns nothing to do for every other state.
func finalizePendingPollerCutover(ctx context.Context, client forge.Client, to GitLabTriggerOwner, lease *GitLabProjectLease, owner, repo string, activeTriggerID int64, triggers []forge.PipelineTriggerToken, red *credentialRedactor) ([]string, error) {
	if !lease.holds(owner, repo) {
		return nil, nil
	}
	if _, ok := to.(GitLabPollerHandoff); !ok {
		return nil, nil
	}
	uid, err := to.PollerUserID(ctx, owner, repo)
	if err != nil {
		if forge.IsNotFound(err) {
			return nil, nil
		}
		return nil, safeAPIError("resolving the GitLab Poller identity", err)
	}
	st, err := loadPollerGenerations(ctx, client, owner, repo)
	if err != nil {
		return nil, err
	}
	p := st.Pending
	if p == nil || p.Phase != PollerPhaseVerified || st.RetiringUserID != 0 || st.CurrentUserID == 0 {
		return nil, nil
	}
	switch {
	case p.UserID == uid:
		next, cutErr := beginPollerCutover(st, uid)
		if cutErr == nil {
			cutErr = writePollerGenerationsDetached(ctx, client, owner, repo, next)
		}
		if cutErr != nil {
			return nil, fmt.Errorf("recording the interrupted GitLab Poller cutover to user ID %d: %w", uid, cutErr)
		}
		return []string{fmt.Sprintf("Recorded the interrupted cutover to the GitLab Poller generation (user ID %d); the superseded Poller (user ID %d) stays at Developer access until it is retired", uid, next.RetiringUserID)}, nil
	case activeTriggerID != 0 && activeTriggerID == p.TriggerID && st.CurrentUserID == uid:
		for _, t := range triggers {
			if t.ID == activeTriggerID && t.OwnerID == p.UserID {
				return cutOverPollerGeneration(ctx, client, to, owner, repo, uid, p.UserID, red)
			}
		}
	}
	return nil, nil
}

// unfinishedPollerCutoverDetail reports a verified pending generation whose
// cutover is not recorded, so a probe that finds the webhook provisioned does
// not report the fast path complete while the handoff is unfinished (for
// example when the runtime-secret write failed without committing, leaving the
// Poller identity unresolvable). It returns "" when no cutover is pending or
// the trigger owner does not support generation handoff.
func unfinishedPollerCutoverDetail(ctx context.Context, client forge.Client, to GitLabTriggerOwner, owner, repo string) (string, error) {
	if _, ok := to.(GitLabPollerHandoff); !ok {
		return "", nil
	}
	st, err := loadPollerGenerations(ctx, client, owner, repo)
	if err != nil {
		return "", err
	}
	p := st.Pending
	if p == nil || p.Phase != PollerPhaseVerified || st.RetiringUserID != 0 || st.CurrentUserID == 0 {
		return "", nil
	}
	return fmt.Sprintf("GitLab Poller cutover to the verified replacement (user ID %d) is not yet recorded; the next provisioning run finishes it", p.UserID), nil
}

// reconcilePendingPollerGeneration returns a replacement Poller generation
// left above Developer by an interrupted handoff to verified Developer access
// and revokes its bootstrap credential. The replacement exists only in the
// generation document until cutover, so reconcilePollerElevation cannot see it
// through the managed Poller accounts; this runs from it, ahead of every
// safety and readiness early return, so none of them can leave the account
// elevated or its bootstrap credential live, and a Maintainer-owned trigger is
// never revoked before its owner is demoted. A quarantined generation is
// demoted and its credentials revoked as well, and stays quarantined: when
// that cannot be verified the error is returned and the quarantine obligations
// are retained. A recorded or verified generation has its membership
// reconciled and leftover bootstrap credentials revoked, and its credentials
// are contained when Developer access cannot be verified; its published
// runtime credential is otherwise kept. A trigger owner without handoff support, or a project without
// a pending generation, changes nothing.
func reconcilePendingPollerGeneration(ctx context.Context, client forge.Client, to GitLabTriggerOwner, owner, repo string) (GitLabWebhookResult, error) {
	h, ok := to.(GitLabPollerHandoff)
	if !ok {
		return GitLabWebhookResult{}, nil
	}
	st, err := loadPollerGenerations(ctx, client, owner, repo)
	if err != nil {
		return GitLabWebhookResult{}, err
	}
	p := st.Pending
	if p == nil || p.UserID <= 0 {
		return GitLabWebhookResult{}, nil
	}
	g := &pollerGenerationRun{ctx: ctx, client: client, h: h, owner: owner, repo: repo, red: &credentialRedactor{}, st: st}
	switch p.Phase {
	case PollerPhaseElevated, PollerPhaseTriggerRequested:
		// A recovered generation resumes, and a quarantined one defers the
		// handoff, on the next attempt through its recorded phase.
		_, _, err := g.recoverInterrupted()
		return GitLabWebhookResult{Action: "update", Details: g.details}, err
	case PollerPhaseQuarantined:
		if demoteErr := g.demote(p.UserID); demoteErr != nil {
			return GitLabWebhookResult{Action: "deferred"}, fmt.Errorf("the quarantined replacement GitLab Poller generation (user ID %d) could not be verified at Developer access without credentials; it stays quarantined: %w", p.UserID, demoteErr)
		}
	case PollerPhaseAccountRecorded, PollerPhaseVerified:
		// These phases are not unconditionally demoted: a verified generation
		// may own a legitimately published runtime credential, so only its
		// membership and leftover bootstrap credentials are reconciled. A
		// membership that cannot be returned to Developer fails closed by
		// containing the account's credentials, and the pending document is
		// kept as the recovery state. The managed Poller reconciliation has
		// already handled this account when it is the selected Poller.
		if uid, idErr := to.PollerUserID(ctx, owner, repo); idErr == nil && uid == p.UserID {
			return GitLabWebhookResult{}, nil
		}
		return reconcileOnePoller(ctx, client, to, owner, repo, p.UserID)
	}
	return GitLabWebhookResult{}, nil
}

// writePollerGenerationsDetached writes the generation document on a bounded
// context detached from cancellation, so a canceled run still records what it
// did.
func writePollerGenerationsDetached(ctx context.Context, client forge.Client, owner, repo string, st PollerGenerationState) error {
	writeCtx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	return writePollerGenerations(writeCtx, client, owner, repo, st)
}

// pollerCurrentMismatch returns why the generation document does not agree
// with the managed Poller uid, or "" when it does (or records no current
// Poller yet and uid is not otherwise recorded). A blocked document reports
// its own reason first, so an interrupted cutover names its recovery.
func pollerCurrentMismatch(st PollerGenerationState, uid int64) string {
	if st.CurrentUserID == uid {
		return ""
	}
	if reason := pollerHandoffBlocked(st); reason != "" {
		return reason
	}
	if st.CurrentUserID == 0 && uid != st.RetiringUserID && (st.Pending == nil || st.Pending.UserID != uid) {
		return ""
	}
	return fmt.Sprintf("webhook fast-path blocked: %s records GitLab Poller generation user ID %d as current, but the managed Poller is user ID %d, so no replacement Poller generation is started. Reconcile the Poller accounts, then correct current_user_id. The polling schedules remain in effect", forge.VarGitLabPollerGenerations, st.CurrentUserID, uid)
}

// cutOverPollerGeneration makes the verified generation newUID the runtime
// Poller. Its runtime credential is created first; rotation state then records
// newUID as the managed Poller account and the credential as its incoming
// token; the credential is stored; and finally the generation document makes
// newUID current and records previousUID as retiring. A failure before the
// credential is stored contains newUID's credentials, leaving the previous
// Poller in service. An error from the store itself is an unknown outcome: the
// replacement credential and its rotation tracking are kept, marked
// distributing, and reconciled on the next run. Every step runs on a bounded
// context detached from cancellation.
func cutOverPollerGeneration(ctx context.Context, client forge.Client, to GitLabTriggerOwner, owner, repo string, previousUID, newUID int64, red *credentialRedactor) ([]string, error) {
	ctx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	contain := func(cause error) error {
		containCtx, cancelContain := gitlabCleanupContext(ctx)
		defer cancelContain()
		if err := to.ContainPoller(containCtx, owner, repo, newUID); err != nil {
			return errors.Join(cause, safeAPIError(fmt.Sprintf("revoking the credentials of the replacement GitLab Poller (user ID %d) after its cutover failed", newUID), err))
		}
		return cause
	}

	now := time.Now()
	expiresAt := GitLabPATExpiresAt(now)
	tok, err := to.CreatePollerRuntimeToken(ctx, owner, repo, newUID, expiresAt)
	if tok != nil {
		red.add(tok.Token)
	}
	switch {
	case err != nil:
		return nil, contain(safeAPIError(fmt.Sprintf("creating the runtime credential of the replacement GitLab Poller (user ID %d)", newUID), err))
	case tok == nil || tok.Token == "" || tok.ID <= 0:
		return nil, contain(fmt.Errorf("creating the runtime credential of the replacement GitLab Poller (user ID %d): GitLab returned no token value or ID", newUID))
	case !canMaskGitLabValue(tok.Token):
		return nil, contain(fmt.Errorf("the runtime credential of the replacement GitLab Poller (user ID %d) cannot be masked and was not stored", newUID))
	}

	previous, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return nil, contain(safeAPIError("reading rotation state before the GitLab Poller cutover", err))
	}
	next := previous
	next.Roles = make(map[string]rotationRoleState, len(previous.Roles)+1)
	for role, rs := range previous.Roles {
		next.Roles[role] = rs
	}
	old := previous.Roles[string(gitlabroles.RolePoller)]
	if old.ManagedUserID > 0 && int64(old.ManagedUserID) != previousUID {
		return nil, contain(fmt.Errorf("GitLab Poller cutover refused: rotation state records managed Poller user ID %d, not the current Poller user ID %d", old.ManagedUserID, previousUID))
	}
	// The superseded account's credentials are retired by user ID through the
	// generation document, not by rotation cleanup, which only revokes tokens
	// of the recorded managed account. Outstanding obligations that are not
	// the superseded account's runtime credential are kept, and so is the
	// provenance of every token fullsend created (a migrated legacy token
	// still owed revocation stays revocable).
	var outgoing []int
	for _, id := range old.OutgoingIDs {
		if id != tok.ID && !slices.Contains(outgoing, id) {
			outgoing = append(outgoing, id)
		}
	}
	// The replacement is recorded as distributing before its publication is
	// attempted and as distributed only after the secret write succeeds, so an
	// interrupted run or a failed marker write never leaves a durable record
	// that claims a credential that was not installed.
	next.Roles[string(gitlabroles.RolePoller)] = rotationRoleState{
		CreatedTokenIDs: uniqueInts(append(append([]int(nil), old.CreatedTokenIDs...), tok.ID)),
		ManagedUserID:   int(newUID),
		Phase:           rotationPhaseDistributing,
		IncomingID:      tok.ID,
		OutgoingIDs:     outgoing,
		ExpiresAt:       expiresAt,
		ExcludedUserIDs: append([]int(nil), old.ExcludedUserIDs...),
	}
	if err := writeRotationState(ctx, client, owner, repo, next); err != nil {
		return nil, contain(safeAPIError(fmt.Sprintf("recording the replacement GitLab Poller (user ID %d) in rotation state", newUID), err))
	}
	if storeErr := client.CreateRepoSecret(ctx, owner, repo, forge.SecretGitLabPollerToken, tok.Token); storeErr != nil {
		// A write error does not prove the previous credential is still
		// installed: GitLab may have committed the variable and lost the
		// response, and the installed value cannot be read back. Revoking the
		// replacement could leave the variable holding a revoked credential
		// and stop polling, so the replacement and its rotation tracking are
		// kept, as rotateOneRole keeps an ambiguous publication. The role is
		// already durably distributing with the replacement as its incoming
		// token, so the next run's rotation recovery keeps it as an outgoing
		// token and distributes a replacement, and the cutover itself is
		// recorded on the next run through the verified pending generation.
		// Recording the error text is best effort: the phase is already
		// persisted.
		role := next.Roles[string(gitlabroles.RolePoller)]
		role.Error = "storing replacement credential failed; publication outcome unknown"
		next.Roles[string(gitlabroles.RolePoller)] = role
		markCtx, cancelMark := gitlabCleanupContext(ctx)
		_ = writeRotationState(markCtx, client, owner, repo, next)
		cancelMark()
		return nil, safeAPIError(fmt.Sprintf("storing %s for the replacement GitLab Poller (user ID %d); its publication outcome is unknown, so the replacement credential was kept for recovery and not revoked", forge.SecretGitLabPollerToken, newUID), storeErr)
	}
	details := []string{fmt.Sprintf("Published the runtime credential (%s) of the replacement GitLab Poller (user ID %d) after it was verified at Developer access", forge.SecretGitLabPollerToken, newUID)}

	// The credential is installed: record the distribution. A failed write
	// leaves the role distributing, which the next run's rotation recovery
	// resolves by keeping this credential as outgoing and distributing a
	// replacement, so it does not undo the cutover.
	phase := rotationPhaseIdle
	if len(outgoing) > 0 {
		phase = rotationPhaseOverlapping
	}
	role := next.Roles[string(gitlabroles.RolePoller)]
	role.Phase = phase
	role.DistributedAt = now.UTC().Format(time.RFC3339)
	role.Error = ""
	next.Roles[string(gitlabroles.RolePoller)] = role
	if err := writeRotationState(ctx, client, owner, repo, next); err != nil {
		details = append(details, fmt.Sprintf("Could not record the distribution of the replacement GitLab Poller credential (user ID %d) in rotation state (%v); it stays marked distributing and the next run reconciles it", newUID, safeAPIError("writing rotation state", err)))
	}

	// The credential is live; the generation document now records the
	// cutover. A failed write leaves the verified pending generation, which
	// blocks further generations and names its recovery.
	st, err := loadPollerGenerations(ctx, client, owner, repo)
	if err == nil {
		st, err = beginPollerCutover(st, newUID)
	}
	if err == nil {
		err = writePollerGenerations(ctx, client, owner, repo, st)
	}
	if err != nil {
		return details, fmt.Errorf("recording the GitLab Poller cutover to user ID %d in %s: %w", newUID, forge.VarGitLabPollerGenerations, err)
	}
	details = append(details, fmt.Sprintf("Cut the GitLab Poller over to user ID %d; the superseded Poller (user ID %d) stays at Developer access until it is retired", newUID, previousUID))
	return details, nil
}
