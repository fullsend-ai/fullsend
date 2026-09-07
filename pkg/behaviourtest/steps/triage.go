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
	sc.Step(`^a test repository with fullsend installed$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenInstalledTestRepository(ctx, world.FromContext(ctx))
	})
	sc.Step(`^an installed repository "([^"]+)"$`, func(ctx context.Context, fullName string) (context.Context, error) {
		return ctx, givenInstalledRepository(world.FromContext(ctx), fullName)
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

func givenInstalledTestRepository(ctx context.Context, w *world.World) error {
	if w.RepoName != "" {
		return nil
	}

	if w.Driver == nil {
		return fmt.Errorf("no install driver configured; use a Factory to construct a Driver for the suite")
	}

	repoName, err := w.Driver.CreateRepo(ctx, w.ScenarioName)
	if err != nil {
		return fmt.Errorf("creating repo: %w", err)
	}

	w.RepoOwner = w.Org
	w.RepoName = repoName
	w.RepoFull = w.Org + "/" + repoName

	return nil
}

func givenInstalledRepository(w *world.World, fullName string) error {
	owner, repo, err := scm.ParseRepo(fullName)
	if err != nil {
		return err
	}
	if owner != w.Org {
		return fmt.Errorf("repository owner %q does not match test org %q", owner, w.Org)
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
		return fmt.Errorf("no repo configured; call 'Given a test repository with fullsend installed' before creating issues")
	}
	issue, err := w.SCM.CreateIssue(context.Background(), w.RepoOwner, w.RepoName, title, body)
	if err != nil {
		return err
	}
	w.IssueNumber = issue.Number
	w.IssueTitle = title
	return nil
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
