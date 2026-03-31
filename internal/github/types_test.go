package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultAppConfig(t *testing.T) {
	cfg := DefaultAppConfig("my-org")

	assert.Equal(t, "fullsend-my-org", cfg.Name)
	assert.Contains(t, cfg.Description, "my-org")
	assert.Contains(t, cfg.URL, "fullsend")

	// Verify minimum required permissions per acceptance criteria
	assert.Equal(t, "write", cfg.Permissions.Issues, "issues should be read/write")
	assert.Equal(t, "write", cfg.Permissions.PullReqs, "PRs should be read/write")
	assert.Equal(t, "read", cfg.Permissions.Checks, "checks should be read")
	assert.Equal(t, "write", cfg.Permissions.Contents, "contents should be write")

	// Should subscribe to the events needed for the workflow
	assert.Contains(t, cfg.Events, "issues")
	assert.Contains(t, cfg.Events, "issue_comment")
	assert.Contains(t, cfg.Events, "pull_request")
}

func TestDefaultAppConfig_DifferentOrg(t *testing.T) {
	cfg := DefaultAppConfig("acme-corp")

	assert.Equal(t, "fullsend-acme-corp", cfg.Name)
	assert.Contains(t, cfg.Description, "acme-corp")
}
