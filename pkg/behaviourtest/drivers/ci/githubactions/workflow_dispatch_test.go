package githubactions

// The workflow-dispatch half of the GitHub Actions driver's tests. The
// acceptance-test commit could not hold these: they call the driver's
// DispatchWorkflow and WaitForWorkflowConclusion, which the code patch
// adds, so they do not compile on the base branch. They ride in the fix
// commit, verbatim from the adopted branch's tests.patch.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
)

var _ ci.WorkflowDispatcher = (*Driver)(nil)

// dispatchRecordingClient records the DispatchWorkflow call.
type dispatchRecordingClient struct {
	*forge.FakeClient
	file, ref string
	inputs    map[string]string
	ctx       context.Context
}

func (c *dispatchRecordingClient) DispatchWorkflow(ctx context.Context, _, _, workflowFile, ref string, inputs map[string]string) error {
	c.file, c.ref, c.inputs, c.ctx = workflowFile, ref, inputs, ctx
	return nil
}

func TestDispatchWorkflow_PassesInputs(t *testing.T) {
	t.Parallel()

	client := &dispatchRecordingClient{FakeClient: forge.NewFakeClient()}
	d := newTestDriver(client)
	require.NoError(t, d.DispatchWorkflow(context.Background(), "org", "repo", ".github/workflows/prioritize.yml", "main", map[string]string{"event_payload": "{}"}))
	assert.Equal(t, "prioritize.yml", client.file)
	assert.Equal(t, "main", client.ref)
	assert.Equal(t, map[string]string{"event_payload": "{}"}, client.inputs)
}

// The dispatch runs with ambiguous retry disabled, like the forge
// client's own UpdateFileIfMatch: a replayed dispatch POST could start a
// second run of the workflow, so an ambiguous timeout or 5xx must be
// returned to the step, not retried by the client's transport loop.
func TestDispatchWorkflow_NoAmbiguousRetry(t *testing.T) {
	t.Parallel()

	client := &dispatchRecordingClient{FakeClient: forge.NewFakeClient()}
	d := newTestDriver(client)
	require.NoError(t, d.DispatchWorkflow(context.Background(), "org", "repo", ".github/workflows/prioritize.yml", "main", nil))
	require.NotNil(t, client.ctx, "the driver must pass a context to the client")
	assert.True(t, forge.AmbiguousRetryDisabled(client.ctx), "the dispatch must not be retried on an ambiguous timeout or server error")
}

// completingClient serves a run as in progress from ListWorkflowRuns and
// as completed with conclusion from GetWorkflowRun.
type completingClient struct {
	*forge.FakeClient
	conclusion string
}

func (c *completingClient) GetWorkflowRun(_ context.Context, _, _ string, runID int) (*forge.WorkflowRun, error) {
	return &forge.WorkflowRun{ID: runID, Status: "completed", Conclusion: c.conclusion}, nil
}

