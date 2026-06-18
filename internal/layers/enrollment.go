package layers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	shimWorkflowPath = ".github/workflows/fullsend.yaml"

	// repoMaintenanceWorkflow is the workflow file that handles enrollment.
	repoMaintenanceWorkflow = "repo-maintenance.yml"

	workflowRegistrationMaxWait = 5 * time.Minute
	workflowRegistrationPoll    = 5 * time.Second

	workflowDispatchRetryAttempts = 24
	workflowDispatchRetryInitial  = 3 * time.Second
	workflowDispatchRetryMax      = 15 * time.Second
)

// EnrollmentLayer monitors workflow-driven enrollment of target repos.
// Enrollment is performed by the repo-maintenance workflow in .fullsend,
// which creates PRs with shim workflows in response to config.yaml changes.
// This layer dispatches that workflow and reports the results.
type EnrollmentLayer struct {
	org           string
	client        forge.Client
	enabledRepos  []string
	disabledRepos []string
	ui            *ui.Printer
}

// Compile-time check that EnrollmentLayer implements Layer.
var _ Layer = (*EnrollmentLayer)(nil)

// NewEnrollmentLayer creates a new EnrollmentLayer.
func NewEnrollmentLayer(org string, client forge.Client, enabledRepos, disabledRepos []string, printer *ui.Printer) *EnrollmentLayer {
	return &EnrollmentLayer{
		org:           org,
		client:        client,
		enabledRepos:  enabledRepos,
		disabledRepos: disabledRepos,
		ui:            printer,
	}
}

func (l *EnrollmentLayer) Name() string {
	return "enrollment"
}

// RequiredScopes returns the scopes needed for the given operation.
func (l *EnrollmentLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall:
		// Enrollment dispatches repo-maintenance.yml on .fullsend.
		return []string{"repo"}
	case OpUninstall:
		if len(l.disabledRepos) > 0 {
			return []string{"repo"}
		}
		return nil
	case OpAnalyze:
		return []string{"repo"}
	default:
		return nil
	}
}

// Install dispatches the repo-maintenance workflow to handle enrollment,
// waits for it to complete, and reports any enrollment PRs created.
func (l *EnrollmentLayer) Install(ctx context.Context) error {
	if len(l.enabledRepos) == 0 && len(l.disabledRepos) == 0 {
		l.ui.StepInfo("no repositories to reconcile")
		return nil
	}

	dispatchTime := time.Now().UTC().Add(-30 * time.Second)

	l.ui.StepStart("dispatching repo-maintenance workflow for enrollment")
	if err := l.awaitWorkflowRegistration(ctx); err != nil {
		return fmt.Errorf("waiting for repo-maintenance workflow: %w", err)
	}
	dispatchErr := l.dispatchRepoMaintenanceWithRetry(ctx)
	if dispatchErr != nil {
		if !isWorkflowDispatchNotReady(dispatchErr) {
			return fmt.Errorf("dispatching repo-maintenance: %w", dispatchErr)
		}
		l.ui.StepWarn(fmt.Sprintf("workflow dispatch failed (%v); waiting for push-triggered run", dispatchErr))
	} else {
		l.ui.StepDone("dispatched repo-maintenance workflow")
	}

	// Wait for the workflow run to complete.
	run, err := l.awaitWorkflowRun(ctx, dispatchTime)
	if err != nil {
		if dispatchErr != nil {
			return fmt.Errorf("dispatching repo-maintenance: %w", dispatchErr)
		}
		l.ui.StepWarn(fmt.Sprintf("could not confirm enrollment: %v", err))
		l.ui.StepInfo("check the repo-maintenance workflow in .fullsend for results")
		return nil // non-fatal — enrollment may still succeed
	}

	if run.Conclusion == "success" {
		l.ui.StepDone("enrollment completed successfully")
	} else {
		l.ui.StepWarn(fmt.Sprintf("enrollment workflow completed with conclusion: %s", run.Conclusion))
		l.showWorkflowLogs(ctx, run.ID)
	}
	l.ui.StepInfo(fmt.Sprintf("workflow run: %s", run.HTMLURL))

	// Discover and report reconciliation PRs.
	l.reportReconciliationPRs(ctx)

	return nil
}

