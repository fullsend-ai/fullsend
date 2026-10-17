package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlablifecycle"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// maybeConvergeGitLabTriggerAccount installs, repairs, or (with rotate set and
// a ready account) rotates the trigger-only service account and its
// protected-branch resources (#8437). It holds the project lease for the whole
// transaction and passes it to the lifecycle. A deferral is reported and is not
// a failure: existing polling stays the dispatch path, and the new dispatch
// route stays inactive until the activation gate passes (#8438). The managed
// polling schedules are owned by the trigger account only while the activation
// is recorded (#8508). Clients that are not the live GitLab client (tests) have
// no trigger-account API and are skipped.
func maybeConvergeGitLabTriggerAccount(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string, rotate bool) (err error) {
	if opts.testConvergeTriggerAccount != nil {
		return opts.testConvergeTriggerAccount(ctx, client, owner, repo)
	}
	glClient, ok := client.(*gitlab.LiveClient)
	if !ok {
		return nil
	}
	repoFullName := owner + "/" + repo
	if opts.dryRun {
		printer.StepInfo(fmt.Sprintf("[%s] Would converge the GitLab trigger account", repoFullName))
		return nil
	}
	info, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("reading the default branch for the GitLab trigger account: %w", repos.SafeAPIError("reading the repository", err))
	}
	printer.StepStart(fmt.Sprintf("[%s] Converging the GitLab trigger account", repoFullName))
	lease, release, err := repos.LockGitLabProjectLease(ctx, client, owner, repo)
	if err != nil {
		printer.StepFail(fmt.Sprintf("[%s] GitLab trigger account lease unavailable", repoFullName))
		return fmt.Errorf("serializing the GitLab trigger account with other installers: %w", err)
	}
	defer release(&err)

	triggerOwner := gitlablifecycle.NewTriggerOwner(glClient)
	lifecycleOpts, err := triggerOwner.TriggerAccountOptions(ctx, owner, repo, info.DefaultBranch, lease)
	if err != nil {
		printer.StepFail(fmt.Sprintf("[%s] GitLab trigger account inventory failed", repoFullName))
		return err
	}
	lifecycleOpts.ReactivateSchedules = gitlabReactivateSchedules(ctx, opts, client, printer, owner, repo)
	var result repos.GitLabTriggerAccountResult
	diag := repos.DiagnoseGitLabTriggerAccount(ctx, client, nil, owner, repo, "")
	if rotate && diag.State == repos.GitLabTriggerStateReady {
		result, err = repos.RotateGitLabTriggerAccount(ctx, lifecycleOpts)
	} else {
		result, err = repos.ConvergeGitLabTriggerAccount(ctx, lifecycleOpts)
	}
	if err != nil {
		printer.StepFail(fmt.Sprintf("[%s] GitLab trigger account failed", repoFullName))
		// The dispatch activation was withdrawn, so the managed polling
		// schedules go back to the installer (#8508).
		return errors.Join(err, reconcileGitLabTriggerScheduleOwnership(ctx, lifecycleOpts, printer, repoFullName, false))
	}
	switch {
	case result.Deferred != "":
		printer.StepInfo(fmt.Sprintf("[%s] GitLab trigger account deferred: %s. Polling stays the dispatch path", repoFullName, result.Deferred))
		return reconcileGitLabTriggerScheduleOwnership(ctx, lifecycleOpts, printer, repoFullName, false)
	case result.Ready:
		printer.StepDone(fmt.Sprintf("[%s] GitLab trigger account ready (the dispatch route stays inactive until it is activated separately)", repoFullName))
		gate := reportGitLabTriggerDispatchGate(ctx, client, lifecycleOpts.API, printer, owner, repo, info.DefaultBranch)
		// Only a gate that just passed activates the route; a deferred gate
		// leaves the ledger without the activation (the lifecycle cleared it).
		if err := recordGitLabTriggerDispatchActivation(ctx, lifecycleOpts, printer, repoFullName, gate.Ready); err != nil {
			return err
		}
		// The managed polling schedules move to the trigger account only once
		// the activation is recorded; otherwise they stay with (or go back to)
		// the installer, so a poll job runs as the trigger account only while
		// the child-pipeline dispatch route can be selected.
		return reconcileGitLabTriggerScheduleOwnership(ctx, lifecycleOpts, printer, repoFullName, gate.Ready)
	}
	return nil
}

// gitlabReactivateSchedules reports whether the trigger account lifecycle may
// reactivate disabled managed polling schedules. --reactivate-schedules is
// honored only when the committed-template prerequisites convergeSchedules
// requires hold; otherwise disabled schedules stay disabled, including when
// the ownership stage replaces them (#8508).
func gitlabReactivateSchedules(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string) bool {
	if !opts.reactivateSchedules {
		return false
	}
	reason := repos.GitLabScheduleReactivationDeferral(ctx, client, owner, repo)
	if reason == "" {
		return true
	}
	printer.StepInfo(fmt.Sprintf("[%s] Not reactivating disabled GitLab polling schedules: %s", owner+"/"+repo, reason))
	return false
}

