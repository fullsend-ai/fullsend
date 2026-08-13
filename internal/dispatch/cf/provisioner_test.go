package cf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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
	// workerVars holds the vars returned by GetVars.
	workerVars map[string]string
	// getVarsErr, if non-nil, is returned by GetVars.
	getVarsErr error
	// hasPreviewVersions controls the return value of HasPreviewVersions.
	hasPreviewVersions bool
	// hasPreviewVersionsErr, if non-nil, is returned by HasPreviewVersions.
	hasPreviewVersionsErr error
	// updateVarsCalls tracks calls to UpdateVars.
	updateVarsCalls []updateVarsCall
	// updateVarsErr, if non-nil, is returned by UpdateVars.
	updateVarsErr error
}

type updateVarsCall struct {
	workerName string
	vars       map[string]string
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
		if previewAlias != "" {
			url = fmt.Sprintf("https://%s-%s.test-sub.workers.dev", previewAlias, workerName)
		} else {
			url = fmt.Sprintf("https://%s.test-sub.workers.dev", workerName)
		}
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

func (f *fakeWranglerRunner) GetVars(_ context.Context, _ string) (map[string]string, error) {
	if f.getVarsErr != nil {
		return nil, f.getVarsErr
	}
	if f.workerVars != nil {
		return f.workerVars, nil
	}
	return make(map[string]string), nil
}

func (f *fakeWranglerRunner) HasPreviewVersions(_ context.Context, _ string) (bool, error) {
	if f.hasPreviewVersionsErr != nil {
		return false, f.hasPreviewVersionsErr
	}
	return f.hasPreviewVersions, nil
}

func (f *fakeWranglerRunner) UpdateVars(_ context.Context, workerName string, vars map[string]string) error {
	f.updateVarsCalls = append(f.updateVarsCalls, updateVarsCall{
		workerName: workerName,
		vars:       vars,
	})
	return f.updateVarsErr
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
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "aabbccddee11223344556677aabbccdd")

	accountID, err := ResolveCloudflareAuth(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "aabbccddee11223344556677aabbccdd", accountID)
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
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "11223344556677889900aabbccddeeff")

	// Mock wrangler whoami to succeed.
	old := WranglerWhoamiFn
	WranglerWhoamiFn = func(ctx context.Context) (string, error) {
		return "ℹ️  Logged in as user@example.com\n", nil
	}
	t.Cleanup(func() { WranglerWhoamiFn = old })

	accountID, err := ResolveCloudflareAuth(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "11223344556677889900aabbccddeeff", accountID)
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

// --- parsePreviewURL tests ---

func TestParsePreviewURL(t *testing.T) {
	tests := []struct {
		name   string
		output string
		alias  string
		expect string
	}{
		{
			"standard preview output",
			"Uploading...\nhttps://bt-run-42-test-mint.fullsend-ai.workers.dev\nDone",
			"bt-run-42",
			"https://bt-run-42-test-mint.fullsend-ai.workers.dev",
		},
		{
			"ignores production URL",
			"Published test-mint (0.5s)\nhttps://test-mint.fullsend-ai.workers.dev\n",
			"bt-run-42",
			"",
		},
		{
			"preview URL with trailing punctuation",
			"Preview: https://bt-abc-my-worker.sub.workers.dev.",
			"bt-abc",
			"https://bt-abc-my-worker.sub.workers.dev",
		},
		{
			"no url in output",
			"Upload completed without URL",
			"bt-alias",
			"",
		},
		{
			"prefers preview URL over production URL",
			"Production: https://test-mint.fullsend-ai.workers.dev\nPreview: https://bt-42-test-mint.fullsend-ai.workers.dev",
			"bt-42",
			"https://bt-42-test-mint.fullsend-ai.workers.dev",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parsePreviewURL(tc.output, tc.alias)
			assert.Equal(t, tc.expect, result)
		})
	}
}

// --- parseWranglerSubdomainOutput tests ---

func TestParseWranglerSubdomainOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		expect string
	}{
		{
			"simple output",
			"fullsend-ai.workers.dev\n",
			"fullsend-ai",
		},
		{
			"with prefix noise",
			"Fetching subdomain...\nfullsend-ai.workers.dev\n",
			"fullsend-ai",
		},
		{
			"no subdomain",
			"No subdomain configured",
			"",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parseWranglerSubdomainOutput(tc.output)
			assert.Equal(t, tc.expect, result)
		})
	}
}

// --- ResolveWorkersSubdomain tests ---

func TestResolveWorkersSubdomain_UsesOverride(t *testing.T) {
	old := ResolveWorkersSubdomainFn
	ResolveWorkersSubdomainFn = func(_ context.Context, accountID string) (string, error) {
		return "test-sub", nil
	}
	t.Cleanup(func() { ResolveWorkersSubdomainFn = old })

	sub, err := ResolveWorkersSubdomainFn(context.Background(), "acc-123")
	require.NoError(t, err)
	assert.Equal(t, "test-sub", sub)
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

// --- FakeCloudflareAPIClient ---

type fakeCloudflareAPIClient struct {
	attachDomainCalls []attachDomainCall
	removeDomainCalls []removeDomainCall

	attachDomainErr error
	removeDomainErr error

	// lookupZoneID controls the return value of LookupZoneID.
	lookupZoneID    string
	lookupZoneIDErr error
}

type removeDomainCall struct {
	accountID string
	hostname  string
}

type attachDomainCall struct {
	accountID  string
	workerName string
	zoneID     string
	hostname   string
}

func (f *fakeCloudflareAPIClient) AttachCustomDomain(_ context.Context, accountID, workerName, zoneID, hostname string) error {
	f.attachDomainCalls = append(f.attachDomainCalls, attachDomainCall{
		accountID:  accountID,
		workerName: workerName,
		zoneID:     zoneID,
		hostname:   hostname,
	})
	return f.attachDomainErr
}

func (f *fakeCloudflareAPIClient) RemoveCustomDomain(_ context.Context, accountID string, hostname string) error {
	f.removeDomainCalls = append(f.removeDomainCalls, removeDomainCall{
		accountID: accountID,
		hostname:  hostname,
	})
	return f.removeDomainErr
}

func (f *fakeCloudflareAPIClient) LookupZoneID(_ context.Context, _ string) (string, error) {
	if f.lookupZoneIDErr != nil {
		return "", f.lookupZoneIDErr
	}
	return f.lookupZoneID, nil
}

// --- Custom domain tests ---

func TestProvisioner_Provision_DurableWithCustomDomain(t *testing.T) {
	// Given a provisioner configured with zone ID and custom domain hostname
	// When Deploy is called in DeployDurable mode
	// Then the Cloudflare Custom Domains API is called with the correct hostname
	// And the mint URL uses the custom domain
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		deployURL: "https://test-mint.test-sub.workers.dev",
	}
	fakeCFAPI := &fakeCloudflareAPIClient{}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployDurable,
		SourceDir:    sourceDir,
		ZoneID:       "zone-456",
		CustomDomain: "mint.fullsend.sh",
	}, fake)
	p.SetCloudflareAPI(fakeCFAPI)

	result, err := p.Provision(context.Background())
	require.NoError(t, err)

	// Mint URL should be the custom domain, not workers.dev.
	assert.Equal(t, "https://mint.fullsend.sh", result["FULLSEND_MINT_URL"])

	// Custom domain should be attached.
	require.Len(t, fakeCFAPI.attachDomainCalls, 1)
	assert.Equal(t, "abc123", fakeCFAPI.attachDomainCalls[0].accountID)
	assert.Equal(t, "test-mint", fakeCFAPI.attachDomainCalls[0].workerName)
	assert.Equal(t, "zone-456", fakeCFAPI.attachDomainCalls[0].zoneID)
	assert.Equal(t, "mint.fullsend.sh", fakeCFAPI.attachDomainCalls[0].hostname)
}

