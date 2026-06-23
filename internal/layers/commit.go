package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// CommitScaffoldFiles delivers scaffold files to a repository. When direct is
// false (the default), files are committed to a feature branch and delivered
// via PR. When direct is true, files are pushed directly to the default branch,
// falling back to a PR if branch protection blocks the push.
//
// The returned bool is true when files were committed directly to the default
// branch (false for PR-based delivery, idempotent no-ops, or unchanged content).
func CommitScaffoldFiles(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo, defaultBranch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile, direct bool) (bool, error) {

	if direct {
		return commitScaffoldDirect(ctx, client, printer,
			owner, repo, defaultBranch, commitMsg, prTitle, prBody, files)
	}
	return commitScaffoldViaPR(ctx, client, printer,
		owner, repo, defaultBranch, commitMsg, prTitle, prBody, files)
}

// commitScaffoldViaPR creates a feature branch, commits files, and opens a PR.
func commitScaffoldViaPR(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo, defaultBranch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile) (bool, error) {

	// Fixed branch name so re-runs update the same PR rather than creating a
	// new one each time. If the branch already exists, we commit on top of it.
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

	// Always attempt PR creation even when branchCommitted is false — a prior
	// run may have committed to the branch but crashed before opening the PR.
	proposal, prErr := client.CreateChangeProposal(ctx, owner, repo,
		prTitle, prBody, scaffoldBranch, defaultBranch)
	if prErr != nil {
		if forge.IsNoChanges(prErr) {
			printer.StepDone("Scaffold branch and PR up to date")
			return false, nil
		}
		if !forge.IsAlreadyExists(prErr) {
			printer.StepFail("Failed to create scaffold PR")
			return false, fmt.Errorf("creating scaffold PR: %w", prErr)
		}
		if branchCommitted {
			printer.StepDone("Scaffold PR already exists — updated with new files")
			printer.StepInfo("Merge the PR to activate fullsend workflows")
		} else {
			printer.StepDone("Scaffold branch and PR up to date")
		}
	} else {
		printer.StepDone(fmt.Sprintf("Created PR #%d: %s", proposal.Number, proposal.URL))
		printer.StepInfo("Merge the PR to activate fullsend workflows")
	}
	return false, nil
}

// commitScaffoldDirect pushes files directly to the default branch, falling
// back to a PR when branch protection blocks the push.
func commitScaffoldDirect(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo, defaultBranch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile) (bool, error) {

	committed, err := client.CommitFiles(ctx, owner, repo, commitMsg, files)
	if err != nil && forge.IsBranchProtected(err) {
		printer.StepWarn("Default branch is protected — creating scaffold PR instead")
		fallbackBody := fmt.Sprintf("The default branch (%s) has branch protection rules that prevent direct pushes.\n\n"+
			"Merge this PR to deliver the scaffold files.", defaultBranch)
		return commitScaffoldViaPR(ctx, client, printer,
			owner, repo, defaultBranch, commitMsg, prTitle, fallbackBody, files)
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
