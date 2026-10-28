package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveLocalPathsRelativeToHarnessFile(t *testing.T) {
	root := t.TempDir()
	harnessDir := filepath.Join(root, "agents", "custom")
	writeSkill(t, filepath.Join(harnessDir, "skills", "custom"), "custom-skill")
	writeSkill(t, filepath.Join(harnessDir, "skills", "forge"), "forge-skill")
	writeSkill(t, filepath.Join(harnessDir, "skills", "overlay"), "overlay-skill")

	harness := &Harness{
		Agent:      "agent.md",
		Policy:     "policy.yaml",
		PreScript:  "scripts/pre.sh",
		PostScript: "scripts/post.sh",
		AgentInput: "input",
		Skills:     []SkillEntry{{Source: "skills/custom"}},
		Plugins:    []PluginSpec{{Path: "plugins/custom"}},
		HostFiles:  []HostFile{{Src: "files/host.txt", Dest: "/tmp/host.txt"}},
		APIServers: []APIServer{{Name: "api", Script: "scripts/api.sh"}},
		Providers:  []string{"providers/custom.yaml"},
		OpenShell:  &OpenShellConfig{Profiles: []string{"profiles/custom.yaml"}},
		Overlays:   []OverlayEntry{{}},
	}
	harness.ValidationLoop = &ValidationLoop{
		Script: "scripts/validate.sh",
		Schema: "schemas/validate.json",
	}
	harness.Forge = map[string]*ForgeConfig{
		"github": nil,
		"gitlab": {
			PreScript: "scripts/forge-pre.sh",
			Skills:    []SkillEntry{{Source: "skills/forge"}},
			HostFiles: []HostFile{{Src: "files/forge-host.txt", Dest: "/tmp/forge-host.txt"}},
			Providers: []string{"providers/forge.yaml"},
			OpenShell: &OpenShellConfig{Profiles: []string{"profiles/forge.yaml"}},
			ValidationLoop: &ValidationLoop{
				Script: "scripts/forge-validate.sh",
				Schema: "schemas/forge-validate.json",
			},
		},
	}
	harness.Overlays[0].ForgeConfig = ForgeConfig{
		PostScript: "scripts/overlay-post.sh",
		Skills:     []SkillEntry{{Source: "skills/overlay"}},
		HostFiles:  []HostFile{{Src: "files/overlay-host.txt", Dest: "/tmp/overlay-host.txt"}},
		Providers:  []string{"providers/overlay.yaml"},
		OpenShell:  &OpenShellConfig{Profiles: []string{"profiles/overlay.yaml"}},
		ValidationLoop: &ValidationLoop{
			Script: "scripts/overlay-validate.sh",
			Schema: "schemas/overlay-validate.json",
		},
	}

	require.NoError(t, harness.resolveLocalPathsRelativeTo(harnessDir, root))

	assert.Equal(t, filepath.Join(harnessDir, "agent.md"), harness.Agent)
	assert.Equal(t, filepath.Join(harnessDir, "policy.yaml"), harness.Policy)
	assert.Equal(t, filepath.Join(harnessDir, "scripts", "pre.sh"), harness.PreScript)
	assert.Equal(t, filepath.Join(harnessDir, "scripts", "post.sh"), harness.PostScript)
	assert.Equal(t, filepath.Join(harnessDir, "input"), harness.AgentInput)
	assert.Equal(t, filepath.Join(harnessDir, "skills", "custom"), harness.Skills[0].Source)
	assert.Equal(t, "custom-skill", harness.Skills[0].mergeName)
	assert.Equal(t, filepath.Join(harnessDir, "plugins", "custom"), harness.Plugins[0].Path)
	assert.Equal(t, filepath.Join(harnessDir, "files", "host.txt"), harness.HostFiles[0].Src)
	assert.Equal(t, filepath.Join(harnessDir, "scripts", "api.sh"), harness.APIServers[0].Script)
	assert.Equal(t, filepath.Join(harnessDir, "scripts", "validate.sh"), harness.ValidationLoop.Script)
	assert.Equal(t, filepath.Join(harnessDir, "schemas", "validate.json"), harness.ValidationLoop.Schema)
	assert.Equal(t, filepath.Join(harnessDir, "profiles", "custom.yaml"), harness.OpenShell.Profiles[0])
	assert.Equal(t, filepath.Join(harnessDir, "providers", "custom.yaml"), harness.Providers[0])

	forge := harness.Forge["gitlab"]
	assert.Equal(t, filepath.Join(harnessDir, "scripts", "forge-pre.sh"), forge.PreScript)
	assert.Equal(t, filepath.Join(harnessDir, "skills", "forge"), forge.Skills[0].Source)
	assert.Equal(t, filepath.Join(harnessDir, "files", "forge-host.txt"), forge.HostFiles[0].Src)
	assert.Equal(t, filepath.Join(harnessDir, "providers", "forge.yaml"), forge.Providers[0])
	assert.Equal(t, filepath.Join(harnessDir, "profiles", "forge.yaml"), forge.OpenShell.Profiles[0])
	assert.Equal(t, filepath.Join(harnessDir, "scripts", "forge-validate.sh"), forge.ValidationLoop.Script)
	assert.Equal(t, filepath.Join(harnessDir, "schemas", "forge-validate.json"), forge.ValidationLoop.Schema)

	overlay := harness.Overlays[0].ForgeConfig
	assert.Equal(t, filepath.Join(harnessDir, "scripts", "overlay-post.sh"), overlay.PostScript)
	assert.Equal(t, filepath.Join(harnessDir, "skills", "overlay"), overlay.Skills[0].Source)
	assert.Equal(t, filepath.Join(harnessDir, "files", "overlay-host.txt"), overlay.HostFiles[0].Src)
	assert.Equal(t, filepath.Join(harnessDir, "providers", "overlay.yaml"), overlay.Providers[0])
	assert.Equal(t, filepath.Join(harnessDir, "profiles", "overlay.yaml"), overlay.OpenShell.Profiles[0])
	assert.Equal(t, filepath.Join(harnessDir, "scripts", "overlay-validate.sh"), overlay.ValidationLoop.Script)
	assert.Equal(t, filepath.Join(harnessDir, "schemas", "overlay-validate.json"), overlay.ValidationLoop.Schema)
}

