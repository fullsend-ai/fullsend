package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
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

// --- fetchPreset tests ---

func TestFetchPreset_LocalFile(t *testing.T) {
	dir := t.TempDir()
	content := "version: \"1\"\nruntime: claude\n"
	path := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	data, err := fetchPreset(path)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestFetchPreset_LocalFileMissing(t *testing.T) {
	_, err := fetchPreset("/nonexistent/path/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading preset file")
}

func TestFetchPreset_LocalFileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	require.NoError(t, os.WriteFile(path, []byte{}, 0o644))

	_, err := fetchPreset(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is empty")
}

func TestFetchPreset_HTTPS(t *testing.T) {
	content := "version: \"1\"\nruntime: claude\n"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content))
	}))
	defer srv.Close()

	// Use the test server's client that trusts its TLS cert.
	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()

	data, err := fetchPreset(srv.URL + "/preset.yaml")
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestFetchPreset_HTTPSNotFound(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()

	_, err := fetchPreset(srv.URL + "/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
}

func TestFetchPreset_HTTPSEmpty(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()

	_, err := fetchPreset(srv.URL + "/empty.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is empty")
}

func TestFetchPreset_UnsupportedScheme(t *testing.T) {
	_, err := fetchPreset("ftp://example.com/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported URL scheme")
}

func TestFetchPreset_HTTPSchemeRejectsViaHTTPS(t *testing.T) {
	// http:// URLs are not supported — only https:// is.
	// fetchPreset with http:// will hit the unsupported scheme path.
	_, err := fetchPreset("http://example.com/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported URL scheme")
}

func TestFetchPreset_LocalFileExceedsMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.yaml")
	data := make([]byte, presetMaxSize+1)
	for i := range data {
		data[i] = 'a'
	}
	require.NoError(t, os.WriteFile(path, data, 0o644))

	_, err := fetchPreset(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestFetchPreset_HTTPSRedirectToHTTP_Rejected(t *testing.T) {
	// httpTarget serves on plain HTTP.
	httpTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should not reach here"))
	}))
	defer httpTarget.Close()

	// TLS server redirects to the plain HTTP target.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpTarget.URL+"/preset.yaml", http.StatusFound)
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	defer func() { http.DefaultTransport = origTransport }()

	_, err := fetchPreset(srv.URL + "/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-HTTPS")
}

// --- validatePresetHash tests ---

func TestValidatePresetHash_Match(t *testing.T) {
	data := []byte("hello world")
	hash := sha256.Sum256(data)
	hexHash := hex.EncodeToString(hash[:])

	err := validatePresetHash(data, hexHash)
	require.NoError(t, err)
}

func TestValidatePresetHash_MatchUppercase(t *testing.T) {
	data := []byte("hello world")
	hash := sha256.Sum256(data)
	hexHash := strings.ToUpper(hex.EncodeToString(hash[:]))

	err := validatePresetHash(data, hexHash)
	require.NoError(t, err)
}

func TestValidatePresetHash_Mismatch(t *testing.T) {
	data := []byte("hello world")
	wrongHash := strings.Repeat("ab", 32)

	err := validatePresetHash(data, wrongHash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preset hash mismatch")
}

func TestValidatePresetHash_InvalidLength(t *testing.T) {
	err := validatePresetHash([]byte("data"), "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "64-character")
}

func TestValidatePresetHash_InvalidHex(t *testing.T) {
	err := validatePresetHash([]byte("data"), strings.Repeat("zz", 32))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid hex")
}

// --- validatePresetYAML tests ---

func TestValidatePresetYAML_Valid(t *testing.T) {
	err := validatePresetYAML([]byte("version: \"1\"\nruntime: claude\n"))
	require.NoError(t, err)
}

func TestValidatePresetYAML_Invalid(t *testing.T) {
	err := validatePresetYAML([]byte(":\n  - :\n  bad: [unclosed"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid YAML")
}

// --- isRemotePreset tests ---

func TestIsRemotePreset(t *testing.T) {
	assert.True(t, isRemotePreset("https://example.com/preset.yaml"))
	assert.True(t, isRemotePreset("http://example.com/preset.yaml"))
	assert.False(t, isRemotePreset("/local/path/preset.yaml"))
	assert.False(t, isRemotePreset("relative/path.yaml"))
}

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

func TestGitHubSetupCmd_ConfigWithRuntime_Rejected(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")

	dir := t.TempDir()
	presetPath := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte("version: \"1\"\n"), 0o644))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--config", presetPath,
		"--runtime", "claude"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--runtime cannot be used with --config")
}

func TestGitHubSetupCmd_ConfigWithAgents_Rejected(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")

	dir := t.TempDir()
	presetPath := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(presetPath, []byte("version: \"1\"\n"), 0o644))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--config", presetPath,
		"--agents", "triage,review"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--agents cannot be used with --config")
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
