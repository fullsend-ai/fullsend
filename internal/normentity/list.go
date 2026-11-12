package normentity

import (
	"bytes"
	"encoding/json"
)

// CoverageStatus says how much of a collection the known members represent.
type CoverageStatus string

// Collection coverage states defined by the normalized-entity v1
// specification.
const (
	// CoverageComplete: exhaustively known at the stated source revision; an
	// empty known list is truly empty.
	CoverageComplete CoverageStatus = "complete"
	// CoveragePartial: some members are known, more may exist.
	CoveragePartial CoverageStatus = "partial"
	// CoverageNotLoaded: no members were requested.
	CoverageNotLoaded CoverageStatus = "not_loaded"
	// CoverageUnknown: completeness cannot be established.
	CoverageUnknown CoverageStatus = "unknown"
	// CoverageUnsupported: the source or kind cannot provide the collection.
	CoverageUnsupported CoverageStatus = "unsupported"
	// CoverageFailed: fetch, provenance, or permission resolution failed.
	CoverageFailed CoverageStatus = "failed"
)

// Coverage describes the unknown remainder of one collection. Reason and
// SourceRevision are optional host diagnostics.
type Coverage struct {
	Status         CoverageStatus `json:"status"`
	Reason         string         `json:"reason,omitempty"`
	SourceRevision string         `json:"source_revision,omitempty"`
}

// IsZero reports whether no coverage has been recorded.
func (c Coverage) IsZero() bool { return c == Coverage{} }

// List is a logical CEL list that may be only partly known: the known
// members plus the coverage of the remainder.
//
// The specification serializes coverage in a separate host-only `coverage`
// document, but a CEL binding needs both at once — it MUST NOT bind the known
// members as an ordinary complete list when coverage is non-complete. Keeping
// them together here means the binding can wrap Known and Coverage directly,
// and the two can never drift apart or be keyed to the wrong collection.
// [Document] splits and rejoins them at the JSON boundary.
//
// The zero List is absent: the collection does not apply to the entity's kind
// and is omitted. A present collection with no known members and no recorded
// coverage has a non-nil empty Known. Tag List-typed struct members with
// `omitzero`.
type List[T any] struct {
	// Known holds the known members in the collection's specified order.
	Known []T
	// Coverage describes any members beyond Known.
	Coverage Coverage
}

// IsZero reports whether the collection is absent.
func (l List[T]) IsZero() bool { return l.Coverage.IsZero() && l.Known == nil }

// MarshalJSON implements [json.Marshaler]. The serialized form is the bare
// array of known members; coverage is serialized by [Document].
func (l List[T]) MarshalJSON() ([]byte, error) {
	if l.Known == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(l.Known)
}

// UnmarshalJSON implements [json.Unmarshaler]. Coverage is rejoined by
// [Document]; until then it is zero.
func (l *List[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		l.Known = nil
		return nil
	}
	return json.Unmarshal(data, &l.Known)
}
