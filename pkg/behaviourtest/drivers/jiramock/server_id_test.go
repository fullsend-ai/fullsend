package jiramock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// SetIssueID replaces the synthetic numeric ID an issue was added with, so a
// scenario can serve an issue id fullsend cannot map; an unknown issue key is
// reported as false.
func TestSetIssueIDReplacesID(t *testing.T) {
	t.Parallel()
	srv, state := NewServer()
	defer srv.Close()

	state.AddIssue("PROJ-1", nil)
	assert.True(t, state.SetIssueID("PROJ-1", "not-a-number"))
	assert.False(t, state.SetIssueID("PROJ-404", "1"), "an unknown issue is reported")

	client, err := jira.New("test-token", jira.WithBaseURL(srv.URL))
	require.NoError(t, err)

	issues, err := client.SearchIssues(context.Background(), "project = PROJ", 0)
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "not-a-number", issues[0].ID)
}
