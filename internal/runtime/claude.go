package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const claudeDebugLog = "claude-debug.log"

// ClaudeRuntime implements Runtime using the Claude Code CLI.
type ClaudeRuntime struct{}

func (ClaudeRuntime) Name() string { return "claude" }

func (ClaudeRuntime) ConfigDir() string { return sandbox.SandboxClaudeConfig }

func (ClaudeRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

func (r ClaudeRuntime) EnvExports() []string {
	return []string{fmt.Sprintf("export CLAUDE_CONFIG_DIR=%s", r.ConfigDir())}
}

func (r ClaudeRuntime) Bootstrap(input BootstrapInput) error {
	agentPath := input.AgentPath()
	if agentPath == "" {
		return fmt.Errorf("agent path is required")
	}

	sandboxName := input.SandboxName()
	configDir := r.ConfigDir()

	mkdirCmd := fmt.Sprintf("mkdir -p %s/agents %s/skills %s/plugins",
		configDir, configDir, configDir)
	if _, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating runtime config dirs: %w", err)
	}

	if err := sandbox.Upload(sandboxName, agentPath,
		fmt.Sprintf("%s/agents/", configDir)); err != nil {
		return fmt.Errorf("copying agent definition: %w", err)
	}

	for _, skillPath := range input.SkillDirs() {
		if skillPath == "" {
			continue
		}
		if err := sandbox.Upload(sandboxName, skillPath,
			fmt.Sprintf("%s/skills/", configDir)); err != nil {
			return fmt.Errorf("copying skill %q: %w", skillPath, err)
		}
		fmt.Fprintf(os.Stderr, "Skill %q: uploaded to sandbox\n", filepath.Base(skillPath))
	}

	var pluginDirs []string
	for _, p := range input.PluginDirs() {
		if p != "" {
			pluginDirs = append(pluginDirs, p)
		}
	}
	if len(pluginDirs) > 0 {
		if err := bootstrapPlugins(sandboxName, configDir, pluginDirs); err != nil {
			return fmt.Errorf("bootstrapping plugins: %w", err)
		}
	}

	hooksInput, ok := input.(ClaudeHooksBootstrap)
	if !ok {
		return nil
	}
	return installClaudeHooks(sandboxName, hooksInput.ClaudeSandboxHooks())
}

func (ClaudeRuntime) Run(params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	cmd := buildRunCommand(params)
	stdout, execCmd, cancel, err := sandbox.ExecStreamReader(params.SandboxName, cmd, params.Timeout, os.Stderr)
	if err != nil {
		return -1, err
	}
	defer cancel()

	var r io.Reader = stdout
	if params.OutputPath != "" {
		f, ferr := os.Create(params.OutputPath)
		if ferr != nil {
			printer.StepWarn(fmt.Sprintf("Failed to create %s: ", params.OutputPath) + ferr.Error())
		} else {
			defer f.Close()
			r = io.TeeReader(stdout, f)
		}
	}

	if parseErr := progressParser(r, printer, start, metrics); parseErr != nil {
		fmt.Fprintf(os.Stderr, "  progress parser: %v\n", sanitizeOutput(parseErr.Error()))
		cancel()
		io.Copy(io.Discard, r)
	}

	waitErr := execCmd.Wait()
	exitCode := -1
	if execCmd.ProcessState != nil {
		exitCode = execCmd.ProcessState.ExitCode()
	}

	if waitErr != nil && execCmd.ProcessState == nil {
		return exitCode, fmt.Errorf("openshell exec failed: %w", waitErr)
	}

	return exitCode, nil
}

func (r ClaudeRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearCmd := fmt.Sprintf("rm -rf %s/output/* %s/*.jsonl", r.WorkspaceDir(), r.ConfigDir())
	_, _, _, err := sandbox.Exec(sandboxName, clearCmd, 10*time.Second)
	return err
}

