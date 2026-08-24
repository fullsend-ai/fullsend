package cli

import (
	"bytes"
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
	assert.Equal(t, "", mintURLFlag.DefValue, "flag default should be empty; code default provides the value")

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

	vendorFlag := cmd.Flags().Lookup("vendor")
	require.NotNil(t, vendorFlag, "expected --vendor flag")

	directFlag := cmd.Flags().Lookup("direct")
	require.NotNil(t, directFlag, "expected --direct flag")
	assert.Equal(t, "false", directFlag.DefValue)

	inferenceProviderFlag := cmd.Flags().Lookup("inference-provider")
	require.NotNil(t, inferenceProviderFlag, "expected --inference-provider flag")
	assert.Equal(t, "", inferenceProviderFlag.DefValue, "flag default should be empty; code default provides the value")

	inferenceProjectFlag := cmd.Flags().Lookup("inference-project")
	require.NotNil(t, inferenceProjectFlag, "expected --inference-project flag")

	inferenceRegionFlag := cmd.Flags().Lookup("inference-region")
	require.NotNil(t, inferenceRegionFlag, "expected --inference-region flag")
	assert.Equal(t, "", inferenceRegionFlag.DefValue, "flag default should be empty; code default provides the value")

	inferenceWIFFlag := cmd.Flags().Lookup("inference-wif-provider")
	require.NotNil(t, inferenceWIFFlag, "expected --inference-wif-provider flag")

	signoffFlag := cmd.Flags().Lookup("signoff")
	require.NotNil(t, signoffFlag, "expected --signoff flag")
	assert.Equal(t, "false", signoffFlag.DefValue)
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

func TestGitHubSetupCmd_PerRepoDryRun_Vendor(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--dry-run",
		"--vendor"})
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

	err := runEnableRepos(context.Background(), client, printer, "testorg", []string{"web-app"}, false, true, false)
	require.NoError(t, err)

	require.Len(t, client.CreatedFiles, 1)
	updatedCfg, err := config.ParseOrgConfig(client.CreatedFiles[0].Content)
	require.NoError(t, err)
	assert.True(t, updatedCfg.RepoMap()["web-app"].Enabled)
	assert.False(t, updatedCfg.RepoMap()["api"].Enabled)
}

// --- buildPresetOverlay tests ---

func TestBuildPresetOverlay_NoFlagsChanged(t *testing.T) {
	cfg := githubSetupConfig{
		mintURL:         DefaultMintURL,
		inferenceRegion: "global",
		changedFlags:    map[string]bool{},
	}
	overlay := buildPresetOverlay(cfg)

	// No flags changed: returns nil so the caller uses stubConfigYAML.
	assert.Nil(t, overlay)
}

func TestBuildPresetOverlay_FlagsPopulateOverlay(t *testing.T) {
	cfg := githubSetupConfig{
		mintURL:              "https://custom-mint.example.com",
		inferenceProvider:    "vertex",
		inferenceProject:     "custom-project",
		inferenceRegion:      "us-west2",
		inferenceWIFProvider: "projects/789/locations/global/workloadIdentityPools/pool/providers/prov",
		changedFlags: map[string]bool{
			"mint-url":               true,
			"inference-provider":     true,
			"inference-project":      true,
			"inference-region":       true,
			"inference-wif-provider": true,
		},
	}
	overlay := buildPresetOverlay(cfg)

	assert.Equal(t, "https://custom-mint.example.com", overlay.ConfigMintURL())
	assert.Equal(t, "vertex", overlay.ConfigInferenceProvider())
	assert.Equal(t, "custom-project", overlay.ConfigInferenceProject())
	assert.Equal(t, "us-west2", overlay.ConfigInferenceRegion())
	assert.Equal(t, "projects/789/locations/global/workloadIdentityPools/pool/providers/prov", overlay.ConfigInferenceWIFProvider())
}

