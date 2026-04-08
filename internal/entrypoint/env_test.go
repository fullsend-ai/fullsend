package entrypoint

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writePayloadFile writes JSON content to a temp file and returns its path.
func writePayloadFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	err := os.WriteFile(path, []byte(content), 0o600)
	require.NoError(t, err)
	return path
}

// setRequiredEnv sets all required environment variables for LoadEnv and
// returns a payload file path using client_payload.issue_number.
func setRequiredEnv(t *testing.T, issueNumber int) string {
	t.Helper()
	t.Setenv("GITHUB_REPOSITORY", "acme-org/my-repo")
	t.Setenv("GITHUB_WORKSPACE", "/workspace")
	t.Setenv("FULLSEND_CODE_BOT_TOKEN", "ghp_test_token")

	payload := fmt.Sprintf(`{"client_payload":{"issue_number":%d}}`, issueNumber)
	path := writePayloadFile(t, payload)
	t.Setenv("GITHUB_EVENT_PATH", path)
	return path
}

func TestLoadEnv_HappyPath(t *testing.T) {
	setRequiredEnv(t, 42)

	env, err := LoadEnv()
	require.NoError(t, err)
	require.NotNil(t, env)

	assert.Equal(t, "acme-org", env.Owner)
	assert.Equal(t, "my-repo", env.Repo)
	assert.Equal(t, 42, env.IssueNumber)
	assert.Equal(t, "/workspace", env.Workspace)
	assert.Equal(t, "ghp_test_token", env.BotToken)
	assert.Equal(t, "agents", env.AgentDir)
	assert.Empty(t, env.DefaultBranch)
}

func TestLoadEnv_InputsPayload(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "acme-org/my-repo")
	t.Setenv("GITHUB_WORKSPACE", "/workspace")
	t.Setenv("FULLSEND_CODE_BOT_TOKEN", "ghp_test_token")

	payload := `{"inputs":{"issue_number":"99"}}`
	path := writePayloadFile(t, payload)
	t.Setenv("GITHUB_EVENT_PATH", path)

	env, err := LoadEnv()
	require.NoError(t, err)
	assert.Equal(t, 99, env.IssueNumber)
}

func TestLoadEnv_MissingRepository(t *testing.T) {
	t.Setenv("GITHUB_WORKSPACE", "/workspace")
	t.Setenv("FULLSEND_CODE_BOT_TOKEN", "ghp_test_token")
	// GITHUB_REPOSITORY intentionally not set

	_, err := LoadEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GITHUB_REPOSITORY")
}

func TestLoadEnv_MissingWorkspace(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "acme-org/my-repo")
	t.Setenv("FULLSEND_CODE_BOT_TOKEN", "ghp_test_token")
	// GITHUB_WORKSPACE intentionally not set

	_, err := LoadEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GITHUB_WORKSPACE")
}

func TestLoadEnv_MissingBotToken(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "acme-org/my-repo")
	t.Setenv("GITHUB_WORKSPACE", "/workspace")
	// FULLSEND_CODE_BOT_TOKEN intentionally not set

	_, err := LoadEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FULLSEND_CODE_BOT_TOKEN")
}

func TestLoadEnv_MissingEventPath(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "acme-org/my-repo")
	t.Setenv("GITHUB_WORKSPACE", "/workspace")
	t.Setenv("FULLSEND_CODE_BOT_TOKEN", "ghp_test_token")
	// GITHUB_EVENT_PATH intentionally not set

	_, err := LoadEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GITHUB_EVENT_PATH")
}

func TestLoadEnv_InvalidPayload(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "acme-org/my-repo")
	t.Setenv("GITHUB_WORKSPACE", "/workspace")
	t.Setenv("FULLSEND_CODE_BOT_TOKEN", "ghp_test_token")

	path := writePayloadFile(t, `not valid json`)
	t.Setenv("GITHUB_EVENT_PATH", path)

	_, err := LoadEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse event payload")
}

func TestLoadEnv_MissingIssueNumber(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "acme-org/my-repo")
	t.Setenv("GITHUB_WORKSPACE", "/workspace")
	t.Setenv("FULLSEND_CODE_BOT_TOKEN", "ghp_test_token")

	path := writePayloadFile(t, `{"something_else":{"key":"value"}}`)
	t.Setenv("GITHUB_EVENT_PATH", path)

	_, err := LoadEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issue number")
}

func TestLoadEnv_CustomAgentDir(t *testing.T) {
	setRequiredEnv(t, 7)
	t.Setenv("FULLSEND_AGENT_DIR", "custom-agents")

	env, err := LoadEnv()
	require.NoError(t, err)
	assert.Equal(t, "custom-agents", env.AgentDir)
}

func TestLoadEnv_DefaultBranch(t *testing.T) {
	setRequiredEnv(t, 7)
	t.Setenv("FULLSEND_DEFAULT_BRANCH", "main")

	env, err := LoadEnv()
	require.NoError(t, err)
	assert.Equal(t, "main", env.DefaultBranch)
}
