package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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
// polling schedules are owned by the trigger account in the settled state only
// while activation is recorded (#8508). An interrupted hand-back can briefly
// leave account-owned schedules after activation is withdrawn. Clients that
// are not the live GitLab client (tests) have
// no trigger-account API and are skipped.
func maybeConvergeGitLabTriggerAccount(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string, rotate, deferRetirement bool, manifest *repos.Manifest) (err error) {
	if opts.testConvergeTriggerAccount != nil {
		return opts.testConvergeTriggerAccount(ctx, client, owner, repo, rotate)
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
	lifecycleOpts.DeferRetirement = deferRetirement
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
		gate := reportGitLabTriggerDispatchGate(ctx, client, lifecycleOpts.API, printer, owner, repo, info.DefaultBranch, manifest)
		// Only a gate that just passed activates the route; a deferred gate
		// leaves the ledger without the activation (the lifecycle cleared it).
		if err := recordGitLabTriggerDispatchActivation(ctx, lifecycleOpts, printer, repoFullName, gate.Ready); err != nil {
			return err
		}
		// Once activation is recorded, managed polling schedules move to the
		// trigger account. Otherwise hand them to the installer. A failed hand-back
		// can leave account-owned schedules temporarily; the next install retries.
		return reconcileGitLabTriggerScheduleOwnership(ctx, lifecycleOpts, printer, repoFullName, gate.Ready)
	}
	return nil
}

// finishGitLabTriggerAccountRetirement runs only after webhook convergence has
// verified the current route. Earlier account triggers remain usable if the
// hook update fails, and a subsequent install can retry the cutover.
func finishGitLabTriggerAccountRetirement(ctx context.Context, opts *reposInstallConfig, client forge.Client, owner, repo string) (err error) {
	if opts.dryRun {
		return nil
	}
	glClient, ok := client.(*gitlab.LiveClient)
	if !ok {
		return nil
	}
	info, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return repos.SafeAPIError("reading the repository before trigger account retirement", err)
	}
	lease, release, err := repos.LockGitLabProjectLease(ctx, client, owner, repo)
	if err != nil {
		return err
	}
	defer release(&err)
	pending, err := repos.GitLabTriggerAccountRetirementPending(ctx, client, owner, repo)
	if err != nil || !pending {
		return err
	}
	// Verify while holding the same lease as retirement. An installer that
	// rotates between a pre-lock check and cleanup could otherwise leave the
	// hook on a generation this call is about to revoke.
	// Only an actual reference to a retired credential blocks retirement. A
	// deferred webhook fast path with no managed webhook, or a webhook on
	// other credentials, leaves nothing that retirement would break.
	inUse, err := repos.GitLabWebhookUsesRetiredTriggerCredential(ctx, client, repos.GitLabBaseURL(client), owner, repo)
	if err != nil {
		return err
	}
	if inUse {
		return errors.New("GitLab trigger account: webhook route is not verified; keeping retired trigger credentials for a later retry")
	}
	triggerOwner := gitlablifecycle.NewTriggerOwner(glClient)
	lifecycleOpts, err := triggerOwner.TriggerAccountOptions(ctx, owner, repo, info.DefaultBranch, lease)
	if err != nil {
		return err
	}
	return repos.RetireGitLabTriggerAccount(ctx, lifecycleOpts)
}

// gitlabTriggerContainTimeout bounds the containment that runs after a webhook
// owner-verification failure, on a context detached from the install's own
// cancellation so a canceled run still contains the account.
const gitlabTriggerContainTimeout = 2 * time.Minute

