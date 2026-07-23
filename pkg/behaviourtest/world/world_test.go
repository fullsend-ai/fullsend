package world

import (
	"testing"

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
	inst := &fakeInstallState{prefix: ".fullsend"}
	original := &World{Install: inst}
	clone := original.Clone()

	// Drivers are shared by reference (safe because they hold no
	// mutable state today — see #5441 for concurrency work).
	assert.Same(t, original.Install, clone.Install)
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