func TestWaitForWorkflowConclusion(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := func(status, conclusion, event, created string) map[string]*forge.WorkflowRun {
		return map[string]*forge.WorkflowRun{"org/repo/prioritize.yml": {
			ID: 11, Status: status, Conclusion: conclusion, Event: event, CreatedAt: created,
		}}
	}

	t.Run("a completed failure is returned without an error", func(t *testing.T) {
		t.Parallel()
		client := forge.NewFakeClient()
		client.WorkflowRuns = runs("completed", "failure", "workflow_dispatch", "2026-01-02T00:00:00Z")
		run, err := newTestDriver(client).WaitForWorkflowConclusion(context.Background(), "org", "repo", ".github/workflows/prioritize.yml", after, "workflow_dispatch")
		require.NoError(t, err)
		assert.Equal(t, 11, run.ID)
		assert.Equal(t, "failure", run.Conclusion)
	})

	t.Run("an in-progress run is waited on until it completes", func(t *testing.T) {
		t.Parallel()
		client := &completingClient{FakeClient: forge.NewFakeClient(), conclusion: "failure"}
		client.WorkflowRuns = runs("in_progress", "", "workflow_dispatch", "2026-01-02T00:00:00Z")
		run, err := newTestDriver(client).WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
		require.NoError(t, err)
		assert.Equal(t, "completed", run.Status)
		assert.Equal(t, "failure", run.Conclusion)
	})

	// A workflow with cancel-in-progress replaces a cancelled run with a
	// newer one; the conclusion returned is the replacement's, not the
	// cancelled run's (the same replacement-following WaitForWorkflow does).
	t.Run("a cancelled run is replaced by a later one", func(t *testing.T) {
		t.Parallel()
		run := func(id int, conclusion string) forge.WorkflowRun {
			return forge.WorkflowRun{ID: id, Status: "completed", Conclusion: conclusion, Event: "workflow_dispatch", CreatedAt: "2026-01-02T00:00:00Z"}
		}
		client := &replacingClient{FakeClient: forge.NewFakeClient(), final: []forge.WorkflowRun{run(10, "cancelled"), run(11, "failure")}}
		got, err := newTestDriver(client).WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
		require.NoError(t, err)
		assert.Equal(t, 11, got.ID)
		assert.Equal(t, "failure", got.Conclusion)
	})

	t.Run("a cancelled run with no replacement is returned", func(t *testing.T) {
		t.Parallel()
		run := func(id int, conclusion string) forge.WorkflowRun {
			return forge.WorkflowRun{ID: id, Status: "completed", Conclusion: conclusion, Event: "workflow_dispatch", CreatedAt: "2026-01-02T00:00:00Z"}
		}
		client := &replacingClient{FakeClient: forge.NewFakeClient(), final: []forge.WorkflowRun{run(10, "cancelled")}}
		// The re-list grace is bounded by the wait's deadline, so the
		// clock must advance for the wait to give up on a replacement.
		d := newTestDriver(client)
		d.nowFunc = steppingClock(after, time.Minute)
		got, err := d.WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
		require.NoError(t, err)
		assert.Equal(t, 10, got.ID)
		assert.Equal(t, "cancelled", got.Conclusion)
	})

	for name, wf := range map[string]map[string]*forge.WorkflowRun{
		"a run for another event is not dispatched": runs("completed", "failure", "push", "2026-01-02T00:00:00Z"),
		"an earlier run is not dispatched":          runs("completed", "failure", "workflow_dispatch", "2025-12-31T00:00:00Z"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := forge.NewFakeClient()
			client.WorkflowRuns = wf
			d := newTestDriver(client)
			d.nowFunc = steppingClock(after, time.Minute)
			_, err := d.WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "was not dispatched")
		})
	}

	t.Run("a run that never completes times out", func(t *testing.T) {
		t.Parallel()
		client := forge.NewFakeClient()
		client.WorkflowRuns = runs("in_progress", "", "workflow_dispatch", "2026-01-02T00:00:00Z")
		d := newTestDriver(client)
		d.nowFunc = steppingClock(after, time.Minute)
		_, err := d.WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "did not complete")
	})

	t.Run("a listing that keeps failing is reported on the dispatch timeout", func(t *testing.T) {
		t.Parallel()
		client := &failingListingClient{FakeClient: forge.NewFakeClient()}
		d := newTestDriver(client)
		d.nowFunc = steppingClock(after, time.Minute)
		_, err := d.WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "was not dispatched")
		assert.Contains(t, err.Error(), "run listings")
		assert.Contains(t, err.Error(), "secondary rate limit")
	})

	t.Run("a run lookup that keeps failing is reported on the completion timeout", func(t *testing.T) {
		t.Parallel()
		client := forge.NewFakeClient()
		client.WorkflowRuns = runs("in_progress", "", "workflow_dispatch", "2026-01-02T00:00:00Z")
		d := newTestDriver(&failingLookupClient{FakeClient: client})
		d.nowFunc = steppingClock(after, time.Minute)
		_, err := d.WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "did not complete within deadline")
		assert.Contains(t, err.Error(), "run lookups")
		assert.Contains(t, err.Error(), "secondary rate limit")
	})

	t.Run("a cancelled context stops the wait", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		d := newTestDriver(forge.NewFakeClient())
		d.afterFunc = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
		_, err := d.WaitForWorkflowConclusion(ctx, "org", "repo", "prioritize.yml", after, "workflow_dispatch")
		require.ErrorIs(t, err, context.Canceled)
	})
}

