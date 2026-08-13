package world

import (
	"net/http/httptest"
	"path/filepath"
	"time"

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
	Config  env.RunnerConfig
	SCM     scm.Driver
	CI      ci.Driver
	Install install.State

	Org       string
	RepoFull  string
	RepoOwner string
	RepoName  string
	Token     string
	Logf      func(string, ...any)

	// FixturesRoot is module-relative (e.g. "e2e/behaviour" or "behaviour").
	FixturesRoot string

	ScenarioStart time.Time

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

	// LeasedRepoName is the logical test-repo name acquired from a RepoPool
	// for this scenario's duration. Empty when no pool is configured.
	LeasedRepoName string

	// RepoDriver is the unified repo-allocation driver that owns pool
	// leasing and lazy repo creation/install. Shared across scenarios
	// (like other driver fields) and safe for concurrent use. Nil when
	// repo allocation is not configured.
	RepoDriver install.Driver

	// KillSwitchActivated records whether this scenario activated the
	// repo-level kill switch. CleanupScenario uses this to deactivate
	// the switch so the next scenario on this slot is not affected.
	KillSwitchActivated bool

	// Jira mock state — set by the "Given a mock Jira server" step.
	JiraMockServer *httptest.Server
	JiraMockState  *jiramock.State
	JiraConfigDir  string // temp dir holding .fullsend/ layout for the poller
}

// Clone creates a shallow copy of w. Drivers and shared state (SCM,
// CI, Install as install.State) are shared by reference — this is safe
// because the production implementations are immutable wrappers:
//   - scm/github.Driver holds only a forge.Client (concurrent-safe).
//   - ci/githubactions.Driver holds a forge.Client and an immutable Token.
//   - install.PerRepoState holds only immutable string fields.
//
// Race tests in each driver package (TestConcurrentAccess,
// TestConcurrentStateAccess) verify the real types under -race with
// forge.FakeClient. See scm.Driver, ci.Driver, and install.State doc
// comments for the concurrency contract.
//
// Scenario-level fields are copied verbatim; callers should call
// resetScenarioWorld (in package suite) to zero them for each new scenario.
func (w *World) Clone() *World {
	clone := *w
	return &clone
}

const BehaviourScriptRepoPath = "behaviour/current-scenario.yaml"

// BehaviourScriptPath returns the repo-relative path for the dummy agent script.
func (w *World) BehaviourScriptPath() string {
	if w.Install == nil {
		return BehaviourScriptRepoPath
	}
	if prefix := w.Install.ConfigPathPrefix(); prefix != "" {
		return filepath.Join(prefix, BehaviourScriptRepoPath)
	}
	return BehaviourScriptRepoPath
}
