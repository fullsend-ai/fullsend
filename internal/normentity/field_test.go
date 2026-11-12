package normentity

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFieldStates(t *testing.T) {
	var absent Field[string]
	assert.True(t, absent.IsZero())
	_, ok := absent.Value()
	assert.False(t, ok)
	assert.Nil(t, absent.Ptr())
	_, err := json.Marshal(absent)
	assert.Error(t, err, "an absent field must be omitted, not marshaled")

	known := Known("x")
	assert.False(t, known.IsZero())
	v, ok := known.Value()
	assert.True(t, ok)
	assert.Equal(t, "x", v)
	*known.Ptr() = "y"
	v, _ = known.Value()
	assert.Equal(t, "y", v)

	null := Null[string]()
	assert.False(t, null.IsZero())
	assert.True(t, null.IsNull())
	_, ok = null.Value()
	assert.False(t, ok)
	_, ok = null.UnknownReason()
	assert.False(t, ok)

	unk := Unknown[string](ReasonPartial)
	reason, ok := unk.UnknownReason()
	assert.True(t, ok)
	assert.Equal(t, ReasonPartial, reason)
	assert.Nil(t, unk.Ptr())
	assert.False(t, unk.IsNull())
}

func TestFieldJSON(t *testing.T) {
	type holder struct {
		S Field[string] `json:"s,omitzero"`
		N Field[int]    `json:"n,omitzero"`
		O Field[Actor]  `json:"o,omitzero"`
		B Field[bool]   `json:"b,omitzero"`
	}
	in := holder{
		S: Known(""), // an empty string is a concrete value, not absent
		N: Null[int](),
		O: Unknown[Actor](ReasonConsistencyUnknown),
	}
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	assert.JSONEq(t, `{"s":"","n":null,"o":{"$unknown":"consistency_unknown"}}`, string(raw))

	var out holder
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, in, out)
	assert.True(t, out.B.IsZero(), "an omitted member stays absent")

	// A concrete object is decoded, not mistaken for a marker.
	require.NoError(t, json.Unmarshal([]byte(`{"o":{"id":"7","kind":"human"}}`), &out))
	a, ok := out.O.Value()
	require.True(t, ok)
	id, _ := a.ID.Value()
	assert.Equal(t, "7", id)

	for _, bad := range []string{
		`{"s":{"$unknown":"bogus"}}`,
		`{"n":"text"}`,
	} {
		assert.Error(t, json.Unmarshal([]byte(bad), &out), bad)
	}

	_, err = json.Marshal(holder{S: Unknown[string]("bogus")})
	assert.Error(t, err)
}

func TestUnknownReasonValid(t *testing.T) {
	for _, r := range []UnknownReason{ReasonNotLoaded, ReasonPartial, ReasonUnsupported, ReasonFailed, ReasonConsistencyUnknown} {
		assert.True(t, r.Valid(), r)
	}
	assert.False(t, UnknownReason("").Valid())
	assert.False(t, UnknownReason("nope").Valid())
}

func TestListJSON(t *testing.T) {
	var absent List[string]
	assert.True(t, absent.IsZero())

	empty := List[string]{Known: []string{}}
	assert.False(t, empty.IsZero(), "present with no members")
	raw, err := json.Marshal(empty)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(raw))

	// A present list with only coverage still serializes as an empty array.
	raw, err = json.Marshal(List[string]{Coverage: Coverage{Status: CoverageNotLoaded}})
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(raw))

	var l List[string]
	require.NoError(t, json.Unmarshal([]byte(`["a","b"]`), &l))
	assert.Equal(t, []string{"a", "b"}, l.Known)
	assert.True(t, l.Coverage.IsZero(), "coverage is rejoined by Document")
	require.NoError(t, json.Unmarshal([]byte(`null`), &l))
	assert.Nil(t, l.Known)
	assert.Error(t, json.Unmarshal([]byte(`{}`), &l))
}

func TestExtensionValueJSON(t *testing.T) {
	s, n, b := "text", 2.5, true
	for name, v := range map[string]ExtensionAtom{
		"string": {String: &s},
		"number": {Number: &n},
		"bool":   {Bool: &b},
		"option": {Option: &ExtensionOption{ID: "1", Label: Known("One")}},
		"actor":  {Actor: &Actor{ID: Known("u1"), Kind: Known(ActorHuman)}},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(v)
			require.NoError(t, err)
			var out ExtensionAtom
			require.NoError(t, json.Unmarshal(raw, &out))
			assert.Equal(t, v, out)
		})
	}

	// A list value keeps its members; its coverage is rejoined by Document.
	raw, err := json.Marshal(ExtensionValue{List: &List[ExtensionAtom]{Known: []ExtensionAtom{{String: &s}}}})
	require.NoError(t, err)
	assert.JSONEq(t, `["text"]`, string(raw))
	var lv ExtensionValue
	require.NoError(t, json.Unmarshal(raw, &lv))
	require.NotNil(t, lv.List)
	assert.Nil(t, lv.Atom)

	// Exactly one member must be set.
	for name, bad := range map[string]any{
		"empty atom":  ExtensionAtom{},
		"two atoms":   ExtensionAtom{String: &s, Bool: &b},
		"empty value": ExtensionValue{},
		"both":        ExtensionValue{Atom: &ExtensionAtom{String: &s}, List: &List[ExtensionAtom]{}},
	} {
		_, err := json.Marshal(bad)
		assert.Error(t, err, name)
	}
	for _, bad := range []string{``, `null`, `[null]`, `{"x":`} {
		var a ExtensionAtom
		assert.Error(t, a.UnmarshalJSON([]byte(bad)), bad)
	}
	var out ExtensionAtom
	assert.Error(t, json.Unmarshal([]byte(`"x`), &out))
}

func TestExtensionsAlwaysEmitBothMaps(t *testing.T) {
	raw, err := json.Marshal(Extensions{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"custom_fields":{},"properties":{}}`, string(raw))
}
