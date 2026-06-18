package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestAdminCommand_HasSubcommands(t *testing.T) {
	cmd := newAdminCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Use] = true
	}
	assert.True(t, names["install <org-or-owner/repo>"], "expected install subcommand")
	assert.True(t, names["uninstall <org>"], "expected uninstall subcommand")
	assert.True(t, names["analyze <org>"], "expected analyze subcommand")
	assert.True(t, names["enable"], "expected enable subcommand")
	assert.True(t, names["disable"], "expected disable subcommand")
	assert.False(t, names["init <owner/repo>"], "init subcommand should not exist — merged into install")
}

func TestInstallCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestInstallCmd_Flags(t *testing.T) {
	cmd := newInstallCmd()

	agentsFlag := cmd.Flags().Lookup("agents")
	require.NotNil(t, agentsFlag, "expected --agents flag")
	assert.Equal(t, strings.Join(config.DefaultAgentRoles(), ","), agentsFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")

	skipAppSetupFlag := cmd.Flags().Lookup("skip-app-setup")
	require.NotNil(t, skipAppSetupFlag, "expected --skip-app-setup flag")

	vendorFlag := cmd.Flags().Lookup("vendor")
	require.NotNil(t, vendorFlag, "expected --vendor flag")
	assert.Equal(t, "false", vendorFlag.DefValue)

	inferenceProjectFlag := cmd.Flags().Lookup("inference-project")
	require.NotNil(t, inferenceProjectFlag, "expected --inference-project flag")

	inferenceRegionFlag := cmd.Flags().Lookup("inference-region")
	require.NotNil(t, inferenceRegionFlag, "expected --inference-region flag")
	assert.Equal(t, "global", inferenceRegionFlag.DefValue)

	inferenceWIFProviderFlag := cmd.Flags().Lookup("inference-wif-provider")
	require.NotNil(t, inferenceWIFProviderFlag, "expected --inference-wif-provider flag")

	// Old GCP flags should have been removed.
	assert.Nil(t, cmd.Flags().Lookup("gcp-project"), "--gcp-project flag should have been renamed to --inference-project")
	assert.Nil(t, cmd.Flags().Lookup("gcp-region"), "--gcp-region flag should have been renamed to --inference-region")
	assert.Nil(t, cmd.Flags().Lookup("gcp-wif-provider"), "--gcp-wif-provider flag should have been renamed to --inference-wif-provider")

	// --gcp-wif-sa-email removed (direct WIF, no intermediate SA)
	wifSAEmailFlag := cmd.Flags().Lookup("gcp-wif-sa-email")
	assert.Nil(t, wifSAEmailFlag, "--gcp-wif-sa-email flag should have been removed")

	// --repo flag should not exist (issue #495)
	repoFlag := cmd.Flags().Lookup("repo")
	assert.Nil(t, repoFlag, "--repo flag should have been removed")

	mintProviderFlag := cmd.Flags().Lookup("mint-provider")
	require.NotNil(t, mintProviderFlag, "expected --mint-provider flag")
	assert.Equal(t, "gcf", mintProviderFlag.DefValue)

	mintProjectFlag := cmd.Flags().Lookup("mint-project")
	require.NotNil(t, mintProjectFlag, "expected --mint-project flag")

	mintRegionFlag := cmd.Flags().Lookup("mint-region")
	require.NotNil(t, mintRegionFlag, "expected --mint-region flag")
	assert.Equal(t, "us-central1", mintRegionFlag.DefValue)

	mintSourceDirFlag := cmd.Flags().Lookup("mint-source-dir")
	require.NotNil(t, mintSourceDirFlag, "expected --mint-source-dir flag")

	mintURLFlag := cmd.Flags().Lookup("mint-url")
	require.NotNil(t, mintURLFlag, "expected --mint-url flag")
	assert.Equal(t, DefaultMintURL, mintURLFlag.DefValue)

	// --gcp-auth-mode removed (WIF is the only mode)
	gcpAuthModeFlag := cmd.Flags().Lookup("gcp-auth-mode")
	assert.Nil(t, gcpAuthModeFlag, "--gcp-auth-mode flag should have been removed")

	// --scaffold-customized removed (customized dirs always included)
	scaffoldCustomizedFlag := cmd.Flags().Lookup("scaffold-customized")
	assert.Nil(t, scaffoldCustomizedFlag, "--scaffold-customized flag should have been removed")

	skipMintCheckFlag := cmd.Flags().Lookup("skip-mint-check")
	require.NotNil(t, skipMintCheckFlag, "expected --skip-mint-check flag")
	assert.Equal(t, "false", skipMintCheckFlag.DefValue)

	skipMintDeployFlag := cmd.Flags().Lookup("skip-mint-deploy")
	require.NotNil(t, skipMintDeployFlag, "expected --skip-mint-deploy flag")

	appSetFlag := cmd.Flags().Lookup("app-set")
	require.NotNil(t, appSetFlag, "expected --app-set flag")
	assert.Equal(t, "fullsend-ai", appSetFlag.DefValue)
}

func TestInstallCmd_InvalidAppSet(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "myorg",
		"--inference-project", "proj", "--mint-project", "proj",
		"--app-set", "INVALID"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --app-set")
}

func TestInstallCmd_PerRepoRequiresMintProjectWithoutDefaultURL(t *testing.T) {
	cmd := newRootCmd()
	// When --mint-url is explicitly cleared, --mint-project is required.
	cmd.SetArgs([]string{"admin", "install", "acme/widget", "--mint-url", ""})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-project")
}

func TestInstallCmd_PerRepoDefaultMintURLSkipsMintProject(t *testing.T) {
	cmd := newRootCmd()
	// With the default mint URL, --mint-project is not required.
	// The error should be about --inference-project, not --mint-project.
	cmd.SetArgs([]string{"admin", "install", "acme/widget"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-project is required for per-repo installation")
}

func TestInstallCmd_PerRepoRequiresInferenceProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget", "--mint-url", "https://mint-test-abc123.run.app"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-project is required for per-repo installation")
}

func TestInstallCmd_PerRepoRejectsInvalidFormat(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/", "--mint-url", "https://mint-test-abc123.run.app", "--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo must be in owner/repo format")
}

func TestInstallCmd_PerRepoRejectsMultiSlash(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/team/repo", "--mint-url", "https://mint-test-abc123.run.app", "--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid repo name")
}

func TestInstallCmd_PerRepoRejectsNonHTTPSMintURL(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget", "--mint-url", "http://mint.example.com", "--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-url must be a valid HTTPS URL")
}

func TestInstallCmd_PerRepoRejectsNonCloudRunMintURL(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget", "--mint-url", "https://evil.example.com", "--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-url must be a Cloud Run URL")
}

func TestInstallCmd_PerRepoRejectsPerOrgFlags(t *testing.T) {
	perOrgOnly := []struct {
		flag  string
		value string
	}{
		{"enroll-all", ""},
		{"enroll-none", ""},
	}
	for _, tc := range perOrgOnly {
		t.Run(tc.flag, func(t *testing.T) {
			cmd := newRootCmd()
			args := []string{"admin", "install", "acme/widget",
				"--mint-url", "https://mint-test-abc123.run.app",
				"--inference-project", "my-project"}
			if tc.value != "" {
				args = append(args, "--"+tc.flag, tc.value)
			} else {
				args = append(args, "--"+tc.flag)
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("--%s is only valid for per-org installation", tc.flag))
		})
	}
}

func TestInstallCmd_PerRepoAcceptsSharedFlags(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	sharedFlags := []struct {
		flag  string
		value string
	}{
		{"public", ""},
		{"skip-app-setup", ""},
		{"mint-provider", "gcf"},
		{"mint-source-dir", "/tmp/src"},
		{"skip-mint-deploy", ""},
		{"app-set", "custom-prefix"},
		{"vendor", ""},
	}
	for _, tc := range sharedFlags {
		t.Run(tc.flag, func(t *testing.T) {
			cmd := newRootCmd()
			args := []string{"admin", "install", "acme/widget",
				"--mint-url", "https://mint-test-abc123.run.app",
				"--inference-project", "my-project",
				"--dry-run"}
			if tc.value != "" {
				args = append(args, "--"+tc.flag, tc.value)
			} else {
				args = append(args, "--"+tc.flag)
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			require.NoError(t, err, "--%s should be accepted in per-repo mode", tc.flag)
		})
	}
}

func TestInstallCmd_ForceMintDeployFlagRemoved(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme",
		"--force-mint-deploy",
		"--inference-project", "my-project",
		"--mint-project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "force-mint-deploy")
}

func TestInstallCmd_PerRepoAcceptsMintRegion(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-region", "us-central1",
		"--inference-project", "my-project",
		"--mint-region", "europe-west1",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestParseAgentRoles(t *testing.T) {
	tests := []struct {
		input   string
		want    []string
		wantErr bool
	}{
		{"triage,review,coder", []string{"triage", "review", "coder"}, false},
		{" triage , review ", []string{"triage", "review"}, false},
		{"", nil, false},
		{"single", []string{"single"}, false},
		{"Invalid", nil, true},
		{"ok,BAD-role", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseAgentRoles(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid role name")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
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

	appSetFlag := cmd.Flags().Lookup("app-set")
	require.NotNil(t, appSetFlag, "expected --app-set flag")
	assert.Equal(t, "fullsend-ai", appSetFlag.DefValue)
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

func TestValidateEnabledRepos_AllValid(t *testing.T) {
	err := validateEnabledRepos(
		[]string{"web-app", "api-server"},
		[]string{"web-app", "api-server", "docs"},
	)
	assert.NoError(t, err)
}

func TestValidateEnabledRepos_NoRepoFlag(t *testing.T) {
	err := validateEnabledRepos(nil, []string{"web-app", "docs"})
	assert.NoError(t, err)
}

func TestValidateEnabledRepos_MissingOne(t *testing.T) {
	err := validateEnabledRepos(
		[]string{"integration-service"},
		[]string{"web-app", "docs"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integration-service")
	assert.Contains(t, err.Error(), "forks, archived, or misspelled")
}

func TestValidateEnabledRepos_MultipleMissing(t *testing.T) {
	err := validateEnabledRepos(
		[]string{"web-app", "fork-repo", "archived-repo"},
		[]string{"web-app", "docs"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fork-repo")
	assert.Contains(t, err.Error(), "archived-repo")
	// web-app is valid, should not appear in the error.
	assert.NotContains(t, err.Error(), "web-app")
}

func TestValidateEnabledRepos_EmptyDiscovered(t *testing.T) {
	err := validateEnabledRepos(
		[]string{"some-repo"},
		[]string{},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "some-repo")
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

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestEnsureConfigRepoExists_CreatesWhenMissing(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := ensureConfigRepoExists(context.Background(), client, printer, "myorg")
	require.NoError(t, err)
	require.Len(t, client.CreatedRepos, 1)
	assert.Equal(t, ".fullsend", client.CreatedRepos[0].Name)
	assert.False(t, client.CreatedRepos[0].Private)
}

func TestEnsureConfigRepoExists_NoOpWhenExists(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: ".fullsend", FullName: "myorg/.fullsend"},
	}
	printer := ui.New(&discardWriter{})

	err := ensureConfigRepoExists(context.Background(), client, printer, "myorg")
	require.NoError(t, err)
	assert.Empty(t, client.CreatedRepos)
}

func TestEnsureConfigRepoExists_ReturnsError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["GetRepo"] = fmt.Errorf("network error")
	printer := ui.New(&discardWriter{})

	err := ensureConfigRepoExists(context.Background(), client, printer, "myorg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking for config repo")
}

func TestEnableCommand_HasReposSubcommand(t *testing.T) {
	cmd := newEnableCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["repos"], "expected repos subcommand")
}

func TestDisableCommand_HasReposSubcommand(t *testing.T) {
	cmd := newDisableCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["repos"], "expected repos subcommand")
}

func TestReposEnableCmd_RequiresOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "enable", "repos"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least 1 arg")
}

func TestReposEnableCmd_RequiresReposOrAllFlag(t *testing.T) {
	cmd := newRootCmd()
	// Set GH_TOKEN to avoid token resolution error.
	t.Setenv("GH_TOKEN", "test-token")
	cmd.SetArgs([]string{"admin", "enable", "repos", "testorg"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must specify repository names or use --all flag")
}

func TestReposEnableCmd_HasAllFlag(t *testing.T) {
	cmd := newEnableReposCmd()
	allFlag := cmd.Flags().Lookup("all")
	require.NotNil(t, allFlag, "expected --all flag")
	assert.Equal(t, "false", allFlag.DefValue)
}

func TestReposDisableCmd_RequiresOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "disable", "repos"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least 1 arg")
}

func TestReposDisableCmd_RequiresReposOrAllFlag(t *testing.T) {
	cmd := newRootCmd()
	// Set GH_TOKEN to avoid token resolution error.
	t.Setenv("GH_TOKEN", "test-token")
	cmd.SetArgs([]string{"admin", "disable", "repos", "testorg"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must specify repository names or use --all flag")
}

func TestReposDisableCmd_HasAllFlag(t *testing.T) {
	cmd := newDisableReposCmd()
	allFlag := cmd.Flags().Lookup("all")
	require.NotNil(t, allFlag, "expected --all flag")
	assert.Equal(t, "false", allFlag.DefValue)
}

func TestReposEnableCmd_AllIgnoresPositionalArgs(t *testing.T) {
	// When --all is set, positional repo arguments are ignored
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
		"api":     false,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app", "api"})
	printer := ui.New(&discardWriter{})

	// Pass "web-app" as a positional arg, but --all should ignore it and enable both repos
	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, true, true)
	require.NoError(t, err)

	// Verify both repos were enabled (--all behavior), not just web-app
	require.Len(t, client.CreatedFiles, 1)
	updatedCfg, err := config.ParseOrgConfig(client.CreatedFiles[0].Content)
	require.NoError(t, err)
	assert.True(t, updatedCfg.Repos["web-app"].Enabled)
	assert.True(t, updatedCfg.Repos["api"].Enabled)
}

func TestReposDisableCmd_AllIgnoresPositionalArgs(t *testing.T) {
	// When --all is set, positional repo arguments are ignored
	cfg := setupTestConfig(map[string]bool{
		"web-app": true,
		"api":     true,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app", "api"})
	printer := ui.New(&discardWriter{})

	// Pass "web-app" as a positional arg, but --all should ignore it and disable both repos
	err := runDisableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, true, true)
	require.NoError(t, err)

	// Verify both repos were disabled (--all behavior), not just web-app
	require.Len(t, client.CreatedFiles, 1)
	updatedCfg, err := config.ParseOrgConfig(client.CreatedFiles[0].Content)
	require.NoError(t, err)
	assert.False(t, updatedCfg.Repos["web-app"].Enabled)
	assert.False(t, updatedCfg.Repos["api"].Enabled)
}

// Test helpers

func setupTestConfig(repos map[string]bool) *config.OrgConfig {
	repoNames := make([]string, 0, len(repos))
	enabledRepos := make([]string, 0)
	for name, enabled := range repos {
		repoNames = append(repoNames, name)
		if enabled {
			enabledRepos = append(enabledRepos, name)
		}
	}
	// Sort to ensure deterministic order despite map iteration being non-deterministic.
	sort.Strings(repoNames)
	sort.Strings(enabledRepos)
	return config.NewOrgConfig(repoNames, enabledRepos, []string{"triage"}, nil, "", "")
}

func setupTestClient(org string, cfg *config.OrgConfig, orgRepos []string) *forge.FakeClient {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: ".fullsend", FullName: org + "/.fullsend"},
	}
	for _, name := range orgRepos {
		client.Repos = append(client.Repos, forge.Repository{
			Name:     name,
			FullName: org + "/" + name,
		})
	}
	if cfg != nil {
		cfgData, _ := cfg.Marshal()
		client.FileContents[org+"/.fullsend/config.yaml"] = cfgData
	}
	// Fail the workflow dispatch so unit tests skip awaitRepoMaintenance
	// (which would poll for 3 minutes against an empty fake).
	client.Errors["DispatchWorkflow"] = fmt.Errorf("fake: dispatch not configured")
	return client
}

// Business logic tests for runEnableRepos

func TestRunEnableRepos_EnableSingleRepo(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
		"api":     false,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app", "api"})
	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.NoError(t, err)

	// Verify config was updated.
	require.Len(t, client.CreatedFiles, 1)
	updatedCfg, err := config.ParseOrgConfig(client.CreatedFiles[0].Content)
	require.NoError(t, err)
	assert.True(t, updatedCfg.Repos["web-app"].Enabled)
	assert.False(t, updatedCfg.Repos["api"].Enabled)
}

func TestRunEnableRepos_EnableMultipleRepos(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
		"api":     false,
		"docs":    false,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app", "api", "docs"})
	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app", "docs"}, false, true)
	require.NoError(t, err)

	// Verify config was updated.
	require.Len(t, client.CreatedFiles, 1)
	updatedCfg, err := config.ParseOrgConfig(client.CreatedFiles[0].Content)
	require.NoError(t, err)
	assert.True(t, updatedCfg.Repos["web-app"].Enabled)
	assert.True(t, updatedCfg.Repos["docs"].Enabled)
	assert.False(t, updatedCfg.Repos["api"].Enabled)
}

func TestRunEnableRepos_EnableAllRepos(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
		"api":     false,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app", "api", "new-repo"})
	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", nil, true, true)
	require.NoError(t, err)

	// Verify all repos were enabled (excluding .fullsend).
	require.Len(t, client.CreatedFiles, 1)
	updatedCfg, err := config.ParseOrgConfig(client.CreatedFiles[0].Content)
	require.NoError(t, err)
	assert.True(t, updatedCfg.Repos["web-app"].Enabled)
	assert.True(t, updatedCfg.Repos["api"].Enabled)
	assert.True(t, updatedCfg.Repos["new-repo"].Enabled)
	// .fullsend should not be in repos map.
	_, hasFullsend := updatedCfg.Repos[".fullsend"]
	assert.False(t, hasFullsend)
}

func TestRunEnableRepos_NoOpWhenAlreadyEnabled(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": true,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app"})
	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.NoError(t, err)

	// Verify no file was created (no changes).
	assert.Empty(t, client.CreatedFiles)
}

func TestRunEnableRepos_ErrorWhenFullsendRepoMissing(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".fullsend repository not found")
}

func TestRunEnableRepos_ErrorWhenConfigMissing(t *testing.T) {
	client := setupTestClient("testorg", nil, []string{"web-app"})
	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config.yaml")
}

func TestRunEnableRepos_ErrorWhenEnablingFullsend(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app"})
	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{".fullsend"}, false, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot enable .fullsend repository")
}

func TestRunEnableRepos_ErrorWhenRepoNotFound(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app"})
	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"nonexistent"}, false, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repository nonexistent not found")
}

func TestRunEnableRepos_CommitMessageFormat(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
		"api":     false,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app", "api"})
	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app", "api"}, false, true)
	require.NoError(t, err)

	require.Len(t, client.CreatedFiles, 1)
	assert.Contains(t, client.CreatedFiles[0].Message, "chore: enable 2 repositories")
}

func TestRunEnableRepos_UpdatesOrgVariableVisibility(t *testing.T) {
	// Setup: initial config with repo A enabled, repo B disabled.
	// Dispatch mode is oidc-mint. Org variable FULLSEND_MINT_URL exists.
	cfg := setupTestConfig(map[string]bool{
		"web-app": true,
		"api":     false,
	})
	cfg.Dispatch.Mode = "oidc-mint"

	client := setupTestClient("testorg", cfg, []string{"web-app", "api"})
	// Assign repo IDs so we can verify they appear in the variable visibility.
	for i := range client.Repos {
		switch client.Repos[i].Name {
		case ".fullsend":
			client.Repos[i].ID = 100
		case "web-app":
			client.Repos[i].ID = 200
		case "api":
			client.Repos[i].ID = 300
		}
	}
	// Pre-populate the org variable so it "exists".
	client.OrgVariables = map[string]bool{"testorg/FULLSEND_MINT_URL": true}
	client.OrgVariableValues = map[string]string{"testorg/FULLSEND_MINT_URL": "https://mint.example.com"}

	printer := ui.New(&discardWriter{})

	// Action: enable repo "api".
	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"api"}, false, true)
	require.NoError(t, err)

	// Assert: SetOrgVariableRepos was called with both enrolled repo IDs
	// plus the config repo (.fullsend).
	require.Contains(t, client.OrgVariableRepoIDs, "testorg/FULLSEND_MINT_URL")
	repoIDs := client.OrgVariableRepoIDs["testorg/FULLSEND_MINT_URL"]
	assert.Contains(t, repoIDs, int64(100), "config repo ID should be included")
	assert.Contains(t, repoIDs, int64(200), "web-app repo ID should be included")
	assert.Contains(t, repoIDs, int64(300), "api repo ID should be included")
}

func TestRunEnableRepos_SkipsVariableSyncWhenNotOIDCMint(t *testing.T) {
	// When dispatch mode is not oidc-mint, variable sync should be skipped.
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
	})
	// No dispatch mode set (empty string).

	client := setupTestClient("testorg", cfg, []string{"web-app"})
	client.OrgVariables = map[string]bool{"testorg/FULLSEND_MINT_URL": true}

	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.NoError(t, err)

	// SetOrgVariableRepos should not have been called.
	assert.Nil(t, client.OrgVariableRepoIDs)
}

