package artifacts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindOutputFilesReturnsMatchesAcrossArtifacts(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"fullsend-pre/run/agent-result.json":          "first",
		"fullsend-pre.matrix-1/run/agent-result.json": "second",
	} {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}

	got, err := FindOutputFiles(root, "agent-result.json")
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("first"), []byte("second")}, got)

	first, err := FindOutputFile(root, "agent-result.json")
	require.NoError(t, err)
	assert.Equal(t, []byte("first"), first)
}

func TestFindOutputFilesReportsMissingFileAndRoot(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		_, err := FindOutputFiles(t.TempDir(), "missing.json")
		require.ErrorContains(t, err, "missing.json not found")
		_, err = FindOutputFile(t.TempDir(), "missing.json")
		require.ErrorContains(t, err, "missing.json not found")
	})
	t.Run("missing root", func(t *testing.T) {
		_, err := FindOutputFiles(filepath.Join(t.TempDir(), "missing"), "result.json")
		require.Error(t, err)
	})
}

func TestFindBehaviourResults(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fullsend-pre", "behaviour-results.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"passed":true}`), 0o600))

	got, err := FindBehaviourResults(root)
	require.NoError(t, err)
	assert.JSONEq(t, `{"passed":true}`, string(got))
}
