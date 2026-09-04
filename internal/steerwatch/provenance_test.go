package steerwatch

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestRouteJobName(t *testing.T) {
	assert.True(t, routeJobName("Route"))
	assert.True(t, routeJobName("dispatch / Route"))
	assert.True(t, routeJobName("fullsend-dispatch / Route"))
	assert.False(t, routeJobName("Reroute"))
	assert.False(t, routeJobName("dispatch / Review"))
}

func TestSameDispatchChain(t *testing.T) {
	mine := []forge.ReferencedWorkflow{
		{Path: "o/r/.github/workflows/reusable-dispatch.yml@main", Ref: "refs/heads/main", SHA: "abc"},
		{Path: "o/r/.github/workflows/other.yml@main", Ref: "refs/heads/main", SHA: "def"},
	}

	t.Run("same set in a different order matches", func(t *testing.T) {
		theirs := []forge.ReferencedWorkflow{mine[1], mine[0]}
		assert.True(t, sameDispatchChain(mine, theirs))
	})
	t.Run("a different sha at the same path and ref passes", func(t *testing.T) {
		// A branch-pinned shim (@main) resolves to a new sha whenever the
		// branch advances; that is the same trusted workflow, newer.
		theirs := []forge.ReferencedWorkflow{mine[0], {Path: mine[1].Path, Ref: mine[1].Ref, SHA: "zzz"}}
		assert.True(t, sameDispatchChain(mine, theirs))
	})
	t.Run("a different ref fails", func(t *testing.T) {
		theirs := []forge.ReferencedWorkflow{mine[0], {Path: mine[1].Path, Ref: "refs/tags/v0.1.0", SHA: mine[1].SHA}}
		assert.False(t, sameDispatchChain(mine, theirs))
	})
	t.Run("a renamed reusable workflow fails", func(t *testing.T) {
		theirs := []forge.ReferencedWorkflow{mine[0], {Path: "o/r/.github/workflows/evil.yml@main", Ref: mine[1].Ref, SHA: mine[1].SHA}}
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
			jobs := []forge.WorkflowJob{{Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
				{Name: stageName, Status: tt.status, Conclusion: tt.conclusion}}
			selected, found := jobSelected(jobs, stageName)
			require.True(t, found)
			assert.Equal(t, tt.wantSelected, selected)
		})
	}
}

func TestJobSelected_JobAbsent(t *testing.T) {
	_, found := jobSelected([]forge.WorkflowJob{{Name: "dispatch / Triage"}}, stageName)
	assert.False(t, found)
}

func TestRouteSucceeded(t *testing.T) {
	// Under queue: single a later event cancels the earlier pending stage
	// job and the run concludes cancelled — but the Route job's success is
	// what carries the authorization, so the run is still a valid steer.
	jobs := []forge.WorkflowJob{
		{Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
		{Name: stageName, Status: "completed", Conclusion: "cancelled"},
	}
	assert.True(t, routeSucceeded(jobs))

	jobs[0].Conclusion = "failure"
	assert.False(t, routeSucceeded(jobs))

	assert.False(t, routeSucceeded([]forge.WorkflowJob{{Name: stageName}}), "no Route job at all")
}

func TestBoundToItem(t *testing.T) {
	prRun := forge.WorkflowRun{PullRequestNumbers: []int{7}}
	assert.True(t, boundToItem(prRun, "", 7))
	assert.False(t, boundToItem(prRun, "", 8))

	// issue_comment runs carry no pull_requests[], so the shim's run-name
	// (returned as display_title) is what binds them.
	commentRun := forge.WorkflowRun{DisplayTitle: "org/repo#7"}
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
			run := forgeRun(tt.run)
			rej := w.candidateChecks(run)
			require.NotNil(t, rej, "candidate should have been rejected")
			assert.Equal(t, tt.wantCheck, rej.check, rej.String())
		})
	}
}

