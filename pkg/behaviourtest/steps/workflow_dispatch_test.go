package steps

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeDispatcherCI implements ci.Driver (through the embedded nil
// interface, never called), ci.WorkflowDispatcher and the harness
// failed-agent wait, so both the dispatch steps and the harness log
// steps run against it.
type fakeDispatcherCI struct {
	ci.Driver

	dispatchErr error
	file, ref   string
	inputs      map[string]string
	floor       int

	run      *forge.WorkflowRun
	waitErr  error
	after    time.Time
	event    string
	minRunID int
	logs     string
	logsErr  error
	// logsSeq, when set, is returned one entry per GetRunLogs call (the last
	// repeats); logCalls counts the calls.
	logsSeq  []string
	logCalls int
	// errSeq, when set, is the error returned on each call (nil once past its end).
	errSeq []error
}

func (f *fakeDispatcherCI) DispatchWorkflow(_ context.Context, _, _, file, ref string, inputs map[string]string) (int, error) {
	f.file, f.ref, f.inputs = file, ref, inputs
	return f.floor, f.dispatchErr
}

func (f *fakeDispatcherCI) WaitForWorkflowConclusion(_ context.Context, _, _, _ string, after time.Time, event string, minRunID int) (*forge.WorkflowRun, error) {
	f.after, f.event, f.minRunID = after, event, minRunID
	return f.run, f.waitErr
}

func (f *fakeDispatcherCI) WaitForFailedHarnessAgent(context.Context, string, string, string, time.Time) (*forge.WorkflowRun, error) {
	return f.run, f.waitErr
}

func (f *fakeDispatcherCI) GetRunLogs(context.Context, string, string, int) (string, error) {
	f.logCalls++
	if f.logCalls <= len(f.errSeq) && f.errSeq[f.logCalls-1] != nil {
		return "", f.errSeq[f.logCalls-1]
	}
	if len(f.logsSeq) > 0 {
		return f.logsSeq[min(f.logCalls, len(f.logsSeq))-1], nil
	}
	return f.logs, f.logsErr
}

// defaultBranchSCM implements scm.Driver (through the embedded nil
// interface) with only GetDefaultBranch.
type defaultBranchSCM struct {
	scm.Driver
	err error
}

func (f *defaultBranchSCM) GetDefaultBranch(context.Context, string, string) (string, error) {
	return "main", f.err
}

func TestWhenWorkflowDispatchedWithPayload(t *testing.T) {
	fake := &fakeDispatcherCI{}
	w := &world.World{Org: "org", RepoName: "repo", CI: fake, SCM: &defaultBranchSCM{}}
	before := time.Now()
	require.NoError(t, whenWorkflowDispatchedWithPayload(context.Background(), w, "prioritize.yml", "  {\"issue\":{\"number\":1}}\n"))
	assert.Equal(t, "prioritize.yml", fake.file)
	assert.Equal(t, "main", fake.ref)
	assert.Equal(t, map[string]string{
		"event_type":    "issues",
		"source_repo":   "org/repo",
		"event_payload": `{"issue":{"number":1}}`,
	}, fake.inputs)
	// The trigger time moves forward to the dispatch and is not rewound
	// by the skew buffer, truncated to second precision like every
	// trigger step (forge created_at has no sub-second precision, #7957):
	// other steps filter on ScenarioStart as is.
	assert.True(t, w.ScenarioStart.After(before.Add(-time.Second)), "the trigger time moves to the dispatch, not before it")
	assert.False(t, w.ScenarioStart.After(time.Now()), "the trigger time is the dispatch")
	assert.Equal(t, w.ScenarioStart, w.ScenarioStart.Truncate(time.Second), "the trigger time is truncated to second precision")
	assert.Equal(t, "prioritize.yml", w.DispatchedWorkflowFile, "the dispatched file is recorded for the failure step's fail-fast")
	assert.Equal(t, w.ScenarioStart, w.DispatchedWorkflowAt, "the dispatch time is recorded for the failure step's window")
}

// The dispatch step records the run-ID floor the driver returned, and
// the failure step passes it to the wait: only a run above the floor is
// the dispatched run, so a run of the file that was already listed
// before the dispatch can never become the asserted run.
func TestWorkflowDispatchSteps_RunFloor(t *testing.T) {
	fake := &fakeDispatcherCI{floor: 9, run: &forge.WorkflowRun{ID: 11, Status: "completed", Conclusion: "failure"}, logs: "needle"}
	w := &world.World{Org: "org", RepoName: "repo", CI: fake, SCM: &defaultBranchSCM{}}
	require.NoError(t, whenWorkflowDispatchedWithPayload(context.Background(), w, "prioritize.yml", "{}"))
	assert.Equal(t, 9, w.DispatchedWorkflowRunFloor, "the dispatch step records the driver's run-ID floor")
	require.NoError(t, thenWorkflowFailsLogging(context.Background(), w, "prioritize.yml", "needle"))
	assert.Equal(t, 9, fake.minRunID, "the failure step's wait accepts only runs above the floor")
}

func TestWhenWorkflowDispatchedWithPayload_Errors(t *testing.T) {
	err := whenWorkflowDispatchedWithPayload(context.Background(), &world.World{CI: &fakeDispatcherCI{}}, "prioritize.yml", "{}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repository leased")

	err = whenWorkflowDispatchedWithPayload(context.Background(), &world.World{Org: "org", RepoName: "repo", CI: &fakeListerCI{}}, "prioritize.yml", "{}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ci.WorkflowDispatcher")

	err = whenWorkflowDispatchedWithPayload(context.Background(), &world.World{Org: "org", RepoName: "repo", CI: &fakeDispatcherCI{}, SCM: &defaultBranchSCM{err: errors.New("boom")}}, "prioritize.yml", "{}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default branch")

	err = whenWorkflowDispatchedWithPayload(context.Background(), &world.World{Org: "org", RepoName: "repo", CI: &fakeDispatcherCI{dispatchErr: errors.New("403")}, SCM: &defaultBranchSCM{}}, "prioritize.yml", "{}")
	require.EqualError(t, err, "403")
}

func TestThenWorkflowFailsLogging(t *testing.T) {
	shrinkLogPoll(t, 20*time.Millisecond)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const needle = "invalid _normalized_event"
	failed := func() *forge.WorkflowRun {
		return &forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "failure"}
	}
	for name, tc := range map[string]struct {
		fake    *fakeDispatcherCI
		wantErr string
	}{
		"failed run logging the needle":    {fake: &fakeDispatcherCI{run: failed(), logs: "x " + needle + " y"}},
		"timed out run logging the needle": {fake: &fakeDispatcherCI{run: &forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "timed_out"}, logs: "x " + needle + " y"}},
		"startup failure run":              {fake: &fakeDispatcherCI{run: &forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "startup_failure"}, logs: needle}, wantErr: "failed before any job started"},
		"successful run":                   {fake: &fakeDispatcherCI{run: &forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "success"}, logs: needle}, wantErr: `concluded with "success"; want a failure`},
		"cancelled run":                    {fake: &fakeDispatcherCI{run: &forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "cancelled"}, logs: needle}, wantErr: `concluded with "cancelled"; want a failure`},
		"failed run without the needle":    {fake: &fakeDispatcherCI{run: failed(), logs: "other"}, wantErr: `failed, but its logs do not contain "invalid _normalized_event" (url=`},
		"logs incomplete":                  {fake: &fakeDispatcherCI{run: failed(), logs: "other\n[failed to fetch logs: HTTP 502]"}, wantErr: `but they are incomplete (they hold "[failed to fetch logs:")`},
		"logs empty":                       {fake: &fakeDispatcherCI{run: failed(), logs: " \n"}, wantErr: "returned no log output"},
		"logs truncated":                   {fake: &fakeDispatcherCI{run: failed(), logs: "other\n[log truncated: job 1 exceeds 1048576 bytes]"}, wantErr: `but they are incomplete (they hold "[log truncated:")`},
		"logs fetch error":                 {fake: &fakeDispatcherCI{run: failed(), logsErr: errors.New("502")}, wantErr: "could not be fetched"},
		"wait error":                       {fake: &fakeDispatcherCI{waitErr: errors.New("was not dispatched")}, wantErr: "was not dispatched"},
	} {
		t.Run(name, func(t *testing.T) {
			w := &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: tc.fake, DispatchedWorkflowFile: "prioritize.yml", DispatchedWorkflowAt: start}
			err := thenWorkflowFailsLogging(context.Background(), w, "prioritize.yml", needle)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, start.Add(-issueOpenDrainSkewBuffer), tc.fake.after, "the wait window opens a skew buffer before the dispatch time")
			assert.Equal(t, start, w.ScenarioStart, "the wait leaves the trigger time alone")
			assert.Equal(t, "workflow_dispatch", tc.fake.event)
			assert.Equal(t, 7, w.WorkflowRun.ID)
		})
	}

	err := thenWorkflowFailsLogging(context.Background(), &world.World{CI: &fakeDispatcherCI{}}, "prioritize.yml", needle)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repository leased")

	// Without a dispatch step the failure step fails fast instead of
	// waiting out the whole-scenario window on a run that never comes.
	err = thenWorkflowFailsLogging(context.Background(), &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: &fakeDispatcherCI{}}, "prioritize.yml", needle)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workflow was dispatched in this scenario")

	// A failure assertion naming a different file than the one dispatched
	// fails fast instead of burning the detection window on it.
	err = thenWorkflowFailsLogging(context.Background(), &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: &fakeDispatcherCI{}, DispatchedWorkflowFile: "prioritize.yml", DispatchedWorkflowAt: start}, "fullsend.yaml", needle)
	require.Error(t, err)
	assert.EqualError(t, err, `no "fullsend.yaml" workflow was dispatched in this scenario; "prioritize.yml" was`)

	// The comparison is base-normalized: the dispatch step records the
	// file as written while the driver matches on its base name, so a
	// path-prefixed assertion still matches the recorded bare file.
	fake := &fakeDispatcherCI{run: failed(), logs: "x " + needle + " y"}
	require.NoError(t, thenWorkflowFailsLogging(context.Background(), &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: fake, DispatchedWorkflowFile: "prioritize.yml", DispatchedWorkflowAt: start}, ".github/workflows/prioritize.yml", needle))

	// A trigger step between the dispatch and the failure assertion
	// moves ScenarioStart forward; the detection window still opens at
	// the recorded dispatch time, so the dispatched run stays in it.
	fake = &fakeDispatcherCI{run: failed(), logs: "x " + needle + " y"}
	w := &world.World{Org: "org", RepoName: "repo", ScenarioStart: start.Add(10 * time.Minute), CI: fake, DispatchedWorkflowFile: "prioritize.yml", DispatchedWorkflowAt: start}
	require.NoError(t, thenWorkflowFailsLogging(context.Background(), w, "prioritize.yml", needle))
	assert.Equal(t, start.Add(-issueOpenDrainSkewBuffer), fake.after, "the wait window opens at the recorded dispatch time, not the moved trigger time")

	err = thenWorkflowFailsLogging(context.Background(), &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: &fakeListerCI{}, DispatchedWorkflowFile: "prioritize.yml", DispatchedWorkflowAt: start}, "prioritize.yml", needle)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ci.WorkflowDispatcher")
}