// repairGitLabTriggerSchedules hands managed polling schedules the trigger
// account owns back to the installer when they carry pipeline variables (#8508).
// Install runs it after the repository selection and configuration preflights
// succeed, before convergence reaches the
// pipeline-variable migration preflight, which rejects variables on any managed
// schedule and cannot remove them from an account-owned one. It takes the
// project lease for its own transaction and does nothing without a
// trigger-account ledger. Clients that are not the live GitLab client (tests)
// have no trigger-account API and are skipped.
func repairGitLabTriggerSchedules(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string) (err error) {
	if opts.testRepairTriggerSchedules != nil {
		return opts.testRepairTriggerSchedules(ctx, client, owner, repo)
	}
	glClient, ok := client.(*gitlab.LiveClient)
	if !ok || opts.dryRun {
		return nil
	}
	_, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabTriggerAccount)
	if err != nil {
		return fmt.Errorf("reading %s: %w", forge.VarGitLabTriggerAccount, repos.SafeAPIError("reading the trigger account ledger", err))
	}
	if !exists {
		return nil
	}
	repoFullName := owner + "/" + repo
	lease, release, err := repos.LockGitLabProjectLease(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("serializing the GitLab trigger account with other installers: %w", err)
	}
	defer release(&err)
	// ReactivateSchedules is deliberately not forwarded: this repair runs
	// before the typed-wrapper, input-contract, template and restriction
	// gates, so it keeps a disabled schedule disabled and leaves reactivation
	// to convergeSchedules and the ownership stage, which check them.
	lifecycleOpts := gitlablifecycle.NewTriggerOwner(glClient).ContainmentOptions(owner, repo, lease)
	res, err := repos.RepairGitLabTriggerSchedules(ctx, lifecycleOpts)
	if err != nil {
		return err
	}
	if len(res.Created) > 0 || len(res.Removed) > 0 {
		printer.StepDone(fmt.Sprintf("[%s] GitLab polling schedules owned by the trigger account that carried pipeline variables were handed back to the installer (%d created, %d removed)", repoFullName, len(res.Created), len(res.Removed)))
	}
	return nil
}

// recordGitLabTriggerDispatchActivation records (or withdraws) the dispatch
// activation. When the write fails the activation has been withdrawn as far as
// possible, so the child-pipeline route is not selected. The poll job can
// still use the protected Poller credential injected by GitLab, but every
// account-owned managed schedule goes back to the installer so its ownership
// matches the selected route. A hand-back failure is joined to the original
// error (#8508).
func recordGitLabTriggerDispatchActivation(ctx context.Context, lifecycleOpts repos.GitLabTriggerAccountOptions, printer *ui.Printer, repoFullName string, active bool) error {
	err := repos.SetGitLabTriggerDispatchActive(ctx, lifecycleOpts, active)
	if err == nil {
		return nil
	}
	printer.StepFail(fmt.Sprintf("[%s] Recording the GitLab trigger dispatch activation failed", repoFullName))
	return errors.Join(err, reconcileGitLabTriggerScheduleOwnership(ctx, lifecycleOpts, printer, repoFullName, false))
}

// reconcileGitLabTriggerScheduleOwnership moves the managed polling schedules
// to the trigger account when dispatchActive is set, or hands every
// account-owned managed schedule back to the installer otherwise (#8508).
func reconcileGitLabTriggerScheduleOwnership(ctx context.Context, lifecycleOpts repos.GitLabTriggerAccountOptions, printer *ui.Printer, repoFullName string, dispatchActive bool) error {
	res, err := repos.ReconcileGitLabTriggerScheduleOwnership(ctx, lifecycleOpts, dispatchActive)
	if err != nil {
		printer.StepFail(fmt.Sprintf("[%s] Reconciling the owner of the GitLab polling schedules failed", repoFullName))
		return err
	}
	if len(res.Created) == 0 && len(res.Removed) == 0 {
		return nil
	}
	owner := "the installer"
	if dispatchActive {
		owner = "the trigger account"
	}
	printer.StepDone(fmt.Sprintf("[%s] GitLab polling schedules are owned by %s (%d created, %d removed)", repoFullName, owner, len(res.Created), len(res.Removed)))
	return nil
}

// reportGitLabTriggerDispatchGate rechecks the trigger-dispatch activation
// gate (#8438) and reports the result. Every install path that prepares the
// trigger account reaches it, including the one taken after GitLab refuses the
// Poller protected-ref grant. A deferral is informational: it changes nothing
// and polling stays the dispatch path.
func reportGitLabTriggerDispatchGate(ctx context.Context, client forge.Client, api repos.GitLabTriggerAccountAPI, printer *ui.Printer, owner, repo, branch string) repos.GitLabTriggerDispatchGate {
	gate := repos.CheckGitLabTriggerDispatchGate(ctx, client, api, owner, repo, branch)
	printGitLabTriggerDispatchGate(printer, owner+"/"+repo, gate)
	return gate
}

