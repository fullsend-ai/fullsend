package entitycel

import (
	"errors"
	"reflect"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

var errPartialNative = errors.New("entitycel: a partially known list has no native value")

// partialList is a CEL list whose known members are only part of the
// collection: more members may exist. It answers only what every completion
// of the collection agrees on:
//
//   - membership of a known member is true; anything else is unknown;
//   - size and indexing are unknown (known members need not be a contiguous
//     prefix of the collection);
//   - iteration visits the known members and then one unknown member, which
//     stands for the unknown remainder; the comprehension macros in [NewEnv]
//     additionally compare that with the known-only result so a remainder
//     that might be empty is never assumed to hold a member.
type partialList struct {
	known     []ref.Val
	remainder *types.Unknown
}

func newPartialList(known []any, remainder *types.Unknown) *partialList {
	vals := make([]ref.Val, len(known))
	for i, k := range known {
		vals[i] = types.DefaultTypeAdapter.NativeToValue(k)
	}
	return &partialList{known: vals, remainder: remainder}
}

// A partial list deliberately does not implement traits.Lister (it has no
// Add method): cel-go code that accepts any Lister assumes Size is a
// concrete int. List concatenation is handled by [decorate] instead.
var (
	_ traits.Container = (*partialList)(nil)
	_ traits.Indexer   = (*partialList)(nil)
	_ traits.Iterable  = (*partialList)(nil)
	_ traits.Sizer     = (*partialList)(nil)
)

// knownList returns the known members as an ordinary list.
func (l *partialList) knownList() ref.Val {
	return types.DefaultTypeAdapter.NativeToValue(l.known)
}

// ConvertToNative implements ref.Val. A partial list has no faithful native
// form.
func (l *partialList) ConvertToNative(reflect.Type) (any, error) {
	return nil, errPartialNative
}

// ConvertToType implements ref.Val.
func (l *partialList) ConvertToType(t ref.Type) ref.Val {
	switch t {
	case types.ListType:
		return l
	case types.TypeType:
		return types.ListType
	}
	return l.remainder
}

// Equal implements ref.Val.
func (l *partialList) Equal(other ref.Val) ref.Val { return equal(l, other) }

// Type implements ref.Val.
func (l *partialList) Type() ref.Type { return types.ListType }

// Value implements ref.Val.
func (l *partialList) Value() any { return l.known }

// Contains implements traits.Container.
func (l *partialList) Contains(elem ref.Val) ref.Val { return contains(l, elem) }

// Get implements traits.Indexer.
func (l *partialList) Get(index ref.Val) ref.Val {
	if _, err := types.IndexOrError(index); err != nil {
		return types.ValOrErr(index, "%v", err)
	}
	return l.remainder
}

// Size implements traits.Sizer.
func (l *partialList) Size() ref.Val { return l.remainder }

// Iterator implements traits.Iterable.
func (l *partialList) Iterator() traits.Iterator {
	return &partialIterator{list: l}
}

// partialIterator visits the known members, then the unknown remainder once.
type partialIterator struct {
	list *partialList
	next int
}

func (it *partialIterator) ConvertToNative(reflect.Type) (any, error) {
	return nil, errPartialNative
}
func (it *partialIterator) ConvertToType(ref.Type) ref.Val { return types.NewErr("no such overload") }
func (it *partialIterator) Equal(ref.Val) ref.Val          { return types.NewErr("no such overload") }
func (it *partialIterator) Type() ref.Type                 { return types.IteratorType }
func (it *partialIterator) Value() any                     { return nil }

func (it *partialIterator) HasNext() ref.Val {
	return types.Bool(it.next <= len(it.list.known))
}

func (it *partialIterator) Next() ref.Val {
	i := it.next
	if i > len(it.list.known) {
		return nil
	}
	it.next++
	if i < len(it.list.known) {
		return it.list.known[i]
	}
	return it.list.remainder
}
