package entitycel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/parser"

	"github.com/fullsend-ai/fullsend/internal/normentity"
)

// evalTimeout bounds the wall-clock time of one predicate evaluation, so a
// deeply nested comprehension over large collections cannot spin. It is far
// above any realistic entity-source predicate. A variable so tests can shrink
// it.
//
// A cel.CostLimit is not used: cel-go's runtime cost tracker assumes every
// Sizer reports a concrete integer size, which a partial list (whose size is
// unknown) deliberately does not.
var evalTimeout = 5 * time.Second

// interruptCheckFrequency is how many comprehension iterations run between
// checks of the evaluation context for cancellation.
const interruptCheckFrequency = 100

// Result is the three-valued result of an entity-source predicate. The zero
// value is Unknown so an unset result never reads as a definite answer.
type Result int

// Predicate results.
const (
	// Unknown: the evidence does not settle the predicate.
	Unknown Result = iota
	// False: every completion of the evidence makes the predicate false.
	False
	// True: every completion of the evidence makes the predicate true.
	True
)

func (r Result) String() string {
	switch r {
	case True:
		return "true"
	case False:
		return "false"
	}
	return "unknown"
}

// ErrMalformedEntity wraps a failure to bind the entity, such as an invalid
// coverage status. It is a data problem, distinct from a predicate that
// fails to compile or evaluate.
var ErrMalformedEntity = errors.New("entitycel: malformed entity")

// Internal functions introduced by the comprehension macros. Their names are
// not valid CEL identifiers, so predicates cannot call them directly.
const (
	fnKnown = "@entitycel_known"
	fnMerge = "@entitycel_merge"
)

// NewEnv creates the CEL environment for entity-source predicates: root
// variables entity (required) and event (null when no event prompted the
// evaluation), both dyn.
//
// Its comprehension macros (all, exists, exists_one, existsOne, map, filter)
// evaluate the comprehension twice when the range is a partial list: over
// the known members only, and over the known members followed by the unknown
// remainder. The result is kept only when both agree; otherwise it is
// unknown. A remainder may hold no members or several, so neither pass alone
// may decide the result.
func NewEnv() (*cel.Env, error) {
	t := cel.TypeParamType("T")
	return cel.NewEnv(
		cel.Variable(RootEntity, cel.DynType),
		cel.Variable(RootEvent, cel.DynType),
		cel.ClearMacros(),
		cel.Macros(
			parser.HasMacro,
			soundMacro(parser.AllMacro, parser.MakeAll),
			soundMacro(parser.ExistsMacro, parser.MakeExists),
			soundMacro(parser.ExistsOneMacro, parser.MakeExistsOne),
			soundMacro(parser.ExistsOneMacroNew, parser.MakeExistsOne),
			soundMacro(parser.MapMacro, parser.MakeMap),
			soundMacro(parser.MapFilterMacro, parser.MakeMap),
			soundMacro(parser.FilterMacro, parser.MakeFilter),
		),
		cel.Function(fnKnown,
			cel.Overload("entitycel_known_T", []*cel.Type{t}, t,
				cel.UnaryBinding(knownMembers))),
		cel.Function(fnMerge,
			cel.Overload("entitycel_merge_dyn_T_T", []*cel.Type{cel.DynType, t, t}, t,
				cel.FunctionBinding(mergeFold))),
	)
}

// maxMacroOperandNodes bounds the combined size, in AST nodes, of a
// comprehension macro's range and arguments. The expansion below copies the
// range into three places and the arguments into two, so chained or nested
// comprehensions would otherwise grow exponentially with nesting depth. The
// bound caps each expansion at a small constant multiple of this size, and
// therefore the compiled AST at a size linear in the number of macros.
const maxMacroOperandNodes = 5000

// exprSize returns the number of nodes in the expressions.
func exprSize(exprs ...ast.Expr) int {
	n := 0
	v := ast.NewExprVisitor(func(ast.Expr) { n++ })
	for _, e := range exprs {
		ast.PreOrderVisit(e, v)
	}
	return n
}

