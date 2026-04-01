package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUninstallCmd_NoArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"uninstall"})

	err := cmd.Execute()
	assert.Error(t, err, "uninstall without args should fail")
}

func TestUninstallCmd_NoToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"uninstall", "my-org"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no GitHub token found")
}

func TestUninstallCmd_Help(t *testing.T) {
	cmd := newUninstallCmd()

	assert.Equal(t, "uninstall <org>", cmd.Use)
	assert.Contains(t, cmd.Long, "config.yaml")
	assert.Contains(t, cmd.Long, "--yolo")
}

func TestUninstallCmd_Registered(t *testing.T) {
	cmd := newRootCmd()

	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "uninstall" {
			found = true
			break
		}
	}
	require.True(t, found, "root command should have 'uninstall' subcommand")
}
