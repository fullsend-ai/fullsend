package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
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

func TestResolveForge_PolicyOverride(t *testing.T) {
	h := &Harness{
		Agent:  "agents/test.md",
		Policy: "policies/default.yaml",
		Forge: map[string]*ForgeConfig{
			"gitlab": {Policy: "policies/gitlab.yaml"},
		},
	}

	require.NoError(t, h.ResolveForge("gitlab"))
	assert.Equal(t, "policies/gitlab.yaml", h.Policy)
}

func TestResolveForge_PolicyNotOverriddenWhenEmpty(t *testing.T) {
	h := &Harness{
		Agent:  "agents/test.md",
		Policy: "policies/default.yaml",
		Forge: map[string]*ForgeConfig{
			"github": {},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, "policies/default.yaml", h.Policy)
}

func TestResolveForge_SkillsConcat(t *testing.T) {
	h := &Harness{
		Agent:  "agents/test.md",
		Skills: []SkillEntry{{Source: "skills/common-a"}, {Source: "skills/common-b"}},
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []SkillEntry{{Source: "skills/gh-specific"}},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []string{"skills/common-a", "skills/common-b", "skills/gh-specific"}, SkillSources(h.Skills))
}

// TestResolveForge_SkillsOverrideByBasename verifies that a forge skill
// whose basename matches a top-level skill replaces it instead of producing
// a duplicate (see #5408).
func TestResolveForge_SkillsOverrideByBasename(t *testing.T) {
	h := &Harness{
		Agent:  "agents/test.md",
		Skills: []SkillEntry{{Source: "/cache/code-implementation"}, {Source: "skills/common-b"}},
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []SkillEntry{{Source: "skills/code-implementation"}},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []string{"skills/code-implementation", "skills/common-b"}, SkillSources(h.Skills))
}

func TestResolveForge_NilSkillsInherits(t *testing.T) {
	h := &Harness{
		Agent:  "agents/test.md",
		Skills: []SkillEntry{{Source: "skills/common"}},
		Forge: map[string]*ForgeConfig{
			"github": {},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []string{"skills/common"}, SkillSources(h.Skills))
}

func TestResolveForge_EmptySkillsAddsNothing(t *testing.T) {
	h := &Harness{
		Agent:  "agents/test.md",
		Skills: []SkillEntry{{Source: "skills/common"}},
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []SkillEntry{},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []string{"skills/common"}, SkillSources(h.Skills))
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
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"gihub": {PreScript: "scripts/gh.sh"},
		},
	}

	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized key")
	assert.Contains(t, err.Error(), "gihub")
	assert.Contains(t, err.Error(), "valid: github, gitlab, jira")
}

func TestValidateForge_JiraBlock(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"jira": {PreScript: "scripts/jira.sh"},
		},
	}

	// validateForge should accept a jira block.
	require.NoError(t, h.validateForge())

	// ResolveForge should merge it into the harness.
	require.NoError(t, h.ResolveForge("jira"))
	assert.Nil(t, h.Forge)
	assert.Equal(t, "scripts/jira.sh", h.PreScript)
}

func TestValidate_ForgeScriptURL(t *testing.T) {
	t.Run("pre_script URL", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			Role:  "test",
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
			Role:  "test",
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
			Role:  "test",
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

	t.Run("validation_loop.schema URL", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			Role:  "test",
			Forge: map[string]*ForgeConfig{
				"github": {
					ValidationLoop: &ValidationLoop{
						Script:        "scripts/validate.sh",
						Schema:        "https://evil.com/schema.json",
						MaxIterations: 1,
					},
				},
			},
		}
		err := h.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "forge.github.validation_loop.schema must be a local path")
	})

	t.Run("validation_loop missing script", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			Role:  "test",
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
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				PreScript:  "scripts/pre-gh.sh",
				PostScript: "scripts/post-gh.sh",
				Skills:     []SkillEntry{{Source: "skills/gh-issue"}},
				RunnerEnv:  map[string]string{"GH_TOKEN": "${GH_TOKEN}"},
				ValidationLoop: &ValidationLoop{
					Script:        "scripts/validate-gh.sh",
					MaxIterations: 2,
				},
			},
			"gitlab": {
				PreScript: "scripts/pre-gl.sh",
				Skills:    []SkillEntry{{Source: "skills/gl-issue"}},
				RunnerEnv: map[string]string{"GITLAB_TOKEN": "${GITLAB_TOKEN}"},
			},
		},
	}
	require.NoError(t, h.Validate())
}

