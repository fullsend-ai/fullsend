package githubactions

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
)

var _ ci.EmptyMatrixWaiter = (*Driver)(nil)

const emptyMatrixPlaceholderJob = "dispatch / Harness run (${{ matrix.agent }})"

func emptyMatrixClient(runs []forge.WorkflowRun, jobs map[int][]forge.WorkflowJob) *forge.FakeClient {
	client := forge.NewFakeClient()
	client.WorkflowRunsList = map[string][]forge.WorkflowRun{"org/repo/fullsend.yaml": runs}
	client.WorkflowRunJobs = jobs
	return client
}

func TestWaitForHarnessEmptyMatrix_Success(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := emptyMatrixClient(
		[]forge.WorkflowRun{{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"}},
		map[int][]forge.WorkflowJob{10: {
			{ID: 1, Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
			{ID: 2, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
			{ID: 3, Name: emptyMatrixPlaceholderJob, Status: "completed", Conclusion: "skipped"},
		}},
	)

	run, err := newTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 10, run.ID)
}

func TestWaitForHarnessEmptyMatrix_IgnoresCancelledAndOlderRuns(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := emptyMatrixClient(
		[]forge.WorkflowRun{
			{ID: 5, Status: "completed", Conclusion: "failure", CreatedAt: "2025-12-31T00:00:00Z"},
			{ID: 10, Status: "completed", Conclusion: "cancelled", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:01:00Z"},
		},
		map[int][]forge.WorkflowJob{
			10: {{ID: 1, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "cancelled"}},
			20: {
				{ID: 2, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
				{ID: 4, Name: emptyMatrixPlaceholderJob, Status: "completed", Conclusion: "skipped"},
			},
		},
	)

	run, err := newTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
	require.NoError(t, err)
	assert.Equal(t, 20, run.ID)
}

func TestWaitForHarnessEmptyMatrix_IgnoresSkippedRuns(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := emptyMatrixClient(
		[]forge.WorkflowRun{
			{ID: 10, Status: "completed", Conclusion: "skipped", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 20, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:01:00Z"},
		},
		map[int][]forge.WorkflowJob{
			10: {{ID: 1, Name: "dispatch", Status: "completed", Conclusion: "skipped"}},
			20: {
				{ID: 2, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
				{ID: 4, Name: emptyMatrixPlaceholderJob, Status: "completed", Conclusion: "skipped"},
			},
		},
	)

	run, err := newTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
	require.NoError(t, err)
	assert.Equal(t, 20, run.ID)
}

func TestWaitForHarnessEmptyMatrix_DispatchJobFailed(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := emptyMatrixClient(
		[]forge.WorkflowRun{{ID: 10, Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z"}},
		map[int][]forge.WorkflowJob{10: {
			{ID: 2, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "failure"},
			{ID: 3, Name: emptyMatrixPlaceholderJob, Status: "completed", Conclusion: "skipped"},
		}},
	)

	_, err := newTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concluded failure")
}

func TestWaitForHarnessEmptyMatrix_AgentScheduled(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := emptyMatrixClient(
		[]forge.WorkflowRun{{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"}},
		map[int][]forge.WorkflowJob{10: {
			{ID: 2, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
			{ID: 3, Name: "dispatch / Harness run (fork-auth-probe)", Status: "completed", Conclusion: "success"},
		}},
	)

	_, err := newTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Harness run (fork-auth-probe)")
}

func TestWaitForHarnessEmptyMatrix_NoRunTimesOut(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := emptyMatrixClient(nil, nil)

	_, err := newTimeoutTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no harness run created after the trigger time")
}

func TestWaitForHarnessEmptyMatrix_PendingRunTimesOut(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := emptyMatrixClient(
		[]forge.WorkflowRun{{ID: 10, Status: "in_progress", CreatedAt: "2026-01-02T00:00:00Z"}},
		nil,
	)

	_, err := newTimeoutTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still in progress")
}

func TestWaitForHarnessEmptyMatrix_MissingDispatchJobTimesOut(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := emptyMatrixClient(
		[]forge.WorkflowRun{{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"}},
		map[int][]forge.WorkflowJob{10: {{ID: 1, Name: "dispatch / Route", Status: "completed", Conclusion: "success"}}},
	)

	_, err := newTimeoutTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still in progress")
}

func TestWaitForHarnessEmptyMatrix_MissingPlaceholderTimesOut(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := emptyMatrixClient(
		[]forge.WorkflowRun{{ID: 10, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"}},
		map[int][]forge.WorkflowJob{10: {
			{ID: 2, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
		}},
	)

	_, err := newTimeoutTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still in progress")
}

func TestWaitForHarnessEmptyMatrix_NonSkippedPlaceholderFails(t *testing.T) {
	t.Parallel()

	for _, conclusion := range []string{"cancelled", "failure", "timed_out"} {
		t.Run(conclusion, func(t *testing.T) {
			t.Parallel()

			after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			client := emptyMatrixClient(
				[]forge.WorkflowRun{{ID: 10, Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z"}},
				map[int][]forge.WorkflowJob{10: {
					{ID: 2, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
					{ID: 3, Name: emptyMatrixPlaceholderJob, Status: "completed", Conclusion: conclusion},
				}},
			)

			_, err := newTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "concluded "+conclusion+" without expanding")
		})
	}
}

// truncatedRunsClient wraps FakeClient so ListWorkflowRuns returns only a
// configured subset, simulating the live client's single-page cap, while
// ListWorkflowRunsSince returns the full configured list.
type truncatedRunsClient struct {
	*forge.FakeClient
	truncated []forge.WorkflowRun
}

func (c *truncatedRunsClient) ListWorkflowRuns(_ context.Context, _, _, _ string) ([]forge.WorkflowRun, error) {
	return append([]forge.WorkflowRun(nil), c.truncated...), nil
}

func TestWaitForHarnessEmptyMatrix_EarlierViolatingRunBeyondFirstPage(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()

	// An earlier run whose dispatch job failed, followed by ten newer
	// runs with a proper empty-matrix decision that fill a single page.
	full := []forge.WorkflowRun{
		{ID: 100, Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z"},
	}
	jobs := map[int][]forge.WorkflowJob{
		100: {{ID: 1, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "failure"}},
	}
	for i := 0; i < 10; i++ {
		id := 200 + i
		full = append(full, forge.WorkflowRun{
			ID: id, Status: "completed", Conclusion: "success",
			CreatedAt: fmt.Sprintf("2026-01-02T01:%02d:00Z", i),
		})
		jobs[id] = []forge.WorkflowJob{
			{ID: 2, Name: "dispatch / Harness dispatch", Status: "completed", Conclusion: "success"},
			{ID: 3, Name: emptyMatrixPlaceholderJob, Status: "completed", Conclusion: "skipped"},
		}
	}
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{"org/repo/fullsend.yaml": full}
	fake.WorkflowRunJobs = jobs

	client := &truncatedRunsClient{FakeClient: fake, truncated: full[1:]}

	_, err := newTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow run 100")
}

func TestWaitForHarnessEmptyMatrix_ListingErrorReported(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.Errors = map[string]error{"ListWorkflowRuns": errors.New("rate limited")}

	_, err := newTimeoutTestDriver(client).WaitForHarnessEmptyMatrix(context.Background(), "org", "repo", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "last lookup error: rate limited")
}
