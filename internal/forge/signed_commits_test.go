package forge

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsSignedCommitsRequired(t *testing.T) {
	wrapped := fmt.Errorf("update ref: %w: %w: github api: 422", ErrBranchProtected, ErrSignedCommitsRequired)
	assert.True(t, IsSignedCommitsRequired(wrapped))
	assert.True(t, IsBranchProtected(wrapped), "a signature requirement is also a protection rejection")

	assert.False(t, IsSignedCommitsRequired(ErrBranchProtected))
	assert.False(t, IsSignedCommitsRequired(nil))
	assert.False(t, IsSignedCommitsRequired(fmt.Errorf("other")))
}
