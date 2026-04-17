package layers

import (
	"context"
	"fmt"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	shimWorkflowPath = ".github/workflows/fullsend.yaml"

	// repoMaintenanceWorkflow is the workflow file that handles enrollment.
	repoMaintenanceWorkflow = "repo-maintenance.yml"
)

// EnrollmentLayer monitors workflow-driven enrollment of target repos.
// Enrollment is performed by the repo-maintenance workflow in .fullsend,
// which creates PRs with shim workflows in response to config.yaml changes.
// This layer dispatches that workflow and reports the results.
type EnrollmentLayer struct {
	org          string
	client       forge.Client
	enabledRepos []string
	ui           *ui.Printer
}

// Compile-time check that EnrollmentLayer implements Layer.
var _ Layer = (*EnrollmentLayer)(nil)

// NewEnrollmentLayer creates a new EnrollmentLayer.
func NewEnrollmentLayer(org string, client forge.Client, enabledRepos []string, printer *ui.Printer) *EnrollmentLayer {
	return &EnrollmentLayer{
		org:          org,
		client:       client,
		enabledRepos: enabledRepos,
		ui:           printer,
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
		return nil // no-op
	case OpAnalyze:
		return []string{"repo"}
	default:
		return nil
	}
}

// Install dispatches the repo-maintenance workflow to handle enrollment,
// waits for it to complete, and reports any enrollment PRs created.
func (l *EnrollmentLayer) Install(ctx context.Context) error {
	if len(l.enabledRepos) == 0 {
		l.ui.StepInfo("no repositories to enroll")
		return nil
	}

	dispatchTime := time.Now().UTC().Add(-30 * time.Second)

	l.ui.StepStart("dispatching repo-maintenance workflow for enrollment")
	err := l.client.DispatchWorkflow(ctx, l.org, forge.ConfigRepoName, repoMaintenanceWorkflow, "main", nil)
	if err != nil {
		return fmt.Errorf("dispatching repo-maintenance: %w", err)
	}
	l.ui.StepDone("dispatched repo-maintenance workflow")

	// Wait for the workflow run to complete.
	run, err := l.awaitWorkflowRun(ctx, dispatchTime)
	if err != nil {
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

	// Discover and report enrollment PRs.
	l.reportEnrollmentPRs(ctx)

	return nil
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
			l.ui.StepInfo(fmt.Sprintf("workflow run %d: %s", run.ID, run.Status))
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

// reportEnrollmentPRs lists PRs on each enabled repo and reports enrollment PR URLs.
func (l *EnrollmentLayer) reportEnrollmentPRs(ctx context.Context) {
	for _, repo := range l.enabledRepos {
		prs, err := l.client.ListRepoPullRequests(ctx, l.org, repo)
		if err != nil {
			continue
		}
		for _, pr := range prs {
			// Must match PR_TITLE in scripts/enroll-repos.sh.
			if pr.Title == "Connect to fullsend agent pipeline" {
				l.ui.PRLink(repo, pr.URL)
				break
			}
		}
	}
}

// Uninstall is a no-op. Individual repo cleanup is not automated —
// repos keep their shim workflows.
func (l *EnrollmentLayer) Uninstall(_ context.Context) error {
	return nil
}

// Analyze checks which enabled repos have the shim workflow installed.
func (l *EnrollmentLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	var enrolled, notEnrolled []string
	for _, repo := range l.enabledRepos {
		_, err := l.client.GetFileContent(ctx, l.org, repo, shimWorkflowPath)
		if err == nil {
			enrolled = append(enrolled, repo)
		} else if forge.IsNotFound(err) {
			notEnrolled = append(notEnrolled, repo)
		} else {
			return nil, fmt.Errorf("checking enrollment for %s: %w", repo, err)
		}
	}

	switch {
	case len(l.enabledRepos) == 0:
		report.Status = StatusInstalled
		report.Details = append(report.Details, "no repositories enrolled")
	case len(notEnrolled) == 0:
		report.Status = StatusInstalled
		for _, r := range enrolled {
			report.Details = append(report.Details, r+" enrolled")
		}
	case len(enrolled) == 0:
		report.Status = StatusNotInstalled
		for _, r := range notEnrolled {
			report.WouldInstall = append(report.WouldInstall, "create enrollment PR for "+r)
		}
	default:
		report.Status = StatusDegraded
		for _, r := range enrolled {
			report.Details = append(report.Details, r+" enrolled")
		}
		for _, r := range notEnrolled {
			report.WouldFix = append(report.WouldFix, "create enrollment PR for "+r)
		}
	}

	return report, nil
}
