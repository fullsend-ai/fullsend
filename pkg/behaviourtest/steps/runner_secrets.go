package steps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/forge"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerRunnerSecretSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the repository has the runner secret "([A-Z][A-Z0-9_]*)"$`, func(ctx context.Context, name string) (context.Context, error) {
		return ctx, givenRepositoryRunnerSecret(world.FromContext(ctx), name)
	})
	sc.Step(`^the harness "([^"]+)" workflow fails with "([^"]+)" in its logs$`, func(ctx context.Context, agent, needle string) (context.Context, error) {
		return ctx, thenHarnessWorkflowFailsWithLog(ctx, world.FromContext(ctx), agent, needle)
	})
}

// runnerSecretsBundle returns the FULLSEND_GITHUB_RUNNER_SECRETS value
// holding one name with a fresh random value, and that value.
func runnerSecretsBundle(name string) (bundle, value string, err error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generating runner secret value: %w", err)
	}
	value = "behaviour-" + hex.EncodeToString(buf)
	data, err := json.Marshal(map[string]string{name: value})
	if err != nil {
		return "", "", err
	}
	return string(data), value, nil
}

// givenRepositoryRunnerSecret sets the enrolled repo's
// FULLSEND_GITHUB_RUNNER_SECRETS secret to an object holding name with a
// random value, registered for redaction. The channel exists on GitHub
// only. CleanupScenario deletes the secret. A second call in one scenario
// is an error: the write replaces the whole object, so it would drop the
// first name.
func givenRepositoryRunnerSecret(w *world.World, name string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before setting a runner secret")
	}
	if w.RunnerSecretsSecretSet {
		return fmt.Errorf("this scenario already set %s; the step sets the whole object to one name, so a second call would drop the first", forge.SecretGitHubRunnerSecrets)
	}
	ghDriver, ok := w.SCM.(*scmgh.Driver)
	if !ok {
		return fmt.Errorf("runner secrets require the GitHub SCM driver")
	}
	bundle, value, err := runnerSecretsBundle(name)
	if err != nil {
		return err
	}
	// The value is "behaviour-" and hex, which no quoting escapes, so each
	// form of the bundle holds it verbatim and registering it masks them.
	registerSecretForms(value)
	// Mark first, so cleanup still runs after a partly failed create.
	w.RunnerSecretsSecretSet = true
	if err := ghDriver.Client.CreateRepoSecret(mutationCtx(w), w.Org, w.RepoName, forge.SecretGitHubRunnerSecrets, bundle); err != nil {
		return fmt.Errorf("setting repository secret %s: %w", forge.SecretGitHubRunnerSecrets, err)
	}
	return nil
}

// deleteRunnerSecretsSecret deletes the repository secret that
// givenRepositoryRunnerSecret set. A secret that is already gone is not an
// error.
func deleteRunnerSecretsSecret(w *world.World) error {
	if !w.RunnerSecretsSecretSet {
		return nil
	}
	ghDriver, ok := w.SCM.(*scmgh.Driver)
	if !ok {
		return fmt.Errorf("runner secrets require the GitHub SCM driver")
	}
	err := ghDriver.Client.DeleteRepoSecret(mutationCtx(w), w.Org, w.RepoName, forge.SecretGitHubRunnerSecrets)
	if err != nil && !forge.IsNotFound(err) {
		return err
	}
	w.RunnerSecretsSecretSet = false
	return nil
}

// thenHarnessWorkflowFailsWithLog waits for the agent's harness run to
// fail and checks that its logs contain needle, for a run that fails
// before any post-script could report on the forge (a harness refused at
// load, for example). Logs that could not be fetched, were empty or hold a
// fetch-failure note are fetched again until logPollWindow passes
// (fetchUsableSubjectLogs, shared with the workflow-logs-contain and
// workflow-dispatch failure steps);
// the last fetch is saved as the debug artifact, unless it is partial and a
// complete snapshot is already saved for the run. When the text is missing,
// the error says whether the logs could not be fetched, were empty or were
// incomplete, so a log-fetch problem does not read as the run lacking the
// text.
func thenHarnessWorkflowFailsWithLog(ctx context.Context, w *world.World, agent, needle string) error {
	agent = strings.TrimSpace(agent)
	if w.ScenarioStart.IsZero() {
		return fmt.Errorf("no workflow trigger time recorded")
	}
	// The shared guard in checkRunLogsNeedle would catch a blank needle
	// only after this step's harness wait — up to dispatchWait — so
	// reject it here, before the run is resolved, the way the sibling
	// log steps do.
	if err := rejectBlankNeedle(needle); err != nil {
		return err
	}
	run, err := w.CI.WaitForFailedHarnessAgent(ctx, w.Org, w.RepoName, agent, w.ScenarioStart)
	if err != nil {
		return err
	}
	w.WorkflowRun = run
	// A harness wait step re-resolves the harness run after a dispatch:
	// harness steps reading w.WorkflowRun are legal again from here on.
	w.DispatchedWorkflow = false
	subject := fmt.Sprintf("harness %q", agent)
	// The saved artifact keeps the bare agent name, the directory the
	// "completes successfully" step saves the same run to.
	logs, fetchErr, stopped := fetchUsableSubjectLogs(ctx, w, subject, agent, needle, run)
	_, checkErr := checkRunLogsNeedle(run, subject, needle, logs, fetchErr)
	if stopped != nil {
		return fmt.Errorf("%w (stopped waiting for the logs: %w)", checkErr, stopped)
	}
	return checkErr
}