func (l *EnrollmentLayer) dispatchRepoMaintenanceWithRetry(ctx context.Context) error {
	delay := workflowDispatchRetryInitial
	var lastErr error

	for attempt := range workflowDispatchRetryAttempts {
		if attempt > 0 {
			l.ui.StepInfo(fmt.Sprintf("workflow dispatch not ready, retrying in %s (attempt %d/%d)", delay, attempt+1, workflowDispatchRetryAttempts))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay += workflowDispatchRetryInitial
			if delay > workflowDispatchRetryMax {
				delay = workflowDispatchRetryMax
			}
		}

		lastErr = l.client.DispatchWorkflow(ctx, l.org, forge.ConfigRepoName, repoMaintenanceWorkflow, "main", nil)
		if lastErr == nil {
			return nil
		}
		if !isWorkflowDispatchNotReady(lastErr) {
			return lastErr
		}
	}

	return lastErr
}

func (l *EnrollmentLayer) awaitWorkflowRegistration(ctx context.Context) error {
	deadline := time.Now().Add(workflowRegistrationMaxWait)
	attempt := 0

	for {
		attempt++
		wf, err := l.client.GetWorkflow(ctx, l.org, forge.ConfigRepoName, repoMaintenanceWorkflow)
		if err == nil && wf.State == "active" {
			if attempt > 1 {
				l.ui.StepInfo(fmt.Sprintf("repo-maintenance workflow registered (state: active, attempt %d)", attempt))
			}
			return nil
		}
		if err != nil && !forge.IsNotFound(err) {
			return fmt.Errorf("checking repo-maintenance workflow registration: %w", err)
		}

		if time.Now().After(deadline) {
			state := "not found"
			if wf != nil {
				state = wf.State
			}
			return fmt.Errorf("repo-maintenance workflow not ready after %s (last state: %s)", workflowRegistrationMaxWait, state)
		}

		l.ui.StepInfo(fmt.Sprintf("waiting for repo-maintenance workflow registration (attempt %d)...", attempt))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(workflowRegistrationPoll):
		}
	}
}

func isWorkflowDispatchNotReady(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *gh.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 422 {
		return false
	}
	return strings.Contains(apiErr.Message, "workflow_dispatch")
}

// awaitWorkflowRun polls for a repo-maintenance workflow run created after
// dispatchTime and waits for it to complete.
func (l *EnrollmentLayer) awaitWorkflowRun(ctx context.Context, dispatchTime time.Time) (*forge.WorkflowRun, error) {
	for attempt := range 36 { // 3 minutes max
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}

		runs, err := l.client.ListWorkflowRuns(ctx, l.org, forge.ConfigRepoName, repoMaintenanceWorkflow)
		if err != nil {
			l.ui.StepInfo(fmt.Sprintf("waiting for workflow run (attempt %d)...", attempt+1))
			continue
		}

		for i := range runs {
			run := &runs[i]
			runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
			if parseErr != nil {
				continue
			}
			if runTime.Before(dispatchTime) {
				continue
			}

			if run.Status == "completed" {
				return run, nil
			}
			l.ui.StepInfo(fmt.Sprintf("workflow run: %s (%s)", run.HTMLURL, run.Status))
			break // found our run, keep waiting
		}
	}
	return nil, fmt.Errorf("timed out waiting for repo-maintenance workflow")
}

// showWorkflowLogs fetches and displays workflow run logs locally so the user
// can diagnose enrollment failures without visiting the GitHub Actions UI.
func (l *EnrollmentLayer) showWorkflowLogs(ctx context.Context, runID int) {
	logs, err := l.client.GetWorkflowRunLogs(ctx, l.org, forge.ConfigRepoName, runID)
	if err != nil {
		l.ui.StepInfo(fmt.Sprintf("could not fetch workflow logs: %v", err))
		return
	}
	if logs != "" {
		l.ui.StepInfo("workflow logs:")
		l.ui.Raw(logs)
	}
}

