package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/appsetup"
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
	assert.True(t, names["set"], "expected set subcommand")
	assert.Len(t, names, 2, "expected only setup and set subcommands")
	for _, removed := range []string{"enroll", "unenroll", "status", "uninstall", "sync-scaffold"} {
		assert.False(t, names[removed], "per-org subcommand %q should have been removed", removed)
	}
}

func TestGitHubCommand_UseStringsRequireOwnerRepo(t *testing.T) {
	assert.Equal(t, "setup <owner/repo>", newGitHubSetupCmd().Use)
	setUse := newGitHubSetCmd().Use
	assert.Contains(t, setUse, "<owner/repo>")
	assert.NotContains(t, setUse, "<org")
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
	assert.Equal(t, strings.Join(config.PerRepoDefaultRoles(), ","), agentsFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")

	assert.Nil(t, cmd.Flags().Lookup("skip-app-setup"), "--skip-app-setup has no effect on repository setup; app creation is handled by admin install")
	assert.Nil(t, cmd.Flags().Lookup("public"), "--public has no effect on repository setup; app creation is handled by admin install")

	appSetFlag := cmd.Flags().Lookup("app-set")
	require.NotNil(t, appSetFlag, "expected --app-set flag")
	assert.Equal(t, "fullsend-ai", appSetFlag.DefValue)

	assert.Nil(t, cmd.Flags().Lookup("enroll-all"), "--enroll-all was removed with per-org installation")
	assert.Nil(t, cmd.Flags().Lookup("enroll-none"), "--enroll-none was removed with per-org installation")

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

	for _, name := range []string{"openai-audience", "openai-identity-provider-id", "openai-service-account-id"} {
		f := cmd.Flags().Lookup(name)
		require.NotNil(t, f, "expected --%s flag", name)
		assert.Equal(t, "", f.DefValue)
	}

	signoffFlag := cmd.Flags().Lookup("signoff")
	require.NotNil(t, signoffFlag, "expected --signoff flag")
	assert.Equal(t, "false", signoffFlag.DefValue)

	fullsendRefFlag := cmd.Flags().Lookup("fullsend-ref")
	require.NotNil(t, fullsendRefFlag, "expected --fullsend-ref flag")
	assert.Equal(t, "", fullsendRefFlag.DefValue)
}

func TestGitHubSetupCmd_UsesDefaultMintURL(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	// Without explicit --mint-url, the default should be used and
	// validation should not fail on a missing URL.
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestGitHubSetupCmd_RejectsOrgOnlyTarget(t *testing.T) {
	// Point token resolution at a value so that, if the org-target guard
	// were missing, the command would proceed toward forge calls instead of
	// failing on a missing token. The guard must fire before any of that.
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme",
		"--mint-url", "https://mint-test-abc123.run.app"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
	assert.Contains(t, err.Error(), "per-org installation has been removed")
	assert.Contains(t, err.Error(), `"acme"`)
}

func TestGitHubSetupCmd_OrgTargetCheckedBeforeOtherValidation(t *testing.T) {
	// The org-target guard is the first check in RunE, so it wins over
	// flag validation errors that would otherwise be reported.
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme",
		"--mint-url", "http://not-secure.run.app",
		"--fullsend-ref", "main"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "per-org installation has been removed")
	assert.NotContains(t, err.Error(), "HTTPS URL")
}

func TestGitHubSetupCmd_ValidatesMintURLHTTPS(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
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

func TestGitHubSetupCmd_PerRepoWithoutGCP(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		mintURL:      "https://mint-test-abc123.run.app",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		changedFlags: map[string]bool{"mint-url": true},
	})
	require.NoError(t, err)
	for _, secret := range client.CreatedSecrets {
		assert.NotContains(t, secret.Name, "FULLSEND_GCP_")
	}
}

func TestGitHubSetupCmd_PerRepoRequiresProjectWhenWIFConfigured(t *testing.T) {
	// A WIF provider without a project is an incomplete Vertex pair.
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		changedFlags:         map[string]bool{"mint-url": true, "inference-wif-provider": true},
	})
	require.Error(t, err)
	errMsg := err.Error()
	assert.True(t, strings.Contains(errMsg, "--inference-project") ||
		strings.Contains(errMsg, "FULLSEND_GCP_PROJECT_ID"),
		"expected error to mention --inference-project or FULLSEND_GCP_PROJECT_ID, got: %s", errMsg)
}

