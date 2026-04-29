package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const codeownersPath = "CODEOWNERS"

const syncBranch = "fullsend/sync-scaffold"
const syncPRTitle = "chore: sync scaffold files"
const syncPRBody = `Automated scaffold sync by "fullsend install".

This PR updates scaffold configuration files to the latest version.

> **Note:** Workflows will not execute until this PR is merged.`

// managedFiles lists every file this layer manages.
// Populated at init from the scaffold plus the CODEOWNERS sentinel.
var managedFiles []string

func init() {
	if err := scaffold.WalkFullsendRepo(func(path string, _ []byte) error {
		managedFiles = append(managedFiles, path)
		return nil
	}); err != nil {
		panic(fmt.Sprintf("walking scaffold: %v", err))
	}
	managedFiles = append(managedFiles, codeownersPath)
}

// WorkflowsLayer manages workflow files and CODEOWNERS in the .fullsend
// config repo. It writes the reusable agent dispatch workflow, the repo
// onboarding workflow, and a CODEOWNERS file that grants the installing
// user ownership of all config-repo contents.
type WorkflowsLayer struct {
	org               string
	client            forge.Client
	ui                *ui.Printer
	authenticatedUser string
}

// Compile-time check that WorkflowsLayer implements Layer.
var _ Layer = (*WorkflowsLayer)(nil)

// NewWorkflowsLayer creates a new WorkflowsLayer.
// user is the authenticated user who will own CODEOWNERS entries.
func NewWorkflowsLayer(org string, client forge.Client, printer *ui.Printer, user string) *WorkflowsLayer {
	return &WorkflowsLayer{
		org:               org,
		client:            client,
		ui:                printer,
		authenticatedUser: user,
	}
}

func (l *WorkflowsLayer) Name() string {
	return "workflows"
}

// RequiredScopes returns the scopes needed for the given operation.
func (l *WorkflowsLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall:
		// Writing to .github/workflows/ paths requires the workflow scope.
		// Without it, GitHub returns 404 (not 403), which is deeply confusing.
		return []string{"repo", "workflow"}
	case OpUninstall:
		return nil // no-op
	case OpAnalyze:
		return []string{"repo"}
	default:
		return nil
	}
}

// Install writes the workflow files and CODEOWNERS to the .fullsend repo
// via a pull request. All files are committed atomically to a sync branch,
// then a PR is created (or an existing one is reused).
func (l *WorkflowsLayer) Install(ctx context.Context) error {
	files, err := scaffold.CollectTreeFiles()
	if err != nil {
		return fmt.Errorf("collecting scaffold files: %w", err)
	}

	files = append(files, forge.TreeFile{
		Path:    codeownersPath,
		Content: []byte(l.codeownersContent()),
		Mode:    "100644",
	})

	if err := l.client.CreateBranch(ctx, l.org, forge.ConfigRepoName, syncBranch); err != nil && !forge.IsAlreadyExists(err) {
		return fmt.Errorf("creating sync branch: %w", err)
	}

	l.ui.StepStart(fmt.Sprintf("Syncing %d scaffold files", len(files)))
	if err := l.client.SyncFiles(ctx, l.org, forge.ConfigRepoName, syncBranch, "chore: sync scaffold files", files); err != nil {
		l.ui.StepFail("Failed to sync scaffold files")
		return fmt.Errorf("syncing scaffold files: %w", err)
	}
	l.ui.StepDone(fmt.Sprintf("Synced %d scaffold files", len(files)))

	pr, err := l.createOrUpdatePR(ctx)
	if err != nil {
		l.ui.StepWarn("Files synced but could not create PR: " + err.Error())
		return nil
	}
	l.ui.StepDone("PR: " + pr.URL)

	return nil
}

func (l *WorkflowsLayer) createOrUpdatePR(ctx context.Context) (*forge.ChangeProposal, error) {
	prs, err := l.client.ListRepoPullRequests(ctx, l.org, forge.ConfigRepoName)
	if err != nil {
		return nil, fmt.Errorf("listing pull requests: %w", err)
	}
	for i := range prs {
		if prs[i].Head == syncBranch {
			_ = l.client.UpdateChangeProposal(ctx, l.org, forge.ConfigRepoName, prs[i].Number, syncPRTitle, syncPRBody)
			return &prs[i], nil
		}
	}

	repo, err := l.client.GetRepo(ctx, l.org, forge.ConfigRepoName)
	if err != nil {
		return nil, fmt.Errorf("getting repo: %w", err)
	}

	return l.client.CreateChangeProposal(ctx, l.org, forge.ConfigRepoName,
		syncPRTitle, syncPRBody, syncBranch, repo.DefaultBranch)
}

// Uninstall is a no-op. Workflow files are removed when the config repo
// is deleted by the ConfigRepoLayer.
func (l *WorkflowsLayer) Uninstall(_ context.Context) error {
	return nil
}

// Analyze checks which managed files exist in the config repo.
func (l *WorkflowsLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	var present, missing []string
	for _, path := range managedFiles {
		_, err := l.client.GetFileContent(ctx, l.org, forge.ConfigRepoName, path)
		if err != nil {
			if forge.IsNotFound(err) {
				missing = append(missing, path)
				continue
			}
			return nil, fmt.Errorf("checking %s: %w", path, err)
		}
		present = append(present, path)
	}

	switch {
	case len(missing) == 0:
		report.Status = StatusInstalled
		for _, p := range present {
			report.Details = append(report.Details, p+" exists")
		}
	case len(present) == 0:
		report.Status = StatusNotInstalled
		for _, m := range missing {
			report.WouldInstall = append(report.WouldInstall, "write "+m)
		}
	default:
		report.Status = StatusDegraded
		for _, p := range present {
			report.Details = append(report.Details, p+" exists")
		}
		for _, m := range missing {
			report.WouldFix = append(report.WouldFix, "write "+m)
		}
	}

	return report, nil
}

func (l *WorkflowsLayer) codeownersContent() string {
	return fmt.Sprintf("# fullsend configuration is governed by org admins.\n* @%s\n", l.authenticatedUser)
}
