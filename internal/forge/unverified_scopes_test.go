package forge

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnverifiedScopes(t *testing.T) {
	err := &UnverifiedScopesError{Scopes: []string{"group top", "instance"}}
	assert.Equal(t, "could not inspect inherited variable scopes: group top, instance", err.Error())
	assert.Equal(t, []string{"group top", "instance"}, UnverifiedScopes(fmt.Errorf("wrapped: %w", err)))
	assert.Nil(t, UnverifiedScopes(errors.New("other")))
	assert.Nil(t, UnverifiedScopes(nil))
}
