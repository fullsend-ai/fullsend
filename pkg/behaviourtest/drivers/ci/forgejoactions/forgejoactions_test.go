package forgejoactions

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/forgejo"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
)

// The Forgejo adapter is the RunClient the driver runs against.
var _ RunClient = (*forgejo.LiveClient)(nil)

// trigger is the scenario trigger time the tests wait from.
var trigger = time.Date(2026, 11, 24, 12, 0, 0, 700_000_000, time.UTC)

// fakeForgejo serves the Forgejo Actions read API for repository o/r
// under a path prefix. It fails the test on any request that is not a
// GET, so no driver operation can dispatch, cancel, or rerun a run.
type fakeForgejo struct {
	t        *testing.T
	mu       sync.Mutex
	runs     []fakeRun // any order; listed newest first
	jobs     map[int][]fakeJob
	arts     map[int][]fakeArtifact
	zips     map[int][]byte
	requests []string
	failList int // number of run listings to fail with HTTP 500
}

type fakeRun struct {
	ID       int
	Workflow string
	Event    string
	Status   string // Forgejo status
	Created  time.Time
}

type fakeJob struct {
	Name   string
	Status string // Forgejo status
}

type fakeArtifact struct {
	ID   int
	Name string
}

const prefix = "/git"

func (f *fakeForgejo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if r.Method != http.MethodGet {
		f.t.Errorf("unexpected %s %s: the CI driver only reads", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path, ok := strings.CutPrefix(r.URL.Path, prefix+"/api/v1/repos/o/r/actions/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	switch {
	case path == "runs":
		if f.failList > 0 {
			f.failList--
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"listing broke"}`))
			return
		}
		f.listRuns(w, r)
	case len(parts) == 2 && parts[0] == "runs":
		for _, run := range f.runs {
			if strconv.Itoa(run.ID) == parts[1] {
				writeJSON(w, runJSON(run))
				return
			}
		}
		http.NotFound(w, r)
	case len(parts) == 3 && parts[0] == "runs" && parts[2] == "jobs":
		id, _ := strconv.Atoi(parts[1])
		out := []map[string]any{}
		for i, j := range f.jobs[id] {
			out = append(out, map[string]any{"id": id*100 + i, "name": j.Name, "status": j.Status, "attempt": 1})
		}
		writeJSON(w, out)
	case len(parts) == 3 && parts[0] == "runs" && parts[2] == "artifacts":
		id, _ := strconv.Atoi(parts[1])
		out := []map[string]any{}
		if r.URL.Query().Get("page") == "1" {
			for _, a := range f.arts[id] {
				out = append(out, map[string]any{"id": a.ID, "name": a.Name, "run_id": id})
			}
		}
		writeJSON(w, out)
	case len(parts) == 3 && parts[0] == "artifacts" && parts[2] == "zip":
		id, _ := strconv.Atoi(parts[1])
		data, ok := f.zips[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	case len(parts) == 3 && parts[0] == "jobs" && parts[2] == "logs":
		_, _ = w.Write([]byte("log of job " + parts[1]))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeForgejo) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	runs := slices.Clone(f.runs)
	slices.SortFunc(runs, func(a, b fakeRun) int { return b.ID - a.ID })
	var out []map[string]any
	for _, run := range runs {
		if wf := q.Get("workflow_id"); wf != "" && run.Workflow != wf {
			continue
		}
		out = append(out, runJSON(run))
	}
	start := min((page-1)*limit, len(out))
	end := min(start+limit, len(out))
	writeJSON(w, map[string]any{"total_count": len(out), "workflow_runs": out[start:end]})
}

func runJSON(run fakeRun) map[string]any {
	return map[string]any{
		"id": run.ID, "workflow_id": run.Workflow, "event": run.Event, "trigger_event": run.Event,
		"status": run.Status, "html_url": "https://forgejo.example/o/r/actions/runs/" + strconv.Itoa(run.ID),
		"created": run.Created.Format(time.RFC3339),
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// update changes the fake's state under its lock.
func (f *fakeForgejo) update(fn func(f *fakeForgejo)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeForgejo) requestCount(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.Contains(r, substr) {
			n++
		}
	}
	return n
}

// newDriver returns a driver backed by the real Forgejo adapter talking
// to a fake Forgejo at a path-prefixed base URL. Its timers fire at once
// and its clock advances a minute per reading, so every wait ends fast.
func newDriver(t *testing.T) (*Driver, *fakeForgejo) {
	t.Helper()
	f := &fakeForgejo{t: t, jobs: map[int][]fakeJob{}, arts: map[int][]fakeArtifact{}, zips: map[int][]byte{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	client, err := forgejo.New(srv.URL+prefix, "tok", srv.Client())
	require.NoError(t, err)
	d, ok := New(client, "tok").(*Driver)
	require.True(t, ok)
	d.afterFunc = func(time.Duration) <-chan time.Time {
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
	var clockMu sync.Mutex
	clock := trigger
	d.nowFunc = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock = clock.Add(time.Minute)
		return clock
	}
	return d, f
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

var ctx = context.Background()

func TestNewImplementsTheOptionalExtensions(t *testing.T) {
	t.Parallel()
	client := forge.NewFakeClient()
	d := New(client, "tok")
	assert.Implements(t, (*ci.RunLister)(nil), d)
	assert.Implements(t, (*ci.EmptyMatrixWaiter)(nil), d)
	assert.Implements(t, (*ci.AgentArtifactDownloader)(nil), d)
	fd, ok := d.(*Driver)
	require.True(t, ok)
	assert.Same(t, client, fd.Client)
	assert.Equal(t, "tok", fd.Token)
}

func TestWaitForWorkflowMatchesWorkflowEventAndStartTime(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.runs = []fakeRun{
		{ID: 1, Workflow: "fullsend.yaml", Event: "issues", Status: "success", Created: trigger.Add(-time.Minute)},
		{ID: 2, Workflow: "fullsend.yaml", Event: "issues", Status: "success", Created: trigger.Truncate(time.Second)},
		{ID: 3, Workflow: "fullsend.yaml", Event: "issue_comment", Status: "success", Created: trigger.Add(time.Second)},
		{ID: 4, Workflow: "other.yaml", Event: "issues", Status: "success", Created: trigger.Add(time.Second)},
	}

	run, err := d.WaitForWorkflow(ctx, "o", "r", ".forgejo/workflows/fullsend.yaml", trigger, "issues")
	require.NoError(t, err)
	assert.Equal(t, 2, run.ID, "a run created in the trigger's own second matches")
	assert.Equal(t, "completed", run.Status)

	run, err = d.WaitForWorkflow(ctx, "o", "r", "fullsend.yaml", trigger, "")
	require.NoError(t, err)
	assert.Equal(t, 3, run.ID, "any event matches when none is given")
}

func TestWaitForWorkflowNotDispatched(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.failList = 1
	f.runs = []fakeRun{{ID: 1, Workflow: "fullsend.yaml", Event: "issues", Status: "success", Created: trigger.Add(-30 * time.Second)}}

	run, err := d.WaitForWorkflow(ctx, "o", "r", "fullsend.yaml", trigger, "issues")
	assert.Nil(t, run)
	require.ErrorIs(t, err, ci.ErrWorkflowNotDispatched)
	var nd *ci.NotDispatchedError
	require.ErrorAs(t, err, &nd)
	assert.Equal(t, "fullsend.yaml", nd.WorkflowFile)
	assert.Equal(t, "issues", nd.Event)
	assert.Contains(t, nd.Detail, "listing broke", "the poll errors are reported")

	// Steps retry once from a trigger moved back by the clock-skew
	// buffer; the same run is then found.
	run, err = d.WaitForWorkflow(ctx, "o", "r", "fullsend.yaml", trigger.Add(-time.Minute), "issues")
	require.NoError(t, err)
	assert.Equal(t, 1, run.ID)
}

func TestWaitForWorkflowWaitsForCompletion(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.runs = []fakeRun{{ID: 5, Workflow: "fullsend.yaml", Event: "issues", Status: "running", Created: trigger}}
	gets := 0
	d.afterFunc = func(time.Duration) <-chan time.Time {
		if f.requestCount("/runs/5") > gets {
			gets++
			f.update(func(f *fakeForgejo) { f.runs[0].Status = "success" })
		}
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}

	run, err := d.WaitForWorkflow(ctx, "o", "r", "fullsend.yaml", trigger, "issues")
	require.NoError(t, err)
	assert.Equal(t, 5, run.ID)
	assert.Equal(t, "success", run.Conclusion)
}

func TestWaitForWorkflowFailedRun(t *testing.T) {
	t.Parallel()

	t.Run("failed during dispatch", func(t *testing.T) {
		t.Parallel()
		d, f := newDriver(t)
		f.runs = []fakeRun{{ID: 5, Workflow: "fullsend.yaml", Event: "issues", Status: "failure", Created: trigger}}
		_, err := d.WaitForWorkflow(ctx, "o", "r", "fullsend.yaml", trigger, "issues")
		require.ErrorContains(t, err, `concluded with "failure" during dispatch`)
		assert.NotErrorIs(t, err, ci.ErrWorkflowNotDispatched)
	})

	t.Run("cancelled run superseded by a newer success", func(t *testing.T) {
		t.Parallel()
		d, f := newDriver(t)
		f.runs = []fakeRun{{ID: 5, Workflow: "fullsend.yaml", Event: "issues", Status: "running", Created: trigger}}
		d.afterFunc = func(time.Duration) <-chan time.Time {
			if f.requestCount("/runs/5") > 0 {
				f.update(func(f *fakeForgejo) {
					f.runs = []fakeRun{
						{ID: 5, Workflow: "fullsend.yaml", Event: "issues", Status: "cancelled", Created: trigger},
						{ID: 6, Workflow: "fullsend.yaml", Event: "issues", Status: "success", Created: trigger.Add(time.Second)},
					}
				})
			}
			c := make(chan time.Time, 1)
			c <- time.Time{}
			return c
		}
		run, err := d.WaitForWorkflow(ctx, "o", "r", "fullsend.yaml", trigger, "issues")
		require.NoError(t, err)
		assert.Equal(t, 6, run.ID)
	})

	t.Run("failure with no replacement", func(t *testing.T) {
		t.Parallel()
		d, f := newDriver(t)
		f.runs = []fakeRun{{ID: 5, Workflow: "fullsend.yaml", Event: "issues", Status: "running", Created: trigger}}
		d.afterFunc = func(time.Duration) <-chan time.Time {
			if f.requestCount("/runs/5") > 0 {
				f.update(func(f *fakeForgejo) { f.runs[0].Status = "failure" })
			}
			c := make(chan time.Time, 1)
			c <- time.Time{}
			return c
		}
		run, err := d.WaitForWorkflow(ctx, "o", "r", "fullsend.yaml", trigger, "issues")
		require.ErrorContains(t, err, `run 5 concluded with "failure"`)
		require.NotNil(t, run)
		assert.Equal(t, 5, run.ID)
	})

	t.Run("never completes", func(t *testing.T) {
		t.Parallel()
		d, f := newDriver(t)
		f.runs = []fakeRun{{ID: 5, Workflow: "fullsend.yaml", Event: "issues", Status: "running", Created: trigger}}
		_, err := d.WaitForWorkflow(ctx, "o", "r", "fullsend.yaml", trigger, "issues")
		require.ErrorContains(t, err, "run 5 did not complete within deadline")
	})
}

func TestWaitForWorkflowHonoursContext(t *testing.T) {
	t.Parallel()
	d, _ := newDriver(t)
	d.afterFunc = func(time.Duration) <-chan time.Time { return nil }
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err := d.WaitForWorkflow(cctx, "o", "r", "fullsend.yaml", trigger, "")
	require.ErrorIs(t, err, context.Canceled)
}

func TestFindCompletedWorkflowRun(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.runs = []fakeRun{
		{ID: 1, Workflow: "fullsend.yaml", Event: "issues", Status: "success", Created: trigger.Add(-time.Hour)},
		{ID: 2, Workflow: "fullsend.yaml", Event: "issues", Status: "running", Created: trigger},
	}
	_, err := d.FindCompletedWorkflowRun(ctx, "o", "r", "fullsend.yaml", trigger)
	require.ErrorContains(t, err, "no completed workflow run for fullsend.yaml")

	f.update(func(f *fakeForgejo) { f.runs[1].Status = "success" })
	run, err := d.FindCompletedWorkflowRun(ctx, "o", "r", "fullsend.yaml", trigger)
	require.NoError(t, err)
	assert.Equal(t, 2, run.ID)
}

func TestAssertNoWorkflow(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.runs = []fakeRun{
		{ID: 1, Workflow: "fullsend.yaml", Event: "issues", Status: "success", Created: trigger.Add(-time.Minute)},
		{ID: 2, Workflow: "other.yaml", Event: "issues", Status: "success", Created: trigger.Add(time.Minute)},
	}
	require.NoError(t, d.AssertNoWorkflow(ctx, "o", "r", "fullsend.yaml", trigger))

	f.update(func(f *fakeForgejo) {
		f.runs = append(f.runs, fakeRun{ID: 3, Workflow: "fullsend.yaml", Event: "issues", Status: "running", Created: trigger.Add(time.Minute)})
	})
	require.ErrorContains(t, d.AssertNoWorkflow(ctx, "o", "r", "fullsend.yaml", trigger), "unexpected workflow fullsend.yaml run 3")

	f.update(func(f *fakeForgejo) { f.failList = 1 })
	require.Error(t, d.AssertNoWorkflow(ctx, "o", "r", "fullsend.yaml", trigger))
}

func TestListRecentRunsAndLogs(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.runs = []fakeRun{
		{ID: 1, Workflow: "fullsend.yaml", Event: "issues", Status: "success", Created: trigger},
		{ID: 2, Workflow: "shim.yaml", Event: "issue_comment", Status: "failure", Created: trigger},
	}
	f.jobs[2] = []fakeJob{{Name: "Route", Status: "failure"}}

	runs, err := d.ListRecentRuns(ctx, "o", "r", 10)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, "shim.yaml", runs[0].Path, "the failure diagnostics name each run's workflow file")
	assert.Equal(t, "issue_comment", runs[0].Event)

	logs, err := d.GetRunLogs(ctx, "o", "r", 2)
	require.NoError(t, err)
	assert.Contains(t, logs, "Route (failure)")
	assert.Contains(t, logs, "log of job 200")
}

func TestDownloadsFromARun(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.arts[7] = []fakeArtifact{{ID: 1, Name: "fullsend-triage.matrix-0"}, {ID: 2, Name: "fullsend-triage.matrix-1"}, {ID: 3, Name: "fullsend-code"}}
	f.zips[1] = zipOf(t, map[string]string{"a.txt": "zero"})
	f.zips[2] = zipOf(t, map[string]string{"a.txt": "one"})
	f.zips[3] = zipOf(t, map[string]string{"dir/b.txt": "code"})

	dir := t.TempDir()
	require.NoError(t, ci.DownloadAgentArtifactsFromRun(ctx, d, "o", "r", 7, "triage", dir))
	data, err := os.ReadFile(filepath.Join(dir, "fullsend-triage.matrix-1", "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "one", string(data))
	assert.NoFileExists(t, filepath.Join(dir, "fullsend-code", "dir", "b.txt"))

	require.NoError(t, d.DownloadNamedArtifactFromRun(ctx, "o", "r", 7, "fullsend-code", dir))
	assert.FileExists(t, filepath.Join(dir, "fullsend-code", "dir", "b.txt"))

	all := t.TempDir()
	require.NoError(t, d.DownloadArtifacts(ctx, "o", "r", 7, all))
	assert.FileExists(t, filepath.Join(all, "fullsend-triage.matrix-0", "a.txt"))

	require.ErrorContains(t, d.DownloadNamedArtifactFromRun(ctx, "o", "r", 7, "fullsend-review", dir), `artifact "fullsend-review" not found in workflow run 7`)
	require.ErrorContains(t, d.DownloadAgentArtifactsFromRun(ctx, "o", "r", 7, "review", dir), `no artifact for agent "review"`)

	f.update(func(f *fakeForgejo) { delete(f.zips, 3) })
	require.ErrorIs(t, d.DownloadNamedArtifactFromRun(ctx, "o", "r", 7, "fullsend-code", dir), forge.ErrNotFound)
}

func TestDownloadAfterUsesOnlyTheNewestRunsOwnArtifacts(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.runs = []fakeRun{
		{ID: 1, Workflow: "fullsend.yaml", Event: "issues", Status: "success", Created: trigger.Add(-time.Hour)},
		{ID: 2, Workflow: "fullsend.yaml", Event: "issues", Status: "success", Created: trigger},
		{ID: 3, Workflow: "fullsend.yaml", Event: "issues", Status: "success", Created: trigger.Add(time.Second)},
		{ID: 4, Workflow: "fullsend.yaml", Event: "issues", Status: "running", Created: trigger.Add(2 * time.Second)},
	}
	f.arts[1] = []fakeArtifact{{ID: 10, Name: "fullsend-triage"}}
	f.arts[2] = []fakeArtifact{{ID: 20, Name: "fullsend-triage"}}
	f.arts[3] = []fakeArtifact{{ID: 30, Name: "fullsend-triage.matrix-0"}, {ID: 31, Name: "fullsend-triage.matrix-1"}}
	f.arts[4] = []fakeArtifact{{ID: 40, Name: "fullsend-triage.matrix-0"}}
	for _, id := range []int{10, 20, 30, 31, 40} {
		f.zips[id] = zipOf(t, map[string]string{"run.txt": strconv.Itoa(id)})
	}

	dir := t.TempDir()
	require.NoError(t, ci.DownloadAgentArtifactsAfter(ctx, d, "o", "r", "triage", trigger, dir))
	data, err := os.ReadFile(filepath.Join(dir, "fullsend-triage.matrix-0", "run.txt"))
	require.NoError(t, err)
	assert.Equal(t, "30", string(data), "the running run 4 is skipped and the older runs are not used")
	assert.FileExists(t, filepath.Join(dir, "fullsend-triage.matrix-1", "run.txt"))
	assert.NoDirExists(t, filepath.Join(dir, "fullsend-triage"))
	assert.Zero(t, f.requestCount("/actions/artifacts?"), "no repository-wide artifact listing")
	assert.Zero(t, f.requestCount("/runs/1/artifacts"), "runs before the trigger are not scanned")

	named := t.TempDir()
	require.NoError(t, d.DownloadNamedArtifactAfter(ctx, "o", "r", "fullsend-triage", trigger, named))
	data, err = os.ReadFile(filepath.Join(named, "fullsend-triage", "run.txt"))
	require.NoError(t, err)
	assert.Equal(t, "20", string(data))

	err = d.DownloadNamedArtifactAfter(ctx, "o", "r", "fullsend-review", trigger, t.TempDir())
	require.ErrorContains(t, err, `artifact "fullsend-review" not found in any workflow run after`)

	// A listed artifact whose upload is not confirmed yet (404) is
	// polled for rather than failing the download.
	f.update(func(f *fakeForgejo) { delete(f.zips, 20) })
	err = d.DownloadNamedArtifactAfter(ctx, "o", "r", "fullsend-triage", trigger, t.TempDir())
	require.ErrorContains(t, err, "not found in any workflow run")
}

func TestAgentArtifactNameMatches(t *testing.T) {
	t.Parallel()
	assert.True(t, agentArtifactNameMatches("fullsend-triage", "fullsend-triage"))
	assert.True(t, agentArtifactNameMatches("fullsend-triage.matrix-12", "fullsend-triage"))
	assert.False(t, agentArtifactNameMatches("fullsend-triage.matrix-x", "fullsend-triage"))
	assert.False(t, agentArtifactNameMatches("fullsend-triage-2", "fullsend-triage"))
	assert.False(t, agentArtifactNameMatches("triage", "triage"))
	assert.False(t, agentArtifactNameMatches("fullsend-a.b", "fullsend-a.b"))
}

func harnessRun(id int, status string, created time.Time) fakeRun {
	return fakeRun{ID: id, Workflow: "fullsend.yaml", Event: "issues", Status: status, Created: created}
}

func TestWaitForHarnessAgent(t *testing.T) {
	t.Parallel()

	t.Run("succeeds on the agent's job", func(t *testing.T) {
		t.Parallel()
		d, f := newDriver(t)
		f.runs = []fakeRun{harnessRun(1, "success", trigger.Add(-time.Hour)), harnessRun(2, "success", trigger), harnessRun(3, "success", trigger)}
		f.jobs[2] = []fakeJob{{Name: "dispatch / Triage", Status: "success"}}
		f.jobs[3] = []fakeJob{{Name: "dispatch / Harness run (other)", Status: "success"}}
		run, err := d.WaitForHarnessAgent(ctx, "o", "r", "triage", trigger)
		require.NoError(t, err)
		assert.Equal(t, 2, run.ID)
	})

	t.Run("succeeds on the agent's artifact", func(t *testing.T) {
		t.Parallel()
		d, f := newDriver(t)
		f.runs = []fakeRun{harnessRun(2, "success", trigger)}
		f.jobs[2] = []fakeJob{{Name: "unrecognised", Status: "success"}}
		f.arts[2] = []fakeArtifact{{ID: 1, Name: "fullsend-pr-ping.matrix-0"}}
		run, err := d.WaitForHarnessAgent(ctx, "o", "r", "pr-ping", trigger)
		require.NoError(t, err)
		assert.Equal(t, 2, run.ID)
	})

	t.Run("fails with the failed run", func(t *testing.T) {
		t.Parallel()
		d, f := newDriver(t)
		f.runs = []fakeRun{harnessRun(2, "failure", trigger)}
		f.jobs[2] = []fakeJob{{Name: "Harness run (pr-ping)", Status: "failure"}}
		run, err := d.WaitForHarnessAgent(ctx, "o", "r", "pr-ping", trigger)
		require.ErrorContains(t, err, `harness agent "pr-ping": workflow run 2 concluded with "failure"`)
		require.NotNil(t, run)
		assert.Equal(t, 2, run.ID)
	})

	t.Run("a newer pending run holds an older failure", func(t *testing.T) {
		t.Parallel()
		d, f := newDriver(t)
		f.runs = []fakeRun{harnessRun(2, "failure", trigger), harnessRun(3, "running", trigger)}
		f.jobs[2] = []fakeJob{{Name: "Harness run (pr-ping)", Status: "failure"}}
		f.jobs[3] = []fakeJob{{Name: "Harness run (pr-ping)", Status: "running"}}
		run, err := d.WaitForHarnessAgent(ctx, "o", "r", "pr-ping", trigger)
		assert.Nil(t, run)
		require.ErrorContains(t, err, `harness agent "pr-ping" did not complete successfully within`)
	})

	t.Run("a sibling job's failure and cancelled runs are ignored", func(t *testing.T) {
		t.Parallel()
		d, f := newDriver(t)
		f.runs = []fakeRun{harnessRun(2, "failure", trigger), harnessRun(3, "cancelled", trigger), harnessRun(4, "running", trigger)}
		f.jobs[2] = []fakeJob{{Name: "Harness run (pr-ping)", Status: "success"}, {Name: "Harness run (x)", Status: "failure"}}
		f.jobs[3] = []fakeJob{{Name: "Harness run (pr-ping)", Status: "cancelled"}}
		f.jobs[4] = []fakeJob{{Name: "Harness run (x)", Status: "running"}, {Name: "Route", Status: "success"}}
		f.failList = 1
		_, err := d.WaitForHarnessAgent(ctx, "o", "r", "pr-ping", trigger)
		require.ErrorContains(t, err, "did not complete successfully")
		assert.Contains(t, err.Error(), "listing broke")
	})
}

func TestWaitForHarnessAgentRound(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.runs = []fakeRun{harnessRun(2, "success", trigger), harnessRun(3, "success", trigger.Add(time.Second)), harnessRun(4, "failure", trigger.Add(2*time.Second))}
	f.jobs[2] = []fakeJob{{Name: "Harness run (review)", Status: "success"}}
	f.jobs[3] = []fakeJob{{Name: "Harness run (review)", Status: "success"}}
	f.jobs[4] = []fakeJob{{Name: "Harness run (review)", Status: "failure"}}

	run, err := d.WaitForHarnessAgentRound(ctx, "o", "r", "review", trigger, map[int]bool{})
	require.NoError(t, err)
	assert.Equal(t, 2, run.ID, "the earliest round is selected")

	run, err = d.WaitForHarnessAgentRound(ctx, "o", "r", "review", trigger, map[int]bool{2: true})
	require.NoError(t, err)
	assert.Equal(t, 3, run.ID)

	run, err = d.WaitForHarnessAgentRound(ctx, "o", "r", "review", trigger, map[int]bool{2: true, 3: true})
	require.ErrorContains(t, err, `workflow run 4 concluded with "failure"`)
	assert.Equal(t, 4, run.ID)

	f.update(func(f *fakeForgejo) { f.runs[0].Status = "running"; f.jobs[2][0].Status = "running" })
	_, err = d.WaitForHarnessAgentRound(ctx, "o", "r", "review", trigger, map[int]bool{})
	require.ErrorContains(t, err, "did not complete a new round successfully", "an unsettled earlier round holds later ones")
}

func TestWaitForFailedHarnessAgent(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.runs = []fakeRun{harnessRun(2, "failure", trigger)}
	f.jobs[2] = []fakeJob{{Name: "Code", Status: "failure"}}

	run, err := d.WaitForFailedHarnessAgent(ctx, "o", "r", "code", trigger)
	require.NoError(t, err)
	assert.Equal(t, 2, run.ID)

	f.update(func(f *fakeForgejo) { f.runs[0].Status = "success"; f.jobs[2][0].Status = "success" })
	_, err = d.WaitForFailedHarnessAgent(ctx, "o", "r", "code", trigger)
	require.ErrorContains(t, err, "concluded successfully; expected failure")

	f.update(func(f *fakeForgejo) { f.jobs[2] = nil })
	_, err = d.WaitForFailedHarnessAgent(ctx, "o", "r", "code", trigger)
	require.ErrorContains(t, err, "did not complete with a failure within")
}

func TestAssertNoHarnessAgentArtifact(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.runs = []fakeRun{harnessRun(1, "success", trigger.Add(-time.Hour)), harnessRun(2, "success", trigger), harnessRun(3, "skipped", trigger)}
	f.jobs[1] = []fakeJob{{Name: "Triage", Status: "success"}}
	f.jobs[2] = []fakeJob{{Name: "Triage", Status: "skipped"}, {Name: "Harness run (other)", Status: "success"}}
	require.NoError(t, d.AssertNoHarnessAgentArtifact(ctx, "o", "r", "triage", trigger))

	f.update(func(f *fakeForgejo) {
		f.jobs[2] = append(f.jobs[2], fakeJob{Name: "Harness run (triage)", Status: "cancelled"})
	})
	require.ErrorContains(t, d.AssertNoHarnessAgentArtifact(ctx, "o", "r", "triage", trigger), `job "Harness run (triage)" found in workflow run 2`)

	f.update(func(f *fakeForgejo) { f.runs = append(f.runs, harnessRun(99, "running", trigger)) })
	f.update(func(f *fakeForgejo) { f.failList = 1 })
	require.Error(t, d.AssertNoHarnessAgentArtifact(ctx, "o", "r", "triage", trigger))
}

func TestCountHarnessDispatches(t *testing.T) {
	t.Parallel()
	d, f := newDriver(t)
	f.runs = []fakeRun{
		harnessRun(1, "success", trigger.Add(-time.Hour)),
		harnessRun(2, "success", trigger),
		harnessRun(3, "cancelled", trigger),
		harnessRun(4, "failure", trigger),
		harnessRun(5, "skipped", trigger),
		harnessRun(6, "success", trigger),
	}
	f.jobs[1] = []fakeJob{{Name: "Harness run (x)", Status: "success"}}
	f.jobs[2] = []fakeJob{{Name: "Harness run (x)", Status: "success"}}
	f.jobs[3] = []fakeJob{{Name: "Harness run (x)", Status: "cancelled"}}
	f.jobs[4] = []fakeJob{{Name: "Harness run (x)", Status: "failure"}}
	f.jobs[6] = []fakeJob{{Name: "Harness run (y)", Status: "success"}}

	n, err := d.CountHarnessDispatches(ctx, "o", "r", "x", trigger)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	f.update(func(f *fakeForgejo) {
		f.runs = append(f.runs, harnessRun(7, "waiting", trigger), harnessRun(8, "running", trigger))
		f.jobs[8] = []fakeJob{{Name: "Route", Status: "running"}}
	})
	_, err = d.CountHarnessDispatches(ctx, "o", "r", "x", trigger)
	require.ErrorContains(t, err, "2 run(s) still pending")

	f.update(func(f *fakeForgejo) { f.failList = 1 })
	_, err = d.CountHarnessDispatches(ctx, "o", "r", "x", trigger)
	require.Error(t, err)
}

func TestWaitForHarnessEmptyMatrix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		status  string
		jobs    []fakeJob
		wantErr string
	}{
		{name: "placeholder replaced", status: "success", jobs: []fakeJob{{Name: "dispatch / Harness dispatch", Status: "success"}}},
		{name: "placeholder skipped", status: "success", jobs: []fakeJob{
			{Name: "dispatch / Harness dispatch", Status: "success"},
			{Name: "dispatch / Harness run (${{ matrix.agent }})", Status: "skipped"},
		}},
		{name: "agent scheduled", status: "success", jobs: []fakeJob{
			{Name: "Harness dispatch", Status: "success"}, {Name: "Harness run (triage)", Status: "success"},
		}, wantErr: `job "Harness run (triage)" was scheduled`},
		{name: "placeholder failed", status: "failure", jobs: []fakeJob{
			{Name: "Harness dispatch", Status: "success"}, {Name: "Harness run (${{ matrix.agent }})", Status: "failure"},
		}, wantErr: "to be skipped with an empty matrix"},
		{name: "dispatch failed", status: "failure", jobs: []fakeJob{{Name: "Harness dispatch", Status: "failure"}}, wantErr: "concluded failure"},
		{name: "no dispatch job", status: "failure", jobs: []fakeJob{{Name: "Route", Status: "failure"}}, wantErr: "has no such job"},
		{name: "still running", status: "running", wantErr: "1 harness run(s) still in progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, f := newDriver(t)
			f.runs = []fakeRun{harnessRun(1, "cancelled", trigger), harnessRun(2, tc.status, trigger)}
			f.jobs[2] = tc.jobs
			run, err := d.WaitForHarnessEmptyMatrix(ctx, "o", "r", trigger)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 2, run.ID)
		})
	}

	t.Run("no run", func(t *testing.T) {
		t.Parallel()
		d, f := newDriver(t)
		f.failList = 1
		_, err := d.WaitForHarnessEmptyMatrix(ctx, "o", "r", trigger)
		require.ErrorContains(t, err, "no harness run created after the trigger time")
		assert.Contains(t, err.Error(), "last lookup error")
	})
}

func TestExtractArtifactZipRejectsUnsafeEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.ErrorContains(t, extractArtifactZip("a", zipOf(t, map[string]string{"../evil": "x"}), dir), "path traversal")
	require.ErrorContains(t, extractArtifactZip("a", []byte("not a zip"), dir), "parse artifact zip")
	require.NoError(t, extractArtifactZip("..", zipOf(t, map[string]string{"sub/": "", "f": "x"}), dir))
	assert.FileExists(t, filepath.Join(dir, "artifact", "f"))
	_, err := readLimited(strings.NewReader("12345"), 4)
	require.Error(t, err)
}

func TestPollErrorsDescribe(t *testing.T) {
	t.Parallel()
	var p pollErrors
	p.record(ctx, nil)
	p.record(ctx, errors.New("boom"))
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	p.record(expired, context.DeadlineExceeded)
	assert.Equal(t, " (failed on 1 of 3 polls during the wait; last: boom) (1 of 3 polls cut short by the wait budget)", p.describe("polls"))
	assert.Empty(t, pollErrors{}.describe("polls"))
}

func TestZeroValueDriverUsesTheRealClock(t *testing.T) {
	t.Parallel()
	d := &Driver{}
	assert.WithinDuration(t, time.Now(), d.now(), time.Minute)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, d.sleep(cctx, time.Hour), context.Canceled)
}
