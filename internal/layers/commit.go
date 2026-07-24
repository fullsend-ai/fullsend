package layers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// CommitScaffoldFiles delivers scaffold files to a repository. When direct is
// false (the default), files are committed to a feature branch and delivered
// via PR. When direct is true, files are pushed directly to the default branch,
// falling back to a PR if branch protection blocks the push.
//
// The in parameter enables interactive prompts (e.g., fork-vs-upstream choice).
// Pass os.Stdin for interactive CLI callers; pass nil for non-interactive
// callers (sync-scaffold), which default to forking without prompting.
//
// The returned bool is true when files were committed directly to the default
// branch (false for PR-based delivery, idempotent no-ops, or unchanged content).
func CommitScaffoldFiles(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo, defaultBranch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile, direct bool, in io.Reader) (bool, error) {

	commitMsg = adaptCommitMsg(ctx, client, printer, owner, repo, commitMsg)

	if direct {
		return commitScaffoldDirect(ctx, client, printer,
			owner, repo, defaultBranch, commitMsg, prTitle, prBody, files, in)
	}
	return commitScaffoldViaPR(ctx, client, printer,
		owner, repo, defaultBranch, commitMsg, prTitle, prBody, files, in)
}

// CommitFilesViaPR delivers files via a pull request on the given branch.
// Uses a fixed branch name so re-runs update the same PR.
func CommitFilesViaPR(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo, defaultBranch, branch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile) (bool, error) {

	return commitViaPR(ctx, client, printer,
		owner, repo, defaultBranch, branch, commitMsg, prTitle, prBody, files)
}

const defaultScaffoldBranch = "fullsend/scaffold-install"

// commitScaffoldViaPR creates a feature branch, commits files, and opens a PR.
// For non-owner users, it defaults to creating a fork and opening a cross-fork
// PR rather than pushing directly to the upstream repository.
func commitScaffoldViaPR(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo, defaultBranch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile, in io.Reader) (bool, error) {

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		return false, fmt.Errorf("getting authenticated user: %w", err)
	}

	// Owner pushes directly to the repo — no fork needed.
	if strings.EqualFold(user, owner) {
		return commitBranchAndPR(ctx, client, printer,
			owner, repo, owner, repo, defaultScaffoldBranch, defaultBranch,
			commitMsg, prTitle, prBody, files)
	}

	// Non-owner with write access can push directly — avoids fork trust
	// gates (/ok-to-test) that block CI on cross-fork PRs.
	if hasWriteAccess(ctx, client, owner, repo, user) {
		printer.StepInfo(fmt.Sprintf("User %s has write access — pushing directly to %s/%s", user, owner, repo))
		return commitBranchAndPR(ctx, client, printer,
			owner, repo, owner, repo, defaultScaffoldBranch, defaultBranch,
			commitMsg, prTitle, prBody, files)
	}

	// Non-owner: check for existing fork first.
	forkOwner, forkRepo, err := client.FindExistingFork(ctx, owner, repo)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Could not check for existing fork: %v", err))
	}

	if forkOwner != "" {
		printer.StepDone(fmt.Sprintf("Using existing fork %s/%s", forkOwner, forkRepo))
		return commitViaFork(ctx, client, printer,
			owner, repo, forkOwner, forkRepo, defaultScaffoldBranch, defaultBranch,
			commitMsg, prTitle, prBody, files)
	}

	// No existing fork — decide whether to create one.
	// Fine-grained PATs are scoped to a single org and cannot create cross-org
	// forks, so the fork option is unavailable when one is detected.
	isFineGrained, fgErr := isFineGrainedToken(ctx, client)
	if fgErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not detect token type: %v", fgErr))
	}

	useFork := true
	if isFineGrained {
		if in != nil {
			confirmed, promptErr := promptUpstreamOnly(printer, in, owner, repo)
			if promptErr != nil {
				return false, fmt.Errorf("reading delivery choice: %w", promptErr)
			}
			if !confirmed {
				return false, fmt.Errorf("upstream delivery declined by user")
			}
		} else {
			printer.StepInfo("Non-interactive mode: fine-grained token detected, pushing to upstream")
		}
		useFork = false
	} else if in != nil {
		choice, promptErr := promptForkChoice(printer, in)
		if promptErr != nil {
			return false, fmt.Errorf("reading fork choice: %w", promptErr)
		}
		useFork = choice
	} else {
		printer.StepInfo("Non-interactive mode: creating fork by default")
	}

	if useFork {
		return forkAndCommit(ctx, client, printer,
			owner, repo, defaultScaffoldBranch, defaultBranch,
			commitMsg, prTitle, prBody, files)
	}

	// Upstream path: try to push directly, fail clearly on 403.
	return commitBranchAndPR(ctx, client, printer,
		owner, repo, owner, repo, defaultScaffoldBranch, defaultBranch,
		commitMsg, prTitle, prBody, files)
}

