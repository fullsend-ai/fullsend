package steps

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerOwnersSteps(sc *godog.ScenarioContext) {
	sc.Step(`^an OWNERS file listing the (bot|outsider) as (?:an? )?(approver|reviewer)(?: only)?$`, func(ctx context.Context, actor, role string) (context.Context, error) {
		return ctx, givenActorInOwners(world.FromContext(ctx), actor, role)
	})
	sc.Step(`^an OWNERS file with alias "([^"]+)" as approver$`, func(ctx context.Context, alias string) (context.Context, error) {
		return ctx, givenOwnersFileWithAlias(world.FromContext(ctx), alias)
	})
	sc.Step(`^an OWNERS_ALIASES file mapping "([^"]+)" to the (bot|outsider)$`, func(ctx context.Context, alias, actor string) (context.Context, error) {
		return ctx, givenOwnersAliasesFile(world.FromContext(ctx), alias, actor)
	})
	sc.Step(`^OWNERS authorization is enabled$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenOwnersAuthEnabled(world.FromContext(ctx))
	})
	sc.Step(`^the triage workflow logs contain "([^"]+)"$`, func(ctx context.Context, needle string) (context.Context, error) {
		return ctx, thenWorkflowLogsContain(world.FromContext(ctx), needle)
	})
	sc.Step(`^the triage workflow logs do not contain "([^"]+)"$`, func(ctx context.Context, needle string) (context.Context, error) {
		return ctx, thenWorkflowLogsDoNotContain(world.FromContext(ctx), needle)
	})
	sc.Step(`^the (bot|outsider|write actor) opens an issue for OWNERS auth testing$`, func(ctx context.Context, actor string) (context.Context, error) {
		return ctx, whenIssueOpenedForOwnersAuth(world.FromContext(ctx), actor)
	})
	sc.Step(`^the outsider posts "([^"]+)" on the issue$`, func(ctx context.Context, command string) (context.Context, error) {
		return ctx, whenOutsiderPostsCommand(world.FromContext(ctx), command)
	})
	sc.Step(`^the dispatch run does not authorize via OWNERS$`, func(ctx context.Context) (context.Context, error) {
		return ctx, thenDispatchRunDoesNotAuthorizeViaOwners(world.FromContext(ctx))
	})
}

func resolveActorLogin(w *world.World, actor string) (string, error) {
	switch actor {
	case "bot":
		ghDriver, ok := w.SCM.(*scmgh.Driver)
		if !ok {
			return "", fmt.Errorf("OWNERS test requires GitHub SCM driver")
		}
		return ghDriver.Client.GetAuthenticatedUser(context.Background())
	case "outsider":
		if err := requireOutsider(w); err != nil {
			return "", err
		}
		return w.OutsiderLogin, nil
	case "write actor":
		if err := requireWriteActor(w); err != nil {
			return "", err
		}
		return w.WriteLogin, nil
	default:
		return "", fmt.Errorf("unknown actor %q", actor)
	}
}

func commitFile(w *world.World, path, message, content string) error {
	if err := w.SCM.CommitFile(context.Background(),
		w.Org, w.RepoName,
		path, message, []byte(content)); err != nil {
		return fmt.Errorf("committing %s: %w", path, err)
	}
	return nil
}

func givenActorInOwners(w *world.World, actor, role string) error {
	login, err := resolveActorLogin(w, actor)
	if err != nil {
		return err
	}
	var content string
	switch role {
	case "approver":
		content = fmt.Sprintf("approvers:\n  - %s\nreviewers: []\n", login)
	case "reviewer":
		content = fmt.Sprintf("approvers: []\nreviewers:\n  - %s\n", login)
	default:
		return fmt.Errorf("unknown OWNERS role %q", role)
	}
	if err := commitFile(w, "OWNERS", "behaviour: add OWNERS file for auth test", content); err != nil {
		return err
	}
	w.OwnersAuthActivated = true
	return nil
}

func givenOwnersFileWithAlias(w *world.World, alias string) error {
	if err := commitFile(w, "OWNERS", "behaviour: add OWNERS file for auth test",
		fmt.Sprintf("approvers:\n  - %s\n", alias)); err != nil {
		return err
	}
	w.OwnersAuthActivated = true
	return nil
}

func givenOwnersAliasesFile(w *world.World, alias, actor string) error {
	login, err := resolveActorLogin(w, actor)
	if err != nil {
		return err
	}
	if err := commitFile(w, "OWNERS_ALIASES", "behaviour: add OWNERS_ALIASES for auth test",
		fmt.Sprintf("aliases:\n  %s:\n    - %s\n", alias, login)); err != nil {
		return err
	}
	w.OwnersAuthActivated = true
	return nil
}

func givenOwnersAuthEnabled(w *world.World) error {
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(),
		w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	cfg.SetAuthorizationOwnersFile(true)
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(),
		w.Org, w.RepoName,
		cfgPath, "behaviour: enable OWNERS authorization",
		merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	w.OwnersAuthActivated = true
	return nil
}

