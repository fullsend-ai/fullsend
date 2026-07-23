package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffHarness_Identical(t *testing.T) {
	h := &Harness{
		Agent:          "agents/triage.md",
		Model:          "opus",
		TimeoutMinutes: 10,
		Skills:         []string{"skills/a"},
	}
	result := DiffHarness(h, h, nil)
	assert.Nil(t, result.Child)
	assert.Empty(t, result.Warnings)
}

func TestDiffHarness_ScalarDifference(t *testing.T) {
	base := &Harness{
		Agent: "agents/triage.md",
		Model: "opus",
		Image: "ghcr.io/example:latest",
	}
	child := &Harness{
		Agent: "agents/triage.md",
		Model: "sonnet",
		Image: "ghcr.io/example:latest",
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Equal(t, "sonnet", result.Child.Model)
	assert.Empty(t, result.Child.Agent, "unchanged scalar should not appear in diff")
	assert.Empty(t, result.Child.Image, "unchanged scalar should not appear in diff")
}

func TestDiffHarness_SliceAddition(t *testing.T) {
	base := &Harness{
		Skills: []string{"skills/a", "skills/b"},
	}
	child := &Harness{
		Skills: []string{"skills/a", "skills/b", "skills/c"},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Equal(t, []string{"skills/c"}, result.Child.Skills)
	assert.Empty(t, result.Warnings)
}

// TestDiffHarness_SkillOverrideByBasename verifies that when a child skill
// overrides a base skill by basename (different full path, same basename),
// diffSkills treats this as an override (extra) rather than a removal+addition.
func TestDiffHarness_SkillOverrideByBasename(t *testing.T) {
	base := &Harness{
		Skills: []string{"/cache/sha256/abc/code-implementation", "skills/pr-review"},
	}
	child := &Harness{
		Skills: []string{"skills/code-implementation", "skills/pr-review"},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child, "basename override should not abort diff")
	assert.Equal(t, []string{"skills/code-implementation"}, result.Child.Skills)
	assert.Empty(t, result.Warnings)
}

func TestDiffHarness_SliceRemoval(t *testing.T) {
	base := &Harness{
		Skills: []string{"skills/a", "skills/b", "skills/c"},
	}
	child := &Harness{
		Skills: []string{"skills/a"},
	}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child, "removal should abort diff")
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "removes items from base")
}

func TestDiffHarness_CustomizedFileKeepsField(t *testing.T) {
	base := &Harness{
		Agent:  "agents/triage.md",
		Policy: "policies/triage.yaml",
	}
	child := &Harness{
		Agent:  "agents/triage.md",
		Policy: "policies/triage.yaml",
	}
	customized := map[string]bool{
		"agents/triage.md": true,
	}
	result := DiffHarness(base, child, customized)
	require.NotNil(t, result.Child)
	assert.Equal(t, "agents/triage.md", result.Child.Agent, "customized file path should be kept")
	assert.Empty(t, result.Child.Policy, "non-customized file should not appear")
}

