package celtrigger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	require.NoError(t, Validate(""))
	require.NoError(t, Validate("  "))
	require.NoError(t, Validate(`event.transition.kind == "opened"`))
	require.ErrorContains(t, Validate(`event.transition.kind ==`), "Syntax error")
	require.ErrorContains(t, Validate(`"opened"`), "must evaluate to bool")
}

func TestEvaluate(t *testing.T) {
	event := map[string]any{"transition": map[string]any{"kind": "opened"}}

	ok, err := Evaluate(`event.transition.kind == "opened"`, event)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = Evaluate(`event.transition.kind == "closed"`, event)
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = Evaluate("", event)
	require.NoError(t, err)
	assert.False(t, ok, "an empty trigger never matches")

	_, err = Evaluate(`event.transition.kind ==`, event)
	require.Error(t, err)

	_, err = Evaluate(`event.missing.field == "x"`, event)
	require.Error(t, err, "a missing field is an evaluation error")
}
