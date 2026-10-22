package agentnew

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBasePolicy_IsTheTemplateAndACopy(t *testing.T) {
	want, err := templates.ReadFile("templates/policies/base.yaml")
	require.NoError(t, err)

	got := BasePolicy()
	assert.Equal(t, string(want), string(got))

	got[0] = 'X'
	assert.Equal(t, string(want), string(BasePolicy()), "a caller's edit must not change later results")
}