func TestRunEnableRepos_VariableSyncErrorDoesNotBlockEnable(t *testing.T) {
	// When SetOrgVariableRepos fails, the enable command should still
	// succeed (best-effort contract).
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
	})
	cfg.Dispatch.Mode = "oidc-mint"

	client := setupTestClient("testorg", cfg, []string{"web-app"})
	for i := range client.Repos {
		switch client.Repos[i].Name {
		case ".fullsend":
			client.Repos[i].ID = 100
		case "web-app":
			client.Repos[i].ID = 200
		}
	}
	client.OrgVariables = map[string]bool{"testorg/FULLSEND_MINT_URL": true}
	client.Errors["SetOrgVariableRepos"] = fmt.Errorf("API rate limit exceeded")

	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.NoError(t, err, "enable should succeed even when variable sync fails")
}

func TestRunEnableRepos_SkipsVariableSyncWhenVariableNotExists(t *testing.T) {
	// When the org variable doesn't exist yet (mint not provisioned),
	// sync should skip gracefully.
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
	})
	cfg.Dispatch.Mode = "oidc-mint"

	client := setupTestClient("testorg", cfg, []string{"web-app"})
	// No OrgVariables set — FULLSEND_MINT_URL doesn't exist.

	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.NoError(t, err)

	// SetOrgVariableRepos should not have been called.
	assert.Nil(t, client.OrgVariableRepoIDs)
}

