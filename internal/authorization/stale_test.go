package authorization

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNonCollaboratorAssociation(t *testing.T) {
	assert.True(t, IsNonCollaboratorAssociation("NONE"))
	assert.True(t, IsNonCollaboratorAssociation("read"))
	assert.False(t, IsNonCollaboratorAssociation("MEMBER"))
	assert.False(t, IsNonCollaboratorAssociation("OWNER"))
}
