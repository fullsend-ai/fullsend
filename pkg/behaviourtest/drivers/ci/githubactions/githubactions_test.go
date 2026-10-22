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
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
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

func TestDispatchWait_AtLeast20Minutes(t *testing.T) {
	t.Parallel()

	// Raised from 12 to 20 minutes so longer-running playback scenarios
	// (which replay a full dummy-agent pipeline rather than a single
	// dispatch) have enough budget to complete without timing out.
	assert.GreaterOrEqual(t, dispatchWait, 20*time.Minute,
		"harness wait budget (%v) should be at least 20 minutes", dispatchWait)
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

func TestCountHarnessDispatches_MatchesBuiltinRoleJobName(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): built-in stages (triage, code, review,
	// fix, retro, prioritize) run as a fixed per-role job named after the
	// stage (e.g. "Triage"), not as a "Harness run (<agent>)" matrix job
	// — that naming is only used for user-registered custom harnesses.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Triage", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_BuiltinRoleJobNameDoesNotMatchOtherAgent(t *testing.T) {
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
		10: {{ID: 1, Name: "dispatch / Triage", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "code", after)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
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

func TestCountHarnessDispatches_FractionalSecondBoundaryExcludesSameSecondRun(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): forge-reported CreatedAt timestamps have
	// second precision, but an unnormalized trigger boundary (e.g. a bare
	// time.Now()) carries a fractional component. A run created in the
	// same wall-clock second as the boundary can therefore parse as
	// earlier than it and be excluded — this is exactly why callers (see
	// steps.whenIssueLabeled) must truncate the boundary to second
	// precision before calling CountHarnessDispatches. This test pins
	// both halves of that contract: the untruncated boundary excludes the
	// same-second run, and the truncated boundary includes it.
	runCreatedAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	fractionalAfter := runCreatedAt.Add(500 * time.Millisecond)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: runCreatedAt.Format(time.RFC3339)},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", fractionalAfter)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "an unnormalized fractional-second boundary excludes a same-second run")

	count, err = d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", fractionalAfter.Truncate(time.Second))
	require.NoError(t, err)
	assert.Equal(t, 1, count, "truncating the boundary to forge timestamp precision includes the same-second run")
}

func TestWaitForHarnessAgentRound_SelectsEarliestEligibleRun(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): both review rounds have already
	// completed successfully by the time this (first round's) wait
	// runs. WaitForHarnessAgent's highest-ID selection would pick run
	// 200 (the second round); WaitForHarnessAgentRound must pick the
	// earliest eligible one instead when nothing has been consumed yet.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
		200: {{ID: 2, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID, "the earliest eligible run should be selected, not the highest-ID one")
}

func TestWaitForHarnessAgentRound_SkipsConsumedRuns(t *testing.T) {
	t.Parallel()

	// Once a round's run is recorded as consumed, the next call must
	// select the next earliest eligible run instead of re-matching it.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
		200: {{ID: 2, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, map[int]bool{100: true})
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

// truncatingListWorkflowRunsClient wraps FakeClient so ListWorkflowRuns
// returns only a configured, truncated subset — simulating the live
// GitHub client's per_page=10 cap with no pagination (#7996 review).
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
// is a regression test (#7996 review): listHarnessRunsAfter's shared
// listing (used by WaitForHarnessAgent's latest-eligible-run selection)
// calls ListWorkflowRuns, whose live implementation requests only the
// newest 10 runs with no pagination. Ten newer harness runs for another
// agent can push the earliest eligible, not-yet-consumed run for this
// agent off that single page entirely — sorting and checking consumed
// afterward cannot recover a run the listing never returned.
// harnessRoundPollOnce's earliest-round selection must instead use
// ListWorkflowRunsSince, which paginates back to the after boundary, so it
// still finds the earliest eligible run even when a truncated
// ListWorkflowRuns listing (simulated here) omits it.
func TestWaitForHarnessAgentRound_SelectsEarliestEligibleRunBeyondTruncatedPage(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()

	// The earliest eligible "review" run, plus ten newer "triage" runs
	// that would fill (and, on the live client, truncate) a single
	// ListWorkflowRuns page.
	full := []forge.WorkflowRun{
		{ID: 100, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
	}
	jobs := map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
	}
	for i := 0; i < 10; i++ {
		id := 200 + i
		full = append(full, forge.WorkflowRun{
			ID: id, Status: "completed", Conclusion: "success",
			CreatedAt: fmt.Sprintf("2026-01-02T01:%02d:00Z", i),
		})
		jobs[id] = []forge.WorkflowJob{{ID: 2, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}}
	}
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{"org/repo/fullsend.yaml": full}
	fake.WorkflowRunJobs = jobs

	// Simulate the live client's truncated single page: the ten newer
	// "triage" runs fill it, omitting the earliest eligible "review" run
	// (id 100) that ListWorkflowRunsSince would still find.
	client := &truncatingListWorkflowRunsClient{FakeClient: fake, truncated: full[1:]}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID, "the earliest eligible run must be found even though a truncated listing omits it")
}

func TestWaitForHarnessAgentRound_FailFastOnGenuineFailure(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 100, Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z",
				HTMLURL: "https://github.com/org/repo/actions/runs/100"},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "failure"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.Error(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID)
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
		"org/repo/fullsend.yaml": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		200: {{ID: 2, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
	}
	client := &erroringRunJobsClient{FakeClient: fake, errRun: 100, err: fmt.Errorf("jobs API error")}

	d := newTimeoutTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.Error(t, err)
	assert.Nil(t, run, "run 200 must not be selected while run 100's job lookup is unresolved")
}

// unexpandedThenResolvedJobsClient wraps FakeClient so ListWorkflowRunJobs
// returns an in-flight "Route" job (the dispatch job that computes the
// harness matrix, not yet expanded) for targetRun on the first callsLeft
// calls, then falls back to the FakeClient's configured job list — as if
// the earlier run's own agent job appeared on a subsequent poll.
type unexpandedThenResolvedJobsClient struct {
	*forge.FakeClient
	mu        sync.Mutex
	targetRun int
	callsLeft int
}

func (c *unexpandedThenResolvedJobsClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	if runID == c.targetRun {
		c.mu.Lock()
		if c.callsLeft > 0 {
			c.callsLeft--
			c.mu.Unlock()
			return []forge.WorkflowJob{{ID: 99, Name: "dispatch / Route", Status: "in_progress"}}, nil
		}
		c.mu.Unlock()
	}
	return c.FakeClient.ListWorkflowRunJobs(ctx, owner, repo, runID)
}

func TestWaitForHarnessAgentRound_EarlierRunJobAppearsOnSubsequentPoll(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): run 100 (earliest, still in_progress)
	// has not expanded its matrix yet on the first poll — only the
	// dispatch job that computes it ("Route") is visible, so agent's
	// absence is inconclusive. Run 200 (later) already has a successful
	// agent job. Before the fix, an unexpanded matrix was treated the
	// same as "agent not scheduled here" and the scan fell through to
	// run 200. The round must instead wait for run 100 to resolve, and
	// once its own agent job appears (and succeeds) on a later poll,
	// select run 100 rather than run 200.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
		200: {{ID: 2, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
	}
	client := &unexpandedThenResolvedJobsClient{FakeClient: fake, targetRun: 100, callsLeft: 1}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID, "the earliest run must be selected once its job appears, not the later already-resolved run")
}

type routePendingWithUnrelatedMatrixClient struct {
	*forge.FakeClient
	mu        sync.Mutex
	targetRun int
	callsLeft int
}

func (c *routePendingWithUnrelatedMatrixClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	if runID == c.targetRun {
		c.mu.Lock()
		if c.callsLeft > 0 {
			c.callsLeft--
			c.mu.Unlock()
			return []forge.WorkflowJob{
				{ID: 99, Name: "dispatch / Route", Status: "in_progress"},
				{ID: 98, Name: "dispatch / Harness run (other-agent)", Status: "completed", Conclusion: "success"},
			}, nil
		}
		c.mu.Unlock()
	}
	return c.FakeClient.ListWorkflowRunJobs(ctx, owner, repo, runID)
}

func TestWaitForHarnessAgentRound_RoutePendingBlocksUnrelatedExpandedMatrix(t *testing.T) {
	t.Parallel()

	// Regression (#7996 review): an unrelated matrix can expand while the
	// Route job that computes the requested built-in stage is still pending.
	// The unrelated expansion must not make the earliest run look resolved and
	// allow a later successful run to satisfy this round.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
		200: {{ID: 2, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
	}
	client := &routePendingWithUnrelatedMatrixClient{FakeClient: fake, targetRun: 100, callsLeft: 1}

	run, err := newTestDriver(client).WaitForHarnessAgentRound(context.Background(), "org", "repo", "triage", after, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID, "a pending Route job must keep the earliest run authoritative")
}

// skippedBuiltinThenMatrixJobsClient wraps FakeClient so ListWorkflowRunJobs
// returns only targetRun's skipped built-in stage job (no "Harness run ("
// marker, so the matrix is unresolved) for the first callsLeft calls, then
// falls back to the FakeClient's configured job list — as if the matrix job
// for the same agent name appeared and succeeded on a subsequent poll.
type skippedBuiltinThenMatrixJobsClient struct {
	*forge.FakeClient
	mu        sync.Mutex
	targetRun int
	callsLeft int
}

func (c *skippedBuiltinThenMatrixJobsClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	if runID == c.targetRun {
		c.mu.Lock()
		if c.callsLeft > 0 {
			c.callsLeft--
			c.mu.Unlock()
			return []forge.WorkflowJob{{ID: 91, Name: "dispatch / Review", Status: "completed", Conclusion: "skipped"}}, nil
		}
		c.mu.Unlock()
	}
	return c.FakeClient.ListWorkflowRunJobs(ctx, owner, repo, runID)
}

// cancelledBuiltinThenMatrixJobsClient mirrors skippedBuiltinThenMatrixJobsClient,
// but targetRun's static job is cancelled rather than skipped — the static
// job and a same-named custom harness matrix job run in distinct
// concurrency groups, so the static job's own cancellation says nothing
// about whether the matrix job will still appear and run (#7996 review).
type cancelledBuiltinThenMatrixJobsClient struct {
	*forge.FakeClient
	mu        sync.Mutex
	targetRun int
	callsLeft int
}

func (c *cancelledBuiltinThenMatrixJobsClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	if runID == c.targetRun {
		c.mu.Lock()
		if c.callsLeft > 0 {
			c.callsLeft--
			c.mu.Unlock()
			return []forge.WorkflowJob{{ID: 91, Name: "dispatch / Review", Status: "completed", Conclusion: "cancelled"}}, nil
		}
		c.mu.Unlock()
	}
	return c.FakeClient.ListWorkflowRunJobs(ctx, owner, repo, runID)
}

func TestWaitForHarnessAgentRound_SkippedBuiltinMatrixUnresolvedWaits(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review, second pass): run 100 (earliest,
	// in_progress) only shows its skipped built-in "Review" stage job on
	// the first poll — the matrix that would carry a same-named custom
	// harness job has not resolved yet (no "Harness run (" job present).
	// matchAgentJob reports a match on that skipped built-in job, so the
	// !hasJob guard alone does not hold off run 200 (later, already
	// successful). Before the fix, isConcurrencySuperseded treated the
	// skipped conclusion as settled and let the scan fall through to run
	// 200. The round must instead wait, and once run 100's own matrix job
	// appears (and succeeds) on a later poll, select run 100.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {
			{ID: 91, Name: "dispatch / Review", Status: "completed", Conclusion: "skipped"},
			{ID: 92, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"},
		},
		200: {{ID: 2, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
	}
	client := &skippedBuiltinThenMatrixJobsClient{FakeClient: fake, targetRun: 100, callsLeft: 1}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID, "the earliest run must be selected once its matrix job appears, not the later already-resolved run")
}

// TestWaitForHarnessAgentRound_CancelledBuiltinMatrixUnresolvedWaits is a
// regression test for the #7996 review: like the skipped-builtin case
// above, run 100 (earliest, in_progress) only shows its cancelled built-in
// "Review" stage job on the first poll — the matrix that would carry a
// same-named custom harness job has not resolved yet. Before the fix,
// isConcurrencySuperseded treated the cancelled conclusion as settled
// ("continue"), letting the scan fall through to run 200 (later, already
// successful) and select the wrong round. The round must instead wait, and
// once run 100's own matrix job appears (and succeeds) on a later poll,
// select run 100.
func TestWaitForHarnessAgentRound_CancelledBuiltinMatrixUnresolvedWaits(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {
			{ID: 91, Name: "dispatch / Review", Status: "completed", Conclusion: "cancelled"},
			{ID: 92, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"},
		},
		200: {{ID: 2, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
	}
	client := &cancelledBuiltinThenMatrixJobsClient{FakeClient: fake, targetRun: 100, callsLeft: 1}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID, "the earliest run must be selected once its matrix job appears, not the later already-resolved run")
}

// successfulBuiltinThenFailedMatrixJobsClient wraps FakeClient so
// ListWorkflowRunJobs returns only targetRun's successful built-in stage
// job (no "Harness run (" marker and no "Route" job, so the matrix is
// unresolved) for the first callsLeft calls, then falls back to the
// FakeClient's configured job list — as if the matrix job for the same
// agent name appeared afterward and concluded with a genuine failure.
type successfulBuiltinThenFailedMatrixJobsClient struct {
	*forge.FakeClient
	mu        sync.Mutex
	targetRun int
	callsLeft int
}

func (c *successfulBuiltinThenFailedMatrixJobsClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	if runID == c.targetRun {
		c.mu.Lock()
		if c.callsLeft > 0 {
			c.callsLeft--
			c.mu.Unlock()
			return []forge.WorkflowJob{{ID: 91, Name: "dispatch / Triage", Status: "completed", Conclusion: "success"}}, nil
		}
		c.mu.Unlock()
	}
	return c.FakeClient.ListWorkflowRunJobs(ctx, owner, repo, runID)
}

// TestWaitForHarnessAgentRound_SuccessfulBuiltinMatrixUnresolvedWaits is a
// regression test for the #7996 review (second pass): the unresolved-matrix
// guard previously only protected skipped/cancelled matches. Run 100
// (earliest, in_progress) shows only its successful built-in "Triage" stage
// job on the first poll — the matrix that would carry a same-named custom
// harness job has not resolved yet. Before the fix, a successful built-in
// match fell straight through to the `job.Conclusion == "success"` return,
// settling the round as successful on the very first poll without ever
// seeing the real "Harness run (triage)" job that later concludes with a
// genuine failure. The round must instead wait for the matrix to resolve,
// and once the real matrix job appears (and fails), report that failure
// for run 100 rather than the built-in job's unrelated success.
func TestWaitForHarnessAgentRound_SuccessfulBuiltinMatrixUnresolvedWaits(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 100, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {
			{ID: 91, Name: "dispatch / Triage", Status: "completed", Conclusion: "success"},
			{ID: 92, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"},
		},
	}
	client := &successfulBuiltinThenFailedMatrixJobsClient{FakeClient: fake, targetRun: 100, callsLeft: 1}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "triage", after, nil)
	require.Error(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID)
	assert.Contains(t, err.Error(), `concluded with "failure"`,
		"the real matrix job's failure must be reported instead of the unrelated built-in job's success")
}

// completedRoutePendingDispatchThenMatrixJobsClient wraps FakeClient so
// ListWorkflowRunJobs returns targetRun's completed Route job, a skipped
// static stage job, and a still-running Harness dispatch job (no
// "Harness run (" marker, so the matrix has not expanded) for the first
// callsLeft calls, then falls back to the FakeClient's configured job
// list — as if the matrix job for the same agent name appeared and
// succeeded on a subsequent poll.
type completedRoutePendingDispatchThenMatrixJobsClient struct {
	*forge.FakeClient
	mu        sync.Mutex
	targetRun int
	callsLeft int
}

func (c *completedRoutePendingDispatchThenMatrixJobsClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	if runID == c.targetRun {
		c.mu.Lock()
		if c.callsLeft > 0 {
			c.callsLeft--
			c.mu.Unlock()
			return []forge.WorkflowJob{
				{ID: 90, Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
				{ID: 91, Name: "dispatch / Review", Status: "completed", Conclusion: "skipped"},
				{ID: 93, Name: "dispatch / Harness dispatch", Status: "in_progress"},
			}, nil
		}
		c.mu.Unlock()
	}
	return c.FakeClient.ListWorkflowRunJobs(ctx, owner, repo, runID)
}

// TestWaitForHarnessAgentRound_CompletedRouteStillWaitsForPendingHarnessDispatch
// is a regression test for the #7996 review: Route and Harness dispatch are
// independent jobs, and the custom-harness matrix depends on Harness
// dispatch, not Route. Run 100 (earliest, in_progress) shows a completed
// Route job, a skipped static "Review" job, and a still-running Harness
// dispatch job on the first poll — the matrix that would carry a same-named
// custom harness job has not expanded yet. Before the fix,
// harnessMatrixUnresolved treated the completed Route job as proof the
// matrix had resolved, so the skipped static match was treated as settled
// and the scan fell through to run 200 (later, already successful). The
// round must instead wait, and once run 100's own matrix job appears (and
// succeeds) on a later poll, select run 100.
func TestWaitForHarnessAgentRound_CompletedRouteStillWaitsForPendingHarnessDispatch(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {
			{ID: 90, Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
			{ID: 91, Name: "dispatch / Review", Status: "completed", Conclusion: "skipped"},
			{ID: 92, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"},
		},
		200: {{ID: 2, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
	}
	client := &completedRoutePendingDispatchThenMatrixJobsClient{FakeClient: fake, targetRun: 100, callsLeft: 1}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID, "the earliest run must be selected once its matrix job appears, not the later already-resolved run")
}

// completedProducersNoMatrixThenMatrixJobsClient wraps FakeClient so
// ListWorkflowRunJobs returns targetRun's completed Route job, completed
// Harness dispatch job, and a skipped static stage job — with no matrix
// job or empty-matrix placeholder visible yet — for the first callsLeft
// calls, then falls back to the FakeClient's configured job list. It
// models downstream job visibility lagging the producer jobs' completion.
type completedProducersNoMatrixThenMatrixJobsClient struct {
	*forge.FakeClient
	mu        sync.Mutex
	targetRun int
	callsLeft int
}

func (c *completedProducersNoMatrixThenMatrixJobsClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	if runID == c.targetRun {
		c.mu.Lock()
		if c.callsLeft > 0 {
			c.callsLeft--
			c.mu.Unlock()
			return []forge.WorkflowJob{
				{ID: 90, Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
				{ID: 91, Name: "dispatch / Review", Status: "completed", Conclusion: "skipped"},
				{ID: 93, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
			}, nil
		}
		c.mu.Unlock()
	}
	return c.FakeClient.ListWorkflowRunJobs(ctx, owner, repo, runID)
}

// TestWaitForHarnessAgentRound_CompletedProducersWithoutMatrixStillWait is
// a regression test for the #7996 review: completed Route and Harness
// dispatch jobs do not prove the matrix has resolved, because downstream
// job visibility can lag producer completion. Run 100 (earliest,
// in_progress) shows both producers completed but no matrix job or
// empty-matrix placeholder on the first poll; the requested matrix job
// appears on the next poll. The round must wait and select run 100 rather
// than skipping it for the later, already-successful run 200.
func TestWaitForHarnessAgentRound_CompletedProducersWithoutMatrixStillWait(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 200, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T01:00:00Z"},
			{ID: 100, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {
			{ID: 90, Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
			{ID: 91, Name: "dispatch / Review", Status: "completed", Conclusion: "skipped"},
			{ID: 93, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
			{ID: 92, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"},
		},
		200: {{ID: 2, Name: "dispatch / Harness run (review)", Status: "completed", Conclusion: "success"}},
	}
	client := &completedProducersNoMatrixThenMatrixJobsClient{FakeClient: fake, targetRun: 100, callsLeft: 1}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgentRound(context.Background(), "org", "repo", "review", after, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 100, run.ID, "the earliest run must be selected once its matrix job appears, not the later already-resolved run")
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
	require.NotNil(t, run, "the failed run is returned with the error so its logs can be saved")
	assert.Equal(t, 42, run.ID)
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
	require.NotNil(t, run, "the failed run is returned with the error so its logs can be saved")
	assert.Equal(t, 50, run.ID)
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
	require.NotNil(t, run, "the failed run is returned with the error so its logs can be saved")
	assert.Equal(t, 60, run.ID)
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
func TestHarnessPollOnce_SkippedBuiltinJobDoesNotTriggerFailFast(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): builtinRoleJobName makes matchAgentJob
	// also match a built-in stage's static job name (e.g. "Fix"), which a
	// run's job list carries even when that stage was skipped for this
	// particular trigger. The run's overall "failure" here comes entirely
	// from a sibling "Code" job; the "fix" agent's own job never ran, so
	// it must not be fail-fasted.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 55, Status: "completed", Conclusion: "failure",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/55",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		55: {
			{ID: 1, Name: "dispatch / Code", Status: "completed", Conclusion: "failure"},
			{ID: 2, Name: "dispatch / Fix", Status: "completed", Conclusion: "skipped"},
		},
	}

	d := newTestDriver(client)
	var artifactErrs, runsErrs, lookupErrs pollErrors
	run, done, err := d.harnessPollOnce(context.Background(), time.Minute, "org", "repo", "fix", after, &artifactErrs, &runsErrs, &lookupErrs)
	require.NoError(t, err)
	assert.False(t, done, "a skipped built-in job must not fail-fast on a sibling job's failure")
	assert.Nil(t, run)
}

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

// TestWaitForHarnessAgent_CancelledMaxIDArtifactHidesLowerIDSuccess
// verifies that the hidden-success scan (harnessArtifactRunSuccess) still
// runs when the highest-ID matching artifact belongs to a cancelled or
// skipped run. Before this fix, isConcurrencySuperseded returned early
// without ever scanning the other artifacts, so a cancelled/skipped run
// whose artifact happened to have a higher ID than an already-succeeded
// run's artifact would hide that success forever instead of just for one
// poll.
func TestWaitForHarnessAgent_CancelledMaxIDArtifactHidesLowerIDSuccess(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	// Run 100 is cancelled but its artifact (ID 20) has a higher ID than
	// run 200's already-succeeded artifact (ID 10) — e.g. the cancelled
	// run's if: always() upload finished after the success run's.
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
	fake.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 10, Name: "fullsend-review", CreatedAt: "2026-01-02T00:01:30Z", WorkflowRunID: 200},
			{ID: 20, Name: "fullsend-review", CreatedAt: "2026-01-02T00:02:00Z", WorkflowRunID: 100},
		},
	}

	d := &Driver{Client: fake, afterFunc: instantAfter}
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "review", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

// TestWaitForHarnessAgent_SkippedMaxIDArtifactHidesLowerIDSuccess mirrors
// TestWaitForHarnessAgent_CancelledMaxIDArtifactHidesLowerIDSuccess for a
// "skipped" conclusion, which is also concurrency-group noise.
func TestWaitForHarnessAgent_SkippedMaxIDArtifactHidesLowerIDSuccess(t *testing.T) {
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
	fake.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 10, Name: "fullsend-review", CreatedAt: "2026-01-02T00:01:30Z", WorkflowRunID: 200},
			{ID: 20, Name: "fullsend-review", CreatedAt: "2026-01-02T00:02:00Z", WorkflowRunID: 100},
		},
	}

	d := &Driver{Client: fake, afterFunc: instantAfter}
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
// that when both sibling runs schedule the same agent and both conclude
// failure, fail-fast still triggers: there is no later pending or
// successful run to supersede the failure.
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
				ID: 200, Status: "completed", Conclusion: "failure",
				CreatedAt: "2026-01-02T00:01:00Z",
				HTMLURL:   "https://github.com/org/repo/actions/runs/200",
			},
		},
	}
	// Both runs scheduled the agent's job and both failed.
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}},
		200: {{ID: 2, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	require.NotNil(t, run, "the failed run is returned with the error so its logs can be saved")
	assert.Equal(t, 100, run.ID)
	assert.Contains(t, err.Error(), "concluded with \"failure\" before producing artifact")
}

// TestWaitForHarnessAgent_NewerSiblingSucceededOverallButSkippedAgentJobDoesNotSuppressFailFast
// covers the gap hasSupersedingAgentRun left before checking the matched
// job's own conclusion: run 200 completes with an overall "success" (its
// other matrix job succeeded) but this agent's own "Harness run (triage)"
// job was skipped, never actually running the agent. That must not
// suppress fail-fast on run 100's genuine earlier failure.
func TestWaitForHarnessAgent_NewerSiblingSucceededOverallButSkippedAgentJobDoesNotSuppressFailFast(t *testing.T) {
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
				ID: 200, Status: "completed", Conclusion: "success",
				CreatedAt: "2026-01-02T00:01:00Z",
			},
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}},
		200: {
			{ID: 2, Name: "dispatch / Harness run (other-agent)", Status: "completed", Conclusion: "success"},
			{ID: 3, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "skipped"},
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	require.NotNil(t, run, "the failed run is returned with the error so its logs can be saved")
	assert.Equal(t, 100, run.ID)
	assert.Contains(t, err.Error(), "concluded with \"failure\" before producing artifact")
}

