package steps

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// mockCIDriver implements ci.Driver with configurable CountHarnessDispatches,
// DownloadNamedArtifactFromRun, WaitForHarnessAgentRound, and WaitForWorkflow
// stubs.
type mockCIDriver struct {
	ci.Driver                  // satisfies the full interface; untested methods panic
	countFn                    func(ctx context.Context, owner, repo, agent string, after time.Time) (int, error)
	downloadNamedArtifactFn    func(ctx context.Context, owner, repo string, runID int, artifactName, destDir string) error
	waitForHarnessAgentRoundFn func(ctx context.Context, owner, repo, agent string, after time.Time, consumed map[int]bool) (*forge.WorkflowRun, error)
	waitForWorkflowFn          func(ctx context.Context, owner, repo, workflowFile string, after time.Time, event string) (*forge.WorkflowRun, error)
}

func (m *mockCIDriver) CountHarnessDispatches(ctx context.Context, owner, repo, agent string, after time.Time) (int, error) {
	return m.countFn(ctx, owner, repo, agent, after)
}

func (m *mockCIDriver) DownloadNamedArtifactFromRun(ctx context.Context, owner, repo string, runID int, artifactName, destDir string) error {
	return m.downloadNamedArtifactFn(ctx, owner, repo, runID, artifactName, destDir)
}

func (m *mockCIDriver) WaitForHarnessAgentRound(ctx context.Context, owner, repo, agent string, after time.Time, consumed map[int]bool) (*forge.WorkflowRun, error) {
	return m.waitForHarnessAgentRoundFn(ctx, owner, repo, agent, after, consumed)
}

func (m *mockCIDriver) WaitForWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time, event string) (*forge.WorkflowRun, error) {
	return m.waitForWorkflowFn(ctx, owner, repo, workflowFile, after, event)
}

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
		RepoName:      "test-repo",
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
		RepoName:      "test-repo",
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
		RepoName:      "test-repo",
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
		RepoName:      "test-repo",
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
		RepoName:      "test-repo",
		ScenarioStart: time.Now(),
	}
	err := thenHarnessDispatchedExactly(w, "triage", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API failure")
}
