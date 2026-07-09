package harness

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// NewTriggerEnv creates a CEL environment with root variable event (dyn type).
func NewTriggerEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("event", cel.DynType),
	)
}

// ValidateTriggerExpression compiles a harness trigger CEL expression.
// Empty trigger is valid (manual fullsend run only).
func ValidateTriggerExpression(expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}
	env, err := NewTriggerEnv()
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

// EvaluateTrigger evaluates a compiled trigger against event data (map form).
func EvaluateTrigger(expr string, event map[string]any) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false, nil
	}
	env, err := NewTriggerEnv()
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