func TestValidate_ForgeNilConfig(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": nil,
		},
	}
	require.NoError(t, h.Validate())
}

func TestValidate_ForgePolicyURLWithoutHash(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				Policy: "https://example.com/policies/sandbox.yaml",
			},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge.github.policy")
	assert.Contains(t, err.Error(), "integrity hash")
}

func TestValidate_ForgePolicyURLWithHash(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				Policy: "https://example.com/policies/sandbox.yaml#sha256=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			},
		},
	}
	require.NoError(t, h.Validate())
}

func TestValidate_ForgePolicyLocalPath(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"gitlab": {
				Policy: "policies/triage-gitlab.yaml",
			},
		},
	}
	require.NoError(t, h.Validate())
}

func TestValidate_ForgeSkillURLWithoutHash(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []SkillEntry{{Source: "https://example.com/skills/summarize.md"}},
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
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []SkillEntry{{Source: "https://example.com/skills/summarize.md#sha256=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}},
			},
		},
	}
	require.NoError(t, h.Validate())
}

func TestResolveForge_MergesEnv(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &EnvConfig{
			Runner:  map[string]string{"SHARED": "base"},
			Sandbox: map[string]string{"SHARED_SB": "base"},
		},
		Forge: map[string]*ForgeConfig{
			"github": {
				Env: &EnvConfig{
					Runner:  map[string]string{"GH_TOKEN": "tok"},
					Sandbox: map[string]string{"PR_URL": "url"},
				},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))

	require.NotNil(t, h.Env)
	assert.Equal(t, "base", h.Env.Runner["SHARED"])
	assert.Equal(t, "tok", h.Env.Runner["GH_TOKEN"])
	assert.Equal(t, "base", h.Env.Sandbox["SHARED_SB"])
	assert.Equal(t, "url", h.Env.Sandbox["PR_URL"])
}

func TestResolveForge_EnvForgeOverridesTopLevel(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &EnvConfig{
			Runner: map[string]string{"KEY": "top"},
		},
		Forge: map[string]*ForgeConfig{
			"github": {
				Env: &EnvConfig{
					Runner: map[string]string{"KEY": "forge"},
				},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, "forge", h.Env.Runner["KEY"])
}

func TestResolveForge_EnvInheritedWhenForgeNil(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &EnvConfig{
			Runner:  map[string]string{"INHERITED": "yes"},
			Sandbox: map[string]string{"ALSO": "inherited"},
		},
		Forge: map[string]*ForgeConfig{
			"github": {},
		},
	}

	require.NoError(t, h.ResolveForge("github"))

	require.NotNil(t, h.Env)
	assert.Equal(t, "yes", h.Env.Runner["INHERITED"])
	assert.Equal(t, "inherited", h.Env.Sandbox["ALSO"])
}