func TestBuildPresetOverlay_PartialFlags(t *testing.T) {
	cfg := githubSetupConfig{
		mintURL:          "https://custom-mint.example.com",
		inferenceProject: "custom-project",
		inferenceRegion:  "global",
		changedFlags: map[string]bool{
			"mint-url": true,
			// inference-project, inference-region, inference-wif-provider not changed
		},
	}
	overlay := buildPresetOverlay(cfg)

	// Only mint-url was changed, so only it should be locally set in
	// the overlay.  Other accessors resolve through the parent chain
	// to code defaults.
	assert.Equal(t, "https://custom-mint.example.com", overlay.ConfigMintURL())
	assert.Equal(t, config.DefaultPerRepoInferenceProvider, overlay.ConfigInferenceProvider())
	assert.Equal(t, "", overlay.ConfigInferenceProject())
	assert.Equal(t, config.DefaultPerRepoInferenceRegion, overlay.ConfigInferenceRegion())
	assert.Equal(t, "", overlay.ConfigInferenceWIFProvider())

	// Marshal should emit ONLY the locally-set field (mint_url).
	data, err := overlay.Marshal()
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, "mint_url:")
	assert.NotContains(t, s, "inference:")
}

// --- No-preset: only changed flags go into config.yaml ---

func TestRunGitHubSetupPerRepo_NoPreset_NoMintInferenceFlags(t *testing.T) {
	// When no --mint-url, --inference-provider, or --inference-region
	// flags are set, config.yaml should NOT contain those values —
	// they are resolved from code defaults at runtime.
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	client.Secrets = map[string]bool{
		"acme/widget/FULLSEND_GCP_PROJECT_ID":   true,
		"acme/widget/FULLSEND_GCP_WIF_PROVIDER": true,
	}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		changedFlags: map[string]bool{}, // no flags changed
	})
	require.NoError(t, err)

	// config.yaml should NOT contain mint/inference values.
	var cfgContent []byte
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			if f.Path == ".fullsend/config.yaml" {
				cfgContent = f.Content
				break
			}
		}
	}
	require.NotEmpty(t, cfgContent, "expected .fullsend/config.yaml in committed files")
	s := string(cfgContent)
	assert.NotContains(t, s, "mint_url:")
	assert.NotContains(t, s, "inference:")

	// Dual-write vars should use resolved code defaults.
	varNames := make(map[string]string)
	for _, v := range client.Variables {
		varNames[v.Name] = v.Value
	}
	assert.Equal(t, config.DefaultPerRepoMintURL, varNames["FULLSEND_MINT_URL"])
	assert.Equal(t, config.DefaultPerRepoInferenceRegion, varNames["FULLSEND_GCP_REGION"])
}

func TestRunGitHubSetupPerRepo_NoPreset_ExplicitFlags(t *testing.T) {
	// When flags are explicitly set, only those values go into
	// config.yaml.  Dual-write vars use the flag values.
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://custom-mint.example.com",
		inferenceProvider:    "vertex",
		inferenceProject:     "my-project",
		inferenceRegion:      "us-west2",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		changedFlags: map[string]bool{
			"mint-url":               true,
			"inference-provider":     true,
			"inference-project":      true,
			"inference-region":       true,
			"inference-wif-provider": true,
		},
	})
	require.NoError(t, err)

	// config.yaml should contain the explicitly-set values.
	var cfgContent []byte
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			if f.Path == ".fullsend/config.yaml" {
				cfgContent = f.Content
				break
			}
		}
	}
	require.NotEmpty(t, cfgContent, "expected .fullsend/config.yaml in committed files")
	cfg, parseErr := config.ParsePerRepoConfig(cfgContent)
	require.NoError(t, parseErr)
	assert.Equal(t, "https://custom-mint.example.com", cfg.ConfigMintURL())
	assert.Equal(t, "vertex", cfg.ConfigInferenceProvider())
	assert.Equal(t, "my-project", cfg.ConfigInferenceProject())
	assert.Equal(t, "us-west2", cfg.ConfigInferenceRegion())

	// Dual-write vars use the flag values.
	varNames := make(map[string]string)
	for _, v := range client.Variables {
		varNames[v.Name] = v.Value
	}
	assert.Equal(t, "https://custom-mint.example.com", varNames["FULLSEND_MINT_URL"])
	assert.Equal(t, "us-west2", varNames["FULLSEND_GCP_REGION"])
}