func TestDiffHarness_MapDifference(t *testing.T) {
	base := &Harness{
		RunnerEnv: map[string]string{
			"KEY1": "val1",
			"KEY2": "val2",
		},
	}
	child := &Harness{
		RunnerEnv: map[string]string{
			"KEY1": "val1",
			"KEY2": "changed",
			"KEY3": "new",
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Equal(t, map[string]string{"KEY2": "changed", "KEY3": "new"}, result.Child.RunnerEnv)
}

func TestDiffHarness_EnvDifference(t *testing.T) {
	base := &Harness{
		Env: &EnvConfig{
			Runner:  map[string]string{"A": "1"},
			Sandbox: map[string]string{"B": "2"},
		},
	}
	child := &Harness{
		Env: &EnvConfig{
			Runner:  map[string]string{"A": "1", "C": "3"},
			Sandbox: map[string]string{"B": "2"},
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	require.NotNil(t, result.Child.Env)
	assert.Equal(t, map[string]string{"C": "3"}, result.Child.Env.Runner)
	assert.Nil(t, result.Child.Env.Sandbox, "identical sandbox should not appear")
}

func TestDiffHarness_HostFileDifference(t *testing.T) {
	base := &Harness{
		HostFiles: []HostFile{
			{Src: "a.txt", Dest: "/tmp/a"},
			{Src: "b.txt", Dest: "/tmp/b"},
		},
	}
	child := &Harness{
		HostFiles: []HostFile{
			{Src: "a.txt", Dest: "/tmp/a"},
			{Src: "b-custom.txt", Dest: "/tmp/b"},
			{Src: "c.txt", Dest: "/tmp/c"},
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Len(t, result.Child.HostFiles, 2)
	assert.Equal(t, "/tmp/b", result.Child.HostFiles[0].Dest)
	assert.Equal(t, "/tmp/c", result.Child.HostFiles[1].Dest)
}

func TestDiffHarness_ForgeDifference(t *testing.T) {
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {
				PreScript:  "scripts/pre.sh",
				PostScript: "scripts/post.sh",
			},
		},
	}
	child := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {
				PreScript:  "scripts/pre.sh",
				PostScript: "scripts/custom-post.sh",
			},
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	require.NotNil(t, result.Child.Forge["github"])
	assert.Equal(t, "scripts/custom-post.sh", result.Child.Forge["github"].PostScript)
	assert.Empty(t, result.Child.Forge["github"].PreScript, "unchanged forge field should not appear")
}

func TestDiffHarness_MultipleFieldTypes(t *testing.T) {
	base := &Harness{
		Agent:          "agents/code.md",
		Model:          "opus",
		Skills:         []string{"skills/a"},
		TimeoutMinutes: 10,
		RunnerEnv:      map[string]string{"K": "v"},
	}
	child := &Harness{
		Agent:          "agents/code.md",
		Model:          "sonnet",
		Skills:         []string{"skills/a", "skills/b"},
		TimeoutMinutes: 20,
		RunnerEnv:      map[string]string{"K": "v", "K2": "v2"},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Equal(t, "sonnet", result.Child.Model)
	assert.Equal(t, []string{"skills/b"}, result.Child.Skills)
	assert.Equal(t, 20, result.Child.TimeoutMinutes)
	assert.Equal(t, map[string]string{"K2": "v2"}, result.Child.RunnerEnv)
	assert.Empty(t, result.Child.Agent)
}

func TestDiffHarness_ValidationLoopDifference(t *testing.T) {
	base := &Harness{
		ValidationLoop: &ValidationLoop{
			Script:        "scripts/validate.sh",
			MaxIterations: 2,
		},
	}
	child := &Harness{
		ValidationLoop: &ValidationLoop{
			Script:        "scripts/validate.sh",
			MaxIterations: 5,
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	require.NotNil(t, result.Child.ValidationLoop)
	assert.Equal(t, 5, result.Child.ValidationLoop.MaxIterations)
	// Field-level diff: unchanged Script should not appear in diff
	assert.Equal(t, "", result.Child.ValidationLoop.Script,
		"unchanged Script should not be in diff")
}

func TestDiffHarness_ValidationLoopFieldLevelRoundTrip(t *testing.T) {
	// Verify that diff→mergeBaseIntoChild round-trips correctly when only
	// some ValidationLoop fields differ. This is the scenario from the HIGH
	// review finding: child overrides Script but keeps base Schema/FeedbackMode.
	base := &Harness{
		Agent: "agents/test.md",
		ValidationLoop: &ValidationLoop{
			Script:        "base-validate.sh",
			Schema:        "base-schema.json",
			MaxIterations: 5,
			FeedbackMode:  "append",
		},
	}
	child := &Harness{
		Agent: "agents/test.md",
		ValidationLoop: &ValidationLoop{
			Script:        "child-validate.sh",
			Schema:        "base-schema.json",
			MaxIterations: 5,
			FeedbackMode:  "append",
		},
	}

	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child, "diff should be non-nil")
	require.Empty(t, result.Warnings)
	require.NotNil(t, result.Child.ValidationLoop)

	// Only Script differs — other fields should be zero (not included in diff)
	assert.Equal(t, "child-validate.sh", result.Child.ValidationLoop.Script)
	assert.Equal(t, "", result.Child.ValidationLoop.Schema,
		"matching Schema should not be in diff")
	assert.Equal(t, 0, result.Child.ValidationLoop.MaxIterations,
		"matching MaxIterations should not be in diff")
	assert.Equal(t, "", result.Child.ValidationLoop.FeedbackMode,
		"matching FeedbackMode should not be in diff")

	// Round-trip: compose the diff child with base
	mergeBaseIntoChild(base, result.Child)
	assert.Equal(t, child.ValidationLoop.Script, result.Child.ValidationLoop.Script)
	assert.Equal(t, child.ValidationLoop.Schema, result.Child.ValidationLoop.Schema)
	assert.Equal(t, child.ValidationLoop.MaxIterations, result.Child.ValidationLoop.MaxIterations)
	assert.Equal(t, child.ValidationLoop.FeedbackMode, result.Child.ValidationLoop.FeedbackMode)
}

func TestDiffHarness_NewForgePlatform(t *testing.T) {
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "pre.sh"},
		},
	}
	child := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "pre.sh"},
			"gitlab": {PreScript: "gl-pre.sh"},
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Nil(t, result.Child.Forge["github"], "identical platform should not appear")
	assert.NotNil(t, result.Child.Forge["gitlab"])
	assert.Equal(t, "gl-pre.sh", result.Child.Forge["gitlab"].PreScript)
}

func TestDiffHarness_AllowRuntimeFetch_AlwaysKept(t *testing.T) {
	base := &Harness{
		AllowRuntimeFetch: true,
		Model:             "opus",
	}
	child := &Harness{
		AllowRuntimeFetch: true,
		Model:             "opus",
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child, "AllowRuntimeFetch must be kept even when matching base")
	assert.True(t, result.Child.AllowRuntimeFetch)
}

func TestDiffHarness_MaxRuntimeFetches_AlwaysKept(t *testing.T) {
	maxFetches := 20
	base := &Harness{
		MaxRuntimeFetches: &maxFetches,
	}
	child := &Harness{
		MaxRuntimeFetches: &maxFetches,
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child, "MaxRuntimeFetches must be kept even when matching base")
	require.NotNil(t, result.Child.MaxRuntimeFetches)
	assert.Equal(t, 20, *result.Child.MaxRuntimeFetches)
}

func TestDiffHarness_AllowedRemoteResources_AlwaysKept(t *testing.T) {
	base := &Harness{
		AllowedRemoteResources: []string{"https://example.com/"},
	}
	child := &Harness{
		AllowedRemoteResources: []string{"https://example.com/"},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child, "AllowedRemoteResources must be kept even when matching base")
	assert.Equal(t, []string{"https://example.com/"}, result.Child.AllowedRemoteResources)
}

func TestDiffHarness_SecurityFields_OmittedWhenZero(t *testing.T) {
	base := &Harness{
		AllowRuntimeFetch: false,
		Model:             "opus",
	}
	child := &Harness{
		AllowRuntimeFetch: false,
		Model:             "opus",
	}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child, "zero-value security fields should not force a diff")
}

func TestDiffHarness_SecurityFields_Combined(t *testing.T) {
	maxFetches := 50
	base := &Harness{
		AllowRuntimeFetch:      true,
		MaxRuntimeFetches:      &maxFetches,
		AllowedRemoteResources: []string{"https://example.com/"},
		Model:                  "opus",
	}
	child := &Harness{
		AllowRuntimeFetch:      true,
		MaxRuntimeFetches:      &maxFetches,
		AllowedRemoteResources: []string{"https://example.com/", "https://other.com/"},
		Model:                  "sonnet",
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.True(t, result.Child.AllowRuntimeFetch)
	assert.Equal(t, 50, *result.Child.MaxRuntimeFetches)
	assert.Equal(t, []string{"https://example.com/", "https://other.com/"}, result.Child.AllowedRemoteResources)
	assert.Equal(t, "sonnet", result.Child.Model)
}

func TestDiffHarness_DescriptionAndSlug(t *testing.T) {
	base := &Harness{Description: "old", Slug: "old-slug", Role: "reviewer"}
	child := &Harness{Description: "new", Slug: "new-slug", Role: "reviewer"}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Equal(t, "new", result.Child.Description)
	assert.Equal(t, "new-slug", result.Child.Slug)
	assert.Empty(t, result.Child.Role)
}

func TestDiffHarness_ImageAndRole(t *testing.T) {
	base := &Harness{Image: "img:1", Role: "coder"}
	child := &Harness{Image: "img:2", Role: "reviewer"}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Equal(t, "img:2", result.Child.Image)
	assert.Equal(t, "reviewer", result.Child.Role)
}

func TestDiffHarness_PrePostScript(t *testing.T) {
	base := &Harness{PreScript: "scripts/pre.sh", PostScript: "scripts/post.sh", AgentInput: "input.txt"}
	child := &Harness{PreScript: "scripts/pre2.sh", PostScript: "scripts/post.sh", AgentInput: "input2.txt"}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Equal(t, "scripts/pre2.sh", result.Child.PreScript)
	assert.Empty(t, result.Child.PostScript)
	assert.Equal(t, "input2.txt", result.Child.AgentInput)
}

func TestDiffHarness_SandboxTimeout(t *testing.T) {
	base := &Harness{SandboxTimeoutSeconds: 60}
	child := &Harness{SandboxTimeoutSeconds: 120}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Equal(t, 120, result.Child.SandboxTimeoutSeconds)
}

func TestDiffHarness_SecurityDifference(t *testing.T) {
	base := &Harness{
		Security: &SecurityConfig{FailMode: "closed"},
	}
	child := &Harness{
		Security: &SecurityConfig{FailMode: "open"},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	require.NotNil(t, result.Child.Security)
	assert.Equal(t, "open", result.Child.Security.FailMode)
}

func TestDiffHarness_SecurityIdentical(t *testing.T) {
	sec := &SecurityConfig{FailMode: "closed"}
	base := &Harness{Security: sec, Model: "opus"}
	child := &Harness{Security: sec, Model: "opus"}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child)
}

func TestDiffHarness_APIServerModification(t *testing.T) {
	base := &Harness{
		APIServers: []APIServer{
			{Name: "proxy", Script: "start.sh", Port: 8080},
		},
	}
	child := &Harness{
		APIServers: []APIServer{
			{Name: "proxy", Script: "start-v2.sh", Port: 8080},
			{Name: "metrics", Script: "metrics.sh", Port: 9090},
		},
	}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child, "modified api_server should abort diff")
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "api_servers")
}

