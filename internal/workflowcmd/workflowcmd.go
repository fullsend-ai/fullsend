// Package workflowcmd makes dynamic strings safe to embed in GitHub Actions
// workflow command annotations (::error::, ::warning::).
package workflowcmd

import (
	"strings"
	"unicode"
)

// annotationEscaper encodes the characters GitHub Actions treats specially
// in a workflow command message: '%' first (so a literal "%0A" in a value
// stays visible as text), then CR and LF. Breaking up "::" is done separately
// in SanitizeAnnotation, because a non-overlapping replacer leaves "::" in
// runs of three or more colons.
var annotationEscaper = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")

// SanitizeAnnotation makes a dynamic string safe to embed in a workflow
// command annotation: control characters (including ANSI escape
// sequences' ESC) are dropped, the characters annotationEscaper names are
// encoded, and "::" is broken up until none remains so a value cannot read
// as the start of another workflow command.
func SanitizeAnnotation(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = annotationEscaper.Replace(s)
	for strings.Contains(s, "::") {
		s = strings.ReplaceAll(s, "::", ": :")
	}
	return s
}
