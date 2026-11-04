package steps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

// harnessLogPollWindow bounds how long thenHarnessWorkflowFailsWithLog
// keeps fetching a failed run's logs that came back unusable (a fetch
// error, an empty body, or a fetch-failure note): logs fetched just after a
// run completes have come back unusable, cause not confirmed. Logs that are
// complete, or truncated at a size or page cap, are final and are not
// fetched again. Variables (not constants) so unit tests can shrink the
// window.
var harnessLogPollWindow = 60 * time.Second

var harnessLogPollInterval = 5 * time.Second

// thenHarnessWorkflowFailsWithLog waits for the agent's harness run to
// fail and checks that its logs contain needle, for a run that fails
// before any post-script could report on the forge (a harness refused at
// load, for example). Logs that could not be fetched, were empty or hold a
// fetch-failure note are fetched again until harnessLogPollWindow passes;
// the last fetch is saved as the debug artifact. When the text is missing,
// the error says whether the logs could not be fetched, were empty or were
// incomplete, so a log-fetch problem does not read as the run lacking the
// text.
func thenHarnessWorkflowFailsWithLog(ctx context.Context, w *world.World, agent, needle string) error {
	agent = strings.TrimSpace(agent)
	if w.ScenarioStart.IsZero() {
		return fmt.Errorf("no workflow trigger time recorded")
	}
	run, err := w.CI.WaitForFailedHarnessAgent(ctx, w.Org, w.RepoName, agent, w.ScenarioStart)
	if err != nil {
		return err
	}
	w.WorkflowRun = run
	deadline := time.Now().Add(harnessLogPollWindow)
	for {
		fetchCtx, cancel := context.WithTimeout(ctx, logFetchTimeout)
		logs, fetchErr := w.CI.GetRunLogs(fetchCtx, w.Org, w.RepoName, run.ID)
		cancel()
		retry, checkErr := checkHarnessRunLogs(run, agent, needle, logs, fetchErr)
		if !retry || time.Now().After(deadline) {
			saveFetchedWorkflowRunLogs(w, agent, run, logs, fetchErr)
			return checkErr
		}
		select {
		case <-ctx.Done():
			saveFetchedWorkflowRunLogs(w, agent, run, logs, fetchErr)
			return fmt.Errorf("%w (stopped waiting for the logs: %w)", checkErr, ctx.Err())
		case <-time.After(harnessLogPollInterval):
		}
	}
}

// checkHarnessRunLogs reports whether logs hold needle, and whether the
// logs were unusable in a way a later fetch can change (worth fetching
// again) rather than final. A truncation note is final: the client cuts
// each log at a fixed size and reads one page of jobs.
func checkHarnessRunLogs(run *forge.WorkflowRun, agent, needle, logs string, fetchErr error) (retry bool, err error) {
	switch {
	case fetchErr != nil:
		return true, fmt.Errorf("harness %q run %d failed, but its logs could not be fetched, so %q was not checked (url=%s): %w", agent, run.ID, needle, run.HTMLURL, fetchErr)
	case strings.Contains(logs, needle):
		return false, nil
	case strings.TrimSpace(logs) == "":
		return true, fmt.Errorf("harness %q run %d failed, but the forge returned no log output, so %q was not checked (url=%s)", agent, run.ID, needle, run.HTMLURL)
	}
	if note := partialLogMarker(logs); note.marker != "" {
		return !note.truncation, fmt.Errorf("harness %q run %d failed and its logs do not contain %q, but they are incomplete (they hold %q), so the text may be in the part not fetched (url=%s)", agent, run.ID, needle, note.marker, run.HTMLURL)
	}
	return false, fmt.Errorf("harness %q run %d failed, but its logs do not contain %q (url=%s)", agent, run.ID, needle, run.HTMLURL)
}
