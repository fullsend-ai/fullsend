package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	agentName   string
	skillDirs   []string
	pluginDirs  []string
}

func (b bootstrapInput) SandboxName() string  { return b.sandboxName }
func (b bootstrapInput) AgentPath() string    { return b.agentPath }
func (b bootstrapInput) AgentName() string    { return b.agentName }
func (b bootstrapInput) SkillDirs() []string  { return b.skillDirs }
func (b bootstrapInput) PluginDirs() []string { return b.pluginDirs }

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

func TestAgentDestName(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
		agentPath string
		expected  string
	}{
		{
			name:      "uses agent name without .md suffix",
			agentName: "review",
			agentPath: "/cache/abc123/content",
			expected:  "review.md",
		},
		{
			name:      "strips .md suffix to avoid duplication",
			agentName: "review.md",
			agentPath: "/cache/abc123/content",
			expected:  "review.md",
		},
		{
			name:      "falls back to basename when agent name is empty",
			agentName: "",
			agentPath: "/path/to/agents/code.md",
			expected:  "code.md",
		},
		{
			name:      "fallback with cache path returns content basename",
			agentName: "",
			agentPath: "/cache/abc123/content",
			expected:  "content",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := agentDestName(tc.agentName, tc.agentPath)
			assert.Equal(t, tc.expected, got)
		})
	}
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

// TestClaudeRuntime_Bootstrap_AgentNameDest verifies that Bootstrap uses
// agentDestName to derive the destination filename and calls UploadFile
// with the correct path. A stub openshell binary is placed on PATH so
// sandbox operations succeed without a real sandbox.
func TestClaudeRuntime_Bootstrap_AgentNameDest(t *testing.T) {
	// Create a stub openshell that always exits 0.
	stubDir := t.TempDir()
	stubPath := filepath.Join(stubDir, "openshell")
	require.NoError(t, os.WriteFile(stubPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", stubDir)

	agentFile := filepath.Join(t.TempDir(), "content")
	require.NoError(t, os.WriteFile(agentFile, []byte("# agent definition"), 0o644))

	err := ClaudeRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "test-sandbox",
		agentPath:   agentFile,
		agentName:   "review",
	})
	// The stub openshell succeeds for all sandbox calls, so Bootstrap
	// should complete without error, exercising agentDestName and the
	// UploadFile call path.
	assert.NoError(t, err)
}

// TestClaudeRuntime_Bootstrap_AgentNameEmpty verifies the fallback path
// where AgentName is empty and the source basename is used.
func TestClaudeRuntime_Bootstrap_AgentNameEmpty(t *testing.T) {
	stubDir := t.TempDir()
	stubPath := filepath.Join(stubDir, "openshell")
	require.NoError(t, os.WriteFile(stubPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", stubDir)

	agentFile := filepath.Join(t.TempDir(), "code.md")
	require.NoError(t, os.WriteFile(agentFile, []byte("# agent definition"), 0o644))

	err := ClaudeRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "test-sandbox",
		agentPath:   agentFile,
		agentName:   "",
	})
	assert.NoError(t, err)
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

// fakeOpenshellBootstrap installs a fake "openshell" on PATH (ahead of the
// real "tar", which is left reachable via the pre-existing PATH) that logs
// every invocation's full argv to logPath and, on "sandbox upload <name>
// <local> <remote>" where <local> is a tarball, copies it to sentinelPath.
// Bootstrap uploads several other regular files (the agent definition,
// plugin marketplace config) in the same run, so the copy is restricted to
// *.tar.gz uploads — otherwise a later, unrelated file upload would
// overwrite the sentinel before the test can inspect it. This lets tests
// verify the real archive content and the real destination path Bootstrap
// computes, not just that some tar command ran. See #5247 review findings:
// the original mocked-tar tests proved "UploadDir was called" but not "it
// was called with the right destination or transferred the real content."
func fakeOpenshellBootstrap(t *testing.T, logPath, sentinelPath string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> '" + logPath + "'\n" +
		"if [ \"$2\" = \"upload\" ]; then\n" +
		"  case \"$4\" in\n" +
		"    *.tar.gz) cp \"$4\" '" + sentinelPath + "' ;;\n" +
		"  esac\n" +
		"fi\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func extractTarball(t *testing.T, archivePath string) string {
	t.Helper()
	dest := t.TempDir()
	out, err := exec.Command("tar", "-xzf", archivePath, "-C", dest).CombinedOutput()
	require.NoError(t, err, "extracting archive: %s", string(out))
	return dest
}

// TestClaudeRuntime_Bootstrap_SkillSymlink verifies that skill uploads
// survive a symlinked cache path (as created by fetch.CacheNamedSymlink):
// the real target content — not a dangling symlink entry — must actually
// land in the sandbox, at a destination named after the skill rather than
// the cache-internal "tree". See #5247.
func TestClaudeRuntime_Bootstrap_SkillSymlink(t *testing.T) {
	// Create a cache-like structure: tree/ contains the skill, and
	// "pr-review" is a symlink to "tree".
	cacheDir := t.TempDir()
	treeDir := filepath.Join(cacheDir, "tree")
	require.NoError(t, os.MkdirAll(treeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(treeDir, "SKILL.md"),
		[]byte("---\nname: pr-review\n---\n# PR Review"), 0o644))
	require.NoError(t, os.Symlink("tree", filepath.Join(cacheDir, "pr-review")))

	skillPath := filepath.Join(cacheDir, "pr-review") // symlink

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshellBootstrap(t, logPath, sentinelPath)

	agentFile := filepath.Join(t.TempDir(), "agent.md")
	require.NoError(t, os.WriteFile(agentFile, []byte("# agent"), 0o644))

	err := ClaudeRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "test-sandbox",
		agentPath:   agentFile,
		agentName:   "review",
		skillDirs:   []string{skillPath},
	})
	require.NoError(t, err)

	// The real SKILL.md content must have made it into the archive that
	// was uploaded — not a zero-byte dangling symlink entry.
	extracted := extractTarball(t, sentinelPath)
	got, readErr := os.ReadFile(filepath.Join(extracted, "SKILL.md"))
	require.NoError(t, readErr)
	assert.Contains(t, string(got), "# PR Review")

	// The sandbox extraction destination must be named after the skill,
	// not the cache-internal "tree" directory name — the regression this
	// issue (#5247) describes.
	log, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(log), sandbox.SandboxClaudeConfig+"/skills/pr-review")
	assert.NotContains(t, string(log), "/skills/tree")
}