// TestWaitForHarnessAgent_FailedRunSupersededByLaterSuccess covers the
// #7574 dual-dispatch race: a workflow run for an agent concludes
// "failure" (having scheduled the agent's harness job), and a later run
// for the same agent subsequently succeeds and uploads its artifact.
// Fail-fast must not treat the earlier failure as authoritative.
func TestWaitForHarnessAgent_FailedRunSupersededByLaterSuccess(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
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
	// GetWorkflowRun looks up by ID across WorkflowRuns values; the
	// later poll's artifact points at run 200, which has already
	// succeeded by then.
	fake.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/failed": {
			ID: 100, Status: "completed", Conclusion: "failure",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/100",
		},
		"org/repo/success": {
			ID: 200, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:01:00Z",
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (reaction-ping)", Status: "completed", Conclusion: "failure"}},
		200: {{ID: 2, Name: "dispatch / Harness run (reaction-ping)", Status: "in_progress"}},
	}

	client := &settlingArtifactsClient{
		FakeClient: fake,
		callsLeft:  1,
		// First poll: no artifact, so the fail-fast scan would fire
		// on run 100 without the supersede check.
		beforeArts: nil,
		afterArts: []forge.RepositoryArtifact{
			{ID: 20, Name: "fullsend-reaction-ping", CreatedAt: "2026-01-02T00:02:00Z", WorkflowRunID: 200},
		},
	}

	d := &Driver{Client: client, afterFunc: instantAfter}
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "reaction-ping", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

