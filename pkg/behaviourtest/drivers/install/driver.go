package install

import "context"

// Driver provisions and tears down fullsend in an acquired pool org.
// Driver is used only during suite setup (single-threaded) and is not
// shared across concurrent scenarios.
type Driver interface {
	Install(ctx context.Context, org string) (State, error)
	Teardown(ctx context.Context, org string, state State) error
}

// State describes where behaviour tests find fullsend configuration after install.
//
// Concurrency: the perRepoState implementation is a read-only snapshot
// whose fields (org, repo) are set at construction and never modified.
// All accessor methods return derived constants. Sharing a single State
// across goroutines via World.Clone is safe by design for
// GODOG_CONCURRENCY>1. TestConcurrentStateAccess in this package
// exercises concurrent reads under -race.
//
// If a future implementation adds mutable state, it must synchronize
// access or be deep-copied per scenario in World.Clone.
type State interface {
	Mode() string
	TestRepo() string
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
