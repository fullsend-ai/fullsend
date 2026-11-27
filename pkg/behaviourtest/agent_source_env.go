package behaviourtest

import (
	"sort"
	"testing"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
)

// applyAgentSourceEnv sets the environment an install.AgentSourceEnvReporter
// driver reports in the runner process, for the rest of the test. The
// fullsend CLI processes the suite starts inherit the process environment,
// so built-in agents resolve from the driver's agent-definition source
// instead of the fullsend-ai/agents GitHub fallback. A driver that is not
// a reporter, or reports nothing, leaves the environment unchanged.
func applyAgentSourceEnv(t testing.TB, driver install.Driver) {
	t.Helper()
	reporter, ok := driver.(install.AgentSourceEnvReporter)
	if !ok {
		return
	}
	env := reporter.AgentSourceEnv()
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Setenv(name, env[name])
		t.Logf("agent source: %s=%s", name, env[name])
	}
}