func TestDiffHarness_APIServerAdditionOnly(t *testing.T) {
	base := &Harness{
		APIServers: []APIServer{
			{Name: "proxy", Script: "start.sh", Port: 8080},
		},
	}
	child := &Harness{
		APIServers: []APIServer{
			{Name: "proxy", Script: "start.sh", Port: 8080},
			{Name: "metrics", Script: "metrics.sh", Port: 9090},
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Len(t, result.Child.APIServers, 1)
	assert.Equal(t, "metrics", result.Child.APIServers[0].Name)
}

func TestDiffHarness_PluginsRemoval(t *testing.T) {
	base := &Harness{Plugins: []string{"a", "b"}}
	child := &Harness{Plugins: []string{"a"}}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child)
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "plugins")
}

func TestDiffHarness_ProvidersRemoval(t *testing.T) {
	base := &Harness{Providers: []string{"p1", "p2"}}
	child := &Harness{Providers: []string{"p1"}}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child)
	assert.Contains(t, result.Warnings[0], "providers")
}

func TestDiffHarness_ProvidersAddition(t *testing.T) {
	base := &Harness{Providers: []string{"p1"}}
	child := &Harness{Providers: []string{"p1", "p2"}}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Equal(t, []string{"p2"}, result.Child.Providers)
}

