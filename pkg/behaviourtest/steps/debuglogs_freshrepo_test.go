package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/failclass"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/retryselect"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const (
	freshOrg  = "halfsend-18"
	freshRepo = "test-repo-04"
)

// freshRepoWorld returns a World for a scenario on a slot repo installed
// fresh for it, whose CI driver lists runs from fake.
func freshRepoWorld(fake *forge.FakeClient) *world.World {
	return &world.World{
		Org: freshOrg, RepoName: freshRepo, LeasedRepoName: freshRepo, LeasedRepoOrg: freshOrg,
		RepoFreshlyInstalled: true,
		ScenarioName:         "OWNERS approver is not authorized when not opted in",
		ScenarioURI:          "features/dispatch/owners-auth.feature",
		CI:                   &githubactions.Driver{Client: fake},
		Logf:                 func(string, ...any) {},
	}
}

// TestCollectFailureLogs_FreshRepoNoRunIsInfrastructure pins #9779: when
// the first event on a freshly installed repo starts no run at all,
// the error CollectFailureLogs returns is infrastructure, and the retry
// selects exactly that scenario.
func TestCollectFailureLogs_FreshRepoNoRunIsInfrastructure(t *testing.T) {
	shortenDispatchGrace(t, 2*time.Minute)
	fakeDispatchClock(t, time.Date(2026, 11, 22, 1, 5, 44, 0, time.UTC), nil)
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

	fake := forge.NewFakeClient()
	w := freshRepoWorld(fake)
	scenarioErr := notDispatchedErr()

	got := CollectFailureLogs(context.Background(), w, scenarioErr)

	assert.Equal(t, behaviourfail.Infrastructure, failclass.Classify(got))
	var infra *behaviourfail.InfraError
	require.ErrorAs(t, got, &infra)
	assert.Equal(t, freshRepoNoRunReason, infra.Reason)
	assert.ErrorIs(t, got, ci.ErrWorkflowNotDispatched, "the dispatch timeout stays reachable")
	assert.Equal(t, behaviourfail.Scenario, failclass.Classify(scenarioErr), "the input error is not changed")

	summary := readFailureSummary(t, artifactDir)
	assert.Contains(t, summary, "no matching run exists, which suggests the event produced no run")
	assert.Contains(t, summary, "classification: infrastructure")

	// The suite records the returned error; the retry reruns the scenario.
	rec := &behaviourfail.Recorder{}
	rec.RecordScenarioFailure(w.ScenarioURI, w.ScenarioName, failclass.Classify(got), got.Error())
	report := rec.Report(behaviourfail.Infrastructure)
	require.Equal(t, behaviourfail.Infrastructure, report.Class)
	data, err := json.Marshal(report)
	require.NoError(t, err)
	reportPath := filepath.Join(t.TempDir(), "failure-class.json")
	require.NoError(t, os.WriteFile(reportPath, data, 0o600))
	fsys := fstest.MapFS{"features/dispatch/owners-auth.feature": {Data: []byte(`Feature: OWNERS

  Scenario: OWNERS approver is authorized when opted in
    Given the enrolled test repository

  Scenario: OWNERS approver is not authorized when not opted in
    Given the enrolled test repository
`)}}
	sel, err := retryselect.Select(fsys, []string{"features"}, reportPath)
	require.NoError(t, err)
	assert.True(t, sel.Targeted)
	assert.Equal(t, []string{"features/dispatch/owners-auth.feature:6"}, sel.Paths)
}

// TestCollectFailureLogs_FreshRepoNoRunMarkIsNarrow covers the cases
// where the evidence does not show that the workflow never reacted to an
// event in a repo installed for the scenario: the error stays unmarked.
func TestCollectFailureLogs_FreshRepoNoRunMarkIsNarrow(t *testing.T) {
	setupPush := forge.WorkflowRun{
		ID: 4100, Name: "setup", Event: "push", Status: "completed", Conclusion: "success",
		CreatedAt: "2026-11-22T01:00:20Z", Path: ".github/workflows/setup.yaml",
	}
	tests := map[string]struct {
		runs      []forge.WorkflowRun
		grace     time.Duration
		mutate    func(*world.World)
		err       error
		wantInfra bool
	}{
		"unrelated workflow ran: still marked": {
			runs:      []forge.WorkflowRun{setupPush},
			wantInfra: true,
		},
		"not freshly installed": {
			mutate: func(w *world.World) { w.RepoFreshlyInstalled = false },
		},
		"diagnosed repo is not the leased one": {
			mutate: func(w *world.World) { w.RepoName = "other-repo" },
		},
		"workflow ran for another event": {
			runs: []forge.WorkflowRun{{
				ID: 4200, Name: "fullsend", Event: "issue_comment", Status: "completed",
				CreatedAt: "2026-11-22T01:01:00Z", Path: ".github/workflows/fullsend.yaml",
			}},
		},
		"run with an unknown workflow file": {
			runs: []forge.WorkflowRun{{ID: 4300, Name: "fullsend", Event: "push", CreatedAt: "2026-11-22T01:01:00Z"}},
		},
		"late run": {
			runs: []forge.WorkflowRun{{
				ID: 4400, Name: "fullsend", Event: "issues", Status: "queued",
				CreatedAt: "2026-11-22T01:06:30Z", Path: ".github/workflows/fullsend.yaml",
			}},
		},
		"no grace wait": {
			grace: -1,
		},
		"joined with a scenario cause": {
			err: errors.Join(notDispatchedErr(), errors.New("expected label ready-to-code, got none")),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			grace := 2 * time.Minute
			if tc.grace != 0 {
				grace = tc.grace
			}
			shortenDispatchGrace(t, grace)
			fakeDispatchClock(t, time.Date(2026, 11, 22, 1, 5, 44, 0, time.UTC), nil)
			t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())

			fake := forge.NewFakeClient()
			fake.RecentWorkflowRuns = map[string][]forge.WorkflowRun{freshOrg + "/" + freshRepo: tc.runs}
			w := freshRepoWorld(fake)
			if tc.mutate != nil {
				tc.mutate(w)
			}
			scenarioErr := tc.err
			if scenarioErr == nil {
				scenarioErr = notDispatchedErr()
			}

			got := CollectFailureLogs(context.Background(), w, scenarioErr)
			if tc.wantInfra {
				assert.Equal(t, behaviourfail.Infrastructure, failclass.Classify(got))
				return
			}
			assert.Same(t, scenarioErr, got, "the error is returned unchanged")
			assert.Equal(t, behaviourfail.Scenario, failclass.Classify(got))
		})
	}
}

func TestCollectFailureLogs_ReturnsErrorUnchangedWithoutDiagnostics(t *testing.T) {
	scenarioErr := fmt.Errorf("wrapped: %w", notDispatchedErr())

	assert.Same(t, scenarioErr, CollectFailureLogs(context.Background(), nil, scenarioErr), "nil World")

	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", "")
	w := freshRepoWorld(forge.NewFakeClient())
	assert.Same(t, scenarioErr, CollectFailureLogs(context.Background(), w, scenarioErr), "no artifact dir, no diagnostics")

	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())
	other := errors.New("issue 42 missing label")
	assert.Same(t, other, CollectFailureLogs(context.Background(), w, other), "not a dispatch timeout")
}
