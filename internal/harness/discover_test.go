package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	require.NoError(t, err)
}

func TestDiscoverAgents(t *testing.T) {
	t.Run("multiple harnesses sorted by role", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "triage.yaml", "agent: agents/triage.md\nrole: triage\nslug: fs-triage\n")
		writeFile(t, dir, "code.yaml", "agent: agents/code.md\nrole: coder\nslug: fs-coder\n")
		writeFile(t, dir, "review.yaml", "agent: agents/review.md\nrole: review\nslug: fs-review\n")

		agents, err := DiscoverAgents(dir)
		require.NoError(t, err)
		require.Len(t, agents, 3)

		assert.Equal(t, "coder", agents[0].Role)
		assert.Equal(t, "review", agents[1].Role)
		assert.Equal(t, "triage", agents[2].Role)

		assert.Equal(t, "fs-coder", agents[0].Slug)
		assert.Equal(t, "code.yaml", agents[0].Filename)
		assert.Equal(t, filepath.Join(dir, "code.yaml"), agents[0].Path)
	})

	t.Run("skips harness without role or slug", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "legacy.yaml", "agent: agents/legacy.md\n")
		writeFile(t, dir, "modern.yaml", "agent: agents/modern.md\nrole: triage\nslug: fs-triage\n")

		agents, err := DiscoverAgents(dir)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		assert.Equal(t, "triage", agents[0].Role)
	})

	t.Run("role only without slug is included", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "partial.yaml", "agent: agents/partial.md\nrole: triage\n")

		agents, err := DiscoverAgents(dir)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		assert.Equal(t, "triage", agents[0].Role)
		assert.Empty(t, agents[0].Slug)
	})

	t.Run("slug only without role is included", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "slug-only.yaml", "agent: agents/slug.md\nslug: fs-triage\n")

		agents, err := DiscoverAgents(dir)
		require.NoError(t, err)
		require.Len(t, agents, 1)
	})

	t.Run("malformed YAML returns multi-error with valid files", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "good.yaml", "agent: agents/good.md\nrole: triage\nslug: fs-triage\n")
		writeFile(t, dir, "bad.yaml", ":\n  :\n    - [invalid yaml")

		agents, err := DiscoverAgents(dir)
		require.Error(t, err)
		require.Len(t, agents, 1)
		assert.Equal(t, "triage", agents[0].Role)
	})

	t.Run("empty directory returns empty list", func(t *testing.T) {
		dir := t.TempDir()

		agents, err := DiscoverAgents(dir)
		require.NoError(t, err)
		assert.Empty(t, agents)
	})

	t.Run("non-existent directory returns nil nil", func(t *testing.T) {
		agents, err := DiscoverAgents("/nonexistent/path/that/does/not/exist")
		require.NoError(t, err)
		assert.Nil(t, agents)
	})

	t.Run("yml extension is discovered", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "agent.yml", "agent: agents/agent.md\nrole: triage\nslug: fs-triage\n")

		agents, err := DiscoverAgents(dir)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		assert.Equal(t, "agent.yml", agents[0].Filename)
	})

	t.Run("skips subdirectories", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "triage.yaml", "agent: agents/triage.md\nrole: triage\nslug: fs-triage\n")
		require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0o755))

		agents, err := DiscoverAgents(dir)
		require.NoError(t, err)
		require.Len(t, agents, 1)
	})

	t.Run("same role sorted by filename", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "fix.yaml", "agent: agents/fix.md\nrole: coder\nslug: fs-coder\n")
		writeFile(t, dir, "code.yaml", "agent: agents/code.md\nrole: coder\nslug: fs-coder\n")

		agents, err := DiscoverAgents(dir)
		require.NoError(t, err)
		require.Len(t, agents, 2)
		assert.Equal(t, "code.yaml", agents[0].Filename)
		assert.Equal(t, "fix.yaml", agents[1].Filename)
	})

	t.Run("path is absolute even with relative dir", func(t *testing.T) {
		relDir := filepath.Join(t.TempDir(), "reltest")
		require.NoError(t, os.Mkdir(relDir, 0o755))
		writeFile(t, relDir, "triage.yaml", "agent: agents/triage.md\nrole: triage\nslug: fs-triage\n")

		cwd, err := os.Getwd()
		require.NoError(t, err)
		rel, err := filepath.Rel(cwd, relDir)
		require.NoError(t, err)
		if filepath.IsAbs(rel) {
			t.Skipf("could not produce relative path (got %q)", rel)
		}

		agents, err := DiscoverAgents(rel)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		assert.True(t, filepath.IsAbs(agents[0].Path), "Path %q is not absolute", agents[0].Path)
	})

	t.Run("skips non-YAML files", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "triage.yaml", "agent: agents/triage.md\nrole: triage\nslug: fs-triage\n")
		writeFile(t, dir, "readme.md", "# Harness directory\n")
		writeFile(t, dir, "notes.txt", "some notes\n")

		agents, err := DiscoverAgents(dir)
		require.NoError(t, err)
		require.Len(t, agents, 1)
	})
}
