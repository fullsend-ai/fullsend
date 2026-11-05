package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// GitLab trigger-account states reported by repos status.
const (
	GitLabTriggerStateNotInstalled = "not-installed"
	GitLabTriggerStateReady        = "ready"
	GitLabTriggerStateDrifted      = "drifted"
	GitLabTriggerStateDeferred     = "deferred"
	GitLabTriggerStateInProgress   = "in-progress"
	GitLabTriggerStateCleanup      = "cleanup-pending"
	GitLabTriggerStateUnreadable   = "unreadable"
)

// GitLabTriggerAccountStatus is the readiness of the trigger-only service
// account. Reasons name resources by ID and never carry token values.
type GitLabTriggerAccountStatus struct {
	State   string   `json:"state"`
	Reasons []string `json:"reasons,omitempty"`
}

// GitLabTriggerAccountUninstaller is implemented by the GitLab uninstall token
// client. It removes every trigger-account resource the ledger proves
// fullsend owns, under the project lease the uninstall already holds, and
// keeps FULLSEND_GITLAB_TRIGGER_ACCOUNT until all of them are gone.
//
// ContainGitLabTriggerAccountForUninstall is the containment-only step: it
// revokes recorded bootstrap tokens and restores Developer access without the
// Poller inventory, so uninstall can run it before any gate that can return
// early. It removes nothing and does nothing when no ledger exists.
type GitLabTriggerAccountUninstaller interface {
	ContainGitLabTriggerAccountForUninstall(ctx context.Context, client forge.Client, lease *GitLabProjectLease, owner, repo string) error
	RemoveGitLabTriggerAccountForUninstall(ctx context.Context, client forge.Client, lease *GitLabProjectLease, owner, repo string) error
}

// DiagnoseGitLabTriggerAccount reports the trigger account's readiness from
// the ledger and the trigger-token sink. When api is non-nil it also checks
// the recorded resources against GitLab (drift) and, before anything is
// installed, reports why install would defer. branch is the protected default
// branch the account must be granted on. It only reads.
func DiagnoseGitLabTriggerAccount(ctx context.Context, client forge.Client, api GitLabTriggerAccountAPI, owner, repo, branch string) GitLabTriggerAccountStatus {
	return diagnoseGitLabTriggerAccount(ctx, client, api, owner, repo, branch, false)
}

