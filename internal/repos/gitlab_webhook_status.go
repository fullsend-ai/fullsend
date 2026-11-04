package repos

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// GitLab webhook fast-path states reported by repos status (ADR 0125).
const (
	// GitLabWebhookStateReady: the managed webhook and its trigger
	// credential are provisioned as desired and every gate holds.
	GitLabWebhookStateReady = "ready"
	// GitLabWebhookStateMissing: every gate holds but no Fullsend-managed
	// webhook exists; repos install creates it.
	GitLabWebhookStateMissing = "missing"
	// GitLabWebhookStateDrifted: the fast path is misconfigured, or a
	// safety or readiness contract is unmet.
	GitLabWebhookStateDrifted = "drifted"
	// GitLabWebhookStateDeferred: nothing is provisioned yet and the
	// pipeline wrapper on the default branch does not include the
	// dispatcher (a fresh install whose initialization MR has not merged,
	// or a scaffold version that predates the fast path). Informational.
	GitLabWebhookStateDeferred = "deferred"
	// GitLabWebhookStateUnreadable: the fast path could not be verified.
	GitLabWebhookStateUnreadable = "unreadable"
)

// gitlabWebhookDriftField is the Drift.Field for webhook fast-path drift.
const gitlabWebhookDriftField = "gitlab-webhook"

// GitLabWebhookStatus is the readiness of the GitLab webhook fast path.
// It carries names, numeric IDs and booleans only: never the trigger
// token, the webhook secret, or the webhook URL (which embeds the token).
type GitLabWebhookStatus struct {
	State string `json:"state"`
	// WebhookPresent reports that at least one Fullsend-managed project
	// webhook exists.
	WebhookPresent bool `json:"webhook_present"`
	// WebhookConfigured reports that exactly one managed webhook carries
	// the active trigger credential with the desired configuration.
	WebhookConfigured bool `json:"webhook_configured"`
	// TriggerTokenReady reports that the trigger credential the webhook
	// must carry is stored safely and tied to its pipeline trigger token.
	TriggerTokenReady bool `json:"trigger_token_ready"`
	// WebhookSecretReady reports that FULLSEND_WEBHOOK_SECRET is stored as
	// a masked, protected, wildcard-scoped environment variable.
	WebhookSecretReady bool     `json:"webhook_secret_ready"`
	Reasons            []string `json:"reasons,omitempty"`
}

