package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// --- CLI flag integration tests ---

func TestGitHubSetupCmd_ConfigFlags(t *testing.T) {
	cmd := newGitHubSetupCmd()

	configFlag := cmd.Flags().Lookup("config")
	require.NotNil(t, configFlag, "expected --config flag")
	assert.Equal(t, "", configFlag.DefValue)

	configHashFlag := cmd.Flags().Lookup("config-hash")
	require.NotNil(t, configHashFlag, "expected --config-hash flag")
	assert.Equal(t, "", configHashFlag.DefValue)
}

func TestGitHubSetupCmd_ConfigHashWithoutConfig(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--config-hash", "abc123"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--config-hash requires --config")
}

func TestGitHubSetupCmd_ConfigWithRuntime_Accepted(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")

	dir := t.TempDir()
	presetPath := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte("version: \"1\"\n"), 0o644))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--config", presetPath,
		"--runtime", "claude",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestGitHubSetupCmd_ConfigWithAgents_Accepted(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")

	dir := t.TempDir()
	presetPath := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte("version: \"1\"\n"), 0o644))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--config", presetPath,
		"--agents", "triage,review",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestGitHubSetupCmd_ConfigInPerOrgMode_Rejected(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")

	dir := t.TempDir()
	presetPath := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte("version: \"1\"\n"), 0o644))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme",
		"--config", presetPath})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--config is only valid for per-repo setup")
}

func TestRunGitHubSetupPerRepo_WithPreset_DryRun(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")

	// Create a local preset file.
	dir := t.TempDir()
	presetContent := "version: \"1\"\nruntime: claude\nroles:\n  - triage\n"
	presetPath := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte(presetContent), 0o644))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--config", presetPath,
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestRunGitHubSetupPerRepo_WithPresetAndHash_DryRun(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")

	presetContent := "version: \"1\"\nruntime: claude\nroles:\n  - triage\n"
	hash := sha256.Sum256([]byte(presetContent))
	hexHash := hex.EncodeToString(hash[:])

	dir := t.TempDir()
	presetPath := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte(presetContent), 0o644))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--config", presetPath,
		"--config-hash", hexHash,
		"--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestRunGitHubSetupPerRepo_WithPresetHashMismatch_DryRun(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")

	dir := t.TempDir()
	presetPath := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte("version: \"1\"\n"), 0o644))

	wrongHash := strings.Repeat("ab", 32)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-project", "my-project",
		"--inference-wif-provider", "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		"--config", presetPath,
		"--config-hash", wrongHash,
		"--dry-run"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preset hash mismatch")
}

// --- File layout integration tests ---

func TestRunGitHubSetupPerRepo_WithPreset_CommitsBaseAndStub(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	printer := ui.New(&discardWriter{})

	presetContent := "version: \"1\"\nruntime: claude\nroles:\n  - triage\n"

	// Write the preset to a temp file.
	dir := t.TempDir()
	presetPath := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte(presetContent), 0o644))

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset:         presetPath,
	})
	require.NoError(t, err)

	// Default mode delivers via PR — verify files were committed.
	require.NotEmpty(t, client.CommittedFilesToBranch)

	// Collect committed file paths and their content.
	filesByPath := make(map[string][]byte)
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			filesByPath[f.Path] = f.Content
		}
	}

	// Verify config.base.yaml was committed with preset content.
	baseContent, hasBase := filesByPath[".fullsend/config.base.yaml"]
	require.True(t, hasBase, "expected .fullsend/config.base.yaml in committed files")
	assert.Equal(t, presetContent, string(baseContent))

	// Verify config.yaml is the stub overlay (not a full generated config).
	cfgContent, hasCfg := filesByPath[".fullsend/config.yaml"]
	require.True(t, hasCfg, "expected .fullsend/config.yaml in committed files")
	assert.Equal(t, stubConfigYAML, string(cfgContent))
	assert.Contains(t, string(cfgContent), "overlay")
	assert.Contains(t, string(cfgContent), "config.base.yaml")
}

func TestRunGitHubSetupPerRepo_WithPresetAndHash_CommitsFiles(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	printer := ui.New(&discardWriter{})

	presetContent := "version: \"1\"\nruntime: claude\nroles:\n  - triage\n"
	hash := sha256.Sum256([]byte(presetContent))
	hexHash := hex.EncodeToString(hash[:])

	dir := t.TempDir()
	presetPath := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte(presetContent), 0o644))

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset:         presetPath,
		configHash:           hexHash,
	})
	require.NoError(t, err)

	// Verify both config files are committed.
	filesByPath := make(map[string][]byte)
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			filesByPath[f.Path] = f.Content
		}
	}

	_, hasBase := filesByPath[".fullsend/config.base.yaml"]
	assert.True(t, hasBase, "expected .fullsend/config.base.yaml")
	_, hasCfg := filesByPath[".fullsend/config.yaml"]
	assert.True(t, hasCfg, "expected .fullsend/config.yaml")
}

