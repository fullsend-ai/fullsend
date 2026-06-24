package authorization

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGateByName(t *testing.T) {
	gate := GateByName("workflow-change")
	require.NotNil(t, gate)
	assert.Equal(t, "workflow-change-needed", gate.NeededLabel)
	assert.Equal(t, "workflow-change-allowed", gate.AllowedLabel)
	assert.Nil(t, GateByName("unknown"))
}

func TestGates(t *testing.T) {
	gates := Gates()
	require.Len(t, gates, 1)
	assert.Equal(t, "workflow-change", gates[0].Name)
}
