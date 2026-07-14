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
