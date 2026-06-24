package gcf

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// healthyClient returns an *http.Client whose transport always responds 200 OK.
// Used in provisioner tests to satisfy the post-deploy health check without
// hitting a real endpoint.
func healthyClient() *http.Client {
	return &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       http.NoBody,
			}, nil
		}),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// newTestProvisioner wraps NewProvisioner with a healthy HTTP client so
// the post-deploy health check doesn't hit a real endpoint.
func newTestProvisioner(cfg Config, gcpAPI GCFClient) *Provisioner {
	p := NewProvisioner(cfg, gcpAPI)
	p.httpClient = healthyClient()
	return p
}

// --- helpers ---

func fakeFunctionSourceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.23\n\nrequire mintcore v0.0.0\n\nreplace mintcore => ../mintcore\n"), 0644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package function\n"), 0644)
	// bundleFunctionSource expects a sibling mintcore directory at ../mintcore.
	mintcoreDir := filepath.Join(dir, "..", "mintcore")
	os.MkdirAll(mintcoreDir, 0755)
	os.WriteFile(filepath.Join(mintcoreDir, "go.mod"), []byte("module mintcore\n\ngo 1.23\n"), 0644)
	os.WriteFile(filepath.Join(mintcoreDir, "stub.go"), []byte("package mintcore\n"), 0644)
	return dir
}

func singleRolePEMs() map[string][]byte {
	return map[string][]byte{"coder": []byte("test-pem-data")}
}

func singleRoleAppIDs() map[string]string {
	return map[string]string{"coder": "12345"}
}

func multiRolePEMs() map[string][]byte {
	return map[string][]byte{
		"coder":  []byte("coder-pem"),
		"triage": []byte("triage-pem"),
	}
}

func multiRoleAppIDs() map[string]string {
	return map[string]string{
		"coder":  "12345",
		"triage": "67890",
	}
}

// --- unit tests ---

func TestProvisioner_Name(t *testing.T) {
	p := newTestProvisioner(Config{}, nil)
	assert.Equal(t, "gcf", p.Name())
}

func TestProvisioner_OrgSecretNames(t *testing.T) {
	p := newTestProvisioner(Config{}, nil)
	assert.Nil(t, p.OrgSecretNames())
}

func TestProvisioner_OrgVariableNames(t *testing.T) {
	p := newTestProvisioner(Config{}, nil)
	assert.Equal(t, []string{"FULLSEND_MINT_URL"}, p.OrgVariableNames())
}

func TestProvisioner_DefaultConfig(t *testing.T) {
	p := newTestProvisioner(Config{}, nil)
	assert.Equal(t, "us-central1", p.cfg.Region)
	assert.Equal(t, "fullsend-pool", p.cfg.WIFPoolName)
	assert.Equal(t, "github-oidc", p.cfg.WIFProvider)
}

func TestProvisioner_CustomConfig(t *testing.T) {
	p := newTestProvisioner(Config{
		Region:      "europe-west1",
		WIFPoolName: "custom-pool",
		WIFProvider: "custom-prov",
	}, nil)
	assert.Equal(t, "europe-west1", p.cfg.Region)
	assert.Equal(t, "custom-pool", p.cfg.WIFPoolName)
	assert.Equal(t, "custom-prov", p.cfg.WIFProvider)
}

func TestSecretID(t *testing.T) {
	assert.Equal(t, "fullsend-coder-app-pem", secretID("coder"))
	assert.Equal(t, "fullsend-coder-app-pem", secretID("fix"))
	assert.Equal(t, "fullsend-triage-app-pem", secretID("triage"))
}

func TestStoreAgentPEM_FixRoleUsesCoderSecret(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = ErrSecretNotFound

	p := newTestProvisioner(Config{ProjectID: "my-project"}, fake)
	err := p.StoreAgentPEM(context.Background(), "fix", []byte("pem-data"))
	require.NoError(t, err)

	assert.Equal(t, "fullsend-coder-app-pem", fake.secretVersionNames[len(fake.secretVersionNames)-1])
}

// --- StoreAgentPEM tests ---

func TestStoreAgentPEM_CreatesNewSecret(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = ErrSecretNotFound

	p := newTestProvisioner(Config{ProjectID: "my-project"}, fake)
	err := p.StoreAgentPEM(context.Background(), "coder", []byte("pem-data"))
	require.NoError(t, err)

	assert.Equal(t, []string{
		"GetSecret",
		"CreateSecret",
		"AddSecretVersion",
		"SetSecretIAMBinding",
	}, fake.calls)
}

func TestStoreAgentPEM_ExistingSecretSkipsCreate(t *testing.T) {
	fake := newFakeGCFClient()

	p := newTestProvisioner(Config{ProjectID: "my-project"}, fake)
	err := p.StoreAgentPEM(context.Background(), "coder", []byte("pem-data"))
	require.NoError(t, err)

	assert.Contains(t, fake.calls, "GetSecret")
	assert.NotContains(t, fake.calls, "CreateSecret")
	assert.Contains(t, fake.calls, "AddSecretVersion")
	assert.Contains(t, fake.calls, "SetSecretIAMBinding")
}

func TestStoreAgentPEM_MissingProjectID(t *testing.T) {
	p := newTestProvisioner(Config{}, newFakeGCFClient())
	err := p.StoreAgentPEM(context.Background(), "coder", []byte("pem"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP project ID is required")
}

func TestStoreAgentPEM_InvalidRole(t *testing.T) {
	p := newTestProvisioner(Config{ProjectID: "my-project"}, newFakeGCFClient())
	for _, role := range []string{"CODER", "co der", "../escape", "role;drop"} {
		err := p.StoreAgentPEM(context.Background(), role, []byte("pem"))
		require.Error(t, err, "role %q should be rejected", role)
		assert.Contains(t, err.Error(), "invalid role name")
	}
	err := p.StoreAgentPEM(context.Background(), "coder", []byte("pem"))
	require.NoError(t, err)
}

func TestStoreAgentPEM_GetSecretNonNotFoundError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = fmt.Errorf("permission denied")

	p := newTestProvisioner(Config{ProjectID: "my-project"}, fake)
	err := p.StoreAgentPEM(context.Background(), "coder", []byte("pem"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestEnsureMintServiceAccount(t *testing.T) {
	fake := newFakeGCFClient()
	p := newTestProvisioner(Config{ProjectID: "my-project"}, fake)

	err := p.EnsureMintServiceAccount(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"CreateServiceAccount"}, fake.calls)
}

func TestEnsureMintServiceAccount_MissingProjectID(t *testing.T) {
	p := newTestProvisioner(Config{}, newFakeGCFClient())
	err := p.EnsureMintServiceAccount(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project ID is required")
}

// --- self-managed provision tests ---

func TestProvisioner_Provision_FullFlow(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = ErrSecretNotFound
	fake.functionInfoAfterCreate = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":           "test-org",
			"ROLE_APP_IDS":           `{"coder":"12345"}`,
			"ALLOWED_ROLES":          "coder",
			"ALLOWED_WORKFLOW_FILES": "*",
		},
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)

	expected := []string{
		"GetFunction", // auto-routing check (no existing function → full deploy)
		"CreateServiceAccount",
		"GetProjectNumber",
		"CreateWIFPool",
		"GetWIFProvider",
		"CreateWIFProvider",
		"SetProjectIAMBinding",
		"GetSecret",
		"CreateSecret",
		"AddSecretVersion",
		"SetSecretIAMBinding",
		"UploadFunctionSource",
		"CreateFunction",
		"WaitForOperation",
		"GetFunction",
		"GetFunction",              // EnsureOrgInMint checks function metadata
		"GetServiceTrafficEnvVars", // EnsureOrgInMint reads traffic-serving env vars (no-op after first deploy)
		"SetCloudRunInvoker",
	}
	assert.Equal(t, expected, fake.calls)

	require.Contains(t, vars, "FULLSEND_MINT_URL")
	assert.Equal(t, "https://fullsend-mint-abc123.run.app", vars["FULLSEND_MINT_URL"])

	// Verify project IAM binding arguments.
	require.Len(t, fake.projectIAMBindings, 1)
	assert.Equal(t, "my-project", fake.projectIAMBindings[0].ProjectID)
	assert.Equal(t, "roles/aiplatform.user", fake.projectIAMBindings[0].Role)
	assert.Contains(t, fake.projectIAMBindings[0].Member, "principalSet://iam.googleapis.com/")
	assert.Contains(t, fake.projectIAMBindings[0].Member, "attribute.repository/test-org/.fullsend")

	// Verify PEMs were zeroed.
	for role, pem := range p.cfg.AgentPEMs {
		for _, b := range pem {
			if b != 0 {
				t.Fatalf("PEM for role %s was not zeroed after provisioning", role)
			}
		}
	}
}

func TestProvisioner_Provision_MultiRole(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = ErrSecretNotFound
	fake.functionInfoAfterCreate = &FunctionInfo{
		URI: "https://fullsend-mint-abc123.run.app",
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         multiRolePEMs(),
		AgentAppIDs:       multiRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://fullsend-mint-abc123.run.app", vars["FULLSEND_MINT_URL"])

	// Each role should trigger GetSecret+CreateSecret+AddSecretVersion+SetSecretIAMBinding.
	getSecretCount := 0
	createSecretCount := 0
	addVersionCount := 0
	iamCount := 0
	for _, call := range fake.calls {
		switch call {
		case "GetSecret":
			getSecretCount++
		case "CreateSecret":
			createSecretCount++
		case "AddSecretVersion":
			addVersionCount++
		case "SetSecretIAMBinding":
			iamCount++
		}
	}
	assert.Equal(t, 2, getSecretCount)
	assert.Equal(t, 2, createSecretCount)
	assert.Equal(t, 2, addVersionCount)
	assert.Equal(t, 2, iamCount)
}

func TestProvisioner_Provision_ExistingFunction(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://us-central1-my-project.cloudfunctions.net/fullsend-mint",
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)

	assert.Contains(t, fake.calls, "UpdateFunction")
	assert.NotContains(t, fake.calls, "CreateFunction")
	assert.NotContains(t, fake.calls, "CreateSecret")
	assert.Contains(t, fake.calls, "AddSecretVersion")
	assert.Contains(t, fake.calls, "SetSecretIAMBinding")
	assert.Contains(t, fake.calls, "SetCloudRunInvoker")

	assert.Equal(t, "https://us-central1-my-project.cloudfunctions.net/fullsend-mint", vars["FULLSEND_MINT_URL"])
}

func TestProvisioner_Provision_SkipsRedeployWhenUnchanged(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	sourceZip, err := bundleFunctionSource(srcDir)
	require.NoError(t, err)
	srcHash := sha256Hex(sourceZip)

	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"GCP_PROJECT_NUMBER":     "123456789",
			"WIF_POOL_NAME":          "fullsend-pool",
			"WIF_PROVIDER_NAME":      "github-oidc",
			"ALLOWED_ORGS":           "test-org",
			"OIDC_AUDIENCE":          "fullsend-mint",
			"ALLOWED_ROLES":          "coder",
			"ROLE_APP_IDS":           `{"coder":"12345"}`,
			"FULLSEND_SOURCE_HASH":   srcHash,
			"ALLOWED_WORKFLOW_FILES": "*",
		},
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: srcDir,
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)

	assert.NotContains(t, fake.calls, "UploadFunctionSource")
	assert.NotContains(t, fake.calls, "CreateFunction")
	assert.NotContains(t, fake.calls, "UpdateFunction")
	assert.NotContains(t, fake.calls, "WaitForOperation")

	assert.Equal(t, "https://fullsend-mint-abc123.run.app", vars["FULLSEND_MINT_URL"])
}

