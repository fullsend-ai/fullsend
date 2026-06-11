package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// --- Command tree tests ---

func TestGitHubCommand_HasSubcommands(t *testing.T) {
	cmd := newGitHubCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["setup"], "expected setup subcommand")
	assert.True(t, names["enroll"], "expected enroll subcommand")
	assert.True(t, names["unenroll"], "expected unenroll subcommand")
	assert.True(t, names["set"], "expected set subcommand")
	assert.True(t, names["status"], "expected status subcommand")
	assert.True(t, names["uninstall"], "expected uninstall subcommand")
	assert.True(t, names["sync-scaffold"], "expected sync-scaffold subcommand")
}

func TestGitHubCommand_RegisteredInRoot(t *testing.T) {
	cmd := newRootCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["github"], "expected github subcommand on root")
}

// --- Setup command tests ---

func TestGitHubSetupCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestGitHubSetupCmd_Flags(t *testing.T) {
	cmd := newGitHubSetupCmd()

	mintURLFlag := cmd.Flags().Lookup("mint-url")
	require.NotNil(t, mintURLFlag, "expected --mint-url flag")
	assert.Equal(t, DefaultMintURL, mintURLFlag.DefValue)

	agentsFlag := cmd.Flags().Lookup("agents")
	require.NotNil(t, agentsFlag, "expected --agents flag")
	assert.Equal(t, strings.Join(config.DefaultAgentRoles(), ","), agentsFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")

	skipAppSetupFlag := cmd.Flags().Lookup("skip-app-setup")
	require.NotNil(t, skipAppSetupFlag, "expected --skip-app-setup flag")

	publicFlag := cmd.Flags().Lookup("public")
	require.NotNil(t, publicFlag, "expected --public flag")

	appSetFlag := cmd.Flags().Lookup("app-set")
	require.NotNil(t, appSetFlag, "expected --app-set flag")
	assert.Equal(t, "fullsend-ai", appSetFlag.DefValue)

	enrollAllFlag := cmd.Flags().Lookup("enroll-all")
	require.NotNil(t, enrollAllFlag, "expected --enroll-all flag")

	enrollNoneFlag := cmd.Flags().Lookup("enroll-none")
	require.NotNil(t, enrollNoneFlag, "expected --enroll-none flag")

	vendorBinaryFlag := cmd.Flags().Lookup("vendor-fullsend-binary")
	require.NotNil(t, vendorBinaryFlag, "expected --vendor-fullsend-binary flag")

	inferenceProjectFlag := cmd.Flags().Lookup("inference-project")
	require.NotNil(t, inferenceProjectFlag, "expected --inference-project flag")

	inferenceRegionFlag := cmd.Flags().Lookup("inference-region")
	require.NotNil(t, inferenceRegionFlag, "expected --inference-region flag")
	assert.Equal(t, "global", inferenceRegionFlag.DefValue)

	inferenceWIFFlag := cmd.Flags().Lookup("inference-wif-provider")
	require.NotNil(t, inferenceWIFFlag, "expected --inference-wif-provider flag")
}

func TestGitHubSetupCmd_UsesDefaultMintURL(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	// Without explicit --mint-url, the default should be used and
	// validation should not fail on a missing URL. The command will
	// fail later (listing repos), but not with a "mint-url is required" error.
	cmd.SetArgs([]string{"github", "setup", "acme",
		"--enroll-none"})
	err := cmd.Execute()
	// The error should be from a downstream step (e.g. listing repos),
	// not from missing --mint-url.
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "--mint-url is required")
}

func TestGitHubSetupCmd_PerRepoRejectsPerOrgFlags(t *testing.T) {
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
			args := []string{"github", "setup", "acme/widget",
				"--mint-url", "https://mint-test-abc123.run.app"}
			if tc.value != "" {
				args = append(args, "--"+tc.flag, tc.value)
			} else {
				args = append(args, "--"+tc.flag)
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "only valid for per-org")
		})
	}
}

func TestGitHubSetupCmd_ValidatesMintURLHTTPS(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme",
		"--mint-url", "http://not-secure.run.app"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTPS URL")
}