// cancellingReplacementClient lists only the already-completed cancelled
// run for its first revealAfter listings, then the newer run that
// replaced it as well: the state a poll sees when a cancel-in-progress
// workflow's replacement is not listed yet. revealAfter 1 is a run
// already cancelled at detection time; 2 is a replacement listed a
// moment after the cancelled run completes (list lag).
type cancellingReplacementClient struct {
	*forge.FakeClient
	mu                     sync.Mutex
	lists                  int
	revealAfter            int
	cancelled, replacement forge.WorkflowRun
}

func (c *cancellingReplacementClient) ListWorkflowRuns(context.Context, string, string, string) ([]forge.WorkflowRun, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lists++
	if c.lists <= c.revealAfter {
		return []forge.WorkflowRun{c.cancelled}, nil
	}
	return []forge.WorkflowRun{c.cancelled, c.replacement}, nil
}

func (c *cancellingReplacementClient) GetWorkflowRun(_ context.Context, _, _ string, runID int) (*forge.WorkflowRun, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if runID == c.replacement.ID {
		return &c.replacement, nil
	}
	return &c.cancelled, nil
}

// A run already cancelled when the wait first lists it is still replaced
// by its newer run: the detection path must not return it as final.
func TestWaitForWorkflowConclusion_CancelledAtDetection(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	run := func(id int, conclusion string) forge.WorkflowRun {
		return forge.WorkflowRun{ID: id, Status: "completed", Conclusion: conclusion, Event: "workflow_dispatch", CreatedAt: "2026-01-02T00:00:00Z"}
	}
	client := &cancellingReplacementClient{FakeClient: forge.NewFakeClient(), revealAfter: 1, cancelled: run(10, "cancelled"), replacement: run(11, "failure")}
	got, err := newTestDriver(client).WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
	require.NoError(t, err)
	assert.Equal(t, 11, got.ID)
	assert.Equal(t, "failure", got.Conclusion)
}

// A replacement listed only after the cancelled run completed is still
// followed: the wait re-lists within its deadline instead of returning
// the cancelled run as final.
func TestWaitForWorkflowConclusion_ReplacementListLag(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	run := func(id int, conclusion string) forge.WorkflowRun {
		return forge.WorkflowRun{ID: id, Status: "completed", Conclusion: conclusion, Event: "workflow_dispatch", CreatedAt: "2026-01-02T00:00:00Z"}
	}
	client := &cancellingReplacementClient{FakeClient: forge.NewFakeClient(), revealAfter: 2, cancelled: run(10, "cancelled"), replacement: run(11, "failure")}
	got, err := newTestDriver(client).WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
	require.NoError(t, err)
	assert.Equal(t, 11, got.ID)
	assert.Equal(t, "failure", got.Conclusion)
}

// failingListingClient fails every run listing: the shape of a
// #6647-style secondary rate limit lasting the whole detection window,
// so the timeout message must say the wait was blind.
type failingListingClient struct {
	*forge.FakeClient
}

func (c *failingListingClient) ListWorkflowRuns(context.Context, string, string, string) ([]forge.WorkflowRun, error) {
	return nil, errors.New("HTTP 403 secondary rate limit")
}

// failingLookupClient fails every single-run lookup while listings
// still work, so the run is detected but its completion is never seen.
type failingLookupClient struct {
	*forge.FakeClient
}

func (c *failingLookupClient) GetWorkflowRun(context.Context, string, string, int) (*forge.WorkflowRun, error) {
	return nil, errors.New("HTTP 403 secondary rate limit")
}

// rateLimitedListingClient serves the cancelled run on its first listing
// (detection) and fails every later one: the shape of a #6647-style
// secondary rate limit hitting the replacement grace.
type rateLimitedListingClient struct {
	*forge.FakeClient
	mu        sync.Mutex
	listed    bool
	cancelled forge.WorkflowRun
}

func (c *rateLimitedListingClient) ListWorkflowRuns(context.Context, string, string, string) ([]forge.WorkflowRun, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.listed {
		c.listed = true
		return []forge.WorkflowRun{c.cancelled}, nil
	}
	return nil, errors.New("HTTP 403 secondary rate limit")
}

func (c *rateLimitedListingClient) GetWorkflowRun(_ context.Context, _, _ string, runID int) (*forge.WorkflowRun, error) {
	return &c.cancelled, nil
}

