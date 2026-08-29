package githubactions

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
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
// tests run without real wall-clock sleeps. Update this helper when new
// fields are added to Driver.
func newTestDriver(client forge.Client) *Driver {
	return &Driver{Client: client, afterFunc: instantAfter}
}

func TestDispatchDetectionWindow_AtLeast4Minutes(t *testing.T) {
	t.Parallel()

	// The dispatch detection window is dispatchTimeout.
	// It must be at least 4 minutes to tolerate slow GitHub webhook
	// delivery. See issues #5503 and #6668.
	assert.GreaterOrEqual(t, dispatchTimeout, 4*time.Minute,
		"dispatch detection window (%v) should be at least 4 minutes", dispatchTimeout)
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
		{8 * time.Second, 30 * time.Second, 16 * time.Second},
		{16 * time.Second, 30 * time.Second, 30 * time.Second},
		{30 * time.Second, 30 * time.Second, 30 * time.Second},
		{1 * time.Second, 1 * time.Second, 1 * time.Second},
	}
	for _, tt := range tests {
		got := nextBackoff(tt.current, tt.max)
		assert.Equal(t, tt.want, got,
			"nextBackoff(%v, %v)", tt.current, tt.max)
	}
}

// delayedRunsClient wraps FakeClient so ListWorkflowRuns returns no
// runs for the first N calls, then returns the configured runs.
// Used to verify exponential backoff intervals during dispatch polling.
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

func TestDispatchPollingBackoff(t *testing.T) {
	t.Parallel()

	// Track intervals passed to afterFunc to verify exponential growth.
	var mu sync.Mutex
	var intervals []time.Duration
	recordingAfter := func(d time.Duration) <-chan time.Time {
		mu.Lock()
		intervals = append(intervals, d)
		mu.Unlock()
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	successRun := forge.WorkflowRun{
		ID: 1, Status: "completed", Conclusion: "success",
		CreatedAt: "2026-01-02T00:00:00Z", Event: "issues",
	}
	fake := forge.NewFakeClient()
	// Seed GetWorkflowRun so the completion-wait loop succeeds.
	fake.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/success": &successRun,
	}
	client := &delayedRunsClient{
		FakeClient: fake,
		delayFor:   4, // return no runs for first 4 calls
		runs:       []forge.WorkflowRun{successRun},
	}

	d := &Driver{Client: client, afterFunc: recordingAfter}
	run, err := d.WaitForWorkflow(context.Background(), "org", "repo", "test.yaml", after, "issues")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 1, run.ID)

	mu.Lock()
	defer mu.Unlock()
	// The dispatch detection phase runs 5 polls (4 empty + 1 successful).
	// After detection the completion-wait loop uses pollInterval, so only
	// check the first 5 intervals for exponential growth.
	dispatchIntervals := intervals[:5]
	assert.Equal(t, dispatchPollInit, dispatchIntervals[0],
		"first interval should be dispatchPollInit")
	for i := 1; i < len(dispatchIntervals); i++ {
		assert.GreaterOrEqual(t, dispatchIntervals[i], dispatchIntervals[i-1],
			"interval[%d] (%v) should be >= interval[%d] (%v)",
			i, dispatchIntervals[i], i-1, dispatchIntervals[i-1])
		assert.LessOrEqual(t, dispatchIntervals[i], dispatchPollMax,
			"interval[%d] (%v) should be <= max (%v)",
			i, dispatchIntervals[i], dispatchPollMax)
	}
	// Verify specific exponential progression: 2s, 4s, 8s, 16s, 30s.
	assert.Equal(t, 2*time.Second, dispatchIntervals[0])
	assert.Equal(t, 4*time.Second, dispatchIntervals[1])
	assert.Equal(t, 8*time.Second, dispatchIntervals[2])
	assert.Equal(t, 16*time.Second, dispatchIntervals[3])
	assert.Equal(t, 30*time.Second, dispatchIntervals[4])
}

func TestSelectWorkflowRun_ReturnsFailedRun(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := []forge.WorkflowRun{
		{ID: 1, Status: "completed", Conclusion: "failure", Event: "issues", CreatedAt: "2026-01-01T01:00:00Z"},
	}

	got := selectWorkflowRun(runs, after, "issues")
	require.NotNil(t, got)
	assert.Equal(t, 1, got.ID)
	assert.Equal(t, "failure", got.Conclusion)
}