// TestWaitForHarnessAgent_ArtifactFirstBranchSupersededByLaterSuccess
// covers the #7574 gap in the artifact-first branch: the harness workflow
// uploads fullsend-{agent} with if: always(), so run 100's own artifact
// is already present when it concludes "failure" (having scheduled the
// agent's harness job) — the quick-success artifact scan finds it before
// the recentRuns job-scan branch ever runs. A later run 200 for the same
// agent is still in progress and then succeeds, uploading its own
// (higher-ID) artifact. WaitForHarnessAgent must not treat run 100's
// artifact as authoritative — it must keep polling until run 200's
// artifact supersedes it.
func TestWaitForHarnessAgent_ArtifactFirstBranchSupersededByLaterSuccess(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
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
	fake.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/failed": {
			ID: 100, Status: "completed", Conclusion: "failure",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/100",
		},
		"org/repo/success": {
			ID: 200, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:01:00Z",
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (reaction-ping)", Status: "completed", Conclusion: "failure"}},
		200: {{ID: 2, Name: "dispatch / Harness run (reaction-ping)", Status: "in_progress"}},
	}

	client := &settlingArtifactsClient{
		FakeClient: fake,
		callsLeft:  1,
		// First poll: only run 100's own artifact is present, so the
		// quick-success artifact scan finds it first and must not
		// fail-fast without applying the supersede check.
		beforeArts: []forge.RepositoryArtifact{
			{ID: 10, Name: "fullsend-reaction-ping", CreatedAt: "2026-01-02T00:00:30Z", WorkflowRunID: 100},
		},
		// Second poll: run 200's own (higher-ID) artifact has landed too.
		afterArts: []forge.RepositoryArtifact{
			{ID: 10, Name: "fullsend-reaction-ping", CreatedAt: "2026-01-02T00:00:30Z", WorkflowRunID: 100},
			{ID: 20, Name: "fullsend-reaction-ping", CreatedAt: "2026-01-02T00:02:00Z", WorkflowRunID: 200},
		},
	}

	d := &Driver{Client: client, afterFunc: instantAfter}
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "reaction-ping", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

