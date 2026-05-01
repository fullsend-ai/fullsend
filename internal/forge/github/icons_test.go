package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIconForRole(t *testing.T) {
	roles := []string{"fullsend", "triage", "coder", "review"}
	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			icon, ok := IconForRole(role)
			require.True(t, ok, "expected icon for role %q", role)
			assert.True(t, len(icon) > 100, "icon bytes should not be trivially small")
			// PNG magic bytes: \x89PNG\r\n\x1a\n
			assert.Equal(t, byte(0x89), icon[0], "should start with PNG magic byte")
			assert.Equal(t, byte('P'), icon[1])
			assert.Equal(t, byte('N'), icon[2])
			assert.Equal(t, byte('G'), icon[3])
		})
	}
}

func TestIconForRole_Unknown(t *testing.T) {
	icon, ok := IconForRole("unknown-agent")
	assert.False(t, ok)
	assert.Nil(t, icon)
}
