package ci

import (
	"context"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Driver abstracts CI workflow operations for behaviour tests.
//
// Concurrency: the githubactions.Driver and gitlabci.Driver
// implementations are immutable wrappers around forge.Client (which is
// itself safe for concurrent use) and hold no unsynchronized mutable
// fields (Client and Token are both set at construction and never
// modified). Sharing a single Driver across goroutines via World.Clone
// is safe by design for GODOG_CONCURRENCY>1. TestConcurrentAccess in
// packages githubactions and gitlabci exercises the real drivers under
// -race with a FakeClient.
//
// If a future implementation adds mutable state (caches, counters,
// buffers), it must synchronize access or be deep-copied per scenario
// in World.Clone.
type Driver interface {
	WaitForWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time, event string) (*forge.WorkflowRun, error)
	FindCompletedWorkflowRun(ctx context.Context, owner, repo, workflowFile string, after time.Time) (*forge.WorkflowRun, error)
	AssertNoWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time) error
	GetRunLogs(ctx context.Context, owner, repo string, runID int) (string, error)
	DownloadArtifacts(ctx context.Context, owner, repo string, runID int, destDir string) error
	DownloadNamedArtifactFromRun(ctx context.Context, owner, repo string, runID int, artifactName string, destDir string) error
	DownloadNamedArtifactAfter(ctx context.Context, owner, repo, artifactName string, after time.Time, destDir string) error
	// WaitForHarnessAgent waits for the named agent's harness run to
	// succeed. When the run concludes with a failure, the error is
	// returned together with that run, so callers can collect its logs
	// before the scenario's repository is torn down; on a timeout or
	// context error the run is nil.
	WaitForHarnessAgent(ctx context.Context, owner, repo, agent string, after time.Time) (*forge.WorkflowRun, error)
	// WaitForHarnessAgentRound is like WaitForHarnessAgent, but for
	// scenarios where the same agent's harness is dispatched more than
	// once (e.g. dummy-playback's review round, retried after fix). It
	// selects the earliest eligible successful run whose ID is not in
	// consumed, rather than WaitForHarnessAgent's latest-eligible-run
	// selection: that selection exists for dual-dispatch resilience when
	// a single occurrence is expected, but it picks a later round's run
	// when an earlier round's own completion is asserted only after the
	// later round has also already finished, and advancing the caller's
	// time cursor afterward cannot repair that selection (#7957).
	WaitForHarnessAgentRound(ctx context.Context, owner, repo, agent string, after time.Time, consumed map[int]bool) (*forge.WorkflowRun, error)
	// WaitForFailedHarnessAgent waits for the named agent's harness run to
	// complete with a terminal failure conclusion (resolved artifact-first
	// via the agent's uploaded artifact, falling back to a job-name scan).
	// It errors out early when the run — or, in the fallback path, the
	// agent's own job — completes successfully instead.
	WaitForFailedHarnessAgent(ctx context.Context, owner, repo, agent string, after time.Time) (*forge.WorkflowRun, error)
	AssertNoHarnessAgentArtifact(ctx context.Context, owner, repo, agent string, after time.Time) error
	CountHarnessDispatches(ctx context.Context, owner, repo, agent string, after time.Time) (int, error)
}

// RunLister is an optional extension of Driver for listing a
// repository's most recent workflow runs across all workflows, newest
// first, returning at most limit runs. The suite uses it to collect the
// logs of every run in a failed scenario's repository before the lease
// ends — including runs no step got hold of (a wait that timed out, a
// failure before any run was resolved). It is a separate interface so
// that external Driver implementations keep compiling; a Driver that
// does not implement it still gets an explanatory failure summary in
// place of the missing logs.
type RunLister interface {
	ListRecentRuns(ctx context.Context, owner, repo string, limit int) ([]forge.WorkflowRun, error)
}
