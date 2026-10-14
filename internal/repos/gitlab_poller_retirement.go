package repos

import (
	"context"
	"fmt"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// Poller generation retirement (#8244).
//
// A cutover leaves the superseded Poller recorded as retiring_user_id, at
// Developer access and still holding its previous runtime credential, so jobs
// that already read that credential keep working. An ordinary install that
// holds the project lease retires it once the cutover is confirmed (the
// managed Poller is the recorded current generation), the rotation grace
// (gitlabroles.DefaultRotationGrace, the window rotation keeps a previous
// credential usable for in-flight jobs) has elapsed since the replacement
// credential was distributed, and no unfinished job runs as it. Retirement
// then revokes the pipeline triggers it owns, removes its per-user merge grant
// on the protected default branch, revokes its credentials, verifies and
// deletes the account, and only then clears retiring_user_id. Every step is
// idempotent. A step that cannot be completed or verified keeps
// retiring_user_id, so the next install retries, and no further Poller
// generation starts in the meantime.
//
// Free-tier quota: ADR 0125 sets no numeric GitLab Free-tier bound, so the
// generation document's own bound is the quota contract. At most one old/new
// pair of live Poller accounts exists (the current Poller plus either one
// pending replacement or one superseded Poller that is still retiring), plus at
// most maxAbandonedPollerGenerations abandoned replacements awaiting deletion.
// Retirement returns the pair to a single Poller, so repeated installs and
// rotations converge instead of accumulating accounts.

// GitLabPollerRetirement is the capability that retires a superseded Poller
// generation account, identified by its recorded user ID, with the installer's
// own authority.
type GitLabPollerRetirement interface {
	// PollerRetirementBlocked returns an operator-safe reason when userID must
	// not be retired yet, or "" when retirement may proceed. Retirement waits
	// while unfinished jobs run as the account (or as a user GitLab did not
	// report), and never deletes an account that owns an
	// administrator-supplied credential. An inventory that cannot be read is
	// an error.
	PollerRetirementBlocked(ctx context.Context, owner, repo string, userID int64) (string, error)
	// RetirePollerAccount revokes the account's managed personal access
	// tokens, verifies through independent inventories that it holds no
	// active personal access token, usable SSH key, unfinished job, pipeline
	// schedule, or pipeline trigger, and deletes the project service account.
	// An account that no longer exists is already retired and succeeds.
	RetirePollerAccount(ctx context.Context, owner, repo string, userID int64) error
}

// pollerRetirementResult is the outcome of retireSupersededPollerGeneration.
// details describes changes that were made; waiting explains why retirement
// was postponed without changing anything. revokedTriggerIDs lists the
// pipeline triggers it revoked, so the caller's cached inventory stays current.
type pollerRetirementResult struct {
	details           []string
	waiting           string
	revokedTriggerIDs []int64
}

// retireSupersededPollerGeneration retires the recorded retiring Poller
// generation (see the overview at the top of this file). activeTriggerID is the trigger
// the webhook uses, which is never revoked. It does nothing without the
// project lease, without a trigger owner that supports retirement, or when no
// Poller is retiring.
func retireSupersededPollerGeneration(ctx context.Context, client forge.Client, to GitLabTriggerOwner, lease *GitLabProjectLease, owner, repo string, activeTriggerID int64, now time.Time) (pollerRetirementResult, error) {
	if !lease.holds(owner, repo) {
		return pollerRetirementResult{}, nil
	}
	r, ok := to.(GitLabPollerRetirement)
	if !ok {
		return pollerRetirementResult{}, nil
	}
	st, err := loadPollerGenerations(ctx, client, owner, repo)
	if err != nil {
		return pollerRetirementResult{}, err
	}
	retiring := st.RetiringUserID
	if retiring <= 0 {
		return pollerRetirementResult{}, nil
	}
	wait := func(reason string) (pollerRetirementResult, error) {
		return pollerRetirementResult{waiting: fmt.Sprintf("Retirement of the superseded GitLab Poller generation (user ID %d) is waiting: %s. Polling and dispatch continue with the current Poller", retiring, reason)}, nil
	}
	uid, err := to.PollerUserID(ctx, owner, repo)
	if err != nil {
		if forge.IsNotFound(err) {
			return wait("the managed Poller could not be identified, so the cutover from it is not confirmed")
		}
		return pollerRetirementResult{}, safeAPIError("resolving the GitLab Poller identity before retiring the superseded generation", err)
	}
	if st.CurrentUserID <= 0 || uid != st.CurrentUserID {
		return wait(fmt.Sprintf("the managed Poller (user ID %d) is not the recorded current generation (user ID %d), so the cutover is not confirmed", uid, st.CurrentUserID))
	}
	if reason, err := pollerRetirementGraceWait(ctx, client, owner, repo, uid, now); err != nil || reason != "" {
		if err != nil {
			return pollerRetirementResult{}, err
		}
		return wait(reason)
	}
	blocked, err := r.PollerRetirementBlocked(ctx, owner, repo, retiring)
	if err != nil {
		return pollerRetirementResult{}, fmt.Errorf("checking whether the superseded GitLab Poller (user ID %d) can be retired: %w", retiring, err)
	}
	if blocked != "" {
		return wait(blocked)
	}

	var res pollerRetirementResult
	revoked, err := revokeRetiringPollerTriggers(ctx, client, owner, repo, retiring, activeTriggerID)
	if err != nil {
		return pollerRetirementResult{}, err
	}
	if len(revoked) > 0 {
		res.revokedTriggerIDs = revoked
		res.details = append(res.details, fmt.Sprintf("Revoked %d pipeline trigger token(s) owned by the superseded GitLab Poller (user ID %d)", len(revoked), retiring))
	}
	removed, err := removeRetiredPollerMergeGrants(ctx, client, owner, repo, retiring)
	if err != nil {
		return res, err
	}
	if removed > 0 {
		res.details = append(res.details, fmt.Sprintf("Removed the protected default-branch merge grant of the superseded GitLab Poller (user ID %d); other merge grants and branch-wide access are unchanged", retiring))
	}
	if err := r.RetirePollerAccount(ctx, owner, repo, retiring); err != nil {
		return res, fmt.Errorf("retiring the superseded GitLab Poller service account (user ID %d); it stays recorded as retiring for the next install: %w", retiring, err)
	}

	// The account is gone: clear retiring_user_id against a fresh read, so a
	// concurrent change to the document is not overwritten. Another run that
	// already cleared it leaves nothing to do.
	cur, err := loadPollerGenerations(ctx, client, owner, repo)
	if err != nil {
		return res, err
	}
	if cur.RetiringUserID == retiring {
		next, finishErr := finishPollerRetirement(cur, retiring)
		if finishErr == nil {
			finishErr = writePollerGenerationsDetached(ctx, client, owner, repo, next)
		}
		if finishErr != nil {
			return res, fmt.Errorf("recording the retirement of the superseded GitLab Poller (user ID %d) in %s: %w", retiring, forge.VarGitLabPollerGenerations, finishErr)
		}
	}
	res.details = append(res.details, fmt.Sprintf("Retired the superseded GitLab Poller generation (user ID %d): its credentials were revoked and its service account was deleted", retiring))
	return res, nil
}

// pollerRetirementGraceWait returns why the superseded Poller's previous
// runtime credential must stay usable a while longer, or "" when the rotation
// grace has elapsed since the current Poller's credential was distributed. A
// distribution that is not durably recorded keeps the grace in force.
func pollerRetirementGraceWait(ctx context.Context, client forge.Client, owner, repo string, currentUID int64, now time.Time) (string, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return "", safeAPIError("reading rotation state before retiring the superseded GitLab Poller", err)
	}
	rs := state.Roles[string(gitlabroles.RolePoller)]
	if int64(rs.ManagedUserID) != currentUID {
		return fmt.Sprintf("rotation state records managed Poller user ID %d, not the current generation (user ID %d)", rs.ManagedUserID, currentUID), nil
	}
	dist, parseErr := time.Parse(time.RFC3339, rs.DistributedAt)
	if rs.Phase == rotationPhaseDistributing || rs.DistributedAt == "" || parseErr != nil {
		return "the distribution of the current Poller credential is not yet recorded", nil
	}
	if until := dist.Add(gitlabroles.DefaultRotationGrace); now.Before(until) {
		return fmt.Sprintf("its previous runtime credential stays usable for in-flight jobs until %s, the end of the %s rotation grace after the replacement credential was distributed", until.UTC().Format(time.RFC3339), gitlabroles.DefaultRotationGrace), nil
	}
	return "", nil
}

