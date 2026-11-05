package repos

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ---------------------------------------------------------------------------
// Webhook trigger credential on the trigger-account route (#8509)
// ---------------------------------------------------------------------------
//
// While the trigger-account dispatch activation is recorded (#8438), the
// managed webhook fires GitLab's native pipeline trigger with the trigger
// account's own trigger token, published by the account lifecycle as
// FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN. A webhook-triggered pipeline then runs
// as that account through its deploy-key grant and the dispatcher selects the
// child-pipeline route, exactly like an account-owned polling schedule. The
// Poller-owned webhook trigger and FULLSEND_TRIGGER_TOKEN are retired once the
// webhook carries the account's token.
//
// Without a recorded activation (for example while the activation gate defers,
// including a project whose trigger account cannot own the protected-branch
// schedules), the webhook keeps the Poller-owned trigger and the Poller api
// route, so the Poller merge grant and its verifiers stay in use.
//
// The webhook never mints, rotates or revokes the account's trigger: the
// trigger-account lifecycle owns it, and repos install converges the webhook
// again after that lifecycle ran so it carries the current credential.

// gitlabWebhookAccount is the active trigger-account route as recorded by the
// ledger and its bound dispatch activation. It holds no token value.
type gitlabWebhookAccount struct {
	accountID   int64
	deployKeyID int
	triggerID   int64
	tokenDigest string
}

// deployKeys maps the account to its recorded deploy key for
// gitlabTriggerOwnerProblem.
func (a *gitlabWebhookAccount) deployKeys() map[int64]int {
	if a == nil || a.deployKeyID <= 0 {
		return nil
	}
	return map[int64]int{a.accountID: a.deployKeyID}
}

// loadGitLabWebhookTriggerAccount returns the active trigger-account route, or
// nil when no ledger exists, the ledger is not a ready generation with a
// published trigger, or the dispatch activation is not bound to it. An error
// means the ledger or activation could not be read or decoded; callers fail
// closed on it.
func loadGitLabWebhookTriggerAccount(ctx context.Context, client forge.Client, owner, repo string) (*gitlabWebhookAccount, error) {
	acct, active, err := loadGitLabTriggerAccountRoute(ctx, client, owner, repo)
	if err != nil || !active {
		return nil, err
	}
	return acct, nil
}

// loadGitLabTriggerAccountRoute returns the trigger account's published
// trigger as the ledger records it, whether or not the dispatch activation is
// recorded and bound to it; active reports the activation. acct is nil when no
// ledger exists or it is not a ready generation with a published trigger. The
// published bearer stays usable through the trigger API even while the
// activation is absent or malformed, so the safety checks hold it to the owner
// ceiling regardless of active. An error means the ledger or activation could
// not be read or decoded; callers fail closed on it.
func loadGitLabTriggerAccountRoute(ctx context.Context, client forge.Client, owner, repo string) (acct *gitlabWebhookAccount, active bool, err error) {
	raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabTriggerAccount)
	if err != nil {
		return nil, false, safeAPIError("reading "+forge.VarGitLabTriggerAccount, err)
	}
	if !exists {
		return nil, false, nil
	}
	st, decodeErr := decodeGitLabTriggerAccountState(raw)
	if decodeErr != nil {
		// Fail closed: a present ledger that cannot be read must not look
		// like an absent account route, or the safety checks would skip the
		// account trigger's owner ceiling. The decode error text is withheld.
		return nil, false, fmt.Errorf("invalid %s: the ledger cannot be decoded, so the trigger-account route cannot be verified; repair it with the trigger-account lifecycle", forge.VarGitLabTriggerAccount)
	}
	if st.Phase != gitLabTriggerPhaseReady || st.AccountID <= 0 || st.TriggerTokenID <= 0 || !st.TriggerPublished {
		return nil, false, nil
	}
	acct = &gitlabWebhookAccount{accountID: int64(st.AccountID), deployKeyID: st.DeployKeyID, triggerID: st.TriggerTokenID, tokenDigest: st.TriggerTokenDigest}
	op := &gitLabTriggerAccountOp{opts: GitLabTriggerAccountOptions{Owner: owner, Repo: repo, State: client}, st: st, exists: true}
	active, err = op.dispatchActivationRecorded(ctx)
	if err != nil {
		return nil, false, err
	}
	return acct, active, nil
}

