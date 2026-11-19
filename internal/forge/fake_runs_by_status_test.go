package forge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeClient_ListWorkflowRunsByStatus(t *testing.T) {
	fc := NewFakeClient()
	fc.WorkflowRunsList = map[string][]WorkflowRun{
		"o/r/w.yml": {
			{ID: 1, Status: "in_progress", CreatedAt: "2026-11-17T19:00:00Z"},
			{ID: 2, Status: "completed", Conclusion: "failure", CreatedAt: "2026-11-17T19:00:00Z"},
			{ID: 3, Status: "completed", Conclusion: "success", CreatedAt: "2026-11-17T19:00:00Z"},
			{ID: 4, Status: "in_progress", CreatedAt: "2026-11-17T17:00:00Z"},
			{ID: 5, Status: "in_progress", CreatedAt: "not a time"},
		},
	}
	ctx := context.Background()
	since := time.Date(2026, 11, 17, 18, 0, 0, 0, time.UTC)

	runs, err := fc.ListWorkflowRunsByStatus(ctx, "o", "r", "w.yml", "in_progress", since)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, 1, runs[0].ID)

	runs, err = fc.ListWorkflowRunsByStatus(ctx, "o", "r", "w.yml", "in_progress", time.Time{})
	require.NoError(t, err)
	assert.Len(t, runs, 3, "a zero createdSince does not filter by time")

	runs, err = fc.ListWorkflowRunsByStatus(ctx, "o", "r", "w.yml", "failure", since)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, 2, runs[0].ID)

	fc.Errors = map[string]error{"ListWorkflowRuns": errors.New("list boom")}
	_, err = fc.ListWorkflowRunsByStatus(ctx, "o", "r", "w.yml", "failure", since)
	require.ErrorContains(t, err, "list boom")
}