func TestDiffHarness_DocCustomizedFile(t *testing.T) {
	base := &Harness{Doc: "agents/doc.md"}
	child := &Harness{Doc: "agents/doc.md"}
	customized := map[string]bool{"agents/doc.md": true}
	result := DiffHarness(base, child, customized)
	require.NotNil(t, result.Child)
	assert.Equal(t, "agents/doc.md", result.Child.Doc)
}

func TestDiffHarness_NilValidationLoopWarning(t *testing.T) {
	base := &Harness{
		ValidationLoop: &ValidationLoop{Script: "validate.sh"},
	}
	child := &Harness{}
	result := DiffHarness(base, child, nil)
	require.NotEmpty(t, result.Warnings)
	assert.Contains(t, result.Warnings[0], "validation_loop: child removes block from base")
	assert.Nil(t, result.Child, "Child should be nil when validation_loop is removed")
}

func TestDiffHarness_NilSecurityWarning(t *testing.T) {
	base := &Harness{
		Security: &SecurityConfig{FailMode: "closed"},
	}
	child := &Harness{}
	result := DiffHarness(base, child, nil)
	require.NotEmpty(t, result.Warnings)
	assert.Contains(t, result.Warnings[0], "security: child removes block from base")
	assert.Nil(t, result.Child, "Child should be nil when security is removed")
}