// reportReconciliationPRs lists PRs on enabled and disabled repos and reports
// enrollment or removal PR URLs.
func (l *EnrollmentLayer) reportReconciliationPRs(ctx context.Context) {
	// Titles must match ENROLL_PR_TITLE and UNENROLL_PR_TITLE in
	// scripts/reconcile-repos.sh.
	for _, repo := range l.enabledRepos {
		l.reportPRByTitle(ctx, repo, "chore: connect to fullsend agent pipeline")
	}
	for _, repo := range l.disabledRepos {
		l.reportPRByTitle(ctx, repo, "chore: disconnect from fullsend agent pipeline")
	}
}

func (l *EnrollmentLayer) reportPRByTitle(ctx context.Context, repo, title string) {
	prs, err := l.client.ListRepoPullRequests(ctx, l.org, repo)
	if err != nil {
		return
	}
	for _, pr := range prs {
		if pr.Title == title {
			l.ui.PRLink(repo, pr.URL)
			break
		}
	}
}

// Uninstall updates config.yaml to mark all repos as disabled and
// dispatches the repo-maintenance workflow to create unenrollment PRs
// that remove shim workflows from enrolled repos. This runs before
// ConfigRepoLayer deletes the .fullsend repo (layers uninstall in
// reverse order), so the workflow is still available to dispatch.
//
// Errors during unenrollment are non-fatal — the user is informed but
// the uninstall continues. Repos that cannot be unenrolled
// automatically will need manual removal of .github/workflows/fullsend.yaml.
func (l *EnrollmentLayer) Uninstall(ctx context.Context) error {
	if len(l.disabledRepos) == 0 {
		l.ui.StepInfo("no repositories to unenroll")
		return nil
	}

	// Read current config and mark all repos as disabled so the
	// reconcile script knows to create unenrollment PRs.
	cfgData, err := l.client.GetFileContent(ctx, l.org, forge.ConfigRepoName, "config.yaml")
	if err != nil {
		if forge.IsNotFound(err) {
			l.ui.StepInfo("config repo unavailable, skipping unenrollment")
			return nil
		}
		l.ui.StepWarn(fmt.Sprintf("could not read config for unenrollment: %v", err))
		return nil
	}

	cfg, err := config.ParseOrgConfig(cfgData)
	if err != nil {
		l.ui.StepWarn(fmt.Sprintf("could not parse config for unenrollment: %v", err))
		return nil
	}

	for name, rc := range cfg.Repos {
		rc.Enabled = false
		cfg.Repos[name] = rc
	}

	data, err := cfg.Marshal()
	if err != nil {
		l.ui.StepWarn(fmt.Sprintf("could not marshal config for unenrollment: %v", err))
		return nil
	}

	l.ui.StepStart("Updating config to disable all repos")
	err = l.client.CreateOrUpdateFile(ctx, l.org, forge.ConfigRepoName, "config.yaml",
		"chore: disable all repos for uninstall", data)
	if err != nil {
		l.ui.StepWarn(fmt.Sprintf("could not update config: %v", err))
		return nil
	}
	l.ui.StepDone("Disabled all repos in config")

	// Dispatch repo-maintenance to create unenrollment PRs.
	dispatchTime := time.Now().UTC().Add(-30 * time.Second)
	l.ui.StepStart("Dispatching repo-maintenance for unenrollment")
	err = l.client.DispatchWorkflow(ctx, l.org, forge.ConfigRepoName, repoMaintenanceWorkflow, "main", nil)
	if err != nil {
		l.ui.StepWarn(fmt.Sprintf("could not dispatch unenrollment workflow: %v", err))
		l.ui.StepInfo("repos may need manual cleanup of .github/workflows/fullsend.yaml")
		return nil
	}
	l.ui.StepDone("Dispatched repo-maintenance for unenrollment")

	// Wait for the workflow run to complete.
	run, err := l.awaitWorkflowRun(ctx, dispatchTime)
	if err != nil {
		l.ui.StepWarn(fmt.Sprintf("could not confirm unenrollment: %v", err))
		l.ui.StepInfo("check the repo-maintenance workflow in .fullsend for results")
		return nil
	}

	if run.Conclusion == "success" {
		l.ui.StepDone("Unenrollment completed successfully")
	} else {
		l.ui.StepWarn(fmt.Sprintf("unenrollment workflow completed with conclusion: %s", run.Conclusion))
		l.showWorkflowLogs(ctx, run.ID)
	}
	l.ui.StepInfo(fmt.Sprintf("workflow run: %s", run.HTMLURL))

	l.reportReconciliationPRs(ctx)

	return nil
}

