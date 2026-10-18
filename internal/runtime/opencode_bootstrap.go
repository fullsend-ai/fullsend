package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
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
	// openCodePluginsDir is reserved for #515's hook plugin adapter, which is
	// not installed yet.
	openCodePluginsDir = "plugins"
	// openCodeHooksExtensionFile is the SHA-256-gated hook plugin adapter
	// #515 will install under openCodePluginsDir.
	openCodeHooksExtensionFile = "fullsend-hooks.ts"
	// openCodeDebugLogFile captures OpenCode's stderr when --debug is set;
	// ExtractDebugLog downloads it.
	openCodeDebugLogFile = "opencode-debug.log"
	// openCodeConfigFile is the runner-owned opencode.json the run prelude
	// writes to ConfigDir (see openCodeInstructionsConfig).
	openCodeConfigFile = "opencode.json"
)

// openCodeAgentPath is the sandbox path of the translated agent definition.
func (r OpenCodeRuntime) openCodeAgentPath(agentName string) string {
	return r.ConfigDir() + "/" + openCodeAgentDir + "/" + agentName + ".md"
}

// openCodeSkillsPath is the sandbox path of the skills directory.
func (r OpenCodeRuntime) openCodeSkillsPath() string {
	return r.ConfigDir() + "/" + openCodeSkillsDir
}

// openCodeHooksExtensionPath is the reserved path of #515's hook plugin
// adapter. Run verifies its SHA-256 before .env is sourced, fail-closed, like
// pi's piHooksGuard.
func (r OpenCodeRuntime) openCodeHooksExtensionPath() string {
	return r.ConfigDir() + "/" + openCodePluginsDir + "/" + openCodeHooksExtensionFile
}

// Bootstrap prepares the runner-owned config dir for one agent run: the
// translated agent at agent/<name>.md, harness skills, and the dir scaffold.
// It preflights the opencode binary so a broken image fails here rather than
// as a silent zero-turn run.
//
// It installs no hooks: OpenCode has no native PreToolUse/PostToolUse hooks,
// and the plugin adapter that bridges security.HookPlan is #515.
func (r OpenCodeRuntime) Bootstrap(input BootstrapInput) error {
	if input == nil {
		return fmt.Errorf("bootstrap input is required")
	}
	sandboxName := input.SandboxName()
	forgetOpenCodeTrustedEnv(sandboxName)
	agentPath := input.AgentPath()
	if agentPath == "" {
		return fmt.Errorf("agent path is required")
	}
	data, err := os.ReadFile(agentPath)
	if err != nil {
		return fmt.Errorf("reading agent definition: %w", err)
	}
	// Same Claude-style agent parser pi uses.
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
	// The written name must match what Run passes to `--agent`; otherwise
	// OpenCode silently falls back to its default agent and full tool set.
	if validated := openCodeValidatedArg(agentName); validated != agentName {
		return fmt.Errorf("agent name %q contains characters openCodeValidatedArg strips (sanitized to %q); the Run command would pass a different name than Bootstrap wrote", agentName, validated)
	}

	cfg := r.ConfigDir()

	mkdirCmd := fmt.Sprintf("mkdir -p %s %s %s",
		shellQuote(cfg+"/"+openCodeAgentDir),
		shellQuote(r.openCodeSkillsPath()),
		shellQuote(cfg+"/"+openCodePluginsDir))
	if _, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating opencode config dirs: %w", err)
	}

	// Read the trusted env first: the agent record is intersected with its
	// permission policy (see intersectPermissionRecord).
	if err := openCodePreflightVersion(sandboxName); err != nil {
		return err
	}
	trustedEnv, err := openCodeReadTrustedEnv(sandboxName)
	if err != nil {
		return err
	}

	trustedRules := parseTrustedPermissionPolicy(trustedEnv.ConfigContent)
	agentMD, err := openCodeAgentMarkdown(agentName, def, trustedRules)
	if err != nil {
		return err
	}
	if err := uploadBytes(sandboxName, r.openCodeAgentPath(agentName), agentMD); err != nil {
		return fmt.Errorf("writing opencode agent definition: %w", err)
	}
	if err := openCodeVerifyAgent(sandboxName, agentName, r); err != nil {
		return err
	}

	if err := uploadHarnessSkills(sandboxName, r.openCodeSkillsPath(), input.SkillDirs()); err != nil {
		return err
	}

	for _, p := range input.Plugins() {
		if p.Path != "" {
			fmt.Fprintf(os.Stderr, "Plugin %q (%s): skipped — OpenCode does not support harness plugins (see docs/runtimes.md)\n", p.SandboxName(), p.Kind)
		}
	}

	if len(def.BashAllowlist) > 0 {
		fmt.Fprintln(os.Stderr, openCodeBashAllowlistWarning(def.BashAllowlist))
	}

	trustedEnv.AgentPath = r.openCodeAgentPath(agentName)
	trustedEnv.AgentDefinition = string(agentMD)
	trustedEnv.AgentModel = def.Model
	recordOpenCodeTrustedEnv(sandboxName, trustedEnv)
	return nil
}

