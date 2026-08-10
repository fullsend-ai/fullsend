package cf

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- FakeWranglerRunner ---

type fakeWranglerRunner struct {
	deployErr    error
	deployURL    string
	deployCalls  []deployCall
	secretCalls  []secretCall
	deleteCalls  []string
	deleteErr    error
	secretPutErr error
	// captureFiles lists relative paths to read from sourceDir during
	// Deploy and store in deployCall.fileContents (for asserting
	// generated file content after the temp dir is cleaned up).
	captureFiles []string
	// workerExists controls the return value of WorkerExists.
	// Defaults to true (Worker exists).
	workerExists *bool
	// workerExistsErr, if non-nil, is returned by WorkerExists.
	workerExistsErr error
}

type deployCall struct {
	sourceDir    string
	workerName   string
	previewAlias string
	envVars      map[string]string
	secrets      map[string][]byte
	// fileContents captures file contents at deploy time. Populated
	// when captureFiles is set on the fakeWranglerRunner.
	fileContents map[string]string
}

type secretCall struct {
	workerName string
	secretName string
	value      []byte
}

func (f *fakeWranglerRunner) Deploy(_ context.Context, sourceDir, workerName string, previewAlias string, envVars map[string]string, secrets map[string][]byte) (string, error) {
	call := deployCall{
		sourceDir:    sourceDir,
		workerName:   workerName,
		previewAlias: previewAlias,
		envVars:      envVars,
		secrets:      secrets,
	}
	// Capture file contents from the source dir before it's cleaned up.
	if len(f.captureFiles) > 0 {
		call.fileContents = make(map[string]string)
		for _, rel := range f.captureFiles {
			data, err := os.ReadFile(filepath.Join(sourceDir, rel))
			if err == nil {
				call.fileContents[rel] = string(data)
			}
		}
	}
	f.deployCalls = append(f.deployCalls, call)
	if f.deployErr != nil {
		return "", f.deployErr
	}
	url := f.deployURL
	if url == "" {
		url = fmt.Sprintf("https://%s.workers.dev", workerName)
	}
	return url, nil
}

func (f *fakeWranglerRunner) PutSecret(_ context.Context, workerName, secretName string, value []byte) error {
	f.secretCalls = append(f.secretCalls, secretCall{
		workerName: workerName,
		secretName: secretName,
		value:      value,
	})
	return f.secretPutErr
}

func (f *fakeWranglerRunner) Delete(_ context.Context, workerName string) error {
	f.deleteCalls = append(f.deleteCalls, workerName)
	return f.deleteErr
}

func (f *fakeWranglerRunner) WorkerExists(_ context.Context, _ string) (bool, error) {
	if f.workerExistsErr != nil {
		return false, f.workerExistsErr
	}
	if f.workerExists != nil {
		return *f.workerExists, nil
	}
	return true, nil // default: Worker exists
}

// --- Provisioner tests ---

func TestProvisioner_Name(t *testing.T) {
	p := NewProvisioner(Config{}, &fakeWranglerRunner{})
	assert.Equal(t, "cf", p.Name())
}

func TestProvisioner_OrgVariableNames(t *testing.T) {
	p := NewProvisioner(Config{}, &fakeWranglerRunner{})
	assert.Equal(t, []string{"FULLSEND_MINT_URL"}, p.OrgVariableNames())
}

func TestProvisioner_OrgSecretNames(t *testing.T) {
	p := NewProvisioner(Config{}, &fakeWranglerRunner{})
	assert.Nil(t, p.OrgSecretNames())
}

func TestProvisioner_Provision_MissingAccountID(t *testing.T) {
	p := NewProvisioner(Config{
		WorkerName: "test-mint",
	}, &fakeWranglerRunner{})

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLOUDFLARE_ACCOUNT_ID")
}

func TestProvisioner_Provision_InvalidWorkerName(t *testing.T) {
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "INVALID_NAME",
	}, &fakeWranglerRunner{})

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid Worker name")
}

func TestProvisioner_Provision_WithSourceDir(t *testing.T) {
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		deployURL: "https://test-mint.workers.dev",
	}

	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		SourceDir:  sourceDir,
	}, fake)

	result, err := p.Provision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://test-mint.workers.dev", result["FULLSEND_MINT_URL"])
	require.Len(t, fake.deployCalls, 1)
	// Deploy receives a temp copy of sourceDir (to keep checkout clean).
	assert.NotEqual(t, sourceDir, fake.deployCalls[0].sourceDir,
		"should deploy from a temp copy, not the original source dir")
	assert.Equal(t, "test-mint", fake.deployCalls[0].workerName)
	assert.Empty(t, fake.deployCalls[0].previewAlias)
}

func TestProvisioner_Provision_Preview(t *testing.T) {
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployPreview,
		PreviewAlias: "bt-run-42",
		SourceDir:    sourceDir,
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.deployCalls, 1)
	assert.Equal(t, "bt-run-42", fake.deployCalls[0].previewAlias)
}

func TestProvisioner_Provision_EnvVars(t *testing.T) {
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{}

	envVars := map[string]string{
		"ROLE_APP_IDS": `{"coder":"12345"}`,
		"ALLOWED_ORGS": "acme",
	}

	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		SourceDir:  sourceDir,
		EnvVars:    envVars,
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.deployCalls, 1)
	assert.Equal(t, `{"coder":"12345"}`, fake.deployCalls[0].envVars["ROLE_APP_IDS"])
	assert.Equal(t, "acme", fake.deployCalls[0].envVars["ALLOWED_ORGS"])
	// OIDC_AUDIENCE should be set by default.
	assert.Equal(t, "fullsend-mint", fake.deployCalls[0].envVars["OIDC_AUDIENCE"])
}

