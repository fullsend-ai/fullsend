package runtime

import (
	"regexp"
	"strings"
)

// ansiEscRe matches ANSI CSI sequences, OSC sequences, and charset designators.
var ansiEscRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][A-Z0-9]`)

// sanitizeOutput strips ANSI escape sequences, control characters, and GHA
// workflow command markers from untrusted sandbox output.
func sanitizeOutput(s string) string {
	return sanitize(s, false)
}

// sanitizeStreamText is like sanitizeOutput but preserves newlines for
// streaming text/thinking deltas. GHA command injection is still blocked
// because "::" is broken into ": :".
func sanitizeStreamText(s string) string {
	return sanitize(s, true)
}

func sanitize(s string, preserveNewlines bool) string {
	s = ansiEscRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "::", ": :")
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