// gitlabTriggerAccountCredentialPublished reads the ledger independently of
// dispatch activation and includes retired generations from an interrupted
// rotation. Either can still have a live bearer in the published variable.
func gitlabTriggerAccountCredentialPublished(ctx context.Context, client forge.Client, owner, repo string) (bool, error) {
	raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabTriggerAccount)
	if err != nil {
		return false, safeAPIError("reading "+forge.VarGitLabTriggerAccount, err)
	}
	if !exists {
		return false, nil
	}
	st, err := decodeGitLabTriggerAccountState(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s: the ledger cannot be decoded", forge.VarGitLabTriggerAccount)
	}
	if st.PublishStarted || st.TriggerPublished {
		return true, nil
	}
	for _, retired := range st.Retired {
		if retired.PublishStarted || retired.TriggerPublished {
			return true, nil
		}
	}
	return false, nil
}

// gitlabTriggerAccountContainmentRequired reports whether a published trigger
// account bearer may be live and so needs containment when the project's
// safety cannot be verified. An unreadable or undecodable ledger counts as
// required (with the error), because the bearer's presence cannot be ruled
// out.
func gitlabTriggerAccountContainmentRequired(ctx context.Context, client forge.Client, owner, repo string) (bool, error) {
	published, err := gitlabTriggerAccountCredentialPublished(ctx, client, owner, repo)
	return published || err != nil, err
}

// GitLabWebhookUsesRetiredTriggerCredential reports whether a managed webhook
// still carries the credential of a retired trigger-account generation: its
// description names a retired generation's trigger, or its URL token matches a
// retired generation's recorded digest. A project with no managed webhook, or
// whose managed webhooks use other credentials (the Poller-owned trigger or the
// current generation), has no unresolved reference, so retirement may proceed
// even while the webhook fast path is deferred. Callers hold the project lease.
// An error means the ledger or the webhooks could not be read; callers fail
// closed on it.
func GitLabWebhookUsesRetiredTriggerCredential(ctx context.Context, client forge.Client, baseURL, owner, repo string) (bool, error) {
	if baseURL == "" {
		return true, errors.New("GitLab base URL unavailable")
	}
	raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabTriggerAccount)
	if err != nil {
		return true, safeAPIError("reading "+forge.VarGitLabTriggerAccount, err)
	}
	if !exists {
		return false, nil
	}
	st, err := decodeGitLabTriggerAccountState(raw)
	if err != nil {
		return true, fmt.Errorf("invalid %s: the ledger cannot be decoded", forge.VarGitLabTriggerAccount)
	}
	if len(st.Retired) == 0 {
		return false, nil
	}
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return true, safeAPIError("reading project", err)
	}
	hooks, err := client.ListProjectHooks(ctx, owner, repo)
	if err != nil {
		return true, safeAPIError("listing project webhooks", err)
	}
	for _, h := range hooks {
		if !isFullsendWebhook(h, baseURL, project.ID, "") {
			continue
		}
		token, hasToken := hookTriggerToken(h.URL, baseURL, project.ID)
		for _, old := range st.Retired {
			if old.TriggerTokenID > 0 && gitlabHookNamesTrigger(h, old.TriggerTokenID) {
				return true, nil
			}
			if hasToken && old.TriggerTokenDigest != "" && triggerTokenDigest(token) == old.TriggerTokenDigest {
				return true, nil
			}
		}
	}
	return false, nil
}

