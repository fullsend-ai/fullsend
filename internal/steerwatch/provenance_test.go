package steerwatch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouteJobName(t *testing.T) {
	assert.True(t, routeJobName("Route"))
	assert.True(t, routeJobName("dispatch / Route"))
	assert.True(t, routeJobName("fullsend-dispatch / Route"))
	assert.False(t, routeJobName("Reroute"))
	assert.False(t, routeJobName("dispatch / Review"))
}

func TestSameDispatchChain(t *testing.T) {
	mine := []referencedWorkflow{
		{Path: "o/r/.github/workflows/reusable-dispatch.yml@main", Ref: "refs/heads/main", SHA: "abc"},
		{Path: "o/r/.github/workflows/other.yml@main", Ref: "refs/heads/main", SHA: "def"},
	}

	t.Run("same set in a different order matches", func(t *testing.T) {
		theirs := []referencedWorkflow{mine[1], mine[0]}
		assert.True(t, sameDispatchChain(mine, theirs))
	})
	t.Run("a different sha fails", func(t *testing.T) {
		theirs := []referencedWorkflow{mine[0], {Path: mine[1].Path, Ref: mine[1].Ref, SHA: "zzz"}}
		assert.False(t, sameDispatchChain(mine, theirs))
	})
	t.Run("a renamed reusable workflow fails", func(t *testing.T) {
		theirs := []referencedWorkflow{mine[0], {Path: "o/r/.github/workflows/evil.yml@main", Ref: mine[1].Ref, SHA: mine[1].SHA}}
		assert.False(t, sameDispatchChain(mine, theirs))
	})
	t.Run("a different length fails", func(t *testing.T) {
		assert.False(t, sameDispatchChain(mine, mine[:1]))
	})
	t.Run("both empty matches", func(t *testing.T) {
		assert.True(t, sameDispatchChain(nil, nil))
	})
}

func TestJobSelected(t *testing.T) {
	tests := []struct {
		name         string
		conclusion   string
		status       string
		wantSelected bool
	}{
		{"queued behind me counts as selected", "", "queued", true},
		{"running counts as selected", "", "in_progress", true},
		{"cancelled keeps its authorization", "cancelled", "completed", true},
		{"success counts", "success", "completed", true},
		{"skipped means the route job did not pick my stage", "skipped", "completed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := []job{{Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
				{Name: stageName, Status: tt.status, Conclusion: tt.conclusion}}
			selected, found := jobSelected(jobs, stageName)
			require.True(t, found)
			assert.Equal(t, tt.wantSelected, selected)
		})
	}
}

func TestJobSelected_JobAbsent(t *testing.T) {
	_, found := jobSelected([]job{{Name: "dispatch / Triage"}}, stageName)
	assert.False(t, found)
}

func TestRouteSucceeded(t *testing.T) {
	// Under queue: single a later event cancels the earlier pending stage
	// job and the run concludes cancelled — but the Route job's success is
	// what carries the authorization, so the run is still a valid steer.
	jobs := []job{
		{Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
		{Name: stageName, Status: "completed", Conclusion: "cancelled"},
	}
	assert.True(t, routeSucceeded(jobs))

	jobs[0].Conclusion = "failure"
	assert.False(t, routeSucceeded(jobs))

	assert.False(t, routeSucceeded([]job{{Name: stageName}}), "no Route job at all")
}

func TestBoundToItem(t *testing.T) {
	prRun := workflowRun{PullRequests: []struct {
		Number int `json:"number"`
	}{{Number: 7}}}
	assert.True(t, boundToItem(prRun, "", 7))
	assert.False(t, boundToItem(prRun, "", 8))

	// issue_comment runs carry no pull_requests[], so the shim's run-name
	// (returned as display_title) is what binds them.
	commentRun := workflowRun{DisplayTitle: "org/repo#7"}
	assert.True(t, boundToItem(commentRun, "org/repo#7", 7))
	assert.False(t, boundToItem(commentRun, "org/repo#8", 8))
	assert.False(t, boundToItem(commentRun, "", 7), "no run-name and no pull_requests means no binding")
}

// The provenance checks are the security boundary: each rejection below is a
// threat from ADR 0101's threat table.
func TestCandidateChecks(t *testing.T) {
	api := newFakeAPI()
	w := newWatcher(t, api, &stubItems{}, &recorder{}, nil)

	tests := []struct {
		name      string
		run       runOpts
		wantCheck string
	}{
		{"my own run", runOpts{id: myRunID, prNumbers: []int{7}}, "self"},
		{"a push run in the same repo", runOpts{id: 2, event: "push", prNumbers: []int{7}}, "event"},
		{"a workflow_dispatch run", runOpts{id: 3, event: "workflow_dispatch", prNumbers: []int{7}}, "event"},
		{"another workflow's run", runOpts{id: 4, path: ".github/workflows/ci.yml", prNumbers: []int{7}}, "shim"},
		{"a replayed run from before my start", runOpts{id: 5, created: "2026-09-03T09:00:00Z", prNumbers: []int{7}}, "fresh"},
		{"a foreign reusable workflow", runOpts{id: 6, prNumbers: []int{7},
			refs: []map[string]string{{"path": "evil/repo/.github/workflows/reusable-dispatch.yml@main", "ref": "refs/heads/main", "sha": "abc"}}}, "chain"},
		{"another work item's run", runOpts{id: 8, prNumbers: []int{99}, title: "org/repo#99"}, "item"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var run workflowRun
			decodeInto(t, runJSON(tt.run), &run)
			rej := w.candidateChecks(run)
			require.NotNil(t, rej, "candidate should have been rejected")
			assert.Equal(t, tt.wantCheck, rej.check, rej.String())
		})
	}
}

