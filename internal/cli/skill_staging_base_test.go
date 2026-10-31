package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// A user's .fullsend overlays the base review harness's pr-review skill
// with file overrides only, without a skills/ directory of its own. The
// staged tree is the base's files with the child's overrides applied
// (#6158).
func TestStageRunSkills_BaseOverlayAppliesChildOverridesToBaseSkill(t *testing.T) {
	dotfs := realTempDir(t)

	// The base skill sits in the fetch cache, as a fetched URL base's does.
	hashDir, err := fetch.CachePath(dotfs, strings.Repeat("e", 64))
	require.NoError(t, err)
	writeTree(t, filepath.Join(hashDir, "tree"), map[string]string{
		"SKILL.md":                      "---\nname: pr-review\n---\nbody\n",
		"sub-agents/security.md":        "---\nname: security\n---\nupstream security\n",
		"sub-agents/user-experience.md": "---\nname: user-experience\n---\nux\n",
	})
	baseSkill, err := fetch.CacheNamedSymlink(filepath.Join(hashDir, "tree"), "pr-review")
	require.NoError(t, err)
	before := snapshotTree(t, filepath.Join(hashDir, "tree"))

	writeTree(t, dotfs, map[string]string{
		"base.yaml":               "agent: agents/base.md\nrole: review\nskills:\n  - " + baseSkill + "\n",
		"personas/rbac-review.md": "---\nname: rbac-review\n---\nlocal rbac\n",
		"review.yaml": `base: ./base.yaml
role: review
skills:
  - skills/pr-review:
      sub-agents/user-experience.md: null
      sub-agents/rbac-review.md: personas/rbac-review.md
`,
	})

	h, _, err := harness.LoadWithBase(context.Background(), filepath.Join(dotfs, "review.yaml"), harness.ComposeOpts{WorkspaceRoot: dotfs})
	require.NoError(t, err)
	require.NoError(t, h.ResolveRelativeTo(dotfs))

	runDir := realTempDir(t)
	dirs, err := stageRunSkills(h, runDir, dotfs, "review", ui.New(io.Discard))
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(runDir, "skills", "0", "pr-review")}, dirs)

	got := snapshotTree(t, dirs[0])
	assert.Contains(t, got["SKILL.md"], "body", "the base's files are staged")
	assert.Contains(t, got["sub-agents/security.md"], "upstream security")
	assert.Contains(t, got["sub-agents/rbac-review.md"], "local rbac")
	assert.NotContains(t, got, "sub-agents/user-experience.md")
	assert.Equal(t, before, snapshotTree(t, filepath.Join(hashDir, "tree")), "the cache is not written")

	data, err := os.ReadFile(filepath.Join(runDir, personasFileName))
	require.NoError(t, err)
	var m personaManifest
	require.NoError(t, json.Unmarshal(data, &m))
	origins := map[string]string{}
	for _, p := range m.Personas {
		origins[p.Name] = p.Origin
	}
	assert.Equal(t, map[string]string{"rbac-review": originAdded, "security": originUpstream}, origins)
	assert.Equal(t, []removedPersona{{Skill: "pr-review", Path: "sub-agents/user-experience.md"}}, m.Removed)
}

// An override source outside the fullsend dir is still refused when the
// skill source is inherited from the base.
func TestStageRunSkills_BaseOverlayOverrideOutsideFullsendDirRefused(t *testing.T) {
	dotfs := realTempDir(t)
	baseSkill := filepath.Join(dotfs, ".fullsend-cache", "pr-review")
	writeTree(t, baseSkill, map[string]string{"SKILL.md": "---\nname: pr-review\n---\n"})
	outside := filepath.Join(realTempDir(t), "x.md")
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o644))

	h := &harness.Harness{Skills: []harness.SkillEntry{{
		Source:    baseSkill,
		Overrides: map[string]*string{"sub-agents/x.md": &outside},
	}}}
	_, err := stageRunSkills(h, realTempDir(t), dotfs, "review", ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is outside")
}

// On the lock-file path the base skill's lock entry rewrites the inherited
// slot's source to the same cached tree and keeps the child's overrides.
func TestResolveFromLock_InheritedBaseSkillKeepsChildOverrides(t *testing.T) {
	dotfs := realTempDir(t)
	files := map[string][]byte{
		"SKILL.md":                      []byte("---\nname: pr-review\n---\n"),
		"sub-agents/user-experience.md": []byte("---\nname: user-experience\n---\n"),
	}
	skillFileURL := "https://raw.githubusercontent.com/fullsend-ai/agents/abc123/skills/pr-review/SKILL.md"
	_, err := fetch.CachePutDir(dotfs, skillFileURL, files)
	require.NoError(t, err)
	entry := &lock.HarnessLock{Dependencies: []lock.DependencyEntry{{
		Field: "skills[0]", URL: skillFileURL, SHA256: fetch.ComputeTreeHash(files), Type: "directory",
	}}}

	rbac := filepath.Join(dotfs, "personas", "rbac-review.md")
	writeTree(t, dotfs, map[string]string{"personas/rbac-review.md": "---\nname: rbac-review\n---\n"})
	h := &harness.Harness{
		Skills: []harness.SkillEntry{{
			Source: filepath.Join(dotfs, ".fullsend-cache", "pr-review"),
			Overrides: map[string]*string{
				"sub-agents/user-experience.md": nil,
				"sub-agents/rbac-review.md":     &rbac,
			},
		}},
		AllowedRemoteResources: []string{"https://raw.githubusercontent.com/fullsend-ai/"},
	}
	_, err = resolveFromLock(h, entry, dotfs, nil, ui.New(io.Discard))
	require.NoError(t, err)
	require.Len(t, h.Skills, 1)
	assert.Equal(t, "pr-review", filepath.Base(h.Skills[0].Source))
	assert.Len(t, h.Skills[0].Overrides, 2)

	runDir := realTempDir(t)
	dirs, err := stageRunSkills(h, runDir, dotfs, "review", ui.New(io.Discard))
	require.NoError(t, err)
	got := snapshotTree(t, dirs[0])
	assert.Contains(t, got, "sub-agents/rbac-review.md")
	assert.NotContains(t, got, "sub-agents/user-experience.md")
}