func TestLoad_WithForgeSection(t *testing.T) {
	content := `
agent: agents/test.md
role: test
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
	assert.Equal(t, []string{"skills/gh-specific"}, SkillSources(h.Forge["github"].Skills))
	assert.Equal(t, "${GH_TOKEN}", h.Forge["github"].RunnerEnv["GH_TOKEN"])
	assert.Equal(t, "scripts/pre-gl.sh", h.Forge["gitlab"].PreScript)
}

func TestForgeConfig_EnvParsesFromYAML(t *testing.T) {
	yaml := `
agent: agents/test.md
role: test
forge:
  github:
    env:
      runner:
        GH_TOKEN: "${GH_TOKEN}"
      sandbox:
        GITHUB_PR_URL: "${GITHUB_PR_URL}"
`
	h, err := parseRaw([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, h.Forge["github"])
	require.NotNil(t, h.Forge["github"].Env)
	assert.Equal(t, map[string]string{"GH_TOKEN": "${GH_TOKEN}"}, h.Forge["github"].Env.Runner)
	assert.Equal(t, map[string]string{"GITHUB_PR_URL": "${GITHUB_PR_URL}"}, h.Forge["github"].Env.Sandbox)
}

func TestResolveForge_HostFilesMerge(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		HostFiles: []HostFile{
			{Src: "env/common.env", Dest: "/run/env/common.env"},
		},
		Forge: map[string]*ForgeConfig{
			"github": {
				HostFiles: []HostFile{
					{Src: "env/github/triage.env", Dest: "/run/env/forge.env"},
				},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	require.Len(t, h.HostFiles, 2)
	assert.Equal(t, "env/common.env", h.HostFiles[0].Src)
	assert.Equal(t, "/run/env/common.env", h.HostFiles[0].Dest)
	assert.Equal(t, "env/github/triage.env", h.HostFiles[1].Src)
	assert.Equal(t, "/run/env/forge.env", h.HostFiles[1].Dest)
}

func TestResolveForge_HostFilesOverrideSameDest(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		HostFiles: []HostFile{
			{Src: "env/default.env", Dest: "/run/env/forge.env"},
		},
		Forge: map[string]*ForgeConfig{
			"gitlab": {
				HostFiles: []HostFile{
					{Src: "env/gitlab/triage.env", Dest: "/run/env/forge.env"},
				},
			},
		},
	}

	require.NoError(t, h.ResolveForge("gitlab"))
	require.Len(t, h.HostFiles, 1)
	assert.Equal(t, "env/gitlab/triage.env", h.HostFiles[0].Src)
	assert.Equal(t, "/run/env/forge.env", h.HostFiles[0].Dest)
}

func TestResolveForge_HostFilesNilInherits(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		HostFiles: []HostFile{
			{Src: "env/common.env", Dest: "/run/env/common.env"},
		},
		Forge: map[string]*ForgeConfig{
			"github": {},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	require.Len(t, h.HostFiles, 1)
	assert.Equal(t, "env/common.env", h.HostFiles[0].Src)
}

func TestResolveForge_HostFilesNilTopLevel(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": {
				HostFiles: []HostFile{
					{Src: "env/github.env", Dest: "/run/env/github.env"},
				},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	require.Len(t, h.HostFiles, 1)
	assert.Equal(t, "env/github.env", h.HostFiles[0].Src)
}

func TestValidate_ForgeHostFileMissingSrc(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				HostFiles: []HostFile{
					{Dest: "/run/env/forge.env"},
				},
			},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge.github.host_files[0]: src is required")
}

func TestValidate_ForgeHostFileMissingDest(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				HostFiles: []HostFile{
					{Src: "env/github.env"},
				},
			},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge.github.host_files[0]: dest is required")
}

func TestValidate_ForgeHostFileSrcURL(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				HostFiles: []HostFile{
					{Src: "https://evil.com/env.file", Dest: "/run/env"},
				},
			},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge.github.host_files[0].src must be a local path")
}

func TestValidate_ForgeHostFileValid(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				HostFiles: []HostFile{
					{Src: "env/github/triage.env", Dest: "/run/env/forge.env"},
				},
			},
		},
	}
	require.NoError(t, h.Validate())
}

func TestForgeConfig_HostFilesParsesFromYAML(t *testing.T) {
	content := `
agent: agents/test.md
role: test
forge:
  gitlab:
    host_files:
      - src: env/gitlab/triage.env
        dest: /run/env/forge.env
        optional: true
`
	h, err := parseRaw([]byte(content))
	require.NoError(t, err)
	require.NotNil(t, h.Forge["gitlab"])
	require.Len(t, h.Forge["gitlab"].HostFiles, 1)
	assert.Equal(t, "env/gitlab/triage.env", h.Forge["gitlab"].HostFiles[0].Src)
	assert.Equal(t, "/run/env/forge.env", h.Forge["gitlab"].HostFiles[0].Dest)
	assert.True(t, h.Forge["gitlab"].HostFiles[0].Optional)
}

func TestForgeConfig_PolicyParsesFromYAML(t *testing.T) {
	content := `
agent: agents/test.md
role: test
policy: policies/triage.yaml
forge:
  gitlab:
    policy: policies/triage-gitlab.yaml
`
	h, err := parseRaw([]byte(content))
	require.NoError(t, err)
	require.NotNil(t, h.Forge["gitlab"])
	assert.Equal(t, "policies/triage-gitlab.yaml", h.Forge["gitlab"].Policy)
}

func TestResolveForge_ProvidersConcat(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		Providers: []string{"providers/vertex-ai.yaml"},
		Forge: map[string]*ForgeConfig{
			"github": {
				Providers: []string{"providers/github-code.yaml"},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []string{"providers/vertex-ai.yaml", "providers/github-code.yaml"}, h.Providers)
}

func TestResolveForge_ProvidersNilInherits(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		Providers: []string{"providers/vertex-ai.yaml"},
		Forge: map[string]*ForgeConfig{
			"github": {},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []string{"providers/vertex-ai.yaml"}, h.Providers)
}

func TestResolveForge_ProvidersNilTopLevel(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": {
				Providers: []string{"providers/github-code.yaml"},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []string{"providers/github-code.yaml"}, h.Providers)
}

func TestResolveForge_OpenShellConcat(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		OpenShell: &OpenShellConfig{Profiles: []string{"profiles/vertex-ai.yaml"}},
		Forge: map[string]*ForgeConfig{
			"github": {
				OpenShell: &OpenShellConfig{Profiles: []string{"profiles/github-code.yaml"}},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []string{"profiles/vertex-ai.yaml", "profiles/github-code.yaml"}, h.OpenShell.Profiles)
}

func TestResolveForge_OpenShellNilInherits(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		OpenShell: &OpenShellConfig{Profiles: []string{"profiles/vertex-ai.yaml"}},
		Forge: map[string]*ForgeConfig{
			"github": {},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	require.NotNil(t, h.OpenShell)
	assert.Equal(t, []string{"profiles/vertex-ai.yaml"}, h.OpenShell.Profiles)
}

func TestResolveForge_OpenShellNilTopLevel(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Forge: map[string]*ForgeConfig{
			"github": {
				OpenShell: &OpenShellConfig{Profiles: []string{"profiles/github-code.yaml"}},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	require.NotNil(t, h.OpenShell)
	assert.Equal(t, []string{"profiles/github-code.yaml"}, h.OpenShell.Profiles)
}

func TestValidate_ForgeProviderURLWithoutHash(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				Providers: []string{"https://example.com/providers/code.yaml"},
			},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge.github.providers[0]")
	assert.Contains(t, err.Error(), "integrity hash")
}

func TestValidate_ForgeProviderURLWithHash(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				Providers: []string{"https://example.com/providers/code.yaml#sha256=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"},
			},
		},
	}
	require.NoError(t, h.Validate())
}

func TestValidate_ForgeOpenShellProfileURLWithoutHash(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				OpenShell: &OpenShellConfig{
					Profiles: []string{"https://example.com/profiles/code.yaml"},
				},
			},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge.github.openshell.profiles[0]")
	assert.Contains(t, err.Error(), "integrity hash")
}

func TestValidate_ForgeOpenShellProfileURLWithHash(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				OpenShell: &OpenShellConfig{
					Profiles: []string{"https://example.com/profiles/code.yaml#sha256=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"},
				},
			},
		},
	}
	require.NoError(t, h.Validate())
}

func TestForgeConfig_ProvidersAndOpenShellParseFromYAML(t *testing.T) {
	content := `
agent: agents/test.md
role: test
forge:
  github:
    providers:
      - providers/github-code.yaml
    openshell:
      profiles:
        - profiles/github-code.yaml
  gitlab:
    providers:
      - providers/gitlab-code.yaml
    openshell:
      profiles:
        - profiles/gitlab-code.yaml
`
	h, err := parseRaw([]byte(content))
	require.NoError(t, err)
	require.NotNil(t, h.Forge["github"])
	assert.Equal(t, []string{"providers/github-code.yaml"}, h.Forge["github"].Providers)
	require.NotNil(t, h.Forge["github"].OpenShell)
	assert.Equal(t, []string{"profiles/github-code.yaml"}, h.Forge["github"].OpenShell.Profiles)
	require.NotNil(t, h.Forge["gitlab"])
	assert.Equal(t, []string{"providers/gitlab-code.yaml"}, h.Forge["gitlab"].Providers)
	require.NotNil(t, h.Forge["gitlab"].OpenShell)
	assert.Equal(t, []string{"profiles/gitlab-code.yaml"}, h.Forge["gitlab"].OpenShell.Profiles)
}

func TestLoad_WithoutForgeSection(t *testing.T) {
	content := `
agent: agents/test.md
role: test
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

// --- Overlay tests ---

func TestValidateOverlays_EmptyWhenRejected(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Overlays: []OverlayEntry{
			{When: "", ForgeConfig: ForgeConfig{PreScript: "scripts/pre.sh"}},
		},
	}
	err := h.validateOverlays()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlays[0].when is required")
}

