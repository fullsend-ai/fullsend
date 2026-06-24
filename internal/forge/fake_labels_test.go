package forge

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeClient_IssueLabels(t *testing.T) {
	ctx := context.Background()
	client := NewFakeClient()
	client.Issues = map[string]Issue{
		"o/r/1": {Number: 1, Labels: []string{"bug"}},
	}

	issue, err := client.GetIssue(ctx, "o", "r", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"bug"}, issue.Labels)

	require.NoError(t, client.AddIssueLabels(ctx, "o", "r", 1, "workflow-change-allowed"))
	issue, err = client.GetIssue(ctx, "o", "r", 1)
	require.NoError(t, err)
	assert.Contains(t, issue.Labels, "workflow-change-allowed")

	ts, err := client.GetLabelAppliedAt(ctx, "o", "r", 1, "workflow-change-allowed")
	require.NoError(t, err)
	assert.False(t, ts.IsZero())

	require.NoError(t, client.RemoveIssueLabel(ctx, "o", "r", 1, "workflow-change-allowed"))
	issue, err = client.GetIssue(ctx, "o", "r", 1)
	require.NoError(t, err)
	assert.NotContains(t, issue.Labels, "workflow-change-allowed")
}

func TestFakeClient_GetLabelAppliedAtNotFound(t *testing.T) {
	client := NewFakeClient()
	client.Issues = map[string]Issue{"o/r/1": {Number: 1}}

	_, err := client.GetLabelAppliedAt(context.Background(), "o", "r", 1, "missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFakeClient_GetCommentAuthorAssociationDefault(t *testing.T) {
	client := NewFakeClient()
	client.CommentAssociations = map[int]string{5: "NONE"}

	assoc, err := client.GetCommentAuthorAssociation(context.Background(), "o", "r", 1, 5)
	require.NoError(t, err)
	assert.Equal(t, "NONE", assoc)

	assoc, err = client.GetCommentAuthorAssociation(context.Background(), "o", "r", 1, 99)
	require.NoError(t, err)
	assert.Equal(t, "MEMBER", assoc)
}

func TestFakeClient_LabelAppliedAtTimestamp(t *testing.T) {
	client := NewFakeClient()
	client.Issues = map[string]Issue{"o/r/1": {Number: 1, Labels: []string{}}}
	client.LabelAppliedAt = map[string]time.Time{
		"o/r/1/workflow-change-allowed": time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC),
	}

	ts, err := client.GetLabelAppliedAt(context.Background(), "o", "r", 1, "workflow-change-allowed")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC), ts.UTC())
}