func TestDiffForge_NilPlatformWarning(t *testing.T) {
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "echo hi"},
		},
	}
	child := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": nil,
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotEmpty(t, result.Warnings)
	assert.Contains(t, result.Warnings[0], "child removes platform from base")
}

func TestDiffForgeConfig_AllFields(t *testing.T) {
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {
				PreScript:  "pre.sh",
				PostScript: "post.sh",
				Skills:     []string{"skill/a"},
				RunnerEnv:  map[string]string{"K": "v"},
				Env:        &EnvConfig{Runner: map[string]string{"R": "1"}},
				ValidationLoop: &ValidationLoop{
					Script:        "validate.sh",
					MaxIterations: 3,
				},
			},
		},
	}
	child := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {
				PreScript:  "pre.sh",
				PostScript: "post-v2.sh",
				Skills:     []string{"skill/a", "skill/b"},
				RunnerEnv:  map[string]string{"K": "v", "K2": "v2"},
				Env:        &EnvConfig{Runner: map[string]string{"R": "1", "R2": "2"}},
				ValidationLoop: &ValidationLoop{
					Script:        "validate-v2.sh",
					MaxIterations: 5,
				},
			},
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	fc := result.Child.Forge["github"]
	require.NotNil(t, fc)
	assert.Equal(t, "post-v2.sh", fc.PostScript)
	assert.Empty(t, fc.PreScript)
	assert.Equal(t, []string{"skill/b"}, fc.Skills)
	assert.Equal(t, map[string]string{"K2": "v2"}, fc.RunnerEnv)
	assert.Equal(t, map[string]string{"R2": "2"}, fc.Env.Runner)
	// Field-level diff: only differing VL fields are included
	require.NotNil(t, fc.ValidationLoop)
	assert.Equal(t, "validate-v2.sh", fc.ValidationLoop.Script)
	assert.Equal(t, 5, fc.ValidationLoop.MaxIterations)
}

func TestDiffForgeConfig_ValidationLoopFieldLevel(t *testing.T) {
	// Forge-level field diff: only the differing field (MaxIterations) should
	// appear; Script should not be in the diff since it matches.
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {
				ValidationLoop: &ValidationLoop{
					Script:        "validate.sh",
					Schema:        "schema.json",
					MaxIterations: 3,
					FeedbackMode:  "append",
				},
			},
		},
	}
	child := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {
				ValidationLoop: &ValidationLoop{
					Script:        "validate.sh",
					Schema:        "schema.json",
					MaxIterations: 5,
					FeedbackMode:  "append",
				},
			},
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	fc := result.Child.Forge["github"]
	require.NotNil(t, fc)
	require.NotNil(t, fc.ValidationLoop)
	assert.Equal(t, 5, fc.ValidationLoop.MaxIterations)
	assert.Equal(t, "", fc.ValidationLoop.Script,
		"unchanged Script should not be in forge diff")
	assert.Equal(t, "", fc.ValidationLoop.Schema,
		"unchanged Schema should not be in forge diff")
}

