package install

import "context"

// MintDriver provisions and tears down fullsend in an acquired pool org.
// MintDriver is used only during suite setup (single-threaded) and is not
// shared across concurrent scenarios.
//
// Renamed from Driver so that the Driver name can refer to the unified
// repo-allocation interface (#6135).
type MintDriver interface {
	Install(ctx context.Context, org string) (State, error)
	Teardown(ctx context.Context, org string, state State) error
}

// Driver is the unified repo-allocation interface that suite and
// scenario lifecycle code speaks to. It combines mint/environment
// lifecycle, repo-name leasing, and lazy repo creation behind a
// single surface. Implementations must be safe for concurrent use.
//
// See #6135 for the full contract.
type Driver interface {
	// AllocateRepo leases a slot and makes that repo ready. It blocks
	// until a slot is free or ctx is cancelled. Returns repo name only
	// (org is fixed for the driver).
	AllocateRepo(ctx context.Context) (repoName string, err error)

	// DeallocateRepo returns a previously allocated repo. Errors on
	// unknown name or double-release.
	DeallocateRepo(ctx context.Context, repoName string) error

	// Finalize tears down suite-scoped resources. If leases are still
	// outstanding, it reclaims them (logging the names), completes
	// teardown, and returns an error so leaked After-hooks fail CI
	// without stranding resources.
	Finalize(ctx context.Context) error

	// Capacity returns the max concurrent outstanding allocations.
	Capacity() int
}

// State describes where behaviour tests find fullsend configuration after install.
//
// Concurrency: the PerRepoState implementation is a read-only snapshot
// whose fields (org, repo, mintURL) are set at construction and never modified.
// All accessor methods return derived constants. Sharing a single State
// across goroutines via World.Clone is safe by design for
// GODOG_CONCURRENCY>1. TestConcurrentStateAccess in this package
// exercises concurrent reads under -race.
//
// If a future implementation adds mutable state, it must synchronize
// access or be deep-copied per scenario in World.Clone.
type State interface {
	Mode() string
	// ConfigOwner and ConfigRepo locate commits for behaviour scripts and config reads.
	ConfigOwner() string
	ConfigRepo() string
	// ConfigPathPrefix is "" for per-org (.fullsend repo root) or ".fullsend" for per-repo.
	ConfigPathPrefix() string
	// TriageWorkflowRepo is the repository polled for triage workflow runs.
	TriageWorkflowRepo() string
	// TriageWorkflowFile is the workflow path passed to ListWorkflowRuns.
	TriageWorkflowFile() string
	// AgentWorkflowFile is the reusable workflow that runs the agent and uploads artifacts.
	AgentWorkflowFile() string
	// AgentArtifactName is the upload-artifact name for triage agent output.
	AgentArtifactName() string
}

// MintURLProvider is optionally implemented by State values that carry
// the effective mint URL. The composed driver uses this to thread the
// mint URL from the MintDriver install output to the internal
// RepoEnsurer.
type MintURLProvider interface {
	MintURL() string
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
)