// revokeRetiringPollerTriggers revokes every pipeline trigger token owned by
// the retiring account and confirms by listing again that none remains. It
// refuses, changing nothing, when the retiring account owns the trigger the
// webhook uses.
func revokeRetiringPollerTriggers(ctx context.Context, client forge.Client, owner, repo string, retiring, activeTriggerID int64) ([]int64, error) {
	triggers, err := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return nil, safeAPIError("listing pipeline trigger tokens before retiring the superseded GitLab Poller", err)
	}
	var owned []int64
	for _, t := range triggers {
		if t.OwnerID != retiring {
			continue
		}
		if activeTriggerID != 0 && t.ID == activeTriggerID {
			return nil, fmt.Errorf("the superseded GitLab Poller (user ID %d) owns pipeline trigger token ID %d, which the webhook uses, so it was not retired; reconcile the webhook trigger, then rerun install", retiring, t.ID)
		}
		owned = append(owned, t.ID)
	}
	if len(owned) == 0 {
		return nil, nil
	}
	for _, id := range owned {
		if err := client.RevokePipelineTriggerToken(ctx, owner, repo, id); err != nil && !forge.IsNotFound(err) {
			return nil, safeAPIError(fmt.Sprintf("revoking pipeline trigger token ID %d of the superseded GitLab Poller", id), err)
		}
	}
	after, err := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return nil, safeAPIError("confirming the pipeline trigger tokens of the superseded GitLab Poller are revoked", err)
	}
	for _, t := range after {
		if t.OwnerID == retiring {
			return nil, fmt.Errorf("pipeline trigger token ID %d of the superseded GitLab Poller (user ID %d) is still listed after revocation; revoke it manually and rerun install", t.ID, retiring)
		}
	}
	return owned, nil
}