func TestDiffHarness_PluginsAddition(t *testing.T) {
	base := &Harness{Plugins: []string{"a"}}
	child := &Harness{Plugins: []string{"a", "b"}}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	assert.Equal(t, []string{"b"}, result.Child.Plugins)
}

func TestDiffHarness_HostFileRemoval(t *testing.T) {
	base := &Harness{
		HostFiles: []HostFile{
			{Src: "a.txt", Dest: "/tmp/a"},
			{Src: "b.txt", Dest: "/tmp/b"},
		},
	}
	child := &Harness{
		HostFiles: []HostFile{
			{Src: "a.txt", Dest: "/tmp/a"},
		},
	}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child, "removal should abort diff")
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "host_files")
}

func TestDiffHarness_APIServerRemoval(t *testing.T) {
	base := &Harness{
		APIServers: []APIServer{
			{Name: "proxy", Script: "start.sh", Port: 8080},
			{Name: "metrics", Script: "metrics.sh", Port: 9090},
		},
	}
	child := &Harness{
		APIServers: []APIServer{
			{Name: "proxy", Script: "start.sh", Port: 8080},
		},
	}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child, "removal should abort diff")
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "api_servers")
}

func TestDiffHarness_RunnerEnvRemoval(t *testing.T) {
	base := &Harness{
		RunnerEnv: map[string]string{"A": "1", "B": "2"},
	}
	child := &Harness{
		RunnerEnv: map[string]string{"A": "1"},
	}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child, "env key removal should abort diff")
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "runner_env")
}

func TestDiffHarness_EnvRunnerRemoval(t *testing.T) {
	base := &Harness{
		Env: &EnvConfig{Runner: map[string]string{"A": "1", "B": "2"}},
	}
	child := &Harness{
		Env: &EnvConfig{Runner: map[string]string{"A": "1"}},
	}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child, "env runner key removal should abort diff")
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "env")
}

func TestDiffHarness_ForgeSkillsRemoval(t *testing.T) {
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []string{"skill/a", "skill/b"},
			},
		},
	}
	child := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []string{"skill/a"},
			},
		},
	}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child, "forge skill removal should abort diff")
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "forge[github].skills")
}

// TestDiffHarness_ForgeSkillsOverrideByBasename verifies that forge skill
// overrides by basename are treated as extras, not removals.
func TestDiffHarness_ForgeSkillsOverrideByBasename(t *testing.T) {
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []string{"/cache/code-review", "skill/common"},
			},
		},
	}
	child := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {
				Skills: []string{"skills/code-review", "skill/common"},
			},
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child, "forge basename override should not abort diff")
	require.NotNil(t, result.Child.Forge)
	require.NotNil(t, result.Child.Forge["github"])
	assert.Equal(t, []string{"skills/code-review"}, result.Child.Forge["github"].Skills)
	assert.Empty(t, result.Warnings)
}

func TestDiffHarness_ForgePlatformRemoval(t *testing.T) {
	base := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "pre.sh"},
			"gitlab": {PreScript: "gl-pre.sh"},
		},
	}
	child := &Harness{
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "pre.sh"},
		},
	}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child, "forge platform removal should abort diff")
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "forge[gitlab]")
	assert.Contains(t, result.Warnings[0], "removes platform from base")
}

