package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestRunCommand_RequiresAgentName(t *testing.T) {
	cmd := newRunCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestRunCommand_HasFullsendDirFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("fullsend-dir")
	require.NotNil(t, flag)
	assert.Equal(t, "", flag.DefValue)

	annotations := flag.Annotations
	require.Contains(t, annotations, "cobra_annotation_bash_completion_one_required_flag")
}

func TestRunCommand_RegisteredOnRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, sub := range root.Commands() {
		if sub.Name() == "run" {
			found = true
			break
		}
	}
	assert.True(t, found, "run command should be registered on root")
}

func TestRunCommand_HasNoPostScriptFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("no-post-script")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

func TestRunCommand_HasOutputDirFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("output-dir")
	require.NotNil(t, flag)
	assert.Equal(t, "", flag.DefValue)
}

func TestRunCommand_HasTargetRepoFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("target-repo")
	require.NotNil(t, flag)
	assert.Equal(t, "", flag.DefValue)

	annotations := flag.Annotations
	require.Contains(t, annotations, "cobra_annotation_bash_completion_one_required_flag")
}

func TestBuildClaudeCommand_Basic(t *testing.T) {
	cmd := buildClaudeCommand("hello-world", "", "/sandbox/workspace/repo", nil, "")
	assert.Contains(t, cmd, "cd /sandbox/workspace/repo")
	assert.Contains(t, cmd, "--agent 'hello-world'")
	assert.NotContains(t, cmd, "--model")
	assert.NotContains(t, cmd, "--plugin-dir")
}

func TestBuildClaudeCommand_WithModel(t *testing.T) {
	cmd := buildClaudeCommand("hello-world", "sonnet", "/sandbox/workspace/repo", nil, "")
	assert.Contains(t, cmd, "--model 'sonnet'")
	assert.Contains(t, cmd, "--agent 'hello-world'")
}

func TestBuildClaudeCommand_EscapesQuotes(t *testing.T) {
	cmd := buildClaudeCommand("test'name", "", "/sandbox/workspace/repo", nil, "")
	assert.NotContains(t, cmd, "'test'name'")
	assert.Contains(t, cmd, "'test'\\''name'")
}

func TestBuildClaudeCommand_WithPluginDirs(t *testing.T) {
	cmd := buildClaudeCommand("agent", "", "/sandbox/workspace/repo", []string{"/sandbox/claude-config/plugins/gopls-lsp"}, "")
	assert.Contains(t, cmd, "--plugin-dir '/sandbox/claude-config/plugins/gopls-lsp'")
}

func TestBuildClaudeCommand_MultiplePluginDirs(t *testing.T) {
	cmd := buildClaudeCommand("agent", "", "/sandbox/workspace/repo", []string{
		"/sandbox/claude-config/plugins/gopls-lsp",
		"/sandbox/claude-config/plugins/other-lsp",
	}, "")
	assert.Contains(t, cmd, "--plugin-dir '/sandbox/claude-config/plugins/gopls-lsp'")
	assert.Contains(t, cmd, "--plugin-dir '/sandbox/claude-config/plugins/other-lsp'")
}

func TestBuildClaudeCommand_PluginDirEscapesQuotes(t *testing.T) {
	cmd := buildClaudeCommand("agent", "", "/sandbox/workspace/repo", []string{"/tmp/path'with'quotes"}, "")
	assert.Contains(t, cmd, "--plugin-dir '/tmp/path'\\''with'\\''quotes'")
}

func TestBuildClaudeCommand_NoPlugins(t *testing.T) {
	cmd := buildClaudeCommand("agent", "", "/sandbox/workspace/repo", nil, "")
	assert.NotContains(t, cmd, "--plugin-dir")
}

func TestBuildClaudeCommand_DebugAll(t *testing.T) {
	cmd := buildClaudeCommand("agent", "", "/sandbox/workspace/repo", nil, "*")
	assert.Contains(t, cmd, "--debug-file '/sandbox/workspace/claude-debug.log'")
	assert.NotContains(t, cmd, "--debug '")
}

func TestBuildClaudeCommand_DebugFiltered(t *testing.T) {
	cmd := buildClaudeCommand("agent", "", "/sandbox/workspace/repo", nil, "api,hooks")
	assert.Contains(t, cmd, "--debug-file '/sandbox/workspace/claude-debug.log'")
	assert.Contains(t, cmd, "--debug 'api,hooks'")
}

