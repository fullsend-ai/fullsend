package install

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

// speedUpValidateRetries sets validateRetryDelay to zero for fast tests
// and returns a cleanup function that restores the original value.
func speedUpValidateRetries(t *testing.T) {
	t.Helper()
	orig := validateRetryDelay
	validateRetryDelay = 0
	t.Cleanup(func() { validateRetryDelay = orig })
}

// retryTestClient is a forge.Client test double that delegates
// GetFileContent to a caller-supplied function. The embedded nil
// forge.Client satisfies the interface; only GetFileContent is called.
type retryTestClient struct {
	forge.Client // panics on uncovered methods
	getFileFn    func(ctx context.Context, owner, repo, path string) ([]byte, error)
}

func (c *retryTestClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	return c.getFileFn(ctx, owner, repo, path)
}

func TestValidatePerRepoPostInstall_OK(t *testing.T) {
	client := forge.NewFakeClient()
	org, repo := "acme", "test-repo"
	perRepoCfg := config.NewPerRepoConfig(config.PerRepoDefaultRoles(), org+"/"+repo)
	perRepoCfg.SetRuntime("dummy")
	cfg, err := perRepoCfg.Marshal()
	require.NoError(t, err)

	client.FileContents = map[string][]byte{
		org + "/" + repo + "/.github/workflows/fullsend.yaml":  []byte("name: fullsend"),
		org + "/" + repo + "/.fullsend/config.yaml":            cfg,
		org + "/" + repo + "/" + scaffold.VendoredMarkerPath(): []byte("marker"),
		org + "/" + repo + "/.fullsend/bin/fullsend":           []byte("binary"),
	}

	err = ValidatePerRepoPostInstall(context.Background(), client, org, repo)
	require.NoError(t, err)
}

func TestValidatePerRepoPostInstall_MissingShim(t *testing.T) {
	speedUpValidateRetries(t)
	client := forge.NewFakeClient()
	err := ValidatePerRepoPostInstall(context.Background(), client, "acme", "test-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fullsend.yaml")
}

func TestValidatePerRepoPostInstallNonVendored_OK(t *testing.T) {
	client := forge.NewFakeClient()
	org, repo := "acme", "test-repo"
	perRepoCfg := config.NewPerRepoConfig(config.PerRepoDefaultRoles(), org+"/"+repo)
	perRepoCfg.SetRuntime("dummy")
	cfg, err := perRepoCfg.Marshal()
	require.NoError(t, err)

	// Non-vendored: only shim + config required, no marker or binary.
	client.FileContents = map[string][]byte{
		org + "/" + repo + "/.github/workflows/fullsend.yaml": []byte("name: fullsend"),
		org + "/" + repo + "/.fullsend/config.yaml":           cfg,
	}

	err = ValidatePerRepoPostInstallNonVendored(context.Background(), client, org, repo)
	require.NoError(t, err)
}

func TestValidatePerRepoPostInstallNonVendored_MissingShim(t *testing.T) {
	speedUpValidateRetries(t)
	client := forge.NewFakeClient()
	err := ValidatePerRepoPostInstallNonVendored(context.Background(), client, "acme", "test-repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fullsend.yaml")
}

func TestValidatePerRepoPostInstallNonVendored_WrongRuntime(t *testing.T) {
	client := forge.NewFakeClient()
	org, repo := "acme", "test-repo"
	cfg, err := config.NewPerRepoConfig(nil, org+"/"+repo).Marshal()
	require.NoError(t, err)

	client.FileContents = map[string][]byte{
		org + "/" + repo + "/.github/workflows/fullsend.yaml": []byte("name: fullsend"),
		org + "/" + repo + "/.fullsend/config.yaml":           cfg,
	}

	err = ValidatePerRepoPostInstallNonVendored(context.Background(), client, org, repo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "want dummy")
}

func TestValidatePerRepoPostInstall_WrongRuntime(t *testing.T) {
	client := forge.NewFakeClient()
	org, repo := "acme", "test-repo"
	cfg, err := config.NewPerRepoConfig(nil, org+"/"+repo).Marshal()
	require.NoError(t, err)

	client.FileContents = map[string][]byte{
		org + "/" + repo + "/.github/workflows/fullsend.yaml":  []byte("name: fullsend"),
		org + "/" + repo + "/.fullsend/config.yaml":            cfg,
		org + "/" + repo + "/" + scaffold.VendoredMarkerPath(): []byte("marker"),
		org + "/" + repo + "/.fullsend/bin/fullsend":           []byte("binary"),
	}

	err = ValidatePerRepoPostInstall(context.Background(), client, org, repo)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "want dummy")
}

func TestParseInferenceStatusWIFProvider_OK(t *testing.T) {
	out := `{
  "status": "healthy",
  "FULLSEND_GCP_PROJECT_ID": "my-project",
  "FULLSEND_GCP_WIF_PROVIDER": "projects/123/locations/global/workloadIdentityPools/fullsend-inference/providers/gh-halfsend-01-test-repo"
}`
	got, err := common.ParseInferenceStatusWIFProvider(out)
	require.NoError(t, err)
	assert.Equal(t, "projects/123/locations/global/workloadIdentityPools/fullsend-inference/providers/gh-halfsend-01-test-repo", got)
}

func TestParseInferenceStatusWIFProvider_NoJSON(t *testing.T) {
	_, err := common.ParseInferenceStatusWIFProvider("no json here")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no JSON status object")
}

func TestParseInferenceStatusWIFProvider_IgnoresLeadingNoise(t *testing.T) {
	out := `Running inference status...
log line with { brace noise
{"status":"healthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/workloadIdentityPools/p/providers/x"}`
	got, err := common.ParseInferenceStatusWIFProvider(out)
	require.NoError(t, err)
	assert.Equal(t, "projects/1/locations/global/workloadIdentityPools/p/providers/x", got)
}