func TestValidateOverlays_NonBoolCELRejected(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Overlays: []OverlayEntry{
			{When: "event.source.system", ForgeConfig: ForgeConfig{PreScript: "scripts/pre.sh"}},
		},
	}
	err := h.validateOverlays()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must evaluate to bool")
}

func TestValidateOverlays_ValidCELAccepted(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Overlays: []OverlayEntry{
			{When: `event.source.system == "github"`, ForgeConfig: ForgeConfig{PreScript: "scripts/pre.sh"}},
		},
	}
	err := h.validateOverlays()
	require.NoError(t, err)
}

func TestValidateOverlays_RuntimeForgeAccepted(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Overlays: []OverlayEntry{
			{When: `runtime.forge == "github"`, ForgeConfig: ForgeConfig{PreScript: "scripts/pre.sh"}},
		},
	}
	err := h.validateOverlays()
	require.NoError(t, err)
}

func TestValidateOverlays_ConfigVariableAccepted(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Overlays: []OverlayEntry{
			{When: `config.tracker == "jira" && runtime.forge == "github"`, ForgeConfig: ForgeConfig{PreScript: "scripts/pre.sh"}},
		},
	}
	err := h.validateOverlays()
	require.NoError(t, err)
}

func TestValidateOverlays_InvalidScriptPathRejected(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Overlays: []OverlayEntry{
			{When: `event.source.system == "github"`, ForgeConfig: ForgeConfig{PreScript: "https://example.com/pre.sh"}},
		},
	}
	err := h.validateOverlays()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlays[0].pre_script must be a local path, not a URL")
}

