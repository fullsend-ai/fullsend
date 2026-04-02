package layers

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	shimWorkflowPath = ".github/workflows/fullsend.yaml"
	enrollBranch     = "fullsend/onboard"
)

// EnrollmentLayer manages repo enrollment in the fullsend pipeline.
// It creates PRs with shim workflow files that route events to the
// reusable agent dispatch workflow in the .fullsend config repo.
type EnrollmentLayer struct {
	org             string
	client          forge.Client
	enabledRepos    []string
	defaultBranches map[string]string
	ui              *ui.Printer
}

// Compile-time check that EnrollmentLayer implements Layer.
var _ Layer = (*EnrollmentLayer)(nil)

// NewEnrollmentLayer creates a new EnrollmentLayer.
func NewEnrollmentLayer(org string, client forge.Client, enabledRepos []string, defaultBranches map[string]string, printer *ui.Printer) *EnrollmentLayer {
	return &EnrollmentLayer{
		org:             org,
		client:          client,
		enabledRepos:    enabledRepos,
		defaultBranches: defaultBranches,
		ui:              printer,
	}
}

func (l *EnrollmentLayer) Name() string {
	return "enrollment"
}

// Install creates enrollment PRs for enabled repos that are not yet enrolled.
// Failures on individual repos are warned and skipped — install does not stop.
func (l *EnrollmentLayer) Install(ctx context.Context) error {
	for _, repo := range l.enabledRepos {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("cancelled during enrollment: %w", err)
		}

		if err := l.enrollRepo(ctx, repo); err != nil {
			l.ui.StepWarn(fmt.Sprintf("Failed to enroll %s: %s", repo, err))
		}
	}
	return nil
}

// enrollRepo creates an enrollment PR for a single repo.
func (l *EnrollmentLayer) enrollRepo(ctx context.Context, repo string) error {
	// Check if already enrolled
	_, err := l.client.GetFileContent(ctx, l.org, repo, shimWorkflowPath)
	if err == nil {
		l.ui.StepInfo(fmt.Sprintf("%s already enrolled", repo))
		return nil
	}
	if !forge.IsNotFound(err) {
		return fmt.Errorf("checking enrollment status for %s: %w", repo, err)
	}

	l.ui.StepStart(fmt.Sprintf("Enrolling %s", repo))

	// Create branch for the enrollment PR
	if err := l.client.CreateBranch(ctx, l.org, repo, enrollBranch); err != nil {
		return fmt.Errorf("creating branch: %w", err)
	}

	// Write shim workflow to the branch
	content := l.shimWorkflowContent()
	if err := l.client.CreateFileOnBranch(ctx, l.org, repo, enrollBranch, shimWorkflowPath,
		"chore: add fullsend shim workflow", []byte(content)); err != nil {
		return fmt.Errorf("writing shim workflow: %w", err)
	}

	// Create enrollment PR
	baseBranch := l.defaultBranches[repo]
	if baseBranch == "" {
		baseBranch = "main"
	}

	pr, err := l.client.CreateChangeProposal(ctx, l.org, repo,
		"Connect to fullsend agent pipeline",
		"This PR adds a shim workflow that routes repository events to the "+
			"fullsend agent dispatch workflow in the `.fullsend` config repo.\n\n"+
			"Once merged, issues, PRs, and comments in this repo will be handled "+
			"by the fullsend agent pipeline.",
		enrollBranch,
		baseBranch,
	)
	if err != nil {
		return fmt.Errorf("creating PR: %w", err)
	}

	l.ui.StepDone(fmt.Sprintf("Created enrollment PR for %s", repo))
	l.ui.PRLink(repo, pr.URL)
	return nil
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
	case len(notEnrolled) == 0 && len(enrolled) > 0:
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

// shimWorkflowContent returns the shim workflow YAML with the org name substituted.
func (l *EnrollmentLayer) shimWorkflowContent() string {
	tmpl := `# fullsend shim workflow
# Routes events to the reusable agent dispatch workflow in .fullsend.
name: fullsend

on:
  issues:
    types: [opened, edited, labeled]
  issue_comment:
    types: [created]
  pull_request:
    types: [opened, synchronize, ready_for_review]
  pull_request_review:
    types: [submitted]

jobs:
  dispatch:
    uses: {org}/.fullsend/.github/workflows/agent.yaml@main
    with:
      event_type: ${{ github.event_name }}
      event_payload: ${{ toJSON(github.event) }}
    secrets:
      APP_PRIVATE_KEY: ${{ secrets.FULLSEND_FULLSEND_APP_PRIVATE_KEY }}
`
	return strings.ReplaceAll(tmpl, "{org}", l.org)
}
