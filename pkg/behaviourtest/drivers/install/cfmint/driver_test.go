package cfmint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
)

func TestWorkerName(t *testing.T) {
	assert.Equal(t, "bt-mint", WorkerName("bt"))
	assert.Equal(t, "e2e-mint", WorkerName("e2e"))
}

func TestParseMintURLFromOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "standard deploy output",
			output: "Deploying...\n✓ Worker deployed at https://bt-abc12345-bt-mint.fullsend-ai.workers.dev\nDone",
			want:   "https://bt-abc12345-bt-mint.fullsend-ai.workers.dev",
		},
		{
			name:   "durable deploy output",
			output: "✓ Worker deployed at https://bt-mint.fullsend-ai.workers.dev\n",
			want:   "https://bt-mint.fullsend-ai.workers.dev",
		},
		{
			name:   "no url in output",
			output: "Deploy completed without URL line",
			want:   "",
		},
		{
			name:   "trailing punctuation stripped",
			output: "✓ Worker deployed at https://bt-x-mint.sub.workers.dev.",
			want:   "https://bt-x-mint.sub.workers.dev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ParseMintURLFromOutput(tt.output))
		})
	}
}

func TestGeneratePreviewAlias(t *testing.T) {
	alias, err := GeneratePreviewAlias()
	require.NoError(t, err)

	// Format: bt-<8-hex-chars>
	assert.True(t, strings.HasPrefix(alias, "bt-"), "alias should start with bt-")
	assert.Len(t, alias, 11, "bt- + 8 hex chars = 11 chars")

	// Must be unique across calls.
	alias2, err := GeneratePreviewAlias()
	require.NoError(t, err)
	assert.NotEqual(t, alias, alias2, "sequential aliases should differ")
}

