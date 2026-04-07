package security

import (
	"fmt"
	"regexp"
	"strings"
)

// SecretRedactor scans text for API keys, tokens, credentials, and
// sensitive patterns, replacing them with masked versions. Adapted from
// Hermes Agent's agent/redact.py.
type SecretRedactor struct {
	prefixPatterns     []secretPattern
	structuralPatterns []secretPattern
}

type secretPattern struct {
	name  string
	regex *regexp.Regexp
}

// NewSecretRedactor creates a redactor with the default pattern set.
func NewSecretRedactor() *SecretRedactor {
	return &SecretRedactor{
		prefixPatterns:     defaultPrefixPatterns(),
		structuralPatterns: defaultStructuralPatterns(),
	}
}

func (s *SecretRedactor) Name() string { return "secret_redactor" }

func (s *SecretRedactor) Scan(text string) ScanResult {
	result := ScanResult{Safe: true, Sanitized: text}
	current := text

	// Prefix-based patterns (full match is the secret)
	for _, p := range s.prefixPatterns {
		matches := p.regex.FindAllString(current, -1)
		for _, match := range matches {
			masked := mask(match)
			result.Findings = append(result.Findings, Finding{
				Scanner:  "secret_redactor",
				Name:     p.name,
				Severity: "critical",
				Detail:   fmt.Sprintf("Redacted %s (%d chars) -> %s", p.name, len(match), masked),
			})
			current = strings.Replace(current, match, masked, 1)
			result.Safe = false
		}
	}

	// Structural patterns (capture group is the secret)
	for _, p := range s.structuralPatterns {
		for _, loc := range p.regex.FindAllStringSubmatchIndex(current, -1) {
			if p.name == "private_key" {
				result.Findings = append(result.Findings, Finding{
					Scanner:  "secret_redactor",
					Name:     p.name,
					Severity: "critical",
					Detail:   "Private key block detected",
				})
				result.Safe = false
				continue
			}

			// Use last capture group as the secret value.
			// FindAllStringSubmatchIndex returns pairs: [full_start, full_end, group1_start, group1_end, ...]
			nGroups := len(loc)/2 - 1
			if nGroups < 1 {
				continue
			}
			lastGroupIdx := nGroups
			start := loc[lastGroupIdx*2]
			end := loc[lastGroupIdx*2+1]
			if start < 0 || end < 0 {
				continue
			}

			secret := current[start:end]
			masked := mask(secret)
			result.Findings = append(result.Findings, Finding{
				Scanner:  "secret_redactor",
				Name:     p.name,
				Severity: "high",
				Detail:   fmt.Sprintf("Redacted %s (%d chars) -> %s", p.name, len(secret), masked),
			})
			current = current[:start] + masked + current[end:]
			result.Safe = false
		}
	}

	if current != text {
		result.Sanitized = current
	} else {
		result.Sanitized = ""
	}

	return result
}

func mask(value string) string {
	if len(value) < 18 {
		return "***"
	}
	return value[:6] + "..." + value[len(value)-4:]
}

func defaultPrefixPatterns() []secretPattern {
	patterns := []struct {
		name    string
		pattern string
	}{
		// Longer prefixes first to match before shorter ones
		{"openai_proj", `sk-proj-[a-zA-Z0-9_-]{20,}`},
		{"anthropic_key", `sk-ant-[a-zA-Z0-9_-]{20,}`},
		{"openai_key", `sk-[a-zA-Z0-9_-]{20,}`},
		{"github_fine_pat", `github_pat_[a-zA-Z0-9_]{22,}`},
		{"github_pat", `ghp_[a-zA-Z0-9]{36,}`},
		{"github_oauth", `gho_[a-zA-Z0-9]{36,}`},
		{"github_user_token", `ghu_[a-zA-Z0-9]{36,}`},
		{"github_server_token", `ghs_[a-zA-Z0-9]{36,}`},
		{"github_refresh_token", `ghr_[a-zA-Z0-9]{36,}`},
		{"slack_token", `xox[baprs]-[a-zA-Z0-9-]{10,}`},
		{"google_api_key", `AIza[a-zA-Z0-9_-]{35}`},
		{"aws_access_key", `AKIA[A-Z0-9]{16}`},
		{"stripe_live", `sk_live_[a-zA-Z0-9]{24,}`},
		{"stripe_test", `sk_test_[a-zA-Z0-9]{24,}`},
		{"sendgrid_key", `SG\.[a-zA-Z0-9_-]{22,}\.[a-zA-Z0-9_-]{20,}`},
		{"hf_token", `hf_[a-zA-Z0-9]{34,}`},
		{"npm_token", `npm_[a-zA-Z0-9]{36,}`},
		{"pypi_token", `pypi-[a-zA-Z0-9_-]{50,}`},
		{"gitlab_pat", `glpat-[a-zA-Z0-9_-]{20,}`},
		{"vault_token", `hvs\.[a-zA-Z0-9_-]{24,}`},
		{"age_secret_key", `AGE-SECRET-KEY-[A-Z0-9]{59}`},
	}

	result := make([]secretPattern, len(patterns))
	for i, p := range patterns {
		result[i] = secretPattern{name: p.name, regex: regexp.MustCompile(p.pattern)}
	}
	return result
}

func defaultStructuralPatterns() []secretPattern {
	patterns := []struct {
		name    string
		pattern string
	}{
		{"env_assignment", `(?:^|\s)(?:export\s+)?([A-Z_]*(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|PASSWD|AUTH|API_KEY)[A-Z_]*)\s*=\s*['"]?([^\s'"]{8,})['"]?`},
		{"json_field", `"(?:api[_-]?key|token|secret|password|credential|auth[_-]?token|access[_-]?key|private[_-]?key)"\s*:\s*"([^"]{8,})"`},
		{"auth_header", `(?i)(?:Authorization|X-Api-Key|X-Auth-Token)\s*:\s*(?:Bearer\s+)?(\S{8,})`},
		{"private_key", `-----BEGIN\s+(?:RSA\s+|EC\s+|OPENSSH\s+)?PRIVATE KEY-----`},
		{"db_connection_password", `(?:postgres|mysql|mongodb|redis)://[^:]+:([^@]{4,})@`},
	}

	result := make([]secretPattern, len(patterns))
	for i, p := range patterns {
		result[i] = secretPattern{name: p.name, regex: regexp.MustCompile("(?m)" + p.pattern)}
	}
	return result
}
