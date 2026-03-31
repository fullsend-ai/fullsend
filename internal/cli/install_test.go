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

func TestInstallCmd_WithRepo(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"install", "my-org", "--repo", "cool-project"})

	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInstallCmd_WithAgents(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"install", "my-org", "--agents", "review,implementation"})

	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInstallCmd_Help(t *testing.T) {
	cmd := newInstallCmd()

	assert.Equal(t, "install <org>", cmd.Use)
	assert.Contains(t, cmd.Long, "safe defaults")
	assert.Contains(t, cmd.Long, "Nothing gets automatically merged")
}

func TestCreateDemoClient(t *testing.T) {
	client := createDemoClient("org", []string{"my-repo"})

	assert.NotNil(t, client)
	// Should have the default repos plus the custom one
	assert.GreaterOrEqual(t, len(client.Repos), 7)

	// Find my-repo
	found := false
	for _, r := range client.Repos {
		if r.Name == "my-repo" {
			found = true
			break
		}
	}
	assert.True(t, found, "custom repo should be in the demo client")
}

func TestCreateDemoClient_NoDuplicates(t *testing.T) {
	// api-gateway is already in the default list
	client := createDemoClient("org", []string{"api-gateway"})

	count := 0
	for _, r := range client.Repos {
		if r.Name == "api-gateway" {
			count++
		}
	}
	assert.Equal(t, 1, count, "should not duplicate existing repos")
}
