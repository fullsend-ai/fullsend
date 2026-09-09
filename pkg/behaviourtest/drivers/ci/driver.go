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
	WaitForHarnessAgent(ctx context.Context, owner, repo, agent string, after time.Time) (*forge.WorkflowRun, error)
	// WaitForFailedHarnessAgent waits for the named agent's harness run to
	// complete with a terminal failure conclusion (resolved artifact-first
	// via the agent's uploaded artifact, falling back to a job-name scan).
	// It errors out early when the run — or, in the fallback path, the
	// agent's own job — completes successfully instead.
	WaitForFailedHarnessAgent(ctx context.Context, owner, repo, agent string, after time.Time) (*forge.WorkflowRun, error)
	AssertNoHarnessAgentArtifact(ctx context.Context, owner, repo, agent string, after time.Time) error
	CountHarnessDispatches(ctx context.Context, owner, repo, agent string, after time.Time) (int, error)
}