// resolveGitLabWebhookAccountToken returns the published account trigger
// value the webhook may embed. problem is non-empty (completing "webhook
// fast-path deferred: …") when the value cannot be bound to the account's
// recorded trigger or is not stored safely; an error means it could not be
// verified. unsafe is set with a problem when the published value exists but
// is not stored as a masked, protected, wildcard-scoped environment variable,
// so it may be exposed: unlike an ordinary deferral, that needs containment.
// The returned token may be non-empty alongside an error or unsafe, so the
// caller registers it with its redactor before reporting.
func resolveGitLabWebhookAccountToken(ctx context.Context, client forge.Client, owner, repo string, acct *gitlabWebhookAccount) (token, problem string, unsafe bool, err error) {
	triggers, err := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return "", "", false, safeAPIError("listing pipeline trigger tokens", err)
	}
	found := false
	for _, t := range triggers {
		if t.ID == acct.triggerID {
			found = true
			if t.OwnerID != acct.accountID {
				return "", fmt.Sprintf("the trigger account's recorded pipeline trigger token (ID %d) is not owned by the trigger account", t.ID), false, nil
			}
		}
	}
	if !found {
		return "", fmt.Sprintf("the trigger account's recorded pipeline trigger token (ID %d) no longer exists; rerun repos install so the trigger account publishes a replacement", acct.triggerID), false, nil
	}
	vars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		return "", "", false, safeAPIError("listing CI/CD variables", err)
	}
	token = vars[forge.SecretGitLabTriggerAccountToken]
	if token == "" {
		return "", forge.SecretGitLabTriggerAccountToken + " is not published; rerun repos install so the trigger account publishes it", false, nil
	}
	prot, err := client.GetRepoSecretProtection(ctx, owner, repo, forge.SecretGitLabTriggerAccountToken)
	if err != nil {
		return token, forge.SecretGitLabTriggerAccountToken + " protection cannot be verified; the published credential must be contained", true, safeAPIError("checking "+forge.SecretGitLabTriggerAccountToken+" protection", err)
	}
	if !secretUsable(prot) {
		return token, forge.SecretGitLabTriggerAccountToken + " is not a masked, protected, wildcard-scoped environment variable; rerun repos install so the trigger account republishes it", true, nil
	}
	if acct.tokenDigest == "" || triggerTokenDigest(token) != acct.tokenDigest {
		return token, forge.SecretGitLabTriggerAccountToken + " does not match the recorded trigger token fingerprint; rerun repos install so the trigger account republishes it", true, nil
	}
	return token, "", false, nil
}

// gitlabHookNamesTrigger reports whether hook's description records triggerID
// as its active trigger (see gitlabWebhookDescriptionFor).
func gitlabHookNamesTrigger(hook forge.ProjectHook, triggerID int64) bool {
	m := gitlabWebhookActiveTokenRe.FindStringSubmatch(hook.Description)
	return m != nil && m[1] == strconv.FormatInt(triggerID, 10)
}

// accountHookIndex returns the index in st.hooks of the managed webhook
// whose trigger URL embeds token, or -1.
func (st gitlabWebhookState) accountHookIndex(baseURL, token string) int {
	if token == "" {
		return -1
	}
	for i, h := range st.hooks {
		if tok, ok := hookTriggerToken(h.URL, baseURL, st.target.projectID); ok && tok == token {
			return i
		}
	}
	return -1
}

// accountRouteProvisioned reports whether the fast path is complete on the
// trigger-account route: exactly one managed webhook, carrying token and
// naming the account's trigger, configured as desired and not disabled, a
// safely stored webhook secret, and no Poller-owned webhook trigger or
// FULLSEND_TRIGGER_TOKEN left to retire.
func (st gitlabWebhookState) accountRouteProvisioned(baseURL, token string, triggerID int64) bool {
	if st.webhookSecret == "" || len(st.hooks) != 1 || len(st.triggers) != 0 || st.triggerStored {
		return false
	}
	if st.accountHookIndex(baseURL, token) != 0 {
		return false
	}
	hook := st.hooks[0]
	if hook.HookDeliveryDisabled() || !gitlabHookNamesTrigger(hook, triggerID) {
		return false
	}
	return gitlabWebhookMatches(hook, desiredGitLabWebhook(baseURL, st.target, token, st.webhookSecret, triggerID))
}