func TestSelectSuccessfulWorkflowRun_SkipsFailed(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := []forge.WorkflowRun{
		{ID: 1, Status: "completed", Conclusion: "failure", Event: "issues", CreatedAt: "2026-01-01T01:00:00Z"},
		{ID: 2, Status: "completed", Conclusion: "success", Event: "issues", CreatedAt: "2026-01-01T02:00:00Z"},
	}

	got := selectSuccessfulWorkflowRun(runs, after, "issues")
	require.NotNil(t, got)
	assert.Equal(t, 2, got.ID)
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

func TestExtractArtifactZip_SanitizesName(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("ok.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	require.NoError(t, extractArtifactZip("../../weird/name", buf.Bytes(), dest))
	_, err = os.Stat(filepath.Join(dest, "name", "ok.txt"))
	require.NoError(t, err)
}

func TestExtractArtifactZip_RejectsAggregateLimit(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	const chunk = 10 << 20 // per-file limit
	for i := 0; i < 11; i++ {
		w, err := zw.Create(fmt.Sprintf("part-%d.bin", i))
		require.NoError(t, err)
		_, err = w.Write(bytes.Repeat([]byte("x"), chunk))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	err := extractArtifactZip("artifact", buf.Bytes(), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aggregate extraction limit")
}

func TestNewestRepositoryArtifactCreatedAt(t *testing.T) {
	t.Parallel()

	arts := []forge.RepositoryArtifact{
		{CreatedAt: "2026-01-01T00:00:00Z"},
		{CreatedAt: "2026-01-02T00:00:00Z"},
	}
	assert.Equal(t, "2026-01-02T00:00:00Z", newestRepositoryArtifactCreatedAt(arts))
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
		"org/repo/fullsend.yaml": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
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
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-03T00:00:00Z"},
			{ID: 30, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-04T00:00:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
		20: {{ID: 2, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
		30: {{ID: 3, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestCountHarnessDispatches_FiltersBeforeTime(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"}, // before
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-07-01T00:00:00Z"}, // after
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
		20: {{ID: 2, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
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
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 30, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
		20: {{ID: 2, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
		30: {{ID: 3, Name: "dispatch / Harness run (code)", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
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

func TestWaitForHarnessAgent_FromRepositoryArtifact(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{
				ID:            10,
				Name:          "fullsend-issue-ping",
				CreatedAt:     "2026-01-02T00:00:00Z",
				WorkflowRunID: 99,
			},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 99, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z",
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "issue-ping", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 99, run.ID)
}

func TestWaitForHarnessAgent_FailFastOnFailure(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// No success artifact — the harness failed before uploading one.
	// The run contains the agent's harness job so fail-fast is correct.
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID:         42,
			Status:     "completed",
			Conclusion: "failure",
			CreatedAt:  "2026-01-02T00:00:00Z",
			HTMLURL:    "https://github.com/org/repo/actions/runs/42",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		42: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), "workflow run 42")
	assert.Contains(t, err.Error(), `"failure"`)
	assert.Contains(t, err.Error(), "https://github.com/org/repo/actions/runs/42")
}

func TestWaitForHarnessAgent_FailFastOnTimedOut(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID:         50,
			Status:     "completed",
			Conclusion: "timed_out",
			CreatedAt:  "2026-01-02T00:00:00Z",
			HTMLURL:    "https://github.com/org/repo/actions/runs/50",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		50: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "timed_out"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), `"timed_out"`)
}

func TestWaitForHarnessAgent_FailFastOnStartupFailure(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID:         60,
			Status:     "completed",
			Conclusion: "startup_failure",
			CreatedAt:  "2026-01-02T00:00:00Z",
			HTMLURL:    "https://github.com/org/repo/actions/runs/60",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		60: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "startup_failure"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), `"startup_failure"`)
}

func TestWaitForFailedHarnessAgent_FromRepositoryArtifact(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// The fullsend action uploads the artifact with if: always(), so a
	// failed standard-stage run (job named "Fix", not "Harness run
	// (fix)") is still resolvable through its artifact.
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 11, Name: "fullsend-fix", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 77},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 77, Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL: "https://github.com/org/repo/actions/runs/77",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		77: {{ID: 1, Name: "dispatch / Fix", Status: "completed", Conclusion: "failure"}},
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
			{ID: 12, Name: "fullsend-fix", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 78},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 78, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL: "https://github.com/org/repo/actions/runs/78",
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
	// No artifact (custom harness failed before uploading one); the run
	// is attributed through its "Harness run (<agent>)" matrix job.
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 79, Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL: "https://github.com/org/repo/actions/runs/79",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		79: {{ID: 1, Name: "dispatch / Harness run (fix-ping)", Status: "completed", Conclusion: "failure"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForFailedHarnessAgent(context.Background(), "org", "repo", "fix-ping", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 79, run.ID)
}

func TestWaitForFailedHarnessAgent_FallbackErrorsOnJobSuccess(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// No artifact for this run at all — the run's overall conclusion is
	// "success", but the fallback must still inspect the agent's own
	// job (not pre-filter on the run-level conclusion) so a run that
	// completes successfully still fails fast via the fallback path,
	// not just the artifact-based one.
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 80, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL: "https://github.com/org/repo/actions/runs/80",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		80: {{ID: 1, Name: "dispatch / Harness run (fix-ping)", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForFailedHarnessAgent(context.Background(), "org", "repo", "fix-ping", after)
	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), "concluded successfully; expected failure")
}

func TestWaitForFailedHarnessAgent_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := newTestDriver(forge.NewFakeClient())
	_, err := d.WaitForFailedHarnessAgent(ctx, "org", "repo", "fix", time.Now())
	require.ErrorIs(t, err, context.Canceled)
}

// TestWaitForHarnessAgent_SiblingRunFailureIgnored verifies the fix for
// #5852: a sibling fullsend.yaml run (e.g. triggered by PR "opened")
// that fails in Route/Review without scheduling the waited agent's
// harness job must NOT trigger fail-fast. The waited agent's run
// (triggered by "labeled") succeeds independently.
func TestWaitForHarnessAgent_SiblingRunFailureIgnored(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()

	// Two concurrent fullsend.yaml runs:
	// Run A (ID=100): "opened" event, failed in Route/Review, no harness jobs.
	// Run B (ID=200): "labeled" event, succeeded with Harness run (pr-ping).
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{
				ID: 100, Status: "completed", Conclusion: "failure",
				CreatedAt: "2026-01-02T00:00:00Z",
				HTMLURL:   "https://github.com/org/repo/actions/runs/100",
			},
			{
				ID: 200, Status: "completed", Conclusion: "success",
				CreatedAt: "2026-01-02T00:01:00Z",
				HTMLURL:   "https://github.com/org/repo/actions/runs/200",
			},
		},
	}
	// Also seed WorkflowRuns for GetWorkflowRun (ID-based lookup).
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/success": {
			ID: 200, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:01:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/200",
		},
	}
	// Run A has no harness job for pr-ping (only Route/Review).
	// Run B has the pr-ping harness job.
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {
			{ID: 1, Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
			{ID: 2, Name: "dispatch / Review", Status: "completed", Conclusion: "failure"},
		},
		200: {
			{ID: 3, Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
			{ID: 4, Name: "dispatch / Harness run (pr-ping)", Status: "completed", Conclusion: "success"},
		},
	}
	// The success artifact from run B.
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 10, Name: "fullsend-pr-ping", CreatedAt: "2026-01-02T00:05:00Z", WorkflowRunID: 200},
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "pr-ping", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

func TestWaitForHarnessAgent_SkippedDoesNotTriggerFailFast(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// A skipped harness run exists but should not trigger fail-fast.
	// The success run is keyed separately so GetWorkflowRun finds it by ID.
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 70, Status: "completed", Conclusion: "skipped",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/70",
		},
		"org/repo/success": {
			ID: 99, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	// Provide a success artifact so the function can succeed.
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{
				ID:            10,
				Name:          "fullsend-triage",
				CreatedAt:     "2026-01-02T00:00:00Z",
				WorkflowRunID: 99,
			},
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 99, run.ID)
}

func TestWaitForHarnessAgent_CancelledDoesNotTriggerFailFast(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// A cancelled harness run should not trigger fail-fast.
	// The success run is keyed separately so GetWorkflowRun finds it by ID.
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 80, Status: "completed", Conclusion: "cancelled",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/80",
		},
		"org/repo/success": {
			ID: 99, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	// Provide a success artifact so the function can succeed.
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{
				ID:            10,
				Name:          "fullsend-triage",
				CreatedAt:     "2026-01-02T00:00:00Z",
				WorkflowRunID: 99,
			},
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 99, run.ID)
}

// settlingArtifactsClient wraps FakeClient so the repository artifact
// list changes after the first poll, simulating a cancelled run's
// artifact appearing before the superseding success run uploads its own.
type settlingArtifactsClient struct {
	*forge.FakeClient
	mu         sync.Mutex
	callsLeft  int
	beforeArts []forge.RepositoryArtifact
	afterArts  []forge.RepositoryArtifact
}

func (c *settlingArtifactsClient) ListRepositoryArtifacts(_ context.Context, _, _ string, _ int) ([]forge.RepositoryArtifact, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.callsLeft > 0 {
		c.callsLeft--
		return append([]forge.RepositoryArtifact(nil), c.beforeArts...), nil
	}
	return append([]forge.RepositoryArtifact(nil), c.afterArts...), nil
}

// TestWaitForHarnessAgent_SkipsCancelledRunArtifact verifies the fix for
// #6387: when the only available artifact belongs to a concurrency-
// cancelled run, WaitForHarnessAgent should skip it and keep polling
// until the superseding run's artifact appears.
func TestWaitForHarnessAgent_SkipsCancelledRunArtifact(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	// Two runs: cancelled run A (100) and success run B (200).
	fake.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/cancelled": {
			ID: 100, Status: "completed", Conclusion: "cancelled",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/100",
		},
		"org/repo/success": {
			ID: 200, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:01:00Z",
		},
	}

	client := &settlingArtifactsClient{
		FakeClient: fake,
		callsLeft:  1,
		// First poll: only the cancelled run's artifact.
		beforeArts: []forge.RepositoryArtifact{
			{ID: 10, Name: "fullsend-review", CreatedAt: "2026-01-02T00:00:30Z", WorkflowRunID: 100},
		},
		// Second poll: success run's artifact also available (higher ID).
		afterArts: []forge.RepositoryArtifact{
			{ID: 10, Name: "fullsend-review", CreatedAt: "2026-01-02T00:00:30Z", WorkflowRunID: 100},
			{ID: 20, Name: "fullsend-review", CreatedAt: "2026-01-02T00:02:00Z", WorkflowRunID: 200},
		},
	}

	d := &Driver{Client: client, afterFunc: instantAfter}
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "review", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

// TestWaitForHarnessAgent_SkipsSkippedRunArtifact mirrors the cancelled
// case but for a "skipped" conclusion, which is also concurrency noise.
func TestWaitForHarnessAgent_SkipsSkippedRunArtifact(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/skipped": {
			ID: 100, Status: "completed", Conclusion: "skipped",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/100",
		},
		"org/repo/success": {
			ID: 200, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:01:00Z",
		},
	}

	client := &settlingArtifactsClient{
		FakeClient: fake,
		callsLeft:  1,
		beforeArts: []forge.RepositoryArtifact{
			{ID: 10, Name: "fullsend-review", CreatedAt: "2026-01-02T00:00:30Z", WorkflowRunID: 100},
		},
		afterArts: []forge.RepositoryArtifact{
			{ID: 10, Name: "fullsend-review", CreatedAt: "2026-01-02T00:00:30Z", WorkflowRunID: 100},
			{ID: 20, Name: "fullsend-review", CreatedAt: "2026-01-02T00:02:00Z", WorkflowRunID: 200},
		},
	}

	d := &Driver{Client: client, afterFunc: instantAfter}
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "review", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

func TestWaitForHarnessAgent_IgnoresRunsBeforeTriggerTime(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// Failed harness run is before the trigger time — should not trigger fail-fast.
	// The success run is keyed separately so GetWorkflowRun finds it by ID.
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 90, Status: "completed", Conclusion: "failure",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/90",
		},
		"org/repo/success": {
			ID: 99, Status: "completed", Conclusion: "success", CreatedAt: "2026-07-01T00:00:00Z",
		},
	}
	// Provide a success artifact so the function can succeed.
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{
				ID:            10,
				Name:          "fullsend-triage",
				CreatedAt:     "2026-07-01T00:00:00Z",
				WorkflowRunID: 99,
			},
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 99, run.ID)
}

