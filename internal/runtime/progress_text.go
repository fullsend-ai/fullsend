package runtime

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/security"
)

var progressRedactor = security.NewSecretRedactor()
var progressUnicodeNormalizer = security.NewUnicodeNormalizer()

func taskProgressText(s string, limit int) string {
	return redactProgressText(s, limit, nil)
}

// redactProgressText redacts complete raw literals before cleanup, then
// repeats after cleanup: removing ANSI and normalizing whitespace can
// reconstruct an opaque secret. Both passes precede truncation and cache
// retention. The caller's redactor uses display-safe replacement markers,
// such as [REDACTED] or ***.
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
	masked := maskRuntimeSecretLiterals(candidate)
	text := masked
	if result := progressRedactor.Scan(masked); !result.Safe {
		text = result.Sanitized
	}
	if text != candidate {
		return text
	}
	return s
}

// maskRuntimeSecretLiterals masks registered runtime secrets and their
// normalized forms in one longest-first pass. The registry is ordered by
// raw length, but normalization can reverse that order: a secret with a
// longer raw value can normalize shorter than another secret's normalized
// form. Masking in registry order would then replace the shorter literal
// first and destroy the longer overlapping credential's match, leaving its
// suffix visible. Pattern redaction runs after the literal pass for the
// same reason. The original literal passed the registration length floor,
// so every non-empty normalized form is a derivative of an authenticated
// credential and is masked, mirroring redactSecretLiteralsWithNormalizer
// in the CLI.
func maskRuntimeSecretLiterals(s string) string {
	var literals []string
	for _, secret := range security.RuntimeSecrets() {
		literals = append(literals, secret)
		if normalized := NormalizeProgressText(secret); normalized != "" && normalized != secret {
			literals = append(literals, normalized)
		}
	}
	slices.SortStableFunc(literals, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for _, literal := range literals {
		if strings.Contains(s, literal) {
			s = strings.ReplaceAll(s, literal, "***")
		}
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