func TestNewPathResolverRejectsNonDirectoryContainmentRoot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "containment-root")
	require.NoError(t, os.WriteFile(file, nil, 0o644))

	_, err := newPathResolver(file, file, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "opening containment directory")
}

func TestPathDiagnostics(t *testing.T) {
	diagnostic := Diagnostic{Severity: SeverityWarning, Field: "policy", Message: "deprecated fallback"}
	harness := &Harness{pathDiagnostics: []Diagnostic{diagnostic}}

	diagnostics := harness.PathDiagnostics()

	require.Len(t, diagnostics, 1)
	assert.Equal(t, diagnostic, diagnostics[0])
}

func TestResolveLocalPathsRelativeToHarnessFile_TraversalRejected(t *testing.T) {
	root := t.TempDir()
	harnessDir := filepath.Join(root, "agents", "custom")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))

	harness := &Harness{Policy: "../../../outside.yaml"}
	err := harness.resolveLocalPathsRelativeTo(harnessDir, root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `policy: path "../../../outside.yaml" resolves outside fullsend directory`)
}

func TestResolveLocalPathsRelativeToHarnessFile_HarnessOutsideContainmentRejected(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	harnessDir := filepath.Join(outsideRoot, "agents", "custom")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))

	harness := &Harness{Policy: "policy.yaml"}
	err := harness.resolveLocalPathsRelativeTo(harnessDir, root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is outside fullsend directory")
}