func TestWaitForHarnessAgent_TimeoutIncludesDiagnostics(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// No artifacts, no terminal failures — will time out.
	// Use in-progress harness run to avoid fail-fast.
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 100, Status: "in_progress",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/100",
		},
	}

	d := newTestDriver(client)
	// Use a cancelled context to avoid waiting the full deadline.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.WaitForHarnessAgent(ctx, "org", "repo", "triage", after)
	require.Error(t, err)
	// Should return context error, not a timeout with diagnostics,
	// because the context was cancelled before the deadline.
	assert.ErrorIs(t, err, context.Canceled)
}

// TestWaitForHarnessAgent_BothRunsScheduleAgent_OneFailsIsFatal tests
// that when both sibling runs schedule the same agent and one fails,
// fail-fast correctly triggers (the failure is real for this agent).
func TestWaitForHarnessAgent_BothRunsScheduleAgent_OneFailsIsFatal(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()

	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{
				ID: 100, Status: "completed", Conclusion: "failure",
				CreatedAt: "2026-01-02T00:00:00Z",
				HTMLURL:   "https://github.com/org/repo/actions/runs/100",
			},
			{
				ID: 200, Status: "in_progress",
				CreatedAt: "2026-01-02T00:01:00Z",
			},
		},
	}
	// Both runs scheduled the agent's job; run 100 failed.
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}},
		200: {{ID: 2, Name: "dispatch / Harness run (triage)", Status: "in_progress"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Nil(t, run)
	assert.Contains(t, err.Error(), "workflow run 100")
	assert.Contains(t, err.Error(), `"failure"`)
}

func TestCountHarnessDispatches_IgnoresRunsWithoutAgentJob(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// Two runs: one with pr-ping job, one with only Route/Review.
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:01:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Route", Status: "completed", Conclusion: "success"}},
		20: {{ID: 2, Name: "dispatch / Harness run (pr-ping)", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "pr-ping", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_ExcludesCancelledJob(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// Two runs for the same agent: one cancelled by concurrency group,
	// one completed successfully. Only the successful one counts (#6053).
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "completed", Conclusion: "cancelled", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:01:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (fork-pr-sync)", Status: "completed", Conclusion: "cancelled"}},
		20: {{ID: 2, Name: "dispatch / Harness run (fork-pr-sync)", Status: "completed", Conclusion: "success"}},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "fork-pr-sync", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_ExcludesSkippedJob(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// Run with skipped harness job (empty matrix from CEL mismatch)
	// plus one successful run. Only the successful one counts.
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:01:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (fork-pr-sync)", Status: "completed", Conclusion: "skipped"}},
		20: {{ID: 2, Name: "dispatch / Harness run (fork-pr-sync)", Status: "completed", Conclusion: "success"}},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "fork-pr-sync", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_CountsFailedJob(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// A failed job is a real dispatch — it should be counted.
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (fork-pr-sync)", Status: "completed", Conclusion: "failure"}},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "fork-pr-sync", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// settlingJobsClient wraps FakeClient so one run's job list changes after
// the first poll, simulating a duplicate run whose harness job is still
// executing when the count is first taken.
type settlingJobsClient struct {
	*forge.FakeClient
	mu         sync.Mutex
	settleRun  int
	callsLeft  int // polls of settleRun before its jobs settle
	beforeJobs []forge.WorkflowJob
	afterJobs  []forge.WorkflowJob
}

func (c *settlingJobsClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	if runID != c.settleRun {
		return c.FakeClient.ListWorkflowRunJobs(ctx, owner, repo, runID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.callsLeft > 0 {
		c.callsLeft--
		return c.beforeJobs, nil
	}
	return c.afterJobs, nil
}

func TestCountHarnessDispatches_PendingDuplicateSettlesToCancelled(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	// Run 10 is a duplicate still being cancelled when the count is
	// first taken; run 20 already succeeded. Once run 10's job settles
	// to cancelled, only run 20 counts (#6053).
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:01Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		20: {{ID: 2, Name: "dispatch / Harness run (fork-pr-sync)", Status: "completed", Conclusion: "success"}},
	}
	client := &settlingJobsClient{
		FakeClient: fake,
		settleRun:  10,
		callsLeft:  1,
		beforeJobs: []forge.WorkflowJob{{ID: 1, Name: "dispatch / Harness run (fork-pr-sync)", Status: "in_progress"}},
		afterJobs:  []forge.WorkflowJob{{ID: 1, Name: "dispatch / Harness run (fork-pr-sync)", Status: "completed", Conclusion: "cancelled"}},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "fork-pr-sync", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_PendingDuplicateSettlesToSuccess(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	// A genuine double dispatch: the in-flight duplicate completes
	// successfully instead of being cancelled, so both runs count and
	// the exact-count assertion still catches the regression.
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:01Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		20: {{ID: 2, Name: "dispatch / Harness run (fork-pr-sync)", Status: "completed", Conclusion: "success"}},
	}
	client := &settlingJobsClient{
		FakeClient: fake,
		settleRun:  10,
		callsLeft:  1,
		beforeJobs: []forge.WorkflowJob{{ID: 1, Name: "dispatch / Harness run (fork-pr-sync)", Status: "in_progress"}},
		afterJobs:  []forge.WorkflowJob{{ID: 1, Name: "dispatch / Harness run (fork-pr-sync)", Status: "completed", Conclusion: "success"}},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "fork-pr-sync", after)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

func TestCountHarnessDispatches_UnexpandedMatrixRunSettles(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	// Run 10 is still executing and its Route job has not expanded the
	// harness matrix yet, so the agent's job is absent on the first
	// poll. It must be treated as pending, not silently skipped.
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:01Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		20: {{ID: 2, Name: "dispatch / Harness run (fork-pr-sync)", Status: "completed", Conclusion: "success"}},
	}
	client := &settlingJobsClient{
		FakeClient: fake,
		settleRun:  10,
		callsLeft:  1,
		beforeJobs: []forge.WorkflowJob{{ID: 1, Name: "dispatch / Route", Status: "in_progress"}},
		afterJobs:  []forge.WorkflowJob{{ID: 1, Name: "dispatch / Harness run (fork-pr-sync)", Status: "completed", Conclusion: "cancelled"}},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "fork-pr-sync", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_ContextCancelledWhilePending(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 10, Status: "in_progress",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (fork-pr-sync)", Status: "in_progress"}},
	}

	d := &Driver{Client: client}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.CountHarnessDispatches(ctx, "org", "repo", "fork-pr-sync", after)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
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
		{"timed_out", false},
		{"", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, isConcurrencySuperseded(tt.conclusion),
			"isConcurrencySuperseded(%q)", tt.conclusion)
	}
}

func TestAssertNoHarnessAgentArtifact_IgnoresOtherAgentJobs(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// A run exists with a different agent's harness job.
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
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
		"org/repo/fullsend.yaml": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `expected harness "triage" not to run`)
}

func TestCountHarnessDispatches_JobsAPIError(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.Errors["ListWorkflowRunJobs"] = fmt.Errorf("jobs API error")

	d := newTestDriver(client)
	_, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jobs API error")
}

func TestAssertNoHarnessAgentArtifact_JobsAPIError(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.Errors["ListWorkflowRunJobs"] = fmt.Errorf("jobs API error")

	d := newTestDriver(client)
	err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jobs API error")
}

func TestHarnessJobSuffix(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Harness run (pr-ping)", harnessJobSuffix("pr-ping"))
	assert.Equal(t, "Harness run (triage)", harnessJobSuffix("triage"))
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

func TestNew_SetsAfterFunc(t *testing.T) {
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
		"org/repo/test.yaml": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForWorkflow(context.Background(), "org", "repo", "test.yaml", after, "")
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 10, run.ID)
}

func TestFindCompletedWorkflowRun_PollsWithTimerAfter(t *testing.T) {
	t.Parallel()

	client := forge.NewFakeClient()
	// No workflow runs — findCompletedWorkflowRunOnce always returns nil,
	// forcing the poll loop to call timerAfter at least once.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	d := &Driver{
		Client: client,
		afterFunc: func(_ time.Duration) <-chan time.Time {
			calls++
			if calls >= 2 {
				cancel()
				return make(chan time.Time) // block — forces ctx.Done()
			}
			ch := make(chan time.Time, 1)
			ch <- time.Now()
			return ch
		},
	}

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := d.FindCompletedWorkflowRun(ctx, "org", "repo", "test.yaml", after)
	require.Error(t, err)
	assert.GreaterOrEqual(t, calls, 1, "timerAfter should have been called")
}

func TestAssertNoWorkflow_NoRunsAfterTriggerTime(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := newTestDriver(forge.NewFakeClient())
	err := d.AssertNoWorkflow(context.Background(), "org", "repo", "test.yaml", after)
	require.NoError(t, err)
}

func TestDownloadNamedArtifactAfter_PollsWithTimerAfter(t *testing.T) {
	t.Parallel()

	client := forge.NewFakeClient()
	// Artifact with wrong name — won't match "wanted", forcing the poll
	// loop to exercise both timerAfter call sites.
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {{ID: 1, Name: "other-artifact", CreatedAt: "2026-01-02T00:00:00Z"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	d := &Driver{
		Client: client,
		afterFunc: func(_ time.Duration) <-chan time.Time {
			calls++
			if calls >= 3 {
				cancel()
				return make(chan time.Time) // block — forces ctx.Done()
			}
			ch := make(chan time.Time, 1)
			ch <- time.Now()
			return ch
		},
	}

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err := d.DownloadNamedArtifactAfter(ctx, "org", "repo", "wanted", after, t.TempDir())
	require.Error(t, err)
	assert.GreaterOrEqual(t, calls, 2, "timerAfter should have been called at least twice")
}

func TestTimerAfter_NilFallback(t *testing.T) {
	t.Parallel()

	// A zero-value Driver (no afterFunc) should fall back to time.After.
	d := &Driver{Client: forge.NewFakeClient()}
	ch := d.timerAfter(1 * time.Millisecond)
	select {
	case <-ch:
		// OK — fallback fired.
	case <-time.After(2 * time.Second):
		t.Fatal("timerAfter nil-fallback did not fire within 2s")
	}
}

func TestWaitForHarnessAgent_NoRealSleep(t *testing.T) {
	t.Parallel()
	start := time.Now()

	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {{ID: 1, Name: "fullsend-triage",
			CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 99}},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/success": {ID: 99, Status: "completed",
			Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
	}

	d := newTestDriver(client)

	run, err := d.WaitForHarnessAgent(context.Background(),
		"org", "repo", "triage",
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.NotNil(t, run)
	assert.Less(t, time.Since(start), 2*time.Second,
		"poll loop should not sleep on real wall-clock intervals")
}

// steppingClock returns a nowFunc that advances by step on every call,
// so deadline-based waits reach their timeout branch deterministically
// without sleeping. The first call returns start.
func steppingClock(start time.Time, step time.Duration) func() time.Time {
	var calls atomic.Int64
	return func() time.Time {
		n := calls.Add(1) - 1
		return start.Add(time.Duration(n) * step)
	}
}

// newTimeoutTestDriver returns a Driver whose harness waits time out
// after a handful of polls: instant timers plus a clock that advances one
// minute per reading.
func newTimeoutTestDriver(client forge.Client) *Driver {
	return &Driver{
		Client:    client,
		afterFunc: instantAfter,
		nowFunc:   steppingClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Minute),
	}
}

var errRateLimited = errors.New("github api: 403 retryable error after 5 attempts on GET /repos/org/repo/actions/workflows/fullsend.yaml/runs (last delay: 54s)")

// TestWaitForHarnessAgent_TimeoutReportsListingErrors covers the #6647
// attempt-1 shape: every listing call fails (rate limited) for the whole
// wait. The timeout must say so instead of "no recent workflow runs".
func TestWaitForHarnessAgent_TimeoutReportsListingErrors(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.Errors = map[string]error{
		"ListWorkflowRuns":        errRateLimited,
		"ListRepositoryArtifacts": errRateLimited,
	}

	d := newTimeoutTestDriver(client)
	_, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, `harness agent "triage" did not complete successfully`)
	assert.NotContains(t, msg, "no recent workflow runs found")
	assert.Contains(t, msg, "run listing failed: "+errRateLimited.Error())
	assert.Contains(t, msg, "artifact listing failed: "+errRateLimited.Error())
	// Both listings failed on every poll; the wait made at least one.
	assertAllPollsFailed(t, msg, "run listing failed")
	assertAllPollsFailed(t, msg, "artifact listing failed")
}

// assertAllPollsFailed checks that the "(failed on N of M polls ...)"
// tail following prefix reports N == M > 0 and carries the 403 text.
func assertAllPollsFailed(t *testing.T, msg, prefix string) {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(prefix) + `: .*?403.*?\(same error on (\d+) of (\d+) polls during the wait\)`)
	m := re.FindStringSubmatch(msg)
	require.NotNil(t, m, "no poll-failure tail after %q in: %s", prefix, msg)
	assert.Equal(t, m[1], m[2], "every poll should have failed")
	assert.NotEqual(t, "0", m[1], "the wait should have polled at least once")
}

// TestWaitForHarnessAgent_TimeoutReportsUnexpandedMatrix covers the #6647
// attempt-2 shape: the Harness dispatch job produced an empty matrix, so
// the only run after the trigger time concluded "success" with the
// harness matrix job skipped under its unexpanded name and no artifact
// was ever uploaded.
func TestWaitForHarnessAgent_TimeoutReportsUnexpandedMatrix(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 300, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-01T00:00:02Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/300",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		300: {
			{ID: 1, Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
			{ID: 2, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
			{ID: 3, Name: "dispatch / Harness run (${{ matrix.agent }})", Status: "completed", Conclusion: "skipped"},
		},
	}

	d := newTimeoutTestDriver(client)
	_, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "local-ping", after)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "run 300: status=completed conclusion=success")
	assert.Contains(t, msg, `harness matrix not expanded (job "dispatch / Harness run (${{ matrix.agent }})" conclusion=skipped): the "dispatch / Harness dispatch" job succeeded with an empty matrix`)
	assert.Contains(t, msg, "no repository artifacts listed")
	assert.NotContains(t, msg, "failed on", "no listing errors occurred")
	assert.NotContains(t, msg, "cut short", "no poll ran with an exhausted budget")
}

func TestDescribeAgentJob(t *testing.T) {
	t.Parallel()

	placeholder := forge.WorkflowJob{ID: 3, Name: "dispatch / Harness run (${{ matrix.agent }})", Status: "completed", Conclusion: "skipped"}
	cases := []struct {
		name string
		jobs []forge.WorkflowJob
		err  error
		want string
	}{
		{name: "lookup error", err: errRateLimited, want: "job lookup failed: " + errRateLimited.Error()},
		{name: "no jobs yet", jobs: []forge.WorkflowJob{}, want: "no jobs listed for run (not populated yet?)"},
		{name: "agent job present", jobs: []forge.WorkflowJob{{Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}},
			want: `agent job "dispatch / Harness run (triage)" status=completed conclusion=failure`},
		{name: "no agent job, no placeholder", jobs: []forge.WorkflowJob{{Name: "dispatch / Route", Status: "completed", Conclusion: "success"}},
			want: `no "Harness run (triage)" job in run`},
		{name: "placeholder, dispatch succeeded", jobs: []forge.WorkflowJob{{Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"}, placeholder},
			want: `harness matrix not expanded (job "dispatch / Harness run (${{ matrix.agent }})" conclusion=skipped): the "dispatch / Harness dispatch" job succeeded with an empty matrix, so no harness was dispatched for this event (possible causes include no registered harness triggers, no trigger matching the event, the actor's role not resolving or not being authorized, or a kill switch); see that job's log`},
		{name: "placeholder, dispatch failed", jobs: []forge.WorkflowJob{{Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "failure"}, placeholder},
			want: `harness matrix not expanded (job "dispatch / Harness run (${{ matrix.agent }})" conclusion=skipped): the "dispatch / Harness dispatch" job concluded failure; see its log`},
		{name: "placeholder, no dispatch job", jobs: []forge.WorkflowJob{placeholder},
			want: `harness matrix not expanded (job "dispatch / Harness run (${{ matrix.agent }})" conclusion=skipped): no "Harness dispatch" job in run to attribute it to`},
		{name: "placeholder cancelled before expansion", jobs: []forge.WorkflowJob{
			{Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
			{Name: "dispatch / Harness run (${{ matrix.agent }})", Status: "completed", Conclusion: "cancelled"}},
			want: `harness matrix not expanded (job "dispatch / Harness run (${{ matrix.agent }})" conclusion=cancelled): the run was cancelled before the matrix was evaluated, not dispatched with an empty matrix; see the run`},
		{name: "placeholder failed without expanding", jobs: []forge.WorkflowJob{
			{Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
			{Name: "dispatch / Harness run (${{ matrix.agent }})", Status: "completed", Conclusion: "failure"}},
			want: `harness matrix not expanded (job "dispatch / Harness run (${{ matrix.agent }})" conclusion=failure): the matrix job concluded failure without expanding; see the run`},
		{name: "placeholder timed out without expanding", jobs: []forge.WorkflowJob{
			{Name: "dispatch / Harness run (${{ matrix.agent }})", Status: "completed", Conclusion: "timed_out"}},
			want: `harness matrix not expanded (job "dispatch / Harness run (${{ matrix.agent }})" conclusion=timed_out): the matrix job concluded timed_out without expanding; see the run`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := forge.NewFakeClient()
			client.WorkflowRunJobs = map[int][]forge.WorkflowJob{7: tc.jobs}
			if tc.err != nil {
				client.Errors = map[string]error{"ListWorkflowRunJobs": tc.err}
			}
			d := newTestDriver(client)
			assert.Equal(t, tc.want, d.describeAgentJob(context.Background(), "org", "repo", 7, "triage"))
		})
	}
}

// TestWaitForHarnessAgent_TimeoutReportsAgentJobAndArtifactRejection
// checks the per-run agent job state and the artifact classification
// against the trigger time.
func TestWaitForHarnessAgent_TimeoutReportsAgentJobAndArtifactRejection(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 400, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-01T12:00:05Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/400",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		400: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
	}
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 50, Name: "fullsend-triage", CreatedAt: "2026-01-01T11:59:59Z", WorkflowRunID: 399},
			{ID: 51, Name: "fullsend-other", CreatedAt: "2026-01-01T12:00:30Z", WorkflowRunID: 400},
			{ID: 52, Name: "fullsend-triage", CreatedAt: "2026-01-01T12:00:30Z", WorkflowRunID: 400},
		},
	}
	// The eligible artifact's run lookup fails on every poll: the wait
	// times out and the diagnostics must name the lookup failure.
	client.Errors = map[string]error{"GetWorkflowRun": errRateLimited}

	d := newTimeoutTestDriver(client)
	_, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, `agent job "dispatch / Harness run (triage)" status=completed conclusion=success`)
	assert.Contains(t, msg, `artifact "fullsend-triage" listed 2 time(s)`)
	assert.Contains(t, msg, "artifact 50: run=399 created_at=2026-01-01T11:59:59Z (rejected: created before trigger time 2026-01-01T12:00:00Z)")
	assert.Contains(t, msg, "artifact 52: run=400 created_at=2026-01-01T12:00:30Z (eligible by trigger time; not explained by the listing")
	assert.Regexp(t, `; run lookups \(failed on (\d+) of (\d+) lookups during the wait; last: .*403`, msg)
	assert.NotContains(t, msg, "cut short")
}

// TestWaitForHarnessAgent_PollsBoundedByRemainingBudget verifies each
// poll's API calls carry a deadline no later than the wait deadline, so
// client-side retries cannot overrun dispatchWait.
func TestWaitForHarnessAgent_PollsBoundedByRemainingBudget(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var remaining []time.Duration
	client := &deadlineRecordingClient{
		FakeClient: forge.NewFakeClient(),
		onCall: func(ctx context.Context) {
			dl, ok := ctx.Deadline()
			mu.Lock()
			defer mu.Unlock()
			if !ok {
				remaining = append(remaining, -1)
				return
			}
			remaining = append(remaining, time.Until(dl))
		},
	}

	d := &Driver{Client: client, afterFunc: instantAfter, nowFunc: steppingClock(start, time.Minute)}
	_, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", start)
	require.Error(t, err)
	mu.Lock()
	defer mu.Unlock()
	// Two listings per poll, then two diagnostics lookups at the end.
	require.GreaterOrEqual(t, len(remaining), 4)
	const slack = time.Second
	polls, diag := remaining[:len(remaining)-2], remaining[len(remaining)-2:]
	for i, r := range polls {
		assert.GreaterOrEqual(t, r, pollMinBudget-slack, "poll call %d started with less than the minimum budget", i)
		assert.LessOrEqual(t, r, dispatchWait+slack, "poll call %d exceeded the wait budget", i)
	}
	// Each iteration reads the clock once, so consecutive polls have
	// strictly less budget.
	for i := 2; i < len(polls); i += 2 {
		assert.Less(t, polls[i], polls[i-2], "poll budget should shrink each iteration")
	}
	for i, r := range diag {
		assert.Greater(t, r, time.Duration(0), "diagnostics lookup %d must carry a deadline", i)
		assert.LessOrEqual(t, r, lookupBudget+slack, "diagnostics lookup %d exceeded lookupBudget", i)
	}
}

// deadlineRecordingClient wraps FakeClient and reports the context of
// each artifact and run listing call.
type deadlineRecordingClient struct {
	*forge.FakeClient
	onCall func(ctx context.Context)
}

func (c *deadlineRecordingClient) ListRepositoryArtifacts(ctx context.Context, owner, repo string, perPage int) ([]forge.RepositoryArtifact, error) {
	c.onCall(ctx)
	return c.FakeClient.ListRepositoryArtifacts(ctx, owner, repo, perPage)
}

func (c *deadlineRecordingClient) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]forge.WorkflowRun, error) {
	c.onCall(ctx)
	return c.FakeClient.ListWorkflowRuns(ctx, owner, repo, workflowFile)
}