// TestWaitForHarnessAgent_ArtifactFirstBranchHigherIDFailureArtifactDoesNotHideSuccess
// covers the realistic ordering the prior artifact-first supersede check
// missed: selectRepositoryArtifactAfter picks the single highest-ID
// matching artifact, and artifact IDs are assigned at upload time. Run 100
// is dispatched first and takes longer, so it concludes "failure" and
// uploads its own fullsend-{agent} artifact *after* run 200 (dispatched
// later) already succeeded and uploaded its own artifact — giving run
// 100's failure artifact the higher ID. Both artifacts are present on the
// very first poll: the max-ID selection alone would keep picking run 100's
// artifact forever, since nothing ever gives run 200's artifact a higher
// ID. WaitForHarnessAgent must scan the other matching artifacts and
// return run 200 instead of waiting out the full timeout.
func TestWaitForHarnessAgent_ArtifactFirstBranchHigherIDFailureArtifactDoesNotHideSuccess(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{
				ID: 100, Status: "completed", Conclusion: "failure",
				CreatedAt: "2026-01-02T00:00:00Z",
				HTMLURL:   "https://github.com/org/repo/actions/runs/100",
			},
			{
				ID: 200, Status: "completed", Conclusion: "success",
				CreatedAt: "2026-01-02T00:01:00Z",
			},
		},
	}
	fake.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/failed": {
			ID: 100, Status: "completed", Conclusion: "failure",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/100",
		},
		"org/repo/success": {
			ID: 200, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:01:00Z",
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (reaction-ping)", Status: "completed", Conclusion: "failure"}},
		200: {{ID: 2, Name: "dispatch / Harness run (reaction-ping)", Status: "completed", Conclusion: "success"}},
	}
	// Run 200's artifact uploads first (lower ID) because it succeeded
	// quickly; run 100 takes longer to fail and uploads afterward,
	// claiming the higher ID even though it is the older run.
	fake.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 10, Name: "fullsend-reaction-ping", CreatedAt: "2026-01-02T00:02:00Z", WorkflowRunID: 200},
			{ID: 20, Name: "fullsend-reaction-ping", CreatedAt: "2026-01-02T00:05:00Z", WorkflowRunID: 100},
		},
	}

	d := newTestDriver(fake)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "reaction-ping", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

