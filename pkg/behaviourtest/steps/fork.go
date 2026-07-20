package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerForkSteps(ctx *godog.ScenarioContext, w *world.World) {
	ctx.Step(`^a fork "([^"]+)" of the enrolled test repository$`, func(forkName string) error {
		return givenFork(w, forkName)
	})
	ctx.Step(`^a fork pull request is opened$`, func() error {
		return whenForkPullRequestOpened(w)
	})
	ctx.Step(`^a commit is pushed to the fork pull request$`, func() error {
		return whenCommitPushedToForkPR(w)
	})
}

// givenFork creates a fork of the enrolled test repository if absent, or
// reuses it if it already exists. The fork is created within the same
// organization as the source repository.
func givenFork(w *world.World, forkName string) error {
	if w.RepoOwner == "" || w.RepoName == "" {
		w.RepoOwner = w.Org
		w.RepoName = w.Install.TestRepo()
		w.RepoFull = w.Org + "/" + w.RepoName
	}

	ctx := context.Background()
	forkRepo, err := w.SCM.CreateFork(ctx, w.RepoOwner, w.RepoName, forkName)
	if err != nil {
		return fmt.Errorf("creating fork %q: %w", forkName, err)
	}
	w.ForkOwner = w.RepoOwner
	w.ForkRepo = forkRepo
	return nil
}

// whenForkPullRequestOpened commits a file to a new branch on the fork
// and opens a cross-fork pull request against the base repository.
func whenForkPullRequestOpened(w *world.World) error {
	if w.ForkOwner == "" || w.ForkRepo == "" {
		return fmt.Errorf("no fork created: use 'Given a fork' first")
	}

	w.ScenarioStart = time.Now()
	branch := fmt.Sprintf("behaviour-fork-pr-%d", time.Now().UnixNano())

	ctx := context.Background()

	// Create the branch on the fork first — GitHub's Contents API
	// (used by CommitFileToFork → CreateOrUpdateFileOnBranch) requires
	// the target branch to already exist.
	if err := w.SCM.CreateBranch(ctx, w.ForkOwner, w.ForkRepo, branch); err != nil {
		return fmt.Errorf("creating fork branch: %w", err)
	}

	msg := fmt.Sprintf("behaviour fork pr %s", branch)
	if err := w.SCM.CommitFileToFork(ctx, w.ForkOwner, w.ForkRepo, branch, "behaviour/fork-pr.txt", msg, []byte("behaviour fork test\n")); err != nil {
		return fmt.Errorf("committing to fork branch: %w", err)
	}

	pr, err := w.SCM.CreateForkChangeProposal(ctx, w.RepoOwner, w.RepoName, "Behaviour fork test PR", "behaviour fork", w.ForkOwner, w.ForkRepo, branch, "main")
	if err != nil {
		return fmt.Errorf("creating fork pull request: %w", err)
	}
	w.ForkPRNumber = pr.Number
	w.ForkPRBranch = branch
	return nil
}

// whenCommitPushedToForkPR pushes an additional commit to the head branch
// of an existing fork pull request.
func whenCommitPushedToForkPR(w *world.World) error {
	if w.ForkPRNumber == 0 {
		return fmt.Errorf("no fork pull request opened")
	}

	w.ScenarioStart = time.Now()
	ctx := context.Background()

	msg := fmt.Sprintf("behaviour: push to fork PR #%d", w.ForkPRNumber)
	content := []byte(fmt.Sprintf("pushed at %d\n", time.Now().UnixNano()))
	if err := w.SCM.CommitFileToFork(ctx, w.ForkOwner, w.ForkRepo, w.ForkPRBranch, "behaviour/fork-push.txt", msg, content); err != nil {
		return fmt.Errorf("pushing commit to fork PR: %w", err)
	}
	return nil
}
