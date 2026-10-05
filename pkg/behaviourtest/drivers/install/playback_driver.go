// playback_driver.go implements PlaybackDriver, a playback-mode marker
// and delegating wrapper around a standard behaviour repo-pool Driver.
// Allocation, WIF resolution, installation, cleanup, and capacity all
// remain owned by the wrapped Driver — this keeps the existing
// pool-based driver paths untouched. The tracking-issue/comment setup
// playback scenarios rely on is provisioned separately via
// playbackInstallHooks, attached to the repoEnsurer by the factories
// (e.g. NewRepoPoolCFMintPreviews) alongside PlaybackDriver construction.
package install

import (
	"context"
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

// playbackRuntime is the config runtime name used for playback installs.
const playbackRuntime = "dummy-playback"

// PlaybackDriver is a playback-mode marker and delegating wrapper around
// a standard behaviour repo-pool Driver: World.IsPlaybackMode type-asserts
// on it to detect a playback suite, and it forwards
// AllocateRepo/DeallocateRepo/Finalize/Capacity unchanged to the wrapped
// Driver. The tracking-issue/comment setup playback installs rely on is
// provisioned by playbackInstallHooks, attached to the repoEnsurer
// independently of this wrapper.
type PlaybackDriver struct {
	base Driver
}

// NewPlaybackDriver wraps a standard behaviour Driver for playback tests.
// base is typically constructed by one of the existing Factory functions
// (e.g. NewRepoPoolCFMintPreviews, NewRepoPoolCFMintStage) using
// GitHubSetupOpts.Runtime set to "dummy-playback" so installed repos run
// the playback runtime; playbackInstallHooks (attached to the ensurer by
// the same factory) provisions the tracking-issue bookkeeping those
// installs rely on.
func NewPlaybackDriver(base Driver) *PlaybackDriver {
	return &PlaybackDriver{base: base}
}

// SetRepoHint is retained for the shared playback step definitions. Pool
// repositories have stable names assigned by the wrapped Driver, so the
// hint is not used to influence allocation.
func (d *PlaybackDriver) SetRepoHint(string) {}

// AllocateRepo delegates to the wrapped Driver.
func (d *PlaybackDriver) AllocateRepo(ctx context.Context) (string, error) {
	return d.base.AllocateRepo(ctx)
}

// DeallocateRepo delegates to the wrapped Driver.
func (d *PlaybackDriver) DeallocateRepo(ctx context.Context, repoName string) error {
	return d.base.DeallocateRepo(ctx, repoName)
}

// Finalize delegates to the wrapped Driver.
func (d *PlaybackDriver) Finalize(ctx context.Context) error {
	return d.base.Finalize(ctx)
}

// Capacity delegates to the wrapped Driver.
func (d *PlaybackDriver) Capacity() int { return d.base.Capacity() }

// Compile-time check: PlaybackDriver implements Driver.
var _ Driver = (*PlaybackDriver)(nil)

// playbackCommentPath is the repo-relative path the dummy-playback
// runtime reads to find its tracking comment. The committed content is
// "gh\n<api path>", mirroring the two-line reference format the runtime
// already expects for other cross-referenced resources.
const playbackCommentPath = ".fullsend/playback-comment-url"

// playbackInstallHooks returns the InstallHooks a repoEnsurer should run
// when opts.Runtime is the playback runtime: after a successful install,
// create a tracking issue and commit the path to its first comment under
// playbackCommentPath. The dummy-playback runtime reads that comment to
// track its position in the scenario's canned playlist. For any other
// runtime this returns the zero-value InstallHooks (no-op).
func playbackInstallHooks(opts common.GitHubSetupOpts, logf func(string, ...any)) InstallHooks {
	if opts.Runtime != playbackRuntime {
		return InstallHooks{}
	}
	return InstallHooks{
		AfterInstall: func(ctx context.Context, client forge.Client, org, repo string, _ any) error {
			target := org + "/" + repo
			logf("[playback] creating tracking issue on %s after install", target)
			issue, err := client.CreateIssue(ctx, org, repo, "Playback tracking", "Internal issue for playback counter tracking")
			if err != nil {
				return fmt.Errorf("creating playback tracking issue on %s: %w", target, err)
			}
			comment, err := client.CreateIssueComment(ctx, org, repo, issue.Number, "playback-current: 1")
			if err != nil {
				return fmt.Errorf("creating playback tracking comment on %s: %w", target, err)
			}
			logf("[playback] committing tracking comment path to %s", target)
			ref := fmt.Sprintf("gh\n/repos/%s/issues/comments/%d", target, comment.ID)
			if _, err := client.CommitFiles(ctx, org, repo,
				"chore: store playback tracking comment [skip ci]",
				[]forge.TreeFile{{Path: playbackCommentPath, Content: []byte(ref), Mode: "100644"}}); err != nil {
				return fmt.Errorf("committing playback tracking comment path on %s: %w", target, err)
			}
			return nil
		},
	}
}

// playbackSetupOpts returns vendored-mode GitHubSetupOpts with ConfigPreset
// populated from BEHAVIOUR_CONFIG_PRESET (same as newRepoEnsurer) and
// Runtime set to the playback runtime when PLAYBACK_RUNTIME is set in the
// environment, or left empty (normal "dummy" runtime) otherwise. Factories
// that build the shared pool driver (e.g. NewRepoPoolCFMintPreviews) call
// this instead of common.DefaultGitHubSetupOpts() so the same factory can
// back either a normal behaviour suite or a playback suite depending on
// how the process was launched, without losing BEHAVIOUR_CONFIG_PRESET
// support for ordinary (non-playback) suites.
func playbackSetupOpts() common.GitHubSetupOpts {
	opts := common.DefaultGitHubSetupOpts()
	opts.ConfigPreset = envConfigPreset()
	if runtime := os.Getenv("PLAYBACK_RUNTIME"); runtime != "" {
		opts.Runtime = runtime
	}
	return opts
}
