package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerStatusCommentSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the (issue|pull request) has a status (?:comment|marker)$`, func(ctx context.Context, target string) (context.Context, error) {
		return ctx, thenTargetHasStatusComment(world.FromContext(ctx), target)
	})
	sc.Step(`^the (issue|pull request) does not have a status (?:comment|marker)$`, func(ctx context.Context, target string) (context.Context, error) {
		return ctx, thenTargetDoesNotHaveStatusComment(world.FromContext(ctx), target)
	})
}

func targetNumber(w *world.World, target string) (int, error) {
	switch target {
	case "issue":
		if w.IssueNumber == 0 {
			return 0, fmt.Errorf("no issue created")
		}
		return w.IssueNumber, nil
	case "pull request":
		if w.PRNumber == 0 {
			return 0, fmt.Errorf("no pull request opened")
		}
		return w.PRNumber, nil
	default:
		return 0, fmt.Errorf("unsupported target %q (must be \"issue\" or \"pull request\")", target)
	}
}

func thenTargetHasStatusComment(w *world.World, target string) error {
	num, err := targetNumber(w, target)
	if err != nil {
		return err
	}
	comments, err := w.SCM.ListComments(context.Background(), w.RepoOwner, w.RepoName, num)
	if err != nil {
		return fmt.Errorf("listing comments on %s #%d: %w", target, num, err)
	}
	for _, c := range comments {
		if statuscomment.IsStatusComment(c.Body) {
			return nil
		}
	}
	return fmt.Errorf("%s #%d does not have a status comment (%d comments checked)", target, num, len(comments))
}

func thenTargetDoesNotHaveStatusComment(w *world.World, target string) error {
	num, err := targetNumber(w, target)
	if err != nil {
		return err
	}
	comments, err := w.SCM.ListComments(context.Background(), w.RepoOwner, w.RepoName, num)
	if err != nil {
		return fmt.Errorf("listing comments on %s #%d: %w", target, num, err)
	}
	for _, c := range comments {
		if statuscomment.IsStatusComment(c.Body) {
			return fmt.Errorf("%s #%d unexpectedly has a status comment: %s", target, num, c.Body)
		}
	}
	return nil
}
