package gitlab

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// TestListWorkflowRunsByStatus_NotSupported verifies that GitLab reports
// the GitHub-only status listing as unsupported without an API call.
func TestListWorkflowRunsByStatus_NotSupported(t *testing.T) {
	client, _ := setupTest(t)
	runs, err := client.ListWorkflowRunsByStatus(context.Background(), "o", "r", "w.yml", "failure", time.Time{})
	require.ErrorIs(t, err, forge.ErrNotSupported)
	assert.Nil(t, runs)
}
