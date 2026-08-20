package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch/cf"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Tests in this file mutate package-level globals (githubAPIBaseURL,
// githubHTTPClient) via save/restore in defer. Do NOT use t.Parallel().

func generateTestPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

// fakeCFWranglerRunner implements cf.WranglerRunner for CLI tests.
type fakeCFWranglerRunner struct {
	deployErr    error
	deployURL    string
	deployCalls  []fakeCFDeployCall
	secretCalls  []fakeCFSecretCall
	deleteCalls  []string
	secretPutErr error
	// workerExists controls the return value of WorkerExists.
	// Defaults to true (Worker exists).
	workerExists *bool
}

type fakeCFDeployCall struct {
	workerName   string
	previewAlias string
	envVars      map[string]string
	secrets      map[string][]byte
}

type fakeCFSecretCall struct {
	workerName string
	secretName string
	value      []byte
}

func (f *fakeCFWranglerRunner) Deploy(_ context.Context, _ string, workerName string, previewAlias string, envVars map[string]string, secrets map[string][]byte) (string, error) {
	f.deployCalls = append(f.deployCalls, fakeCFDeployCall{workerName: workerName, previewAlias: previewAlias, envVars: envVars, secrets: secrets})
	if f.deployErr != nil {
		return "", f.deployErr
	}
	url := f.deployURL
	if url == "" {
		url = fmt.Sprintf("https://%s.workers.dev", workerName)
	}
	return url, nil
}

func (f *fakeCFWranglerRunner) PutSecret(_ context.Context, workerName, secretName string, value []byte) error {
	f.secretCalls = append(f.secretCalls, fakeCFSecretCall{
		workerName: workerName,
		secretName: secretName,
		value:      value,
	})
	return f.secretPutErr
}

func (f *fakeCFWranglerRunner) Delete(_ context.Context, workerName string) error {
	f.deleteCalls = append(f.deleteCalls, workerName)
	return nil
}

func (f *fakeCFWranglerRunner) WorkerExists(_ context.Context, _ string) (bool, error) {
	if f.workerExists != nil {
		return *f.workerExists, nil
	}
	return true, nil // default: Worker exists
}

func TestMintCommand_HasSubcommands(t *testing.T) {
	cmd := newMintCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Use] = true
	}
	assert.True(t, names["deploy"], "expected deploy subcommand")
	assert.True(t, names["delete"], "expected delete subcommand")
	assert.True(t, names["enroll <org|owner/repo>"], "expected enroll subcommand")
	assert.True(t, names["unenroll <org|owner/repo>"], "expected unenroll subcommand")
	assert.True(t, names["status [org]"], "expected status subcommand")
	assert.True(t, names["token"], "expected token subcommand")
	assert.True(t, names["add-role <role>"], "expected add-role subcommand")
	assert.True(t, names["remove-role <role>"], "expected remove-role subcommand")
}

func TestMintAddRoleCmd_Flags(t *testing.T) {
	cmd := newMintAddRoleCmd()
	assert.NotNil(t, cmd.Flags().Lookup("project"))
	assert.NotNil(t, cmd.Flags().Lookup("slug"))
	assert.NotNil(t, cmd.Flags().Lookup("pem"))
	assert.NotNil(t, cmd.Flags().Lookup("use-existing-pem-secret"))
}

func TestMintRemoveRoleCmd_Flags(t *testing.T) {
	cmd := newMintRemoveRoleCmd()
	assert.NotNil(t, cmd.Flags().Lookup("project"))
	assert.NotNil(t, cmd.Flags().Lookup("keep-pem"))
}

func TestMintCommand_RegisteredInRoot(t *testing.T) {
	cmd := newRootCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["mint"], "expected mint command registered in root")
}

// --- deploy command tests ---

func TestMintDeployCmd_Flags(t *testing.T) {
	cmd := newMintDeployCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")
	assert.Equal(t, "", projectFlag.DefValue)

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")
	assert.Equal(t, "us-central1", regionFlag.DefValue)

	sourceDirFlag := cmd.Flags().Lookup("source-dir")
	require.NotNil(t, sourceDirFlag, "expected --source-dir flag")

	skipDeployFlag := cmd.Flags().Lookup("skip-deploy")
	require.NotNil(t, skipDeployFlag, "expected --skip-deploy flag")

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")

	publicFlag := cmd.Flags().Lookup("public")
	require.NotNil(t, publicFlag, "expected --public flag")
	assert.Equal(t, "false", publicFlag.DefValue)
}

func TestMintDeployCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintDeployCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=BAD"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestMintDeployCmd_InvalidRegion(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--region=invalid"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP region")
}

func TestMintDeployCmd_DryRun(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestRunMintDeployGCP_SkipDeployReportsCommitResolution(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:   "https://fullsend-mint-abc123-uc.a.run.app",
			State: "ACTIVE",
			EnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100"}`,
				"ALLOWED_ORGS": "existing-org",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
			"ALLOWED_ORGS": "existing-org",
		}),
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner in ['existing-org']",
		}),
	))

	r, w, err := os.Pipe()
	require.NoError(t, err)
	oldStdout := os.Stdout
	os.Stdout = w
	deployErr := runMintDeployGCP(context.Background(), "my-project-id", "us-central1", t.TempDir(), true, false, "", "", nil, false)
	require.NoError(t, w.Close())
	os.Stdout = oldStdout
	require.NoError(t, deployErr)

	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, r)
	require.NoError(t, copyErr)
	out := buf.String()
	assert.Contains(t, out, "Could not resolve mint commit from checkout")
	assert.Contains(t, out, "Version:")
	assert.Contains(t, out, "Commit:")
	assert.Contains(t, out, "Deployment complete")
}

func TestMintDeployCmd_DryRunShowsResolvedSource(t *testing.T) {
	// When --source-dir is not provided and the default checkout path
	// exists on disk, dry-run should show the resolved path.
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, gcf.DefaultFunctionSourceDir()), 0o755))
	t.Chdir(tmpDir)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run"})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, gcf.DefaultFunctionSourceDir(),
		"dry-run should show resolved checkout path")
	assert.NotContains(t, stdout, "embedded mint function",
		"dry-run should not claim embedded source when checkout path exists")
}

func TestMintDeployCmd_DryRunShowsEmbeddedWhenPathMissing(t *testing.T) {
	// When --source-dir is not provided and the default checkout path
	// does not exist on disk, dry-run should report embedded source.
	t.Chdir(t.TempDir())

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run"})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "embedded mint function",
		"dry-run should report embedded source when checkout path is missing")
	assert.NotContains(t, stdout, gcf.DefaultFunctionSourceDir(),
		"dry-run should not show non-existent checkout path")
}

func TestMintDeployCmd_DryRunWithExplicitSourceDir(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "--source-dir=/custom/path"})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "/custom/path",
		"dry-run should show the explicitly provided source-dir")
}

func TestMintDeployCmd_DryRunPublic(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "--public"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintDeployCmd_NoArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "extra"})
	err := cmd.Execute()
	require.Error(t, err)
}

func TestMintDeployCmd_PemDirFlag(t *testing.T) {
	cmd := newMintDeployCmd()

	pemDirFlag := cmd.Flags().Lookup("pem-dir")
	require.NotNil(t, pemDirFlag, "expected --pem-dir flag")
	assert.Equal(t, "", pemDirFlag.DefValue)
}

func TestMintDeployCmd_DryRunWithPemDir(t *testing.T) {
	pemDir := t.TempDir()
	testPEM := generateTestPEM(t)
	for _, role := range defaultMintRoles() {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "--pem-dir=" + pemDir})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintDeployCmd_DryRunWithBadPemDir(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "--pem-dir=/nonexistent"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--pem-dir")
}

func TestMintDeployCmd_DryRunWithPemDirAsFile(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "notadir.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("dummy"), 0o600))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "--pem-dir=" + tmpFile})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a directory")
}

func TestMintDeployCmd_DryRunWithInvalidPEM(t *testing.T) {
	pemDir := t.TempDir()
	testPEM := generateTestPEM(t)
	for _, role := range defaultMintRoles() {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(pemDir, "coder.pem"), []byte("not-a-pem"), 0o600))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "--pem-dir=" + pemDir})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid PEM for role")
}

// --- deploy command: platform flag tests ---

func TestMintDeployCmd_PlatformFlag(t *testing.T) {
	cmd := newMintDeployCmd()
	platformFlag := cmd.Flags().Lookup("platform")
	require.NotNil(t, platformFlag, "expected --platform flag")
	assert.Equal(t, "gcp", platformFlag.DefValue)
}

func TestMintDeployCmd_CloudflareFlags(t *testing.T) {
	cmd := newMintDeployCmd()

	workerNameFlag := cmd.Flags().Lookup("worker-name")
	require.NotNil(t, workerNameFlag, "expected --worker-name flag")

	previewFlag := cmd.Flags().Lookup("preview")
	require.NotNil(t, previewFlag, "expected --preview flag")
	assert.Equal(t, "", previewFlag.DefValue)
}

func TestMintDeployCmd_InvalidPlatform(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=azure"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported platform")
}

func TestMintDeployCmd_CloudflareMissingEnv(t *testing.T) {
	// Save and restore env vars.
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()

	os.Unsetenv("CLOUDFLARE_ACCOUNT_ID")
	os.Unsetenv("CLOUDFLARE_API_TOKEN")

	// Mock wrangler whoami to fail (no session).
	oldWhoami := cf.WranglerWhoamiFn
	cf.WranglerWhoamiFn = func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("not logged in")
	}
	t.Cleanup(func() { cf.WranglerWhoamiFn = oldWhoami })

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Cloudflare credentials")
}

func TestMintDeployCmd_CloudflareInvalidWorkerName(t *testing.T) {
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()

	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare", "--worker-name=INVALID_NAME"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --worker-name")
}

func TestMintDeployCmd_CloudflareDryRun(t *testing.T) {
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()

	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare", "--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintDeployCmd_CloudflareDryRunShowsResolvedSource(t *testing.T) {
	withCFEnvVars(t)

	// Create the expected checkout path so os.Stat succeeds.
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, cf.DefaultWorkerSourceDir()), 0o755))
	t.Chdir(tmpDir)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare", "--dry-run"})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, cf.DefaultWorkerSourceDir(),
		"dry-run should show resolved checkout path")
	assert.NotContains(t, stdout, "embedded Worker adapter",
		"dry-run should not claim embedded source when checkout path exists")
}

func TestMintDeployCmd_CloudflareDryRunShowsEmbeddedWhenPathMissing(t *testing.T) {
	withCFEnvVars(t)

	// Run from a temp dir where the default checkout path does not exist.
	t.Chdir(t.TempDir())

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare", "--dry-run"})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "embedded Worker adapter",
		"dry-run should report embedded source when checkout path is missing")
	assert.NotContains(t, stdout, cf.DefaultWorkerSourceDir(),
		"dry-run should not show non-existent checkout path")
}

func TestMintDeployCmd_CloudflareDryRunWithExplicitSourceDir(t *testing.T) {
	withCFEnvVars(t)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare", "--dry-run", "--source-dir=/custom/worker/path"})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "/custom/worker/path",
		"dry-run should show the explicitly provided source-dir")
}

func TestMintDeployCmd_CloudflareDryRunPreview(t *testing.T) {
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()

	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare", "--dry-run", "--preview=bt-test-42"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintDeployCmd_CloudflareDryRunPreviewInvalidAlias(t *testing.T) {
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()

	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare", "--preview=INVALID_ALIAS"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --preview alias")
}

// --- Cloudflare non-dry-run deploy tests ---

// withMintCFWrangler overrides the mintCFWranglerFactory package-level
// variable to return a fake WranglerRunner for the test's lifetime.
func withMintCFWrangler(t *testing.T, runner cf.WranglerRunner) {
	t.Helper()
	old := mintCFWranglerFactory
	mintCFWranglerFactory = func(string) cf.WranglerRunner { return runner }
	t.Cleanup(func() { mintCFWranglerFactory = old })
}

// createMinimalWorkerSourceDir creates a temp directory with the minimal
// files required by validateSourceDir (src/index.ts, wrangler.toml,
// package.json) so Provision can succeed without real wrangler.
// Also stubs WASM build functions so auto-staging works without a Go
// toolchain in tests.
func createMinimalWorkerSourceDir(t *testing.T) string {
	t.Helper()
	withFakeWASMBuild(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src/index.ts"), []byte("export default {}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wrangler.toml"), []byte("name = \"test\""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644))
	return dir
}

// withFakeWASMBuild stubs the WASM build/copy functions so that
// provisioner tests don't require a real Go toolchain or GOROOT.
func withFakeWASMBuild(t *testing.T) {
	t.Helper()
	origBuild := cf.BuildWASMFn
	origCopy := cf.CopyWASMExecFn
	cf.BuildWASMFn = func(outPath, _, _ string) error {
		return os.WriteFile(outPath, []byte("fake-wasm"), 0o644)
	}
	cf.CopyWASMExecFn = func(destPath string) error {
		return os.WriteFile(destPath, []byte("fake-exec"), 0o644)
	}
	t.Cleanup(func() {
		cf.BuildWASMFn = origBuild
		cf.CopyWASMExecFn = origCopy
	})
}

// withCFEnvVars sets the required Cloudflare env vars and restores them
// after the test.
func withCFEnvVars(t *testing.T) {
	t.Helper()
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Cleanup(func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	})
}

func TestMintDeployCmd_CloudflareDurableDeploy(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	withMintCFWrangler(t, &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintDeployCmd_CloudflarePreviewDeploy(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	fake := &fakeCFWranglerRunner{
		deployURL: "https://bt-run-42-fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-run-42",
		"--source-dir=" + sourceDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)
	require.Len(t, fake.deployCalls, 1)
	assert.Equal(t, "bt-run-42", fake.deployCalls[0].previewAlias)
}

func TestMintDeployCmd_CloudflareCustomWorkerName(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	fake := &fakeCFWranglerRunner{
		deployURL: "https://custom-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--worker-name=custom-mint",
		"--source-dir=" + sourceDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)
	require.Len(t, fake.deployCalls, 1)
	assert.Equal(t, "custom-mint", fake.deployCalls[0].workerName)
}

func TestMintDeployCmd_CloudflareDeployFailure(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	withMintCFWrangler(t, &fakeCFWranglerRunner{
		deployErr: fmt.Errorf("wrangler deploy failed: exit status 1"),
	})

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deploying worker")
}

func TestMintDeployCmd_CloudflareDeployBadSourceDir(t *testing.T) {
	withCFEnvVars(t)
	withMintCFWrangler(t, &fakeCFWranglerRunner{})

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=/nonexistent/path",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deploying worker")
}

func TestMintDeployCmd_CloudflareDryRunWithPemDir(t *testing.T) {
	withCFEnvVars(t)

	roles := defaultMintRoles()
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	for _, role := range roles {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--pem-dir=" + pemDir,
	})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "Would bootstrap app set")
	assert.Contains(t, stdout, pemDir)
}