func (r ClaudeRuntime) ExtractTranscripts(sandboxName, agentLabel, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return fmt.Errorf("opening output root: %w", err)
	}
	defer root.Close()

	configDir := r.ConfigDir()
	stdout, _, _, err := sandbox.Exec(sandboxName,
		fmt.Sprintf("find %s -name '*.jsonl' 2>/dev/null || true", configDir),
		10*time.Second,
	)
	if err != nil {
		return fmt.Errorf("finding transcripts: %w", err)
	}

	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		fmt.Fprintf(os.Stderr, "  [%s] No transcripts found\n", agentLabel)
		return nil
	}

	for _, remotePath := range strings.Split(trimmed, "\n") {
		remotePath = strings.TrimSpace(remotePath)
		if remotePath == "" {
			continue
		}
		localName := fmt.Sprintf("%s-%s", agentLabel, filepath.Base(remotePath))

		f, createErr := root.Create(localName)
		if createErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Skipping (path rejected): %s: %v\n", agentLabel, localName, createErr)
			continue
		}
		f.Close()

		localPath := filepath.Join(outputDir, localName)
		os.Remove(localPath)
		if dlErr := sandbox.DownloadFile(sandboxName, remotePath, localPath); dlErr != nil {
			fmt.Fprintf(os.Stderr, "  [%s] Failed to copy transcript: %v\n", agentLabel, dlErr)
			continue
		}
		fmt.Fprintf(os.Stderr, "  [%s] Saved transcript: %s\n", agentLabel, localName)
	}

	return nil
}

func (r ClaudeRuntime) ExtractDebugLog(sandboxName, localPath, debug string) error {
	if debug == "" {
		return nil
	}
	remotePath := r.WorkspaceDir() + "/" + claudeDebugLog
	return sandbox.DownloadFile(sandboxName, remotePath, localPath)
}

func (ClaudeRuntime) ParseTranscriptErrors(transcriptDir string) []TranscriptError {
	return parseTranscriptErrors(transcriptDir)
}

func (ClaudeRuntime) EmitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	emitTranscriptErrors(w, summaries)
}

func buildRunCommand(params RunParams) string {
	envFile := sandbox.SandboxWorkspace + "/.env"
	safe := strings.ReplaceAll(params.AgentBaseName, "'", "'\\''")

	parts := []string{
		fmt.Sprintf("cd %s && . %s && claude", params.RepoDir, envFile),
		"--print",
		"--verbose",
		"--output-format stream-json",
	}

	if params.Debug != "" {
		parts = append(parts, fmt.Sprintf("--debug-file '%s/%s'", sandbox.SandboxWorkspace, claudeDebugLog))
		if params.Debug != "*" {
			parts = append(parts, fmt.Sprintf("--debug '%s'", strings.ReplaceAll(params.Debug, "'", "'\\''")))
		}
	}

	if params.Model != "" {
		parts = append(parts, fmt.Sprintf("--model '%s'", strings.ReplaceAll(params.Model, "'", "'\\''")))
	}

	for _, pd := range params.PluginDirs {
		parts = append(parts, fmt.Sprintf("--plugin-dir '%s'", strings.ReplaceAll(pd, "'", "'\\''")))
	}

	parts = append(parts,
		fmt.Sprintf("--agent '%s'", safe),
		"--dangerously-skip-permissions",
		"'Run the agent task'",
	)

	return strings.Join(parts, " ")
}

