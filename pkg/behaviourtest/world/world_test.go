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
