package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// Files and layout OpenCodeRuntime.Bootstrap writes under ConfigDir
// (OPENCODE_CONFIG_DIR).
const (
	// openCodeAgentDir is the subdirectory OpenCode scans for agent
	// definitions ("{agent,agents}/**/*.md", config/agent.ts:13). Bootstrap
	// writes the translated agent there and Run selects it with --agent.
	openCodeAgentDir = "agent"
	// openCodeSkillsDir mirrors the layout OpenCode's skill tool discovers
	// under a config dir.
	openCodeSkillsDir = "skills"
	// openCodePluginsDir is where #515's hook plugin adapter lives; reserved
	// here so the integrity gate and ConfigDir move agree on the path. #510
	// does not install the adapter — it only fixes the location.
	openCodePluginsDir = "plugins"
	// openCodeHooksExtensionFile is the runner-owned, SHA-256-gated hook
	// plugin adapter file #515 installs under openCodePluginsDir. Reserved by
	// #510 (path convention) so #515 lands without reworking ConfigDir.
	openCodeHooksExtensionFile = "fullsend-hooks.ts"
	// openCodeDebugLogFile captures OpenCode's stderr when --debug is set;
	// ExtractDebugLog downloads it.
	openCodeDebugLogFile = "opencode-debug.log"
)

// openCodeAgentPath is the sandbox path of the translated agent definition.
func (r OpenCodeRuntime) openCodeAgentPath(agentName string) string {
	return r.ConfigDir() + "/" + openCodeAgentDir + "/" + agentName + ".md"
}

// openCodeSkillsPath is the sandbox path of the skills directory.
func (r OpenCodeRuntime) openCodeSkillsPath() string {
	return r.ConfigDir() + "/" + openCodeSkillsDir
}

// openCodeHooksExtensionPath is the reserved, runner-owned path of #515's hook
// plugin adapter. Its SHA-256 is verified before .env is sourced (see the
// guard in opencode_run.go), fail-closed, mirroring pi's piHooksGuard.
func (r OpenCodeRuntime) openCodeHooksExtensionPath() string {
	return r.ConfigDir() + "/" + openCodePluginsDir + "/" + openCodeHooksExtensionFile
}

// Bootstrap prepares the runner-owned OpenCode config directory for one agent
// run: the Claude-style agent definition translated into an OpenCode agent
// under agent/<name>.md, harness skills, and the directory scaffold. It
// preflights the pinned opencode binary so a broken image fails here rather
// than as a silent zero-turn run.
//
// It deliberately does NOT install a ClaudeHooksBootstrap-style hook wiring:
// OpenCode has no PreToolUse/PostToolUse hooks of its own, and the plugin
// adapter that bridges security.HookPlan into OpenCode's
// tool.execute.before/after is owned by #515. The type-assert for
// SandboxHooksBootstrap is intentionally omitted here; when #515 lands it
// adds the adapter install and the manifest, keyed off the pinned plugin path
// reserved above.
func (r OpenCodeRuntime) Bootstrap(input BootstrapInput) error {
	if input == nil {
		return fmt.Errorf("bootstrap input is required")
	}
	agentPath := input.AgentPath()
	if agentPath == "" {
		return fmt.Errorf("agent path is required")
	}
	data, err := os.ReadFile(agentPath)
	if err != nil {
		return fmt.Errorf("reading agent definition: %w", err)
	}
	// Reuse the shared Claude-style agent parser (frontmatter + body); the
	// same translation pi uses.
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
		shellQuote(cfg+"/"+openCodeAgentDir),
		shellQuote(r.openCodeSkillsPath()),
		shellQuote(cfg+"/"+openCodePluginsDir))
	if _, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating opencode config dirs: %w", err)
	}

	agentMD, err := openCodeAgentMarkdown(agentName, def)
	if err != nil {
		return err
	}
	if err := uploadBytes(sandboxName, r.openCodeAgentPath(agentName), agentMD); err != nil {
		return fmt.Errorf("writing opencode agent definition: %w", err)
	}

	if err := duplicateDestinationNameError("skill", input.SkillDirs()); err != nil {
		return err
	}
	for _, skillPath := range input.SkillDirs() {
		if skillPath == "" {
			continue
		}
		if err := sandbox.Upload(sandboxName, skillPath, r.openCodeSkillsPath()+"/"); err != nil {
			return fmt.Errorf("copying skill %q: %w", skillPath, err)
		}
		fmt.Fprintf(os.Stderr, "Skill %q: uploaded to sandbox\n", resolveSkillDisplayName(skillPath))
	}

	for _, p := range input.Plugins() {
		if p.Path != "" {
			fmt.Fprintf(os.Stderr, "Plugin %q (%s): skipped — OpenCode does not support harness plugins (see docs/runtimes.md)\n", p.SandboxName(), p.Kind)
		}
	}

	// Hook wiring is #515's responsibility (OpenCode has no native hooks); the
	// plugin adapter path is reserved at openCodeHooksExtensionPath(). Nothing
	// is installed here.

	if err := openCodePreflightVersion(sandboxName); err != nil {
		return err
	}
	return nil
}