func TestBuildClaudeCommand_DebugDisabled(t *testing.T) {
	cmd := buildClaudeCommand("agent", "", "/sandbox/workspace/repo", nil, "")
	assert.NotContains(t, cmd, "--debug-file")
	assert.NotContains(t, cmd, "--debug")
}

func TestBuildClaudeCommand_DebugEscapesQuotes(t *testing.T) {
	cmd := buildClaudeCommand("agent", "", "/sandbox/workspace/repo", nil, "api'hooks")
	assert.Contains(t, cmd, "--debug 'api'\\''hooks'")
}

func TestBuildClaudeCommand_NoDoubleSpaces(t *testing.T) {
	tests := []struct {
		name       string
		agentName  string
		model      string
		pluginDirs []string
		debug      string
	}{
		{"no optional flags", "agent", "", nil, ""},
		{"model only", "agent", "sonnet", nil, ""},
		{"plugins only", "agent", "", []string{"/tmp/plugins/gopls"}, ""},
		{"debug only", "agent", "", nil, "*"},
		{"debug filtered", "agent", "", nil, "api,hooks"},
		{"all flags", "agent", "sonnet", []string{"/tmp/plugins/gopls", "/tmp/plugins/other"}, "api,hooks"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildClaudeCommand(tc.agentName, tc.model, "/sandbox/workspace/repo", tc.pluginDirs, tc.debug)
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
		[]string{pluginDir}, "/tmp/plugins", "/tmp/plugins/marketplaces/claude-plugins-official", "claude-plugins-official", "1.0.0",
	)
	require.NoError(t, err)
	require.Len(t, entries, 4)

	// marketplace.json: has lspServers, owner, correct plugin fields.
	var mkt map[string]any
	require.NoError(t, json.Unmarshal(entries[0].data, &mkt))
	assert.NotNil(t, mkt["owner"])
	plugins := mkt["plugins"].([]any)
	require.Len(t, plugins, 1)
	p := plugins[0].(map[string]any)
	assert.Equal(t, "gopls-lsp", p["name"])
	assert.Equal(t, "1.0.0", p["version"])
	assert.Equal(t, "./plugins/gopls-lsp", p["source"])
	assert.Equal(t, "development", p["category"])
	assert.NotNil(t, p["lspServers"])
	lsp := p["lspServers"].(map[string]any)
	goEntry := lsp["go"].(map[string]any)
	assert.Equal(t, "gopls", goEntry["command"])

	// installed_plugins.json: has qualified name.
	var installed map[string]any
	require.NoError(t, json.Unmarshal(entries[2].data, &installed))
	pluginsMap := installed["plugins"].(map[string]any)
	assert.Contains(t, pluginsMap, "gopls-lsp@claude-plugins-official")

	// settings.json: enabledPlugins.
	var settings map[string]any
	require.NoError(t, json.Unmarshal(entries[3].data, &settings))
	enabled := settings["enabledPlugins"].(map[string]any)
	assert.Equal(t, true, enabled["gopls-lsp@claude-plugins-official"])
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
		"/tmp/plugins", "/tmp/plugins/marketplaces/claude-plugins-official", "claude-plugins-official", "1.0.0",
	)
	require.NoError(t, err)
	require.Len(t, entries, 4)

	var mkt map[string]any
	require.NoError(t, json.Unmarshal(entries[0].data, &mkt))
	plugins := mkt["plugins"].([]any)
	assert.Len(t, plugins, 2)

	var settings map[string]any
	require.NoError(t, json.Unmarshal(entries[3].data, &settings))
	enabled := settings["enabledPlugins"].(map[string]any)
	assert.Len(t, enabled, 2)
}

