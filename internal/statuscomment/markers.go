package statuscomment

import (
	"regexp"

	"github.com/fullsend-ai/fullsend/internal/tracker"
)

// statusMarkerPrefix is the marker name shared by every status comment
// marker. It is used both to build the marker (see buildMarker) and to
// detect status comments (see statusMarkerOpener), so the two cannot drift.
// It is defined in tracker so the Jira tracker's legacy body check shares
// the same value without importing this package.
const statusMarkerPrefix = tracker.StatusMarkerPrefix

// statusMarkerOpener is the literal text that opens a live status marker.
// Detection matches on this rather than on the bare prefix, so agent text
// whose marker syntax was escaped by NeutralizeMarkers is not mistaken for
// a status comment.
const statusMarkerOpener = "<!-- " + statusMarkerPrefix

// markerOpenerRe matches the opener of any fullsend HTML comment marker,
// in any case and with any whitespace around the "fullsend" name.
var markerOpenerRe = regexp.MustCompile(`(?i)<(!--\s*fullsend\s*:)`)

// NeutralizeMarkers escapes the "<" of every fullsend marker opener
// ("<!--\s*fullsend\s*:", case-insensitive) in s as "&lt;". The text
// stays visible when rendered, but it no longer parses as a marker, so
// agent-written content cannot pose as one of the runner's own markers.
//
// Text that is already escaped contains no "<" at the opener, so the
// function is idempotent and repeated application does not grow s.
func NeutralizeMarkers(s string) string {
	return markerOpenerRe.ReplaceAllString(s, "&lt;$1")
}

// adfMarkerOpenerRe matches a fullsend marker opener in Markdown source
// whose "<" is written literally, backslash-escaped, or as a named or
// numeric character reference, and whose "!" and "-" may be
// backslash-escaped. Every one of those forms decodes to a literal
// "<!--" when the Markdown is converted to ADF. Group 1 captures the run
// of backslashes in front of the "<", group 2 the "<" itself and group 3
// the rest of the opener.
var adfMarkerOpenerRe = regexp.MustCompile(`(?i)(\\*)(<|&lt;|&#0*60;|&#x0*3c;)(\\?!(?:\\?-){2}\s*fullsend\s*:)`)

// NeutralizeMarkersADF is NeutralizeMarkers for Markdown that is stored
// as Jira ADF. NeutralizeMarkers' "&lt;" does not survive that path:
// Markdown-to-ADF decodes the reference into a literal "<" in the stored
// text, and ADF-to-Markdown writes that "<" back out unescaped, so the
// marker is live again when the comment is read back.
//
// This instead rewrites every opener whose "<" would decode to a literal
// "<" (see adfMarkerOpenerRe) as "\&lt;". The backslash-escaped
// ampersand is stored in ADF as the literal text "&lt;", and
// ADF-to-Markdown renders literal "&lt;" text as "\&lt;" again, so the
// neutralized form is a fixed point of the round trip. A reference that
// is already escaped ("\&lt;") is literal text, not a "<", and is left
// as is, so the function is idempotent.
func NeutralizeMarkersADF(s string) string {
	return adfMarkerOpenerRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := adfMarkerOpenerRe.FindStringSubmatch(m)
		slashes, opener, rest := sub[1], sub[2], sub[3]
		if opener != "<" && len(slashes)%2 == 1 {
			// The last backslash escapes the "&", so the reference
			// is literal text and the opener is already inert.
			return m
		}
		// An odd run ends in a backslash that escapes a literal "<";
		// drop it. The remaining pairs are literal backslashes.
		return slashes[:len(slashes)-len(slashes)%2] + `\&lt;` + rest
	})
}
