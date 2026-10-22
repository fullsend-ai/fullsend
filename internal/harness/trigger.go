package harness

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"

	"github.com/fullsend-ai/fullsend/internal/celtrigger"
)

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
// per-repo config (ADR 0088).
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
	activation := map[string]any{
		"event":   event,
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
