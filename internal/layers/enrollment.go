package layers

import (
	"context"
	"fmt"

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

// RequiredScopes returns the scopes needed for the given operation.
func (l *EnrollmentLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall:
		// Enrollment writes .github/workflows/fullsend.yaml to target repos
		// and creates PRs. The workflow scope is needed for the workflow file.
		return []string{"repo", "workflow"}
	case OpUninstall:
		return nil // no-op
	case OpAnalyze:
		return []string{"repo"}
	default:
		return nil
	}
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

// enrollRepo creates an enrollment PR for a single repo, or updates the
// shim workflow on an existing enrollment branch if a PR already exists.
// Idempotent: skips repos that already have the shim workflow merged on
// the default branch.
func (l *EnrollmentLayer) enrollRepo(ctx context.Context, repo string) error {
	// Check if already enrolled (shim workflow on default branch).
	_, err := l.client.GetFileContent(ctx, l.org, repo, shimWorkflowPath)
	if err == nil {
		l.ui.StepInfo(fmt.Sprintf("%s already enrolled", repo))
		return nil
	}

	// Check if there's already an open enrollment PR from a previous run.
	// If so, update the shim workflow on the branch to reflect the latest
	// content (e.g., security model changes) rather than skipping.
	prs, err := l.client.ListRepoPullRequests(ctx, l.org, repo)
	if err == nil {
		for _, pr := range prs {
			if pr.Title == "Connect to fullsend agent pipeline" {
				return l.updateExistingEnrollment(ctx, repo, pr)
			}
		}
	}

	l.ui.StepStart(fmt.Sprintf("Enrolling %s", repo))

	// Create branch for the enrollment PR.
	// Idempotent: if the branch exists from a previous partial run, proceed.
	if err := l.client.CreateBranch(ctx, l.org, repo, enrollBranch); err != nil {
		if !forge.IsNotFound(err) {
			l.ui.StepInfo(fmt.Sprintf("Branch %s may already exist, continuing", enrollBranch))
		}
	}

	// Write shim workflow to the branch using upsert to handle re-runs
	// where the branch exists with an old version of the file.
	content := l.shimWorkflowContent()
	if err := l.client.CreateOrUpdateFileOnBranch(ctx, l.org, repo, enrollBranch, shimWorkflowPath,
		"chore: add fullsend shim workflow", []byte(content)); err != nil {
		return fmt.Errorf("writing shim workflow: %w", err)
	}

	// Create enrollment PR.
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

// updateExistingEnrollment updates the shim workflow on an existing
// enrollment branch so the PR always reflects the latest content.
func (l *EnrollmentLayer) updateExistingEnrollment(ctx context.Context, repo string, pr forge.ChangeProposal) error {
	l.ui.StepStart(fmt.Sprintf("Updating shim workflow on %s", repo))

	content := l.shimWorkflowContent()
	if err := l.client.CreateOrUpdateFileOnBranch(ctx, l.org, repo, enrollBranch, shimWorkflowPath,
		"chore: update fullsend shim workflow", []byte(content)); err != nil {
		return fmt.Errorf("updating shim workflow: %w", err)
	}

	l.ui.StepDone(fmt.Sprintf("Updated enrollment PR for %s", repo))
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
		} else {
			notEnrolled = append(notEnrolled, repo)
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

// shimWorkflowContent returns the shim workflow YAML.
// Uses github.repository_owner so the content is org-agnostic.
func (l *EnrollmentLayer) shimWorkflowContent() string {
	return `# fullsend shim workflow
# Routes events to the agent dispatch workflow in .fullsend.
#
# Security: pull_request_target runs the BASE branch version of this workflow,
# preventing PRs from modifying it to exfiltrate the dispatch token.
# This shim never checks out PR code, so it is not vulnerable to "pwn request"
# attacks (see: Trivy CVE-2026-33634, hackerbot-claw campaign).
name: fullsend

on:
  issues:
    types: [opened, edited, labeled]
  issue_comment:
    types: [created]
  pull_request_target:
    types: [opened, synchronize, ready_for_review]
  pull_request_review:
    types: [submitted]

jobs:
  dispatch:
    runs-on: ubuntu-latest
    steps:
      - name: Dispatch to fullsend
        env:
          GH_TOKEN: ${{ secrets.FULLSEND_DISPATCH_TOKEN }}
        run: |
          gh workflow run agent.yaml \
            --repo "${{ github.repository_owner }}/.fullsend" \
            --field event_type="${{ github.event_name }}" \
            --field source_repo="${{ github.repository }}" \
            --field event_payload='${{ toJSON(github.event) }}'
`
}
