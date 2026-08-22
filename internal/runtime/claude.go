package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/skill"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const claudeDebugLog = "claude-debug.log"

// ClaudeRuntime implements Runtime using the Claude Code CLI.
type ClaudeRuntime struct{}

func (ClaudeRuntime) Name() string { return "claude" }

// System returns the OTEL GenAI `gen_ai.system` vendor for Claude Code's models.
func (ClaudeRuntime) System() string { return "anthropic" }

func (ClaudeRuntime) ConfigDir() string { return sandbox.SandboxClaudeConfig }

func (ClaudeRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

// DebugLogName implements DebugLogNamer: the local artifact for --debug output.
func (ClaudeRuntime) DebugLogName() string { return claudeDebugLog }

// NeedsClaudeMDBridge implements ContextBridger. Claude Code auto-loads
// CLAUDE.md but not AGENTS.md, so AGENTS.md-only repos need the pointer file.
func (ClaudeRuntime) NeedsClaudeMDBridge() bool { return true }

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

	agentDest := agentDestName(input.AgentName(), agentPath)

	if err := sandbox.UploadFile(sandboxName, agentPath,
		fmt.Sprintf("%s/agents/%s", configDir, agentDest)); err != nil {
		return fmt.Errorf("copying agent definition: %w", err)
	}

	if err := duplicateDestinationNameError("skill", input.SkillDirs()); err != nil {
		return err
	}
	for _, skillPath := range input.SkillDirs() {
		if skillPath == "" {
			continue
		}
		if err := sandbox.Upload(sandboxName, skillPath,
			fmt.Sprintf("%s/skills/", configDir)); err != nil {
			return fmt.Errorf("copying skill %q: %w", skillPath, err)
		}
		fmt.Fprintf(os.Stderr, "Skill %q: uploaded to sandbox\n", resolveSkillDisplayName(skillPath))
	}

	var pluginDirs []string
	for _, p := range input.PluginDirs() {
		if p != "" {
			pluginDirs = append(pluginDirs, p)
		}
	}
	if len(pluginDirs) > 0 {
		if err := duplicateDestinationNameError("plugin", pluginDirs, reservedPluginDestNames...); err != nil {
			return err
		}
		if err := bootstrapPlugins(sandboxName, configDir, pluginDirs); err != nil {
			return fmt.Errorf("bootstrapping plugins: %w", err)
		}
	}

	hooksInput, ok := input.(SandboxHooksBootstrap)
	if !ok {
		return nil
	}
	return installClaudeHooks(sandboxName, hooksInput.SandboxHookConfig())
}

func (ClaudeRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	cmd := buildRunCommand(params)
	stdout, execCmd, cancel, err := sandbox.ExecStreamReader(ctx, params.SandboxName, cmd, params.Timeout, os.Stderr)
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

	handler := params.OnEvent
	if handler == nil {
		renderer := NewEventRenderer(printer)
		handler = renderer.Handle
	}
	// Always wrap handler to capture metrics regardless of custom/default path.
	innerHandler := handler
	handler = func(evt AgentEvent) {
		switch e := evt.(type) {
		case InitEvent:
			if metrics.Model == "" {
				metrics.Model = e.Model
			}
		case ResultEvent:
			metrics.NumTurns = e.NumTurns
			metrics.TotalCostUSD = e.TotalCostUSD
			metrics.InputTokens = e.InputTokens
			metrics.OutputTokens = e.OutputTokens
			metrics.ReasoningTokens = e.ReasoningTokens
			metrics.CacheCreationInputTokens = e.CacheCreationInputTokens
			metrics.CacheReadInputTokens = e.CacheReadInputTokens
		case ToolUseEvent:
			metrics.ToolCalls.Add(1)
		}
		innerHandler(evt)
	}

	if parseErr := parseClaudeStream(r, handler); parseErr != nil {
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

func (ClaudeRuntime) ParseTranscriptFile(path string) (TranscriptError, bool) {
	return parseTranscriptFile(path)
}

func (ClaudeRuntime) EmitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	emitTranscriptErrors(w, summaries)
}

// reservedPluginDestNames are basenames bootstrapPlugins and
// buildPluginConfigs create directly under configDir/plugins/ — two
// marketplace-scaffolding directories plus the two shared registration
// files every plugin in a Bootstrap call is recorded into. A plugin whose
// directory shares one of these names would resolve to the same sandbox
// destination: for the directories, UploadDir's rm -rf-before-extract wipes
// the scaffolding for every plugin in the batch; for the JSON files, the
// plugin's own directory upload replaces the file's destination with a
// directory before buildPluginConfigs tries to write the file there.
var reservedPluginDestNames = []string{"marketplaces", "cache", "known_marketplaces.json", "installed_plugins.json"}

// duplicateDestinationNameError returns an error if two distinct paths in
// paths share the same basename, or if a basename collides with one of
// reserved. sandbox.UploadDir replaces its destination wholesale rather than
// merging into it, so two different skills or plugins resolving to the same
// sandbox directory name would otherwise upload one, then silently discard
// it when the second overwrites it — the same "expected content silently
// isn't there" failure mode as #5247, just reached through a naming
// collision instead of a broken symlink.
func duplicateDestinationNameError(kind string, paths []string, reserved ...string) error {
	reservedSet := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		reservedSet[r] = true
	}
	seen := make(map[string]string, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		base := filepath.Base(p)
		if reservedSet[base] {
			return fmt.Errorf("%s path %q resolves to %q, which is reserved for internal use", kind, p, base)
		}
		if prior, ok := seen[base]; ok && prior != p {
			return fmt.Errorf("two %s paths both resolve to the sandbox name %q: %q and %q", kind, base, prior, p)
		}
		seen[base] = p
	}
	return nil
}