func TestValidateOverlays_MutualExclusionWithForge(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "scripts/pre-gh.sh"},
		},
		Overlays: []OverlayEntry{
			{When: `event.source.system == "github"`, ForgeConfig: ForgeConfig{PreScript: "scripts/pre.sh"}},
		},
	}
	err := h.validateOverlays()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forge and overlays cannot coexist")
}

func TestValidateOverlays_NoOverlaysIsNoop(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
	}
	err := h.validateOverlays()
	require.NoError(t, err)
}

func TestResolveOverlays_SingleMatch(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		Role:      "fix",
		PreScript: "scripts/common.sh",
		Overlays: []OverlayEntry{
			{When: `event.source.system == "github"`, ForgeConfig: ForgeConfig{PreScript: "scripts/gh.sh"}},
		},
	}
	event := map[string]any{"source": map[string]any{"system": "github"}}
	err := h.ResolveOverlays(event, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "scripts/gh.sh", h.PreScript)
	assert.Nil(t, h.Overlays)
}

func TestResolveOverlays_FirstMatchWins(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Overlays: []OverlayEntry{
			{When: `event.source.system == "github"`, ForgeConfig: ForgeConfig{PreScript: "a.sh"}},
			{When: `event.source.system == "github"`, ForgeConfig: ForgeConfig{PreScript: "b.sh"}},
		},
	}
	event := map[string]any{"source": map[string]any{"system": "github"}}
	err := h.ResolveOverlays(event, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "a.sh", h.PreScript, "first-match-wins: first entry should be applied")
}

func TestResolveOverlays_FirstMatchWinsSkipsLater(t *testing.T) {
	h := &Harness{
		Agent:  "agents/test.md",
		Role:   "fix",
		Skills: []SkillEntry{{Source: "skills/base"}},
		Overlays: []OverlayEntry{
			{When: `event.source.system == "github"`, ForgeConfig: ForgeConfig{Skills: []SkillEntry{{Source: "skills/gh"}}}},
			{When: `event.source.system == "github"`, ForgeConfig: ForgeConfig{Skills: []SkillEntry{{Source: "skills/extra"}}}},
		},
	}
	event := map[string]any{"source": map[string]any{"system": "github"}}
	err := h.ResolveOverlays(event, "", nil)
	require.NoError(t, err)
	require.Len(t, h.Skills, 2, "only first matching overlay should be applied")
	assert.Equal(t, "skills/base", h.Skills[0].Source)
	assert.Equal(t, "skills/gh", h.Skills[1].Source)
}

func TestResolveOverlays_NoMatchUnchanged(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		Role:      "fix",
		PreScript: "scripts/common.sh",
		Overlays: []OverlayEntry{
			{When: `event.source.system == "jira"`, ForgeConfig: ForgeConfig{PreScript: "scripts/jira.sh"}},
		},
	}
	event := map[string]any{"source": map[string]any{"system": "github"}}
	err := h.ResolveOverlays(event, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "scripts/common.sh", h.PreScript)
	assert.Nil(t, h.Overlays)
}

