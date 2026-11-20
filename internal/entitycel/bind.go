// Package entitycel binds a normalized entity (normalized-entity v1) to CEL
// and evaluates entity-source harness predicates over a required `entity`
// and a nullable `event` (ADR 0098, ADR 0147).
//
// The binding preserves the distinctions the specification draws between
// absent, null, known, and unresolved data:
//
//   - an absent field (it does not apply to the entity's kind) is omitted and
//     follows ordinary CEL missing-key behavior;
//   - a null field is CEL null;
//   - an unknown field ($unknown: not_loaded, partial, unsupported, failed,
//     consistency_unknown) is a CEL unknown at its attribute path;
//   - a collection with complete coverage is an ordinary CEL list, and any
//     other coverage (partial, not_loaded, unknown, unsupported, failed, or no
//     recorded coverage) is a list whose remainder is unknown, so a known
//     member can satisfy `exists` while emptiness, size, and absence stay
//     unknown.
//
// Evaluate reports a three-valued [Result]; unknown data can never become a
// false result.
package entitycel

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/google/cel-go/common/types"

	"github.com/fullsend-ai/fullsend/internal/normentity"
)

// RootEntity and RootEvent are the CEL root variables of an entity-source
// predicate.
const (
	RootEntity = "entity"
	RootEvent  = "event"
)

// fieldLike is the non-generic method set of [normentity.Field].
type fieldLike interface {
	IsZero() bool
	IsNull() bool
	UnknownReason() (normentity.UnknownReason, bool)
}

var (
	fieldLikeType = reflect.TypeFor[fieldLike]()
	coverageType  = reflect.TypeFor[normentity.Coverage]()
	extValueType  = reflect.TypeFor[normentity.ExtensionValue]()
	extAtomType   = reflect.TypeFor[normentity.ExtensionAtom]()
)

// Bind converts e into the CEL value bound to the `entity` root variable.
// It fails on a malformed entity,
// such as an unknown coverage status or an extension value that does not set
// exactly one member; callers must treat that as unresolved data, never as a
// false predicate.
func Bind(e *normentity.Entity) (map[string]any, error) {
	if e == nil {
		return nil, fmt.Errorf("entitycel: entity is required")
	}
	v, present, err := bindValue(reflect.ValueOf(*e), nil)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !present || !ok {
		return nil, fmt.Errorf("entitycel: entity did not bind to a map")
	}
	return m, nil
}

// unknownAt returns a CEL unknown whose attribute trail is entity.<path>.
func unknownAt(path []any) *types.Unknown {
	trail := types.NewAttributeTrail(RootEntity)
	for _, q := range path {
		switch q := q.(type) {
		case string:
			trail = types.QualifyAttribute(trail, q)
		case int64:
			trail = types.QualifyAttribute(trail, q)
		}
	}
	return types.NewUnknown(0, trail)
}

// extend returns path with q appended, never aliasing path's backing array.
func extend(path []any, q any) []any {
	return append(slices.Clip(path), q)
}

// bindValue converts one Go value of the entity model. present is false when
// the value is absent and its member must be omitted.
func bindValue(v reflect.Value, path []any) (out any, present bool, err error) {
	t := v.Type()
	switch {
	case t.Implements(fieldLikeType):
		return bindField(v, path)
	case isList(t):
		return bindList(v, path)
	case t == extValueType:
		return bindExtensionValue(v.Interface().(normentity.ExtensionValue), path)
	case t == extAtomType:
		return bindExtensionAtom(v.Interface().(normentity.ExtensionAtom), path)
	}
	switch t.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil, false, nil
		}
		return bindValue(v.Elem(), path)
	case reflect.Struct:
		m := map[string]any{}
		if err := bindStruct(v, path, m); err != nil {
			return nil, false, err
		}
		return m, true, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, false, fmt.Errorf("entitycel: %s: unsupported map key type %s", pathString(path), t.Key())
		}
		m := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			k := iter.Key().String()
			ev, ok, err := bindValue(iter.Value(), extend(path, k))
			if err != nil {
				return nil, false, err
			}
			if ok {
				m[k] = ev
			}
		}
		return m, true, nil
	case reflect.Slice:
		l := make([]any, 0, v.Len())
		for i := range v.Len() {
			ev, ok, err := bindValue(v.Index(i), extend(path, int64(i)))
			if err != nil {
				return nil, false, err
			}
			if !ok {
				return nil, false, fmt.Errorf("entitycel: %s: absent list member", pathString(extend(path, int64(i))))
			}
			l = append(l, ev)
		}
		return l, true, nil
	case reflect.String:
		return v.String(), true, nil
	case reflect.Bool:
		return v.Bool(), true, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), true, nil
	case reflect.Float32, reflect.Float64:
		return v.Float(), true, nil
	}
	return nil, false, fmt.Errorf("entitycel: %s: unsupported type %s", pathString(path), t)
}