func TestProvisioner_Provision_StampsVersion(t *testing.T) {
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		captureFiles: []string{"src/version.ts"},
	}

	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		SourceDir:  sourceDir,
		Version:    "1.2.3",
		Commit:     "deadbeef",
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.deployCalls, 1)

	// Version is stamped into src/version.ts (captured during Deploy
	// before the temp copy is cleaned up).
	versionTS := fake.deployCalls[0].fileContents["src/version.ts"]
	assert.Contains(t, versionTS, `"1.2.3"`)
	assert.Contains(t, versionTS, `"deadbeef"`)

	// Env vars should NOT contain version fields.
	_, hasVersion := fake.deployCalls[0].envVars["FULLSEND_VERSION"]
	_, hasCommit := fake.deployCalls[0].envVars["FULLSEND_COMMIT"]
	assert.False(t, hasVersion, "FULLSEND_VERSION should not be in env vars")
	assert.False(t, hasCommit, "FULLSEND_COMMIT should not be in env vars")
}

func TestProvisioner_Provision_OmitsEmptyVersion(t *testing.T) {
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		captureFiles: []string{"src/version.ts"},
	}

	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		SourceDir:  sourceDir,
		// No Version or Commit set.
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.deployCalls, 1)

	// version.ts should still be written (with empty values).
	versionTS := fake.deployCalls[0].fileContents["src/version.ts"]
	assert.Contains(t, versionTS, `""`)

	// Env vars should NOT contain version fields.
	_, hasVersion := fake.deployCalls[0].envVars["FULLSEND_VERSION"]
	_, hasCommit := fake.deployCalls[0].envVars["FULLSEND_COMMIT"]
	assert.False(t, hasVersion, "FULLSEND_VERSION should not be set when empty")
	assert.False(t, hasCommit, "FULLSEND_COMMIT should not be set when empty")
}

func TestProvisioner_Provision_DeployModePassing(t *testing.T) {
	stubWASMBuild(t)
	// Verify that Deploy is called for both durable and preview modes
	// with the correct preview alias. --keep-vars behavior differs:
	// durable uses --keep-vars (to preserve secrets from StoreAgentPEM),
	// preview does NOT (each preview version is self-contained to
	// prevent cross-preview env var inheritance).
	sourceDir := createFakeWorkerSourceDir(t)

	tests := []struct {
		name         string
		mode         DeployMode
		previewAlias string
	}{
		{"durable", DeployDurable, ""},
		{"preview", DeployPreview, "bt-test"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeWranglerRunner{}
			p := NewProvisioner(Config{
				AccountID:    "abc123",
				WorkerName:   "test-mint",
				SourceDir:    sourceDir,
				DeployMode:   tc.mode,
				PreviewAlias: tc.previewAlias,
			}, fake)

			_, err := p.Provision(context.Background())
			require.NoError(t, err)
			require.Len(t, fake.deployCalls, 1)
			assert.Equal(t, tc.previewAlias, fake.deployCalls[0].previewAlias)
		})
	}
}

func TestProvisioner_Provision_DeployError(t *testing.T) {
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		deployErr: fmt.Errorf("network error"),
	}

	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		SourceDir:  sourceDir,
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deploying worker")
}

func TestProvisioner_Provision_EmbeddedSource(t *testing.T) {
	stubWASMBuild(t)
	fake := &fakeWranglerRunner{
		deployURL: "https://test-mint.workers.dev",
	}

	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		// No SourceDir — uses embedded source.
	}, fake)

	result, err := p.Provision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://test-mint.workers.dev", result["FULLSEND_MINT_URL"])
	require.Len(t, fake.deployCalls, 1)
	// Should have extracted to a temp dir.
	assert.NotEmpty(t, fake.deployCalls[0].sourceDir)
	// Temp dir should be cleaned up.
	_, statErr := os.Stat(fake.deployCalls[0].sourceDir)
	assert.True(t, os.IsNotExist(statErr), "temp dir should be cleaned up")
}

func TestProvisioner_Provision_BadSourceDir(t *testing.T) {
	fake := &fakeWranglerRunner{}
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		SourceDir:  "/nonexistent",
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source-dir")
}

func TestProvisioner_Provision_DefaultWorkerName(t *testing.T) {
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{}

	p := NewProvisioner(Config{
		AccountID: "abc123",
		SourceDir: sourceDir,
		// No WorkerName — should default.
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.deployCalls, 1)
	assert.Equal(t, "fullsend-mint", fake.deployCalls[0].workerName)
}

// --- StoreAgentPEM tests ---

func TestProvisioner_StoreAgentPEM(t *testing.T) {
	fake := &fakeWranglerRunner{}
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
	}, fake)

	err := p.StoreAgentPEM(context.Background(), "coder", []byte("pem-data"))
	require.NoError(t, err)
	require.Len(t, fake.secretCalls, 1)
	assert.Equal(t, "test-mint", fake.secretCalls[0].workerName)
	assert.Equal(t, "CODER_APP_PEM", fake.secretCalls[0].secretName)
	assert.Equal(t, []byte("pem-data"), fake.secretCalls[0].value)
}

func TestProvisioner_StoreAgentPEM_InvalidRole(t *testing.T) {
	fake := &fakeWranglerRunner{}
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
	}, fake)

	err := p.StoreAgentPEM(context.Background(), "INVALID", []byte("pem"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role name")
}

func TestProvisioner_StoreAgentPEM_Error(t *testing.T) {
	fake := &fakeWranglerRunner{
		secretPutErr: fmt.Errorf("api error"),
	}
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
	}, fake)

	err := p.StoreAgentPEM(context.Background(), "coder", []byte("pem"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storing PEM secret")
}

func TestProvisioner_Provision_PreviewWithSecrets(t *testing.T) {
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{}
	secrets := map[string][]byte{
		"CODER_APP_PEM": []byte("pem-data"),
	}
	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployPreview,
		PreviewAlias: "bt-test-42",
		SourceDir:    sourceDir,
		Secrets:      secrets,
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.deployCalls, 1)
	assert.Equal(t, "bt-test-42", fake.deployCalls[0].previewAlias)
	require.NotNil(t, fake.deployCalls[0].secrets,
		"secrets should be passed to Deploy for preview deploys")
	assert.Equal(t, []byte("pem-data"), fake.deployCalls[0].secrets["CODER_APP_PEM"],
		"PEM secrets should be passed through Deploy for preview deploys")
	assert.Empty(t, fake.secretCalls,
		"PutSecret should not be called when secrets are in Deploy")
}