// Business logic tests for runDisableRepos

func TestRunDisableRepos_DisableSingleRepo(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": true,
		"api":     true,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app", "api"})
	printer := ui.New(&discardWriter{})

	err := runDisableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.NoError(t, err)

	// Verify config was updated.
	require.Len(t, client.CreatedFiles, 1)
	updatedCfg, err := config.ParseOrgConfig(client.CreatedFiles[0].Content)
	require.NoError(t, err)
	assert.False(t, updatedCfg.Repos["web-app"].Enabled)
	assert.True(t, updatedCfg.Repos["api"].Enabled)
}

func TestRunDisableRepos_DisableMultipleRepos(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": true,
		"api":     true,
		"docs":    true,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app", "api", "docs"})
	printer := ui.New(&discardWriter{})

	err := runDisableRepos(context.Background(), client, printer, "testorg", []string{"web-app", "docs"}, false, true)
	require.NoError(t, err)

	// Verify config was updated.
	require.Len(t, client.CreatedFiles, 1)
	updatedCfg, err := config.ParseOrgConfig(client.CreatedFiles[0].Content)
	require.NoError(t, err)
	assert.False(t, updatedCfg.Repos["web-app"].Enabled)
	assert.False(t, updatedCfg.Repos["docs"].Enabled)
	assert.True(t, updatedCfg.Repos["api"].Enabled)
}

func TestRunDisableRepos_DisableAllRepos(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": true,
		"api":     true,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app", "api"})
	printer := ui.New(&discardWriter{})

	err := runDisableRepos(context.Background(), client, printer, "testorg", nil, true, true)
	require.NoError(t, err)

	// Verify all repos were disabled.
	require.Len(t, client.CreatedFiles, 1)
	updatedCfg, err := config.ParseOrgConfig(client.CreatedFiles[0].Content)
	require.NoError(t, err)
	assert.False(t, updatedCfg.Repos["web-app"].Enabled)
	assert.False(t, updatedCfg.Repos["api"].Enabled)
}

func TestRunDisableRepos_NoOpWhenAlreadyDisabled(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app"})
	printer := ui.New(&discardWriter{})

	err := runDisableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.NoError(t, err)

	// Verify no file was created (no changes).
	assert.Empty(t, client.CreatedFiles)
}

func TestRunDisableRepos_ErrorWhenFullsendRepoMissing(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runDisableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".fullsend repository not found")
}

func TestRunDisableRepos_ErrorWhenConfigMissing(t *testing.T) {
	client := setupTestClient("testorg", nil, []string{"web-app"})
	printer := ui.New(&discardWriter{})

	err := runDisableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config.yaml")
}

func TestRunDisableRepos_ErrorWhenDisablingFullsend(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": true,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app"})
	printer := ui.New(&discardWriter{})

	err := runDisableRepos(context.Background(), client, printer, "testorg", []string{".fullsend"}, false, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot disable .fullsend repository")
}

func TestRunDisableRepos_AllowsRepoNotInConfig(t *testing.T) {
	// Disable should allow repos not in config (for cleanup of deleted repos).
	cfg := setupTestConfig(map[string]bool{
		"web-app": true,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app"})
	printer := ui.New(&discardWriter{})

	err := runDisableRepos(context.Background(), client, printer, "testorg", []string{"nonexistent"}, false, true)
	require.NoError(t, err)
	// Should succeed but make no changes (repo not in config, nothing to disable)
	assert.Len(t, client.CreatedFiles, 0)
}

func TestRunDisableRepos_CommitMessageFormat(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": true,
		"api":     true,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app", "api"})
	printer := ui.New(&discardWriter{})

	err := runDisableRepos(context.Background(), client, printer, "testorg", []string{"web-app", "api"}, false, true)
	require.NoError(t, err)

	require.Len(t, client.CreatedFiles, 1)
	assert.Contains(t, client.CreatedFiles[0].Message, "chore: disable 2 repositories")
}

func TestPromptEnrollment_ChooseAll(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"lowercase a", "a\n"},
		{"uppercase A", "A\n"},
		{"word all", "all\n"},
		{"word ALL", "ALL\n"},
		{"with spaces", "  a  \n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := strings.NewReader(tc.input)
			printer := ui.New(&discardWriter{})

			enrollAll, err := promptEnrollment(printer, input)
			require.NoError(t, err)
			assert.True(t, enrollAll)
		})
	}
}

func TestPromptEnrollment_ChooseNone(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"lowercase n", "n\n"},
		{"uppercase N", "N\n"},
		{"word none", "none\n"},
		{"word NONE", "NONE\n"},
		{"with spaces", "  n  \n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := strings.NewReader(tc.input)
			printer := ui.New(&discardWriter{})

			enrollAll, err := promptEnrollment(printer, input)
			require.NoError(t, err)
			assert.False(t, enrollAll)
		})
	}
}

func TestPromptEnrollment_RetryOnInvalidInput(t *testing.T) {
	// First input is invalid, second is valid.
	input := strings.NewReader("invalid\na\n")
	printer := ui.New(&discardWriter{})

	enrollAll, err := promptEnrollment(printer, input)
	require.NoError(t, err)
	assert.True(t, enrollAll)
}

func TestPromptEnrollment_MultipleRetriesBeforeValid(t *testing.T) {
	// Multiple invalid inputs before valid one.
	input := strings.NewReader("y\nyes\nmaybe\nn\n")
	printer := ui.New(&discardWriter{})

	enrollAll, err := promptEnrollment(printer, input)
	require.NoError(t, err)
	assert.False(t, enrollAll)
}

func TestPromptEnrollment_ErrorOnEOF(t *testing.T) {
	// EOF without any valid input.
	input := strings.NewReader("")
	printer := ui.New(&discardWriter{})

	_, err := promptEnrollment(printer, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading enrollment choice")
}

func TestPromptEnrollment_ErrorOnReadFailure(t *testing.T) {
	// errorReader always returns an error.
	input := &errorReader{}
	printer := ui.New(&discardWriter{})

	_, err := promptEnrollment(printer, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading enrollment choice")
}

// errorReader is a test helper that always returns an error on Read.
type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, fmt.Errorf("simulated read error")
}

// Regression test for #861: re-running install without repo selection
// must not unenroll previously enrolled repos. When enabledRepos is nil
// (user chose not to modify enrollment), buildLayerStack must suppress
// the disabled-repos list so the enrollment layer becomes a no-op.
func TestBuildLayerStack_NilEnabledRepos_SkipsDisabledRepos(t *testing.T) {
	cfg := config.NewOrgConfig(
		[]string{"repo-a", "repo-b"},
		nil, // nil enabledRepos → all repos are disabled in cfg
		[]string{"triage"},
		nil,
		"",
		"",
	)
	printer := ui.New(&discardWriter{})

	// When enabledRepos is nil (user chose not to change enrollment),
	// buildLayerStack must NOT pass disabled repos to the enrollment layer.
	stack := buildLayerStack(
		"test-org", nil, cfg, printer, "user",
		false, // privateRepo
		nil,   // enabledRepos (nil = no change)
		nil,   // agentCreds
		nil,   // enrolledRepoIDs
		nil,   // inferenceProvider
		false, // vendorBinary
		nil,   // vendorFn
		nil,   // vendorCollect
		"",    // analyzeFullsendSource
		nil,   // dispatcher
		"dev", // commitSHA
	)

	// The enrollment layer (last in the stack) should have no repos to
	// reconcile. InstallAll would fail on earlier layers that need a
	// real client, but the stack itself should be correctly constructed.
	require.NotNil(t, stack)

	// Verify directly: create the enrollment layer the same way
	// buildLayerStack does and confirm Install is a no-op.
	var disabledRepos []string
	// enabledRepos is nil, so disabledRepos stays nil
	enrollLayer := layers.NewEnrollmentLayer("test-org", nil, nil, disabledRepos, printer)
	err := enrollLayer.Install(context.Background())
	require.NoError(t, err)
}

// Regression test for #861: when enabledRepos is explicitly empty (not nil),
// the enrollment layer should still receive disabled repos so that
// unenrollment can proceed when explicitly requested.
func TestBuildLayerStack_EmptyEnabledRepos_IncludesDisabledRepos(t *testing.T) {
	cfg := config.NewOrgConfig(
		[]string{"repo-a", "repo-b"},
		[]string{}, // explicitly empty → all repos are disabled
		[]string{"triage"},
		nil,
		"",
		"",
	)
	printer := ui.New(&discardWriter{})

	stack := buildLayerStack(
		"test-org", nil, cfg, printer, "user",
		false,
		[]string{}, // explicitly empty (not nil)
		nil, nil, nil, false, nil, nil, "", nil, "dev",
	)

	// The enrollment layer should have disabled repos to reconcile.
	require.NotNil(t, stack)
}

func TestLoadExistingEnabledRepos_ReturnsEnabledRepos(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"repo-a": true,
		"repo-b": false,
		"repo-c": true,
	})
	client := setupTestClient("testorg", cfg, nil)

	repos := loadExistingEnabledRepos(context.Background(), client, "testorg")

	// Should return only enabled repos, sorted.
	assert.Equal(t, []string{"repo-a", "repo-c"}, repos)
}