func TestGitHubSetupCmd_PerRepoRequiresWIFProvider(t *testing.T) {
	// --inference-project is supplied explicitly, but neither the (first
	// install, no top layer) config nor a repo secret supplies the WIF
	// provider, so setup must fail the required-value check for it.
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:           "acme/widget",
		mintURL:          "https://mint-test-abc123.run.app",
		inferenceProject: "my-project",
		agents:           strings.Join(config.PerRepoDefaultRoles(), ","),
		changedFlags:     map[string]bool{"mint-url": true, "inference-project": true},
	})
	require.Error(t, err)
	errMsg := err.Error()
	assert.True(t, strings.Contains(errMsg, "--inference-wif-provider") ||
		strings.Contains(errMsg, "FULLSEND_GCP_WIF_PROVIDER"),
		"expected error to mention --inference-wif-provider or FULLSEND_GCP_WIF_PROVIDER, got: %s", errMsg)
}

func TestGitHubSetupCmd_PerRepoInferenceValuesFromExistingConfig(t *testing.T) {
	// A re-run must resolve required inference values from the existing
	// .fullsend/config.yaml top layer — loaded remotely via
	// loadExistingPerRepoConfig — without requiring
	// --inference-project/--inference-wif-provider or a pre-existing
	// repo secret. This models the "supplied by that layer" path that
	// the required-value tests above don't exercise on their own.
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	client.FileContents = map[string][]byte{
		"acme/widget/.fullsend/config.yaml": []byte(`version: "1"
mint_url: https://mint-test-abc123.run.app
inference:
  provider: vertex
  project: existing-project
  region: us-central1
  wif_provider: projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc
`),
	}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		changedFlags: map[string]bool{}, // no CLI flags: values must come from the existing config
	})
	require.NoError(t, err)

	// Both required values were resolved from the existing config layer
	// — not reused from a pre-existing secret (none exists) and not
	// supplied via CLI flags (none were passed).
	secrets := make(map[string]string)
	for _, s := range client.CreatedSecrets {
		secrets[s.Name] = s.Value
	}
	assert.Equal(t, "existing-project", secrets["FULLSEND_GCP_PROJECT_ID"])
	assert.Equal(t, "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc", secrets["FULLSEND_GCP_WIF_PROVIDER"])
}

func TestGitHubSetupCmd_FullsendRefConflictsWithVendor(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--fullsend-ref", "main",
		"--vendor"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--fullsend-ref conflicts with --vendor")
}

func TestGitHubSetupCmd_FullsendRefRejectsInvalidChars(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--fullsend-ref", "v1.0.0; rm -rf /"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--fullsend-ref")
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestGitHubSetupCmd_FullsendRefAcceptedForPerRepo(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--fullsend-ref", "main",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestRunGitHubSetupPerRepo_FullsendRefPropagatesIntoScaffold(t *testing.T) {
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
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		fullsendRef:          "custom-branch-ref",
		changedFlags: map[string]bool{
			"mint-url":               true,
			"inference-project":      true,
			"inference-wif-provider": true,
		},
	})
	require.NoError(t, err)

	// Verify the custom ref propagates into the scaffold workflow file.
	var shimContent []byte
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			if f.Path == ".github/workflows/fullsend.yaml" {
				shimContent = f.Content
				break
			}
		}
	}
	require.NotEmpty(t, shimContent, "expected .github/workflows/fullsend.yaml in committed files")
	shimStr := string(shimContent)
	assert.Contains(t, shimStr, "custom-branch-ref",
		"expected the custom --fullsend-ref to appear in the rendered scaffold workflow")
}

// --- buildPresetOverlay tests ---

