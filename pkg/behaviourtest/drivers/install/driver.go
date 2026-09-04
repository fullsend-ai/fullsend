package install

import (
	"context"
	"os"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// mintDriver provisions and tears down a preview mint for behaviour tests.
// Used only during suite setup (single-threaded) and not shared across
// concurrent scenarios.
//
// This is an unexported interface used internally by concrete driver
// implementations. The suite does not construct or reference it directly.
type mintDriver interface {
	// Install deploys the mint for the given org and returns the mint URL.
	Install(ctx context.Context, org string) (mintURL string, err error)

	// Teardown tears down suite-scoped mint resources. The driver owns
	// its own state (e.g. preview alias) — no external state is needed.
	Teardown(ctx context.Context) error
}

// Factory constructs a unified Driver for a given org. The factory
// performs suite setup (e.g. preview mint deploy) before returning so
// setup failures fail the suite before scenarios run.
//
// All driver-specific inputs (PEMs, allowlists, mint URL)
// are read from env or computed from the org inside the factory.
// Runtime dependencies (forge client, token, CLI binary, GCP project,
// logger) are passed as parameters.
type Factory func(
	org string,
	client forge.Client,
	token, binary, gcpProjectID string,
	logf func(string, ...any),
) (Driver, error)

// Driver owns mint/environment lifecycle and test-repo creation for
// behaviour tests. The suite constructs exactly one Driver via a Factory
// and threads it through World; scenarios call CreateRepo to provision an
// ephemeral repo. Repo deletion is handled by CleanupScenario (in the
// steps package); the driver tracks created repos so Finalize can sweep
// any that were not cleaned up (e.g. when a CI job is cancelled).
//
// Implementations must be safe for concurrent use by multiple godog
// scenarios (GODOG_CONCURRENCY > 1).
type Driver interface {
	// CreateRepo creates a fresh ephemeral repo for a scenario. The hint
	// (typically the scenario name) is incorporated into the repo name
	// for traceability. Returns the repo name only (org is fixed for the
	// driver / World).
	CreateRepo(ctx context.Context, hint string) (repoName string, err error)

	// MarkDeleted removes a repo from the driver's tracking list after
	// it has been successfully deleted by CleanupScenario.
	MarkDeleted(repoName string)

	// Finalize deletes any repos still on the tracking list (handles
	// cancelled runs), then tears down suite-scoped resources (e.g.
	// preview mint). Respects E2E_KEEP_REPOS.
	Finalize(ctx context.Context) error

	// DefaultConcurrency returns the suggested concurrency for the test
	// suite. The suite may use GODOG_CONCURRENCY instead.
	DefaultConcurrency() int
}

// CLIRunnerFunc is the signature for running a fullsend CLI command.
// The default implementation is e2etest.TryRunCLI. Inject a custom
// function in tests to avoid shelling out.
type CLIRunnerFunc func(binary, token string, args ...string) (string, error)

const (
	// TriageWorkflow is the workflow file name for triage.
	TriageWorkflow = "fullsend.yaml"

	// AgentWorkflow is the reusable workflow for the triage agent.
	AgentWorkflow = "reusable-triage.yml"

	// AgentArtifact is the upload-artifact name for triage output.
	AgentArtifact = "fullsend-triage"

	// DefaultConcurrencyValue is the default number of concurrent
	// scenarios when GODOG_CONCURRENCY is not set.
	DefaultConcurrencyValue = 20
)

// KeepRepos reports whether test repos should be preserved after runs.
// TODO: revert to `os.Getenv("E2E_KEEP_REPOS") == "true"` after debugging flaky url-dispatch failure.
func KeepRepos() bool {
	return true || os.Getenv("E2E_KEEP_REPOS") == "true"
}