func TestResolveLocalPathsRelativeToHarnessFile_SymlinkEscapeRejected(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "fullsend")
	harnessDir := filepath.Join(root, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "policy.yaml"), []byte("version: 1\n"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(harnessDir, "linked")))

	harness := &Harness{Policy: "linked/policy.yaml"}
	err := harness.resolveLocalPathsRelativeTo(harnessDir, root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `policy: path "linked/policy.yaml" resolves outside fullsend directory`)
}

func TestResolveLocalPathsRelativeToHarnessFile_TraversalFieldsRejected(t *testing.T) {
	root := t.TempDir()
	harnessDir := filepath.Join(root, "agents", "custom")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	traversal := "../../../outside"

	tests := []struct {
		name    string
		harness *Harness
		field   string
	}{
		{"agent", &Harness{Agent: traversal}, "agent"},
		{"policy", &Harness{Policy: traversal}, "policy"},
		{"pre_script", &Harness{PreScript: traversal}, "pre_script"},
		{"post_script", &Harness{PostScript: traversal}, "post_script"},
		{"agent_input", &Harness{AgentInput: traversal}, "agent_input"},
		{"skill", &Harness{Skills: []SkillEntry{{Source: traversal}}}, "skills[0]"},
		{
			"skill_override",
			&Harness{Skills: []SkillEntry{{
				Source:    "skills/custom",
				Overrides: map[string]*string{"nested/file.md": &traversal},
			}}},
			"skills[0].overrides[nested/file.md]",
		},
		{"plugin", &Harness{Plugins: []PluginSpec{{Path: traversal}}}, "plugins[0]"},
		{"host_file", &Harness{HostFiles: []HostFile{{Src: traversal}}}, "host_files[0].src"},
		{"api_server", &Harness{APIServers: []APIServer{{Script: traversal}}}, "api_servers[0].script"},
		{"validation_script", &Harness{ValidationLoop: &ValidationLoop{Script: traversal}}, "validation_loop.script"},
		{"validation_schema", &Harness{ValidationLoop: &ValidationLoop{Script: "script.sh", Schema: traversal}}, "validation_loop.schema"},
		{"profile", &Harness{OpenShell: &OpenShellConfig{Profiles: []string{traversal}}}, "openshell.profiles[0]"},
		{"provider", &Harness{Providers: []string{traversal}}, "providers[0]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.harness.resolveLocalPathsRelativeTo(harnessDir, root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.field+":")
			assert.Contains(t, err.Error(), "resolves outside fullsend directory")
		})
	}
}

func TestResolveLocalPathsRelativeToHarnessFile_ForgeTraversalFieldsRejected(t *testing.T) {
	root := t.TempDir()
	harnessDir := filepath.Join(root, "agents", "custom")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	traversal := "../../../outside"

	tests := []struct {
		name   string
		config ForgeConfig
		field  string
	}{
		{"pre_script", ForgeConfig{PreScript: traversal}, "forge.github.pre_script"},
		{"post_script", ForgeConfig{PostScript: traversal}, "forge.github.post_script"},
		{"policy", ForgeConfig{Policy: traversal}, "forge.github.policy"},
		{"skill", ForgeConfig{Skills: []SkillEntry{{Source: traversal}}}, "forge.github.skills[0]"},
		{
			"skill_override",
			ForgeConfig{Skills: []SkillEntry{{
				Source:    "skills/custom",
				Overrides: map[string]*string{"nested/file.md": &traversal},
			}}},
			"forge.github.skills[0].overrides[nested/file.md]",
		},
		{"host_file", ForgeConfig{HostFiles: []HostFile{{Src: traversal}}}, "forge.github.host_files[0].src"},
		{"validation_script", ForgeConfig{ValidationLoop: &ValidationLoop{Script: traversal}}, "forge.github.validation_loop.script"},
		{"validation_schema", ForgeConfig{ValidationLoop: &ValidationLoop{Script: "script.sh", Schema: traversal}}, "forge.github.validation_loop.schema"},
		{"profile", ForgeConfig{OpenShell: &OpenShellConfig{Profiles: []string{traversal}}}, "forge.github.openshell.profiles[0]"},
		{"provider", ForgeConfig{Providers: []string{traversal}}, "forge.github.providers[0]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := &Harness{Forge: map[string]*ForgeConfig{"github": &test.config}}
			err := harness.resolveLocalPathsRelativeTo(harnessDir, root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.field+":")
			assert.Contains(t, err.Error(), "resolves outside fullsend directory")
		})
	}
}

