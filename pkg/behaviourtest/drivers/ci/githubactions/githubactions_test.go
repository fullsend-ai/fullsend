package githubactions

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestDispatchDetectionWindow_AtLeast4Minutes(t *testing.T) {
	t.Parallel()

	// The dispatch detection window is dispatchMaxTry × dispatchPoll.
	// It must be at least 4 minutes to tolerate slow GitHub webhook
	// delivery. See issue #5503.
	window := time.Duration(dispatchMaxTry) * dispatchPoll
	assert.GreaterOrEqual(t, window, 4*time.Minute,
		"dispatch detection window (%v) should be at least 4 minutes", window)
}

func TestSelectWorkflowRun_ReturnsFailedRun(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := []forge.WorkflowRun{
		{ID: 1, Status: "completed", Conclusion: "failure", Event: "issues", CreatedAt: "2026-01-01T01:00:00Z"},
	}

	got := selectWorkflowRun(runs, after, "issues")
	require.NotNil(t, got)
	assert.Equal(t, 1, got.ID)
	assert.Equal(t, "failure", got.Conclusion)
}

func TestSelectSuccessfulWorkflowRun_SkipsFailed(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := []forge.WorkflowRun{
		{ID: 1, Status: "completed", Conclusion: "failure", Event: "issues", CreatedAt: "2026-01-01T01:00:00Z"},
		{ID: 2, Status: "completed", Conclusion: "success", Event: "issues", CreatedAt: "2026-01-01T02:00:00Z"},
	}

	got := selectSuccessfulWorkflowRun(runs, after, "issues")
	require.NotNil(t, got)
	assert.Equal(t, 2, got.ID)
}

func TestExtractArtifactZip_RejectsCorruptZip(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	err := extractArtifactZip("artifact", []byte("not-a-zip"), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse artifact zip")
}

func TestExtractArtifactZip_RejectsSymlink(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: "link", Method: zip.Store}
	hdr.SetMode(os.ModeSymlink | 0o755)
	_, err := zw.CreateHeader(hdr)
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	err = extractArtifactZip("../escape", buf.Bytes(), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestExtractArtifactZip_RejectsPathTraversal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../escape.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("nope"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	err = extractArtifactZip("artifact", buf.Bytes(), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")
}

func TestExtractArtifactZip_SanitizesName(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("ok.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	require.NoError(t, extractArtifactZip("../../weird/name", buf.Bytes(), dest))
	_, err = os.Stat(filepath.Join(dest, "name", "ok.txt"))
	require.NoError(t, err)
}

func TestExtractArtifactZip_RejectsAggregateLimit(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	const chunk = 10 << 20 // per-file limit
	for i := 0; i < 11; i++ {
		w, err := zw.Create(fmt.Sprintf("part-%d.bin", i))
		require.NoError(t, err)
		_, err = w.Write(bytes.Repeat([]byte("x"), chunk))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	err := extractArtifactZip("artifact", buf.Bytes(), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aggregate extraction limit")
}

func TestNewestRepositoryArtifactCreatedAt(t *testing.T) {
	t.Parallel()

	arts := []forge.RepositoryArtifact{
		{CreatedAt: "2026-01-01T00:00:00Z"},
		{CreatedAt: "2026-01-02T00:00:00Z"},
	}
	assert.Equal(t, "2026-01-02T00:00:00Z", newestRepositoryArtifactCreatedAt(arts))
}

func TestCountHarnessDispatches_ZeroArtifacts(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// No artifacts at all.

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestCountHarnessDispatches_SingleMatch(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 10},
		},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_MultipleMatches(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 10},
			{ID: 2, Name: "fullsend-triage", CreatedAt: "2026-01-03T00:00:00Z", WorkflowRunID: 20},
			{ID: 3, Name: "fullsend-triage", CreatedAt: "2026-01-04T00:00:00Z", WorkflowRunID: 30},
		},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestCountHarnessDispatches_FiltersBeforeTime(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 10}, // before
			{ID: 2, Name: "fullsend-triage", CreatedAt: "2026-07-01T00:00:00Z", WorkflowRunID: 20}, // after
		},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_FiltersOtherAgents(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 10},
			{ID: 2, Name: "fullsend-review", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 20},
			{ID: 3, Name: "fullsend-code", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 30},
		},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_APIError(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.Errors["ListRepositoryArtifacts"] = fmt.Errorf("API error")

	d := &Driver{Client: client}
	_, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

func TestWaitForHarnessAgent_FromRepositoryArtifact(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{
				ID:            10,
				Name:          "fullsend-issue-ping",
				CreatedAt:     "2026-01-02T00:00:00Z",
				WorkflowRunID: 99,
			},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 99, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z",
		},
	}

	d := &Driver{Client: client}
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "issue-ping", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 99, run.ID)
}
