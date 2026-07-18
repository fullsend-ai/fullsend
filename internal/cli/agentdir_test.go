package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- registerAgentDirFlag / resolveAgentDirFlag tests ---

func TestAgentDirFlag_PrimaryFlagAccepted(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - harness/triage.yaml
`)
	cmd := newAgentListCmd()
	cmd.SetArgs([]string{"--agent-dir", dir})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Empty(t, stderr.String(), "--agent-dir should not emit a deprecation warning")
}

func TestAgentDirFlag_DeprecatedFlagAcceptedWithWarning(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - harness/triage.yaml
`)
	cmd := newAgentListCmd()
	cmd.SetArgs([]string{"--fullsend-dir", dir})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "deprecated")
	assert.Contains(t, stderr.String(), "--agent-dir")
}

func TestAgentDirFlag_BothFlagsError(t *testing.T) {
	dir := t.TempDir()
	cmd := newAgentListCmd()
	cmd.SetArgs([]string{"--agent-dir", dir, "--fullsend-dir", dir})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestAgentDirFlag_NeitherFlagError(t *testing.T) {
	cmd := newAgentListCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent-dir")
}

func TestAgentDirFlag_DeprecatedFlagHidden(t *testing.T) {
	// Verify --fullsend-dir is hidden in all commands that register it.
	t.Run("run", func(t *testing.T) {
		cmd := newRunCmd()
		flag := cmd.Flags().Lookup("fullsend-dir")
		require.NotNil(t, flag)
		assert.True(t, flag.Hidden)
	})
	t.Run("lock", func(t *testing.T) {
		cmd := newLockCmd()
		flag := cmd.Flags().Lookup("fullsend-dir")
		require.NotNil(t, flag)
		assert.True(t, flag.Hidden)
	})
	t.Run("agent add", func(t *testing.T) {
		cmd := newAgentAddCmd()
		flag := cmd.Flags().Lookup("fullsend-dir")
		require.NotNil(t, flag)
		assert.True(t, flag.Hidden)
	})
	t.Run("agent list", func(t *testing.T) {
		cmd := newAgentListCmd()
		flag := cmd.Flags().Lookup("fullsend-dir")
		require.NotNil(t, flag)
		assert.True(t, flag.Hidden)
	})
	t.Run("agent update", func(t *testing.T) {
		cmd := newAgentUpdateCmd()
		flag := cmd.Flags().Lookup("fullsend-dir")
		require.NotNil(t, flag)
		assert.True(t, flag.Hidden)
	})
	t.Run("agent remove", func(t *testing.T) {
		cmd := newAgentRemoveCmd()
		flag := cmd.Flags().Lookup("fullsend-dir")
		require.NotNil(t, flag)
		assert.True(t, flag.Hidden)
	})
	t.Run("agent migrate-customizations", func(t *testing.T) {
		cmd := newAgentMigrateCustomizationsCmd()
		flag := cmd.Flags().Lookup("fullsend-dir")
		require.NotNil(t, flag)
		assert.True(t, flag.Hidden)
	})
}

func TestAgentDirFlag_HelpShowsAgentDir(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("agent-dir")
	require.NotNil(t, flag)
	assert.Contains(t, flag.Usage, "agent definitions")
	assert.NotContains(t, flag.Usage, ".fullsend layout")
}

// Verify --fullsend-dir works across all command types (not just list).

func TestAgentDirFlag_RunCmdDeprecatedAlias(t *testing.T) {
	// Just verify the flag is accepted — the run itself will fail
	// due to missing --target-repo, but the flag parsing should succeed.
	dir := t.TempDir()
	cmd := newRunCmd()
	cmd.SetArgs([]string{"triage", "--fullsend-dir", dir, "--target-repo", "/nonexistent"})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	// Will error on the run itself (missing harness etc.), but the
	// deprecation warning should still be emitted during flag parsing.
	_ = cmd.Execute()
	assert.Contains(t, stderr.String(), "deprecated")
}

func TestAgentDirFlag_LockCmdDeprecatedAlias(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "test.yaml"), []byte("role: triage\n"), 0o644))

	cmd := newLockCmd()
	cmd.SetArgs([]string{"test", "--fullsend-dir", dir})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	_ = cmd.Execute()
	assert.Contains(t, stderr.String(), "deprecated")
}

func TestAgentDirFlag_AgentAddDeprecatedAlias(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "agents: []\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "lint.yaml"), []byte("role: coder\n"), 0o644))

	cmd := newAgentAddCmd()
	cmd.SetArgs([]string{"harness/lint.yaml", "--fullsend-dir", dir})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "deprecated")
}

func TestAgentDirFlag_AgentUpdateDeprecatedAlias(t *testing.T) {
	newSHA := "f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1"
	newContent := []byte("role: triage\n")

	srv, policy := newAgentTestServer(t, map[string][]byte{
		"/org/agents/" + newSHA + "/harness/triage.yaml": newContent,
	})

	origPolicy := fetch.DefaultPolicy
	fetch.DefaultPolicy = policy
	defer func() { fetch.DefaultPolicy = origPolicy }()

	dir := t.TempDir()
	oldSHA := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	writeOrgConfig(t, dir, `agents:
  - source: "`+srv.URL+`/org/agents/`+oldSHA+`/harness/triage.yaml#sha256=0000000000000000000000000000000000000000000000000000000000000000"
allowed_remote_resources:
  - "`+srv.URL+`/org/agents/"
`)

	cmd := newAgentUpdateCmd()
	cmd.SetArgs([]string{"triage", newSHA, "--fullsend-dir", dir})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "deprecated")
}

func TestAgentDirFlag_AgentRemoveDeprecatedAlias(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, `agents:
  - harness/triage.yaml
`)

	cmd := newAgentRemoveCmd()
	cmd.SetArgs([]string{"triage", "--fullsend-dir", dir})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "deprecated")
}

func TestAgentDirFlag_MigrateDeprecatedAlias(t *testing.T) {
	dir := t.TempDir()
	writeOrgConfig(t, dir, "")

	cmd := newAgentMigrateCustomizationsCmd()
	cmd.SetArgs([]string{"--fullsend-dir", dir, "--dry-run"})
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "deprecated")
}
