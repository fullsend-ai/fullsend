package normentity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Extensions is the reusable source-defined data shape permitted on the
// entity, actions, threads, and checks. Both maps are keyed by configured CEL
// alias. The zero pointer means no extensions are declared for the record.
type Extensions struct {
	// CustomFields holds selected schema-defined provider fields (for
	// example Jira customfield_* values).
	CustomFields map[string]ExtensionEntry `json:"custom_fields"`
	// Properties holds selected API key/value properties.
	Properties map[string]ExtensionEntry `json:"properties"`
}

// MarshalJSON implements [json.Marshaler], always emitting both maps.
func (e Extensions) MarshalJSON() ([]byte, error) {
	type plain Extensions
	if e.CustomFields == nil {
		e.CustomFields = map[string]ExtensionEntry{}
	}
	if e.Properties == nil {
		e.Properties = map[string]ExtensionEntry{}
	}
	return json.Marshal(plain(e))
}

// ExtensionEntry is one configured extension value with the actor and time of
// its current revision.
type ExtensionEntry struct {
	SourceKey  string                `json:"source_key"`
	SourcePath string                `json:"source_path,omitempty"`
	Value      Field[ExtensionValue] `json:"value,omitzero"`
	ChangedBy  Field[Actor]          `json:"changed_by,omitzero"`
	ChangedAt  Field[Timestamp]      `json:"changed_at,omitzero"`
}

// ExtensionValue is a known extension value: exactly one of Atom or List. A
// source-confirmed unset value is a null [Field] and an unresolved one an
// unknown [Field]. A list value carries its own coverage.
type ExtensionValue struct {
	Atom *ExtensionAtom
	List *List[ExtensionAtom]
}

// MarshalJSON implements [json.Marshaler].
func (v ExtensionValue) MarshalJSON() ([]byte, error) {
	switch {
	case v.Atom != nil && v.List == nil:
		return json.Marshal(v.Atom)
	case v.List != nil && v.Atom == nil:
		return json.Marshal(v.List)
	}
	return nil, errors.New("normentity: extension value must set exactly one of Atom or List")
}

// UnmarshalJSON implements [json.Unmarshaler].
func (v *ExtensionValue) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	*v = ExtensionValue{}
	if len(data) > 0 && data[0] == '[' {
		v.List = new(List[ExtensionAtom])
		return json.Unmarshal(data, v.List)
	}
	v.Atom = new(ExtensionAtom)
	return json.Unmarshal(data, v.Atom)
}

// ExtensionOption is a typed option value.
type ExtensionOption struct {
	ID    string        `json:"id"`
	Label Field[string] `json:"label,omitzero"`
}

// ExtensionAtom is one typed extension atom: exactly one of a string, number,
// boolean, option, or actor.
type ExtensionAtom struct {
	String *string
	Number *float64
	Bool   *bool
	Option *ExtensionOption
	Actor  *Actor
}

// MarshalJSON implements [json.Marshaler].
func (a ExtensionAtom) MarshalJSON() ([]byte, error) {
	var (
		n   int
		out any
	)
	set := func(present bool, v any) {
		if present {
			n++
			out = v
		}
	}
	set(a.String != nil, a.String)
	set(a.Number != nil, a.Number)
	set(a.Bool != nil, a.Bool)
	set(a.Option != nil, a.Option)
	set(a.Actor != nil, a.Actor)
	if n != 1 {
		return nil, errors.New("normentity: extension atom must set exactly one value")
	}
	return json.Marshal(out)
}

// UnmarshalJSON implements [json.Unmarshaler].
func (a *ExtensionAtom) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	*a = ExtensionAtom{}
	if len(data) == 0 {
		return errors.New("normentity: empty extension atom")
	}
	switch c := data[0]; {
	case c == '"':
		a.String = new(string)
		return json.Unmarshal(data, a.String)
	case c == 't' || c == 'f':
		a.Bool = new(bool)
		return json.Unmarshal(data, a.Bool)
	case c == '-' || (c >= '0' && c <= '9'):
		a.Number = new(float64)
		return json.Unmarshal(data, a.Number)
	case c == '{':
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(data, &probe); err != nil {
			return err
		}
		if _, ok := probe["label"]; ok {
			a.Option = new(ExtensionOption)
			return json.Unmarshal(data, a.Option)
		}
		a.Actor = new(Actor)
		return json.Unmarshal(data, a.Actor)
	}
	return fmt.Errorf("normentity: unsupported extension atom %.20q", data)
}