func TestCandidateChecks_Accepts(t *testing.T) {
	api := newFakeAPI()
	w := newWatcher(t, api, &stubItems{}, &recorder{}, nil)

	run := forgeRun(runOpts{id: 100, event: "pull_request_target", prNumbers: []int{7}})
	assert.Nil(t, w.candidateChecks(run))

	// Once consumed, the same run is never taken again.
	w.markSteered(int64(run.ID), []forge.WorkflowRun{run}, delta{})
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

			run := forgeRun(runOpts{id: 42, prNumbers: []int{7}})
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
		name, err := resolveStageJob([]forge.WorkflowJob{
			{Name: "dispatch / Route", Status: "completed", Conclusion: "success"},
			{Name: stageName, Status: "in_progress"},
			{Name: "dispatch / Fix", Status: "completed", Conclusion: "skipped"},
		}, "")
		require.NoError(t, err)
		assert.Equal(t, stageName, name)
	})

	t.Run("the harness fan-out needs the hint", func(t *testing.T) {
		jobs := []forge.WorkflowJob{
			{Name: "dispatch / Harness Run (reviewer)", Status: "in_progress"},
			{Name: "dispatch / Harness Run (fixer)", Status: "in_progress"},
		}
		name, err := resolveStageJob(jobs, "fixer")
		require.NoError(t, err)
		assert.Equal(t, "dispatch / Harness Run (fixer)", name)
	})

	t.Run("an ambiguous job list fails closed", func(t *testing.T) {
		jobs := []forge.WorkflowJob{
			{Name: "dispatch / Harness Run (a)", Status: "in_progress"},
			{Name: "dispatch / Harness Run (b)", Status: "in_progress"},
		}
		_, err := resolveStageJob(jobs, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot tell which")
	})

	t.Run("no in-progress job fails", func(t *testing.T) {
		_, err := resolveStageJob([]forge.WorkflowJob{{Name: "dispatch / Route", Status: "completed"}}, "")
		require.Error(t, err)
	})

	t.Run("the route job is never mistaken for my stage", func(t *testing.T) {
		_, err := resolveStageJob([]forge.WorkflowJob{{Name: "dispatch / Route", Status: "in_progress"}}, "")
		require.Error(t, err)
	})
}

func TestStart_Errors(t *testing.T) {
	t.Run("my run references no reusable workflow", func(t *testing.T) {
		api := newFakeAPI()
		api.myRun = runJSON(runOpts{id: myRunID, created: runStart, refs: []map[string]string{}})
		api.myJobs = jobsJSON(routeJob("success"), stageJob(stageName, "in_progress", ""))
		srv := api.server(t)
		w := New(Config{Repo: "org/repo", RunID: myRunID,
			StartedAt: mustTime(t, runStart), PollInterval: time.Millisecond},
			testGitHubClient(srv.URL), &stubItems{}, nil, nil)
		err := w.Start(context.Background(), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "references no reusable workflow")
	})

	t.Run("the API is unreachable", func(t *testing.T) {
		api := newFakeAPI()
		api.status["/actions/runs/"] = 403
		srv := api.server(t)
		w := New(Config{Repo: "org/repo", RunID: myRunID,
			StartedAt: mustTime(t, runStart), PollInterval: time.Millisecond},
			testGitHubClient(srv.URL), &stubItems{}, nil, nil)
		err := w.Start(context.Background(), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "403")
		assert.NotContains(t, err.Error(), "job-token", "the token must never reach an error string")
	})
}

func TestRunCreatedAt(t *testing.T) {
	assert.Equal(t, 2026, runCreatedAt(forge.WorkflowRun{CreatedAt: "2026-09-03T10:00:00Z"}).Year())
	// A run the watcher cannot date is a run it cannot prove is a follow-up,
	// so the zero time is the safe answer: it fails the freshness check.
	assert.True(t, runCreatedAt(forge.WorkflowRun{CreatedAt: "not a time"}).IsZero())
	assert.True(t, runCreatedAt(forge.WorkflowRun{}).IsZero())
}

func TestActorLogin(t *testing.T) {
	// The triggering actor is the human who commented; the run's actor can
	// be whoever originally created it on a re-run.
	assert.Equal(t, "reviewer", actorLogin(forge.WorkflowRun{Actor: "octocat", TriggeringActor: "reviewer"}))
	assert.Equal(t, "octocat", actorLogin(forge.WorkflowRun{Actor: "octocat"}))
	assert.Empty(t, actorLogin(forge.WorkflowRun{}))
}

