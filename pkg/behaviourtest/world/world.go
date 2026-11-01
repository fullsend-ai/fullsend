package world

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"time"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/jiramock"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
)

// World holds scenario state and injected drivers.
type World struct {
	Config env.RunnerConfig
	SCM    scm.Driver
	CI     ci.Driver

	Org       string
	RepoFull  string
	RepoOwner string
	RepoName  string
	Token     string
	Logf      func(string, ...any)

	// FixturesRoot is module-relative (e.g. "e2e/behaviour" or "behaviour").
	FixturesRoot string

	ScenarioStart time.Time

	// ScenarioName is the godog scenario name, set by the Before hook.
	// ScenarioBegin is the time the Before hook ran; unlike
	// ScenarioStart, which trigger steps move forward, it never changes
	// during the scenario. Failure log collection uses both to label its
	// summary and to scope the repository's runs to this scenario.
	ScenarioName  string
	ScenarioBegin time.Time

	// SavedLogRunIDs records the workflow runs whose logs have already
	// been written under BEHAVIOUR_ARTIFACT_DIR during the scenario, so
	// failure log collection in the After hook does not fetch them again.
	SavedLogRunIDs map[int]bool

	DummyOps           []runtime.BehaviourOperation
	ArtifactDir        string
	TriageTriggerEvent string // GitHub event for triage dispatch (issues for label path)

	IssueNumber    int
	IssueTitle     string
	WorkflowRun    *forge.WorkflowRun
	TriageWorkflow string

	DispatchAgent string
	PRNumber      int

	// Fork context — set by fork step definitions.
	ForkOwner    string
	ForkRepo     string
	ForkPRNumber int
	ForkPRBranch string
	// ForkSCM is the driver of the test actor that owns the fork, set
	// when the fork was created "owned by the <actor>". Fork-side
	// operations (branch, commits, cleanup) go through it. Nil means
	// the fork belongs to the e2e identity behind SCM.
	ForkSCM scm.Driver

	// URL harness hosting repo — set by URL dispatch step definitions.
	URLHarnessRepoOwner string
	URLHarnessRepoName  string

	// URLBaseHarnesses maps base harness names to their raw URLs (with
	// integrity hash). Set by the "URL-sourced base harness" step and
	// consumed by steps that create child harnesses referencing a remote
	// base via URL.
	URLBaseHarnesses map[string]string

	// Branch-handling scenario state — set by branch step definitions.
	// RecordedBranchSHAs maps branch name → tip SHA captured before a
	// run so "branch X is unchanged" can re-check it afterwards.
	// CreatedBranches, CreatedPRNumbers and CreatedIssueNumbers track
	// resources the steps created (or discovered) so CleanupScenario can
	// remove them.
	// Isolation across Clone()d Worlds relies on the suite invariant
	// that resetScenarioWorld nils these after every clone and the
	// template World never populates them — do not set them on a
	// template.
	RecordedBranchSHAs map[string]string
	CreatedBranches    []string
	CreatedPRNumbers   []int
	// CreatedIssueNumbers holds issues an agent filed that a step
	// matched, closed at cleanup.
	CreatedIssueNumbers []int

	// LeasedRepoName is the logical test-repo name acquired via
	// Driver.AllocateRepo (or install.LeaseDriver.AllocateLease) for this
	// scenario's duration. Empty when no driver is configured.
	LeasedRepoName string

	// LeasedRepoOrg is the org of the slot leased through
	// install.LeaseDriver.AllocateLease; the After hook releases the
	// slot with it. Empty when the driver leases by repo name only.
	LeasedRepoOrg string

	// LeaseOwned reports whether the repository under the leased slot's
	// name is still this scenario's lease (install.LeaseVerifier). The
	// After hook sets it before scenario cleanup, which consults it before
	// every name-based mutation (and before every retry of one) and skips
	// the mutation when the lease is lost or ownership cannot be verified,
	// because the name may by then belong to another holder. Nil means
	// ownership is not tracked (no lease, or tests with bare worlds).
	LeaseOwned func(ctx context.Context) (bool, error)

	// MintURL is the URL of the mint serving the leased repository's org
	// (install.RepoLease.MintURL). Empty when the driver does not report
	// one per lease.
	MintURL string

	// MintStatusGroup is the ORG/TEAM the leased repository's mint was
	// deployed with as --status-github-group
	// (install.RepoLease.MintStatusGroup). It is chosen once per job and
	// need not be the leased org's team. Empty when the driver does not
	// report it.
	MintStatusGroup string

	// MintWorkflowHostRepos is the leased repository's mint's
	// WORKFLOW_HOST_REPOS: the workflow hosts of every org of its mint
	// group (install.RepoLease.MintWorkflowHostRepos). Nil when the
	// driver does not report them.
	MintWorkflowHostRepos []string

	// OrgDrivers builds the SCM and CI drivers for one org from that
	// org's forge client and token. When a scenario leases a slot, the
	// lease step rebinds SCM and CI through it so every operation uses
	// the leased org's credentials. Set by the suite runners on the
	// template World; nil keeps the template drivers.
	OrgDrivers func(client forge.Client, token string) (scm.Driver, ci.Driver, error)

	// Driver is the unified install driver that owns repo allocation,
	// deallocation, and suite-scoped teardown. Shared across scenarios
	// (like other driver fields) and safe for concurrent use.
	// Nil when no driver is configured.
	Driver install.Driver

	// Failures records classified suite failures — infrastructure vs
	// scenario — for the whole run. It is initialized once on the
	// template World (newTemplateWorld) so both suite runners get one
	// recorder per run, and shared by reference through Clone (a
	// shallow copy): every scenario clone appends to the same recorder.
	// Scenarios run concurrently, so the recorder is mutex-protected;
	// its methods are nil-receiver-safe, so hand-built Worlds without a
	// recorder keep working.
	Failures *behaviourfail.Recorder

	// OutsiderSCM is an SCM driver authenticated as fstest-outsider, a
	// GitHub User with no collaborator access. Used by the OWNERS
	// write-denial E2E scenario. Nil when TEST_ACTOR_OUTSIDER_PAT is
	// not set. Shared across scenarios, never reset.
	OutsiderSCM   scm.Driver
	OutsiderLogin string

	// WriteSCM is an SCM driver authenticated as a GitHub User with
	// write-level collaborator access. Used by OWNERS fallthrough
	// scenarios that need an actor passing the collaborator API but
	// not listed in OWNERS. Nil when TEST_ACTOR_WRITE_PAT is not set.
	WriteSCM   scm.Driver
	WriteLogin string
	// WriteToken is the write actor's PAT (TEST_ACTOR_WRITE_PAT). The
	// CLI mint status steps hand it to the fullsend CLI as GH_TOKEN so
	// the CLI's normal GitHub credential discovery picks it up. Empty
	// when the PAT is not set. Shared across scenarios, never reset;
	// never log it.
	WriteToken string

	// CLIBinary is the path of the fullsend CLI built for the suite.
	// Steps that exercise the CLI as an operator would run it. Empty
	// when the bootstrap did not build one.
	CLIBinary string

	// TriageSCM is an SCM driver authenticated as a GitHub User with
	// triage-level access to the pool-org repos. Used by scenarios that
	// act as the triage actor. Nil when TEST_ACTOR_TRIAGE_PAT is not
	// set. Shared across scenarios, never reset.
	TriageSCM   scm.Driver
	TriageLogin string

	// KillSwitchActivated records whether this scenario activated the
	// repo-level kill switch. CleanupScenario uses this to deactivate
	// the switch so the next scenario on this slot is not affected.
	KillSwitchActivated bool

	// KillSwitchBaseActivated records whether this scenario activated
	// the kill switch via .fullsend/config.base.yaml (the central/base
	// layer) rather than config.yaml. CleanupScenario uses this to
	// deactivate it so the next scenario on this slot is not affected.
	// Tracked separately from KillSwitchActivated because the two
	// files are independent.
	KillSwitchBaseActivated bool

	// RuntimeOverridden records that this scenario changed the repo's
	// `runtime:` in .fullsend/config.yaml; RuntimeOriginal is the value
	// to restore. CleanupScenario reverts it so the slot's next scenario
	// runs the install default (dummy) again.
	RuntimeOverridden bool
	RuntimeOriginal   string

	// AllowedResourcesOverridden records that this scenario modified
	// allowed_remote_resources in config.yaml; AllowedResourcesOriginal
	// holds the pre-scenario value. CleanupScenario restores it so the
	// next scenario on this slot does not inherit a leftover allowlist
	// entry — which would let an allowlist-negative scenario pass
	// incorrectly on a reused pool slot.
	AllowedResourcesOverridden bool
	AllowedResourcesOriginal   []string

	// AgentsOverridden records that this scenario modified the agents
	// list in config.yaml; AgentsOriginal holds the pre-scenario value.
	// CleanupScenario restores it so the next scenario on this slot
	// does not inherit custom agent entries from the previous lessee.
	AgentsOverridden bool
	AgentsOriginal   []config.AgentEntry

	// GatewayConfigOverridden records that this scenario committed an
	// inference.gateway block into .fullsend/config.yaml;
	// GatewayConfigOriginal holds the pre-scenario file bytes.
	// CleanupScenario commits them back, before the leased repo is
	// deallocated, so the slot's next scenario does not inherit the block.
	GatewayConfigOverridden bool
	GatewayConfigOriginal   []byte

	// GatewayAPIKeySecretSet records that this scenario set the enrolled
	// repo's FULLSEND_INFERENCE_GATEWAY_API_KEY secret to the test key.
	// CleanupScenario deletes it before the leased repo is deallocated.
	GatewayAPIKeySecretSet bool

	// OwnersAuthActivated records whether this scenario committed an
	// OWNERS file and/or added owners_file to the authorization providers in config.yaml.
	// CleanupScenario removes both.
	OwnersAuthActivated bool

	// Jira mock state — set by the "Given a mock Jira server" step.
	JiraMockServer *httptest.Server
	JiraMockState  *jiramock.State
	JiraConfigDir  string // temp dir holding .fullsend/ layout for the poller

	// PlaybackEntries accumulates the ordered list of canned-result
	// directories a playback scenario will serve, built up by "a <agent>
	// agent that returns <result>" steps before the triggering issue is
	// created. PlaybackCommitted records whether the playlist (and its
	// result fixtures) has already been committed to the leased repo,
	// so a scenario that creates more than one issue does not commit
	// the playlist twice. Both are only meaningful when IsPlaybackMode
	// is true.
	PlaybackEntries   []runtime.PlaybackEntry
	PlaybackCommitted bool

	// ConsumedHarnessRunIDs tracks, per agent, the harness-run IDs
	// already matched by a prior "the <agent> agent completes
	// successfully" step in this scenario. A playback scenario can
	// trigger the same agent's harness more than once (e.g. review is
	// retried after fix); WaitForHarnessAgentRound uses this set to
	// select the earliest eligible successful run not yet consumed
	// instead of re-matching (or skipping past) a round already recorded
	// here.
	ConsumedHarnessRunIDs map[string]map[int]bool

	// HarnessRunArtifactDirs caches each harness run's downloaded
	// artifact directory, keyed by run ID. A playback scenario dispatches
	// several stages in one scenario (triage, code, review, fix); unlike
	// ensureHarnessArtifacts (dispatch.go), which downloads once per
	// scenario into the single ArtifactDir for the single-dispatch case,
	// "the <agent> agent completes successfully" (playback.go) needs a
	// fresh download per round so a later stage's artifacts — and any
	// download failure — are not hidden behind an earlier stage's
	// already-populated ArtifactDir.
	HarnessRunArtifactDirs map[int]string

	// StagePublishedSHA pins, per stage name (the leading path segment of
	// a result fixture, e.g. "code" or "fix"), the pull request's head
	// commit SHA captured at the earliest moment that stage's publication
	// can be observed. "the published repository matches fixture" reads
	// this instead of re-resolving the branch's live tip at comparison
	// time: review and fix can progress automatically (dispatched by
	// forge webhooks) independently of how quickly this scenario's own
	// steps execute, so a later stage can advance the same branch before
	// an earlier stage's own assertion gets around to resolving it
	// (#7957 review). Currently populated only for the "code" stage, from
	// the pull request's first commit (immutable once later stages append
	// to the branch) rather than the branch's live tip; a stage with no
	// pinned entry falls back to live resolution.
	StagePublishedSHA map[string]string

	// MintProbe holds the GitHub mint-probe scenario state, set by the
	// mint probe steps (steps/mint_probe.go). Nil when the scenario does
	// not probe the mint. CleanupScenario deletes the repositories it
	// lists; the probe's shim branch is tracked in CreatedBranches.
	MintProbe *MintProbe

	// MintStatus holds the CLI mint status scenario state, set by the
	// mint status steps (steps/mint_status.go). Nil when the scenario
	// does not run `fullsend mint status`.
	MintStatus *MintStatus
	// BotRoleProbe holds the GitHub bot-role lookup scenario state, set by
	// the bot-role probe steps (steps/bot_role_probe.go). Nil when the
	// scenario does not look up bot roles. CleanupScenario deletes the
	// repositories it lists; the probe's shim branch is tracked in
	// CreatedBranches.
	BotRoleProbe *BotRoleProbe
}