func TestDiffHarness_CustomizedSliceItem_NoDuplication(t *testing.T) {
	// Customized-file semantics don't apply to concatenated slices (skills,
	// plugins, providers) — including a customized item that already exists
	// in the base would cause duplication after composition.
	base := &Harness{
		Skills: []string{"skills/a.yaml", "skills/b.yaml"},
	}
	child := &Harness{
		Skills: []string{"skills/a.yaml", "skills/b.yaml"},
	}
	customized := map[string]bool{
		"skills/b.yaml": true,
	}
	result := DiffHarness(base, child, customized)
	assert.Nil(t, result.Child, "identical slices should not produce a diff even with customized files")
}

func TestDiffHarness_NilBaseEnvWithChildEnv(t *testing.T) {
	base := &Harness{}
	child := &Harness{
		Env: &EnvConfig{
			Runner:  map[string]string{"A": "1"},
			Sandbox: map[string]string{"B": "2"},
		},
	}
	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child)
	require.NotNil(t, result.Child.Env)
	assert.Equal(t, map[string]string{"A": "1"}, result.Child.Env.Runner)
	assert.Equal(t, map[string]string{"B": "2"}, result.Child.Env.Sandbox)
}

func TestDiffHarness_BaseEnvWithNilChildEnv(t *testing.T) {
	base := &Harness{
		Env: &EnvConfig{
			Runner:  map[string]string{"A": "1"},
			Sandbox: map[string]string{"B": "2"},
		},
	}
	child := &Harness{}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child, "env removal should abort diff")
	require.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "env: child removes keys from base")
}

func TestDiffHarness_BaseEnvWithNilChildEnv_EmptyBase(t *testing.T) {
	base := &Harness{
		Env: &EnvConfig{},
	}
	child := &Harness{}
	result := DiffHarness(base, child, nil)
	assert.Nil(t, result.Child, "empty base env with nil child env should produce no diff")
	assert.Empty(t, result.Warnings)
}

