package normevent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func examplesDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	return filepath.Join(root, "docs", "normative", "normalized-event", "v1", "examples")
}

func TestParseJSON_Examples(t *testing.T) {
	dir := examplesDir(t)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var parsed int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		name := e.Name()
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, name)

		ev, err := ParseJSON(data)
		if name == "invalid-path-traversal.json" {
			assert.Error(t, err, name)
			continue
		}
		require.NoError(t, err, name)
		require.NotNil(t, ev, name)
		parsed++
	}
	assert.GreaterOrEqual(t, parsed, 10)
}

func TestIsWriteAuthorized(t *testing.T) {
	assert.True(t, IsWriteAuthorized(RoleWrite))
	assert.True(t, IsWriteAuthorized(RoleAdmin))
	assert.False(t, IsWriteAuthorized(RoleTriage))
	assert.False(t, IsWriteAuthorized(RoleNone))
}

func TestMapGitHubPermission(t *testing.T) {
	assert.Equal(t, RoleWrite, MapGitHubPermission("write"))
	assert.Equal(t, RoleNone, MapGitHubPermission("unknown"))
	assert.Equal(t, RoleNone, MapGitHubPermission("custom-docs-role"))
}

func TestComputeChangeProposalIsFork(t *testing.T) {
	assert.False(t, ComputeChangeProposalIsFork("o/r", "o/r"))
	assert.True(t, ComputeChangeProposalIsFork("fork/r", "o/r"))
	assert.True(t, ComputeChangeProposalIsFork("", "o/r"))
	assert.True(t, ComputeChangeProposalIsFork("o/r", ""))
}

func TestToMap_RoundTrip(t *testing.T) {
	dir := examplesDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "issue-opened.json"))
	require.NoError(t, err)
	ev, err := ParseJSON(data)
	require.NoError(t, err)
	m, err := ev.ToMap()
	require.NoError(t, err)
	assert.Equal(t, "work_item", m["entity"].(map[string]any)["kind"])
}
