package steps

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gaci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
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
	sc.Step(`^the OWNERS auth test posts "([^"]+)" on the issue$`, func(ctx context.Context, command string) (context.Context, error) {
		return ctx, whenSlashCommandPosted(world.FromContext(ctx), command)
	})
	sc.Step(`^the dispatch run logs do not contain "([^"]+)"$`, func(ctx context.Context, needle string) (context.Context, error) {
		return ctx, thenDispatchRunLogsDoNotContain(world.FromContext(ctx), needle)
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
	cfgPath := ".fullsend/config.yaml"
	cfgData, err := w.SCM.GetFileContent(context.Background(),
		w.Install.ConfigOwner(), w.Install.ConfigRepo(), cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	content := string(cfgData)
	if strings.Contains(content, "authorization:") {
		return fmt.Errorf("config.yaml already contains authorization block")
	}
	content += "\nauthorization:\n  owners_file: true\n"
	if err := w.SCM.CommitFile(context.Background(),
		w.Install.ConfigOwner(), w.Install.ConfigRepo(),
		cfgPath, "behaviour: enable OWNERS authorization",
		[]byte(content)); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	w.OwnersAuthActivated = true
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

// getWorkflowLogs downloads the full log archive for the workflow run
// via GitHub's API. The result can be megabytes; string matching on it
// is correct for assertion purposes but not a streaming grep.
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

func whenSlashCommandPosted(w *world.World, command string) error {
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	w.ScenarioStart = time.Now()
	_, err := w.SCM.AddComment(context.Background(),
		w.RepoOwner, w.RepoName, w.IssueNumber, command)
	return err
}

func thenDispatchRunLogsDoNotContain(w *world.World, needle string) error {
	run, err := waitForDispatchRunAnyConclusion(w)
	if err != nil {
		return err
	}
	gaciDriver, ok := w.CI.(*gaci.Driver)
	if !ok {
		return fmt.Errorf("dispatch log check requires GitHub Actions CI driver")
	}
	logs, err := gaciDriver.Client.GetWorkflowRunLogs(context.Background(),
		w.RepoOwner, w.Install.TriageWorkflowRepo(), run.ID)
	if err != nil {
		return fmt.Errorf("fetching dispatch run logs: %w", err)
	}
	if strings.Contains(logs, needle) {
		return fmt.Errorf("dispatch run logs unexpectedly contain %q", needle)
	}
	return nil
}

// waitForDispatchRunAnyConclusion polls for a completed fullsend.yaml
// workflow run triggered by issue_comment, accepting any conclusion
// (success or failure). This is needed because a /fs-code dispatch
// where the code job fails still has useful route-job logs to inspect.
func waitForDispatchRunAnyConclusion(w *world.World) (*forge.WorkflowRun, error) {
	gaciDriver, ok := w.CI.(*gaci.Driver)
	if !ok {
		return nil, fmt.Errorf("dispatch run wait requires GitHub Actions CI driver")
	}
	workflowFile := filepath.Base(w.Install.TriageWorkflowFile())
	ctx := context.Background()

	const poll = 5 * time.Second
	deadline := time.Now().Add(12 * time.Minute)

	for time.Now().Before(deadline) {
		time.Sleep(poll)
		runs, err := gaciDriver.Client.ListWorkflowRuns(ctx,
			w.RepoOwner, w.Install.TriageWorkflowRepo(), workflowFile)
		if err != nil {
			continue
		}
		for _, run := range runs {
			runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
			if parseErr != nil || runTime.Before(w.ScenarioStart) {
				continue
			}
			if run.Event != "issue_comment" {
				continue
			}
			if run.Status == "completed" {
				return &run, nil
			}
		}
	}

	return nil, fmt.Errorf("dispatch workflow (issue_comment) did not complete within deadline")
}

// cleanupOwnersAuth removes the OWNERS file and authorization config
// block committed during the scenario so the repo slot is clean for
// the next scenario.
func cleanupOwnersAuth(w *world.World) {
	ctx := context.Background()
	owner := w.Install.ConfigOwner()
	repo := w.Install.ConfigRepo()

	// Remove the authorization block from config.yaml first, so there's
	// no window where OWNERS auth is enabled with a stale OWNERS file.
	cfgPath := ".fullsend/config.yaml"
	cfgData, err := w.SCM.GetFileContent(ctx, owner, repo, cfgPath)
	if err == nil {
		content := string(cfgData)
		if strings.Contains(content, "authorization:") {
			cleaned := strings.ReplaceAll(content, "\nauthorization:\n  owners_file: true\n", "\n")
			if cleaned != content {
				if err := w.SCM.CommitFile(ctx, owner, repo,
					cfgPath, "behaviour: disable OWNERS authorization",
					[]byte(cleaned)); err != nil {
					worldLogf(w, "behaviour cleanup: disable OWNERS auth: %v", err)
				}
			} else {
				worldLogf(w, "behaviour cleanup: authorization block present but format doesn't match — manual cleanup may be needed")
			}
		}
	}

	// Overwrite OWNERS and OWNERS_ALIASES with empty content rather than
	// deleting — the SCM driver's CommitFile doesn't support file deletion.
	// The residual files are harmless: has_repo_permission won't match
	// anyone in empty lists, and the authorization block was already
	// removed above.
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