func TestDiffHarness_RoundTrip(t *testing.T) {
	maxFetches := 10
	base := &Harness{
		Agent:                  "agents/triage.md",
		Doc:                    "agents/triage-doc.md",
		Description:            "Triage agent",
		Role:                   "reviewer",
		Slug:                   "triage",
		Image:                  "ghcr.io/example:v1",
		Policy:                 "policies/default.yaml",
		Model:                  "opus",
		PreScript:              "scripts/pre.sh",
		PostScript:             "scripts/post.sh",
		AgentInput:             "input.md",
		TimeoutMinutes:         10,
		SandboxTimeoutSeconds:  60,
		AllowRuntimeFetch:      true,
		MaxRuntimeFetches:      &maxFetches,
		AllowedRemoteResources: []string{"https://example.com/"},
		Skills:                 []string{"skills/a", "skills/b"},
		Plugins:                []string{"plugin-a"},
		Providers:              []string{"prov-1"},
		HostFiles: []HostFile{
			{Src: "a.txt", Dest: "/tmp/a"},
			{Src: "b.txt", Dest: "/tmp/b"},
		},
		APIServers: []APIServer{
			{Name: "proxy", Script: "start.sh", Port: 8080},
		},
		RunnerEnv: map[string]string{"K1": "v1", "K2": "v2"},
		Env: &EnvConfig{
			Runner:  map[string]string{"R1": "1"},
			Sandbox: map[string]string{"S1": "2"},
		},
		ValidationLoop: &ValidationLoop{
			Script:        "validate.sh",
			MaxIterations: 3,
		},
		Security: &SecurityConfig{FailMode: "closed"},
		Forge: map[string]*ForgeConfig{
			"github": {
				PreScript:  "gh-pre.sh",
				PostScript: "gh-post.sh",
				Skills:     []string{"skill/gh-a"},
				RunnerEnv:  map[string]string{"GH": "1"},
			},
		},
	}

	childMaxFetches := 20
	child := &Harness{
		Agent:                  "agents/triage.md",
		Doc:                    "agents/triage-doc.md",
		Description:            "Custom triage agent",
		Role:                   "reviewer",
		Slug:                   "custom-triage",
		Image:                  "ghcr.io/example:v2",
		Policy:                 "policies/default.yaml",
		Model:                  "sonnet",
		PreScript:              "scripts/pre.sh",
		PostScript:             "scripts/custom-post.sh",
		AgentInput:             "input.md",
		TimeoutMinutes:         20,
		SandboxTimeoutSeconds:  120,
		AllowRuntimeFetch:      true,
		MaxRuntimeFetches:      &childMaxFetches,
		AllowedRemoteResources: []string{"https://example.com/", "https://other.com/"},
		Skills:                 []string{"skills/a", "skills/b", "skills/c"},
		Plugins:                []string{"plugin-a", "plugin-b"},
		Providers:              []string{"prov-1", "prov-2"},
		HostFiles: []HostFile{
			{Src: "a.txt", Dest: "/tmp/a"},
			{Src: "b-v2.txt", Dest: "/tmp/b"},
			{Src: "c.txt", Dest: "/tmp/c"},
		},
		APIServers: []APIServer{
			{Name: "proxy", Script: "start.sh", Port: 8080},
			{Name: "metrics", Script: "metrics.sh", Port: 9090},
		},
		RunnerEnv: map[string]string{"K1": "v1", "K2": "changed", "K3": "new"},
		Env: &EnvConfig{
			Runner:  map[string]string{"R1": "1", "R2": "new"},
			Sandbox: map[string]string{"S1": "2"},
		},
		ValidationLoop: &ValidationLoop{
			Script:        "validate-v2.sh",
			MaxIterations: 5,
		},
		Security: &SecurityConfig{FailMode: "open"},
		Forge: map[string]*ForgeConfig{
			"github": {
				PreScript:  "gh-pre.sh",
				PostScript: "gh-post-v2.sh",
				Skills:     []string{"skill/gh-a", "skill/gh-b"},
				RunnerEnv:  map[string]string{"GH": "1", "GH2": "2"},
			},
			"gitlab": {
				PreScript: "gl-pre.sh",
			},
		},
	}

	result := DiffHarness(base, child, nil)
	require.NotNil(t, result.Child, "diff should be non-nil for differing harnesses")
	require.Empty(t, result.Warnings, "no warnings expected for additive changes")

	// Compose: merge base into the diff child (mergeBaseIntoChild fills
	// zero-value fields from base, concatenates slices, merges maps).
	mergeBaseIntoChild(base, result.Child)

	assert.Equal(t, child.Agent, result.Child.Agent)
	assert.Equal(t, child.Description, result.Child.Description)
	assert.Equal(t, child.Model, result.Child.Model)
	assert.Equal(t, child.Slug, result.Child.Slug)
	assert.Equal(t, child.Image, result.Child.Image)
	assert.Equal(t, child.PostScript, result.Child.PostScript)
	assert.Equal(t, child.TimeoutMinutes, result.Child.TimeoutMinutes)
	assert.Equal(t, child.SandboxTimeoutSeconds, result.Child.SandboxTimeoutSeconds)
	assert.Equal(t, child.AllowRuntimeFetch, result.Child.AllowRuntimeFetch)
	assert.Equal(t, *child.MaxRuntimeFetches, *result.Child.MaxRuntimeFetches)
	assert.Equal(t, child.AllowedRemoteResources, result.Child.AllowedRemoteResources)
	assert.Equal(t, child.Skills, result.Child.Skills)
	assert.Equal(t, child.Plugins, result.Child.Plugins)
	assert.Equal(t, child.Providers, result.Child.Providers)
	assert.Equal(t, child.RunnerEnv, result.Child.RunnerEnv)
	assert.Equal(t, child.Env, result.Child.Env)
	assert.Equal(t, child.ValidationLoop, result.Child.ValidationLoop)
	assert.Equal(t, child.Security, result.Child.Security)
	assert.Equal(t, child.HostFiles, result.Child.HostFiles)
	assert.Equal(t, child.APIServers, result.Child.APIServers)
	assert.Equal(t, child.Forge, result.Child.Forge)
}
