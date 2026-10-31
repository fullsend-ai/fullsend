package runtime

import (
	"strings"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/security"
)

var progressRedactor = security.NewSecretRedactor()
var progressUnicodeNormalizer = security.NewUnicodeNormalizer()

func taskProgressText(s string, limit int) string {
	return redactProgressText(s, limit, nil)
}

// Redact complete raw literals before cleanup, then repeat after cleanup:
// removing ANSI and normalizing whitespace can reconstruct an opaque secret.
// Both passes precede truncation and cache retention. The caller's redactor
// uses display-safe replacement markers, such as [REDACTED] or ***.
func redactProgressText(s string, limit int, redact func(string) string) string {
	// Pattern and registered-secret matching must also see the raw text:
	// Unicode composition can otherwise destroy a recognizable token prefix.
	pass := func(text string) string {
		if redact != nil {
			text = redact(text)
		}
		return scanProgressSecrets(text)
	}
	normalized := NormalizeProgressText(s)
	raw := pass(s)
	cleanedRaw := NormalizeProgressText(raw)
	cleaned := raw
	if normalized != s {
		cleaned = pass(normalized)
	}
	switch {
	case raw != s && cleaned != normalized && cleanedRaw != cleaned:
		// Different passes masked different spans; suppress residual text.
		s = "[REDACTED]"
	case cleaned != normalized:
		s = cleaned
	default:
		s = cleanedRaw
	}
	s = scanProgressSecrets(s)
	if utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit]) + "…"
	}
	// strings.Fields/Join can return a short slice of the sanitizer's large
	// allocation. Cache only storage proportional to the bounded final value.
	return strings.Clone(s)
}

// scanProgressSecrets applies pattern and registered-literal protection.
// Empty fields cannot match either class and need no scanner allocation.
func scanProgressSecrets(s string) string {
	if s == "" {
		return ""
	}
	// Token patterns do not need outer padding, but exact registered values
	// may contain it. Preserve their full-input match before shortening the
	// scanner input; return the original text when no redaction occurred.
	candidate := s
	if trimmed := strings.TrimSpace(s); len(trimmed) != len(s) {
		registered := false
		for _, secret := range security.RuntimeSecrets() {
			if strings.Contains(s, secret) {
				registered = true
				break
			}
		}
		if !registered {
			candidate = trimmed
		}
	}
	if candidate == "" {
		return s
	}
	if result := progressRedactor.Scan(candidate); !result.Safe {
		return result.Sanitized
	}
	return s
}

// NormalizeProgressText applies the same cleanup to display text and caller
// credential literals. Redactors must match both original and normalized
// values, because cleanup can rewrite whitespace, escapes and CI markers.
func NormalizeProgressText(s string) string {
	// Plain ASCII cannot contain Unicode format/compatibility characters.
	// NUL and ESC still need the normalizer's stronger terminal cleanup.
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf || s[i] == 0 || s[i] == 0x1b {
			s = progressUnicodeNormalizer.Scan(s).Sanitized
			break
		}
	}
	return strings.Join(strings.Fields(sanitizeOutput(s)), " ")
}
