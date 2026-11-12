package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestRunnerSecretsBundle(t *testing.T) {
	bundle, value, err := runnerSecretsBundle("PROBE_TOKEN")
	require.NoError(t, err)
	var got map[string]string
	require.NoError(t, json.Unmarshal([]byte(bundle), &got))
	assert.Equal(t, map[string]string{"PROBE_TOKEN": value}, got)
	assert.GreaterOrEqual(t, len(value), 8, "the runner refuses values too short to redact")

	_, other, err := runnerSecretsBundle("PROBE_TOKEN")
	require.NoError(t, err)
	assert.NotEqual(t, value, other, "each call draws a fresh value")
}

func TestGivenRepositoryRunnerSecret(t *testing.T) {
	fc := forge.NewFakeClient()
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc)}
	require.NoError(t, givenRepositoryRunnerSecret(w, "PROBE_TOKEN"))
	require.Len(t, fc.CreatedSecrets, 1)
	rec := fc.CreatedSecrets[0]
	assert.Equal(t, "org", rec.Owner)
	assert.Equal(t, "repo", rec.Repo)
	assert.Equal(t, forge.SecretGitHubRunnerSecrets, rec.Name)
	var got map[string]string
	require.NoError(t, json.Unmarshal([]byte(rec.Value), &got))
	assert.Contains(t, got, "PROBE_TOKEN")
	assert.True(t, w.RunnerSecretsSecretSet)

	require.NoError(t, deleteRunnerSecretsSecret(w))
	assert.Equal(t, []forge.SecretRecord{{Owner: "org", Repo: "repo", Name: forge.SecretGitHubRunnerSecrets}},
		stripSecretValues(fc.DeletedSecrets), "cleanup deletes the secret")
	assert.False(t, w.RunnerSecretsSecretSet)
	require.NoError(t, deleteRunnerSecretsSecret(w), "a second cleanup is a no-op")
	assert.Len(t, fc.DeletedSecrets, 1)
}

func TestGivenRepositoryRunnerSecret_RedactsEveryFormOfTheBundle(t *testing.T) {
	fc := forge.NewFakeClient()
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc)}
	require.NoError(t, givenRepositoryRunnerSecret(w, "PROBE_TOKEN"))
	require.Len(t, fc.CreatedSecrets, 1)
	bundle := fc.CreatedSecrets[0].Value
	var got map[string]string
	require.NoError(t, json.Unmarshal([]byte(bundle), &got))
	value := got["PROBE_TOKEN"]
	require.NotEmpty(t, value)

	encoded, err := json.Marshal(bundle)
	require.NoError(t, err)
	// Only the value is registered; every form of the bundle holds it
	// verbatim, so each is masked.
	for _, form := range []string{value, bundle, strconv.Quote(bundle), string(encoded)} {
		assert.NotContains(t, RedactText("log: "+form), value, "form %q", form)
	}
}

func TestGivenRepositoryRunnerSecret_FailedCreateStillCleansUp(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"CreateRepoSecret": errors.New("boom")}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc)}
	require.ErrorContains(t, givenRepositoryRunnerSecret(w, "PROBE_TOKEN"), "boom")
	assert.True(t, w.RunnerSecretsSecretSet, "the flag is set before the create, so a partly failed create is still deleted")

	CleanupScenario(w)
	assert.Equal(t, []forge.SecretRecord{{Owner: "org", Repo: "repo", Name: forge.SecretGitHubRunnerSecrets}},
		stripSecretValues(fc.DeletedSecrets), "CleanupScenario deletes the secret")
	assert.False(t, w.RunnerSecretsSecretSet)
}

func TestGivenRepositoryRunnerSecret_SecondCallIsRefused(t *testing.T) {
	fc := forge.NewFakeClient()
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc)}
	require.NoError(t, givenRepositoryRunnerSecret(w, "FIRST_TOKEN"))
	require.ErrorContains(t, givenRepositoryRunnerSecret(w, "SECOND_TOKEN"), "already set")
	require.Len(t, fc.CreatedSecrets, 1, "the second call writes nothing")
	var got map[string]string
	require.NoError(t, json.Unmarshal([]byte(fc.CreatedSecrets[0].Value), &got))
	assert.Contains(t, got, "FIRST_TOKEN", "the first name is kept")
}

