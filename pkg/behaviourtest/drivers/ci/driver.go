package ci

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ErrWorkflowNotDispatched is wrapped by the GitHub Actions driver's
// WaitForWorkflow error (as a *NotDispatchedError) when no matching
// workflow run appeared within the dispatch window. The GitLab CI driver
// does not return it, so the dispatch diagnostics that key on it apply
// to GitHub Actions only. Callers detect it with errors.Is through any
// wraps they add. Its text completes the driver's "workflow <file>
// (<event>)" prefix, so the timeout message still reads "workflow <file>
// (<event>) was not dispatched". The timeout stays scenario-classified:
// whether the run arrives late or never is not yet established (#9469).
var ErrWorkflowNotDispatched = errors.New("was not dispatched")

// NotDispatchedError is the GitHub Actions driver's WaitForWorkflow
// timeout when no matching run appeared. It records the workflow file and
// event that were waited for, so failure diagnostics can look for exactly
// that run, and unwraps to ErrWorkflowNotDispatched.
type NotDispatchedError struct {
	// WorkflowFile is the base name of the workflow file, e.g.
	// "fullsend.yaml".
	WorkflowFile string
	// Event is the triggering event waited for, or empty for any event.
	Event string
	// Detail is appended verbatim to the message, e.g. the poll-error
	// summary " (failed on 3 of 40 polls ...)"; empty when every poll
	// succeeded.
	Detail string
}

func (e *NotDispatchedError) Error() string {
	if e.Event != "" {
		return fmt.Sprintf("workflow %s (%s) %v%s", e.WorkflowFile, e.Event, ErrWorkflowNotDispatched, e.Detail)
	}
	return fmt.Sprintf("workflow %s %v%s", e.WorkflowFile, ErrWorkflowNotDispatched, e.Detail)
}

// Unwrap lets errors.Is(err, ErrWorkflowNotDispatched) match.
func (e *NotDispatchedError) Unwrap() error { return ErrWorkflowNotDispatched }

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
	// DownloadNamedArtifactFromRun and DownloadNamedArtifactAfter match the
	// supplied physical artifact name exactly. Drivers that also implement
	// AgentArtifactDownloader can select output by logical agent instead;
	// see DownloadAgentArtifactsFromRun and DownloadAgentArtifactsAfter.
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

// EmptyMatrixWaiter is an optional extension of Driver for asserting
// that a harness dispatch decided to schedule nothing. Counting
// dispatches or checking for an agent job cannot tell that decision from
// a dispatch job that failed, or from a run that was never created, so
// denial scenarios use this to require a positive outcome. It is a
// separate interface so that external Driver implementations keep
// compiling; a scenario that uses the step on a Driver without it fails
// with an explanatory error.
type EmptyMatrixWaiter interface {
	// WaitForHarnessEmptyMatrix waits for at least one harness run
	// created at or after the given time, and for every such run that
	// was not cancelled to complete with its Harness dispatch job
	// succeeding and no harness agent job scheduled. It returns the first
	// such run, or an error when a run's dispatch job failed, a harness
	// agent job ran, or no run settled before the wait budget elapsed.
	WaitForHarnessEmptyMatrix(ctx context.Context, owner, repo string, after time.Time) (*forge.WorkflowRun, error)
}

// AgentArtifactDownloader is an optional extension of Driver for selecting
// artifacts by logical agent rather than by exact physical artifact name.
// A repeated agent can upload several platform-specific artifacts (for
// example one per matrix entry), and an implementation downloads every
// artifact that belongs to the agent. It is a separate interface so that
// external Driver implementations keep compiling; callers use the package
// helpers DownloadAgentArtifactsFromRun and DownloadAgentArtifactsAfter,
// which fall back to the exact-name Driver methods ("fullsend-<agent>")
// when a Driver does not implement it.
type AgentArtifactDownloader interface {
	DownloadAgentArtifactsFromRun(ctx context.Context, owner, repo string, runID int, agent string, destDir string) error
	DownloadAgentArtifactsAfter(ctx context.Context, owner, repo, agent string, after time.Time, destDir string) error
}

// DownloadAgentArtifactsFromRun downloads the artifacts of the logical agent
// from the given run using d's AgentArtifactDownloader capability when
// present, and otherwise falls back to d's exact-name download of
// "fullsend-<agent>".
func DownloadAgentArtifactsFromRun(ctx context.Context, d Driver, owner, repo string, runID int, agent, destDir string) error {
	if dl, ok := d.(AgentArtifactDownloader); ok {
		return dl.DownloadAgentArtifactsFromRun(ctx, owner, repo, runID, agent, destDir)
	}
	return d.DownloadNamedArtifactFromRun(ctx, owner, repo, runID, "fullsend-"+agent, destDir)
}

// DownloadAgentArtifactsAfter polls for the logical agent's artifacts created
// at or after the given time using d's AgentArtifactDownloader capability
// when present, and otherwise falls back to d's exact-name download of
// "fullsend-<agent>".
func DownloadAgentArtifactsAfter(ctx context.Context, d Driver, owner, repo, agent string, after time.Time, destDir string) error {
	if dl, ok := d.(AgentArtifactDownloader); ok {
		return dl.DownloadAgentArtifactsAfter(ctx, owner, repo, agent, after, destDir)
	}
	return d.DownloadNamedArtifactAfter(ctx, owner, repo, "fullsend-"+agent, after, destDir)
}
