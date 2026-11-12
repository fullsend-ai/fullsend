package normentity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// UnknownReason is the diagnostic carried by a serialized `{"$unknown": ...}`
// marker. Every reason maps to CEL unknown; the distinction is for the host.
type UnknownReason string

// Unknown-marker reasons defined by the normalized-entity v1 specification.
const (
	ReasonNotLoaded          UnknownReason = "not_loaded"
	ReasonPartial            UnknownReason = "partial"
	ReasonUnsupported        UnknownReason = "unsupported"
	ReasonFailed             UnknownReason = "failed"
	ReasonConsistencyUnknown UnknownReason = "consistency_unknown"
)

// Valid reports whether r is one of the specified reasons.
func (r UnknownReason) Valid() bool {
	switch r {
	case ReasonNotLoaded, ReasonPartial, ReasonUnsupported, ReasonFailed, ReasonConsistencyUnknown:
		return true
	}
	return false
}

type fieldState uint8

const (
	stateAbsent fieldState = iota
	stateKnown
	stateNull
	stateUnknown
)

// Field is one entity attribute that may be unresolved. It distinguishes the
// four states the specification gives a field:
//
//   - absent (the zero value): the field does not apply to this kind or
//     category and is omitted from the document;
//   - known: a concrete value of type T;
//   - null: the source confirmed there is no value (only valid on nullable
//     fields; the JSON Schema decides which those are);
//   - unknown: unresolved, with a [UnknownReason]. A CEL binding maps this to
//     CEL unknown at the field's attribute path.
//
// Tag Field-typed struct members with `omitzero` so an absent field is
// omitted when marshaling.
type Field[T any] struct {
	state  fieldState
	value  T
	reason UnknownReason
}

// Known returns a field holding the concrete value v.
func Known[T any](v T) Field[T] { return Field[T]{state: stateKnown, value: v} }

// Null returns a field recording that the source confirmed no value.
func Null[T any]() Field[T] { return Field[T]{state: stateNull} }

// Unknown returns an unresolved field with the given reason.
func Unknown[T any](reason UnknownReason) Field[T] {
	return Field[T]{state: stateUnknown, reason: reason}
}

// IsZero reports whether the field is absent. It lets encoding/json's
// `omitzero` drop inapplicable fields.
func (f Field[T]) IsZero() bool { return f.state == stateAbsent }

// Value returns the concrete value and true when the field is known.
func (f Field[T]) Value() (T, bool) { return f.value, f.state == stateKnown }

// IsNull reports whether the source confirmed there is no value.
func (f Field[T]) IsNull() bool { return f.state == stateNull }

// UnknownReason returns the marker reason and true when the field is unknown.
func (f Field[T]) UnknownReason() (UnknownReason, bool) {
	return f.reason, f.state == stateUnknown
}

// Ptr returns a pointer to the concrete value for in-place updates, or nil
// when the field is not known.
func (f *Field[T]) Ptr() *T {
	if f.state != stateKnown {
		return nil
	}
	return &f.value
}

// MarshalJSON implements [json.Marshaler]. An absent field cannot be
// marshaled directly; omit it with `omitzero`.
func (f Field[T]) MarshalJSON() ([]byte, error) {
	switch f.state {
	case stateKnown:
		return json.Marshal(f.value)
	case stateNull:
		return []byte("null"), nil
	case stateUnknown:
		if !f.reason.Valid() {
			return nil, fmt.Errorf("normentity: invalid $unknown reason %q", f.reason)
		}
		return json.Marshal(map[string]UnknownReason{"$unknown": f.reason})
	}
	return nil, errors.New("normentity: absent field must be omitted (use omitzero)")
}

// UnmarshalJSON implements [json.Unmarshaler]. JSON null becomes a null
// field, an object with a `$unknown` member becomes an unknown field, and
// anything else is decoded as a concrete T.
func (f *Field[T]) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		*f = Null[T]()
		return nil
	}
	if len(data) > 0 && data[0] == '{' {
		var marker struct {
			Unknown *string `json:"$unknown"`
		}
		if err := json.Unmarshal(data, &marker); err == nil && marker.Unknown != nil {
			reason := UnknownReason(*marker.Unknown)
			if !reason.Valid() {
				return fmt.Errorf("normentity: invalid $unknown reason %q", *marker.Unknown)
			}
			*f = Unknown[T](reason)
			return nil
		}
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*f = Known(v)
	return nil
}