func TestMintDeployCmd_CloudflareDryRunWithBadPemDir(t *testing.T) {
	withCFEnvVars(t)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--pem-dir=/nonexistent/path",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--pem-dir")
}

func TestMintDeployCmd_CloudflareDeployWithPemDir(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	roles := defaultMintRoles()
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	for _, role := range roles {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}

	// Set up a fake GitHub API that handles /apps/<slug> (lookup)
	// and /app (verify PEM). The test PEM is valid so verification
	// passes when the server returns the matching app ID.
	appIDCounter := 100
	lastLookedUpID := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			fmt.Fprintf(w, `{"id": %d, "slug": "test-app"}`, lastLookedUpID)
			return
		}
		appIDCounter++
		lastLookedUpID = appIDCounter
		fmt.Fprintf(w, `{"id": %d, "slug": "%s"}`, appIDCounter, r.URL.Path[len("/apps/"):])
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--pem-dir=" + pemDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)

	// Verify ROLE_APP_IDS was set as a Worker env var during deploy.
	require.Len(t, fake.deployCalls, 1)
	deployEnvVars := fake.deployCalls[0].envVars
	assert.Contains(t, deployEnvVars, "ROLE_APP_IDS",
		"ROLE_APP_IDS should be passed as a Worker env var")
	assert.Contains(t, deployEnvVars["ROLE_APP_IDS"], "coder",
		"ROLE_APP_IDS should contain role names")

	// Verify PEM secrets were stored via PutSecret.
	assert.GreaterOrEqual(t, len(fake.secretCalls), len(roles),
		"should store at least one PEM secret per role")
	secretNames := make(map[string]bool)
	for _, call := range fake.secretCalls {
		secretNames[call.secretName] = true
	}
	// Check that known roles produced the expected secret names.
	assert.True(t, secretNames["CODER_APP_PEM"], "expected CODER_APP_PEM secret")
}

func TestMintDeployCmd_CloudflarePreviewDeployWithPemDir(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	roles := defaultMintRoles()
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	for _, role := range roles {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}

	appIDCounter := 200
	lastLookedUpID := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			fmt.Fprintf(w, `{"id": %d, "slug": "test-app"}`, lastLookedUpID)
			return
		}
		appIDCounter++
		lastLookedUpID = appIDCounter
		fmt.Fprintf(w, `{"id": %d, "slug": "%s"}`, appIDCounter, r.URL.Path[len("/apps/"):])
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-test-42",
		"--source-dir=" + sourceDir,
		"--pem-dir=" + pemDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	assert.Equal(t, "bt-test-42", fake.deployCalls[0].previewAlias,
		"preview alias should be passed to deploy")
	assert.Contains(t, fake.deployCalls[0].envVars, "ROLE_APP_IDS",
		"ROLE_APP_IDS should be set for preview deploys too")

	// For preview deploys, PEM secrets are passed through Deploy via
	// --secrets-file (wrangler secret put does not support --preview-alias).
	require.NotNil(t, fake.deployCalls[0].secrets,
		"PEM secrets should be passed through Deploy for preview deploys")
	assert.Contains(t, fake.deployCalls[0].secrets, "CODER_APP_PEM",
		"expected CODER_APP_PEM in deploy secrets")
	assert.Empty(t, fake.secretCalls,
		"PutSecret should NOT be called for preview deploys (secrets go through Deploy)")
}

func TestMintDeployCmd_CloudflareDeployPemSecretFailure(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	roles := defaultMintRoles()
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	for _, role := range roles {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}

	appIDCounter := 300
	lastLookedUpID := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			fmt.Fprintf(w, `{"id": %d, "slug": "test-app"}`, lastLookedUpID)
			return
		}
		appIDCounter++
		lastLookedUpID = appIDCounter
		fmt.Fprintf(w, `{"id": %d, "slug": "%s"}`, appIDCounter, r.URL.Path[len("/apps/"):])
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	fake := &fakeCFWranglerRunner{
		deployURL:    "https://fullsend-mint.workers.dev",
		secretPutErr: fmt.Errorf("simulated secret storage failure"),
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--pem-dir=" + pemDir,
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storing PEM for role")
	assert.Contains(t, err.Error(), "already stored")
	assert.Contains(t, err.Error(), "simulated secret storage failure")
}

func TestMintDeployCmd_CloudflareNoWarningForPemDir(t *testing.T) {
	withCFEnvVars(t)

	// Capture stderr to verify --pem-dir does NOT produce a warning
	// when used with --platform=cloudflare.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--pem-dir=/some/path",
		"--dry-run",
	})
	// Ignore the error (dry-run may fail due to nonexistent pem-dir).
	_ = cmd.Execute()

	w.Close()
	os.Stderr = oldStderr

	out, _ := io.ReadAll(r)
	stderr := string(out)
	assert.NotContains(t, stderr, "--pem-dir is a GCP flag",
		"--pem-dir should not produce a GCP warning on cloudflare")
}

func TestMintDeployCmd_CloudflareDeployWithConfigFlags(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--allowed-orgs=acme,bigcorp",
		"--per-repo-wif-repos=acme/widget,bigcorp/gadget",
		"--workflow-host-repos=fullsend-ai/fullsend",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	assert.Equal(t, "acme,bigcorp", envVars["ALLOWED_ORGS"],
		"ALLOWED_ORGS should be set from --allowed-orgs flag")
	assert.Equal(t, "acme/widget,bigcorp/gadget", envVars["PER_REPO_WIF_REPOS"],
		"PER_REPO_WIF_REPOS should be set from --per-repo-wif-repos flag")
	assert.Equal(t, "fullsend-ai/fullsend", envVars["WORKFLOW_HOST_REPOS"],
		"WORKFLOW_HOST_REPOS should be set from --workflow-host-repos flag")
}

func TestMintDeployCmd_CloudflarePreviewDeployWithConfigFlags(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-test-99",
		"--source-dir=" + sourceDir,
		"--allowed-orgs=acme",
		"--per-repo-wif-repos=acme/widget",
		"--workflow-host-repos=fullsend-ai/fullsend",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	assert.Equal(t, "bt-test-99", fake.deployCalls[0].previewAlias,
		"preview alias should be passed to deploy")
	envVars := fake.deployCalls[0].envVars
	assert.Equal(t, "acme", envVars["ALLOWED_ORGS"],
		"ALLOWED_ORGS should be set for preview deploys")
	assert.Equal(t, "acme/widget", envVars["PER_REPO_WIF_REPOS"],
		"PER_REPO_WIF_REPOS should be set for preview deploys")
	assert.Equal(t, "fullsend-ai/fullsend", envVars["WORKFLOW_HOST_REPOS"],
		"WORKFLOW_HOST_REPOS should be set for preview deploys")
}

func TestMintDeployCmd_CloudflarePublicFlagSetsPRWR(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--public",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	assert.Equal(t, "*", envVars["PER_REPO_WIF_REPOS"],
		"--public should set PER_REPO_WIF_REPOS to *")
}

func TestMintDeployCmd_CloudflarePublicConflictsWithPerRepoWIF(t *testing.T) {
	withCFEnvVars(t)

	// --public + --per-repo-wif-repos should be a hard error.
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--public",
		"--per-repo-wif-repos=acme/widget",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive",
		"--public + --per-repo-wif-repos should fail with mutual exclusion error")
}

func TestMintDeployCmd_CloudflareDryRunWithConfigFlags(t *testing.T) {
	withCFEnvVars(t)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--allowed-orgs=acme",
		"--public",
	})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "ALLOWED_ORGS=acme")
	assert.Contains(t, stdout, "PER_REPO_WIF_REPOS=*")
}

func TestMintDeployCmd_CloudflareNoConfigFlagsOmitsEnvVars(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	_, hasAllowedOrgs := envVars["ALLOWED_ORGS"]
	_, hasPRWR := envVars["PER_REPO_WIF_REPOS"]
	_, hasWHR := envVars["WORKFLOW_HOST_REPOS"]
	assert.False(t, hasAllowedOrgs, "ALLOWED_ORGS should not be set when --allowed-orgs is omitted")
	assert.False(t, hasPRWR, "PER_REPO_WIF_REPOS should not be set when --per-repo-wif-repos is omitted")
	assert.False(t, hasWHR, "WORKFLOW_HOST_REPOS should not be set when --workflow-host-repos is omitted")
	// On a durable deploy, ALLOWED_WORKFLOW_FILES should NOT be set when
	// --allowed-workflow-files is omitted — preserves the existing Worker
	// value via --keep-vars.
	_, hasAWF := envVars["ALLOWED_WORKFLOW_FILES"]
	assert.False(t, hasAWF, "durable deploy: ALLOWED_WORKFLOW_FILES should not be set when --allowed-workflow-files is omitted")
}

func TestMintDeployCmd_CloudflareConfigFlagsWarnOnGCP(t *testing.T) {
	// CF-specific config flags should produce a warning when used with GCP.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=gcp",
		"--project=my-project-id",
		"--dry-run",
		"--allowed-orgs=acme",
	})
	_ = cmd.Execute()

	w.Close()
	os.Stderr = oldStderr

	out, _ := io.ReadAll(r)
	stderr := string(out)
	assert.Contains(t, stderr, "--allowed-orgs is a Cloudflare flag",
		"--allowed-orgs should produce a warning on GCP")
}

func TestMintDeployCmd_CloudflarePublicNoWarning(t *testing.T) {
	withCFEnvVars(t)

	// --public should not produce a warning on cloudflare.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--public",
		"--dry-run",
	})
	_ = cmd.Execute()

	w.Close()
	os.Stderr = oldStderr

	out, _ := io.ReadAll(r)
	stderr := string(out)
	assert.NotContains(t, stderr, "--public is a GCP flag",
		"--public should not produce a GCP warning on cloudflare")
}

func TestMintDeployCmd_NewFlagsExist(t *testing.T) {
	cmd := newMintDeployCmd()

	allowedOrgsFlag := cmd.Flags().Lookup("allowed-orgs")
	require.NotNil(t, allowedOrgsFlag, "expected --allowed-orgs flag")

	perRepoWIFReposFlag := cmd.Flags().Lookup("per-repo-wif-repos")
	require.NotNil(t, perRepoWIFReposFlag, "expected --per-repo-wif-repos flag")

	workflowHostReposFlag := cmd.Flags().Lookup("workflow-host-repos")
	require.NotNil(t, workflowHostReposFlag, "expected --workflow-host-repos flag")

	appSetFlag := cmd.Flags().Lookup("app-set")
	require.NotNil(t, appSetFlag, "expected --app-set flag")

	allowedWorkflowFilesFlag := cmd.Flags().Lookup("allowed-workflow-files")
	require.NotNil(t, allowedWorkflowFilesFlag, "expected --allowed-workflow-files flag")

	rolesFlag := cmd.Flags().Lookup("roles")
	require.NotNil(t, rolesFlag, "expected --roles flag")
}

func TestMintDeployCmd_CloudflareAllowedWorkflowFilesOmitted(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	// When --allowed-workflow-files is omitted on a DURABLE deploy,
	// ALLOWED_WORKFLOW_FILES should NOT be included in env vars —
	// the existing Worker value is preserved via --keep-vars.
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	_, hasAWF := envVars["ALLOWED_WORKFLOW_FILES"]
	assert.False(t, hasAWF,
		"durable deploy: ALLOWED_WORKFLOW_FILES should not be set when flag is omitted (preserves existing Worker value)")
}

func TestMintDeployCmd_CloudflarePreviewOmittedAWFDefaultsStar(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://bt-test-fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	// Preview deploy without --allowed-workflow-files should default
	// ALLOWED_WORKFLOW_FILES=* so the preview is usable out of the box.
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-test",
		"--source-dir=" + sourceDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	val, hasAWF := envVars["ALLOWED_WORKFLOW_FILES"]
	assert.True(t, hasAWF,
		"ALLOWED_WORKFLOW_FILES should be set on preview when flag is omitted")
	assert.Equal(t, "*", val,
		"ALLOWED_WORKFLOW_FILES should default to * on preview when omitted")
}

func TestMintDeployCmd_CloudflarePreviewExplicitAWFUsesValue(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://bt-test-fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	// Preview deploy with explicit --allowed-workflow-files should use
	// that value, not the * default.
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-test",
		"--source-dir=" + sourceDir,
		"--allowed-workflow-files=dispatch.yml",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	assert.Equal(t, "dispatch.yml", envVars["ALLOWED_WORKFLOW_FILES"],
		"ALLOWED_WORKFLOW_FILES should use explicit value on preview")
}

func TestMintDeployCmd_CloudflarePreviewDryRunOmittedAWFShowsStar(t *testing.T) {
	withCFEnvVars(t)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Preview dry-run without --allowed-workflow-files should show
	// ALLOWED_WORKFLOW_FILES=* and the wildcard warning.
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-test",
		"--dry-run",
	})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "ALLOWED_WORKFLOW_FILES=*",
		"preview dry-run should show ALLOWED_WORKFLOW_FILES=* when flag is omitted")
	assert.Contains(t, stdout, "allow any workflow basename",
		"preview dry-run should include a warning about * allowing any workflow")
}

func TestMintDeployCmd_CloudflareAllowedWorkflowFilesExplicit(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--allowed-workflow-files=dispatch.yml,fullsend.yml",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	assert.Equal(t, "dispatch.yml,fullsend.yml", envVars["ALLOWED_WORKFLOW_FILES"],
		"ALLOWED_WORKFLOW_FILES should use the explicit value")
}

