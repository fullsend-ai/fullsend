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
// fetches the latest snapshot with [Cache.Get], merges its newly retrieved
// data, and retries [Cache.Put] with the snapshot's revision.
//
// The cached value is a [normentity.Document], the typed normalized-entity v1
// model. It represents unknown markers, nulls, absent members, and each
// collection's coverage, and the cache never rewrites them, so the contract's
// distinctions between absent, not loaded, unsupported, unknown, failed, and
// incomplete data survive a round trip.
//
// See docs/ADRs/0147-evaluate-entity-cel-over-selectively-acquired-data.md and
// docs/normative/normalized-entity/v1/README.md.
package entitycache

import (
	"errors"

	"github.com/fullsend-ai/fullsend/internal/normentity"
)

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
	// ErrInvalidEntity reports a missing document, one whose identity is
	// inconsistent (see [normentity.Document.Validate]), or one whose
	// entity.id is not the given entity ID.
	ErrInvalidEntity = errors.New("entitycache: invalid entity document")
	// ErrConflict reports a Put whose expected revision is not the entity's
	// current revision. The caller should Get the latest snapshot, merge its
	// data into it, and retry.
	ErrConflict = errors.New("entitycache: revision conflict")
)

// Cache retains partially acquired normalized entities keyed by canonical
// entity ID.
//
// Implementations must be safe for concurrent use, must copy documents on the
// way in and out so callers cannot mutate cached state outside Put, and must
// store documents exactly as given.
type Cache interface {
	// Get returns a private copy of the cached document for id and its
	// current revision. On a miss it returns ok false together with the
	// current revision, which a caller passes to Put to create the entry. An
	// invalid id misses with revision 0.
	Get(id normentity.ID) (doc *normentity.Document, rev Revision, ok bool)
	// Put atomically replaces the cached document for id when expected equals
	// the current revision, and returns the new revision. Otherwise it stores
	// nothing and returns the current revision with [ErrConflict]. It returns
	// [ErrInvalidID] or [ErrInvalidEntity] for invalid input.
	Put(id normentity.ID, doc *normentity.Document, expected Revision) (Revision, error)
	// Invalidate removes any cached document for id and advances its revision,
	// so a Put based on any earlier snapshot, including a miss, conflicts.
	// It returns the new revision, or [ErrInvalidID].
	Invalidate(id normentity.ID) (Revision, error)
}