// openCodeAgentFrontmatter is the OpenCode agent frontmatter subset Bootstrap
// emits (config/core/v1/config/agent.ts AgentSchema). OpenCode uses its own
// schema, so the Claude-style frontmatter is translated rather than copied:
// the body becomes the prompt, `tools:` becomes OpenCode's
// {toolname: bool} record, and `model:`/`description` map across. mode is
// pinned to "primary" so `--agent` selects it as the top-level agent.
type openCodeAgentFrontmatter struct {
	Description string          `json:"description,omitempty"`
	Mode        string          `json:"mode"`
	Model       string          `json:"model,omitempty"`
	Tools       map[string]bool `json:"tools,omitempty"`
}

// openCodeAgentMarkdown renders the translated agent definition as a markdown
// file with YAML-compatible JSON frontmatter (OpenCode parses frontmatter as
// YAML; JSON is valid YAML) followed by the Claude body as the prompt.
func openCodeAgentMarkdown(agentName string, def *piAgentDef) ([]byte, error) {
	fm := openCodeAgentFrontmatter{
		Description: def.Description,
		Mode:        "primary",
		Model:       def.Model,
	}
	if tools := openCodeToolsRecord(def.Tools); tools != nil {
		fm.Tools = tools
	}
	front, err := json.MarshalIndent(fm, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding opencode agent frontmatter: %w", err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(front)
	b.WriteString("\n---\n\n")
	b.WriteString(def.Body)
	b.WriteString("\n")
	return []byte(b.String()), nil
}

// openCodeToolsRecord translates the Claude tool-name allowlist into
// OpenCode's {toolID: enabled} record. nil claudeTools (no restriction) yields
// nil so the frontmatter omits tools and OpenCode's default set applies. A
// non-nil list enables only the mapped tools; Claude names without an OpenCode
// counterpart are dropped with a warning.
func openCodeToolsRecord(claudeTools []string) map[string]bool {
	if claudeTools == nil {
		return nil
	}
	rec := map[string]bool{}
	for _, ct := range claudeTools {
		if ct == "Skill" {
			// OpenCode has a native skill tool; skills are discovered from the
			// skills dir, not enabled per-agent by this name.
			continue
		}
		ot, ok := openCodeToolForClaude[ct]
		if !ok {
			fmt.Fprintf(os.Stderr, "Agent tool %q has no OpenCode equivalent and is dropped from the allowlist\n", ct)
			continue
		}
		rec[ot] = true
	}
	if len(rec) == 0 {
		// An agent that listed only unsupported/Skill tools gets an explicit
		// empty record rather than nil, so OpenCode does not silently fall
		// back to its full default tool set.
		return map[string]bool{}
	}
	return rec
}

// openCodeToolForClaude maps the Claude Code tool names an agent definition
// may list to OpenCode's tool IDs (packages/opencode/src/tool: bash, read,
// write, edit, grep, glob, webfetch, task, list). OpenCode's tool IDs are
// lowercase, like pi's. The shell tool's exposed ID is "bash"
// (tool/shell/id.ts). Task maps to OpenCode's task (sub-agent) tool. Claude
// tools without an OpenCode counterpart are reported as unsupported.
var openCodeToolForClaude = map[string]string{
	"Bash":      "bash",
	"Read":      "read",
	"Write":     "write",
	"Edit":      "edit",
	"MultiEdit": "edit",
	"Grep":      "grep",
	"Glob":      "glob",
	"LS":        "list",
	"WebFetch":  "webfetch",
	"Task":      "task",
}

// openCodeToolNamesSorted returns the enabled tool IDs in a stable order (for
// deterministic tests and logs).
func openCodeToolNamesSorted(rec map[string]bool) []string {
	out := make([]string, 0, len(rec))
	for k, v := range rec {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// openCodePreflightVersion runs `opencode --version` in the sandbox. Failure
// here means the pinned binary is missing or broken in the image, which is
// reported before any iteration rather than as an empty transcript. Phase 0
// (unbound-force#509) confirmed opencode runs headless in the sandbox.
func openCodePreflightVersion(sandboxName string) error {
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, "opencode --version", 30*time.Second)
	if err != nil {
		return fmt.Errorf("opencode preflight: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("opencode preflight: `opencode --version` exited %d: %s", exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	_ = stdout
	return nil
}
