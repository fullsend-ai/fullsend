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
	vendorCollect     VendorCollectFunc
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

// WithVendorCollect configures combined scaffold+vendor commits for --vendor installs.
func (l *WorkflowsLayer) WithVendorCollect(fn VendorCollectFunc) *WorkflowsLayer {
	l.vendorCollect = fn
	return l
}

func (l *WorkflowsLayer) Name() string { return "workflows" }

func (l *WorkflowsLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall:
		// Writing to .github/workflows/ paths requires the workflow scope.
		// Without it, GitHub returns 404 (not 403), which is deeply confusing.
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

	vendorAssetCount := 0
	if l.vendored && l.vendorCollect != nil {
		vendorFiles, count, err := l.vendorCollect(ctx, l.ui, l.org, forge.ConfigRepoName)
		if err != nil {
			return fmt.Errorf("collecting vendored assets: %w", err)
		}
		files = append(files, vendorFiles...)
		vendorAssetCount = count
	}

	cfgRepo, err := l.client.GetRepo(ctx, l.org, forge.ConfigRepoName)
	if err != nil {
		return fmt.Errorf("getting config repo info: %w", err)
	}

	commitMsg := fmt.Sprintf("chore: update fullsend-%s scaffold", l.version)
	if vendorAssetCount > 0 {
		commitMsg = fmt.Sprintf("chore: update fullsend-%s scaffold with vendored assets", l.version)
		l.ui.StepStart(fmt.Sprintf("Writing scaffold and vendored assets (%d content files) to %s/%s (%s branch)",
			vendorAssetCount, l.org, forge.ConfigRepoName, cfgRepo.DefaultBranch))
	} else {
		l.ui.StepStart(fmt.Sprintf("Committing scaffold files to %s/%s (%s branch)",
			l.org, forge.ConfigRepoName, cfgRepo.DefaultBranch))
	}
	committed, err := l.client.CommitFiles(ctx, l.org, forge.ConfigRepoName, commitMsg, files)
	if err != nil {
		l.ui.StepFail("Failed to commit scaffold files")
		return fmt.Errorf("committing scaffold files: %w", err)
	}
	if committed {
		if vendorAssetCount > 0 {
			l.ui.StepDone(fmt.Sprintf("Wrote %d scaffold files and vendored binary (%d content files) to %s",
				len(files), vendorAssetCount, cfgRepo.DefaultBranch))
		} else {
			noun := "files"
			if len(files) == 1 {
				noun = "file"
			}
			l.ui.StepDone(fmt.Sprintf("Pushed %d %s to %s", len(files), noun, cfgRepo.DefaultBranch))
		}
	} else {
		l.ui.StepDone("Scaffold up to date")
	}

	if committed {
		if err := l.activateRepoMaintenance(ctx); err != nil {
			l.ui.StepWarn(fmt.Sprintf("could not activate repo-maintenance workflow: %v", err))
		}
	}

	return nil
}

func (l *WorkflowsLayer) activateRepoMaintenance(ctx context.Context) error {
	content, err := l.client.GetFileContent(ctx, l.org, forge.ConfigRepoName, configFilePath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", configFilePath, err)
	}

	l.ui.StepStart("Activating repo-maintenance workflow")
	if err := l.client.CreateOrUpdateFile(ctx, l.org, forge.ConfigRepoName, configFilePath, "chore: activate fullsend workflows", content); err != nil {
		l.ui.StepFail("Failed to activate repo-maintenance workflow")
		return fmt.Errorf("writing %s: %w", configFilePath, err)
	}
	l.ui.StepDone("Activated repo-maintenance workflow")
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