func TestResolveLocalPathsRelativeToHarnessFile_SkillFallbackKeepsDeclaredIdentity(t *testing.T) {
	root := t.TempDir()
	harnessDir := filepath.Join(root, "agents", "custom")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	legacySkill := writeSkill(t, filepath.Join(root, "skills", "legacy"), "legacy-skill")

	harness := &Harness{Skills: []SkillEntry{{Source: "skills/legacy"}}}
	require.NoError(t, harness.resolveLocalPathsRelativeTo(harnessDir, root))
	assert.Equal(t, legacySkill, harness.Skills[0].Source)
	assert.Equal(t, "legacy-skill", harness.Skills[0].mergeName)
}

func TestResolveLocalPathsRelativeToHarnessFile_FallbackAndDeprecation(t *testing.T) {
	root := t.TempDir()
	harnessDir := filepath.Join(root, "agents", "custom")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "policy.yaml"), []byte("version: 1\n"), 0o644))

	harness := &Harness{Policy: "policy.yaml"}
	require.NoError(t, harness.resolveLocalPathsRelativeTo(harnessDir, root))
	assert.Equal(t, filepath.Join(root, "policy.yaml"), harness.Policy)

	diagnostics := harness.Lint()
	require.Len(t, diagnostics, 1)
	assert.Equal(t, SeverityWarning, diagnostics[0].Severity)
	assert.Equal(t, "policy", diagnostics[0].Field)
	assert.Contains(t, diagnostics[0].Message, "deprecated --fullsend-dir-relative fallback")
}

func TestResolveLocalPathsRelativeToHarnessFile_HarnessFileWinsAndWarns(t *testing.T) {
	root := t.TempDir()
	harnessDir := filepath.Join(root, "agents", "custom")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "policy.yaml"), []byte("version: 1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "policy.yaml"), []byte("version: 1\n"), 0o644))

	harness := &Harness{Policy: "policy.yaml"}
	require.NoError(t, harness.resolveLocalPathsRelativeTo(harnessDir, root))
	assert.Equal(t, filepath.Join(harnessDir, "policy.yaml"), harness.Policy)
	diagnostics := harness.Lint()
	require.Len(t, diagnostics, 1)
	assert.Contains(t, diagnostics[0].Message, "harness-relative location")
	assert.Contains(t, diagnostics[0].Message, "deprecated --fullsend-dir-relative location")
}

func TestResolveLocalPathsRelativeToHarnessFile_FallbackCannotEscapeContainment(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "fullsend")
	harnessDir := filepath.Join(root, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(parent, "policy.yaml"), []byte("version: 1\n"), 0o644))

	harness := &Harness{Policy: "../policy.yaml"}
	require.NoError(t, harness.resolveLocalPathsRelativeTo(harnessDir, root))
	assert.Equal(t, filepath.Join(root, "policy.yaml"), harness.Policy)
	assert.Empty(t, harness.Lint())
}

func TestLoadWithBase_LocalPathsRelativeToNestedHarness(t *testing.T) {
	root := t.TempDir()
	harnessDir := filepath.Join(root, "agents", "custom")
	writeSkill(t, filepath.Join(harnessDir, "skills", "custom"), "custom-skill")
	path := writeTestHarness(t, harnessDir, "harness.yaml", `
agent: agent.md
role: custom
policy: policy.yaml
skills:
  - skills/custom
pre_script: scripts/pre.sh
`)

	harness, _, err := LoadWithBase(context.Background(), path, ComposeOpts{WorkspaceRoot: root})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(harnessDir, "agent.md"), harness.Agent)
	assert.Equal(t, filepath.Join(harnessDir, "policy.yaml"), harness.Policy)
	assert.Equal(t, filepath.Join(harnessDir, "skills", "custom"), SkillSources(harness.Skills)[0])
	assert.Equal(t, filepath.Join(harnessDir, "scripts", "pre.sh"), harness.PreScript)
}