// A blank needle (an authoring mistake in the step's quoted text) would
// match any log output with a space in it, so the step rejects it before
// the run is resolved, ahead of the wait that would otherwise burn the
// dispatch budget on it.
func TestThenWorkflowFailsLogging_EmptyNeedle(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, needle := range map[string]string{
		"empty needle":      "",
		"whitespace needle": " \t",
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeDispatcherCI{run: &forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "failure"}, logs: "x y"}
			w := &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: fake, DispatchedWorkflowFile: "prioritize.yml", DispatchedWorkflowAt: start}
			err := thenWorkflowFailsLogging(context.Background(), w, "prioritize.yml", needle)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "the expected log text is empty; write the text to find in the run's logs")
		})
	}
	// The guard fires before the run is resolved: with a CI driver that
	// lacks ci.WorkflowDispatcher, a blank needle is still the error
	// named, not the missing interface (and never the wait).
	err := thenWorkflowFailsLogging(context.Background(), &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: &fakeListerCI{}, DispatchedWorkflowFile: "prioritize.yml", DispatchedWorkflowAt: start}, "prioritize.yml", " ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the expected log text is empty")
	assert.NotContains(t, err.Error(), "ci.WorkflowDispatcher")
}

// A CI driver without ci.WorkflowDispatcher fails both dispatch steps with
// an error that names the missing interface, so a scenario author can tell
// why the steps do not run on that driver.
func TestWorkflowDispatchSteps_RequireWorkflowDispatcher(t *testing.T) {
	w := &world.World{Org: "org", RepoName: "repo", ScenarioStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), CI: &fakeListerCI{}}

	err := whenWorkflowDispatchedWithPayload(context.Background(), w, "prioritize.yml", "{}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ci.WorkflowDispatcher")

	// The failure step checks that a dispatch ran before it resolves the
	// driver, so arm the recorded file the dispatch step of a capable
	// driver would have set; the driver check is what this test exercises.
	w.DispatchedWorkflowFile = "prioritize.yml"
	w.DispatchedWorkflowAt = w.ScenarioStart
	err = thenWorkflowFailsLogging(context.Background(), w, "prioritize.yml", "needle")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ci.WorkflowDispatcher")
}

