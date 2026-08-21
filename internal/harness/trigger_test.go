package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTriggerExpression(t *testing.T) {
	assert.NoError(t, ValidateTriggerExpression(""))
	assert.NoError(t, ValidateTriggerExpression(`event.entity.kind == "work_item"`))
	assert.Error(t, ValidateTriggerExpression(`event.entity.kind ==`))
	assert.Error(t, ValidateTriggerExpression(`"not a bool"`))
}

func TestEvaluateTrigger(t *testing.T) {
	expr := `event.entity.kind == "work_item" && event.transition.label.name == "ready-for-ping"`
	event := map[string]any{
		"entity": map[string]any{
			"kind": "work_item",
		},
		"transition": map[string]any{
			"label": map[string]any{
				"name": "ready-for-ping",
			},
		},
	}
	ok, err := EvaluateTrigger(expr, event)
	require.NoError(t, err)
	assert.True(t, ok)

	event["entity"] = map[string]any{"kind": "change_proposal"}
	ok, err = EvaluateTrigger(expr, event)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_RuntimeForge(t *testing.T) {
	event := map[string]any{"source": map[string]any{"system": "github"}}
	ok, err := EvaluateOverlay(`runtime.forge == "github"`, event, "github", nil)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = EvaluateOverlay(`runtime.forge == "github"`, event, "gitlab", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_EventField(t *testing.T) {
	event := map[string]any{"source": map[string]any{"system": "jira"}}
	ok, err := EvaluateOverlay(`event.source.system == "jira"`, event, "github", nil)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEvaluateOverlay_ConfigVariable(t *testing.T) {
	event := map[string]any{}
	config := map[string]any{"tracker": "jira"}
	ok, err := EvaluateOverlay(`config.tracker == "jira"`, event, "", config)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEvaluateOverlay_EmptyExpression(t *testing.T) {
	ok, err := EvaluateOverlay("", nil, "", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_WhitespaceExpression(t *testing.T) {
	ok, err := EvaluateOverlay("   ", nil, "", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_InvalidCEL(t *testing.T) {
	_, err := EvaluateOverlay(`runtime.forge ==`, nil, "", nil)
	require.Error(t, err)
}

func TestEvaluateOverlay_NonBoolResult(t *testing.T) {
	_, err := EvaluateOverlay(`"not a bool"`, nil, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not bool")
}

func TestEvaluateOverlay_NilConfig(t *testing.T) {
	event := map[string]any{}
	// nil config should be substituted with empty map
	ok, err := EvaluateOverlay(`runtime.forge == "github"`, event, "github", nil)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEvaluateOverlay_HasGuard(t *testing.T) {
	// has() guard should work for missing keys in empty events
	event := map[string]any{}
	ok, err := EvaluateOverlay(`has(event.source) && event.source.system == "jira"`, event, "", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_CombinedExpression(t *testing.T) {
	event := map[string]any{"source": map[string]any{"system": "jira"}}
	ok, err := EvaluateOverlay(
		`event.source.system == "jira" && runtime.forge == "github"`,
		event, "github", nil,
	)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestNewOverlayEnv(t *testing.T) {
	env, err := NewOverlayEnv()
	require.NoError(t, err)
	require.NotNil(t, env)
}