func TestGitHubSetupCmd_PerRepoDryRun(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestGitHubSetupCmd_PerRepoRequiresInferenceProject(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app"})
	err := cmd.Execute()
	require.Error(t, err)
	// With a fake token the RepoSecretExists call fails, surfacing an API
	// error. Either the API-error path or the not-found path is acceptable
	// here — both mention the secret name or the flag.
	errMsg := err.Error()
	assert.True(t, strings.Contains(errMsg, "--inference-project") ||
		strings.Contains(errMsg, "FULLSEND_GCP_PROJECT_ID"),
		"expected error to mention --inference-project or FULLSEND_GCP_PROJECT_ID, got: %s", errMsg)
}

func TestGitHubSetupCmd_PerRepoRequiresWIFProvider(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project"})
	err := cmd.Execute()
	require.Error(t, err)
	errMsg := err.Error()
	assert.True(t, strings.Contains(errMsg, "--inference-wif-provider") ||
		strings.Contains(errMsg, "FULLSEND_GCP_WIF_PROVIDER"),
		"expected error to mention --inference-wif-provider or FULLSEND_GCP_WIF_PROVIDER, got: %s", errMsg)
}

// --- Enroll command tests ---

func TestGitHubEnrollCmd_RequiresOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "enroll"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least 1 arg")
}

func TestGitHubEnrollCmd_RequiresReposOrAllFlag(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "enroll", "testorg"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must specify repository names or use --all flag")
}

func TestGitHubEnrollCmd_HasAllFlag(t *testing.T) {
	cmd := newGitHubEnrollCmd()
	allFlag := cmd.Flags().Lookup("all")
	require.NotNil(t, allFlag, "expected --all flag")
	assert.Equal(t, "false", allFlag.DefValue)
}

func TestGitHubEnrollCmd_DelegatesCorrectly(t *testing.T) {
	cfg := setupTestConfig(map[string]bool{
		"web-app": false,
		"api":     false,
	})
	client := setupTestClient("testorg", cfg, []string{"web-app", "api"})
	printer := ui.New(&discardWriter{})

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true)
	require.NoError(t, err)

	require.Len(t, client.CreatedFiles, 1)
	updatedCfg, err := config.ParseOrgConfig(client.CreatedFiles[0].Content)
	require.NoError(t, err)
	assert.True(t, updatedCfg.Repos["web-app"].Enabled)
	assert.False(t, updatedCfg.Repos["api"].Enabled)
}

// --- Unenroll command tests ---

func TestGitHubUnenrollCmd_RequiresOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "unenroll"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires at least 1 arg")
}

func TestGitHubUnenrollCmd_RequiresReposOrAllFlag(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "unenroll", "testorg"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must specify repository names or use --all flag")
}

func TestGitHubUnenrollCmd_HasFlags(t *testing.T) {
	cmd := newGitHubUnenrollCmd()
	allFlag := cmd.Flags().Lookup("all")
	require.NotNil(t, allFlag, "expected --all flag")
	yoloFlag := cmd.Flags().Lookup("yolo")
	require.NotNil(t, yoloFlag, "expected --yolo flag")
}

// --- Set command tests ---

func TestGitHubSetCmd_RequiresArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "set"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 3 arg(s)")
}

func TestGitHubSetCmd_RejectsUnknownKey(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "set", "acme", "UNKNOWN_KEY", "some-value"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
	assert.Contains(t, err.Error(), "FULLSEND_GCP_REGION")
}

