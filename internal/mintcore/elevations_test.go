package mintcore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeRoleElevations_WorkflowChange(t *testing.T) {
	perms, err := MergeRoleElevations("coder", []string{"workflow-change"})
	require.NoError(t, err)
	assert.Equal(t, "write", perms["workflows"])
	assert.Equal(t, "write", perms["contents"])
}

func TestMergeRoleElevations_UnknownGate(t *testing.T) {
	_, err := MergeRoleElevations("coder", []string{"unknown"})
	require.Error(t, err)
}

func TestMergeRoleElevations_DisallowedRole(t *testing.T) {
	_, err := MergeRoleElevations("triage", []string{"workflow-change"})
	require.Error(t, err)
}

func TestMergeRoleElevations_NoElevations(t *testing.T) {
	perms, err := MergeRoleElevations("coder", nil)
	require.NoError(t, err)
	_, hasWorkflows := perms["workflows"]
	assert.False(t, hasWorkflows)
}