func TestMintDeployCmd_CloudflareAllowedWorkflowFilesDryRunOmitted(t *testing.T) {
	withCFEnvVars(t)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// When --allowed-workflow-files is omitted on a DURABLE dry-run,
	// ALLOWED_WORKFLOW_FILES should NOT be shown — the existing Worker
	// value is preserved via --keep-vars.
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
	})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.NotContains(t, stdout, "ALLOWED_WORKFLOW_FILES",
		"durable dry-run should not mention ALLOWED_WORKFLOW_FILES when flag is omitted")
}

func TestMintDeployCmd_CloudflareAllowedWorkflowFilesDryRunWildcard(t *testing.T) {
	withCFEnvVars(t)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// When --allowed-workflow-files=* is explicit, dry-run should show
	// the value and include the warning.
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--allowed-workflow-files=*",
	})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "ALLOWED_WORKFLOW_FILES=*",
		"dry-run should show the explicit ALLOWED_WORKFLOW_FILES=* value")
	assert.Contains(t, stdout, "allow any workflow basename",
		"dry-run should include a warning about * allowing any workflow")
}

func TestMintDeployCmd_CloudflareAllowedWorkflowFilesDryRunExplicit(t *testing.T) {
	withCFEnvVars(t)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--allowed-workflow-files=dispatch.yml",
	})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "ALLOWED_WORKFLOW_FILES=dispatch.yml",
		"dry-run should show the explicit value")
	assert.NotContains(t, stdout, "allow any workflow basename",
		"no wildcard warning when explicit non-* value is set")
}

func TestMintDeployCmd_CloudflarePublicAloneWorks(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	// --public alone should still set PER_REPO_WIF_REPOS=*.
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--public",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	assert.Equal(t, "*", envVars["PER_REPO_WIF_REPOS"],
		"--public alone should set PER_REPO_WIF_REPOS to *")
}

func TestMintDeployCmd_CloudflarePerRepoWIFAloneWorks(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	// --per-repo-wif-repos alone should still work.
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--per-repo-wif-repos=acme/widget",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	assert.Equal(t, "acme/widget", envVars["PER_REPO_WIF_REPOS"],
		"--per-repo-wif-repos alone should set the exact value")
}

// --- Empty-flag-clears-var semantics tests ---

func TestMintDeployCmd_CloudflareEmptyAllowedOrgsClearsBinding(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	// --allowed-orgs= (explicit empty) should include ALLOWED_ORGS with
	// an empty value. For durable deploys, --keep-vars clears the
	// existing binding; for preview deploys, the var is set to "".
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--allowed-orgs=",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	val, present := envVars["ALLOWED_ORGS"]
	assert.True(t, present, "ALLOWED_ORGS should be present when --allowed-orgs is explicitly empty")
	assert.Equal(t, "", val, "ALLOWED_ORGS should be empty string to clear existing binding")
}

func TestMintDeployCmd_CloudflareEmptyPerRepoWIFReposClearsBinding(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--per-repo-wif-repos=",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	val, present := envVars["PER_REPO_WIF_REPOS"]
	assert.True(t, present, "PER_REPO_WIF_REPOS should be present when --per-repo-wif-repos is explicitly empty")
	assert.Equal(t, "", val, "PER_REPO_WIF_REPOS should be empty string to clear existing binding")
}

func TestMintDeployCmd_CloudflareEmptyWorkflowHostReposClearsBinding(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--workflow-host-repos=",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	val, present := envVars["WORKFLOW_HOST_REPOS"]
	assert.True(t, present, "WORKFLOW_HOST_REPOS should be present when --workflow-host-repos is explicitly empty")
	assert.Equal(t, "", val, "WORKFLOW_HOST_REPOS should be empty string to clear existing binding")
}

func TestMintDeployCmd_CloudflareEmptyAllowedWorkflowFilesClearsBinding(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--allowed-workflow-files=",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	val, present := envVars["ALLOWED_WORKFLOW_FILES"]
	assert.True(t, present, "ALLOWED_WORKFLOW_FILES should be present when --allowed-workflow-files is explicitly empty")
	assert.Equal(t, "", val, "ALLOWED_WORKFLOW_FILES should be empty string to clear existing binding")
}

func TestMintDeployCmd_CloudflareMultipleEmptyFlagsClearBindings(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	// Simulate switching from dual/per-repo mode to org-only:
	// keep ALLOWED_ORGS with a value, clear PER_REPO_WIF_REPOS.
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--allowed-orgs=fullsand-ai",
		"--per-repo-wif-repos=",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	assert.Equal(t, "fullsand-ai", envVars["ALLOWED_ORGS"],
		"ALLOWED_ORGS should be set to the provided value")
	prwr, present := envVars["PER_REPO_WIF_REPOS"]
	assert.True(t, present, "PER_REPO_WIF_REPOS should be present (cleared)")
	assert.Equal(t, "", prwr, "PER_REPO_WIF_REPOS should be empty to clear binding")
	// WORKFLOW_HOST_REPOS was not set — should not be present.
	_, hasWHR := envVars["WORKFLOW_HOST_REPOS"]
	assert.False(t, hasWHR, "WORKFLOW_HOST_REPOS should not be present when flag is omitted")
}

func TestMintDeployCmd_CloudflareDryRunShowsClearedVars(t *testing.T) {
	withCFEnvVars(t)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--allowed-orgs=acme",
		"--per-repo-wif-repos=",
	})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "Would set ALLOWED_ORGS=acme",
		"dry-run should show setting ALLOWED_ORGS")
	assert.Contains(t, stdout, "Would clear PER_REPO_WIF_REPOS",
		"dry-run should show clearing PER_REPO_WIF_REPOS")
}

func TestMintDeployCmd_CloudflareAppSetNonDefault(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	roles := defaultMintRoles()
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	for _, role := range roles {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}

	// Set up a fake GitHub API. The key check is that app slugs use
	// the custom app set name ("fullsand-ai-coder" etc.), not the
	// default ("fullsend-ai-coder").
	var lookedUpSlugs []string
	appIDCounter := 400
	lastLookedUpID := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			fmt.Fprintf(w, `{"id": %d, "slug": "test-app"}`, lastLookedUpID)
			return
		}
		slug := r.URL.Path[len("/apps/"):]
		lookedUpSlugs = append(lookedUpSlugs, slug)
		appIDCounter++
		lastLookedUpID = appIDCounter
		fmt.Fprintf(w, `{"id": %d, "slug": "%s"}`, appIDCounter, slug)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--pem-dir=" + pemDir,
		"--app-set=fullsand-ai",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	// Verify that the custom app set was used for slug lookups.
	for _, slug := range lookedUpSlugs {
		assert.True(t, strings.HasPrefix(slug, "fullsand-ai-"),
			"expected slug to use custom app set prefix, got %q", slug)
	}
}

func TestMintDeployCmd_CloudflareAppSetDefault(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	roles := defaultMintRoles()
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	for _, role := range roles {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}

	var lookedUpSlugs []string
	appIDCounter := 500
	lastLookedUpID := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			fmt.Fprintf(w, `{"id": %d, "slug": "test-app"}`, lastLookedUpID)
			return
		}
		slug := r.URL.Path[len("/apps/"):]
		lookedUpSlugs = append(lookedUpSlugs, slug)
		appIDCounter++
		lastLookedUpID = appIDCounter
		fmt.Fprintf(w, `{"id": %d, "slug": "%s"}`, appIDCounter, slug)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	// Omit --app-set — should use default "fullsend-ai".
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--pem-dir=" + pemDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)

	// Verify that the default app set was used.
	for _, slug := range lookedUpSlugs {
		assert.True(t, strings.HasPrefix(slug, "fullsend-ai-"),
			"expected slug to use default app set prefix, got %q", slug)
	}
}

// --- --roles flag tests ---

func TestParseRolesFlag(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []string
		wantErr string
	}{
		{
			name:  "standard roles",
			input: "fullsend,triage,coder",
			want:  []string{"coder", "fullsend", "triage"},
		},
		{
			name:  "with e2e",
			input: "fullsend,triage,coder,review,retro,prioritize,e2e",
			want:  []string{"coder", "e2e", "fullsend", "prioritize", "retro", "review", "triage"},
		},
		{
			name:  "alias fix resolved to coder",
			input: "fullsend,fix,triage",
			want:  []string{"coder", "fullsend", "triage"},
		},
		{
			name:  "alias code resolved to coder",
			input: "code,fullsend",
			want:  []string{"coder", "fullsend"},
		},
		{
			name:  "deduplicate",
			input: "coder,coder,triage",
			want:  []string{"coder", "triage"},
		},
		{
			name:  "deduplicate after alias resolution",
			input: "coder,fix",
			want:  []string{"coder"},
		},
		{
			name:  "whitespace trimmed",
			input: " coder , triage ",
			want:  []string{"coder", "triage"},
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: "must not be empty",
		},
		{
			name:    "whitespace only",
			input:   "  ",
			wantErr: "must not be empty",
		},
		{
			name:    "invalid role name",
			input:   "coder,INVALID",
			wantErr: "invalid role",
		},
		{
			name:    "role with double hyphen",
			input:   "coder,my--role",
			wantErr: "invalid role",
		},
		{
			name:  "empty tokens between commas",
			input: "coder,,",
			want:  []string{"coder"},
		},
		{
			name:    "all empty after split",
			input:   ",,,",
			wantErr: "must contain at least one valid role name",
		},
		{
			name:    "whitespace-only segments",
			input:   " , , ",
			wantErr: "must contain at least one valid role name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRolesFlag(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMintDeployCmd_CloudflareRolesOmittedUsesDefaults(t *testing.T) {
	// When --roles is omitted, --pem-dir should require exactly defaultMintRoles() PEMs.
	withCFEnvVars(t)

	roles := defaultMintRoles()
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	for _, role := range roles {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--pem-dir=" + pemDir,
	})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "Would bootstrap app set")
	// e2e should NOT be required when --roles is omitted.
	assert.NotContains(t, stdout, "e2e")
}

func TestMintDeployCmd_CloudflareRolesWithE2E(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	// Create PEMs for default roles + e2e.
	rolesWithE2E := append(defaultMintRoles(), "e2e")
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	for _, role := range rolesWithE2E {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}

	appIDCounter := 600
	lastLookedUpID := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			fmt.Fprintf(w, `{"id": %d, "slug": "test-app"}`, lastLookedUpID)
			return
		}
		appIDCounter++
		lastLookedUpID = appIDCounter
		fmt.Fprintf(w, `{"id": %d, "slug": "%s"}`, appIDCounter, r.URL.Path[len("/apps/"):])
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--pem-dir=" + pemDir,
		"--roles=fullsend,triage,coder,review,retro,prioritize,e2e",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	// Verify ROLE_APP_IDS contains e2e.
	require.Len(t, fake.deployCalls, 1)
	deployEnvVars := fake.deployCalls[0].envVars
	assert.Contains(t, deployEnvVars, "ROLE_APP_IDS",
		"ROLE_APP_IDS should be passed as a Worker env var")
	assert.Contains(t, deployEnvVars["ROLE_APP_IDS"], "e2e",
		"ROLE_APP_IDS should contain e2e when --roles includes it")

	// Verify e2e PEM secret was stored via PutSecret.
	secretNames := make(map[string]bool)
	for _, call := range fake.secretCalls {
		secretNames[call.secretName] = true
	}
	assert.True(t, secretNames["E2E_APP_PEM"], "expected E2E_APP_PEM secret")
}

func TestMintDeployCmd_CloudflareRolesInvalidRole(t *testing.T) {
	withCFEnvVars(t)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--roles=coder,INVALID_ROLE",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
}

func TestMintDeployCmd_CloudflareRolesEmpty(t *testing.T) {
	withCFEnvVars(t)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--roles=",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestMintDeployCmd_CloudflareRolesAliasResolution(t *testing.T) {
	withCFEnvVars(t)

	// Using "fix" should require coder.pem (not fix.pem).
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(pemDir, "coder.pem"), testPEM, 0o600))

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--pem-dir=" + pemDir,
		"--roles=fix",
	})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	_, _ = io.ReadAll(r)

	require.NoError(t, err, "fix alias should resolve to coder and use coder.pem")
}

func TestMintDeployCmd_CloudflareRolesMissingPEM(t *testing.T) {
	withCFEnvVars(t)

	// Create PEMs only for coder, not for e2e.
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(pemDir, "coder.pem"), testPEM, 0o600))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--pem-dir=" + pemDir,
		"--roles=coder,e2e",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "e2e")
	assert.Contains(t, err.Error(), "missing PEM")
}

func TestMintDeployCmd_CloudflareRolesPreviewDeploy(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)

	rolesWithE2E := []string{"coder", "e2e"}
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	for _, role := range rolesWithE2E {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}

	appIDCounter := 700
	lastLookedUpID := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			fmt.Fprintf(w, `{"id": %d, "slug": "test-app"}`, lastLookedUpID)
			return
		}
		appIDCounter++
		lastLookedUpID = appIDCounter
		fmt.Fprintf(w, `{"id": %d, "slug": "%s"}`, appIDCounter, r.URL.Path[len("/apps/"):])
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	fake := &fakeCFWranglerRunner{}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-test-99",
		"--source-dir=" + sourceDir,
		"--pem-dir=" + pemDir,
		"--roles=coder,e2e",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	// ROLE_APP_IDS should contain both coder and e2e.
	assert.Contains(t, fake.deployCalls[0].envVars["ROLE_APP_IDS"], "e2e")
	assert.Contains(t, fake.deployCalls[0].envVars["ROLE_APP_IDS"], "coder")

	// For preview deploys, PEM secrets are passed through Deploy.
	require.NotNil(t, fake.deployCalls[0].secrets)
	assert.Contains(t, fake.deployCalls[0].secrets, "E2E_APP_PEM",
		"expected E2E_APP_PEM in deploy secrets for preview")
	assert.Contains(t, fake.deployCalls[0].secrets, "CODER_APP_PEM",
		"expected CODER_APP_PEM in deploy secrets for preview")
	// PutSecret should not be called for preview deploys.
	assert.Empty(t, fake.secretCalls)
}

func TestMintDeployCmd_CloudflareWranglerSession(t *testing.T) {
	// Test that CF deploy works without CLOUDFLARE_API_TOKEN when a
	// wrangler session is active and CLOUDFLARE_ACCOUNT_ID is set.
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Unsetenv("CLOUDFLARE_API_TOKEN")
	t.Cleanup(func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	})

	// Mock wrangler whoami to succeed.
	oldWhoami := cf.WranglerWhoamiFn
	cf.WranglerWhoamiFn = func(ctx context.Context) (string, error) {
		return "Logged in as user@example.com\n", nil
	}
	t.Cleanup(func() { cf.WranglerWhoamiFn = oldWhoami })

	sourceDir := createMinimalWorkerSourceDir(t)
	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)
	require.Len(t, fake.deployCalls, 1)
}