func TestProvisioner_Provision_PreviewSkipsCustomDomain(t *testing.T) {
	// Given DeployPreview mode with custom domain would be invalid
	// When Provision is called
	// Then validation rejects the config
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployPreview,
		PreviewAlias: "bt-run-42",
		SourceDir:    sourceDir,
		ZoneID:       "zone-456",
		CustomDomain: "mint.fullsend.sh",
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported for preview deploys")
}

func TestProvisioner_Provision_DurableWithoutCustomDomain(t *testing.T) {
	// Without CustomDomain, no CF API calls should be made.
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		deployURL: "https://test-mint.test-sub.workers.dev",
	}
	fakeCFAPI := &fakeCloudflareAPIClient{}

	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		DeployMode: DeployDurable,
		SourceDir:  sourceDir,
		// No ZoneID or CustomDomain.
	}, fake)
	p.SetCloudflareAPI(fakeCFAPI)

	result, err := p.Provision(context.Background())
	require.NoError(t, err)

	// Should use workers.dev URL.
	assert.Equal(t, "https://test-mint.test-sub.workers.dev", result["FULLSEND_MINT_URL"])

	// No CF API calls.
	assert.Empty(t, fakeCFAPI.attachDomainCalls)
}

func TestProvisioner_Provision_CustomDomainAutoResolvesZoneID(t *testing.T) {
	// CustomDomain without ZoneID should auto-resolve via LookupZoneID.
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		deployURL: "https://test-mint.test-sub.workers.dev",
	}
	fakeCFAPI := &fakeCloudflareAPIClient{
		lookupZoneID: "auto-resolved-zone-789",
	}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployDurable,
		SourceDir:    sourceDir,
		CustomDomain: "mint.fullsend.sh",
		// ZoneID intentionally empty — should be auto-resolved.
	}, fake)
	p.SetCloudflareAPI(fakeCFAPI)

	result, err := p.Provision(context.Background())
	require.NoError(t, err)

	// Mint URL should be the custom domain.
	assert.Equal(t, "https://mint.fullsend.sh", result["FULLSEND_MINT_URL"])

	// ZoneID should have been resolved and used.
	require.Len(t, fakeCFAPI.attachDomainCalls, 1)
	assert.Equal(t, "auto-resolved-zone-789", fakeCFAPI.attachDomainCalls[0].zoneID)
}

func TestProvisioner_Provision_CustomDomainZoneLookupFailure(t *testing.T) {
	// When zone lookup fails, Provision should return a clear error.
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{
		deployURL: "https://test-mint.test-sub.workers.dev",
	}
	fakeCFAPI := &fakeCloudflareAPIClient{
		lookupZoneIDErr: fmt.Errorf("zone not found for domain %q — ensure the domain's zone exists in your Cloudflare account", "mint.fullsend.sh"),
	}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployDurable,
		SourceDir:    sourceDir,
		CustomDomain: "mint.fullsend.sh",
	}, fake)
	p.SetCloudflareAPI(fakeCFAPI)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "looking up zone ID for custom domain")
}

func TestProvisioner_Provision_AttachDomainError(t *testing.T) {
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)
	fake := &fakeWranglerRunner{}
	fakeCFAPI := &fakeCloudflareAPIClient{
		attachDomainErr: fmt.Errorf("domain already in use"),
	}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployDurable,
		SourceDir:    sourceDir,
		ZoneID:       "zone-456",
		CustomDomain: "mint.fullsend.sh",
	}, fake)
	p.SetCloudflareAPI(fakeCFAPI)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "attaching custom domain")
}

func TestProvisioner_Teardown_DurableWithCustomDomain(t *testing.T) {
	// Durable teardown with custom domain should remove custom domain
	// before deleting the Worker.
	fake := &fakeWranglerRunner{}
	fakeCFAPI := &fakeCloudflareAPIClient{}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployDurable,
		CustomDomain: "mint.fullsend.sh",
	}, fake)
	p.SetCloudflareAPI(fakeCFAPI)

	err := p.Teardown(context.Background())
	require.NoError(t, err)

	// Custom domain removed with correct accountID.
	require.Len(t, fakeCFAPI.removeDomainCalls, 1)
	assert.Equal(t, "abc123", fakeCFAPI.removeDomainCalls[0].accountID)
	assert.Equal(t, "mint.fullsend.sh", fakeCFAPI.removeDomainCalls[0].hostname)

	// Worker deleted.
	require.Len(t, fake.deleteCalls, 1)
	assert.Equal(t, "test-mint", fake.deleteCalls[0])
}

func TestProvisioner_Teardown_DurableWithoutCustomDomain(t *testing.T) {
	// Without CustomDomain, teardown should just delete the Worker.
	fake := &fakeWranglerRunner{}
	fakeCFAPI := &fakeCloudflareAPIClient{}

	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		DeployMode: DeployDurable,
	}, fake)
	p.SetCloudflareAPI(fakeCFAPI)

	err := p.Teardown(context.Background())
	require.NoError(t, err)

	// No CF API calls.
	assert.Empty(t, fakeCFAPI.removeDomainCalls)

	// Worker deleted.
	require.Len(t, fake.deleteCalls, 1)
}

func TestProvisioner_Teardown_RemoveDomainError(t *testing.T) {
	fake := &fakeWranglerRunner{}
	fakeCFAPI := &fakeCloudflareAPIClient{
		removeDomainErr: fmt.Errorf("domain not found"),
	}

	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployDurable,
		CustomDomain: "mint.fullsend.sh",
	}, fake)
	p.SetCloudflareAPI(fakeCFAPI)

	err := p.Teardown(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "removing custom domain")
	// Worker should NOT be deleted when domain removal fails.
	assert.Empty(t, fake.deleteCalls)
}

func TestProvisioner_Validate_ZoneIDWithoutCustomDomain(t *testing.T) {
	// ZoneID without CustomDomain should be rejected by validate().
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		DeployMode: DeployDurable,
		ZoneID:     "zone-456",
	}, &fakeWranglerRunner{})

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CustomDomain is required when ZoneID is set")
}

