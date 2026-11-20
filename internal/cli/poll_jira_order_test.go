package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The behaviour step "fullsend poll runs the Jira poller with target repo"
// (pkg/behaviourtest/steps/jirapoll.go) writes tracker.jira with a
// syntactically valid HTTPS base_url (https://jira.invalid) and runs the real
// CLI. The step's own unit tests substitute a shell script for the binary and
// the .feature scenario runs only in the behaviour job, so nothing else pins
// the chain the refusal scenario depends on: the config shape the step writes
// passes loadJiraPollSettings — whose connection validation requires an https
// URL and runs before the argument checks — so a target repo containing ".."
// reaches the refusal in validateJiraPollArgs rather than failing on the URL.

// stepJiraPollConfig is the tracker.jira mapping the behaviour step writes,
// verbatim (writeJiraPollTrackerConfig).
const stepJiraPollConfig = `version: "1"
tracker:
  jira:
    base_url: https://jira.invalid
    project: PROJ
    poll:
      enabled: true
`

func TestJiraPollStepConfig_ReachesTargetRepoRefusal(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(stepJiraPollConfig), 0o600))

	// runJiraPoll loads and validates the config before the argument checks;
	// the step's config shape must survive that to reach the target-repo one.
	settings, err := loadJiraPollSettings(dir, "", "", "")
	require.NoError(t, err)

	_, err = validateJiraPollArgs(settings, "test-org/..", filepath.Join(t.TempDir(), "dispatches.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain '..'")
}

// A plain-HTTP base_url (the shape the mock Jira server serves) is refused by
// loadJiraPollSettings before the argument checks run, which is why the step
// writes https://jira.invalid instead of the mock's URL: the refusal scenario
// would otherwise fail on the URL, not on the target repo under test.
func TestJiraPollStepConfig_PlainHTTPBaseURLRefusedBeforeArgs(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(stepJiraPollConfig, "https://jira.invalid", "http://127.0.0.1:1", 1)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600))

	_, err := loadJiraPollSettings(dir, "", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be an https URL")
}