// diagnoseGitLabTriggerAccount permits the dispatch gate (forDispatchGate) to
// check the ready current generation during the intentional webhook cutover
// window. In that mode it neither reports an outstanding retirement nor
// managed polling schedules the account does not own yet as drift, because the
// schedules move to the account only after the gate passes. Status still
// reports both as drift until cleanup completes or install moves the schedules.
func diagnoseGitLabTriggerAccount(ctx context.Context, client forge.Client, api GitLabTriggerAccountAPI, owner, repo, branch string, forDispatchGate bool) GitLabTriggerAccountStatus {
	raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabTriggerAccount)
	if err != nil {
		return GitLabTriggerAccountStatus{State: GitLabTriggerStateUnreadable, Reasons: []string{"could not read " + forge.VarGitLabTriggerAccount}}
	}
	op := &gitLabTriggerAccountOp{opts: GitLabTriggerAccountOptions{Owner: owner, Repo: repo, Branch: branch, API: api, State: client}}
	if !exists {
		return diagnoseUninstalledTriggerAccount(ctx, op, api, branch)
	}
	if strings.TrimSpace(raw) == "" {
		return GitLabTriggerAccountStatus{State: GitLabTriggerStateUnreadable, Reasons: []string{forge.VarGitLabTriggerAccount + " is blank; it must not be emptied by hand. Run repos uninstall to retry cleanup"}}
	}
	st, err := decodeGitLabTriggerAccountState(raw)
	if err != nil {
		return GitLabTriggerAccountStatus{State: GitLabTriggerStateUnreadable, Reasons: []string{err.Error()}}
	}
	op.st, op.exists = st, true

	held := triggerAccountHeldResources(st)
	switch st.Phase {
	case gitLabTriggerPhaseRemoving:
		return GitLabTriggerAccountStatus{State: GitLabTriggerStateCleanup, Reasons: append([]string{"an uninstall is unfinished; re-run repos uninstall to remove what remains"}, held...)}
	case gitLabTriggerPhaseBootstrapping, gitLabTriggerPhaseRotating:
		return GitLabTriggerAccountStatus{State: GitLabTriggerStateInProgress, Reasons: append([]string{"an install or rotation did not finish (" + st.Phase + "); re-run repos install to continue"}, held...)}
	}
	var reasons []string
	// A default-branch change defers install, but it does not hide what is
	// wrong with the resources recorded on the old branch: those are still
	// diagnosed below, and the state stays deferred only when nothing else is
	// wrong.
	branchChanged := st.Branch != branch && branch != ""
	// A published generation whose trigger is no longer published had its
	// token variable removed and trigger revoked, for example after an unsafe
	// variable was contained. Keep saying so, including while install defers.
	removedNote := "the published trigger token was removed or revoked; run repos install to publish a replacement"
	var deferralNotes []string
	if branchChanged {
		deferralNotes = append(deferralNotes, branchChangeReason(st.Branch, branch))
	}
	if st.Elevated || st.PATID != 0 || st.PATPending {
		reasons = append(reasons, "a bootstrap elevation obligation is still recorded")
	}
	if !st.TriggerPublished {
		// Without a branch change this is drift. With one, a contained and
		// removed trigger is part of the explanation for the deferral.
		notPublished := []string{"the trigger token is not published"}
		if st.PublishStarted {
			notPublished = append(notPublished, removedNote)
		}
		if branchChanged {
			if st.PublishStarted {
				deferralNotes = append(deferralNotes, removedNote)
			}
		} else {
			reasons = append(reasons, notPublished...)
		}
	} else if problems, err := triggerSinkProblems(ctx, client, owner, repo); err != nil {
		reasons = append(reasons, "could not verify "+forge.SecretGitLabTriggerAccountToken+"; its masking, protection, type and scope are unverified")
	} else {
		for _, p := range problems {
			reasons = append(reasons, p+"; run repos install to revoke the trigger and publish a replacement")
		}
		if len(problems) == 0 {
			if st.TriggerTokenDigest == "" {
				reasons = append(reasons, "the published trigger token predates value binding; run repos install to rotate it to a fingerprinted generation")
			} else if vars, valueErr := client.ListRepoVariables(ctx, owner, repo); valueErr != nil {
				reasons = append(reasons, "could not verify the published trigger token value against its recorded fingerprint")
			} else if value := vars[forge.SecretGitLabTriggerAccountToken]; value == "" || triggerTokenDigest(value) != st.TriggerTokenDigest {
				reasons = append(reasons, "the published trigger token value does not match its recorded fingerprint; run repos install to contain it and publish a replacement")
			}
		}
	}
	if !forDispatchGate {
		for _, old := range st.Retired {
			reasons = append(reasons, fmt.Sprintf("retirement of the previous trigger account generation (account %d) is unfinished; re-run repos install or repos uninstall to finish it", old.AccountID))
		}
	}
	if api != nil {
		// A live credential is only safe while pipeline-variable overrides are
		// disabled; install and rotation defer, and contain it, otherwise.
		if reason, err := op.overrideRoleDeferral(ctx); err != nil {
			reasons = append(reasons, "could not verify ci_pipeline_variables_minimum_override_role; the pipeline-variable override restriction is unverified, so install and rotation defer")
		} else if reason != "" {
			reasons = append(reasons, "install and rotation defer: "+reason)
		}
		reasons = append(reasons, op.liveDrift(ctx)...)
		// The dispatch gate must not depend on schedule ownership: the
		// schedules move to the account only after the gate passes.
		if !forDispatchGate {
			reasons = append(reasons, op.scheduleOwnershipDrift(ctx)...)
		}
	}
	if len(reasons) > 0 {
		return GitLabTriggerAccountStatus{State: GitLabTriggerStateDrifted, Reasons: append(deferralNotes, reasons...)}
	}
	if branchChanged {
		return GitLabTriggerAccountStatus{State: GitLabTriggerStateDeferred, Reasons: deferralNotes}
	}
	return GitLabTriggerAccountStatus{State: GitLabTriggerStateReady}
}

