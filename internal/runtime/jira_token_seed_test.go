package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

func TestJiraTokenSeed_Paths(t *testing.T) {
	s := JiraTokenSeed()
	assert.Equal(t, "JIRA_TOKEN", s.PlaceholderEnv)
	assert.Equal(t, sandbox.SandboxJiraTokenFile, s.File)
	assert.Contains(t, s.Seed, sandbox.SandboxJiraTokenFile)
	assert.False(t, s.IsZero())
}

// TestJiraTokenSeedFragment runs the seed under a real sh: a gateway
// placeholder lands in the file followed by a newline; a real token, an
// unset variable, another credential's placeholder, or one with shell
// metacharacters is refused and nothing is written.
func TestJiraTokenSeedFragment(t *testing.T) {
	requireSeedLock(t)
	dir := filepath.Join(t.TempDir(), "fullsend-jira")
	file := filepath.Join(dir, "token")
	seed := jiraTokenSeedFragment(dir, file)

	good := piPlaceholderPrefix + "v123_JIRA_TOKEN"
	cmd := exec.Command("sh", "-c", seed)
	cmd.Env = append(os.Environ(), "JIRA_TOKEN="+good)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, good+"\n", string(data))

	require.NoError(t, os.Remove(file))
	for _, in := range []string{"ATATT3xFfGF0-real-token", "", piPlaceholderPrefix + "v1_GH_TOKEN", piPlaceholderPrefix + "v1;rm_JIRA_TOKEN"} {
		cmd := exec.Command("sh", "-c", seed)
		cmd.Env = append(os.Environ(), "JIRA_TOKEN="+in)
		out, err := cmd.CombinedOutput()
		require.Error(t, err, "%q must be refused: %s", in, out)
		_, statErr := os.Stat(file)
		assert.True(t, os.IsNotExist(statErr), "nothing written for %q", in)
	}
}