// blockingClient wraps FakeClient so listing calls block until their
// context expires, returning the context error — the shape of a lookup
// the driver's own budget cuts short.
type blockingClient struct {
	*forge.FakeClient
}

func (c *blockingClient) ListWorkflowRuns(ctx context.Context, _, _, _ string) ([]forge.WorkflowRun, error) {
	<-ctx.Done()
	return nil, fmt.Errorf("http GET runs: %w", ctx.Err())
}

func (c *blockingClient) ListRepositoryArtifacts(ctx context.Context, _, _ string, _ int) ([]forge.RepositoryArtifact, error) {
	<-ctx.Done()
	return nil, fmt.Errorf("http GET artifacts: %w", ctx.Err())
}

// TestHarnessTimeoutDiagnostics_CutShortVsLiveDeadlineError covers the
// attempt-1 shape at diagnostics time. A lookup cut short by the budget
// must headline the informative poll error rather than the driver's own
// cutoff; a deadline error returned while the lookup context is still
// live is a real failure and must be reported as such.
func TestHarnessTimeoutDiagnostics_CutShortVsLiveDeadlineError(t *testing.T) {
	t.Parallel()

	var runsErrs, artifactErrs pollErrors
	for i := 0; i < 3; i++ {
		runsErrs.record(context.Background(), errRateLimited)
		artifactErrs.record(context.Background(), errRateLimited)
	}

	t.Run("cut short by budget", func(t *testing.T) {
		t.Parallel()
		// The parent deadline propagates into the diagnostics envelope
		// and each lookup slice, so the blocking client returns as a
		// budget cutoff without waiting out lookupBudget.
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		d := newTestDriver(&blockingClient{FakeClient: forge.NewFakeClient()})
		got := d.harnessTimeoutDiagnostics(ctx, "org", "repo", "triage", time.Now(), runsErrs, artifactErrs, pollErrors{})
		assert.Contains(t, got, "run listing cut short by the diagnostics budget; last poll error (3 of 3 polls failed): "+errRateLimited.Error())
		assert.Contains(t, got, "artifact listing cut short by the diagnostics budget; last poll error (3 of 3 polls failed): "+errRateLimited.Error())
		assert.NotContains(t, got, "listing failed: http GET")
	})

	t.Run("cut short with no poll error still names the cutoff", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		d := newTestDriver(&blockingClient{FakeClient: forge.NewFakeClient()})
		got := d.harnessTimeoutDiagnostics(ctx, "org", "repo", "triage", time.Now(), pollErrors{}, pollErrors{}, pollErrors{})
		assert.Equal(t, "run listing cut short by the diagnostics budget; artifact listing cut short by the diagnostics budget", got)
	})

	t.Run("deadline error under live context is a real failure", func(t *testing.T) {
		t.Parallel()
		timeout := fmt.Errorf("http GET: %w", context.DeadlineExceeded) // e.g. an HTTP client timeout
		client := forge.NewFakeClient()
		client.Errors = map[string]error{
			"ListWorkflowRuns":        timeout,
			"ListRepositoryArtifacts": timeout,
		}
		d := newTestDriver(client)
		got := d.harnessTimeoutDiagnostics(context.Background(), "org", "repo", "triage", time.Now(), runsErrs, artifactErrs, pollErrors{})
		assert.Contains(t, got, "run listing failed: "+timeout.Error()+" (failed on 3 of 3 polls during the wait; last: "+errRateLimited.Error()+")")
		assert.Contains(t, got, "artifact listing failed: "+timeout.Error()+" (failed on 3 of 3 polls during the wait; last: "+errRateLimited.Error()+")")
		assert.NotContains(t, got, "cut short")
	})
}

