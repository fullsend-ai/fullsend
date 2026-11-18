// Package forgejoactions is the Forgejo Actions ci.Driver for behaviour
// tests.
//
// It is a minimal stub: every operation returns an error that satisfies
// errors.Is(err, drivers.ErrNotImplemented) and names the operation. It
// never delegates to the GitHub Actions or GitLab CI drivers, and it does
// not implement the optional ci.RunLister, ci.EmptyMatrixWaiter, or
// ci.AgentArtifactDownloader extensions. The real driver is #9651.
package forgejoactions

import (
	"context"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
)

// driverName labels this driver in not-implemented errors.
const driverName = "forgejoactions CI"

// Driver implements ci.Driver for Forgejo Actions. Client and Token are
// set at construction and never modified, so it is safe for concurrent
// use.
type Driver struct {
	// Client is the forge client the driver was built with. The stub
	// does not use it.
	Client forge.Client
	// Token is the credential the driver was built with. The stub does
	// not use it.
	Token string
}

var _ ci.Driver = (*Driver)(nil)

// New returns the Forgejo Actions CI driver.
func New(client forge.Client, token string) ci.Driver {
	return &Driver{Client: client, Token: token}
}

func notImplemented(operation string) error {
	return drivers.NotImplemented(driverName, operation)
}

func (d *Driver) WaitForWorkflow(context.Context, string, string, string, time.Time, string) (*forge.WorkflowRun, error) {
	return nil, notImplemented("WaitForWorkflow")
}

func (d *Driver) FindCompletedWorkflowRun(context.Context, string, string, string, time.Time) (*forge.WorkflowRun, error) {
	return nil, notImplemented("FindCompletedWorkflowRun")
}

func (d *Driver) AssertNoWorkflow(context.Context, string, string, string, time.Time) error {
	return notImplemented("AssertNoWorkflow")
}

func (d *Driver) GetRunLogs(context.Context, string, string, int) (string, error) {
	return "", notImplemented("GetRunLogs")
}

func (d *Driver) DownloadArtifacts(context.Context, string, string, int, string) error {
	return notImplemented("DownloadArtifacts")
}

func (d *Driver) DownloadNamedArtifactFromRun(context.Context, string, string, int, string, string) error {
	return notImplemented("DownloadNamedArtifactFromRun")
}

func (d *Driver) DownloadNamedArtifactAfter(context.Context, string, string, string, time.Time, string) error {
	return notImplemented("DownloadNamedArtifactAfter")
}

func (d *Driver) WaitForHarnessAgent(context.Context, string, string, string, time.Time) (*forge.WorkflowRun, error) {
	return nil, notImplemented("WaitForHarnessAgent")
}

func (d *Driver) WaitForHarnessAgentRound(context.Context, string, string, string, time.Time, map[int]bool) (*forge.WorkflowRun, error) {
	return nil, notImplemented("WaitForHarnessAgentRound")
}

func (d *Driver) WaitForFailedHarnessAgent(context.Context, string, string, string, time.Time) (*forge.WorkflowRun, error) {
	return nil, notImplemented("WaitForFailedHarnessAgent")
}

func (d *Driver) AssertNoHarnessAgentArtifact(context.Context, string, string, string, time.Time) error {
	return notImplemented("AssertNoHarnessAgentArtifact")
}

func (d *Driver) CountHarnessDispatches(context.Context, string, string, string, time.Time) (int, error) {
	return 0, notImplemented("CountHarnessDispatches")
}