// openCodeAgentFrontmatter is the subset of OpenCode's agent schema
// (config/core/v1/config/agent.ts) Bootstrap emits. Mode is pinned to
// "primary" so `--agent` selects it as the top-level agent. The deprecated
// `tools:` key is not emitted.
type openCodeAgentFrontmatter struct {
	Description string         `json:"description,omitempty"`
	Mode        string         `json:"mode"`
	Model       string         `json:"model,omitempty"`
	Permission  map[string]any `json:"permission"`
}

// openCodeAgentMarkdown renders the translated agent as markdown with JSON
// frontmatter (valid YAML) and the Claude body as the prompt. The permission
// record is capped by the trusted policy (see intersectPermissionRecord).
func openCodeAgentMarkdown(agentName string, def *piAgentDef, trustedRules []openCodePermissionRule) ([]byte, error) {
	rec := openCodePermissionRecord(def.Tools)
	fm := openCodeAgentFrontmatter{
		Description: def.Description,
		Mode:        "primary",
		Model:       def.Model,
		Permission:  intersectPermissionRecord(rec, trustedRules),
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

// openCodeAllToolIDs is the known set of OpenCode permission keys
// (cli/cmd/agent.ts AVAILABLE_PERMISSIONS plus "write"). A missing entry is
// still caught by the "*": "deny" wildcard for restricted agents.
var openCodeAllToolIDs = []string{
	"bash", "edit", "glob", "grep", "lsp", "read", "skill",
	"task", "todowrite", "webfetch", "websearch", "write",
}

// openCodePermissionRecord translates the Claude tool allowlist into OpenCode's
// {toolID: "allow"|"deny"} record: mapped tools are allowed, everything else
// is denied, and unmapped Claude names are dropped with a warning. Per-argument
// Bash restrictions (Bash(gh,jq)) cannot be expressed and collapse to bare
// bash: "allow". A nil list (unrestricted agent) returns an empty record,
// which OpenCode treats as "use defaults".
func openCodePermissionRecord(claudeTools []string) map[string]string {
	if claudeTools == nil {
		return map[string]string{}
	}

	// Deny every known tool, plus a wildcard for unknown ones (MCP, plugin,
	// or newer upstream tools).
	rec := make(map[string]string, len(openCodeAllToolIDs)+1)
	rec["*"] = "deny"
	for _, id := range openCodeAllToolIDs {
		rec[id] = "deny"
	}

	for _, ct := range claudeTools {
		ot, ok := openCodeToolForClaude[ct]
		if !ok {
			fmt.Fprintf(os.Stderr, "Agent tool %q has no OpenCode equivalent and is dropped from the allowlist\n", ct)
			continue
		}
		rec[ot] = "allow"
	}
	return rec
}

// openCodeToolForClaude maps Claude Code tool names to OpenCode permission
// keys. Write maps to "edit" because permission/index.ts groups
// edit/write/apply_patch under "edit".
var openCodeToolForClaude = map[string]string{
	"Bash":      "bash",
	"Read":      "read",
	"Write":     "edit", // OpenCode's write tool checks the "edit" permission (write.ts:55)
	"Edit":      "edit",
	"MultiEdit": "edit",
	"Grep":      "grep",
	"Glob":      "glob",
	"LS":        "read", // OpenCode's read tool handles both files and directories
	"WebFetch":  "webfetch",
	"Skill":     "skill",
	"Task":      "task",
	"Agent":     "task", // Agent is the current name; Task is the legacy alias
}

// openCodeToolNamesSorted returns the allowed tool IDs in a stable order (for
// deterministic tests and logs).
func openCodeToolNamesSorted(rec map[string]string) []string {
	out := make([]string, 0, len(rec))
	for k, v := range rec {
		if v == "allow" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// openCodePermissionRule is one key of the trusted permission object, in
// config order: a tool id or glob ("*", "web*") and its action string or
// pattern map.
type openCodePermissionRule struct {
	Key   string
	Value json.RawMessage
}

// parseTrustedPermissionPolicy returns the trusted OPENCODE_CONFIG_CONTENT
// permission rules in config order. Order matters: OpenCode applies the last
// matching rule. The config is already validated, so a parse error yields nil.
func parseTrustedPermissionPolicy(configContent string) []openCodePermissionRule {
	var config struct {
		Permission json.RawMessage `json:"permission"`
	}
	if err := json.Unmarshal([]byte(configContent), &config); err != nil || len(config.Permission) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(config.Permission))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var rules []openCodePermissionRule
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil
		}
		rules = append(rules, openCodePermissionRule{Key: key, Value: value})
	}
	return rules
}

// openCodePatternRule is one (argument pattern, action) rule for a tool.
type openCodePatternRule struct {
	Pattern string
	Action  string
}

// trustedRulesFor returns the trusted rules OpenCode would evaluate for tool,
// in order: every key whose glob matches tool contributes its pattern map, or
// "*" for a flat action. A later rule with the same pattern replaces the
// earlier one, which is equivalent under last-match-wins.
func trustedRulesFor(rules []openCodePermissionRule, tool string) []openCodePatternRule {
	var out []openCodePatternRule
	add := func(pattern, action string) {
		out = slices.DeleteFunc(out, func(r openCodePatternRule) bool { return r.Pattern == pattern })
		out = append(out, openCodePatternRule{Pattern: pattern, Action: action})
	}
	for _, rule := range rules {
		if !openCodeWildcardMatch(tool, rule.Key) {
			continue
		}
		var action string
		if json.Unmarshal(rule.Value, &action) == nil {
			add("*", action)
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(rule.Value))
		if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
			continue
		}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			pattern, _ := tok.(string)
			if err := dec.Decode(&action); err != nil {
				break
			}
			add(pattern, action)
		}
	}
	return out
}

