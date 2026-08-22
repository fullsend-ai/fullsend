package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStageMintDeployArgs_WithAppSet(t *testing.T) {
	cfg := stageMintConfig{
		pemDir:            "/tmp/pems",
		allowedOrgs:       "",
		perRepoWIFRepos:   "halfsend/test-repo-01",
		workflowHostRepos: "halfsend/test-repo-01,halfsend/test-repo-02",
		appSet:            "fullsend-test",
	}

	args := StageMintDeployArgs(cfg)

	assert.Contains(t, args, "--platform")
	assert.Contains(t, args, "cloudflare")
	assert.Contains(t, args, "--worker-name")
	assert.Contains(t, args, StageMintWorkerName)
	assert.Contains(t, args, "--custom-domain")
	assert.Contains(t, args, StageMintCustomDomain)
	assert.Contains(t, args, "--pem-dir")
	assert.Contains(t, args, "--app-set")
	assert.Contains(t, args, "--allowed-orgs")
	assert.Contains(t, args, "--per-repo-wif-repos")
	assert.Contains(t, args, "--workflow-host-repos")

	// No --preview flag for durable deploys.
	assert.NotContains(t, args, "--preview")

	for i, a := range args {
		if a == "--allowed-orgs" {
			require.Less(t, i+1, len(args), "--allowed-orgs must have a value")
			assert.Equal(t, "", args[i+1], "--allowed-orgs should be explicit empty for per-repo mode")
			break
		}
	}

	for i, a := range args {
		if a == "--custom-domain" {
			require.Less(t, i+1, len(args), "--custom-domain must have a value")
			assert.Equal(t, StageMintCustomDomain, args[i+1])
			break
		}
	}
}

func TestStageMintDeployArgs_WithoutAppSet(t *testing.T) {
	cfg := stageMintConfig{
		pemDir:            "/tmp/pems",
		allowedOrgs:       "",
		perRepoWIFRepos:   "halfsend/test-repo-01",
		workflowHostRepos: "halfsend/test-repo-01",
	}

	args := StageMintDeployArgs(cfg)

	assert.NotContains(t, args, "--app-set")
	assert.Contains(t, args, "--custom-domain")
	assert.NotContains(t, args, "--preview")
}

func TestNewStageMintDriver_FailsEarly_NoPEMDir(t *testing.T) {
	_, err := newStageMintDriver(nil, "tok", "/bin/fullsend", "", t.Logf, stageMintConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PEMDir is required")
}

func TestNewStageMintDriver_FailsEarly_ReadDirError(t *testing.T) {
	_, err := newStageMintDriver(nil, "tok", "/bin/fullsend", "", t.Logf, stageMintConfig{
		pemDir: "/nonexistent/path/that/does/not/exist",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading PEM dir")
}

func TestNewStageMintDriver_FailsEarly_EmptyPEMDir(t *testing.T) {
	dir := t.TempDir()
	_, err := newStageMintDriver(nil, "tok", "/bin/fullsend", "", t.Logf, stageMintConfig{
		pemDir: dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no .pem files")
}

func TestNewStageMintDriver_OK(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fullsend.pem"), []byte("pem"), 0600))

	d, err := newStageMintDriver(nil, "tok", "/bin/fullsend", "", t.Logf, stageMintConfig{
		pemDir:            dir,
		allowedOrgs:       "",
		perRepoWIFRepos:   "halfsend/test-repo-01",
		workflowHostRepos: "halfsend/test-repo-01",
		appSet:            "fullsend-test",
	})
	require.NoError(t, err)
	require.NotNil(t, d)
}

// newTestStageMintDriver creates a stageMintMintDriver with a mock CLI
// runner for unit testing.
func newTestStageMintDriver(cliRunner CLIRunnerFunc) *stageMintMintDriver {
	return &stageMintMintDriver{
		token:     "tok",
		binary:    "/bin/fullsend",
		logf:      func(string, ...any) {},
		cfg:       stageMintConfig{},
		cliRunner: cliRunner,
	}
}

func TestStageMintInstall_Success(t *testing.T) {
	d := newTestStageMintDriver(func(_, _ string, _ ...string) (string, error) {
		return "Worker deployed at https://stage-mint.fullsend-ai.workers.dev", nil
	})

	mintURL, err := d.Install(context.Background(), StageOrg)
	require.NoError(t, err)
	// The URL should be the custom domain, not the workers.dev URL.
	assert.Equal(t, StageMintURL, mintURL)
}

func TestStageMintInstall_DeployFailure(t *testing.T) {
	d := newTestStageMintDriver(func(_, _ string, _ ...string) (string, error) {
		return "", fmt.Errorf("deploy exploded")
	})

	mintURL, err := d.Install(context.Background(), StageOrg)
	require.Error(t, err)
	assert.Empty(t, mintURL)
	assert.Contains(t, err.Error(), "stage mint deploy")
}

func TestStageMintTeardown_IsNoOp(t *testing.T) {
	called := false
	d := newTestStageMintDriver(func(_, _ string, _ ...string) (string, error) {
		called = true
		return "", nil
	})

	err := d.Teardown(context.Background())
	require.NoError(t, err)
	assert.False(t, called, "CLI should not be called — teardown is a no-op for durable mint")
}

func TestStageMintDriver_ImplementsMintDriver(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fullsend.pem"), []byte("pem"), 0600))

	d, err := newStageMintDriver(nil, "tok", "/bin/fullsend", "", t.Logf, stageMintConfig{
		pemDir:            dir,
		allowedOrgs:       "",
		perRepoWIFRepos:   "halfsend/test-repo-01",
		workflowHostRepos: "halfsend/test-repo-01",
	})
	require.NoError(t, err)

	var _ mintDriver = d
}

// --- buildStageMintDriver tests ---

func TestBuildStageMintDriver_HappyPath(t *testing.T) {
	mint := &testCFMintMintDriver{
		installMintURL: StageMintURL,
	}

	d, err := buildStageMintDriver(mint, nil, "tok", "/bin/fullsend", "proj", 3, t.Logf)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, 3, d.Capacity())
}

func TestBuildStageMintDriver_InstallFails(t *testing.T) {
	mint := &testCFMintMintDriver{
		installErr: fmt.Errorf("deploy boom"),
	}

	_, err := buildStageMintDriver(mint, nil, "tok", "/bin/fullsend", "proj", 3, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stage cfmint factory: deploying mint")
	assert.Contains(t, err.Error(), "deploy boom")
}

func TestBuildStageMintDriver_InvalidPoolSize(t *testing.T) {
	mint := &testCFMintMintDriver{
		installMintURL: StageMintURL,
	}

	_, err := buildStageMintDriver(mint, nil, "tok", "/bin/fullsend", "proj", 0, t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "capacity must be positive")
}

// --- NewRepoPoolCFMintStage factory tests ---

func TestNewRepoPoolCFMintStage_NoPEMs_FailsEarly(t *testing.T) {
	for _, envVar := range cfmintPEMRoleEnvVars {
		t.Setenv(envVar, "")
	}

	_, err := NewRepoPoolCFMintStage("any-org", nil, "tok", "/bin/fullsend", "proj", t.Logf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PEMDir is required")
}

func TestStageConstants(t *testing.T) {
	assert.Equal(t, "stage-mint.fullsend.sh", StageMintCustomDomain)
	assert.Equal(t, "https://stage-mint.fullsend.sh", StageMintURL)
	assert.Equal(t, "stage-mint", StageMintWorkerName)
	assert.Equal(t, "halfsend", StageOrg)
}
