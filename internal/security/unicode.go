package security

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
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
	// Zero-width and invisible format characters (aligned with unicode_posttool.py).
	reZeroWidth = regexp.MustCompile(
		"[\u00AD\u034F\u061C\u0600-\u0605\u070F\u0890-\u0891\u08E2\u180E\u200B-\u200F\u2028\u2029\u2060-\u2064\u206A-\u206F\uFEFF\uFFF9-\uFFFB]+",
	)

	// Bidirectional override characters.
	reBidi = regexp.MustCompile(
		"[\u202A-\u202E\u2066-\u2069]+",
	)

	// ANSI CSI escape sequences (ECMA-48 compliant).
	reANSI = regexp.MustCompile(`\x1b\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]`)

	// ST-terminated escape sequences: OSC (ESC ]), DCS (ESC P), APC (ESC _), PM (ESC ^).
	reSTTerminated = regexp.MustCompile(`\x1b[\]P_^][^\x1b\x07]*(?:\x1b\\|\x07)`)

	// Null bytes.
	reNull = regexp.MustCompile("\x00+")

	// BMP variation selectors (VS1-VS16).
	reVariation = regexp.MustCompile("[\uFE00-\uFE0F]+")
)

// stripTerminalEscapes removes ANSI CSI and OSC sequences from text.
func stripTerminalEscapes(text string) (string, int, int) {
	ansiCount := 0
	current := reANSI.ReplaceAllStringFunc(text, func(string) string {
		ansiCount++
		return ""
	})
	stCount := 0
	current = reSTTerminated.ReplaceAllStringFunc(current, func(string) string {
		stCount++
		return ""
	})
	return current, ansiCount, stCount
}

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

	// ANSI and ST-terminated escapes (OSC, DCS, APC, PM).
	if stripped, ansiCount, stCount := stripTerminalEscapes(current); ansiCount > 0 || stCount > 0 {
		if ansiCount > 0 {
			findings = append(findings, Finding{
				Scanner:  "unicode_normalizer",
				Name:     "ansi_escape",
				Severity: "medium",
				Detail:   fmt.Sprintf("%d ANSI escape sequences removed", ansiCount),
			})
		}
		if stCount > 0 {
			findings = append(findings, Finding{
				Scanner:  "unicode_normalizer",
				Name:     "osc_escape",
				Severity: "medium",
				Detail:   fmt.Sprintf("%d ST-terminated escape sequences removed", stCount),
			})
		}
		current = stripped
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

	// BMP variation selectors (VS1-VS16)
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

	// Supplementary variation selectors (VS17-VS256, U+E0100-U+E01EF).
	var suppVSStripped strings.Builder
	suppVSCount := 0
	for _, r := range current {
		if r >= 0xE0100 && r <= 0xE01EF {
			suppVSCount++
		} else {
			suppVSStripped.WriteRune(r)
		}
	}
	if suppVSCount > 0 {
		findings = append(findings, Finding{
			Scanner:  "unicode_normalizer",
			Name:     "variation_selector",
			Severity: "medium",
			Detail:   fmt.Sprintf("%d supplementary variation selectors removed", suppVSCount),
		})
		current = suppVSStripped.String()
	}

	// Remaining Cf (format) category characters (e.g. U+061C Arabic Letter Mark).
	var cfStripped strings.Builder
	cfCount := 0
	for _, r := range current {
		if unicode.Is(unicode.Cf, r) {
			cfCount++
		} else {
			cfStripped.WriteRune(r)
		}
	}
	if cfCount > 0 {
		findings = append(findings, Finding{
			Scanner:  "unicode_normalizer",
			Name:     "zero_width",
			Severity: "high",
			Detail:   fmt.Sprintf("%d format (Cf) characters removed", cfCount),
		})
		current = cfStripped.String()
	}

	// NFKC normalization (fullwidth -> ASCII, compatibility decomposition)
	nfkc := norm.NFKC.String(current)
	if nfkc != current {
		// Count differing runes by iterating both strings simultaneously.
		origRunes := []rune(current)
		nfkcRunes := []rune(nfkc)
		diffCount := 0
		minLen := len(origRunes)
		if len(nfkcRunes) < minLen {
			minLen = len(nfkcRunes)
		}
		for i := 0; i < minLen; i++ {
			if origRunes[i] != nfkcRunes[i] {
				diffCount++
			}
		}
		// Account for length difference.
		lenDiff := len(origRunes) - len(nfkcRunes)
		if lenDiff < 0 {
			lenDiff = -lenDiff
		}
		diffCount += lenDiff
		if diffCount == 0 {
			diffCount = 1
		}
		findings = append(findings, Finding{
			Scanner:  "unicode_normalizer",
			Name:     "fullwidth",
			Severity: "high",
			Detail:   fmt.Sprintf("NFKC normalization applied (%d characters affected)", diffCount),
		})
		current = nfkc

		// NFKC can reconstruct escape sequences from fullwidth characters.
		if stripped, ansiCount, stCount := stripTerminalEscapes(current); ansiCount > 0 || stCount > 0 {
			if ansiCount > 0 {
				findings = append(findings, Finding{
					Scanner:  "unicode_normalizer",
					Name:     "ansi_escape",
					Severity: "medium",
					Detail:   fmt.Sprintf("%d ANSI escape sequences removed (post-NFKC)", ansiCount),
				})
			}
			if stCount > 0 {
				findings = append(findings, Finding{
					Scanner:  "unicode_normalizer",
					Name:     "osc_escape",
					Severity: "medium",
					Detail:   fmt.Sprintf("%d ST-terminated escape sequences removed (post-NFKC)", stCount),
				})
			}
			current = stripped
		}
	}

	result.Findings = findings
	if current != text {
		result.Sanitized = current
		result.Safe = false // findings exist
	}

	return result
}
