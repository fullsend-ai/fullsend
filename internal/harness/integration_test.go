package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadWithBase_BackwardCompat verifies that a harness with no base, no forge,
// no role, and no slug loads identically to Load().
func TestLoadWithBase_BackwardCompat(t *testing.T) {
	dir := t.TempDir()

	// Create the agent .md file so Validate doesn't fail on missing agent name.
	agentDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "test.md"), []byte(""), 0644))

	path := writeTestHarness(t, dir, "simple.yaml", `
agent: agents/test.md
role: test
timeout_minutes: 5
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)

	assert.Equal(t, "agents/test.md", h.Agent)
	assert.Equal(t, 5, h.TimeoutMinutes)
	assert.Empty(t, h.Base, "base field should be empty (no base)")
	assert.Nil(t, deps, "baseDeps should be nil when no base is used")
	assert.Equal(t, "test", h.Role)
	assert.Empty(t, h.Slug)
	assert.Nil(t, h.Forge)
}

// TestLoadWithBase_BaseWithForgeAndIdentity tests the full pipeline: a base harness
// provides shared defaults (agent, model, skills, runner_env), a child harness
// references the base with its own role/slug, and a forge.github block adds overrides.
func TestLoadWithBase_BaseWithForgeAndIdentity(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/shared.md
role: test
model: sonnet
skills:
  - base-skill-1
  - base-skill-2
runner_env:
  SHARED_KEY: shared-val
  OVERRIDE_KEY: base-val
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
role: triage
slug: fullsend-triage
skills:
  - child-skill-1
forge:
  github:
    pre_script: scripts/gh-pre.sh
    skills:
      - gh-forge-skill
    runner_env:
      OVERRIDE_KEY: forge-val
      GH_TOKEN: "${GH_TOKEN}"
`)

	h, deps, err := LoadWithBase(context.Background(), path, ComposeOpts{
		ForgePlatform: "github",
	})
	require.NoError(t, err)

	// Role and slug come from child.
	assert.Equal(t, "triage", h.Role)
	assert.Equal(t, "fullsend-triage", h.Slug)

	// Agent and model inherited from base (child does not override them).
	assert.Equal(t, "agents/shared.md", h.Agent)
	assert.Equal(t, "sonnet", h.Model)

	// Skills = base skills + child skills + forge.github skills (concatenated in order).
	assert.Equal(t, []string{
		"base-skill-1", "base-skill-2", // from base top-level
		"child-skill-1",  // from child top-level
		"gh-forge-skill", // from forge.github
	}, h.Skills)

	// RunnerEnv = base env merged with forge env (forge keys win).
	// Note: child top-level has no runner_env, so base runner_env is inherited,
	// then forge runner_env is merged on top.
	assert.Equal(t, "shared-val", h.RunnerEnv["SHARED_KEY"])
	assert.Equal(t, "forge-val", h.RunnerEnv["OVERRIDE_KEY"])
	assert.Equal(t, "${GH_TOKEN}", h.RunnerEnv["GH_TOKEN"])

	// PreScript = forge.github.pre_script (overrides base and child top-level).
	assert.Equal(t, "scripts/gh-pre.sh", h.PreScript)

	// Forge map is nil (consumed by ResolveForge).
	assert.Nil(t, h.Forge)

	// Base field is empty (consumed by LoadWithBase).
	assert.Empty(t, h.Base)

	// baseDeps is nil (local base, no URL).
	assert.Nil(t, deps)
}

// TestLoadWithBase_BaseWithForgeSkillsConcatenation specifically tests that skills
// from base top-level, child top-level, base forge block, and child forge block
// concatenate in the correct order.
func TestLoadWithBase_BaseWithForgeSkillsConcatenation(t *testing.T) {
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
skills:
  - base-s1
forge:
  github:
    skills:
      - base-forge-s1
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
skills:
  - child-s1
forge:
  github:
    skills:
      - child-forge-s1
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		ForgePlatform: "github",
	})
	require.NoError(t, err)

	// Order: base-top + child-top + merged-forge (base-forge + child-forge).
	// The forge blocks are merged first (base-forge + child-forge via mergeForgeBlocks),
	// then ResolveForge appends the merged forge skills to the already-concatenated
	// top-level skills (base-top + child-top).
	assert.Equal(t, []string{
		"base-s1",        // base top-level
		"child-s1",       // child top-level
		"base-forge-s1",  // base forge.github (merged into child forge)
		"child-forge-s1", // child forge.github
	}, h.Skills)
}

// TestLoadWithBase_NoBaseIdenticalToLoadWithOpts verifies that loading the same
// harness (no base field) through both LoadWithOpts and LoadWithBase produces
// identical results.
func TestLoadWithBase_NoBaseIdenticalToLoadWithOpts(t *testing.T) {
	dir := t.TempDir()

	content := `
agent: agents/test.md
role: test
model: opus
skills:
  - skill-a
  - skill-b
runner_env:
  KEY1: val1
timeout_minutes: 10
pre_script: scripts/pre.sh
forge:
  github:
    pre_script: scripts/gh-pre.sh
    skills:
      - gh-skill
    runner_env:
      GH_KEY: gh-val
`

	pathA := writeTestHarness(t, dir, "harness-a.yaml", content)
	pathB := writeTestHarness(t, dir, "harness-b.yaml", content)

	hOpts, err := LoadWithOpts(pathA, LoadOpts{ForgePlatform: "github"})
	require.NoError(t, err)

	hBase, deps, err := LoadWithBase(context.Background(), pathB, ComposeOpts{
		ForgePlatform: "github",
	})
	require.NoError(t, err)

	// deps should be nil since there is no base.
	assert.Nil(t, deps)

	// Compare field by field for clarity on failures.
	assert.Equal(t, hOpts.Agent, hBase.Agent)
	assert.Equal(t, hOpts.Model, hBase.Model)
	assert.Equal(t, hOpts.Skills, hBase.Skills)
	assert.Equal(t, hOpts.RunnerEnv, hBase.RunnerEnv)
	assert.Equal(t, hOpts.TimeoutMinutes, hBase.TimeoutMinutes)
	assert.Equal(t, hOpts.PreScript, hBase.PreScript)
	assert.Equal(t, hOpts.PostScript, hBase.PostScript)
	assert.Equal(t, hOpts.Role, hBase.Role)
	assert.Equal(t, hOpts.Slug, hBase.Slug)
	assert.Equal(t, hOpts.Image, hBase.Image)
	assert.Equal(t, hOpts.Policy, hBase.Policy)
	assert.Equal(t, hOpts.Plugins, hBase.Plugins)
	assert.Equal(t, hOpts.Providers, hBase.Providers)
	assert.Equal(t, hOpts.ValidationLoop, hBase.ValidationLoop)
	assert.Equal(t, hOpts.Security, hBase.Security)
	assert.Equal(t, hOpts.HostFiles, hBase.HostFiles)
	assert.Equal(t, hOpts.APIServers, hBase.APIServers)

	// Forge should be nil on both (consumed by ResolveForge).
	assert.Nil(t, hOpts.Forge)
	assert.Nil(t, hBase.Forge)
}