// Harness steps that read w.WorkflowRun with harness semantics — or
// scope a run window from ScenarioStart, which the dispatch step moves —
// fail fast after the dispatch step ran, instead of asserting on the
// dispatched caller workflow's run, counting against a moved window or
// skipping their settle.
func TestWorkflowDispatchSteps_HarnessStepsFailFast(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	w := &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: &fakeDispatcherCI{}, DispatchedWorkflow: true}

	steps := map[string]func() error{
		"agent did not run":        func() error { return thenHarnessAgentDidNotRun(w, "agent") },
		"workflow logs contain":    func() error { return thenHarnessWorkflowLogsContain(context.Background(), w, "agent", "needle", true) },
		"triage workflow complete": func() error { return ensureTriageWorkflowComplete(w) },
		"logs register persona":    func() error { return thenHarnessLogsRegisterPersona(w, "pi", "want", "not-want") },
		"gateway provider cleaned": func() error { return thenGatewayProviderCleanedUp(w) },
		"run skipped by pre-script": func() error {
			return thenHarnessRunSkippedByPreScript(w, "agent")
		},
		"agent submitted review": func() error { return thenAgentSubmittedReview(w, "agent", "APPROVED") },
		"dispatched exactly":     func() error { return thenHarnessDispatchedExactly(w, "agent", 0) },
		"empty matrix":           func() error { return thenHarnessDispatchEmptyMatrix(w) },
		"agent triggered":        func() error { return thenAgentIsTriggered(w, "agent") },
		"owners dispatch run":    func() error { return thenDispatchRunDoesNotAuthorizeViaOwners(w) },
		"agent artifacts":        func() error { return ensureArtifacts(w) },
	}
	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			err := step()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "workflow dispatch step")
		})
	}
}

