package steps

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/config"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerOwnersSteps(sc *godog.ScenarioContext) {
	sc.Step(`^an OWNERS file listing the bot as an approver$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenOwnersFileWithBot(world.FromContext(ctx))
	})
	sc.Step(`^an OWNERS file with alias "([^"]+)" as approver$`, func(ctx context.Context, alias string) (context.Context, error) {
		return ctx, givenOwnersFileWithAlias(world.FromContext(ctx), alias)
	})
	sc.Step(`^an OWNERS_ALIASES file mapping "([^"]+)" to the bot$`, func(ctx context.Context, alias string) (context.Context, error) {
		return ctx, givenOwnersAliasesFile(world.FromContext(ctx), alias)
	})
	sc.Step(`^an OWNERS file listing the bot as a reviewer only$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenOwnersFileWithBotReviewerOnly(world.FromContext(ctx))
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
}

func givenOwnersFileWithBot(w *world.World) error {
	ghDriver, ok := w.SCM.(*scmgh.Driver)
	if !ok {
		return fmt.Errorf("OWNERS test requires GitHub SCM driver")
	}
	botLogin, err := ghDriver.Client.GetAuthenticatedUser(context.Background())
	if err != nil {
		return fmt.Errorf("resolving bot login: %w", err)
	}
	owners := fmt.Sprintf("approvers:\n  - %s\nreviewers: []\n", botLogin)
	if err := w.SCM.CommitFile(context.Background(),
		w.Install.ConfigOwner(), w.Install.ConfigRepo(),
		"OWNERS", "behaviour: add OWNERS file for auth test",
		[]byte(owners)); err != nil {
		return fmt.Errorf("committing OWNERS file: %w", err)
	}
	w.OwnersAuthActivated = true
	return nil
}

func givenOwnersFileWithAlias(w *world.World, alias string) error {
	owners := fmt.Sprintf("approvers:\n  - %s\n", alias)
	if err := w.SCM.CommitFile(context.Background(),
		w.Install.ConfigOwner(), w.Install.ConfigRepo(),
		"OWNERS", "behaviour: add OWNERS file with alias for auth test",
		[]byte(owners)); err != nil {
		return fmt.Errorf("committing OWNERS file: %w", err)
	}
	w.OwnersAuthActivated = true
	return nil
}

func givenOwnersAliasesFile(w *world.World, alias string) error {
	ghDriver, ok := w.SCM.(*scmgh.Driver)
	if !ok {
		return fmt.Errorf("OWNERS test requires GitHub SCM driver")
	}
	botLogin, err := ghDriver.Client.GetAuthenticatedUser(context.Background())
	if err != nil {
		return fmt.Errorf("resolving bot login: %w", err)
	}
	aliases := fmt.Sprintf("aliases:\n  %s:\n    - %s\n", alias, botLogin)
	if err := w.SCM.CommitFile(context.Background(),
		w.Install.ConfigOwner(), w.Install.ConfigRepo(),
		"OWNERS_ALIASES", "behaviour: add OWNERS_ALIASES for auth test",
		[]byte(aliases)); err != nil {
		return fmt.Errorf("committing OWNERS_ALIASES file: %w", err)
	}
	w.OwnersAuthActivated = true
	return nil
}

func givenOwnersFileWithBotReviewerOnly(w *world.World) error {
	ghDriver, ok := w.SCM.(*scmgh.Driver)
	if !ok {
		return fmt.Errorf("OWNERS test requires GitHub SCM driver")
	}
	botLogin, err := ghDriver.Client.GetAuthenticatedUser(context.Background())
	if err != nil {
		return fmt.Errorf("resolving bot login: %w", err)
	}
	owners := fmt.Sprintf("approvers: []\nreviewers:\n  - %s\n", botLogin)
	if err := w.SCM.CommitFile(context.Background(),
		w.Install.ConfigOwner(), w.Install.ConfigRepo(),
		"OWNERS", "behaviour: add OWNERS file (reviewer only) for auth test",
		[]byte(owners)); err != nil {
		return fmt.Errorf("committing OWNERS file: %w", err)
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
	w.ScenarioStart = time.Now()
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

// disableOwnersAuth sets authorization.owners_file: false (removing the
// authorization block) in the enrolled repo's config.yaml.
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

// cleanupOwnersAuth removes the OWNERS file and authorization config
// block committed during the scenario so the repo slot is clean for
// the next scenario.
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
