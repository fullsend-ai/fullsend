package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerDispatchCountSteps(ctx *godog.ScenarioContext, w *world.World) {
	ctx.Step(`^the harness "([^"]+)" was dispatched exactly (\d+) time\(s\)$`, func(agent string, expected int) error {
		return thenHarnessDispatchedExactly(w, agent, expected)
	})
}

func thenHarnessDispatchedExactly(w *world.World, agent string, expected int) error {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return fmt.Errorf("harness agent name is required")
	}
	if w.ScenarioStart.IsZero() {
		return fmt.Errorf("no workflow trigger time recorded")
	}
	ctx := context.Background()
	actual, err := w.CI.CountHarnessDispatches(ctx, w.Org, w.Install.TriageWorkflowRepo(), agent, w.ScenarioStart)
	if err != nil {
		return fmt.Errorf("counting harness dispatches: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("expected harness %q to be dispatched exactly %d time(s), but was dispatched %d time(s)", agent, expected, actual)
	}
	return nil
}