// forkAndCommit creates a fork, waits for it to be ready, then commits.
func forkAndCommit(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo, scaffoldBranch, defaultBranch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile) (bool, error) {

	printer.StepStart("Creating fork")
	forkOwner, forkRepo, err := client.CreateFork(ctx, owner, repo)
	if err != nil {
		printer.StepFail("Failed to create fork")
		return false, fmt.Errorf("creating fork of %s/%s: %w", owner, repo, err)
	}
	printer.StepDone(fmt.Sprintf("Fork created: %s/%s", forkOwner, forkRepo))

	if err := waitForFork(ctx, client, printer, forkOwner, forkRepo); err != nil {
		return false, err
	}

	return commitViaFork(ctx, client, printer,
		owner, repo, forkOwner, forkRepo, scaffoldBranch, defaultBranch,
		commitMsg, prTitle, prBody, files)
}

// commitViaFork pushes to a fork and opens a cross-fork PR.
func commitViaFork(ctx context.Context, client forge.Client, printer *ui.Printer,
	upstreamOwner, upstreamRepo, forkOwner, forkRepo, scaffoldBranch, defaultBranch,
	commitMsg, prTitle, prBody string, files []forge.TreeFile) (bool, error) {

	return commitBranchAndPR(ctx, client, printer,
		upstreamOwner, upstreamRepo, forkOwner, forkRepo,
		scaffoldBranch, defaultBranch, commitMsg, prTitle, prBody, files)
}

