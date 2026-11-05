package steps

import (
	"context"
	"errors"
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

func (f *markerFallbackCI) DownloadAgentArtifactsAfter(context.Context, string, string, string, time.Time, string) error {
	return errors.New("unexpected time-scoped download")
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

// agentRecordingCI records the logical agent requested from the run-scoped
// and time-scoped agent downloads, and fails if an exact-name download is used.
type agentRecordingCI struct {
	ci.Driver
	runAgent   string
	afterAgent string
}

func (f *agentRecordingCI) DownloadAgentArtifactsFromRun(_ context.Context, _, _ string, _ int, agent, destDir string) error {
	f.runAgent = agent
	dir := filepath.Join(destDir, "fullsend-"+agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "unrelated.json"), []byte("{}"), 0o644)
}

func (f *agentRecordingCI) DownloadAgentArtifactsAfter(_ context.Context, _, _, agent string, _ time.Time, destDir string) error {
	f.afterAgent = agent
	dir := filepath.Join(destDir, "fullsend-"+agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "metrics.json"), []byte("{}"), 0o644)
}

func (f *agentRecordingCI) FindCompletedWorkflowRun(context.Context, string, string, string, time.Time) (*forge.WorkflowRun, error) {
	return nil, nil
}

func TestEnsureRunArtifacts_EmptyDispatchAgentDefaultsToTriage(t *testing.T) {
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())
	fake := &agentRecordingCI{}
	w := &world.World{
		WorkflowRun: &forge.WorkflowRun{ID: 1},
		CI:          fake,
	}

	require.NoError(t, ensureRunArtifacts(w, "metrics.json"))
	assert.Equal(t, "triage", fake.runAgent)
	assert.Equal(t, "triage", fake.afterAgent)
	assert.FileExists(t, filepath.Join(w.ArtifactDir, "fullsend-triage", "metrics.json"))
}

// legacyNamedCI is an external-style ci.Driver that implements only the
// exact-name download methods and not ci.AgentArtifactDownloader.
type legacyNamedCI struct {
	ci.Driver
	runName   string
	afterName string
}

func (f *legacyNamedCI) DownloadNamedArtifactFromRun(_ context.Context, _, _ string, _ int, name, destDir string) error {
	f.runName = name
	dir := filepath.Join(destDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "unrelated.json"), []byte("{}"), 0o644)
}

func (f *legacyNamedCI) DownloadNamedArtifactAfter(_ context.Context, _, _, name string, _ time.Time, destDir string) error {
	f.afterName = name
	dir := filepath.Join(destDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "metrics.json"), []byte("{}"), 0o644)
}

func (f *legacyNamedCI) FindCompletedWorkflowRun(context.Context, string, string, string, time.Time) (*forge.WorkflowRun, error) {
	return nil, nil
}

func TestEnsureRunArtifacts_DriverWithoutAgentCapabilityUsesExactNames(t *testing.T) {
	for _, tc := range []struct{ dispatchAgent, wantName string }{
		{"", "fullsend-triage"},
		{"review", "fullsend-review"},
	} {
		t.Run(tc.wantName, func(t *testing.T) {
			t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())
			fake := &legacyNamedCI{}
			w := &world.World{
				DispatchAgent: tc.dispatchAgent,
				WorkflowRun:   &forge.WorkflowRun{ID: 1},
				CI:            fake,
			}

			require.NoError(t, ensureRunArtifacts(w, "metrics.json"))
			assert.Equal(t, tc.wantName, fake.runName)
			assert.Equal(t, tc.wantName, fake.afterName)
			assert.FileExists(t, filepath.Join(w.ArtifactDir, tc.wantName, "metrics.json"))
		})
	}
}