func TestBuildPluginConfigs_NoLspJSON(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "simple-plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.json"),
		[]byte(`{"name":"simple-plugin"}`), 0o644))

	entries, err := buildPluginConfigs(
		[]string{pluginDir}, "/tmp/plugins", "/tmp/plugins/marketplaces/claude-plugins-official", "claude-plugins-official", "1.0.0",
	)
	require.NoError(t, err)
	require.Len(t, entries, 4)

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
		[]string{pluginDir}, "/tmp/plugins", "/tmp/plugins/marketplaces/claude-plugins-official", "claude-plugins-official", "1.0.0",
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
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ".lsp.json"),
		[]byte(``), 0o644))

	entries, err := buildPluginConfigs(
		[]string{pluginDir}, "/tmp/plugins", "/tmp/plugins/marketplaces/claude-plugins-official", "claude-plugins-official", "1.0.0",
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
		[]string{pluginDir}, "/tmp/plugins", "/tmp/plugins/marketplaces/claude-plugins-official", "claude-plugins-official", "1.0.0",
	)
	require.NoError(t, err)
	require.Len(t, entries, 4)

	assert.True(t, strings.HasSuffix(entries[0].path, "/marketplace.json"))
	assert.True(t, strings.HasSuffix(entries[1].path, "/known_marketplaces.json"))
	assert.True(t, strings.HasSuffix(entries[2].path, "/installed_plugins.json"))
	assert.True(t, strings.HasSuffix(entries[3].path, "/settings.json"))

	// known_marketplaces.json has source repo.
	var km map[string]any
	require.NoError(t, json.Unmarshal(entries[1].data, &km))
	mktEntry := km["claude-plugins-official"].(map[string]any)
	source := mktEntry["source"].(map[string]any)
	assert.Equal(t, "anthropics/claude-plugins-official", source["repo"])
}

func TestBuildPluginConfigs_EmptyPluginList(t *testing.T) {
	entries, err := buildPluginConfigs(
		nil, "/tmp/plugins", "/tmp/plugins/marketplaces/claude-plugins-official", "claude-plugins-official", "1.0.0",
	)
	require.NoError(t, err)
	require.Len(t, entries, 4)

	// marketplace.json has empty plugins array.
	var mkt map[string]any
	require.NoError(t, json.Unmarshal(entries[0].data, &mkt))
	assert.Nil(t, mkt["plugins"])

	// settings.json has empty enabledPlugins.
	var settings map[string]any
	require.NoError(t, json.Unmarshal(entries[3].data, &settings))
	enabled := settings["enabledPlugins"].(map[string]any)
	assert.Len(t, enabled, 0)
}

func TestBuildScanContextCommand_SourcesEnv(t *testing.T) {
	traceID := "aabbccdd-1122-4334-8556-aabbccddeeff"
	cmd := buildScanContextCommand("/sandbox/workspace/repo", traceID)
	assert.Contains(t, cmd, ". /sandbox/workspace/.env &&")
	assert.Contains(t, cmd, "FULLSEND_TRACE_ID='"+traceID+"'")
	assert.Contains(t, cmd, "-exec fullsend scan context")
}

func TestCopyFile(t *testing.T) {
	t.Run("copies content and preserves permissions", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "source")
		dst := filepath.Join(t.TempDir(), "dest")

		content := []byte("hello world")
		require.NoError(t, os.WriteFile(src, content, 0o755))

		require.NoError(t, copyFile(src, dst))

		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, content, got)

		info, err := os.Stat(dst)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	})

	t.Run("fails on missing source", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "dest")
		err := copyFile("/no/such/file", dst)
		assert.Error(t, err)
	})
}

func TestCollectOpenshellLogs_EmptyRunDir(t *testing.T) {
	// Should be a no-op when runDir is empty — no panic, no error.
	printer := ui.New(io.Discard)
	collectOpenshellLogs("test-sandbox", "", printer)
}

func TestCollectOpenshellLogs_CreatesLogsDir(t *testing.T) {
	// collectOpenshellLogs should create the logs/ directory and attempt
	// log collection. openshell is not available in test, so we expect
	// warnings but no panic.
	tmpDir := t.TempDir()
	runDir := filepath.Join(tmpDir, "run")
	require.NoError(t, os.MkdirAll(runDir, 0o755))

	printer := ui.New(io.Discard)
	collectOpenshellLogs("nonexistent-sandbox", runDir, printer)

	// The logs directory should be created even if collection fails.
	logsDir := filepath.Join(runDir, "logs")
	_, err := os.Stat(logsDir)
	assert.NoError(t, err, "logs directory should exist")
}

func TestRunCommand_HasEnvFileFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("env-file")
	require.NotNil(t, flag)
	assert.Equal(t, "[]", flag.DefValue)

	// Repeatable: set twice and verify both values are captured.
	require.NoError(t, cmd.Flags().Set("env-file", "/tmp/a.env"))
	require.NoError(t, cmd.Flags().Set("env-file", "/tmp/b.env"))

	val, err := cmd.Flags().GetStringArray("env-file")
	require.NoError(t, err)
	assert.Equal(t, []string{"/tmp/a.env", "/tmp/b.env"}, val)
}