func TestMintDeployCmd_CloudflareNoCredentialsError(t *testing.T) {
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	os.Unsetenv("CLOUDFLARE_ACCOUNT_ID")
	os.Unsetenv("CLOUDFLARE_API_TOKEN")
	t.Cleanup(func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	})

	// Mock wrangler whoami to fail.
	oldWhoami := cf.WranglerWhoamiFn
	cf.WranglerWhoamiFn = func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("not logged in")
	}
	t.Cleanup(func() { cf.WranglerWhoamiFn = oldWhoami })

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Cloudflare credentials")
}

func TestMintDeployCmd_GCPDefaultPlatform(t *testing.T) {
	// Default platform is GCP, so omitting --platform should require --project.
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintDeployCmd_GCPExplicitPlatform(t *testing.T) {
	// Explicitly setting --platform=gcp should still require --project.
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=gcp"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintDeployCmd_GCPPlatformDryRun(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=gcp", "--project=my-project-id", "--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

// --- platform flag warning tests ---

func TestMintDeployCmd_WarnsGCPFlagsOnCloudflare(t *testing.T) {
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()

	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	// Capture stderr to check warnings.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=cloudflare", "--project=my-project", "--dry-run"})
	_ = cmd.Execute()

	w.Close()
	os.Stderr = oldStderr

	out, _ := io.ReadAll(r)
	stderr := string(out)
	assert.Contains(t, stderr, "--project is a GCP flag")
	assert.Contains(t, stderr, "--platform=cloudflare")
}

func TestMintDeployCmd_WarnsCFFlagsOnGCP(t *testing.T) {
	// Capture stderr to check warnings.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=gcp", "--project=my-project-id", "--worker-name=test", "--dry-run"})
	_ = cmd.Execute()

	w.Close()
	os.Stderr = oldStderr

	out, _ := io.ReadAll(r)
	stderr := string(out)
	assert.Contains(t, stderr, "--worker-name is a Cloudflare flag")
	assert.Contains(t, stderr, "--platform=gcp")
}

func TestMintDeployCmd_NoWarningForCorrectPlatformFlags(t *testing.T) {
	// Capture stderr to check no warnings.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--platform=gcp", "--project=my-project-id", "--dry-run"})
	_ = cmd.Execute()

	w.Close()
	os.Stderr = oldStderr

	out, _ := io.ReadAll(r)
	stderr := string(out)
	assert.NotContains(t, stderr, "WARNING:")
}

// --- lookupAppID tests ---

func TestLookupAppID_Success(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/apps/fullsend-ai-coder", r.URL.Path)
		assert.Empty(t, r.Header.Get("Authorization"), "unauthenticated request should have no Authorization header")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 12345, "slug": "fullsend-ai-coder", "client_id": "Iv1.abc123"}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	appID, err := lookupAppID(context.Background(), "fullsend-ai-coder")
	require.NoError(t, err)
	assert.Equal(t, 12345, appID)
}

func TestLookupAppID_EscapesSlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/apps/my%2Fapp", r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 42}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	id, err := lookupAppID(context.Background(), "my/app")
	require.NoError(t, err)
	assert.Equal(t, 42, id)
}

func TestLookupAppID_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	_, err := lookupAppID(context.Background(), "nonexistent-app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestLookupAppID_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	_, err := lookupAppID(context.Background(), "some-app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestLookupAppID_RateLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
	}{
		{"Forbidden", http.StatusForbidden},
		{"TooManyRequests", http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GH_TOKEN", "")
			t.Setenv("GITHUB_TOKEN", "")

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()

			orig := githubAPIBaseURL
			githubAPIBaseURL = srv.URL
			defer func() { githubAPIBaseURL = orig }()

			_, err := lookupAppID(context.Background(), "some-app")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "rate limit")
			assert.Contains(t, err.Error(), "set GH_TOKEN or GITHUB_TOKEN")
		})
	}
}

func TestLookupAppID_AuthenticatedWithGHToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_test_token_123")
	t.Setenv("GITHUB_TOKEN", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer ghp_test_token_123", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	appID, err := lookupAppID(context.Background(), "test-app")
	require.NoError(t, err)
	assert.Equal(t, 99, appID)
}

func TestLookupAppID_AuthenticatedWithGITHUBToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "ghs_fallback_token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer ghs_fallback_token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 77}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	appID, err := lookupAppID(context.Background(), "test-app")
	require.NoError(t, err)
	assert.Equal(t, 77, appID)
}

func TestLookupAppID_GHTokenTakesPrecedence(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_primary")
	t.Setenv("GITHUB_TOKEN", "ghs_secondary")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer ghp_primary", r.Header.Get("Authorization"),
			"GH_TOKEN should take precedence over GITHUB_TOKEN")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 55}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	appID, err := lookupAppID(context.Background(), "test-app")
	require.NoError(t, err)
	assert.Equal(t, 55, appID)
}

func TestLookupAppID_RateLimitAuthenticated(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_some_token")
	t.Setenv("GITHUB_TOKEN", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	_, err := lookupAppID(context.Background(), "some-app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limit")
	assert.NotContains(t, err.Error(), "set GH_TOKEN or GITHUB_TOKEN",
		"authenticated rate limit error should not suggest setting a token")
}

// --- verifyPEMMatchesApp tests ---

func TestVerifyPEMMatchesApp_Success(t *testing.T) {
	testPEM := generateTestPEM(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/app", r.URL.Path)
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 12345, "slug": "test-app"}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	err := verifyPEMMatchesApp(context.Background(), testPEM, 12345, "test-app")
	require.NoError(t, err)
}

func TestVerifyPEMMatchesApp_WrongKey(t *testing.T) {
	testPEM := generateTestPEM(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	err := verifyPEMMatchesApp(context.Background(), testPEM, 12345, "test-app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

func TestVerifyPEMMatchesApp_AppIDMismatch(t *testing.T) {
	testPEM := generateTestPEM(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999, "slug": "different-app"}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	err := verifyPEMMatchesApp(context.Background(), testPEM, 12345, "test-app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authenticated as app 99999 but expected app 12345")
}

// --- listPEMFiles tests ---

func TestListPEMFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "coder.pem"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "review.pem"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o600))

	files := listPEMFiles(dir)
	assert.Equal(t, []string{"coder.pem", "review.pem"}, files)
}

func TestListPEMFiles_EmptyDir(t *testing.T) {
	files := listPEMFiles(t.TempDir())
	assert.Empty(t, files)
}

func TestListPEMFiles_NonexistentDir(t *testing.T) {
	files := listPEMFiles("/nonexistent/path")
	assert.Nil(t, files)
}

// --- loadAppSetPEMs tests ---

func TestLoadAppSetPEMs_Success(t *testing.T) {
	roles := defaultMintRoles()
	testPEM := generateTestPEM(t)

	pemDir := t.TempDir()
	for _, role := range roles {
		err := os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600)
		require.NoError(t, err)
	}

	appIDCounter := 100
	lastLookedUpID := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			fmt.Fprintf(w, `{"id": %d, "slug": "test-app"}`, lastLookedUpID)
			return
		}
		appIDCounter++
		lastLookedUpID = appIDCounter
		fmt.Fprintf(w, `{"id": %d, "slug": "%s"}`, appIDCounter, r.URL.Path[len("/apps/"):])
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	agentPEMs, agentAppIDs, err := loadAppSetPEMs(context.Background(), pemDir, "fullsend-ai", nil)
	require.NoError(t, err)
	assert.Len(t, agentPEMs, len(roles))
	assert.Len(t, agentAppIDs, len(roles))

	for _, role := range roles {
		assert.Contains(t, agentPEMs, role, "expected PEM for role %s", role)
		assert.NotEmpty(t, agentPEMs[role])
		assert.Contains(t, agentAppIDs, role, "expected app ID for role %s", role)
		assert.NotEmpty(t, agentAppIDs[role])
	}
}