// guardedSecretClient runs the request context's retry guard before it
// writes a secret, as the GitHub client does before its PUT.
type guardedSecretClient struct {
	*forge.FakeClient
	writes int
}

func (c *guardedSecretClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if guard := forge.RetryGuard(ctx); guard != nil {
		if err := guard(ctx); err != nil {
			return err
		}
	}
	c.writes++
	return c.FakeClient.CreateRepoSecret(ctx, owner, repo, name, value)
}

func TestGivenRepositoryRunnerSecret_LostLeaseStopsTheWrite(t *testing.T) {
	client := &guardedSecretClient{FakeClient: forge.NewFakeClient()}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(client)}
	w.LeaseOwned = func(context.Context) (bool, error) { return false, nil }

	err := givenRepositoryRunnerSecret(w, "PROBE_TOKEN")
	require.ErrorIs(t, err, errLeaseLost)
	assert.Zero(t, client.writes, "no secret is written to a slot this scenario no longer holds")
}

func TestGivenRepositoryRunnerSecret_RequiresGitHub(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo", SCM: &fakeCleanupSCM{}}
	assert.ErrorContains(t, givenRepositoryRunnerSecret(w, "PROBE_TOKEN"), "require the GitHub SCM driver")
	assert.False(t, w.RunnerSecretsSecretSet)

	assert.ErrorContains(t, givenRepositoryRunnerSecret(&world.World{}, "PROBE_TOKEN"), "no repo configured")
}

func TestDeleteRunnerSecretsSecret_NotFoundIsFine(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"DeleteRepoSecret": fmt.Errorf("%w: secret", forge.ErrNotFound)}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc), RunnerSecretsSecretSet: true}
	require.NoError(t, deleteRunnerSecretsSecret(w), "an already-deleted secret is not an error")
	assert.False(t, w.RunnerSecretsSecretSet)
}

func TestDeleteRunnerSecretsSecret_ErrorKeepsFlagForRetry(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"DeleteRepoSecret": errors.New("boom")}
	w := &world.World{Org: "org", RepoName: "repo", SCM: scmgh.New(fc), RunnerSecretsSecretSet: true}
	require.ErrorContains(t, deleteRunnerSecretsSecret(w), "boom")
	assert.True(t, w.RunnerSecretsSecretSet, "a failed delete is retried by cleanupRetry")
}

// fakeFailedRunLogsCI implements WaitForFailedHarnessAgent and GetRunLogs;
// other ci.Driver methods panic when called.
type fakeFailedRunLogsCI struct {
	ci.Driver

	run      *forge.WorkflowRun
	waitErr  error
	logs     string
	logsErr  error
	logCalls int
	// logsSeq, when set, is returned one entry per call (the last repeats).
	logsSeq []string
}

func (f *fakeFailedRunLogsCI) WaitForFailedHarnessAgent(context.Context, string, string, string, time.Time) (*forge.WorkflowRun, error) {
	return f.run, f.waitErr
}

func (f *fakeFailedRunLogsCI) GetRunLogs(context.Context, string, string, int) (string, error) {
	f.logCalls++
	if len(f.logsSeq) > 0 {
		i := min(f.logCalls, len(f.logsSeq)) - 1
		return f.logsSeq[i], nil
	}
	return f.logs, f.logsErr
}

// shrinkHarnessLogPoll makes the log poll retry at once and give up fast.
func shrinkHarnessLogPoll(t *testing.T, window time.Duration) {
	t.Helper()
	oldWindow, oldInterval := harnessLogPollWindow, harnessLogPollInterval
	harnessLogPollWindow, harnessLogPollInterval = window, time.Millisecond
	t.Cleanup(func() { harnessLogPollWindow, harnessLogPollInterval = oldWindow, oldInterval })
}