// removeRetiredPollerMergeGrants removes every merge-access entry that grants
// the retiring account individually on the exact-name protection rule of the
// default branch, and rereads the rule to verify none remains. This is where
// EnsureGitLabPollerPipelineAccess adds a Poller's grant
// (GrantProtectedBranchMergeUser refuses wildcard rules). Entries are removed
// one at a time by entry ID, so role-based and group entries, other users'
// grants, and wildcard rules are never changed. A forge without
// protected-branch support has nothing to remove.
func removeRetiredPollerMergeGrants(ctx context.Context, client forge.Client, owner, repo string, retiring int64) (int, error) {
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return 0, safeAPIError("reading project before removing the superseded GitLab Poller's merge grant", err)
	}
	branch := project.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	entries, err := retiredPollerMergeEntries(ctx, client, owner, repo, branch, retiring)
	if err != nil {
		if forge.IsNotSupported(err) {
			return 0, nil
		}
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}
	for _, l := range entries {
		if l.ID <= 0 {
			return 0, fmt.Errorf("protected branch %q lists a merge grant for the superseded GitLab Poller (user ID %d) without an entry ID, so it cannot be removed safely; remove it in Settings → Repository → Protected branches, then rerun install", branch, retiring)
		}
	}
	for _, l := range entries {
		if err := client.RevokeProtectedBranchMergeUserEntry(ctx, owner, repo, branch, l.ID, int(retiring)); err != nil {
			return 0, safeAPIError(fmt.Sprintf("removing the merge grant (entry %d) of the superseded GitLab Poller on protected branch %q", l.ID, branch), err)
		}
	}
	remaining, err := retiredPollerMergeEntries(ctx, client, owner, repo, branch, retiring)
	if err != nil {
		return 0, err
	}
	if len(remaining) > 0 {
		return 0, fmt.Errorf("protected branch %q still lists a merge grant for the superseded GitLab Poller (user ID %d) after removal; remove it in Settings → Repository → Protected branches, then rerun install", branch, retiring)
	}
	return len(entries), nil
}

// retiredPollerMergeEntries returns the merge-access entries of the
// exact-name protection rule for branch that grant userID individually.
func retiredPollerMergeEntries(ctx context.Context, client forge.Client, owner, repo, branch string, userID int64) ([]forge.ProtectedBranchAccess, error) {
	rules, err := client.ListProtectedBranches(ctx, owner, repo)
	if err != nil {
		if forge.IsNotSupported(err) {
			return nil, err
		}
		return nil, safeAPIError("listing protected branches to find the superseded GitLab Poller's merge grant", err)
	}
	var out []forge.ProtectedBranchAccess
	for _, rule := range rules {
		if rule.Name != branch {
			continue
		}
		for _, l := range rule.MergeAccessLevels {
			if int64(l.UserID) == userID {
				out = append(out, l)
			}
		}
	}
	return out, nil
}
