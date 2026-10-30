package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerStatusCommentSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the (issue|pull request) does not have a status (?:comment|marker)$`, func(ctx context.Context, target string) (context.Context, error) {
		return ctx, thenTargetDoesNotHaveStatusComment(world.FromContext(ctx), target)
	})
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