func TestThenHarnessWorkflowFailsWithLog(t *testing.T) {
	artifacts := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifacts)
	newWorld := func(c *fakeFailedRunLogsCI) *world.World {
		return &world.World{Org: "org", RepoName: "repo", ScenarioStart: time.Now(), CI: c}
	}
	logs := "Error: invalid harness: overlays[0] references runner secret(s) PROBE_TOKEN, but its when: reads more than runtime.forge and config"
	failedRun := func() *forge.WorkflowRun { return &forge.WorkflowRun{ID: 7, Status: "completed"} }

	c := &fakeFailedRunLogsCI{run: failedRun(), logs: logs}
	w := newWorld(c)
	require.NoError(t, thenHarnessWorkflowFailsWithLog(context.Background(), w, "probe", "references runner secret(s) PROBE_TOKEN"))
	assert.Equal(t, 7, w.WorkflowRun.ID)
	assert.Equal(t, 1, c.logCalls, "logs that hold the text are fetched once")
	assert.FileExists(t, filepath.Join(artifacts, "debug-probe-run-7", "workflow-logs.txt"))

	c.logCalls = 0
	err := thenHarnessWorkflowFailsWithLog(context.Background(), w, "probe", "some other failure")
	assert.ErrorContains(t, err, "its logs do not contain")
	assert.NotContains(t, err.Error(), "incomplete")
	assert.Equal(t, 1, c.logCalls, "complete logs without the text are final, not fetched again")

	w = newWorld(&fakeFailedRunLogsCI{waitErr: errors.New("run 7 concluded successfully; expected failure")})
	assert.ErrorContains(t, thenHarnessWorkflowFailsWithLog(context.Background(), w, "probe", "x"), "concluded successfully")

	w = &world.World{CI: &fakeFailedRunLogsCI{}}
	assert.ErrorContains(t, thenHarnessWorkflowFailsWithLog(context.Background(), w, "probe", "x"), "no workflow trigger time")
}

// A log-fetch problem is reported as one, not as the run lacking the text.
func TestThenHarnessWorkflowFailsWithLog_FetchProblemIsNamed(t *testing.T) {
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())
	// The window must survive a scheduler or GC pause between the deadline
	// set and the first check, so the retryable cases still fetch again on
	// a loaded runner; the 1ms interval keeps the test fast regardless.
	shrinkHarnessLogPoll(t, 200*time.Millisecond)
	cases := []struct {
		name    string
		logs    string
		logsErr error
		want    string
		final   bool
	}{
		{"fetch error", "", errors.New("api down"), "its logs could not be fetched", false},
		{"empty logs", "  \n", nil, "the forge returned no log output", false},
		// Each fetch-failure note, as the forge clients write it, is retried.
		{"job log unavailable", "setup ok\n[logs unavailable: HTTP 404]\n", nil, `they are incomplete (they hold "[logs unavailable:")`, false},
		{"job log fetch failed", "setup ok\n[failed to fetch logs: boom]\n", nil, `they are incomplete (they hold "[failed to fetch logs:")`, false},
		{"job log read failed", "setup ok\n[failed to read logs: EOF]\n", nil, `they are incomplete (they hold "[failed to read logs:")`, false},
		{"trace fetch failed", "=== Job 1 (x): error fetching trace: boom ===\n", nil, `they are incomplete (they hold "error fetching trace:")`, false},
		{"trace read failed", "=== Job 1 (x): error reading trace: EOF ===\n", nil, `they are incomplete (they hold "error reading trace:")`, false},
		// Each truncation note, as the forge clients write it, is final.
		{"job log truncated", "setup ok\n[log truncated: job 1 exceeds 1048576 bytes]\n", nil, `they are incomplete (they hold "[log truncated:")`, true},
		{"job list truncated", "setup ok\n[job list truncated: 100 of 120 jobs included]\n", nil, `they are incomplete (they hold "[job list truncated:")`, true},
		{"trace truncated", "=== Job 1 (x): trace truncated at 10 bytes ===\n", nil, `they are incomplete (they hold "trace truncated at")`, true},
		{"aggregate trace limit", "=== aggregate trace limit (1 bytes) reached, skipping remaining jobs ===\n", nil, `they are incomplete (they hold "aggregate trace limit")`, true},
		// A fetch-failure note decides over a truncation note in the same
		// logs: the failed job's log may arrive on a later fetch.
		{"failed and truncated", "[log truncated: job 1 exceeds 1048576 bytes]\n[logs unavailable: HTTP 404]\n", nil, `they are incomplete (they hold "[logs unavailable:")`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeFailedRunLogsCI{run: &forge.WorkflowRun{ID: 7, Status: "completed"}, logs: tc.logs, logsErr: tc.logsErr}
			w := &world.World{Org: "org", RepoName: "repo", ScenarioStart: time.Now(), CI: fc}
			err := thenHarnessWorkflowFailsWithLog(context.Background(), w, "probe", "needle")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			if tc.logsErr != nil {
				assert.ErrorIs(t, err, tc.logsErr)
			}
			assert.NotContains(t, err.Error(), "its logs do not contain \"needle\" (url")
			if tc.final {
				assert.Equal(t, 1, fc.logCalls, "a truncation note is final")
			} else {
				assert.Greater(t, fc.logCalls, 1, "unusable logs are fetched again until the window passes")
			}
		})
	}
}

