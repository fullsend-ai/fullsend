package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveForge_ScalarOverride(t *testing.T) {
	h := &Harness{
		Agent:      "agents/test.md",
		PreScript:  "scripts/pre-common.sh",
		PostScript: "scripts/post-common.sh",
		Forge: map[string]*ForgeConfig{
			"github": {
				PreScript:  "scripts/pre-gh.sh",
				PostScript: "scripts/post-gh.sh",
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, "scripts/pre-gh.sh", h.PreScript)
	assert.Equal(t, "scripts/post-gh.sh", h.PostScript)
}

func TestResolveForge_ScalarNoOverrideWhenEmpty(t *testing.T) {
	h := &Harness{
		Agent:      "agents/test.md",
		PreScript:  "scripts/pre-common.sh",
		PostScript: "scripts/post-common.sh",
		Forge: map[string]*ForgeConfig{
			"github": {},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, "scripts/pre-common.sh", h.PreScript)
	assert.Equal(t, "scripts/post-common.sh", h.PostScript)
}

func TestResolveForge_SkillsConcat(t *testing.T) {
	h := &Harness{
		Agent:  "agents/test.md",
		Skills: []string{"skills/common-a", "skills/common-b"},
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []string{"skills/gh-specific"},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []string{"skills/common-a", "skills/common-b", "skills/gh-specific"}, h.Skills)
}

func TestResolveForge_NilSkillsInherits(t *testing.T) {
	h := &Harness{
		Agent:  "agents/test.md",
		Skills: []string{"skills/common"},
		Forge: map[string]*ForgeConfig{
			"github": {},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []string{"skills/common"}, h.Skills)
}

func TestResolveForge_EmptySkillsAddsNothing(t *testing.T) {
	h := &Harness{
		Agent:  "agents/test.md",
		Skills: []string{"skills/common"},
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []string{},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []string{"skills/common"}, h.Skills)
}

func TestResolveForge_RunnerEnvMerge(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		RunnerEnv: map[string]string{
			"SHARED_KEY": "shared_val",
			"OVERRIDE":   "base_val",
		},
		Forge: map[string]*ForgeConfig{
			"github": {
				RunnerEnv: map[string]string{
					"OVERRIDE": "forge_val",
					"GH_TOKEN": "${GH_TOKEN}",
				},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, "shared_val", h.RunnerEnv["SHARED_KEY"])
	assert.Equal(t, "forge_val", h.RunnerEnv["OVERRIDE"])
	assert.Equal(t, "${GH_TOKEN}", h.RunnerEnv["GH_TOKEN"])
}

func TestResolveForge_RunnerEnvMerge_NilTopLevel(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": {
				RunnerEnv: map[string]string{
					"GH_TOKEN": "${GH_TOKEN}",
				},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, "${GH_TOKEN}", h.RunnerEnv["GH_TOKEN"])
}

func TestResolveForge_NilRunnerEnvInherits(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		RunnerEnv: map[string]string{
			"SHARED": "val",
		},
		Forge: map[string]*ForgeConfig{
			"github": {},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, map[string]string{"SHARED": "val"}, h.RunnerEnv)
}

func TestResolveForge_ValidationLoopReplace(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		ValidationLoop: &ValidationLoop{
			Script:        "scripts/validate-common.sh",
			MaxIterations: 3,
		},
		Forge: map[string]*ForgeConfig{
			"github": {
				ValidationLoop: &ValidationLoop{
					Script:        "scripts/validate-gh.sh",
					MaxIterations: 1,
				},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	require.NotNil(t, h.ValidationLoop)
	assert.Equal(t, "scripts/validate-gh.sh", h.ValidationLoop.Script)
	assert.Equal(t, 1, h.ValidationLoop.MaxIterations)
}

func TestResolveForge_ValidationLoopNilInherits(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		ValidationLoop: &ValidationLoop{
			Script:        "scripts/validate.sh",
			MaxIterations: 2,
		},
		Forge: map[string]*ForgeConfig{
			"github": {},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	require.NotNil(t, h.ValidationLoop)
	assert.Equal(t, "scripts/validate.sh", h.ValidationLoop.Script)
}

func TestResolveForge_NoForgeSection(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		PreScript: "scripts/pre.sh",
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, "scripts/pre.sh", h.PreScript)
}

func TestResolveForge_EmptyPlatform(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "scripts/gh.sh"},
		},
	}

	require.NoError(t, h.ResolveForge(""))
	assert.NotNil(t, h.Forge, "forge should not be consumed when platform is empty")
}

func TestResolveForge_UnknownPlatform(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "scripts/gh.sh"},
		},
	}

	err := h.ResolveForge("bitbucket")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bitbucket")
	assert.Contains(t, err.Error(), "not valid")
}

