package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// CommitScaffoldFiles commits files to a repo's default branch. If the branch
// is protected, it falls back to creating a PR from a feature branch.
func CommitScaffoldFiles(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo, defaultBranch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile) error {

	committed, err := client.CommitFiles(ctx, owner, repo, commitMsg, files)
	if err != nil && forge.IsBranchProtected(err) {
		printer.StepWarn("Default branch is protected — creating scaffold PR instead")

		const scaffoldBranch = "fullsend/scaffold-install"
		if branchErr := client.CreateBranch(ctx, owner, repo, scaffoldBranch); branchErr != nil {
			if !forge.IsAlreadyExists(branchErr) {
				printer.StepFail("Failed to create scaffold branch")
				return fmt.Errorf("creating scaffold branch: %w", branchErr)
			}
		}

		branchCommitted, commitErr := client.CommitFilesToBranch(ctx, owner, repo, scaffoldBranch, commitMsg, files)
		if commitErr != nil {
			if forge.IsBranchProtected(commitErr) {
				printer.StepFail("Scaffold branch is also protected — cannot commit")
				return fmt.Errorf("scaffold branch %q is protected; configure branch protection to allow pushes to scaffold branches: %w", scaffoldBranch, commitErr)
			}
			printer.StepFail("Failed to commit scaffold files to branch")
			return fmt.Errorf("committing scaffold files to branch: %w", commitErr)
		}

		if branchCommitted {
			proposal, prErr := client.CreateChangeProposal(ctx, owner, repo,
				prTitle, prBody, scaffoldBranch, defaultBranch)
			if prErr != nil {
				if !forge.IsAlreadyExists(prErr) {
					printer.StepFail("Failed to create scaffold PR")
					return fmt.Errorf("creating scaffold PR: %w", prErr)
				}
				printer.StepDone("Scaffold PR already exists")
			} else {
				printer.StepDone(fmt.Sprintf("Created PR #%d: %s", proposal.Number, proposal.URL))
			}
			printer.StepInfo("Merge the PR to activate fullsend workflows")
		} else {
			printer.StepDone("Scaffold branch up to date")
		}
	} else if err != nil {
		printer.StepFail("Failed to commit scaffold files")
		return fmt.Errorf("committing scaffold files: %w", err)
	} else if committed {
		noun := "files"
		if len(files) == 1 {
			noun = "file"
		}
		printer.StepDone(fmt.Sprintf("Pushed %d %s to %s", len(files), noun, defaultBranch))
	} else {
		printer.StepDone("Scaffold up to date")
	}

	return nil
}
