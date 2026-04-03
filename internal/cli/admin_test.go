package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminCommand_HasSubcommands(t *testing.T) {
	cmd := newAdminCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Use] = true
	}
	assert.True(t, names["install <org>"], "expected install subcommand")
	assert.True(t, names["uninstall <org>"], "expected uninstall subcommand")
	assert.True(t, names["analyze <org>"], "expected analyze subcommand")
}

func TestInstallCmd_RequiresOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestInstallCmd_Flags(t *testing.T) {
	cmd := newInstallCmd()

	repoFlag := cmd.Flags().Lookup("repo")
	require.NotNil(t, repoFlag, "expected --repo flag")

	agentsFlag := cmd.Flags().Lookup("agents")
	require.NotNil(t, agentsFlag, "expected --agents flag")
	assert.Equal(t, "fullsend,triage,coder,review", agentsFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")

	skipAppSetupFlag := cmd.Flags().Lookup("skip-app-setup")
	require.NotNil(t, skipAppSetupFlag, "expected --skip-app-setup flag")
}

func TestUninstallCmd_RequiresOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "uninstall"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestUninstallCmd_Flags(t *testing.T) {
	cmd := newUninstallCmd()

	yoloFlag := cmd.Flags().Lookup("yolo")
	require.NotNil(t, yoloFlag, "expected --yolo flag")
}

func TestAnalyzeCmd_RequiresOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "analyze"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestValidateOrgName_Valid(t *testing.T) {
	valid := []string{"my-org", "org123", "A", "abc-def-ghi", "ORG"}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, validateOrgName(name))
		})
	}
}

func TestValidateOrgName_Invalid(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"", "cannot be empty"},
		{"-leading", "cannot start or end with a hyphen"},
		{"trailing-", "cannot start or end with a hyphen"},
		{"invalid@char", "invalid character"},
		{"has space", "invalid character"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOrgName(tc.name)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestResolveToken_EnvVar(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token-123")
	t.Setenv("GITHUB_TOKEN", "")

	token, err := resolveToken()
	require.NoError(t, err)
	assert.Equal(t, "test-token-123", token)
}

func TestResolveToken_GitHubTokenFallback(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "github-token-456")

	token, err := resolveToken()
	require.NoError(t, err)
	assert.Equal(t, "github-token-456", token)
}