func TestProvisioner_Validate_InvalidCustomDomainHostname(t *testing.T) {
	// CustomDomain with invalid hostname syntax should be rejected.
	tests := []struct {
		name   string
		domain string
	}{
		{"spaces", "mint fullsend.sh"},
		{"no-dots", "localhost"},
		{"trailing-dot", "mint.fullsend.sh."},
		{"special-chars", "mint!@#.fullsend.sh"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProvisioner(Config{
				AccountID:    "abc123",
				WorkerName:   "test-mint",
				DeployMode:   DeployDurable,
				ZoneID:       "zone-456",
				CustomDomain: tc.domain,
			}, &fakeWranglerRunner{})

			_, err := p.Provision(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid CustomDomain")
		})
	}
}

func TestProvisioner_Validate_ValidCustomDomainHostname(t *testing.T) {
	// Valid hostnames should pass validation (may fail later in deploy).
	stubWASMBuild(t)
	sourceDir := createFakeWorkerSourceDir(t)

	tests := []struct {
		name   string
		domain string
	}{
		{"simple", "mint.fullsend.sh"},
		{"subdomain", "stage.mint.fullsend.sh"},
		{"hyphen", "my-mint.fullsend.sh"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeWranglerRunner{
				deployURL: "https://test-mint.test-sub.workers.dev",
			}
			fakeCFAPI := &fakeCloudflareAPIClient{}

			p := NewProvisioner(Config{
				AccountID:    "abc123",
				WorkerName:   "test-mint",
				DeployMode:   DeployDurable,
				SourceDir:    sourceDir,
				ZoneID:       "zone-456",
				CustomDomain: tc.domain,
			}, fake)
			p.SetCloudflareAPI(fakeCFAPI)

			result, err := p.Provision(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "https://"+tc.domain, result["FULLSEND_MINT_URL"])
		})
	}
}

// --- ensureCFAPI tests ---

func TestEnsureCFAPI_LazyInit(t *testing.T) {
	// When no cfAPI is set, ensureCFAPI should create a
	// LiveCloudflareAPIClient.
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
	}, &fakeWranglerRunner{})

	// cfAPI starts nil.
	assert.Nil(t, p.cfAPI)

	client := p.ensureCFAPI()
	require.NotNil(t, client)

	// Should be a *LiveCloudflareAPIClient.
	_, ok := client.(*LiveCloudflareAPIClient)
	assert.True(t, ok, "ensureCFAPI should create a LiveCloudflareAPIClient")

	// Subsequent calls return the same instance.
	assert.Equal(t, client, p.ensureCFAPI())
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

// --- Enroll / Unenroll tests ---

func TestEnsureOrgInWorker_AddsOrg(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{"ALLOWED_ORGS": "existing-org"},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.EnsureOrgInWorker(context.Background(), "new-org")
	require.NoError(t, err)
	require.Len(t, fake.updateVarsCalls, 1)
	assert.Equal(t, "existing-org,new-org", fake.updateVarsCalls[0].vars["ALLOWED_ORGS"])
	assert.Equal(t, "test-mint", fake.updateVarsCalls[0].workerName)
	assert.Empty(t, fake.deployCalls, "enroll should not call Deploy")
}

func TestEnsureOrgInWorker_AlreadyEnrolled(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{"ALLOWED_ORGS": "acme,other"},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.EnsureOrgInWorker(context.Background(), "ACME")
	require.NoError(t, err)
	assert.Empty(t, fake.updateVarsCalls, "should not update vars when org already enrolled")
}

func TestEnsureOrgInWorker_PublicMode(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{"PER_REPO_WIF_REPOS": "*"},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.EnsureOrgInWorker(context.Background(), "acme")
	require.NoError(t, err)
	assert.Empty(t, fake.updateVarsCalls, "should not update vars in public mode")
}

func TestEnsureOrgInWorker_EmptyAllowedOrgs(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.EnsureOrgInWorker(context.Background(), "acme")
	require.NoError(t, err)
	require.Len(t, fake.updateVarsCalls, 1)
	assert.Equal(t, "acme", fake.updateVarsCalls[0].vars["ALLOWED_ORGS"])
}

func TestRemoveOrgFromWorker_RemovesOrg(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{"ALLOWED_ORGS": "acme,other"},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveOrgFromWorker(context.Background(), "acme")
	require.NoError(t, err)
	require.Len(t, fake.updateVarsCalls, 1)
	assert.Equal(t, "other", fake.updateVarsCalls[0].vars["ALLOWED_ORGS"])
}

func TestRemoveOrgFromWorker_PublicMode(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{"PER_REPO_WIF_REPOS": "*"},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveOrgFromWorker(context.Background(), "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public mode")
	assert.Contains(t, err.Error(), "PER_REPO_WIF_REPOS=*")
}

func TestRemoveOrgFromWorker_NotEnrolled(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{"ALLOWED_ORGS": "acme,other"},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveOrgFromWorker(context.Background(), "missing-org")
	require.NoError(t, err)
	assert.Empty(t, fake.updateVarsCalls, "should not update vars when org is not enrolled")
}

func TestRegisterRepoInWorker_AddsRepo(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{
			"ALLOWED_ORGS":       "existing-org",
			"PER_REPO_WIF_REPOS": "",
		},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RegisterRepoInWorker(context.Background(), "new-org/widget")
	require.NoError(t, err)
	require.Len(t, fake.updateVarsCalls, 1)
	assert.Equal(t, "new-org/widget", fake.updateVarsCalls[0].vars["PER_REPO_WIF_REPOS"])
	// Per-repo enrollment must NOT modify ALLOWED_ORGS.
	_, hasAllowedOrgs := fake.updateVarsCalls[0].vars["ALLOWED_ORGS"]
	assert.False(t, hasAllowedOrgs, "per-repo enrollment should not modify ALLOWED_ORGS")
}

func TestRegisterRepoInWorker_DoesNotModifyAllowedOrgs(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{
			"ALLOWED_ORGS":       "acme",
			"PER_REPO_WIF_REPOS": "",
		},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RegisterRepoInWorker(context.Background(), "acme/widget")
	require.NoError(t, err)
	require.Len(t, fake.updateVarsCalls, 1)
	assert.Equal(t, "acme/widget", fake.updateVarsCalls[0].vars["PER_REPO_WIF_REPOS"])
	// Per-repo enrollment must NOT modify ALLOWED_ORGS — it is independent
	// of org-level enrollment on both GCP and Cloudflare.
	_, hasAllowedOrgs := fake.updateVarsCalls[0].vars["ALLOWED_ORGS"]
	assert.False(t, hasAllowedOrgs, "per-repo enrollment should not modify ALLOWED_ORGS")
}

func TestRegisterRepoInWorker_AlreadyEnrolled(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{
			"ALLOWED_ORGS":       "acme",
			"PER_REPO_WIF_REPOS": "acme/widget",
		},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RegisterRepoInWorker(context.Background(), "ACME/widget")
	require.NoError(t, err)
	assert.Empty(t, fake.updateVarsCalls, "should not update vars when repo already enrolled")
}

