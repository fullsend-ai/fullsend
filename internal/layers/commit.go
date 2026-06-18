package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// CommitScaffoldFiles commits files to a repo's default branch. If the branch
// is protected, it falls back to creating a PR from a feature branch.
// The returned bool is true when files were committed directly to the default
// branch (false when idempotent, on protected-branch PR fallback, or unchanged).
func CommitScaffoldFiles(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo, defaultBranch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile) (bool, error) {

	committed, err := client.CommitFiles(ctx, owner, repo, commitMsg, files)
	if err != nil && forge.IsBranchProtected(err) {
		printer.StepWarn("Default branch is protected — creating scaffold PR instead")

		// The branch name is fixed so that re-runs update the same PR rather
		// than creating a new one each time. If the branch already exists from
		// a prior run, we commit on top of it. The branch may be behind the
		// current default branch, which can produce merge conflicts in the PR;
		// this is acceptable because the user must merge the PR manually anyway.
		const scaffoldBranch = "fullsend/scaffold-install"
		if branchErr := client.CreateBranch(ctx, owner, repo, scaffoldBranch); branchErr != nil {
			if !forge.IsAlreadyExists(branchErr) {
				printer.StepFail("Failed to create scaffold branch")
				return false, fmt.Errorf("creating scaffold branch: %w", branchErr)
			}
		}

		branchCommitted, commitErr := client.CommitFilesToBranch(ctx, owner, repo, scaffoldBranch, commitMsg, files)
		if commitErr != nil {
			if forge.IsBranchProtected(commitErr) {
				printer.StepFail("Scaffold branch is also protected — cannot commit")
				return false, fmt.Errorf("scaffold branch %q is protected; configure branch protection to allow pushes to scaffold branches: %w", scaffoldBranch, commitErr)
			}
			printer.StepFail("Failed to commit scaffold files to branch")
			return false, fmt.Errorf("committing scaffold files to branch: %w", commitErr)
		}

		// Always attempt PR creation — even when branchCommitted is false.
		// A prior run may have committed to the branch but crashed before
		// creating the PR. ErrAlreadyExists handles the common re-run case.
		proposal, prErr := client.CreateChangeProposal(ctx, owner, repo,
			prTitle, prBody, scaffoldBranch, defaultBranch)
		if prErr != nil {
			if !forge.IsAlreadyExists(prErr) {
				printer.StepFail("Failed to create scaffold PR")
				return false, fmt.Errorf("creating scaffold PR: %w", prErr)
			}
			if branchCommitted {
				printer.StepDone("Scaffold PR already exists — updated with new files")
			} else {
				printer.StepDone("Scaffold branch and PR up to date")
			}
		} else {
			printer.StepDone(fmt.Sprintf("Created PR #%d: %s", proposal.Number, proposal.URL))
		}
		printer.StepInfo("Merge the PR to activate fullsend workflows")
		return false, nil
	} else if err != nil {
		printer.StepFail("Failed to commit scaffold files")
		return false, fmt.Errorf("committing scaffold files: %w", err)
	} else if committed {
		noun := "files"
		if len(files) == 1 {
			noun = "file"
		}
		printer.StepDone(fmt.Sprintf("Pushed %d %s to %s", len(files), noun, defaultBranch))
	} else {
		printer.StepDone("Scaffold up to date")
	}

	return committed, nil
}