func TestApplySandboxImageOverride_Applied(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_IMAGE", "ghcr.io/fullsend-ai/fullsend-sandbox:dev")

	resolved, overridden := applySandboxImageOverride("ghcr.io/fullsend-ai/fullsend-sandbox:latest")
	assert.True(t, overridden)
	assert.Equal(t, "ghcr.io/fullsend-ai/fullsend-sandbox:dev", resolved)
}

func TestApplySandboxImageOverride_NotSet(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_IMAGE", "")

	resolved, overridden := applySandboxImageOverride("ghcr.io/fullsend-ai/fullsend-sandbox:latest")
	assert.False(t, overridden)
	assert.Equal(t, "ghcr.io/fullsend-ai/fullsend-sandbox:latest", resolved)
}

func TestHasAgentsMD_UpperCase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# agents"), 0o644))
	assert.True(t, hasAgentsMD(dir))
}

func TestHasAgentsMD_LowerCase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents.md"), []byte("# agents"), 0o644))
	assert.True(t, hasAgentsMD(dir))
}

func TestHasAgentsMD_TitleCase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Agents.md"), []byte("# agents"), 0o644))
	assert.True(t, hasAgentsMD(dir))
}

func TestHasAgentsMD_Missing(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, hasAgentsMD(dir))
}

func TestHasAgentsMD_OtherFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# claude"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0o644))
	assert.False(t, hasAgentsMD(dir))
}

func TestEnvToList_Sorted(t *testing.T) {
	env := map[string]string{
		"Z_VAR": "z",
		"A_VAR": "a",
		"M_VAR": "m",
	}
	list := envToList(env)
	require.Len(t, list, 3)
	assert.Equal(t, "A_VAR=a", list[0])
	assert.Equal(t, "M_VAR=m", list[1])
	assert.Equal(t, "Z_VAR=z", list[2])
}