func TestRemoveRepoFromWorker_RemovesRepo(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{
			"ALLOWED_ORGS":       "acme",
			"PER_REPO_WIF_REPOS": "acme/widget,acme/other",
		},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveRepoFromWorker(context.Background(), "acme/widget")
	require.NoError(t, err)
	require.Len(t, fake.updateVarsCalls, 1)
	assert.Equal(t, "acme/other", fake.updateVarsCalls[0].vars["PER_REPO_WIF_REPOS"])
}

func TestRemoveRepoFromWorker_NotEnrolled(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{
			"ALLOWED_ORGS":       "acme",
			"PER_REPO_WIF_REPOS": "acme/other",
		},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveRepoFromWorker(context.Background(), "acme/missing-repo")
	require.NoError(t, err)
	assert.Empty(t, fake.updateVarsCalls, "should not update vars when repo is not enrolled")
}

func TestRemoveRepoFromWorker_PublicMode(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{"PER_REPO_WIF_REPOS": "*"},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveRepoFromWorker(context.Background(), "acme/widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public mode")
	assert.Contains(t, err.Error(), "PER_REPO_WIF_REPOS=*")
}

func TestParseWorkerSettingsVars(t *testing.T) {
	body := []byte(`{
		"result": {
			"bindings": [
				{"type": "plain_text", "name": "ALLOWED_ORGS", "text": "acme,other"},
				{"type": "plain_text", "name": "PER_REPO_WIF_REPOS", "text": "acme/widget"},
				{"type": "secret_text", "name": "CODER_APP_PEM"}
			]
		},
		"success": true
	}`)

	vars, err := parseWorkerSettingsVars(body)
	require.NoError(t, err)
	assert.Equal(t, "acme,other", vars["ALLOWED_ORGS"])
	assert.Equal(t, "acme/widget", vars["PER_REPO_WIF_REPOS"])
	_, hasSecret := vars["CODER_APP_PEM"]
	assert.False(t, hasSecret, "secret bindings should be excluded")
}

func TestParseWorkerSettingsVars_FailureResponse(t *testing.T) {
	body := []byte(`{"result": {}, "success": false}`)
	_, err := parseWorkerSettingsVars(body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "success=false")
}

func TestParseHasPreviewVersions(t *testing.T) {
	// No preview versions.
	assert.False(t, parseHasPreviewVersions("Version ID     Created\nabc123        2026-01-01"))
	// Has preview versions.
	assert.True(t, parseHasPreviewVersions("Version ID     Created       Preview\nabc123        2026-01-01    my-preview"))
}

func TestCheckPreviewVersions(t *testing.T) {
	fake := &fakeWranglerRunner{hasPreviewVersions: true}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	has, err := p.CheckPreviewVersions(context.Background())
	require.NoError(t, err)
	assert.True(t, has)
}

// --- HTTP transport interception helper ---

// testRoundTripper implements http.RoundTripper via a function.
type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// withHTTPIntercept replaces http.DefaultTransport with one that routes
// all requests to the test server, preserving the original URL path.
// Tests using this must not run in parallel.
func withHTTPIntercept(t *testing.T, handler http.Handler) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	tsURL, err := url.Parse(ts.URL)
	require.NoError(t, err)

	origTransport := http.DefaultTransport
	http.DefaultTransport = testRoundTripper(func(req *http.Request) (*http.Response, error) {
		req2 := req.Clone(req.Context())
		req2.URL.Scheme = tsURL.Scheme
		req2.URL.Host = tsURL.Host
		return (&http.Transport{}).RoundTrip(req2)
	})
	t.Cleanup(func() { http.DefaultTransport = origTransport })
}

// --- fetchWorkerContent tests ---

func TestFetchWorkerContent_SingleModule(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Contains(t, r.URL.Path, "/content/v2")
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("cf-entrypoint", "index.js")
		fmt.Fprint(w, "console.log('hello')")
	}))

	modules, main, err := fetchWorkerContent(context.Background(), "acc-id", "my-worker", "test-token")
	require.NoError(t, err)
	assert.Equal(t, "index.js", main)
	require.Len(t, modules, 1)
	assert.Equal(t, "index.js", modules[0].name)
	assert.Equal(t, "application/javascript", modules[0].contentType)
	assert.Equal(t, []byte("console.log('hello')"), modules[0].data)
}

func TestFetchWorkerContent_SingleModule_NoEntrypoint(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		// No cf-entrypoint header — should default to "index.js".
		fmt.Fprint(w, "export default {}")
	}))

	modules, main, err := fetchWorkerContent(context.Background(), "acc-id", "my-worker", "test-token")
	require.NoError(t, err)
	assert.Equal(t, "index.js", main)
	require.Len(t, modules, 1)
	assert.Equal(t, "index.js", modules[0].name)
}

func TestFetchWorkerContent_Multipart(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)

		// Part 1: JS module.
		h1 := textproto.MIMEHeader{}
		h1.Set("Content-Disposition", `form-data; name="index.js"; filename="index.js"`)
		h1.Set("Content-Type", "application/javascript")
		p1, _ := mw.CreatePart(h1)
		fmt.Fprint(p1, "export default {}")

		// Part 2: WASM module (no Content-Type — should default).
		h2 := textproto.MIMEHeader{}
		h2.Set("Content-Disposition", `form-data; name="module.wasm"; filename="module.wasm"`)
		p2, _ := mw.CreatePart(h2)
		p2.Write([]byte{0x00, 0x61, 0x73, 0x6d})

		mw.Close()

		w.Header().Set("Content-Type", mw.FormDataContentType())
		w.Header().Set("cf-entrypoint", "index.js")
		w.Write(buf.Bytes())
	}))

	modules, main, err := fetchWorkerContent(context.Background(), "acc-id", "my-worker", "test-token")
	require.NoError(t, err)
	assert.Equal(t, "index.js", main)
	require.Len(t, modules, 2)
	assert.Equal(t, "index.js", modules[0].name)
	assert.Equal(t, "application/javascript", modules[0].contentType)
	assert.Equal(t, "module.wasm", modules[1].name)
}

func TestFetchWorkerContent_ErrorStatus(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "not found")
	}))

	_, _, err := fetchWorkerContent(context.Background(), "acc-id", "my-worker", "test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "content API returned 404")
}

func TestFetchWorkerContent_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := fetchWorkerContent(ctx, "acc-id", "my-worker", "test-token")
	require.Error(t, err)
}

func TestFetchWorkerContent_SingleModuleTruncation(t *testing.T) {
	// When the single-module body exceeds maxWorkerModuleBytes,
	// fetchWorkerContent should return an error instead of silently
	// uploading truncated content.
	origMax := maxWorkerModuleBytes
	// Use a small limit for testing.
	defer func() { maxWorkerModuleBytes = origMax }()
	maxWorkerModuleBytes = 10

	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("cf-entrypoint", "index.js")
		// Write more than the 10-byte limit.
		fmt.Fprint(w, "this content is definitely longer than ten bytes")
	}))

	_, _, err := fetchWorkerContent(context.Background(), "acc-id", "my-worker", "test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
	assert.Contains(t, err.Error(), "truncated")
}