func TestLoadExistingEnabledRepos_ReturnsNilWhenNoConfig(t *testing.T) {
	client := forge.NewFakeClient()

	repos := loadExistingEnabledRepos(context.Background(), client, "testorg")

	assert.Nil(t, repos)
}

func TestCheckInstallScopes_AllPresent(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo", "workflow", "admin:org", "read:org"},
	}
	printer := ui.New(&discardWriter{})

	err := checkInstallScopes(context.Background(), client, printer)
	require.NoError(t, err)
}

func TestCheckInstallScopes_Missing(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo"},
	}
	printer := ui.New(&discardWriter{})

	err := checkInstallScopes(context.Background(), client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow")
	assert.Contains(t, err.Error(), "admin:org")
}

func TestCheckInstallScopes_FineGrainedToken(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: nil,
	}
	printer := ui.New(&discardWriter{})

	err := checkInstallScopes(context.Background(), client, printer)
	require.NoError(t, err)
}

func TestCheckInstallScopes_GetTokenScopesError(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{"GetTokenScopes": errors.New("network error")},
	}
	printer := ui.New(&discardWriter{})

	err := checkInstallScopes(context.Background(), client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking token scopes")
	assert.Contains(t, err.Error(), "network error")
}

func TestCheckInstallScopes_SyncWithLayers(t *testing.T) {
	emptyCfg := &config.OrgConfig{}
	stack := layers.NewStack(
		layers.NewConfigRepoLayer("test-org", nil, emptyCfg, ui.New(&discardWriter{}), false),
		layers.NewWorkflowsLayer("test-org", nil, ui.New(&discardWriter{}), "", "test-version", false),
		layers.NewHarnessWrappersLayer("test-org", nil, ui.New(&discardWriter{}), nil, "dev"),
		layers.NewSecretsLayer("test-org", nil, nil, ui.New(&discardWriter{})),
		layers.NewInferenceLayer("test-org", nil, nil, ui.New(&discardWriter{})),
		layers.NewOIDCDispatchLayer("test-org", nil, nil, nil, ui.New(&discardWriter{})),
		layers.NewEnrollmentLayer("test-org", nil, nil, nil, ui.New(&discardWriter{})),
		layers.NewVendorBinaryLayer("test-org", ".fullsend", nil, ui.New(&discardWriter{}), false, nil),
	)
	layerScopes := stack.CollectRequiredScopes(layers.OpInstall)

	assert.ElementsMatch(t, installRequiredScopes, layerScopes,
		"installRequiredScopes must match the union of RequiredScopes(OpInstall) from all layers; update the variable if a layer's scopes change")
}

func TestCheckPerRepoScopes_AllPresent(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo", "workflow", "read:org"},
	}
	printer := ui.New(&discardWriter{})

	err := checkPerRepoScopes(context.Background(), client, printer)
	require.NoError(t, err)
}

func TestCheckPerRepoScopes_Missing(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo"},
	}
	printer := ui.New(&discardWriter{})

	err := checkPerRepoScopes(context.Background(), client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow")
	assert.NotContains(t, err.Error(), "admin:org")
}

func TestCheckPerRepoScopes_FineGrainedToken(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: nil,
	}
	printer := ui.New(&discardWriter{})

	err := checkPerRepoScopes(context.Background(), client, printer)
	require.NoError(t, err)
}

func TestCheckPerRepoScopes_GetTokenScopesError(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{"GetTokenScopes": errors.New("network error")},
	}
	printer := ui.New(&discardWriter{})

	err := checkPerRepoScopes(context.Background(), client, printer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking token scopes")
	assert.Contains(t, err.Error(), "network error")
}

func TestCheckPerRepoScopes_DoesNotRequireAdminOrg(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo", "workflow"},
	}
	printer := ui.New(&discardWriter{})

	err := checkPerRepoScopes(context.Background(), client, printer)
	require.NoError(t, err, "per-repo should not require admin:org scope")
}

func TestPerRepoRequiredScopes_SubsetOfInstallScopes(t *testing.T) {
	installSet := make(map[string]bool)
	for _, s := range installRequiredScopes {
		installSet[s] = true
	}
	for _, s := range perRepoRequiredScopes {
		assert.True(t, installSet[s],
			"perRepoRequiredScopes contains %q which is not in installRequiredScopes", s)
	}
}

func TestInstallCmd_PerRepoRejectsInvalidRole(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--agents", "triage,INVALID",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role name")
}

func TestInstallCmd_PerRepoRejectsOwnerWithDots(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "my.org/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid owner name")
}

func TestInstallCmd_PerRepoRejectsURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"https URL", "https://github.com/acme/widget"},
		{"http URL", "http://github.com/acme/widget"},
		{"www prefix", "www.github.com/acme/widget"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRootCmd()
			cmd.SetArgs([]string{"admin", "install", tc.input,
				"--mint-url", "https://mint-test-abc123.run.app",
				"--inference-project", "my-project"})
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "expected owner/repo format, got a URL")
		})
	}
}

// --- resolveSharedRoleAppIDs tests ---

func TestResolveSharedRoleAppIDs_MatchesInstalledApps(t *testing.T) {
	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{
		{AppID: 100, AppSlug: "acme-coder"},
		{AppID: 200, AppSlug: "acme-reviewer"},
	}

	existingIDs := map[string]string{
		"coder":    "100",
		"reviewer": "200",
	}

	result, err := resolveSharedRoleAppIDs(context.Background(), fake, existingIDs, "new-org", []string{"coder", "reviewer"})
	require.NoError(t, err)
	assert.Equal(t, "100", result["coder"])
	assert.Equal(t, "200", result["reviewer"])
}

func TestResolveSharedRoleAppIDs_ErrorWhenAppNotInstalled(t *testing.T) {
	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{
		{AppID: 100, AppSlug: "acme-coder"},
	}

	existingIDs := map[string]string{
		"coder":    "100",
		"reviewer": "999",
	}

	_, err := resolveSharedRoleAppIDs(context.Background(), fake, existingIDs, "new-org", []string{"coder", "reviewer"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no shared app for role \"reviewer\"")
}

func TestResolveSharedRoleAppIDs_ErrorWhenNoExistingIDs(t *testing.T) {
	fake := forge.NewFakeClient()

	_, err := resolveSharedRoleAppIDs(context.Background(), fake, nil, "new-org", []string{"coder"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no existing ROLE_APP_IDS")
}

func TestResolveSharedRoleAppIDs_ErrorWhenRoleNotConfigured(t *testing.T) {
	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{{AppID: 100, AppSlug: "acme-coder"}}

	_, err := resolveSharedRoleAppIDs(context.Background(), fake, map[string]string{"coder": "100"}, "new-org", []string{"triage"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no app ID configured for role "triage"`)
}

func TestResolveSharedRoleAppIDs_UsesRoleOnlyIDs(t *testing.T) {
	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{
		{AppID: 100, AppSlug: "acme-coder"},
	}

	existingIDs := map[string]string{
		"coder": "100",
	}

	result, err := resolveSharedRoleAppIDs(context.Background(), fake, existingIDs, "new-org", []string{"coder"})
	require.NoError(t, err)
	assert.Equal(t, "100", result["coder"])
}

func TestResolveSharedRoleAppIDs_IgnoresLegacyOrgScopedKeys(t *testing.T) {
	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{
		{AppID: 100, AppSlug: "acme-coder"},
	}

	existingIDs := map[string]string{
		"acme-corp/coder": "100",
	}

	_, err := resolveSharedRoleAppIDs(context.Background(), fake, existingIDs, "acme-corp", []string{"coder"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no existing ROLE_APP_IDS")
}

func TestDetectSharedApps_MatchesRoleOnlyIDs(t *testing.T) {
	old := detectSharedAppsGCFClientFactory
	detectSharedAppsGCFClientFactory = func(string) gcf.GCFClient {
		return gcf.NewFakeGCFClient(gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.com",
			EnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
			},
		}))
	}
	t.Cleanup(func() { detectSharedAppsGCFClientFactory = old })

	fake := forge.NewFakeClient()
	fake.Installations = []forge.Installation{
		{AppID: 100, AppSlug: "fullsend-ai-coder"},
		{AppID: 200, AppSlug: "fullsend-ai-triage"},
	}

	slugs, roleIDs, err := detectSharedApps(context.Background(), fake, ui.New(&strings.Builder{}), "acme", []string{"coder", "triage"}, "mint-project", "us-central1")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-ai-coder", slugs["coder"])
	assert.Equal(t, "100", roleIDs["coder"])
	assert.Equal(t, "200", roleIDs["triage"])
}

func TestDetectSharedApps_NoRoleOnlyIDs(t *testing.T) {
	old := detectSharedAppsGCFClientFactory
	detectSharedAppsGCFClientFactory = func(string) gcf.GCFClient {
		return gcf.NewFakeGCFClient(gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"acme/coder":"100"}`},
		}))
	}
	t.Cleanup(func() { detectSharedAppsGCFClientFactory = old })

	slugs, roleIDs, err := detectSharedApps(context.Background(), forge.NewFakeClient(), ui.New(&strings.Builder{}), "acme", []string{"coder"}, "mint-project", "us-central1")
	require.NoError(t, err)
	assert.Empty(t, slugs)
	assert.Empty(t, roleIDs)
}