func TestBuildPresetOverlay_NoFlagsChanged(t *testing.T) {
	cfg := githubSetupConfig{
		mintURL:         DefaultMintURL,
		inferenceRegion: "global",
		changedFlags:    map[string]bool{},
	}
	overlay := buildPresetOverlay(cfg, nil)

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
	overlay := buildPresetOverlay(cfg, nil)

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
	overlay := buildPresetOverlay(cfg, nil)

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
	cmd.SetArgs([]string{"github", "set", "acme/widget", "UNKNOWN_KEY", "some-value"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
	assert.Contains(t, err.Error(), "FULLSEND_GCP_REGION")
}

func TestGitHubSetCmd_RejectsMintURL(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSet(context.Background(), client, printer, "acme/widget", "FULLSEND_MINT_URL", "https://new-mint.run.app/")
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

func TestGitHubSetCmd_SetsOpenAIAPIKey(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSet(context.Background(), client, printer, "acme/widget", openAIRepoSecretName, "test-openai-key")
	require.NoError(t, err)

	require.Len(t, client.CreatedSecrets, 1)
	assert.Equal(t, openAIRepoSecretName, client.CreatedSecrets[0].Name)
	assert.Equal(t, "test-openai-key", client.CreatedSecrets[0].Value)
	assert.Equal(t, "acme", client.CreatedSecrets[0].Owner)
	assert.Equal(t, "widget", client.CreatedSecrets[0].Repo)
}

func TestGitHubSetCmd_RejectsEmptyValue(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	for _, value := range []string{"", "   "} {
		err := runGitHubSet(context.Background(), client, printer, "acme/widget", openAIRepoSecretName, value)
		require.Error(t, err, "empty value %q should be rejected", value)
		assert.Contains(t, err.Error(), "must not be empty")
	}
	assert.Empty(t, client.CreatedSecrets, "an empty value must not be stored")
}

func TestConfigKeyMapping_AllKeys(t *testing.T) {
	expectedKeys := []string{
		"FULLSEND_GCP_REGION",
		"FULLSEND_REVIEW_CLIENT_ID",
		forge.PerRepoGuardVar,
		"FULLSEND_GCP_PROJECT_ID",
		"FULLSEND_GCP_WIF_PROVIDER",
		openAIRepoSecretName,
	}
	for _, key := range expectedKeys {
		_, ok := configKeyMapping[key]
		assert.True(t, ok, "expected key %s in configKeyMapping", key)
	}
	info := configKeyMapping[forge.PerRepoGuardVar]
	assert.Equal(t, storageVariable, info.storage)

	reviewInfo := configKeyMapping["FULLSEND_REVIEW_CLIENT_ID"]
	assert.Equal(t, storageVariable, reviewInfo.storage)

	openAIInfo := configKeyMapping[openAIRepoSecretName]
	assert.Equal(t, storageSecret, openAIInfo.storage)
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

	err := runGitHubSet(context.Background(), client, printer, "-invalid/widget", "FULLSEND_GCP_REGION", "us-east5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid owner name")
}

func TestGitHubSetCmd_ValidatesRepoTarget(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	err := runGitHubSet(context.Background(), client, printer, "/repo", "FULLSEND_GCP_REGION", "us-east5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid owner name")
}

func TestRunGitHubSet_RejectsOrgOnlyTarget(t *testing.T) {
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})

	for _, key := range []string{"FULLSEND_GCP_REGION", "FULLSEND_GCP_PROJECT_ID"} {
		err := runGitHubSet(context.Background(), client, printer, "acme", key, "some-value")
		require.Error(t, err, "org-only target must be rejected for %s", key)
		assert.Contains(t, err.Error(), "owner/repo")
		assert.Contains(t, err.Error(), "per-org installation has been removed")
		assert.Contains(t, err.Error(), "fullsend github set")
	}
	assert.Empty(t, client.Variables, "no variable may be written for an org-only target")
	assert.Empty(t, client.CreatedSecrets, "no secret may be written for an org-only target")
}

func TestGitHubSetCmd_RejectsOrgOnlyTarget(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "set", "acme", "FULLSEND_GCP_REGION", "us-east5"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
	assert.Contains(t, err.Error(), "per-org installation has been removed")
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

func TestRunGitHubSetupPerRepo_WritesAppSetWhenChanged(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}

	err := runGitHubSetupPerRepo(context.Background(), client, ui.New(&discardWriter{}), githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		appSet:               "custom-set",
		changedFlags: map[string]bool{
			"mint-url":               true,
			"inference-project":      true,
			"inference-wif-provider": true,
			"inference-region":       true,
			"app-set":                true,
		},
	})
	require.NoError(t, err)

	varNames := make(map[string]string)
	for _, v := range client.Variables {
		varNames[v.Name] = v.Value
	}
	assert.Equal(t, "custom-set", varNames["FULLSEND_APP_SET"])
}

