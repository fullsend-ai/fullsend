package steps

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestGivenCustomHarness_Validation(t *testing.T) {
	w := &world.World{}
	require.Error(t, givenCustomHarness(w, "", "doc"))
	require.Error(t, givenCustomHarness(w, "agent", ""))
}

func TestDispatchSteps_RequireScenarioStart(t *testing.T) {
	w := &world.World{}
	require.Error(t, thenHarnessWorkflowCompletes(w, "agent"))
	require.Error(t, thenHarnessAgentDidNotRun(w, "agent"))
}

func TestDispatchSteps_RequirePullRequest(t *testing.T) {
	w := &world.World{ScenarioStart: time.Now()}
	require.Error(t, whenPullRequestLabeled(w, "label"))
	require.Error(t, whenPullRequestReviewComment(w))
}

func TestEnsureHarnessArtifacts_NoWorkflowRun(t *testing.T) {
	w := &world.World{ScenarioStart: time.Now()}
	err := ensureHarnessArtifacts(w, "agent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow run")
}
