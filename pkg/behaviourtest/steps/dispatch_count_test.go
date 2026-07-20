package steps

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// mockCIDriver implements ci.Driver with a configurable CountHarnessDispatches.
type mockCIDriver struct {
	ci.Driver // satisfies the full interface; untested methods panic
	countFn   func(ctx context.Context, owner, repo, agent string, after time.Time) (int, error)
}

func (m *mockCIDriver) CountHarnessDispatches(ctx context.Context, owner, repo, agent string, after time.Time) (int, error) {
	return m.countFn(ctx, owner, repo, agent, after)
}

// mockInstallState satisfies install.State for test scenarios.
type mockInstallState struct{}

func (m *mockInstallState) Mode() string               { return "per-repo" }
func (m *mockInstallState) TestRepo() string           { return "test-repo" }
func (m *mockInstallState) ConfigOwner() string        { return "test-org" }
func (m *mockInstallState) ConfigRepo() string         { return "test-repo" }
func (m *mockInstallState) ConfigPathPrefix() string   { return "" }
func (m *mockInstallState) TriageWorkflowFile() string { return "triage.yml" }
func (m *mockInstallState) TriageWorkflowRepo() string { return "test-repo" }
func (m *mockInstallState) AgentWorkflowFile() string  { return "agent.yml" }
func (m *mockInstallState) AgentArtifactName() string  { return "fullsend-triage" }

func TestThenHarnessDispatchedExactly_RequiresScenarioStart(t *testing.T) {
	w := &world.World{}
	err := thenHarnessDispatchedExactly(w, "agent", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trigger time")
}

func TestThenHarnessDispatchedExactly_RequiresAgentName(t *testing.T) {
	w := &world.World{ScenarioStart: time.Now()}
	err := thenHarnessDispatchedExactly(w, "", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}

func TestThenHarnessDispatchedExactly_ZeroDispatches(t *testing.T) {
	mock := &mockCIDriver{
		countFn: func(_ context.Context, _, _, _ string, _ time.Time) (int, error) {
			return 0, nil
		},
	}
	w := &world.World{
		CI:            mock,
		Org:           "test-org",
		Install:       &mockInstallState{},
		ScenarioStart: time.Now(),
	}
	require.NoError(t, thenHarnessDispatchedExactly(w, "triage", 0))
}

func TestThenHarnessDispatchedExactly_SingleDispatch(t *testing.T) {
	mock := &mockCIDriver{
		countFn: func(_ context.Context, _, _, _ string, _ time.Time) (int, error) {
			return 1, nil
		},
	}
	w := &world.World{
		CI:            mock,
		Org:           "test-org",
		Install:       &mockInstallState{},
		ScenarioStart: time.Now(),
	}
	require.NoError(t, thenHarnessDispatchedExactly(w, "triage", 1))
}

func TestThenHarnessDispatchedExactly_MultipleDispatches(t *testing.T) {
	mock := &mockCIDriver{
		countFn: func(_ context.Context, _, _, _ string, _ time.Time) (int, error) {
			return 3, nil
		},
	}
	w := &world.World{
		CI:            mock,
		Org:           "test-org",
		Install:       &mockInstallState{},
		ScenarioStart: time.Now(),
	}
	require.NoError(t, thenHarnessDispatchedExactly(w, "triage", 3))
}

func TestThenHarnessDispatchedExactly_CountMismatch(t *testing.T) {
	mock := &mockCIDriver{
		countFn: func(_ context.Context, _, _, _ string, _ time.Time) (int, error) {
			return 2, nil
		},
	}
	w := &world.World{
		CI:            mock,
		Org:           "test-org",
		Install:       &mockInstallState{},
		ScenarioStart: time.Now(),
	}
	err := thenHarnessDispatchedExactly(w, "triage", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dispatched 2 time(s)")
	assert.Contains(t, err.Error(), "exactly 1 time(s)")
}

func TestThenHarnessDispatchedExactly_DriverError(t *testing.T) {
	mock := &mockCIDriver{
		countFn: func(_ context.Context, _, _, _ string, _ time.Time) (int, error) {
			return 0, fmt.Errorf("API failure")
		},
	}
	w := &world.World{
		CI:            mock,
		Org:           "test-org",
		Install:       &mockInstallState{},
		ScenarioStart: time.Now(),
	}
	err := thenHarnessDispatchedExactly(w, "triage", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API failure")
}