// TestRunGitHubSetupPerRepo_PreservesExistingAppSet verifies that when --app-set
// is not passed, an existing custom FULLSEND_APP_SET is preserved rather than
// overwritten with the built-in default.
func TestRunGitHubSetupPerRepo_PreservesExistingAppSet(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	client.VariableValues["acme/widget/FULLSEND_APP_SET"] = "existing-custom"

	err := runGitHubSetupPerRepo(context.Background(), client, ui.New(&discardWriter{}), githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		appSet:               "fullsend-ai", // default value; flag not changed
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
	assert.Equal(t, "existing-custom", varNames["FULLSEND_APP_SET"])
}

// TestRunGitHubSetupPerRepo_ReviewClientIDUsesPreservedAppSet verifies that
// FULLSEND_REVIEW_CLIENT_ID is resolved against the same effective app set
// that is persisted as FULLSEND_APP_SET, not the --app-set flag's default.
// On a re-run where --app-set is not passed and the repo already carries a
// custom app set, the old code resolved the review client ID against the
// flag default (here "fullsend-ai") while persisting the preserved value
// (here "existing-custom") — a mismatch that could point review-comment
// provenance validation at the wrong GitHub App.
func TestRunGitHubSetupPerRepo_ReviewClientIDUsesPreservedAppSet(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	client.VariableValues["acme/widget/FULLSEND_APP_SET"] = "existing-custom"
	client.AppClientIDs = map[string]string{
		"existing-custom-review": "preserved-client-id",
		"fullsend-ai-review":     "wrong-default-client-id",
	}

	err := runGitHubSetupPerRepo(context.Background(), client, ui.New(&discardWriter{}), githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		appSet:               "fullsend-ai", // default value; flag not changed
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
	assert.Equal(t, "existing-custom", varNames["FULLSEND_APP_SET"])
	assert.Equal(t, "preserved-client-id", varNames["FULLSEND_REVIEW_CLIENT_ID"])
}

// TestRunGitHubSetupPerRepo_RejectsMalformedExistingAppSet verifies that a
// malformed FULLSEND_APP_SET value already on the repo (not written through
// fullsend's validated CLI/manifest paths) is not preserved as-is: it fails
// appsetup.ValidateAppSet, so the effective app set falls back to the
// built-in default instead of being used unchecked to build a GitHub App
// slug for review-client-ID resolution.
func TestRunGitHubSetupPerRepo_RejectsMalformedExistingAppSet(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	client.VariableValues["acme/widget/FULLSEND_APP_SET"] = "not valid!/app set"

	err := runGitHubSetupPerRepo(context.Background(), client, ui.New(&discardWriter{}), githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		appSet:               "fullsend-ai", // default value; flag not changed
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
	assert.Equal(t, appsetup.DefaultAppSet, varNames["FULLSEND_APP_SET"])
}

func TestRunGitHubSetupPerRepo_SkipsAppSetWriteOnReadError(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	// A pre-existing custom app set that must not be clobbered.
	client.VariableValues["acme/widget/FULLSEND_APP_SET"] = "existing-custom"
	// The read used to decide preserve-vs-default fails outright (not a
	// missing-variable 404). The write must be skipped so the flag default
	// never overwrites the possibly-custom existing value.
	client.Errors = map[string]error{"GetRepoVariable": fmt.Errorf("boom")}

	err := runGitHubSetupPerRepo(context.Background(), client, ui.New(&discardWriter{}), githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		appSet:               "fullsend-ai", // default value; flag not changed
		changedFlags: map[string]bool{
			"mint-url":               true,
			"inference-project":      true,
			"inference-wif-provider": true,
			"inference-region":       true,
		},
	})
	require.NoError(t, err)

	// No FULLSEND_APP_SET write should have been issued.
	for _, v := range client.Variables {
		if v.Name == "FULLSEND_APP_SET" {
			t.Errorf("FULLSEND_APP_SET should not be written when the read fails, got %q", v.Value)
		}
	}
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
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceRegion:      "global",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
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
	assert.Contains(t, err.Error(), "invalid --runtime")
}

// existingPerRepoConfigForRerun is a customized config as a repo would have
// after editing it by hand: a non-default runtime, per-agent settings, a
// custom agent, and a comment that a struct round-trip would drop.
const existingPerRepoConfigForRerun = `# fullsend per-repo configuration
# hand-written note: triage runs Grok on pi, code stays on Claude Code
version: "1"
runtime: pi
roles:
  - triage
  - coder
agents:
  - source: harness/lint.yaml
  - name: triage
    model: xai-vertex/xai/grok-4.6
  - name: code
    runtime: claude
    model: sonnet
`

func newRerunSetupClient(t *testing.T, existing string) *forge.FakeClient {
	t.Helper()
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	client.Secrets = map[string]bool{
		"acme/widget/FULLSEND_GCP_PROJECT_ID":   true,
		"acme/widget/FULLSEND_GCP_WIF_PROVIDER": true,
	}
	if existing != "" {
		client.FileContents = map[string][]byte{"acme/widget/.fullsend/config.yaml": []byte(existing)}
	}
	return client
}

func committedScaffoldFile(client *forge.FakeClient, path string) ([]byte, bool) {
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			if f.Path == path {
				return f.Content, true
			}
		}
	}
	return nil, false
}

