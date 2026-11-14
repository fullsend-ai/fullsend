package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	scmgithub "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// TestCollectFailureLogs_DispatchSimulation drives the real
// CollectFailureLogs path, with the real GitHub Actions CI driver and
// GitHub SCM driver over a fake forge, for the two outcomes the dispatch
// diagnostics exist to tell apart (#9469). The real CI driver is used
// for listing runs only: the test does not run the scenario's wait, it
// constructs the NotDispatchedError that wait would return.
//
//   - late: the issue exists, no run existed while the scenario waited,
//     and a matching fullsend.yaml (issues) run is created during the
//     grace period;
//   - none: the issue exists and no run is created at all.
//
// In a real run drainIssueOpenWorkflow waits about 10 minutes (two
// 5-minute waits, the second with a 30s skew buffer). The fixture times
// are simpler: the issue is created 5 minutes before the wait ends, so
// the printed gaps are shorter than a real run's would be.
//
// The grace wait uses a simulated clock, so the test does not sleep.
// Set DISPATCH_SIM_KEEP_DIR to a directory to keep each generated
// failure-summary.txt there as sim-summary-<case>.txt.
func TestCollectFailureLogs_DispatchSimulation(t *testing.T) {
	const (
		org, repo    = "halfsend-22", "test-repo-03"
		issueNumber  = 7
		issueCreated = "2026-11-14T00:05:58Z"
	)
	// Runner clock when the scenario's wait gave up (the grace wait
	// starts here); the fixture puts it 5 minutes after the issue was
	// opened, not the ~10 minutes of a real drainIssueOpenWorkflow.
	waitEnd := time.Date(2026, 11, 14, 0, 10, 58, 0, time.UTC)

	tests := map[string]struct {
		lateRun *forge.WorkflowRun
		want    []string
	}{
		"late": {
			lateRun: &forge.WorkflowRun{
				ID: 4242, Name: "fullsend", Event: "issues", Status: "queued",
				CreatedAt: "2026-11-14T00:11:40Z", Path: ".github/workflows/fullsend.yaml",
			},
			want: []string{
				"listed at: 2026-11-14T00:12:58Z (runner clock), after a 2m0s grace period that began at 2026-11-14T00:10:58Z",
				"issue #7: created_at 2026-11-14T00:05:58Z, state open",
				`run 4242: workflow ".github/workflows/fullsend.yaml", event "issues", created_at 2026-11-14T00:11:40Z`,
				"run 4242: created_at 2026-11-14T00:11:40Z, +5m42s after the issue's created_at",
				"matching fullsend.yaml (issues) run created after the wait ended: yes (run 4242",
			},
		},
		"none": {
			want: []string{
				"listed at: 2026-11-14T00:12:58Z (runner clock), after a 2m0s grace period that began at 2026-11-14T00:10:58Z",
				"issue #7: created_at 2026-11-14T00:05:58Z, state open",
				"matching fullsend.yaml (issues) runs: none",
				"matching fullsend.yaml (issues) run created after the wait ended: no",
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			shortenDispatchGrace(t, 2*time.Minute)
			artifactDir := t.TempDir()
			t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)

			fake := forge.NewFakeClient()
			fake.OpenIssues = map[string][]forge.Issue{
				org + "/" + repo: {{Number: issueNumber, Title: "triage me", State: "open", CreatedAt: issueCreated}},
			}
			// Only the setup push run exists while the scenario waits.
			fake.RecentWorkflowRuns = map[string][]forge.WorkflowRun{
				org + "/" + repo: {{
					ID: 4100, Name: "setup", Event: "push", Status: "completed", Conclusion: "success",
					CreatedAt: "2026-11-14T00:04:12Z", Path: ".github/workflows/setup.yaml",
				}},
			}
			// The late run appears while the grace period runs.
			fakeDispatchClock(t, waitEnd, func() {
				if tc.lateRun != nil {
					key := org + "/" + repo
					fake.RecentWorkflowRuns[key] = append([]forge.WorkflowRun{*tc.lateRun}, fake.RecentWorkflowRuns[key]...)
				}
			})

			w := &world.World{
				Org: org, RepoName: repo, IssueNumber: issueNumber,
				ScenarioName: "OWNERS alias resolves to grant access",
				CI:           &githubactions.Driver{Client: fake},
				SCM:          scmgithub.New(fake),
				Logf:         func(string, ...any) {},
			}
			// Constructed by the test, not returned by a real wait: the
			// error WaitForWorkflow returns, wrapped as
			// drainIssueOpenWorkflow does.
			scenarioErr := fmt.Errorf("waiting for issue-open workflow: %w",
				&ci.NotDispatchedError{WorkflowFile: "fullsend.yaml", Event: "issues"})

			CollectFailureLogs(context.Background(), w, scenarioErr)

			summary := readFailureSummary(t, artifactDir)
			for _, want := range tc.want {
				assert.Contains(t, summary, want)
			}
			if keep := os.Getenv("DISPATCH_SIM_KEEP_DIR"); keep != "" {
				require.NoError(t, os.WriteFile(filepath.Join(keep, "sim-summary-"+name+".txt"), []byte(summary), 0o600))
			}
		})
	}
}