func TestGitHubSetCmd_RejectsMintURL(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSet(context.Background(), client, printer, "acme", "FULLSEND_MINT_URL", "https://new-mint.run.app/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
}

func TestGitHubSetCmd_SetsRepoVariable(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSet(context.Background(), client, printer, "acme/widget", "FULLSEND_GCP_REGION", "us-east5")
	require.NoError(t, err)

	require.Len(t, client.Variables, 1)
	assert.Equal(t, "FULLSEND_GCP_REGION", client.Variables[0].Name)
	assert.Equal(t, "us-east5", client.Variables[0].Value)
	assert.Equal(t, "acme", client.Variables[0].Owner)
	assert.Equal(t, "widget", client.Variables[0].Repo)
}

func TestGitHubSetCmd_SetsRepoSecret(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSet(context.Background(), client, printer, "acme/widget", "FULLSEND_GCP_PROJECT_ID", "my-project-123")
	require.NoError(t, err)

	require.Len(t, client.CreatedSecrets, 1)
	assert.Equal(t, "FULLSEND_GCP_PROJECT_ID", client.CreatedSecrets[0].Name)
	assert.Equal(t, "my-project-123", client.CreatedSecrets[0].Value)
}

func TestConfigKeyMapping_AllKeys(t *testing.T) {
	expectedKeys := []string{
		"FULLSEND_GCP_REGION",
		forge.PerRepoGuardVar,
		"FULLSEND_GCP_PROJECT_ID",
		"FULLSEND_GCP_WIF_PROVIDER",
	}
	for _, key := range expectedKeys {
		_, ok := configKeyMapping[key]
		assert.True(t, ok, "expected key %s in configKeyMapping", key)
	}
	info := configKeyMapping[forge.PerRepoGuardVar]
	assert.Equal(t, storageVariable, info.storage)
}

func TestGitHubSetCmd_ValidatesWIFProvider(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSet(context.Background(), client, printer, "acme/widget", "FULLSEND_GCP_WIF_PROVIDER", "garbage")
	require.Error(t, err)
}

func TestGitHubSetCmd_ValidatesTarget(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSet(context.Background(), client, printer, "-invalid", "FULLSEND_GCP_REGION", "us-east5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot start or end with a hyphen")
}

func TestGitHubSetCmd_ValidatesRepoTarget(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSet(context.Background(), client, printer, "/repo", "FULLSEND_GCP_REGION", "us-east5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid owner name")
}

func TestRunGitHubStatus_NonNotFoundError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors = map[string]error{
		"GetRepo": fmt.Errorf("permission denied"),
	}
	printer := ui.New(&discardWriter{})

	err := runGitHubStatus(context.Background(), client, printer, "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking config repo")
}

// --- Status command tests ---

func TestGitHubStatusCmd_RequiresOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "status"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestGitHubStatusCmd_ValidatesOrg(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	// Use "--" to prevent cobra from parsing the org name as a flag.
	cmd.SetArgs([]string{"github", "status", "--", "-leading"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot start or end with a hyphen")
}

func TestRunGitHubStatus_BasicReport(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: ".fullsend", FullName: "acme/.fullsend"},
	}
	cfg := config.NewOrgConfig([]string{"widget"}, []string{"widget"}, []string{"triage"}, nil, "")
	cfgData, _ := cfg.Marshal()
	client.FileContents["acme/.fullsend/config.yaml"] = cfgData
	client.OrgVariables = map[string]bool{"acme/FULLSEND_MINT_URL": true}
	printer := ui.New(&discardWriter{})

	err := runGitHubStatus(context.Background(), client, printer, "acme")
	require.NoError(t, err)
}

func TestRunGitHubStatus_NoConfigRepo(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubStatus(context.Background(), client, printer, "acme")
	require.NoError(t, err)
}

// --- Uninstall command tests ---

func TestGitHubUninstallCmd_RequiresOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "uninstall"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestGitHubUninstallCmd_HasFlags(t *testing.T) {
	cmd := newGitHubUninstallCmd()
	yoloFlag := cmd.Flags().Lookup("yolo")
	require.NotNil(t, yoloFlag, "expected --yolo flag")
	appSetFlag := cmd.Flags().Lookup("app-set")
	require.NotNil(t, appSetFlag, "expected --app-set flag")
}

func TestRunGitHubUninstall_DeletesResources(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: ".fullsend", FullName: "acme/.fullsend"},
	}
	client.OrgVariables = map[string]bool{"acme/FULLSEND_MINT_URL": true}
	printer := ui.New(&discardWriter{})

	err := runGitHubUninstall(context.Background(), client, printer, "acme", "fullsend-ai")
	require.NoError(t, err)

	// Verify repo was deleted.
	assert.Contains(t, client.DeletedRepos, "acme/.fullsend")
	// Verify org variable was deleted.
	assert.Contains(t, client.DeletedOrgVariables, "acme/FULLSEND_MINT_URL")
}

func TestRunGitHubUninstall_NoConfigRepo(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubUninstall(context.Background(), client, printer, "acme", "fullsend-ai")
	require.NoError(t, err)
}

// --- Sync-scaffold command tests ---

func TestGitHubSyncScaffoldCmd_RequiresOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "sync-scaffold"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestRunGitHubSyncScaffold_CommitsFiles(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: ".fullsend", FullName: "acme/.fullsend"},
	}
	client.AuthenticatedUser = "testuser"
	printer := ui.New(&discardWriter{})

	err := runGitHubSyncScaffold(context.Background(), client, printer, "acme")
	require.NoError(t, err)

	// Verify at least one file was committed.
	require.NotEmpty(t, client.CommittedFiles, "expected scaffold files to be committed")
}

