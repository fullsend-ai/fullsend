package ci

import (
	"context"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Driver abstracts CI workflow operations for behaviour tests.
type Driver interface {
	WaitForWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time) (*forge.WorkflowRun, error)
	AssertNoWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time) error
	GetRunLogs(ctx context.Context, owner, repo string, runID int) (string, error)
	DownloadArtifacts(ctx context.Context, owner, repo string, runID int, destDir string) error
}
