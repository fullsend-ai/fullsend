package world

import (
	"net/http/httptest"
	"path/filepath"
	"time"

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
	// CreatedBranches and CreatedPRNumbers track resources the branch
	// steps created (or discovered) so CleanupScenario can remove them.
	// Isolation across Clone()d Worlds relies on the suite invariant
	// that resetScenarioWorld nils these after every clone and the
	// template World never populates them — do not set them on a
	// template.
	RecordedBranchSHAs map[string]string
	CreatedBranches    []string
	CreatedPRNumbers   []int

	// LeasedRepoName is the logical test-repo name acquired via
	// Driver.AllocateRepo for this scenario's duration. Empty when no
	// driver is configured.
	LeasedRepoName string

	// Driver is the unified install driver that owns repo allocation,
	// deallocation, and suite-scoped teardown. Shared across scenarios
	// (like other driver fields) and safe for concurrent use.
	// Nil when no driver is configured.
	Driver install.Driver

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

	// KillSwitchActivated records whether this scenario activated the
	// repo-level kill switch. CleanupScenario uses this to deactivate
	// the switch so the next scenario on this slot is not affected.
	KillSwitchActivated bool

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
}

// Clone creates a shallow copy of w. Drivers and shared state (SCM,
// CI, Driver) are shared by reference — this is safe because the
// production implementations are immutable wrappers:
//   - scm/github.Driver holds only a forge.Client (concurrent-safe).
//   - ci/githubactions.Driver holds a forge.Client and an immutable Token.
//   - install.Driver is concurrent-safe by contract.
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
