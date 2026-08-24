package runtime

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// Files PiRuntime.Bootstrap writes under ConfigDir (PI_CODING_AGENT_DIR).
const (
	// piManifestFile carries everything Run and the hook extension need from
	// Bootstrap: Bootstrap and Run are separate calls on a value receiver, so
	// the sandbox is the only state shared between them.
	piManifestFile = "fullsend-manifest.json"
	// piHooksExtensionFile is the embedded pi extension that runs the
	// sandbox hook scripts; loaded explicitly with -e, never auto-discovered
	// (Run passes --no-extensions).
	piHooksExtensionFile = "fullsend-hooks.js"
	// piAppendSystemFile is pi's hook for appending to its default system
	// prompt (packages/coding-agent README "System Prompt"); the agent body
	// goes here rather than SYSTEM.md so pi's tool guidance is kept.
	piAppendSystemFile = "APPEND_SYSTEM.md"
	piSettingsFile     = "settings.json"
	piDebugLogFile     = "pi-debug.log"
)

//go:embed pi_extension/fullsend-hooks.js
var piHooksExtensionJS []byte

// piManifest is the JSON document at ConfigDir/fullsend-manifest.json.
type piManifest struct {
	AgentName   string `json:"agentName"`
	Description string `json:"description,omitempty"`
	// Model is the agent definition's model; the harness model wins at Run.
	Model string `json:"model,omitempty"`
	// Tools are pi tool names for --tools; nil means pi's default set.
	Tools []string `json:"tools"`
	// BashAllowlist is the Bash(a,b) first-token allowlist from the agent
	// definition. Under Claude Code it is steering, not a security control
	// (ADR 0027: the sandbox is the boundary and tool-level restrictions are
	// bypassable), so by default the extension only logs violations;
	// BashAllowlistMode "enforce" (FULLSEND_PI_BASH_ALLOWLIST=enforce in
	// the runner environment) makes it block.
	BashAllowlist     []string `json:"bashAllowlist"`
	BashAllowlistMode string   `json:"bashAllowlistMode"`
	PiVersion         string   `json:"piVersion,omitempty"`
	// Hooks is nil when the harness has security disabled.
	Hooks *piHooksManifest `json:"hooks"`
}

type piHooksManifest struct {
	Dir    string        `json:"dir"`
	Groups []piHookGroup `json:"groups"`
	// ToolNames maps pi tool names to the Claude names the scripts expect.
	ToolNames map[string]string `json:"toolNames"`
}

type piHookGroup struct {
	Phase   string   `json:"phase"`
	Tools   []string `json:"tools"`
	Scripts []string `json:"scripts"`
}

func (r PiRuntime) piHooksDir() string { return r.ConfigDir() + "/hooks" }

func (r PiRuntime) piManifestPath() string { return r.ConfigDir() + "/" + piManifestFile }

func (r PiRuntime) piSessionsDir() string { return r.ConfigDir() + "/sessions" }

