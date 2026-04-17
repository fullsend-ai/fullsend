package scaffold

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFullsendRepoFilesExist(t *testing.T) {
	expected := []string{
		".github/workflows/triage.yml",
		".github/workflows/code.yml",
		".github/workflows/review.yml",
		".github/workflows/repo-maintenance.yml",
		".github/actions/fullsend/action.yml",
		".github/scripts/setup-agent-env.sh",
		"agents/triage.md",
		"env/gcp-vertex.env",
		"env/triage.env",
		"harness/triage.yaml",
		"policies/triage.yaml",
		"scripts/validate-triage.sh",
	}

	for _, path := range expected {
		content, err := FullsendRepoFile(path)
		require.NoError(t, err, "reading %s", path)
		assert.NotEmpty(t, content, "%s should not be empty", path)
	}
}

func TestTargetRepoShimExists(t *testing.T) {
	content, err := TargetRepoFile(".github/workflows/fullsend.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(content), "dispatch-triage")
	assert.Contains(t, string(content), "dispatch-code")
	assert.Contains(t, string(content), "dispatch-review")
}

func TestWalkFullsendRepo(t *testing.T) {
	var paths []string
	err := WalkFullsendRepo(func(path string, content []byte) error {
		paths = append(paths, path)
		return nil
	})
	require.NoError(t, err)
	assert.True(t, len(paths) >= 12, "expected at least 12 files, got %d", len(paths))
}

func TestTriageWorkflowContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/triage.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "workflow_dispatch")
	assert.Contains(t, s, "event_type")
	assert.Contains(t, s, "source_repo")
	assert.Contains(t, s, "event_payload")
	assert.Contains(t, s, "setup-agent-env.sh")
	assert.Contains(t, s, "fullsend")
}

func TestCompositeActionContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/actions/fullsend/action.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "fullsend run")
	assert.Contains(t, s, "openshell")
}

func TestSetupAgentEnvContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/scripts/setup-agent-env.sh")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "AGENT_PREFIX")
	assert.Contains(t, s, "GITHUB_ENV")
}
