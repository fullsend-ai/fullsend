package suite

import (
	"context"
	"strings"
	"time"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v21"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/steps"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// InitScenario registers tag-based skips, Before/After hooks, and shared steps.
func InitScenario(sc *godog.ScenarioContext, w *world.World) {
	sc.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		if err := SkipErrorForTagNames(tagNames(sc.Tags), w); err != nil {
			return ctx, err
		}
		resetScenarioWorld(w)
		return ctx, nil
	})
	sc.After(func(ctx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		steps.CleanupScenario(w)
		return ctx, err
	})
	steps.Register(sc, w)
}

func resetScenarioWorld(w *world.World) {
	w.ScenarioStart = time.Now()
	w.DummyOps = nil
	w.IssueNumber = 0
	w.IssueTitle = ""
	w.PRNumber = 0
	w.DispatchAgent = ""
	w.TriageWorkflow = ""
	w.TriageTriggerEvent = ""
	w.WorkflowRun = nil
	w.ArtifactDir = ""
	w.ForkOwner = ""
	w.ForkRepo = ""
	w.ForkPRNumber = 0
	w.ForkPRBranch = ""
}

func tagNames(tags []*messages.PickleTag) []string {
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, tag.Name)
	}
	return names
}

// SkipErrorForTagNames returns godog.ErrSkip when compatibility tags exclude the scenario.
func SkipErrorForTagNames(tags []string, w *world.World) error {
	for _, tag := range tags {
		name := strings.TrimPrefix(tag, "@")
		switch {
		case name == "skip:per-org" && w.Config.InstallMode == "per-org":
			return godog.ErrSkip
		case name == "skip:per-repo" && w.Config.InstallMode == "per-repo":
			return godog.ErrSkip
		case name == "requires:per-repo" && w.Config.InstallMode != "per-repo":
			return godog.ErrSkip
		case name == "skip:gitlab" && w.Config.SCM == "gitlab":
			return godog.ErrSkip
		}
	}
	return nil
}