// convergeGitLabDispatchRoute runs the trigger-account lifecycle and the
// webhook fast path for one repository in the order that keeps the webhook on
// the right dispatch route (#8509), and returns their joined errors.
//
// Early containment withdraws the dispatch activation on every install, and
// only the lifecycle re-records it. When a trigger-account ledger already
// exists, the lifecycle therefore runs before the webhook, so the webhook is
// judged and converged against the activation the lifecycle settled instead of
// against the temporarily withdrawn Poller route, which would cut the webhook
// over to a Poller trigger or, when that defers, delete a correctly owned
// account webhook that the later refresh cannot recreate. Whether the webhook
// needs work is rechecked after the lifecycle for the same reason. Without a
// ledger (a fresh install) nothing was withdrawn, so the webhook runs first and
// the lifecycle follows once the Poller and its generations are settled.
//
// A webhook failure that says the published trigger's owner is outside the
// Developer ceiling or unverifiable contains the trigger account (demotes it,
// or revokes its published token and trigger) before returning, because the
// webhook step only removes the managed webhook.
func convergeGitLabDispatchRoute(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string, needsWebhook bool, manifest *repos.Manifest) error {
	repoFullName := owner + "/" + repo
	accountFirst := false
	if !opts.dryRun {
		_, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabTriggerAccount)
		if err != nil {
			printer.StepWarn(fmt.Sprintf("[%s] Could not read the GitLab trigger account ledger to order the install stages: %v", repoFullName, repos.SafeAPIError("reading the trigger account ledger", err)))
		}
		accountFirst = err == nil && exists
	}
	convergeAccount := func() error {
		// Trigger-only service account (#8437): after the Poller and its
		// generations are settled, so the verified Poller inventory it stays
		// separate from is current. A deferral is not a failure.
		tErr := maybeConvergeGitLabTriggerAccount(ctx, opts, client, printer, owner, repo, opts.rotateGitLabTriggerAccount, true, manifest)
		if tErr != nil {
			printer.StepWarn(fmt.Sprintf("[%s] GitLab trigger account failed: %v", repoFullName, tErr))
		}
		return tErr
	}
	var tErr error
	if accountFirst {
		tErr = convergeAccount()
		if !needsWebhook {
			// The lifecycle may have re-recorded or withdrawn the route, so
			// the webhook is probed again; a probe that cannot complete
			// reports work, like the convergence probe.
			needsWebhook, _ = repos.GitLabWebhookNeedsProvisioning(ctx, client, repos.GitLabBaseURL(client), owner, repo)
		}
	}
	// Webhook fast-path (ADR 0125): runs after the override-role step because
	// it requires no_one_allowed before a live webhook may start trigger
	// pipelines.
	if needsWebhook || opts.rotateGitLabTriggerToken {
		if err := setupGitLabWebhookFastPath(ctx, client, printer, owner, repo, opts.rotateGitLabTriggerToken, opts.dryRun); err != nil {
			printer.StepWarn(fmt.Sprintf("[%s] GitLab webhook fast-path provisioning failed: %v", repoFullName, err))
			errs := []error{tErr, err}
			if repos.GitLabWebhookTriggerOwnerUnsafe(err) {
				errs = append(errs, containGitLabTriggerAccountAfterWebhookFailure(ctx, opts, client, printer, owner, repo))
			}
			return errors.Join(errs...)
		}
	}
	if !accountFirst {
		tErr = convergeAccount()
	}
	// The trigger-account stage can activate, rotate, or withdraw the dispatch
	// route whose trigger token the webhook carries (#8509), so the webhook is
	// converged again when it no longer matches.
	wErr := refreshGitLabWebhookTriggerRoute(ctx, client, printer, owner, repo, opts.dryRun)
	errs := []error{tErr, wErr}
	if wErr != nil {
		printer.StepWarn(fmt.Sprintf("[%s] GitLab webhook fast-path provisioning failed: %v", repoFullName, wErr))
		// The refresh can reach the account route after the lifecycle activated
		// it, so an owner-safety failure there needs the same containment as the
		// initial webhook step: the webhook step only removes the webhook.
		if repos.GitLabWebhookTriggerOwnerUnsafe(wErr) {
			errs = append(errs, containGitLabTriggerAccountAfterWebhookFailure(ctx, opts, client, printer, owner, repo))
		}
	}
	if tErr == nil && wErr == nil {
		errs = append(errs, finishGitLabTriggerAccountRetirement(ctx, opts, client, owner, repo))
	}
	return errors.Join(errs...)
}

// containGitLabTriggerAccountAfterWebhookFailure runs the ownership-verified
// trigger-account containment on a bounded context detached from ctx's
// cancellation, and reports a failure.
func containGitLabTriggerAccountAfterWebhookFailure(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitlabTriggerContainTimeout)
	defer cancel()
	var err error
	if opts.testContainTriggerAccount != nil {
		err = opts.testContainTriggerAccount(cctx, client, owner, repo)
	} else {
		err = containGitLabTriggerAccount(cctx, opts, client, printer, owner, repo)
	}
	if err != nil {
		printer.StepWarn(fmt.Sprintf("[%s/%s] Containing the GitLab trigger account after the webhook failure failed: %v", owner, repo, err))
		return fmt.Errorf("containing the GitLab trigger account after the webhook failure: %w", err)
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
	return reportGitLabTriggerScheduleReconcile(printer, repoFullName, res, err, dispatchActive)
}