// MintStatus is the state of one CLI mint status scenario: the mint
// address the operator knows, the credential the CLI was given, and
// what the CLI reported. Output never holds a credential: it is
// redacted before it is stored.
type MintStatus struct {
	// MintURL is the mint address passed as --mint-url.
	MintURL string
	// Credential names the credential the CLI ran with, as in step
	// text (e.g. "write actor credential").
	Credential string

	// Ran records that the CLI was run.
	Ran bool
	// ExitCode is the CLI's exit code; -1 when it did not exit
	// normally (e.g. killed on timeout).
	ExitCode int
	// TimedOut records that the CLI was killed at the step's deadline.
	TimedOut bool
	// Output is the CLI's combined stdout and stderr, redacted.
	Output string
	// CredentialInOutput records that the raw output contained the
	// credential, before redaction.
	CredentialInOutput bool
}

// BotRoleProbe is the state of one bot-role lookup scenario (ADR 0107): a
// reusable probe workflow in a temporary host repository, called from a
// shim workflow on a temporary branch of the leased repository. Each probe
// job asks the mint's POST /v1/bot-role for the registered role of one bot
// login, authenticated by the job's GitHub Actions OIDC token, whose
// repository claim is the leased repository.
type BotRoleProbe struct {
	// Forge is the forge the Examples row targets ("github").
	Forge string
	// HostRepo hosts the reusable probe workflow (the approved
	// mint-probe host of the leased slot).
	HostRepo string
	// CreatedRepos lists the host repositories this scenario created in
	// the pool org, for CleanupScenario.
	CreatedRepos []string

	// Identity is the identity token under test, for example
	// "registered:review" or "unregistered-e2e".
	Identity string
	// Mode is the subject probe's mode: "lookup" or "forced_failure"
	// (the probe sends a deliberately invalid credential).
	Mode string
	// ActorLogin is the bot login the subject asks about.
	ActorLogin string
	// ClaimedRole is a role the subject request claims in its body, which
	// the mint must ignore; empty when none is claimed.
	ClaimedRole string
	// RegisteredRole is the role a registered identity must resolve to;
	// empty for identities that are not registered.
	RegisteredRole string
	// ControlLogin is the registered bot login the control asks about.
	ControlLogin string

	// Branch and WorkflowName identify the shim run.
	Branch       string
	WorkflowName string
	Run          *forge.WorkflowRun

	// Control is a lookup of the registered review bot, made in the same
	// workflow run; it must be recognized for any subject outcome to
	// count. Subject is the lookup under test.
	Control *BotRoleProbeResult
	Subject *BotRoleProbeResult
}

