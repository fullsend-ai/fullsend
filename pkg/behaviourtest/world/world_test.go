package world

import (
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	scmgithub "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/stretchr/testify/assert"
)

// fakeInstallState implements install.State for unit tests.
type fakeInstallState struct {
	prefix string
}

func (f *fakeInstallState) Mode() string               { return "" }
func (f *fakeInstallState) TestRepo() string           { return "" }
func (f *fakeInstallState) ConfigOwner() string        { return "" }
func (f *fakeInstallState) ConfigRepo() string         { return "" }
func (f *fakeInstallState) ConfigPathPrefix() string   { return f.prefix }
func (f *fakeInstallState) TriageWorkflowRepo() string { return "" }
func (f *fakeInstallState) TriageWorkflowFile() string { return "" }
func (f *fakeInstallState) AgentWorkflowFile() string  { return "" }
func (f *fakeInstallState) AgentArtifactName() string  { return "" }

func TestClone_CopiesAllFields(t *testing.T) {
	original := &World{
		Org:            "test-org",
		RepoFull:       "test-org/test-repo",
		RepoOwner:      "test-org",
		RepoName:       "test-repo",
		Token:          "tok",
		FixturesRoot:   "e2e/behaviour",
		Install:        &fakeInstallState{prefix: ".fullsend"},
		IssueNumber:    42,
		PRNumber:       7,
		DispatchAgent:  "triage",
		ArtifactDir:    "/tmp/art",
		ForkOwner:      "fork-org",
		ForkRepo:       "fork-repo",
		ForkPRNumber:   99,
		ForkPRBranch:   "pr-branch",
		LeasedRepoName: "test-repo-03",
	}
	clone := original.Clone()

	// Template / driver fields are copied.
	assert.Equal(t, original.Org, clone.Org)
	assert.Equal(t, original.RepoFull, clone.RepoFull)
	assert.Equal(t, original.RepoOwner, clone.RepoOwner)
	assert.Equal(t, original.RepoName, clone.RepoName)
	assert.Equal(t, original.Token, clone.Token)
	assert.Equal(t, original.FixturesRoot, clone.FixturesRoot)
	assert.Same(t, original.Install, clone.Install)

	// Scenario fields are also copied (value copy). The caller is
	// responsible for zeroing them via resetScenarioWorld.
	assert.Equal(t, 42, clone.IssueNumber)
	assert.Equal(t, 7, clone.PRNumber)
	assert.Equal(t, "triage", clone.DispatchAgent)
	assert.Equal(t, "test-repo-03", clone.LeasedRepoName)
}

func TestClone_IndependentMutation(t *testing.T) {
	original := &World{Org: "test-org", RepoName: "test-repo"}
	clone := original.Clone()

	clone.IssueNumber = 123
	clone.RepoName = "test-repo-01"
	assert.Equal(t, 0, original.IssueNumber)
	assert.Equal(t, "test-repo", original.RepoName)
}

func TestClone_SharesDriversByReference(t *testing.T) {
	fc := forge.NewFakeClient()
	scmDriver := scmgithub.New(fc)
	ciDriver := githubactions.New(fc, "tok")
	inst := &fakeInstallState{prefix: ".fullsend"}
	original := &World{SCM: scmDriver, CI: ciDriver, Install: inst}
	clone := original.Clone()

	// Drivers are shared by reference — the production implementations
	// are immutable wrappers around forge.Client and are safe for
	// concurrent use. Race tests in each driver package verify this
	// under -race (see #5441).
	assert.Same(t, original.SCM, clone.SCM)
	assert.Same(t, original.CI, clone.CI)
	assert.Same(t, original.Install, clone.Install)
}

// TestClone_ConcurrentFieldIndependence verifies that scenario-specific
// value fields on cloned Worlds can be mutated independently from
// concurrent goroutines without racing. This mirrors the
// GODOG_CONCURRENCY>1 pattern where each scenario gets its own clone.
//
// Note: concurrency safety of the shared driver pointers (SCM, CI,
// Install) is verified by race tests in the respective driver packages
// (scm/github, ci/githubactions, install), not here.
func TestClone_ConcurrentFieldIndependence(t *testing.T) {
	t.Parallel()

	template := &World{
		Config:       env.RunnerConfig{SCM: "github", CI: "githubactions", InstallMode: "per-repo"},
		Install:      &fakeInstallState{prefix: ".fullsend"},
		Org:          "test-org",
		RepoFull:     "test-org/test-repo",
		RepoOwner:    "test-org",
		RepoName:     "test-repo",
		Token:        "tok",
		FixturesRoot: "e2e/behaviour",
	}

	const numClones = 12
	clones := make([]*World, numClones)
	for i := range numClones {
		clones[i] = template.Clone()
		clones[i].IssueNumber = 0
		clones[i].PRNumber = 0
		clones[i].ScenarioStart = time.Now()
	}

	// Each goroutine mutates only its own clone's scenario fields.
	var wg sync.WaitGroup
	for i, w := range clones {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Read shared template fields (value copies).
			_ = w.Config.InstallMode
			_ = w.FixturesRoot

			// Mutate scenario-specific fields independently.
			w.IssueNumber = i + 1
			w.PRNumber = i + 100
			w.DispatchAgent = "triage"
			w.ArtifactDir = "/tmp/art"
			w.ForkOwner = "fork-org"
		}()
	}
	wg.Wait()

	for i, w := range clones {
		assert.Equal(t, i+1, w.IssueNumber, "clone %d IssueNumber", i)
		assert.Equal(t, i+100, w.PRNumber, "clone %d PRNumber", i)
	}

	// Shared Install is still the same instance.
	for i, w := range clones {
		assert.Same(t, template.Install, w.Install, "clone %d Install", i)
	}
}

func TestBehaviourScriptPath_NilInstall(t *testing.T) {
	w := &World{}
	got := w.BehaviourScriptPath()
	assert.Equal(t, BehaviourScriptRepoPath, got)
}

func TestBehaviourScriptPath_EmptyPrefix(t *testing.T) {
	w := &World{Install: &fakeInstallState{prefix: ""}}
	got := w.BehaviourScriptPath()
	assert.Equal(t, BehaviourScriptRepoPath, got)
}

func TestBehaviourScriptPath_WithPrefix(t *testing.T) {
	w := &World{Install: &fakeInstallState{prefix: ".fullsend"}}
	got := w.BehaviourScriptPath()
	assert.Equal(t, ".fullsend/behaviour/current-scenario.yaml", got)
}