func TestRunGitHubSetupPerRepo_Rerun_KeepsExistingConfigVerbatim(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := newRerunSetupClient(t, existingPerRepoConfigForRerun)
	var out bytes.Buffer
	printer := ui.New(&out)

	// No config-targeting flag: the scaffold refreshes the managed files
	// but leaves .fullsend/config.yaml out entirely, so the agents: entries,
	// the runtime and the hand-written comment survive.
	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		changedFlags: map[string]bool{},
	})
	require.NoError(t, err)
	_, present := committedScaffoldFile(client, ".fullsend/config.yaml")
	assert.False(t, present, "existing config.yaml must not be rewritten on a flag-less re-run")
	assert.NotEmpty(t, client.CommittedFilesToBranch, "managed scaffold files are still delivered")
	assert.Contains(t, out.String(), "Keeping existing .fullsend/config.yaml")
	assert.NotContains(t, out.String(), "runtime?", "no runtime prompt on a re-run")
}

func TestRunGitHubSetupPerRepo_Rerun_ChangesOnlyTheFlaggedKey(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := newRerunSetupClient(t, existingPerRepoConfigForRerun)
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		runtime:      "claude",
		changedFlags: map[string]bool{"runtime": true},
	})
	require.NoError(t, err)
	content, present := committedScaffoldFile(client, ".fullsend/config.yaml")
	require.True(t, present, "--runtime targets config.yaml, so it is rewritten")
	cfg, err := config.ParsePerRepoConfig(content)
	require.NoError(t, err)
	pr := cfg.(config.PerRepoConfigReader)
	assert.Equal(t, "claude", pr.ConfigRuntime(), "flagged key changed")
	assert.Equal(t, []string{"triage", "coder"}, pr.ConfigRoles(), "roles kept (--agents not passed, despite its default)")
	require.Len(t, pr.AgentEntries(), 3, "custom agent and per-agent settings kept")
	triage, ok := config.AgentSettingsFor(pr.AgentEntries(), "triage")
	require.True(t, ok, "agent settings kept")
	assert.Equal(t, "xai-vertex/xai/grok-4.6", triage.Model)
	code, ok := config.AgentSettingsFor(pr.AgentEntries(), "code")
	require.True(t, ok)
	assert.Equal(t, "sonnet", code.Model)
}

func TestRunGitHubSetupPerRepo_Rerun_InvalidExistingConfigFails(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := newRerunSetupClient(t, "version: \"1\"\nagents: {not: a, list: here}\n")
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		changedFlags: map[string]bool{},
	})
	require.Error(t, err, "a broken existing config is never silently regenerated")
	assert.Contains(t, err.Error(), "existing .fullsend/config.yaml")
	assert.Empty(t, client.CommittedFilesToBranch)
}

func TestRunGitHubSetupPerRepo_Rerun_PresetKeepsExistingOverlay(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := newRerunSetupClient(t, existingPerRepoConfigForRerun)
	printer := ui.New(&discardWriter{})
	preset := filepath.Join(t.TempDir(), "preset.yaml")
	require.NoError(t, os.WriteFile(preset, []byte("# fullsend per-repo configuration\nversion: \"1\"\ninference:\n  region: europe-west1\n"), 0o644))

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset: preset,
		changedFlags: map[string]bool{"config": true},
	})
	require.NoError(t, err)
	_, present := committedScaffoldFile(client, ".fullsend/config.yaml")
	assert.False(t, present, "--config rewrites config.base.yaml; the existing overlay is kept")
	_, basePresent := committedScaffoldFile(client, ".fullsend/config.base.yaml")
	assert.True(t, basePresent)
}