// --- Teardown tests ---

func TestProvisioner_Teardown_PreviewWithAlias(t *testing.T) {
	fake := &fakeWranglerRunner{}
	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployPreview,
		PreviewAlias: "bt-run-42",
	}, fake)

	err := p.Teardown(context.Background())
	require.NoError(t, err)
	// Preview-alias teardown should NOT delete the Worker script.
	assert.Empty(t, fake.deleteCalls, "preview-alias teardown must not call Delete")
}

func TestProvisioner_Provision_DurableWithPreviewAliasRejected(t *testing.T) {
	sourceDir := createFakeWorkerSourceDir(t)
	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployDurable,
		PreviewAlias: "bt-run-42",
		SourceDir:    sourceDir,
	}, &fakeWranglerRunner{})

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires DeployMode=DeployPreview")
}

func TestProvisioner_Provision_DurableWithSecretsRejected(t *testing.T) {
	sourceDir := createFakeWorkerSourceDir(t)
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		DeployMode: DeployDurable,
		SourceDir:  sourceDir,
		Secrets:    map[string][]byte{"CODER_APP_PEM": []byte("pem-data")},
	}, &fakeWranglerRunner{})

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Config.Secrets must be empty for durable deploys")
}

func TestProvisioner_Teardown_DurableDeletesWorker(t *testing.T) {
	fake := &fakeWranglerRunner{}
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		DeployMode: DeployDurable,
	}, fake)

	err := p.Teardown(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.deleteCalls, 1, "durable teardown must call Delete")
	assert.Equal(t, "test-mint", fake.deleteCalls[0])
}

// --- WASM auto-staging tests ---

func TestEnsureWASMArtifacts_AlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	// Pre-stage both files.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mintcore.wasm"), []byte("wasm"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wasm_exec.js"), []byte("exec"), 0o644))

	// Should be a no-op — no build functions called.
	buildCalled := false
	origBuild := BuildWASMFn
	BuildWASMFn = func(outPath string) error {
		buildCalled = true
		return nil
	}
	t.Cleanup(func() { BuildWASMFn = origBuild })

	err := ensureWASMArtifacts(dir)
	require.NoError(t, err)
	assert.False(t, buildCalled, "should not build when WASM is already present")
}

func TestEnsureWASMArtifacts_MissingBoth(t *testing.T) {
	stubWASMBuild(t)
	dir := t.TempDir()

	err := ensureWASMArtifacts(dir)
	require.NoError(t, err)

	// Both files should now exist.
	assert.True(t, fileExistsAndNonEmpty(filepath.Join(dir, "mintcore.wasm")),
		"mintcore.wasm should be created")
	assert.True(t, fileExistsAndNonEmpty(filepath.Join(dir, "wasm_exec.js")),
		"wasm_exec.js should be created")
}

func TestEnsureWASMArtifacts_MissingWASMOnly(t *testing.T) {
	stubWASMBuild(t)
	dir := t.TempDir()
	// Pre-stage wasm_exec.js but not mintcore.wasm.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wasm_exec.js"), []byte("exec"), 0o644))

	err := ensureWASMArtifacts(dir)
	require.NoError(t, err)
	assert.True(t, fileExistsAndNonEmpty(filepath.Join(dir, "mintcore.wasm")))
}

func TestEnsureWASMArtifacts_BuildError(t *testing.T) {
	origBuild := BuildWASMFn
	origCopy := CopyWASMExecFn
	BuildWASMFn = func(outPath string) error {
		return fmt.Errorf("go build failed")
	}
	CopyWASMExecFn = func(destPath string) error {
		return os.WriteFile(destPath, []byte("exec"), 0o644)
	}
	t.Cleanup(func() {
		BuildWASMFn = origBuild
		CopyWASMExecFn = origCopy
	})

	dir := t.TempDir()
	err := ensureWASMArtifacts(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auto-building mintcore.wasm")
}

func TestProvisioner_Provision_SourceDirNotModified(t *testing.T) {
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		captureFiles: []string{"mintcore.wasm"},
	}

	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		SourceDir:  sourceDir,
		Version:    "1.0.0",
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	// Original source dir should NOT contain WASM artifacts or
	// generated files — deploy operates on a temp copy.
	_, err = os.Stat(filepath.Join(sourceDir, "mintcore.wasm"))
	assert.True(t, os.IsNotExist(err), "original source dir should not have mintcore.wasm")
	_, err = os.Stat(filepath.Join(sourceDir, "src", "version.ts"))
	assert.True(t, os.IsNotExist(err), "original source dir should not have generated version.ts")

	// But the temp copy (deploy dir) should have WASM artifacts.
	require.Len(t, fake.deployCalls, 1)
	assert.NotEmpty(t, fake.deployCalls[0].fileContents["mintcore.wasm"],
		"deploy dir should have auto-staged mintcore.wasm")
}

func TestProvisioner_Provision_EmbeddedAutoStagesWASM(t *testing.T) {
	stubWASMBuild(t)
	fake := &fakeWranglerRunner{
		deployURL:    "https://test-mint.workers.dev",
		captureFiles: []string{"mintcore.wasm", "wasm_exec.js"},
	}

	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		// No SourceDir — uses embedded source with auto WASM staging.
	}, fake)

	result, err := p.Provision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://test-mint.workers.dev", result["FULLSEND_MINT_URL"])
	require.Len(t, fake.deployCalls, 1)

	// WASM artifacts should have been auto-staged (captured during Deploy
	// before the temp dir was cleaned up).
	assert.NotEmpty(t, fake.deployCalls[0].fileContents["mintcore.wasm"],
		"embedded deploy should auto-stage mintcore.wasm")
	assert.NotEmpty(t, fake.deployCalls[0].fileContents["wasm_exec.js"],
		"embedded deploy should auto-stage wasm_exec.js")
}

