package security

import (
	"fmt"
	"regexp"
	"strings"
)

// ContextInjectionScanner detects prompt injection patterns in project
// context files (AGENTS.md, .cursorrules, CLAUDE.md, etc.) before they
// are loaded into an agent's system prompt. Adapted from Hermes Agent's
// prompt_builder.py injection scanning.
type ContextInjectionScanner struct {
	patterns []injectionPattern
}

type injectionPattern struct {
	name     string
	category string // "instruction_override", "credential_exfil", "hidden_content", "unicode"
	severity string // "critical", "high", "medium"
	regex    *regexp.Regexp
}

// NewContextInjectionScanner creates a scanner with the default pattern set.
func NewContextInjectionScanner() *ContextInjectionScanner {
	return &ContextInjectionScanner{patterns: defaultPatterns()}
}

func (c *ContextInjectionScanner) Name() string { return "context_injection" }

func (c *ContextInjectionScanner) Scan(text string) ScanResult {
	result := ScanResult{Safe: true}

	for _, p := range c.patterns {
		for _, loc := range p.regex.FindAllStringIndex(text, -1) {
			matched := text[loc[0]:loc[1]]
			if len(matched) > 120 {
				matched = matched[:120] + "..."
			}

			// Compute line number.
			lineNum := strings.Count(text[:loc[0]], "\n") + 1

			result.Findings = append(result.Findings, Finding{
				Scanner:  "context_injection",
				Name:     p.name,
				Severity: p.severity,
				Detail:   fmt.Sprintf("[%s] line %d: %s", p.category, lineNum, matched),
				Position: loc[0],
			})
			result.Safe = false
		}
	}

	return result
}

// ScannableFiles is the set of filenames that should be scanned for
// prompt injection before being loaded into agent context.
var ScannableFiles = map[string]bool{
	"agents.md":               true,
	".cursorrules":            true,
	"claude.md":               true,
	".claude.md":              true,
	"soul.md":                 true,
	".hermes.md":              true,
	"hermes.md":               true,
	"gemini.md":               true,
	".gemini.md":              true,
	"copilot-instructions.md": true,
	"skill.md":                true,
	"plugin.json":             true,
	".lsp.json":               true,
}

// ShouldScan reports whether a filename should be scanned for injection.
func ShouldScan(filename string) bool {
	return ScannableFiles[strings.ToLower(filename)]
}

func defaultPatterns() []injectionPattern {
	compile := func(name, category, severity, pattern string) injectionPattern {
		return injectionPattern{
			name:     name,
			category: category,
			severity: severity,
			regex:    regexp.MustCompile("(?i)" + pattern),
		}
	}

	return []injectionPattern{
		// Instruction override attempts
		compile("ignore_instructions", "instruction_override", "critical",
			`(?:ignore|disregard|forget|override|bypass)\s+(?:all\s+)?(?:previous|above|prior|earlier|your|any|all)\s+(?:instructions?|rules?|guidelines?|prompts?|constraints?|directives?)`),
		compile("system_prompt_override", "instruction_override", "critical",
			`system\s+prompt\s+(?:override|change|update|replace|modify)`),
		compile("act_no_restrictions", "instruction_override", "critical",
			`act\s+as\s+if\s+you\s+have\s+no\s+(?:restrictions?|limits?|rules?|constraints?|guidelines?)`),
		compile("do_not_tell", "instruction_override", "high",
			`do\s+not\s+(?:tell|inform|reveal|mention|disclose)\s+(?:the\s+)?user`),
		compile("new_instructions", "instruction_override", "high",
			`(?:your\s+)?new\s+(?:instructions?|rules?|guidelines?|role|task)\s+(?:are|is|:)`),
		compile("pretend_you_are", "instruction_override", "high",
			`(?:pretend|imagine|suppose|assume)\s+(?:you\s+are|that\s+you)`),

		// Credential exfiltration
		compile("curl_with_creds", "credential_exfil", "critical",
			`curl\s+.*\$(?:GITHUB_TOKEN|GH_TOKEN|API_KEY|SECRET|PASSWORD|AWS_SECRET|OPENAI_API_KEY|ANTHROPIC_API_KEY)`),
		compile("cat_secrets_file", "credential_exfil", "critical",
			`cat\s+(?:~/?\.|/(?:home|root)/[^/]+/\.)(?:env|ssh/id_|aws/credentials|netrc|pgpass|docker/config\.json|kube/config)`),
		compile("env_exfil_printenv", "credential_exfil", "high",
			`(?:printenv|env\s*\||\$\{?(?:!|#))`),
		compile("base64_env_exfil", "credential_exfil", "critical",
			`base64\s+.*\$(?:\w*(?:KEY|TOKEN|SECRET|PASSWORD)\w*)`),

		// Hidden content
		{
			name:     "hidden_html_comment",
			category: "hidden_content",
			severity: "high",
			regex:    regexp.MustCompile(`(?is)<!--\s*(?:.*?)(?:ignore|override|system|secret|hidden|inject|bypass)\s*(?:.*?)-->`),
		},
		compile("hidden_div", "hidden_content", "high",
			`<(?:div|span|p)\s+[^>]*(?:display\s*:\s*none|visibility\s*:\s*hidden)[^>]*>`),

		// Execution-via-translation
		compile("translate_and_execute", "instruction_override", "high",
			`translate\s+.*(?:and|then)\s+(?:execute|run|eval|exec)`),
	}
}