func TestRunGitHubSetupPerRepo_WithPresetHashMismatch_Aborts(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	printer := ui.New(&discardWriter{})

	presetContent := "version: \"1\"\nruntime: claude\n"
	wrongHash := strings.Repeat("ab", 32)

	dir := t.TempDir()
	presetPath := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte(presetContent), 0o644))

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset:         presetPath,
		configHash:           wrongHash,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preset hash mismatch")

	// Verify nothing was committed (aborted before commit).
	assert.Empty(t, client.CommittedFiles)
	assert.Empty(t, client.CommittedFilesToBranch)
}

func TestRunGitHubSetupPerRepo_WithoutPreset_NoBaseFile(t *testing.T) {
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
	})
	require.NoError(t, err)

	// Without --config, no config.base.yaml should be committed.
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			assert.NotEqual(t, ".fullsend/config.base.yaml", f.Path,
				"config.base.yaml should not be committed without --config")
		}
	}
}

func TestRunGitHubSetupPerRepo_WithPresetMissingFile_Errors(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:               "acme/widget",
		mintURL:              "https://mint-test-abc123.run.app",
		inferenceProject:     "my-project",
		inferenceWIFProvider: "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
		inferenceRegion:      "global",
		agents:               strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset:         "/nonexistent/path/preset.yaml",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading preset file")
}

const validWIFProvider = "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc"

func writeSetupPreset(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "preset.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func newSetupClient(t *testing.T) *forge.FakeClient {
	t.Helper()
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	return client
}

func committedSetupFiles(client *forge.FakeClient) map[string][]byte {
	out := make(map[string][]byte)
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			out[f.Path] = f.Content
		}
	}
	return out
}

func TestRunGitHubSetupPerRepo_CLIOverridesBaseLayer(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := newSetupClient(t)
	printer := ui.New(&discardWriter{})
	presetContent := "version: \"1\"\nruntime: claude\ninference:\n  project: preset-project\n  wif_provider: " + validWIFProvider + "\n  region: europe-west1\n"
	presetPath := writeSetupPreset(t, presetContent)

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:           "acme/widget",
		agents:           strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset:     presetPath,
		inferenceProject: "cli-project",
		inferenceRegion:  "us-west2",
		changedFlags: map[string]bool{
			"config":            true,
			"inference-project": true,
			"inference-region":  true,
		},
	})
	require.NoError(t, err)

	files := committedSetupFiles(client)
	require.Equal(t, presetContent, string(files[".fullsend/config.base.yaml"]), "preset must be committed unchanged")
	overlay, err := config.ParsePerRepoConfig(files[".fullsend/config.yaml"])
	require.NoError(t, err)
	assert.Equal(t, "cli-project", overlay.ConfigInferenceProject())
	assert.Equal(t, "us-west2", overlay.ConfigInferenceRegion())
	assert.Empty(t, overlay.ConfigInferenceWIFProvider(), "omitted CLI values stay inherited, not materialized")

	composed, err := config.ParsePerRepoConfigWriterLayered(files[".fullsend/config.yaml"], files[".fullsend/config.base.yaml"])
	require.NoError(t, err)
	assert.Equal(t, "cli-project", composed.ConfigInferenceProject())
	assert.Equal(t, validWIFProvider, composed.ConfigInferenceWIFProvider())
	assert.Equal(t, "us-west2", composed.ConfigInferenceRegion())
	assert.Equal(t, "claude", composed.ConfigRuntime())

	secretNames := make(map[string]string)
	for _, s := range client.CreatedSecrets {
		secretNames[s.Name] = s.Value
	}
	assert.Equal(t, "cli-project", secretNames["FULLSEND_GCP_PROJECT_ID"])
	assert.Equal(t, validWIFProvider, secretNames["FULLSEND_GCP_WIF_PROVIDER"])
	varNames := make(map[string]string)
	for _, v := range client.Variables {
		varNames[v.Name] = v.Value
	}
	assert.Equal(t, "us-west2", varNames["FULLSEND_GCP_REGION"])
}

