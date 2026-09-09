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
//
// Control-character stripping runs to a fixpoint before and after NFKC so
// a single non-overlapping substitution cannot leave a newly adjacent
// CSI/OSC sequence (or any other recognized control/invisible payload)
// in the emitted text. See #445.
type UnicodeNormalizer struct{}

// NewUnicodeNormalizer creates a UnicodeNormalizer.
func NewUnicodeNormalizer() *UnicodeNormalizer {
	return &UnicodeNormalizer{}
}

func (u *UnicodeNormalizer) Name() string { return "unicode_normalizer" }

const (
	// maxSanitizePasses bounds the strip fixpoint. Each pass strictly
	// shortens the string when it matches, so this is defense in depth
	// against a pathological input of adjacent reconstructed sequences.
	maxSanitizePasses = 64
	// maxDecodedLog bounds tag-character payloads recorded in findings.
	// Decoded hidden text never enters Sanitized output.
	maxDecodedLog = 200
)

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

	// reC1 matches bare C1 control bytes (U+0080-U+009F) on their own,
	// independent of a leading ESC. reANSI and reSTTerminated only
	// recognize the 7-bit ESC-prefixed forms of CSI/OSC/DCS/ST/PM/APC; the
	// 8-bit C1 introducers (U+009B CSI, U+009D OSC, U+0090 DCS, U+009C ST,
	// U+009E PM, U+009F APC) are complete, live introducers on their own
	// and would otherwise stabilize unnoticed on the very first pass,
	// bypassing stripUntilStable's fail-closed backstop entirely (it only
	// runs when the fixpoint loop fails to stabilize). Stripping every C1
	// byte unconditionally on every pass removes the introducer regardless
	// of what follows it, the same guarantee reESCOrC1 already relies on
	// for the backstop case.
	reC1 = regexp.MustCompile("[\u0080-\u009F]+")

	// reESCOrC1 is the fail-closed backstop for stripUntilStable. A single
	// non-overlapping ReplaceAllStringFunc pass on an adjacent-ESC run
	// (e.g. ESC*65 + "["*130, where "[" is itself a valid ECMA-48 CSI
	// final byte) removes only one reconstructed CSI per pass, so a large
	// enough run outruns maxSanitizePasses. reANSI and reSTTerminated both
	// require a leading ESC (0x1B); stripping every ESC and C1 control
	// byte (0x80-0x9F) unconditionally therefore guarantees neither can
	// remain, regardless of how the surrounding bytes are shaped.
	reESCOrC1 = regexp.MustCompile("[\x1b\u0080-\u009F]+")
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

func countRunesInMatches(text string, locs [][]int) int {
	count := 0
	for _, loc := range locs {
		count += utf8.RuneCountInString(text[loc[0]:loc[1]])
	}
	return count
}

func appendFinding(findings []Finding, name, severity, detail, suffix string) []Finding {
	return append(findings, Finding{
		Scanner:  "unicode_normalizer",
		Name:     name,
		Severity: severity,
		Detail:   detail + suffix,
	})
}

// stripControlCharacters removes one pass of every control/invisible
// category (null, ANSI/OSC, zero-width, bidi, tag, variation selectors,
// remaining Cf). It does not apply NFKC.
func stripControlCharacters(text, detailSuffix string) (string, []Finding) {
	current := text
	var findings []Finding

	if locs := reNull.FindAllStringIndex(current, -1); len(locs) > 0 {
		count := 0
		for _, loc := range locs {
			count += loc[1] - loc[0]
		}
		findings = appendFinding(findings, "null_byte", "high",
			fmt.Sprintf("%d null bytes removed", count), detailSuffix)
		current = reNull.ReplaceAllString(current, "")
	}

	if stripped, ansiCount, stCount := stripTerminalEscapes(current); ansiCount > 0 || stCount > 0 {
		if ansiCount > 0 {
			findings = appendFinding(findings, "ansi_escape", "medium",
				fmt.Sprintf("%d ANSI escape sequences removed", ansiCount), detailSuffix)
		}
		if stCount > 0 {
			findings = appendFinding(findings, "osc_escape", "medium",
				fmt.Sprintf("%d ST-terminated escape sequences removed", stCount), detailSuffix)
		}
		current = stripped
	}

	// Bare C1 control bytes (U+0080-U+009F), independent of a leading ESC.
	// reANSI/reSTTerminated only recognize 7-bit ESC-prefixed introducers;
	// an 8-bit C1 introducer (e.g. U+009B CSI) forms a complete sequence on
	// its own and would otherwise never change across a pass, stabilizing
	// on pass 1 and bypassing the fail-closed backstop entirely (#445).
	if locs := reC1.FindAllStringIndex(current, -1); len(locs) > 0 {
		count := countRunesInMatches(current, locs)
		findings = appendFinding(findings, "ansi_escape", "high",
			fmt.Sprintf("%d C1 control byte(s) removed", count), detailSuffix)
		current = reC1.ReplaceAllString(current, "")
	}

	if locs := reZeroWidth.FindAllStringIndex(current, -1); len(locs) > 0 {
		count := countRunesInMatches(current, locs)
		findings = appendFinding(findings, "zero_width", "high",
			fmt.Sprintf("%d zero-width characters removed", count), detailSuffix)
		current = reZeroWidth.ReplaceAllString(current, "")
	}

	if locs := reBidi.FindAllStringIndex(current, -1); len(locs) > 0 {
		count := countRunesInMatches(current, locs)
		findings = appendFinding(findings, "bidi_override", "high",
			fmt.Sprintf("%d bidirectional override characters removed", count), detailSuffix)
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
			if len(d) > maxDecodedLog {
				d = d[:maxDecodedLog] + "..."
			}
			detail += fmt.Sprintf(" (decoded hidden text: %s)", d)
		}
		findings = appendFinding(findings, "tag_char", "critical", detail, detailSuffix)
		current = tagStripped.String()
	}

	if locs := reVariation.FindAllStringIndex(current, -1); len(locs) > 0 {
		count := countRunesInMatches(current, locs)
		findings = appendFinding(findings, "variation_selector", "medium",
			fmt.Sprintf("%d variation selectors removed", count), detailSuffix)
		current = reVariation.ReplaceAllString(current, "")
	}

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
		findings = appendFinding(findings, "variation_selector", "medium",
			fmt.Sprintf("%d supplementary variation selectors removed", suppVSCount), detailSuffix)
		current = suppVSStripped.String()
	}

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
		findings = appendFinding(findings, "zero_width", "high",
			fmt.Sprintf("%d format (Cf) characters removed", cfCount), detailSuffix)
		current = cfStripped.String()
	}

	return current, findings
}

