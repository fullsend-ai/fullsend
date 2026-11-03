package steps

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// markerFallbackCI serves a tracked run whose agent artifact lacks the
// required marker and a nested agent run whose artifact has it.
type markerFallbackCI struct {
	ci.Driver
	trackedRunID int
	nestedRunID  int
}

func (f *markerFallbackCI) DownloadAgentArtifactsFromRun(_ context.Context, _, _ string, runID int, agent, destDir string) error {
	dir := filepath.Join(destDir, "fullsend-"+agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := "unrelated.json"
	if runID == f.nestedRunID {
		name = "metrics.json"
	}
	return os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644)
}

func (f *markerFallbackCI) FindCompletedWorkflowRun(context.Context, string, string, string, time.Time) (*forge.WorkflowRun, error) {
	return &forge.WorkflowRun{ID: f.nestedRunID}, nil
}

func TestEnsureRunArtifacts_AgentDownloadWithoutMarkerFallsBack(t *testing.T) {
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())
	w := &world.World{
		DispatchAgent: "review",
		WorkflowRun:   &forge.WorkflowRun{ID: 1},
		CI:            &markerFallbackCI{trackedRunID: 1, nestedRunID: 2},
	}

	require.NoError(t, ensureRunArtifacts(w, "metrics.json"))
	require.NotEmpty(t, w.ArtifactDir)
	assert.FileExists(t, filepath.Join(w.ArtifactDir, "fullsend-review", "metrics.json"))
	assert.NoFileExists(t, filepath.Join(w.ArtifactDir, "fullsend-review", "unrelated.json"))
}
