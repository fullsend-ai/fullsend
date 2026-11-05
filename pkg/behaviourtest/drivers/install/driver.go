package install

import (
	"context"
	"errors"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// mintDriver deploys and tears down a mint serving pool orgs. The
// shared driver deploys one per mint group (see mintPlan), the first
// time a slot in any of the group's orgs is leased, and tears them all
// down at Finalize.
//
// This is an unexported interface used internally by concrete driver
// implementations. The suite does not construct or reference it directly.
type mintDriver interface {
	// Install deploys the mint and returns the mint URL.
	Install(ctx context.Context) (mintURL string, err error)

	// Teardown tears down suite-scoped mint resources. The driver owns
	// its own state (e.g. preview alias) — no external state is needed.
	Teardown(ctx context.Context) error
}

// OrgTokenFunc returns an installation token for org: the e2e GitHub App
// installation token minted for that org in CI, or the developer's own
// token locally. It is called at most once per org per suite (see
// PoolConfig.TokenFor).
type OrgTokenFunc func(ctx context.Context, org string) (string, error)

// PoolConfig is the input of a Factory: the pool to lease from and the
// suite's runtime dependencies.
type PoolConfig struct {
	// Layout is the pool's orgs and slots (see PoolLayoutForEnvironment).
	Layout PoolLayout
	// TokenFor returns an org's installation token. The driver calls it
	// once per org and keeps the token for the rest of the suite: an
	// installation token lives an hour, longer than the suite's job time
	// limit (slotJobTimeLimit), so a token minted at any point of the run
	// is still valid at its end.
	TokenFor OrgTokenFunc
	// NewClient builds a forge client from a token (e.g.
	// e2etest.NewLiveClient). One client is built per org, so the
	// allocator's per-org rate-limit gate also sees the requests the
	// org's scenarios make with it.
	NewClient func(token string) forge.Client
	// Binary is the fullsend CLI built for the suite.
	Binary string
	// GCPProjectID is the inference GCP project ("" skips inference).
	GCPProjectID string
	// Logf is the suite logger.
	Logf func(string, ...any)
}

func (p PoolConfig) validate() error {
	if err := p.Layout.validate(); err != nil {
		return err
	}
	if p.TokenFor == nil {
		return errors.New("pool config: TokenFor must not be nil")
	}
	if p.NewClient == nil {
		return errors.New("pool config: NewClient must not be nil")
	}
	return nil
}

// Factory constructs the suite's shared Driver over a pool of
// (org, repo-name) slots. Driver-specific inputs (PEMs, allowlists,
// mint URL) are read from env or computed per org; each org's mint is
// deployed the first time a slot in it is leased. Configuration a
// factory can check without deploying (e.g. PEMs present) fails the
// factory, before scenarios run.
type Factory func(pool PoolConfig) (Driver, error)

// RepoLease is one leased pool slot and everything a scenario needs to
// operate on it: the slot's org and repo, that org's installation token
// and the forge client built from it, and the URL of the mint serving
// the org. A scenario's World, SCM and CI drivers, mint configuration,
// and cleanup all use this org.
type RepoLease struct {
	Org     string
	Repo    string
	Token   string
	Client  forge.Client
	MintURL string

	// MintStatusGroup is the ORG/TEAM the mint was deployed with as
	// --status-github-group: one pool org's mint-status team, picked
	// once per job and shared by every mint. It need not be the lease's
	// org. Empty when the driver does not configure status auth.
	MintStatusGroup string
	// MintWorkflowHostRepos is the mint's WORKFLOW_HOST_REPOS: every
	// workflow host of the mint group serving the lease's org. Nil when
	// the driver does not configure the mint's workflow hosts.
	MintWorkflowHostRepos []string
}

// LeaseDriver is a Driver that leases slots across pool orgs. Callers
// that know about it use AllocateLease and DeallocateLease, which carry
// the slot's org; the shared pool driver returns an error from
// AllocateRepo, since a repo name alone does not identify a slot.
type LeaseDriver interface {
	Driver

	// AllocateLease claims a free (org, repo) slot through the slot
	// allocator, deploys the org's mint if this is the org's first
	// lease, and installs fullsend into the slot repo the claim created.
	// Blocks until a slot is free or ctx is cancelled. A slot whose
	// install fails is released before the error is returned.
	AllocateLease(ctx context.Context) (RepoLease, error)

	// DeallocateLease releases a slot returned by AllocateLease: it
	// removes leftover per-slot state, then deletes the slot repo if it
	// is still this lease's (another holder's repository under the same
	// name is never deleted) and frees the slot. Errors on an unknown
	// slot or a double release. Called after scenario cleanup and debug
	// collection.
	DeallocateLease(ctx context.Context, org, repo string) error
}

// LeaseVerifier is implemented by a LeaseDriver that can tell whether a
// slot it leased is still its caller's. Scenario cleanup mutates the
// repository and auxiliary repositories by name, and the name may
// have been taken over by another holder (for example after GC deleted
// a slow lessee's repo), so cleanup asks before each mutation and skips
// it when the answer is no or cannot be determined.
type LeaseVerifier interface {
	// StillOwnsLease reports whether the repository currently under
	// (org, repo) is still the one this driver's lease claimed. It
	// changes no state. false means the repo is gone, was replaced, or
	// the lease is not outstanding; an error means ownership could not
	// be verified, which callers treat as not owned.
	StillOwnsLease(ctx context.Context, org, repo string) (bool, error)
}

// Driver owns mint/environment lifecycle and test-repo allocation for
// behaviour tests. The suite constructs exactly one Driver via a Factory
// and threads it through World; scenarios lease a ready repo and
// release it after cleanup. Finalize tears down suite-scoped resources
// and reclaims any outstanding leases.
//
// Implementations must be safe for concurrent use by multiple godog
// scenarios (GODOG_CONCURRENCY > 1).
type Driver interface {
	// AllocateRepo leases a repo in the driver's single org and makes it
	// ready. Drivers that lease across orgs implement LeaseDriver and
	// fail this call; use AllocateLease on them.
	AllocateRepo(ctx context.Context) (repoName string, err error)

	// DeallocateRepo releases a repo returned by AllocateRepo. Errors on
	// unknown name or double-release.
	DeallocateRepo(ctx context.Context, repoName string) error

	// Finalize always tears down suite-scoped resources (e.g. preview
	// mint). If leases are still outstanding, it reclaims them (logging
	// the names), completes teardown, and returns an error so leaked
	// After-hooks fail CI without stranding resources.
	Finalize(ctx context.Context) error

	// Capacity is the max concurrent outstanding allocations (the
	// driver's real parallelism ceiling: every slot of the pool). Godog
	// concurrency is controlled separately by GODOG_CONCURRENCY. If
	// concurrency exceeds Capacity(), excess workers block while
	// allocating — the suite emits an advisory warning but does not fail.
	Capacity() int
}

// MintURLReporter is an optional Driver interface reporting the URL of
// the mint the driver was configured with. It is separate from Driver
// so external Driver implementations keep compiling. A LeaseDriver
// reports each lease's mint in RepoLease.MintURL instead, since every
// org has its own mint; steps that need the mint address (the CLI mint
// status scenarios) use the lease's and fall back to this interface.
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