func TestWaitForFailedHarnessAgent_TimeoutReportsListingErrors(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.Errors = map[string]error{
		"ListWorkflowRuns":        errRateLimited,
		"ListRepositoryArtifacts": errRateLimited,
	}

	d := newTimeoutTestDriver(client)
	_, err := d.WaitForFailedHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, `harness agent "triage" did not complete with a failure`)
	assert.NotContains(t, msg, "no recent workflow runs found")
	assert.Contains(t, msg, "run listing failed: "+errRateLimited.Error())
	assert.Contains(t, msg, "artifact listing failed: "+errRateLimited.Error())
	assert.Contains(t, msg, "same error on")
	assert.Contains(t, msg, "polls during the wait")
}

func TestPollErrors_KeepsInformativeErrorOverBudgetCutoff(t *testing.T) {
	t.Parallel()

	live := context.Background()
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	var p pollErrors
	p.record(live, nil)
	p.record(live, errRateLimited)
	p.record(expired, fmt.Errorf("http GET: %w", context.DeadlineExceeded))
	assert.Equal(t, 3, p.calls)
	assert.Equal(t, 1, p.failed)
	assert.Equal(t, 1, p.cutShort)
	assert.ErrorIs(t, p.last, errRateLimited)
	assert.Equal(t, " (failed on 1 of 3 polls during the wait; last: "+errRateLimited.Error()+") (1 of 3 polls cut short by the wait budget)", p.describe(nil, "polls"))
	assert.Equal(t, " (same error on 1 of 3 polls during the wait) (1 of 3 polls cut short by the wait budget)", p.describe(errRateLimited, "polls"))

	// A deadline error under a live context is a real failure (e.g. an
	// HTTP client timeout), not a budget cutoff — and so is one under a
	// context that was cancelled rather than expired.
	var q pollErrors
	q.record(live, context.DeadlineExceeded)
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	q.record(cancelled, fmt.Errorf("http GET: %w", context.DeadlineExceeded))
	assert.Equal(t, 2, q.failed)
	assert.Equal(t, 0, q.cutShort)

	// Jittered retry delays do not defeat the same-error collapse.
	var r pollErrors
	r.record(live, errors.New("github api: 403 retryable error after 5 attempts on GET /x (last delay: 54.2s)"))
	assert.Equal(t, " (same error on 1 of 1 polls during the wait)",
		r.describe(errors.New("github api: 403 retryable error after 5 attempts on GET /x (last delay: 31.7s)"), "polls"))

	assert.Empty(t, pollErrors{}.describe(nil, "polls"))
}

