package entitycel

import (
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/interpreter"
)

// cel-go's built-in list and map equality treat any element comparison that
// is not definitely false — including an unknown — as equal, and compare a
// list's size by identity, so `[] == entity.labels` would be false for an
// empty partial list and `[unknown] == [1]` would be true. decorate replaces
// the operators whose built-in implementations compare container members
// (==, !=, in, and list +) with versions that propagate unknowns from nested
// values and partial lists instead of resolving them.
func decorate(i interpreter.InterpretableV2) (interpreter.InterpretableV2, error) {
	call, ok := i.(interpreter.InterpretableCall)
	if !ok || len(call.Args()) != 2 {
		return i, nil
	}
	var op func(lhs, rhs ref.Val) ref.Val
	switch call.Function() {
	case operators.Equals:
		op = equal
	case operators.NotEquals:
		op = notEqual
	case operators.In:
		op = func(lhs, rhs ref.Val) ref.Val { return in(lhs, rhs) }
	case operators.Add:
		op = add
	default:
		return i, nil
	}
	return &soundCall{InterpretableCall: call, op: op}, nil
}

// soundCall evaluates a binary operator with an unknown-preserving
// implementation.
type soundCall struct {
	interpreter.InterpretableCall
	op func(lhs, rhs ref.Val) ref.Val
}

func (c *soundCall) Exec(frame *interpreter.ExecutionFrame) ref.Val {
	args := c.Args()
	return c.op(args[0].Exec(frame), args[1].Exec(frame))
}

func (c *soundCall) Eval(a interpreter.Activation) ref.Val {
	return c.Exec(interpreter.AsFrame(a))
}

// mergeUnknowns returns the merge of every unknown among vals, or nil.
func mergeUnknowns(vals ...ref.Val) *types.Unknown {
	var unk *types.Unknown
	for _, v := range vals {
		if u, ok := v.(*types.Unknown); ok {
			if unk == nil {
				unk = u
			} else {
				unk = types.MergeUnknowns(unk, u)
			}
		}
	}
	return unk
}

// strictOperands applies CEL's strict-operator rule: an error or unknown
// operand is the result, left operand first.
func strictOperands(lhs, rhs ref.Val) (ref.Val, bool) {
	if types.IsUnknownOrError(lhs) {
		return lhs, true
	}
	if types.IsUnknownOrError(rhs) {
		return rhs, true
	}
	return nil, false
}

// equal is CEL equality that is true or false only when every completion of
// the operands' unknown values and partial lists agrees.
func equal(lhs, rhs ref.Val) ref.Val {
	if types.IsError(lhs) {
		return lhs
	}
	if types.IsError(rhs) {
		return rhs
	}
	if unk := mergeUnknowns(lhs, rhs); unk != nil {
		return unk
	}
	if lhs == types.NullValue || rhs == types.NullValue {
		return types.Bool(lhs == rhs)
	}
	lp, lPartial := lhs.(*partialList)
	rp, rPartial := rhs.(*partialList)
	switch {
	case lPartial && rPartial:
		return types.MergeUnknowns(lp.remainder, rp.remainder)
	case lPartial:
		return partialEqual(lp, rhs)
	case rPartial:
		return partialEqual(rp, lhs)
	}
	if ll, ok := lhs.(traits.Lister); ok {
		rl, ok := rhs.(traits.Lister)
		if !ok {
			return types.False
		}
		return listEqual(ll, rl)
	}
	if lm, ok := lhs.(traits.Mapper); ok {
		rm, ok := rhs.(traits.Mapper)
		if !ok {
			return types.False
		}
		return mapEqual(lm, rm)
	}
	return types.Equal(lhs, rhs)
}

func notEqual(lhs, rhs ref.Val) ref.Val {
	eq := equal(lhs, rhs)
	if b, ok := eq.(types.Bool); ok {
		return !b
	}
	return eq
}

// partialEqual compares a partial list with a value that is not one. The
// partial list holds at least its known members, so it cannot equal a
// shorter list or a non-list; anything else depends on the remainder.
func partialEqual(p *partialList, other ref.Val) ref.Val {
	ol, ok := other.(traits.Lister)
	if !ok {
		return types.False
	}
	n, ok := ol.Size().(types.Int)
	if ok && int64(len(p.known)) > int64(n) {
		return types.False
	}
	return p.remainder
}

// combine folds member comparison results: any false is false, otherwise
// any unknown is unknown, otherwise any error is the error.
type combine struct {
	unk *types.Unknown
	err ref.Val
}