// bindStruct adds a struct's members to m using their JSON names, flattening
// embedded structs as encoding/json does.
func bindStruct(v reflect.Value, path []any, m map[string]any) error {
	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		tag := sf.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		fv := v.Field(i)
		if sf.Anonymous && name == "" && sf.Type.Kind() == reflect.Struct {
			if err := bindStruct(fv, path, m); err != nil {
				return err
			}
			continue
		}
		if name == "" {
			name = sf.Name
		}
		if hasOpt(opts, "omitempty") && isEmpty(fv) {
			continue
		}
		bv, ok, err := bindValue(fv, extend(path, name))
		if err != nil {
			return err
		}
		if ok {
			m[name] = bv
		}
	}
	return nil
}

func hasOpt(opts, want string) bool {
	for o := range strings.SplitSeq(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

// isEmpty mirrors encoding/json's omitempty test for the kinds the entity
// model uses it on.
func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String, reflect.Map, reflect.Slice:
		return v.Len() == 0
	case reflect.Pointer, reflect.Interface:
		return v.IsNil()
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	}
	return false
}

// bindField converts a [normentity.Field]: absent is omitted, null is CEL
// null, unknown is CEL unknown at the field's path, and known is converted.
func bindField(v reflect.Value, path []any) (any, bool, error) {
	f := v.Interface().(fieldLike)
	switch {
	case f.IsZero():
		return nil, false, nil
	case f.IsNull():
		return types.NullValue, true, nil
	}
	if _, unknown := f.UnknownReason(); unknown {
		return unknownAt(path), true, nil
	}
	res := v.MethodByName("Value").Call(nil)
	if !res[1].Bool() {
		return nil, false, fmt.Errorf("entitycel: %s: field is in no valid state", pathString(path))
	}
	return bindValue(res[0], path)
}

// isList reports whether t is an instantiation of [normentity.List].
func isList(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t.NumField() != 2 {
		return false
	}
	known, ok := t.FieldByName("Known")
	if !ok || known.Type.Kind() != reflect.Slice {
		return false
	}
	cov, ok := t.FieldByName("Coverage")
	return ok && cov.Type == coverageType
}

// bindList converts a [normentity.List]. Only complete coverage yields an
// ordinary list; every other state leaves the remainder unknown.
func bindList(v reflect.Value, path []any) (any, bool, error) {
	if v.Interface().(interface{ IsZero() bool }).IsZero() {
		return nil, false, nil
	}
	cov := v.FieldByName("Coverage").Interface().(normentity.Coverage)
	known, _, err := bindValue(v.FieldByName("Known"), path)
	if err != nil {
		return nil, false, err
	}
	members := known.([]any)
	switch cov.Status {
	case normentity.CoverageComplete:
		return members, true, nil
	case "", normentity.CoveragePartial, normentity.CoverageNotLoaded, normentity.CoverageUnknown,
		normentity.CoverageUnsupported, normentity.CoverageFailed:
		return newPartialList(members, unknownAt(path)), true, nil
	}
	return nil, false, fmt.Errorf("entitycel: %s: invalid coverage status %q", pathString(path), cov.Status)
}

func bindExtensionValue(x normentity.ExtensionValue, path []any) (any, bool, error) {
	switch {
	case x.Atom != nil && x.List == nil:
		return bindExtensionAtom(*x.Atom, path)
	case x.List != nil && x.Atom == nil:
		return bindList(reflect.ValueOf(*x.List), path)
	}
	return nil, false, fmt.Errorf("entitycel: %s: extension value must set exactly one of Atom or List", pathString(path))
}

func bindExtensionAtom(a normentity.ExtensionAtom, path []any) (any, bool, error) {
	var set []reflect.Value
	for _, p := range []any{a.String, a.Number, a.Bool, a.Option, a.Actor} {
		if rv := reflect.ValueOf(p); !rv.IsNil() {
			set = append(set, rv)
		}
	}
	if len(set) != 1 {
		return nil, false, fmt.Errorf("entitycel: %s: extension atom must set exactly one value", pathString(path))
	}
	return bindValue(set[0], path)
}

// pathString renders path for error messages.
func pathString(path []any) string {
	var b strings.Builder
	b.WriteString(RootEntity)
	for _, q := range path {
		switch q := q.(type) {
		case string:
			b.WriteString(".")
			b.WriteString(q)
		case int64:
			fmt.Fprintf(&b, "[%d]", q)
		}
	}
	return b.String()
}