func TestDetectSharedApps_ReadRoleAppIDsError(t *testing.T) {
	old := detectSharedAppsGCFClientFactory
	detectSharedAppsGCFClientFactory = func(string) gcf.GCFClient {
		return gcf.NewFakeGCFClient(gcf.WithFakeErrors(map[string]error{
			"GetFunction": fmt.Errorf("permission denied"),
		}))
	}
	t.Cleanup(func() { detectSharedAppsGCFClientFactory = old })

	out := &strings.Builder{}
	slugs, roleIDs, err := detectSharedApps(context.Background(), forge.NewFakeClient(), ui.New(out), "acme", []string{"coder"}, "mint-project", "us-central1")
	require.NoError(t, err)
	assert.Nil(t, slugs)
	assert.Nil(t, roleIDs)
	assert.Contains(t, out.String(), "Could not read ROLE_APP_IDS")
}

func TestDetectSharedApps_ListInstallationsError(t *testing.T) {
	old := detectSharedAppsGCFClientFactory
	detectSharedAppsGCFClientFactory = func(string) gcf.GCFClient {
		return gcf.NewFakeGCFClient(
			gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
				URI:     "https://mint.example.com",
				EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
			}),
			gcf.WithFakeTrafficEnvVars(map[string]string{
				"ROLE_APP_IDS": `{"coder":"100"}`,
			}),
		)
	}
	t.Cleanup(func() { detectSharedAppsGCFClientFactory = old })

	fake := forge.NewFakeClient()
	fake.Errors["ListOrgInstallations"] = fmt.Errorf("forbidden")

	slugs, roleIDs, err := detectSharedApps(context.Background(), fake, ui.New(&strings.Builder{}), "acme", []string{"coder"}, "mint-project", "us-central1")
	require.NoError(t, err)
	assert.Nil(t, slugs)
	assert.Equal(t, map[string]string{"coder": "100"}, roleIDs)
}

func TestInstallCmd_SkipMintCheckUsesDefaultMintURL(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--inference-project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	// With the default mint URL, --skip-mint-check no longer errors
	// when --mint-url is not explicitly provided.
	require.NoError(t, err)
}

func TestInstallCmd_SkipMintCheckRejectsEmptyMintURL(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--mint-url", "",
		"--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-url is required when using --skip-mint-check")
}

func TestInstallCmd_SkipMintCheckAcceptsNonCloudRunURL(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--mint-url", "https://mint.example.com/v1/token",
		"--inference-project", "my-project",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInstallCmd_SkipMintCheckSkipsMintProject(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	// Without --skip-mint-check and without --mint-project/--mint-url, an error is returned.
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget"})
	err := cmd.Execute()
	require.Error(t, err)

	// With --skip-mint-check, --mint-project is not required.
	cmd2 := newRootCmd()
	cmd2.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--mint-url", "https://mint.example.com/v1/token",
		"--inference-project", "my-project",
		"--dry-run"})
	err2 := cmd2.Execute()
	require.NoError(t, err2)
}

func TestInstallCmd_SkipMintCheckPerOrgUsesDefaultMintURL(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	// With default mint URL, --skip-mint-check succeeds without explicit --mint-url.
	// The command may fail downstream (e.g. listing repos), but should not
	// error on missing --mint-url.
	cmd.SetArgs([]string{"admin", "install", "acme",
		"--skip-mint-check",
		"--enroll-none"})
	err := cmd.Execute()
	if err != nil {
		assert.NotContains(t, err.Error(), "--mint-url is required when using --skip-mint-check")
	}
}

func TestInstallCmd_SkipMintCheckPerOrgRejectsEmptyMintURL(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme",
		"--skip-mint-check",
		"--mint-url", ""})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-url is required when using --skip-mint-check")
}

func TestInstallCmd_SkipMintCheckRejectsUserinfo(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--mint-url", "https://user:pass@mint.example.com/v1/token",
		"--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain embedded credentials")
}

func TestInstallCmd_SkipMintCheckRejectsHTTP(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--skip-mint-check",
		"--mint-url", "http://mint.example.com",
		"--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--mint-url must be a valid HTTPS URL")
}

func TestSkipMintDispatcher(t *testing.T) {
	d := &skipMintDispatcher{mintURL: "https://mint.example.com/v1/token"}
	assert.Equal(t, "skip-mint-check", d.Name())
	assert.Nil(t, d.OrgSecretNames())
	assert.Equal(t, []string{"FULLSEND_MINT_URL"}, d.OrgVariableNames())
	assert.NoError(t, d.StoreAgentPEM(context.Background(), "role", []byte("pem")))
	vars, err := d.Provision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"FULLSEND_MINT_URL": "https://mint.example.com/v1/token"}, vars)
}

func TestValidateMintURLHTTPS(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"valid URL", "https://mint.example.com/v1/token", ""},
		{"valid with port", "https://mint.example.com:8443/v1", ""},
		{"http rejected", "http://mint.example.com", "HTTPS URL"},
		{"empty string", "", "HTTPS URL"},
		{"no host", "https://", "HTTPS URL"},
		{"userinfo", "https://user:pass@host.com", "embedded credentials"},
		{"username only", "https://user@host.com", "embedded credentials"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMintURLHTTPS(tc.input)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidateSkipMintCheck(t *testing.T) {
	require.Error(t, validateSkipMintCheck(""))
	require.Error(t, validateSkipMintCheck("http://example.com"))
	require.NoError(t, validateSkipMintCheck("https://mint.example.com/v1/token"))
}

func TestValidateWIFProvider_Valid(t *testing.T) {
	valid := []string{
		"projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/gh-acme-widget",
		"projects/999999999999/locations/global/workloadIdentityPools/my-pool-123/providers/my-provider-456",
		"projects/1/locations/global/workloadIdentityPools/abcd/providers/efgh",
		"projects/1/locations/global/workloadIdentityPools/a-very-long-pool-name-32-chars1/providers/a-very-long-prov-name-32-chars1",
	}
	for _, v := range valid {
		t.Run(v, func(t *testing.T) {
			require.NoError(t, validateWIFProvider(v))
		})
	}
}

func TestValidateWIFProvider_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"bare name", "standalone-fullsend"},
		{"missing projects prefix", "123/locations/global/workloadIdentityPools/pool/providers/prov"},
		{"partial path", "projects/123/locations/global/workloadIdentityPools/pool"},
		{"wrong location", "projects/123/locations/us-east1/workloadIdentityPools/pool/providers/prov"},
		{"non-numeric project", "projects/my-project/locations/global/workloadIdentityPools/pool/providers/prov"},
		{"empty string", ""},
		{"trailing slash", "projects/123/locations/global/workloadIdentityPools/pool/providers/prov/"},
		{"uppercase pool", "projects/123/locations/global/workloadIdentityPools/Pool/providers/prov"},
		{"pool too short (1 char)", "projects/123/locations/global/workloadIdentityPools/a/providers/abcd"},
		{"pool too short (3 chars)", "projects/123/locations/global/workloadIdentityPools/abc/providers/abcd"},
		{"provider too short (1 char)", "projects/123/locations/global/workloadIdentityPools/abcd/providers/a"},
		{"pool trailing hyphen", "projects/123/locations/global/workloadIdentityPools/abcd-/providers/abcd"},
		{"provider trailing hyphen", "projects/123/locations/global/workloadIdentityPools/abcd/providers/abcd-"},
		{"pool too long (33 chars)", "projects/123/locations/global/workloadIdentityPools/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/providers/abcd"},
		{"provider too long (33 chars)", "projects/123/locations/global/workloadIdentityPools/abcd/providers/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWIFProvider(tc.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--inference-wif-provider must be a full WIF provider resource name")
		})
	}
}

func TestInstallCmd_PerOrgRejectsInvalidWIFProvider(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme",
		"--dry-run",
		"--inference-project", "my-project",
		"--inference-wif-provider", "standalone-fullsend"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-wif-provider must be a full WIF provider resource name")
}

func TestInstallCmd_PerRepoRejectsInvalidWIFProvider(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "just-a-name"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-wif-provider must be a full WIF provider resource name")
}

func TestInstallCmd_PerOrgAcceptsValidWIFProvider(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme",
		"--dry-run",
		"--enroll-none",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc"})
	err := cmd.Execute()
	// Per-org dry-run hits live GitHub API for repo listing; expect a downstream
	// error but NOT a WIF validation error — proving validation passed.
	if err != nil {
		assert.NotContains(t, err.Error(), "--inference-wif-provider must be")
	}
}