// fakeHarnessResolveCI answers the harness wait with a fixed successful
// run, serves its logs, and accepts artifact downloads, so a full
// "completes successfully" step can run against it.
type fakeHarnessResolveCI struct {
	ci.Driver
	run  *forge.WorkflowRun
	logs string
}

func (f *fakeHarnessResolveCI) WaitForHarnessAgent(context.Context, string, string, string, time.Time) (*forge.WorkflowRun, error) {
	return f.run, nil
}

func (f *fakeHarnessResolveCI) GetRunLogs(context.Context, string, string, int) (string, error) {
	return f.logs, nil
}

func (f *fakeHarnessResolveCI) DownloadNamedArtifactFromRun(context.Context, string, string, int, string, string) error {
	return nil
}

// A harness wait step re-resolves the harness run after a dispatch: it
// clears the guard, so harness steps reading w.WorkflowRun are legal
// again from there on — the escape hatch the guide documents.
func TestWorkflowDispatchSteps_HarnessWaitReResolvesRun(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	fake := &fakeHarnessResolveCI{
		run:  &forge.WorkflowRun{ID: 9, Status: "completed", Conclusion: "success"},
		logs: "x needle y",
	}
	w := &world.World{
		Org:                    "org",
		RepoName:               "repo",
		ScenarioStart:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		CI:                     fake,
		DispatchedWorkflow:     true,
		DispatchedWorkflowFile: "prioritize.yml",
		DispatchedWorkflowAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Logf:                   func(string, ...any) {},
	}

	require.NoError(t, thenHarnessWorkflowCompletes(w, "agent"))
	assert.False(t, w.DispatchedWorkflow, "a harness wait step re-resolves the harness run and clears the guard")
	assert.Equal(t, 9, w.WorkflowRun.ID)

	require.NoError(t, thenHarnessWorkflowLogsContain(context.Background(), w, "agent", "needle", true),
		"the guard no longer fires once the harness run is recorded again")
}