func TestRunGitHubSetupPerRepo_NoPreset_PartialFlags(t *testing.T) {
	// When only --mint-url is set, only mint_url goes into config.yaml.
	// Inference region var uses the code default.
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	client.Secrets = map[string]bool{
		"acme/widget/FULLSEND_GCP_PROJECT_ID":   true,
		"acme/widget/FULLSEND_GCP_WIF_PROVIDER": true,
	}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:  "acme/widget",
		mintURL: "https://custom-mint.example.com",
		agents:  strings.Join(config.PerRepoDefaultRoles(), ","),
		changedFlags: map[string]bool{
			"mint-url": true,
		},
	})
	require.NoError(t, err)

	// config.yaml should contain only mint_url.
	var cfgContent []byte
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			if f.Path == ".fullsend/config.yaml" {
				cfgContent = f.Content
				break
			}
		}
	}
	require.NotEmpty(t, cfgContent)
	s := string(cfgContent)
	assert.Contains(t, s, "mint_url:")
	assert.NotContains(t, s, "inference:")

	// Vars: mint_url from flag, region from code default.
	varNames := make(map[string]string)
	for _, v := range client.Variables {
		varNames[v.Name] = v.Value
	}
	assert.Equal(t, "https://custom-mint.example.com", varNames["FULLSEND_MINT_URL"])
	assert.Equal(t, config.DefaultPerRepoInferenceRegion, varNames["FULLSEND_GCP_REGION"])
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
		"FULLSEND_REVIEW_CLIENT_ID",
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

	reviewInfo := configKeyMapping["FULLSEND_REVIEW_CLIENT_ID"]
	assert.Equal(t, storageVariable, reviewInfo.storage)
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
	cfg := config.NewOrgConfig([]string{"widget"}, []string{"widget"}, []string{"triage"}, "", "")
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

func TestRunGitHubUninstall_NonGitHub_SkipsAppUninstall(t *testing.T) {
	inner := forge.NewFakeClient()
	client := &nonGitHubClient{Client: inner}
	var buf strings.Builder
	printer := ui.New(&buf)

	err := runGitHubUninstall(context.Background(), client, printer, "acme", "fullsend-ai")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "App uninstall is not available on this forge")
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

func TestRunGitHubUninstall_UsesHarnessDiscovery(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: ".fullsend", FullName: "acme/.fullsend"},
	}
	// Provide config.yaml with agents: block (should be bypassed).
	client.FileContents = map[string][]byte{
		"acme/.fullsend/config.yaml": []byte("version: v1\ndispatch:\n  platform: github-actions\nagents:\n  - role: triage\n    slug: old-triage\n"),
	}
	// Provide harness directory with wrapper files.
	client.DirContents = map[string][]forge.DirectoryEntry{
		"acme/.fullsend/harness@main": {
			{Path: "harness/triage.yaml", Type: "file"},
		},
	}
	client.FileContentsRef = map[string][]byte{
		"acme/.fullsend/harness/triage.yaml@main": []byte("role: triage\nslug: harness-triage\n"),
	}
	client.Installations = []forge.Installation{
		{ID: 1, AppSlug: "harness-triage"},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	err := runGitHubUninstall(context.Background(), client, printer, "acme", "fullsend-ai")
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "harness-triage")
	assert.NotContains(t, output, "old-triage")
	assert.NotContains(t, output, "agents: block")
}

func TestRunGitHubUninstall_NoHarnessFiles_FallsBackToDefaultNaming(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: ".fullsend", FullName: "acme/.fullsend"},
	}
	client.FileContents = map[string][]byte{
		"acme/.fullsend/config.yaml": []byte("version: v1\ndispatch:\n  platform: github-actions\n"),
	}
	client.Installations = []forge.Installation{
		{ID: 1, AppSlug: "fullsend-ai-triage"},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	err := runGitHubUninstall(context.Background(), client, printer, "acme", "fullsend-ai")
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "fullsend-ai-triage")
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

	err := runGitHubSyncScaffold(context.Background(), client, printer, "acme", true)
	require.NoError(t, err)

	// sync-scaffold uses direct mode — files are committed to the default branch.
	require.NotEmpty(t, client.CommittedFiles, "expected scaffold files to be committed directly")
}

func TestRunGitHubSyncScaffold_VendoredMarker(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: ".fullsend", FullName: "acme/.fullsend"},
	}
	client.AuthenticatedUser = "testuser"
	client.FileContents = map[string][]byte{
		"acme/.fullsend/.defaults/action.yml": []byte("marker"),
		"acme/.fullsend/config.yaml":          []byte("repos: {}\n"),
	}
	printer := ui.New(&discardWriter{})

	err := runGitHubSyncScaffold(context.Background(), client, printer, "acme", true)
	require.NoError(t, err)
	require.NotEmpty(t, client.CommittedFiles)
}