func TestShellSafeExpandEnv(t *testing.T) {
	tests := []struct {
		name     string
		template string
		env      map[string]string
		want     string
	}{
		{
			name:     "simple value",
			template: `export FOO="${FOO}"`,
			env:      map[string]string{"FOO": "bar"},
			want:     `export FOO="bar"`,
		},
		{
			name:     "value with double quotes",
			template: `export MSG="${MSG}"`,
			env:      map[string]string{"MSG": `say "hello"`},
			want:     `export MSG="say \"hello\""`,
		},
		{
			name:     "value with parentheses",
			template: `export MSG="${MSG}"`,
			env:      map[string]string{"MSG": "fix (example) thing"},
			want:     `export MSG="fix (example) thing"`,
		},
		{
			name:     "value with single quotes",
			template: `export MSG="${MSG}"`,
			env:      map[string]string{"MSG": "it's broken"},
			want:     `export MSG="it's broken"`,
		},
		{
			name:     "value with dollar sign",
			template: `export V="${V}"`,
			env:      map[string]string{"V": "$HOME"},
			want:     `export V="\$HOME"`,
		},
		{
			name:     "value with backticks",
			template: `export CMD="${CMD}"`,
			env:      map[string]string{"CMD": "use `grep` here"},
			want:     "export CMD=\"use \\`grep\\` here\"",
		},
		{
			name:     "value with backslashes",
			template: `export P="${P}"`,
			env:      map[string]string{"P": `C:\Users\test`},
			want:     `export P="C:\\Users\\test"`,
		},
		{
			name:     "value with all four special chars",
			template: `export V="${V}"`,
			env:      map[string]string{"V": "a]\" $x `y` \\z"},
			want:     `export V="a]\" \$x ` + "\\`y\\`" + ` \\z"`,
		},
		{
			name:     "value with shell metacharacters safe inside double quotes",
			template: `export CMD="${CMD}"`,
			env:      map[string]string{"CMD": "foo || true && bar; baz > /dev/null"},
			want:     `export CMD="foo || true && bar; baz > /dev/null"`,
		},
		{
			name:     "empty value",
			template: `export FOO="${FOO}"`,
			env:      map[string]string{"FOO": ""},
			want:     `export FOO=""`,
		},
		{
			name:     "undefined variable",
			template: `export FOO="${UNDEFINED}"`,
			env:      map[string]string{},
			want:     `export FOO=""`,
		},
		{
			name:     "static lines unchanged",
			template: "export STATIC='hello world'",
			env:      map[string]string{},
			want:     "export STATIC='hello world'",
		},
		{
			name:     "multiple variables",
			template: "export A=\"${A}\"\nexport B=\"${B}\"",
			env:      map[string]string{"A": "1", "B": "two (2)"},
			want:     "export A=\"1\"\nexport B=\"two (2)\"",
		},
		{
			name:     "unquoted template with simple value",
			template: "export FOO=${FOO}",
			env:      map[string]string{"FOO": "bar"},
			want:     "export FOO=bar",
		},
		{
			name:     "braceless $VAR expansion",
			template: `export FOO="$FOO"`,
			env:      map[string]string{"FOO": `say "hello"`},
			want:     `export FOO="say \"hello\""`,
		},
		{
			name:     "real-world HUMAN_INSTRUCTION from issue 615",
			template: `export HUMAN_INSTRUCTION="${HUMAN_INSTRUCTION}"`,
			env:      map[string]string{"HUMAN_INSTRUCTION": `replacing --search "$ISSUE_NUMBER in:body,title" with timeline API || true`},
			want:     `export HUMAN_INSTRUCTION="replacing --search \"\$ISSUE_NUMBER in:body,title\" with timeline API || true"`,
		},
		{
			name:     "real-world instruction with parentheses from failing run",
			template: `export HUMAN_INSTRUCTION="${HUMAN_INSTRUCTION}"`,
			env:      map[string]string{"HUMAN_INSTRUCTION": `An administrator with elevated access to the GCP project (for example, with the ability to set IAM policy) can grant all required roles`},
			want:     `export HUMAN_INSTRUCTION="An administrator with elevated access to the GCP project (for example, with the ability to set IAM policy) can grant all required roles"`,
		},
		{
			name:     "injection attempt: break out of double quotes",
			template: `export V="${V}"`,
			env:      map[string]string{"V": `"; rm -rf /; echo "`},
			want:     `export V="\"; rm -rf /; echo \""`,
		},
		{
			name:     "injection attempt: command substitution",
			template: `export V="${V}"`,
			env:      map[string]string{"V": `$(cat /etc/passwd)`},
			want:     `export V="\$(cat /etc/passwd)"`,
		},
		{
			name:     "injection attempt: backtick substitution",
			template: `export V="${V}"`,
			env:      map[string]string{"V": "`cat /etc/passwd`"},
			want:     "export V=\"\\`cat /etc/passwd\\`\"",
		},
		{
			name:     "newlines in value",
			template: `export V="${V}"`,
			env:      map[string]string{"V": "line1\nline2\nline3"},
			want:     "export V=\"line1\nline2\nline3\"",
		},
		{
			name:     "tabs and special whitespace",
			template: `export V="${V}"`,
			env:      map[string]string{"V": "col1\tcol2"},
			want:     "export V=\"col1\tcol2\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got := shellSafeExpandEnv(tt.template)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestShellSafeExpandEnv_ShellRoundtrip verifies that expanded env files
// produce the original value when sourced by a real shell. This is the
// definitive safety test: if the value survives a roundtrip through
// sh -c '. file && printf "%s" "$VAR"', the escaping is correct.
func TestShellSafeExpandEnv_ShellRoundtrip(t *testing.T) {
	values := []struct {
		name  string
		value string
	}{
		{"simple", "hello world"},
		{"double quotes", `say "hello" to "world"`},
		{"single quotes", "it's a test"},
		{"parentheses", "fix (example) thing"},
		{"pipes and logic", "foo || true && bar"},
		{"dollar sign", "cost is $100 or $HOME"},
		{"command substitution", "$(rm -rf /)"},
		{"backtick substitution", "`rm -rf /`"},
		{"backslashes", `path\to\file`},
		{"semicolons", "cmd1; cmd2; cmd3"},
		{"redirects", "echo foo > /tmp/evil"},
		{"glob chars", "match *.go and file?.txt"},
		{"mixed injection", `"; $(evil) ` + "`more`" + ` && rm -rf / #`},
		{"all four special chars", `quote" dollar$ tick` + "`" + ` slash\`},
		{"newlines", "line1\nline2\nline3"},
		{"tabs", "col1\tcol2"},
		{"empty", ""},
		{"unicode", "こんにちは 🎉"},
		{"real issue 615", `replacing --search "$ISSUE_NUMBER in:body,title" with timeline API || true`},
		{"real failing run", `An administrator with elevated access to the GCP project (for example, with the ability to set IAM policy) can grant all required roles with a single script:`},
		{"already escaped backslash", `already \" escaped`},
		{"nested quotes", `He said "she said 'hello'" today`},
		{"hash comment char", "value # not a comment"},
		{"exclamation mark", "hello! world!"},
		{"curly braces", "use ${VAR} syntax"},
		{"square brackets", "array[0] = value"},
		{"tilde", "~user/path"},
		{"ampersand", "Tom & Jerry"},
	}

	for _, tt := range values {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_VAL", tt.value)
			expanded := shellSafeExpandEnv(`export TEST_VAL="${TEST_VAL}"`)

			// Write expanded content to a temp file and source it in sh.
			envFile := filepath.Join(t.TempDir(), "test.env")
			require.NoError(t, os.WriteFile(envFile, []byte(expanded+"\n"), 0o644))

			// Use printf "%s" (not echo) to avoid interpretation of \n etc.
			cmd := exec.Command("sh", "-c", fmt.Sprintf(`. %s && printf '%%s' "$TEST_VAL"`, envFile))
			out, err := cmd.Output()
			require.NoError(t, err, "shell failed to source expanded env file; expanded content:\n%s", expanded)
			assert.Equal(t, tt.value, string(out), "value did not survive shell roundtrip")
		})
	}
}

func TestNeedsCrossCompilation(t *testing.T) {
	result := needsCrossCompilation()
	if runtime.GOOS == "linux" {
		assert.False(t, result, "should not need cross-compilation on Linux")
	} else {
		assert.True(t, result, "should need cross-compilation on %s", runtime.GOOS)
	}
}

func TestSandboxArch_Default(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_ARCH", "")
	assert.Equal(t, runtime.GOARCH, sandboxArch())
}

func TestSandboxArch_Override(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_ARCH", "amd64")
	assert.Equal(t, "amd64", sandboxArch())
}

func TestSandboxArch_InvalidFallsBack(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_ARCH", "../../etc/passwd")
	assert.Equal(t, runtime.GOARCH, sandboxArch())
}

func TestValidateLinuxBinary_RejectsNonELF(t *testing.T) {
	// A plain text file should be rejected.
	tmp := filepath.Join(t.TempDir(), "not-elf")
	require.NoError(t, os.WriteFile(tmp, []byte("#!/bin/sh\necho hello"), 0o755))
	err := validateLinuxBinary(tmp)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid ELF binary")
}

func TestValidateLinuxBinary_RejectsMissing(t *testing.T) {
	err := validateLinuxBinary("/tmp/nonexistent-fullsend-binary-12345")
	require.Error(t, err)
}

func TestValidateLinuxBinary_AcceptsHostBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host binary is only ELF on Linux")
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	assert.NoError(t, validateLinuxBinary(exe))
}

func TestIsReleasedVersion(t *testing.T) {
	tests := []struct {
		version  string
		expected bool
	}{
		{"0.4.0", true},
		{"v0.4.0", true},
		{"1.0.0", true},
		{"dev", false},
		{"", false},
		{"0.4.0-3-gabcdef", false},
		{"0.4.0-vendored", false},
		{"0.4.0-crosscompiled", false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			assert.Equal(t, tt.expected, isReleasedVersion(tt.version), "version=%q", tt.version)
		})
	}
}

