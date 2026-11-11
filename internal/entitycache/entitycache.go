// Package entitycache implements a revisioned cache for partially acquired
// normalized entities.
//
// Entity data is acquired progressively (ADR 0147): several clients may fetch
// additional fields or history for the same entity concurrently. The cache
// retains the current partial entity for each canonical entity ID together
// with a revision, and updates it only through an atomic compare-and-swap so
// concurrent clients cannot silently overwrite each other's updates.
//
// Merging is deliberately outside the cache API. After a conflict, a caller
// fetches the latest snapshot with [Cache.Get], merges in its newly retrieved
// data, and retries [Cache.Put] with the snapshot's revision.
//
// The cached value is a serialized normalized-entity v1 document stored
// exactly as given. The cache never rewrites unknown markers, collection
// coverage, or nullable fields, so the contract's distinctions between absent,
// not loaded, unsupported, unknown, failed, and incomplete data survive a
// round trip.
//
// See docs/ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md and
// docs/normative/normalized-entity/v1/README.md.
package entitycache

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// entityIDPrefix versions the canonical entity identifier format.
const entityIDPrefix = "fullsend:entity:v1:"

// EntityID is the canonical fullsend entity ID defined by the
// normalized-entity v1 specification:
// `fullsend:entity:v1:<system>:<host>:<kind>:<source_id>`, where each
// component is independently percent-encoded from its UTF-8 bytes (RFC 3986
// unreserved bytes stay literal; every other byte becomes `%XX` with
// uppercase hex).
//
// It is not the entity-context snapshot's hashed record key, a repository or
// project path, or a contextual reference such as `#123`.
type EntityID string

// NewEntityID builds the canonical entity ID from the normalized source system
// (for example "github"), the source host (lowercased, including a non-default
// port when present), the entity kind (for example "work_item"), and the
// provider's immutable entity ID.
func NewEntityID(system, host, kind, sourceID string) (EntityID, error) {
	if system == "" || host == "" || kind == "" || sourceID == "" {
		return "", errors.New("entitycache: entity ID requires system, host, kind, and source ID")
	}
	return EntityID(entityIDPrefix +
		escapeComponent(system) + ":" +
		escapeComponent(strings.ToLower(host)) + ":" +
		escapeComponent(kind) + ":" +
		escapeComponent(sourceID)), nil
}

// Valid reports whether id has the canonical shape produced by NewEntityID.
func (id EntityID) Valid() bool {
	rest, ok := strings.CutPrefix(string(id), entityIDPrefix)
	if !ok {
		return false
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 4 || slices.Contains(parts, "") {
		return false
	}
	// Decode each component, rebuild through NewEntityID, and require an
	// exact match so every entity has exactly one spelling. Malformed or
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
	canonical, err := NewEntityID(decoded[0], decoded[1], decoded[2], decoded[3])
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

// Entity is a serialized normalized-entity v1 document, possibly partial: a
// JSON object whose `entity` member carries field values (with
// `{"$unknown": "<reason>"}` markers for unresolved values) and whose
// `coverage` and `view` members carry host-only metadata. The cache treats it
// as opaque apart from checking that `entity.id` matches the cache key.
type Entity = json.RawMessage

// validateEntity checks that doc is a JSON object whose `entity.id` member is
// exactly id. Key matching is case-sensitive.
func validateEntity(id EntityID, doc Entity) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil || top == nil {
		return fmt.Errorf("%w: document is not a JSON object", ErrInvalidEntity)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(top["entity"], &fields); err != nil || fields == nil {
		return fmt.Errorf("%w: document has no entity object", ErrInvalidEntity)
	}
	var got string
	if err := json.Unmarshal(fields["id"], &got); err != nil {
		return fmt.Errorf("%w: entity.id is missing or not a string", ErrInvalidEntity)
	}
	if EntityID(got) != id {
		return fmt.Errorf("%w: entity.id %q does not match key %q", ErrInvalidEntity, got, id)
	}
	return nil
}

// Revision identifies a version of one entity's cache state. Revisions for a
// given entity ID strictly increase with every successful Put and every
// Invalidate, and are never reused, so equal revisions imply no intervening
// change. Revisions are comparable only for the same entity ID. An entity ID
// the cache has never seen has revision 0.
type Revision uint64

// Errors returned by [Cache] methods.
var (
	// ErrInvalidID reports an entity ID that is not in canonical form.
	ErrInvalidID = errors.New("entitycache: invalid entity ID")
	// ErrInvalidEntity reports a document that is not a normalized-entity
	// JSON object for the given entity ID.
	ErrInvalidEntity = errors.New("entitycache: invalid entity document")
	// ErrConflict reports a Put whose expected revision is not the entity's
	// current revision. The caller should Get the latest snapshot, merge its
	// data into it, and retry.
	ErrConflict = errors.New("entitycache: revision conflict")
)

// Cache retains partially acquired normalized entities keyed by canonical
// entity ID.
//
// Implementations must be safe for concurrent use, must copy entities on the
// way in and out so callers cannot mutate cached state outside Put, and must
// store entities exactly as given.
type Cache interface {
	// Get returns the cached entity for id and its current revision. On a
	// miss it returns ok false together with the current revision, which a
	// caller passes to Put to create the entry. An invalid id misses with
	// revision 0.
	Get(id EntityID) (entity Entity, rev Revision, ok bool)
	// Put atomically replaces the cached entity for id when expected equals
	// the current revision, and returns the new revision. Otherwise it stores
	// nothing and returns the current revision with [ErrConflict]. It returns
	// [ErrInvalidID] or [ErrInvalidEntity] for invalid input.
	Put(id EntityID, entity Entity, expected Revision) (Revision, error)
	// Invalidate removes any cached entity for id and advances its revision,
	// so a Put based on any earlier snapshot, including a miss, conflicts.
	// It returns the new revision, or [ErrInvalidID].
	Invalidate(id EntityID) (Revision, error)
}