// stripUntilStable repeats stripControlCharacters until the text stops
// changing or maxSanitizePasses is reached. A single regexp substitution
// is non-overlapping, so ESC ESC [[[[ becomes ESC [[ after one pass —
// itself a CSI sequence that a second pass must remove.
func stripUntilStable(text, detailSuffix string) (string, []Finding) {
	current := text
	var findings []Finding
	stabilized := false
	for range maxSanitizePasses {
		next, extra := stripControlCharacters(current, detailSuffix)
		findings = append(findings, extra...)
		if next == current {
			stabilized = true
			break
		}
		current = next
	}

	if !stabilized && reESCOrC1.MatchString(current) {
		// Exhausted the pass budget without reaching a fixpoint. A
		// pathological run of adjacent ESC bytes (optionally reconstructed
		// from fullwidth brackets by NFKC) can make each pass remove only
		// one CSI, outrunning any fixed cap (#445). Returning the residual
		// text here would fail open — it can still contain a live CSI/OSC
		// sequence copied into Sanitized. Fail closed instead: strip every
		// remaining ESC (0x1B) and C1 control byte (0x80-0x9F) outright.
		// reANSI and reSTTerminated both require a leading ESC byte, so
		// this guarantees neither survives.
		before := current
		current = reESCOrC1.ReplaceAllString(current, "")
		removed := utf8.RuneCountInString(before) - utf8.RuneCountInString(current)
		findings = appendFinding(findings, "ansi_escape", "high",
			fmt.Sprintf("%d escape/control byte(s) force-stripped after exceeding %d sanitize passes (fail-closed)", removed, maxSanitizePasses),
			detailSuffix)
	}

	return current, findings
}

func nfkcDiffCount(original, nfkc string) int {
	origRunes := []rune(original)
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
	lenDiff := len(origRunes) - len(nfkcRunes)
	if lenDiff < 0 {
		lenDiff = -lenDiff
	}
	diffCount += lenDiff
	if diffCount == 0 {
		diffCount = 1
	}
	return diffCount
}

func (u *UnicodeNormalizer) Scan(text string) ScanResult {
	result := ScanResult{Safe: true, Sanitized: text}

	current, findings := stripUntilStable(text, "")

	// NFKC normalization (fullwidth -> ASCII, compatibility decomposition)
	nfkc := norm.NFKC.String(current)
	if nfkc != current {
		findings = appendFinding(findings, "fullwidth", "high",
			fmt.Sprintf("NFKC normalization applied (%d characters affected)", nfkcDiffCount(current, nfkc)), "")
		current = nfkc

		// NFKC can reconstruct control sequences from fullwidth
		// characters (ESC + U+FF3B → CSI). Re-check every category to
		// a fixpoint so the last scan cannot leave a reconstructed
		// payload in the emitted text.
		stripped, extra := stripUntilStable(current, " (post-NFKC)")
		findings = append(findings, extra...)
		current = stripped
	}

	result.Findings = findings
	if current != text {
		result.Sanitized = current
		result.Safe = false // findings exist
	}

	return result
}
