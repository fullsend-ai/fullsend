package ci

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exactNameDriver implements only the required Driver methods, like an
// external implementation written before AgentArtifactDownloader existed.
type exactNameDriver struct {
	Driver // nil; any unexpected method call panics
	calls  []string
	err    error
}

func (d *exactNameDriver) DownloadNamedArtifactFromRun(_ context.Context, _, _ string, _ int, artifactName, destDir string) error {
	d.calls = append(d.calls, "run:"+artifactName+":"+destDir)
	return d.err
}

func (d *exactNameDriver) DownloadNamedArtifactAfter(_ context.Context, _, _, artifactName string, _ time.Time, destDir string) error {
	d.calls = append(d.calls, "after:"+artifactName+":"+destDir)
	return d.err
}

// agentAwareDriver additionally implements AgentArtifactDownloader.
type agentAwareDriver struct {
	exactNameDriver
}

func (d *agentAwareDriver) DownloadAgentArtifactsFromRun(_ context.Context, _, _ string, _ int, agent, destDir string) error {
	d.calls = append(d.calls, "agent-run:"+agent+":"+destDir)
	return d.err
}

func (d *agentAwareDriver) DownloadAgentArtifactsAfter(_ context.Context, _, _, agent string, _ time.Time, destDir string) error {
	d.calls = append(d.calls, "agent-after:"+agent+":"+destDir)
	return d.err
}

var (
	_ Driver                  = (*exactNameDriver)(nil)
	_ AgentArtifactDownloader = (*agentAwareDriver)(nil)
)

func TestDownloadAgentArtifactsFromRunFallsBackToExactName(t *testing.T) {
	t.Parallel()
	d := &exactNameDriver{}
	_, isAware := Driver(d).(AgentArtifactDownloader)
	require.False(t, isAware)

	require.NoError(t, DownloadAgentArtifactsFromRun(context.Background(), d, "org", "repo", 7, "triage", "dest"))
	assert.Equal(t, []string{"run:fullsend-triage:dest"}, d.calls)
}

func TestDownloadAgentArtifactsFromRunUsesCapability(t *testing.T) {
	t.Parallel()
	d := &agentAwareDriver{}
	require.NoError(t, DownloadAgentArtifactsFromRun(context.Background(), d, "org", "repo", 7, "review", "dest"))
	assert.Equal(t, []string{"agent-run:review:dest"}, d.calls)
}

func TestDownloadAgentArtifactsAfterFallsBackToExactName(t *testing.T) {
	t.Parallel()
	d := &exactNameDriver{}
	require.NoError(t, DownloadAgentArtifactsAfter(context.Background(), d, "org", "repo", "fix", time.Time{}, "dest"))
	assert.Equal(t, []string{"after:fullsend-fix:dest"}, d.calls)
}

func TestDownloadAgentArtifactsAfterUsesCapability(t *testing.T) {
	t.Parallel()
	d := &agentAwareDriver{}
	require.NoError(t, DownloadAgentArtifactsAfter(context.Background(), d, "org", "repo", "fix", time.Time{}, "dest"))
	assert.Equal(t, []string{"agent-after:fix:dest"}, d.calls)
}

func TestDownloadAgentArtifactsPropagateErrors(t *testing.T) {
	t.Parallel()
	want := assert.AnError
	for name, d := range map[string]Driver{
		"fallback":   &exactNameDriver{err: want},
		"capability": &agentAwareDriver{exactNameDriver{err: want}},
	} {
		assert.ErrorIs(t, DownloadAgentArtifactsFromRun(context.Background(), d, "o", "r", 1, "a", "d"), want, name)
		assert.ErrorIs(t, DownloadAgentArtifactsAfter(context.Background(), d, "o", "r", "a", time.Time{}, "d"), want, name)
	}
}
