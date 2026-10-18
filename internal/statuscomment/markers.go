package statuscomment

import "regexp"

// statusMarkerPrefix is the marker name shared by every status comment
// marker. It is used both to build the marker (see buildMarker) and to
// detect status comments (see statusMarkerOpener), so the two cannot drift.
const statusMarkerPrefix = "fullsend:agent-status:"

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