// TestClaudeRuntime_Bootstrap_SkillRegularDir verifies no regression: a
// regular (non-symlink) skill directory still uploads its real content to
// the same destination shape as before.
func TestClaudeRuntime_Bootstrap_SkillRegularDir(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: my-skill\n---\n# My Skill"), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshellBootstrap(t, logPath, sentinelPath)

	agentFile := filepath.Join(t.TempDir(), "agent.md")
	require.NoError(t, os.WriteFile(agentFile, []byte("# agent"), 0o644))

	err := ClaudeRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "test-sandbox",
		agentPath:   agentFile,
		agentName:   "review",
		skillDirs:   []string{skillDir},
	})
	require.NoError(t, err)

	extracted := extractTarball(t, sentinelPath)
	got, readErr := os.ReadFile(filepath.Join(extracted, "SKILL.md"))
	require.NoError(t, readErr)
	assert.Contains(t, string(got), "# My Skill")

	log, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(log), sandbox.SandboxClaudeConfig+"/skills/my-skill")
}

// TestClaudeRuntime_Bootstrap_PluginSymlink verifies that plugin uploads
// survive a symlinked path the same way skills do. No current code path
// produces a symlinked plugin directory yet (plugins don't go through
// cache/URL resolution) — this is forward-looking coverage for the
// "Related risk" #5247 calls out: bootstrapPlugins used the same unsafe
// pattern the skill loop did, and would reproduce this exact bug the day
// plugin cache resolution lands.
func TestClaudeRuntime_Bootstrap_PluginSymlink(t *testing.T) {
	cacheDir := t.TempDir()
	treeDir := filepath.Join(cacheDir, "tree")
	require.NoError(t, os.MkdirAll(treeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(treeDir, "plugin.json"),
		[]byte(`{"name":"gopls-lsp"}`), 0o644))
	require.NoError(t, os.Symlink("tree", filepath.Join(cacheDir, "gopls-lsp")))

	pluginPath := filepath.Join(cacheDir, "gopls-lsp") // symlink

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshellBootstrap(t, logPath, sentinelPath)

	agentFile := filepath.Join(t.TempDir(), "agent.md")
	require.NoError(t, os.WriteFile(agentFile, []byte("# agent"), 0o644))

	err := ClaudeRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "test-sandbox",
		agentPath:   agentFile,
		agentName:   "review",
		pluginDirs:  []string{pluginPath},
	})
	require.NoError(t, err)

	extracted := extractTarball(t, sentinelPath)
	got, readErr := os.ReadFile(filepath.Join(extracted, "plugin.json"))
	require.NoError(t, readErr)
	assert.Contains(t, string(got), "gopls-lsp")

	log, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(log), sandbox.SandboxClaudeConfig+"/plugins/gopls-lsp")
	assert.NotContains(t, string(log), "/plugins/tree")
}

