package install

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gaci "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

// PlaybackDriver decorates the standard behaviour repo-pool driver with the
// playback runtime's tracking-comment setup. Allocation, WIF resolution,
// installation, cleanup, and capacity all remain owned by the shared driver.
type PlaybackDriver struct {
	base   Driver
	org    string
	client forge.Client
	logf   func(string, ...any)
}

// NewPlaybackDriver wraps a standard behaviour Driver for playback tests.
func NewPlaybackDriver(base Driver, org string, client forge.Client, logf func(string, ...any)) *PlaybackDriver {
	return &PlaybackDriver{base: base, org: org, client: client, logf: logf}
}

// SetRepoHint is retained for the shared playback steps. Pool repositories
// have stable names, so the hint is no longer part of allocation.
func (d *PlaybackDriver) SetRepoHint(string) {}

func (d *PlaybackDriver) AllocateRepo(ctx context.Context) (string, error) {
	return d.base.AllocateRepo(ctx)
}

func playbackInstallHooks(opts common.GitHubSetupOpts, _ string, _ forge.Client, logf func(string, ...any)) InstallHooks {
	if opts.Runtime != "dummy-playback" {
		return InstallHooks{}
	}
	return InstallHooks{
		AfterInstall: func(ctx context.Context, client forge.Client, org, repo string, _ any) error {
			target := org + "/" + repo
			logf("[playback] creating tracking issue on %s after install", target)
			trigger := time.Now()
			issue, err := client.CreateIssue(ctx, org, repo, "Playback tracking", "Internal issue for playback counter tracking")
			if err != nil {
				return err
			}
			if _, err := gaci.New(client, "").WaitForWorkflow(ctx, org, repo, PerRepoTriageWorkflow, trigger, "issues"); err != nil {
				return fmt.Errorf("waiting for tracking issue workflow: %w", err)
			}
			comment, err := client.CreateIssueComment(ctx, org, repo, issue.Number, "playback-current: 1")
			if err != nil {
				return err
			}
			logf("[playback] committing tracking comment path to %s/%s", org, repo)
			ref := fmt.Sprintf("gh\n/repos/%s/issues/comments/%d", target, comment.ID)
			_, err = client.CommitFiles(ctx, org, repo,
				"chore: store playback tracking comment [skip ci]",
				[]forge.TreeFile{{Path: ".fullsend/playback-comment-url", Content: []byte(ref), Mode: "100644"}})
			return err
		},
	}
}

// InstalledBotToken is kept for the shared CI-step adapter. GitHub playback
// uses the existing suite CI driver and does not need to reconfigure it.
func (d *PlaybackDriver) InstalledBotToken(ctx context.Context, repoName string) (string, error) {
	val, ok, err := d.client.GetRepoVariable(ctx, d.org, repoName, "FULLSEND_FORGE_TOKEN")
	if err != nil {
		return "", fmt.Errorf("reading FULLSEND_FORGE_TOKEN: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("FULLSEND_FORGE_TOKEN not found on %s/%s", d.org, repoName)
	}
	return val, nil
}

func (d *PlaybackDriver) DeallocateRepo(ctx context.Context, repoName string) error {
	return d.base.DeallocateRepo(ctx, repoName)
}

func (d *PlaybackDriver) Finalize(ctx context.Context) error {
	return d.base.Finalize(ctx)
}

func (d *PlaybackDriver) Capacity() int { return d.base.Capacity() }

func playbackSetupOpts() common.GitHubSetupOpts {
	opts := common.DefaultGitHubSetupOpts()
	if runtime := os.Getenv("PLAYBACK_RUNTIME"); runtime != "" {
		opts.Runtime = runtime
	}
	return opts
}

var _ Driver = (*PlaybackDriver)(nil)
