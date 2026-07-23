package ci

import (
	"context"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Driver abstracts CI workflow operations for behaviour tests.
//
// Concurrency: the githubactions.Driver implementation is an immutable
// wrapper around forge.Client (which is itself safe for concurrent use)
// and holds no unsynchronized mutable fields (Client and Token are both
// set at construction and never modified). Sharing a single Driver
// across goroutines via World.Clone is safe by design for
// GODOG_CONCURRENCY>1. TestConcurrentAccess in package githubactions
// exercises the real driver under -race with a FakeClient.
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
	AssertNoHarnessAgentArtifact(ctx context.Context, owner, repo, agent string, after time.Time) error
	CountHarnessDispatches(ctx context.Context, owner, repo, agent string, after time.Time) (int, error)
}
