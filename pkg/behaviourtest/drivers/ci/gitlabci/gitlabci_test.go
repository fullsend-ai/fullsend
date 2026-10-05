package gitlabci

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// instantAfter returns a timer function that fires immediately, allowing
// poll-loop tests to run without real wall-clock sleeps.
func instantAfter(_ time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}

// newTestDriver returns a Driver with instantAfter injected so poll-loop
// tests run without real wall-clock sleeps.
func newTestDriver(client forge.Client) *Driver {
	return &Driver{Client: client, afterFunc: instantAfter}
}

func TestNextBackoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		current time.Duration
		max     time.Duration
		want    time.Duration
	}{
		{2 * time.Second, 30 * time.Second, 4 * time.Second},
		{4 * time.Second, 30 * time.Second, 8 * time.Second},
		{16 * time.Second, 30 * time.Second, 30 * time.Second},
		{30 * time.Second, 30 * time.Second, 30 * time.Second},
	}
	for _, tt := range tests {
		got := nextBackoff(tt.current, tt.max)
		assert.Equal(t, tt.want, got, "nextBackoff(%v, %v)", tt.current, tt.max)
	}
}

func TestNew_SetsFields(t *testing.T) {
	t.Parallel()

	d := New(forge.NewFakeClient(), "tok")
	driver, ok := d.(*Driver)
	require.True(t, ok, "New should return *Driver")
	assert.NotNil(t, driver.afterFunc, "afterFunc should be set by New")
	assert.NotNil(t, driver.nowFunc, "nowFunc should be set by New")
	assert.Equal(t, "tok", driver.Token)
}

func TestWaitForWorkflow_Success(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForWorkflow(context.Background(), "org", "repo", "pipeline", after, "")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 10, run.ID)
}