func TestApplySetupFlagsToConfig_EveryFlag(t *testing.T) {
	t.Parallel()
	w := config.NewPerRepoConfig([]string{"triage"}, "")
	changed := applySetupFlagsToConfig(githubSetupConfig{
		runtime: "pi", agents: "triage,coder", mintURL: "https://mint.fullsend.sh",
		inferenceProvider: "vertex", inferenceProject: "proj", inferenceRegion: "europe-west1",
		inferenceWIFProvider: "projects/1/locations/global/workloadIdentityPools/p/providers/x",
		changedFlags:         map[string]bool{"runtime": true, "agents": true, "mint-url": true, "inference-provider": true, "inference-project": true, "inference-region": true, "inference-wif-provider": true},
	}, w, []string{"triage", "coder"})
	assert.Equal(t, []string{"runtime", "roles", "mint_url", "inference.provider", "inference.project", "inference.region", "inference.wif_provider"}, changed)
	assert.Equal(t, "pi", w.ConfigRuntime())
	assert.Equal(t, []string{"triage", "coder"}, w.ConfigRoles())
	assert.Equal(t, "https://mint.fullsend.sh", w.ConfigMintURL())
	assert.Equal(t, "vertex", w.ConfigInferenceProvider())
	assert.Equal(t, "proj", w.ConfigInferenceProject())
	assert.Equal(t, "europe-west1", w.ConfigInferenceRegion())
	assert.Equal(t, "projects/1/locations/global/workloadIdentityPools/p/providers/x", w.ConfigInferenceWIFProvider())

	// Nothing flagged: nothing changes.
	before, _ := w.Marshal()
	assert.Empty(t, applySetupFlagsToConfig(githubSetupConfig{changedFlags: map[string]bool{}}, w, nil))
	after, _ := w.Marshal()
	assert.Equal(t, string(before), string(after))
	assert.False(t, setupConfigFlagsChanged(githubSetupConfig{changedFlags: map[string]bool{"dry-run": true}}))
	assert.True(t, setupConfigFlagsChanged(githubSetupConfig{changedFlags: map[string]bool{"inference-region": true}}))
}

func TestLoadExistingPerRepoConfig(t *testing.T) {
	t.Parallel()
	// Missing file: first install.
	client := forge.NewFakeClient()
	cfg, err := loadExistingPerRepoConfig(context.Background(), client, "acme", "widget")
	require.NoError(t, err)
	assert.Nil(t, cfg)

	// Org-style content in the per-repo path is refused, as is a read error.
	client.FileContents = map[string][]byte{"acme/widget/.fullsend/config.yaml": []byte("version: \"1\"\ndispatch:\n  platform: github\ndefaults:\n  roles: [triage]\nrepos: {}\n")}
	_, err = loadExistingPerRepoConfig(context.Background(), client, "acme", "widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a per-repo config")
	client.GetFileContentErrors = map[string]error{"acme/widget/.fullsend/config.yaml": fmt.Errorf("github api: 500")}
	_, err = loadExistingPerRepoConfig(context.Background(), client, "acme", "widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading existing .fullsend/config.yaml")
}

func TestLoadExistingPerRepoConfig_WithBaseLayer(t *testing.T) {
	t.Parallel()
	// An overlay entry tunes a custom agent registered only in the base
	// layer. Without the base, ValidateAgentEntries would reject the
	// overlay entry as "not a built-in agent".
	baseYAML := `version: "1"
agents:
  - name: lint
    source: https://raw.githubusercontent.com/acme/agents/main/harness/lint.yaml#sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
allowed_remote_resources:
  - https://raw.githubusercontent.com/acme/agents/
`
	overlayYAML := `version: "1"
agents:
  - name: lint
    effort: medium
`
	client := forge.NewFakeClient()
	client.FileContents = map[string][]byte{
		"acme/widget/.fullsend/config.yaml":      []byte(overlayYAML),
		"acme/widget/.fullsend/config.base.yaml": []byte(baseYAML),
	}
	cfg, err := loadExistingPerRepoConfig(context.Background(), client, "acme", "widget")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	// The merged agent list should carry the base's source on the lint entry.
	agents := cfg.AgentEntries()
	require.Len(t, agents, 1)
	assert.Equal(t, "lint", agents[0].Name)
	assert.Contains(t, agents[0].Source, "lint.yaml")
	assert.Equal(t, "medium", agents[0].Effort)

	// Verify validation passes on the merged set (it would fail without the base).
	require.NoError(t, cfg.Validate())
}