// --- parseTarget tests ---

func TestParseTarget_Org(t *testing.T) {
	owner, repo, isRepo := parseTarget("acme")
	assert.Equal(t, "acme", owner)
	assert.Equal(t, "", repo)
	assert.False(t, isRepo)
}

func TestParseTarget_Repo(t *testing.T) {
	owner, repo, isRepo := parseTarget("acme/widget")
	assert.Equal(t, "acme", owner)
	assert.Equal(t, "widget", repo)
	assert.True(t, isRepo)
}

// --- Per-repo setup business logic tests ---

func TestRunGitHubSetupPerRepo(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"repo", "workflow"}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
	})
	require.NoError(t, err)

	// Verify scaffold files were committed.
	require.NotEmpty(t, client.CommittedFiles)

	// Verify repo variables were set.
	varNames := make(map[string]string)
	for _, v := range client.Variables {
		varNames[v.Name] = v.Value
	}
	assert.Equal(t, "https://mint-test-abc123.run.app", varNames["FULLSEND_MINT_URL"])
	assert.Equal(t, "global", varNames["FULLSEND_GCP_REGION"])
	assert.Equal(t, "true", varNames["FULLSEND_PER_REPO_INSTALL"])

	// Verify repo secrets were set.
	secretNames := make(map[string]string)
	for _, s := range client.CreatedSecrets {
		secretNames[s.Name] = s.Value
	}
	assert.Equal(t, "my-project", secretNames["FULLSEND_GCP_PROJECT_ID"])
	assert.Contains(t, secretNames, "FULLSEND_GCP_WIF_PROVIDER")
}

func TestGitHubSetCmd_OrgTargetDefaultsToConfigRepo(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSet(context.Background(), client, printer, "acme", "FULLSEND_GCP_REGION", "us-east5")
	require.NoError(t, err)

	// Org target should default to .fullsend repo.
	require.Len(t, client.Variables, 1)
	assert.Equal(t, "FULLSEND_GCP_REGION", client.Variables[0].Name)
	assert.Equal(t, "us-east5", client.Variables[0].Value)
	assert.Equal(t, "acme", client.Variables[0].Owner)
	assert.Equal(t, forge.ConfigRepoName, client.Variables[0].Repo)
}

func TestGitHubSetCmd_OrgTargetSecretDefaultsToConfigRepo(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSet(context.Background(), client, printer, "acme", "FULLSEND_GCP_PROJECT_ID", "my-project")
	require.NoError(t, err)

	require.Len(t, client.CreatedSecrets, 1)
	assert.Equal(t, "FULLSEND_GCP_PROJECT_ID", client.CreatedSecrets[0].Name)
	assert.Equal(t, "my-project", client.CreatedSecrets[0].Value)
	assert.Equal(t, "acme", client.CreatedSecrets[0].Owner)
	assert.Equal(t, forge.ConfigRepoName, client.CreatedSecrets[0].Repo)
}

func TestRunGitHubUninstall_ListInstallationsError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: ".fullsend", FullName: "acme/.fullsend"},
	}
	client.Errors = map[string]error{
		"ListOrgInstallations": fmt.Errorf("insufficient permissions"),
	}
	printer := ui.New(&discardWriter{})

	err := runGitHubUninstall(context.Background(), client, printer, "acme", "fullsend-ai")
	require.NoError(t, err)

	// Verify repo was still deleted despite ListOrgInstallations failure.
	assert.Contains(t, client.DeletedRepos, "acme/.fullsend")
}

func TestParseTarget_MultipleSlashes(t *testing.T) {
	owner, repo, isRepo := parseTarget("acme/widget/extra")
	assert.Equal(t, "acme", owner)
	assert.Equal(t, "widget/extra", repo)
	assert.True(t, isRepo)
}

func TestParseTarget_EmptyString(t *testing.T) {
	owner, repo, isRepo := parseTarget("")
	assert.Equal(t, "", owner)
	assert.Equal(t, "", repo)
	assert.False(t, isRepo)
}

func TestParseTarget_JustSlash(t *testing.T) {
	owner, repo, isRepo := parseTarget("/")
	assert.Equal(t, "", owner)
	assert.Equal(t, "", repo)
	assert.True(t, isRepo)
}

func TestRunGitHubSetupPerRepo_DryRun(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		dryRun:               true,
	})
	require.NoError(t, err)

	// Verify nothing was actually written.
	assert.Empty(t, client.CommittedFiles)
	assert.Empty(t, client.Variables)
	assert.Empty(t, client.CreatedSecrets)
}