func TestCandidateChecks_Accepts(t *testing.T) {
	api := newFakeAPI()
	w := newWatcher(t, api, &stubItems{}, &recorder{}, nil)

	var run workflowRun
	decodeInto(t, runJSON(runOpts{id: 100, event: "pull_request_target", prNumbers: []int{7}}), &run)
	assert.Nil(t, w.candidateChecks(run))

	// Once consumed, the same run is never taken again.
	w.markConsumed([]workflowRun{run}, 1)
	rej := w.candidateChecks(run)
	require.NotNil(t, rej)
	assert.Equal(t, "once", rej.check)
}

func TestJobChecks(t *testing.T) {
	tests := []struct {
		name      string
		jobs      map[string]any
		wantCheck string
	}{
		{
			name:      "a fork author's /fs-steer skips every stage",
			jobs:      jobsJSON(routeJob("success"), stageJob(stageName, "completed", "skipped")),
			wantCheck: "stage",
		},
		{
			name:      "the route job rejected the actor",
			jobs:      jobsJSON(routeJob("failure"), stageJob(stageName, "completed", "skipped")),
			wantCheck: "route",
		},
		{
			name:      "my stage is not in the candidate run at all",
			jobs:      jobsJSON(routeJob("success"), stageJob("dispatch / Triage", "queued", "")),
			wantCheck: "stage",
		},
		{
			name: "queued behind me is accepted",
			jobs: jobsJSON(routeJob("success"), stageJob(stageName, "queued", "")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI()
			api.jobsByID[42] = tt.jobs
			w := newWatcher(t, api, &stubItems{}, &recorder{}, nil)

			var run workflowRun
			decodeInto(t, runJSON(runOpts{id: 42, prNumbers: []int{7}}), &run)
			rej, err := w.jobChecks(context.Background(), run)
			require.NoError(t, err)
			if tt.wantCheck == "" {
				assert.Nil(t, rej)
				return
			}
			require.NotNil(t, rej)
			assert.Equal(t, tt.wantCheck, rej.check, rej.String())
		})
	}
}

func TestResolveStageJob(t *testing.T) {
	t.Run("the single in-progress job is mine", func(t *testing.T) {
		name, err := resolveStageJob([]job{
			{Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
			{Name: stageName, Status: "in_progress"},
			{Name: "dispatch / Fix", Status: "completed", Conclusion: "skipped"},
		}, "")
		require.NoError(t, err)
		assert.Equal(t, stageName, name)
	})

	t.Run("the harness fan-out needs the hint", func(t *testing.T) {
		jobs := []job{
			{Name: "dispatch / Harness Run (reviewer)", Status: "in_progress"},
			{Name: "dispatch / Harness Run (fixer)", Status: "in_progress"},
		}
		name, err := resolveStageJob(jobs, "fixer")
		require.NoError(t, err)
		assert.Equal(t, "dispatch / Harness Run (fixer)", name)
	})

	t.Run("an ambiguous job list fails closed", func(t *testing.T) {
		jobs := []job{
			{Name: "dispatch / Harness Run (a)", Status: "in_progress"},
			{Name: "dispatch / Harness Run (b)", Status: "in_progress"},
		}
		_, err := resolveStageJob(jobs, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot tell which")
	})

	t.Run("no in-progress job fails", func(t *testing.T) {
		_, err := resolveStageJob([]job{{Name: "dispatch / Route", Status: "completed"}}, "")
		require.Error(t, err)
	})

	t.Run("the route job is never mistaken for my stage", func(t *testing.T) {
		_, err := resolveStageJob([]job{{Name: "dispatch / Route", Status: "in_progress"}}, "")
		require.Error(t, err)
	})
}

func TestStart_Errors(t *testing.T) {
	t.Run("my run references no reusable workflow", func(t *testing.T) {
		api := newFakeAPI()
		api.myRun = runJSON(runOpts{id: myRunID, created: runStart, refs: []map[string]string{}})
		api.myJobs = jobsJSON(routeJob("success"), stageJob(stageName, "in_progress", ""))
		srv := api.server(t)
		w := New(Config{Repo: "org/repo", RunID: myRunID, APIBase: srv.URL,
			StartedAt: mustTime(t, runStart), PollInterval: time.Millisecond}, &stubItems{}, nil, nil)
		err := w.Start(context.Background(), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "references no reusable workflow")
	})

	t.Run("the API is unreachable", func(t *testing.T) {
		api := newFakeAPI()
		api.status["/actions/runs/"] = 403
		srv := api.server(t)
		w := New(Config{Repo: "org/repo", RunID: myRunID, APIBase: srv.URL,
			StartedAt: mustTime(t, runStart), PollInterval: time.Millisecond}, &stubItems{}, nil, nil)
		err := w.Start(context.Background(), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "403")
		assert.NotContains(t, err.Error(), "job-token", "the token must never reach an error string")
	})
}
