package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoFullsendDir walks up from the test working directory to the
// checkout's .fullsend directory. Skips when that overlay is not present
// (for example a trimmed module-only checkout).
func repoFullsendDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for range 6 {
		candidate := filepath.Join(dir, ".fullsend", "config.yaml")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return filepath.Join(dir, ".fullsend")
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("repo .fullsend/config.yaml not found from test dir")
	return ""
}

// TestRepoOverlay_RetroTemporarilyDisabled pins this repository's
// operational overlay: retro is suppressed via agents[].enabled, not by
// dropping the role. reusable-dispatch.yml's agent-check skips a stage
// when the last matching agents[] entry has enabled: false, and that
// gate is shared by automatic PR-close retro and manual /fs-retro.
// Dropping retro from roles: would not stop dispatch while roles still
// lists fullsend (backward-compat in the role-check step). See #7805.
func TestRepoOverlay_RetroTemporarilyDisabled(t *testing.T) {
	cfg, err := LoadConfigWriter(repoFullsendDir(t), LoadOpts{MissingOK: false})
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())

	assert.True(t, IsAgentExplicitlyDisabled(cfg.AgentEntries(), "retro"),
		"retro must be skipped by dispatch agent-check (automatic and /fs-retro)")
	for _, name := range []string{"triage", "code", "fix", "review", "prioritize"} {
		assert.False(t, IsAgentExplicitlyDisabled(cfg.AgentEntries(), name),
			"%s must remain dispatchable", name)
	}

	pr, ok := cfg.(PerRepoConfigReader)
	require.True(t, ok)
	assert.Contains(t, pr.ConfigRoles(), "retro",
		"keep the retro role so the GitHub App stays installed; re-enable by deleting enabled: false")
	assert.Contains(t, pr.ConfigRoles(), "fullsend")

	entry, found := AgentSettingsFor(cfg.AgentEntries(), "retro")
	require.True(t, found)
	assert.Equal(t, "sonnet", entry.Model,
		"model pin is retained so re-enable is deleting enabled: false")
}