func TestProvisioner_Provision_SameHashAutoRoutesToExistingMint(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	sourceZip, err := bundleFunctionSource(srcDir)
	require.NoError(t, err)
	srcHash := sha256Hex(sourceZip)

	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"GCP_PROJECT_NUMBER":     "123456789",
			"WIF_POOL_NAME":          "fullsend-pool",
			"WIF_PROVIDER_NAME":      "github-oidc",
			"ALLOWED_ORGS":           "test-org",
			"OIDC_AUDIENCE":          "fullsend-mint",
			"ALLOWED_ROLES":          "coder",
			"ROLE_APP_IDS":           `{"coder":"12345"}`,
			"FULLSEND_SOURCE_HASH":   srcHash,
			"ALLOWED_WORKFLOW_FILES": "*",
		},
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: srcDir,
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)

	// Same hash → WIF infrastructure still runs, but code deploy is skipped.
	assert.Contains(t, fake.calls, "GetProjectNumber")
	assert.Contains(t, fake.calls, "CreateServiceAccount")
	assert.Contains(t, fake.calls, "CreateWIFPool")
	assert.Contains(t, fake.calls, "CreateWIFProvider")
	assert.Contains(t, fake.calls, "SetProjectIAMBinding")
	// Code deploy skipped — auto-routed to provisionWithExistingMint for PEM + org registration.
	assert.NotContains(t, fake.calls, "UploadFunctionSource")
	assert.NotContains(t, fake.calls, "CreateFunction")
	assert.NotContains(t, fake.calls, "UpdateFunction")
	assert.Equal(t, "https://fullsend-mint-abc123.run.app", vars["FULLSEND_MINT_URL"])
}

func TestProvisioner_Provision_SkipDeployReusesExisting(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
		DeployMode:        DeploySkip,
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)

	// No code deployment.
	assert.NotContains(t, fake.calls, "UploadFunctionSource")
	assert.NotContains(t, fake.calls, "CreateFunction")
	assert.NotContains(t, fake.calls, "UpdateFunction")

	// EnsureOrgInMint still registers the org via env-var-only update.
	assert.Contains(t, fake.calls, "UpdateServiceEnvVars")
	assert.Equal(t, "https://fullsend-mint-abc123.run.app", vars["FULLSEND_MINT_URL"])
}

func TestProvisioner_Provision_SkipDeployNoExistingFunction(t *testing.T) {
	fake := newFakeGCFClient()

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
		DeployMode:        DeploySkip,
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skip-mint-deploy")
}

func TestProvisioner_Provision_CodeChanged_UpdatesFunction(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"GCP_PROJECT_NUMBER":     "123456789",
			"WIF_POOL_NAME":          "fullsend-pool",
			"WIF_PROVIDER_NAME":      "github-oidc",
			"ALLOWED_ORGS":           "test-org",
			"OIDC_AUDIENCE":          "fullsend-mint",
			"ALLOWED_ROLES":          "coder",
			"ROLE_APP_IDS":           `{"coder":"12345"}`,
			"FULLSEND_SOURCE_HASH":   "old-hash-that-wont-match",
			"ALLOWED_WORKFLOW_FILES": "*",
		},
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)

	// Code deploy happens via UpdateFunction (not CreateFunction).
	assert.Contains(t, fake.calls, "UploadFunctionSource")
	assert.Contains(t, fake.calls, "UpdateFunction")
	assert.NotContains(t, fake.calls, "CreateFunction")

	// UpdateFunction preserves existing env vars, only updating the hash.
	require.NotNil(t, fake.lastCreateFunctionEnvVars)
	assert.Equal(t, "test-org", fake.lastCreateFunctionEnvVars["ALLOWED_ORGS"])
	assert.NotEqual(t, "old-hash-that-wont-match", fake.lastCreateFunctionEnvVars["FULLSEND_SOURCE_HASH"])

	assert.Equal(t, "https://fullsend-mint-abc123.run.app", vars["FULLSEND_MINT_URL"])
}

func TestProvisioner_Provision_SameCodeNewOrg_EnvVarOnlyUpdate(t *testing.T) {
	srcDir := fakeFunctionSourceDir(t)
	sourceZip, err := bundleFunctionSource(srcDir)
	require.NoError(t, err)
	srcHash := sha256Hex(sourceZip)

	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"GCP_PROJECT_NUMBER":     "123456789",
			"WIF_POOL_NAME":          "fullsend-pool",
			"WIF_PROVIDER_NAME":      "github-oidc",
			"ALLOWED_ORGS":           "existing-org",
			"OIDC_AUDIENCE":          "fullsend-mint",
			"ALLOWED_ROLES":          "coder",
			"ROLE_APP_IDS":           `{"coder":"99999"}`,
			"FULLSEND_SOURCE_HASH":   srcHash,
			"ALLOWED_WORKFLOW_FILES": "*",
		},
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"new-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: srcDir,
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)

	// No code deployment — same source hash.
	assert.NotContains(t, fake.calls, "UploadFunctionSource")
	assert.NotContains(t, fake.calls, "CreateFunction")
	assert.NotContains(t, fake.calls, "UpdateFunction")

	// EnsureOrgInMint adds the new org via env-var-only update.
	assert.Contains(t, fake.calls, "UpdateServiceEnvVars")

	// Verify new org was added to ALLOWED_ORGS alongside existing.
	require.NotNil(t, fake.lastUpdateServiceEnvVars)
	allowedOrgs := fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"]
	assert.Contains(t, allowedOrgs, "new-org")
	assert.Contains(t, allowedOrgs, "existing-org")

	assert.Equal(t, "https://fullsend-mint-abc123.run.app", vars["FULLSEND_MINT_URL"])
}

func TestProvisioner_Provision_SecretExistsSkipsCreation(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://fullsend-mint-abc123.run.app",
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	assert.Contains(t, fake.calls, "GetSecret")
	assert.NotContains(t, fake.calls, "CreateSecret")
	assert.Contains(t, fake.calls, "AddSecretVersion")
	assert.Contains(t, fake.calls, "SetSecretIAMBinding")
}

func TestProvisioner_Provision_SecretNotFoundCreatesNew(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = ErrSecretNotFound
	fake.functionInfo = &FunctionInfo{
		URI: "https://fullsend-mint-abc123.run.app",
	}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	assert.Contains(t, fake.calls, "GetSecret")
	assert.Contains(t, fake.calls, "CreateSecret")
	assert.Contains(t, fake.calls, "AddSecretVersion")
	assert.Contains(t, fake.calls, "SetSecretIAMBinding")
}

// --- bundled mode tests ---

func TestProvisioner_Provision_BundledMode(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = ErrSecretNotFound
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/shared-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-shared.run.app",
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "test-org",
		},
	}

	p := newTestProvisioner(Config{
		ProjectID:  "shared-project",
		GitHubOrgs: []string{"test-org"},
		AgentPEMs:  singleRolePEMs(),
		MintURL:    "https://fullsend-mint-shared.run.app",
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "https://fullsend-mint-shared.run.app", vars["FULLSEND_MINT_URL"])

	// Bundled mode should store PEMs but skip all infra calls.
	assert.NotContains(t, fake.calls, "GetProjectNumber")
	assert.NotContains(t, fake.calls, "CreateServiceAccount")
	assert.NotContains(t, fake.calls, "CreateWIFPool")
	assert.NotContains(t, fake.calls, "CreateFunction")
	assert.Contains(t, fake.calls, "GetSecret")
	assert.Contains(t, fake.calls, "CreateSecret")
	assert.Contains(t, fake.calls, "AddSecretVersion")
}

func TestProvisioner_Provision_BundledMode_MissingProjectID(t *testing.T) {
	p := newTestProvisioner(Config{
		GitHubOrgs: []string{"test-org"},
		AgentPEMs:  singleRolePEMs(),
		MintURL:    "https://fullsend-mint-shared.run.app",
	}, newFakeGCFClient())

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP project ID is required")
}

func TestProvisioner_Provision_BundledMode_InvalidMintURL(t *testing.T) {
	tests := []struct {
		name    string
		mintURL string
	}{
		{"HTTP not HTTPS", "http://mint.example.com"},
		{"no scheme", "mint.example.com"},
		{"empty host", "https://"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProvisioner(Config{
				ProjectID:  "shared-project",
				GitHubOrgs: []string{"test-org"},
				AgentPEMs:  singleRolePEMs(),
				MintURL:    tc.mintURL,
			}, newFakeGCFClient())

			_, err := p.Provision(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "must be a valid Cloud Run URL")
		})
	}
}

// --- validation error tests ---

func TestProvisioner_Provision_MissingProjectID(t *testing.T) {
	p := newTestProvisioner(Config{
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, newFakeGCFClient())

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP project ID is required")
}

func TestProvisioner_Provision_MissingGitHubOrg(t *testing.T) {
	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, newFakeGCFClient())

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one GitHub org is required")
}

func TestProvisioner_Provision_NoPEMs_SecretsExist(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{URI: "https://fullsend-mint-abc123.run.app"}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         nil,
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://fullsend-mint-abc123.run.app", vars["FULLSEND_MINT_URL"])

	assert.NotContains(t, fake.calls, "AddSecretVersion")
	assert.Contains(t, fake.calls, "GetSecret")
	assert.Contains(t, fake.calls, "UpdateFunction")
}

func TestProvisioner_Provision_NoPEMs_SecretsMissing(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = ErrSecretNotFound

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         nil,
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no PEM and secret")
}

func TestProvisioner_Provision_PartialPEMs(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{URI: "https://fullsend-mint-abc123.run.app"}

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         map[string][]byte{"coder": []byte("coder-pem")},
		AgentAppIDs:       multiRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://fullsend-mint-abc123.run.app", vars["FULLSEND_MINT_URL"])

	addVersionCount := 0
	getSecretCount := 0
	for _, call := range fake.calls {
		switch call {
		case "AddSecretVersion":
			addVersionCount++
		case "GetSecret":
			getSecretCount++
		}
	}
	assert.Equal(t, 1, addVersionCount, "only coder PEM should be stored")
	assert.GreaterOrEqual(t, getSecretCount, 2, "GetSecret for coder PEM store + triage secret verify")
}

func TestProvisioner_Provision_NoPEMs_APIError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = fmt.Errorf("permission denied")

	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         nil,
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking secret")
	assert.Contains(t, err.Error(), "permission denied")
}