// Bootstrap prepares the runner-owned pi config directory for one agent run:
// agent body as APPEND_SYSTEM.md, locked-down settings.json, skills, the
// hook scripts plus the fullsend hook extension when the harness enables
// security, and the manifest Run reads. It also preflights the pinned pi
// binary so a broken image fails here rather than as a silent zero-turn run.
func (r PiRuntime) Bootstrap(input BootstrapInput) error {
	agentPath := input.AgentPath()
	if agentPath == "" {
		return fmt.Errorf("agent path is required")
	}
	data, err := os.ReadFile(agentPath)
	if err != nil {
		return fmt.Errorf("reading agent definition: %w", err)
	}
	def, err := parsePiAgent(data)
	if err != nil {
		return err
	}
	agentName := input.AgentName()
	if agentName == "" {
		agentName = def.Name
	}
	if agentName == "" {
		agentName = strings.TrimSuffix(agentDestName("", agentPath), ".md")
	}

	sandboxName := input.SandboxName()
	cfg := r.ConfigDir()

	mkdirCmd := fmt.Sprintf("mkdir -p %s %s %s",
		shellQuote(cfg+"/skills"), shellQuote(r.piSessionsDir()), shellQuote(r.piHooksDir()))
	if _, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating pi config dirs: %w", err)
	}

	if err := uploadBytes(sandboxName, cfg+"/"+piAppendSystemFile, piAppendSystem(agentName, def)); err != nil {
		return fmt.Errorf("writing %s: %w", piAppendSystemFile, err)
	}
	settings, err := piSettingsJSON()
	if err != nil {
		return err
	}
	if err := uploadBytes(sandboxName, cfg+"/"+piSettingsFile, settings); err != nil {
		return fmt.Errorf("writing %s: %w", piSettingsFile, err)
	}

	if err := duplicateDestinationNameError("skill", input.SkillDirs()); err != nil {
		return err
	}
	for _, skillPath := range input.SkillDirs() {
		if skillPath == "" {
			continue
		}
		if err := sandbox.Upload(sandboxName, skillPath, cfg+"/skills/"); err != nil {
			return fmt.Errorf("copying skill %q: %w", skillPath, err)
		}
		fmt.Fprintf(os.Stderr, "Skill %q: uploaded to sandbox\n", resolveSkillDisplayName(skillPath))
	}

	for _, p := range input.PluginDirs() {
		if p != "" {
			fmt.Fprintf(os.Stderr, "Plugin %q: skipped — pi does not support Claude plugins (see docs/runtimes.md)\n", p)
		}
	}

	tools, unsupported := piToolsFor(def.Tools)
	for _, u := range unsupported {
		fmt.Fprintf(os.Stderr, "Agent tool %q has no pi equivalent and is dropped from the allowlist\n", u)
	}
	// pi's skills are prompt-driven: the system prompt tells the model to
	// `read` a skill's SKILL.md, and that section is only emitted when the
	// read tool is active (system-prompt.ts). An agent that lists Skill or
	// ships skills therefore needs read, even if its tools: omitted Read.
	if tools != nil && (hasTool(def.Tools, "Skill") || len(input.SkillDirs()) > 0) && !hasTool(tools, "read") {
		tools = append(tools, "read")
	}
	manifest := piManifest{
		AgentName:         agentName,
		Description:       def.Description,
		Model:             def.Model,
		Tools:             tools,
		BashAllowlist:     def.BashAllowlist,
		BashAllowlistMode: piBashAllowlistMode(),
	}

	if hooksInput, ok := input.(SandboxHooksBootstrap); ok {
		hooks := hooksInput.SandboxHookConfig()
		if err := installHookScripts(sandboxName, r.piHooksDir(), hooks); err != nil {
			return err
		}
		if err := appendHookEnv(sandboxName, hooks); err != nil {
			return err
		}
		if err := uploadBytes(sandboxName, cfg+"/"+piHooksExtensionFile, piHooksExtensionJS); err != nil {
			return fmt.Errorf("installing hook extension: %w", err)
		}
		manifest.Hooks = piHooksManifestFor(r.piHooksDir(), hooks)
	}

	version, err := piPreflightVersion(sandboxName)
	if err != nil {
		return err
	}
	manifest.PiVersion = version

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding pi manifest: %w", err)
	}
	if err := uploadBytes(sandboxName, r.piManifestPath(), manifestJSON); err != nil {
		return fmt.Errorf("writing %s: %w", piManifestFile, err)
	}
	return nil
}

// piBashAllowlistEnv selects how the extension treats a Bash(a,b) allowlist
// violation: "warn" (default, Claude Code parity) or "enforce".
const piBashAllowlistEnv = "FULLSEND_PI_BASH_ALLOWLIST"

func piBashAllowlistMode() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(piBashAllowlistEnv)), "enforce") {
		return "enforce"
	}
	return "warn"
}

// piAppendSystem renders the agent definition for APPEND_SYSTEM.md. Claude
// Code shows the agent its own name/description; pi gets the same header so
// prompts that refer to "this agent" still resolve.
func piAppendSystem(agentName string, def *piAgentDef) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Agent: %s\n\n", agentName)
	if def.Description != "" {
		b.WriteString(def.Description)
		b.WriteString("\n\n")
	}
	b.WriteString(def.Body)
	b.WriteString("\n")
	b.WriteString(piNoSubagentNote)
	return []byte(b.String())
}