// add records one result and reports whether it is definitely false.
func (c *combine) add(v ref.Val) bool {
	switch {
	case v == types.False:
		return true
	case types.IsUnknown(v):
		c.unk = mergeUnknowns(c.unk, v)
	case types.IsError(v):
		if c.err == nil {
			c.err = v
		}
	}
	return false
}

func (c *combine) result() ref.Val {
	if c.unk != nil {
		return c.unk
	}
	if c.err != nil {
		return c.err
	}
	return types.True
}

func listEqual(a, b traits.Lister) ref.Val {
	as, bs := a.Size(), b.Size()
	if unk := mergeUnknowns(as, bs); unk != nil {
		return unk
	}
	an, aok := as.(types.Int)
	bn, bok := bs.(types.Int)
	if !aok || !bok {
		return types.NewErr("no such overload")
	}
	if an != bn {
		return types.False
	}
	var c combine
	for i := range an {
		if c.add(equal(a.Get(i), b.Get(i))) {
			return types.False
		}
	}
	return c.result()
}

func mapEqual(a, b traits.Mapper) ref.Val {
	if a.Size() != b.Size() {
		return types.False
	}
	var c combine
	it := a.Iterator()
	for it.HasNext() == types.True {
		key := it.Next()
		av, _ := a.Find(key)
		bv, found := b.Find(key)
		if !found {
			return types.False
		}
		if c.add(equal(av, bv)) {
			return types.False
		}
	}
	return c.result()
}

// in evaluates `elem in container`.
func in(elem, container ref.Val) ref.Val {
	if v, ok := strictOperands(elem, container); ok {
		return v
	}
	switch c := container.(type) {
	case *partialList:
		if found := memberOf(elem, c.known); found == types.True {
			return found
		}
		return c.remainder
	case traits.Lister:
		known, _, _ := listParts(c)
		return memberOf(elem, known)
	case traits.Container:
		if container.Type().HasTrait(traits.ContainerType) {
			return c.Contains(elem)
		}
	}
	return types.MaybeNoSuchOverloadErr(container)
}

// contains implements traits.Container for a partial list.
func contains(p *partialList, elem ref.Val) ref.Val {
	if types.IsUnknownOrError(elem) {
		return elem
	}
	return in(elem, p)
}

// memberOf is true when some member definitely equals elem, unknown when
// none does but some comparison is unknown, and false otherwise.
func memberOf(elem ref.Val, members []ref.Val) ref.Val {
	var unk *types.Unknown
	for _, m := range members {
		switch eq := equal(elem, m); {
		case eq == types.True:
			return types.True
		case types.IsUnknown(eq):
			unk = mergeUnknowns(unk, eq)
		}
	}
	if unk != nil {
		return unk
	}
	return types.False
}

// listParts returns a list's known members and, for a partial list, its
// unknown remainder. ok is false when v is not a list.
func listParts(v ref.Val) (known []ref.Val, remainder *types.Unknown, ok bool) {
	switch l := v.(type) {
	case *partialList:
		return l.known, l.remainder, true
	case traits.Lister:
		it := l.Iterator()
		for it.HasNext() == types.True {
			known = append(known, it.Next())
		}
		return known, nil, true
	}
	return nil, nil, false
}

// add evaluates `lhs + rhs`, keeping a concatenation that involves a partial
// list partial.
func add(lhs, rhs ref.Val) ref.Val {
	if v, ok := strictOperands(lhs, rhs); ok {
		return v
	}
	_, lPartial := lhs.(*partialList)
	_, rPartial := rhs.(*partialList)
	if lPartial || rPartial {
		return concat(lhs, rhs)
	}
	if adder, ok := lhs.(traits.Adder); ok && lhs.Type().HasTrait(traits.AdderType) {
		return adder.Add(rhs)
	}
	return types.MaybeNoSuchOverloadErr(lhs)
}

// concat joins two lists, at least one partial, into a partial list.
func concat(lhs, rhs ref.Val) ref.Val {
	lk, lr, ok := listParts(lhs)
	if !ok {
		return types.MaybeNoSuchOverloadErr(lhs)
	}
	rk, rr, ok := listParts(rhs)
	if !ok {
		return types.MaybeNoSuchOverloadErr(rhs)
	}
	remainder := lr
	switch {
	case remainder == nil:
		remainder = rr
	case rr != nil:
		remainder = types.MergeUnknowns(lr, rr)
	}
	known := append(append([]ref.Val{}, lk...), rk...)
	return &partialList{known: known, remainder: remainder}
}