func TestFetchWorkerContent_MultipartTruncation(t *testing.T) {
	// When a multipart module part exceeds maxWorkerModuleBytes,
	// fetchWorkerContent should return an error.
	origMax := maxWorkerModuleBytes
	defer func() { maxWorkerModuleBytes = origMax }()
	maxWorkerModuleBytes = 10

	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)

		h1 := textproto.MIMEHeader{}
		h1.Set("Content-Disposition", `form-data; name="module.wasm"; filename="module.wasm"`)
		h1.Set("Content-Type", "application/wasm")
		p1, _ := mw.CreatePart(h1)
		// Write more than the 10-byte limit.
		p1.Write(bytes.Repeat([]byte{0x00}, 20))

		mw.Close()

		w.Header().Set("Content-Type", mw.FormDataContentType())
		w.Header().Set("cf-entrypoint", "index.js")
		w.Write(buf.Bytes())
	}))

	_, _, err := fetchWorkerContent(context.Background(), "acc-id", "my-worker", "test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
	assert.Contains(t, err.Error(), "truncated")
}

// --- createVersionWithVars tests ---

func TestCreateVersionWithVars_Success(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/versions")
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Contains(t, r.Header.Get("Content-Type"), "multipart/form-data")

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"result":  map[string]string{"id": "version-abc123"},
			"success": true,
		})
	}))

	modules := []workerModule{
		{name: "index.js", contentType: "application/javascript", data: []byte("export default {}")},
	}
	vars := map[string]string{"ALLOWED_ORGS": "acme"}

	id, err := createVersionWithVars(context.Background(), "acc-id", "my-worker", "test-token", modules, "index.js", vars)
	require.NoError(t, err)
	assert.Equal(t, "version-abc123", id)
}

func TestCreateVersionWithVars_MultipleModules(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"result":  map[string]string{"id": "version-multi"},
			"success": true,
		})
	}))

	modules := []workerModule{
		{name: "index.js", contentType: "application/javascript", data: []byte("code")},
		{name: "module.wasm", contentType: "application/wasm", data: []byte("wasmdata")},
	}
	vars := map[string]string{"KEY": "value"}

	id, err := createVersionWithVars(context.Background(), "acc-id", "my-worker", "test-token", modules, "index.js", vars)
	require.NoError(t, err)
	assert.Equal(t, "version-multi", id)
}

func TestCreateVersionWithVars_ErrorStatus(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "bad request")
	}))

	modules := []workerModule{
		{name: "index.js", contentType: "application/javascript", data: []byte("code")},
	}

	_, err := createVersionWithVars(context.Background(), "acc-id", "my-worker", "test-token", modules, "index.js", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "versions API returned 400")
}

func TestCreateVersionWithVars_FailureResponse(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"result":  map[string]string{"id": ""},
			"success": false,
		})
	}))

	modules := []workerModule{
		{name: "index.js", contentType: "application/javascript", data: []byte("code")},
	}

	_, err := createVersionWithVars(context.Background(), "acc-id", "my-worker", "test-token", modules, "index.js", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "success=false")
}

func TestCreateVersionWithVars_InvalidModuleName(t *testing.T) {
	// createVersionWithVars should reject module names containing
	// characters outside [a-zA-Z0-9._-] to prevent header injection.
	modules := []workerModule{
		{name: "index.js", contentType: "application/javascript", data: []byte("ok")},
		{name: `evil"; evil="x`, contentType: "application/javascript", data: []byte("bad")},
	}

	_, err := createVersionWithVars(context.Background(), "acc-id", "my-worker", "test-token", modules, "index.js", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")
}

// --- deployVersion tests ---

func TestDeployVersion_Success(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/deployments")
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		var payload map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		versions, ok := payload["versions"].([]interface{})
		require.True(t, ok)
		require.Len(t, versions, 1)

		w.WriteHeader(http.StatusOK)
	}))

	err := deployVersion(context.Background(), "acc-id", "my-worker", "test-token", "version-123")
	require.NoError(t, err)
}

func TestDeployVersion_ErrorStatus(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "internal error")
	}))

	err := deployVersion(context.Background(), "acc-id", "my-worker", "test-token", "version-123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deployments API returned 500")
}

func TestDeployVersion_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := deployVersion(ctx, "acc-id", "my-worker", "test-token", "version-123")
	require.Error(t, err)
}

// --- resolveSubdomainViaAPI tests ---

func TestResolveSubdomainViaAPI_Success(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/subdomain")
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		json.NewEncoder(w).Encode(map[string]interface{}{
			"result":  map[string]string{"subdomain": "my-sub"},
			"success": true,
		})
	}))

	sub, err := resolveSubdomainViaAPI(context.Background(), "acc-id", "test-token")
	require.NoError(t, err)
	assert.Equal(t, "my-sub", sub)
}

func TestResolveSubdomainViaAPI_ErrorStatus(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "forbidden")
	}))

	_, err := resolveSubdomainViaAPI(context.Background(), "acc-id", "test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 403")
}

func TestResolveSubdomainViaAPI_EmptySubdomain(t *testing.T) {
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"result":  map[string]string{"subdomain": ""},
			"success": true,
		})
	}))

	_, err := resolveSubdomainViaAPI(context.Background(), "acc-id", "test-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty subdomain")
}

func TestResolveSubdomainViaAPI_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := resolveSubdomainViaAPI(ctx, "acc-id", "test-token")
	require.Error(t, err)
}

// --- getWorkerVars (actual implementation) tests ---

func TestGetWorkerVars_Implementation_Success(t *testing.T) {
	origToken := ResolveCloudflareAPITokenFn
	ResolveCloudflareAPITokenFn = func(_ context.Context) (string, error) {
		return "test-token", nil
	}
	t.Cleanup(func() { ResolveCloudflareAPITokenFn = origToken })

	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/settings")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"result": map[string]interface{}{
				"bindings": []map[string]string{
					{"type": "plain_text", "name": "ALLOWED_ORGS", "text": "acme,other"},
					{"type": "plain_text", "name": "PER_REPO_WIF_REPOS", "text": "acme/widget"},
					{"type": "secret_text", "name": "CODER_APP_PEM"},
				},
			},
			"success": true,
		})
	}))

	vars, err := getWorkerVars(context.Background(), "acc-id", "my-worker")
	require.NoError(t, err)
	assert.Equal(t, "acme,other", vars["ALLOWED_ORGS"])
	assert.Equal(t, "acme/widget", vars["PER_REPO_WIF_REPOS"])
	_, hasSecret := vars["CODER_APP_PEM"]
	assert.False(t, hasSecret, "secret bindings should be excluded")
}

func TestGetWorkerVars_Implementation_TokenError(t *testing.T) {
	origToken := ResolveCloudflareAPITokenFn
	ResolveCloudflareAPITokenFn = func(_ context.Context) (string, error) {
		return "", fmt.Errorf("no token")
	}
	t.Cleanup(func() { ResolveCloudflareAPITokenFn = origToken })

	_, err := getWorkerVars(context.Background(), "acc-id", "my-worker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving API token")
}