// commitBranchAndPR creates a branch on targetOwner/targetRepo, commits files,
// and opens a PR against upstreamOwner/upstreamRepo. When target == upstream
// (same-repo PR), head is the branch name. When target != upstream (cross-fork
// PR), head is "forkOwner:branchName".
func commitBranchAndPR(ctx context.Context, client forge.Client, printer *ui.Printer,
	upstreamOwner, upstreamRepo, targetOwner, targetRepo,
	scaffoldBranch, defaultBranch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile) (bool, error) {

	isCrossRepo := !strings.EqualFold(targetOwner, upstreamOwner) || !strings.EqualFold(targetRepo, upstreamRepo)

	var branchErr error
	if isCrossRepo {
		// Cross-fork: create the scaffold branch from the upstream's HEAD so
		// the PR diff only contains scaffold changes, even if the fork's
		// default branch is behind upstream.
		upstreamSHA, shaErr := client.GetBranchRef(ctx, upstreamOwner, upstreamRepo, defaultBranch)
		if shaErr != nil {
			printer.StepFail("Failed to resolve upstream branch")
			return false, fmt.Errorf("getting upstream branch ref for %s/%s@%s: %w",
				upstreamOwner, upstreamRepo, defaultBranch, shaErr)
		}
		branchErr = client.CreateBranchFromSHA(ctx, targetOwner, targetRepo, scaffoldBranch, upstreamSHA)
	} else {
		branchErr = client.CreateBranch(ctx, targetOwner, targetRepo, scaffoldBranch)
	}

	if branchErr != nil {
		if forge.IsForbidden(branchErr) {
			printer.StepFail("Insufficient permissions to push to repository")
			return false, fmt.Errorf("cannot push to %s/%s (403 forbidden); re-run with the fork option or check your token scopes: %w",
				targetOwner, targetRepo, branchErr)
		}
		if !forge.IsAlreadyExists(branchErr) {
			printer.StepFail("Failed to create scaffold branch")
			return false, fmt.Errorf("creating scaffold branch: %w", branchErr)
		}
	}

	branchCommitted, commitErr := client.CommitFilesToBranch(ctx, targetOwner, targetRepo, scaffoldBranch, commitMsg, files)
	if commitErr != nil {
		if forge.IsBranchProtected(commitErr) {
			printer.StepFail("Scaffold branch is also protected — cannot commit")
			return false, fmt.Errorf("scaffold branch %q is protected; configure branch protection to allow pushes to scaffold branches: %w", scaffoldBranch, commitErr)
		}
		printer.StepFail("Failed to commit scaffold files to branch")
		return false, fmt.Errorf("committing scaffold files to branch: %w", commitErr)
	}

	// For cross-fork PRs, head must be "forkOwner:branchName".
	head := scaffoldBranch
	if !strings.EqualFold(targetOwner, upstreamOwner) || !strings.EqualFold(targetRepo, upstreamRepo) {
		head = targetOwner + ":" + scaffoldBranch
	}

	proposal, prErr := client.CreateChangeProposal(ctx, upstreamOwner, upstreamRepo,
		prTitle, prBody, head, defaultBranch)
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

// commitViaPR creates a feature branch, commits files, and opens a PR.
// Unlike commitBranchAndPR, this is a simpler pathway for same-owner PRs
// (e.g., enrollment config updates) that don't need fork support.
func commitViaPR(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo, defaultBranch, branch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile) (bool, error) {

	if branchErr := client.CreateBranch(ctx, owner, repo, branch); branchErr != nil {
		if forge.IsForbidden(branchErr) {
			printer.StepFail("Insufficient permissions to create branch")
			return false, fmt.Errorf("cannot push to %s/%s (403 forbidden); check your token scopes: %w",
				owner, repo, branchErr)
		}
		if !forge.IsAlreadyExists(branchErr) {
			printer.StepFail("Failed to create feature branch")
			return false, fmt.Errorf("creating feature branch: %w", branchErr)
		}
	}

	branchCommitted, commitErr := client.CommitFilesToBranch(ctx, owner, repo, branch, commitMsg, files)
	if commitErr != nil {
		if forge.IsBranchProtected(commitErr) {
			printer.StepFail("Feature branch is protected — cannot commit")
			return false, fmt.Errorf("branch %q is protected; configure branch protection to allow pushes: %w", branch, commitErr)
		}
		printer.StepFail("Failed to commit files to branch")
		return false, fmt.Errorf("committing files to branch: %w", commitErr)
	}

	proposal, prErr := client.CreateChangeProposal(ctx, owner, repo,
		prTitle, prBody, branch, defaultBranch)
	if prErr != nil {
		if forge.IsNoChanges(prErr) {
			printer.StepDone("Branch and PR up to date")
			return false, nil
		}
		if !forge.IsAlreadyExists(prErr) {
			printer.StepFail("Failed to create PR")
			return false, fmt.Errorf("creating PR: %w", prErr)
		}
		if branchCommitted {
			printer.StepDone("PR already exists — updated with new files")
			printer.StepInfo("Merge the PR to apply changes")
		} else {
			printer.StepDone("Branch and PR up to date")
		}
	} else {
		printer.StepDone(fmt.Sprintf("Created PR #%d: %s", proposal.Number, proposal.URL))
		printer.StepInfo("Merge the PR to apply changes")
	}
	return false, nil
}

// waitForFork polls GetRepo until the fork is ready or the timeout expires.
// GitHub fork creation is async (202 Accepted) and can take up to several
// minutes for large repos.
func waitForFork(ctx context.Context, client forge.Client, printer *ui.Printer,
	forkOwner, forkRepo string) error {

	const (
		pollInterval = 3 * time.Second
		timeout      = 2 * time.Minute
	)

	deadline := time.Now().Add(timeout)
	printer.StepStart(fmt.Sprintf("Waiting for fork %s/%s to be ready", forkOwner, forkRepo))

	for {
		if _, err := client.GetRepo(ctx, forkOwner, forkRepo); err == nil {
			printer.StepDone("Fork is ready")
			return nil
		} else if !forge.IsNotFound(err) {
			printer.StepFail("Error checking fork status")
			return fmt.Errorf("checking fork %s/%s: %w", forkOwner, forkRepo, err)
		}
		if time.Now().After(deadline) {
			printer.StepFail("Timed out waiting for fork")
			return fmt.Errorf("fork %s/%s not ready after %s; try again in a few minutes", forkOwner, forkRepo, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// promptForkChoice asks the user whether to create a fork or push upstream.
// Returns true for fork, false for upstream.
func promptForkChoice(printer *ui.Printer, in io.Reader) (bool, error) {
	printer.Blank()
	printer.StepInfo("You don't have a fork of this repository.")
	printer.StepInfo("Choose how to deliver scaffold files:")
	printer.StepInfo("  [f] Create a fork (recommended)")
	printer.StepInfo("  [u] Push directly to upstream repository")
	printer.Blank()

	const maxAttempts = 5
	reader := bufio.NewReader(in)
	for i := 0; i < maxAttempts; i++ {
		printer.StepInfo("Enter choice [f]: ")
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return true, err
		}
		choice := strings.TrimSpace(strings.ToLower(line))
		switch choice {
		case "", "f":
			return true, nil
		case "u":
			return false, nil
		default:
			printer.StepWarn("Invalid choice. Please enter 'f' or 'u'.")
		}
	}
	return true, fmt.Errorf("too many invalid attempts")
}

// isFineGrainedToken reports whether the current token is a fine-grained PAT.
// Fine-grained PATs return nil from GetTokenScopes because GitHub doesn't
// populate the X-OAuth-Scopes header for them.
func isFineGrainedToken(ctx context.Context, client forge.Client) (bool, error) {
	isInstall, err := client.IsInstallationToken(ctx)
	if err != nil {
		return false, err
	}
	if isInstall {
		return false, nil
	}

	scopes, err := client.GetTokenScopes(ctx)
	if err != nil {
		return false, err
	}
	return scopes == nil, nil
}

// promptUpstreamOnly explains that the fork option is unavailable with a
// fine-grained PAT and asks the user to confirm pushing to upstream. The
// upstream path creates a PR with the fullsend scaffolding files — it does
// not push directly to the default branch.
func promptUpstreamOnly(printer *ui.Printer, in io.Reader, owner, repo string) (bool, error) {
	printer.Blank()
	printer.StepWarn("Fine-grained token detected — fork option is not available.")
	printer.StepInfo("Fine-grained PATs are scoped to a single organization and cannot")
	printer.StepInfo("create forks in other accounts. This is a GitHub platform limitation.")
	printer.Blank()
	printer.StepInfo(fmt.Sprintf("Option [u] will push a branch to %s/%s and create a PR", owner, repo))
	printer.StepInfo("containing the fullsend scaffolding files (config, workflows, secrets).")
	printer.StepInfo("No changes are made to the default branch until the PR is merged.")
	printer.Blank()

	const maxAttempts = 5
	reader := bufio.NewReader(in)
	for i := 0; i < maxAttempts; i++ {
		printer.StepInfo("Proceed with upstream PR? [y/n]: ")
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return false, err
		}
		choice := strings.TrimSpace(strings.ToLower(line))
		switch choice {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			printer.StepWarn("Invalid choice. Please enter 'y' or 'n'.")
		}
	}
	return false, fmt.Errorf("too many invalid attempts")
}

// commitScaffoldDirect pushes files directly to the default branch, falling
// back to a PR when branch protection blocks the push.
func commitScaffoldDirect(ctx context.Context, client forge.Client, printer *ui.Printer,
	owner, repo, defaultBranch, commitMsg, prTitle, prBody string,
	files []forge.TreeFile, in io.Reader) (bool, error) {

	committed, err := client.CommitFiles(ctx, owner, repo, commitMsg, files)
	if err != nil && forge.IsNonFastForward(err) {
		printer.StepWarn("Ref update hit auto_init race — retrying")
		committed, err = client.CommitFiles(ctx, owner, repo, commitMsg, files)
	}
	if err != nil && forge.IsBranchProtected(err) {
		printer.StepWarn("Default branch is protected — creating scaffold PR instead")
		fallbackBody := fmt.Sprintf("The default branch (%s) has branch protection rules that prevent direct pushes.\n\n"+
			"Merge this PR to deliver the scaffold files.", defaultBranch)
		return commitScaffoldViaPR(ctx, client, printer,
			owner, repo, defaultBranch, commitMsg, prTitle, fallbackBody, files, in)
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

// adaptCommitMsg checks the target repo for a .gitlint title-match-regex. If
// the current commit message doesn't match, it tries common conventional-commit
// alternatives (chore:, ci:, build:, bare description). Falls back to warning when no
// alternative matches — we can't fabricate a valid message for an arbitrary regex.
func adaptCommitMsg(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo, commitMsg string) string {
	re := gitlintTitleRegex(ctx, client, owner, repo)
	if re == nil {
		return commitMsg
	}
	title := strings.SplitN(commitMsg, "\n", 2)[0]
	if re.MatchString(title) {
		return commitMsg
	}

	desc := title
	if idx := strings.Index(title, ": "); idx >= 0 {
		desc = title[idx+2:]
	}
	alternatives := []string{
		"chore: " + desc,
		"ci: " + desc,
		"build: " + desc,
		desc,
	}
	var body string
	if parts := strings.SplitN(commitMsg, "\n", 2); len(parts) == 2 {
		body = parts[1]
	}
	for _, alt := range alternatives {
		if alt == title {
			continue
		}
		if re.MatchString(alt) {
			adapted := alt
			if body != "" {
				adapted += "\n" + body
			}
			printer.StepInfo(fmt.Sprintf("Adapted scaffold commit message to match .gitlint: %q", alt))
			return adapted
		}
	}

	printer.StepWarn(fmt.Sprintf("Scaffold commit message %q does not match this repo's .gitlint title-match-regex (%s) — commit-lint CI may fail",
		title, re.String()))
	return commitMsg
}

// gitlintTitleRegex reads .gitlint from the target repo and extracts the
// title-match-regex value. Returns nil if the file doesn't exist or has no
// title-match-regex rule.
func gitlintTitleRegex(ctx context.Context, client forge.Client, owner, repo string) *regexp.Regexp {
	content, err := client.GetFileContent(ctx, owner, repo, ".gitlint")
	if err != nil {
		return nil
	}
	inSection := false
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inSection = strings.EqualFold(line, "[title-match-regex]")
			continue
		}
		if !inSection {
			continue
		}
		if parts := strings.SplitN(line, "=", 2); len(parts) == 2 && strings.TrimSpace(parts[0]) == "regex" {
			val := strings.TrimSpace(parts[1])
			re, err := regexp.Compile(val)
			if err != nil {
				return nil
			}
			return re
		}
	}
	return nil
}

// hasWriteAccess checks whether the user has write or admin permission on the
// repo. Returns false on any error (or non-GitHub forges) so the caller falls
// through to the fork path.
func hasWriteAccess(ctx context.Context, client forge.Client, owner, repo, user string) bool {
	ghExt, ok := client.(forge.GitHubExtensions)
	if !ok {
		return false
	}
	role, err := ghExt.GetCollaboratorPermission(ctx, owner, repo, user)
	if err != nil {
		return false
	}
	return role == "write" || role == "maintain" || role == "admin"
}