func TestRunGitHubSyncScaffold_InvalidConfig(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{Name: ".fullsend", FullName: "acme/.fullsend"}}
	client.AuthenticatedUser = "testuser"
	client.FileContents = map[string][]byte{
		"acme/.fullsend/config.yaml": []byte("not: valid: yaml: ["),
	}
	printer := ui.New(&discardWriter{})

	err := runGitHubSyncScaffold(context.Background(), client, printer, "acme", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config.yaml")
}

func TestRunGitHubSyncScaffold_DefaultCreatesPR(t *testing.T) {
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{
		{Name: ".fullsend", FullName: "acme/.fullsend", DefaultBranch: "main"},
	}
	client.AuthenticatedUser = "acme"
	printer := ui.New(&discardWriter{})

	// direct=false means PR-based delivery (the default).
	err := runGitHubSyncScaffold(context.Background(), client, printer, "acme", false)
	require.NoError(t, err)

	// Should create a branch and PR, not commit directly.
	assert.NotEmpty(t, client.CreatedBranches, "expected a scaffold branch to be created")
	assert.NotEmpty(t, client.CreatedProposals, "expected a scaffold PR to be created")
	assert.Empty(t, client.CommittedFiles, "expected no direct commits when using PR delivery")
}

func TestGitHubSyncScaffoldCmd_HasDirectFlag(t *testing.T) {
	cmd := newGitHubSyncScaffoldCmd()
	directFlag := cmd.Flags().Lookup("direct")
	require.NotNil(t, directFlag, "expected --direct flag")
	assert.Equal(t, "false", directFlag.DefValue)
	// --pr flag should not exist; PR delivery is the default.
	assert.Nil(t, cmd.Flags().Lookup("pr"), "unexpected --pr flag; PR delivery is the default")
}

func TestGitHubEnrollCmd_HasDirectFlag(t *testing.T) {
	cmd := newGitHubEnrollCmd()
	directFlag := cmd.Flags().Lookup("direct")
	require.NotNil(t, directFlag, "expected --direct flag")
	assert.Equal(t, "false", directFlag.DefValue)
	// --pr flag should not exist; PR delivery is the default.
	assert.Nil(t, cmd.Flags().Lookup("pr"), "unexpected --pr flag; PR delivery is the default")
}

func TestGitHubUnenrollCmd_HasDirectFlag(t *testing.T) {
	cmd := newGitHubUnenrollCmd()
	directFlag := cmd.Flags().Lookup("direct")
	require.NotNil(t, directFlag, "expected --direct flag")
	// --pr flag should not exist; PR delivery is the default.
	assert.Nil(t, cmd.Flags().Lookup("pr"), "unexpected --pr flag; PR delivery is the default")
}

func TestRunGitHubSetupPerOrg_DryRun(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "testuser"
	client.Repos = []forge.Repository{
		{Name: forge.ConfigRepoName, FullName: "acme/" + forge.ConfigRepoName},
		{Name: "widget", FullName: "acme/widget"},
	}
	var buf strings.Builder
	err := runGitHubSetupPerOrg(context.Background(), client, ui.New(&buf), githubSetupConfig{
		target:               "acme",
		mintURL:              "https://mint.example.com/v1/token",
		agents:               strings.Join(config.DefaultAgentRoles(), ","),
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		dryRun:               true,
		enrollNone:           true,
		skipAppSetup:         true,
		vendor:               true,
	})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Layer: vendor")
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
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		changedFlags: map[string]bool{
			"mint-url":               true,
			"inference-project":      true,
			"inference-wif-provider": true,
			"inference-region":       true,
		},
	})
	require.NoError(t, err)

	// Default mode delivers via PR — verify files were committed to the scaffold branch.
	require.NotEmpty(t, client.CommittedFilesToBranch)
	require.NotEmpty(t, client.CreatedProposals)

	// Verify repo variables were set (dual-write alongside config.yaml
	// for backward compatibility with existing workflow templates).
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

