package steps

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerReactionSteps(sc *godog.ScenarioContext) {
	sc.Step(`^status notification reactions are enabled$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenReactionsEnabled(world.FromContext(ctx))
	})
	sc.Step(`^the issue has a "([^"]+)" reaction$`, func(ctx context.Context, content string) (context.Context, error) {
		return ctx, thenIssueHasReaction(world.FromContext(ctx), content)
	})
	sc.Step(`^the issue does not have a "([^"]+)" reaction$`, func(ctx context.Context, content string) (context.Context, error) {
		return ctx, thenIssueDoesNotHaveReaction(world.FromContext(ctx), content)
	})
}

// givenReactionsEnabled sets status_notifications.reaction.start and
// .completion to "enabled" in the enrolled repo's config.yaml. This
// causes the runner to post emoji reactions on start and completion.
func givenReactionsEnabled(w *world.World) error {
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	cfg.SetStatusNotifications(&config.StatusNotificationConfig{
		Comment: config.CommentNotificationConfig{
			Start:      "enabled",
			Completion: "enabled",
		},
		Reaction: config.ReactionNotificationConfig{
			Start:      "enabled",
			Completion: "enabled",
		},
	})
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: enable reaction notifications", merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// DisableReactionNotifications removes the status_notifications from
// the enrolled repo's config.yaml. Exported so CleanupScenario can
// call it during scenario teardown.
func DisableReactionNotifications(w *world.World) error {
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	cfg.SetStatusNotifications(nil)
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: disable reaction notifications", merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

func thenIssueHasReaction(w *world.World, content string) error {
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	reactions, err := w.SCM.ListIssueReactions(context.Background(), w.RepoOwner, w.RepoName, w.IssueNumber)
	if err != nil {
		return fmt.Errorf("listing reactions: %w", err)
	}
	for _, r := range reactions {
		if r.Content == content {
			return nil
		}
	}
	var found []string
	for _, r := range reactions {
		found = append(found, r.Content)
	}
	return fmt.Errorf("issue #%d reactions %v do not include %q", w.IssueNumber, found, content)
}

func thenIssueDoesNotHaveReaction(w *world.World, content string) error {
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	reactions, err := w.SCM.ListIssueReactions(context.Background(), w.RepoOwner, w.RepoName, w.IssueNumber)
	if err != nil {
		return fmt.Errorf("listing reactions: %w", err)
	}
	for _, r := range reactions {
		if r.Content == content {
			return fmt.Errorf("issue #%d unexpectedly has %q reaction", w.IssueNumber, content)
		}
	}
	return nil
}

// reactionsEnabledInConfig checks if status_notifications.reaction is
// set in the repo config. Used by cleanup to decide whether to reset.
func reactionsEnabledInConfig(w *world.World) bool {
	if w.SCM == nil || w.Org == "" || w.RepoName == "" {
		return false
	}
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return false
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return false
	}
	sn := cfg.StatusNotifications()
	if sn == nil {
		return false
	}
	return (sn.Reaction.Start != "" && sn.Reaction.Start != "disabled") ||
		(sn.Reaction.Completion != "" && sn.Reaction.Completion != "disabled")
}
