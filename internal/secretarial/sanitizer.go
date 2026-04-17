// Package secretarial implements the meeting-notes-to-backlog agent.
package secretarial

import (
	"regexp"
	"strings"
)

const redacted = "[REDACTED]"

// pattern pairs a human-readable name with a compiled regexp.
type pattern struct {
	name string
	re   *regexp.Regexp
}

// scrubPatterns are applied in order. Order matters: more specific patterns
// (e.g. slack_webhook) should come before generic ones (e.g. generic_api_key)
// to avoid partial matches.
var scrubPatterns = []pattern{
	{"email", regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)},
	{"phone_us", regexp.MustCompile(`\b(?:\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}\b`)},
	{"phone_intl", regexp.MustCompile(`\+\d{1,3}[-.\s]?\d{4,14}\b`)},
	{"ipv4", regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\.){3}(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\b`)},
	{"ssn", regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)},
	{"credit_card", regexp.MustCompile(`\b(?:\d[ \-]?){13,19}\b`)},
	{"aws_access_key", regexp.MustCompile(`\b(?:AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b`)},
	{"aws_secret_key", regexp.MustCompile(`(?i)(?:aws.?secret.?(?:access)?.?key)\s*[:=]\s*[A-Za-z0-9/+=]{40}`)},
	{"github_pat", regexp.MustCompile(`\b(?:ghp|gho|ghs|ghr)_[A-Za-z0-9_]{36,255}\b`)},
	{"slack_webhook", regexp.MustCompile(`https://hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/[A-Za-z0-9]+`)},
	{"pem_private_key", regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----.*?-----END (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`)},
	{"gcp_private_key_id", regexp.MustCompile(`(?i)private_key_id\s*[:=]\s*['"]?[a-f0-9]{40}['"]?`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\b`)},
	{"generic_api_key", regexp.MustCompile(`(?i)(?:api[_\-]?key|token|secret|password|bearer)\s*[:=]\s*['"]?[A-Za-z0-9\-_.~+/]{20,}['"]?`)},
}

// suspiciousUnicode matches non-rendering characters that can carry
// steganographic prompt-injection payloads (per the repo's threat model).
var suspiciousUnicode = regexp.MustCompile(
	"[" +
		"\U000E0000-\U000E007F" + // Tag characters
		"\u200B" + // Zero-width space
		"\u200C" + // Zero-width non-joiner
		"\u200D" + // Zero-width joiner
		"\uFEFF" + // BOM / zero-width no-break space
		"\u202A-\u202E" + // Bidi overrides
		"\u2066-\u2069" + // Bidi isolates
		"]+",
)

// ScrubSensitiveContent returns text with PII, secrets, and suspicious
// Unicode stripped or replaced with [REDACTED].
func ScrubSensitiveContent(text string) string {
	text = suspiciousUnicode.ReplaceAllString(text, "")

	for _, p := range scrubPatterns {
		text = p.re.ReplaceAllString(text, redacted)
	}

	return text
}

// ContainsSensitiveContent reports whether text matches any scrub pattern.
func ContainsSensitiveContent(text string) bool {
	return strings.Contains(ScrubSensitiveContent(text), redacted)
}
