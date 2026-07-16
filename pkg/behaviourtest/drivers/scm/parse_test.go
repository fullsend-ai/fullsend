package scm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRepo(t *testing.T) {
	t.Parallel()

	owner, repo, err := ParseRepo("acme/widget")
	require.NoError(t, err)
	assert.Equal(t, "acme", owner)
	assert.Equal(t, "widget", repo)

	_, _, err = ParseRepo("invalid")
	require.Error(t, err)
}