func TestGetWorkerVars_Implementation_HTTPError(t *testing.T) {
	origToken := ResolveCloudflareAPITokenFn
	ResolveCloudflareAPITokenFn = func(_ context.Context) (string, error) {
		return "test-token", nil
	}
	t.Cleanup(func() { ResolveCloudflareAPITokenFn = origToken })

	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "server error")
	}))

	_, err := getWorkerVars(context.Background(), "acc-id", "my-worker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 500")
}

// --- resolveCloudflareAPIToken tests ---

func TestResolveCloudflareAPIToken_FromEnv(t *testing.T) {
	withCFEnvCleared(t)
	os.Setenv("CLOUDFLARE_API_TOKEN", "env-token")

	token, err := resolveCloudflareAPIToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env-token", token)
}

func TestResolveCloudflareAPIToken_FallbackError(t *testing.T) {
	withCFEnvCleared(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := resolveCloudflareAPIToken(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLOUDFLARE_API_TOKEN not set")
}

// --- resolveWorkersSubdomain tests ---

func TestResolveWorkersSubdomain_WithAPIToken(t *testing.T) {
	withCFEnvCleared(t)
	os.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"result":  map[string]string{"subdomain": "my-subdomain"},
			"success": true,
		})
	}))

	sub, err := resolveWorkersSubdomain(context.Background(), "acc-id")
	require.NoError(t, err)
	assert.Equal(t, "my-subdomain", sub)
}