func TestCopyDir(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0o644)
	os.WriteFile(filepath.Join(src, "sub", "nested.txt"), []byte("nested"), 0o644)

	dst := t.TempDir()
	err := copyDir(src, dst)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dst, "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))

	data, err = os.ReadFile(filepath.Join(dst, "sub", "nested.txt"))
	require.NoError(t, err)
	assert.Equal(t, "nested", string(data))
}

// --- PEMSecretsFromRoles tests ---

func TestPEMSecretsFromRoles(t *testing.T) {
	agentPEMs := map[string][]byte{
		"coder":  []byte("coder-pem"),
		"triage": []byte("triage-pem"),
	}
	secrets := PEMSecretsFromRoles(agentPEMs)
	assert.Len(t, secrets, 2)
	assert.Equal(t, []byte("coder-pem"), secrets["CODER_APP_PEM"])
	assert.Equal(t, []byte("triage-pem"), secrets["TRIAGE_APP_PEM"])
}

func TestPEMSecretsFromRoles_Empty(t *testing.T) {
	secrets := PEMSecretsFromRoles(nil)
	assert.Empty(t, secrets)
}

// --- writeSecretsFile tests ---

func TestWriteSecretsFile(t *testing.T) {
	secrets := map[string][]byte{
		"MY_SECRET": []byte("secret-value"),
	}
	path, cleanup, err := writeSecretsFile(secrets)
	require.NoError(t, err)
	defer cleanup()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"MY_SECRET"`)
	assert.Contains(t, string(data), `"secret-value"`)

	// Verify file permissions are 0600.
	info, statErr := os.Stat(path)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"secrets file should have 0600 permissions")

	// Verify cleanup removes the file.
	cleanup()
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

// --- pemSecretName tests ---

func TestPemSecretName(t *testing.T) {
	tests := []struct {
		role   string
		expect string
	}{
		{"coder", "CODER_APP_PEM"},
		{"triage", "TRIAGE_APP_PEM"},
		{"review", "REVIEW_APP_PEM"},
	}
	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			assert.Equal(t, tc.expect, pemSecretName(tc.role))
		})
	}
}

// --- ValidateWorkerName tests ---

func TestValidateWorkerName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"fullsend-mint", true},
		{"my-worker-123", true},
		{"ab", true},
		{"a", false},                   // too short
		{"UPPER", false},               // uppercase
		{"has_underscore", false},      // underscore
		{"-starts-with-hyphen", false}, // starts with hyphen
		{"ends-with-hyphen-", false},   // ends with hyphen
		{"", false},                    // empty
		{"a-very-long-worker-name-that-exceeds-the-maximum-allowed-length-of-63-chars", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.valid, ValidateWorkerName(tc.name))
		})
	}
}

// --- ValidatePreviewAlias tests ---

func TestValidatePreviewAlias(t *testing.T) {
	tests := []struct {
		alias string
		valid bool
	}{
		{"bt-run-42", true},
		{"my-preview", true},
		{"ab", true},
		{"a", false},                   // too short
		{"UPPER", false},               // uppercase
		{"has_underscore", false},      // underscore
		{"-starts-with-hyphen", false}, // starts with hyphen
		{"", false},                    // empty
	}
	for _, tc := range tests {
		t.Run(tc.alias, func(t *testing.T) {
			assert.Equal(t, tc.valid, ValidatePreviewAlias(tc.alias))
		})
	}
}

// --- ValidateCloudflareEnv tests ---

func TestValidateCloudflareEnv_Missing(t *testing.T) {
	// Save and restore env vars.
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()

	os.Unsetenv("CLOUDFLARE_ACCOUNT_ID")
	os.Unsetenv("CLOUDFLARE_API_TOKEN")

	err := ValidateCloudflareEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLOUDFLARE_ACCOUNT_ID")
	assert.Contains(t, err.Error(), "CLOUDFLARE_API_TOKEN")
}

func TestValidateCloudflareEnv_Present(t *testing.T) {
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	defer func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	}()

	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	err := ValidateCloudflareEnv()
	require.NoError(t, err)
}

// --- ResolveCloudflareAuth tests ---

func withCFEnvCleared(t *testing.T) {
	t.Helper()
	origAccount := os.Getenv("CLOUDFLARE_ACCOUNT_ID")
	origToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	os.Unsetenv("CLOUDFLARE_ACCOUNT_ID")
	os.Unsetenv("CLOUDFLARE_API_TOKEN")
	t.Cleanup(func() {
		os.Setenv("CLOUDFLARE_ACCOUNT_ID", origAccount)
		os.Setenv("CLOUDFLARE_API_TOKEN", origToken)
	})
}

func TestResolveCloudflareAuth_TokenAndAccountID(t *testing.T) {
	withCFEnvCleared(t)
	os.Setenv("CLOUDFLARE_API_TOKEN", "my-token")
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "my-account-id")

	accountID, err := ResolveCloudflareAuth(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "my-account-id", accountID)
}

func TestResolveCloudflareAuth_TokenWithoutAccountID(t *testing.T) {
	withCFEnvCleared(t)
	os.Setenv("CLOUDFLARE_API_TOKEN", "my-token")

	_, err := ResolveCloudflareAuth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLOUDFLARE_ACCOUNT_ID is missing")
}

func TestResolveCloudflareAuth_WranglerSession_WithAccountEnv(t *testing.T) {
	withCFEnvCleared(t)
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "env-account-id")

	// Mock wrangler whoami to succeed.
	old := WranglerWhoamiFn
	WranglerWhoamiFn = func(ctx context.Context) (string, error) {
		return "ℹ️  Logged in as user@example.com\n", nil
	}
	t.Cleanup(func() { WranglerWhoamiFn = old })

	accountID, err := ResolveCloudflareAuth(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env-account-id", accountID)
}

func TestResolveCloudflareAuth_WranglerSession_DiscoverAccountID(t *testing.T) {
	withCFEnvCleared(t)

	old := WranglerWhoamiFn
	WranglerWhoamiFn = func(ctx context.Context) (string, error) {
		return "┌──────────────┬──────────────────────────────────┐\n" +
			"│ Account Name │ Account ID                       │\n" +
			"├──────────────┼──────────────────────────────────┤\n" +
			"│ My Account   │ abcdef1234567890abcdef1234567890 │\n" +
			"└──────────────┴──────────────────────────────────┘\n", nil
	}
	t.Cleanup(func() { WranglerWhoamiFn = old })

	accountID, err := ResolveCloudflareAuth(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "abcdef1234567890abcdef1234567890", accountID)
}

func TestResolveCloudflareAuth_WranglerSession_MultipleAccounts(t *testing.T) {
	withCFEnvCleared(t)

	old := WranglerWhoamiFn
	WranglerWhoamiFn = func(ctx context.Context) (string, error) {
		return "┌──────────────┬──────────────────────────────────┐\n" +
			"│ Account Name │ Account ID                       │\n" +
			"├──────────────┼──────────────────────────────────┤\n" +
			"│ Account One  │ aaaabbbbccccddddeeeeffffaaaabbbb │\n" +
			"│ Account Two  │ 11112222333344445555666677778888 │\n" +
			"└──────────────┴──────────────────────────────────┘\n", nil
	}
	t.Cleanup(func() { WranglerWhoamiFn = old })

	_, err := ResolveCloudflareAuth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not be auto-detected")
}

func TestResolveCloudflareAuth_NoCredentials(t *testing.T) {
	withCFEnvCleared(t)

	old := WranglerWhoamiFn
	WranglerWhoamiFn = func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("not logged in")
	}
	t.Cleanup(func() { WranglerWhoamiFn = old })

	_, err := ResolveCloudflareAuth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Cloudflare credentials")
	assert.Contains(t, err.Error(), "wrangler login")
}

// --- parseWranglerWhoamiAccountID tests ---

func TestParseWranglerWhoamiAccountID_SingleAccount(t *testing.T) {
	output := "┌──────────────┬──────────────────────────────────┐\n" +
		"│ Account Name │ Account ID                       │\n" +
		"├──────────────┼──────────────────────────────────┤\n" +
		"│ My Account   │ abcdef1234567890abcdef1234567890 │\n" +
		"└──────────────┴──────────────────────────────────┘\n"
	assert.Equal(t, "abcdef1234567890abcdef1234567890", parseWranglerWhoamiAccountID(output))
}

func TestParseWranglerWhoamiAccountID_NoAccount(t *testing.T) {
	output := "ℹ️  Logged in as user@example.com\n"
	assert.Equal(t, "", parseWranglerWhoamiAccountID(output))
}

func TestParseWranglerWhoamiAccountID_MultipleAccounts(t *testing.T) {
	output := "│ Account One  │ aaaabbbbccccddddeeeeffffaaaabbbb │\n" +
		"│ Account Two  │ 11112222333344445555666677778888 │\n"
	assert.Equal(t, "", parseWranglerWhoamiAccountID(output))
}

// --- Embed integrity tests ---

func TestEmbeddedWorkerSource_ContainsRequiredFiles(t *testing.T) {
	for _, path := range embeddedWorkerFiles {
		t.Run(path, func(t *testing.T) {
			data, err := embeddedWorkerSource.ReadFile(path)
			require.NoError(t, err, "embedded file %s should be readable", path)
			assert.NotEmpty(t, data, "embedded file %s should not be empty", path)
		})
	}
}

func TestExtractEmbeddedSource(t *testing.T) {
	dir := t.TempDir()
	err := extractEmbeddedSource(dir)
	require.NoError(t, err)

	// Verify key files were extracted.
	for _, name := range []string{"src/index.ts", "wrangler.toml", "package.json"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		require.NoError(t, err, "expected %s to exist", name)
		assert.True(t, info.Size() > 0, "expected %s to be non-empty", name)
	}
}

// --- validateSourceDir tests ---

func TestValidateSourceDir_Valid(t *testing.T) {
	dir := createFakeWorkerSourceDir(t)
	err := validateSourceDir(dir)
	require.NoError(t, err)
}

func TestValidateSourceDir_MissingDir(t *testing.T) {
	err := validateSourceDir("/nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source-dir")
}

func TestValidateSourceDir_MissingFile(t *testing.T) {
	dir := t.TempDir()
	// Create only some required files.
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src/index.ts"), []byte("//ts"), 0o644)
	os.WriteFile(filepath.Join(dir, "wrangler.toml"), []byte("name = \"test\""), 0o644)
	// Missing package.json.

	err := validateSourceDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "package.json")
}

// --- parseWorkerURL tests ---

func TestParseWorkerURL(t *testing.T) {
	tests := []struct {
		name   string
		output string
		expect string
	}{
		{
			"standard output",
			"Published test-mint (0.5s)\nhttps://test-mint.workers.dev",
			"https://test-mint.workers.dev",
		},
		{
			"with trailing punctuation",
			"Deployed to https://my-worker.workers.dev.",
			"https://my-worker.workers.dev",
		},
		{
			"no url in output",
			"Some other output\nwithout a URL",
			"",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parseWorkerURL(tc.output, "test-mint")
			assert.Equal(t, tc.expect, result)
		})
	}
}

// --- writeVersionTS tests ---

func TestWriteVersionTS(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)

	err := writeVersionTS(dir, "2.0.0", "abc123")
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "src", "version.ts"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `export const FULLSEND_VERSION = "2.0.0"`)
	assert.Contains(t, string(data), `export const FULLSEND_COMMIT = "abc123"`)
	assert.Contains(t, string(data), "Generated at deploy time")
}

func TestWriteVersionTS_EmptyValues(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)

	err := writeVersionTS(dir, "", "")
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "src", "version.ts"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `export const FULLSEND_VERSION = ""`)
	assert.Contains(t, string(data), `export const FULLSEND_COMMIT = ""`)
}

func TestWriteVersionTS_CreatesSrcDir(t *testing.T) {
	dir := t.TempDir()
	// Don't create src/ — writeVersionTS should create it.

	err := writeVersionTS(dir, "1.0.0", "fff")
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "src", "version.ts"))
	require.NoError(t, err)
}

// --- DefaultWorkerSourceDir tests ---

func TestDefaultWorkerSourceDir(t *testing.T) {
	dir := DefaultWorkerSourceDir()
	assert.Equal(t, filepath.Join("internal", "dispatch", "cf", "workersrc"), dir)
}

// --- EmbeddedWorkerSource tests ---

func TestEmbeddedWorkerSource_ReturnsFS(t *testing.T) {
	fsys := EmbeddedWorkerSource()
	require.NotNil(t, fsys)
	// Verify we can read a known file through the returned FS.
	data, err := fs.ReadFile(fsys, "workersrc/src/index.ts")
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

// --- NewLiveWranglerRunner tests ---

func TestNewLiveWranglerRunner(t *testing.T) {
	runner := NewLiveWranglerRunner("test-account-id")
	require.NotNil(t, runner)
	assert.Equal(t, "test-account-id", runner.AccountID)
}

// --- validateSourceDir not-a-directory ---

func TestValidateSourceDir_NotADirectory(t *testing.T) {
	// Create a file (not a directory) and pass it as source dir.
	f := filepath.Join(t.TempDir(), "notadir")
	require.NoError(t, os.WriteFile(f, []byte("content"), 0o644))

	err := validateSourceDir(f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

// --- LiveWranglerRunner error path tests ---
//
// These tests exercise the command-construction and error-handling
// code paths in the LiveWranglerRunner methods. They use an already-
// cancelled context so the exec call fails immediately without
// hitting the network.

func TestLiveWranglerRunner_Deploy_DurableCommandError(t *testing.T) {
	dir := t.TempDir()
	runner := &LiveWranglerRunner{AccountID: "test-account"}

	// Cancel context immediately so exec fails without running wrangler.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	envVars := map[string]string{"KEY": "value"}
	_, err := runner.Deploy(ctx, dir, "test-worker", "", envVars, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrangler deploy failed")
}

func TestLiveWranglerRunner_Deploy_PreviewCommandError(t *testing.T) {
	dir := t.TempDir()
	runner := &LiveWranglerRunner{AccountID: "test-account"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	envVars := map[string]string{"KEY": "value"}
	_, err := runner.Deploy(ctx, dir, "test-worker", "bt-alias", envVars, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrangler versions upload failed")
}

func TestLiveWranglerRunner_PutSecret_CommandError(t *testing.T) {
	runner := &LiveWranglerRunner{AccountID: "test-account"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runner.PutSecret(ctx, "test-worker", "MY_SECRET", []byte("secret-value"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrangler secret put failed")
}

func TestLiveWranglerRunner_Delete_CommandError(t *testing.T) {
	runner := &LiveWranglerRunner{AccountID: "test-account"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runner.Delete(ctx, "test-worker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrangler delete failed")
}

// --- isHex tests ---

func TestIsHex(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"0123456789abcdef", true},
		{"ABCDEF", true},
		{"0123456789ABCDEF", true},
		{"abcdefg", false}, // 'g' is not hex
		{"xyz", false},
		{"12 34", false}, // space
		{"a1b2c3", true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.want, isHex(tc.input))
		})
	}
}

// --- writeSecretsFile additional tests ---

func TestWriteSecretsFile_MultipleSecrets(t *testing.T) {
	secrets := map[string][]byte{
		"CODER_APP_PEM":  []byte("pem-data-1"),
		"REVIEW_APP_PEM": []byte("pem-data-2"),
	}
	path, cleanup, err := writeSecretsFile(secrets)
	require.NoError(t, err)
	defer cleanup()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"CODER_APP_PEM"`)
	assert.Contains(t, string(data), `"REVIEW_APP_PEM"`)
	assert.Contains(t, string(data), `"pem-data-1"`)
	assert.Contains(t, string(data), `"pem-data-2"`)
}

