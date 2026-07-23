package steps

import (
	"github.com/cucumber/godog"
)

// Register binds all step definitions. Steps retrieve their per-scenario
// World from the godog context via world.FromContext.
func Register(sc *godog.ScenarioContext) {
	registerDummyAgentSteps(sc)
	registerTriageSteps(sc)
	registerDispatchSteps(sc)
	registerDispatchCountSteps(sc)
	registerForkSteps(sc)
}