// ensureGitLabWebhookAccountRoute converges the fast path on the
// trigger-account route. It runs after the safety and readiness gates, which
// already verified the account trigger's owner (see gitlabTriggerSafetyProblem).
// It points the managed webhook at the account's published trigger token,
// verifies the published state, and only then revokes the Poller-owned webhook
// triggers and deletes FULLSEND_TRIGGER_TOKEN, so an interrupted run leaves a
// working webhook and the next run finishes the cleanup. carried are details
// from earlier steps of this run.
func ensureGitLabWebhookAccountRoute(ctx context.Context, client forge.Client, st gitlabWebhookState, acct *gitlabWebhookAccount, baseURL, owner, repo string, rotate, dryRun bool, red *credentialRedactor, carried []string) (GitLabWebhookResult, error) {
	res := GitLabWebhookResult{Details: carried}
	token, problem, unsafe, err := resolveGitLabWebhookAccountToken(ctx, client, owner, repo, acct)
	red.add(token)
	if err != nil && !unsafe {
		return res, err
	}
	if unsafe {
		// The published bearer may be exposed to unprotected jobs or logs, so
		// deferring and leaving the webhook dispatching with it is not enough.
		// Remove the managed webhook and report a typed error: repos install
		// then runs the ownership-verified trigger-account containment, which
		// the webhook step cannot do (GitLabWebhookTriggerOwnerUnsafe).
		res.Action = "deferred"
		res.Details = append(res.Details, "webhook fast-path deferred: "+problem)
		teardownCtx, cancelTeardown := gitlabCleanupContext(ctx)
		defer cancelTeardown()
		details, teardownErr := revokeGitLabWebhookFastPath(teardownCtx, client, owner, repo, dryRun)
		res.Details = append(res.Details, details...)
		if dryRun {
			return res, nil
		}
		return res, errors.Join(errGitLabAccountTokenUnsafe, err, safeAPIError("tearing down the managed fast path", teardownErr))
	}
	if problem != "" {
		// Nothing is changed: an existing webhook keeps working until the
		// trigger account republishes its credential.
		res.Action = "deferred"
		res.Details = append(res.Details, "webhook fast-path deferred: "+problem)
		return res, nil
	}
	// GitLab does not return trigger values in its trigger inventory. Reject a
	// value that is demonstrably the Poller bearer before retiring that bearer:
	// the account variable can be copied from the Poller variable or from the
	// URL of an existing webhook that names a different trigger ID.
	if st.triggerStored && token == st.ownerToken {
		res.Action = "deferred"
		res.Details = append(res.Details, "webhook fast-path deferred: the trigger account variable matches the Poller-owned trigger credential; repair the account token variable before moving the webhook")
		return res, nil
	}
	for _, hook := range st.hooks {
		oldToken, ok := hookTriggerToken(hook.URL, baseURL, st.target.projectID)
		if ok && oldToken == token && !gitlabHookNamesTrigger(hook, acct.triggerID) {
			res.Action = "deferred"
			res.Details = append(res.Details, "webhook fast-path deferred: the trigger account variable matches a webhook credential attributed to another trigger; repair the account token variable before moving the webhook")
			return res, nil
		}
	}
	if rotate {
		res.Details = append(res.Details, fmt.Sprintf("The webhook uses the trigger account's pipeline trigger token (%s); rotate it with --rotate-gitlab-trigger-account. %s is not used on this route", forge.SecretGitLabTriggerAccountToken, forge.SecretTriggerToken))
	}
	if st.accountRouteProvisioned(baseURL, token, acct.triggerID) {
		if len(res.Details) > 0 {
			res.Action = "update"
			return res, nil
		}
		return GitLabWebhookResult{Action: "none"}, nil
	}
	res.Action = "update"
	if dryRun {
		res.Action = "would-update"
	}
	keepIdx := st.accountHookIndex(baseURL, token)
	tokenChanged := keepIdx < 0 || !gitlabHookNamesTrigger(st.hooks[keepIdx], acct.triggerID)
	if keepIdx < 0 {
		keepIdx = 0
	}
	if tokenChanged {
		res.Details = append(res.Details, fmt.Sprintf("%s the webhook to the trigger account's pipeline trigger token (ID %d, %s)", webhookVerb(dryRun, "Moved", "Would move"), acct.triggerID, forge.SecretGitLabTriggerAccountToken))
	}
	if err := convergeGitLabWebhookHook(ctx, client, st, baseURL, owner, repo, token, acct.triggerID, keepIdx, tokenChanged, dryRun, red, &res); err != nil {
		return res, err
	}
	if tokenChanged && !dryRun {
		if raceErr := verifyGitLabWebhookActiveToken(ctx, client, baseURL, owner, repo, st.target.projectID, token, acct.triggerID); raceErr != nil {
			if GitLabWebhookTriggerOwnerUnsafe(raceErr) {
				// The account's access is outside the Developer runtime ceiling
				// or unverifiable: remove the managed webhook so no event starts
				// a pipeline with it. The account's trigger and published token are
				// not touched here: repos install runs the ownership-verified
				// trigger-account containment when it sees this error
				// (GitLabWebhookTriggerOwnerUnsafe).
				res.Action = "deferred"
				teardownCtx, cancelTeardown := gitlabCleanupContext(ctx)
				defer cancelTeardown()
				details, teardownErr := revokeGitLabWebhookFastPath(teardownCtx, client, owner, repo, false)
				res.Details = append(res.Details, details...)
				return res, errors.Join(raceErr, safeAPIError("tearing down the managed fast path", teardownErr))
			}
			return res, raceErr
		}
	}
	// Retire the Poller-owned webhook credential only now that the webhook
	// carries the account's token.
	var errs []error
	for _, old := range st.triggers {
		if !dryRun {
			if revokeErr := client.RevokePipelineTriggerToken(ctx, owner, repo, old.ID); revokeErr != nil && !forge.IsNotFound(revokeErr) {
				errs = append(errs, fmt.Errorf("revoking Poller-owned pipeline trigger token ID %d: %w", old.ID, revokeErr))
				continue
			}
		}
		res.Details = append(res.Details, fmt.Sprintf("%s Poller-owned pipeline trigger token (ID %d): the webhook uses the trigger account's token", webhookVerb(dryRun, "Revoked", "Would revoke"), old.ID))
	}
	if st.triggerStored {
		if !dryRun {
			if delErr := client.DeleteRepoSecret(ctx, owner, repo, forge.SecretTriggerToken); delErr != nil && !forge.IsNotFound(delErr) {
				errs = append(errs, fmt.Errorf("deleting %s: %w", forge.SecretTriggerToken, delErr))
			} else {
				res.Details = append(res.Details, fmt.Sprintf("Deleted the %s variable: the webhook uses the trigger account's token", forge.SecretTriggerToken))
			}
		} else {
			res.Details = append(res.Details, fmt.Sprintf("Would delete the %s variable: the webhook uses the trigger account's token", forge.SecretTriggerToken))
		}
	}
	return res, errors.Join(errs...)
}

