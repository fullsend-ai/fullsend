package cli

import (
	"bytes"
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// pinUpstreamRef rewrites @v0 references in scaffolded workflow files to
// point to the given git ref. This is committed as a separate commit after
// install so the history shows the original v0 install followed by an
// explicit pin. Users can use this to pin to a release tag (e.g. v0.15.0)
// or a specific commit SHA for testing.
func pinUpstreamRef(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo, ref string, workflowPaths []string) error {
	printer.StepStart(fmt.Sprintf("Pinning upstream ref to %s", ref))

	var updated []forge.TreeFile
	for _, path := range workflowPaths {
		content, err := client.GetFileContent(ctx, owner, repo, path)
		if err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("reading %s: %w", path, err)
		}
		rewritten := bytes.ReplaceAll(content, []byte("@v0"), []byte("@"+ref))
		rewritten = bytes.ReplaceAll(rewritten, []byte("fullsend_ai_ref: v0"), []byte("fullsend_ai_ref: "+ref))
		rewritten = bytes.ReplaceAll(rewritten, []byte("ref: v0"), []byte("ref: "+ref))
		if !bytes.Equal(content, rewritten) {
			updated = append(updated, forge.TreeFile{
				Path:    path,
				Content: rewritten,
				Mode:    "100644",
			})
		}
	}

	if len(updated) == 0 {
		printer.StepDone("No workflow files needed pinning")
		return nil
	}

	committed, err := client.CommitFiles(ctx, owner, repo,
		fmt.Sprintf("chore: pin upstream ref to %s", ref),
		updated)
	if err != nil {
		printer.StepFail("Failed to pin upstream ref")
		return fmt.Errorf("committing upstream ref pin: %w", err)
	}
	if committed {
		printer.StepDone(fmt.Sprintf("Pinned %d workflow files to %s", len(updated), ref))
	} else {
		printer.StepDone("Workflow files already pinned")
	}
	return nil
}

// perOrgWorkflowPaths lists the scaffold workflow files installed in
// the per-org config repo (.fullsend).
var perOrgWorkflowPaths = []string{
	".github/workflows/code.yml",
	".github/workflows/fix.yml",
	".github/workflows/prioritize-scheduler.yml",
	".github/workflows/prioritize.yml",
	".github/workflows/repo-maintenance.yml",
	".github/workflows/retro.yml",
	".github/workflows/review.yml",
	".github/workflows/triage.yml",
}

// perRepoWorkflowPaths lists the workflow files installed in per-repo mode.
var perRepoWorkflowPaths = []string{
	".github/workflows/fullsend.yaml",
}
