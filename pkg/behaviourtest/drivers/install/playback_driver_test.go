package install

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

// fakeBaseDriver is a minimal Driver test double for verifying that
// PlaybackDriver delegates to its wrapped base Driver.
type fakeBaseDriver struct {
	allocateCalls  int
	allocateName   string
	allocateErr    error
	deallocateName string
	deallocateErr  error
	finalizeCalled bool
	finalizeErr    error
	capacity       int
}

func (f *fakeBaseDriver) AllocateRepo(context.Context) (string, error) {
	f.allocateCalls++
	return f.allocateName, f.allocateErr
}

func (f *fakeBaseDriver) DeallocateRepo(_ context.Context, repoName string) error {
	f.deallocateName = repoName
	return f.deallocateErr
}

func (f *fakeBaseDriver) Finalize(context.Context) error {
	f.finalizeCalled = true
	return f.finalizeErr
}

func (f *fakeBaseDriver) Capacity() int { return f.capacity }

var _ Driver = (*fakeBaseDriver)(nil)

func TestPlaybackDriver_DelegatesToBase(t *testing.T) {
	base := &fakeBaseDriver{allocateName: "test-repo-01", capacity: 7}
	pd := NewPlaybackDriver(base)

	name, err := pd.AllocateRepo(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "test-repo-01", name)
	assert.Equal(t, 1, base.allocateCalls)

	require.NoError(t, pd.DeallocateRepo(context.Background(), "test-repo-01"))
	assert.Equal(t, "test-repo-01", base.deallocateName)

	require.NoError(t, pd.Finalize(context.Background()))
	assert.True(t, base.finalizeCalled)

	assert.Equal(t, 7, pd.Capacity())
}

func TestPlaybackDriver_AllocateRepo_PropagatesBaseError(t *testing.T) {
	base := &fakeBaseDriver{allocateErr: errors.New("pool exhausted")}
	pd := NewPlaybackDriver(base)

	_, err := pd.AllocateRepo(context.Background())
	assert.ErrorContains(t, err, "pool exhausted")
}

func TestPlaybackDriver_SetRepoHint_NoOp(t *testing.T) {
	base := &fakeBaseDriver{}
	pd := NewPlaybackDriver(base)

	// SetRepoHint is retained for the shared playback step definitions
	// but does not affect allocation — pool repos have stable names
	// assigned by the wrapped Driver.
	assert.NotPanics(t, func() { pd.SetRepoHint("Some Scenario Name") })
}

func TestPlaybackInstallHooks_NonPlaybackRuntime_NoHooks(t *testing.T) {
	hooks := playbackInstallHooks(common.GitHubSetupOpts{}, t.Logf)
	assert.Nil(t, hooks.BeforeInstall)
	assert.Nil(t, hooks.AfterInstall)

	hooks = playbackInstallHooks(common.GitHubSetupOpts{Runtime: "dummy"}, t.Logf)
	assert.Nil(t, hooks.AfterInstall)
}

func TestPlaybackInstallHooks_AfterInstall_CreatesTrackingIssueAndComment(t *testing.T) {
	client := forge.NewFakeClient()
	hooks := playbackInstallHooks(common.GitHubSetupOpts{Runtime: "dummy-playback"}, t.Logf)
	require.NotNil(t, hooks.AfterInstall)

	err := hooks.AfterInstall(context.Background(), client, "acme", "test-repo-01", nil)
	require.NoError(t, err)

	content, err := client.GetFileContent(context.Background(), "acme", "test-repo-01", playbackCommentPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "acme/test-repo-01/issues/comments/")
}

func TestPlaybackInstallHooks_AfterInstall_CreateIssueError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors = map[string]error{"CreateIssue": errors.New("boom")}
	hooks := playbackInstallHooks(common.GitHubSetupOpts{Runtime: "dummy-playback"}, t.Logf)

	err := hooks.AfterInstall(context.Background(), client, "acme", "test-repo-01", nil)
	assert.ErrorContains(t, err, "creating playback tracking issue")
}

func TestPlaybackSetupOpts_DefaultsToVendoredDummy(t *testing.T) {
	t.Setenv("PLAYBACK_RUNTIME", "")
	opts := playbackSetupOpts()
	assert.True(t, opts.Vendor)
	assert.Empty(t, opts.Runtime)
}

func TestPlaybackSetupOpts_HonoursEnvOverride(t *testing.T) {
	t.Setenv("PLAYBACK_RUNTIME", "dummy-playback")
	opts := playbackSetupOpts()
	assert.Equal(t, "dummy-playback", opts.Runtime)
}

// TestPlaybackSetupOpts_PropagatesConfigPreset is a regression test for a
// factory-path bug: NewRepoPoolCFMintPreviews builds its GitHubSetupOpts via
// playbackSetupOpts instead of common.DefaultGitHubSetupOpts, and the
// replacement silently dropped BEHAVIOUR_CONFIG_PRESET (unlike
// newRepoEnsurer, which applies it onto the vendored-mode defaults). That
// made ordinary DEV suites silently lose their requested preset even when
// PLAYBACK_RUNTIME is unset. playbackSetupOpts must forward the preset so
// it reaches `github setup` as --config.
func TestPlaybackSetupOpts_PropagatesConfigPreset(t *testing.T) {
	t.Setenv("PLAYBACK_RUNTIME", "")
	t.Setenv("BEHAVIOUR_CONFIG_PRESET", "https://example.com/preset.yaml")
	opts := playbackSetupOpts()
	assert.Equal(t, "https://example.com/preset.yaml", opts.ConfigPreset)

	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}
	err := common.RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--config")
	assert.Contains(t, capturedArgs, "https://example.com/preset.yaml")
}

// TestPlaybackSetupOpts_ConfigPresetAndRuntime is a regression test
// covering the combined preset + explicit playback runtime case: the
// installed runtime must match what doEnsure's post-install validation
// expects (opts.Runtime), even when a config preset is also set.
func TestPlaybackSetupOpts_ConfigPresetAndRuntime(t *testing.T) {
	t.Setenv("PLAYBACK_RUNTIME", "dummy-playback")
	t.Setenv("BEHAVIOUR_CONFIG_PRESET", "https://example.com/preset.yaml")
	opts := playbackSetupOpts()
	assert.Equal(t, "https://example.com/preset.yaml", opts.ConfigPreset)
	assert.Equal(t, "dummy-playback", opts.Runtime)

	var capturedArgs []string
	runner := func(_, _ string, args ...string) (string, error) {
		capturedArgs = args
		return "", nil
	}
	err := common.RunGitHubSetupWithOpts("/bin/fullsend", "tok", "org/repo", "https://mint.test", "", opts, runner, t.Logf)
	require.NoError(t, err)
	assert.Contains(t, capturedArgs, "--config")
	assert.Contains(t, capturedArgs, "https://example.com/preset.yaml")
	assert.Contains(t, capturedArgs, "--runtime")
	assert.Contains(t, capturedArgs, "dummy-playback")
}