func TestResolveOverlays_NilEventNoop(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		Role:      "fix",
		PreScript: "scripts/common.sh",
		Overlays: []OverlayEntry{
			{When: `runtime.forge == "github"`, ForgeConfig: ForgeConfig{PreScript: "scripts/gh.sh"}},
		},
	}
	// Nil event is converted to empty map; overlays conditioned on runtime.forge
	// or config can still match (ADR 0088 — overlays work in CLI paths without event).
	err := h.ResolveOverlays(nil, "github", nil)
	require.NoError(t, err)
	assert.Equal(t, "scripts/gh.sh", h.PreScript, "overlay should match on runtime.forge even when event is nil")
	assert.Nil(t, h.Overlays, "overlays should be consumed after resolution")
}

func TestResolveOverlays_EmptyOverlaysNoop(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		Role:      "fix",
		PreScript: "scripts/common.sh",
	}
	err := h.ResolveOverlays(map[string]any{"source": map[string]any{"system": "github"}}, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "scripts/common.sh", h.PreScript)
}

func TestResolveOverlays_RuntimeForge(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Overlays: []OverlayEntry{
			{When: `runtime.forge == "github"`, ForgeConfig: ForgeConfig{PreScript: "scripts/gh.sh"}},
			{When: `runtime.forge == "gitlab"`, ForgeConfig: ForgeConfig{PreScript: "scripts/gl.sh"}},
		},
	}
	event := map[string]any{"source": map[string]any{"system": "jira"}}
	err := h.ResolveOverlays(event, "github", nil)
	require.NoError(t, err)
	assert.Equal(t, "scripts/gh.sh", h.PreScript)
}

func TestResolveOverlays_ConfigVariable(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Overlays: []OverlayEntry{
			{When: `config.tracker == "jira"`, ForgeConfig: ForgeConfig{PreScript: "scripts/jira.sh"}},
			{When: `config.tracker == "github"`, ForgeConfig: ForgeConfig{PreScript: "scripts/gh.sh"}},
		},
	}
	event := map[string]any{"source": map[string]any{"system": "jira"}}
	config := map[string]any{"tracker": "jira"}
	err := h.ResolveOverlays(event, "github", config)
	require.NoError(t, err)
	assert.Equal(t, "scripts/jira.sh", h.PreScript)
}

func TestResolveOverlays_CombinedWhenExpression(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Overlays: []OverlayEntry{
			{When: `event.source.system == "jira" && runtime.forge == "github"`, ForgeConfig: ForgeConfig{
				PreScript: "scripts/jira-on-gh.sh",
				Skills:    []SkillEntry{{Source: "skills/jira-read"}},
			}},
			{When: `event.source.system == "github"`, ForgeConfig: ForgeConfig{PreScript: "scripts/gh.sh"}},
		},
	}
	event := map[string]any{"source": map[string]any{"system": "jira"}}
	err := h.ResolveOverlays(event, "github", nil)
	require.NoError(t, err)
	assert.Equal(t, "scripts/jira-on-gh.sh", h.PreScript)
	require.Len(t, h.Skills, 1)
	assert.Equal(t, "skills/jira-read", h.Skills[0].Source)
}

// TestResolveOverlays_CELErrorSkipsToFallback verifies that a CEL evaluation
// error in an earlier overlay (e.g., accessing event.source.system when event
// is empty) does not abort resolution — the error is logged as non-matching,
// and a broader fallback overlay can still match. This matches the
// MatchHarnesses pattern in harnessdispatch/enumerate.go and supports the
// more-specific-first pattern documented in bring-your-own-agent.md.
func TestResolveOverlays_CELErrorSkipsToFallback(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		Role:      "fix",
		PreScript: "scripts/common.sh",
		Overlays: []OverlayEntry{
			// More-specific overlay: references event.source.system which will
			// error when event is empty (no such key).
			{When: `event.source.system == "jira" && runtime.forge == "github"`, ForgeConfig: ForgeConfig{
				PreScript: "scripts/jira-on-gh.sh",
			}},
			// Broader fallback: conditioned only on runtime.forge, always evaluable.
			{When: `runtime.forge == "github"`, ForgeConfig: ForgeConfig{
				PreScript: "scripts/gh-fallback.sh",
			}},
		},
	}
	// Empty event: event.source.system access will error on the first overlay.
	// The fallback overlay should still match.
	err := h.ResolveOverlays(map[string]any{}, "github", nil)
	require.NoError(t, err)
	assert.Equal(t, "scripts/gh-fallback.sh", h.PreScript,
		"fallback overlay should match when earlier overlay has a CEL eval error")
	assert.Nil(t, h.Overlays, "overlays should be consumed after resolution")
}