func TestRunGitHubSetupPerRepo_WritesReviewClientID(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}

	// Configure GetAppClientID to return the review app's client ID.
	client.AppClientIDs = map[string]string{
		"fullsend-ai-review": "Iv23li1nIorNLIQy6NWK",
	}

	err := runGitHubSetupPerRepo(context.Background(), client, ui.New(&discardWriter{}), githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		appSet:               "fullsend-ai",
		changedFlags: map[string]bool{
			"mint-url":               true,
			"inference-project":      true,
			"inference-wif-provider": true,
			"inference-region":       true,
		},
	})
	require.NoError(t, err)

	varNames := make(map[string]string)
	for _, v := range client.Variables {
		varNames[v.Name] = v.Value
	}
	assert.Equal(t, "Iv23li1nIorNLIQy6NWK", varNames["FULLSEND_REVIEW_CLIENT_ID"])
}

func TestRunGitHubSetupPerRepo_SkipsReviewClientIDOnLookupFailure(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	// No AppClientIDs configured → GetAppClientID returns ErrNotFound.

	err := runGitHubSetupPerRepo(context.Background(), client, ui.New(&discardWriter{}), githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		appSet:               "fullsend-ai",
		changedFlags: map[string]bool{
			"mint-url":               true,
			"inference-project":      true,
			"inference-wif-provider": true,
			"inference-region":       true,
		},
	})
	require.NoError(t, err)

	// Verify that FULLSEND_REVIEW_CLIENT_ID was NOT set (lookup failed).
	for _, v := range client.Variables {
		if v.Name == "FULLSEND_REVIEW_CLIENT_ID" {
			t.Error("FULLSEND_REVIEW_CLIENT_ID should not be set when GetAppClientID fails")
		}
	}
}

func TestResolveReviewAppClientID_Success(t *testing.T) {
	client := forge.NewFakeClient()
	client.AppClientIDs = map[string]string{
		"fullsend-ai-review": "Iv23li1nIorNLIQy6NWK",
	}
	got := resolveReviewAppClientID(context.Background(), client, "fullsend-ai")
	assert.Equal(t, "Iv23li1nIorNLIQy6NWK", got)
}

func TestResolveReviewAppClientID_CustomAppSet(t *testing.T) {
	client := forge.NewFakeClient()
	client.AppClientIDs = map[string]string{
		"custom-review": "Iv1.custom123",
	}
	got := resolveReviewAppClientID(context.Background(), client, "custom")
	assert.Equal(t, "Iv1.custom123", got)
}

func TestResolveReviewAppClientID_AppNotFound(t *testing.T) {
	client := forge.NewFakeClient()
	// No AppClientIDs configured.
	got := resolveReviewAppClientID(context.Background(), client, "fullsend-ai")
	assert.Equal(t, "", got)
}

func TestResolveReviewAppClientID_APIError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors = map[string]error{
		"GetAppClientID": fmt.Errorf("rate limit exceeded"),
	}
	got := resolveReviewAppClientID(context.Background(), client, "fullsend-ai")
	assert.Equal(t, "", got)
}

// newSignoffTestSetup returns a pre-configured fake client and base config
// for signoff tests. Override fields on the returned values as needed.
func newSignoffTestSetup(t *testing.T) (*forge.FakeClient, githubSetupConfig) {
	t.Helper()
	t.Setenv("GH_TOKEN", "test-token")

	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.AuthenticatedUserIdentity = &forge.UserIdentity{
		Name:  "Test User",
		Email: "test@example.com",
	}
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}

	cfg := githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
	}
	return client, cfg
}

func TestRunGitHubSetupPerRepo_SignoffAddsTrailer(t *testing.T) {
	client, cfg := newSignoffTestSetup(t)
	cfg.signoff = true
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, cfg)
	require.NoError(t, err)

	// Verify the commit message contains the Signed-off-by trailer.
	require.NotEmpty(t, client.CommittedFilesToBranch)
	commitMsg := client.CommittedFilesToBranch[0].Message
	assert.Contains(t, commitMsg, "Signed-off-by: Test User <test@example.com>")
}

func TestRunGitHubSetupPerRepo_WithoutSignoffOmitsTrailer(t *testing.T) {
	client, cfg := newSignoffTestSetup(t)
	cfg.signoff = false
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, cfg)
	require.NoError(t, err)

	// Verify the commit message does NOT contain a Signed-off-by trailer.
	require.NotEmpty(t, client.CommittedFilesToBranch)
	commitMsg := client.CommittedFilesToBranch[0].Message
	assert.NotContains(t, commitMsg, "Signed-off-by")
}