// A truncation note is final: the same run's logs are cut at the same cap on
// every fetch, so the step does not wait out the window.
func TestThenHarnessWorkflowFailsWithLog_TruncationIsFinal(t *testing.T) {
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())
	shrinkHarnessLogPoll(t, time.Minute)
	c := &fakeFailedRunLogsCI{run: &forge.WorkflowRun{ID: 7, Status: "completed"},
		logs: "setup ok\n[log truncated: job 1 exceeds 1048576 bytes]\n"}
	w := &world.World{Org: "org", RepoName: "repo", ScenarioStart: time.Now(), CI: c}
	err := thenHarnessWorkflowFailsWithLog(context.Background(), w, "probe", "needle")
	assert.ErrorContains(t, err, "they are incomplete")
	assert.Equal(t, 1, c.logCalls)
}

// A cancelled scenario stops the wait and keeps the reason the logs were
// unusable, and the last fetch is still saved.
func TestThenHarnessWorkflowFailsWithLog_CancelKeepsDiagnosis(t *testing.T) {
	artifacts := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifacts)
	shrinkHarnessLogPoll(t, time.Minute)
	harnessLogPollInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	c := &fakeFailedRunLogsCI{run: &forge.WorkflowRun{ID: 7, Status: "completed"}, logs: "setup ok\n[logs unavailable: HTTP 404]\n"}
	w := &world.World{Org: "org", RepoName: "repo", ScenarioStart: time.Now(), CI: c}
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	err := thenHarnessWorkflowFailsWithLog(ctx, w, "probe", "needle")
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "they are incomplete")
	assert.FileExists(t, filepath.Join(artifacts, "debug-probe-run-7", "workflow-logs.txt"))
}

// Logs that are not ready when the run completes are fetched again, and the
// text found on a later fetch passes the step; the saved artifact is the last
// fetch.
func TestThenHarnessWorkflowFailsWithLog_RetriesUntilLogsArrive(t *testing.T) {
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())
	shrinkHarnessLogPoll(t, time.Minute)
	c := &fakeFailedRunLogsCI{run: &forge.WorkflowRun{ID: 7, Status: "completed"},
		logsSeq: []string{"", "setup ok\n[logs unavailable: HTTP 404]\n", "Error: needle here"}}
	w := &world.World{Org: "org", RepoName: "repo", ScenarioStart: time.Now(), CI: c}
	artifacts := os.Getenv("BEHAVIOUR_ARTIFACT_DIR")
	require.NoError(t, thenHarnessWorkflowFailsWithLog(context.Background(), w, "probe", "needle"))
	assert.Equal(t, 3, c.logCalls)
	saved, err := os.ReadFile(filepath.Join(artifacts, "debug-probe-run-7", "workflow-logs.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(saved), "needle here")
}
