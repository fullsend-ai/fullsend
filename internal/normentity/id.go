package normentity

import (
	"errors"
	"net/url"
	"slices"
	"strings"
)

// idPrefix versions the canonical entity identifier format.
const idPrefix = "fullsend:entity:v1:"

// ID is the canonical fullsend entity ID defined by the normalized-entity v1
// specification: `fullsend:entity:v1:<system>:<host>:<kind>:<source_id>`,
// where each component is independently percent-encoded from its UTF-8 bytes
// (RFC 3986 unreserved bytes stay literal; every other byte becomes `%XX`
// with uppercase hex).
//
// It is not the entity-context snapshot's hashed record key, a repository or
// project path, or a contextual reference such as `#123`.
type ID string

// NewID builds the canonical entity ID from the normalized source system (for
// example "github"), the source host (lowercased, including a non-default port
// when present), the entity kind (for example "work_item"), and the provider's
// immutable entity ID.
func NewID(system, host, kind, sourceID string) (ID, error) {
	if system == "" || host == "" || kind == "" || sourceID == "" {
		return "", errors.New("normentity: entity ID requires system, host, kind, and source ID")
	}
	return ID(idPrefix +
		escapeComponent(system) + ":" +
		escapeComponent(strings.ToLower(host)) + ":" +
		escapeComponent(kind) + ":" +
		escapeComponent(sourceID)), nil
}

// Valid reports whether id has the canonical shape produced by NewID.
func (id ID) Valid() bool {
	rest, ok := strings.CutPrefix(string(id), idPrefix)
	if !ok {
		return false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 4 || slices.Contains(parts, "") {
		return false
	}
	// Decode each component, rebuild through NewID, and require an exact
	// match so every entity has exactly one spelling. Malformed or
	// lowercase-hex escapes, raw reserved bytes, and uppercase hosts all
	// fail the comparison.
	var decoded [4]string
	for i, p := range parts {
		d, err := url.PathUnescape(p)
		if err != nil {
			return false
		}
		decoded[i] = d
	}
	canonical, err := NewID(decoded[0], decoded[1], decoded[2], decoded[3])
	return err == nil && canonical == id
}

// escapeComponent percent-encodes every byte that is not an RFC 3986
// unreserved character, using uppercase hexadecimal digits.
func escapeComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0F])
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	switch {
	case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9':
		return true
	case c == '-', c == '.', c == '_', c == '~':
		return true
	}
	return false
}
