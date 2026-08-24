package runtime

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// piAgentDef is the subset of a Claude-style agent definition (markdown with
// YAML frontmatter) that PiRuntime consumes. Claude Code reads the same file
// natively via --agent; pi has no agent concept, so Bootstrap translates it:
// the body becomes APPEND_SYSTEM.md, `tools:` becomes pi's --tools allowlist
// plus the Bash first-token allowlist, and `model:` is the fallback when the
// harness sets none.
type piAgentDef struct {
	Name        string
	Description string
	Model       string
	// Tools are Claude tool names from the frontmatter `tools:` entry, with
	// any `(...)` argument restriction stripped. nil means the entry was
	// absent, i.e. the runtime's default tool set applies.
	Tools []string
	// BashAllowlist holds the prefixes from `Bash(a,b,c)`; nil when Bash is
	// unrestricted or absent.
	BashAllowlist []string
	Body          string
}

type piAgentFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Model       string `yaml:"model"`
	Tools       any    `yaml:"tools"`
}

// parsePiAgent splits a `---` YAML frontmatter block from the markdown body
// and extracts the fields above. A file without frontmatter is all body.
func parsePiAgent(data []byte) (*piAgentDef, error) {
	def := &piAgentDef{}
	content := bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if !bytes.HasPrefix(content, []byte("---")) {
		def.Body = strings.TrimSpace(string(content))
		return def, nil
	}
	// The opener must be a line that is exactly "---" (trailing whitespace
	// and CRLF tolerated); the frontmatter ends at the next such line. Lines
	// that merely start with "---", or an indented "---" inside a YAML block
	// scalar, belong to the YAML or the body. A file whose first line starts
	// with "---" but is not a fence is rejected rather than treated as
	// all-body: that would silently drop the tools: restriction.
	lines := bytes.SplitAfter(content, []byte("\n"))
	isFence := func(line []byte) bool {
		return strings.TrimRight(string(line), " \t\r\n") == "---"
	}
	if !isFence(lines[0]) {
		return nil, fmt.Errorf("agent definition: first line starts with --- but is not a frontmatter fence: %q", strings.TrimRight(string(lines[0]), "\r\n"))
	}
	var front, body []byte
	closed := false
	for i := 1; i < len(lines); i++ {
		if isFence(lines[i]) {
			front = bytes.Join(lines[1:i], nil)
			body = bytes.Join(lines[i+1:], nil)
			closed = true
			break
		}
	}
	if !closed {
		return nil, fmt.Errorf("agent definition: unterminated frontmatter")
	}

	var fm piAgentFrontmatter
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return nil, fmt.Errorf("agent definition: parsing frontmatter: %w", err)
	}
	def.Name = strings.TrimSpace(fm.Name)
	def.Description = strings.TrimSpace(fm.Description)
	def.Model = strings.TrimSpace(fm.Model)
	def.Body = strings.TrimSpace(string(body))

	specs, err := piToolSpecs(fm.Tools)
	if err != nil {
		return nil, err
	}
	if specs != nil {
		def.Tools, def.BashAllowlist = parseClaudeToolSpecs(specs)
	}
	return def, nil
}

// piToolSpecs normalizes the frontmatter `tools:` value — Claude accepts a
// comma-separated string ("Bash(gh,jq),Skill") or a YAML list — into one
// list of specs. nil means the key was absent.
func piToolSpecs(v any) ([]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		return splitTopLevelCommas(t), nil
	case []any:
		var out []string
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("agent definition: tools entries must be strings, got %T", item)
			}
			out = append(out, splitTopLevelCommas(s)...)
		}
		if out == nil {
			out = []string{}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("agent definition: tools must be a string or list, got %T", v)
	}
}

// splitTopLevelCommas splits on commas that are not inside parentheses, so
// "Bash(gh,jq),Skill" yields ["Bash(gh,jq)", "Skill"].
func splitTopLevelCommas(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				if part := strings.TrimSpace(s[start:i]); part != "" {
					out = append(out, part)
				}
				start = i + 1
			}
		}
	}
	if part := strings.TrimSpace(s[start:]); part != "" {
		out = append(out, part)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// parseClaudeToolSpecs turns specs like "Bash(gh,curl,jq)" and "Skill" into
// the tool-name list and the Bash prefix allowlist. An unparenthesized
// "Bash" leaves Bash unrestricted.
func parseClaudeToolSpecs(specs []string) (tools, bashAllowlist []string) {
	tools = []string{}
	seen := map[string]bool{}
	for _, spec := range specs {
		name := spec
		var args string
		if i := strings.IndexByte(spec, '('); i >= 0 && strings.HasSuffix(spec, ")") {
			name = strings.TrimSpace(spec[:i])
			args = spec[i+1 : len(spec)-1]
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		tools = append(tools, name)
		if name == "Bash" && args != "" {
			for _, a := range strings.Split(args, ",") {
				if a = strings.TrimSpace(a); a != "" {
					bashAllowlist = append(bashAllowlist, a)
				}
			}
		}
	}
	return tools, bashAllowlist
}

// piToolForClaude maps the Claude Code tool names an agent definition may
// list to pi's built-in tools (packages/coding-agent/src/core/tools/index.ts:
// read, bash, edit, write, grep, find, ls). Claude tools without a pi
// counterpart are reported as unsupported; Skill maps to no tool (pi's
// skills are prompt-driven), and Bootstrap adds read for it, since pi only
// emits the skills section of the system prompt when read is active.
var piToolForClaude = map[string]string{
	"Bash":      "bash",
	"Read":      "read",
	"Write":     "write",
	"Edit":      "edit",
	"MultiEdit": "edit",
	"Grep":      "grep",
	"Glob":      "find",
	"LS":        "ls",
}

// claudeToolForPi is the inverse map, handed to the hook adapter so the hook
// scripts keep seeing Claude-vocabulary tool names (security.HookGroup.Tools,
// FULLSEND_TOOL_ALLOWLIST) regardless of the runtime (#608).
var claudeToolForPi = map[string]string{
	"bash":  "Bash",
	"read":  "Read",
	"write": "Write",
	"edit":  "Edit",
	"grep":  "Grep",
	"find":  "Glob",
	"ls":    "LS",
}

func hasTool(tools []string, name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
	}
	return false
}

// piToolsFor translates Claude tool names to pi's. The returned tools list
// is nil when claudeTools is nil (no restriction) and non-nil otherwise,
// preserving order and dropping duplicates.
func piToolsFor(claudeTools []string) (tools, unsupported []string) {
	if claudeTools == nil {
		return nil, nil
	}
	tools = []string{}
	seen := map[string]bool{}
	for _, ct := range claudeTools {
		if ct == "Skill" {
			continue
		}
		pt, ok := piToolForClaude[ct]
		if !ok {
			unsupported = append(unsupported, ct)
			continue
		}
		if !seen[pt] {
			seen[pt] = true
			tools = append(tools, pt)
		}
	}
	return tools, unsupported
}