// printGitLabTriggerDispatchGate prints a dispatch-gate result.
func printGitLabTriggerDispatchGate(printer *ui.Printer, repoFullName string, gate repos.GitLabTriggerDispatchGate) {
	if !gate.Ready {
		printer.StepInfo(fmt.Sprintf("[%s] GitLab trigger dispatch activation deferred: %s. Polling stays the dispatch path", repoFullName, strings.Join(gate.Deferred, "; ")))
		return
	}
	printer.StepInfo(fmt.Sprintf("[%s] GitLab trigger dispatch prerequisites verified (schedules migrated off pipeline variables, override role no_one_allowed, job-token repository push disabled, trigger account ready); the dispatch activation is recorded for pipelines the trigger account creates", repoFullName))
}

// containFailedKeys returns the owner/repo names of repos whose trigger-account
// containment failed, for repos.ConvergeConfig.ExcludeRepos.
func containFailedKeys(failed []repos.ConvergeResult) []string {
	keys := make([]string, 0, len(failed))
	for _, r := range failed {
		keys = append(keys, r.Owner+"/"+r.Repo)
	}
	return keys
}

// pollerMergeGrantRejected reports whether err is GitLab refusing the legacy
// Poller per-user protected-branch merge grant (GitLab.com Free), as opposed
// to an unreadable project or a transient failure.
func pollerMergeGrantRejected(err error) bool {
	var grantErr *repos.PollerMergeGrantError
	if !errors.As(err, &grantErr) {
		return false
	}
	var apiErr *gitlab.APIError
	if !errors.As(grantErr.Err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusBadRequest, http.StatusPaymentRequired, http.StatusForbidden, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// containGitLabTriggerAccount is the containment-only recovery that install
// runs for every targeted GitLab repository before the Poller inventory is
// read and before any other install stage can fail or skip it. Under the
// project lease it revokes recorded bootstrap tokens and restores Developer
// access for an interrupted trigger-account bootstrap, and it creates nothing.
// It does not touch the webhook fast path or any other credential. Clients
// that are not the live GitLab client (tests) have no trigger-account API and
// are skipped.
func containGitLabTriggerAccount(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string) (err error) {
	glClient, ok := client.(*gitlab.LiveClient)
	if !ok {
		return nil
	}
	repoFullName := owner + "/" + repo
	_, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabTriggerAccount)
	if err != nil {
		return fmt.Errorf("reading %s: %w", forge.VarGitLabTriggerAccount, repos.SafeAPIError("reading the trigger account ledger", err))
	}
	if !exists {
		return nil
	}
	if opts.dryRun {
		printer.StepInfo(fmt.Sprintf("[%s] Would contain an unfinished GitLab trigger account bootstrap", repoFullName))
		return nil
	}
	lease, release, err := repos.LockGitLabProjectLease(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("serializing GitLab trigger account recovery with other installers: %w", err)
	}
	defer release(&err)
	triggerOwner := gitlablifecycle.NewTriggerOwner(glClient)
	if err := repos.ContainGitLabTriggerAccount(ctx, triggerOwner.ContainmentOptions(owner, repo, lease)); err != nil {
		return fmt.Errorf("containing the GitLab trigger account: %w", err)
	}
	return nil
}

// appendGitLabTriggerAccountStatus replaces the ledger-only trigger account
// diagnosis with one that also checks the recorded resources against GitLab.
// It reports whether the trigger account now shows drift; the caller counts a
// repo once, only on its no-drift to drift transition.
func appendGitLabTriggerAccountStatus(ctx context.Context, client forge.Client, st *repos.RepoStatus) bool {
	glClient, ok := client.(*gitlab.LiveClient)
	if !ok {
		return false
	}
	info, err := client.GetRepo(ctx, st.Owner, st.Repo)
	if err != nil {
		// Without the default branch the live checks cannot run. A ledger-only
		// "ready" must not stand as if it had been verified against GitLab.
		if st.GitLabTriggerAccount != nil && st.GitLabTriggerAccount.State == repos.GitLabTriggerStateReady {
			repos.ApplyGitLabTriggerAccountStatus(st, repos.GitLabTriggerAccountStatus{
				State:   repos.GitLabTriggerStateUnreadable,
				Reasons: []string{"could not read the repository to verify the trigger account against GitLab; the ledger alone is not proof it is ready"},
			})
		}
		return hasGitLabTriggerAccountDrift(st)
	}
	api := gitlablifecycle.NewTriggerAccountAPI(glClient)
	repos.ApplyGitLabTriggerAccountStatus(st, repos.DiagnoseGitLabTriggerAccount(ctx, client, api, st.Owner, st.Repo, info.DefaultBranch))
	return hasGitLabTriggerAccountDrift(st)
}

func hasGitLabTriggerAccountDrift(st *repos.RepoStatus) bool {
	for _, d := range st.Drifts {
		if d.Field == "gitlab-trigger-account" {
			return true
		}
	}
	return false
}
