package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRootCommand_HasVersion(t *testing.T) {
	cmd := newRootCmd()
	assert.Equal(t, "dev", cmd.Version)
}

func TestRootCommand_HasAdminSubcommand(t *testing.T) {
	cmd := newRootCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Use == "admin" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected admin subcommand")
}

func TestRootCommand_SilencesUsageOnError(t *testing.T) {
	cmd := newRootCmd()
	assert.True(t, cmd.SilenceUsage)
	assert.True(t, cmd.SilenceErrors)
}
