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

func TestClone_CopiesDriverFields(t *testing.T) {
	original := &World{
		Org:          "test-org",
		RepoFull:     "test-org/test-repo",
		RepoOwner:    "test-org",
		RepoName:     "test-repo",
		Token:        "tok",
		FixturesRoot: "e2e/behaviour",
		Install:      &fakeInstallState{prefix: ".fullsend"},
	}
	clone := original.Clone()

	assert.Equal(t, original.Org, clone.Org)
	assert.Equal(t, original.RepoFull, clone.RepoFull)
	assert.Equal(t, original.RepoOwner, clone.RepoOwner)
	assert.Equal(t, original.RepoName, clone.RepoName)
	assert.Equal(t, original.Token, clone.Token)
	assert.Equal(t, original.FixturesRoot, clone.FixturesRoot)
	assert.Same(t, original.Install, clone.Install)
}

func TestClone_ZerosScenarioFields(t *testing.T) {
	original := &World{
		Org:            "test-org",
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

	assert.Equal(t, 0, clone.IssueNumber)
	assert.Equal(t, 0, clone.PRNumber)
	assert.Equal(t, "", clone.DispatchAgent)
	assert.Equal(t, "", clone.ArtifactDir)
	assert.Equal(t, "", clone.ForkOwner)
	assert.Equal(t, "", clone.ForkRepo)
	assert.Equal(t, 0, clone.ForkPRNumber)
	assert.Equal(t, "", clone.ForkPRBranch)
	assert.Equal(t, "", clone.LeasedRepoName)
}

func TestClone_IndependentMutation(t *testing.T) {
	original := &World{Org: "test-org", RepoName: "test-repo"}
	clone := original.Clone()

	clone.IssueNumber = 123
	clone.RepoName = "test-repo-01"
	assert.Equal(t, 0, original.IssueNumber)
	assert.Equal(t, "test-repo", original.RepoName)
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