func TestProvisioner_Provision_BundledMode_NoPEMs_SecretsExist(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/shared-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-shared.run.app",
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "test-org",
			"ROLE_APP_IDS": `{"coder":"12345"}`,
		},
	}

	p := newTestProvisioner(Config{
		ProjectID:   "shared-project",
		GitHubOrgs:  []string{"test-org"},
		AgentPEMs:   nil,
		AgentAppIDs: singleRoleAppIDs(),
		MintURL:     "https://fullsend-mint-shared.run.app",
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://fullsend-mint-shared.run.app", vars["FULLSEND_MINT_URL"])

	assert.NotContains(t, fake.calls, "AddSecretVersion")
	assert.Contains(t, fake.calls, "GetSecret")
}

func TestProvisioner_Provision_BundledMode_NoPEMs_SecretsMissing(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = ErrSecretNotFound

	p := newTestProvisioner(Config{
		ProjectID:   "shared-project",
		GitHubOrgs:  []string{"test-org"},
		AgentPEMs:   nil,
		AgentAppIDs: singleRoleAppIDs(),
		MintURL:     "https://fullsend-mint-shared.run.app",
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no PEM and secret")
}

func TestProvisioner_Provision_BundledMode_NoPEMs_APIError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = fmt.Errorf("permission denied")

	p := newTestProvisioner(Config{
		ProjectID:   "shared-project",
		GitHubOrgs:  []string{"test-org"},
		AgentPEMs:   nil,
		AgentAppIDs: singleRoleAppIDs(),
		MintURL:     "https://fullsend-mint-shared.run.app",
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking secret")
	assert.Contains(t, err.Error(), "permission denied")
}

func TestProvisioner_Provision_BundledMode_PartialPEMs(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/shared-project/locations/us-central1/functions/fullsend-mint",
		State: "ACTIVE",
		URI:   "https://fullsend-mint-shared.run.app",
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "test-org",
			"ROLE_APP_IDS": `{"coder":"12345","triage":"67890"}`,
		},
	}

	p := newTestProvisioner(Config{
		ProjectID:   "shared-project",
		GitHubOrgs:  []string{"test-org"},
		AgentPEMs:   map[string][]byte{"coder": []byte("coder-pem")},
		AgentAppIDs: multiRoleAppIDs(),
		MintURL:     "https://fullsend-mint-shared.run.app",
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://fullsend-mint-shared.run.app", vars["FULLSEND_MINT_URL"])

	addVersionCount := 0
	getSecretCount := 0
	for _, call := range fake.calls {
		switch call {
		case "AddSecretVersion":
			addVersionCount++
		case "GetSecret":
			getSecretCount++
		}
	}
	assert.Equal(t, 1, addVersionCount, "only coder PEM should be stored")
	assert.GreaterOrEqual(t, getSecretCount, 2, "GetSecret for coder PEM store + triage secret verify")
}

func TestProvisioner_Provision_MissingAppIDs(t *testing.T) {
	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, newFakeGCFClient())

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one agent App ID is required")
}

func TestProvisioner_Provision_PEMWithoutAppID(t *testing.T) {
	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         map[string][]byte{"coder": []byte("pem"), "review": []byte("pem")},
		AgentAppIDs:       map[string]string{"coder": "123"},
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, newFakeGCFClient())

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has a PEM but no corresponding App ID")
}

func TestProvisioner_Provision_DuplicateOrgs(t *testing.T) {
	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"acme", "ACME"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, newFakeGCFClient())

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate GitHub org")
}

func TestProvisioner_Provision_InvalidGitHubOrg(t *testing.T) {
	tests := []struct {
		name string
		org  string
	}{
		{"injection attempt", "org'; DROP TABLE --"},
		{"starts with hyphen", "-org"},
		{"ends with hyphen", "org-"},
		{"special chars", "org/evil"},
		{"spaces", "my org"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProvisioner(Config{
				ProjectID:         "test-project-id",
				GitHubOrgs:        []string{tc.org},
				AgentPEMs:         singleRolePEMs(),
				AgentAppIDs:       singleRoleAppIDs(),
				FunctionSourceDir: fakeFunctionSourceDir(t),
			}, newFakeGCFClient())

			_, err := p.Provision(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid GitHub org name")
		})
	}
}

// --- GCP API error tests ---

func TestProvisioner_Provision_GetProjectNumberError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetProjectNumber"] = fmt.Errorf("permission denied")

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestProvisioner_Provision_CreateSAError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["CreateServiceAccount"] = fmt.Errorf("quota exceeded")

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota exceeded")
}

func TestProvisioner_Provision_CreateWIFPoolError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["CreateWIFPool"] = fmt.Errorf("pool error")

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pool error")
}

func TestProvisioner_Provision_CreateWIFProviderError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["CreateWIFProvider"] = fmt.Errorf("provider error")

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider error")
}

func TestProvisioner_Provision_GetWIFProviderError_FailsFast(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetWIFProvider"] = fmt.Errorf("transient error")

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading existing WIF provider for merge")
}

func TestProvisioner_Provision_CreateSecretError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = ErrSecretNotFound
	fake.errs["CreateSecret"] = fmt.Errorf("secret error")

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret error")
}

func TestProvisioner_Provision_AddSecretVersionError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetSecret"] = ErrSecretNotFound
	fake.errs["AddSecretVersion"] = fmt.Errorf("version error")

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version error")
}

func TestProvisioner_Provision_SetProjectIAMBindingError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["SetProjectIAMBinding"] = fmt.Errorf("project iam denied")

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "granting Agent Platform access for org org")
	assert.Contains(t, err.Error(), "project iam denied")
}

func TestProvisioner_Provision_MultiOrg_ProjectIAMBindings(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfoAfterCreate = &FunctionInfo{URI: "https://mint.run.app"}

	p := newTestProvisioner(Config{
		ProjectID:         "shared-project",
		GitHubOrgs:        []string{"org-a", "org-b"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	require.Len(t, fake.projectIAMBindings, 2)
	assert.Contains(t, fake.projectIAMBindings[0].Member, "attribute.repository/org-a/.fullsend")
	assert.Contains(t, fake.projectIAMBindings[1].Member, "attribute.repository/org-b/.fullsend")
	assert.Equal(t, "roles/aiplatform.user", fake.projectIAMBindings[0].Role)
	assert.Equal(t, "roles/aiplatform.user", fake.projectIAMBindings[1].Role)
}

func TestProvisioner_Provision_SetIAMBindingError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["SetSecretIAMBinding"] = fmt.Errorf("iam error")

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "iam error")
}

func TestProvisioner_Provision_CreateFunctionError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["CreateFunction"] = fmt.Errorf("deploy failed")

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deploy failed")
}

func TestProvisioner_Provision_GetFunctionError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetFunction"] = fmt.Errorf("function check failed")

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "function check failed")
}

// --- bundleFunctionSource tests ---

func TestBundleFunctionSource_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	// Create sibling mintcore dir so addDirToZip doesn't fail first.
	mintcoreDir := filepath.Join(dir, "..", "mintcore")
	os.MkdirAll(mintcoreDir, 0755)
	os.WriteFile(filepath.Join(mintcoreDir, "stub.go"), []byte("package mintcore\n"), 0644)

	_, err := bundleFunctionSource(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no deployable source files")
}

func TestBundleFunctionSource_MissingGoMod(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/main.go", []byte("package main"), 0644)
	// Create sibling mintcore dir so addDirToZip doesn't fail first.
	mintcoreDir := filepath.Join(dir, "..", "mintcore")
	os.MkdirAll(mintcoreDir, 0755)
	os.WriteFile(filepath.Join(mintcoreDir, "stub.go"), []byte("package mintcore\n"), 0644)

	_, err := bundleFunctionSource(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing go.mod")
}

func TestBundleFunctionSource_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/main.go", []byte("package main"), 0644)
	os.WriteFile(dir+"/go.mod", []byte("module test\n\nreplace mintcore => ../mintcore\n"), 0644)
	os.WriteFile(dir+"/main_test.go", []byte("package main"), 0644)
	os.WriteFile(dir+"/.hidden", []byte("hidden"), 0644)
	// Create sibling mintcore dir so addDirToZip doesn't fail first.
	mintcoreDir := filepath.Join(dir, "..", "mintcore")
	os.MkdirAll(mintcoreDir, 0755)
	os.WriteFile(filepath.Join(mintcoreDir, "stub.go"), []byte("package mintcore\n"), 0644)

	data, err := bundleFunctionSource(dir)
	require.NoError(t, err)

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	assert.Contains(t, names, "main.go")
	assert.Contains(t, names, "go.mod")
	assert.NotContains(t, names, "main_test.go")
	assert.NotContains(t, names, ".hidden")
}

func TestBundleFunctionSource_EmptyPath_UsesEmbedded(t *testing.T) {
	data, err := bundleFunctionSource("")
	require.NoError(t, err)
	require.NotEmpty(t, data)

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	assert.Contains(t, names, "go.mod")
	assert.Contains(t, names, "main.go")
	assert.Contains(t, names, "go.sum")
	assert.NotContains(t, names, "main_test.go")
}

func TestBundleFunctionSource_NonexistentDir_UsesEmbedded(t *testing.T) {
	data, err := bundleFunctionSource("/nonexistent/path/to/mint")
	require.NoError(t, err)
	require.NotEmpty(t, data)

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	assert.Contains(t, names, "go.mod")
	assert.Contains(t, names, "go.sum")
	assert.Contains(t, names, "main.go")
}

func TestBundleEmbeddedMintSource(t *testing.T) {
	data, err := bundleEmbeddedMintSource()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	assert.Contains(t, names, "go.mod")
	assert.Contains(t, names, "go.sum")
	assert.Contains(t, names, "main.go")
	assert.Contains(t, names, "mintcore/go.mod")
	assert.Contains(t, names, "mintcore/gcp_pem.go")
	assert.Contains(t, names, "mintcore/github.go")
	assert.Contains(t, names, "mintcore/jwks_verifier.go")
	assert.Contains(t, names, "mintcore/claims.go")
	assert.Contains(t, names, "mintcore/elevations.go")
	assert.Contains(t, names, "mintcore/patterns.go")
	assert.Contains(t, names, "mintcore/sts_verifier.go")
	assert.Contains(t, names, "mintcore/wif.go")
	assert.Contains(t, names, "mintcore/handler.go")
	assert.Contains(t, names, "mintcore/interfaces.go")
	assert.Contains(t, names, "mintcore/go.sum")
	assert.Len(t, names, 15)
}

