package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/gitlabci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerRepoSteps(sc *godog.ScenarioContext) {
	sc.Step(`^an installed test repository$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenInstalledTestRepository(ctx, world.FromContext(ctx))
	})
}

func givenInstalledTestRepository(ctx context.Context, w *world.World) error {
	if w.LeasedRepoName != "" {
		return nil
	}

	if w.Driver == nil {
		return fmt.Errorf("no install driver configured; use a Factory to construct a Driver for the suite")
	}

	if w.ScenarioName != "" && w.IsPlaybackMode() {
		w.Driver.(*install.PlaybackDriver).SetRepoHint(slugify(w.ScenarioName))
	}

	repoName, err := w.Driver.AllocateRepo(ctx)
	if err != nil {
		return fmt.Errorf("allocating repo: %w", err)
	}

	w.LeasedRepoName = repoName
	w.RepoOwner = w.Org
	w.RepoName = repoName
	w.RepoFull = w.Org + "/" + repoName

	if pd, ok := w.Driver.(*install.PlaybackDriver); ok {
		if err := reconfigureCIWithBotToken(ctx, w, pd, repoName); err != nil {
			w.Logf("[repo] WARNING: could not reconfigure CI driver with bot token: %v", err)
		}
	}

	if w.IsPlaybackMode() && len(w.PlaybackEntries) > 0 && !w.PlaybackCommitted {
		if err := commitPlaylist(w); err != nil {
			return fmt.Errorf("auto-committing playlist: %w", err)
		}
	}

	return nil
}

func reconfigureCIWithBotToken(ctx context.Context, w *world.World, pd *install.PlaybackDriver, repoName string) error {
	if resolveForge(w) != "gitlab" {
		return nil
	}
	botToken, err := pd.InstalledBotToken(ctx, repoName)
	if err != nil {
		return err
	}
	fc, ok := w.CI.(interface{ ForgeClient() forge.Client })
	if !ok {
		return fmt.Errorf("CI driver does not expose ForgeClient")
	}
	w.CI = gitlabci.New(fc.ForgeClient(), botToken)
	w.Logf("[repo] CI driver reconfigured with installed bot token")
	return nil
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == ' ' || r == '_' || r == '-' {
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > 30 {
		out = out[:30]
	}
	return strings.TrimRight(out, "-")
}
