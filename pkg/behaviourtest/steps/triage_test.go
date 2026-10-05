package steps

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestWhenIssueLabeled_RequiresIssue(t *testing.T) {
	err := whenIssueLabeled(&world.World{}, "ready-for-triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no issue created")
}

func TestWhenIssueLabeled_TruncatesScenarioStartToSecondPrecision(t *testing.T) {
	// Regression (#7957 review): forge-reported workflow/pipeline
	// CreatedAt timestamps have second precision, but an unnormalized
	// time.Now() carries a fractional component. A run created in the
	// same wall-clock second as this trigger would then parse as earlier
	// than an untruncated boundary and be rejected by every creation-time
	// filter derived from it (CountHarnessDispatches,
	// WaitForHarnessAgentRound) on every retry, not just the first.
	// whenIssueLabeled must truncate ScenarioStart to second precision so
	// the boundary can never exceed a same-second forge timestamp.
	w := &world.World{IssueNumber: 1, SCM: &fakeDispatchSCM{}}
	require.NoError(t, whenIssueLabeled(w, "ready-for-triage"))
	assert.Equal(t, w.ScenarioStart.Truncate(time.Second), w.ScenarioStart,
		"ScenarioStart should already be truncated to whole-second precision")
	assert.Zero(t, w.ScenarioStart.Nanosecond())
}
