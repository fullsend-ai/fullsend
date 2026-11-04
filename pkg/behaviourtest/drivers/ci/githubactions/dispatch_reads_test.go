package githubactions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dispatchReadsClient measures requests while two runs settle over six polls.
type dispatchReadsClient struct {
	*forge.FakeClient
	polls    int
	jobReads map[int]int
}

func (c *dispatchReadsClient) ListWorkflowRunsSince(ctx context.Context, owner, repo, workflow string, after time.Time) ([]forge.WorkflowRun, error) {
	c.polls++
	runs, err := c.FakeClient.ListWorkflowRunsSince(ctx, owner, repo, workflow, after)
	for i := range runs {
		if runs[i].ID > 30 && c.polls >= 6 {
			runs[i].Status = "completed"
		}
	}
	return runs, err
}

func (c *dispatchReadsClient) ListWorkflowRuns(ctx context.Context, owner, repo, workflow string) ([]forge.WorkflowRun, error) {
	runs, err := c.ListWorkflowRunsSince(ctx, owner, repo, workflow, time.Time{})
	if len(runs) > 10 {
		runs = runs[:10]
	}
	return runs, err
}

func (c *dispatchReadsClient) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error) {
	c.jobReads[runID]++
	return c.FakeClient.ListWorkflowRunJobs(ctx, owner, repo, runID)
}

func newDispatchReadsClient(after time.Time) *dispatchReadsClient {
	fake := forge.NewFakeClient()
	var runs []forge.WorkflowRun
	jobs := make(map[int][]forge.WorkflowJob)
	for id := 32; id > 0; id-- {
		run := forge.WorkflowRun{ID: id, RunAttempt: 1, Status: "completed", Conclusion: "success", CreatedAt: after.Add(time.Duration(id) * time.Second).Format(time.RFC3339)}
		switch {
		case id > 30:
			run.Status = "in_progress"
		case id > 10:
			run.Conclusion = "skipped"
		}
		runs = append(runs, run)
		jobs[id] = []forge.WorkflowJob{{ID: id, RunAttempt: 1, Name: "dispatch / Route", Status: "completed", Conclusion: "success"}}
	}
	// The target dispatch is older than the ten newest runs.
	jobs[1] = []forge.WorkflowJob{{ID: 1, RunAttempt: 1, Name: "dispatch / Harness run (fix)", Status: "completed", Conclusion: "success"}}
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{"org/repo/fullsend.yaml": runs}
	fake.WorkflowRunJobs = jobs
	return &dispatchReadsClient{FakeClient: fake, jobReads: make(map[int]int)}
}

func TestHarnessDispatchAssertions_RequestCounts(t *testing.T) {
	t.Parallel()
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Run("count across six polls", func(t *testing.T) {
		client := newDispatchReadsClient(after)
		d := &Driver{Client: client, afterFunc: instantAfter}
		count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "fix", after)
		require.NoError(t, err)
		var total int
		for _, calls := range client.jobReads {
			total += calls
		}
		t.Logf("list calls=%d jobs calls=%d unique jobs URLs=%d count=%d", client.polls, total, len(client.jobReads), count)
		assert.Equal(t, 1, count, "the older dispatch must still be counted")
		assert.Equal(t, 6, client.polls)
		assert.Equal(t, 67, total, "one decisive match once, nine inconclusive and two active runs on six polls")
		assert.Equal(t, 12, len(client.jobReads), "skipped workflows need no jobs lookup")
	})
	t.Run("negative assertion", func(t *testing.T) {
		client := newDispatchReadsClient(after)
		d := &Driver{Client: client}
		require.NoError(t, d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "absent", after))
		t.Logf("list calls=%d jobs calls=%d", client.polls, len(client.jobReads))
		assert.Len(t, client.jobReads, 12)
		for _, calls := range client.jobReads {
			assert.Equal(t, 1, calls)
		}
	})
}

func TestHarnessDispatchAssertions_SkippedRunNeedsNoJobs(t *testing.T) {
	t.Parallel()
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := forge.NewFakeClient()
	fake.WorkflowRunsList = map[string][]forge.WorkflowRun{"org/repo/fullsend.yaml": {
		{ID: 1, Status: "completed", Conclusion: "skipped", CreatedAt: after.Format(time.RFC3339)},
	}}
	fake.Errors["ListWorkflowRunJobs"] = errors.New("jobs must not be requested for a skipped workflow")
	d := &Driver{Client: fake}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "fix", after)
	require.NoError(t, err)
	assert.Zero(t, count)
	require.NoError(t, d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "fix", after))
}

