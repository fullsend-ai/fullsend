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
	assert.Contains(t, args, "--per-repo-wif-repos")
	assert.Contains(t, args, "--workflow-host-repos")
	assert.NotContains(t, args, "--allowed-orgs", "per-org mint enrollment was removed")

	// No --preview flag for durable deploys.
	assert.NotContains(t, args, "--preview")

	for i, a := range args {
		if a == "--custom-domain" {
			require.Less(t, i+1, len(args), "--custom-domain must have a value")
			assert.Equal(t, StageMintCustomDomain, args[i+1])
			break
		}
	}
}

func TestStageMintDeployArgs_AllowedWorkflowFiles(t *testing.T) {
	cfg := stageMintConfig{
		pemDir:            "/tmp/pems",
		perRepoWIFRepos:   "halfsend/repo-01",
		workflowHostRepos: "halfsend/repo-01",
	}
	args := StageMintDeployArgs(cfg)

	for i, a := range args {
		if a == "--allowed-workflow-files" {
			require.Less(t, i+1, len(args), "--allowed-workflow-files must have a value")
			assert.Equal(t, "*", args[i+1])
			return
		}
	}
	t.Fatal("--allowed-workflow-files flag not found in args")
}

func TestStageMintDeployArgs_WithoutAppSet(t *testing.T) {
	cfg := stageMintConfig{
		pemDir:            "/tmp/pems",
		perRepoWIFRepos:   "halfsend/test-repo-01",
		workflowHostRepos: "halfsend/test-repo-01",
	}

	args := StageMintDeployArgs(cfg)

	assert.NotContains(t, args, "--app-set")
	assert.Contains(t, args, "--custom-domain")
	assert.NotContains(t, args, "--preview")
	assert.NotContains(t, args, "--status-auth", "no status group, no GitHub status auth")
}

func TestStageMintDeployArgs_StatusGitHubGroup(t *testing.T) {
	cfg := stageMintConfig{
		pemDir:            "/tmp/pems",
		perRepoWIFRepos:   "halfsend/test-repo-01",
		workflowHostRepos: "halfsend/test-repo-01",
		statusGitHubGroup: mintStatusGitHubGroup(StageOrg),
	}

	args := StageMintDeployArgs(cfg)

	assert.Equal(t, []string{"--status-auth", "oidc,github", "--status-github-group", "halfsend/mint-status"},
		args[len(args)-4:])
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

	mintURL, err := d.Install(context.Background())
	require.NoError(t, err)
	// The URL should be the custom domain, not the workers.dev URL.
	assert.Equal(t, StageMintURL, mintURL)
}

func TestStageMintInstall_DeployFailure(t *testing.T) {
	d := newTestStageMintDriver(func(_, _ string, _ ...string) (string, error) {
		return "", fmt.Errorf("deploy exploded")
	})

	mintURL, err := d.Install(context.Background())
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
		perRepoWIFRepos:   "halfsend/test-repo-01",
		workflowHostRepos: "halfsend/test-repo-01",
	})
	require.NoError(t, err)

	var _ mintDriver = d
}

// --- NewRepoPoolCFMintStage factory tests ---

func TestNewRepoPoolCFMintStage_WrongLayout_RejectsEarly(t *testing.T) {
	_, err := NewRepoPoolCFMintStage(testPoolConfig(t, DevPoolLayout(2)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "single org")
	assert.Contains(t, err.Error(), "halfsend-01")

	_, err = NewRepoPoolCFMintStage(testPoolConfig(t, PoolLayout{}))
	require.ErrorContains(t, err, "stage cfmint factory")
}

func TestNewRepoPoolCFMintStage_NoPEMs_FailsEarly(t *testing.T) {
	for _, envVar := range cfmintPEMRoleEnvVars {
		t.Setenv(envVar, "")
	}

	_, err := NewRepoPoolCFMintStage(testPoolConfig(t, StagePoolLayout(2)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PEMDir is required")
}

func TestNewRepoPoolCFMintStage_UnsuffixedOrgPool(t *testing.T) {
	for _, envVar := range cfmintPEMRoleEnvVars {
		t.Setenv(envVar, "")
	}
	t.Setenv("TEST_FULLSEND_PEM", "fake-pem")
	t.Setenv(envTraceCollector, "false")
	stubWranglerPrep(t, nil)

	d, err := NewRepoPoolCFMintStage(testPoolConfig(t, StagePoolLayout(3)))
	require.NoError(t, err)
	assert.Equal(t, 3, d.Capacity())
	assert.Equal(t, []string{StageOrg}, d.(*composedDriver).alloc.layout.Orgs())
}

func TestStageSetupOpts(t *testing.T) {
	t.Setenv("PLAYBACK_RUNTIME", "dummy-playback")
	t.Setenv("BEHAVIOUR_CONFIG_PRESET", "preset.yaml")
	opts := stageSetupOpts()
	assert.False(t, opts.Vendor)
	assert.Equal(t, stageFullsendRef, opts.FullsendRef)
	assert.Equal(t, "dummy-playback", opts.Runtime)
	assert.Equal(t, "preset.yaml", opts.ConfigPreset)
}

func TestStageConstants(t *testing.T) {
	assert.Equal(t, "stage-mint.fullsend.sh", StageMintCustomDomain)
	assert.Equal(t, "https://stage-mint.fullsend.sh", StageMintURL)
	assert.Equal(t, "stage-mint", StageMintWorkerName)
	assert.Equal(t, "halfsend", StageOrg)
}