func TestWriteSecretsFile_EmptySecrets(t *testing.T) {
	secrets := map[string][]byte{}
	path, cleanup, err := writeSecretsFile(secrets)
	require.NoError(t, err)
	defer cleanup()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(data))
}

// --- resolveSourceDir additional tests ---

func TestResolveSourceDir_ExplicitMissingSrcDir(t *testing.T) {
	// An explicit source dir that is not a valid Worker source should fail
	// during Provision (at validateSourceDir).
	stubWASMBuild(t)
	dir := t.TempDir()
	// Create wrangler.toml and package.json but NOT src/index.ts.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wrangler.toml"), []byte("name = \"test\""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644))

	fake := &fakeWranglerRunner{deployURL: "https://test.workers.dev"}
	p := NewProvisioner(Config{
		AccountID: "test-account",
		SourceDir: dir,
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "src/index.ts")
}

// --- Provisioner env var passing tests ---

func TestProvisioner_Provision_EmptyEnvVarPassedToWrangler(t *testing.T) {
	// Verify that empty-string env vars are passed through to the wrangler
	// runner (enabling --var KEY: to clear bindings with --keep-vars).
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)

	fake := &fakeWranglerRunner{deployURL: "https://test.workers.dev"}
	p := NewProvisioner(Config{
		AccountID: "test-account",
		SourceDir: sourceDir,
		EnvVars: map[string]string{
			"ALLOWED_ORGS":       "acme",
			"PER_REPO_WIF_REPOS": "",
		},
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	require.Len(t, fake.deployCalls, 1)
	envVars := fake.deployCalls[0].envVars
	assert.Equal(t, "acme", envVars["ALLOWED_ORGS"])
	prwr, present := envVars["PER_REPO_WIF_REPOS"]
	assert.True(t, present, "empty env var should be present in deploy call")
	assert.Equal(t, "", prwr, "empty env var should be empty string")
}

// --- Bootstrap (auto-create durable before preview) tests ---

func boolPtr(b bool) *bool { return &b }

func TestProvisioner_Provision_PreviewBootstrap_WorkerMissing(t *testing.T) {
	// When the Worker does not exist, a preview deploy should first
	// perform a durable bootstrap deploy, then the preview deploy.
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		workerExists: boolPtr(false),
	}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployPreview,
		PreviewAlias: "bt-run-42",
		SourceDir:    sourceDir,
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "acme",
		},
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	// Expect two deploy calls: first durable bootstrap, then preview.
	require.Len(t, fake.deployCalls, 2)
	assert.Empty(t, fake.deployCalls[0].previewAlias, "first deploy should be durable (no preview alias)")
	assert.Equal(t, "bt-run-42", fake.deployCalls[1].previewAlias, "second deploy should be preview")
	assert.Equal(t, "test-mint", fake.deployCalls[0].workerName)
	assert.Equal(t, "test-mint", fake.deployCalls[1].workerName)
	// Bootstrap deploy must have empty env vars.
	assert.Empty(t, fake.deployCalls[0].envVars,
		"bootstrap deploy must not set env vars (prevents dual-enrollment via --keep-vars)")
	assert.Empty(t, fake.deployCalls[0].secrets,
		"bootstrap deploy must not include secrets")
	// Preview deploy should receive the configured env vars.
	assert.Equal(t, "acme", fake.deployCalls[1].envVars["ALLOWED_ORGS"],
		"preview deploy should receive configured env vars")
}