func TestLoadAppSetPEMs_MissingPEM(t *testing.T) {
	pemDir := t.TempDir()
	// Only write one PEM — the rest will be missing.
	err := os.WriteFile(filepath.Join(pemDir, "fullsend.pem"), []byte("fake"), 0o600)
	require.NoError(t, err)

	_, _, err = loadAppSetPEMs(context.Background(), pemDir, "fullsend-ai", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing PEM file for role")
}

func TestLoadAppSetPEMs_InvalidAppSet(t *testing.T) {
	_, _, err := loadAppSetPEMs(context.Background(), t.TempDir(), "INVALID CHARS", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid app set")
}

func TestLoadAppSetPEMs_InvalidPEM(t *testing.T) {
	pemDir := t.TempDir()
	testPEM := generateTestPEM(t)
	roles := defaultMintRoles()
	for _, role := range roles {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}
	// Overwrite one with invalid content.
	require.NoError(t, os.WriteFile(filepath.Join(pemDir, "coder.pem"), []byte("not-a-pem"), 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			fmt.Fprintln(w, `{"id": 1, "slug": "test-app"}`)
			return
		}
		fmt.Fprintln(w, `{"id": 999, "slug": "test-app"}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	_, _, err := loadAppSetPEMs(context.Background(), pemDir, "fullsend-ai", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid PEM for role")
}

func TestLoadAppSetPEMs_BadDir(t *testing.T) {
	_, _, err := loadAppSetPEMs(context.Background(), "/nonexistent/path", "fullsend-ai", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--pem-dir")
}

func TestLoadAppSetPEMs_FileNotDir(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "notadir.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("dummy"), 0o600))

	_, _, err := loadAppSetPEMs(context.Background(), tmpFile, "fullsend-ai", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a directory")
}

func TestGitHubHTTPClient_HasTimeout(t *testing.T) {
	assert.Equal(t, 30*time.Second, githubHTTPClient.Timeout)
}

func TestLoadAppSetPEMs_AppNotFound(t *testing.T) {
	roles := defaultMintRoles()
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	for _, role := range roles {
		err := os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600)
		require.NoError(t, err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	_, _, err := loadAppSetPEMs(context.Background(), pemDir, "fullsend-ai", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "looking up app ID")
	assert.Contains(t, err.Error(), "not found")
}

// --- enroll command tests ---

func TestMintEnrollCmd_Flags(t *testing.T) {
	cmd := newMintEnrollCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")
	assert.Equal(t, "us-central1", regionFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")

	assert.Nil(t, cmd.Flags().Lookup("app-set"))
	assert.Nil(t, cmd.Flags().Lookup("role-app-ids"))
	assert.Nil(t, cmd.Flags().Lookup("roles"))
}

func TestMintEnrollCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestMintEnrollCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintEnrollCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll", "acme", "--project=BAD"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

// --- delete command tests ---

func TestMintDeleteCmd_Flags(t *testing.T) {
	cmd := newMintDeleteCmd()

	platformFlag := cmd.Flags().Lookup("platform")
	require.NotNil(t, platformFlag, "expected --platform flag")
	assert.Equal(t, "gcp", platformFlag.DefValue)

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")
	assert.Equal(t, "us-central1", regionFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")

	yoloFlag := cmd.Flags().Lookup("yolo")
	require.NotNil(t, yoloFlag, "expected --yolo flag")

	workerNameFlag := cmd.Flags().Lookup("worker-name")
	require.NotNil(t, workerNameFlag, "expected --worker-name flag")

	previewFlag := cmd.Flags().Lookup("preview")
	require.NotNil(t, previewFlag, "expected --preview flag")
}

func TestMintDeleteGCP_RequiresProject(t *testing.T) {
	err := runMintDeleteGCP(context.Background(), "", "us-central1", false, false, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintDeleteGCP_InvalidProject(t *testing.T) {
	err := runMintDeleteGCP(context.Background(), "INVALID", "us-central1", false, false, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestMintDeleteGCP_DryRun(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			Name:  "projects/test-proj/locations/us-central1/functions/fullsend-mint",
			State: "ACTIVE",
			URI:   "https://fullsend-mint-abc123.a.run.app",
			EnvVars: map[string]string{
				"ROLE_APP_IDS":  `{"coder":"123","triage":"456"}`,
				"ALLOWED_ORGS":  "acme",
				"ALLOWED_ROLES": "coder,triage",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS":  `{"coder":"123","triage":"456"}`,
			"ALLOWED_ORGS":  "acme",
			"ALLOWED_ROLES": "coder,triage",
		}),
	)

	withMintGCFClient(t, client)

	err := runMintDeleteGCP(context.Background(), "test-project1", "us-central1", true, false, os.Stdin)
	require.NoError(t, err)

	// Dry run should NOT delete any secrets.
	assert.Empty(t, gcf.DeletedSecretIDs(client), "dry run should not delete any secrets")
}

func TestMintDeleteGCP_FullTeardown(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			Name:  "projects/test-proj/locations/us-central1/functions/fullsend-mint",
			State: "ACTIVE",
			URI:   "https://fullsend-mint-abc123.a.run.app",
			EnvVars: map[string]string{
				"ROLE_APP_IDS":  `{"coder":"123","triage":"456"}`,
				"ALLOWED_ORGS":  "acme",
				"ALLOWED_ROLES": "coder,triage",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS":  `{"coder":"123","triage":"456"}`,
			"ALLOWED_ORGS":  "acme",
			"ALLOWED_ROLES": "coder,triage",
		}),
	)

	withMintGCFClient(t, client)

	err := runMintDeleteGCP(context.Background(), "test-project1", "us-central1", false, true, os.Stdin)
	require.NoError(t, err)

	// Verify PEM secrets were deleted.
	deletedSecrets := gcf.DeletedSecretIDs(client)
	assert.NotEmpty(t, deletedSecrets, "expected PEM secrets to be deleted")

	// Verify delete operations were called.
	calls := gcf.RecordedCalls(client)
	assert.Contains(t, calls, "DeleteFunction", "expected DeleteFunction to be called")
	assert.Contains(t, calls, "DeleteServiceAccount", "expected DeleteServiceAccount to be called")
	assert.Contains(t, calls, "DeleteWIFPool", "expected DeleteWIFPool to be called")
}

func TestMintDeleteGCP_MintNotFound(t *testing.T) {
	client := gcf.NewFakeGCFClient()
	// Default fake: no functionInfo → DiscoverMint finds no function.

	withMintGCFClient(t, client)

	err := runMintDeleteGCP(context.Background(), "test-project1", "us-central1", false, true, os.Stdin)
	// Should succeed gracefully — nothing to delete.
	require.NoError(t, err)
}

func TestMintDeleteCloudflare_DryRunDurable(t *testing.T) {
	// Dry run should not call any wrangler methods.
	origFactory := mintCFWranglerFactory
	fakeCF := &fakeCFWranglerRunner{}
	mintCFWranglerFactory = func(string) cf.WranglerRunner { return fakeCF }
	defer func() { mintCFWranglerFactory = origFactory }()

	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	err := runMintDeleteCloudflare(context.Background(), "test-mint", "", "", true, false, os.Stdin)
	require.NoError(t, err)
	assert.Empty(t, fakeCF.deployCalls, "dry run should not deploy")
}

func TestMintDeleteCloudflare_DurableTeardown(t *testing.T) {
	origFactory := mintCFWranglerFactory
	fakeCF := &fakeCFWranglerRunner{}
	mintCFWranglerFactory = func(string) cf.WranglerRunner { return fakeCF }
	defer func() { mintCFWranglerFactory = origFactory }()

	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	err := runMintDeleteCloudflare(context.Background(), "test-mint", "", "", false, true, os.Stdin)
	require.NoError(t, err)
	assert.Empty(t, fakeCF.deployCalls, "durable delete should not deploy")
	assert.Len(t, fakeCF.deleteCalls, 1, "expected exactly one Delete call")
	assert.Equal(t, "test-mint", fakeCF.deleteCalls[0], "expected Delete called with worker name")
}

func TestMintDeleteCloudflare_PreviewTeardown(t *testing.T) {
	origFactory := mintCFWranglerFactory
	fakeCF := &fakeCFWranglerRunner{}
	mintCFWranglerFactory = func(string) cf.WranglerRunner { return fakeCF }
	defer func() { mintCFWranglerFactory = origFactory }()

	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	err := runMintDeleteCloudflare(context.Background(), "test-mint", "bt-run-42", "", false, true, os.Stdin)
	require.NoError(t, err)
	assert.Empty(t, fakeCF.deployCalls, "preview teardown should not deploy")
}

func TestMintDeleteCloudflare_DurableWithCustomDomainSummary(t *testing.T) {
	// Delete with --custom-domain in dry-run should mention the domain
	// in the output, verifying CLI argument wiring.
	// Non-dry-run custom domain teardown is tested at the provisioner
	// layer (TestProvisioner_Teardown_DurableWithCustomDomain).
	origFactory := mintCFWranglerFactory
	fakeCF := &fakeCFWranglerRunner{}
	mintCFWranglerFactory = func(string) cf.WranglerRunner { return fakeCF }
	defer func() { mintCFWranglerFactory = origFactory }()

	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	// Dry run verifies the flag is wired through.
	err := runMintDeleteCloudflare(context.Background(), "test-mint", "", "mint.fullsend.sh", true, false, os.Stdin)
	require.NoError(t, err)
	assert.Empty(t, fakeCF.deleteCalls, "dry run should not delete")
}

func TestMintDeployCmd_CloudflareCustomDomainResolvesZone(t *testing.T) {
	// Deploy with --custom-domain should call ResolveZoneIDForDomainFn
	// to resolve the zone ID before constructing the provisioner config.
	// The provisioner-layer test (TestProvisioner_Provision_DurableWithCustomDomain)
	// covers the full attach flow.
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	// Stub zone ID resolution and track calls.
	var resolvedDomain string
	origResolve := cf.ResolveZoneIDForDomainFn
	cf.ResolveZoneIDForDomainFn = func(_ context.Context, domain string) (string, error) {
		resolvedDomain = domain
		return "zone-abc123", nil
	}
	t.Cleanup(func() { cf.ResolveZoneIDForDomainFn = origResolve })

	// The deploy will succeed through zone resolution but fail at
	// AttachCustomDomain (no fake CF API client at CLI layer). We
	// verify zone resolution was called with the right domain.
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--custom-domain=mint.fullsend.sh",
	})
	_ = cmd.Execute() // may fail at API call; we check the zone resolution happened
	assert.Equal(t, "mint.fullsend.sh", resolvedDomain, "expected ResolveZoneIDForDomainFn to be called with the custom domain")
}

func TestMintDeployCmd_CloudflareCustomDomainDryRun(t *testing.T) {
	// Dry run with --custom-domain should show the custom domain info.
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	withMintCFWrangler(t, &fakeCFWranglerRunner{})

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--custom-domain=mint.fullsend.sh",
		"--dry-run",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintDeployCmd_CloudflareCustomDomainWithPreviewRejected(t *testing.T) {
	// --custom-domain + --preview should fail at CLI level before reaching
	// the provisioner's validate(). This ensures dry-run and actual deploy
	// produce the same error, rather than dry-run silently discarding the
	// custom domain output.
	withCFEnvVars(t)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--custom-domain=mint.fullsend.sh",
		"--preview=bt-test-42",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported for preview deploys",
		"--custom-domain + --preview should be rejected at CLI level")
}

func TestMintDeployCmd_CloudflareCustomDomainWithPreviewRejectedNonDryRun(t *testing.T) {
	// Same rejection should happen for non-dry-run deploys.
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	withMintCFWrangler(t, &fakeCFWranglerRunner{})

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--custom-domain=mint.fullsend.sh",
		"--preview=bt-test-42",
		"--source-dir=" + sourceDir,
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported for preview deploys",
		"--custom-domain + --preview should be rejected at CLI level")
}

func TestMintDeployCmd_CustomDomainWarnsOnGCP(t *testing.T) {
	// --custom-domain should produce a warning when used with --platform=gcp,
	// matching the behavior of all other Cloudflare-only flags.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=gcp",
		"--project=my-project-id",
		"--dry-run",
		"--custom-domain=mint.fullsend.sh",
	})
	_ = cmd.Execute()

	w.Close()
	os.Stderr = oldStderr

	out, _ := io.ReadAll(r)
	stderr := string(out)
	assert.Contains(t, stderr, "--custom-domain is a Cloudflare flag",
		"--custom-domain should produce a warning on GCP")
}

func TestMintDeployCmd_CloudflareCustomDomainZoneLookupFailure(t *testing.T) {
	// Deploy with --custom-domain should fail when zone lookup fails.
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	fake := &fakeCFWranglerRunner{
		deployURL: "https://fullsend-mint.workers.dev",
	}
	withMintCFWrangler(t, fake)

	origResolve := cf.ResolveZoneIDForDomainFn
	cf.ResolveZoneIDForDomainFn = func(_ context.Context, _ string) (string, error) {
		return "", fmt.Errorf("zone not found")
	}
	t.Cleanup(func() { cf.ResolveZoneIDForDomainFn = origResolve })

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--custom-domain=mint.fullsend.sh",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving zone ID")
}

func TestMintDeleteGCP_ConfirmationRequired(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			Name:  "projects/test-proj/locations/us-central1/functions/fullsend-mint",
			State: "ACTIVE",
			URI:   "https://fullsend-mint-abc123.a.run.app",
			EnvVars: map[string]string{
				"ROLE_APP_IDS":  `{"coder":"123"}`,
				"ALLOWED_ORGS":  "acme",
				"ALLOWED_ROLES": "coder",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS":  `{"coder":"123"}`,
			"ALLOWED_ORGS":  "acme",
			"ALLOWED_ROLES": "coder",
		}),
	)

	withMintGCFClient(t, client)

	// stdin is not a terminal → should fail without --yolo.
	err := runMintDeleteGCP(context.Background(), "test-project1", "us-central1", false, false, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is not a terminal")
}

func TestMintDeleteGCP_InvalidRegion(t *testing.T) {
	err := runMintDeleteGCP(context.Background(), "test-project1", "INVALID!", false, false, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP region")
}

func TestMintDeleteGCP_DiscoveryFails(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeErrors(map[string]error{
			"GetFunction": fmt.Errorf("API unavailable"),
		}),
	)
	withMintGCFClient(t, client)

	err := runMintDeleteGCP(context.Background(), "test-project1", "us-central1", false, true, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovering mint")
}

func TestMintDeleteGCP_DeleteFunctionFails(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			Name:  "projects/test-proj/locations/us-central1/functions/fullsend-mint",
			State: "ACTIVE",
			URI:   "https://fullsend-mint-abc123.a.run.app",
			EnvVars: map[string]string{
				"ROLE_APP_IDS":  `{"coder":"123"}`,
				"ALLOWED_ORGS":  "acme",
				"ALLOWED_ROLES": "coder",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS":  `{"coder":"123"}`,
			"ALLOWED_ORGS":  "acme",
			"ALLOWED_ROLES": "coder",
		}),
		gcf.WithFakeErrors(map[string]error{
			"DeleteFunction": fmt.Errorf("permission denied"),
		}),
	)
	withMintGCFClient(t, client)

	err := runMintDeleteGCP(context.Background(), "test-project1", "us-central1", false, true, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting Cloud Function")
}

func TestMintDeleteGCP_WarningsOnSAAndWIFFailure(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			Name:  "projects/test-proj/locations/us-central1/functions/fullsend-mint",
			State: "ACTIVE",
			URI:   "https://fullsend-mint-abc123.a.run.app",
			EnvVars: map[string]string{
				"ROLE_APP_IDS":  `{"coder":"123"}`,
				"ALLOWED_ORGS":  "acme",
				"ALLOWED_ROLES": "coder",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS":  `{"coder":"123"}`,
			"ALLOWED_ORGS":  "acme",
			"ALLOWED_ROLES": "coder",
		}),
		gcf.WithFakeErrors(map[string]error{
			"DeleteServiceAccount": fmt.Errorf("SA delete failed"),
			"GetProjectNumber":     fmt.Errorf("project number lookup failed"),
		}),
	)
	withMintGCFClient(t, client)

	// Should succeed despite SA and WIF failures (they're warnings, not hard errors).
	err := runMintDeleteGCP(context.Background(), "test-project1", "us-central1", false, true, os.Stdin)
	require.NoError(t, err)

	// Verify DeleteFunction was still called.
	calls := gcf.RecordedCalls(client)
	assert.Contains(t, calls, "DeleteFunction", "expected DeleteFunction to be called")
}

func TestMintDeleteCloudflare_InvalidWorkerName(t *testing.T) {
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	err := runMintDeleteCloudflare(context.Background(), "INVALID_NAME!", "", "", false, true, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --worker-name")
}

func TestMintDeleteCloudflare_InvalidPreviewAlias(t *testing.T) {
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	err := runMintDeleteCloudflare(context.Background(), "test-mint", "INVALID!", "", false, true, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --preview alias")
}

func TestMintDeleteCloudflare_AuthFailure(t *testing.T) {
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()
	os.Unsetenv("CLOUDFLARE_ACCOUNT_ID")
	os.Unsetenv("CLOUDFLARE_API_TOKEN")

	oldWhoami := cf.WranglerWhoamiFn
	cf.WranglerWhoamiFn = func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("not logged in")
	}
	t.Cleanup(func() { cf.WranglerWhoamiFn = oldWhoami })

	err := runMintDeleteCloudflare(context.Background(), "test-mint", "", "", false, true, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cloudflare credentials")
}

func TestMintDeleteCloudflare_DryRunPreview(t *testing.T) {
	origFactory := mintCFWranglerFactory
	fakeCF := &fakeCFWranglerRunner{}
	mintCFWranglerFactory = func(string) cf.WranglerRunner { return fakeCF }
	defer func() { mintCFWranglerFactory = origFactory }()

	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	err := runMintDeleteCloudflare(context.Background(), "test-mint", "bt-run-42", "", true, false, os.Stdin)
	require.NoError(t, err)
	assert.Empty(t, fakeCF.deployCalls, "dry run should not deploy")
	assert.Empty(t, fakeCF.deleteCalls, "dry run should not delete")
}

func TestMintDeleteCloudflare_DefaultWorkerName(t *testing.T) {
	origFactory := mintCFWranglerFactory
	fakeCF := &fakeCFWranglerRunner{}
	mintCFWranglerFactory = func(string) cf.WranglerRunner { return fakeCF }
	defer func() { mintCFWranglerFactory = origFactory }()

	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	// Empty worker name should use default "fullsend-mint".
	err := runMintDeleteCloudflare(context.Background(), "", "", "", false, true, os.Stdin)
	require.NoError(t, err)
	assert.Len(t, fakeCF.deleteCalls, 1, "expected exactly one Delete call")
	assert.Equal(t, "fullsend-mint", fakeCF.deleteCalls[0], "expected Delete called with default worker name")
}

func TestMintDeleteCloudflare_ConfirmationRequired(t *testing.T) {
	origFactory := mintCFWranglerFactory
	fakeCF := &fakeCFWranglerRunner{}
	mintCFWranglerFactory = func(string) cf.WranglerRunner { return fakeCF }
	defer func() { mintCFWranglerFactory = origFactory }()

	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	// stdin is not a terminal → should fail without --yolo.
	err := runMintDeleteCloudflare(context.Background(), "test-mint", "", "", false, false, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is not a terminal")
}

func TestMintDeleteCloudflare_UnsupportedPlatform(t *testing.T) {
	cmd := newMintDeleteCmd()
	cmd.SetArgs([]string{"--platform=azure"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported platform")
}

func TestConfirmDelete(t *testing.T) {
	printer := ui.New(io.Discard)

	// Matching input.
	reader := bufio.NewReader(strings.NewReader("delete\n"))
	err := confirmDelete(printer, "mint infrastructure", reader, true)
	require.NoError(t, err)

	// Mismatched input.
	reader = bufio.NewReader(strings.NewReader("nope\n"))
	err = confirmDelete(printer, "mint infrastructure", reader, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirmation did not match")

	// Not a terminal.
	reader = bufio.NewReader(strings.NewReader("delete\n"))
	err = confirmDelete(printer, "mint infrastructure", reader, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is not a terminal")
}

// --- unenroll command tests ---

func TestMintUnenrollCmd_Flags(t *testing.T) {
	cmd := newMintUnenrollCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")

	deleteProviderFlag := cmd.Flags().Lookup("delete-provider")
	require.NotNil(t, deleteProviderFlag, "expected --delete-provider flag")
	assert.Equal(t, "false", deleteProviderFlag.DefValue)

	yoloFlag := cmd.Flags().Lookup("yolo")
	require.NotNil(t, yoloFlag, "expected --yolo flag")
}

func TestMintUnenrollCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "unenroll"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestMintUnenrollCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "unenroll", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

// --- status command tests ---

func TestMintStatusCmd_Flags(t *testing.T) {
	cmd := newMintStatusCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")
}

func TestMintStatusCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "status"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintStatusCmd_InvalidOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "status", "-org", "--project=my-project-id"})
	err := cmd.Execute()
	require.Error(t, err)
}

func TestMintStatusCmd_TooManyArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "status", "org1", "org2", "--project=my-project-id"})
	err := cmd.Execute()
	require.Error(t, err)
}

// --- role aliasing tests ---

func TestResolveRole(t *testing.T) {
	assert.Equal(t, "coder", resolveRole("code"))
	assert.Equal(t, "coder", resolveRole("fix"))
	assert.Equal(t, "coder", resolveRole("coder"))
	assert.Equal(t, "triage", resolveRole("triage"))
	assert.Equal(t, "review", resolveRole("review"))
}

func TestDefaultMintRoles(t *testing.T) {
	roles := defaultMintRoles()
	assert.Equal(t, config.DefaultAgentRoles(), roles)
}

func TestRolesFromAppIDs_RoleOnly(t *testing.T) {
	roles := rolesFromAppIDs(map[string]string{
		"coder":         "100",
		"triage":        "200",
		"acme/coder":    "999",
		"widget/triage": "888",
	})
	assert.Equal(t, []string{"coder", "triage"}, roles)
}

func TestParseAllowedOrgs_SkipsPlaceholder(t *testing.T) {
	orgs := parseAllowedOrgs("widget, " + gcf.PlaceholderOrg + ", acme")
	assert.Equal(t, []string{"acme", "widget"}, orgs)
}

func TestIsPublicMintRepos(t *testing.T) {
	assert.True(t, isPublicMintRepos("*"))
	assert.True(t, isPublicMintRepos("org/repo,*"))
	assert.False(t, isPublicMintRepos("acme/repo,widget/repo"))
	assert.False(t, isPublicMintRepos(""))
}

func TestMintValidationMessage(t *testing.T) {
	assert.Equal(t, "Mint validated (public mode — org registration not required)",
		mintValidationMessage(map[string]string{"PER_REPO_WIF_REPOS": "*"}, nil))
	assert.Equal(t, "Mint validated and org registered",
		mintValidationMessage(map[string]string{"ALLOWED_ORGS": "acme"}, nil))
	assert.Equal(t, "Mint validated and org registered",
		mintValidationMessage(nil, fmt.Errorf("unavailable")))
}

func TestPemSecretRoles_DeduplicatesAliases(t *testing.T) {
	roles := pemSecretRoles([]string{"fix", "coder", "triage", "fix"})
	assert.Equal(t, []string{"coder", "triage"}, roles)
}

type fakeEnrollmentVerifier struct {
	revInfo *gcf.ServiceRevisionInfo
	revErr  error
	envVars map[string]string
	envErr  error
}

func (f *fakeEnrollmentVerifier) GetServiceRevisionInfo(context.Context) (*gcf.ServiceRevisionInfo, error) {
	return f.revInfo, f.revErr
}

func (f *fakeEnrollmentVerifier) GetServiceTrafficEnvVars(context.Context) (map[string]string, error) {
	return f.envVars, f.envErr
}

func TestVerifyEnrollment_OrgPresent(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	verifyEnrollment(context.Background(), printer, &fakeEnrollmentVerifier{
		revInfo: &gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TrafficPercent:         100,
			TemplateMatchesTraffic: true,
			TrafficEnvVars: map[string]string{
				"ALLOWED_ORGS": "acme,widget",
			},
		},
	}, "widget", "my-project")
}

func TestVerifyEnrollment_OrgMissing(t *testing.T) {
	out := &strings.Builder{}
	printer := ui.New(out)
	verifyEnrollment(context.Background(), printer, &fakeEnrollmentVerifier{
		envVars: map[string]string{
			"ALLOWED_ORGS": "acme",
		},
	}, "widget", "my-project")
	assert.Contains(t, out.String(), "FAILED")
}

func TestVerifyEnrollment_PublicMode(t *testing.T) {
	out := &strings.Builder{}
	printer := ui.New(out)
	verifyEnrollment(context.Background(), printer, &fakeEnrollmentVerifier{
		envVars: map[string]string{
			"ALLOWED_ORGS":       "*",
			"PER_REPO_WIF_REPOS": "*",
		},
	}, "any-org", "my-project")
	assert.Contains(t, out.String(), "Public mint mode")
	assert.NotContains(t, out.String(), "FAILED")
}

func TestVerifyEnrollment_FallsBackToTrafficEnvVars(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	verifyEnrollment(context.Background(), printer, &fakeEnrollmentVerifier{
		revErr: fmt.Errorf("revision unavailable"),
		envVars: map[string]string{
			"ALLOWED_ORGS": "acme",
		},
	}, "acme", "my-project")
}

func withMintGCFClient(t *testing.T, client gcf.GCFClient) {
	t.Helper()
	old := mintGCFClientFactory
	mintGCFClientFactory = func(string) gcf.GCFClient { return client }
	t.Cleanup(func() { mintGCFClientFactory = old })
}

func mintDiscoveryClient() gcf.GCFClient {
	return gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.com",
			EnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
				"ALLOWED_ORGS": "existing-org",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
			"ALLOWED_ORGS": "existing-org",
		}),
		gcf.WithFakeRevisionInfo(&gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TrafficPercent:         100,
			TemplateMatchesTraffic: true,
			TrafficEnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
				"ALLOWED_ORGS": "existing-org,acme",
			},
			RecentRevisions: []gcf.RevisionSummary{{
				Name:       "fullsend-mint-00001",
				CreateTime: "2026-06-16T12:00:00Z",
				Active:     true,
			}},
		}),
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner in ['existing-org']",
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-coder-app-pem":  true,
			"fullsend-triage-app-pem": true,
		}),
	)
}

func publicMintDiscoveryClient() gcf.GCFClient {
	return gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.com",
			EnvVars: map[string]string{
				"ROLE_APP_IDS":       `{"coder":"100","triage":"200"}`,
				"PER_REPO_WIF_REPOS": "*",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS":       `{"coder":"100","triage":"200"}`,
			"PER_REPO_WIF_REPOS": "*",
		}),
		gcf.WithFakeRevisionInfo(&gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TrafficPercent:         100,
			TemplateMatchesTraffic: true,
			TrafficEnvVars: map[string]string{
				"ROLE_APP_IDS":       `{"coder":"100","triage":"200"}`,
				"PER_REPO_WIF_REPOS": "*",
			},
		}),
	)
}

func TestRunMintEnrollOrg_DryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", true)
	require.NoError(t, err)
}

func TestRunMintEnrollOrg_DryRunPreservesCaseInPreview(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintEnrollOrg(context.Background(), printer, "AcmeCorp", "my-project", "us-central1", true)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Would add AcmeCorp to WIF provider condition")
}

func TestRunMintEnrollOrg_NoRoleAppIDs(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"acme/coder":"100"}`},
		}),
	))
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no role app IDs")
}

