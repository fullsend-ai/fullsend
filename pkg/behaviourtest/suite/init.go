package suite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v21"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/steps"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// InitScenario registers tag-based skips, Before/After hooks, and shared steps.
// Each scenario receives its own World cloned from template. The unified
// install.Driver on template.Driver handles repo allocation/deallocation;
// scenarios that need a repo call AllocateRepo in a step (e.g. "Given the
// enrolled test repository"), and the After hook deallocates on cleanup.
func InitScenario(sc *godog.ScenarioContext, template *world.World) {
	sc.Before(func(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
		return beforeScenario(ctx, tagNames(scenario.Tags), template)
	})
	sc.After(func(ctx context.Context, scenario *godog.Scenario, err error) (context.Context, error) {
		return afterScenario(ctx, template.Driver, err)
	})
	steps.Register(sc)
}

// beforeScenario clones the template World, resets scenario fields.
// Repo allocation is handled by the step (via Driver.AllocateRepo),
// not by the Before hook.
func beforeScenario(ctx context.Context, tags []string, template *world.World) (context.Context, error) {
	if err := SkipErrorForTagNames(tags, template); err != nil {
		return ctx, err
	}
	w := template.Clone()
	resetScenarioWorld(w)

	ctx = world.WithWorld(ctx, w)
	return ctx, nil
}

// afterScenario runs scenario cleanup and deallocates the repo if one was
// allocated. Deallocation errors are surfaced as test failures rather than
// panicking the godog runner.
//
// driver.DeallocateRepo is deferred so the lease is returned even if
// steps.CleanupScenario panics. Named return values allow the deferred
// closure to surface a deallocation error when no scenario error exists.
func afterScenario(ctx context.Context, driver install.Driver, scenarioErr error) (_ context.Context, retErr error) {
	retErr = scenarioErr
	w := world.FromContext(ctx)
	if w == nil {
		return ctx, retErr
	}
	if driver != nil && w.LeasedRepoName != "" {
		name := w.LeasedRepoName
		defer func() {
			if deallocErr := driver.DeallocateRepo(ctx, name); deallocErr != nil {
				if w.Logf != nil {
					w.Logf("deallocating repo: %v", deallocErr)
				}
				if retErr == nil {
					retErr = fmt.Errorf("deallocating repo: %w", deallocErr)
				}
			}
		}()
	}
	steps.CleanupScenario(w)
	return ctx, retErr
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
	w.LeasedRepoName = ""
	w.KillSwitchActivated = false
	w.RuntimeOverridden = false
	w.RuntimeOriginal = ""
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
		case name == "skip:per-org" && w.Config.InstallMode == "per-org":
			return godog.ErrSkip
		case name == "skip:per-repo" && w.Config.InstallMode == "per-repo":
			return godog.ErrSkip
		case name == "requires:per-repo" && w.Config.InstallMode != "per-repo":
			return godog.ErrSkip
		case name == "skip:gitlab" && w.Config.SCM == "gitlab":
			return godog.ErrSkip
		case strings.HasPrefix(name, "requires:capability:"):
			// Skip unless the runner declares the capability via
			// BEHAVIOUR_CAPABILITIES. Gates scenarios that assert
			// behavior only present past a dependency version, so CI
			// stays green until the dependency ships and the runner
			// opts in.
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