// artifactFirstListRunsErrorClient wraps settlingArtifactsClient so
// ListWorkflowRuns fails for the first N calls, simulating a transient
// listing error on the artifact-first branch's supersede check.
type artifactFirstListRunsErrorClient struct {
	*settlingArtifactsClient
	mu            sync.Mutex
	runsCallsLeft int
}

func (c *artifactFirstListRunsErrorClient) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]forge.WorkflowRun, error) {
	c.mu.Lock()
	if c.runsCallsLeft > 0 {
		c.runsCallsLeft--
		c.mu.Unlock()
		return nil, errors.New("simulated transient ListWorkflowRuns error")
	}
	c.mu.Unlock()
	return c.settlingArtifactsClient.FakeClient.ListWorkflowRuns(ctx, owner, repo, workflowFile)
}

// TestWaitForHarnessAgent_ArtifactFirstBranchListRunsErrorKeepsPolling
// covers the gap the artifact-first branch's supersede check left after
// #7574 landed: run 100's own fullsend-{agent} artifact is present when
// it concludes "failure" (having scheduled the agent's harness job), so
// the quick-success scan reaches the new supersede check on this poll.
// listHarnessRunsAfter's underlying ListWorkflowRuns call fails on this
// poll — its documented contract (WaitForHarnessAgent's doc comment) is
// that listing failures never end the wait, but the artifact-first
// branch ignored runsErr and walked the resulting nil recentRuns slice,
// so hasSupersedingAgentRun reported false and harnessPollOnce fail-fast
// returned instead of falling through to keep polling. A later run 200
// for the same agent goes on to succeed and uploads its own (higher-ID)
// artifact once the listing error has cleared.
func TestWaitForHarnessAgent_ArtifactFirstBranchListRunsErrorKeepsPolling(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{
				ID: 100, Status: "completed", Conclusion: "failure",
				CreatedAt: "2026-01-02T00:00:00Z",
				HTMLURL:   "https://github.com/org/repo/actions/runs/100",
			},
			{
				ID: 200, Status: "completed", Conclusion: "success",
				CreatedAt: "2026-01-02T00:01:00Z",
			},
		},
	}
	fake.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/failed": {
			ID: 100, Status: "completed", Conclusion: "failure",
			CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL:   "https://github.com/org/repo/actions/runs/100",
		},
		"org/repo/success": {
			ID: 200, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:01:00Z",
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (reaction-ping)", Status: "completed", Conclusion: "failure"}},
		200: {{ID: 2, Name: "dispatch / Harness run (reaction-ping)", Status: "completed", Conclusion: "success"}},
	}

	client := &artifactFirstListRunsErrorClient{
		settlingArtifactsClient: &settlingArtifactsClient{
			FakeClient: fake,
			callsLeft:  1,
			// First poll: only run 100's own artifact is present, so the
			// quick-success artifact scan reaches the supersede check,
			// whose listHarnessRunsAfter call fails this poll.
			beforeArts: []forge.RepositoryArtifact{
				{ID: 10, Name: "fullsend-reaction-ping", CreatedAt: "2026-01-02T00:00:30Z", WorkflowRunID: 100},
			},
			// Second poll: run 200's own (higher-ID) artifact has landed,
			// so the quick-success scan returns before any listing call.
			afterArts: []forge.RepositoryArtifact{
				{ID: 10, Name: "fullsend-reaction-ping", CreatedAt: "2026-01-02T00:00:30Z", WorkflowRunID: 100},
				{ID: 20, Name: "fullsend-reaction-ping", CreatedAt: "2026-01-02T00:02:00Z", WorkflowRunID: 200},
			},
		},
		runsCallsLeft: 1,
	}

	d := &Driver{Client: client, afterFunc: instantAfter}
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "reaction-ping", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

// TestWaitForHarnessAgent_QueuedSiblingWithUnexpandedMatrixDoesNotFailFast
// covers the #7574 remediation for the queued/unexpanded-matrix window:
// run 100 concludes "failure" having scheduled the agent's harness job,
// and a later run 200 is still in progress with only the dispatch job
// that computes the matrix ("Route") visible — its own harness matrix has
// not expanded yet, so it is not yet known whether it will schedule this
// agent. Treating that absence as a genuine "no" would fail-fast on run
// 100 even though run 200 goes on to succeed for the same agent.
func TestWaitForHarnessAgent_QueuedSiblingWithUnexpandedMatrixDoesNotFailFast(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
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
	fake.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/success": {
			ID: 200, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:01:00Z",
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}},
		// Run 200's matrix has not expanded yet on every fail-fast scan
		// this test performs: only the dispatch job is visible.
		200: {{ID: 2, Name: "dispatch / Route", Status: "in_progress"}},
	}

	client := &settlingArtifactsClient{
		FakeClient: fake,
		callsLeft:  1,
		// First poll: no artifact yet, so the fail-fast scan runs and
		// must not treat run 200's unexpanded matrix as a genuine
		// absence of the agent's job.
		beforeArts: nil,
		afterArts: []forge.RepositoryArtifact{
			{ID: 20, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:02:00Z", WorkflowRunID: 200},
		},
	}

	d := &Driver{Client: client, afterFunc: instantAfter}
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 200, run.ID)
}

