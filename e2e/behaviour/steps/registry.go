package steps

import (
	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/e2e/behaviour/world"
)

func Register(ctx *godog.ScenarioContext, w *world.World) {
	registerDummyAgentSteps(ctx, w)
	registerTriageSteps(ctx, w)
	registerDispatchSteps(ctx, w)
}
