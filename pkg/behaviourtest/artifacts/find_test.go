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

func TestFindOutputFilesByArtifactGroupsAndOrdersNewestFirst(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"fullsend-pre/run/iteration-1/output/agent-result.json":          "a1",
		"fullsend-pre/run/iteration-10/output/agent-result.json":         "a10",
		"fullsend-pre/run/iteration-2/output/agent-result.json":          "a2",
		"fullsend-pre.matrix-1/run/iteration-1/output/agent-result.json": "b1",
	} {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}

	got, err := FindOutputFilesByArtifact(root, "agent-result.json")
	require.NoError(t, err)
	assert.Equal(t, [][][]byte{
		{[]byte("a10"), []byte("a2"), []byte("a1")},
		{[]byte("b1")},
	}, got)

	_, err = FindOutputFilesByArtifact(t.TempDir(), "agent-result.json")
	require.ErrorContains(t, err, "agent-result.json not found")
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