func TestRunMintEnrollOrg_PlaceholderOrgRejected(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollOrg(context.Background(), printer, gcf.PlaceholderOrg, "my-project", "us-central1", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "placeholder")
}

func TestRunMintEnrollOrg_Success(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", false)
	require.NoError(t, err)
}

func TestRunMintEnrollOrg_PreservesCaseInWIFCondition(t *testing.T) {
	client := mintDiscoveryClient()
	withMintGCFClient(t, client)
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollOrg(context.Background(), printer, "AcmeCorp", "my-project", "us-central1", false)
	require.NoError(t, err)

	condition := gcf.LastWIFProviderCondition(client)
	assert.Contains(t, condition, "AcmeCorp")
	assert.NotContains(t, condition, "acmecorp")
}

func TestRunMintEnrollOrg_PublicMode(t *testing.T) {
	withMintGCFClient(t, publicMintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintEnrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", false)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "public mode")
	assert.Contains(t, out.String(), "not required")
}

func TestRunMintEnrollRepo_DryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintEnrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", true)
	require.NoError(t, err)
	// Per-repo enrollment should not mention ALLOWED_ORGS.
	assert.NotContains(t, out.String(), "ALLOWED_ORGS")
	assert.Contains(t, out.String(), "PER_REPO_WIF_REPOS")
}

func TestRunMintEnrollRepo_InvalidFormat(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollRepo(context.Background(), printer, "not-a-repo", "my-project", "us-central1", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

func TestQueryMintHealth_WithVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/health", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok","version":"0.27.0","commit":"abc123"}`)
	}))
	defer srv.Close()

	ver, commit, err := queryMintHealth(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "0.27.0", ver)
	assert.Equal(t, "abc123", commit)
}

func TestQueryMintHealth_NoVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok"}`)
	}))
	defer srv.Close()

	ver, commit, err := queryMintHealth(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Empty(t, ver)
	assert.Empty(t, commit)
}

func TestQueryMintHealth_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, _, err := queryMintHealth(context.Background(), srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestQueryMintHealth_ServiceUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, `{"status":"unhealthy","version":"0.28.0","commit":"def456"}`)
	}))
	defer srv.Close()

	ver, commit, err := queryMintHealth(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "0.28.0", ver)
	assert.Equal(t, "def456", commit)
}

func TestRunMintStatus_Healthy(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "acme")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "coder = 100")
	assert.Contains(t, out.String(), "existing-org")
}

func TestRunMintStatus_WithHealthVersion(t *testing.T) {
	// Spin up a health server that returns version metadata.
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"status":"ok","version":"1.0.0","commit":"abc123"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer healthSrv.Close()

	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: healthSrv.URL,
			EnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100"}`,
				"ALLOWED_ORGS": "test-org",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
			"ALLOWED_ORGS": "test-org",
		}),
		gcf.WithFakeRevisionInfo(&gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TrafficPercent:         100,
			TemplateMatchesTraffic: true,
			TrafficEnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100"}`,
				"ALLOWED_ORGS": "test-org",
			},
			RecentRevisions: []gcf.RevisionSummary{{
				Name:       "fullsend-mint-00001",
				CreateTime: "2026-06-16T12:00:00Z",
				Active:     true,
			}},
		}),
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner in ['test-org']",
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-coder-pem": true,
		}),
	)
	withMintGCFClient(t, client)
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "test-org")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Version")
	assert.Contains(t, out.String(), "1.0.0")
	assert.Contains(t, out.String(), "Commit")
	assert.Contains(t, out.String(), "abc123")
}

func TestQueryMintHealth_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `not-valid-json`)
	}))
	defer srv.Close()

	_, _, err := queryMintHealth(context.Background(), srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding health response")
}

func TestQueryMintHealth_ConnectionRefused(t *testing.T) {
	// Use a URL pointing to a port that nothing is listening on.
	_, _, err := queryMintHealth(context.Background(), "http://127.0.0.1:1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "querying health")
}

func TestQueryMintHealth_BadURL(t *testing.T) {
	// A URL with an invalid scheme triggers the request-creation error path.
	_, _, err := queryMintHealth(context.Background(), "://bad-url")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating health request")
}

func TestRunMintStatus_HealthError(t *testing.T) {
	// When the health endpoint is unreachable, runMintStatus should still
	// succeed and print a warning instead of failing.
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "http://127.0.0.1:1", // unreachable
			EnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100"}`,
				"ALLOWED_ORGS": "test-org",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
			"ALLOWED_ORGS": "test-org",
		}),
		gcf.WithFakeRevisionInfo(&gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TrafficPercent:         100,
			TemplateMatchesTraffic: true,
			TrafficEnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100"}`,
				"ALLOWED_ORGS": "test-org",
			},
			RecentRevisions: []gcf.RevisionSummary{{
				Name:       "fullsend-mint-00001",
				CreateTime: "2026-06-16T12:00:00Z",
				Active:     true,
			}},
		}),
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner in ['test-org']",
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-coder-pem": true,
		}),
	)
	withMintGCFClient(t, client)
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "test-org")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Could not query mint version")
}

func TestRunMintStatus_NotInstalled(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "not-installed")
}

func TestRunMintStatus_OrgNotEnrolled(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "missing-org")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "not in ALLOWED_ORGS")
}

func TestRunMintStatus_PublicMode(t *testing.T) {
	withMintGCFClient(t, publicMintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "any-org")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Public (PER_REPO_WIF_REPOS=*)")
	assert.Contains(t, out.String(), "public mode — all orgs")
	assert.NotContains(t, out.String(), "not in ALLOWED_ORGS")
}

func TestRunMintStatus_TemplateDivergence(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.com",
			EnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100"}`,
				"ALLOWED_ORGS": "acme",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
			"ALLOWED_ORGS": "acme",
		}),
		gcf.WithFakeRevisionInfo(&gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TemplateRevision:       "projects/p/locations/r/services/s/revisions/fullsend-mint-00002",
			TemplateMatchesTraffic: false,
		}),
	)
	withMintGCFClient(t, client)
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "diverges")
}

func TestRunMintEnrollRepo_Success(t *testing.T) {
	client := mintDiscoveryClient()
	withMintGCFClient(t, client)
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", false)
	require.NoError(t, err)

	// Repo enrollment must not grant any IAM roles (issue #5913) — Vertex AI
	// access is provisioned separately via 'fullsend inference provision'.
	assert.Zero(t, gcf.ProjectIAMBindingCount(client))
}

func TestRunMintEnrollRepo_PreservesCaseInWIFCondition(t *testing.T) {
	client := mintDiscoveryClient()
	withMintGCFClient(t, client)
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollRepo(context.Background(), printer, "Acme/Widget", "my-project", "us-central1", false)
	require.NoError(t, err)

	assert.Equal(t, "assertion.repository == 'Acme/Widget'", gcf.LastWIFProviderCondition(client))
	assert.Zero(t, gcf.ProjectIAMBindingCount(client))
}

