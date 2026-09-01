package install

import (
	"context"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// mintDriver provisions and tears down fullsend in an acquired pool org.
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
// All driver-specific inputs (PEMs, allowlists, mint URL, pool size)
// are read from env or computed from the org inside the factory.
// Runtime dependencies (forge client, token, CLI binary, GCP project,
// logger) are passed as parameters.
type Factory func(
	org string,
	client forge.Client,
	token, binary, gcpProjectID string,
	logf func(string, ...any),
) (Driver, error)

// Driver owns mint/environment lifecycle and test-repo allocation for
// behaviour tests. The suite constructs exactly one Driver via a Factory
// and threads it through World; scenarios call AllocateRepo to lease a
// ready repo and DeallocateRepo to return it. Finalize tears down
// suite-scoped resources and reclaims any outstanding leases.
//
// Implementations must be safe for concurrent use by multiple godog
// scenarios (GODOG_CONCURRENCY > 1).
type Driver interface {
	// AllocateRepo leases a slot and makes that repo ready (create if
	// missing, install if needed). Blocks until a slot is free or ctx
	// is cancelled. Returns the repo name only (org is fixed for the
	// driver / World).
	AllocateRepo(ctx context.Context) (repoName string, err error)

	// DeallocateRepo returns a previously allocated repo. Errors on
	// unknown name or double-release.
	DeallocateRepo(ctx context.Context, repoName string) error

	// Finalize always tears down suite-scoped resources (e.g. preview
	// mint). If leases are still outstanding, it reclaims them (logging
	// the names), completes teardown, and returns an error so leaked
	// After-hooks fail CI without stranding resources.
	Finalize(ctx context.Context) error

	// Capacity is the max concurrent outstanding allocations (the
	// driver's real parallelism ceiling). Suite may default concurrency
	// to Capacity() or honor GODOG_CONCURRENCY. If concurrency exceeds
	// Capacity(), excess workers block in AllocateRepo — the suite
	// emits an advisory warning but does not fail.
	Capacity() int
}

// CLIRunnerFunc is the signature for running a fullsend CLI command.
// The default implementation is e2etest.TryRunCLI. Inject a custom
// function in tests to avoid shelling out.
type CLIRunnerFunc func(binary, token string, args ...string) (string, error)

const (
	// PerRepoTriageWorkflow is the workflow path for per-repo triage.
	PerRepoTriageWorkflow = "fullsend.yaml"

	// PerRepoAgentWorkflow is the reusable workflow for the triage agent.
	PerRepoAgentWorkflow = "reusable-triage.yml"

	// PerRepoAgentArtifact is the upload-artifact name for triage output.
	PerRepoAgentArtifact = "fullsend-triage"

	// DefaultPoolSize is the default number of concurrent ephemeral repo
	// slots. Drivers use this as the default capacity when no override is set.
	DefaultPoolSize = 12
)