// resolveSkillDisplayName returns a human-friendly name for a skill directory.
// It reads the SKILL.md frontmatter name if available, falling back to
// filepath.Base for local skills where the directory name is already correct.
func resolveSkillDisplayName(skillPath string) string {
	base := filepath.Base(skillPath)
	data, err := os.ReadFile(filepath.Join(skillPath, "SKILL.md"))
	if err != nil {
		return base
	}
	meta, err := skill.ParseFrontmatter(data)
	if err != nil || meta == nil || meta.Name == "" {
		return base
	}
	return meta.Name
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

	if params.HooksSettingsPath != "" {
		parts = append(parts, fmt.Sprintf("--settings '%s'", strings.ReplaceAll(params.HooksSettingsPath, "'", "'\\''")))
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

	if params.Effort != "" {
		parts = append(parts, fmt.Sprintf("--effort '%s'", strings.ReplaceAll(params.Effort, "'", "'\\''")))
	}

	for _, pd := range params.PluginDirs {
		parts = append(parts, fmt.Sprintf("--plugin-dir '%s'", strings.ReplaceAll(pd, "'", "'\\''")))
	}

	prompt := DefaultAgentPrompt
	if params.Prompt != "" {
		prompt = params.Prompt
	}
	parts = append(parts,
		fmt.Sprintf("--agent '%s'", safe),
		"--dangerously-skip-permissions",
		fmt.Sprintf("'%s'", strings.ReplaceAll(prompt, "'", "'\\''")),
	)

	return strings.Join(parts, " ")
}

// Claude Code reads settings from two separate files in the sandbox:
//   - {CLAUDE_CONFIG_DIR}/settings.json — plugin marketplace state (bootstrapPlugins)
//   - {CLAUDE_CONFIG_DIR}/hooks.json    — security Pre/PostToolUse hooks (here)
//
// The hooks file is loaded via --settings in buildRunCommand, which takes
// precedence over project/local settings. Hook scripts and wiring are
// co-located under the runner-owned config directory, outside the
// agent-writable workspace tree (#6358).
func installClaudeHooks(sandboxName string, hooks security.SandboxHookConfig) error {
	// security.SandboxHooksDir is the directory the generated hooks.json
	// commands point at; installHookScripts creates it.
	if err := installHookScripts(sandboxName, security.SandboxHooksDir, hooks); err != nil {
		return err
	}

	hooksJSON, err := security.GenerateHooksConfig(hooks)
	if err != nil {
		return fmt.Errorf("generating hooks config: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "fullsend-hooks-")
	if err != nil {
		return fmt.Errorf("creating temp hooks file: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	tmpPath := filepath.Join(tmpDir, "hooks.json")
	if err := os.WriteFile(tmpPath, hooksJSON, 0o600); err != nil {
		return fmt.Errorf("writing hooks config: %w", err)
	}

	if err := sandbox.Upload(sandboxName, tmpPath, security.SandboxHooksSettings); err != nil {
		return fmt.Errorf("copying hooks.json to sandbox: %w", err)
	}

	return appendHookEnv(sandboxName, hooks)
}

func bootstrapPlugins(sandboxName, configDir string, plugins []string) error {
	const marketplace = "claude-plugins-official"
	const version = "1.0.0"
	pluginsBase := configDir + "/plugins"
	mktBase := pluginsBase + "/marketplaces/" + marketplace

	var mkdirParts, echoParts []string
	mkdirParts = append(mkdirParts, shellQuote(mktBase+"/.claude-plugin"))
	for _, p := range plugins {
		name := filepath.Base(p)
		pluginDir := mktBase + "/plugins/" + name
		cacheDir := fmt.Sprintf("%s/cache/%s/%s/%s", pluginsBase, marketplace, name, version)
		mkdirParts = append(mkdirParts, shellQuote(pluginDir), shellQuote(cacheDir))
		echoParts = append(echoParts,
			fmt.Sprintf("echo %s > %s", shellQuote("# "+name), shellQuote(cacheDir+"/README.md")),
			fmt.Sprintf("echo %s > %s", shellQuote("# "+name), shellQuote(pluginDir+"/README.md")),
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

// agentDestName returns the sandbox filename for the agent definition.
// When agentName is non-empty it produces {name}.md; otherwise it falls
// back to the source file's basename.
func agentDestName(agentName, agentPath string) string {
	if agentName != "" {
		return strings.TrimSuffix(agentName, ".md") + ".md"
	}
	return filepath.Base(agentPath)
}

// Ensure ClaudeRuntime implements Runtime and TranscriptHandler.
var (
	_ Runtime           = ClaudeRuntime{}
	_ TranscriptHandler = ClaudeRuntime{}
	_ DebugLogNamer     = ClaudeRuntime{}
	_ ContextBridger    = ClaudeRuntime{}
)