func TestParseInferenceStatusWIFProvider_Unhealthy(t *testing.T) {
	_, err := common.ParseInferenceStatusWIFProvider(`{"status":"unhealthy","FULLSEND_GCP_WIF_PROVIDER":"projects/1/locations/global/workloadIdentityPools/p/providers/x"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "healthy")
}

// --- getFileWithRetry unit tests ---

func TestGetFileWithRetry_SucceedsAfterTransient404(t *testing.T) {
	speedUpValidateRetries(t)

	var calls atomic.Int32
	client := &retryTestClient{
		getFileFn: func(_ context.Context, _, _, _ string) ([]byte, error) {
			if calls.Add(1) <= 2 {
				return nil, forge.ErrNotFound
			}
			return []byte("content"), nil
		},
	}

	data, err := getFileWithRetry(context.Background(), client, "org", "repo", "file.txt")
	require.NoError(t, err)
	assert.Equal(t, []byte("content"), data)
	assert.Equal(t, int32(3), calls.Load(), "should succeed on third attempt")
}

func TestGetFileWithRetry_ExhaustsRetries(t *testing.T) {
	speedUpValidateRetries(t)

	var calls atomic.Int32
	client := &retryTestClient{
		getFileFn: func(_ context.Context, _, _, _ string) ([]byte, error) {
			calls.Add(1)
			return nil, forge.ErrNotFound
		},
	}

	_, err := getFileWithRetry(context.Background(), client, "org", "repo", "file.txt")
	require.Error(t, err)
	assert.True(t, forge.IsNotFound(err), "error should be ErrNotFound")
	assert.Equal(t, int32(validateMaxAttempts), calls.Load(),
		"should exhaust all retry attempts")
}

func TestGetFileWithRetry_NonNotFoundFailsImmediately(t *testing.T) {
	speedUpValidateRetries(t)

	networkErr := fmt.Errorf("network timeout")
	var calls atomic.Int32
	client := &retryTestClient{
		getFileFn: func(_ context.Context, _, _, _ string) ([]byte, error) {
			calls.Add(1)
			return nil, networkErr
		},
	}

	_, err := getFileWithRetry(context.Background(), client, "org", "repo", "file.txt")
	require.Error(t, err)
	assert.ErrorIs(t, err, networkErr)
	assert.Equal(t, int32(1), calls.Load(),
		"non-404 errors should not be retried")
}

func TestGetFileWithRetry_RespectsContextCancellation(t *testing.T) {
	speedUpValidateRetries(t)

	// Override delay to something non-zero so the select can observe
	// cancellation before the timer fires.
	validateRetryDelay = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	client := &retryTestClient{
		getFileFn: func(_ context.Context, _, _, _ string) ([]byte, error) {
			if calls.Add(1) == 1 {
				cancel() // cancel after first 404
			}
			return nil, forge.ErrNotFound
		},
	}

	_, err := getFileWithRetry(ctx, client, "org", "repo", "file.txt")
	require.Error(t, err)
	assert.Equal(t, context.Canceled, err)
	assert.Equal(t, int32(1), calls.Load(),
		"should stop after context is cancelled")
}

// --- ValidatePerRepoPostInstall retry integration tests ---

// transientNotFoundClient wraps a forge.FakeClient and returns
// ErrNotFound for the first failCount GetFileContent calls before
// delegating to the underlying client.
type transientNotFoundClient struct {
	*forge.FakeClient
	calls     atomic.Int32
	failCount int32
}

func (c *transientNotFoundClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	if c.calls.Add(1) <= c.failCount {
		return nil, forge.ErrNotFound
	}
	return c.FakeClient.GetFileContent(ctx, owner, repo, path)
}

func TestValidatePerRepoPostInstall_RetriesTransient404(t *testing.T) {
	speedUpValidateRetries(t)

	org, repo := "acme", "test-repo"
	inner := forge.NewFakeClient()
	perRepoCfg := config.NewPerRepoConfig(config.PerRepoDefaultRoles(), org+"/"+repo)
	perRepoCfg.SetRuntime("dummy")
	cfg, err := perRepoCfg.Marshal()
	require.NoError(t, err)

	inner.FileContents = map[string][]byte{
		org + "/" + repo + "/.github/workflows/fullsend.yaml":  []byte("name: fullsend"),
		org + "/" + repo + "/.fullsend/config.yaml":            cfg,
		org + "/" + repo + "/" + scaffold.VendoredMarkerPath(): []byte("marker"),
		org + "/" + repo + "/.fullsend/bin/fullsend":           []byte("binary"),
	}

	// Fail the first 2 GetFileContent calls with 404, then succeed.
	client := &transientNotFoundClient{FakeClient: inner, failCount: 2}

	err = ValidatePerRepoPostInstall(context.Background(), client, org, repo)
	require.NoError(t, err, "validation should succeed after transient 404s")
	assert.GreaterOrEqual(t, client.calls.Load(), int32(3),
		"should have retried at least once")
}

func TestValidatePerRepoPostInstall_ExhaustsRetriesWithClearError(t *testing.T) {
	speedUpValidateRetries(t)

	// All calls return 404 — retries should exhaust and surface a clear error.
	client := &retryTestClient{
		getFileFn: func(_ context.Context, _, _, _ string) ([]byte, error) {
			return nil, forge.ErrNotFound
		},
	}

	err := ValidatePerRepoPostInstall(context.Background(), client, "org", "repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing .github/workflows/fullsend.yaml")
}