func TestEmbeddedMintSource_MatchesOriginal(t *testing.T) {
	// origDir is internal/mint/ relative to this test's package (internal/dispatch/gcf/).
	origDir := filepath.Join("..", "..", "mint")
	mintcoreDir := filepath.Join("..", "..", "mintcore")
	entries, err := os.ReadDir(origDir)
	if os.IsNotExist(err) {
		t.Skipf("original mint source not available at %s (running outside repo)", origDir)
	}
	require.NoError(t, err, "reading original mint dir")

	// Check that every embedded file matches its original.
	for embeddedName, realName := range embeddedMintFiles {
		var origPath string
		if strings.HasPrefix(realName, "mintcore/") {
			origPath = filepath.Join(mintcoreDir, strings.TrimPrefix(realName, "mintcore/"))
		} else {
			origPath = filepath.Join(origDir, realName)
		}

		orig, err := os.ReadFile(origPath)
		require.NoError(t, err, "reading original %s", realName)

		embedded, err := embeddedMintSource.ReadFile("mintsrc/" + embeddedName)
		require.NoError(t, err, "reading embedded %s", embeddedName)

		// The embedded go.mod uses ./mintcore while the original uses ../mintcore.
		if realName == "go.mod" {
			orig = []byte(strings.Replace(string(orig), "=> ../mintcore", "=> ./mintcore", 1))
		}

		assert.Equal(t, string(orig), string(embedded),
			"mintsrc/%s is out of sync with original %s — copy to internal/dispatch/gcf/mintsrc/%s to update",
			embeddedName, realName, embeddedName)
	}

	// Check that no deployable files in internal/mint/ are missing from the embed map.
	knownReals := make(map[string]bool)
	for _, realName := range embeddedMintFiles {
		knownReals[realName] = true
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		if !knownReals[entry.Name()] {
			t.Errorf("internal/mint/%s exists but is not in embeddedMintFiles — add it to mintsrc/ with .embed suffix", entry.Name())
		}
	}

	// Check mintcore files too.
	mintcoreEntries, err := os.ReadDir(mintcoreDir)
	if err == nil {
		for _, entry := range mintcoreEntries {
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			// go.sum is now included in embeddedMintFiles.
			expected := "mintcore/" + entry.Name()
			if !knownReals[expected] {
				t.Errorf("internal/mintcore/%s exists but is not in embeddedMintFiles — add it to mintsrc/mintcore/ with .embed suffix", entry.Name())
			}
		}
	}
}

// --- multi-org tests ---

func TestProvisioner_Provision_MultiOrg_WIFCondition(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfoAfterCreate = &FunctionInfo{URI: "https://mint.run.app"}

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"acme", "widgetco"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "assertion.repository_owner in ['acme', 'widgetco']",
		fake.lastWIFProviderConfig.AttributeCondition)

	expectedIAMAudience := "https://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc"
	assert.Equal(t, []string{"fullsend-mint", expectedIAMAudience},
		fake.lastWIFProviderConfig.AllowedAudiences)
}

func TestProvisioner_Provision_SingleOrg_WIFCondition(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfoAfterCreate = &FunctionInfo{URI: "https://mint.run.app"}

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"acme"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "assertion.repository_owner == 'acme'",
		fake.lastWIFProviderConfig.AttributeCondition)

	expectedIAMAudience := "https://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc"
	assert.Equal(t, []string{"fullsend-mint", expectedIAMAudience},
		fake.lastWIFProviderConfig.AllowedAudiences)
}

func TestProvisioner_Provision_WIF_AllowedAudiences(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfoAfterCreate = &FunctionInfo{URI: "https://mint.run.app"}

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"acme"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{
		"fullsend-mint",
		"https://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc",
	}, fake.lastWIFProviderConfig.AllowedAudiences)
}

func TestProvisioner_Provision_MultiOrg_PEMStorage(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfoAfterCreate = &FunctionInfo{URI: "https://mint.run.app"}

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"org1", "org2"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	// PEMs are stored once per role (shared across orgs), so 1 role = 1 GetSecret + 1 AddSecretVersion.
	getSecretCount := 0
	addVersionCount := 0
	for _, call := range fake.calls {
		if call == "GetSecret" {
			getSecretCount++
		}
		if call == "AddSecretVersion" {
			addVersionCount++
		}
	}
	assert.Equal(t, 1, getSecretCount, "expected GetSecret called once per role")
	assert.Equal(t, 1, addVersionCount, "expected AddSecretVersion called once per role")
}

func TestProvisioner_Provision_MultiOrg_MergeDoesNotOverwriteExistingPEMs(t *testing.T) {
	fake := newFakeGCFClient()
	// Simulate an existing deployed function from a previous org's install.
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.run.app",
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "existing-org",
			"ROLE_APP_IDS": `{"coder":"999"}`,
		},
	}
	// Simulate existing WIF provider with existing-org already configured.
	fake.wifProvider = &WIFProviderInfo{
		AttributeCondition: "assertion.repository_owner == 'existing-org'",
	}

	p := newTestProvisioner(Config{
		ProjectID:         "test-project-id",
		GitHubOrgs:        []string{"new-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	// PEMs are stored once per role regardless of installing org.
	require.NotEmpty(t, fake.secretVersionNames, "expected at least one PEM to be stored")
	for _, name := range fake.secretVersionNames {
		assert.Equal(t, "fullsend-coder-app-pem", name)
	}

	// WIF condition should include both orgs.
	assert.Equal(t, "assertion.repository_owner in ['existing-org', 'new-org']",
		fake.lastWIFProviderConfig.AttributeCondition)

	// EnsureOrgInMint only updates ALLOWED_ORGS; shared ROLE_APP_IDS are unchanged.
	require.NotNil(t, fake.lastUpdateServiceEnvVars, "expected EnsureOrgInMint to update env vars")
	assert.Contains(t, fake.lastUpdateServiceEnvVars["ROLE_APP_IDS"], `"coder":"999"`)
	assert.Contains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "new-org")
	assert.Contains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "existing-org")
}

// --- ProvisionWIF tests ---

func TestProvisionWIF_HappyPath(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
	}, fake)

	wifProvider, err := p.ProvisionWIF(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc", wifProvider)

	assert.Contains(t, fake.calls, "GetProjectNumber")
	assert.Contains(t, fake.calls, "CreateWIFPool")
	assert.Contains(t, fake.calls, "CreateWIFProvider")
	assert.Contains(t, fake.calls, "SetProjectIAMBinding")

	assert.Equal(t, "assertion.repository_owner == 'acme'", fake.lastWIFProviderConfig.AttributeCondition)
}

func TestProvisionWIF_MissingProjectID(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		GitHubOrgs: []string{"acme"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP project ID is required")
}

func TestProvisionWIF_MissingOrgs(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID: "my-project",
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one GitHub org is required")
}

func TestProvisionWIF_IAMBindingFails(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["SetProjectIAMBinding"] = fmt.Errorf("policy error")
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "granting Agent Platform access for org acme")
}

func TestProvisionWIF_MultipleOrgs(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme", "beta"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "assertion.repository_owner in ['acme', 'beta']", fake.lastWIFProviderConfig.AttributeCondition)

	require.Len(t, fake.projectIAMBindings, 2)
	assert.Contains(t, fake.projectIAMBindings[0].Member, "attribute.repository/acme/.fullsend")
	assert.Contains(t, fake.projectIAMBindings[1].Member, "attribute.repository/beta/.fullsend")
}

func TestProvisionWIF_GetProjectNumberFails(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetProjectNumber"] = fmt.Errorf("forbidden")
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting project number")
}

func TestProvisionWIF_CreateWIFPoolFails(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["CreateWIFPool"] = fmt.Errorf("quota exceeded")
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating WIF pool")
}

func TestProvisionWIF_CreateWIFProviderFails(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["CreateWIFProvider"] = fmt.Errorf("invalid config")
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating WIF provider")
}

func TestProvisionWIF_InvalidOrgName(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"bad org!"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GitHub org name")
}

func TestProvisionWIF_DuplicateOrg(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme", "ACME"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate GitHub org after normalization")
}

func TestProvisionWIF_DoesNotMutateInput(t *testing.T) {
	fake := newFakeGCFClient()
	orgs := []string{"ACME"}
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: orgs,
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ACME", orgs[0], "ProvisionWIF should not mutate the input slice")
}

func TestProvisionWIF_InvalidProjectID(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID:  "BAD",
		GitHubOrgs: []string{"acme"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestProvisionWIF_NormalizesOrgCase(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"ACME"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "assertion.repository_owner == 'acme'", fake.lastWIFProviderConfig.AttributeCondition)
}

func TestProvisionWIF_RepoScoped(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
		Repo:       "acme/widget",
	}, fake)

	wifPath, err := p.ProvisionWIF(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "gh-acme-widget", fake.lastWIFProviderID)
	assert.Equal(t, "assertion.repository == 'acme/widget'", fake.lastWIFProviderConfig.AttributeCondition)
	assert.Contains(t, wifPath, "gh-acme-widget")

	require.Len(t, fake.projectIAMBindings, 1)
	assert.Contains(t, fake.projectIAMBindings[0].Member, "attribute.repository/acme/widget")

	assert.NotContains(t, fake.calls, "GetWIFProvider")
}

func TestProvisionWIF_RepoScoped_LowercasesRepo(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
		Repo:       "Acme/Widget",
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "assertion.repository == 'acme/widget'", fake.lastWIFProviderConfig.AttributeCondition)
	assert.Contains(t, fake.projectIAMBindings[0].Member, "attribute.repository/acme/widget")
	assert.Equal(t, "Acme/Widget", p.cfg.Repo, "ProvisionWIF should not mutate p.cfg.Repo")
}

func TestProvisionWIF_RepoScoped_DotPrefixRepo(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"nonflux"},
		Repo:       "nonflux/.fullsend",
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "assertion.repository == 'nonflux/.fullsend'", fake.lastWIFProviderConfig.AttributeCondition)
}

func TestProvisionWIF_RepoScoped_ErrorPreservesOriginalCase(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
		Repo:       "Owner.Name/Repo",
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Owner.Name", "error should show original casing, not lowercased")
}

func TestProvisionWIF_RepoScoped_DoesNotTouchSharedProvider(t *testing.T) {
	fake := newFakeGCFClient()
	fake.wifProvider = &WIFProviderInfo{
		AttributeCondition: "assertion.repository_owner == 'nonflux'",
	}
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
		Repo:       "acme/widget",
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "gh-acme-widget", fake.lastWIFProviderID)
	assert.Equal(t, "assertion.repository == 'acme/widget'", fake.lastWIFProviderConfig.AttributeCondition)
}

func TestProvisionWIF_OrgScoped_Unchanged(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "github-oidc", fake.lastWIFProviderID)
	assert.Equal(t, "assertion.repository_owner == 'acme'", fake.lastWIFProviderConfig.AttributeCondition)
	require.Len(t, fake.projectIAMBindings, 1)
	assert.Contains(t, fake.projectIAMBindings[0].Member, "attribute.repository/acme/.fullsend")
}