// openCodePatternMapJSON encodes rules as an ordered OpenCode pattern map.
func openCodePatternMapJSON(rules []openCodePatternRule) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, r := range rules {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(r.Pattern)
		v, _ := json.Marshal(r.Action)
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// openCodeWildcardMatch mirrors OpenCode's Wildcard.match for permission keys:
// "*" matches any run of characters and "?" one character.
func openCodeWildcardMatch(s, pattern string) bool {
	re := "^" + strings.NewReplacer(`\*`, ".*", `\?`, ".").Replace(regexp.QuoteMeta(pattern)) + "$"
	ok, _ := regexp.MatchString(re, s)
	return ok
}

// intersectPermissionRecord makes the trusted policy the ceiling for the agent
// record. OpenCode evaluates agent rules after global ones with findLast
// (permission/index.ts:28-32), so without this the agent frontmatter could
// widen the global gate. An agent "allow" becomes the trusted rules for that
// tool (trustedRulesFor): a single "*" rule as a flat action, anything else
// as an ordered pattern map, so the agent's own "*": "deny" cannot shadow a
// trusted pattern and a flat "allow" cannot widen one. Headless runs
// auto-reject "ask". A tool no trusted rule matches keeps the agent's action,
// as does an unrestricted (empty) record.
func intersectPermissionRecord(rec map[string]string, rules []openCodePermissionRule) map[string]any {
	out := make(map[string]any, len(rec))
	for tool, agentAction := range rec {
		trusted := trustedRulesFor(rules, tool)
		switch {
		case agentAction != "allow" || len(trusted) == 0:
			out[tool] = agentAction
		case len(trusted) == 1 && trusted[0].Pattern == "*":
			out[tool] = trusted[0].Action
		default:
			out[tool] = openCodePatternMapJSON(trusted)
		}
	}
	return out
}

// openCodeInstructionsConfig returns a runner-owned opencode.json whose
// `instructions` re-attach the workspace AGENTS.md that
// OPENCODE_DISABLE_PROJECT_CONFIG suppresses. Absolute paths bypass that flag
// (instruction.ts:140-145), and the array concatenates with
// OPENCODE_CONFIG_CONTENT's (config.ts:45-51).
func openCodeInstructionsConfig(repoDir string) string {
	data, _ := json.Marshal(struct {
		Instructions []string `json:"instructions"`
	}{Instructions: []string{repoDir + "/AGENTS.md"}})
	return string(data)
}

func openCodeBashAllowlistWarning(allowlist []string) string {
	return fmt.Sprintf(
		"Agent Bash allowlist (%s) is recorded but not enforced on opencode — per-argument restrictions collapse to bare bash: \"allow\" until the hook adapter in unbound-force/unbound-force#515 enforces them (see docs/contributing/runtime-implementation.md)",
		strings.Join(allowlist, ", "))
}

// openCodeVerifyAgent runs `opencode debug agent <name>` and checks the agent
// loaded as mode "primary". It catches a silent fallback to the default agent
// (*:allow) from a frontmatter parse failure or mode:subagent.
func openCodeVerifyAgent(sandboxName, agentName string, r OpenCodeRuntime) error {
	cmd := fmt.Sprintf("cd %s && OPENCODE_CONFIG_DIR=%s opencode debug agent %s 2>/dev/null",
		shellQuote(r.WorkspaceDir()),
		shellQuote(r.ConfigDir()),
		shellQuote(openCodeValidatedArg(agentName)))
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, cmd, 30*time.Second)
	if err != nil {
		return fmt.Errorf("verifying opencode agent %q: %w", agentName, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("opencode debug agent %q exited %d: %s", agentName, exitCode, strings.TrimSpace(sanitizeOutput(stderr)))
	}
	out := strings.TrimSpace(sanitizeOutput(stdout))
	if !strings.Contains(out, `"mode":"primary"`) && !strings.Contains(out, `"mode": "primary"`) {
		return fmt.Errorf("opencode agent %q did not load as mode:primary (got: %s); OpenCode may have fallen back to its default agent", agentName, out)
	}
	return nil
}

// openCodePreflightVersion runs `opencode --version` so a missing or broken
// binary is reported before any iteration, not as an empty transcript.
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
