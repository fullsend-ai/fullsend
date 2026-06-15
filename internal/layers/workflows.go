package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const codeownersPath = "CODEOWNERS"

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
	for _, dir := range scaffold.CustomizedDirs() {
		managedFiles = append(managedFiles, dir+"/.gitkeep")
	}
	managedFiles = append(managedFiles, codeownersPath)
}

// WorkflowsLayer manages workflow files and CODEOWNERS in the .fullsend
// config repo. It writes the thin caller workflows, composite actions,
// and a CODEOWNERS file that grants the installing user ownership of all
// config-repo contents.
type WorkflowsLayer struct {
	org               string
	client            forge.Client
	ui                *ui.Printer
	authenticatedUser string
	version           string
}

// Compile-time check that WorkflowsLayer implements Layer.
var _ Layer = (*WorkflowsLayer)(nil)

// NewWorkflowsLayer creates a new WorkflowsLayer.
// user is the authenticated user who will own CODEOWNERS entries.
// version is the fullsend CLI version that generated the scaffold.
func NewWorkflowsLayer(org string, client forge.Client, printer *ui.Printer, user, version string) *WorkflowsLayer {
	return &WorkflowsLayer{
		org:               org,
		client:            client,
		ui:                printer,
		authenticatedUser: user,
		version:           version,
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
// in a single atomic commit using the Git Trees API. If all files already
// match the current tree, no commit is created (idempotent).
func (l *WorkflowsLayer) Install(ctx context.Context) error {
	var files []forge.TreeFile
	err := scaffold.WalkFullsendRepo(func(path string, content []byte) error {
		files = append(files, forge.TreeFile{
			Path:    path,
			Content: scaffold.PrependManagedHeader(path, content),
			Mode:    scaffold.FileMode(path),
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("collecting scaffold files: %w", err)
	}

	for _, dir := range scaffold.CustomizedDirs() {
		files = append(files, forge.TreeFile{
			Path:    dir + "/.gitkeep",
			Content: []byte(""),
			Mode:    "100644",
		})
	}

	files = append(files, forge.TreeFile{
		Path:    codeownersPath,
		Content: []byte(l.codeownersContent()),
		Mode:    "100644",
	})

	cfgRepo, err := l.client.GetRepo(ctx, l.org, forge.ConfigRepoName)
	if err != nil {
		return fmt.Errorf("getting config repo info: %w", err)
	}
	l.ui.StepStart(fmt.Sprintf("Committing scaffold files to %s/%s (%s branch)",
		l.org, forge.ConfigRepoName, cfgRepo.DefaultBranch))
	commitMsg := fmt.Sprintf("chore: update fullsend-%s scaffold", l.version)
	prTitle := "chore: add fullsend scaffold files"
	prBody := fmt.Sprintf("This PR adds the fullsend scaffold files to the %s config repo.\n\n"+
		"The default branch (%s) has branch protection rules that prevent direct pushes, "+
		"so these files are delivered via PR instead.\n\n"+
		"Merge this PR to activate fullsend workflows.", forge.ConfigRepoName, cfgRepo.DefaultBranch)
	return CommitScaffoldFiles(ctx, l.client, l.ui,
		l.org, forge.ConfigRepoName, cfgRepo.DefaultBranch,
		commitMsg, prTitle, prBody, files)
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
