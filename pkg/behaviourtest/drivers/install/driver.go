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
// ready repo and DeallocateRepo to delete it and return the name.
// Finalize tears down suite-scoped resources and reclaims any
// outstanding leases.
//
// Implementations must be safe for concurrent use by multiple godog
// scenarios (GODOG_CONCURRENCY > 1).
type Driver interface {
	// AllocateRepo leases a slot and makes that repo ready (delete and
	// recreate if it already exists, then install). Blocks until a slot
	// is free or ctx is cancelled. Returns the repo name only (org is
	// fixed for the driver / World).
	AllocateRepo(ctx context.Context) (repoName string, err error)

	// DeallocateRepo deletes the leased repository (best-effort) and
	// returns the name to the pool. Errors on unknown name or
	// double-release. Called after scenario cleanup and debug
	// collection so leftover base-repo state cannot leak to the next
	// lessee.
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

// MintURLReporter is an optional Driver interface reporting the URL of
// the mint the driver deployed or was configured with. It is separate
// from Driver so external Driver implementations keep compiling; steps
// that need the mint address (the CLI mint status scenarios) fail with
// an explanatory error on a driver without it.
type MintURLReporter interface {
	// MintURL returns the suite's mint URL, or "" when none is known.
	MintURL() string
}

// CLIRunnerFunc is the signature for running a fullsend CLI command.
// The default implementation is e2etest.TryRunCLI. Inject a custom
// function in tests to avoid shelling out.
type CLIRunnerFunc func(binary, token string, args ...string) (string, error)

const (
	// PerRepoTriageWorkflow is the workflow path for per-repo triage.
	PerRepoTriageWorkflow = "fullsend.yaml"

	// PerRepoAgentWorkflow is the per-repo shim workflow that runs the agent.
	PerRepoAgentWorkflow = "fullsend.yaml"

	// PerRepoAgentArtifact is the upload-artifact name for triage output.
	PerRepoAgentArtifact = "fullsend-triage"

	// DefaultPoolSize is the number of test-repo-NN repos in a pool org.
	// Drivers use this as the default capacity when no override is set.
	DefaultPoolSize = 12

	// MintProbeHostRepoSuffix names the per-slot repository that hosts
	// the reusable mint-probe workflow of the GitHub mint scope
	// scenarios: test-repo-NN + suffix. The CF mint drivers list these
	// names in the mint's WORKFLOW_HOST_REPOS next to the pool repos, so
	// a workflow hosted there is an approved workflow host while staying
	// a different repository from the leased test-repo-NN that calls it.
	// The repositories themselves are created and deleted per scenario.
	MintProbeHostRepoSuffix = "-mint-host"

	// MintStatusTeamSlug is the slug of the team in each pool org (and
	// the STAGE org) whose members may call the mint's /v1/status with
	// a GitHub user token. The CF mint drivers deploy their mint with
	// --status-auth=oidc,github --status-github-group=ORG/<slug>; the
	// team itself is created by hack/setup-new-e2e-org.sh.
	MintStatusTeamSlug = "mint-status"
)

// mintStatusGitHubGroup returns the --status-github-group value for a
// pool org's mint: ORG/MintStatusTeamSlug.
func mintStatusGitHubGroup(org string) string {
	return org + "/" + MintStatusTeamSlug
}