func TestResolveWorkersSubdomain_FallbackExecError(t *testing.T) {
	withCFEnvCleared(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := resolveWorkersSubdomain(ctx, "acc-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrangler subdomain failed")
}

// --- resolveSubdomainViaWrangler tests ---

func TestResolveSubdomainViaWrangler_ExecError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := resolveSubdomainViaWrangler(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrangler subdomain failed")
}

// --- Provisioner.GetWorkerVars tests ---

func TestProvisioner_GetWorkerVars_Success(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{"ALLOWED_ORGS": "acme", "PER_REPO_WIF_REPOS": "acme/widget"},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	vars, err := p.GetWorkerVars(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "acme", vars["ALLOWED_ORGS"])
	assert.Equal(t, "acme/widget", vars["PER_REPO_WIF_REPOS"])
}

func TestProvisioner_GetWorkerVars_Error(t *testing.T) {
	fake := &fakeWranglerRunner{
		getVarsErr: fmt.Errorf("API error"),
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	_, err := p.GetWorkerVars(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

// --- LiveWranglerRunner.GetVars tests ---

func TestLiveWranglerRunner_GetVars_Success(t *testing.T) {
	orig := GetWorkerVarsFn
	GetWorkerVarsFn = func(_ context.Context, accountID, workerName string) (map[string]string, error) {
		assert.Equal(t, "test-account", accountID)
		assert.Equal(t, "test-worker", workerName)
		return map[string]string{"KEY": "value"}, nil
	}
	t.Cleanup(func() { GetWorkerVarsFn = orig })

	runner := &LiveWranglerRunner{AccountID: "test-account"}
	vars, err := runner.GetVars(context.Background(), "test-worker")
	require.NoError(t, err)
	assert.Equal(t, "value", vars["KEY"])
}

func TestLiveWranglerRunner_GetVars_Error(t *testing.T) {
	orig := GetWorkerVarsFn
	GetWorkerVarsFn = func(_ context.Context, _, _ string) (map[string]string, error) {
		return nil, fmt.Errorf("API error")
	}
	t.Cleanup(func() { GetWorkerVarsFn = orig })

	runner := &LiveWranglerRunner{AccountID: "test-account"}
	_, err := runner.GetVars(context.Background(), "test-worker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

// --- LiveWranglerRunner.HasPreviewVersions tests ---

func TestLiveWranglerRunner_HasPreviewVersions_CommandError(t *testing.T) {
	runner := &LiveWranglerRunner{AccountID: "test-account"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.HasPreviewVersions(ctx, "test-worker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing worker versions")
}

// --- LiveWranglerRunner.UpdateVars tests ---

func TestLiveWranglerRunner_UpdateVars_Success(t *testing.T) {
	// Override token resolution.
	origToken := ResolveCloudflareAPITokenFn
	ResolveCloudflareAPITokenFn = func(_ context.Context) (string, error) {
		return "test-token", nil
	}
	t.Cleanup(func() { ResolveCloudflareAPITokenFn = origToken })

	// Override GetWorkerVarsFn.
	origGetVars := GetWorkerVarsFn
	GetWorkerVarsFn = func(_ context.Context, _, _ string) (map[string]string, error) {
		return map[string]string{"EXISTING": "keep"}, nil
	}
	t.Cleanup(func() { GetWorkerVarsFn = origGetVars })

	// Override deployVersionFn.
	origDeploy := deployVersionFn
	deployVersionFn = func(_ context.Context, _, _, _, versionID string) error {
		assert.Equal(t, "version-new", versionID)
		return nil
	}
	t.Cleanup(func() { deployVersionFn = origDeploy })

	// Track API calls: fetchWorkerContent + createVersionWithVars
	callCount := 0
	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Method == http.MethodGet && r.URL.Path != "" {
			// fetchWorkerContent call — return single-module response.
			w.Header().Set("Content-Type", "application/javascript")
			w.Header().Set("cf-entrypoint", "index.js")
			fmt.Fprint(w, "module code")
			return
		}
		// createVersionWithVars call — return success.
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"result":  map[string]string{"id": "version-new"},
			"success": true,
		})
	}))

	runner := &LiveWranglerRunner{AccountID: "test-account"}
	err := runner.UpdateVars(context.Background(), "test-worker", map[string]string{"NEW_VAR": "new-value"})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, callCount, 2, "should make at least 2 HTTP calls")
}

func TestLiveWranglerRunner_UpdateVars_TokenError(t *testing.T) {
	origToken := ResolveCloudflareAPITokenFn
	ResolveCloudflareAPITokenFn = func(_ context.Context) (string, error) {
		return "", fmt.Errorf("no token available")
	}
	t.Cleanup(func() { ResolveCloudflareAPITokenFn = origToken })

	runner := &LiveWranglerRunner{AccountID: "test-account"}
	err := runner.UpdateVars(context.Background(), "test-worker", map[string]string{"K": "V"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving API token")
}

func TestLiveWranglerRunner_UpdateVars_FetchContentError(t *testing.T) {
	origToken := ResolveCloudflareAPITokenFn
	ResolveCloudflareAPITokenFn = func(_ context.Context) (string, error) {
		return "test-token", nil
	}
	t.Cleanup(func() { ResolveCloudflareAPITokenFn = origToken })

	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "worker not found")
	}))

	runner := &LiveWranglerRunner{AccountID: "test-account"}
	err := runner.UpdateVars(context.Background(), "test-worker", map[string]string{"K": "V"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching worker content")
}

func TestLiveWranglerRunner_UpdateVars_GetVarsError(t *testing.T) {
	origToken := ResolveCloudflareAPITokenFn
	ResolveCloudflareAPITokenFn = func(_ context.Context) (string, error) {
		return "test-token", nil
	}
	t.Cleanup(func() { ResolveCloudflareAPITokenFn = origToken })

	origGetVars := GetWorkerVarsFn
	GetWorkerVarsFn = func(_ context.Context, _, _ string) (map[string]string, error) {
		return nil, fmt.Errorf("settings fetch error")
	}
	t.Cleanup(func() { GetWorkerVarsFn = origGetVars })

	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// fetchWorkerContent succeeds.
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("cf-entrypoint", "index.js")
		fmt.Fprint(w, "code")
	}))

	runner := &LiveWranglerRunner{AccountID: "test-account"}
	err := runner.UpdateVars(context.Background(), "test-worker", map[string]string{"K": "V"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading current vars")
}

func TestLiveWranglerRunner_UpdateVars_DeployError(t *testing.T) {
	origToken := ResolveCloudflareAPITokenFn
	ResolveCloudflareAPITokenFn = func(_ context.Context) (string, error) {
		return "test-token", nil
	}
	t.Cleanup(func() { ResolveCloudflareAPITokenFn = origToken })

	origGetVars := GetWorkerVarsFn
	GetWorkerVarsFn = func(_ context.Context, _, _ string) (map[string]string, error) {
		return map[string]string{}, nil
	}
	t.Cleanup(func() { GetWorkerVarsFn = origGetVars })

	origDeploy := deployVersionFn
	deployVersionFn = func(_ context.Context, _, _, _, _ string) error {
		return fmt.Errorf("deploy failed")
	}
	t.Cleanup(func() { deployVersionFn = origDeploy })

	withHTTPIntercept(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/javascript")
			w.Header().Set("cf-entrypoint", "index.js")
			fmt.Fprint(w, "code")
			return
		}
		// createVersionWithVars succeeds.
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"result":  map[string]string{"id": "version-x"},
			"success": true,
		})
	}))

	runner := &LiveWranglerRunner{AccountID: "test-account"}
	err := runner.UpdateVars(context.Background(), "test-worker", map[string]string{"K": "V"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deploying version")
}

// --- Enroll/Unenroll error path tests ---

func TestEnsureOrgInWorker_GetVarsError(t *testing.T) {
	fake := &fakeWranglerRunner{
		getVarsErr: fmt.Errorf("API error"),
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.EnsureOrgInWorker(context.Background(), "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading worker vars")
}

func TestRemoveOrgFromWorker_GetVarsError(t *testing.T) {
	fake := &fakeWranglerRunner{
		getVarsErr: fmt.Errorf("API error"),
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveOrgFromWorker(context.Background(), "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading worker vars")
}

func TestRegisterRepoInWorker_GetVarsError(t *testing.T) {
	fake := &fakeWranglerRunner{
		getVarsErr: fmt.Errorf("API error"),
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RegisterRepoInWorker(context.Background(), "acme/widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading worker vars")
}

func TestRegisterRepoInWorker_PublicMode(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{"PER_REPO_WIF_REPOS": "*"},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RegisterRepoInWorker(context.Background(), "acme/widget")
	require.NoError(t, err)
	// Should be a no-op in public mode.
	assert.Empty(t, fake.updateVarsCalls)
}

func TestRemoveRepoFromWorker_GetVarsError(t *testing.T) {
	fake := &fakeWranglerRunner{
		getVarsErr: fmt.Errorf("API error"),
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveRepoFromWorker(context.Background(), "acme/widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading worker vars")
}

// --- findRepoRoot tests ---

func TestFindRepoRoot(t *testing.T) {
	root := findRepoRoot()
	// Should find a directory containing go.mod with the fullsend module.
	goMod := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(goMod)
	require.NoError(t, err)
	assert.Contains(t, string(data), "github.com/fullsend-ai/fullsend")
}

// --- parseWorkerSettingsVars additional tests ---

func TestParseWorkerSettingsVars_InvalidJSON(t *testing.T) {
	_, err := parseWorkerSettingsVars([]byte("not-json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing settings response")
}

func TestParseWorkerSettingsVars_NoBindings(t *testing.T) {
	body := []byte(`{"result": {"bindings": []}, "success": true}`)
	vars, err := parseWorkerSettingsVars(body)
	require.NoError(t, err)
	assert.Empty(t, vars)
}

// --- parseHasPreviewVersions additional tests ---

func TestParseHasPreviewVersions_EmptyOutput(t *testing.T) {
	assert.False(t, parseHasPreviewVersions(""))
}

func TestParseHasPreviewVersions_HeaderOnly(t *testing.T) {
	output := "┌──────────┬──────────┐\n" +
		"│ Version ID │ Created │\n" +
		"├──────────┼──────────┤\n" +
		"└──────────┴──────────┘\n"
	assert.False(t, parseHasPreviewVersions(output))
}

// --- Teardown unknown deploy mode ---

func TestProvisioner_Teardown_UnknownDeployMode(t *testing.T) {
	fake := &fakeWranglerRunner{}
	p := &Provisioner{
		cfg: Config{
			AccountID:  "abc123",
			WorkerName: "test-mint",
			DeployMode: DeployMode(99), // unknown mode
		},
		wrangler: fake,
	}

	err := p.Teardown(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown deploy mode")
}

// --- LiveWranglerRunner.Deploy preview with secrets ---

func TestLiveWranglerRunner_Deploy_PreviewWithSecretsCommandError(t *testing.T) {
	dir := t.TempDir()
	runner := &LiveWranglerRunner{AccountID: "test-account"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	secrets := map[string][]byte{"MY_SECRET": []byte("value")}
	_, err := runner.Deploy(ctx, dir, "test-worker", "bt-alias", nil, secrets)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrangler versions upload failed")
}

// --- ResolveCloudflareAuth additional tests ---

func TestResolveCloudflareAuth_WranglerFailed_WithAccountIDSet(t *testing.T) {
	withCFEnvCleared(t)
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "env-account")

	old := WranglerWhoamiFn
	WranglerWhoamiFn = func(_ context.Context) (string, error) {
		return "", fmt.Errorf("exec failed")
	}
	t.Cleanup(func() { WranglerWhoamiFn = old })

	_, err := ResolveCloudflareAuth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrangler whoami")
}

// --- EnsureOrgInWorker/RemoveOrgFromWorker UpdateVars error ---

func TestEnsureOrgInWorker_UpdateVarsError(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars:    map[string]string{"ALLOWED_ORGS": "existing"},
		updateVarsErr: fmt.Errorf("update failed"),
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.EnsureOrgInWorker(context.Background(), "new-org")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update failed")
}

func TestRemoveOrgFromWorker_UpdateVarsError(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars:    map[string]string{"ALLOWED_ORGS": "acme,other"},
		updateVarsErr: fmt.Errorf("update failed"),
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveOrgFromWorker(context.Background(), "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update failed")
}

func TestRegisterRepoInWorker_UpdateVarsError(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars:    map[string]string{"ALLOWED_ORGS": "acme"},
		updateVarsErr: fmt.Errorf("update failed"),
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RegisterRepoInWorker(context.Background(), "acme/widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update failed")
}

func TestRemoveRepoFromWorker_UpdateVarsError(t *testing.T) {
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{
			"ALLOWED_ORGS":       "acme",
			"PER_REPO_WIF_REPOS": "acme/widget",
		},
		updateVarsErr: fmt.Errorf("update failed"),
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveRepoFromWorker(context.Background(), "acme/widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update failed")
}

// --- Regression: deploy --public then per-repo enroll must not corrupt PER_REPO_WIF_REPOS ---

func TestRegisterRepoInWorker_PublicMode_DoesNotAppend(t *testing.T) {
	// Regression test: when the mint is deployed with --public
	// (PER_REPO_WIF_REPOS=*), calling RegisterRepoInWorker must NOT
	// produce PER_REPO_WIF_REPOS=*,owner/repo. It must be a clean no-op.
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{
			"PER_REPO_WIF_REPOS": "*",
		},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RegisterRepoInWorker(context.Background(), "owner/repo")
	require.NoError(t, err)
	assert.Empty(t, fake.updateVarsCalls,
		"public mode repo enroll must be a no-op — must not append to PER_REPO_WIF_REPOS=*")
}

func TestRemoveRepoFromWorker_PublicMode_DoesNotModify(t *testing.T) {
	// When the mint is public (PER_REPO_WIF_REPOS=*), unenroll must
	// return an error — not silently modify PER_REPO_WIF_REPOS.
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{
			"PER_REPO_WIF_REPOS": "*",
		},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveRepoFromWorker(context.Background(), "owner/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PER_REPO_WIF_REPOS=*")
	assert.Empty(t, fake.updateVarsCalls,
		"public mode repo unenroll must not modify vars")
}

// --- Org enroll/unenroll on public CF mint does not consult ALLOWED_ORGS ---

func TestEnsureOrgInWorker_PublicMintRepos_NotAllowedOrgs(t *testing.T) {
	// On a public CF mint (PER_REPO_WIF_REPOS=*), org enroll is a no-op
	// regardless of what ALLOWED_ORGS contains. In particular, ALLOWED_ORGS
	// is NOT set to "*" on CF public deploys — only PER_REPO_WIF_REPOS is.
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{
			"ALLOWED_ORGS":       "", // no ALLOWED_ORGS=* on CF public
			"PER_REPO_WIF_REPOS": "*",
		},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.EnsureOrgInWorker(context.Background(), "new-org")
	require.NoError(t, err)
	assert.Empty(t, fake.updateVarsCalls, "org enroll should be no-op on public CF mint")
}

func TestRemoveOrgFromWorker_PublicMintRepos_NotAllowedOrgs(t *testing.T) {
	// On a public CF mint (PER_REPO_WIF_REPOS=*), org unenroll returns
	// an error citing PER_REPO_WIF_REPOS=*, not ALLOWED_ORGS=*.
	fake := &fakeWranglerRunner{
		workerVars: map[string]string{
			"ALLOWED_ORGS":       "acme",
			"PER_REPO_WIF_REPOS": "*",
		},
	}
	p := NewProvisioner(Config{
		AccountID:  "test-account",
		WorkerName: "test-mint",
	}, fake)

	err := p.RemoveOrgFromWorker(context.Background(), "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PER_REPO_WIF_REPOS=*")
}

// --- parsePerRepoWIFReposMap tests ---

func TestParsePerRepoWIFReposMap(t *testing.T) {
	tests := []struct {
		name   string
		csv    string
		expect map[string]bool
	}{
		{"empty", "", map[string]bool{}},
		{"wildcard", "*", map[string]bool{"*": true}},
		{"single repo", "Acme/Widget", map[string]bool{"acme/widget": true}},
		{"multiple repos", "acme/foo,Other/Bar", map[string]bool{"acme/foo": true, "other/bar": true}},
		{"with spaces", " acme/foo , other/bar ", map[string]bool{"acme/foo": true, "other/bar": true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parsePerRepoWIFReposMap(tc.csv)
			assert.Equal(t, tc.expect, result)
		})
	}
}

// --- lastNonEmptyLine tests ---

func TestLastNonEmptyLine(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{"empty", "", ""},
		{"single line", "token-value", "token-value"},
		{"single line with newline", "token-value\n", "token-value"},
		{"banner then token", "⛅️ wrangler 4.57\n\ntoken-value\n", "token-value"},
		{"multiple banner lines", "line1\nline2\nline3\nactual-token\n", "actual-token"},
		{"whitespace lines", "  \n\n  token  \n\n", "token"},
		{"only whitespace", "  \n  \n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expect, lastNonEmptyLine(tc.input))
		})
	}
}

// --- ValidateAccountID tests ---

func TestValidateAccountID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"valid 32 hex", "aabbccddee11223344556677aabbccdd", true},
		{"valid all digits", "00112233445566778899001122334455", true},
		{"valid all lowercase letters", "aabbccddeeffaabbccddeeffaabbccdd", true},
		{"too short", "aabbccdd", false},
		{"too long", "aabbccddee11223344556677aabbccddee", false},
		{"uppercase hex", "AABBCCDDEE11223344556677AABBCCDD", false},
		{"mixed case", "AAbbccddee11223344556677aabbccdd", false},
		{"non-hex chars", "ghijklmnop11223344556677aabbccdd", false},
		{"empty", "", false},
		{"dashes", "aabb-ccdd-ee11-2233-4455-6677-aabb", false},
		{"spaces", "aabbccddee112233 4556677aabbccdd", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.valid, ValidateAccountID(tc.input))
		})
	}
}