func TestExtractFullsendFromTarGz_PathTraversal(t *testing.T) {
	// Create a tar.gz with a path-traversal entry named "../../../tmp/fullsend".
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("malicious binary content")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "../../../tmp/fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	destPath := filepath.Join(t.TempDir(), "fullsend")
	err = extractFullsendFromTarGz(&buf, destPath)
	assert.Error(t, err, "should reject traversal entry and report binary not found")
	assert.Contains(t, err.Error(), "not found in archive")
}

func TestExtractFullsendFromTarGz_ValidEntry(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("valid binary content")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend_0.4.0_linux_amd64/fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	destPath := filepath.Join(t.TempDir(), "fullsend")
	err = extractFullsendFromTarGz(&buf, destPath)
	require.NoError(t, err)

	data, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, "valid binary content", string(data))
}

func TestCrossCompileFullsend_ProducesBinary(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("cross-compilation test only meaningful on non-Linux hosts")
	}
	if testing.Short() {
		t.Skip("skipping cross-compilation in short mode")
	}

	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "fullsend")
	err := crossCompileFullsend(runtime.GOARCH, binPath)
	require.NoError(t, err)

	info, err := os.Stat(binPath)
	require.NoError(t, err)
	assert.True(t, info.Size() > 0, "binary should be non-empty")
}

