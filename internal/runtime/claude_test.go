package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

type bootstrapInput struct {
	sandboxName string
	agentPath   string
}

func (b bootstrapInput) SandboxName() string  { return b.sandboxName }
func (b bootstrapInput) AgentPath() string    { return b.agentPath }
func (b bootstrapInput) SkillDirs() []string  { return nil }
func (b bootstrapInput) PluginDirs() []string { return nil }

func TestBootstrap_EmptyAgentPath(t *testing.T) {
	err := ClaudeRuntime{}.Bootstrap(bootstrapInput{sandboxName: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent path is required")
}

func TestDefaultRuntime(t *testing.T) {
	backend := Default()
	assert.Equal(t, "claude", backend.Name())
	assert.Equal(t, sandbox.SandboxClaudeConfig, backend.ConfigDir())
	assert.Equal(t, sandbox.SandboxWorkspace, backend.WorkspaceDir())
	assert.Contains(t, backend.EnvExports()[0], "CLAUDE_CONFIG_DIR")
	assert.NotNil(t, backend.Transcripts)
}

func testRunCommand(agentName, model, repoDir string, pluginDirs []string, debug string) string {
	return buildRunCommand(RunParams{
		AgentBaseName: agentName,
		Model:         model,
		RepoDir:       repoDir,
		PluginDirs:    pluginDirs,
		Debug:         debug,
	})
}

func TestBuildRunCommand_Basic(t *testing.T) {
	cmd := testRunCommand("hello-world", "", "/sandbox/workspace/repo", nil, "")
	assert.Contains(t, cmd, "cd /sandbox/workspace/repo")
	assert.Contains(t, cmd, "--agent 'hello-world'")
	assert.NotContains(t, cmd, "--model")
	assert.NotContains(t, cmd, "--plugin-dir")
}

func TestBuildRunCommand_WithModel(t *testing.T) {
	cmd := testRunCommand("hello-world", "sonnet", "/sandbox/workspace/repo", nil, "")
	assert.Contains(t, cmd, "--model 'sonnet'")
	assert.Contains(t, cmd, "--agent 'hello-world'")
}

func TestBuildRunCommand_EscapesQuotes(t *testing.T) {
	cmd := testRunCommand("test'name", "", "/sandbox/workspace/repo", nil, "")
	assert.NotContains(t, cmd, "'test'name'")
	assert.Contains(t, cmd, "'test'\\''name'")
}

func TestBuildRunCommand_WithPluginDirs(t *testing.T) {
	cmd := testRunCommand("agent", "", "/sandbox/workspace/repo", []string{"/sandbox/claude-config/plugins/gopls-lsp"}, "")
	assert.Contains(t, cmd, "--plugin-dir '/sandbox/claude-config/plugins/gopls-lsp'")
}

func TestBuildRunCommand_DebugAll(t *testing.T) {
	cmd := testRunCommand("agent", "", "/sandbox/workspace/repo", nil, "*")
	assert.Contains(t, cmd, "--debug-file '/sandbox/workspace/claude-debug.log'")
	assert.NotContains(t, cmd, "--debug '")
}

func TestBuildRunCommand_DebugFiltered(t *testing.T) {
	cmd := testRunCommand("agent", "", "/sandbox/workspace/repo", nil, "api,hooks")
	assert.Contains(t, cmd, "--debug-file '/sandbox/workspace/claude-debug.log'")
	assert.Contains(t, cmd, "--debug 'api,hooks'")
}

func TestBuildRunCommand_MultiplePluginDirs(t *testing.T) {
	cmd := testRunCommand("agent", "", "/sandbox/workspace/repo", []string{
		"/sandbox/claude-config/plugins/gopls-lsp",
		"/sandbox/claude-config/plugins/other-lsp",
	}, "")
	assert.Contains(t, cmd, "--plugin-dir '/sandbox/claude-config/plugins/gopls-lsp'")
	assert.Contains(t, cmd, "--plugin-dir '/sandbox/claude-config/plugins/other-lsp'")
}

func TestBuildRunCommand_PluginDirEscapesQuotes(t *testing.T) {
	cmd := testRunCommand("agent", "", "/sandbox/workspace/repo", []string{"/sandbox/path'with'quotes"}, "")
	assert.Contains(t, cmd, "--plugin-dir '/sandbox/path'\\''with'\\''quotes'")
}

func TestBuildRunCommand_NoPlugins(t *testing.T) {
	cmd := testRunCommand("agent", "", "/sandbox/workspace/repo", nil, "")
	assert.NotContains(t, cmd, "--plugin-dir")
}

func TestBuildRunCommand_DebugDisabled(t *testing.T) {
	cmd := testRunCommand("agent", "", "/sandbox/workspace/repo", nil, "")
	assert.NotContains(t, cmd, "--debug-file")
	assert.NotContains(t, cmd, "--debug")
}

func TestBuildRunCommand_DebugEscapesQuotes(t *testing.T) {
	cmd := testRunCommand("agent", "", "/sandbox/workspace/repo", nil, "api'hooks")
	assert.Contains(t, cmd, "--debug 'api'\\''hooks'")
}

func TestBuildRunCommand_NoDoubleSpaces(t *testing.T) {
	tests := []struct {
		name       string
		agentName  string
		model      string
		pluginDirs []string
		debug      string
	}{
		{"no optional flags", "agent", "", nil, ""},
		{"model only", "agent", "sonnet", nil, ""},
		{"plugins only", "agent", "", []string{"/sandbox/plugins/gopls"}, ""},
		{"debug only", "agent", "", nil, "*"},
		{"debug filtered", "agent", "", nil, "api,hooks"},
		{"all flags", "agent", "sonnet", []string{"/sandbox/plugins/gopls", "/sandbox/plugins/other"}, "api,hooks"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := testRunCommand(tc.agentName, tc.model, "/sandbox/workspace/repo", tc.pluginDirs, tc.debug)
			assert.NotContains(t, cmd, "  ", "command should not contain double spaces")
		})
	}
}

func TestBuildPluginConfigs_SinglePlugin(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "gopls-lsp")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"),
		[]byte(`{"name":"gopls-lsp"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ".lsp.json"),
		[]byte(`{"go":{"command":"gopls","args":["serve"]}}`), 0o644))

	entries, err := buildPluginConfigs(
		[]string{pluginDir}, "/sandbox/plugins", "/sandbox/plugins/marketplaces/claude-plugins-official",
		"claude-plugins-official", "1.0.0", "/sandbox/claude-config",
	)
	require.NoError(t, err)
	require.Len(t, entries, 4)

	var mkt map[string]any
	require.NoError(t, json.Unmarshal(entries[0].data, &mkt))
	plugins := mkt["plugins"].([]any)
	require.Len(t, plugins, 1)
	p := plugins[0].(map[string]any)
	assert.Equal(t, "gopls-lsp", p["name"])
	assert.NotNil(t, p["lspServers"])
}

func TestBuildPluginConfigs_MultiplePlugins(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"plugin-a", "plugin-b"} {
		pd := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(pd, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(pd, "plugin.json"),
			[]byte(fmt.Sprintf(`{"name":%q}`, name)), 0o644))
	}

	entries, err := buildPluginConfigs(
		[]string{filepath.Join(dir, "plugin-a"), filepath.Join(dir, "plugin-b")},
		"/sandbox/plugins", "/sandbox/plugins/marketplaces/claude-plugins-official",
		"claude-plugins-official", "1.0.0", "/sandbox/claude-config",
	)
	require.NoError(t, err)
	require.Len(t, entries, 4)

	var mkt map[string]any
	require.NoError(t, json.Unmarshal(entries[0].data, &mkt))
	plugins := mkt["plugins"].([]any)
	assert.Len(t, plugins, 2)
}

func TestBuildPluginConfigs_NoLspJSON(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "simple-plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"),
		[]byte(`{"name":"simple-plugin"}`), 0o644))

	entries, err := buildPluginConfigs(
		[]string{pluginDir}, "/sandbox/plugins", "/sandbox/plugins/marketplaces/claude-plugins-official",
		"claude-plugins-official", "1.0.0", "/sandbox/claude-config",
	)
	require.NoError(t, err)

	var mkt map[string]any
	require.NoError(t, json.Unmarshal(entries[0].data, &mkt))
	plugins := mkt["plugins"].([]any)
	p := plugins[0].(map[string]any)
	assert.Nil(t, p["lspServers"])
}

func TestBuildPluginConfigs_InvalidLspJSON(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "bad-lsp")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"),
		[]byte(`{"name":"bad-lsp"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ".lsp.json"),
		[]byte(`{broken`), 0o644))

	entries, err := buildPluginConfigs(
		[]string{pluginDir}, "/sandbox/plugins", "/sandbox/plugins/marketplaces/claude-plugins-official",
		"claude-plugins-official", "1.0.0", "/sandbox/claude-config",
	)
	require.NoError(t, err)

	var mkt map[string]any
	require.NoError(t, json.Unmarshal(entries[0].data, &mkt))
	plugins := mkt["plugins"].([]any)
	p := plugins[0].(map[string]any)
	assert.Nil(t, p["lspServers"])
}