// BotRoleProbeResult is the compact, token-free result a bot-role probe
// job uploads as an artifact.
type BotRoleProbeResult struct {
	// Mode, ActorLogin, Repo and ClaimedRole echo the probe's request.
	Mode        string `json:"mode"`
	ActorLogin  string `json:"actor_login"`
	Repo        string `json:"repo"`
	ClaimedRole string `json:"claimed_role,omitempty"`

	// OIDCVerified is true when the probe verified the OIDC token's
	// signature, issuer, audience and time claims before calling the mint.
	OIDCVerified bool `json:"oidc_verified"`

	// Repository and JobWorkflowRef are the OIDC claims of the job.
	Repository     string `json:"repository,omitempty"`
	JobWorkflowRef string `json:"job_workflow_ref,omitempty"`

	// HTTPStatus is the mint's status code; zero when the mint was not
	// called or did not answer.
	HTTPStatus int `json:"http_status,omitempty"`
	// Response keeps only the outcome, role and error strings of the
	// mint's body (each truncated to 300 characters).
	Response *BotRoleProbeResponse `json:"response,omitempty"`

	// Error is a probe-side failure: "config", "oidc" or "transport".
	Error string `json:"error,omitempty"`
}

// BotRoleProbeResponse is the sanitized body of the mint's answer.
type BotRoleProbeResponse struct {
	Outcome string `json:"outcome,omitempty"`
	Role    string `json:"role,omitempty"`
	Error   string `json:"error,omitempty"`
}