// fakeReArmCI answers both waits: the harness wait with a successful
// harness run, and the workflow-dispatch failure wait with a failed
// dispatched run whose logs hold the needle.
type fakeReArmCI struct {
	fakeHarnessResolveCI

	dispatched *forge.WorkflowRun
}

func (f *fakeReArmCI) DispatchWorkflow(context.Context, string, string, string, string, map[string]string) (int, error) {
	return 0, nil
}

func (f *fakeReArmCI) WaitForWorkflowConclusion(context.Context, string, string, string, time.Time, string, int) (*forge.WorkflowRun, error) {
	return f.dispatched, nil
}

// The failure step records the dispatched caller run and re-arms the
// fail-fast itself, so a harness step that follows it after a re-resolve
// fails fast instead of asserting on the dispatched run — the wrong-run
// false pass the guard exists to stop.
func TestWorkflowDispatchSteps_FailureStepReArmsGuard(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	fake := &fakeReArmCI{
		fakeHarnessResolveCI: fakeHarnessResolveCI{
			run:  &forge.WorkflowRun{ID: 9, Status: "completed", Conclusion: "success"},
			logs: "x needle y",
		},
		dispatched: &forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "failure"},
	}
	w := &world.World{
		Org:                    "org",
		RepoName:               "repo",
		ScenarioStart:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		CI:                     fake,
		DispatchedWorkflow:     true,
		DispatchedWorkflowFile: "prioritize.yml",
		DispatchedWorkflowAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Logf:                   func(string, ...any) {},
	}

	// A harness wait re-resolves the harness run and clears the guard…
	require.NoError(t, thenHarnessWorkflowCompletes(w, "agent"))
	assert.False(t, w.DispatchedWorkflow)

	// …then the failure step swaps the caller run back in and re-arms it.
	require.NoError(t, thenWorkflowFailsLogging(context.Background(), w, "prioritize.yml", "needle"))
	assert.True(t, w.DispatchedWorkflow, "the failure step records the dispatched run and re-arms the fail-fast")
	assert.Equal(t, 7, w.WorkflowRun.ID)

	err := thenHarnessWorkflowLogsContain(context.Background(), w, "agent", "needle", true)
	require.Error(t, err, "a harness step after the failure step must fail fast, not assert on the dispatched run")
	assert.Contains(t, err.Error(), "workflow dispatch step")
}

// Logs that are not ready when the run concludes are fetched again; a
// truncation note is final.
func TestThenWorkflowFailsLogging_Refetch(t *testing.T) {
	shrinkLogPoll(t, time.Minute)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	failed := &forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "failure"}

	f := &fakeDispatcherCI{run: failed, logsSeq: []string{"", "x\n[logs unavailable: HTTP 404]\n", "x needle y"}}
	require.NoError(t, thenWorkflowFailsLogging(context.Background(), &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: f, DispatchedWorkflowFile: "prioritize.yml", DispatchedWorkflowAt: start}, "prioritize.yml", "needle"))
	assert.Equal(t, 3, f.logCalls)

	f = &fakeDispatcherCI{run: failed, logs: "x\n[job list truncated: 100 of 120 jobs included]\n"}
	err := thenWorkflowFailsLogging(context.Background(), &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: f, DispatchedWorkflowFile: "prioritize.yml", DispatchedWorkflowAt: start}, "prioritize.yml", "needle")
	assert.ErrorContains(t, err, "they are incomplete")
	assert.Equal(t, 1, f.logCalls)
}