func TestResolveForge_ValidPlatformNotConfigured(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "scripts/gh.sh"},
		},
	}

	err := h.ResolveForge("gitlab")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gitlab")
	assert.Contains(t, err.Error(), "not configured")
}

func TestResolveForge_ForgeConsumed(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "scripts/gh.sh"},
			"gitlab": {PreScript: "scripts/gl.sh"},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Nil(t, h.Forge)
}

func TestValidate_ForgeUnrecognizedKey(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"gihub": {PreScript: "scripts/gh.sh"},
		},
	}

	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized key")
	assert.Contains(t, err.Error(), "gihub")
	assert.Contains(t, err.Error(), "valid: github, gitlab")
}

func TestValidate_ForgeScriptURL(t *testing.T) {
	t.Run("pre_script URL", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			Forge: map[string]*ForgeConfig{
				"github": {PreScript: "https://example.com/scripts/pre.sh"},
			},
		}
		err := h.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "forge.github.pre_script must be a local path")
	})

	t.Run("post_script URL", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			Forge: map[string]*ForgeConfig{
				"gitlab": {PostScript: "https://example.com/scripts/post.sh"},
			},
		}
		err := h.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "forge.gitlab.post_script must be a local path")
	})

	t.Run("validation_loop.script URL", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			Forge: map[string]*ForgeConfig{
				"github": {
					ValidationLoop: &ValidationLoop{
						Script:        "https://example.com/scripts/validate.sh",
						MaxIterations: 1,
					},
				},
			},
		}
		err := h.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "forge.github.validation_loop.script must be a local path")
	})

	t.Run("validation_loop missing script", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			Forge: map[string]*ForgeConfig{
				"github": {
					ValidationLoop: &ValidationLoop{
						MaxIterations: 1,
					},
				},
			},
		}
		err := h.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "forge.github.validation_loop.script is required")
	})
}

func TestValidate_ForgeValidConfig(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": {
				PreScript:  "scripts/pre-gh.sh",
				PostScript: "scripts/post-gh.sh",
				Skills:     []string{"skills/gh-issue"},
				RunnerEnv:  map[string]string{"GH_TOKEN": "${GH_TOKEN}"},
				ValidationLoop: &ValidationLoop{
					Script:        "scripts/validate-gh.sh",
					MaxIterations: 2,
				},
			},
			"gitlab": {
				PreScript: "scripts/pre-gl.sh",
				Skills:    []string{"skills/gl-issue"},
				RunnerEnv: map[string]string{"GITLAB_TOKEN": "${GITLAB_TOKEN}"},
			},
		},
	}
	require.NoError(t, h.Validate())
}

func TestValidate_ForgeNilConfig(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": nil,
		},
	}
	require.NoError(t, h.Validate())
}

func TestValidate_ForgeSkillURLWithoutHash(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []string{"https://example.com/skills/summarize.md"},
			},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge.github.skills[0]")
	assert.Contains(t, err.Error(), "integrity hash")
}

func TestValidate_ForgeSkillURLWithHash(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []string{"https://example.com/skills/summarize.md#sha256=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"},
			},
		},
	}
	require.NoError(t, h.Validate())
}

func TestLoad_WithForgeSection(t *testing.T) {
	content := `
agent: agents/test.md
pre_script: scripts/pre-common.sh
skills:
  - skills/common
runner_env:
  SHARED: shared_val
forge:
  github:
    pre_script: scripts/pre-gh.sh
    skills:
      - skills/gh-specific
    runner_env:
      GH_TOKEN: "${GH_TOKEN}"
  gitlab:
    pre_script: scripts/pre-gl.sh
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)

	require.NotNil(t, h.Forge)
	require.Contains(t, h.Forge, "github")
	require.Contains(t, h.Forge, "gitlab")

	assert.Equal(t, "scripts/pre-gh.sh", h.Forge["github"].PreScript)
	assert.Equal(t, []string{"skills/gh-specific"}, h.Forge["github"].Skills)
	assert.Equal(t, "${GH_TOKEN}", h.Forge["github"].RunnerEnv["GH_TOKEN"])
	assert.Equal(t, "scripts/pre-gl.sh", h.Forge["gitlab"].PreScript)
}

func TestLoad_WithoutForgeSection(t *testing.T) {
	content := `
agent: agents/test.md
pre_script: scripts/pre.sh
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)

	assert.Nil(t, h.Forge)
	assert.Equal(t, "scripts/pre.sh", h.PreScript)
}