func diagnoseUninstalledTriggerAccount(ctx context.Context, op *gitLabTriggerAccountOp, api GitLabTriggerAccountAPI, branch string) GitLabTriggerAccountStatus {
	status := GitLabTriggerAccountStatus{State: GitLabTriggerStateNotInstalled, Reasons: []string{"no trigger account is installed; run repos install"}}
	if api == nil || branch == "" {
		return status
	}
	reason, err := op.deferral(ctx)
	switch {
	case err != nil:
		status.Reasons = append(status.Reasons, "could not check whether install can proceed")
	case reason != "":
		status.State = GitLabTriggerStateDeferred
		status.Reasons = []string{"install defers: " + reason}
	}
	return status
}

// triggerAccountHeldResources names what the ledger still records.
func triggerAccountHeldResources(st gitLabTriggerAccountState) []string {
	var held []string
	for _, old := range st.Retired {
		held = append(held, fmt.Sprintf("previous generation account %d is awaiting retirement", old.AccountID))
	}
	if st.AccountID != 0 {
		held = append(held, fmt.Sprintf("service account %d is recorded", st.AccountID))
	}
	if st.Elevated {
		held = append(held, "the account may still hold Maintainer access")
	}
	if st.PATID != 0 || st.PATPending {
		held = append(held, "a bootstrap personal access token may still be active")
	}
	if st.DeployKeyID != 0 || st.DeployKeyFingerprint != "" {
		held = append(held, fmt.Sprintf("deploy key %d is recorded", st.DeployKeyID))
	}
	if st.GrantEntryID != 0 {
		held = append(held, fmt.Sprintf("protected-branch push entry %d on %s is recorded", st.GrantEntryID, st.Branch))
	}
	if st.TriggerTokenID != 0 {
		held = append(held, fmt.Sprintf("pipeline trigger token %d is recorded", st.TriggerTokenID))
	}
	return held
}

// foreignStateReason words a failed foreign-credential or foreign-resource
// check. An inventory that could not be read (a redacted API error) is
// unverified; any other failure names what was found.
func foreignStateReason(err error) string {
	var apiErr *redactedError
	if errors.As(err, &apiErr) {
		return "could not verify the trigger account holds no credential or resource fullsend did not create: " + err.Error()
	}
	return "install would refuse to continue: " + err.Error()
}

// scheduleOwnershipDrift reports managed polling schedules that the trigger
// account does not own although the dispatch activation is recorded for it.
// GitLab runs a scheduled pipeline with its owner's permissions, so such a
// schedule polls as that user (for example the installer) instead of the
// Developer-level account until install moves it (#8602). Without a recorded
// activation the schedules are meant to stay installer-owned.
func (op *gitLabTriggerAccountOp) scheduleOwnershipDrift(ctx context.Context) []string {
	active, err := op.dispatchActivationRecorded(ctx)
	if err != nil {
		return []string{"could not read the dispatch activation; the owner of the managed polling schedules is unverified"}
	}
	if !active {
		return nil
	}
	schedules, err := op.opts.State.ListPipelineSchedules(ctx, op.opts.Owner, op.opts.Repo)
	if err != nil {
		return []string{"could not list pipeline schedules; the owner of the managed polling schedules is unverified"}
	}
	var reasons []string
	installerID := -1
	for _, s := range schedules {
		if !isManagedPipelineSchedule(s.Description) || s.OwnerID == int64(op.st.AccountID) {
			continue
		}
		// Install adopts only a retired generation's schedules and those of a
		// verified installer Maintainer (planAccountSchedules); it refuses any
		// other owner, so re-running it would not repair that drift.
		adoptable := op.recordedGenerationOwner(s.OwnerID)
		if !adoptable {
			if installerID < 0 {
				id, err := op.opts.API.GetAuthenticatedUserID(ctx)
				if err != nil {
					reasons = append(reasons, "could not verify the installer; the owner of the managed polling schedules is unverified")
					break
				}
				installerID = id
			}
			trusted, err := op.installerScheduleOwner(ctx, s.OwnerID, int64(installerID))
			if err != nil {
				reasons = append(reasons, fmt.Sprintf("could not verify the owner of managed polling schedule %d; its owner is unverified", s.ID))
				continue
			}
			adoptable = trusted
		}
		advice := "re-run repos install to move it to the trigger account"
		if !adoptable {
			advice = "install refuses to adopt it because its owner is not a verified installer Maintainer; an administrator must remove or reassign it, then re-run repos install"
		}
		reasons = append(reasons, fmt.Sprintf("managed polling schedule %d (%q) is owned by user %d, not the trigger account, although the dispatch activation is recorded, so it runs with that user's permissions; %s", s.ID, s.Description, s.OwnerID, advice))
	}
	return reasons
}