func TestProvisioner_Provision_PreviewBootstrap_WorkerExists(t *testing.T) {
	// When the Worker already exists, preview deploy should NOT
	// perform a bootstrap durable deploy.
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		workerExists: boolPtr(true),
	}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployPreview,
		PreviewAlias: "bt-run-42",
		SourceDir:    sourceDir,
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	// Only one deploy call — no bootstrap needed.
	require.Len(t, fake.deployCalls, 1)
	assert.Equal(t, "bt-run-42", fake.deployCalls[0].previewAlias)
}

func TestProvisioner_Provision_PreviewBootstrap_WithSecrets(t *testing.T) {
	// Bootstrap deploy should NOT include PEM secrets or env vars — it
	// creates an empty durable script shell. PEM secrets and env vars
	// land only on the preview version deployed immediately after.
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	secrets := map[string][]byte{
		"CODER_APP_PEM": []byte("pem-data"),
	}
	fake := &fakeWranglerRunner{
		workerExists: boolPtr(false),
	}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployPreview,
		PreviewAlias: "bt-run-42",
		SourceDir:    sourceDir,
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "acme",
			"ROLE_APP_IDS": `{"coder":"42"}`,
		},
		Secrets: secrets,
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.deployCalls, 2)
	// Bootstrap durable deploy must have empty env vars and no secrets.
	assert.Empty(t, fake.deployCalls[0].envVars,
		"bootstrap deploy must not set env vars (prevents dual-enrollment via --keep-vars)")
	assert.Empty(t, fake.deployCalls[0].secrets,
		"bootstrap deploy must not include secrets (PEMs land on preview only)")
	// Preview deploy should include both secrets and env vars.
	assert.Equal(t, []byte("pem-data"), fake.deployCalls[1].secrets["CODER_APP_PEM"],
		"preview deploy should include PEM secrets")
	assert.Equal(t, "acme", fake.deployCalls[1].envVars["ALLOWED_ORGS"],
		"preview deploy should include configured env vars")
}

