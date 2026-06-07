package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cucumber/godog"

	gaci "github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/ci/githubactions"
	scmgh "github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/world"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/runtime"
)

func registerTriageSteps(ctx *godog.ScenarioContext, w *world.World) {
	ctx.Step(`^the enrolled test repository$`, func() error { return givenEnrolledTestRepository(w) })
	ctx.Step(`^an enrolled repository "([^"]+)"$`, func(fullName string) error {
		return givenEnrolledRepository(w, fullName)
	})
	ctx.Step(`^an issue with title "([^"]+)" and body containing "([^"]+)"$`, func(title, bodyContains string) error {
		return givenIssueWithTitleAndBody(w, title, bodyContains)
	})
	ctx.Step(`^an issue$`, func() error { return givenIssue(w) })
	ctx.Step(`^a member comments "([^"]+)" on the issue$`, func(comment string) error {
		return whenMemberComments(w, comment)
	})
	ctx.Step(`^the triage workflow completes successfully$`, func() error {
		return thenTriageWorkflowCompletes(w)
	})
	ctx.Step(`^the issue has label "([^"]+)"$`, func(label string) error {
		return thenIssueHasLabel(w, label)
	})
}

func givenEnrolledTestRepository(w *world.World) error {
	w.RepoOwner = w.Org
	w.RepoName = w.Env.TestRepo()
	w.RepoFull = w.Org + "/" + w.RepoName
	return nil
}

func givenEnrolledRepository(w *world.World, fullName string) error {
	owner, repo, err := scmgh.ParseRepo(fullName)
	if err != nil {
		return err
	}
	if owner != w.Org {
		return fmt.Errorf("repository owner %q does not match test org %q", owner, w.Org)
	}
	if repo != w.Env.TestRepo() {
		return fmt.Errorf("repository %q is not the enrolled test repo %q", repo, w.Env.TestRepo())
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
		w.RepoName = w.Env.TestRepo()
		w.RepoFull = w.Org + "/" + w.RepoName
	}
	issue, err := w.SCM.CreateIssue(context.Background(), w.RepoOwner, w.RepoName, title, body)
	if err != nil {
		return err
	}
	w.IssueNumber = issue.Number
	w.IssueTitle = title
	w.ScenarioStart = time.Now()
	return nil
}

func whenMemberComments(w *world.World, comment string) error {
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	_, err := w.SCM.AddComment(context.Background(), w.RepoOwner, w.RepoName, w.IssueNumber, comment)
	return err
}

func thenTriageWorkflowCompletes(w *world.World) error {
	ctx := context.Background()
	run, err := w.CI.WaitForWorkflow(ctx, w.Org, forge.ConfigRepoName, "triage.yml", w.ScenarioStart)
	if err != nil {
		return err
	}
	w.WorkflowRun = run

	artifactDir, err := os.MkdirTemp("", "behaviour-artifacts-*")
	if err != nil {
		return err
	}
	w.ArtifactDir = artifactDir
	if err := w.CI.DownloadArtifacts(ctx, w.Org, forge.ConfigRepoName, run.ID, artifactDir); err != nil {
		return err
	}

	if err := verifyDummyExpectations(w, artifactDir); err != nil {
		return err
	}
	return verifyOutputExpectations(w, artifactDir)
}

func verifyDummyExpectations(w *world.World, artifactDir string) error {
	data, err := gaci.FindBehaviourResults(artifactDir)
	if err != nil {
		return err
	}
	var results runtime.BehaviourResults
	if err := json.Unmarshal(data, &results); err != nil {
		return fmt.Errorf("parsing behaviour-results.json: %w", err)
	}
	byDescription := map[string]runtime.BehaviourOpResult{}
	for _, res := range results.Operations {
		byDescription[res.Description] = res
	}
	for _, exp := range w.DummyExpectations {
		res, ok := byDescription[exp.Description]
		if !ok {
			return fmt.Errorf("operation %q not found in behaviour-results.json", exp.Description)
		}
		if res.Success != exp.ExpectSuccess {
			return fmt.Errorf("operation %q: expected success=%v, got success=%v (error: %s)", exp.Description, exp.ExpectSuccess, res.Success, res.Error)
		}
	}
	return nil
}

func verifyOutputExpectations(w *world.World, artifactDir string) error {
	for _, exp := range w.OutputExpectations {
		data, err := gaci.FindOutputFile(artifactDir, exp.FileName)
		if err != nil {
			return err
		}
		actual := strings.TrimSpace(string(data))
		expected := strings.TrimSpace(exp.Content)
		if exp.Exact {
			if actual != expected {
				return fmt.Errorf("output file %q: expected %q, got %q", exp.FileName, expected, actual)
			}
			continue
		}
		if !strings.Contains(actual, expected) {
			return fmt.Errorf("output file %q: expected substring %q in %q", exp.FileName, expected, actual)
		}
	}
	return nil
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

func CleanupScenario(w *world.World) {
	ctx := context.Background()
	if w.IssueNumber > 0 {
		_ = w.SCM.CloseIssue(ctx, w.RepoOwner, w.RepoName, w.IssueNumber)
	}
	if w.ArtifactDir != "" {
		_ = os.RemoveAll(w.ArtifactDir)
	}
	empty := []byte("ops: []\n")
	_ = w.SCM.CommitFile(ctx, w.Org, ".fullsend", world.BehaviourScriptRepoPath, "behaviour: clear dummy agent script", empty)
}
