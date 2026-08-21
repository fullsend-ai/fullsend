package steps

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
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
// Existing Comment settings are preserved so enabling reactions does
// not clobber unrelated notification config from other scenarios
// sharing the same pool-repo slot.
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
	// Preserve any existing Comment settings from the current config.
	sn := &config.StatusNotificationConfig{
		Reaction: config.ReactionNotificationConfig{
			Start:      "enabled",
			Completion: "enabled",
		},
	}
	if existing := cfg.StatusNotifications(); existing != nil {
		sn.Comment = existing.Comment
	}
	cfg.SetStatusNotifications(sn)
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: enable reaction notifications", merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// DisableReactionNotifications clears only the Reaction fields from
// status_notifications in the enrolled repo's config.yaml, preserving
// any existing Comment settings. Exported so CleanupScenario can call
// it during scenario teardown without clobbering unrelated config on a
// shared pool-repo slot.
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
	existing := cfg.StatusNotifications()
	if existing != nil && (existing.Comment.Start != "" || existing.Comment.Completion != "") {
		// Preserve non-default Comment settings; clear only Reaction.
		cfg.SetStatusNotifications(&config.StatusNotificationConfig{
			Comment: existing.Comment,
		})
	} else {
		cfg.SetStatusNotifications(nil)
	}
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

	// Get the bot user login to filter reactions — on shared test repos,
	// leftover reactions from other runs or humans can cause false positives.
	botLogin, err := getBotLogin(w)
	if err != nil {
		return fmt.Errorf("getting bot login: %w", err)
	}

	reactions, err := w.SCM.ListIssueReactions(context.Background(), w.RepoOwner, w.RepoName, w.IssueNumber)
	if err != nil {
		return fmt.Errorf("listing reactions: %w", err)
	}
	for _, r := range reactions {
		if r.User == botLogin && r.Content == content {
			return nil
		}
	}
	var found []string
	for _, r := range reactions {
		if r.User == botLogin {
			found = append(found, r.Content)
		}
	}
	return fmt.Errorf("issue #%d bot reactions %v do not include %q", w.IssueNumber, found, content)
}

func thenIssueDoesNotHaveReaction(w *world.World, content string) error {
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}

	// Get the bot user login to filter reactions — on shared test repos,
	// leftover reactions from other runs or humans can cause false negatives.
	botLogin, err := getBotLogin(w)
	if err != nil {
		return fmt.Errorf("getting bot login: %w", err)
	}

	reactions, err := w.SCM.ListIssueReactions(context.Background(), w.RepoOwner, w.RepoName, w.IssueNumber)
	if err != nil {
		return fmt.Errorf("listing reactions: %w", err)
	}
	for _, r := range reactions {
		if r.User == botLogin && r.Content == content {
			return fmt.Errorf("issue #%d unexpectedly has bot %q reaction", w.IssueNumber, content)
		}
	}
	return nil
}

// botUserProvider is an optional interface that test SCM fakes can
// implement to provide the bot user login for reaction filtering.
type botUserProvider interface {
	GetBotUser() string
}

// getBotLogin returns the authenticated user's login from the SCM driver's
// underlying forge.Client (for github.Driver) or from the optional
// botUserProvider interface (for test fakes). Used to filter reactions by
// the bot user in assertion helpers.
func getBotLogin(w *world.World) (string, error) {
	// Try test fake interface first — avoids import cycle if a fake
	// implements this helper.
	if bup, ok := w.SCM.(botUserProvider); ok {
		return bup.GetBotUser(), nil
	}
	// Type-assert to *github.Driver to access the underlying Client.
	ghDriver, ok := w.SCM.(*github.Driver)
	if !ok {
		return "", fmt.Errorf("SCM driver is not *github.Driver (got %T)", w.SCM)
	}
	return ghDriver.Client.GetAuthenticatedUser(context.Background())
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
