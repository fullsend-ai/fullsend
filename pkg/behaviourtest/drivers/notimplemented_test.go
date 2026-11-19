package drivers

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotImplemented(t *testing.T) {
	t.Parallel()

	err := NotImplemented("forgejo SCM", "CreateIssue")
	require.ErrorIs(t, err, ErrNotImplemented)
	assert.Equal(t, "forgejo SCM driver: CreateIssue: not implemented", err.Error())

	var nie *NotImplementedError
	require.True(t, errors.As(err, &nie))
	assert.Equal(t, "forgejo SCM", nie.Driver)
	assert.Equal(t, "CreateIssue", nie.Operation)
}