func TestCountHarnessDispatches_CompletedRunJobsBecomeVisible(t *testing.T) {
	t.Parallel()
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, firstJob := range []string{"dispatch / Route", "dispatch / Harness run (${{ matrix.agent }})", "dispatch / Fix", "dispatch / Harness run (fix)"} {
		t.Run(firstJob, func(t *testing.T) {
			client := newDispatchReadsClient(after)
			run := client.WorkflowRunsList["org/repo/fullsend.yaml"][31]
			client.WorkflowRunJobs[1] = []forge.WorkflowJob{{ID: 1, RunAttempt: 1, Name: firstJob, Status: "completed", Conclusion: "skipped"}}
			if firstJob == "dispatch / Route" {
				client.WorkflowRunJobs[1][0].Conclusion = "success"
			}
			d := &Driver{Client: client, afterFunc: func(time.Duration) <-chan time.Time {
				if client.polls == 1 {
					client.WorkflowRunJobs[1] = append(client.WorkflowRunJobs[1], forge.WorkflowJob{
						ID: 100, RunAttempt: 1, Name: "dispatch / Harness run (fix)", Status: "completed", Conclusion: "success",
					})
				}
				return instantAfter(0)
			}}
			count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "fix", after)
			require.NoError(t, err)
			assert.Equal(t, 1, count, "a lagging listing must not permanently hide the completed agent job")
			assert.Equal(t, 2, client.jobReads[1], "reread the inconclusive listing, then cache the decisive match")
			assert.Equal(t, 6, client.polls, "a pending sibling keeps the assertion polling")
			assert.Equal(t, run, client.WorkflowRunsList["org/repo/fullsend.yaml"][31], "completed run metadata stays unchanged")
		})
	}
}

func TestHarnessDispatchAssertions_RelevantJobsErrorsRemainFatal(t *testing.T) {
	t.Parallel()
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, run := range []forge.WorkflowRun{
		{Status: "completed", Conclusion: "success"},
		{Status: "completed", Conclusion: "failure"},
		{Status: "completed", Conclusion: "cancelled"},
		{Status: "in_progress", Conclusion: "skipped"},
		{},
	} {
		t.Run(run.Status+"/"+run.Conclusion, func(t *testing.T) {
			fake := forge.NewFakeClient()
			run.ID, run.CreatedAt = 1, after.Format(time.RFC3339)
			fake.WorkflowRunsList = map[string][]forge.WorkflowRun{"org/repo/fullsend.yaml": {run}}
			lookupErr := errors.New("jobs lookup failed")
			fake.Errors["ListWorkflowRunJobs"] = lookupErr
			d := &Driver{Client: fake}
			_, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "fix", after)
			require.ErrorIs(t, err, lookupErr)
			require.ErrorIs(t, d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "fix", after), lookupErr)
		})
	}
}

func TestCountHarnessDispatches_CompletedJobCache(t *testing.T) {
	t.Parallel()
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"rerun", "unknown run attempt", "unknown job attempt", "mismatched job attempt", "unfinished job", "unfinished conclusion", "empty jobs", "assertion local", "changed conclusion"} {
		t.Run(name, func(t *testing.T) {
			client := newDispatchReadsClient(after)
			run := &client.WorkflowRunsList["org/repo/fullsend.yaml"][31]
			job := &client.WorkflowRunJobs[1][0]
			switch name {
			case "unknown run attempt":
				run.RunAttempt = 0
			case "unknown job attempt":
				job.RunAttempt = 0
			case "mismatched job attempt":
				job.RunAttempt = 2
			case "unfinished job":
				job.Status = "queued"
			case "unfinished conclusion":
				job.Conclusion = ""
			case "empty jobs":
				client.WorkflowRunJobs[1] = nil
			}
			d := &Driver{Client: client, afterFunc: func(time.Duration) <-chan time.Time {
				if client.polls == 1 {
					switch name {
					case "rerun":
						run.RunAttempt = 2
						job.RunAttempt = 2
					case "changed conclusion":
						run.Conclusion = "cancelled"
					}
					client.WorkflowRunJobs[1] = []forge.WorkflowJob{{ID: 100, RunAttempt: run.RunAttempt, Name: "dispatch / Harness run (fix)", Status: "completed", Conclusion: "cancelled"}}
				}
				return instantAfter(0)
			}}
			count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "fix", after)
			require.NoError(t, err)
			if name == "assertion local" {
				assert.Equal(t, 1, count, "the completed attempt is reused within the first assertion")
				assert.Equal(t, 1, client.jobReads[1])
				count, err = d.CountHarnessDispatches(context.Background(), "org", "repo", "fix", after)
				require.NoError(t, err)
			}
			assert.Zero(t, count, "a changed or uncertified job listing must be fetched again")
			assert.GreaterOrEqual(t, client.jobReads[1], 2)
		})
	}
}
