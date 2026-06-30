package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreflightGitHubResult_SkippedFields(t *testing.T) {
	// Verify the result struct reports skip reasons correctly.
	r := &preflightGitHubResult{Skipped: true, SkipReason: "GH_TOKEN not set in sandbox"}
	assert.True(t, r.Skipped)
	assert.Equal(t, "GH_TOKEN not set in sandbox", r.SkipReason)
}

func TestPreflightGitHubResult_NotSkipped(t *testing.T) {
	r := &preflightGitHubResult{}
	assert.False(t, r.Skipped)
	assert.Empty(t, r.SkipReason)
}

func TestPreflightGitHubTimeout(t *testing.T) {
	// Ensure the timeout constant is set to a reasonable value.
	require.Greater(t, preflightGitHubTimeout.Seconds(), float64(0))
	require.LessOrEqual(t, preflightGitHubTimeout.Seconds(), float64(60))
}