// piNoSubagentNote makes the absence of a sub-agent tool explicit so skills
// written for Claude Code's Agent tool (pr-review, retro) take their
// single-context path deliberately instead of recording a failed dispatch.
// A fullsend-owned Agent tool for pi is tracked on #6527.
const piNoSubagentNote = "\n## Runtime note\n\n" +
	"This agent runs on the pi runtime (FULLSEND_RUNTIME=pi). No sub-agent tool " +
	"(Agent/Task) is available. When a skill says to dispatch sub-agents, execute each " +
	"sub-agent definition yourself, in the listed order, with the same context package, " +
	"and treat each output as that sub-agent's result.\n"

// piSettingsJSON is the locked-down global settings for the sandbox run.
// defaultProjectTrust "never" means a repo-owned .pi/ (settings, extensions,
// SYSTEM.md) is never loaded in non-interactive modes; skills as slash
// commands are irrelevant headless; retry/compaction stay on so a transient
// provider error or a long session does not end the run
// (parsePiStream models both).
func piSettingsJSON() ([]byte, error) {
	settings := map[string]any{
		"defaultProjectTrust": "never",
		"quietStartup":        true,
		"enableSkillCommands": false,
		"retry":               map[string]any{"enabled": true},
		"compaction":          map[string]any{"enabled": true},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding pi settings: %w", err)
	}
	return data, nil
}

func piHooksManifestFor(hooksDir string, hooks security.SandboxHookConfig) *piHooksManifest {
	m := &piHooksManifest{Dir: hooksDir, Groups: []piHookGroup{}, ToolNames: maps.Clone(claudeToolForPi)}
	// Every plan group is written, including PostToolUseFailure: the adapter
	// only asks for PreToolUse/PostToolUse, and pi's tool_result event already
	// covers failed calls, so that group maps onto nothing there. The adapter
	// also keeps its own spawn timeout rather than reading one from here.
	for _, g := range security.HookPlan(hooks) {
		m.Groups = append(m.Groups, piHookGroup{
			Phase:   string(g.Phase),
			Tools:   append([]string(nil), g.Tools...),
			Scripts: append([]string(nil), g.Scripts...),
		})
	}
	return m
}

// piPreflightVersion runs `pi --version` in the sandbox. Failure here means
// the pinned binary is missing or broken in the image, which is reported
// before any iteration rather than as an empty transcript.
func piPreflightVersion(sandboxName string) (string, error) {
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, "pi --version", 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("pi preflight: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("pi preflight: `pi --version` exited %d: %s", exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	version := strings.TrimSpace(stdout)
	if i := strings.LastIndexByte(version, '\n'); i >= 0 {
		version = strings.TrimSpace(version[i+1:])
	}
	return sanitizeOutput(version), nil
}

// uploadBytes writes data to remotePath in the sandbox through a temp file.
func uploadBytes(sandboxName, remotePath string, data []byte) error {
	tmp, err := os.CreateTemp("", "fullsend-pi-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	tmp.Close()
	return sandbox.Upload(sandboxName, tmp.Name(), remotePath)
}

// piManifestMaxBytes bounds the manifest read back through exec stdout; a
// real manifest is a few KiB.
const piManifestMaxBytes = 1 << 20

// readPiManifest fetches the manifest Bootstrap wrote.
func readPiManifest(sandboxName, manifestPath string) (*piManifest, error) {
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, "cat "+shellQuote(manifestPath), 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("reading pi manifest: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("reading pi manifest: exit %d: %s (was Bootstrap run?)", exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	if len(stdout) > piManifestMaxBytes {
		return nil, fmt.Errorf("reading pi manifest: %d bytes exceeds the %d-byte limit", len(stdout), piManifestMaxBytes)
	}
	var m piManifest
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		return nil, fmt.Errorf("decoding pi manifest: %w", err)
	}
	return &m, nil
}
