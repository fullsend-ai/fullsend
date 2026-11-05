package repos

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
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
// then revokes the pipeline triggers it owns, revokes its credentials,
// verifies and deletes the account, and only then clears retiring_user_id. It
// leaves any per-user merge grant on the protected default branch in place
// (see preservedPollerMergeGrants). Every step is idempotent. A step that cannot be completed or verified keeps
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
//
// Synchronization: implementations are not safe for concurrent use and hold no
// lock of their own. Callers must hold the GitLabProjectLease for owner/repo
// (retireSupersededPollerGeneration is the only production caller and does
// nothing without it) and call one method at a time. PollerRetirementBlocked
// only reads, but its answer is meaningful only while that lease is held, and
// RetirePollerAccount must never run without it.
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

// webhookTriggerEvidence re-reads the managed webhooks, the pipeline trigger
// inventory and the trigger-account ledger and returns the trigger IDs that the
// webhooks use or may use, which retirement must never revoke. unresolved is
// non-empty when a managed webhook exists whose trigger cannot be established
// from that evidence.
type webhookTriggerEvidence func(ctx context.Context) (protectedTriggerIDs []int64, unresolved string, err error)

// retireSupersededPollerGeneration retires the recorded retiring Poller
// generation (see the overview at the top of this file). evidence is called
// immediately before any trigger is revoked, after the slower inventory checks,
// so the webhook trigger it protects is current rather than the one an earlier
// probe saw; retirement waits when it cannot establish that trigger. It does
// nothing without the project lease, without a trigger owner that supports
// retirement, or when no Poller is retiring.
func retireSupersededPollerGeneration(ctx context.Context, client forge.Client, to GitLabTriggerOwner, lease *GitLabProjectLease, owner, repo string, evidence webhookTriggerEvidence, now time.Time) (pollerRetirementResult, error) {
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
	protected, unresolved, err := evidence(ctx)
	if err != nil {
		return pollerRetirementResult{}, err
	}
	if unresolved != "" {
		return wait(unresolved + ", so none of the superseded Poller's triggers can be revoked safely; install reconciles the webhook trigger first")
	}

	var res pollerRetirementResult
	revoked, blockedTriggers, err := revokeRetiringPollerTriggers(ctx, client, owner, repo, retiring, protected)
	if err != nil {
		return pollerRetirementResult{}, err
	}
	if blockedTriggers != "" {
		return wait(blockedTriggers)
	}
	if len(revoked) > 0 {
		res.revokedTriggerIDs = revoked
		res.details = append(res.details, fmt.Sprintf("Revoked %d pipeline trigger token(s) owned by the superseded GitLab Poller (user ID %d)", len(revoked), retiring))
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
	// The account is already gone and recorded as retired, so a failure to read
	// the protected branches only changes the note, never the outcome.
	kept, grantErr := preservedPollerMergeGrants(ctx, client, owner, repo, retiring)
	switch {
	case grantErr != nil:
		res.details = append(res.details, fmt.Sprintf("Could not check whether the protected default branch still lists a merge grant for the retired GitLab Poller (user ID %d); inspect Settings → Repository → Protected branches and remove any grant that is no longer needed", retiring))
	case kept > 0:
		res.details = append(res.details, fmt.Sprintf("Left the protected default-branch merge grant of the retired GitLab Poller (user ID %d) in place: fullsend does not record who created a per-user merge grant, so it may be an administrator's. Remove it in Settings → Repository → Protected branches if it is no longer needed", retiring))
	}
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
// the retiring account and confirms by listing again that none remains.
// protected holds the trigger IDs the webhooks use or may use. A trigger whose
// owner GitLab omits is attributed only by evidence: the managed webhook
// trigger description marks it as a fullsend webhook trigger, which install
// already treats as superseded once no webhook uses it. Any other ownerless
// trigger cannot be attributed, and account deletion would fail closed on it
// every install, so blocked names it and the operator action instead. When a
// protected trigger belongs to the retiring account (or has no reported owner),
// or an ownerless trigger cannot be attributed, nothing is changed and blocked
// explains why.
func revokeRetiringPollerTriggers(ctx context.Context, client forge.Client, owner, repo string, retiring int64, protected []int64) (revoked []int64, blocked string, err error) {
	triggers, err := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return nil, "", safeAPIError("listing pipeline trigger tokens before retiring the superseded GitLab Poller", err)
	}
	var owned, inUse, unattributed []int64
	for _, t := range triggers {
		if t.OwnerID != retiring && t.OwnerID != 0 {
			continue
		}
		switch {
		case slices.Contains(protected, t.ID):
			inUse = append(inUse, t.ID)
		case t.OwnerID == retiring || t.Description == GitLabWebhookTriggerDescription:
			owned = append(owned, t.ID)
		default:
			unattributed = append(unattributed, t.ID)
		}
	}
	if len(inUse) > 0 {
		return nil, fmt.Sprintf("the webhook uses pipeline trigger token ID(s) %s that belong to, or cannot be attributed away from, the superseded GitLab Poller (user ID %d); it was not retired. Install reconciles the webhook trigger and retries", joinInt64s(inUse), retiring), nil
	}
	if len(unattributed) > 0 {
		return nil, fmt.Sprintf("GitLab does not report the owner of pipeline trigger token ID(s) %s, so fullsend cannot show they are not the superseded GitLab Poller's (user ID %d). If they are not in use, revoke them in Settings → CI/CD → Pipeline trigger tokens, then rerun install", joinInt64s(unattributed), retiring), nil
	}
	if len(owned) == 0 {
		return nil, "", nil
	}
	for _, id := range owned {
		if err := client.RevokePipelineTriggerToken(ctx, owner, repo, id); err != nil && !forge.IsNotFound(err) {
			return nil, "", safeAPIError(fmt.Sprintf("revoking pipeline trigger token ID %d of the superseded GitLab Poller", id), err)
		}
	}
	after, err := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return nil, "", safeAPIError("confirming the pipeline trigger tokens of the superseded GitLab Poller are revoked", err)
	}
	for _, t := range after {
		if t.OwnerID == retiring || slices.Contains(owned, t.ID) {
			return nil, "", fmt.Errorf("pipeline trigger token ID %d of the superseded GitLab Poller (user ID %d) is still listed after revocation; revoke it manually and rerun install", t.ID, retiring)
		}
	}
	return owned, "", nil
}