func TestFormatArtifactDiagnostics(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, "no repository artifacts listed", formatArtifactDiagnostics(nil, "fullsend-agent", after))

	notFound := formatArtifactDiagnostics([]forge.RepositoryArtifact{
		{ID: 1, Name: "fullsend-other", CreatedAt: "2026-01-01T12:00:00Z"},
		{ID: 2, Name: "fullsend-another", CreatedAt: "2026-01-01T12:00:00Z"},
	}, "fullsend-agent", after)
	assert.Equal(t, `artifact "fullsend-agent" not among the 2 listed artifacts (listing is capped at 100 newest)`, notFound)

	classified := formatArtifactDiagnostics([]forge.RepositoryArtifact{
		{ID: 10, Name: "fullsend-agent", CreatedAt: "2026-01-01T11:59:59Z", WorkflowRunID: 99},
		{ID: 11, Name: "fullsend-agent", CreatedAt: "not-a-time", WorkflowRunID: 100},
		{ID: 12, Name: "fullsend-agent", CreatedAt: "2026-01-01T12:00:30Z", WorkflowRunID: 101},
		{ID: 13, Name: "fullsend-other", CreatedAt: "2026-01-01T12:00:30Z", WorkflowRunID: 101},
	}, "fullsend-agent", after)
	assert.Contains(t, classified, `artifact "fullsend-agent" listed 3 time(s):`)
	assert.Contains(t, classified, "artifact 10: run=99 created_at=2026-01-01T11:59:59Z (rejected: created before trigger time 2026-01-01T12:00:00Z)")
	assert.Contains(t, classified, "artifact 11: run=100 created_at=not-a-time (rejected: unparseable created_at)")
	assert.Contains(t, classified, "artifact 12: run=101 created_at=2026-01-01T12:00:30Z (eligible by trigger time; not explained by the listing")
	assert.NotContains(t, classified, "artifact 13")
}