func TestRunGitHubSetupPerRepo_BaseOnlyRequiredValuesPass(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := newSetupClient(t)
	printer := ui.New(&discardWriter{})
	presetContent := "version: \"1\"\ninference:\n  project: preset-project\n  wif_provider: " + validWIFProvider + "\n"
	presetPath := writeSetupPreset(t, presetContent)

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset: presetPath,
		changedFlags: map[string]bool{"config": true},
	})
	require.NoError(t, err)

	files := committedSetupFiles(client)
	assert.Equal(t, presetContent, string(files[".fullsend/config.base.yaml"]))
	assert.Equal(t, stubConfigYAML, string(files[".fullsend/config.yaml"]))

	secretNames := make(map[string]string)
	for _, s := range client.CreatedSecrets {
		secretNames[s.Name] = s.Value
	}
	assert.Equal(t, "preset-project", secretNames["FULLSEND_GCP_PROJECT_ID"])
	assert.Equal(t, validWIFProvider, secretNames["FULLSEND_GCP_WIF_PROVIDER"])
}

func TestRunGitHubSetupPerRepo_CLIOnlyPersistentValues(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := newSetupClient(t)
	printer := ui.New(&discardWriter{})
	presetPath := writeSetupPreset(t, "version: \"1\"\nruntime: claude\n")

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:               "acme/widget",
		agents:               "triage,review",
		runtime:              "pi",
		mintURL:              "https://custom-mint.example.com",
		inferenceProject:     "cli-project",
		inferenceWIFProvider: validWIFProvider,
		configPreset:         presetPath,
		changedFlags: map[string]bool{
			"config":                 true,
			"runtime":                true,
			"agents":                 true,
			"mint-url":               true,
			"inference-project":      true,
			"inference-wif-provider": true,
		},
	})
	require.NoError(t, err)

	files := committedSetupFiles(client)
	overlay, err := config.ParsePerRepoConfig(files[".fullsend/config.yaml"])
	require.NoError(t, err)
	assert.Equal(t, "pi", overlay.ConfigRuntime())
	assert.Equal(t, []string{"triage", "review"}, overlay.ConfigRoles())
	assert.Equal(t, "https://custom-mint.example.com", overlay.ConfigMintURL())
	assert.Equal(t, "cli-project", overlay.ConfigInferenceProject())
	assert.Equal(t, validWIFProvider, overlay.ConfigInferenceWIFProvider())
}

func TestRunGitHubSetupPerRepo_MissingRequiredAfterComposeFails(t *testing.T) {
	client := newSetupClient(t)
	printer := ui.New(&discardWriter{})
	presetPath := writeSetupPreset(t, "version: \"1\"\nruntime: claude\n")

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset: presetPath,
		changedFlags: map[string]bool{"config": true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--inference-project is required")
	assert.Empty(t, client.CommittedFilesToBranch)
}

func TestRunGitHubSetupPerRepo_InvalidCLIValueFails(t *testing.T) {
	client := newSetupClient(t)
	printer := ui.New(&discardWriter{})
	presetPath := writeSetupPreset(t, "version: \"1\"\ninference:\n  project: preset-project\n  wif_provider: "+validWIFProvider+"\n")

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset: presetPath,
		runtime:      "not-a-runtime",
		changedFlags: map[string]bool{"config": true, "runtime": true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --runtime")
	assert.Empty(t, client.CommittedFilesToBranch)
}

func TestRunGitHubSetupPerRepo_InvalidPresetValueFails(t *testing.T) {
	client := newSetupClient(t)
	printer := ui.New(&discardWriter{})
	presetPath := writeSetupPreset(t, "version: \"1\"\nruntime: not-a-runtime\ninference:\n  project: preset-project\n  wif_provider: "+validWIFProvider+"\n")

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset: presetPath,
		changedFlags: map[string]bool{"config": true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid preset")
	assert.Empty(t, client.CommittedFilesToBranch)
}

func TestRunGitHubSetupPerRepo_InvalidPresetMintURLFails(t *testing.T) {
	client := newSetupClient(t)
	printer := ui.New(&discardWriter{})
	presetPath := writeSetupPreset(t, "version: \"1\"\nmint_url: http://insecure.example.com\ninference:\n  project: preset-project\n  wif_provider: "+validWIFProvider+"\n")

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:       "acme/widget",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset: presetPath,
		changedFlags: map[string]bool{"config": true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preset mint_url")
	assert.Empty(t, client.CommittedFilesToBranch)
}

func TestRunGitHubSetupPerRepo_InvalidCLIProviderFails(t *testing.T) {
	client := newSetupClient(t)
	printer := ui.New(&discardWriter{})
	presetPath := writeSetupPreset(t, "version: \"1\"\ninference:\n  project: preset-project\n  wif_provider: "+validWIFProvider+"\n")

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:            "acme/widget",
		agents:            strings.Join(config.PerRepoDefaultRoles(), ","),
		configPreset:      presetPath,
		inferenceProvider: "openai",
		changedFlags:      map[string]bool{"config": true, "inference-provider": true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --inference-provider")
	assert.Empty(t, client.CommittedFilesToBranch)
}
