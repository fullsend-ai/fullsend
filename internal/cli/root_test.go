package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCmd_Help(t *testing.T) {
	cmd := newRootCmd()

	// Should have the install subcommand
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "install" {
			found = true
			break
		}
	}
	assert.True(t, found, "root command should have 'install' subcommand")
}

func TestRootCmd_Version(t *testing.T) {
	cmd := newRootCmd()
	assert.Equal(t, "dev", cmd.Version)
}

func TestRootCmd_NoArgs(t *testing.T) {
	cmd := newRootCmd()
	err := cmd.Execute()
	require.NoError(t, err)
}
