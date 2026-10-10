package runtime

import (
	"regexp"
	"strings"
)

// ansiEscRe matches ANSI CSI sequences, OSC sequences, and charset designators.
var ansiEscRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][A-Z0-9]`)

// sanitizeOutput strips ANSI escape sequences, control characters, and GHA
// workflow command markers from untrusted sandbox output. External sinks
// consume it through TranscriptError.DisplayMessage.
func sanitizeOutput(s string) string {
	return sanitize(s, false)
}

// sanitizeStreamText is like sanitizeOutput but preserves newlines for
// streaming text/thinking deltas. Within a single input, "::" is broken
// into ": :"; a caller concatenating separately sanitized chunks owns the
// chunk boundaries, where a trailing and a leading colon can still meet.
func sanitizeStreamText(s string) string {
	return sanitize(s, true)
}

// SanitizeForDisplay strips ANSI escapes, control characters, and GHA workflow
// command markers from untrusted text. Exported form of sanitizeOutput.
func SanitizeForDisplay(s string) string {
	return sanitizeOutput(s)
}

func sanitize(s string, preserveNewlines bool) string {
	s = ansiEscRe.ReplaceAllString(s, "")
	// A single non-overlapping ReplaceAll pass reconstitutes "::" at the
	// seam of two replacements (":::" -> ": ::"), so break colon pairs to
	// a fixed point: no output contains "::" and re-sanitizing is a no-op.
	// At most two passes run: the first leaves no colon run longer than
	// two (every seam pair sits between inserted spaces), and the second
	// breaks those isolated pairs without creating new ones.
	for strings.Contains(s, "::") {
		s = strings.ReplaceAll(s, "::", ": :")
	}
	for _, enc := range []string{"%0A", "%0a", "%0D", "%0d"} {
		s = strings.ReplaceAll(s, enc, " ")
	}
	var buf strings.Builder
	buf.Grow(len(s))
	for _, r := range s {
		if (r >= 0x20 && r < 0x7F) || r > 0x9F {
			buf.WriteRune(r)
		} else if preserveNewlines && r == '\n' {
			buf.WriteByte('\n')
		} else {
			buf.WriteByte(' ')
		}
	}
	return buf.String()
}