func TestLoadExistingPerRepoConfig_BaseReadError(t *testing.T) {
	t.Parallel()
	client := forge.NewFakeClient()
	client.FileContents = map[string][]byte{
		"acme/widget/.fullsend/config.yaml": []byte("version: \"1\"\n"),
	}
	client.GetFileContentErrors = map[string]error{
		"acme/widget/.fullsend/config.base.yaml": fmt.Errorf("github api: 500"),
	}
	_, err := loadExistingPerRepoConfig(context.Background(), client, "acme", "widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading existing .fullsend/config.base.yaml")
}

func TestGitHubSetupCmd_OpenAIFlagsAllOrNone(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--openai-audience", "fullsend://acme",
		"--dry-run"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be set together (missing identity_provider_id, service_account_id)")

	// One empty flag is not a request to clear the block.
	cmd = newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--openai-audience", "",
		"--dry-run"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pass all three empty to remove the block")
}

func TestBuildPresetOverlay_OpenAI(t *testing.T) {
	cfg := githubSetupConfig{
		changedFlags:             map[string]bool{"openai-audience": true, "openai-identity-provider-id": true, "openai-service-account-id": true},
		openaiAudience:           " fullsend://acme ",
		openaiIdentityProviderID: "idp_1",
		openaiServiceAccountID:   "sa_1",
	}
	o := buildPresetOverlay(cfg, nil)
	require.NotNil(t, o)
	assert.Equal(t, config.OpenAIWIFConfig{Audience: "fullsend://acme", IdentityProviderID: "idp_1", ServiceAccountID: "sa_1"}, o.ConfigInferenceOpenAI())
	assert.True(t, setupConfigFlagsChanged(cfg), "the openai flags turn a re-run into a config change")
}

func TestBuildPresetOverlay_RuntimeAndAgents(t *testing.T) {
	cfg := githubSetupConfig{
		runtime:      "pi",
		changedFlags: map[string]bool{"runtime": true, "agents": true},
	}
	o := buildPresetOverlay(cfg, []string{"triage", "review"})
	require.NotNil(t, o)
	assert.Equal(t, "pi", o.ConfigRuntime())
	assert.Equal(t, []string{"triage", "review"}, o.ConfigRoles())
	data, err := o.Marshal()
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, "runtime:")
	assert.Contains(t, s, "roles:")
}

