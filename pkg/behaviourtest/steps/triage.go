package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerTriageSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the enrolled test repository$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenEnrolledTestRepository(world.FromContext(ctx))
	})
	sc.Step(`^an enrolled repository "([^"]+)"$`, func(ctx context.Context, fullName string) (context.Context, error) {
		return ctx, givenEnrolledRepository(world.FromContext(ctx), fullName)
	})
	sc.Step(`^an issue with title "([^"]+)" and body containing "([^"]+)"$`, func(ctx context.Context, title, bodyContains string) (context.Context, error) {
		return ctx, givenIssueWithTitleAndBody(world.FromContext(ctx), title, bodyContains)
	})
	sc.Step(`^an issue$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenIssue(world.FromContext(ctx))
	})
	sc.Step(`^the issue is labeled "([^"]+)"$`, func(ctx context.Context, label string) (context.Context, error) {
		return ctx, whenIssueLabeled(world.FromContext(ctx), label)
	})
	sc.Step(`^the triage workflow completes successfully$`, func(ctx context.Context) (context.Context, error) {
		return ctx, thenTriageWorkflowCompletes(world.FromContext(ctx))
	})
	sc.Step(`^the issue has label "([^"]+)"$`, func(ctx context.Context, label string) (context.Context, error) {
		return ctx, thenIssueHasLabel(world.FromContext(ctx), label)
	})
}

func givenEnrolledTestRepository(w *world.World) error {
	w.RepoOwner = w.Org
	w.RepoName = w.Install.TestRepo()
	w.RepoFull = w.Org + "/" + w.RepoName
	return nil
}

func givenEnrolledRepository(w *world.World, fullName string) error {
	owner, repo, err := scm.ParseRepo(fullName)
	if err != nil {
		return err
	}
	if owner != w.Org {
		return fmt.Errorf("repository owner %q does not match test org %q", owner, w.Org)
	}
	if repo != w.Install.TestRepo() {
		return fmt.Errorf("repository %q is not the enrolled test repo %q", repo, w.Install.TestRepo())
	}
	w.RepoFull = fullName
	w.RepoOwner = owner
	w.RepoName = repo
	return nil
}

func givenIssueWithTitleAndBody(w *world.World, title, bodyContains string) error {
	body := fmt.Sprintf("Behaviour test issue\n\n%s\n", bodyContains)
	return createIssue(w, title, body)
}

func givenIssue(w *world.World) error {
	title := fmt.Sprintf("behaviour-issue-%d", time.Now().UnixNano())
	body := "Behaviour test issue body with steps to reproduce for triage."
	return createIssue(w, title, body)
}

func createIssue(w *world.World, title, body string) error {
	if w.RepoOwner == "" || w.RepoName == "" {
		w.RepoOwner = w.Org
		w.RepoName = w.Install.TestRepo()
		w.RepoFull = w.Org + "/" + w.RepoName
	}
	trigger := time.Now()
	issue, err := w.SCM.CreateIssue(context.Background(), w.RepoOwner, w.RepoName, title, body)
	if err != nil {
		return err
	}
	w.IssueNumber = issue.Number
	w.IssueTitle = title
	// fullsend.yaml triggers on issues opened and labeled. Drain the issue-open
	// run before applying ready-for-triage so the labeled dispatch is not skipped.
	if err := drainIssueOpenWorkflow(w, trigger); err != nil {
		return err
	}
	return nil
}

// issueOpenDrainSkewBuffer is subtracted from the trigger timestamp on
// retry to compensate for clock drift between the test runner and GitHub.
// 30 s covers typical NTP drift without matching stale runs from prior
// scenarios (pool repos are org-isolated).
const issueOpenDrainSkewBuffer = 30 * time.Second

// drainIssueOpenWorkflow waits for the fullsend.yaml workflow to process
// the issues.opened event. If the first attempt fails (typically due to
// GitHub Actions webhook delivery lag or clock skew between the runner
// and GitHub), it retries once with a relaxed trigger timestamp.
func drainIssueOpenWorkflow(w *world.World, trigger time.Time) error {
	ctx := context.Background()
	repo := w.Install.TriageWorkflowRepo()
	file := w.Install.TriageWorkflowFile()

	_, err := w.CI.WaitForWorkflow(ctx, w.Org, repo, file, trigger, issueOpenEvent)
	if err == nil {
		return nil
	}

	// Retry with a clock-skew buffer. When the test runner's clock is
	// slightly ahead of GitHub's, the workflow run's CreatedAt falls
	// before our trigger timestamp and the first poll window misses it.
	worldLogf(w, "issue-open drain: retrying with skew buffer: %v", err)
	buffered := trigger.Add(-issueOpenDrainSkewBuffer)
	if _, retryErr := w.CI.WaitForWorkflow(ctx, w.Org, repo, file, buffered, issueOpenEvent); retryErr == nil {
		return nil
	}

	return fmt.Errorf("waiting for issue-open workflow: %w", err)
}

func whenIssueLabeled(w *world.World, label string) error {
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	w.ScenarioStart = time.Now()
	w.TriageTriggerEvent = issueOpenEvent
	return w.SCM.AddIssueLabels(context.Background(), w.RepoOwner, w.RepoName, w.IssueNumber, label)
}

func thenTriageWorkflowCompletes(w *world.World) error {
	return ensureTriageWorkflowComplete(w)
}

func thenIssueHasLabel(w *world.World, label string) error {
	issue, err := w.SCM.GetIssue(context.Background(), w.RepoOwner, w.RepoName, w.IssueNumber)
	if err != nil {
		return err
	}
	for _, name := range issue.Labels {
		if name == label {
			return nil
		}
	}
	return fmt.Errorf("issue #%d labels %v do not include %q", w.IssueNumber, issue.Labels, label)
}
