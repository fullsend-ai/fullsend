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
	gaci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerOwnersSteps(sc *godog.ScenarioContext) {
	sc.Step(`^an OWNERS file listing the bot as (?:an? )?(approver|reviewer)(?: only)?$`, func(ctx context.Context, role string) (context.Context, error) {
		return ctx, givenBotInOwners(world.FromContext(ctx), role)
	})
	sc.Step(`^an OWNERS file with alias "([^"]+)" as approver$`, func(ctx context.Context, alias string) (context.Context, error) {
		return ctx, givenOwnersFileWithAlias(world.FromContext(ctx), alias)
	})
	sc.Step(`^an OWNERS_ALIASES file mapping "([^"]+)" to the bot$`, func(ctx context.Context, alias string) (context.Context, error) {
		return ctx, givenOwnersAliasesFile(world.FromContext(ctx), alias)
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
	sc.Step(`^an issue is opened for OWNERS auth testing$`, func(ctx context.Context) (context.Context, error) {
		return ctx, whenIssueOpenedForOwnersAuth(world.FromContext(ctx))
	})
	sc.Step(`^an OWNERS file listing the outsider as a reviewer$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenOwnersFileWithOutsiderReviewer(world.FromContext(ctx))
	})
	sc.Step(`^the outsider posts "([^"]+)" on the issue$`, func(ctx context.Context, command string) (context.Context, error) {
		return ctx, whenOutsiderPostsCommand(world.FromContext(ctx), command)
	})
	sc.Step(`^the dispatch run does not authorize via OWNERS$`, func(ctx context.Context) (context.Context, error) {
		return ctx, thenDispatchRunDoesNotAuthorizeViaOwners(world.FromContext(ctx))
	})
}

func resolveBotLogin(w *world.World) (string, error) {
	ghDriver, ok := w.SCM.(*scmgh.Driver)
	if !ok {
		return "", fmt.Errorf("OWNERS test requires GitHub SCM driver")
	}
	return ghDriver.Client.GetAuthenticatedUser(context.Background())
}

func commitFile(w *world.World, path, message, content string) error {
	if err := w.SCM.CommitFile(context.Background(),
		w.Install.ConfigOwner(), w.Install.ConfigRepo(),
		path, message, []byte(content)); err != nil {
		return fmt.Errorf("committing %s: %w", path, err)
	}
	return nil
}

func givenBotInOwners(w *world.World, role string) error {
	login, err := resolveBotLogin(w)
	if err != nil {
		return fmt.Errorf("resolving bot login: %w", err)
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

func givenOwnersAliasesFile(w *world.World, alias string) error {
	login, err := resolveBotLogin(w)
	if err != nil {
		return fmt.Errorf("resolving bot login: %w", err)
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
		w.Install.ConfigOwner(), w.Install.ConfigRepo(), cfgPath)
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
		w.Install.ConfigOwner(), w.Install.ConfigRepo(),
		cfgPath, "behaviour: enable OWNERS authorization",
		merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	w.OwnersAuthActivated = true
	return nil
}

// whenIssueOpenedForOwnersAuth creates an issue without draining the
// issues.opened workflow run. The issues.opened path unconditionally
// calls is_event_actor_authorized, which exercises has_repo_permission
// and the OWNERS authorization code path.
func whenIssueOpenedForOwnersAuth(w *world.World) error {
	if w.RepoOwner == "" || w.RepoName == "" {
		w.RepoOwner = w.Org
		w.RepoName = w.Install.TestRepo()
		w.RepoFull = w.Org + "/" + w.RepoName
	}
	w.ScenarioStart = time.Now().Add(-issueOpenDrainSkewBuffer)
	w.TriageTriggerEvent = issueOpenEvent
	title := fmt.Sprintf("behaviour-owners-auth-%d", time.Now().UnixNano())
	body := "Behaviour test issue for OWNERS authorization path."
	issue, err := w.SCM.CreateIssue(context.Background(), w.RepoOwner, w.RepoName, title, body)
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
		w.RepoOwner, w.Install.TriageWorkflowRepo(), w.WorkflowRun.ID)
}

func requireOutsider(w *world.World) error {
	if w.OutsiderSCM == nil {
		return fmt.Errorf("TEST_ACTOR_OUTSIDER_PAT not set")
	}
	return nil
}

func givenOwnersFileWithOutsiderReviewer(w *world.World) error {
	if err := requireOutsider(w); err != nil {
		return err
	}
	if err := commitFile(w, "OWNERS", "behaviour: add OWNERS file for auth test",
		fmt.Sprintf("approvers: []\nreviewers:\n  - %s\n", w.OutsiderLogin)); err != nil {
		return err
	}
	w.OwnersAuthActivated = true
	return nil
}

func whenOutsiderPostsCommand(w *world.World, command string) error {
	if err := requireOutsider(w); err != nil {
		return err
	}
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	w.ScenarioStart = time.Now().Add(-issueOpenDrainSkewBuffer)
	_, err := w.OutsiderSCM.AddComment(context.Background(),
		w.RepoOwner, w.RepoName, w.IssueNumber, command)
	return err
}

func thenDispatchRunDoesNotAuthorizeViaOwners(w *world.World) error {
	gaciDriver, ok := w.CI.(*gaci.Driver)
	if !ok {
		return fmt.Errorf("dispatch run check requires GitHub Actions CI driver")
	}
	run, err := waitForDispatchRun(gaciDriver, w)
	if err != nil {
		return err
	}
	logs, err := gaciDriver.Client.GetWorkflowRunLogs(context.Background(),
		w.RepoOwner, w.Install.TriageWorkflowRepo(), run.ID)
	if err != nil {
		return fmt.Errorf("fetching dispatch run logs: %w", err)
	}
	if strings.Contains(logs, "authorized via OWNERS file") {
		return fmt.Errorf("dispatch run logs unexpectedly contain OWNERS authorization")
	}
	if !strings.Contains(logs, "No stage matched") {
		return fmt.Errorf("dispatch run logs do not contain 'No stage matched' — dispatch may have proceeded via a non-OWNERS path")
	}
	return nil
}

func waitForDispatchRun(driver *gaci.Driver, w *world.World) (*forge.WorkflowRun, error) {
	workflowFile := filepath.Base(w.Install.TriageWorkflowFile())
	ctx := context.Background()

	const poll = 5 * time.Second
	deadline := time.Now().Add(12 * time.Minute)

	for time.Now().Before(deadline) {
		time.Sleep(poll)
		runs, err := driver.Client.ListWorkflowRuns(ctx,
			w.RepoOwner, w.Install.TriageWorkflowRepo(), workflowFile)
		if err != nil {
			continue
		}
		for _, run := range runs {
			if run.Event != "issue_comment" {
				continue
			}
			if w.OutsiderLogin != "" && run.ActorLogin != w.OutsiderLogin {
				continue
			}
			runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
			if parseErr != nil || runTime.Before(w.ScenarioStart) {
				continue
			}
			if run.Status == "completed" {
				return &run, nil
			}
		}
	}

	return nil, fmt.Errorf("dispatch workflow (issue_comment, actor=%s) did not complete within deadline", w.OutsiderLogin)
}

func disableOwnersAuth(w *world.World) error {
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(),
		w.Install.ConfigOwner(), w.Install.ConfigRepo(), cfgPath)
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
		w.Install.ConfigOwner(), w.Install.ConfigRepo(),
		cfgPath, "behaviour: disable OWNERS authorization",
		merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

func cleanupOwnersAuth(w *world.World) {
	ctx := context.Background()
	owner := w.Install.ConfigOwner()
	repo := w.Install.ConfigRepo()

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