// DiagnoseGitLabWebhook reports the webhook fast path's readiness on
// owner/repo using the same probe, safety and readiness checks as
// EnsureGitLabWebhookFastPath. It only reads.
//
// A safety violation (for example ci_pipeline_variables_minimum_override_role
// not no_one_allowed) or an unmet readiness gate (for example a wrapper or
// root .gitlab-ci.yml without the typed pipeline-input contract) is drift:
// it needs an administrator or a repos install to fix. The one informational
// state is deferred: nothing is provisioned and the committed pipeline
// wrapper does not include the dispatcher yet.
func DiagnoseGitLabWebhook(ctx context.Context, client forge.Client, owner, repo string) GitLabWebhookStatus {
	baseURL := GitLabBaseURL(client)
	if baseURL == "" {
		return GitLabWebhookStatus{State: GitLabWebhookStateUnreadable, Reasons: []string{"GitLab base URL unavailable; the webhook fast path is unverified"}}
	}
	st, err := probeGitLabWebhookState(ctx, client, baseURL, owner, repo)
	if err != nil {
		// The probe redacts or withholds credential-bearing error text.
		return GitLabWebhookStatus{State: GitLabWebhookStateUnreadable, Reasons: []string{"could not read the webhook fast path: " + err.Error()}}
	}
	red := &credentialRedactor{}
	red.add(st.known...)
	for _, h := range st.hooks {
		red.addHookURL(h.URL)
	}
	unreadable := func(what string, err error) GitLabWebhookStatus {
		return GitLabWebhookStatus{State: GitLabWebhookStateUnreadable, WebhookPresent: len(st.hooks) > 0, Reasons: []string{"could not verify " + what + ": " + red.redact(err).Error()}}
	}

	status := GitLabWebhookStatus{
		WebhookPresent:     len(st.hooks) > 0,
		WebhookSecretReady: st.webhookSecret != "",
	}
	live := len(st.hooks) > 0 || len(st.triggers) > 0 || st.triggerStored
	if !live {
		// A published trigger-account bearer, including a retired generation
		// from an interrupted rotation, is project-wide and stays usable
		// after the webhook is gone. An unreadable ledger fails closed: its
		// bearer cannot be ruled out, so the safety checks still run.
		live, _ = gitlabTriggerAccountContainmentRequired(ctx, client, owner, repo)
	}
	if !live {
		wrapper, wErr := client.GetFileContent(ctx, owner, repo, fullsendPipelineInclude)
		if wErr != nil && !forge.IsNotFound(wErr) {
			return unreadable("the pipeline wrapper", safeAPIError("reading "+fullsendPipelineInclude, wErr))
		}
		if wErr != nil || !gitlabWrapperReferencesDispatcher(wrapper) {
			status.State = GitLabWebhookStateDeferred
			status.Reasons = []string{"the pipeline wrapper on the default branch does not include the dispatcher yet; repos install provisions the webhook fast path once it does"}
			return status
		}
	}

	// Safety comes first, as in EnsureGitLabWebhookFastPath: a violation
	// revokes live credentials and blocks provisioning.
	problem, err := gitlabTriggerSafetyProblem(ctx, client, owner, repo, st.target.defaultBranch)
	if err != nil {
		return unreadable("the trigger-token safety invariants", err)
	}
	if problem != "" {
		status.State = GitLabWebhookStateDrifted
		advice := "fix it, then run repos install to provision the webhook fast path"
		if live {
			advice = "fix it; repos install revokes the managed webhook and trigger credential until then"
		}
		status.Reasons = []string{problem + "; " + advice}
		return status
	}
	reason, err := gitlabWebhookReadiness(ctx, client, owner, repo, st.target.defaultBranch)
	if err != nil {
		return unreadable("webhook fast-path readiness", err)
	}
	if reason != "" {
		status.State = GitLabWebhookStateDrifted
		status.Reasons = []string{reason}
		return status
	}

	acct, err := loadGitLabWebhookTriggerAccount(ctx, client, owner, repo)
	if err != nil {
		return unreadable("the trigger-account route", err)
	}
	if acct != nil {
		token, tokProblem, _, tokErr := resolveGitLabWebhookAccountToken(ctx, client, owner, repo, acct)
		red.add(token)
		if tokErr != nil {
			return unreadable("the trigger account's published trigger token", tokErr)
		}
		status.TriggerTokenReady = tokProblem == ""
		if tokProblem != "" {
			status.Reasons = append(status.Reasons, tokProblem)
		}
		status.Reasons = append(status.Reasons, gitlabWebhookHookReasons(st, baseURL, token, acct.triggerID, st.accountHookIndex(baseURL, token), true)...)
		if len(st.triggers) > 0 {
			status.Reasons = append(status.Reasons, fmt.Sprintf("%d Poller-owned webhook pipeline trigger token(s) await retirement on the trigger-account route", len(st.triggers)))
		}
		if st.triggerStored {
			status.Reasons = append(status.Reasons, forge.SecretTriggerToken+" awaits retirement on the trigger-account route")
		}
		status.WebhookConfigured = tokProblem == "" && st.accountRouteProvisioned(baseURL, token, acct.triggerID)
	} else {
		activeID := st.activeTriggerID(baseURL)
		status.TriggerTokenReady = st.triggerToken != "" && activeID != 0
		switch {
		case st.triggerUnsafe:
			status.Reasons = append(status.Reasons, forge.SecretTriggerToken+" is not a masked, protected, wildcard-scoped environment variable; repos install replaces it")
		case st.triggerToken == "":
			status.Reasons = append(status.Reasons, forge.SecretTriggerToken+" is not stored")
		case activeID == 0 && len(st.hooks) > 0:
			status.Reasons = append(status.Reasons, "the stored "+forge.SecretTriggerToken+" cannot be tied to a Fullsend-managed pipeline trigger token; repos install replaces it")
		}
		switch {
		case len(st.triggers) == 0:
			status.Reasons = append(status.Reasons, "no Fullsend-managed pipeline trigger token exists")
		case len(st.triggers) > 1:
			status.Reasons = append(status.Reasons, fmt.Sprintf("%d superseded Fullsend-managed pipeline trigger token(s) await revocation", len(st.triggers)-1))
		}
		status.Reasons = append(status.Reasons, gitlabWebhookHookReasons(st, baseURL, st.triggerToken, activeID, st.activeHookIndex(baseURL), false)...)
		status.WebhookConfigured = st.provisioned(baseURL)
		if activeID == 0 {
			// Repairing the fast path needs a replacement trigger token, which
			// repos install mints through a fresh Poller generation. Name what
			// blocks that generation so the drift reads as pending (#8802).
			gen, genErr := loadPollerGenerations(ctx, client, owner, repo)
			if genErr != nil {
				return unreadable("the GitLab Poller generation state", genErr)
			}
			managedID, idErr := ManagedGitLabRoleAccountID(ctx, client, owner, repo, gitlabroles.RolePoller)
			if idErr != nil {
				return unreadable("the managed GitLab Poller identity", idErr)
			}
			if p := gen.Pending; p != nil && p.UserID > 0 && (p.Phase == PollerPhaseElevated || p.Phase == PollerPhaseTriggerRequested) && pollerTriggerRequestUnknown(*p) {
				// Install first recovers an interrupted generation, and one
				// whose single trigger request may have been sent is
				// quarantined, which then blocks the replacement.
				quarantined := *p
				quarantined.Phase = PollerPhaseQuarantined
				quarantined.Reason = pollerTriggerUnknownReason(p.UserID)
				gen.Pending = &quarantined
			}
			if p := gen.Pending; p != nil && p.Phase == PollerPhaseVerified && gen.RetiringUserID == 0 {
				next, cutErr := beginPollerCutover(gen, p.UserID)
				if p.UserID > 0 && int64(managedID) == p.UserID && cutErr == nil {
					// The pending account already is the managed Poller, so
					// install records the interrupted cutover
					// (prepareHandoffPollerOwnedTrigger), which makes the
					// previous current Poller retiring. Diagnose that
					// retirement obligation, not the unrecorded generation.
					gen = next
				} else {
					// Install recovers any other verified generation on its
					// own: it resumes or abandons an unwired one. It is not a
					// blocker. A generation whose trigger may have been
					// distributed is abandoned, which counts against the
					// abandoned-account cap that can refuse the replacement.
					if p.TriggerDistributed {
						gen.AbandonedUserIDs = append(slices.Clone(gen.AbandonedUserIDs), p.UserID)
					}
					gen.Pending = nil
				}
			}
			// Install defers on the same checks, in the same order: a blocked
			// generation document first, then a current Poller that is not the
			// managed one. Without a managed Poller install has nothing to
			// compare, so only the document's own blocker applies.
			blocked := pollerHandoffBlocked(gen)
			if blocked == "" && managedID > 0 {
				blocked = pollerCurrentMismatch(gen, int64(managedID))
			}
			if blocked != "" {
				status.Reasons = append(status.Reasons, "reconciliation pending: "+blocked)
			}
		}
	}

	switch {
	case status.WebhookConfigured && len(status.Reasons) == 0:
		status.State = GitLabWebhookStateReady
	case !status.WebhookPresent:
		status.State = GitLabWebhookStateMissing
	default:
		status.State = GitLabWebhookStateDrifted
	}
	if status.State != GitLabWebhookStateReady && len(status.Reasons) == 0 {
		status.Reasons = []string{"the webhook fast path is not fully provisioned; run repos install to repair it"}
	}
	return status
}

