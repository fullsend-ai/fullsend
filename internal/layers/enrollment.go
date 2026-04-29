package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const shimWorkflowPath = ".github/workflows/fullsend.yaml"

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
		return nil // no-op
	case OpAnalyze:
		return []string{"repo"}
	default:
		return nil
	}
}

// Install records the enrollment configuration. The actual enrollment
// happens when the scaffold PR is merged — the repo-maintenance workflow
// triggers automatically and creates enrollment PRs for each target repo.
func (l *EnrollmentLayer) Install(ctx context.Context) error {
	if len(l.enabledRepos) == 0 && len(l.disabledRepos) == 0 {
		l.ui.StepInfo("no repositories to reconcile")
		return nil
	}

	for _, repo := range l.enabledRepos {
		l.ui.StepInfo(fmt.Sprintf("%s will be enrolled when the scaffold PR is merged", repo))
	}
	for _, repo := range l.disabledRepos {
		l.ui.StepInfo(fmt.Sprintf("%s will be unenrolled when the scaffold PR is merged", repo))
	}

	// Report any existing reconciliation PRs from prior runs.
	l.reportReconciliationPRs(ctx)

	return nil
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

// Uninstall is a no-op. Individual repo cleanup is not automated —
// repos keep their shim workflows.
func (l *EnrollmentLayer) Uninstall(_ context.Context) error {
	return nil
}

// Analyze checks which enabled repos have the shim workflow installed and
// which disabled repos still have it.
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

	// Check disabled repos for stale shims.
	var staleShim []string
	for _, repo := range l.disabledRepos {
		_, err := l.client.GetFileContent(ctx, l.org, repo, shimWorkflowPath)
		if err == nil {
			staleShim = append(staleShim, repo)
		} else if forge.IsNotFound(err) {
			// Good — shim already removed.
		} else {
			return nil, fmt.Errorf("checking enrollment for %s: %w", repo, err)
		}
	}

	hasDrift := len(notEnrolled) > 0 || len(staleShim) > 0

	switch {
	case len(l.enabledRepos) == 0 && len(l.disabledRepos) == 0:
		report.Status = StatusInstalled
		report.Details = append(report.Details, "no repositories configured")
	case hasDrift:
		if len(enrolled) == 0 && len(staleShim) == 0 {
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