// A listing that keeps failing during the replacement grace is reported,
// not read as "no replacement": the cancelled run comes back together
// with the listing error instead of a silent "cancelled is final".
func TestWaitForWorkflowConclusion_ReplacementListingFails(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := &rateLimitedListingClient{FakeClient: forge.NewFakeClient(), cancelled: forge.WorkflowRun{ID: 10, Status: "completed", Conclusion: "cancelled", Event: "workflow_dispatch", CreatedAt: "2026-01-02T00:00:00Z"}}
	d := newTestDriver(client)
	d.nowFunc = steppingClock(after, time.Minute)
	got, err := d.WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
	require.Error(t, err)
	assert.ErrorContains(t, err, "replacement listing kept failing")
	assert.ErrorContains(t, err, "secondary rate limit")
	assert.Equal(t, 10, got.ID)
}

// completingFailureClient lists an in-progress run (detection) and then
// serves it completed with a failure from GetWorkflowRun while every
// later listing fails: the state WaitForWorkflow's replacement scan sees
// under a #6647-style secondary rate limit.
type completingFailureClient struct {
	*forge.FakeClient
	mu     sync.Mutex
	listed bool
	run    forge.WorkflowRun
}

func (c *completingFailureClient) ListWorkflowRuns(context.Context, string, string, string) ([]forge.WorkflowRun, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.listed {
		c.listed = true
		return []forge.WorkflowRun{c.run}, nil
	}
	return nil, errors.New("HTTP 403 secondary rate limit")
}

func (c *completingFailureClient) GetWorkflowRun(_ context.Context, _, _ string, _ int) (*forge.WorkflowRun, error) {
	completed := c.run
	completed.Status = "completed"
	completed.Conclusion = "failure"
	return &completed, nil
}

// WaitForWorkflow's replacement scan reports a listing that keeps
// failing on the conclusion error, like its sibling's relist loop,
// instead of reading it as "no replacement" and losing the cause.
func TestWaitForWorkflow_ReplacementListingFails(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := &completingFailureClient{FakeClient: forge.NewFakeClient(), run: forge.WorkflowRun{ID: 10, Status: "in_progress", Event: "issues", CreatedAt: "2026-01-02T00:00:00Z"}}
	d := newTestDriver(client)
	got, err := d.WaitForWorkflow(context.Background(), "org", "repo", "prioritize.yml", after, "issues")
	require.Error(t, err)
	assert.ErrorContains(t, err, `concluded with "failure"`)
	assert.ErrorContains(t, err, "replacement listings")
	assert.ErrorContains(t, err, "secondary rate limit")
	require.NotNil(t, got)
	assert.Equal(t, 10, got.ID)
}