// GitLabWebhookTriggerRouteStale reports whether the managed webhook's trigger
// credential no longer matches the dispatch route (#8509): the trigger-account
// route is active but no managed webhook names the account's recorded trigger,
// or the route is inactive while a managed webhook names a trigger that is not
// a Poller-owned webhook trigger. Its only production caller is the
// account-route repair after a failed Poller stage (the account-only webhook
// path), which converges the webhook again when it reports true. A project
// without a managed webhook is stale only while the account route is active;
// on the Poller route the webhook step provisions it.
func GitLabWebhookTriggerRouteStale(ctx context.Context, client forge.Client, baseURL, owner, repo string) (bool, error) {
	if baseURL == "" {
		return false, nil
	}
	project, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return false, safeAPIError("reading project", err)
	}
	hooks, err := client.ListProjectHooks(ctx, owner, repo)
	if err != nil {
		return false, safeAPIError("listing project webhooks", err)
	}
	var managed []forge.ProjectHook
	for _, h := range hooks {
		if isFullsendWebhook(h, baseURL, project.ID, "") {
			managed = append(managed, h)
		}
	}
	acct, err := loadGitLabWebhookTriggerAccount(ctx, client, owner, repo)
	if err != nil {
		return false, err
	}
	if len(managed) == 0 {
		// On the Poller route a missing webhook is provisioned by the webhook
		// step. Once the lifecycle activated the account route, that earlier
		// step may have deferred without creating a hook, so a fresh install
		// would finish without the account-owned webhook.
		return acct != nil, nil
	}
	if acct != nil {
		// A matching description alone is not proof that the URL still carries
		// the published bearer, that delivery is enabled, or that a duplicate
		// hook was not left behind. Use the complete read-only convergence probe.
		return GitLabWebhookNeedsProvisioning(ctx, client, baseURL, owner, repo)
	}
	triggers, err := client.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return false, safeAPIError("listing pipeline trigger tokens", err)
	}
	for _, h := range managed {
		m := gitlabWebhookActiveTokenRe.FindStringSubmatch(h.Description)
		if m == nil {
			continue
		}
		legacy := false
		for _, t := range triggers {
			if strconv.FormatInt(t.ID, 10) == m[1] && t.Description == GitLabWebhookTriggerDescription {
				legacy = true
			}
		}
		if !legacy {
			return true, nil
		}
	}
	return false, nil
}