// joinInt64s renders IDs as a comma-separated list.
func joinInt64s(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ", ")
}

// currentWebhookTriggerEvidence is the webhookTriggerEvidence the install uses.
// It re-probes the managed webhooks and trigger inventory and reads the
// trigger-account ledger, so it protects the trigger the webhook uses now on
// either dispatch route: the account's recorded trigger when a webhook embeds
// the account's published token, and the legacy FULLSEND_TRIGGER_TOKEN trigger
// the Poller-owned webhook embeds. Every trigger a managed webhook's
// description names is protected as well (the description is only a hint, so it
// protects but never establishes which trigger a webhook uses). A managed
// webhook that embeds neither credential leaves the evidence unresolved.
func currentWebhookTriggerEvidence(client forge.Client, baseURL, owner, repo string) webhookTriggerEvidence {
	return func(ctx context.Context) ([]int64, string, error) {
		st, err := probeGitLabWebhookState(ctx, client, baseURL, owner, repo)
		if err != nil {
			return nil, "", err
		}
		if len(st.hooks) == 0 {
			return nil, "", nil
		}
		acct, err := loadGitLabWebhookTriggerAccount(ctx, client, owner, repo)
		if err != nil {
			return nil, "", err
		}
		var accountToken string
		var protected []int64
		if acct != nil {
			vars, err := client.ListRepoVariables(ctx, owner, repo)
			if err != nil {
				return nil, "", safeAPIError("listing CI/CD variables", err)
			}
			accountToken = vars[forge.SecretGitLabTriggerAccountToken]
			protected = append(protected, acct.triggerID)
		}
		legacyID := st.activeTriggerID(baseURL)
		legacyIdx := -1
		if legacyID != 0 {
			protected = append(protected, legacyID)
			legacyIdx = st.activeHookIndex(baseURL)
		}
		unresolved := ""
		for i, h := range st.hooks {
			if m := gitlabWebhookActiveTokenRe.FindStringSubmatch(h.Description); m != nil {
				if id, parseErr := strconv.ParseInt(m[1], 10, 64); parseErr == nil {
					protected = append(protected, id)
				}
			}
			known := i == legacyIdx
			if tok, ok := hookTriggerToken(h.URL, baseURL, st.target.projectID); ok && accountToken != "" && tok == accountToken {
				known = true
			}
			if !known {
				unresolved = "a managed webhook's active pipeline trigger cannot be identified from its credential"
			}
		}
		return protected, unresolved, nil
	}
}

// preservedPollerMergeGrants counts the merge-access entries that grant the
// retiring account individually on the exact-name protection rule of the
// default branch (where EnsureGitLabPollerPipelineAccess adds a Poller's grant;
// GrantProtectedBranchMergeUser refuses wildcard rules). It never removes them:
// EnsureGitLabPollerPipelineAccess reuses an existing grant, and fullsend
// records no durable provenance for per-user merge grants, so an entry for the
// retiring user ID cannot be proven to be fullsend's rather than an
// administrator's. A forge without protected-branch support has none.
func preservedPollerMergeGrants(ctx context.Context, client forge.Client, owner, repo string, retiring int64) (int, error) {
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return 0, safeAPIError("reading project to check the retired GitLab Poller's merge grant", err)
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
