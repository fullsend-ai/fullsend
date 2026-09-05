package suite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v21"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/steps"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// InitScenario registers tag-based skips, Before/After hooks, and shared steps.
// Each scenario receives its own World cloned from template. The unified
// install.Driver on template.Driver handles repo creation/deletion;
// scenarios that need a repo call CreateRepo in a step (e.g. "Given a
// test repository with fullsend installed"), and the After hook deletes
// the repo on cleanup.
func InitScenario(sc *godog.ScenarioContext, template *world.World) {
	sc.Before(func(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
		return beforeScenario(ctx, scenario, tagNames(scenario.Tags), template)
	})
	sc.After(func(ctx context.Context, scenario *godog.Scenario, err error) (context.Context, error) {
		return afterScenario(ctx, err)
	})
	steps.Register(sc)
}

func beforeScenario(ctx context.Context, scenario *godog.Scenario, tags []string, template *world.World) (context.Context, error) {
	if err := SkipErrorForTagNames(tags, template); err != nil {
		return ctx, err
	}
	w := template.Clone()
	resetScenarioWorld(w)
	w.ScenarioName = scenario.Name

	ctx = world.WithWorld(ctx, w)
	return ctx, nil
}

// afterScenario runs scenario cleanup: deletes all repos created during
// the scenario (main, fork, URL harness). Cleanup errors are logged but
// never fail the test — only step failures should drive the test result.
func afterScenario(ctx context.Context, scenarioErr error) (context.Context, error) {
	w := world.FromContext(ctx)
	if w == nil {
		return ctx, scenarioErr
	}
	if err := steps.CleanupScenario(w); err != nil && w.Logf != nil {
		w.Logf("cleanup: %v", err)
	}
	return ctx, scenarioErr
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
	w.URLHarnessRepoOwner = ""
	w.URLHarnessRepoName = ""
	w.URLBaseHarnesses = nil
	w.RecordedBranchSHAs = nil
	w.CreatedBranches = nil
	w.CreatedPRNumbers = nil
	w.RepoOwner = w.Org
	w.RepoName = ""
	w.RepoFull = ""
	w.ScenarioName = ""
	w.JiraMockServer = nil
	w.JiraMockState = nil
	w.JiraConfigDir = ""
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
		case name == "skip:gitlab" && w.Config.SCM == "gitlab":
			return godog.ErrSkip
		case strings.HasPrefix(name, "requires:capability:"):
			capability := strings.TrimPrefix(name, "requires:capability:")
			if capability == "" {
				return fmt.Errorf("malformed tag %q: requires:capability: needs a name", tag)
			}
			if !w.Config.HasCapability(capability) {
				return godog.ErrSkip
			}
		}
	}
	return nil
}