func TestRunMintEnrollRepo_PublicMode(t *testing.T) {
	withMintGCFClient(t, publicMintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintEnrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", false)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "public mode")
	assert.Contains(t, out.String(), "default WIF provider")
}

func TestRunMintUnenrollOrg_DryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	printer := ui.New(&strings.Builder{})
	err := runMintUnenrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", true, true, os.Stdin)
	require.NoError(t, err)
}

func TestRunMintUnenrollOrg_PublicMode(t *testing.T) {
	withMintGCFClient(t, publicMintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintUnenrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", false, true, os.Stdin)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "public mode")
	assert.Contains(t, out.String(), "not supported")
}

func TestRunMintUnenrollOrg_Success(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.com",
			EnvVars: map[string]string{
				"ALLOWED_ORGS": "acme,other",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ALLOWED_ORGS": "acme,other",
		}),
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner in ['acme', 'other']",
		}),
	)
	withMintGCFClient(t, client)
	printer := ui.New(&strings.Builder{})
	err := runMintUnenrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", false, true, os.Stdin)
	require.NoError(t, err)
}

func TestRunMintUnenrollRepo_DryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	printer := ui.New(&strings.Builder{})
	err := runMintUnenrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", false, true, true, os.Stdin)
	require.NoError(t, err)
}

func TestRunMintUnenrollRepo_PublicMode(t *testing.T) {
	withMintGCFClient(t, publicMintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintUnenrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", false, true, true, os.Stdin)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "public mode")
	assert.Contains(t, out.String(), "per-repo unenroll is not supported")
}

func TestRunMintUnenrollRepo_Success(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{URI: "https://mint.example.com"}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"PER_REPO_WIF_REPOS": "acme/widget,acme/other",
		}),
	))
	printer := ui.New(&strings.Builder{})
	err := runMintUnenrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", false, true, true, os.Stdin)
	require.NoError(t, err)
}

func TestRunMintUnenrollRepo_DeleteProvider(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{URI: "https://mint.example.com"}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"PER_REPO_WIF_REPOS": "acme/widget",
		}),
	)
	withMintGCFClient(t, client)
	printer := ui.New(&strings.Builder{})
	err := runMintUnenrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", true, true, true, os.Stdin)
	require.NoError(t, err)
}

func TestMintEnrollCmd_DryRunOrg(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll", "acme", "--project=my-project-id", "--dry-run"})
	require.NoError(t, cmd.Execute())
}

func TestMintEnrollCmd_DryRunRepo(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll", "acme/widget", "--project=my-project-id", "--dry-run"})
	require.NoError(t, cmd.Execute())
}

func TestMintUnenrollCmd_DryRunOrg(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "unenroll", "acme", "--project=my-project-id", "--dry-run"})
	require.NoError(t, cmd.Execute())
}

func TestVerifyEnrollment_TrafficRevisionWarning(t *testing.T) {
	out := &strings.Builder{}
	printer := ui.New(out)
	verifyEnrollment(context.Background(), printer, &fakeEnrollmentVerifier{
		revInfo: &gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TemplateMatchesTraffic: false,
		},
		envVars: map[string]string{
			"ALLOWED_ORGS": "acme",
		},
	}, "acme", "my-project")
	assert.Contains(t, out.String(), "may not be serving")
}

// --- confirmUnenroll tests ---

func TestConfirmUnenroll_Match(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader("acme-org\n"))
	err := confirmUnenroll(printer, "acme-org", reader, true)
	require.NoError(t, err)
}

func TestConfirmUnenroll_Mismatch(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader("wrong-name\n"))
	err := confirmUnenroll(printer, "acme-org", reader, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirmation did not match")
}

func TestConfirmUnenroll_EOF(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader(""))
	err := confirmUnenroll(printer, "acme-org", reader, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading confirmation")
}

func TestConfirmUnenroll_NonTerminal(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader("acme-org\n"))
	err := confirmUnenroll(printer, "acme-org", reader, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is not a terminal")
}

// --- mint add-role / remove-role tests ---

func TestValidateMintSetupRole(t *testing.T) {
	t.Parallel()
	role, err := validateMintSetupRole("coder")
	require.NoError(t, err)
	assert.Equal(t, "coder", role)

	role, err = validateMintSetupRole("e2e")
	require.NoError(t, err)
	assert.Equal(t, "e2e", role)

	role, err = validateMintSetupRole("scribe")
	require.NoError(t, err)
	assert.Equal(t, "scribe", role)

	_, err = validateMintSetupRole("fix")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coder")
	assert.NotContains(t, err.Error(), "add role")

	_, err = validateMintSetupRole("unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported role")
	assert.Contains(t, err.Error(), "scribe")
}

func TestValidateAppSlug(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateAppSlug("fullsend-ai-review"))
	require.NoError(t, validateAppSlug("my-app"))
	err := validateAppSlug("Bad_Slug")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid app slug")
}

func TestParseMintAddRoleMode(t *testing.T) {
	t.Parallel()
	mode, err := parseMintAddRoleMode("my-app", "/tmp/pem", "", false)
	require.NoError(t, err)
	assert.Equal(t, addRoleModeSlugPEM, mode)

	mode, err = parseMintAddRoleMode("my-app", "", "", true)
	require.NoError(t, err)
	assert.Equal(t, addRoleModeExistingSecret, mode)

	mode, err = parseMintAddRoleMode("", "", "acme", false)
	require.NoError(t, err)
	assert.Equal(t, addRoleModeBrowser, mode)

	_, err = parseMintAddRoleMode("my-app", "/tmp/pem", "", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")

	_, err = parseMintAddRoleMode("my-app", "", "acme", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined")

	_, err = parseMintAddRoleMode("", "", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "specify one input mode")
}

func TestMintSetupAddRoleCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "add-role", "coder", "--slug=app", "--pem=/tmp/x.pem"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintSetupAddRoleCmd_PemAndUseExistingMutuallyExclusive(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "coder",
		"--project=my-project-id",
		"--slug=fullsend-ai-coder",
		"--pem=/tmp/coder.pem",
		"--use-existing-pem-secret",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestMintSetupAddRoleCmd_NoInputMode(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "add-role", "coder", "--project=my-project-id"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "specify one input mode")
}

func TestMintSetupAddRoleCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "coder",
		"--project=BAD",
		"--slug=app",
		"--pem=/tmp/x.pem",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestMintSetupAddRoleCmd_InvalidRegion(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "coder",
		"--project=my-project-id",
		"--region=invalid",
		"--slug=app",
		"--pem=/tmp/x.pem",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP region")
}

func TestMintSetupRemoveRoleCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "remove-role", "coder", "--project=BAD"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestMintSetupAddRoleCmd_ForceOverwrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-coder-app-pem": true,
		}),
	))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "coder",
		"--project=my-project-id",
		"--slug=fullsend-ai-coder",
		"--use-existing-pem-secret",
		"--force",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintSetupAddRoleCmd_ExistingSecretDryRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": true,
		}),
	))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--slug=fullsend-ai-review",
		"--use-existing-pem-secret",
		"--dry-run",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintSetupAddRoleCmd_AlreadyRegistered(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "coder",
		"--project=my-project-id",
		"--slug=fullsend-ai-coder",
		"--use-existing-pem-secret",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestMintSetupRemoveRoleCmd_DryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "remove-role", "coder",
		"--project=my-project-id",
		"--dry-run",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintSetupRemoveRoleCmd_NotRegistered(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "remove-role", "review",
		"--project=my-project-id",
		"--dry-run",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not registered")
}

func TestMintAddRoleCmd_BrowserDryRun(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
	))
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--org=acme-corp",
		"--dry-run",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintTrafficRoleAppIDs_PrefersTrafficRevision(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100","review":"200"}`,
		}),
	))
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "my-project-id", Region: "us-central1"}, mintGCFClientFactory("my-project-id"))
	discovery := &gcf.MintDiscovery{
		URL:        "https://mint.example.com",
		RoleAppIDs: map[string]string{"coder": "100"},
	}
	roles, err := mintTrafficRoleAppIDs(context.Background(), nil, provisioner, discovery)
	require.NoError(t, err)
	assert.Equal(t, "200", roles["review"])
}

func TestConfirmUnenroll_CustomAbortLabel(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader("wrong\n"))
	err := confirmUnenroll(printer, "retro", reader, true, "remove-role")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aborting remove-role")
}

func TestMintAddRoleCmd_ExistingSecretRegisters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/apps/fullsend-ai-review", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": true,
		}),
	))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--slug=fullsend-ai-review",
		"--use-existing-pem-secret",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintAddRoleCmd_SlugPEMRegisters(t *testing.T) {
	testPEM := generateTestPEM(t)
	pemPath := filepath.Join(t.TempDir(), "review.pem")
	require.NoError(t, os.WriteFile(pemPath, testPEM, 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apps/fullsend-ai-review":
			fmt.Fprintln(w, `{"id": 88888}`)
		case "/app":
			fmt.Fprintln(w, `{"id": 88888}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeErrors(map[string]error{"GetSecret": gcf.ErrSecretNotFound}),
	))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--slug=fullsend-ai-review",
		"--pem=" + pemPath,
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintRemoveRoleCmd_YoloSuccess(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "remove-role", "triage",
		"--project=my-project-id",
		"--yolo",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintTrafficRoleAppIDs_InvalidJSON(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `not-json`,
		}),
	))
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "my-project-id", Region: "us-central1"}, mintGCFClientFactory("my-project-id"))
	_, err := mintTrafficRoleAppIDs(context.Background(), nil, provisioner, &gcf.MintDiscovery{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing traffic ROLE_APP_IDS")
}

func TestMintTrafficRoleAppIDs_FallbackWhenTrafficEmpty(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeTrafficEnvVars(map[string]string{}),
	))
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "my-project-id", Region: "us-central1"}, mintGCFClientFactory("my-project-id"))
	discovery := &gcf.MintDiscovery{RoleAppIDs: map[string]string{"coder": "100"}}
	roles, err := mintTrafficRoleAppIDs(context.Background(), nil, provisioner, discovery)
	require.NoError(t, err)
	assert.Equal(t, "100", roles["coder"])
}

func TestMintAddRoleCmd_ExistingSecretMissingPEM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": false,
		}),
	))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--slug=fullsend-ai-review",
		"--use-existing-pem-secret",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestMintRemoveRoleCmd_KeepPEMDryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "remove-role", "coder",
		"--project=my-project-id",
		"--keep-pem",
		"--dry-run",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestResolveAddRoleFromSlugPEM_InvalidPEM(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	pemPath := filepath.Join(t.TempDir(), "bad.pem")
	require.NoError(t, os.WriteFile(pemPath, []byte("not-a-pem"), 0o600))
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromSlugPEM(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role:    "review",
		slug:    "fullsend-ai-review",
		pemPath: pemPath,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid PEM")
}

func TestResolveAddRoleFromBrowser_InvalidOrg(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromBrowser(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		org:  "-invalid-",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "organization name")
}

func TestResolveAddRoleFromSlugPEM_MissingFile(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromSlugPEM(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role:    "review",
		slug:    "fullsend-ai-review",
		pemPath: filepath.Join(t.TempDir(), "missing.pem"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading PEM file")
}

func TestMintTrafficRoleAppIDs_FallbackOnTrafficError(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeErrors(map[string]error{
			"GetServiceTrafficEnvVars": fmt.Errorf("unavailable"),
		}),
	))
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "my-project-id", Region: "us-central1"}, mintGCFClientFactory("my-project-id"))
	discovery := &gcf.MintDiscovery{RoleAppIDs: map[string]string{"coder": "100"}}
	out := &strings.Builder{}
	printer := ui.New(out)
	roles, err := mintTrafficRoleAppIDs(context.Background(), printer, provisioner, discovery)
	require.NoError(t, err)
	assert.Equal(t, "100", roles["coder"])
	assert.Contains(t, out.String(), "traffic-serving env vars")
}

func withMintAddRoleHooks(t *testing.T, resolveToken func() (string, error), appSetup func(context.Context, forge.Client, *ui.Printer, string, []string, string, string, bool, map[string]string, string, map[string]string) ([]layers.AgentCredentials, error)) {
	t.Helper()
	oldToken := mintAddRoleResolveToken
	oldSetup := mintAddRoleAppSetup
	if resolveToken != nil {
		mintAddRoleResolveToken = resolveToken
	}
	if appSetup != nil {
		mintAddRoleAppSetup = appSetup
	}
	t.Cleanup(func() {
		mintAddRoleResolveToken = oldToken
		mintAddRoleAppSetup = oldSetup
	})
}

func TestResolveAddRoleFromBrowser_NoToken(t *testing.T) {
	withMintAddRoleHooks(t, func() (string, error) {
		return "", fmt.Errorf("no GitHub token found")
	}, nil)
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromBrowser(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		org:  "acme-corp",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no GitHub token")
}

func TestResolveAddRoleFromBrowser_Success(t *testing.T) {
	withMintAddRoleHooks(t,
		func() (string, error) { return "test-token", nil },
		func(_ context.Context, _ forge.Client, _ *ui.Printer, org string, roles []string, _ string, _ string, _ bool, _ map[string]string, _ string, _ map[string]string) ([]layers.AgentCredentials, error) {
			assert.Equal(t, "acme-corp", org)
			assert.Equal(t, []string{"review"}, roles)
			return []layers.AgentCredentials{{Slug: "fullsend-ai-review", AppID: 424242}}, nil
		},
	)
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	appID, err := resolveAddRoleFromBrowser(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		org:  "Acme-Corp",
	})
	require.NoError(t, err)
	assert.Equal(t, 424242, appID)
}

func TestResolveAddRoleFromBrowser_AppSetupFails(t *testing.T) {
	withMintAddRoleHooks(t,
		func() (string, error) { return "test-token", nil },
		func(context.Context, forge.Client, *ui.Printer, string, []string, string, string, bool, map[string]string, string, map[string]string) ([]layers.AgentCredentials, error) {
			return nil, fmt.Errorf("manifest flow failed")
		},
	)
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromBrowser(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		org:  "acme-corp",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest flow failed")
}

func TestResolveAddRoleFromBrowser_WrongCredCount(t *testing.T) {
	withMintAddRoleHooks(t,
		func() (string, error) { return "test-token", nil },
		func(context.Context, forge.Client, *ui.Printer, string, []string, string, string, bool, map[string]string, string, map[string]string) ([]layers.AgentCredentials, error) {
			return []layers.AgentCredentials{{AppID: 1}, {AppID: 2}}, nil
		},
	)
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromBrowser(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		org:  "acme-corp",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected one app credential")
}

func TestMintAddRoleCmd_BrowserRegisters(t *testing.T) {
	withMintAddRoleHooks(t,
		func() (string, error) { return "test-token", nil },
		func(context.Context, forge.Client, *ui.Printer, string, []string, string, string, bool, map[string]string, string, map[string]string) ([]layers.AgentCredentials, error) {
			return []layers.AgentCredentials{{Slug: "fullsend-ai-review", AppID: 55555}}, nil
		},
	)
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
	))
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--org=acme-corp",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestRunMintSetupAddRole_DiscoveryFails(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient())
	printer := ui.New(&strings.Builder{})
	err := runMintSetupAddRole(context.Background(), printer, mintSetupAddRoleConfig{
		role:    "review",
		project: "my-project-id",
		region:  "us-central1",
		slug:    "fullsend-ai-review",
		pemPath: "/tmp/missing.pem",
		mode:    addRoleModeSlugPEM,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint not found")
}

func TestRunMintSetupAddRole_AddRoleFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": true,
		}),
		gcf.WithFakeErrors(map[string]error{
			"UpdateServiceEnvVars": fmt.Errorf("permission denied"),
		}),
	))

	printer := ui.New(&strings.Builder{})
	err := runMintSetupAddRole(context.Background(), printer, mintSetupAddRoleConfig{
		role:                 "review",
		project:              "my-project-id",
		region:               "us-central1",
		slug:                 "fullsend-ai-review",
		mode:                 addRoleModeExistingSecret,
		useExistingPEMSecret: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registering role on mint")
	assert.NotContains(t, err.Error(), "use-existing-pem-secret")
}

func TestRunMintSetupAddRole_AddRoleFailsAfterPEMStored(t *testing.T) {
	testPEM := generateTestPEM(t)
	pemPath := filepath.Join(t.TempDir(), "review.pem")
	require.NoError(t, os.WriteFile(pemPath, testPEM, 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apps/fullsend-ai-review":
			fmt.Fprintln(w, `{"id": 88888}`)
		case "/app":
			fmt.Fprintln(w, `{"id": 88888}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": false,
		}),
		gcf.WithFakeErrors(map[string]error{
			"UpdateServiceEnvVars": fmt.Errorf("permission denied"),
		}),
	))

	printer := ui.New(&strings.Builder{})
	err := runMintSetupAddRole(context.Background(), printer, mintSetupAddRoleConfig{
		role:    "review",
		project: "my-project-id",
		region:  "us-central1",
		slug:    "fullsend-ai-review",
		pemPath: pemPath,
		mode:    addRoleModeSlugPEM,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registering role on mint")
	assert.Contains(t, err.Error(), "use-existing-pem-secret")
	assert.Contains(t, err.Error(), "gcloud secrets delete")
}

func TestRunMintSetupRemoveRole_RemoveFails(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
		}),
		gcf.WithFakeErrors(map[string]error{
			"UpdateServiceEnvVars": fmt.Errorf("permission denied"),
		}),
	))
	printer := ui.New(&strings.Builder{})
	err := runMintSetupRemoveRole(context.Background(), printer, "triage", "my-project-id", "us-central1", false, false, true, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "removing role from mint")
}