func TestResolveLinuxBinary_Download(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping download test in short mode")
	}

	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "fullsend")
	err := downloadReleaseBinary("0.4.0", "amd64", binPath)
	require.NoError(t, err)

	info, err := os.Stat(binPath)
	require.NoError(t, err)
	assert.True(t, info.Size() > 0, "downloaded binary should be non-empty")

	// Verify the downloaded artifact is a valid Linux ELF for the requested arch.
	t.Setenv("FULLSEND_SANDBOX_ARCH", "amd64")
	assert.NoError(t, validateLinuxBinary(binPath), "downloaded binary should be a valid Linux/amd64 ELF")
}

func TestReadOIDCAuthFile_Success(t *testing.T) {
	f := filepath.Join(t.TempDir(), "auth")
	require.NoError(t, os.WriteFile(f, []byte("bearer test-token"), 0o600))
	val, err := readOIDCAuthFile(f)
	require.NoError(t, err)
	assert.Equal(t, "bearer test-token", val)
}

func TestReadOIDCAuthFile_EmptyPath(t *testing.T) {
	_, err := readOIDCAuthFile("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not set")
}

func TestReadOIDCAuthFile_EmptyFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "auth")
	require.NoError(t, os.WriteFile(f, []byte(""), 0o600))
	_, err := readOIDCAuthFile(f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestRefreshOIDCToken_FetchSucceedsSCPFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "bearer test-auth", r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"value":"fresh-oidc-token-content"}`)
	}))
	defer srv.Close()

	err := refreshOIDCToken(context.Background(), "nonexistent-sandbox", srv.URL, "bearer test-auth")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copying token to sandbox")
}

func TestRefreshOIDCToken_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	err := refreshOIDCToken(context.Background(), "nonexistent-sandbox", srv.URL, "bearer test-auth")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

func TestRefreshOIDCToken_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := refreshOIDCToken(context.Background(), "nonexistent-sandbox", srv.URL, "bearer test-auth")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty token")
}

func TestRefreshOIDCToken_NonJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>Service Unavailable</html>")
	}))
	defer srv.Close()

	err := refreshOIDCToken(context.Background(), "nonexistent-sandbox", srv.URL, "bearer test-auth")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-JSON response")
}

func TestRefreshOIDCToken_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"value":"fresh-oidc-token-content"}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := refreshOIDCToken(ctx, "nonexistent-sandbox", srv.URL, "bearer test-auth")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching OIDC token")
}

func TestRunOIDCRefresh_TicksAndStops(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"value":"fresh-oidc-token-content"}`)
	}))
	defer srv.Close()

	origInterval := oidcRefreshInterval
	oidcRefreshInterval = 50 * time.Millisecond
	defer func() { oidcRefreshInterval = origInterval }()

	ctx, cancel := context.WithCancel(context.Background())
	printer := ui.New(io.Discard)

	finished := make(chan struct{})
	go func() {
		runOIDCRefresh(ctx, "nonexistent-sandbox", srv.URL, "bearer test-auth", printer)
		close(finished)
	}()

	require.Eventually(t, func() bool { return calls.Load() >= 2 }, 2*time.Second, 10*time.Millisecond,
		"expected at least 2 refresh calls")

	cancel()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("runOIDCRefresh did not exit after context was cancelled")
	}

	assert.GreaterOrEqual(t, calls.Load(), int32(2))
}

func TestRunHeartbeat_SingleNoticeOnCompletion(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	// Use a very short heartbeat interval so ticks happen quickly.
	origInterval := heartbeatInterval
	heartbeatInterval = 50 * time.Millisecond
	defer func() { heartbeatInterval = origInterval }()

	var buf bytes.Buffer
	printer := ui.New(io.Discard)
	done := make(chan struct{})
	start := time.Now()

	finished := make(chan struct{})
	go func() {
		runHeartbeatTo(&buf, printer, start, 10*time.Minute, done)
		close(finished)
	}()

	// Let it tick several times.
	time.Sleep(200 * time.Millisecond)
	close(done)

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("runHeartbeat did not exit after done was closed")
	}

	output := buf.String()

	// Should contain exactly one ::notice:: line — the completion notice.
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var noticeLines []string
	for _, line := range lines {
		if strings.Contains(line, "::notice::") {
			noticeLines = append(noticeLines, line)
		}
	}
	assert.Len(t, noticeLines, 1, "expected exactly one ::notice:: annotation, got: %v", noticeLines)
	assert.Contains(t, noticeLines[0], "Agent completed (")
}

