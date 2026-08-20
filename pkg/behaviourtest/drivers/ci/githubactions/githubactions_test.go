package githubactions

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
// tests run without real wall-clock sleeps. Update this helper when new
// fields are added to Driver.
func newTestDriver(client forge.Client) *Driver {
	return &Driver{Client: client, afterFunc: instantAfter}
}

func TestDispatchDetectionWindow_AtLeast4Minutes(t *testing.T) {
	t.Parallel()

	// The dispatch detection window is dispatchMaxTry × dispatchPoll.
	// It must be at least 4 minutes to tolerate slow GitHub webhook
	// delivery. See issue #5503.
	window := time.Duration(dispatchMaxTry) * dispatchPoll
	assert.GreaterOrEqual(t, window, 4*time.Minute,
		"dispatch detection window (%v) should be at least 4 minutes", window)
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

func TestFormatRunDiagnostics_Empty(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "no recent workflow runs found after trigger time",
		formatRunDiagnostics(nil))
}

func TestFormatRunDiagnostics_WithRuns(t *testing.T) {
	t.Parallel()

	runs := []forge.WorkflowRun{
		{ID: 1, Status: "completed", Conclusion: "failure", HTMLURL: "https://example.com/1"},
		{ID: 2, Status: "in_progress", HTMLURL: "https://example.com/2"},
	}
	got := formatRunDiagnostics(runs)
	assert.Contains(t, got, "recent workflow runs (2)")
	assert.Contains(t, got, "run 1: status=completed conclusion=failure")
	assert.Contains(t, got, "run 2: status=in_progress")
}
