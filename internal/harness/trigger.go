package harness

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"

	"github.com/fullsend-ai/fullsend/internal/celtrigger"
)

// trustedOverlayEventPaths lists the event field paths a guarded overlay's
// when: expression may read (ADR 0112 Decision 2). It mirrors the "Trusted
// fields" section of docs/normative/normalized-event/v1/README.md;
// TestTrustedOverlayEventPaths_MatchSpecDoc fails when the two drift.
var trustedOverlayEventPaths = map[string]bool{
	"event.repo":                            true,
	"event.source.system":                   true,
	"event.entity.source.system":            true,
	"event.actor.role":                      true,
	"event.state.change_proposal.base_repo": true,
	"event.state.change_proposal.is_fork":   true,
}

// overlayRequiredEventObjects are the top-level objects the normalized event
// schema always requires (normalized-event.schema.json, "required"). A guarded
// overlay may test has() on these and on a trusted field. A trusted field
// under an optional object (state.change_proposal) exists only when that
// object does, so has() on it, like any read of it, matches only on events
// that carry the object, and its negation never matches: the missing object
// is a missing-key error, which ResolveOverlays treats as a non-match. has()
// on the optional object itself is refused like any read of a parent object.
var overlayRequiredEventObjects = map[string]bool{
	"event.repo": true, "event.entity": true, "event.transition": true,
	"event.actor": true, "event.state": true, "event.source": true,
}