func TestRunHeartbeat_NoNoticeWhenNotCI(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "false")

	origInterval := heartbeatInterval
	heartbeatInterval = 50 * time.Millisecond
	defer func() { heartbeatInterval = origInterval }()

	var buf bytes.Buffer
	printer := ui.New(io.Discard)
	done := make(chan struct{})
	start := time.Now()

	finished := make(chan struct{})
	go func() {
		runHeartbeatTo(&buf, printer, start, 10*time.Minute, done)
		close(finished)
	}()

	time.Sleep(200 * time.Millisecond)
	close(done)

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("runHeartbeat did not exit after done was closed")
	}

	assert.Empty(t, buf.String(), "should not emit any ::notice:: when not in CI")
}

func TestDownloadChecksumForAsset_ParsesLine(t *testing.T) {
	body := "1b4f0e9851971998e732078544c96b36c3d01cedf7caa332359d6f1d83567014  fullsend_1.0.0_linux_arm64.tar.gz\n" +
		"60303ae22b998861bce3b28f33eec1be758a213c86c93c076dbe9f558c11c752  fullsend_1.0.0_linux_amd64.tar.gz\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	origBaseURL := releaseBaseURL
	releaseBaseURL = srv.URL
	defer func() { releaseBaseURL = origBaseURL }()

	hash, err := downloadChecksumForAsset("1.0.0", "fullsend_1.0.0_linux_amd64.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, "60303ae22b998861bce3b28f33eec1be758a213c86c93c076dbe9f558c11c752", hash)
}

func TestDownloadChecksumForAsset_AssetNotFound(t *testing.T) {
	body := "1b4f0e9851971998e732078544c96b36c3d01cedf7caa332359d6f1d83567014  fullsend_1.0.0_linux_amd64.tar.gz\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	origBaseURL := releaseBaseURL
	releaseBaseURL = srv.URL
	defer func() { releaseBaseURL = origBaseURL }()

	_, err := downloadChecksumForAsset("1.0.0", "fullsend_1.0.0_linux_arm64.tar.gz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in checksums.txt")
}

func TestDownloadChecksumForAsset_InvalidHex(t *testing.T) {
	body := "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ  fullsend_1.0.0_linux_amd64.tar.gz\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	origBaseURL := releaseBaseURL
	releaseBaseURL = srv.URL
	defer func() { releaseBaseURL = origBaseURL }()

	_, err := downloadChecksumForAsset("1.0.0", "fullsend_1.0.0_linux_amd64.tar.gz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid hex hash")
}

func TestDownloadReleaseBinary_ChecksumMismatch(t *testing.T) {
	// Build a valid tar.gz containing a "fullsend" binary.
	var tarBuf bytes.Buffer
	gw := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gw)
	content := []byte("fake binary")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	tarBytes := tarBuf.Bytes()

	// Serve a checksums.txt with a WRONG hash for the asset.
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	checksumBody := fmt.Sprintf("%s  fullsend_1.0.0_linux_amd64.tar.gz\n", wrongHash)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0.0/checksums.txt" {
			fmt.Fprint(w, checksumBody)
		} else if r.URL.Path == "/v1.0.0/fullsend_1.0.0_linux_amd64.tar.gz" {
			w.Write(tarBytes)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origBaseURL := releaseBaseURL
	releaseBaseURL = srv.URL
	defer func() { releaseBaseURL = origBaseURL }()

	destPath := filepath.Join(t.TempDir(), "fullsend")
	err = downloadReleaseBinary("1.0.0", "amd64", destPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
}

func TestDownloadReleaseBinary_ChecksumMatch(t *testing.T) {
	var tarBuf bytes.Buffer
	gw := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gw)
	content := []byte("good binary")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	tarBytes := tarBuf.Bytes()
	h := sha256.Sum256(tarBytes)
	correctHash := hex.EncodeToString(h[:])

	checksumBody := fmt.Sprintf("%s  fullsend_2.0.0_linux_amd64.tar.gz\n", correctHash)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2.0.0/checksums.txt" {
			fmt.Fprint(w, checksumBody)
		} else if r.URL.Path == "/v2.0.0/fullsend_2.0.0_linux_amd64.tar.gz" {
			w.Write(tarBytes)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origBaseURL := releaseBaseURL
	releaseBaseURL = srv.URL
	defer func() { releaseBaseURL = origBaseURL }()

	destPath := filepath.Join(t.TempDir(), "fullsend")
	err = downloadReleaseBinary("2.0.0", "amd64", destPath)
	require.NoError(t, err)

	data, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, "good binary", string(data))
}