func TestProvisioner_Provision_PreviewBootstrap_BootstrapFails(t *testing.T) {
	// When the bootstrap durable deploy fails, the preview deploy
	// should not be attempted.
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	callCount := 0
	fake := &fakeWranglerRunner{
		workerExists: boolPtr(false),
	}
	// Make deploy fail on the first call (bootstrap) only.
	origDeploy := fake.Deploy
	_ = origDeploy // unused, we override via deployErr
	fake.deployErr = fmt.Errorf("bootstrap failed")

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployPreview,
		PreviewAlias: "bt-run-42",
		SourceDir:    sourceDir,
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bootstrap durable deploy")
	_ = callCount
	// Only bootstrap deploy should be attempted (which failed).
	require.Len(t, fake.deployCalls, 1)
}

func TestProvisioner_Provision_PreviewBootstrap_CheckExistenceFails(t *testing.T) {
	// When WorkerExists fails, Provision should return the error.
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		workerExistsErr: fmt.Errorf("API timeout"),
	}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployPreview,
		PreviewAlias: "bt-run-42",
		SourceDir:    sourceDir,
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking worker existence")
	assert.Empty(t, fake.deployCalls, "no deploy should be attempted when existence check fails")
}

func TestProvisioner_Provision_DurableSkipsBootstrap(t *testing.T) {
	// Durable deploys should never trigger a bootstrap existence check.
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		// workerExists is nil (default true), but shouldn't be called at all.
	}

	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		DeployMode: DeployDurable,
		SourceDir:  sourceDir,
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	// Only one deploy call — no bootstrap check.
	require.Len(t, fake.deployCalls, 1)
	assert.Empty(t, fake.deployCalls[0].previewAlias)
}

// --- LiveWranglerRunner.WorkerExists tests ---

func TestLiveWranglerRunner_WorkerExists_CommandError(t *testing.T) {
	runner := &LiveWranglerRunner{AccountID: "test-account"}

	// Cancel context immediately so exec fails.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.WorkerExists(ctx, "test-worker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking worker existence")
}

// --- copyDir additional edge case tests ---

func TestCopyDir_SkipsSymlinks(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0o644)
	// Create a symlink — it should be skipped.
	os.Symlink(filepath.Join(src, "file.txt"), filepath.Join(src, "link.txt"))

	dst := t.TempDir()
	err := copyDir(src, dst)
	require.NoError(t, err)

	// Regular file should exist.
	data, err := os.ReadFile(filepath.Join(dst, "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))

	// Symlink should NOT exist.
	_, err = os.Lstat(filepath.Join(dst, "link.txt"))
	assert.True(t, os.IsNotExist(err), "symlink should not be copied")
}

// --- writeSecretsFile edge cases ---

func TestWriteSecretsFile_NilSecrets(t *testing.T) {
	// nil secrets should behave like empty map.
	secrets := map[string][]byte(nil)
	// writeSecretsFile doesn't special-case nil, so it should work
	// (json.Marshal of empty map produces "{}")
	path, cleanup, err := writeSecretsFile(secrets)
	require.NoError(t, err)
	defer cleanup()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(data))
}

