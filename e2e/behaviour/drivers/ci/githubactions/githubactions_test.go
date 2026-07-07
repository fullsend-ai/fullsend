package githubactions

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

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