func TestLoadWithBase_LocalBasePathsKeepBaseHarnessOrigin(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "bases", "custom")
	childDir := filepath.Join(root, "agents", "child")
	writeTestHarness(t, baseDir, "base.yaml", `
agent: base-agent.md
role: custom
pre_script: base-pre.sh
`)
	path := writeTestHarness(t, childDir, "child.yaml", `
base: ../../bases/custom/base.yaml
agent: child-agent.md
policy: child-policy.yaml
`)

	harness, _, err := LoadWithBase(context.Background(), path, ComposeOpts{WorkspaceRoot: root})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(childDir, "child-agent.md"), harness.Agent)
	assert.Equal(t, filepath.Join(childDir, "child-policy.yaml"), harness.Policy)
	assert.Equal(t, filepath.Join(baseDir, "base-pre.sh"), harness.PreScript)
}

func TestLoadWithBase_LocalPathsFallbackToFullsendDir(t *testing.T) {
	root := t.TempDir()
	harnessDir := filepath.Join(root, "agents", "custom")
	require.NoError(t, os.WriteFile(filepath.Join(root, "policy.yaml"), []byte("version: 1\n"), 0o644))
	path := writeTestHarness(t, harnessDir, "harness.yaml", `
agent: agent.md
role: custom
policy: policy.yaml
`)

	harness, _, err := LoadWithBase(context.Background(), path, ComposeOpts{WorkspaceRoot: root})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "policy.yaml"), harness.Policy)
	require.Len(t, harness.Lint(), 1)
	assert.True(t, strings.Contains(harness.Lint()[0].Message, "deprecated --fullsend-dir-relative fallback"))
}

func TestLoadWithBase_UnusedForgePathDoesNotBlockActiveRun(t *testing.T) {
	root := t.TempDir()
	harnessDir := filepath.Join(root, "agents", "custom")
	path := writeTestHarness(t, harnessDir, "harness.yaml", `
agent: agent.md
role: custom
forge:
  github:
    policy: policy.yaml
  gitlab:
    policy: ../../../outside.yaml
`)

	harness, _, err := LoadWithBase(context.Background(), path, ComposeOpts{
		WorkspaceRoot: root,
		ForgePlatform: "github",
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(harnessDir, "policy.yaml"), harness.Policy)
}

func TestLoadWithBase_LocalParentSegmentsResolveFromNestedHarness(t *testing.T) {
	root := t.TempDir()
	harnessDir := filepath.Join(root, "harness")
	writeSkill(t, filepath.Join(root, "skills", "custom"), "custom-skill")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "plugins", "custom"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "overrides"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "overrides", "custom.md"), []byte("Override.\n"), 0o644))
	path := writeTestHarness(t, harnessDir, "harness.yaml", `
agent: ../agents/agent.md
role: custom
skills:
  - ../skills/custom:
      sub-agents/custom.md: ../overrides/custom.md
plugins:
  - path: ../plugins/custom
`)

	harness, _, err := LoadWithBase(context.Background(), path, ComposeOpts{WorkspaceRoot: root})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "plugins", "custom"), harness.Plugins[0].Path)
	assert.Equal(t, filepath.Join(root, "skills", "custom"), harness.Skills[0].Source)
	require.NotNil(t, harness.Skills[0].Overrides["sub-agents/custom.md"])
	assert.Equal(t, filepath.Join(root, "overrides", "custom.md"), *harness.Skills[0].Overrides["sub-agents/custom.md"])
}