// Claude Code reads two settings.json files in the sandbox:
//   - {CLAUDE_CONFIG_DIR}/settings.json — plugin marketplace state (bootstrapPlugins)
//   - {SandboxWorkspace}/.claude/settings.json — security Pre/PostToolUse hooks (here)
//
// Keep these paths separate; merging them would mix plugin config with hook wiring.
func installClaudeHooks(sandboxName string, hooks security.ClaudeSandboxHooks) error {
	hooksDir := sandbox.SandboxWorkspace + "/.claude/hooks"
	mkdirCmd := fmt.Sprintf("mkdir -p %s %s/.claude", hooksDir, sandbox.SandboxWorkspace)
	if _, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating Claude hooks dir: %w", err)
	}

	hookFiles := security.HookFiles(hooks)
	for name, content := range hookFiles {
		tmpFile, err := os.CreateTemp("", "fullsend-hook-*")
		if err != nil {
			return fmt.Errorf("creating temp file for hook %s: %w", name, err)
		}
		if _, err := tmpFile.Write(content); err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return fmt.Errorf("writing hook %s: %w", name, err)
		}
		tmpFile.Close()

		remotePath := fmt.Sprintf("%s/.claude/hooks/%s", sandbox.SandboxWorkspace, name)
		if err := sandbox.Upload(sandboxName, tmpFile.Name(), remotePath); err != nil {
			os.Remove(tmpFile.Name())
			return fmt.Errorf("copying hook %s to sandbox: %w", name, err)
		}
		os.Remove(tmpFile.Name())

		chmodCmd := fmt.Sprintf("chmod +x %s", remotePath)
		if _, _, _, err := sandbox.Exec(sandboxName, chmodCmd, 10*time.Second); err != nil {
			return fmt.Errorf("chmod hook %s: %w", name, err)
		}
	}

	settingsJSON, err := security.GenerateClaudeSettings(hooks)
	if err != nil {
		return fmt.Errorf("generating claude settings: %w", err)
	}

	tmpSettings, err := os.CreateTemp("", "fullsend-settings-*.json")
	if err != nil {
		return fmt.Errorf("creating temp settings file: %w", err)
	}
	if _, err := tmpSettings.Write(settingsJSON); err != nil {
		tmpSettings.Close()
		os.Remove(tmpSettings.Name())
		return fmt.Errorf("writing settings: %w", err)
	}
	tmpSettings.Close()

	remoteSettings := fmt.Sprintf("%s/.claude/settings.json", sandbox.SandboxWorkspace)
	if err := sandbox.Upload(sandboxName, tmpSettings.Name(), remoteSettings); err != nil {
		os.Remove(tmpSettings.Name())
		return fmt.Errorf("copying settings.json to sandbox: %w", err)
	}
	os.Remove(tmpSettings.Name())

	if failOn := hooks.TirithFailOn(); failOn != "" {
		escapedFailOn := strings.ReplaceAll(failOn, "'", "'\\''")
		envCmd := fmt.Sprintf("echo 'export TIRITH_FAIL_ON=%s' >> %s/.env",
			escapedFailOn, sandbox.SandboxWorkspace)
		if _, _, _, err := sandbox.Exec(sandboxName, envCmd, 10*time.Second); err != nil {
			return fmt.Errorf("setting TIRITH_FAIL_ON: %w", err)
		}
	}
	if hooks.TirithRequired() {
		envCmd := fmt.Sprintf("echo 'export TIRITH_REQUIRED=1' >> %s/.env", sandbox.SandboxWorkspace)
		if _, _, _, err := sandbox.Exec(sandboxName, envCmd, 10*time.Second); err != nil {
			return fmt.Errorf("setting TIRITH_REQUIRED: %w", err)
		}
	}

	return nil
}