// TestWaitForHarnessAgent_SkipsPollBelowMinimumBudget drives the clock so
// the first poll would start with only 3s of budget: it must be skipped
// and the wait must go straight to diagnostics (whose two lookups are
// the only calls made).
func TestWaitForHarnessAgent_SkipsPollBelowMinimumBudget(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	readings := []time.Time{start, start.Add(dispatchWait - 3*time.Second)}
	var idx atomic.Int64
	clock := func() time.Time {
		i := int(idx.Add(1) - 1)
		if i >= len(readings) {
			return readings[len(readings)-1]
		}
		return readings[i]
	}

	var mu sync.Mutex
	var calls int
	client := &deadlineRecordingClient{
		FakeClient: forge.NewFakeClient(),
		onCall: func(context.Context) {
			mu.Lock()
			calls++
			mu.Unlock()
		},
	}
	d := &Driver{Client: client, afterFunc: instantAfter, nowFunc: clock}
	_, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", start)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "cut short")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, calls, "only the two diagnostics listings should run")
}

func TestFormatRunDiagnosticsWithJobs(t *testing.T) {
	t.Parallel()

	client := forge.NewFakeClient()
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		1: {{Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}},
	}
	d := newTestDriver(client)
	ctx := context.Background()

	assert.Equal(t, "no recent workflow runs found after trigger time", d.formatRunDiagnosticsWithJobs(ctx, "org", "repo", "triage", nil))

	got := d.formatRunDiagnosticsWithJobs(ctx, "org", "repo", "triage", []forge.WorkflowRun{
		{ID: 1, Status: "completed", Conclusion: "failure", HTMLURL: "https://github.com/org/repo/actions/runs/1"},
		{ID: 2, Status: "in_progress", HTMLURL: "https://github.com/org/repo/actions/runs/2"},
	})
	assert.Equal(t, "recent workflow runs (2):"+
		"\n  run 1: status=completed conclusion=failure url=https://github.com/org/repo/actions/runs/1; agent job \"dispatch / Harness run (triage)\" status=completed conclusion=failure"+
		"\n  run 2: status=in_progress conclusion= url=https://github.com/org/repo/actions/runs/2", got)
}