// MintProbe is the state of one mint-probe scenario: a reusable probe
// workflow in a temporary host repository, called from a shim workflow
// on a temporary branch of the leased repository. The host repository
// appears in the OIDC job_workflow_ref claim, the leased repository in
// the repository claim, so the two are always different repositories.
type MintProbe struct {
	// Forge is the forge the Examples row targets ("github").
	Forge string
	// HostArrangement is "approved" or "unapproved": whether the host
	// of the subject request's workflow is on the mint's
	// WORKFLOW_HOST_REPOS.
	HostArrangement string
	// ApprovedHostRepo hosts the control request's workflow, and the
	// subject's when HostArrangement is "approved".
	ApprovedHostRepo string
	// SubjectHostRepo hosts the subject request's workflow.
	SubjectHostRepo string
	// CreatedRepos lists the host repositories this scenario created in
	// the pool org, for CleanupScenario.
	CreatedRepos []string

	// Request of the subject probe; Repos is the mint request's repos.
	Role  string
	Level string
	Repos []string

	// Branch and WorkflowName identify the shim run.
	Branch       string
	WorkflowName string
	Run          *forge.WorkflowRun

	// Control is the same-repository request through the approved host
	// for the same role and level, made in the same workflow run; it
	// must mint for any subject outcome to count. Subject is the
	// request under test.
	Control *MintProbeResult
	Subject *MintProbeResult
}