// TestWaitForHarnessAgent_NewerSiblingWithoutAgentJobDoesNotSuppressFailFast
// verifies that a later run that did not schedule this agent does not
// keep polling past a genuine failure of this agent's job.
func TestWaitForHarnessAgent_NewerSiblingWithoutAgentJobDoesNotSuppressFailFast(t *testing.T) {
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
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}},
		200: {
			{ID: 2, Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
			{ID: 3, Name: "dispatch / Harness run (review)", Status: "in_progress"},
		},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	require.NotNil(t, run, "the failed run is returned with the error so its logs can be saved")
	assert.Equal(t, 100, run.ID)
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

func TestCountHarnessDispatches_SkippedBuiltinMatchOnUnresolvedMatrixSettles(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review, third pass): run 10 exposes only its
	// skipped static "Triage" job while its matrix is still unresolved (no
	// "Harness run (" job yet) and the run itself has not terminated.
	// matchAgentJob reports that skipped job as a match, so without the
	// guard this run was silently classified as neither pending nor
	// counted — settling (and undercounting) before a same-named custom
	// harness matrix job it was still waiting on even appeared. Once that
	// matrix job appears and succeeds, it must be the one counted.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	client := &settlingJobsClient{
		FakeClient: fake,
		settleRun:  10,
		callsLeft:  1,
		beforeJobs: []forge.WorkflowJob{{ID: 1, Name: "dispatch / Triage", Status: "completed", Conclusion: "skipped"}},
		afterJobs: []forge.WorkflowJob{
			{ID: 1, Name: "dispatch / Triage", Status: "completed", Conclusion: "skipped"},
			{ID: 2, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"},
		},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestCountHarnessDispatches_CancelledBuiltinMatchOnUnresolvedMatrixSettles
// is a regression test for the #7996 review: like the skipped-builtin case
// above, run 10 exposes only its cancelled static "Triage" job while its
// matrix is still unresolved (no "Harness run (" job yet) and the run
// itself has not terminated. Before the fix, settleHarnessDispatchCount's
// unresolved-matrix guard only matched a "skipped" conclusion, so this
// cancelled match fell through every switch case (isConcurrencySuperseded
// excludes it from the counted case too) and the run settled as neither
// counted nor pending — undercounting before the same-named custom harness
// matrix job it was still waiting on even appeared. Once that matrix job
// appears and succeeds, it must be the one counted.
func TestCountHarnessDispatches_CancelledBuiltinMatchOnUnresolvedMatrixSettles(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	client := &settlingJobsClient{
		FakeClient: fake,
		settleRun:  10,
		callsLeft:  1,
		beforeJobs: []forge.WorkflowJob{{ID: 1, Name: "dispatch / Triage", Status: "completed", Conclusion: "cancelled"}},
		afterJobs: []forge.WorkflowJob{
			{ID: 1, Name: "dispatch / Triage", Status: "completed", Conclusion: "cancelled"},
			{ID: 2, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"},
		},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestSettleHarnessDispatchCount_SuccessfulBuiltinMatrixUnresolvedPending is
// a regression test for the #7996 review (second pass): the
// unresolved-matrix guard previously only classified a weak (skipped or
// cancelled) built-in match as pending. A successful (or genuinely failed)
// built-in match — e.g. a completed "Triage" job — on a still-executing run
// whose matrix has not resolved yet (no "Harness run (" job and no "Route"
// job present) was counted immediately instead, before the independent
// Harness dispatch job had a chance to expand a same-named custom harness
// matrix job for this run. settleHarnessDispatchCount must classify it as
// pending instead, the same way a weak match already was.
func TestSettleHarnessDispatchCount_SuccessfulBuiltinMatrixUnresolvedPending(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			{ID: 10, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}
	fake.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Triage", Status: "completed", Conclusion: "success"}},
	}

	d := &Driver{Client: fake}
	count, pending, err := d.settleHarnessDispatchCount(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "a successful built-in match must not be counted while the custom matrix remains unresolved")
	assert.Equal(t, 1, pending, "the run must be classified as pending until the real matrix job appears")
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

func TestWorkflowRunNewer(t *testing.T) {
	t.Parallel()

	older := forge.WorkflowRun{ID: 100, CreatedAt: "2026-01-02T00:00:00Z"}
	newer := forge.WorkflowRun{ID: 200, CreatedAt: "2026-01-02T00:01:00Z"}
	sameTimeHigherID := forge.WorkflowRun{ID: 201, CreatedAt: "2026-01-02T00:00:00Z"}
	unparseableHigh := forge.WorkflowRun{ID: 300, CreatedAt: "not-a-time"}
	unparseableLow := forge.WorkflowRun{ID: 50, CreatedAt: "also-bad"}

	assert.True(t, workflowRunNewer(newer, older))
	assert.False(t, workflowRunNewer(older, newer))
	assert.False(t, workflowRunNewer(older, older))
	assert.True(t, workflowRunNewer(sameTimeHigherID, older), "equal CreatedAt falls back to ID")
	assert.False(t, workflowRunNewer(older, sameTimeHigherID))
	assert.True(t, workflowRunNewer(unparseableHigh, unparseableLow), "unparseable CreatedAt falls back to ID")
	assert.False(t, workflowRunNewer(unparseableLow, unparseableHigh))
}

func TestHasSupersedingAgentRun(t *testing.T) {
	t.Parallel()

	failed := forge.WorkflowRun{
		ID: 100, Status: "completed", Conclusion: "failure",
		CreatedAt: "2026-01-02T00:00:00Z",
	}
	newerInProgress := forge.WorkflowRun{
		ID: 200, Status: "in_progress",
		CreatedAt: "2026-01-02T00:01:00Z",
	}
	newerSuccess := forge.WorkflowRun{
		ID: 201, Status: "completed", Conclusion: "success",
		CreatedAt: "2026-01-02T00:02:00Z",
	}
	newerFailure := forge.WorkflowRun{
		ID: 202, Status: "completed", Conclusion: "failure",
		CreatedAt: "2026-01-02T00:03:00Z",
	}
	olderInProgress := forge.WorkflowRun{
		ID: 50, Status: "in_progress",
		CreatedAt: "2026-01-01T23:00:00Z",
	}
	newerOtherAgent := forge.WorkflowRun{
		ID: 203, Status: "in_progress",
		CreatedAt: "2026-01-02T00:04:00Z",
	}
	newerQueuedUnexpanded := forge.WorkflowRun{
		ID: 204, Status: "in_progress",
		CreatedAt: "2026-01-02T00:05:00Z",
	}
	newerQueuedNoJobs := forge.WorkflowRun{
		ID: 205, Status: "queued",
		CreatedAt: "2026-01-02T00:06:00Z",
	}
	// The overall run concluded "success" (its other matrix jobs
	// succeeded), but this agent's own harness job was skipped — that
	// must not count as superseding.
	newerSuccessOverallButAgentSkipped := forge.WorkflowRun{
		ID: 206, Status: "completed", Conclusion: "success",
		CreatedAt: "2026-01-02T00:07:00Z",
	}
	// Still executing, and the only match so far is the skipped static
	// "Triage" job — present on every run regardless of whether a
	// same-named custom harness matrix job will still appear. The matrix
	// has not resolved (no "Harness run (" job yet), so this skipped
	// match alone must not rule the run out as superseding (#7957
	// review, third pass).
	newerSkippedBuiltinUnexpanded := forge.WorkflowRun{
		ID: 207, Status: "in_progress",
		CreatedAt: "2026-01-02T00:08:00Z",
	}
	// Same as newerSkippedBuiltinUnexpanded, but the static job is
	// cancelled rather than skipped: the static job and a same-named
	// custom harness matrix job run in distinct concurrency groups, so the
	// static job's own cancellation says nothing about whether the matrix
	// job will still appear and run (#7996 review).
	newerCancelledBuiltinUnexpanded := forge.WorkflowRun{
		ID: 208, Status: "in_progress",
		CreatedAt: "2026-01-02T00:09:00Z",
	}

	// Still executing, and the only match so far is a completed failed
	// built-in "Triage" job. Its matrix has not resolved, so a same-named
	// custom harness matrix job can still appear; the built-in failure
	// alone must not rule the run out as superseding (#7996 review).
	newerFailedBuiltinUnexpanded := forge.WorkflowRun{
		ID: 209, Status: "in_progress",
		CreatedAt: "2026-01-02T00:10:00Z",
	}

	client := forge.NewFakeClient()
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}},
		209: {{ID: 12, Name: "dispatch / Triage", Status: "completed", Conclusion: "failure"}},
		200: {{ID: 2, Name: "dispatch / Harness run (triage)", Status: "in_progress"}},
		201: {{ID: 3, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}},
		202: {{ID: 4, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}},
		50:  {{ID: 5, Name: "dispatch / Harness run (triage)", Status: "in_progress"}},
		203: {{ID: 6, Name: "dispatch / Harness run (review)", Status: "in_progress"}},
		// Matrix not expanded yet: only the dispatch job that computes
		// the matrix has appeared so far.
		204: {{ID: 7, Name: "dispatch / Route", Status: "in_progress"}},
		// Queued run with no jobs listed at all yet.
		205: {},
		206: {
			{ID: 8, Name: "dispatch / Harness run (other-agent)", Status: "completed", Conclusion: "success"},
			{ID: 9, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "skipped"},
		},
		207: {{ID: 10, Name: "dispatch / Triage", Status: "completed", Conclusion: "skipped"}},
		208: {{ID: 11, Name: "dispatch / Triage", Status: "completed", Conclusion: "cancelled"}},
	}
	d := newTestDriver(client)
	var lookupErrs pollErrors

	assert.True(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed, newerInProgress}, &lookupErrs), "newer in-progress run for the same agent")
	assert.True(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed, newerSuccess}, &lookupErrs), "newer successful run for the same agent")
	assert.False(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed, newerFailure}, &lookupErrs), "newer failure is not a supersede")
	assert.False(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{olderInProgress, failed}, &lookupErrs), "older in-progress run is not newer")
	assert.False(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed, newerOtherAgent}, &lookupErrs), "newer run for a different agent: matrix expanded without this agent")
	assert.False(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed}, &lookupErrs), "no other runs")
	assert.True(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed, newerQueuedUnexpanded}, &lookupErrs),
		"newer run whose matrix has not expanded yet (only the Route job) is inconclusive, not a genuine absence")
	assert.False(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed, newerSuccessOverallButAgentSkipped}, &lookupErrs),
		"newer run that succeeded overall but skipped this agent's own job is not a supersede")
	assert.True(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed, newerQueuedNoJobs}, &lookupErrs),
		"newer queued run with no jobs listed yet is inconclusive")
	assert.True(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed, newerSkippedBuiltinUnexpanded}, &lookupErrs),
		"newer run matching only a skipped built-in stage job, with its matrix still unresolved, is inconclusive")
	assert.True(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed, newerCancelledBuiltinUnexpanded}, &lookupErrs),
		"newer run matching only a cancelled built-in stage job, with its matrix still unresolved, is inconclusive")
	assert.True(t, d.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed, newerFailedBuiltinUnexpanded}, &lookupErrs),
		"newer run matching only a failed built-in stage job, with its matrix still unresolved, is inconclusive")

	errClient := forge.NewFakeClient()
	errClient.Errors["ListWorkflowRunJobs"] = fmt.Errorf("jobs API error")
	errDriver := newTestDriver(errClient)
	var lookupOnErr pollErrors
	assert.True(t, errDriver.hasSupersedingAgentRun(context.Background(), "org", "repo", "triage", failed,
		[]forge.WorkflowRun{failed, newerInProgress}, &lookupOnErr),
		"job-list error on the newer run is inconclusive, not a confirmed non-supersede")
	assert.Greater(t, lookupOnErr.failed, 0)
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