func TestNewDriver_FailsEarly_NoPEMDir(t *testing.T) {
	_, err := NewDriver(nil, "tok", "/bin/fullsend", "", t.Logf, Config{
		SuiteName: "bt",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PEMDir is required")
}

func TestNewDriver_FailsEarly_EmptyPEMDir(t *testing.T) {
	dir := t.TempDir()
	_, err := NewDriver(nil, "tok", "/bin/fullsend", "", t.Logf, Config{
		PEMDir:    dir,
		SuiteName: "bt",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no .pem files")
}

func TestNewDriver_FailsEarly_NoSuiteName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fullsend.pem"), []byte("pem"), 0600))

	_, err := NewDriver(nil, "tok", "/bin/fullsend", "", t.Logf, Config{
		PEMDir: dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SuiteName is required")
}

func TestNewDriver_OK(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fullsend.pem"), []byte("pem"), 0600))

	d, err := NewDriver(nil, "tok", "/bin/fullsend", "", t.Logf, Config{
		PEMDir:            dir,
		SuiteName:         "bt",
		AllowedOrgs:       "",
		PerRepoWIFRepos:   "my-org/test-repo-01,my-org/test-repo-02",
		WorkflowHostRepos: "my-org/test-repo-01,my-org/test-repo-02",
		AppSet:            "fullsend-test",
	})
	require.NoError(t, err)
	require.NotNil(t, d)
}

func TestDeployArgs_WithAppSet(t *testing.T) {
	cfg := Config{
		PEMDir:            "/tmp/pems",
		SuiteName:         "bt",
		AllowedOrgs:       "",
		PerRepoWIFRepos:   "my-org/test-repo-01",
		WorkflowHostRepos: "my-org/test-repo-01,my-org/test-repo-02",
		AppSet:            "fullsend-test",
	}

	args := DeployArgs("bt-abc12345", "bt-mint", cfg)

	assert.Contains(t, args, "--app-set")
	// Find the value after --app-set.
	for i, a := range args {
		if a == "--app-set" {
			require.Less(t, i+1, len(args), "--app-set must have a value")
			assert.Equal(t, "fullsend-test", args[i+1])
			break
		}
	}
	// Verify other expected flags are present.
	assert.Contains(t, args, "--pem-dir")
	assert.Contains(t, args, "--allowed-orgs")
	assert.Contains(t, args, "--per-repo-wif-repos")
	assert.Contains(t, args, "--workflow-host-repos")

	// Verify --allowed-orgs is explicitly empty (per-repo mode).
	for i, a := range args {
		if a == "--allowed-orgs" {
			require.Less(t, i+1, len(args), "--allowed-orgs must have a value")
			assert.Equal(t, "", args[i+1], "--allowed-orgs should be explicit empty for per-repo mode")
			break
		}
	}

	// Verify --workflow-host-repos value.
	for i, a := range args {
		if a == "--workflow-host-repos" {
			require.Less(t, i+1, len(args), "--workflow-host-repos must have a value")
			assert.Equal(t, "my-org/test-repo-01,my-org/test-repo-02", args[i+1])
			break
		}
	}
}

func TestDeployArgs_WithoutAppSet(t *testing.T) {
	cfg := Config{
		PEMDir:            "/tmp/pems",
		SuiteName:         "bt",
		AllowedOrgs:       "",
		PerRepoWIFRepos:   "my-org/test-repo-01",
		WorkflowHostRepos: "my-org/test-repo-01",
	}

	args := DeployArgs("bt-abc12345", "bt-mint", cfg)

	// --app-set should not be present when AppSet is empty.
	assert.NotContains(t, args, "--app-set")
	// Other flags should still be present.
	assert.Contains(t, args, "--pem-dir")
	assert.Contains(t, args, "--allowed-orgs")
	assert.Contains(t, args, "--workflow-host-repos")
}

func TestTeardownArgs(t *testing.T) {
	args := TeardownArgs("bt-abc12345", "bt-mint")

	assert.Contains(t, args, "--platform")
	assert.Contains(t, args, "--preview")
	assert.Contains(t, args, "--worker-name")
	assert.Contains(t, args, "--yolo")

	// Verify --worker-name value matches deploy worker name.
	for i, a := range args {
		if a == "--worker-name" {
			require.Less(t, i+1, len(args), "--worker-name must have a value")
			assert.Equal(t, "bt-mint", args[i+1])
			break
		}
	}

	// Verify --preview value.
	for i, a := range args {
		if a == "--preview" {
			require.Less(t, i+1, len(args), "--preview must have a value")
			assert.Equal(t, "bt-abc12345", args[i+1])
			break
		}
	}
}

func TestDriver_Implements_Install_Driver(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fullsend.pem"), []byte("pem"), 0600))

	d, err := NewDriver(nil, "tok", "/bin/fullsend", "", t.Logf, Config{
		PEMDir:            dir,
		SuiteName:         "bt",
		AllowedOrgs:       "",
		PerRepoWIFRepos:   "org/repo",
		WorkflowHostRepos: "org/repo",
	})
	require.NoError(t, err)

	// Verify it implements install.MintDriver.
	var _ install.MintDriver = d
}

func TestPerRepoState_MintURL(t *testing.T) {
	st := install.NewPerRepoState("test-org", "test-repo", "https://bt-test-bt-mint.fullsend-ai.workers.dev")
	assert.Equal(t, "https://bt-test-bt-mint.fullsend-ai.workers.dev", st.MintURL())

	// Verify MintURLProvider interface.
	var provider install.MintURLProvider = st
	assert.Equal(t, st.MintURL(), provider.MintURL())
}

// newTestDriver creates a driver with a mock CLI runner for unit testing.
// It bypasses NewDriver validation (PEM dir, suite name) since those paths
// are already covered by TestNewDriver_* tests.
func newTestDriver(cliRunner install.CLIRunnerFunc) *driver {
	return &driver{
		token:      "tok",
		binary:     "/bin/fullsend",
		logf:       func(string, ...any) {},
		cfg:        Config{SuiteName: "bt"},
		workerName: WorkerName("bt"),
		cliRunner:  cliRunner,
	}
}

func TestInstall_Success(t *testing.T) {
	const wantMintURL = "https://bt-abc12345-bt-mint.fullsend-ai.workers.dev"
	d := newTestDriver(func(_, _ string, _ ...string) (string, error) {
		return "✓ Worker deployed at " + wantMintURL, nil
	})

	state, err := d.Install(context.Background(), "my-org")
	require.NoError(t, err)
	require.NotNil(t, state)

	assert.Equal(t, "per-repo", state.Mode())
	assert.Equal(t, "my-org", state.ConfigOwner())
	// ConfigRepo is empty — the driver only manages the mint, not a
	// specific repo. Per-repo state is created by the RepoEnsurer.
	assert.Equal(t, "", state.ConfigRepo())

	provider, ok := state.(install.MintURLProvider)
	require.True(t, ok)
	assert.Equal(t, wantMintURL, provider.MintURL())

	// previewAlias should be set for teardown.
	assert.NotEmpty(t, d.previewAlias)
}

func TestInstall_DeployFailure(t *testing.T) {
	d := newTestDriver(func(_, _ string, _ ...string) (string, error) {
		return "", fmt.Errorf("deploy exploded")
	})

	state, err := d.Install(context.Background(), "my-org")
	require.Error(t, err)
	assert.Nil(t, state)
	assert.Contains(t, err.Error(), "deploying CF preview mint for BT")
}

func TestInstall_NoMintURLInOutput(t *testing.T) {
	d := newTestDriver(func(_, _ string, _ ...string) (string, error) {
		return "Deploying...\nDone", nil
	})

	state, err := d.Install(context.Background(), "my-org")
	require.Error(t, err)
	assert.Nil(t, state)
	assert.Contains(t, err.Error(), "could not parse mint URL")
}

func TestTeardown_WithPreview(t *testing.T) {
	var calledArgs []string
	d := newTestDriver(func(_, _ string, args ...string) (string, error) {
		calledArgs = args
		return "", nil
	})
	d.previewAlias = "bt-abc12345"

	err := d.Teardown(context.Background(), "my-org", nil)
	require.NoError(t, err)
	assert.Contains(t, calledArgs, "--preview")
	assert.Contains(t, calledArgs, "bt-abc12345")
}

func TestTeardown_NoPreview(t *testing.T) {
	called := false
	d := newTestDriver(func(_, _ string, _ ...string) (string, error) {
		called = true
		return "", nil
	})
	// previewAlias is empty — teardownPreview should be a no-op.

	err := d.Teardown(context.Background(), "my-org", nil)
	require.NoError(t, err)
	assert.False(t, called, "CLI should not be called when no preview was deployed")
}

func TestTeardown_CLIFailure_LogsButDoesNotFail(t *testing.T) {
	var logged []string
	d := newTestDriver(func(_, _ string, _ ...string) (string, error) {
		return "", fmt.Errorf("teardown boom")
	})
	d.previewAlias = "bt-abc12345"
	d.logf = func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}

	err := d.Teardown(context.Background(), "my-org", nil)
	require.NoError(t, err, "teardown failures should be logged, not returned")
	assert.True(t, len(logged) > 0, "expected log output on teardown failure")

	// Verify at least one log line mentions the failure.
	var foundFailure bool
	for _, l := range logged {
		if strings.Contains(l, "teardown failed") {
			foundFailure = true
			break
		}
	}
	assert.True(t, foundFailure, "expected a log line about teardown failure")
}