// A fetch error is fetched again, and a cancelled scenario stops the wait
// with the reason the logs were unusable.
func TestThenWorkflowFailsLogging_RefetchErrorAndCancel(t *testing.T) {
	oldWindow, oldInterval := logPollWindow, logPollInterval
	t.Cleanup(func() { logPollWindow, logPollInterval = oldWindow, oldInterval })
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	failed := &forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "failure"}

	logPollWindow, logPollInterval = time.Minute, time.Millisecond
	f := &fakeDispatcherCI{run: failed, errSeq: []error{errors.New("502")}, logsSeq: []string{"x needle y"}}
	require.NoError(t, thenWorkflowFailsLogging(context.Background(), &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: f, DispatchedWorkflowFile: "prioritize.yml", DispatchedWorkflowAt: start}, "prioritize.yml", "needle"))
	assert.Equal(t, 2, f.logCalls, "a fetch error is fetched again")

	logPollInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	f = &fakeDispatcherCI{run: failed, logs: " "}
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	err := thenWorkflowFailsLogging(ctx, &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: f, DispatchedWorkflowFile: "prioritize.yml", DispatchedWorkflowAt: start}, "prioritize.yml", "needle")
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "returned no log output")
}

// artifactCacheCI answers the harness wait with a fixed successful run
// and records which run's artifacts were downloaded, so a test can tell
// a cached artifact directory from a fresh download.
type artifactCacheCI struct {
	fakeHarnessResolveCI

	mu          sync.Mutex
	downloadIDs []int
}

func (f *artifactCacheCI) DownloadNamedArtifactFromRun(_ context.Context, _, _ string, runID int, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloadIDs = append(f.downloadIDs, runID)
	return nil
}

func (f *artifactCacheCI) downloadedRunIDs() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.downloadIDs...)
}

// A harness wait that re-resolves a different harness run drops the
// artifact directory cached for the earlier run: artifact assertions
// after it must download the new run's artifacts, not pass on the old
// run's output while w.WorkflowRun names the new run. Re-resolving the
// same run keeps the cache.
func TestWorkflowDispatchSteps_HarnessWaitDropsStaleArtifactCache(t *testing.T) {
	fake := &artifactCacheCI{fakeHarnessResolveCI: fakeHarnessResolveCI{
		run: &forge.WorkflowRun{ID: 9, Status: "completed", Conclusion: "success"},
	}}
	w := &world.World{
		Org:                    "org",
		RepoName:               "repo",
		ScenarioStart:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		CI:                     fake,
		DispatchedWorkflow:     true,
		DispatchedWorkflowFile: "prioritize.yml",
		DispatchedWorkflowAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Logf:                   func(string, ...any) {},
	}

	// The state the dispatch step leaves behind: an earlier run's
	// artifacts were downloaded before it, and the scenario's run is not
	// the harness run the wait is about to record.
	staleDir := t.TempDir()
	w.ArtifactDir = staleDir
	w.WorkflowRun = &forge.WorkflowRun{ID: 4, Status: "completed", Conclusion: "success"}

	require.NoError(t, thenHarnessWorkflowCompletes(w, "agent"))
	assert.Equal(t, 9, w.WorkflowRun.ID)
	assert.NotEqual(t, staleDir, w.ArtifactDir, "the earlier run's cached artifact directory must not survive a different run")
	assert.Equal(t, []int{9}, fake.downloadedRunIDs(), "the new run's artifacts must be downloaded")
	_, statErr := os.Stat(staleDir)
	assert.True(t, os.IsNotExist(statErr), "the dropped artifact directory is removed, not left behind")

	// Re-resolving the same run keeps the cache: no second download.
	cached := w.ArtifactDir
	require.NoError(t, thenHarnessWorkflowCompletes(w, "agent"))
	assert.Equal(t, cached, w.ArtifactDir)
	assert.Equal(t, []int{9}, fake.downloadedRunIDs())
}