func TestRunMintSetupRemoveRole_DeletePEMFails(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
		}),
		gcf.WithFakeErrors(map[string]error{
			"DeleteSecret": fmt.Errorf("permission denied"),
		}),
	))
	printer := ui.New(&strings.Builder{})
	err := runMintSetupRemoveRole(context.Background(), printer, "triage", "my-project-id", "us-central1", false, false, true, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting PEM secret")
	assert.Contains(t, err.Error(), "gcloud secrets delete")
}

func TestResolveAddRoleFromSlugPEM_LookupFails(t *testing.T) {
	testPEM := generateTestPEM(t)
	pemPath := filepath.Join(t.TempDir(), "review.pem")
	require.NoError(t, os.WriteFile(pemPath, testPEM, 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromSlugPEM(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role:    "review",
		slug:    "missing-app",
		pemPath: pemPath,
	})
	require.Error(t, err)
}

func TestResolveAddRoleFromSlugPEM_StoreFails(t *testing.T) {
	testPEM := generateTestPEM(t)
	pemPath := filepath.Join(t.TempDir(), "review.pem")
	require.NoError(t, os.WriteFile(pemPath, testPEM, 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apps/fullsend-ai-review":
			fmt.Fprintln(w, `{"id": 88888}`)
		case "/app":
			fmt.Fprintln(w, `{"id": 88888}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": false,
		}),
		gcf.WithFakeErrors(map[string]error{
			"CreateSecret": fmt.Errorf("permission denied"),
		}),
	))
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "my-test-proj1"}, mintGCFClientFactory("my-test-proj1"))
	_, err := resolveAddRoleFromSlugPEM(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role:    "review",
		slug:    "fullsend-ai-review",
		pemPath: pemPath,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storing PEM")
}

func TestResolveAddRoleFromExistingSecret_CheckFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeErrors(map[string]error{
			"GetSecret": fmt.Errorf("api unavailable"),
		}),
	))
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, mintGCFClientFactory("p"))
	_, err := resolveAddRoleFromExistingSecret(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		slug: "fullsend-ai-review",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking PEM secret")
}

// --- workflow-host subcommand tests ---

func TestMintCommand_HasWorkflowHostSubcommand(t *testing.T) {
	cmd := newMintCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Use] = true
	}
	assert.True(t, names["workflow-host"], "expected workflow-host subcommand")
}

func TestMintWorkflowHostCmd_HasSubcommands(t *testing.T) {
	cmd := newMintWorkflowHostCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Use] = true
	}
	assert.True(t, names["add <owner/repo>"], "expected add subcommand")
	assert.True(t, names["remove <owner/repo>"], "expected remove subcommand")
	assert.True(t, names["list"], "expected list subcommand")
}

func TestMintWorkflowHostAddCmd_Flags(t *testing.T) {
	cmd := newMintWorkflowHostAddCmd()
	assert.NotNil(t, cmd.Flags().Lookup("project"))
	assert.NotNil(t, cmd.Flags().Lookup("region"))
	assert.NotNil(t, cmd.Flags().Lookup("dry-run"))
}

func TestMintWorkflowHostAddCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "add"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestMintWorkflowHostAddCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "add", "acme/repo"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintWorkflowHostAddCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "add", "acme/repo", "--project=BAD"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestMintWorkflowHostAddCmd_InvalidRepoFormat(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "add", "just-a-name", "--project=my-test-proj1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo format")
}

func TestMintWorkflowHostAddCmd_DryRun(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "add", "acme/workflows", "--project=my-test-proj1", "--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintWorkflowHostAddCmd_Success(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "add", "acme/workflows", "--project=my-test-proj1"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintWorkflowHostAddCmd_MintNotFound(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeErrors(map[string]error{
			"GetFunction":           gcf.ErrFunctionNotFound,
			"GetCloudRunServiceURI": fmt.Errorf("not found"),
		}),
	))
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "add", "acme/workflows", "--project=my-test-proj1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint not found")
}

func TestMintWorkflowHostRemoveCmd_Flags(t *testing.T) {
	cmd := newMintWorkflowHostRemoveCmd()
	assert.NotNil(t, cmd.Flags().Lookup("project"))
	assert.NotNil(t, cmd.Flags().Lookup("region"))
	assert.NotNil(t, cmd.Flags().Lookup("dry-run"))
}

func TestMintWorkflowHostRemoveCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "remove"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestMintWorkflowHostRemoveCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "remove", "acme/repo"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintWorkflowHostRemoveCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "remove", "acme/repo", "--project=BAD"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestMintWorkflowHostRemoveCmd_InvalidRepoFormat(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "remove", "just-a-name", "--project=my-test-proj1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo format")
}

func TestMintWorkflowHostRemoveCmd_DryRun(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "remove", "acme/workflows", "--project=my-test-proj1", "--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintWorkflowHostRemoveCmd_Success(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "remove", "acme/workflows", "--project=my-test-proj1"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintWorkflowHostRemoveCmd_MintNotFound(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeErrors(map[string]error{
			"GetFunction":           gcf.ErrFunctionNotFound,
			"GetCloudRunServiceURI": fmt.Errorf("not found"),
		}),
	))
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "remove", "acme/workflows", "--project=my-test-proj1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint not found")
}

func TestMintWorkflowHostListCmd_Flags(t *testing.T) {
	cmd := newMintWorkflowHostListCmd()
	assert.NotNil(t, cmd.Flags().Lookup("project"))
	assert.NotNil(t, cmd.Flags().Lookup("region"))
}

func TestMintWorkflowHostListCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "list"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintWorkflowHostListCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "list", "--project=BAD"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestMintWorkflowHostListCmd_ShowsDefault(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "list", "--project=my-test-proj1"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintWorkflowHostListCmd_ShowsConfiguredRepos(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.com",
			EnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100"}`,
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS":        `{"coder":"100"}`,
			"WORKFLOW_HOST_REPOS": "acme/workflows,other/repo",
		}),
	)
	withMintGCFClient(t, client)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "list", "--project=my-test-proj1"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintWorkflowHostListCmd_MintNotFound(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeErrors(map[string]error{
			"GetFunction":           gcf.ErrFunctionNotFound,
			"GetCloudRunServiceURI": fmt.Errorf("not found"),
		}),
	))
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "workflow-host", "list", "--project=my-test-proj1"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint not found")
}

func TestRunMintStatus_ShowsWorkflowHostRepos(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.com",
			EnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100"}`,
				"ALLOWED_ORGS": "test-org",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS":        `{"coder":"100"}`,
			"ALLOWED_ORGS":        "test-org",
			"WORKFLOW_HOST_REPOS": "acme/custom-workflows",
		}),
		gcf.WithFakeRevisionInfo(&gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TrafficPercent:         100,
			TemplateMatchesTraffic: true,
			TrafficEnvVars: map[string]string{
				"ROLE_APP_IDS":        `{"coder":"100"}`,
				"ALLOWED_ORGS":        "test-org",
				"WORKFLOW_HOST_REPOS": "acme/custom-workflows",
			},
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-coder-app-pem": true,
		}),
	)
	withMintGCFClient(t, client)
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Workflow Host Repos")
	assert.Contains(t, out.String(), "acme/custom-workflows")
}

func TestRunMintStatus_ShowsDefaultWorkflowHostRepos(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Workflow Host Repos")
	assert.Contains(t, out.String(), "fullsend-ai/fullsend")
}

// --- CF --app-set invalid value tests ---

func TestMintDeployCmd_CloudflareInvalidAppSet(t *testing.T) {
	withCFEnvVars(t)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--app-set=INVALID_SET!",
		"--dry-run",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --app-set")
}

func TestMintDeployCmd_GCPInvalidAppSet(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=gcp",
		"--project=my-project-id",
		"--app-set=BAD!!",
		"--dry-run",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --app-set")
}

// --- Preview bootstrap dry-run messaging ---

func TestMintDeployCmd_CloudflareDryRunPreviewShowsBootstrapNote(t *testing.T) {
	withCFEnvVars(t)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-run-42",
		"--dry-run",
	})
	err := cmd.Execute()

	w.Close()
	os.Stdout = oldStdout

	out, _ := io.ReadAll(r)
	stdout := string(out)

	require.NoError(t, err)
	assert.Contains(t, stdout, "does not exist")
	assert.Contains(t, stdout, "empty durable deploy")
	assert.Contains(t, stdout, "mint config applies to the preview version only")
}

// --- Invalid platform test (azure variant) ---

func TestMintDeployCmd_InvalidPlatformAzure(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=azure",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported platform")
}

// --- CF --pem-dir dry-run validation failure ---

func TestMintDeployCmd_CloudflareDryRunPemDirMissingRoles(t *testing.T) {
	withCFEnvVars(t)

	// Create a pem dir with only one role file (missing others).
	pemDir := t.TempDir()
	testPEM := generateTestPEM(t)
	require.NoError(t, os.WriteFile(filepath.Join(pemDir, "coder.pem"), testPEM, 0o600))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--dry-run",
		"--pem-dir=" + pemDir,
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing PEM file")
}

// --- CF deploy: preview deploys with bootstrap (end-to-end CLI test) ---

func TestMintDeployCmd_CloudflarePreviewBootstrapDeploy(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	workerMissing := false
	fake := &fakeCFWranglerRunner{
		deployURL:    "https://bt-run-42-fullsend-mint.workers.dev",
		workerExists: &workerMissing,
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-run-42",
		"--source-dir=" + sourceDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)
	// Should have two deploy calls: bootstrap durable + preview.
	require.Len(t, fake.deployCalls, 2)
	assert.Empty(t, fake.deployCalls[0].previewAlias, "first call should be durable bootstrap")
	assert.Equal(t, "bt-run-42", fake.deployCalls[1].previewAlias, "second call should be preview")
}

func TestMintDeployCmd_CloudflarePreviewSkipsBootstrapWhenWorkerExists(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	workerPresent := true
	fake := &fakeCFWranglerRunner{
		deployURL:    "https://bt-run-42-fullsend-mint.workers.dev",
		workerExists: &workerPresent,
	}
	withMintCFWrangler(t, fake)

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--preview=bt-run-42",
		"--source-dir=" + sourceDir,
	})
	err := cmd.Execute()
	require.NoError(t, err)
	// Only one deploy call — no bootstrap needed.
	require.Len(t, fake.deployCalls, 1)
	assert.Equal(t, "bt-run-42", fake.deployCalls[0].previewAlias)
}

// --- CF --pem-dir loadAppSetPEMs failure path ---

func TestMintDeployCmd_CloudflarePemDirLoadFailure(t *testing.T) {
	withCFEnvVars(t)
	sourceDir := createMinimalWorkerSourceDir(t)
	withMintCFWrangler(t, &fakeCFWranglerRunner{})

	// Create a pem dir with invalid PEM data.
	pemDir := t.TempDir()
	roles := defaultMintRoles()
	for _, role := range roles {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), []byte("not-a-pem"), 0o600))
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "deploy",
		"--platform=cloudflare",
		"--source-dir=" + sourceDir,
		"--pem-dir=" + pemDir,
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading app set PEMs")
}