// withPollBudget bounds one wait-loop call by the remaining budget and
// reports a budget-cut call as cut, so pollErrors counts it as the
// driver's doing rather than an API failure.
func TestWithPollBudget(t *testing.T) {
	t.Parallel()

	t.Run("a call that outruns the budget is cut short", func(t *testing.T) {
		t.Parallel()
		_, err, cut := withPollBudget(context.Background(), 5*time.Millisecond, func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.True(t, cut)
	})

	t.Run("a call that fits the budget is returned", func(t *testing.T) {
		t.Parallel()
		v, err, cut := withPollBudget(context.Background(), time.Minute, func(context.Context) (int, error) {
			return 7, nil
		})
		require.NoError(t, err)
		assert.Equal(t, 7, v)
		assert.False(t, cut)
	})

	t.Run("a spent budget cuts the call at once", func(t *testing.T) {
		t.Parallel()
		_, err, cut := withPollBudget(context.Background(), -time.Second, func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.True(t, cut)
	})

	t.Run("a deadline error under a live context is not a cut", func(t *testing.T) {
		t.Parallel()
		_, err, cut := withPollBudget(context.Background(), time.Minute, func(context.Context) (string, error) {
			return "", context.DeadlineExceeded
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.False(t, cut)
	})
}

// recordBounded counts a budget-cut call as cut short, not failed.
func TestPollErrors_RecordBounded(t *testing.T) {
	t.Parallel()

	var p pollErrors
	p.recordBounded(context.Background(), errors.New("HTTP 403 secondary rate limit"), false)
	p.recordBounded(context.Background(), context.DeadlineExceeded, true)
	assert.Equal(t, 2, p.calls)
	assert.Equal(t, 1, p.failed)
	assert.Equal(t, 1, p.cutShort)
}

// budgetRecordingClient records whether each API call it served ran
// under a context carrying a deadline: the dispatched-run waits bound
// every poll by the remaining wait budget (withPollBudget), so the
// client's rate-limit retries — up to five, each waiting on
// Retry-After — cannot stretch one poll past the wait's deadline, the
// #6647 shape harnessPollOnce fixed for the harness path.
type budgetRecordingClient struct {
	*forge.FakeClient
	// lookup, when set, serves GetWorkflowRun so a test can serve a run
	// whose completion appears only on lookup; nil uses the fake's.
	lookup    func(ctx context.Context, owner, repo string, id int) (*forge.WorkflowRun, error)
	mu        sync.Mutex
	calls     int
	unbounded int
}

func (c *budgetRecordingClient) note(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if _, ok := ctx.Deadline(); !ok {
		c.unbounded++
	}
}

func (c *budgetRecordingClient) ListWorkflowRuns(ctx context.Context, owner, repo, workflowFile string) ([]forge.WorkflowRun, error) {
	c.note(ctx)
	return c.FakeClient.ListWorkflowRuns(ctx, owner, repo, workflowFile)
}

func (c *budgetRecordingClient) GetWorkflowRun(ctx context.Context, owner, repo string, id int) (*forge.WorkflowRun, error) {
	c.note(ctx)
	if c.lookup != nil {
		return c.lookup(ctx, owner, repo, id)
	}
	return c.FakeClient.GetWorkflowRun(ctx, owner, repo, id)
}

// Every API call the dispatched-run waits make — detection listing,
// run lookup and replacement-grace relist — runs under a context
// bounded by the wait's remaining budget: without the bound, one
// #6647-style secondary rate limit stretches a poll minutes past the
// deadline, because the client retries up to five times honoring
// Retry-After.
func TestDispatchedRunWaits_PollsAreBounded(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := func(status, conclusion string) map[string]*forge.WorkflowRun {
		return map[string]*forge.WorkflowRun{"org/repo/prioritize.yml": {
			ID: 11, Status: status, Conclusion: conclusion, Event: "workflow_dispatch", CreatedAt: "2026-01-02T00:00:00Z",
		}}
	}

	t.Run("detection listing and run lookup", func(t *testing.T) {
		t.Parallel()
		client := &budgetRecordingClient{FakeClient: forge.NewFakeClient()}
		client.WorkflowRuns = runs("in_progress", "")
		client.lookup = func(_ context.Context, _, _ string, id int) (*forge.WorkflowRun, error) {
			return &forge.WorkflowRun{ID: id, Status: "completed", Conclusion: "failure"}, nil
		}
		run, err := newTestDriver(client).WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
		require.NoError(t, err)
		assert.Equal(t, "failure", run.Conclusion)
		client.mu.Lock()
		calls, unbounded := client.calls, client.unbounded
		client.mu.Unlock()
		assert.Greater(t, calls, 1, "the wait must have listed and looked the run up")
		assert.Zero(t, unbounded, "every poll must run under a deadline-bounded context")
	})

	t.Run("replacement-grace relist", func(t *testing.T) {
		t.Parallel()
		client := &budgetRecordingClient{FakeClient: forge.NewFakeClient()}
		client.WorkflowRuns = runs("completed", "cancelled")
		d := newTestDriver(client)
		// The re-list grace is bounded by the wait's deadline, so the
		// clock must advance for the wait to give up on a replacement.
		d.nowFunc = steppingClock(after, time.Minute)
		got, err := d.WaitForWorkflowConclusion(context.Background(), "org", "repo", "prioritize.yml", after, "workflow_dispatch")
		require.NoError(t, err)
		assert.Equal(t, "cancelled", got.Conclusion)
		client.mu.Lock()
		calls, unbounded := client.calls, client.unbounded
		client.mu.Unlock()
		assert.Greater(t, calls, 1, "the wait must have re-listed during the grace")
		assert.Zero(t, unbounded, "every poll must run under a deadline-bounded context")
	})
}