// TestResolveOverlays_CELErrorAllFail verifies that when all overlays fail
// with CEL evaluation errors, no overlay is applied and the harness retains
// its original values.
func TestResolveOverlays_CELErrorAllFail(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		Role:      "fix",
		PreScript: "scripts/common.sh",
		Overlays: []OverlayEntry{
			{When: `event.source.system == "jira"`, ForgeConfig: ForgeConfig{PreScript: "scripts/jira.sh"}},
			{When: `event.source.system == "github"`, ForgeConfig: ForgeConfig{PreScript: "scripts/gh.sh"}},
		},
	}
	// Empty event: both overlays will fail on event.source access.
	err := h.ResolveOverlays(map[string]any{}, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "scripts/common.sh", h.PreScript,
		"harness should retain original values when all overlays fail")
	assert.Nil(t, h.Overlays, "overlays should be consumed even when all fail")
}

// --- BuildConfigMap tests ---

func TestBuildConfigMap_NilConfig(t *testing.T) {
	t.Parallel()
	assert.Nil(t, BuildConfigMap(nil))
}

func TestBuildConfigMap_PerRepoConfig(t *testing.T) {
	t.Parallel()
	cfg := config.NewPerRepoConfig([]string{"triage", "code"}, "org/repo")
	pr, ok := cfg.(config.PerRepoConfigReader)
	require.True(t, ok)
	// Set per-repo specific fields via the writer interface.
	if w, ok := cfg.(config.PerRepoConfigWriter); ok {
		w.SetRuntime("claude")
	}
	_ = pr // verify type assertion works

	m := BuildConfigMap(cfg)
	require.NotNil(t, m)
	assert.Equal(t, "claude", m["runtime"])
	roles, ok := m["roles"].([]any)
	require.True(t, ok)
	assert.Contains(t, roles, "triage")
	assert.Contains(t, roles, "code")
}

func TestBuildConfigMap_OrgConfig(t *testing.T) {
	t.Parallel()
	// Org configs don't implement PerRepoConfigReader, so the map
	// should be nil (no per-repo fields to expose).
	orgCfg := config.NewOrgConfig(nil, nil, nil, "", "")
	m := BuildConfigMap(orgCfg)
	assert.Nil(t, m)
}

func TestBuildConfigMap_AllFields(t *testing.T) {
	t.Parallel()
	// Test that BuildConfigMap exposes all non-sensitive per-repo config
	// fields (PR #6285: removed 4-key whitelist).
	cfg := config.NewPerRepoConfig([]string{"triage"}, "org/repo")

	// Set fields via writer interface
	if w, ok := cfg.(config.PerRepoConfigWriter); ok {
		w.SetRuntime("claude")
		w.SetKillSwitch(true)
		w.SetAgents([]config.AgentEntry{
			{Name: "my-agent", Source: "https://example.com/agent.yaml"},
		})
		w.SetAllowedRemoteResources([]string{"https://example.com/*"})
	}

	m := BuildConfigMap(cfg)
	require.NotNil(t, m)

	// Core fields
	assert.Equal(t, "claude", m["runtime"])
	roles, ok := m["roles"].([]any)
	require.True(t, ok)
	assert.Contains(t, roles, "triage")

	// Operational fields
	assert.Equal(t, "1", m["version"])
	assert.Equal(t, true, m["kill_switch"])

	// Agent entries
	agents, ok := m["agents"].([]any)
	require.True(t, ok)
	require.Len(t, agents, 1)
	agentMap, ok := agents[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "my-agent", agentMap["name"])
	assert.Equal(t, "https://example.com/agent.yaml", agentMap["source"])

	// Security policies
	arr, ok := m["allowed_remote_resources"].([]any)
	require.True(t, ok)
	assert.Contains(t, arr, "https://example.com/*")

	// Issue creation config (set by NewPerRepoConfig with targetRepo)
	ci, ok := m["create_issues"].(map[string]any)
	require.True(t, ok)
	repos, ok := ci["allow_repos"].([]any)
	require.True(t, ok)
	assert.Contains(t, repos, "org/repo")
	assert.Contains(t, repos, "fullsend-ai/fullsend")
}
