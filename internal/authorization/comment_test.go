package authorization

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStickyMarker(t *testing.T) {
	assert.Equal(t, "<!-- fullsend:auth:workflow-change -->", StickyMarker(workflowChangeGate))
}

func TestCommentBody(t *testing.T) {
	assert.Contains(t, CommentBody(workflowChangeGate, PhasePreRun, StatusBlocked), "workflow-change-allowed")
	assert.Contains(t, CommentBody(workflowChangeGate, PhaseMint, StatusBlocked), "workflows: write")
	assert.Contains(t, CommentBody(workflowChangeGate, PhasePrePush, StatusUnauthorizedPush), "Workflow file push blocked")
	assert.Contains(t, CommentBody(workflowChangeGate, PhasePreRun, StatusStale), "authorization expired")
	assert.Empty(t, CommentBody(workflowChangeGate, PhasePreRun, StatusOK))
}

func TestParseChangedFiles(t *testing.T) {
	files := ParseChangedFiles("  src/a.go\n\n.github/workflows/ci.yml \n")
	assert.Equal(t, []string{"src/a.go", ".github/workflows/ci.yml"}, files)
}

func TestMatchesAnyInList(t *testing.T) {
	patterns := WorkflowFilePatterns()
	assert.True(t, MatchesAnyInList([]string{"README.md", ".github/workflows/ci.yml"}, patterns))
	assert.False(t, MatchesAnyInList([]string{"", "README.md"}, patterns))
}