// liveDrift compares the recorded resources with GitLab. A failed read is
// reported as unverified rather than as drift or health.
func (op *gitLabTriggerAccountOp) liveDrift(ctx context.Context) []string {
	var reasons []string
	acct, found, err := op.findOwnedAccount(ctx)
	switch {
	case err != nil:
		return []string{"the recorded service account failed its ownership check: " + err.Error()}
	case !found:
		return []string{fmt.Sprintf("recorded service account %d no longer exists", op.st.AccountID)}
	}
	level, err := op.opts.API.GetProjectMemberAccessLevel(ctx, op.opts.Owner, op.opts.Repo, int64(acct.ID))
	switch {
	case err != nil:
		reasons = append(reasons, "could not verify the account's project access")
	case level != forge.GitLabAccessLevelDeveloper:
		reasons = append(reasons, fmt.Sprintf("the account has access level %d, expected Developer (30)", level))
	}
	if key, found, err := op.findOwnedDeployKey(ctx); err != nil {
		reasons = append(reasons, "the recorded deploy key failed its ownership check: "+err.Error())
	} else if !found {
		reasons = append(reasons, fmt.Sprintf("recorded deploy key %d is missing", op.st.DeployKeyID))
	} else if !key.CanPush {
		reasons = append(reasons, fmt.Sprintf("recorded deploy key %d cannot push, so it does not admit the trigger account on %s", key.ID, op.st.Branch))
	}
	// Install refuses to continue while the account holds a credential or
	// resource fullsend did not create, so status must not call that ready. A
	// recorded bootstrap obligation is already reported, and its own PAT would
	// otherwise be counted as foreign.
	if !op.st.Elevated && op.st.PATID == 0 && !op.st.PATPending {
		for _, check := range []func(context.Context) error{op.checkForeignPATs, op.checkForeignResources} {
			if err := check(ctx); err != nil {
				reasons = append(reasons, foreignStateReason(err))
			}
		}
	}
	if op.st.TriggerTokenID != 0 {
		tok, found, err := op.findTrigger(ctx, op.st.TriggerTokenID)
		switch {
		case err != nil:
			reasons = append(reasons, "the recorded trigger failed its ownership check: "+err.Error())
		case !found:
			reasons = append(reasons, fmt.Sprintf("recorded pipeline trigger token %d is missing", op.st.TriggerTokenID))
		case tok.OwnerID != int64(acct.ID):
			reasons = append(reasons, fmt.Sprintf("pipeline trigger token %d is not owned by the trigger account", tok.ID))
		}
	}
	if entry, err := op.opts.API.ProtectedBranchPushDeployKeyEntryID(ctx, op.opts.Owner, op.opts.Repo, op.st.Branch, op.st.DeployKeyID); err != nil {
		reasons = append(reasons, "could not read the protected-branch push entry")
	} else if entry == 0 || entry != op.st.GrantEntryID {
		reasons = append(reasons, fmt.Sprintf("the push entry recorded on %s is missing or changed", op.st.Branch))
	}
	if exact, err := op.opts.API.HasExactProtectedBranchRule(ctx, op.opts.Owner, op.opts.Repo, op.st.Branch); err != nil {
		reasons = append(reasons, "could not read the protected-branch rule")
	} else if !exact {
		reasons = append(reasons, fmt.Sprintf("branch %s no longer has an exact-name protected-branch rule", op.st.Branch))
	}
	return reasons
}
