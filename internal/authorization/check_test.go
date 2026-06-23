package authorization

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestMatchesAny_WorkflowPatterns(t *testing.T) {
	patterns := WorkflowFilePatterns()
	assert.True(t, MatchesAny(".github/workflows/ci.yml", patterns))
	assert.True(t, MatchesAny(".github/workflows/nested/job.yml", patterns))
	assert.False(t, MatchesAny("src/main.go", patterns))
	assert.False(t, MatchesAny(".github/CODEOWNERS", patterns))
}

func TestEvaluate_PreRunBlocked(t *testing.T) {
	client := forge.NewFakeClient()
	client.Issues = map[string]forge.Issue{
		"o/r/1": {Number: 1, Labels: []string{"workflow-change-needed"}},
	}

	result, err := Evaluate(t.Context(), client, workflowChangeGate, Target{Owner: "o", Repo: "r", Number: 1}, PhasePreRun, Options{})
	require.NoError(t, err)
	assert.Equal(t, StatusBlocked, result.Status)
}

func TestEvaluate_MintElevationWhenAllowed(t *testing.T) {
	client := forge.NewFakeClient()
	client.Issues = map[string]forge.Issue{
		"o/r/1": {Number: 1, Labels: []string{"workflow-change-allowed"}},
	}
	client.LabelAppliedAt = map[string]time.Time{
		"o/r/1/workflow-change-allowed": time.Now().Add(-time.Hour),
	}

	result, err := Evaluate(t.Context(), client, workflowChangeGate, Target{Owner: "o", Repo: "r", Number: 1}, PhaseMint, Options{})
	require.NoError(t, err)
	assert.Equal(t, StatusOK, result.Status)
	assert.Equal(t, []string{"workflow-change"}, result.Elevations)
}

func TestEvaluate_PrePushUnauthorized(t *testing.T) {
	client := forge.NewFakeClient()
	client.Issues = map[string]forge.Issue{
		"o/r/1": {Number: 1, Labels: []string{}},
	}

	result, err := Evaluate(t.Context(), client, workflowChangeGate, Target{Owner: "o", Repo: "r", Number: 1}, PhasePrePush, Options{
		ChangedFiles: []string{".github/workflows/ci.yml"},
	})
	require.NoError(t, err)
	assert.Equal(t, StatusUnauthorizedPush, result.Status)
}

func TestCheckStale_NonCollaboratorComment(t *testing.T) {
	client := forge.NewFakeClient()
	allowedAt := time.Now().Add(-time.Hour)
	client.IssueComments = map[string][]forge.IssueComment{
		"o/r/1": {{
			ID:        99,
			Body:      "please /fs-code again",
			CreatedAt: allowedAt.Add(time.Minute).Format(time.RFC3339),
		}},
	}
	client.CommentAssociations = map[int]string{99: "NONE"}

	stale, err := CheckStale(t.Context(), client, "o", "r", 1, workflowChangeGate, allowedAt, 0)
	require.NoError(t, err)
	assert.True(t, stale)
}

func TestIsAgentInfluencingComment(t *testing.T) {
	assert.True(t, IsAgentInfluencingComment("try /fs-fix please"))
	assert.False(t, IsAgentInfluencingComment("looks good to me"))
}