// soundMacro wraps a receiver comprehension macro so that
// range.m(args) expands to
// @entitycel_merge(range, m(@entitycel_known(range), args), m(range, args)).
// It rejects an expansion whose range and arguments together exceed
// [maxMacroOperandNodes].
func soundMacro(std parser.Macro, expand parser.MacroExpander) cel.Macro {
	return parser.NewReceiverMacro(std.Function(), std.ArgCount(),
		func(eh parser.ExprHelper, target ast.Expr, args []ast.Expr) (ast.Expr, *common.Error) {
			if exprSize(append([]ast.Expr{target}, args...)...) > maxMacroOperandNodes {
				return nil, eh.NewError(target.ID(), fmt.Sprintf(
					"%s() operands exceed %d expression nodes; simplify the predicate",
					std.Function(), maxMacroOperandNodes))
			}
			knownFold, err := expand(eh, eh.NewCall(fnKnown, eh.Copy(target)), copyExprs(eh, args))
			if err != nil {
				return nil, err
			}
			fullFold, err := expand(eh, eh.Copy(target), copyExprs(eh, args))
			if err != nil {
				return nil, err
			}
			return eh.NewCall(fnMerge, target, knownFold, fullFold), nil
		})
}

func copyExprs(eh parser.ExprHelper, exprs []ast.Expr) []ast.Expr {
	out := make([]ast.Expr, len(exprs))
	for i, e := range exprs {
		out[i] = eh.Copy(e)
	}
	return out
}

// knownMembers returns a partial list's known members as an ordinary list,
// and any other value unchanged.
func knownMembers(v ref.Val) ref.Val {
	if p, ok := v.(*partialList); ok {
		return p.knownList()
	}
	return v
}

// mergeFold keeps a comprehension result over a partial list only when the
// known-only and remainder passes agree. Unknown and error arguments never
// reach it: CEL returns them before calling a strict function.
func mergeFold(args ...ref.Val) ref.Val {
	if len(args) != 3 {
		return types.NewErr("no such overload")
	}
	p, ok := args[0].(*partialList)
	if !ok {
		return args[2]
	}
	if equal(args[1], args[2]) == types.True {
		return args[2]
	}
	return p.remainder
}

// compile compiles a predicate and checks that it is a boolean.
func compile(env *cel.Env, expr string) (*cel.Ast, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, fmt.Errorf("entity-source predicate is empty")
	}
	checked, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	if !checked.OutputType().IsExactType(types.BoolType) {
		return nil, fmt.Errorf("entity-source predicate must evaluate to bool, got %v", checked.OutputType())
	}
	return checked, nil
}

// Validate compiles an entity-source predicate and checks that it is a
// boolean. Unlike an event-only trigger, an empty predicate is invalid: an
// entity-source harness has no other routing rule.
func Validate(expr string) error {
	env, err := NewEnv()
	if err != nil {
		return fmt.Errorf("creating CEL env: %w", err)
	}
	_, err = compile(env, expr)
	return err
}

// Evaluate evaluates an entity-source predicate over the bound entity and an
// optional event (map form). A nil event binds `event` to CEL null, as for an
// eventless (scheduled or bound-run) evaluation.
//
// It returns True or False only when the known data settles the predicate,
// and Unknown when the result depends on unresolved fields or on members a
// partial collection may hold. Evaluation is bounded by an internal wall-clock
// timeout and stops when ctx is cancelled, returning an error in either case.
// A context that is already done on entry returns its error before any
// binding or compilation work; after that, both are checked only while
// comprehensions iterate, so a predicate with no comprehension is not
// interrupted mid-run. Callers must check ctx themselves after Evaluate
// returns. An error — a predicate that does not compile,
// a malformed entity (wrapping [ErrMalformedEntity]), or an evaluation error
// such as reading a field of a null event — is returned with Unknown and must
// never be read as false.
func Evaluate(ctx context.Context, expr string, entity *normentity.Entity, event map[string]any) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Unknown, err
	}
	bound, err := Bind(entity)
	if err != nil {
		return Unknown, fmt.Errorf("%w: %w", ErrMalformedEntity, err)
	}
	env, err := NewEnv()
	if err != nil {
		return Unknown, fmt.Errorf("creating CEL env: %w", err)
	}
	checked, err := compile(env, expr)
	if err != nil {
		return Unknown, err
	}
	prg, err := env.Program(checked,
		cel.CustomDecoratorV2(decorate),
		cel.InterruptCheckFrequency(interruptCheckFrequency))
	if err != nil {
		return Unknown, err
	}
	var ev any = types.NullValue
	if event != nil {
		ev = event
	}
	ctx, cancel := context.WithTimeout(ctx, evalTimeout)
	defer cancel()
	out, _, err := prg.ContextEval(ctx, map[string]any{RootEntity: bound, RootEvent: ev})
	if err != nil {
		return Unknown, err
	}
	switch v := out.(type) {
	case types.Bool:
		if v {
			return True, nil
		}
		return False, nil
	case *types.Unknown:
		return Unknown, nil
	}
	return Unknown, fmt.Errorf("entity-source predicate result is not bool: %v", out.Type())
}