func TestWaitForWorkflow_Failure(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 10, Status: "completed", Conclusion: "failure",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}

	d := newTestDriver(client)
	_, err := d.WaitForWorkflow(context.Background(), "org", "repo", "pipeline", after, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"failure"`)
}

func TestFindCompletedWorkflowRun_Success(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 42, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}

	d := newTestDriver(client)
	run, err := d.FindCompletedWorkflowRun(context.Background(), "org", "repo", "", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 42, run.ID)
}

func TestAssertNoWorkflow_NoRunsAfterTriggerTime(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := newTestDriver(forge.NewFakeClient())
	err := d.AssertNoWorkflow(context.Background(), "org", "repo", "", after)
	require.NoError(t, err)
}

func TestAssertNoWorkflow_DetectsRun(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}

	d := newTestDriver(client)
	err := d.AssertNoWorkflow(context.Background(), "org", "repo", "", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected pipeline 10")
}

func TestGetRunLogs(t *testing.T) {
	t.Parallel()

	d := newTestDriver(forge.NewFakeClient())
	logs, err := d.GetRunLogs(context.Background(), "org", "repo", 1)
	require.NoError(t, err)
	assert.Contains(t, logs, "fake workflow logs")
}

func TestDownloadNamedArtifactFromRun_NotFound(t *testing.T) {
	t.Parallel()

	client := forge.NewFakeClient()
	d := newTestDriver(client)
	err := d.DownloadNamedArtifactFromRun(context.Background(), "org", "repo", 1, "missing", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestDownloadNamedArtifactFromRun_MapsPortableNameToScaffoldJobName(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): GitLab has no separate named-artifact
	// concept — ListWorkflowRunArtifacts reports the owning job's name,
	// and the scaffold's generic agent-stage template names every job
	// "fullsend <agent> agent" (fullsend-agent.yml), not the portable
	// "fullsend-<agent>" convention shared callers (e.g. playback.go)
	// pass in.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("agent-result.json")
	require.NoError(t, err)
	_, err = w.Write([]byte(`{"ok":true}`))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	client := forge.NewFakeClient()
	client.WorkflowRunArtifacts = map[int][]forge.WorkflowArtifact{
		5: {{ID: 50, Name: "fullsend review agent"}},
	}
	client.WorkflowArtifactContents = map[int][]byte{50: buf.Bytes()}

	d := newTestDriver(client)
	dest := t.TempDir()
	err = d.DownloadNamedArtifactFromRun(context.Background(), "org", "repo", 5, "fullsend-review", dest)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(dest, "fullsend review agent", "agent-result.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(data))
}

func TestDownloadNamedArtifactAfter_MapsPortableNameToScaffoldJobName(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): same portable-to-job-name translation
	// as DownloadNamedArtifactFromRun, exercised through the
	// repository-level artifact poll instead.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("agent-result.json")
	require.NoError(t, err)
	_, err = w.Write([]byte(`{"ok":true}`))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 60, Name: "fullsend fix agent", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 7},
		},
	}
	client.WorkflowArtifactContents = map[int][]byte{60: buf.Bytes()}

	d := newTestDriver(client)
	dest := t.TempDir()
	err = d.DownloadNamedArtifactAfter(context.Background(), "org", "repo", "fullsend-fix", after, dest)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(dest, "fullsend fix agent", "agent-result.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(data))
}

func TestWaitForHarnessAgentRound_SelectsEarliestEligibleRun(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): both review rounds have already
	// completed successfully by the time this (first round's) wait
	// runs. WaitForHarnessAgentRound must pick the earliest eligible run
	// rather than WaitForHarnessAgent's artifact-first "latest eligible
	// run wins" behavior.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "fullsend review agent", Status: "completed", Conclusion: "success"}},
		200: {{ID: 2, Name: "fullsend review agent", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID)
}

func TestWaitForHarnessAgentRound_SkipsConsumedRuns(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "fullsend review agent", Status: "completed", Conclusion: "success"}},
		200: {{ID: 2, Name: "fullsend review agent", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, map[int]bool{100: true})
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

// erroringRunJobsClient wraps FakeClient so ListWorkflowRunJobs returns an
// error for one specific run, simulating a transient job-lookup failure
// (or a not-yet-visible job list) for an earlier candidate while a later
// run is already resolved.
type erroringRunJobsClient struct {
	*forge.FakeClient
	errRun int
	err    error
}

func (c *erroringRunJobsClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	if runID == c.errRun {
		return nil, c.err
	}
	return c.FakeClient.ListWorkflowRunJobs(ctx, owner, repo, runID)
}

func TestWaitForHarnessAgentRound_JobLookupErrorHoldsOffLaterRun(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): a job-lookup error on the earliest
	// unconsumed run must not let the scan fall through to a later,
	// already-resolved run. Run 100 (earliest) cannot be looked up; run
	// 200 (later) already has a successful agent job. Before the fix,
	// the lookup error on run 100 was treated the same as "no job" and
	// the scan selected run 200 instead of waiting for run 100 to
	// resolve — satisfying the wrong round's completion assertion.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		200: {{ID: 2, Name: "fullsend review agent", Status: "completed", Conclusion: "success"}},
	}
	client := &erroringRunJobsClient{FakeClient: fake, errRun: 100, err: fmt.Errorf("jobs API error")}

	callCount := 0
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	d := &Driver{
		Client:    client,
		afterFunc: instantAfter,
		nowFunc: func() time.Time {
			callCount++
			if callCount <= 2 {
				return start
			}
			return start.Add(dispatchWait + time.Minute)
		},
	}
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.Error(t, err)
	assert.Nil(t, run, "run 200 must not be selected while run 100's job lookup is unresolved")
}

// noJobThenResolvedJobsClient wraps FakeClient so ListWorkflowRunJobs
// returns no jobs at all for targetRun on the first callsLeft calls, then
// falls back to the FakeClient's configured job list — as if the earlier
// pipeline's own agent job had not been created/listed yet and appeared on
// a subsequent poll.
type noJobThenResolvedJobsClient struct {
	*forge.FakeClient
	mu        sync.Mutex
	targetRun int
	callsLeft int
}

func (c *noJobThenResolvedJobsClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	if runID == c.targetRun {
		c.mu.Lock()
		if c.callsLeft > 0 {
			c.callsLeft--
			c.mu.Unlock()
			return nil, nil
		}
		c.mu.Unlock()
	}
	return c.FakeClient.ListWorkflowRunJobs(ctx, owner, repo, runID)
}

func TestWaitForHarnessAgentRound_EarlierRunJobAppearsOnSubsequentPoll(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): pipeline 100 (earliest) is still running
	// and does not show agent's job yet on the first poll — its absence
	// is inconclusive while the pipeline has not reached a terminal
	// state. Pipeline 200 (later) already has a successful agent job.
	// Before the fix, the earliest candidate's job-less state was treated
	// the same as "agent not scheduled here" regardless of the pipeline's
	// own status, and the scan fell through to pipeline 200. The round
	// must instead wait for pipeline 100 to resolve, and once its own
	// agent job appears (and succeeds) on a later poll, select pipeline
	// 100 rather than pipeline 200.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "running", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "fullsend review agent", Status: "completed", Conclusion: "success"}},
		200: {{ID: 2, Name: "fullsend review agent", Status: "completed", Conclusion: "success"}},
	}
	client := &noJobThenResolvedJobsClient{FakeClient: fake, targetRun: 100, callsLeft: 1}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID, "the earliest pipeline must be selected once its job appears, not the later already-resolved one")
}

// truncatingListWorkflowRunsClient wraps FakeClient so ListWorkflowRuns
// returns only a configured, truncated subset — simulating the live
// GitLab client's per_page=100 cap with no pagination (#7996 review).
// ListWorkflowRunsSince is left to the embedded FakeClient, which returns
// the full configured list, mirroring the live client's paginated listing.
type truncatingListWorkflowRunsClient struct {
	*forge.FakeClient
	truncated []forge.WorkflowRun
}

func (c *truncatingListWorkflowRunsClient) ListWorkflowRuns(_ context.Context, _, _, _ string) ([]forge.WorkflowRun, error) {
	return append([]forge.WorkflowRun(nil), c.truncated...), nil
}

// TestWaitForHarnessAgentRound_SelectsEarliestEligibleRunBeyondTruncatedPage
// is a regression test (#7996 review): harnessRoundPollOnce's earliest-round
// selection calls ListWorkflowRuns, whose live implementation returns only
// the newest 100 pipelines with no pagination. Enough newer pipelines (for
// this agent or others) can push the earliest eligible, not-yet-consumed
// round off that single page entirely — sorting and checking consumed
// afterward cannot recover a pipeline the listing never returned.
// harnessRoundPollOnce must instead use ListWorkflowRunsSince, which
// paginates back to the after boundary, so it still finds the earliest
// eligible round even when a truncated ListWorkflowRuns listing (simulated
// here) omits it.
func TestWaitForHarnessAgentRound_SelectsEarliestEligibleRunBeyondTruncatedPage(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()

	// The earliest eligible "review" round, plus 100 newer "triage" rounds
	// that would fill (and, on the live client, truncate) a single
	// ListWorkflowRuns page.
	full := []forge.WorkflowRun{
		{ID: 100, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
	}
	jobs := map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "fullsend review agent", Status: "completed", Conclusion: "success"}},
	}
	for i := 0; i < 100; i++ {
		id := 200 + i
		full = append(full, forge.WorkflowRun{
			ID: id, Status: "completed", Conclusion: "success",
			CreatedAt: fmt.Sprintf("2026-01-02T01:%02d:00Z", i%60),
		})
		jobs[id] = []forge.WorkflowJob{{ID: 2, Name: "fullsend triage agent", Status: "completed", Conclusion: "success"}}
	}
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{"org/repo/": full}
	fake.WorkflowRunJobs = jobs

	// Simulate the live client's truncated single page: the newer "triage"
	// rounds fill it, omitting the earliest eligible "review" round (id
	// 100) that ListWorkflowRunsSince would still find.
	client := &truncatingListWorkflowRunsClient{FakeClient: fake, truncated: full[1:]}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID, "the earliest eligible round must be found even though a truncated listing omits it")
}

func TestWaitForHarnessAgent_FromRepositoryArtifact(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{
				ID:            10,
				Name:          "fullsend triage agent",
				CreatedAt:     "2026-01-02T00:00:00Z",
				WorkflowRunID: 99,
			},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 99, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 99, run.ID)
}

func TestWaitForHarnessAgent_FailFastOnFailure(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID:         42,
			Status:     "completed",
			Conclusion: "failure",
			CreatedAt:  "2026-01-02T00:00:00Z",
			HTMLURL:    "https://gitlab.com/org/repo/-/pipelines/42",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		42: {{ID: 1, Name: "fullsend triage agent", Status: "completed", Conclusion: "failure"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	require.NotNil(t, run, "the failed run is returned with the error so its logs can be saved")
	assert.Equal(t, 42, run.ID)
	assert.Contains(t, err.Error(), "pipeline 42")
	assert.Contains(t, err.Error(), `"failure"`)
}

func TestWaitForHarnessAgent_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := newTestDriver(forge.NewFakeClient())
	_, err := d.WaitForHarnessAgent(ctx, "org", "repo", "triage", time.Now())
	require.ErrorIs(t, err, context.Canceled)
}

func TestWaitForHarnessAgent_SkipsCancelledRunArtifact(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/cancelled": {
			ID: 100, Status: "completed", Conclusion: "cancelled",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
		"org/repo/success": {
			ID: 200, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:01:00Z",
		},
	}
	// First poll has only the cancelled artifact; second has both.
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 20, Name: "fullsend review agent", CreatedAt: "2026-01-02T00:02:00Z", WorkflowRunID: 200},
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "review", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

func TestWaitForFailedHarnessAgent_FromRepositoryArtifact(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 11, Name: "fullsend fix agent", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 77},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 77, Status: "completed", Conclusion: "failure",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://gitlab.com/org/repo/-/pipelines/77",
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForFailedHarnessAgent(context.Background(), "org", "repo", "fix", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 77, run.ID)
}

func TestWaitForFailedHarnessAgent_ErrorsOnSuccess(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 12, Name: "fullsend fix agent", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 78},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 78, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://gitlab.com/org/repo/-/pipelines/78",
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForFailedHarnessAgent(context.Background(), "org", "repo", "fix", after)
	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), "concluded successfully; expected failure")
}

func TestWaitForFailedHarnessAgent_FallbackJobNameMatch(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 79, Status: "completed", Conclusion: "failure",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://gitlab.com/org/repo/-/pipelines/79",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		79: {{ID: 1, Name: "fullsend fix-ping agent", Status: "completed", Conclusion: "failure"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForFailedHarnessAgent(context.Background(), "org", "repo", "fix-ping", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 79, run.ID)
}

func TestWaitForFailedHarnessAgent_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := newTestDriver(forge.NewFakeClient())
	_, err := d.WaitForFailedHarnessAgent(ctx, "org", "repo", "fix", time.Now())
	require.ErrorIs(t, err, context.Canceled)
}

func TestCountHarnessDispatches_NoRuns(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()

	d := newTestDriver(client)
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestCountHarnessDispatches_SingleMatch(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "fullsend triage agent", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_MultipleMatches(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/": {
			{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-03T00:00:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "fullsend triage agent", Status: "completed", Conclusion: "success"}},
		20: {{ID: 2, Name: "fullsend triage agent", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestCountHarnessDispatches_FiltersBeforeTime(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/": {
			{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-07-01T00:00:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "fullsend triage agent", Status: "completed", Conclusion: "success"}},
		20: {{ID: 2, Name: "fullsend triage agent", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_FiltersOtherAgents(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/": {
			{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "fullsend triage agent", Status: "completed", Conclusion: "success"}},
		20: {{ID: 2, Name: "fullsend review agent", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_ExcludesCancelledJob(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/": {
			{ID: 10, Status: "completed", Conclusion: "cancelled", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:01:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "fullsend triage agent", Status: "completed", Conclusion: "cancelled"}},
		20: {{ID: 2, Name: "fullsend triage agent", Status: "completed", Conclusion: "success"}},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_APIError(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.Errors["ListWorkflowRuns"] = fmt.Errorf("API error")

	d := newTestDriver(client)
	_, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

func TestAssertNoHarnessAgentArtifact_IgnoresOtherAgentJobs(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "fullsend review agent", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err, "should not fail — the run has a different agent's job")
}

func TestAssertNoHarnessAgentArtifact_DetectsAgentJob(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "fullsend triage agent", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `expected harness "triage" not to run`)
}

func TestIsTerminalFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		conclusion string
		want       bool
	}{
		{"failure", true},
		{"timed_out", true},
		{"startup_failure", true},
		{"skipped", false},
		{"cancelled", false},
		{"success", false},
		{"", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, isTerminalFailure(tt.conclusion),
			"isTerminalFailure(%q)", tt.conclusion)
	}
}

func TestIsConcurrencySuperseded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		conclusion string
		want       bool
	}{
		{"cancelled", true},
		{"skipped", true},
		{"success", false},
		{"failure", false},
		{"", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, isConcurrencySuperseded(tt.conclusion),
			"isConcurrencySuperseded(%q)", tt.conclusion)
	}
}

func TestHarnessJobSuffix(t *testing.T) {
	t.Parallel()

	// GitLab's generic agent-stage template names every job
	// "fullsend <agent> agent" regardless of built-in vs custom harness
	// (fullsend-agent.yml; #7957 review) — unlike GitHub Actions' separate
	// "Harness run (<agent>)" matrix naming.
	assert.Equal(t, "fullsend pr-ping agent", harnessJobSuffix("pr-ping"))
	assert.Equal(t, "fullsend triage agent", harnessJobSuffix("triage"))
}

func TestGitlabArtifactJobName(t *testing.T) {
	t.Parallel()

	// The portable "fullsend-<agent>" convention (shared with the GitHub
	// driver) maps to the scaffold's job-name convention; anything else
	// passes through unchanged (#7957 review).
	assert.Equal(t, "fullsend review agent", gitlabArtifactJobName("fullsend-review"))
	assert.Equal(t, "fullsend pr-ping agent", gitlabArtifactJobName("fullsend-pr-ping"))
	assert.Equal(t, "already-a-job-name", gitlabArtifactJobName("already-a-job-name"))
}

func TestExtractArtifactZip_RejectsCorruptZip(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	err := extractArtifactZip("artifact", []byte("not-a-zip"), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse artifact zip")
}

func TestExtractArtifactZip_RejectsSymlink(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: "link", Method: zip.Store}
	hdr.SetMode(os.ModeSymlink | 0o755)
	_, err := zw.CreateHeader(hdr)
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	err = extractArtifactZip("../escape", buf.Bytes(), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestExtractArtifactZip_RejectsPathTraversal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../escape.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("nope"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	err = extractArtifactZip("artifact", buf.Bytes(), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")
}

func TestExtractArtifactZip_WritesFile(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("ok.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	require.NoError(t, extractArtifactZip("test-art", buf.Bytes(), dest))
	data, err := os.ReadFile(filepath.Join(dest, "test-art", "ok.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestSelectWorkflowRun(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := []forge.WorkflowRun{
		{ID: 1, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		{ID: 2, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-03T00:00:00Z"},
	}
	got := selectWorkflowRun(runs, after, "")
	require.NotNil(t, got)
	assert.Equal(t, 2, got.ID)
}

func TestSelectWorkflowRun_FiltersEvent(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := []forge.WorkflowRun{
		{ID: 1, Event: "api", Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		{ID: 2, Event: "push", Status: "completed", Conclusion: "success", CreatedAt: "2026-01-03T00:00:00Z"},
	}
	got := selectWorkflowRun(runs, after, "api")
	require.NotNil(t, got)
	assert.Equal(t, 1, got.ID)
}

func TestWaitForHarnessAgent_SiblingRunFailureIgnored(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/": {
			{
				ID: 100, Status: "completed", Conclusion: "failure",
				CreatedAt: "2026-01-02T00:00:00Z",
				HTMLURL:   "https://gitlab.com/org/repo/-/pipelines/100",
			},
			{
				ID: 200, Status: "completed", Conclusion: "success",
				CreatedAt: "2026-01-02T00:01:00Z",
				HTMLURL:   "https://gitlab.com/org/repo/-/pipelines/200",
			},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/success": {
			ID: 200, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:01:00Z",
		},
	}
	// Run 100 has no harness job for pr-ping; Run 200 does.
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Route", Status: "completed", Conclusion: "failure"}},
		200: {{ID: 2, Name: "fullsend pr-ping agent", Status: "completed", Conclusion: "success"}},
	}
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 10, Name: "fullsend pr-ping agent", CreatedAt: "2026-01-02T00:05:00Z", WorkflowRunID: 200},
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "pr-ping", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

func TestTimerAfter_NilFallback(t *testing.T) {
	t.Parallel()

	d := &Driver{Client: forge.NewFakeClient()}
	ch := d.timerAfter(1 * time.Millisecond)
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timerAfter nil-fallback did not fire within 2s")
	}
}

func TestNow_NilFallback(t *testing.T) {
	t.Parallel()

	d := &Driver{Client: forge.NewFakeClient()}
	// Should not panic.
	n := d.now()
	assert.False(t, n.IsZero())
}

// delayedRunsClient wraps FakeClient so ListWorkflowRuns returns no runs
// for the first N calls, then returns the configured runs.
type delayedRunsClient struct {
	*forge.FakeClient
	mu       sync.Mutex
	calls    int
	delayFor int
	runs     []forge.WorkflowRun
}

func (c *delayedRunsClient) ListWorkflowRuns(_ context.Context, _, _, _ string) ([]forge.WorkflowRun, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls <= c.delayFor {
		return nil, nil
	}
	return append([]forge.WorkflowRun(nil), c.runs...), nil
}

func TestWaitForWorkflow_PollsUntilDispatch(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	successRun := forge.WorkflowRun{
		ID: 1, Status: "completed", Conclusion: "success",
		CreatedAt: "2026-01-02T00:00:00Z",
	}
	fake := forge.NewFakeClient()
	fake.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/success": &successRun,
	}
	client := &delayedRunsClient{
		FakeClient: fake,
		delayFor:   3,
		runs:       []forge.WorkflowRun{successRun},
	}

	d := &Driver{Client: client, afterFunc: instantAfter}
	run, err := d.WaitForWorkflow(context.Background(), "org", "repo", "", after, "")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 1, run.ID)
}

func TestWaitForHarnessAgent_LookupErrsTracked(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// A completed failed pipeline exists but ListWorkflowRunJobs will fail.
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/": {
			{
				ID: 42, Status: "completed", Conclusion: "failure",
				CreatedAt: "2026-01-02T00:00:00Z",
				HTMLURL:   "https://gitlab.com/org/repo/-/pipelines/42",
			},
		},
	}
	// Inject error for ListWorkflowRunJobs so runHasAgentJob fails.
	client.Errors["ListWorkflowRunJobs"] = fmt.Errorf("gitlab: 500 internal server error")

	// Stepping clock: first call returns start, second call returns past deadline.
	callCount := 0
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	d := &Driver{
		Client:    client,
		afterFunc: instantAfter,
		nowFunc: func() time.Time {
			callCount++
			if callCount <= 2 {
				return start
			}
			return start.Add(dispatchWait + time.Minute)
		},
	}

	_, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not complete successfully within deadline")
	assert.Contains(t, err.Error(), "lookups")
	assert.Contains(t, err.Error(), "500 internal server error")
}

func TestWaitForFailedHarnessAgent_LookupErrsTracked(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// A completed pipeline exists but ListWorkflowRunJobs will fail.
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/": {
			{
				ID: 55, Status: "completed", Conclusion: "success",
				CreatedAt: "2026-01-02T00:00:00Z",
				HTMLURL:   "https://gitlab.com/org/repo/-/pipelines/55",
			},
		},
	}
	// Inject error for ListWorkflowRunJobs so runHasAgentJob fails.
	client.Errors["ListWorkflowRunJobs"] = fmt.Errorf("gitlab: 502 bad gateway")

	// Stepping clock: first call returns start, second call returns past deadline.
	callCount := 0
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	d := &Driver{
		Client:    client,
		afterFunc: instantAfter,
		nowFunc: func() time.Time {
			callCount++
			if callCount <= 2 {
				return start
			}
			return start.Add(dispatchWait + time.Minute)
		},
	}

	_, err := d.WaitForFailedHarnessAgent(context.Background(), "org", "repo", "fix", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not complete with a failure within deadline")
	assert.Contains(t, err.Error(), "lookups")
	assert.Contains(t, err.Error(), "502 bad gateway")
}

// TestWaitForHarnessAgent_ArtifactFirstFailureReturnsRun: a failed
// pipeline whose fullsend-{agent} artifact is found first comes back
// with the error, so the step can save its logs.
func TestWaitForHarnessAgent_ArtifactFirstFailureReturnsRun(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 10, Name: "fullsend pi-smoke agent", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 88},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/": {
			ID: 88, Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL: "https://gitlab.com/org/repo/-/pipelines/88",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		88: {{ID: 1, Name: "fullsend pi-smoke agent", Status: "completed", Conclusion: "failure"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "pi-smoke", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `harness run for "pi-smoke" concluded with "failure"`)
	require.NotNil(t, run, "the failed run is returned with the error so its logs can be saved")
	assert.Equal(t, 88, run.ID)
}