// TestClaudeRuntime_Bootstrap_DuplicateSkillNames_FailsLoudly verifies that
// two distinct skill sources resolving to the same sandbox directory name
// are rejected up front with a clear error, instead of silently uploading
// one and then having UploadDir's destination-clearing discard it when the
// second is extracted on top — the same "content silently isn't the one you
// expected" shape as #5247, reached via a naming collision instead of a
// broken symlink.
func TestClaudeRuntime_Bootstrap_DuplicateSkillNames_FailsLoudly(t *testing.T) {
	skillA := filepath.Join(t.TempDir(), "pr-review")
	require.NoError(t, os.MkdirAll(skillA, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillA, "SKILL.md"), []byte("skill A"), 0o644))

	skillB := filepath.Join(t.TempDir(), "pr-review") // same basename, different source
	require.NoError(t, os.MkdirAll(skillB, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillB, "SKILL.md"), []byte("skill B"), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshellBootstrap(t, logPath, sentinelPath)

	agentFile := filepath.Join(t.TempDir(), "agent.md")
	require.NoError(t, os.WriteFile(agentFile, []byte("# agent"), 0o644))

	err := ClaudeRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "test-sandbox",
		agentPath:   agentFile,
		agentName:   "review",
		skillDirs:   []string{skillA, skillB},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pr-review")

	// Nothing should have been uploaded once the collision is detected.
	_, statErr := os.Stat(sentinelPath)
	assert.True(t, os.IsNotExist(statErr), "no skill upload should occur when a name collision is detected")
}

// TestClaudeRuntime_Bootstrap_ReservedPluginName_FailsLoudly verifies that a
// plugin directory named after any fixed path bootstrapPlugins or
// buildPluginConfigs creates directly under configDir/plugins/ — the two
// marketplace-scaffolding directories ("marketplaces", "cache") and the two
// shared registration files ("known_marketplaces.json",
// "installed_plugins.json") — is rejected up front. Without this check, such
// a plugin's content upload would resolve to the same sandbox destination:
// for the directories, UploadDir's rm -rf-before-extract silently destroys
// every other plugin's registration in the same Bootstrap call; for the
// files, the plugin's own directory upload replaces the destination with a
// directory before buildPluginConfigs tries to write the file there.
func TestClaudeRuntime_Bootstrap_ReservedPluginName_FailsLoudly(t *testing.T) {
	for _, reserved := range []string{"marketplaces", "cache", "known_marketplaces.json", "installed_plugins.json"} {
		t.Run(reserved, func(t *testing.T) {
			pluginDir := filepath.Join(t.TempDir(), reserved)
			require.NoError(t, os.MkdirAll(pluginDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"name":"x"}`), 0o644))

			binDir := t.TempDir()
			logPath := filepath.Join(binDir, "openshell.log")
			sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
			fakeOpenshellBootstrap(t, logPath, sentinelPath)

			agentFile := filepath.Join(t.TempDir(), "agent.md")
			require.NoError(t, os.WriteFile(agentFile, []byte("# agent"), 0o644))

			err := ClaudeRuntime{}.Bootstrap(bootstrapInput{
				sandboxName: "test-sandbox",
				agentPath:   agentFile,
				agentName:   "review",
				pluginDirs:  []string{pluginDir},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), reserved)

			// Nothing should have been uploaded once the collision is detected.
			_, statErr := os.Stat(sentinelPath)
			assert.True(t, os.IsNotExist(statErr), "no plugin upload should occur when a reserved-name collision is detected")
		})
	}
}

// TestClaudeRuntime_Bootstrap_PluginMaliciousName_MarketplaceSetupQuoted
// verifies bootstrapPlugins' marketplace-scaffolding command (the mkdir/echo
// batch built from each plugin's basename) shell-quotes the basename before
// interpolating it into the "sandbox exec" call, the same way UploadDir's
// extract command quotes its own path arguments. Before this fix, a
// basename containing a single quote could break out of the surrounding
// `echo '# <name>'` string.
func TestClaudeRuntime_Bootstrap_PluginMaliciousName_MarketplaceSetupQuoted(t *testing.T) {
	evilName := "evil'; touch pwned #"
	pluginDir := filepath.Join(t.TempDir(), evilName)
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"name":"x"}`), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshellBootstrap(t, logPath, sentinelPath)

	agentFile := filepath.Join(t.TempDir(), "agent.md")
	require.NoError(t, os.WriteFile(agentFile, []byte("# agent"), 0o644))

	err := ClaudeRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "test-sandbox",
		agentPath:   agentFile,
		agentName:   "review",
		pluginDirs:  []string{pluginDir},
	})
	require.NoError(t, err)

	log, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	// ShellQuote is applied to the full destination path the basename is
	// embedded in, not the basename in isolation, so check for the escaped
	// fragment ("'\''" in place of the embedded quote) rather than a
	// standalone quoted basename.
	escapedName := strings.ReplaceAll(evilName, "'", `'\''`)
	assert.Contains(t, string(log), escapedName,
		"the embedded single quote in the basename must be escaped within the quoted destination path")
	assert.NotContains(t, string(log), "'"+evilName+"'",
		"the raw, unescaped basename must never appear directly quoted — that shape is what broke out of the quoting before the fix")
}

func TestClaudeRuntimeSystem(t *testing.T) {
	// gen_ai.system is the OTEL vendor value for the runtime's models; Claude
	// Code runs Anthropic models. Sourcing it from the runtime (not hardcoding
	// in the CLI) keeps telemetry runtime-agnostic per ADR 0050.
	assert.Equal(t, "anthropic", ClaudeRuntime{}.System())
}