// --- ensureWASMArtifacts edge case: CopyWASMExec error ---

func TestEnsureWASMArtifacts_CopyExecError(t *testing.T) {
	origBuild := BuildWASMFn
	origCopy := CopyWASMExecFn
	BuildWASMFn = func(outPath string) error {
		return os.WriteFile(outPath, []byte("wasm"), 0o644)
	}
	CopyWASMExecFn = func(destPath string) error {
		return fmt.Errorf("copy failed")
	}
	t.Cleanup(func() {
		BuildWASMFn = origBuild
		CopyWASMExecFn = origCopy
	})

	dir := t.TempDir()
	err := ensureWASMArtifacts(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copying wasm_exec.js")
}

// --- Provisioner.Provision resolveSourceDir error test ---

func TestProvisioner_Provision_SourceDirIsFile(t *testing.T) {
	// sourceDir that is a file (not a directory) should fail validation.
	f := filepath.Join(t.TempDir(), "notadir")
	require.NoError(t, os.WriteFile(f, []byte("content"), 0o644))

	p := NewProvisioner(Config{
		AccountID: "abc123",
		SourceDir: f,
	}, &fakeWranglerRunner{})

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

// --- Provisioner validate edge cases ---

func TestProvisioner_Provision_PreviewWithoutAlias(t *testing.T) {
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		DeployMode: DeployPreview,
		// PreviewAlias intentionally empty.
	}, &fakeWranglerRunner{})

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-empty PreviewAlias")
}

// --- Provisioner.Teardown durable is rejected ---

func TestProvisioner_Teardown_DurableDeletesWorker_Default(t *testing.T) {
	// Same as existing test but with default deploy mode.
	fake := &fakeWranglerRunner{}
	p := &Provisioner{
		cfg: Config{
			AccountID:  "abc123",
			WorkerName: "test-mint",
			// DeployMode defaults to DeployDurable (0).
		},
		wrangler: fake,
	}

	err := p.Teardown(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.deleteCalls, 1, "durable teardown must call Delete")
	assert.Equal(t, "test-mint", fake.deleteCalls[0])
}

func TestProvisioner_Teardown_ValidationFails(t *testing.T) {
	// Empty AccountID should fail validation.
	p := NewProvisioner(Config{
		WorkerName: "test-mint",
		DeployMode: DeployDurable,
	}, &fakeWranglerRunner{})

	err := p.Teardown(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLOUDFLARE_ACCOUNT_ID is required")
}

func TestProvisioner_Teardown_DeleteError(t *testing.T) {
	fake := &fakeWranglerRunner{deleteErr: fmt.Errorf("wrangler delete failed")}
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		DeployMode: DeployDurable,
	}, fake)

	err := p.Teardown(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrangler delete failed")
}

// --- fileExistsAndNonEmpty tests ---

func TestFileExistsAndNonEmpty_EmptyFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(f, []byte(""), 0o644))
	assert.False(t, fileExistsAndNonEmpty(f), "empty file should return false")
}

func TestFileExistsAndNonEmpty_NonExistent(t *testing.T) {
	assert.False(t, fileExistsAndNonEmpty("/nonexistent/path"))
}

func TestFileExistsAndNonEmpty_NonEmpty(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(f, []byte("data"), 0o644))
	assert.True(t, fileExistsAndNonEmpty(f))
}

// --- PreviewAlias validation tests ---

func TestProvisioner_Provision_PreviewBootstrap_EmptyEnvVars(t *testing.T) {
	// When bootstrap is triggered, only the preview deploy should
	// receive env vars. The bootstrap durable deploy must set NO env
	// vars to prevent dual-enrollment via --keep-vars inheritance.
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		workerExists: boolPtr(false),
	}

	envVars := map[string]string{
		"ALLOWED_ORGS": "acme",
		"ROLE_APP_IDS": `{"coder":"42"}`,
	}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployPreview,
		PreviewAlias: "bt-run-42",
		SourceDir:    sourceDir,
		EnvVars:      envVars,
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.deployCalls, 2)
	// Bootstrap durable deploy must have empty env vars.
	assert.Empty(t, fake.deployCalls[0].envVars,
		"bootstrap deploy must not set env vars")
	// Preview deploy should receive the configured env vars.
	assert.Equal(t, "acme", fake.deployCalls[1].envVars["ALLOWED_ORGS"],
		"preview deploy should receive configured env vars")
	assert.Equal(t, `{"coder":"42"}`, fake.deployCalls[1].envVars["ROLE_APP_IDS"],
		"preview deploy should receive ROLE_APP_IDS")
}

// --- helpers ---

func createFakeWorkerSourceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src/index.ts"), []byte("export default {}"), 0o644)
	os.WriteFile(filepath.Join(dir, "wrangler.toml"), []byte("name = \"test\""), 0o644)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644)
	return dir
}

// stubWASMBuild replaces BuildWASMFn and CopyWASMExecFn with fakes
// that write placeholder files. Restores the originals on cleanup.
func stubWASMBuild(t *testing.T) {
	t.Helper()
	origBuild := BuildWASMFn
	origCopy := CopyWASMExecFn
	BuildWASMFn = func(outPath string) error {
		return os.WriteFile(outPath, []byte("fake-wasm"), 0o644)
	}
	CopyWASMExecFn = func(destPath string) error {
		return os.WriteFile(destPath, []byte("fake-exec"), 0o644)
	}
	t.Cleanup(func() {
		BuildWASMFn = origBuild
		CopyWASMExecFn = origCopy
	})
}