func TestRunGitHubSetupPerRepo_SignoffMissingIdentity(t *testing.T) {
	client, cfg := newSignoffTestSetup(t)
	client.AuthenticatedUserIdentity = nil // simulates a bot token
	cfg.signoff = true
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--signoff requires a GitHub user identity")
}

func TestRunGitHubSetupPerRepo_SignoffDirect(t *testing.T) {
	client, cfg := newSignoffTestSetup(t)
	cfg.signoff = true
	cfg.direct = true
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, cfg)
	require.NoError(t, err)

	// Direct mode commits to the default branch.
	require.NotEmpty(t, client.CommittedFiles)
	commitMsg := client.CommittedFiles[0].Message
	assert.Contains(t, commitMsg, "Signed-off-by: Test User <test@example.com>")
}

func TestGitHubSetupCmd_SignoffRejectedForOrgTarget(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme", "--signoff"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--signoff is only valid for per-repo setup")
}

func TestRunGitHubSetupPerRepo_SignoffEmptyIdentityFields(t *testing.T) {
	client, cfg := newSignoffTestSetup(t)
	client.AuthenticatedUserIdentity = &forge.UserIdentity{Name: "", Email: ""}
	cfg.signoff = true
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--signoff requires a GitHub user identity with both name and email set")
}

func TestRunGitHubSetupPerRepo_DryRunSignoffShowsTrailer(t *testing.T) {
	client, cfg := newSignoffTestSetup(t)
	cfg.signoff = true
	cfg.dryRun = true
	var buf bytes.Buffer
	printer := ui.New(&buf)

	err := runGitHubSetupPerRepo(context.Background(), client, printer, cfg)
	require.NoError(t, err)

	// Dry run should display the trailer that would be added.
	assert.Contains(t, buf.String(), "Signed-off-by: Test User <test@example.com>")
	// Nothing should actually be committed.
	assert.Empty(t, client.CommittedFiles)
	assert.Empty(t, client.CommittedFilesToBranch)
}

func TestRunGitHubSetupPerRepo_DryRunSignoffMissingIdentity(t *testing.T) {
	client, cfg := newSignoffTestSetup(t)
	client.AuthenticatedUserIdentity = nil
	cfg.signoff = true
	cfg.dryRun = true
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--signoff requires a GitHub user identity")
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
		changedFlags: map[string]bool{
			"mint-url":               true,
			"inference-project":      true,
			"inference-wif-provider": true,
			"inference-region":       true,
		},
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
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
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

	// Default mode delivers via PR — verify files were committed to the scaffold branch.
	require.NotEmpty(t, client.CommittedFilesToBranch)

	// Verify repo variables were set (dual-write).
	varNames := make(map[string]string)
	for _, v := range client.Variables {
		varNames[v.Name] = v.Value
	}
	assert.Equal(t, "https://mint-test-abc123.run.app", varNames["FULLSEND_MINT_URL"])
	assert.Equal(t, "global", varNames["FULLSEND_GCP_REGION"])
	assert.Equal(t, "true", varNames["FULLSEND_PER_REPO_INSTALL"])

	// Verify no secrets were overwritten (both were reused).
	assert.Empty(t, client.CreatedSecrets)
}

func TestRunGitHubSetupPerRepo_PartialReuse_ProjectOnly(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
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
	client.AuthenticatedUser = "acme"
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
	client.AuthenticatedUser = "acme"
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
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
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
	client.AuthenticatedUser = "acme"
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

func TestRunGitHubSetupPerRepo_RuntimeInConfig(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
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
		runtime:         "dummy",
	})
	require.NoError(t, err)
	require.NotEmpty(t, client.CommittedFilesToBranch)

	var cfgContent []byte
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			if f.Path == ".fullsend/config.yaml" {
				cfgContent = f.Content
				break
			}
		}
	}
	require.NotEmpty(t, cfgContent, "expected .fullsend/config.yaml in committed files")
	cfg, err := config.ParsePerRepoConfig(cfgContent)
	require.NoError(t, err)
	assert.Equal(t, "dummy", cfg.ConfigRuntime())
}

func TestRunGitHubSetupPerRepo_InvalidRuntime(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
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
		runtime:         "invalid-runtime",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid runtime")
}