func TestAssertNoHarnessAgentArtifact_SkippedBuiltinJobIsNotEvidence(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): builtinRoleJobName makes matchAgentJob
	// also match a built-in stage's static job name (e.g. "Fix"), which
	// is present on every run's job list but skipped for a trigger that
	// never reached that stage. A skipped job must not count as evidence
	// the agent ran.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Fix", Status: "completed", Conclusion: "skipped"}},
	}

	d := newTestDriver(client)
	err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "fix", after)
	require.NoError(t, err, "a skipped built-in job must not be treated as evidence the agent ran")
}

func TestAssertNoHarnessAgentArtifact_CancelledJobIsEvidence(t *testing.T) {
	t.Parallel()

	// Regression (#7957 review): unlike a skipped job (never scheduled), a
	// cancelled job can mean the agent started running before the job was
	// cancelled. forge.WorkflowJob exposes no started-at metadata that
	// would let the driver rule that out, so a cancelled job must still
	// fail the "agent did not run" assertion.
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 10, Status: "completed", Conclusion: "cancelled",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		10: {{ID: 1, Name: "dispatch / Harness run (fix)", Status: "completed", Conclusion: "cancelled"}},
	}

	d := newTestDriver(client)
	err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "fix", after)
	require.Error(t, err, "a cancelled agent job must still be treated as evidence the agent ran")
	assert.Contains(t, err.Error(), `expected harness "fix" not to run`)
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

// TestAssertNoHarnessAgentArtifact_SkippedThenCancelledOrderIndependent is a
// regression test for the #7996 review: a job list can contain both a
// skipped built-in stage job (e.g. "Triage") and a cancelled same-named
// custom harness matrix job ("Harness run (triage)") for the same run.
// matchAgentJob's single "best" match keeps whichever of the two weak
// (skipped/cancelled) jobs it saw first, so before the fix a skipped match
// listed before the cancelled one hid the cancelled job — which can mean
// the agent ran before being cancelled — from the negative assertion. The
// assertion must fail regardless of which order the jobs are listed in.
func TestAssertNoHarnessAgentArtifact_SkippedThenCancelledOrderIndependent(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	skipped := forge.WorkflowJob{ID: 1, Name: "dispatch / Triage", Status: "completed", Conclusion: "skipped"}
	cancelled := forge.WorkflowJob{ID: 2, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "cancelled"}

	t.Run("skipped listed before cancelled", func(t *testing.T) {
		t.Parallel()
		client := forge.NewFakeClient()
		client.WorkflowRuns = map[string]*forge.WorkflowRun{
			"org/repo/fullsend.yaml": {
				ID: 10, Status: "completed", Conclusion: "cancelled",
				CreatedAt: "2026-01-02T00:00:00Z",
			},
		}
		client.WorkflowRunJobs = map[int][]forge.WorkflowJob{10: {skipped, cancelled}}

		d := newTestDriver(client)
		err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "triage", after)
		require.Error(t, err, "a cancelled matrix job must still fail the assertion even when a skipped static job for the same agent is listed first")
		assert.Contains(t, err.Error(), `expected harness "triage" not to run`)
	})

	t.Run("cancelled listed before skipped", func(t *testing.T) {
		t.Parallel()
		client := forge.NewFakeClient()
		client.WorkflowRuns = map[string]*forge.WorkflowRun{
			"org/repo/fullsend.yaml": {
				ID: 10, Status: "completed", Conclusion: "cancelled",
				CreatedAt: "2026-01-02T00:00:00Z",
			},
		}
		client.WorkflowRunJobs = map[int][]forge.WorkflowJob{10: {cancelled, skipped}}

		d := newTestDriver(client)
		err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "triage", after)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `expected harness "triage" not to run`)
	})
}

func TestHarnessJobSuffix(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Harness run (pr-ping)", harnessJobSuffix("pr-ping"))
	assert.Equal(t, "Harness run (triage)", harnessJobSuffix("triage"))
}

func TestBuiltinRoleJobName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Triage", builtinRoleJobName("triage"))
	assert.Equal(t, "Code", builtinRoleJobName("code"))
	assert.Equal(t, "Review", builtinRoleJobName("review"))
	assert.Equal(t, "Fix", builtinRoleJobName("fix"))
	assert.Equal(t, "Retro", builtinRoleJobName("retro"))
	assert.Equal(t, "Prioritize", builtinRoleJobName("prioritize"))
	assert.Empty(t, builtinRoleJobName("pr-ping"), "custom harness names are not built-in roles")
	assert.Empty(t, builtinRoleJobName(""))
}

func TestMatchAgentJob_BuiltinRoleAndCustomHarness(t *testing.T) {
	t.Parallel()

	jobs := []forge.WorkflowJob{
		{ID: 1, Name: "dispatch / Triage"},
		{ID: 2, Name: "dispatch / Harness run (pr-ping)"},
	}

	hasJob, job := matchAgentJob(jobs, "triage")
	require.True(t, hasJob)
	assert.Equal(t, 1, job.ID)

	hasJob, job = matchAgentJob(jobs, "pr-ping")
	require.True(t, hasJob)
	assert.Equal(t, 2, job.ID)

	hasJob, _ = matchAgentJob(jobs, "code")
	assert.False(t, hasJob)
}

// TestMatchAgentJob_PrefersExecutedOverSkipped is a regression test for
// the #7957 review: a skipped built-in stage job (e.g. a static "Triage"
// job present on every run but skipped for this trigger) must not shadow
// a same-named, actually-executed matrix job ("Harness run (triage)")
// just because it appears first in the job list. Both listing orders are
// covered since the forge API gives no ordering guarantee.
func TestMatchAgentJob_PrefersExecutedOverSkipped(t *testing.T) {
	t.Parallel()

	skipped := forge.WorkflowJob{ID: 1, Name: "dispatch / Triage", Status: "completed", Conclusion: "skipped"}
	executed := forge.WorkflowJob{ID: 2, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}

	t.Run("skipped listed before executed", func(t *testing.T) {
		t.Parallel()
		hasJob, job := matchAgentJob([]forge.WorkflowJob{skipped, executed}, "triage")
		require.True(t, hasJob)
		assert.Equal(t, executed.ID, job.ID, "an executed matrix job must win over an earlier, skipped static job")
		assert.Equal(t, "success", job.Conclusion)
	})

	t.Run("executed listed before skipped", func(t *testing.T) {
		t.Parallel()
		hasJob, job := matchAgentJob([]forge.WorkflowJob{executed, skipped}, "triage")
		require.True(t, hasJob)
		assert.Equal(t, executed.ID, job.ID, "an executed matrix job must still win regardless of list order")
		assert.Equal(t, "success", job.Conclusion)
	})

	t.Run("only skipped matches reports the skipped job", func(t *testing.T) {
		t.Parallel()
		hasJob, job := matchAgentJob([]forge.WorkflowJob{skipped}, "triage")
		require.True(t, hasJob)
		assert.Equal(t, skipped.ID, job.ID)
		assert.Equal(t, "skipped", job.Conclusion)
	})

	t.Run("pending listed before skipped", func(t *testing.T) {
		t.Parallel()
		pending := forge.WorkflowJob{ID: 3, Name: "dispatch / Harness run (triage)", Status: "in_progress"}
		hasJob, job := matchAgentJob([]forge.WorkflowJob{skipped, pending}, "triage")
		require.True(t, hasJob)
		assert.Equal(t, pending.ID, job.ID, "a still-running matrix job must win over a skipped static job")
	})
}

