package security

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// UnicodeNormalizer strips invisible Unicode characters and normalizes
// fullwidth characters to prevent command obfuscation and hidden payload
// injection. Adapted from Hermes Agent's approval.py.
type UnicodeNormalizer struct{}

// NewUnicodeNormalizer creates a UnicodeNormalizer.
func NewUnicodeNormalizer() *UnicodeNormalizer {
	return &UnicodeNormalizer{}
}

func (u *UnicodeNormalizer) Name() string { return "unicode_normalizer" }

var (
	// Zero-width and invisible characters.
	reZeroWidth = regexp.MustCompile(
		"[\u200B\u200C\u200D\uFEFF\u00AD\u2060-\u2064]+",
	)

	// Bidirectional override characters.
	reBidi = regexp.MustCompile(
		"[\u202A-\u202E\u2066-\u2069]+",
	)

	// ANSI escape sequences.
	reANSI = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

	// Null bytes.
	reNull = regexp.MustCompile("\x00+")

	// Variation selectors.
	reVariation = regexp.MustCompile("[\uFE00-\uFE0F]+")
)

func (u *UnicodeNormalizer) Scan(text string) ScanResult {
	result := ScanResult{Safe: true, Sanitized: text}
	current := text
	var findings []Finding

	// Null bytes
	if locs := reNull.FindAllStringIndex(current, -1); len(locs) > 0 {
		count := 0
		for _, loc := range locs {
			count += loc[1] - loc[0]
		}
		findings = append(findings, Finding{
			Scanner:  "unicode_normalizer",
			Name:     "null_byte",
			Severity: "high",
			Detail:   fmt.Sprintf("%d null bytes removed", count),
		})
		current = reNull.ReplaceAllString(current, "")
	}

	// ANSI escapes
	if matches := reANSI.FindAllString(current, -1); len(matches) > 0 {
		findings = append(findings, Finding{
			Scanner:  "unicode_normalizer",
			Name:     "ansi_escape",
			Severity: "medium",
			Detail:   fmt.Sprintf("%d ANSI escape sequences removed", len(matches)),
		})
		current = reANSI.ReplaceAllString(current, "")
	}

	// Zero-width characters
	if locs := reZeroWidth.FindAllStringIndex(current, -1); len(locs) > 0 {
		count := 0
		for _, loc := range locs {
			count += utf8.RuneCountInString(current[loc[0]:loc[1]])
		}
		findings = append(findings, Finding{
			Scanner:  "unicode_normalizer",
			Name:     "zero_width",
			Severity: "high",
			Detail:   fmt.Sprintf("%d zero-width characters removed", count),
		})
		current = reZeroWidth.ReplaceAllString(current, "")
	}

	// Bidirectional overrides
	if locs := reBidi.FindAllStringIndex(current, -1); len(locs) > 0 {
		count := 0
		for _, loc := range locs {
			count += utf8.RuneCountInString(current[loc[0]:loc[1]])
		}
		findings = append(findings, Finding{
			Scanner:  "unicode_normalizer",
			Name:     "bidi_override",
			Severity: "high",
			Detail:   fmt.Sprintf("%d bidirectional override characters removed", count),
		})
		current = reBidi.ReplaceAllString(current, "")
	}

	// Tag characters (U+E0000-U+E007F) — Go regexp doesn't support
	// supplementary plane ranges well, so we iterate runes.
	var tagStripped strings.Builder
	var decoded strings.Builder
	tagCount := 0
	for _, r := range current {
		if r >= 0xE0000 && r <= 0xE007F {
			tagCount++
			decoded.WriteRune(rune(r - 0xE0000))
		} else {
			tagStripped.WriteRune(r)
		}
	}
	if tagCount > 0 {
		detail := fmt.Sprintf("%d tag characters removed", tagCount)
		if d := decoded.String(); strings.TrimSpace(d) != "" {
			detail += fmt.Sprintf(" (decoded hidden text: %s)", d)
		}
		findings = append(findings, Finding{
			Scanner:  "unicode_normalizer",
			Name:     "tag_char",
			Severity: "critical",
			Detail:   detail,
		})
		current = tagStripped.String()
	}

	// Variation selectors
	if locs := reVariation.FindAllStringIndex(current, -1); len(locs) > 0 {
		count := 0
		for _, loc := range locs {
			count += utf8.RuneCountInString(current[loc[0]:loc[1]])
		}
		findings = append(findings, Finding{
			Scanner:  "unicode_normalizer",
			Name:     "variation_selector",
			Severity: "medium",
			Detail:   fmt.Sprintf("%d variation selectors removed", count),
		})
		current = reVariation.ReplaceAllString(current, "")
	}

	// NFKC normalization (fullwidth -> ASCII, compatibility decomposition)
	nfkc := norm.NFKC.String(current)
	if nfkc != current {
		diffCount := 0
		for i, r := range nfkc {
			if i < len(current) {
				origRune, _ := utf8.DecodeRuneInString(current[i:])
				if origRune != r {
					diffCount++
				}
			}
		}
		if diffCount == 0 && len(nfkc) != len(current) {
			diffCount = 1
		}
		findings = append(findings, Finding{
			Scanner:  "unicode_normalizer",
			Name:     "fullwidth",
			Severity: "high",
			Detail:   fmt.Sprintf("NFKC normalization applied (%d characters affected)", diffCount),
		})
		current = nfkc
	}

	result.Findings = findings
	if current != text {
		result.Sanitized = current
		result.Safe = false // findings exist
	}

	return result
}