func TestRunGitHubSetupPerRepo_ReusesExistingSecrets(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"repo", "workflow"}
	// Pre-populate secrets as if a previous run stored them.
	client.Secrets = map[string]bool{
		"acme/widget/FULLSEND_GCP_PROJECT_ID":   true,
		"acme/widget/FULLSEND_GCP_WIF_PROVIDER": true,
	}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:          "acme/widget",
		mintURL:         "https://mint-test-abc123.run.app",
		inferenceRegion: "global",
		agents:          strings.Join(config.PerRepoDefaultRoles(), ","),
		// inferenceProject and inferenceWIFProvider intentionally omitted.
	})
	require.NoError(t, err)

	// Verify scaffold files were committed.
	require.NotEmpty(t, client.CommittedFiles)

	// Verify repo variables were set.
	varNames := make(map[string]string)
	for _, v := range client.Variables {
		varNames[v.Name] = v.Value
	}
	assert.Equal(t, "https://mint-test-abc123.run.app", varNames["FULLSEND_MINT_URL"])
	assert.Equal(t, "global", varNames["FULLSEND_GCP_REGION"])
	assert.Equal(t, "true", varNames["FULLSEND_PER_REPO_INSTALL"])

	// Verify no secrets were overwritten.
	assert.Empty(t, client.CreatedSecrets)
}

func TestRunGitHubSetupPerRepo_PartialReuse_ProjectOnly(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"repo", "workflow"}
	// Only the project secret exists; WIF is provided via flag.
	client.Secrets = map[string]bool{
		"acme/widget/FULLSEND_GCP_PROJECT_ID": true,
	}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceRegion:      "global",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
	})
	require.NoError(t, err)

	// Verify only WIF secret was written (project was reused).
	secretNames := make(map[string]string)
	for _, s := range client.CreatedSecrets {
		secretNames[s.Name] = s.Value
	}
	assert.NotContains(t, secretNames, "FULLSEND_GCP_PROJECT_ID")
	assert.Contains(t, secretNames, "FULLSEND_GCP_WIF_PROVIDER")
}

func TestRunGitHubSetupPerRepo_MissingFlagNoExistingSecret(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	// No existing secrets and no flags — should fail.
	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:          "acme/widget",
		mintURL:         "https://mint-test-abc123.run.app",
		inferenceRegion: "global",
		agents:          strings.Join(config.PerRepoDefaultRoles(), ","),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-project is required")
}

func TestRunGitHubSetupPerRepo_MissingWIFNoExistingSecret(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	// Project flag provided, WIF missing with no existing secret.
	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:           "acme/widget",
		mintURL:          "https://mint-test-abc123.run.app",
		inferenceProject: "my-project",
		inferenceRegion:  "global",
		agents:           strings.Join(config.PerRepoDefaultRoles(), ","),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-wif-provider is required")
}

func TestRunGitHubSetupPerRepo_PartialReuse_WIFOnly(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.TokenScopes = []string{"repo", "workflow"}
	// Only the WIF secret exists; project is provided via flag.
	client.Secrets = map[string]bool{
		"acme/widget/FULLSEND_GCP_WIF_PROVIDER": true,
	}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:           "acme/widget",
		mintURL:          "https://mint-test-abc123.run.app",
		inferenceRegion:  "global",
		inferenceProject: "my-project",
		agents:           strings.Join(config.PerRepoDefaultRoles(), ","),
	})
	require.NoError(t, err)

	// Verify only project secret was written (WIF was reused).
	secretNames := make(map[string]string)
	for _, s := range client.CreatedSecrets {
		secretNames[s.Name] = s.Value
	}
	assert.Contains(t, secretNames, "FULLSEND_GCP_PROJECT_ID")
	assert.NotContains(t, secretNames, "FULLSEND_GCP_WIF_PROVIDER")
}

func TestRunGitHubSetupPerRepo_SecretCheckError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors = map[string]error{
		"RepoSecretExists": fmt.Errorf("API rate limit exceeded"),
	}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:          "acme/widget",
		mintURL:         "https://mint-test-abc123.run.app",
		inferenceRegion: "global",
		agents:          strings.Join(config.PerRepoDefaultRoles(), ","),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API rate limit exceeded")
	assert.Contains(t, err.Error(), "checking existing secret")
}
