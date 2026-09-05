package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/pluginformat"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

// claudePluginDir and piPluginDir write the smallest directory each format
// is recognised by, so the bootstrap input can detect a real kind.
func claudePluginDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"`+name+`"}`), 0o644))
	return dir
}

func piPluginDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.js"), []byte("export default function () {}"), 0o644))
	return dir
}

func TestNewHarnessBootstrap_WithoutSecurity(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "agents/test.md",
		Security: &harness.SecurityConfig{
			Enabled: &disabled,
		},
	}
	boot, err := newHarnessBootstrap(h, "sandbox-1", "test", "", nil, nil, "")
	require.NoError(t, err)

	_, ok := boot.(agentruntime.SandboxHooksBootstrap)
	assert.False(t, ok)
	assert.Equal(t, "sandbox-1", boot.SandboxName())
	assert.Equal(t, "agents/test.md", boot.AgentPath())
	assert.Equal(t, "test", boot.AgentName())
	assert.Nil(t, boot.ModelAliases(), "no per-repo overrides means no alias map")
}

// The per-repo models.aliases map reaches the runtime through
// BootstrapInput, which is how it lands in pi's sub-agent model table
// (#7020); a run with overrides must carry them here, not just on
// RunParams.
func TestNewHarnessBootstrap_CarriesModelAliases(t *testing.T) {
	disabled := false
	h := &harness.Harness{
		Agent: "agents/test.md",
		Security: &harness.SecurityConfig{
			Enabled: &disabled,
		},
	}
	aliases := map[string]string{"sonnet": "claude-sonnet-5"}
	boot, err := newHarnessBootstrap(h, "sandbox-1", "test", "", aliases, nil, "")
	require.NoError(t, err)
	assert.Equal(t, aliases, boot.ModelAliases())
}

func TestNewHarnessBootstrap_WithSecurity(t *testing.T) {
	plugin := claudePluginDir(t, "p")
	h := &harness.Harness{
		Agent:   "agents/test.md",
		Skills:  []harness.SkillEntry{{Source: "skills/a"}},
		Plugins: []harness.PluginSpec{{Path: plugin}},
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				Tirith: &harness.TirithConfig{FailOn: "critical"},
			},
		},
	}
	boot, err := newHarnessBootstrap(h, "sandbox-1", "test", "", nil, nil, "")
	require.NoError(t, err)

	hooksBoot, ok := boot.(agentruntime.SandboxHooksBootstrap)
	require.True(t, ok)
	// The harness sandbox_hooks block is carried through unchanged.
	assert.Equal(t, "critical", hooksBoot.SandboxHookConfig().TirithFailOn())
	assert.True(t, hooksBoot.SandboxHookConfig().TirithRequired())
	assert.Equal(t, []agentruntime.PluginInput{
		{Name: "p", Path: plugin, Kind: pluginformat.KindClaude},
	}, boot.Plugins())
	assert.Equal(t, harness.SkillSources(h.Skills), boot.SkillDirs())
}

func TestNewHarnessBootstrap_WithForgeEgressEntry(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{},
		},
	}
	boot, err := newHarnessBootstrap(h, "sandbox-1", "test", "gitlab.company.com:443", nil, nil, "")
	require.NoError(t, err)

	hooksBoot, ok := boot.(agentruntime.SandboxHooksBootstrap)
	require.True(t, ok)
	assert.Equal(t, "gitlab.company.com:443", hooksBoot.SandboxHookConfig().ForgeEgressEntry())
}

// TestNewHarnessBootstrap_CarriesPlugins covers the mapping the runtimes
// dispatch on: every entry is passed through with the format its directory
// is in, so each runtime can load its own and name the rest.
func TestNewHarnessBootstrap_CarriesPlugins(t *testing.T) {
	t.Parallel()
	claude := claudePluginDir(t, "gopls-lsp")
	diagnostics := piPluginDir(t, "go-diagnostics")
	fff := piPluginDir(t, "pi-fff")
	h := &harness.Harness{
		Agent:  "/fs/agents/code.md",
		Skills: []harness.SkillEntry{{Source: "/fs/skills/a"}},
		Plugins: []harness.PluginSpec{
			{Path: claude},
			{Path: diagnostics},
			{
				Path: fff,
				Env:  map[string]string{"FFF_MULTIGREP": "1"},
				Pi:   &harness.PiPluginOptions{Args: []string{"--fff-mode", "override"}},
			},
		},
	}
	boot, err := newHarnessBootstrap(h, "sb", "code", "", nil, nil, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"/fs/skills/a"}, boot.SkillDirs())
	assert.Equal(t, []agentruntime.PluginInput{
		{Name: "gopls-lsp", Path: claude, Kind: pluginformat.KindClaude},
		{Name: "go-diagnostics", Path: diagnostics, Kind: pluginformat.KindPi},
		{
			Name: "pi-fff", Path: fff, Kind: pluginformat.KindPi,
			Env:    map[string]string{"FFF_MULTIGREP": "1"},
			PiArgs: []string{"--fff-mode", "override"},
		},
	}, boot.Plugins())

	// The security-enabled wrapper exposes the same list.
	_, hooked := boot.(agentruntime.SandboxHooksBootstrap)
	require.True(t, hooked, "security defaults on, so the hooks wrapper is returned")

	// No plugins: nil, not an empty slice, so runtimes can len() it.
	bare, err := newHarnessBootstrap(&harness.Harness{Agent: "a.md"}, "sb", "code", "", nil, nil, "")
	require.NoError(t, err)
	assert.Nil(t, bare.Plugins())
	got, err := pluginInputs(nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestNewHarnessBootstrap_UndetectablePlugin covers the ordering guard: by
// this point ValidateFilesExist has already refused a directory no runtime
// would load, so a failure here is a caller bug and is reported with the
// offending entry rather than silently producing a kindless input.
func TestNewHarnessBootstrap_UndetectablePlugin(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "neither")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("#"), 0o644))

	_, err := newHarnessBootstrap(&harness.Harness{
		Agent:   "a.md",
		Plugins: []harness.PluginSpec{{Path: dir}},
	}, "sb", "code", "", nil, nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugins[0]")
	assert.Contains(t, err.Error(), "not a Claude plugin")

	_, err = newHarnessBootstrap(&harness.Harness{
		Agent:   "a.md",
		Plugins: []harness.PluginSpec{{Path: filepath.Join(t.TempDir(), "missing")}},
	}, "sb", "code", "", nil, nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugins[0]")
}

// TestDescribePlugins covers the run header line: each entry is tagged
// with the format it is in, and an unreadable one is printed bare.
func TestDescribePlugins(t *testing.T) {
	t.Parallel()
	claude := claudePluginDir(t, "gopls-lsp")
	pi := piPluginDir(t, "go-diagnostics")
	missing := filepath.Join(t.TempDir(), "missing")

	assert.Equal(t, []string{
		claude + " (claude)",
		pi + " (pi)",
		missing,
	}, describePlugins([]harness.PluginSpec{{Path: claude}, {Path: pi}, {Path: missing}}))
}