// TestMatchAgentJob_PrefersExecutedOverCancelled is a regression test for
// the #7957 review, fifth pass: a cancelled built-in stage job (e.g. a
// static "Triage" job cancelled by its own concurrency group) must not
// shadow a same-named, actually-executed or still-pending matrix job
// ("Harness run (triage)") just because it appears first in the job
// list, the same way a skipped static job was already prevented from
// doing so. The built-in static job and the custom matrix job run in
// distinct concurrency groups, so the static job's cancellation outcome
// is independent of whether the matrix job actually ran. Both listing
// orders are covered since the forge API gives no ordering guarantee.
func TestMatchAgentJob_PrefersExecutedOverCancelled(t *testing.T) {
	t.Parallel()

	cancelled := forge.WorkflowJob{ID: 1, Name: "dispatch / Triage", Status: "completed", Conclusion: "cancelled"}
	executed := forge.WorkflowJob{ID: 2, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "success"}

	t.Run("cancelled listed before executed", func(t *testing.T) {
		t.Parallel()
		hasJob, job := matchAgentJob([]forge.WorkflowJob{cancelled, executed}, "triage")
		require.True(t, hasJob)
		assert.Equal(t, executed.ID, job.ID, "an executed matrix job must win over an earlier, cancelled static job")
		assert.Equal(t, "success", job.Conclusion)
	})

	t.Run("executed listed before cancelled", func(t *testing.T) {
		t.Parallel()
		hasJob, job := matchAgentJob([]forge.WorkflowJob{executed, cancelled}, "triage")
		require.True(t, hasJob)
		assert.Equal(t, executed.ID, job.ID, "an executed matrix job must still win regardless of list order")
		assert.Equal(t, "success", job.Conclusion)
	})

	t.Run("only cancelled matches reports the cancelled job", func(t *testing.T) {
		t.Parallel()
		hasJob, job := matchAgentJob([]forge.WorkflowJob{cancelled}, "triage")
		require.True(t, hasJob, "a cancelled-only match must still be reported so negative assertions retain it as evidence")
		assert.Equal(t, cancelled.ID, job.ID)
		assert.Equal(t, "cancelled", job.Conclusion)
	})

	t.Run("pending listed before cancelled", func(t *testing.T) {
		t.Parallel()
		pending := forge.WorkflowJob{ID: 3, Name: "dispatch / Harness run (triage)", Status: "in_progress"}
		hasJob, job := matchAgentJob([]forge.WorkflowJob{cancelled, pending}, "triage")
		require.True(t, hasJob)
		assert.Equal(t, pending.ID, job.ID, "a still-running matrix job must win over a cancelled static job")
	})
}

// TestMatchAgentJob_PendingAndFailureOutrankSuccess is a regression test
// for the #7996 review: when a built-in stage job and a same-named custom
// harness matrix job both reach a non-weak state, matchAgentJob must not
// just keep whichever is listed first. A still-running custom job must not
// be hidden behind an already-successful built-in job (round polling must
// not report success prematurely), and a genuinely failed custom job must
// not be hidden behind a successful built-in job either (the failure must
// not be dropped from fail-fast/dispatch-counting). Both listing orders
// are covered since the forge API gives no ordering guarantee.
func TestMatchAgentJob_PendingAndFailureOutrankSuccess(t *testing.T) {
	t.Parallel()

	builtinSuccess := forge.WorkflowJob{ID: 1, Name: "dispatch / Triage", Status: "completed", Conclusion: "success"}
	customPending := forge.WorkflowJob{ID: 2, Name: "dispatch / Harness run (triage)", Status: "in_progress"}
	customFailed := forge.WorkflowJob{ID: 3, Name: "dispatch / Harness run (triage)", Status: "completed", Conclusion: "failure"}

	t.Run("successful built-in listed before pending custom", func(t *testing.T) {
		t.Parallel()
		hasJob, job := matchAgentJob([]forge.WorkflowJob{builtinSuccess, customPending}, "triage")
		require.True(t, hasJob)
		assert.Equal(t, customPending.ID, job.ID, "a still-running custom job must not be hidden behind an earlier successful built-in job")
	})

	t.Run("pending custom listed before successful built-in", func(t *testing.T) {
		t.Parallel()
		hasJob, job := matchAgentJob([]forge.WorkflowJob{customPending, builtinSuccess}, "triage")
		require.True(t, hasJob)
		assert.Equal(t, customPending.ID, job.ID, "a still-running custom job must win regardless of list order")
	})

	t.Run("successful built-in listed before failed custom", func(t *testing.T) {
		t.Parallel()
		hasJob, job := matchAgentJob([]forge.WorkflowJob{builtinSuccess, customFailed}, "triage")
		require.True(t, hasJob)
		assert.Equal(t, customFailed.ID, job.ID, "a genuine custom-job failure must not be hidden behind an earlier successful built-in job")
	})

	t.Run("failed custom listed before successful built-in", func(t *testing.T) {
		t.Parallel()
		hasJob, job := matchAgentJob([]forge.WorkflowJob{customFailed, builtinSuccess}, "triage")
		require.True(t, hasJob)
		assert.Equal(t, customFailed.ID, job.ID, "a genuine custom-job failure must win regardless of list order")
	})
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

// TestWaitForHarnessAgent_ArtifactFirstFailureReturnsRun covers the
// common failure path: the harness uploads fullsend-{agent} with
// if: always(), so a failed run's artifact is found first. The failed
// run must come back with the error so the step can save its logs.
func TestWaitForHarnessAgent_ArtifactFirstFailureReturnsRun(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 10, Name: "fullsend-pi-smoke", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 88},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 88, Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z",
			HTMLURL: "https://github.com/org/repo/actions/runs/88",
		},
	}
	client.WorkflowRunJobs = map[int][]forge.WorkflowJob{
		88: {{ID: 1, Name: "dispatch / Harness run (pi-smoke)", Status: "completed", Conclusion: "failure"}},
	}

	d := newTestDriver(client)
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "pi-smoke", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `harness run for "pi-smoke" concluded with "failure"`)
	require.NotNil(t, run, "the failed run is returned with the error so its logs can be saved")
	assert.Equal(t, 88, run.ID)
}

func TestListRecentRuns_ImplementsRunLister(t *testing.T) {
	t.Parallel()

	client := forge.NewFakeClient()
	client.RecentWorkflowRuns = map[string][]forge.WorkflowRun{
		"org/repo": {{ID: 3}, {ID: 2}, {ID: 1}},
	}
	lister, ok := New(client, "tok").(ci.RunLister)
	require.True(t, ok, "driver must implement ci.RunLister for failure log collection")

	runs, err := lister.ListRecentRuns(context.Background(), "org", "repo", 2)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, 3, runs[0].ID)
	assert.Equal(t, 2, runs[1].ID)

	client.Errors["ListRecentWorkflowRuns"] = fmt.Errorf("boom")
	_, err = lister.ListRecentRuns(context.Background(), "org", "repo", 2)
	require.ErrorContains(t, err, "boom")
}