func bootstrapPlugins(sandboxName, configDir string, plugins []string) error {
	const marketplace = "claude-plugins-official"
	const version = "1.0.0"
	pluginsBase := configDir + "/plugins"
	mktBase := pluginsBase + "/marketplaces/" + marketplace

	var mkdirParts, echoParts []string
	mkdirParts = append(mkdirParts, mktBase+"/.claude-plugin")
	for _, p := range plugins {
		name := filepath.Base(p)
		cacheDir := fmt.Sprintf("%s/cache/%s/%s/%s", pluginsBase, marketplace, name, version)
		mkdirParts = append(mkdirParts, mktBase+"/plugins/"+name, cacheDir)
		echoParts = append(echoParts,
			fmt.Sprintf("echo '# %s' > %s/README.md", name, cacheDir),
			fmt.Sprintf("echo '# %s' > %s/plugins/%s/README.md", name, mktBase, name),
		)
	}
	batchCmd := "mkdir -p " + strings.Join(mkdirParts, " ")
	if len(echoParts) > 0 {
		batchCmd += " && " + strings.Join(echoParts, " && ")
	}
	if _, _, _, err := sandbox.Exec(sandboxName, batchCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating marketplace dirs: %w", err)
	}

	for _, pluginPath := range plugins {
		if err := sandbox.Upload(sandboxName, pluginPath,
			fmt.Sprintf("%s/plugins/", configDir)); err != nil {
			return fmt.Errorf("copying plugin %q: %w", pluginPath, err)
		}
	}

	configs, err := buildPluginConfigs(plugins, pluginsBase, mktBase, marketplace, version, configDir)
	if err != nil {
		return fmt.Errorf("building plugin configs: %w", err)
	}
	for _, entry := range configs {
		tmp, err := os.CreateTemp("", "fullsend-plugin-*.json")
		if err != nil {
			return fmt.Errorf("creating temp file for %s: %w", filepath.Base(entry.path), err)
		}
		if _, err := tmp.Write(entry.data); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return fmt.Errorf("writing %s: %w", filepath.Base(entry.path), err)
		}
		tmp.Close()
		uploadErr := sandbox.Upload(sandboxName, tmp.Name(), entry.path)
		os.Remove(tmp.Name())
		if uploadErr != nil {
			return fmt.Errorf("uploading %s: %w", filepath.Base(entry.path), uploadErr)
		}
	}
	return nil
}

type pluginConfigEntry struct {
	path string
	data []byte
}

func buildPluginConfigs(plugins []string, pluginsBase, mktBase, marketplace, version, configDir string) ([]pluginConfigEntry, error) {
	var mktPlugins []any
	installedPlugins := map[string]any{}
	enabledPlugins := map[string]bool{}
	ts := "2026-01-01T00:00:00.000Z"

	for _, pluginPath := range plugins {
		name := filepath.Base(pluginPath)
		qualifiedName := name + "@" + marketplace
		cacheDir := fmt.Sprintf("%s/cache/%s/%s/%s", pluginsBase, marketplace, name, version)

		mp := map[string]any{
			"name": name, "version": version,
			"source": "./plugins/" + name, "category": "development",
		}
		if data, err := os.ReadFile(filepath.Join(pluginPath, ".lsp.json")); err == nil {
			var servers map[string]any
			if json.Unmarshal(data, &servers) == nil {
				mp["lspServers"] = servers
			}
		}
		mktPlugins = append(mktPlugins, mp)
		installedPlugins[qualifiedName] = []map[string]string{{
			"scope": "user", "installPath": cacheDir, "version": version,
			"installedAt": ts, "lastUpdated": ts,
		}}
		enabledPlugins[qualifiedName] = true
	}

	entries := []struct {
		path string
		data any
	}{
		{mktBase + "/.claude-plugin/marketplace.json", map[string]any{
			"$schema": "https://anthropic.com/claude-code/marketplace.schema.json",
			"name":    marketplace,
			"owner":   map[string]string{"name": "Anthropic", "email": "support@anthropic.com"},
			"plugins": mktPlugins,
		}},
		{pluginsBase + "/known_marketplaces.json", map[string]any{
			marketplace: map[string]any{
				"source":          map[string]string{"source": "github", "repo": "anthropics/claude-plugins-official"},
				"installLocation": mktBase, "lastUpdated": ts,
			},
		}},
		{pluginsBase + "/installed_plugins.json", map[string]any{
			"version": 2, "plugins": installedPlugins,
		}},
		{configDir + "/settings.json", map[string]any{
			"enabledPlugins": enabledPlugins,
		}},
	}

	var result []pluginConfigEntry
	for _, entry := range entries {
		data, err := json.Marshal(entry.data)
		if err != nil {
			return nil, fmt.Errorf("marshaling %s: %w", filepath.Base(entry.path), err)
		}
		result = append(result, pluginConfigEntry{path: entry.path, data: data})
	}
	return result, nil
}

// Ensure ClaudeRuntime implements Runtime and TranscriptHandler.
var (
	_ Runtime           = ClaudeRuntime{}
	_ TranscriptHandler = ClaudeRuntime{}
)