// gitlabWebhookHookReasons names what is wrong with the webhook secret and
// the managed webhooks. idx is the managed hook carrying token (or -1), and
// triggerID the pipeline trigger token the hook must name. requireNamed
// also requires the hook description to record triggerID, as the
// trigger-account route does.
func gitlabWebhookHookReasons(st gitlabWebhookState, baseURL, token string, triggerID int64, idx int, requireNamed bool) []string {
	var reasons []string
	switch {
	case st.secretUnsafe:
		reasons = append(reasons, forge.SecretWebhookSecret+" is not a masked, protected, wildcard-scoped environment variable; repos install replaces it")
	case st.webhookSecret == "":
		reasons = append(reasons, forge.SecretWebhookSecret+" is not stored")
	}
	switch {
	case len(st.hooks) == 0:
		reasons = append(reasons, "no Fullsend-managed project webhook exists; repos install creates it")
	case idx < 0:
		reasons = append(reasons, "no Fullsend-managed project webhook carries the active trigger token; repos install updates it")
	default:
		hook := st.hooks[idx]
		switch {
		case hook.HookDeliveryDisabled():
			reasons = append(reasons, fmt.Sprintf("GitLab disabled the managed project webhook (ID %d) after delivery failures; repos install recreates it", hook.ID))
		case requireNamed && !gitlabHookNamesTrigger(hook, triggerID):
			reasons = append(reasons, fmt.Sprintf("the managed project webhook (ID %d) does not record pipeline trigger token ID %d; repos install updates it", hook.ID, triggerID))
		case !gitlabWebhookMatches(hook, desiredGitLabWebhook(baseURL, st.target, token, st.webhookSecret, triggerID)):
			reasons = append(reasons, fmt.Sprintf("the managed project webhook (ID %d) differs from the desired trigger ref, events or SSL verification; repos install updates it", hook.ID))
		}
	}
	if len(st.hooks) > 1 {
		reasons = append(reasons, fmt.Sprintf("%d duplicate Fullsend-managed project webhook(s) exist; repos install deletes them", len(st.hooks)-1))
	}
	return reasons
}

// ApplyGitLabWebhookStatus records the webhook fast path's diagnosis on
// status and reports every state but ready and deferred as
// gitlab-webhook drift.
func ApplyGitLabWebhookStatus(status *RepoStatus, diag GitLabWebhookStatus) {
	status.GitLabWebhook = &diag
	status.Drifts = slices.DeleteFunc(status.Drifts, func(d Drift) bool { return d.Field == gitlabWebhookDriftField })
	switch diag.State {
	case GitLabWebhookStateReady, GitLabWebhookStateDeferred:
		return
	}
	status.Drifts = append(status.Drifts, Drift{
		Field:    gitlabWebhookDriftField,
		Expected: GitLabWebhookStateReady,
		Actual:   diag.State + ": " + strings.Join(diag.Reasons, "; "),
	})
}