// reportGitLabTriggerScheduleReconcile prints the outcome of a schedule
// ownership run and returns its error unchanged.
func reportGitLabTriggerScheduleReconcile(printer *ui.Printer, repoFullName string, res repos.GitLabTriggerSchedulesResult, err error, dispatchActive bool) error {
	if err != nil {
		printer.StepFail(fmt.Sprintf("[%s] Reconciling the owner of the GitLab polling schedules failed", repoFullName))
		// An interrupted transfer (for example a failed deletion of the
		// installer-owned originals) can leave account-owned schedules behind.
		// A retry keeps them in sync and creates nothing, so warn now.
		if dispatchActive && len(res.Created) > 0 {
			warnGitLabTriggerScheduleMinInstallerCLI(printer, repoFullName)
		}
		return err
	}
	printGitLabTriggerScheduleOwnership(printer, repoFullName, res, dispatchActive)
	return nil
}

// gitlabTriggerScheduleMinInstallerCLI names the oldest CLI that accepts
// trigger-account-owned managed schedules in repos install and repos
// uninstall (#8508).
const gitlabTriggerScheduleMinInstallerCLI = "the first Fullsend release or commit containing fullsend-ai/fullsend#8508"

// printGitLabTriggerScheduleOwnership reports a schedule-ownership run. When
// this run transferred schedules to the trigger account it also warns that
// older installer CLIs now refuse the project (#8543).
func printGitLabTriggerScheduleOwnership(printer *ui.Printer, repoFullName string, res repos.GitLabTriggerSchedulesResult, dispatchActive bool) {
	if len(res.Created) == 0 && len(res.Removed) == 0 {
		return
	}
	owner := "the installer"
	if dispatchActive {
		owner = "the trigger account"
	}
	printer.StepDone(fmt.Sprintf("[%s] GitLab polling schedules are owned by %s (%d created, %d removed)", repoFullName, owner, len(res.Created), len(res.Removed)))
	if dispatchActive && len(res.Created) > 0 {
		warnGitLabTriggerScheduleMinInstallerCLI(printer, repoFullName)
	}
}

// warnGitLabTriggerScheduleMinInstallerCLI warns that trigger-account-owned
// schedules exist, so older installer CLIs refuse the project (#8543).
func warnGitLabTriggerScheduleMinInstallerCLI(printer *ui.Printer, repoFullName string) {
	printer.StepWarn(fmt.Sprintf("[%s] repos install and repos uninstall for this project now require %s (this CLI, %s, qualifies): earlier CLIs treat the trigger-account-owned schedules as foreign and refuse to proceed. Upgrade every CLI that installs or uninstalls this project before its next run; to return to an older CLI, first use a current CLI to hand the schedules back to the installer. See the GitLab configuration guide", repoFullName, gitlabTriggerScheduleMinInstallerCLI, version))
}

// reportGitLabTriggerDispatchGate rechecks the trigger-dispatch activation
// gate (#8438) and reports the result. Every install path that prepares the
// trigger account reaches it, including the one taken after GitLab refuses the
// Poller protected-ref grant. A deferral is informational: it changes nothing
// and polling stays the dispatch path.
func reportGitLabTriggerDispatchGate(ctx context.Context, client forge.Client, api repos.GitLabTriggerAccountAPI, printer *ui.Printer, owner, repo, branch string, manifest *repos.Manifest) repos.GitLabTriggerDispatchGate {
	gate := repos.CheckGitLabTriggerDispatchGate(ctx, client, api, owner, repo, branch, manifest)
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
	if len(gate.UnverifiedInheritedScopes) > 0 {
		printer.StepInfo(fmt.Sprintf("[%s] Unverified inherited CI/CD variable scopes (the installing credential cannot read them): %s. An override there of an execution-controlling variable such as PATH, LD_PRELOAD, BASH_ENV, or GIT_TEMPLATE_DIR would run before the job verifies its creator, with the trigger account's credentials; fullsend trusts instance administrators and group Owners not to set one. See the inherited CI/CD variable trust section of the GitLab configuration guide", repoFullName, strings.Join(gate.UnverifiedInheritedScopes, ", ")))
	}
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
