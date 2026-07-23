package steps

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
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

func TestNegativeSettleDuration(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		world    *world.World
		now      time.Time
		wantZero bool          // true if settle should be skipped
		wantDur  time.Duration // exact expected duration (checked when wantZero is false)
	}{
		{
			name: "WorkflowRun set — skip settle",
			world: &world.World{
				ScenarioStart: now.Add(-30 * time.Second),
				WorkflowRun:   &forge.WorkflowRun{ID: 1},
			},
			now:      now,
			wantZero: true,
		},
		{
			name: "standalone negative — full settle",
			world: &world.World{
				ScenarioStart: now,
			},
			now:     now,
			wantDur: defaultSettleDuration,
		},
		{
			name:    "ScenarioStart zero — full settle (safety)",
			world:   &world.World{},
			now:     now,
			wantDur: defaultSettleDuration,
		},
		{
			name: "partial elapsed — remaining settle",
			world: &world.World{
				ScenarioStart: now.Add(-60 * time.Second),
			},
			now:     now,
			wantDur: 30 * time.Second,
		},
		{
			name: "elapsed >= settle budget — skip settle",
			world: &world.World{
				ScenarioStart: now.Add(-90 * time.Second),
			},
			now:      now,
			wantZero: true,
		},
		{
			name: "elapsed > settle budget — skip settle",
			world: &world.World{
				ScenarioStart: now.Add(-120 * time.Second),
			},
			now:      now,
			wantZero: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := negativeSettleDuration(tc.world, tc.now)
			if tc.wantZero {
				assert.Equal(t, time.Duration(0), got)
			} else {
				assert.Equal(t, tc.wantDur, got)
			}
		})
	}
}