// overlayMissingKey reports whether err is cel-go's missing-map-key error,
// raised when a when: expression reads a key the event does not carry. Such
// an error is a non-match, not a failure. cel-go v0.30.0 builds it with the
// text "no such key: <key>" (interpreter/attributes.go, common/types/map.go)
// and exports no type for it, so each error in the unwrap chain, outermost
// first, is matched by prefix.
func overlayMissingKey(err error) bool {
	for err != nil {
		if strings.HasPrefix(err.Error(), "no such key") {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

// guardedOverlayWhenTrusted reports whether a compiled overlay when:
// expression reads only trusted facts: runtime.forge, config, the exact event
// paths in trustedOverlayEventPaths, `event == null` / `event != null`, and
// has() on a trusted field or on an object the schema always requires. Any
// other reference rooted at event is untrusted: an unlisted leaf, a parent
// object read as a value, indexed, passed to a function or iterated by a
// comprehension, or a select on a computed operand. So is any other
// identifier: runtime read as a value or under another field, and a variable
// this environment does not declare, unless a comprehension in the
// expression binds it. The first untrusted
// reference is returned so the validation error can name it (ADR 0112
// Decision 2).
func guardedOverlayWhenTrusted(checked *cel.Ast) (offending string, ok bool) {
	return walkTrustedOverlayExpr(checked.NativeRep().Expr(), nil)
}

// walkTrustedOverlayExpr reports whether e reads only trusted facts. When it
// does not, it returns the first offending reference and false. locals holds
// the comprehension variables in scope at e.
func walkTrustedOverlayExpr(e celast.Expr, locals map[string]bool) (string, bool) {
	switch e.Kind() {
	case celast.LiteralKind:
		return "", true
	case celast.IdentKind:
		name := e.AsIdent()
		switch {
		case isOverlayRootIdent(name, "config"), locals[name]:
			return "", true
		case isOverlayRootIdent(name, "event"), isOverlayRootIdent(name, "runtime"):
			return strings.TrimPrefix(name, "."), false
		}
		return name, false
	case celast.SelectKind:
		sel := e.AsSelect()
		if op := sel.Operand(); op.Kind() == celast.IdentKind && isOverlayRootIdent(op.AsIdent(), "runtime") && !locals[op.AsIdent()] {
			if sel.FieldName() == "forge" {
				return "", true
			}
			return "runtime." + sel.FieldName(), false
		}
		// runtime.forge is a string: a field under it is not a trusted fact.
		if op := sel.Operand(); op.Kind() == celast.SelectKind {
			if inner := op.AsSelect().Operand(); inner.Kind() == celast.IdentKind && isOverlayRootIdent(inner.AsIdent(), "runtime") && !locals[inner.AsIdent()] {
				return "runtime." + op.AsSelect().FieldName() + "." + sel.FieldName(), false
			}
		}
		if path := eventPath(e, locals); path != "" {
			if trustedOverlayEventPaths[path] {
				return "", true
			}
			if sel.IsTestOnly() && overlayRequiredEventObjects[path] {
				return "", true
			}
			return path, false
		}
		return walkTrustedOverlayExpr(sel.Operand(), locals)
	case celast.CallKind:
		call := e.AsCall()
		if isEventNullCheck(call) {
			return "", true
		}
		// The trusted facts are strings: indexing one reads nothing trusted.
		if call.FunctionName() == operators.Index && len(call.Args()) == 2 {
			if target := call.Args()[0]; target.Kind() == celast.SelectKind {
				if op := target.AsSelect().Operand(); op.Kind() == celast.IdentKind && isOverlayRootIdent(op.AsIdent(), "runtime") && !locals[op.AsIdent()] {
					return "runtime." + target.AsSelect().FieldName(), false
				}
				if path := eventPath(target, locals); trustedOverlayEventPaths[path] {
					return path, false
				}
			}
		}
		if call.IsMemberFunction() {
			if offending, ok := walkTrustedOverlayExpr(call.Target(), locals); !ok {
				return offending, false
			}
		}
		for _, arg := range call.Args() {
			if offending, ok := walkTrustedOverlayExpr(arg, locals); !ok {
				return offending, false
			}
		}
		return "", true
	case celast.ComprehensionKind:
		c := e.AsComprehension()
		for _, sub := range []celast.Expr{c.IterRange(), c.AccuInit()} {
			if offending, ok := walkTrustedOverlayExpr(sub, locals); !ok {
				return offending, false
			}
		}
		inner := maps.Clone(locals)
		if inner == nil {
			inner = map[string]bool{}
		}
		inner[c.IterVar()], inner[c.IterVar2()], inner[c.AccuVar()] = true, true, true
		for _, sub := range []celast.Expr{c.LoopCondition(), c.LoopStep(), c.Result()} {
			if offending, ok := walkTrustedOverlayExpr(sub, inner); !ok {
				return offending, false
			}
		}
		return "", true
	case celast.ListKind:
		for _, el := range e.AsList().Elements() {
			if offending, ok := walkTrustedOverlayExpr(el, locals); !ok {
				return offending, false
			}
		}
		return "", true
	case celast.MapKind:
		for _, entry := range e.AsMap().Entries() {
			me := entry.AsMapEntry()
			if offending, ok := walkTrustedOverlayExpr(me.Key(), locals); !ok {
				return offending, false
			}
			if offending, ok := walkTrustedOverlayExpr(me.Value(), locals); !ok {
				return offending, false
			}
		}
		return "", true
	case celast.StructKind:
		for _, field := range e.AsStruct().Fields() {
			if offending, ok := walkTrustedOverlayExpr(field.AsStructField().Value(), locals); !ok {
				return offending, false
			}
		}
		return "", true
	}
	// An expression kind this walker does not know is not proven trusted.
	return "unsupported expression", false
}

// isOverlayRootIdent reports whether a checked identifier names the overlay
// root variable root. cel-go v0.30.0's checker keeps the leading dot of a
// global reference (".event") when a comprehension variable of the same name
// shadows it (checker.checkIdent / resolveSimpleIdent), so both spellings
// must be treated as the root: otherwise `[0].exists(event, .event.title ==
// "x")` reads the real event while looking like a local.
func isOverlayRootIdent(name, root string) bool {
	return strings.TrimPrefix(name, ".") == root
}

// isEventNullCheck reports whether call is `event == null` or `event != null`,
// which ADR 0112 allows on a guarded overlay: it reveals only that an event
// exists.
func isEventNullCheck(call celast.CallExpr) bool {
	if call.FunctionName() != operators.Equals && call.FunctionName() != operators.NotEquals {
		return false
	}
	args := call.Args()
	isEvent := func(a celast.Expr) bool {
		return a.Kind() == celast.IdentKind && isOverlayRootIdent(a.AsIdent(), "event")
	}
	isNull := func(a celast.Expr) bool {
		return a.Kind() == celast.LiteralKind && a.AsLiteral().Type() == types.NullType
	}
	return (isEvent(args[0]) && isNull(args[1])) || (isNull(args[0]) && isEvent(args[1]))
}

// eventPath follows a chain of Select nodes down to an "event" Ident and
// returns the dotted path (e.g. "event.source.system"). It returns "" when
// the chain does not root at the event variable, including when "event" is a
// comprehension variable in locals (the global is then spelled ".event").
func eventPath(e celast.Expr, locals map[string]bool) string {
	var parts []string
	cur := e
	for cur.Kind() == celast.SelectKind {
		sel := cur.AsSelect()
		parts = append(parts, sel.FieldName())
		cur = sel.Operand()
	}
	if cur.Kind() != celast.IdentKind || !isOverlayRootIdent(cur.AsIdent(), "event") || locals[cur.AsIdent()] {
		return ""
	}
	slices.Reverse(parts)
	return "event." + strings.Join(parts, ".")
}

// NewTriggerEnv creates a CEL environment with root variable event (dyn type).
func NewTriggerEnv() (*cel.Env, error) {
	return celtrigger.NewEnv()
}

// NewOverlayEnv creates a CEL environment for overlay when expressions.
// It extends the trigger environment with runtime and config variables
// (ADR 0088). runtime.forge is the effective forge platform; config is
// the per-repo config from config.yaml.
func NewOverlayEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("event", cel.DynType),
		cel.Variable("runtime", cel.DynType),
		cel.Variable("config", cel.DynType),
	)
}

// ValidateTriggerExpression compiles a harness trigger CEL expression.
// Empty trigger is valid (manual fullsend run only).
func ValidateTriggerExpression(expr string) error {
	return celtrigger.Validate(expr)
}

// EvaluateTrigger evaluates a compiled trigger against event data (map form).
func EvaluateTrigger(expr string, event map[string]any) (bool, error) {
	return celtrigger.Evaluate(expr, event)
}

// EvaluateOverlay evaluates an overlay when expression against the overlay
// CEL environment: event data, runtime context (forge platform), and
// per-repo config (ADR 0088). A nil event is bound as CEL null, so
// `event != null` is false without an event, `has(event.x)` is false, and
// selecting an event field or using event as a map (`event.x`,
// `size(event)`, `"k" in event`) is an evaluation error, which
// ResolveOverlays treats as a non-match.
func EvaluateOverlay(expr string, event map[string]any, forgePlatform string, config map[string]any) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false, nil
	}
	env, err := NewOverlayEnv()
	if err != nil {
		return false, err
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return false, issues.Err()
	}
	prg, err := env.Program(ast)
	if err != nil {
		return false, err
	}
	if config == nil {
		config = map[string]any{}
	}
	var eventVal any = types.NullValue
	if event != nil {
		eventVal = event
	}
	activation := map[string]any{
		"event":   eventVal,
		"runtime": map[string]any{"forge": forgePlatform},
		"config":  config,
	}
	out, _, err := prg.Eval(activation)
	if err != nil {
		return false, err
	}
	b, ok := out.(types.Bool)
	if !ok {
		if br, ok := out.(ref.Val); ok {
			if b, ok := br.Value().(bool); ok {
				return b, nil
			}
		}
		return false, fmt.Errorf("overlay when result is not bool: %T", out)
	}
	return bool(b), nil
}
