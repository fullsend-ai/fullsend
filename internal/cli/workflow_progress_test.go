package cli

import (
	"strings"
	"testing"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
)

func TestIterationEventHandler_WorkflowProgressIsDisplayOnly(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	const secret = "opaque-workflow-runner-secret-for-test"
	const provider = "opaque-workflow-provider-secret-for-test"
	t.Setenv("GH_WORKFLOW_TOKEN", provider)
	var output strings.Builder
	renderer := agentruntime.NewEventRenderer(ui.New(&output))
	renderer.RedactProgress = func(s string) string { return redactSecretLiterals(s, map[string]string{"PUSH_TOKEN": secret}) }
	collector := newContentCollector(4096)
	tracker, _, agent := toolSpanFixture(t)
	defer agent.End()
	handler := iterationEventHandler(renderer.Handle, collector, tracker)
	handler(agentruntime.TaskProgressEvent{TaskID: "workflow", State: agentruntime.TaskPaused, Label: secret, Detail: provider})
	handler(agentruntime.StatusProgressEvent{Kind: "notice", Label: "Hook", Detail: secret + " " + provider})
	assert.Contains(t, output.String(), "paused")
	assert.Contains(t, output.String(), "Hook")
	assert.NotContains(t, output.String(), secret)
	assert.NotContains(t, output.String(), provider)
	assert.Empty(t, collector.parts, "status metadata must not become model output")
	assert.Zero(t, tracker.created, "status metadata must not fabricate tool spans")
	assert.Empty(t, tracker.open)
}
