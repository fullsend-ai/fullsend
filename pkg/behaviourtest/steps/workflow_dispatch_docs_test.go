package steps

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The behaviour-test guides describe the dispatch steps: the scenario
// author's guide documents the steps themselves and the driver guide
// documents the optional interface that backs them. Follows the
// doc-drift test precedent in internal/agentnew/trigger_fixtures_test.go.
func TestWorkflowDispatchSteps_DocumentedInGuides(t *testing.T) {
	t.Parallel()

	scenarioGuide, err := os.ReadFile("../../../docs/guides/dev/behaviour-testing.md")
	require.NoError(t, err)
	const heading = "### Workflow dispatch steps"
	idx := strings.Index(string(scenarioGuide), heading)
	require.GreaterOrEqual(t, idx, 0, "the scenario guide has no workflow dispatch steps section")
	section := string(scenarioGuide)[idx:]
	assert.Contains(t, section, "workflow is dispatched with event payload")
	assert.Contains(t, section, "workflow fails with")
	assert.Contains(t, section, "in its logs")

	driverGuide, err := os.ReadFile("../../../docs/guides/dev/behaviour-drivers.md")
	require.NoError(t, err)
	assert.Contains(t, string(driverGuide), "ci.WorkflowDispatcher")
	assert.Contains(t, string(driverGuide), "DispatchWorkflow")
	assert.Contains(t, string(driverGuide), "WaitForWorkflowConclusion")
}
