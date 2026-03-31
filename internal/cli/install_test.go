package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallCmd_NoArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"install"})

	err := cmd.Execute()
	assert.Error(t, err, "install without args should fail")
}

func TestInstallCmd_DryRun(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"install", "my-org", "--dry-run"})

	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInstallCmd_NoToken(t *testing.T) {
	// Without a token, install should fail with a clear error
	t.Setenv("GITHUB_TOKEN", "")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"install", "my-org"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GITHUB_TOKEN not set")
}

func TestInstallCmd_Help(t *testing.T) {
	cmd := newInstallCmd()

	assert.Equal(t, "install <org>", cmd.Use)
	assert.Contains(t, cmd.Long, "safe defaults")
	assert.Contains(t, cmd.Long, "Nothing gets automatically merged")
	assert.Contains(t, cmd.Long, "GITHUB_TOKEN")
}