func TestRunsSince_MalformedRepo(t *testing.T) {
	w := New(Config{Repo: "norepo"}, nil, &stubItems{}, nil, nil)
	_, err := w.runsSince(context.Background(), "fullsend.yml", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

// The runner's clock starts when the job picks up a machine, which can be
// well after the run was created. A follow-up that landed in that gap is a
// genuine follow-up, and rejecting it strands the update with the pending run.
func TestIsFollowUp(t *testing.T) {
	api := newFakeAPI()
	// My run was created at 10:00; the runner (StartedAt) only got going at
	// 10:03.
	api.myRun = runJSON(runOpts{id: myRunID, created: "2026-09-03T10:00:00Z"})
	w := newWatcher(t, api, &stubItems{}, &recorder{}, func(c *Config) {
		c.StartedAt = mustTime(t, "2026-09-03T10:03:00Z")
	})

	assert.Equal(t, mustTime(t, "2026-09-03T10:00:00Z"), w.freshAfter,
		"the baseline is my run's server-side creation, not the runner's clock")

	tests := []struct {
		name string
		run  runOpts
		want bool
	}{
		{"created while my job was still queued", runOpts{id: myRunID + 1, created: "2026-09-03T10:01:00Z"}, true},
		{"created after the runner started", runOpts{id: myRunID + 2, created: "2026-09-03T10:05:00Z"}, true},
		{"created before my run", runOpts{id: myRunID - 5, created: "2026-09-03T09:59:00Z"}, false},
		{"same second, larger id is later", runOpts{id: myRunID + 1, created: "2026-09-03T10:00:00Z"}, true},
		{"same second, smaller id is earlier", runOpts{id: myRunID - 1, created: "2026-09-03T10:00:00Z"}, false},
		{"undateable run is never a follow-up", runOpts{id: myRunID + 3, created: "not a time"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, w.isFollowUp(forgeRun(tt.run)))
		})
	}
}

func TestFreshAfter_FallsBackToRunnerStart(t *testing.T) {
	api := newFakeAPI()
	api.myRun = runJSON(runOpts{id: myRunID, created: "not a time"})
	w := newWatcher(t, api, &stubItems{}, &recorder{}, func(c *Config) {
		c.StartedAt = mustTime(t, runStart)
	})
	assert.Equal(t, mustTime(t, runStart), w.freshAfter)
}

// The amendment set is security-critical: an event here lets its run's actor
// author text the envelope tells the agent takes precedence over its
// original instructions. Pinned so widening it is a deliberate act with a
// test to change, not a one-word edit.
func TestAmendmentEventsIsExactlyIssueComment(t *testing.T) {
	assert.Equal(t, map[string]bool{"issue_comment": true}, amendmentEvents)
}

func TestAmendmentEventsAreASubsetOfAcceptedEvents(t *testing.T) {
	for event := range amendmentEvents {
		assert.True(t, allowedEvents[event],
			"%q confers amendment authority but is not even accepted as a follow-up", event)
	}
}

// routeArmEvents are the events reusable-dispatch.yml's route step has a case
// arm for. Pinned so that adding an arm fails here and forces a re-audit of
// whether that event's actor is the principal the arm authorizes — the F1
// question. See amendmentEvents for the audit itself.
var routeArmEvents = []string{
	"issue_comment",
	"issues",
	"pull_request_target",
	"pull_request_review",
}

func TestRouteArmsHaveNotDrifted(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)

	// The arms are the `<event>)` labels of the `case "${EVENT_NAME}" in`
	// statement in the route step.
	body := string(content)
	start := strings.Index(body, `case "${EVENT_NAME}" in`)
	require.Positive(t, start, "the route step's event case statement moved")
	armRe := regexp.MustCompile(`(?m)^\s{12}([a-z_]+)\)`)

	var found []string
	for _, m := range armRe.FindAllStringSubmatch(body[start:], -1) {
		found = append(found, m[1])
	}
	assert.Equal(t, routeArmEvents, found,
		"a route arm was added or removed; re-audit whether its run's actor is the "+
			"principal that arm authorizes, then update routeArmEvents and amendmentEvents")

	for event := range amendmentEvents {
		assert.Contains(t, found, event,
			"%q confers amendment authority but no route arm authorizes anyone for it", event)
	}
}

// The equivalence that makes issue_comment amendment-eligible — the login
// the route arm checks (github.event.comment.user.login) is the login the
// run reports — holds only while the shim delivers `created` events. On an
// `edited` event the sender is the editor, who need not be the comment's
// author, and the route arm never inspects the action.
//
// That invariant lives in workflow YAML outside this package, so it is
// pinned here: a shim that widened the types would silently reopen the gap.
func TestIssueCommentIsCreatedOnly(t *testing.T) {
	require.True(t, amendmentEvents["issue_comment"],
		"this test exists to guard issue_comment's amendment eligibility")

	shims := map[string]string{
		"repo shim":     filepath.Join("..", "..", ".github", "workflows", "fullsend.yaml"),
		"shim template": filepath.Join("..", "..", "internal", "scaffold", "fullsend-repo", "templates", "shim-per-repo.yaml"),
	}
	for name, path := range shims {
		t.Run(name, func(t *testing.T) {
			content, err := os.ReadFile(path)
			require.NoError(t, err)

			var wf struct {
				On struct {
					IssueComment struct {
						Types []string `yaml:"types"`
					} `yaml:"issue_comment"`
				} `yaml:"on"`
			}
			require.NoError(t, yaml.Unmarshal(content, &wf))

			assert.Equal(t, []string{"created"}, wf.On.IssueComment.Types,
				"widening issue_comment types breaks the actor-equals-authorizee equivalence "+
					"that makes it the only amendment-eligible event; see amendmentEvents")
		})
	}
}