func whenIssueOpenedForOwnersAuth(w *world.World, actor string) error {
	if w.RepoOwner == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before creating issues")
	}
	scmDriver := w.SCM
	switch actor {
	case "outsider":
		if err := requireOutsider(w); err != nil {
			return err
		}
		scmDriver = w.OutsiderSCM
	case "write actor":
		if err := requireWriteActor(w); err != nil {
			return err
		}
		scmDriver = w.WriteSCM
	}
	w.ScenarioStart = time.Now().Add(-issueOpenDrainSkewBuffer)
	w.TriageTriggerEvent = issueOpenEvent
	title := fmt.Sprintf("behaviour-owners-auth-%d", time.Now().UnixNano())
	body := "Behaviour test issue for OWNERS authorization path."
	issue, err := scmDriver.CreateIssue(context.Background(), w.RepoOwner, w.RepoName, title, body)
	if err != nil {
		return err
	}
	w.IssueNumber = issue.Number
	w.IssueTitle = title
	return nil
}

func thenWorkflowLogsContain(w *world.World, needle string) error {
	logs, err := getWorkflowLogs(w)
	if err != nil {
		return err
	}
	if !strings.Contains(logs, needle) {
		return fmt.Errorf("workflow logs do not contain %q", needle)
	}
	return nil
}

func thenWorkflowLogsDoNotContain(w *world.World, needle string) error {
	logs, err := getWorkflowLogs(w)
	if err != nil {
		return err
	}
	if strings.Contains(logs, needle) {
		return fmt.Errorf("workflow logs unexpectedly contain %q", needle)
	}
	return nil
}

func getWorkflowLogs(w *world.World) (string, error) {
	if err := ensureTriageWorkflowComplete(w); err != nil {
		return "", err
	}
	if w.WorkflowRun == nil {
		return "", fmt.Errorf("no workflow run recorded")
	}
	return w.CI.GetRunLogs(context.Background(),
		w.RepoOwner, w.RepoName, w.WorkflowRun.ID)
}

func requireWriteActor(w *world.World) error {
	if w.WriteSCM == nil {
		return fmt.Errorf("TEST_ACTOR_WRITE_PAT not set")
	}
	return nil
}

func requireOutsider(w *world.World) error {
	if w.OutsiderSCM == nil {
		return fmt.Errorf("TEST_ACTOR_OUTSIDER_PAT not set")
	}
	return nil
}

func whenOutsiderPostsCommand(w *world.World, command string) error {
	if err := requireOutsider(w); err != nil {
		return err
	}
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	w.ScenarioStart = time.Now()
	_, err := w.OutsiderSCM.AddComment(context.Background(),
		w.RepoOwner, w.RepoName, w.IssueNumber, command)
	return err
}

func thenDispatchRunDoesNotAuthorizeViaOwners(w *world.World) error {
	run, err := waitForDispatchRun(w)
	if err != nil {
		return err
	}
	logs, err := w.CI.GetRunLogs(context.Background(),
		w.RepoOwner, w.RepoName, run.ID)
	if err != nil {
		return fmt.Errorf("fetching dispatch run logs: %w", err)
	}
	if strings.Contains(logs, "##[notice]OWNERS file resolved user") {
		return fmt.Errorf("dispatch run %d (%s) logs unexpectedly contain OWNERS authorization", run.ID, run.HTMLURL)
	}
	return nil
}

func waitForDispatchRun(w *world.World) (*forge.WorkflowRun, error) {
	ctx := context.Background()
	repo := w.RepoName
	file := install.PerRepoTriageWorkflow

	run, err := w.CI.WaitForWorkflow(ctx, w.RepoOwner, repo, file, w.ScenarioStart, issueCommentEvent)
	if err == nil {
		return run, nil
	}

	worldLogf(w, "dispatch run: retrying with skew buffer: %v", err)
	buffered := w.ScenarioStart.Add(-issueOpenDrainSkewBuffer)
	if retryRun, retryErr := w.CI.WaitForWorkflow(ctx, w.RepoOwner, repo, file, buffered, issueCommentEvent); retryErr == nil {
		return retryRun, nil
	}
	return nil, fmt.Errorf("waiting for dispatch workflow (issue_comment): %w", err)
}

func disableOwnersAuth(w *world.World) error {
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(),
		w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	cfg.SetAuthorizationOwnersFile(false)
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(),
		w.Org, w.RepoName,
		cfgPath, "behaviour: disable OWNERS authorization",
		merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

func cleanupOwnersAuth(w *world.World) {
	ctx := context.Background()
	owner := w.Org
	repo := w.RepoName

	if err := disableOwnersAuth(w); err != nil {
		worldLogf(w, "behaviour cleanup: disable OWNERS auth: %v", err)
	}

	empty := []byte("approvers: []\nreviewers: []\n")
	if err := w.SCM.CommitFile(ctx, owner, repo,
		"OWNERS", "behaviour: clear OWNERS file", empty); err != nil {
		worldLogf(w, "behaviour cleanup: remove OWNERS file: %v", err)
	}
	emptyAliases := []byte("aliases: {}\n")
	if err := w.SCM.CommitFile(ctx, owner, repo,
		"OWNERS_ALIASES", "behaviour: clear OWNERS_ALIASES file", emptyAliases); err != nil {
		worldLogf(w, "behaviour cleanup: remove OWNERS_ALIASES file: %v", err)
	}
}