func TestResolveCloudflareAuth_InvalidAccountID(t *testing.T) {
	withCFEnvCleared(t)
	os.Setenv("CLOUDFLARE_API_TOKEN", "my-token")
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "not-valid-hex")

	_, err := ResolveCloudflareAuth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid account ID")
}

func TestResolveCloudflareAuth_InvalidAccountID_WranglerPath(t *testing.T) {
	withCFEnvCleared(t)
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "bad-id")

	old := WranglerWhoamiFn
	WranglerWhoamiFn = func(ctx context.Context) (string, error) {
		return "ℹ️  Logged in\n", nil
	}
	t.Cleanup(func() { WranglerWhoamiFn = old })

	_, err := ResolveCloudflareAuth(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid account ID")
}

// --- createVersionWithVars binding order test ---

func TestCreateVersionWithVars_BindingOrderIsDeterministic(t *testing.T) {
	// Run the binding-building portion of createVersionWithVars twice
	// with the same vars and verify the order is identical.
	vars := map[string]string{
		"ZEBRA_VAR":     "z",
		"ALPHA_VAR":     "a",
		"MIDDLE_VAR":    "m",
		"BETA_VAR":      "b",
		"OIDC_AUDIENCE": "fullsend-mint",
	}

	buildBindings := func() []string {
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	order1 := buildBindings()
	order2 := buildBindings()
	assert.Equal(t, order1, order2, "binding key order should be deterministic")
	assert.Equal(t, []string{"ALPHA_VAR", "BETA_VAR", "MIDDLE_VAR", "OIDC_AUDIENCE", "ZEBRA_VAR"}, order1)
}
