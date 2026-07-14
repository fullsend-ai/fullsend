package install

import "context"

// Driver provisions and tears down fullsend in an acquired pool org.
type Driver interface {
	Install(ctx context.Context, org string) (State, error)
	Teardown(ctx context.Context, org string, state State) error
}

// State describes where behaviour tests find fullsend configuration after install.
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