// MintProbeResult is the compact, token-free result a probe job
// uploads as an artifact.
type MintProbeResult struct {
	// Outcome is "minted", "denied" (the mint answered 401/403) or
	// "error" (the probe could not get a policy answer).
	Outcome string `json:"outcome"`
	// Error classifies an "error" outcome: "config", "oidc",
	// "transport", "http", "response" or "revoke" (the minted token
	// could not be revoked).
	Error      string `json:"error,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	// Message is the mint's error message, or the probe's own
	// diagnostic for an "error" outcome.
	Message string `json:"message,omitempty"`

	Role  string   `json:"role"`
	Level string   `json:"level"`
	Repos []string `json:"repos"`

	// OIDCVerified is true when the probe verified the OIDC token's
	// signature, issuer, audience and time claims before calling the mint.
	// A host denial is only credible from a verified token.
	OIDCVerified bool `json:"oidc_verified,omitempty"`

	// Repository and JobWorkflowRef are the OIDC claims the probe sent.
	Repository     string `json:"repository,omitempty"`
	JobWorkflowRef string `json:"job_workflow_ref,omitempty"`

	// Scope of a minted token, as reported by the mint.
	RepositorySelection string            `json:"repository_selection,omitempty"`
	GrantedRepos        []string          `json:"granted_repos,omitempty"`
	GrantedPermissions  map[string]string `json:"granted_permissions,omitempty"`
}

// Clone creates a shallow copy of w. Drivers and shared state (SCM,
// CI, Driver, Failures) are shared by reference — this is safe because
// the shared types are safe for concurrent use:
//   - scm/github.Driver holds only a forge.Client (concurrent-safe).
//   - ci/githubactions.Driver holds a forge.Client and an immutable Token.
//   - install.Driver is concurrent-safe by contract.
//   - behaviourfail.Recorder is mutex-protected.
//
// Race tests in each driver package (TestConcurrentAccess,
// TestConcurrentStateAccess) verify the real types under -race with
// forge.FakeClient.
//
// Scenario-level fields are copied verbatim; callers should call
// resetScenarioWorld (in package suite) to zero them for each new scenario.
func (w *World) Clone() *World {
	clone := *w
	return &clone
}

// IsPlaybackMode reports whether the world's Driver is a
// *install.PlaybackDriver, i.e. the suite is running dummy-playback
// scenarios rather than the standard behaviour suite.
func (w *World) IsPlaybackMode() bool {
	_, ok := w.Driver.(*install.PlaybackDriver)
	return ok
}

const BehaviourScriptRepoPath = "behaviour/current-scenario.yaml"

// BehaviourScriptPath returns the repo-relative path for the dummy agent script.
// BT is per-repo only; config always lives under .fullsend/.
func (w *World) BehaviourScriptPath() string {
	return filepath.Join(".fullsend", BehaviourScriptRepoPath)
}
