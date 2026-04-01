package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoCmd_Registered(t *testing.T) {
	cmd := newRootCmd()

	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "repo" {
			found = true
			break
		}
	}
	require.True(t, found, "root command should have 'repo' subcommand")
}

func TestRepoOnboardCmd_NoArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"repo", "onboard"})

	err := cmd.Execute()
	assert.Error(t, err, "repo onboard without args should fail")
}

func TestRepoOnboardCmd_NoOrg(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("GITHUB_OWNER", "")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"repo", "onboard", "my-repo"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "organization not specified")
}

func TestRepoOnboardCmd_Help(t *testing.T) {
	cmd := newRepoOnboardCmd()

	assert.Equal(t, "onboard <repository>", cmd.Use)
	assert.Contains(t, cmd.Long, "shim workflow")
}
