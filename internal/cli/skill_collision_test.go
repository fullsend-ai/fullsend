package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestWarnRepoSkillCollisions_WarnsForShadowedSkill(t *testing.T) {
	repoDir := t.TempDir()
	repoSkillDir := filepath.Join(repoDir, ".claude", "skills", "code-review")
	require.NoError(t, os.MkdirAll(repoSkillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoSkillDir, "SKILL.md"), []byte("# Repo review"), 0o644))

	harnessSkillDir := filepath.Join(t.TempDir(), "code-review")
	require.NoError(t, os.MkdirAll(harnessSkillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harnessSkillDir, "SKILL.md"), []byte("# Harness review"), 0o644))

	var output bytes.Buffer
	warnRepoSkillCollisions(repoDir, []string{harnessSkillDir}, ui.New(&output))

	assert.Contains(t, output.String(), `Repo skill "code-review" is shadowed by a harness skill of the same name`)
	assert.Contains(t, output.String(), "use a unique skill name to extend it")
	assert.Contains(t, output.String(), "base: harness composition to override it")
}

func TestIsReadableSkillMarkerFS_AcceptsSupportedCasing(t *testing.T) {
	for _, marker := range []string{"SKILL.md", "skill.md", "Skill.md"} {
		t.Run(marker, func(t *testing.T) {
			skillFS := fstest.MapFS{
				marker: &fstest.MapFile{Data: []byte("# Skill")},
			}

			assert.True(t, isReadableSkillMarkerFS(skillFS))
		})
	}
}

func TestWarnRepoSkillCollisions_DoesNotWarnWithoutCollision(t *testing.T) {
	repoDir := t.TempDir()
	projectSkillsDir := filepath.Join(repoDir, ".claude", "skills")
	require.NoError(t, os.MkdirAll(filepath.Join(projectSkillsDir, "repo-only"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectSkillsDir, "repo-only", "SKILL.md"), []byte("# Repo only"), 0o644))
	// A matching harness directory without SKILL.md is not a discoverable skill.
	require.NoError(t, os.MkdirAll(filepath.Join(projectSkillsDir, "markerless-harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectSkillsDir, "markerless-harness", "SKILL.md"), []byte("# Repo skill"), 0o644))
	// A matching repository directory without SKILL.md is not a discoverable skill.
	require.NoError(t, os.MkdirAll(filepath.Join(projectSkillsDir, "markerless-repo"), 0o755))
	// SKILL.md must be a regular file on both sides.
	require.NoError(t, os.MkdirAll(filepath.Join(projectSkillsDir, "directory-marker-harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectSkillsDir, "directory-marker-harness", "SKILL.md"), []byte("# Repo skill"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(projectSkillsDir, "directory-marker-repo", "SKILL.md"), 0o755))

	harnessRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(harnessRoot, "markerless-harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(harnessRoot, "markerless-repo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harnessRoot, "markerless-repo", "SKILL.md"), []byte("# Harness skill"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(harnessRoot, "directory-marker-harness", "SKILL.md"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(harnessRoot, "directory-marker-repo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harnessRoot, "directory-marker-repo", "SKILL.md"), []byte("# Harness skill"), 0o644))
	harnessSkills := []string{
		"",
		filepath.Join(harnessRoot, "harness-only"),
		filepath.Join(harnessRoot, "markerless-harness"),
		filepath.Join(harnessRoot, "markerless-repo"),
		filepath.Join(harnessRoot, "directory-marker-harness"),
		filepath.Join(harnessRoot, "directory-marker-repo"),
	}

	var output bytes.Buffer
	warnRepoSkillCollisions(repoDir, harnessSkills, ui.New(&output))

	assert.Empty(t, output.String())
}

func TestWarnRepoSkillCollisions_DoesNotWarnForUnreadableSkillMarker(t *testing.T) {
	for _, unreadableSide := range []string{"harness", "repo"} {
		t.Run(unreadableSide, func(t *testing.T) {
			repoDir := t.TempDir()
			repoSkillDir := filepath.Join(repoDir, ".claude", "skills", "code-review")
			require.NoError(t, os.MkdirAll(repoSkillDir, 0o755))
			repoMarker := filepath.Join(repoSkillDir, "SKILL.md")
			require.NoError(t, os.WriteFile(repoMarker, []byte("# Repo review"), 0o644))

			harnessSkillDir := filepath.Join(t.TempDir(), "code-review")
			require.NoError(t, os.MkdirAll(harnessSkillDir, 0o755))
			harnessMarker := filepath.Join(harnessSkillDir, "SKILL.md")
			require.NoError(t, os.WriteFile(harnessMarker, []byte("# Harness review"), 0o644))

			unreadableMarker := harnessMarker
			if unreadableSide == "repo" {
				unreadableMarker = repoMarker
			}
			require.NoError(t, os.Chmod(unreadableMarker, 0o000))
			if file, err := os.Open(unreadableMarker); err == nil {
				require.NoError(t, file.Close())
				t.Skip("filesystem does not enforce unreadable file permissions")
			}

			var output bytes.Buffer
			warnRepoSkillCollisions(repoDir, []string{harnessSkillDir}, ui.New(&output))

			assert.Empty(t, output.String())
		})
	}
}

func TestWarnRepoSkillCollisions_DoesNotWarnWithoutProjectSkillsDirectory(t *testing.T) {
	var output bytes.Buffer
	warnRepoSkillCollisions(t.TempDir(), nil, ui.New(&output))

	assert.Empty(t, output.String())
}
