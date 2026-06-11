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
		} else {
			buf.WriteByte(' ')
		}
	}
	return buf.String()
}