// Analyze checks which enabled repos have the shim workflow installed and
// which disabled repos still have it.
func (l *EnrollmentLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	var enrolled, notEnrolled, perRepo, guardFailed []string

	// checkGuard returns true if the repo is per-repo managed and should be
	// skipped by the per-org enrollment analysis.
	checkGuard := func(repo string) (skip bool, err error) {
		guardVal, guardExists, guardErr := l.client.GetRepoVariable(ctx, l.org, repo, forge.PerRepoGuardVar)
		if guardErr != nil {
			guardFailed = append(guardFailed, repo)
			return true, nil
		}
		if guardExists && guardVal == "true" {
			perRepo = append(perRepo, repo)
			return true, nil
		}
		return false, nil
	}

	for _, repo := range l.enabledRepos {
		if skip, _ := checkGuard(repo); skip {
			continue
		}

		_, err := l.client.GetFileContent(ctx, l.org, repo, shimWorkflowPath)
		if err == nil {
			enrolled = append(enrolled, repo)
		} else if forge.IsNotFound(err) {
			notEnrolled = append(notEnrolled, repo)
		} else {
			return nil, fmt.Errorf("checking enrollment for %s: %w", repo, err)
		}
	}

	// Check disabled repos for stale shims (skip per-repo managed repos).
	var staleShim []string
	for _, repo := range l.disabledRepos {
		if skip, _ := checkGuard(repo); skip {
			continue
		}

		_, err := l.client.GetFileContent(ctx, l.org, repo, shimWorkflowPath)
		if err == nil {
			staleShim = append(staleShim, repo)
		} else if forge.IsNotFound(err) {
			// Good — shim already removed.
		} else {
			return nil, fmt.Errorf("checking enrollment for %s: %w", repo, err)
		}
	}

	hasDrift := len(notEnrolled) > 0 || len(staleShim) > 0 || len(guardFailed) > 0

	// If every repo failed the guard check, the token likely lacks the required
	// scope — surface a prominent warning so the operator can investigate.
	totalRepos := len(l.enabledRepos) + len(l.disabledRepos)
	if totalRepos > 0 && len(guardFailed) == totalRepos {
		report.Details = append(report.Details,
			fmt.Sprintf("all %d repos failed guard check — verify your token has variables:read scope", totalRepos))
	}

	for _, r := range perRepo {
		report.Details = append(report.Details, r+" (per-repo install, skipped)")
	}
	for _, r := range guardFailed {
		report.Details = append(report.Details, r+" (guard check failed, skipped)")
	}

	switch {
	case len(l.enabledRepos) == 0 && len(l.disabledRepos) == 0:
		report.Status = StatusInstalled
		report.Details = append(report.Details, "no repositories configured")
	case hasDrift:
		if len(enrolled) == 0 && len(staleShim) == 0 && len(perRepo) == 0 && len(guardFailed) == 0 {
			report.Status = StatusNotInstalled
		} else {
			report.Status = StatusDegraded
		}
		for _, r := range enrolled {
			report.Details = append(report.Details, r+" enrolled")
		}
		for _, r := range notEnrolled {
			report.WouldInstall = append(report.WouldInstall, "create enrollment PR for "+r)
		}
		for _, r := range staleShim {
			report.WouldFix = append(report.WouldFix, "create removal PR for "+r)
		}
	default:
		report.Status = StatusInstalled
		for _, r := range enrolled {
			report.Details = append(report.Details, r+" enrolled")
		}
	}

	return report, nil
}