func TestProvisionWIF_RepoScoped_RejectsInvalidRepo(t *testing.T) {
	tests := []struct {
		name, repo, errContains string
	}{
		{"no slash", "just-a-name", "owner/repo format"},
		{"empty owner", "/repo", "owner/repo format"},
		{"empty repo", "owner/", "owner/repo format"},
		{"quotes in owner", "owner's/repo", "invalid repo owner"},
		{"backslash in repo", `owner/repo\`, "must contain only"},
		{"spaces in repo", "owner/my repo", "must contain only"},
		{"underscore in owner", "_owner/repo", "invalid repo owner"},
		{"dot in owner", "owner.name/repo", "invalid repo owner"},
		{"dot as repo", "owner/.", "cannot be"},
		{"dotdot as repo", "owner/..", "cannot be"},
		{"dot as owner", "./repo", "invalid repo owner"},
		{"double-hyphen in owner", "org--name/repo", "invalid repo owner"},
		{"git suffix", "owner/repo.git", "cannot end with"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeGCFClient()
			p := NewProvisioner(Config{
				ProjectID:  "my-project",
				GitHubOrgs: []string{"acme"},
				Repo:       tt.repo,
			}, fake)
			_, err := p.ProvisionWIF(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
			assert.NotContains(t, fake.calls, "GetProjectNumber")
		})
	}
}

func TestProvisionWIF_OrgScoped_MergesExistingOrgs(t *testing.T) {
	fake := newFakeGCFClient()
	fake.wifProvider = &WIFProviderInfo{
		AttributeCondition: "assertion.repository_owner in ['beta', 'gamma']",
	}
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.NoError(t, err)

	assert.Contains(t, fake.calls, "GetWIFProvider")
	assert.Equal(t, "assertion.repository_owner in ['acme', 'beta', 'gamma']",
		fake.lastWIFProviderConfig.AttributeCondition)

	// IAM binding should only be for the installing org, not the merged ones.
	require.Len(t, fake.projectIAMBindings, 1)
	assert.Contains(t, fake.projectIAMBindings[0].Member, "attribute.repository/acme/.fullsend")
}

func TestProvisionWIF_OrgScoped_GetProviderError_FailsToPreventClobber(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetWIFProvider"] = fmt.Errorf("transient error")
	p := NewProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"acme"},
	}, fake)

	_, err := p.ProvisionWIF(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading existing WIF provider for merge")
}

func TestParseConditionOrgs(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		want      []string
	}{
		{"single org", "assertion.repository_owner == 'acme'", []string{"acme"}},
		{"multiple orgs", "assertion.repository_owner in ['alpha', 'beta', 'gamma']", []string{"alpha", "beta", "gamma"}},
		{"legacy repo-scoped", "assertion.repository == 'acme/.fullsend'", []string{"acme"}},
		{"mixed case normalized", "assertion.repository_owner in ['AcMe', 'BETA']", []string{"acme", "beta"}},
		{"empty condition", "", nil},
		{"no quoted orgs", "assertion.repository_owner == true", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseConditionOrgs(tc.condition)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBuildAttributeCondition(t *testing.T) {
	t.Run("single org scopes to repository_owner", func(t *testing.T) {
		got := buildAttributeCondition([]string{"myorg"})
		assert.Equal(t, "assertion.repository_owner == 'myorg'", got)
	})

	t.Run("multiple orgs uses in with repository_owner", func(t *testing.T) {
		got := buildAttributeCondition([]string{"org1", "org2"})
		assert.Equal(t, "assertion.repository_owner in ['org1', 'org2']", got)
	})
}

func TestBuildRepoProviderID(t *testing.T) {
	tests := []struct {
		owner, repo string
		want        string
	}{
		{"acme", "widget", "gh-acme-widget"},
		{"Acme", "My.Repo_v2", "gh-acme-my-repo-v2"},
		{"org", "very-long-repository-name-that-exceeds-limit", "gh-org-very-long-repository-name"},
		{"a", "b", "gh-a-b"},
		{"nonflux", "integration-service", "gh-nonflux-integration-service"},
		{"halfsend", "test-repo", "gh-halfsend-test-repo"},
	}
	for _, tt := range tests {
		t.Run(tt.owner+"/"+tt.repo, func(t *testing.T) {
			got := mintcore.BuildRepoProviderID(tt.owner, tt.repo)
			assert.Equal(t, tt.want, got)
			assert.GreaterOrEqual(t, len(got), 4)
			assert.LessOrEqual(t, len(got), 32)
			assert.NotEqual(t, '-', rune(got[len(got)-1]))
		})
	}
}

// --- stripPlaceholderOrg tests ---

func TestStripPlaceholderOrg(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", ""},
		{"only placeholder", PlaceholderOrg, ""},
		{"placeholder with real orgs", "acme," + PlaceholderOrg + ",widgetco", "acme,widgetco"},
		{"no placeholder", "acme,widgetco", "acme,widgetco"},
		{"placeholder at start", PlaceholderOrg + ",acme", "acme"},
		{"placeholder at end", "acme," + PlaceholderOrg, "acme"},
		{"multiple placeholders", PlaceholderOrg + "," + PlaceholderOrg, ""},
		{"whitespace around entries", " acme , " + PlaceholderOrg + " , widgetco ", "acme,widgetco"},
		{"single real org", "acme", "acme"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stripPlaceholderOrg(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

// --- interface compliance ---

func TestProvisioner_ImplementsDispatcher(t *testing.T) {
	var _ interface {
		Name() string
		Provision(context.Context) (map[string]string, error)
		StoreAgentPEM(context.Context, string, []byte) error
		OrgSecretNames() []string
		OrgVariableNames() []string
	} = (*Provisioner)(nil)
}

func TestGetExistingRoleAppIDs_ReturnsMap(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://example.com",
		EnvVars: map[string]string{
			"ROLE_APP_IDS": `{"triage":"123","coder":"456"}`,
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	m, err := p.GetExistingRoleAppIDs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"triage": "123",
		"coder":  "456",
	}, m)
}

func TestGetExistingRoleAppIDs_NoFunction(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = nil

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	m, err := p.GetExistingRoleAppIDs(context.Background())
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestGetExistingRoleAppIDs_EmptyEnvVars(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI:     "https://example.com",
		EnvVars: map[string]string{},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	m, err := p.GetExistingRoleAppIDs(context.Background())
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestGetExistingRoleAppIDs_MalformedJSON(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://example.com",
		EnvVars: map[string]string{
			"ROLE_APP_IDS": "not-json",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	m, err := p.GetExistingRoleAppIDs(context.Background())
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestGetExistingRoleAppIDs_GetFunctionError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetFunction"] = fmt.Errorf("permission denied")

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	m, err := p.GetExistingRoleAppIDs(context.Background())
	require.Error(t, err)
	assert.Nil(t, m)
	assert.Contains(t, err.Error(), "checking mint function")
}

// --- GetFunctionURL tests ---

func TestGetFunctionURL_ReturnsURL(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI:   "https://fullsend-mint-abc123.run.app",
		State: "ACTIVE",
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	url, err := p.GetFunctionURL(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://fullsend-mint-abc123.run.app", url)
}

func TestGetFunctionURL_NoFunction(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = nil

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	_, err := p.GetFunctionURL(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGetFunctionURL_EmptyURI(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		State: "ACTIVE",
		URI:   "",
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	_, err := p.GetFunctionURL(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- Provision with non-ACTIVE function ---

func TestProvisioner_Provision_NonActiveFunction_TriggersDeploy(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		Name:  "projects/my-project/locations/us-central1/functions/fullsend-mint",
		State: "FAILED",
		URI:   "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"FULLSEND_SOURCE_HASH": "different-hash",
		},
	}
	p := newTestProvisioner(Config{
		ProjectID:         "my-project",
		GitHubOrgs:        []string{"test-org"},
		AgentPEMs:         singleRolePEMs(),
		AgentAppIDs:       singleRoleAppIDs(),
		FunctionSourceDir: fakeFunctionSourceDir(t),
	}, fake)

	vars, err := p.Provision(context.Background())
	require.NoError(t, err)

	// Non-ACTIVE function should trigger full deploy path (UpdateFunction).
	assert.Contains(t, fake.calls, "UpdateFunction")
	assert.Equal(t, "https://fullsend-mint-abc123.run.app", vars["FULLSEND_MINT_URL"])
}

// --- PEM auto-copy in provisionWithExistingMint ---

func TestProvisioner_Provision_BundledMode_RequiresExistingPEM(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{
			"ROLE_APP_IDS":  `{"coder":"12345"}`,
			"ALLOWED_ORGS":  "source-org",
			"ALLOWED_ROLES": "coder",
		},
	}
	fake.secrets = map[string]bool{}

	p := newTestProvisioner(Config{
		ProjectID:   "my-project",
		GitHubOrgs:  []string{"target-org"},
		AgentAppIDs: map[string]string{"coder": "12345"},
		MintURL:     "https://fullsend-mint-abc123.run.app",
	}, fake)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no PEM")
	assert.NotContains(t, fake.calls, "AccessSecretVersion")
}

// --- EnsureOrgInMint tests ---

func TestEnsureOrgInMint_OrgAlreadyCovered(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":  "acme-corp",
			"ROLE_APP_IDS":  `{"coder":"111","reviewer":"222"}`,
			"ALLOWED_ROLES": "coder,reviewer",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "acme-corp")
	require.NoError(t, err)
	assert.NotContains(t, fake.calls, "UpdateServiceEnvVars")
}

func TestEnsureOrgInMint_AddsNewOrg(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":  "existing-org",
			"ROLE_APP_IDS":  `{"coder":"100"}`,
			"ALLOWED_ROLES": "coder",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "new-org")
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "UpdateServiceEnvVars")
	assert.NotContains(t, fake.calls, "WaitForOperation")

	require.NotNil(t, fake.lastUpdateServiceEnvVars)
	assert.Contains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "new-org")
	assert.Contains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "existing-org")

	var roleAppIDs map[string]string
	require.NoError(t, json.Unmarshal([]byte(fake.lastUpdateServiceEnvVars["ROLE_APP_IDS"]), &roleAppIDs))
	assert.Equal(t, "100", roleAppIDs["coder"])
}

func TestEnsureOrgInMint_FunctionNotFound(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetFunction"] = fmt.Errorf("function not found")

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "acme-corp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting mint function")
}

func TestEnsureOrgInMint_URLMismatch(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://different-mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "acme-corp",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "acme-corp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint URL mismatch")
}

func TestEnsureOrgInMint_OrgAlreadyEnrolled_NoRoleChange(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":  "acme-corp",
			"ROLE_APP_IDS":  `{"coder":"111"}`,
			"ALLOWED_ROLES": "coder",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "acme-corp")
	require.NoError(t, err)
	assert.NotContains(t, fake.calls, "UpdateServiceEnvVars")
}

func TestEnsureOrgInMint_UpdateFails(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "existing-org",
			"ROLE_APP_IDS": `{"coder":"100"}`,
		},
	}
	fake.errs["UpdateServiceEnvVars"] = fmt.Errorf("permission denied")

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "new-org")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating mint env vars")
}

func TestEnsureOrgInMint_PartialFailureSurfacesRevision(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "existing-org",
			"ROLE_APP_IDS": `{"coder":"100"}`,
		},
	}
	fake.errs["UpdateServiceEnvVars"] = fmt.Errorf("traffic routing failed")
	fake.updateServiceRevision = "fullsend-mint-00115-abc"

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "new-org")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revision fullsend-mint-00115-abc created but traffic routing may have failed")
	assert.Contains(t, err.Error(), "traffic routing failed")
}

func TestEnsureOrgInMint_EmptyRoleAppIDs(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "existing-org",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "new-org")
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "UpdateServiceEnvVars")
	assert.Contains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "new-org")
}

func TestEnsureOrgInMint_NilReturn(t *testing.T) {
	fake := newFakeGCFClient()
	// functionInfo defaults to nil, simulating a 404 (nil, nil) return.

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "acme-corp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in project")
}

func TestEnsureOrgInMint_LowercasesOrg(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":  "existing-org",
			"ROLE_APP_IDS":  `{"coder":"100"}`,
			"ALLOWED_ROLES": "coder",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "AcmeCorp")
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "UpdateServiceEnvVars")
	assert.Contains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "acmecorp")
	assert.NotContains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "AcmeCorp")
}

func TestEnsureOrgInMint_DefaultsAllowedWorkflowFiles(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":  "existing-org",
			"ROLE_APP_IDS":  `{"coder":"100"}`,
			"ALLOWED_ROLES": "coder",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "new-org")
	require.NoError(t, err)
	assert.Equal(t, "*", fake.lastUpdateServiceEnvVars["ALLOWED_WORKFLOW_FILES"])
}

func TestEnsureOrgInMint_PreservesExistingAllowedWorkflowFiles(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":           "existing-org",
			"ROLE_APP_IDS":           `{"coder":"100"}`,
			"ALLOWED_ROLES":          "coder",
			"ALLOWED_WORKFLOW_FILES": ".github/workflows/ci.yml",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "new-org")
	require.NoError(t, err)
	assert.Equal(t, ".github/workflows/ci.yml", fake.lastUpdateServiceEnvVars["ALLOWED_WORKFLOW_FILES"])
}

func TestEnsureOrgInMint_ReadsFromTrafficServingRevision(t *testing.T) {
	// When the service template has diverged from the traffic-serving
	// revision (e.g., template has empty ALLOWED_ORGS while the serving
	// revision has 20 orgs), EnsureOrgInMint should read from the
	// traffic-serving revision so the merge preserves existing orgs.
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		// Service template (via GetFunction) — stale/empty.
		EnvVars: map[string]string{
			"ALLOWED_ORGS":  "",
			"ROLE_APP_IDS":  `{}`,
			"ALLOWED_ROLES": "",
		},
	}
	// Traffic-serving revision has the real data.
	fake.trafficEnvVars = map[string]string{
		"ALLOWED_ORGS":  "org-a,org-b,org-c",
		"ROLE_APP_IDS":  `{"coder":"100"}`,
		"ALLOWED_ROLES": "coder",
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "new-org")
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "GetServiceTrafficEnvVars")
	require.NotNil(t, fake.lastUpdateServiceEnvVars)

	// All existing orgs must be preserved, not clobbered.
	allowedOrgs := fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"]
	assert.Contains(t, allowedOrgs, "org-a")
	assert.Contains(t, allowedOrgs, "org-b")
	assert.Contains(t, allowedOrgs, "org-c")
	assert.Contains(t, allowedOrgs, "new-org")

	// Existing role app IDs must be preserved.
	var roleAppIDs map[string]string
	require.NoError(t, json.Unmarshal([]byte(fake.lastUpdateServiceEnvVars["ROLE_APP_IDS"]), &roleAppIDs))
	assert.Equal(t, "100", roleAppIDs["coder"])
}

func TestEnsureOrgInMint_TrafficEnvVarsError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI:     "https://mint.example.com",
		EnvVars: map[string]string{},
	}
	fake.errs["GetServiceTrafficEnvVars"] = fmt.Errorf("Cloud Run API unavailable")

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "new-org")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading traffic-serving env vars")
}

func TestMergeAllowedOrgs_EmptyExisting(t *testing.T) {
	// When existing ALLOWED_ORGS is empty (e.g., from a diverged template),
	// the merge must still preserve the desired orgs rather than silently
	// skipping.
	existing := map[string]string{"ALLOWED_ORGS": ""}
	desired := map[string]string{"ALLOWED_ORGS": "new-org"}
	mergeAllowedOrgs(existing, desired)
	assert.Equal(t, "new-org", desired["ALLOWED_ORGS"])
}

func TestMergeAllowedOrgs_BothEmpty(t *testing.T) {
	existing := map[string]string{"ALLOWED_ORGS": ""}
	desired := map[string]string{"ALLOWED_ORGS": ""}
	mergeAllowedOrgs(existing, desired)
	assert.Equal(t, "", desired["ALLOWED_ORGS"])
}

func TestEnsureOrgInMint_ProceedsOnFirstEnrollment(t *testing.T) {
	// When ALLOWED_ORGS is empty and ROLE_APP_IDS is also empty (or has
	// only the enrolling org), this is a genuine first enrollment — proceed.
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI:     "https://mint.example.com",
		EnvVars: map[string]string{},
	}
	fake.trafficEnvVars = map[string]string{
		"ALLOWED_ORGS": "",
		"ROLE_APP_IDS": `{}`,
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "new-org")
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "UpdateServiceEnvVars")
	assert.Equal(t, "new-org", fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"])
}

func TestRegisterPerRepoWIF_AddsNewRepo(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI:     "https://mint.example.com",
		EnvVars: map[string]string{},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RegisterPerRepoWIF(context.Background(), "acme-corp/my-service")
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "UpdateServiceEnvVars")
	assert.Equal(t, "acme-corp/my-service", fake.lastUpdateServiceEnvVars["PER_REPO_WIF_REPOS"])
}

func TestRegisterPerRepoWIF_AppendsToExisting(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"PER_REPO_WIF_REPOS": "acme-corp/first-repo",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RegisterPerRepoWIF(context.Background(), "acme-corp/second-repo")
	require.NoError(t, err)
	assert.Equal(t, "acme-corp/first-repo,acme-corp/second-repo", fake.lastUpdateServiceEnvVars["PER_REPO_WIF_REPOS"])
}

func TestRegisterPerRepoWIF_Idempotent(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"PER_REPO_WIF_REPOS": "acme-corp/my-service",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RegisterPerRepoWIF(context.Background(), "acme-corp/my-service")
	require.NoError(t, err)
	assert.NotContains(t, fake.calls, "UpdateServiceEnvVars")
}

func TestRegisterPerRepoWIF_FunctionNotFound(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = nil

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RegisterPerRepoWIF(context.Background(), "acme-corp/my-service")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint function not found")
}

func TestRegisterPerRepoWIF_LowercasesRepo(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI:     "https://mint.example.com",
		EnvVars: map[string]string{},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RegisterPerRepoWIF(context.Background(), "Acme-Corp/My-Service")
	require.NoError(t, err)
	assert.Equal(t, "acme-corp/my-service", fake.lastUpdateServiceEnvVars["PER_REPO_WIF_REPOS"])
}

func TestRegisterPerRepoWIF_RejectsInvalidFormat(t *testing.T) {
	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, newFakeGCFClient())

	tests := []struct {
		name, repo string
	}{
		{"no slash", "just-a-name"},
		{"empty owner", "/repo"},
		{"empty repo", "owner/"},
		{"comma injection", "legit/repo,evil/repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.RegisterPerRepoWIF(context.Background(), tt.repo)
			require.Error(t, err)
		})
	}
}

func TestRegisterPerRepoWIF_NilEnvVars(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI:     "https://mint.example.com",
		EnvVars: nil,
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RegisterPerRepoWIF(context.Background(), "acme-corp/my-service")
	require.NoError(t, err)
	assert.Equal(t, "acme-corp/my-service", fake.lastUpdateServiceEnvVars["PER_REPO_WIF_REPOS"])
}

func TestRegisterPerRepoWIF_GetFunctionError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetFunction"] = fmt.Errorf("permission denied")

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RegisterPerRepoWIF(context.Background(), "acme-corp/my-service")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting mint function")
}

func TestRegisterPerRepoWIF_PartialFailureSurfacesRevision(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI:     "https://mint.example.com",
		EnvVars: map[string]string{},
	}
	fake.errs["UpdateServiceEnvVars"] = fmt.Errorf("traffic routing failed")
	fake.updateServiceRevision = "fullsend-mint-00116-def"

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RegisterPerRepoWIF(context.Background(), "acme-corp/my-service")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revision fullsend-mint-00116-def created but traffic routing may have failed")
	assert.Contains(t, err.Error(), "traffic routing failed")
}

func TestRegisterPerRepoWIF_ReadsFromTrafficServingRevision(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		// Template has stale/empty data.
		EnvVars: map[string]string{},
	}
	// Traffic-serving revision has existing repos.
	fake.trafficEnvVars = map[string]string{
		"PER_REPO_WIF_REPOS": "existing-org/existing-repo",
		"ALLOWED_ORGS":       "existing-org",
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RegisterPerRepoWIF(context.Background(), "new-org/new-repo")
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "GetServiceTrafficEnvVars")
	// Must preserve existing repos from traffic-serving revision.
	assert.Equal(t, "existing-org/existing-repo,new-org/new-repo", fake.lastUpdateServiceEnvVars["PER_REPO_WIF_REPOS"])
	// Must also preserve other env vars from traffic-serving revision.
	assert.Equal(t, "existing-org", fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"])
}

// --- RemoveOrgFromMint tests ---

func TestRemoveOrgFromMint_RemovesOrgOnly(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":  "acme,other-org",
			"ROLE_APP_IDS":  `{"coder":"111","triage":"222"}`,
			"ALLOWED_ROLES": "coder,triage",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveOrgFromMint(context.Background(), "acme")
	require.NoError(t, err)

	assert.Contains(t, fake.calls, "UpdateServiceEnvVars")
	assert.NotContains(t, fake.calls, "WaitForOperation")

	// acme should be removed from ALLOWED_ORGS.
	assert.Equal(t, "other-org", fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"])

	// ROLE_APP_IDS are shared and unchanged.
	var roleAppIDs map[string]string
	require.NoError(t, json.Unmarshal([]byte(fake.lastUpdateServiceEnvVars["ROLE_APP_IDS"]), &roleAppIDs))
	assert.Equal(t, "111", roleAppIDs["coder"])
	assert.Equal(t, "222", roleAppIDs["triage"])
	assert.Equal(t, "coder,triage", fake.lastUpdateServiceEnvVars["ALLOWED_ROLES"])
}

func TestRemoveOrgFromMint_FunctionNotFound(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = nil

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveOrgFromMint(context.Background(), "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRemoveOrgFromMint_GetFunctionError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetFunction"] = fmt.Errorf("permission denied")

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveOrgFromMint(context.Background(), "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting mint function")
}

func TestRemoveOrgFromMint_LowercasesOrg(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "acme",
			"ROLE_APP_IDS": `{"coder":"111"}`,
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveOrgFromMint(context.Background(), "ACME")
	require.NoError(t, err)

	assert.Equal(t, "", fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"])
}

func TestRemoveOrgFromMint_ReadsFromTrafficServingRevision(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		// Template has stale/empty data.
		EnvVars: map[string]string{},
	}
	// Traffic-serving revision has the real data.
	fake.trafficEnvVars = map[string]string{
		"ALLOWED_ORGS":  "acme,keep-org,remove-org",
		"ROLE_APP_IDS":  `{"coder":"111"}`,
		"ALLOWED_ROLES": "coder",
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveOrgFromMint(context.Background(), "remove-org")
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "GetServiceTrafficEnvVars")

	// Remaining orgs must be preserved from traffic-serving revision.
	assert.Contains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "acme")
	assert.Contains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "keep-org")
	assert.NotContains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "remove-org")

	var roleAppIDs map[string]string
	require.NoError(t, json.Unmarshal([]byte(fake.lastUpdateServiceEnvVars["ROLE_APP_IDS"]), &roleAppIDs))
	assert.Equal(t, "111", roleAppIDs["coder"])
}

func TestRemoveOrgFromMint_UpdateFails(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "acme",
			"ROLE_APP_IDS": `{"coder":"111"}`,
		},
	}
	fake.errs["UpdateServiceEnvVars"] = fmt.Errorf("permission denied")

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveOrgFromMint(context.Background(), "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "removing org from mint env vars")
}

func TestRemoveOrgFromMint_PartialFailureSurfacesRevision(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS": "acme",
			"ROLE_APP_IDS": `{"coder":"111"}`,
		},
	}
	fake.errs["UpdateServiceEnvVars"] = fmt.Errorf("traffic routing failed")
	fake.updateServiceRevision = "fullsend-mint-00117-ghi"

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveOrgFromMint(context.Background(), "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revision fullsend-mint-00117-ghi created but traffic routing may have failed")
	assert.Contains(t, err.Error(), "traffic routing failed")
}

// --- RemoveRepoFromMint tests ---

func TestRemoveRepoFromMint_RemovesRepo(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"PER_REPO_WIF_REPOS": "acme/first,acme/second",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveRepoFromMint(context.Background(), "acme/first")
	require.NoError(t, err)

	assert.Contains(t, fake.calls, "UpdateServiceEnvVars")
	assert.Equal(t, "acme/second", fake.lastUpdateServiceEnvVars["PER_REPO_WIF_REPOS"])
}

func TestRemoveRepoFromMint_LastRepo(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"PER_REPO_WIF_REPOS": "acme/only",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveRepoFromMint(context.Background(), "acme/only")
	require.NoError(t, err)

	assert.Equal(t, "", fake.lastUpdateServiceEnvVars["PER_REPO_WIF_REPOS"])
}

func TestRemoveRepoFromMint_FunctionNotFound(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = nil

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveRepoFromMint(context.Background(), "acme/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint function not found")
}

func TestRemoveRepoFromMint_ReadsFromTrafficServingRevision(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		// Template has stale/empty data.
		EnvVars: map[string]string{},
	}
	// Traffic-serving revision has the real data.
	fake.trafficEnvVars = map[string]string{
		"PER_REPO_WIF_REPOS": "acme/keep-repo,acme/remove-repo",
		"ALLOWED_ORGS":       "acme",
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveRepoFromMint(context.Background(), "acme/remove-repo")
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "GetServiceTrafficEnvVars")
	assert.Equal(t, "acme/keep-repo", fake.lastUpdateServiceEnvVars["PER_REPO_WIF_REPOS"])
	// Must preserve other env vars from traffic-serving revision.
	assert.Equal(t, "acme", fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"])
}

func TestRemoveRepoFromMint_LowercasesRepo(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"PER_REPO_WIF_REPOS": "acme/widget",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveRepoFromMint(context.Background(), "Acme/Widget")
	require.NoError(t, err)

	assert.Equal(t, "", fake.lastUpdateServiceEnvVars["PER_REPO_WIF_REPOS"])
}

func TestRemoveRepoFromMint_PartialFailureSurfacesRevision(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"PER_REPO_WIF_REPOS": "acme/first,acme/second",
		},
	}
	fake.errs["UpdateServiceEnvVars"] = fmt.Errorf("traffic routing failed")
	fake.updateServiceRevision = "fullsend-mint-00118-jkl"

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveRepoFromMint(context.Background(), "acme/first")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revision fullsend-mint-00118-jkl created but traffic routing may have failed")
	assert.Contains(t, err.Error(), "traffic routing failed")
}

// --- DisableWIFProvider tests ---

func TestDisableWIFProvider_Success(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{ProjectID: "proj1"}, fake)
	err := p.DisableWIFProvider(context.Background(), "gh-acme-widget")
	require.NoError(t, err)

	assert.Contains(t, fake.calls, "GetProjectNumber")
	assert.Contains(t, fake.calls, "DisableWIFProvider")
}

func TestDisableWIFProvider_GetProjectNumberError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetProjectNumber"] = fmt.Errorf("permission denied")

	p := NewProvisioner(Config{ProjectID: "proj1"}, fake)
	err := p.DisableWIFProvider(context.Background(), "gh-acme-widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting project number")
}

// --- DeleteWIFProvider tests ---

func TestDeleteWIFProvider_Success(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{ProjectID: "proj1"}, fake)
	err := p.DeleteWIFProvider(context.Background(), "gh-acme-widget")
	require.NoError(t, err)

	assert.Contains(t, fake.calls, "GetProjectNumber")
	assert.Contains(t, fake.calls, "DeleteWIFProvider")
}

func TestDeleteWIFProvider_GetProjectNumberError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["GetProjectNumber"] = fmt.Errorf("permission denied")

	p := NewProvisioner(Config{ProjectID: "proj1"}, fake)
	err := p.DeleteWIFProvider(context.Background(), "gh-acme-widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting project number")
}

// --- ValidateProjectID and ValidateRegion tests ---

func TestValidateProjectID(t *testing.T) {
	assert.True(t, ValidateProjectID("my-project-id"))
	assert.True(t, ValidateProjectID("project-123456"))
	assert.False(t, ValidateProjectID("BAD"))
	assert.False(t, ValidateProjectID(""))
	assert.False(t, ValidateProjectID("ab")) // too short
}

func TestValidateRegion(t *testing.T) {
	assert.True(t, ValidateRegion("us-central1"))
	assert.True(t, ValidateRegion("europe-west4"))
	assert.False(t, ValidateRegion("invalid"))
	assert.False(t, ValidateRegion(""))
}

// --- Cloud Run revision awareness tests ---

func TestProvisioner_GetServiceRevisionInfo(t *testing.T) {
	fake := newFakeGCFClient()
	fake.revisionInfo = &ServiceRevisionInfo{
		TrafficRevision:        "projects/p/locations/r/services/s/revisions/fullsend-mint-00114-fm9",
		TrafficRevisionShort:   "fullsend-mint-00114-fm9",
		TrafficAllocType:       "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION",
		TemplateMatchesTraffic: true,
	}

	p := newTestProvisioner(Config{
		ProjectID: "my-project",
		Region:    "us-central1",
	}, fake)

	info, err := p.GetServiceRevisionInfo(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "fullsend-mint-00114-fm9", info.TrafficRevisionShort)
	assert.Contains(t, fake.calls, "GetServiceRevisionInfo")
}

func TestProvisioner_GetServiceTrafficEnvVars(t *testing.T) {
	fake := newFakeGCFClient()
	fake.trafficEnvVars = map[string]string{
		"ALLOWED_ORGS": "acme",
		"ROLE_APP_IDS": `{"coder":"111"}`,
	}

	p := newTestProvisioner(Config{
		ProjectID: "my-project",
		Region:    "us-central1",
	}, fake)

	envVars, err := p.GetServiceTrafficEnvVars(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "acme", envVars["ALLOWED_ORGS"])
	assert.Contains(t, fake.calls, "GetServiceTrafficEnvVars")
}

func TestProvisioner_EnsureOrgInMint_PreservesInfraKeysFromTrafficRevision(t *testing.T) {
	// UpdateServiceEnvVars on main uses REVISION-pinned routing, so the
	// traffic-serving revision always contains the full env var set including
	// infrastructure keys. EnsureOrgInMint builds the updated env vars
	// entirely from the traffic-serving revision state.
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI:     "https://fullsend-mint-abc123.run.app",
		EnvVars: map[string]string{},
	}
	// Traffic revision has both infra keys and org data.
	fake.trafficEnvVars = map[string]string{
		"GCP_PROJECT_NUMBER":     "123456789",
		"WIF_POOL_NAME":          "fullsend-pool",
		"WIF_PROVIDER_NAME":      "github-oidc",
		"OIDC_AUDIENCE":          "fullsend-mint",
		"FULLSEND_SOURCE_HASH":   "abc123",
		"ALLOWED_ORGS":           "existing-org",
		"ROLE_APP_IDS":           `{"coder":"99999"}`,
		"ALLOWED_WORKFLOW_FILES": "*",
	}

	p := newTestProvisioner(Config{
		ProjectID:  "my-project",
		GitHubOrgs: []string{"new-org"},
	}, fake)

	err := p.EnsureOrgInMint(context.Background(), "https://fullsend-mint-abc123.run.app", "new-org")
	require.NoError(t, err)

	require.NotNil(t, fake.lastUpdateServiceEnvVars)

	// Infrastructure keys from traffic revision should be preserved.
	assert.Equal(t, "123456789", fake.lastUpdateServiceEnvVars["GCP_PROJECT_NUMBER"])
	assert.Equal(t, "fullsend-pool", fake.lastUpdateServiceEnvVars["WIF_POOL_NAME"])
	assert.Equal(t, "github-oidc", fake.lastUpdateServiceEnvVars["WIF_PROVIDER_NAME"])
	assert.Equal(t, "fullsend-mint", fake.lastUpdateServiceEnvVars["OIDC_AUDIENCE"])
	assert.Equal(t, "abc123", fake.lastUpdateServiceEnvVars["FULLSEND_SOURCE_HASH"])

	// Org-relevant keys should include both existing and new org.
	assert.Contains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "existing-org")
	assert.Contains(t, fake.lastUpdateServiceEnvVars["ALLOWED_ORGS"], "new-org")
}

func TestMergeRoleAppIDsJSON_EmptyExistingPreservesDesired(t *testing.T) {
	merged, err := mergeRoleAppIDsJSON("", map[string]string{"coder": "111"})
	require.NoError(t, err)
	assert.Equal(t, `{"coder":"111"}`, merged)
}

func TestMergeRoleAppIDsJSON_MergesRoleOnlyAndIgnoresLegacy(t *testing.T) {
	existing := `{"acme/coder":"999","coder":"100","triage":"200"}`
	merged, err := mergeRoleAppIDsJSON(existing, map[string]string{"coder": "300", "review": "400"})
	require.NoError(t, err)

	var ids map[string]string
	require.NoError(t, json.Unmarshal([]byte(merged), &ids))
	assert.Equal(t, "300", ids["coder"])
	assert.Equal(t, "200", ids["triage"])
	assert.Equal(t, "400", ids["review"])
	assert.Equal(t, "999", ids["acme/coder"])
}

func TestDeriveAllowedRoles_IgnoresLegacyOrgScopedKeys(t *testing.T) {
	roles := deriveAllowedRoles(`{"acme/coder":"1","coder":"2","triage":"3"}`)
	assert.Equal(t, "coder,triage", roles)
}

func TestDeriveAllowedRoles_InvalidJSON(t *testing.T) {
	assert.Equal(t, "", deriveAllowedRoles("{bad"))
}

func TestDeriveAllowedRoles_LegacyOnlyKeys(t *testing.T) {
	assert.Equal(t, "", deriveAllowedRoles(`{"acme/coder":"100"}`))
}

func TestMergeRoleAppIDsJSON_InvalidJSON(t *testing.T) {
	_, err := mergeRoleAppIDsJSON("{bad", map[string]string{"coder": "1"})
	require.Error(t, err)
}

func TestMarshalRoleAppIDs_Empty(t *testing.T) {
	raw, err := marshalRoleAppIDs(nil)
	require.NoError(t, err)
	assert.Equal(t, "{}", raw)
}

func TestMarshalRoleAppIDs_SortsKeys(t *testing.T) {
	raw, err := marshalRoleAppIDs(map[string]string{"triage": "2", "coder": "1"})
	require.NoError(t, err)
	assert.Equal(t, `{"coder":"1","triage":"2"}`, raw)
}

func TestEnsureOrgInMint_DerivesAllowedRolesWhenEmpty(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
	}
	fake.trafficEnvVars = map[string]string{
		"ALLOWED_ORGS": "",
		"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.EnsureOrgInMint(context.Background(), "https://mint.example.com", "new-org")
	require.NoError(t, err)
	assert.Equal(t, "coder,triage", fake.lastUpdateServiceEnvVars["ALLOWED_ROLES"])
}

func TestEnsureOrgInWIFCondition_AddsOrgAndStripsPlaceholder(t *testing.T) {
	fake := NewFakeGCFClient(
		WithFakeWIFProvider(&WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner in ['" + PlaceholderOrg + "']",
		}),
	)
	p := NewProvisioner(Config{
		ProjectID:   "proj1",
		Region:      "us-central1",
		WIFPoolName: "fullsend-pool",
		WIFProvider: "github-oidc",
	}, fake)

	err := p.EnsureOrgInWIFCondition(context.Background(), "Acme")
	require.NoError(t, err)
	assert.Contains(t, fake.(*fakeGCFClient).calls, "UpdateWIFProvider")
	assert.Contains(t, fake.(*fakeGCFClient).lastWIFProviderConfig.AttributeCondition, "'acme'")
	assert.NotContains(t, fake.(*fakeGCFClient).lastWIFProviderConfig.AttributeCondition, PlaceholderOrg)
}

func TestEnsureOrgInWIFCondition_NoOpWhenAlreadyPresent(t *testing.T) {
	condition := "assertion.repository_owner == 'acme'"
	fake := NewFakeGCFClient(WithFakeWIFProvider(&WIFProviderInfo{AttributeCondition: condition}))
	p := NewProvisioner(Config{
		ProjectID:   "proj1",
		Region:      "us-central1",
		WIFPoolName: "fullsend-pool",
		WIFProvider: "github-oidc",
	}, fake)

	err := p.EnsureOrgInWIFCondition(context.Background(), "acme")
	require.NoError(t, err)
	assert.NotContains(t, fake.(*fakeGCFClient).calls, "UpdateWIFProvider")
}

func TestRemoveOrgFromWIFCondition_RemovesOrgAndAddsPlaceholder(t *testing.T) {
	fake := NewFakeGCFClient(WithFakeWIFProvider(&WIFProviderInfo{
		AttributeCondition: "assertion.repository_owner in ['acme', 'other']",
	}))
	p := NewProvisioner(Config{
		ProjectID:   "proj1",
		Region:      "us-central1",
		WIFPoolName: "fullsend-pool",
		WIFProvider: "github-oidc",
	}, fake)

	err := p.RemoveOrgFromWIFCondition(context.Background(), "acme")
	require.NoError(t, err)
	assert.Contains(t, fake.(*fakeGCFClient).calls, "UpdateWIFProvider")
	assert.Contains(t, fake.(*fakeGCFClient).lastWIFProviderConfig.AttributeCondition, "'other'")
	assert.NotContains(t, fake.(*fakeGCFClient).lastWIFProviderConfig.AttributeCondition, "'acme'")
}

func TestRemoveOrgFromWIFCondition_NoOpWhenOrgAbsent(t *testing.T) {
	fake := NewFakeGCFClient(WithFakeWIFProvider(&WIFProviderInfo{
		AttributeCondition: "assertion.repository_owner in ['other']",
	}))
	p := NewProvisioner(Config{
		ProjectID:   "proj1",
		Region:      "us-central1",
		WIFPoolName: "fullsend-pool",
		WIFProvider: "github-oidc",
	}, fake)

	err := p.RemoveOrgFromWIFCondition(context.Background(), "acme")
	require.NoError(t, err)
	assert.NotContains(t, fake.(*fakeGCFClient).calls, "UpdateWIFProvider")
}

// --- Role management tests ---

func TestRemoveRoleFromAppIDsJSON(t *testing.T) {
	t.Parallel()
	out, err := removeRoleFromAppIDsJSON(`{"coder":"1","review":"2","acme/coder":"9"}`, "coder")
	require.NoError(t, err)
	var m map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	assert.Equal(t, map[string]string{"review": "2", "acme/coder": "9"}, m)
}

func TestAddRoleToMint_MergesRoleAppIDs(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ALLOWED_ORGS":  "acme-corp",
			"ROLE_APP_IDS":  `{"coder":"100"}`,
			"ALLOWED_ROLES": "coder",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.AddRoleToMint(context.Background(), "review", "200")
	require.NoError(t, err)

	require.NotNil(t, fake.lastUpdateServiceEnvVars)
	var roleAppIDs map[string]string
	require.NoError(t, json.Unmarshal([]byte(fake.lastUpdateServiceEnvVars["ROLE_APP_IDS"]), &roleAppIDs))
	assert.Equal(t, "100", roleAppIDs["coder"])
	assert.Equal(t, "200", roleAppIDs["review"])
	assert.Equal(t, "coder,review", fake.lastUpdateServiceEnvVars["ALLOWED_ROLES"])
}

func TestAddRoleToMint_MissingProjectID(t *testing.T) {
	p := NewProvisioner(Config{}, newFakeGCFClient())
	err := p.AddRoleToMint(context.Background(), "coder", "123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP project ID is required")
}

func TestRemoveRoleFromMint_PrunesRoleAppIDs(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ROLE_APP_IDS":  `{"coder":"100","review":"200"}`,
			"ALLOWED_ROLES": "coder,review",
		},
	}

	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveRoleFromMint(context.Background(), "review")
	require.NoError(t, err)

	require.NotNil(t, fake.lastUpdateServiceEnvVars)
	var roleAppIDs map[string]string
	require.NoError(t, json.Unmarshal([]byte(fake.lastUpdateServiceEnvVars["ROLE_APP_IDS"]), &roleAppIDs))
	assert.Equal(t, map[string]string{"coder": "100"}, roleAppIDs)
	assert.Equal(t, "coder", fake.lastUpdateServiceEnvVars["ALLOWED_ROLES"])
}

func TestDeleteAgentPEM(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{ProjectID: "proj1"}, fake)
	err := p.DeleteAgentPEM(context.Background(), "coder")
	require.NoError(t, err)
	assert.Contains(t, fake.calls, "DeleteSecret")
}

func TestDeleteAgentPEM_FixRoleUsesCoderSecret(t *testing.T) {
	fake := newFakeGCFClient()
	p := NewProvisioner(Config{ProjectID: "proj1"}, fake)
	err := p.DeleteAgentPEM(context.Background(), "fix")
	require.NoError(t, err)
	assert.Equal(t, []string{"fullsend-coder-app-pem"}, fake.deletedSecretIDs)
}

func TestDeleteAgentPEM_MissingProjectID(t *testing.T) {
	p := NewProvisioner(Config{}, newFakeGCFClient())
	err := p.DeleteAgentPEM(context.Background(), "coder")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP project ID is required")
}

func TestRemoveRoleFromMint_MissingProjectID(t *testing.T) {
	p := NewProvisioner(Config{}, newFakeGCFClient())
	err := p.RemoveRoleFromMint(context.Background(), "coder")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP project ID is required")
}

func TestAddRoleToMint_InvalidRole(t *testing.T) {
	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, newFakeGCFClient())
	err := p.AddRoleToMint(context.Background(), "BAD", "123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role name")
}

func TestAddRoleToMint_EmptyAppID(t *testing.T) {
	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, newFakeGCFClient())
	err := p.AddRoleToMint(context.Background(), "coder", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "app ID is required")
}

func TestAddRoleToMint_MalformedExistingJSON(t *testing.T) {
	fake := newFakeGCFClient()
	fake.trafficEnvVars = map[string]string{"ROLE_APP_IDS": "not-json"}
	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.AddRoleToMint(context.Background(), "coder", "123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "merging ROLE_APP_IDS")
}

func TestAddRoleToMint_UpdateEnvVarsError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI:     "https://mint.example.com",
		EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
	}
	fake.errs["UpdateServiceEnvVars"] = fmt.Errorf("permission denied")
	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.AddRoleToMint(context.Background(), "review", "200")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating mint env vars")
}

func TestRemoveRoleFromMint_InvalidRole(t *testing.T) {
	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, newFakeGCFClient())
	err := p.RemoveRoleFromMint(context.Background(), "BAD")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role name")
}

func TestRemoveRoleFromMint_MalformedExistingJSON(t *testing.T) {
	fake := newFakeGCFClient()
	fake.trafficEnvVars = map[string]string{"ROLE_APP_IDS": "not-json"}
	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveRoleFromMint(context.Background(), "coder")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pruning ROLE_APP_IDS")
}

func TestDeleteAgentPEM_InvalidRole(t *testing.T) {
	p := NewProvisioner(Config{ProjectID: "proj1"}, newFakeGCFClient())
	err := p.DeleteAgentPEM(context.Background(), "BAD")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role name")
}

func TestDeleteAgentPEM_DeleteFails(t *testing.T) {
	fake := newFakeGCFClient()
	fake.errs["DeleteSecret"] = fmt.Errorf("permission denied")
	p := NewProvisioner(Config{ProjectID: "proj1"}, fake)
	err := p.DeleteAgentPEM(context.Background(), "coder")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting secret")
}

func TestAddRoleToMint_RevisionRoutingFails(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI:     "https://mint.example.com",
		EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
	}
	fake.updateServiceRevision = "fullsend-mint-00099"
	fake.errs["UpdateServiceEnvVars"] = fmt.Errorf("routing failed")
	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.AddRoleToMint(context.Background(), "review", "200")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traffic routing may have failed")
	assert.Contains(t, err.Error(), "fullsend-mint-00099")
}

func TestRemoveRoleFromMint_UpdateEnvVarsError(t *testing.T) {
	fake := newFakeGCFClient()
	fake.functionInfo = &FunctionInfo{
		URI: "https://mint.example.com",
		EnvVars: map[string]string{
			"ROLE_APP_IDS":  `{"coder":"100","review":"200"}`,
			"ALLOWED_ROLES": "coder,review",
		},
	}
	fake.errs["UpdateServiceEnvVars"] = fmt.Errorf("permission denied")
	p := NewProvisioner(Config{ProjectID: "proj1", Region: "us-central1"}, fake)
	err := p.RemoveRoleFromMint(context.Background(), "review")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "updating mint env vars")
}