func TestBuildPluginConfigs_EmptyLspJSON(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "empty-lsp")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"),
		[]byte(`{"name":"empty-lsp"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ".lsp.json"), []byte(``), 0o644))

	entries, err := buildPluginConfigs(
		[]string{pluginDir}, "/sandbox/plugins", "/sandbox/plugins/marketplaces/claude-plugins-official",
		"claude-plugins-official", "1.0.0", "/sandbox/claude-config",
	)
	require.NoError(t, err)

	var mkt map[string]any
	require.NoError(t, json.Unmarshal(entries[0].data, &mkt))
	plugins := mkt["plugins"].([]any)
	p := plugins[0].(map[string]any)
	assert.Nil(t, p["lspServers"])
}

func TestBuildPluginConfigs_ConfigStructure(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "test-plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"),
		[]byte(`{"name":"test-plugin"}`), 0o644))

	entries, err := buildPluginConfigs(
		[]string{pluginDir}, "/sandbox/plugins", "/sandbox/plugins/marketplaces/claude-plugins-official",
		"claude-plugins-official", "1.0.0", "/sandbox/claude-config",
	)
	require.NoError(t, err)
	require.Len(t, entries, 4)

	assert.True(t, strings.HasSuffix(entries[0].path, "/marketplace.json"))
	assert.True(t, strings.HasSuffix(entries[1].path, "/known_marketplaces.json"))
	assert.True(t, strings.HasSuffix(entries[2].path, "/installed_plugins.json"))
	assert.True(t, strings.HasSuffix(entries[3].path, "/settings.json"))
}

func TestBuildPluginConfigs_EmptyPluginList(t *testing.T) {
	entries, err := buildPluginConfigs(
		nil, "/sandbox/plugins", "/sandbox/plugins/marketplaces/claude-plugins-official",
		"claude-plugins-official", "1.0.0", "/sandbox/claude-config",
	)
	require.NoError(t, err)
	require.Len(t, entries, 4)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(entries[3].data, &settings))
	enabled := settings["enabledPlugins"].(map[string]any)
	assert.Len(t, enabled, 0)
}

func TestClaudeRuntime_Run_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	var metrics RunMetrics
	printer := ui.New(io.Discard)

	exitCode, err := ClaudeRuntime{}.Run(context.Background(), RunParams{
		SandboxName:   "test-sandbox",
		AgentBaseName: "test-agent",
		RepoDir:       "/sandbox/workspace/repo",
		Timeout:       10 * time.Second,
	}, printer, time.Now(), &metrics)

	assert.Error(t, err)
	assert.Equal(t, -1, exitCode)
}

func TestClaudeRuntime_Bootstrap_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	agentDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "agent.md"), []byte("test"), 0o644))

	err := ClaudeRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "test-sandbox",
		agentPath:   agentDir,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "creating runtime config dirs")
}

func TestClaudeRuntime_ClearIterationArtifacts_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	err := ClaudeRuntime{}.ClearIterationArtifacts("test-sandbox")
	assert.Error(t, err)
}

func TestClaudeRuntime_ExtractTranscripts_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	outputDir := t.TempDir()
	err := ClaudeRuntime{}.ExtractTranscripts("test-sandbox", "test-agent", outputDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "finding transcripts")
}

func TestResolveSkillDisplayName(t *testing.T) {
	tests := []struct {
		name     string
		dirName  string
		skillMD  string // empty means no SKILL.md
		expected string
	}{
		{
			name:     "frontmatter name overrides directory name",
			dirName:  "tree",
			skillMD:  "---\nname: architecture\n---\n# Architecture skill",
			expected: "architecture",
		},
		{
			name:     "falls back to filepath.Base when no SKILL.md",
			dirName:  "my-skill",
			skillMD:  "",
			expected: "my-skill",
		},
		{
			name:     "falls back when frontmatter has no name field",
			dirName:  "tree",
			skillMD:  "---\ndescription: some skill\n---\n# Content",
			expected: "tree",
		},
		{
			name:     "falls back when SKILL.md has no frontmatter",
			dirName:  "tree",
			skillMD:  "# Just a heading\nNo frontmatter here.",
			expected: "tree",
		},
		{
			name:     "local skill with matching directory name",
			dirName:  "public-research",
			skillMD:  "---\nname: public-research\n---\n# Public Research",
			expected: "public-research",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), tc.dirName)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			if tc.skillMD != "" {
				require.NoError(t, os.WriteFile(
					filepath.Join(dir, "SKILL.md"),
					[]byte(tc.skillMD), 0o644))
			}

			got := resolveSkillDisplayName(dir)
			assert.Equal(t, tc.expected, got)
		})
	}
}