func TestInstallCmd_PerRepoAcceptsValidWIFProvider(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInstallCmd_PerRepoDryRun_Vendor(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--dry-run",
		"--vendor"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestRunDryRun_WithDiscoveredRepos(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "testuser"
	discovered := []forge.Repository{
		{Name: forge.ConfigRepoName, FullName: "testorg/" + forge.ConfigRepoName, DefaultBranch: "main"},
		{Name: "myrepo", FullName: "testorg/myrepo", DefaultBranch: "main"},
	}
	client.Repos = discovered

	var buf bytes.Buffer
	printer := ui.New(&buf)
	err := runDryRun(
		context.Background(), client, printer, "testorg",
		[]string{"myrepo"},
		config.DefaultAgentRoles(),
		nil,
		"",
		true,
		"https://mint.example.com/v1/token",
		discovered,
		true,
		"",
		"",
	)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Layer: vendor")
}

func TestRunAnalyze_WithFakeClient(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "testuser"
	client.Repos = []forge.Repository{
		{Name: forge.ConfigRepoName, FullName: "testorg/" + forge.ConfigRepoName},
	}

	var buf bytes.Buffer
	err := runAnalyze(context.Background(), client, ui.New(&buf), "testorg", "")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Layer:")
}

func TestRunInstall_RequiresAgentCredsWhenMintEnabled(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "testuser"
	discovered := []forge.Repository{
		{Name: forge.ConfigRepoName, FullName: "testorg/" + forge.ConfigRepoName},
	}
	client.Repos = discovered

	err := runInstall(
		context.Background(), client, ui.New(&bytes.Buffer{}), "testorg",
		[]string{}, config.DefaultAgentRoles(), nil,
		nil, "",
		false, "", "",
		"gcf", "test-project", "us-central1", "", true,
		"https://mint.example.com/v1/token",
		false,
		discovered,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC mint requires")
}

func TestRunInstall_WithSkipMintCheck(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{"myrepo": false})
	client := setupTestClient("testorg", cfg, []string{"myrepo"})
	client.AuthenticatedUser = "testuser"

	var agentCreds []layers.AgentCredentials
	for _, role := range config.DefaultAgentRoles() {
		agentCreds = append(agentCreds, layers.AgentCredentials{
			AgentEntry: config.AgentEntry{Role: role},
		})
	}

	err := runInstall(
		context.Background(), client, ui.New(&bytes.Buffer{}), "testorg",
		nil, config.DefaultAgentRoles(), agentCreds,
		nil, "",
		false, "", "",
		"gcf", "test-project", "us-central1", "", true,
		"https://mint.example.com/v1/token",
		true,
		client.Repos,
	)
	require.NoError(t, err)
}

func TestRunInstall_DiscoversRepos(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{"myrepo": false})
	client := setupTestClient("testorg", cfg, []string{"myrepo"})
	client.AuthenticatedUser = "testuser"

	var agentCreds []layers.AgentCredentials
	for _, role := range config.DefaultAgentRoles() {
		agentCreds = append(agentCreds, layers.AgentCredentials{
			AgentEntry: config.AgentEntry{Role: role},
		})
	}

	var buf bytes.Buffer
	err := runInstall(
		context.Background(), client, ui.New(&buf), "testorg",
		nil, config.DefaultAgentRoles(), agentCreds,
		nil, "",
		false, "", "",
		"gcf", "test-project", "us-central1", "", true,
		"https://mint.example.com/v1/token",
		true,
		nil,
	)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Discovering repositories")
}

func TestRunInstall_InvalidEnabledRepo(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "testuser"
	discovered := []forge.Repository{
		{Name: "myrepo", FullName: "testorg/myrepo"},
	}

	err := runInstall(
		context.Background(), client, ui.New(&bytes.Buffer{}), "testorg",
		[]string{"missing-repo"}, config.DefaultAgentRoles(), nil,
		nil, "",
		false, "", "",
		"gcf", "test-project", "us-central1", "", true,
		"https://mint.example.com/v1/token",
		true,
		discovered,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-repo")
}

func TestRunInstall_WithVendorAndSkipMint(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{"myrepo": false})
	client := setupTestClient("testorg", cfg, []string{"myrepo"})
	client.AuthenticatedUser = "testuser"

	var agentCreds []layers.AgentCredentials
	for _, role := range config.DefaultAgentRoles() {
		agentCreds = append(agentCreds, layers.AgentCredentials{
			AgentEntry: config.AgentEntry{Role: role},
		})
	}

	var buf bytes.Buffer
	err := runInstall(
		context.Background(), client, ui.New(&buf), "testorg",
		nil, config.DefaultAgentRoles(), agentCreds,
		nil, "",
		true, "", "",
		"gcf", "test-project", "us-central1", "", true,
		"https://mint.example.com/v1/token",
		true,
		client.Repos,
	)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "vendored assets")
}

