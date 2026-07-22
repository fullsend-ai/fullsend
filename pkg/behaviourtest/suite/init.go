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
// Each scenario receives its own World cloned from template. If pool is non-nil,
// a repo name is leased from it for the scenario's duration.
func InitScenario(sc *godog.ScenarioContext, template *world.World, pool *world.RepoPool) {
	sc.Before(func(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
		return beforeScenario(ctx, tagNames(scenario.Tags), template, pool)
	})
	sc.After(func(ctx context.Context, scenario *godog.Scenario, err error) (context.Context, error) {
		return afterScenario(ctx, pool, err)
	})
	steps.Register(sc)
}

// beforeScenario clones the template World, resets scenario fields, and
// optionally acquires a pool lease. Extracted for unit testing without
// live godog infrastructure.
func beforeScenario(ctx context.Context, tags []string, template *world.World, pool *world.RepoPool) (context.Context, error) {
	if err := SkipErrorForTagNames(tags, template); err != nil {
		return ctx, err
	}
	w := template.Clone()
	resetScenarioWorld(w)

	if pool != nil {
		name, err := pool.Acquire(ctx)
		if err != nil {
			return ctx, fmt.Errorf("acquiring pool repo name: %w", err)
		}
		w.LeasedRepoName = name
	}

	ctx = world.WithWorld(ctx, w)
	return ctx, nil
}

// afterScenario runs scenario cleanup and releases the pool lease.
// Extracted for unit testing. Release errors are surfaced as test
// failures rather than panicking the godog runner.
//
// pool.Release is deferred so the lease is returned even if
// CleanupScenario panics. Named return values allow the deferred
// closure to surface a release error when no scenario error exists.
func afterScenario(ctx context.Context, pool *world.RepoPool, scenarioErr error) (_ context.Context, retErr error) {
	retErr = scenarioErr
	w := world.FromContext(ctx)
	if w == nil {
		return ctx, retErr
	}
	if pool != nil && w.LeasedRepoName != "" {
		name := w.LeasedRepoName
		defer func() {
			if releaseErr := pool.Release(name); releaseErr != nil {
				if w.Logf != nil {
					w.Logf("releasing pool repo name: %v", releaseErr)
				}
				if retErr == nil {
					retErr = fmt.Errorf("releasing pool repo name: %w", releaseErr)
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
	w.LeasedRepoName = ""
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