func TestValidatePresetLayer(t *testing.T) {
	t.Parallel()
	require.NoError(t, validatePresetLayer([]byte("version: \"1\"\nruntime: claude\n")))

	err := validatePresetLayer([]byte("version: \"1\"\ndispatch:\n  platform: github\ndefaults:\n  roles: [triage]\nrepos: {}\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a per-repo configuration")

	err = validatePresetLayer([]byte("version: \"1\"\nruntime: not-a-runtime\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid preset")

	err = validatePresetLayer([]byte("version: \"1\"\nmint_url: http://insecure.example.com\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preset mint_url")
}

func TestValidateCLISetupValues(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateCLISetupValues(githubSetupConfig{}))

	err := validateCLISetupValues(githubSetupConfig{inferenceProvider: "openai"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --inference-provider")

	err = validateCLISetupValues(githubSetupConfig{inferenceWIFProvider: "not-a-wif"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-wif-provider must be a full WIF provider")
}

func TestValidateSetupValueFormats(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateSetupValueFormats(nil, "composed config"))

	cfg := config.NewEmptyPerRepoOverlay()
	cfg.SetMintURL("https://mint.example.com")
	require.NoError(t, validateSetupValueFormats(cfg, "composed config"))

	cfg.SetMintURL("http://insecure.example.com")
	err := validateSetupValueFormats(cfg, "composed config")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "composed config mint_url")

	cfg = config.NewEmptyPerRepoOverlay()
	cfg.SetInferenceWIFProvider("not-a-wif")
	err = validateSetupValueFormats(cfg, "composed config")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "composed config inference.wif_provider")
}

func TestComposeSetupLayers(t *testing.T) {
	t.Parallel()
	overlay := config.NewEmptyPerRepoOverlay()
	overlay.SetInferenceProject("cli-project")

	effective, err := composeSetupLayers(nil, overlay, nil)
	require.NoError(t, err)
	assert.Equal(t, "cli-project", effective.ConfigInferenceProject())

	effective, err = composeSetupLayers(nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, effective)

	base := []byte("version: \"1\"\ninference:\n  project: preset-project\n  wif_provider: projects/1/locations/global/workloadIdentityPools/p/providers/x\n")
	effective, err = composeSetupLayers(nil, overlay, base)
	require.NoError(t, err)
	assert.Equal(t, "cli-project", effective.ConfigInferenceProject())
	assert.Equal(t, "projects/1/locations/global/workloadIdentityPools/p/providers/x", effective.ConfigInferenceWIFProvider())
}

func TestEffectiveSetupAccessors(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "from-flag", effectiveInferenceProject(githubSetupConfig{inferenceProject: "from-flag"}, nil))
	assert.Equal(t, "", effectiveInferenceProject(githubSetupConfig{}, nil))
	assert.Equal(t, "from-flag", effectiveInferenceWIF(githubSetupConfig{inferenceWIFProvider: "from-flag"}, nil))
	assert.Equal(t, "", effectiveInferenceWIF(githubSetupConfig{}, nil))
	assert.Equal(t, "https://flag.example.com", effectiveSetupMintURL(githubSetupConfig{mintURL: "https://flag.example.com"}, nil))
	assert.Equal(t, config.DefaultPerRepoMintURL, effectiveSetupMintURL(githubSetupConfig{}, nil))
	assert.Equal(t, "us-west2", effectiveInferenceRegion(githubSetupConfig{inferenceRegion: "us-west2"}, nil))
	assert.Equal(t, config.DefaultPerRepoInferenceRegion, effectiveInferenceRegion(githubSetupConfig{}, nil))
	assert.Equal(t, "pi", effectiveSetupRuntime(githubSetupConfig{runtime: "pi"}, nil))
	assert.Equal(t, "", effectiveSetupRuntime(githubSetupConfig{}, nil))

	cfg := config.NewEmptyPerRepoOverlay()
	cfg.SetMintURL("https://preset.example.com")
	cfg.SetInferenceRegion("europe-west1")
	cfg.SetRuntime("codex")
	cfg.SetInferenceProject("preset-project")
	cfg.SetInferenceWIFProvider("preset-wif")
	assert.Equal(t, "https://preset.example.com", effectiveSetupMintURL(githubSetupConfig{}, cfg))
	assert.Equal(t, "europe-west1", effectiveInferenceRegion(githubSetupConfig{}, cfg))
	assert.Equal(t, "codex", effectiveSetupRuntime(githubSetupConfig{}, cfg))
	assert.Equal(t, "preset-project", effectiveInferenceProject(githubSetupConfig{}, cfg))
	assert.Equal(t, "preset-wif", effectiveInferenceWIF(githubSetupConfig{}, cfg))
}

func TestResolveInferenceReuse_SecretCheckErrors(t *testing.T) {
	t.Parallel()
	client := forge.NewFakeClient()
	client.Errors = map[string]error{"RepoSecretExists": fmt.Errorf("boom")}
	_, _, err := resolveInferenceReuse(context.Background(), client, "acme", "widget", githubSetupConfig{inferenceWIFProvider: "provider"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking existing secret FULLSEND_GCP_PROJECT_ID")

	client = forge.NewFakeClient()
	client.Secrets = map[string]bool{"acme/widget/FULLSEND_GCP_PROJECT_ID": true}
	client.Errors = map[string]error{"RepoSecretExists": fmt.Errorf("boom")}
	_, _, err = resolveInferenceReuse(context.Background(), client, "acme", "widget", githubSetupConfig{inferenceProject: "p"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking existing secret FULLSEND_GCP_WIF_PROVIDER")
}
