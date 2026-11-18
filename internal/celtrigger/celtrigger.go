// Package celtrigger compiles and evaluates agent trigger expressions: CEL
// booleans over a NormalizedEvent bound to the root variable event (ADR
// 0061). It has no fullsend dependencies, so both internal/config (an
// agents[].trigger entry) and internal/harness (a harness trigger: field)
// validate triggers the same way.
package celtrigger

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// NewEnv creates a CEL environment with root variable event (dyn type).
func NewEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("event", cel.DynType),
	)
}

// Validate compiles a trigger expression and checks that it is a boolean.
// An empty expression is valid (no trigger).
func Validate(expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}
	env, err := NewEnv()
	if err != nil {
		return fmt.Errorf("creating CEL env: %w", err)
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return issues.Err()
	}
	if !ast.OutputType().IsExactType(types.BoolType) {
		return fmt.Errorf("trigger expression must evaluate to bool, got %v", ast.OutputType())
	}
	return nil
}

// Evaluate evaluates a trigger expression against event data (map form). An
// empty expression never matches.
func Evaluate(expr string, event map[string]any) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false, nil
	}
	env, err := NewEnv()
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
	out, _, err := prg.Eval(map[string]any{"event": event})
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
		return false, fmt.Errorf("trigger result is not bool: %T", out)
	}
	return bool(b), nil
}