func TestRunPerRepoInstall_ValidationErrors(t *testing.T) {
	base := perRepoInstallConfig{
		RepoFullName:     "acme/widget",
		Agents:           strings.Join(config.PerRepoDefaultRoles(), ","),
		InferenceProject: "my-project",
		MintProject:      "my-project",
		MintURL:          "https://mint.example.com/v1/token",
		SkipMintCheck:    true,
	}
	tests := []struct {
		name string
		cfg  perRepoInstallConfig
		want string
	}{
		{
			name: "url not owner/repo",
			cfg: func() perRepoInstallConfig {
				c := base
				c.RepoFullName = "https://github.com/acme/widget"
				return c
			}(),
			want: "expected owner/repo format",
		},
		{
			name: "invalid owner",
			cfg: func() perRepoInstallConfig {
				c := base
				c.RepoFullName = "-bad/widget"
				return c
			}(),
			want: "invalid owner name",
		},
		{
			name: "missing inference project",
			cfg: func() perRepoInstallConfig {
				c := base
				c.InferenceProject = ""
				return c
			}(),
			want: "--inference-project is required",
		},
		{
			name: "missing mint project without skip",
			cfg: func() perRepoInstallConfig {
				c := base
				c.SkipMintCheck = false
				c.MintURL = ""
				c.MintProject = ""
				return c
			}(),
			want: "--mint-project",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runPerRepoInstall(context.Background(), tt.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestFilterSlugsByAppSet(t *testing.T) {
	tests := []struct {
		name   string
		appSet string
		slugs  map[string]string
		want   map[string]string
	}{
		{
			name:   "matching app-set preserved",
			appSet: "fullsend-ai",
			slugs:  map[string]string{"coder": "fullsend-ai-coder", "review": "fullsend-ai-review"},
			want:   map[string]string{"coder": "fullsend-ai-coder", "review": "fullsend-ai-review"},
		},
		{
			name:   "different app-set filtered out",
			appSet: "fullsend-ai",
			slugs:  map[string]string{"coder": "konflux-ci-coder", "review": "konflux-ci-review"},
			want:   map[string]string{},
		},
		{
			name:   "mixed app-sets keeps only matching",
			appSet: "fullsend-ai",
			slugs:  map[string]string{"coder": "fullsend-ai-coder", "review": "konflux-ci-review"},
			want:   map[string]string{"coder": "fullsend-ai-coder"},
		},
		{
			name:   "nil input returns empty map",
			appSet: "fullsend-ai",
			slugs:  nil,
			want:   map[string]string{},
		},
		{
			name:   "shorter prefix does not match longer slug",
			appSet: "fullsend",
			slugs:  map[string]string{"coder": "fullsend-ai-coder"},
			want:   map[string]string{},
		},
		{
			name:   "default app-set matches own slugs",
			appSet: "fullsend",
			slugs:  map[string]string{"coder": "fullsend-coder", "review": "fullsend-review"},
			want:   map[string]string{"coder": "fullsend-coder", "review": "fullsend-review"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterSlugsByAppSet(tt.slugs, tt.appSet)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRunUninstall_LegacySlugsIncludedWhenConfigUnavailable(t *testing.T) {
	// When the config repo is unavailable, runUninstall should check
	// both current (fullsend-ai-*) and legacy (fullsend-*) app naming
	// so that apps created under an older version are not silently skipped.
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"admin:org", "repo", "delete_repo"}
	client.Errors["GetFileContent"] = errors.New("not found")

	// Simulate a legacy app installed under the old naming convention.
	client.Installations = []forge.Installation{
		{ID: 1, AppSlug: "fullsend-coder"},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	err := runUninstall(context.Background(), client, printer, "test-org", "fullsend-ai", appsetup.NopBrowser{}, strings.NewReader(""))
	require.NoError(t, err)

	output := buf.String()
	// The legacy slug "fullsend-coder" should NOT be reported as "not found".
	assert.NotContains(t, output, "App fullsend-coder not found")
	// It should appear in the app cleanup section.
	assert.Contains(t, output, "fullsend-coder")
}

func TestRunUninstall_WarnsWhenNoAppsFound(t *testing.T) {
	// When no apps are found under any naming convention, the uninstaller
	// should warn the user instead of silently reporting success.
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"admin:org", "repo", "delete_repo"}
	client.Errors["GetFileContent"] = errors.New("not found")

	// No installations at all.
	client.Installations = []forge.Installation{}

	var buf strings.Builder
	printer := ui.New(&buf)

	err := runUninstall(context.Background(), client, printer, "test-org", "fullsend-ai", appsetup.NopBrowser{}, strings.NewReader(""))
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "No fullsend apps found installed in this organization")
}

func TestRunUninstall_LegacySlugsSkippedWhenAppSetMatchesLegacy(t *testing.T) {
	// When --app-set is explicitly "fullsend" (matching legacy), the legacy
	// slugs should not be duplicated.
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"admin:org", "repo", "delete_repo"}
	client.Errors["GetFileContent"] = errors.New("not found")

	client.Installations = []forge.Installation{
		{ID: 1, AppSlug: "fullsend-coder"},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	err := runUninstall(context.Background(), client, printer, "test-org", "fullsend", appsetup.NopBrowser{}, strings.NewReader(""))
	require.NoError(t, err)

	output := buf.String()
	// Should find the legacy app and attempt cleanup.
	assert.NotContains(t, output, "No fullsend apps found")
	assert.Contains(t, output, "fullsend-coder")
}

func TestRunUninstall_DedupsSlugsAcrossAppSets(t *testing.T) {
	// When legacy and current app-sets produce the same slug, the slug
	// should appear only once in the cleanup output (no duplicate browser opens).
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"admin:org", "repo", "delete_repo"}
	client.Errors["GetFileContent"] = errors.New("not found")

	client.Installations = []forge.Installation{
		{ID: 1, AppSlug: "fullsend-coder"},
		{ID: 2, AppSlug: "fullsend-triage"},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	// Use "fullsend" which matches a legacy prefix — without dedup,
	// each slug would appear twice.
	err := runUninstall(context.Background(), client, printer, "test-org", "fullsend", appsetup.NopBrowser{}, strings.NewReader(""))
	require.NoError(t, err)

	output := buf.String()
	// Each slug should appear in the "Opening ... installation settings" line exactly once.
	assert.Equal(t, 1, strings.Count(output, "Opening fullsend-coder installation settings"))
	assert.Equal(t, 1, strings.Count(output, "Opening fullsend-triage installation settings"))
}

func TestRunUninstall_NopBrowserSkipsBrowserOpen(t *testing.T) {
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"admin:org", "repo", "delete_repo"}
	client.Errors["GetFileContent"] = errors.New("not found")

	client.Installations = []forge.Installation{
		{ID: 1, AppSlug: "fullsend-ai-coder"},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	err := runUninstall(context.Background(), client, printer, "test-org", "fullsend-ai", appsetup.NopBrowser{}, strings.NewReader(""))
	require.NoError(t, err)

	output := buf.String()
	// NopBrowser.Open returns nil, so the success path runs.
	assert.Contains(t, output, "Opened fullsend-ai-coder")
	assert.Contains(t, output, "settings/installations/1")
	assert.NotContains(t, output, "Could not open browser")
}

func TestRunUninstall_UsesHarnessDiscovery(t *testing.T) {
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"admin:org", "repo", "delete_repo"}

	// Provide config.yaml with agents: block (should be skipped in favor of harness).
	client.FileContents = map[string][]byte{
		"test-org/.fullsend/config.yaml": []byte("version: v1\ndispatch:\n  platform: github-actions\nagents:\n  - role: triage\n    slug: old-triage\n"),
	}
	// Provide harness directory with wrapper files.
	client.DirContents = map[string][]forge.DirectoryEntry{
		"test-org/.fullsend/harness@main": {
			{Path: "harness/triage.yaml", Type: "file"},
			{Path: "harness/coder.yaml", Type: "file"},
		},
	}
	client.FileContentsRef = map[string][]byte{
		"test-org/.fullsend/harness/triage.yaml@main": []byte("role: triage\nslug: my-triage\n"),
		"test-org/.fullsend/harness/coder.yaml@main":  []byte("role: coder\nslug: my-coder\n"),
	}

	client.Installations = []forge.Installation{
		{ID: 1, AppSlug: "my-triage"},
		{ID: 2, AppSlug: "my-coder"},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	err := runUninstall(context.Background(), client, printer, "test-org", "fullsend-ai", appsetup.NopBrowser{}, strings.NewReader("\n\n"))
	require.NoError(t, err)

	output := buf.String()
	// Should use harness-discovered slugs.
	assert.Contains(t, output, "my-triage")
	assert.Contains(t, output, "my-coder")
	// Should NOT emit the deprecation warning about agents: block.
	assert.NotContains(t, output, "agents: block")
}

func TestRunUninstall_FallsBackToAgentsBlockWithWarning(t *testing.T) {
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"admin:org", "repo", "delete_repo"}

	// Provide config.yaml with agents: block but no harness directory.
	client.FileContents = map[string][]byte{
		"test-org/.fullsend/config.yaml": []byte("version: v1\ndispatch:\n  platform: github-actions\nagents:\n  - role: triage\n    slug: cfg-triage\n"),
	}

	client.Installations = []forge.Installation{
		{ID: 1, AppSlug: "cfg-triage"},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	err := runUninstall(context.Background(), client, printer, "test-org", "fullsend-ai", appsetup.NopBrowser{}, strings.NewReader("\n"))
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "cfg-triage")
	assert.Contains(t, output, "agents: block")
}

func TestAwaitRepoMaintenance_Success(t *testing.T) {
	client := forge.NewFakeClient()
	dispatchTime := time.Now().UTC().Add(-10 * time.Second)
	client.WorkflowRuns["testorg/.fullsend/repo-maintenance.yml"] = &forge.WorkflowRun{
		ID:         42,
		Status:     "completed",
		Conclusion: "success",
		HTMLURL:    "https://github.com/testorg/.fullsend/actions/runs/42",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	client.Annotations = []forge.Annotation{
		{Level: "notice", Message: "Enrollment PR: https://github.com/testorg/web-app/pull/1"},
		{Level: "warning", Message: "some warning"},
		{Level: "notice", Message: "Enrollment PR: https://github.com/testorg/api/pull/2"},
	}

	var buf bytes.Buffer
	printer := ui.New(&buf)

	awaitRepoMaintenanceWithInterval(context.Background(), client, printer, "testorg", dispatchTime, 1*time.Millisecond, 2)

	output := buf.String()
	assert.Contains(t, output, "repo-maintenance completed successfully")
	assert.Contains(t, output, "https://github.com/testorg/.fullsend/actions/runs/42")
	assert.Contains(t, output, "Enrollment PR: https://github.com/testorg/web-app/pull/1")
	assert.Contains(t, output, "Enrollment PR: https://github.com/testorg/api/pull/2")
	// Warnings from annotations should not be printed (only notices).
	assert.NotContains(t, output, "some warning")
}

func TestAwaitRepoMaintenance_Timeout(t *testing.T) {
	client := forge.NewFakeClient()
	dispatchTime := time.Now().UTC().Add(-10 * time.Second)
	// No workflow runs configured — will timeout.

	var buf bytes.Buffer
	printer := ui.New(&buf)

	awaitRepoMaintenanceWithInterval(context.Background(), client, printer, "testorg", dispatchTime, 1*time.Millisecond, 2)

	output := buf.String()
	assert.Contains(t, output, "timed out waiting for repo-maintenance workflow")
}

func TestAwaitRepoMaintenance_Failure(t *testing.T) {
	client := forge.NewFakeClient()
	dispatchTime := time.Now().UTC().Add(-10 * time.Second)
	client.WorkflowRuns["testorg/.fullsend/repo-maintenance.yml"] = &forge.WorkflowRun{
		ID:         43,
		Status:     "completed",
		Conclusion: "failure",
		HTMLURL:    "https://github.com/testorg/.fullsend/actions/runs/43",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	var buf bytes.Buffer
	printer := ui.New(&buf)

	awaitRepoMaintenanceWithInterval(context.Background(), client, printer, "testorg", dispatchTime, 1*time.Millisecond, 2)

	output := buf.String()
	assert.Contains(t, output, "repo-maintenance completed with conclusion: failure")
}

func TestAwaitRepoMaintenance_ContextCancelled(t *testing.T) {
	client := forge.NewFakeClient()
	dispatchTime := time.Now().UTC().Add(-10 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	var buf bytes.Buffer
	printer := ui.New(&buf)

	awaitRepoMaintenanceWithInterval(ctx, client, printer, "testorg", dispatchTime, 1*time.Millisecond, 36)

	output := buf.String()
	assert.Contains(t, output, "context cancelled while waiting for workflow")
}

func TestInstallCmd_SkipMintCheckStillValidatesWIFProvider(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"admin", "install", "acme",
		"--dry-run",
		"--skip-mint-check",
		"--mint-url", "https://mint.example.com/v1/token",
		"--inference-project", "my-project",
		"--inference-wif-provider", "standalone-fullsend"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-wif-provider must be a full WIF provider resource name")
}

func TestApplyPerRepoScaffold(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".github/workflows/fullsend.yaml", Content: []byte("workflow"), Mode: "100644"},
		{Path: ".fullsend/config.yaml", Content: []byte("config"), Mode: "100644"},
	}
	repoVars := map[string]string{
		"FULLSEND_MINT_URL":   "https://mint.example.run.app",
		"FULLSEND_GCP_REGION": "global",
		forge.PerRepoGuardVar: "true",
	}
	repoSecrets := map[string]string{
		"FULLSEND_GCP_PROJECT_ID":   "my-project",
		"FULLSEND_GCP_WIF_PROVIDER": "projects/123/locations/global/workloadIdentityPools/pool/providers/prov",
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, repoVars, repoSecrets)
	require.NoError(t, err)

	require.Len(t, client.CommittedFiles, 1)
	assert.Equal(t, "acme", client.CommittedFiles[0].Owner)
	assert.Equal(t, "widget", client.CommittedFiles[0].Repo)
	assert.Len(t, client.CommittedFiles[0].Files, 2)

	varNames := make(map[string]string)
	for _, v := range client.Variables {
		varNames[v.Name] = v.Value
	}
	assert.Equal(t, "https://mint.example.run.app", varNames["FULLSEND_MINT_URL"])
	assert.Equal(t, "global", varNames["FULLSEND_GCP_REGION"])
	assert.Equal(t, "true", varNames[forge.PerRepoGuardVar])

	secretNames := make(map[string]string)
	for _, s := range client.CreatedSecrets {
		secretNames[s.Name] = s.Value
	}
	assert.Equal(t, "my-project", secretNames["FULLSEND_GCP_PROJECT_ID"])
	assert.Contains(t, secretNames, "FULLSEND_GCP_WIF_PROVIDER")
}

func TestApplyPerRepoScaffold_GetRepoError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["GetRepo"] = errors.New("not found")
	printer := ui.New(&bytes.Buffer{})

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting repo info")
}

func TestApplyPerRepoScaffold_CommitFilesError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = errors.New("permission denied")
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing scaffold files")
	assert.Empty(t, client.CreatedBranches, "should not attempt fallback for generic error")
	assert.Empty(t, client.CreatedProposals, "should not attempt fallback for generic error")
}

func TestApplyPerRepoScaffold_Idempotent(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	noChange := false
	client.CommitFilesChanged = &noChange
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, map[string]string{"K": "V"}, map[string]string{"S": "secret"})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "up to date")
	assert.Len(t, client.Variables, 1, "variables should still be set even when files are unchanged")
	assert.Len(t, client.CreatedSecrets, 1, "secrets should still be set even when files are unchanged")
}

func TestApplyPerRepoScaffold_NonMainBranch(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "develop"}}
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "acme/widget (develop branch)")
	assert.Contains(t, buf.String(), "Pushed 1 file to develop")
}

func TestApplyPerRepoScaffold_CreateVariableError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors = map[string]error{
		"CreateOrUpdateRepoVariable": errors.New("rate limited"),
	}
	printer := ui.New(&bytes.Buffer{})

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", nil, map[string]string{"K": "V"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting repo variable")
}

func TestApplyPerRepoScaffold_CreateSecretError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors = map[string]error{
		"CreateRepoSecret": errors.New("forbidden"),
	}
	printer := ui.New(&bytes.Buffer{})

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", nil, nil, map[string]string{"S": "V"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting repo secret")
}

