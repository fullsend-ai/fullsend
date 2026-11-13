package e2eauth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveCollaboratorRole(t *testing.T) {
	role, err := ResolveCollaboratorRole("write", "write")
	require.NoError(t, err)
	assert.Equal(t, "write", role)

	role, err = ResolveCollaboratorRole("write", "read")
	require.NoError(t, err)
	assert.Equal(t, "none", role)

	role, err = ResolveCollaboratorRole("Developer", "write")
	require.NoError(t, err)
	assert.Equal(t, "write", role)

	_, err = ResolveCollaboratorRole("Developer", "")
	require.Error(t, err)
}
