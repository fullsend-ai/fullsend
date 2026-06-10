package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const codeownersPath = "CODEOWNERS"

// WorkflowsLayer manages workflow files and CODEOWNERS in the .fullsend
// config repo.
type WorkflowsLayer struct {
	org               string
	client            forge.Client
	ui                *ui.Printer
	authenticatedUser string
	version           string
	vendored          bool
}

var _ Layer = (*WorkflowsLayer)(nil)

// NewWorkflowsLayer creates a new WorkflowsLayer.
func NewWorkflowsLayer(org string, client forge.Client, printer *ui.Printer, user, version string, vendored bool) *WorkflowsLayer {
	return &WorkflowsLayer{
		org:               org,
		client:            client,
		ui:                printer,
		authenticatedUser: user,
		version:           version,
		vendored:          vendored,
	}
}

func (l *WorkflowsLayer) Name() string { return "workflows" }

func (l *WorkflowsLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall:
		return []string{"repo", "workflow"}
	case OpUninstall:
		return nil
	case OpAnalyze:
		return []string{"repo"}
	default:
		return nil
	}
}

func (l *WorkflowsLayer) Install(ctx context.Context) error {
	installFiles, err := scaffold.CollectInstallFiles(scaffold.CollectInstallFilesOptions{
		RenderOptions: scaffold.RenderOptionsForInstall(l.vendored, false),
		PathPrefix:    "",
	})
	if err != nil {
		return fmt.Errorf("collecting scaffold files: %w", err)
	}

	var files []forge.TreeFile
	for _, f := range installFiles {
		files = append(files, forge.TreeFile{
			Path:    f.Path,
			Content: f.Content,
			Mode:    f.Mode,
		})
	}

	files = append(files, forge.TreeFile{
		Path:    codeownersPath,
		Content: []byte(l.codeownersContent()),
		Mode:    "100644",
	})

	l.ui.StepStart("Writing scaffold files")
	committed, err := l.client.CommitFiles(ctx, l.org, forge.ConfigRepoName,
		fmt.Sprintf("chore: update fullsend-%s scaffold", l.version), files)
	if err != nil {
		l.ui.StepFail("Failed to write scaffold files")
		return fmt.Errorf("committing scaffold files: %w", err)
	}
	if committed {
		l.ui.StepDone(fmt.Sprintf("Wrote %d files", len(files)))
	} else {
		l.ui.StepDone("Scaffold up to date")
	}

	return nil
}

func (l *WorkflowsLayer) Uninstall(_ context.Context) error { return nil }

func (l *WorkflowsLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	managed, err := scaffold.ManagedPaths(false, "")
	if err != nil {
		return nil, err
	}
	managed = append(managed, codeownersPath)

	var present, missing []string
	for _, path := range managed {
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