func TestApplyPerRepoScaffold_ProtectedBranchFallback(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".github/workflows/fullsend.yaml", Content: []byte("workflow"), Mode: "100644"},
	}
	repoVars := map[string]string{"K": "V"}
	repoSecrets := map[string]string{"S": "secret"}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, repoVars, repoSecrets)
	require.NoError(t, err)

	require.Len(t, client.CreatedBranches, 1)
	assert.Equal(t, "acme/widget/fullsend/scaffold-install", client.CreatedBranches[0])

	require.Len(t, client.CommittedFilesToBranch, 1)
	assert.Equal(t, "fullsend/scaffold-install", client.CommittedFilesToBranch[0].Branch)
	assert.Len(t, client.CommittedFilesToBranch[0].Files, 1)

	require.Len(t, client.CreatedProposals, 1)
	assert.Contains(t, client.CreatedProposals[0].Title, "fullsend")

	output := buf.String()
	assert.Contains(t, output, "protected")
	assert.Contains(t, output, "PR #1")
	assert.Contains(t, output, "Merge the PR")
}

func TestApplyPerRepoScaffold_ProtectedBranch_ExistingBranch(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateBranch"] = fmt.Errorf("branch: %w", forge.ErrAlreadyExists)
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil)
	require.NoError(t, err)

	require.Len(t, client.CommittedFilesToBranch, 1, "should proceed despite branch existing")
	require.Len(t, client.CreatedProposals, 1)
}

func TestApplyPerRepoScaffold_ProtectedBranch_StillSetsVarsAndSecrets(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}
	repoVars := map[string]string{"FULLSEND_MINT_URL": "https://mint.example.run.app"}
	repoSecrets := map[string]string{"FULLSEND_GCP_PROJECT_ID": "my-project"}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, repoVars, repoSecrets)
	require.NoError(t, err)

	assert.Len(t, client.Variables, 1, "variables should be set even with PR fallback")
	assert.Len(t, client.CreatedSecrets, 1, "secrets should be set even with PR fallback")
}

func TestApplyPerRepoScaffold_ProtectedBranch_CreateBranchFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateBranch"] = fmt.Errorf("forbidden")
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating scaffold branch")
}

func TestApplyPerRepoScaffold_ProtectedBranch_CommitToBranchFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CommitFilesToBranch"] = fmt.Errorf("server error")
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committing scaffold files to branch")
}

func TestApplyPerRepoScaffold_ProtectedBranch_ScaffoldBranchAlsoProtected(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CommitFilesToBranch"] = fmt.Errorf("%w: scaffold branch also protected", forge.ErrBranchProtected)
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scaffold branch")
	assert.Contains(t, err.Error(), "configure branch protection")
}

func TestApplyPerRepoScaffold_ProtectedBranch_CreatePRFails(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateChangeProposal"] = fmt.Errorf("forbidden")
	printer := ui.New(&bytes.Buffer{})

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating scaffold PR")
}

func TestApplyPerRepoScaffold_ProtectedBranch_DuplicatePR(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateChangeProposal"] = fmt.Errorf("pr: %w", forge.ErrAlreadyExists)
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "already exists")
	assert.Contains(t, output, "Merge the PR")
}

func TestLoadKnownSlugs_HarnessFilesPreferred(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents["myorg/.fullsend/harness@HEAD"] = []forge.DirectoryEntry{
		{Path: "harness/triage.yaml", Type: "file"},
		{Path: "harness/coder.yaml", Type: "file"},
	}
	client.FileContentsRef["myorg/.fullsend/harness/triage.yaml@HEAD"] = []byte("role: triage\nslug: fullsend-ai-triage\n")
	client.FileContentsRef["myorg/.fullsend/harness/coder.yaml@HEAD"] = []byte("role: coder\nslug: fullsend-ai-coder\n")

	// Also set up config.yaml agents: block — should NOT be used.
	client.FileContents["myorg/.fullsend/config.yaml"] = []byte(`version: "1"
agents:
  - role: triage
    slug: old-triage-slug
    name: old-triage
`)

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Equal(t, map[string]string{
		"triage": "fullsend-ai-triage",
		"coder":  "fullsend-ai-coder",
	}, slugs)
	assert.NotContains(t, buf.String(), "agents: block")
}

func TestLoadKnownSlugs_FallbackToAgentsBlock(t *testing.T) {
	client := forge.NewFakeClient()
	// No harness/ directory → ErrNotFound from DirContents.

	client.FileContents["myorg/.fullsend/config.yaml"] = []byte(`version: "1"
agents:
  - role: triage
    slug: fullsend-ai-triage
    name: fullsend-ai-triage
  - role: coder
    slug: fullsend-ai-coder
    name: fullsend-ai-coder
`)

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Equal(t, map[string]string{
		"triage": "fullsend-ai-triage",
		"coder":  "fullsend-ai-coder",
	}, slugs)
	assert.Contains(t, buf.String(), "agents: block")
}

func TestLoadKnownSlugs_HarnessFilesWithoutRoleSlug_FallsBack(t *testing.T) {
	client := forge.NewFakeClient()
	// Harness files exist but lack role/slug (legacy format).
	client.DirContents["myorg/.fullsend/harness@HEAD"] = []forge.DirectoryEntry{
		{Path: "harness/triage.yaml", Type: "file"},
	}
	client.FileContentsRef["myorg/.fullsend/harness/triage.yaml@HEAD"] = []byte("agent: agents/triage.md\nmodel: opus\n")

	client.FileContents["myorg/.fullsend/config.yaml"] = []byte(`version: "1"
agents:
  - role: triage
    slug: fullsend-ai-triage
    name: fullsend-ai-triage
`)

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Equal(t, map[string]string{
		"triage": "fullsend-ai-triage",
	}, slugs)
	assert.Contains(t, buf.String(), "agents: block")
}

func TestLoadKnownSlugs_NeitherSource_ReturnsNil(t *testing.T) {
	client := forge.NewFakeClient()
	// No harness/ dir, no config.yaml.

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Nil(t, slugs)
	assert.NotContains(t, buf.String(), "agents: block")
}

func TestLoadKnownSlugs_DuplicateRoles_FirstWins(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents["myorg/.fullsend/harness@HEAD"] = []forge.DirectoryEntry{
		{Path: "harness/code.yaml", Type: "file"},
		{Path: "harness/fix.yaml", Type: "file"},
	}
	// Both files declare role: coder. DiscoverRemoteAgents sorts by Role then
	// Filename, so code.yaml comes first.
	client.FileContentsRef["myorg/.fullsend/harness/code.yaml@HEAD"] = []byte("role: coder\nslug: fullsend-ai-coder\n")
	client.FileContentsRef["myorg/.fullsend/harness/fix.yaml@HEAD"] = []byte("role: coder\nslug: fullsend-ai-fix\n")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Equal(t, map[string]string{
		"coder": "fullsend-ai-coder",
	}, slugs)
	assert.Contains(t, buf.String(), "duplicate role")
}

func TestLoadKnownSlugs_PartialError_LogsWarning(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents["myorg/.fullsend/harness@HEAD"] = []forge.DirectoryEntry{
		{Path: "harness/triage.yaml", Type: "file"},
		{Path: "harness/bad.yaml", Type: "file"},
	}
	client.FileContentsRef["myorg/.fullsend/harness/triage.yaml@HEAD"] = []byte("role: triage\nslug: fullsend-ai-triage\n")
	// bad.yaml is not in FileContentsRef → GetFileContentAtRef returns ErrNotFound.

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Equal(t, map[string]string{
		"triage": "fullsend-ai-triage",
	}, slugs)
	assert.Contains(t, buf.String(), "harness discovery")
}

func TestLoadKnownSlugs_RoleWithoutSlug_WarnsAndSkips(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents["myorg/.fullsend/harness@HEAD"] = []forge.DirectoryEntry{
		{Path: "harness/triage.yaml", Type: "file"},
	}
	client.FileContentsRef["myorg/.fullsend/harness/triage.yaml@HEAD"] = []byte("role: triage\n")

	client.FileContents["myorg/.fullsend/config.yaml"] = []byte(`version: "1"
agents:
  - role: triage
    slug: fullsend-ai-triage
    name: fullsend-ai-triage
`)

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Equal(t, map[string]string{
		"triage": "fullsend-ai-triage",
	}, slugs)
	assert.Contains(t, buf.String(), "both must be set")
}

func TestLoadKnownSlugs_HardError_ZeroAgents_FallsBack(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["ListDirectoryContents"] = fmt.Errorf("network timeout")

	client.FileContents["myorg/.fullsend/config.yaml"] = []byte(`version: "1"
agents:
  - role: triage
    slug: fullsend-ai-triage
    name: fullsend-ai-triage
`)

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Equal(t, map[string]string{
		"triage": "fullsend-ai-triage",
	}, slugs)
	assert.Contains(t, buf.String(), "harness discovery")
	assert.Contains(t, buf.String(), "deprecated")
}

func TestLoadKnownSlugs_MalformedConfig_ReturnsNil(t *testing.T) {
	client := forge.NewFakeClient()
	// No harness/ dir, malformed config.yaml.
	client.FileContents["myorg/.fullsend/config.yaml"] = []byte("not: valid: yaml: [")

	var buf bytes.Buffer
	printer := ui.New(&buf)
	slugs := loadKnownSlugs(context.Background(), client, "myorg", forge.ConfigRepoName, "HEAD", printer)

	assert.Nil(t, slugs)
}

func TestApplyPerRepoScaffold_ProtectedBranch_BranchUpToDate(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.Errors["CommitFiles"] = fmt.Errorf("%w: github api: 422", forge.ErrBranchProtected)
	client.Errors["CreateChangeProposal"] = fmt.Errorf("PR: %w", forge.ErrAlreadyExists)
	noChange := false
	client.CommitFilesChanged = &noChange
	var buf bytes.Buffer
	printer := ui.New(&buf)

	files := []forge.TreeFile{
		{Path: ".fullsend/config.yaml", Content: []byte("cfg"), Mode: "100644"},
	}

	err := applyPerRepoScaffold(context.Background(), client, printer,
		"acme", "widget", files, nil, nil)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "up to date")
}
