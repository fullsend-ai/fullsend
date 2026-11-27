package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const forgejoLabeledPayload = `{
  "action": "label_updated",
  "issue": {
    "number": 42,
    "html_url": "https://code.example.com/fullsend-ai/demo/issues/42",
    "user": {"id": 5, "login": "alice"},
    "labels": [{"name": "ready-for-ping"}]
  },
  "label": {"name": "ready-for-ping"},
  "repository": {"full_name": "fullsend-ai/demo"},
  "sender": {"id": 5, "login": "alice"}
}`

func clearForgejoRunnerEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"FORGEJO_ACTIONS", "GITEA_ACTIONS",
		"FORGEJO_EVENT_PATH", "FORGEJO_EVENT_NAME", "FORGEJO_REPOSITORY", "FORGEJO_SERVER_URL",
		"GITHUB_EVENT_PATH", "GITHUB_EVENT_NAME", "GITHUB_REPOSITORY", "GITHUB_SERVER_URL",
	} {
		t.Setenv(k, "")
	}
}

func runDispatchCapture(t *testing.T, opts dispatchOpts) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	errCh := make(chan error, 1)
	go func() {
		defer w.Close()
		errCh <- runDispatch(context.Background(), opts)
	}()

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	os.Stdout = old
	return buf.String(), <-errCh
}

func TestRunDispatch_ForgejoEventDriver(t *testing.T) {
	clearForgejoRunnerEnv(t)
	dir := t.TempDir()
	writeDispatchFixture(t, dir)
	eventPath := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(eventPath, []byte(forgejoLabeledPayload), 0o644))
	t.Setenv("FORGEJO_EVENT_PATH", eventPath)
	t.Setenv("FORGEJO_SERVER_URL", "https://code.example.com")

	out, err := runDispatchCapture(t, dispatchOpts{
		inputDriver:  "forgejo-event",
		outputDriver: "json",
		configDir:    dir,
		repository:   "fullsend-ai/demo",
		eventName:    "issues",
	})
	require.NoError(t, err)
	// No verified Forgejo permission lookup is wired, so the actor role is
	// none and the authorization gate denies the matching harness.
	assert.NotContains(t, out, "issue-ping")
}

func TestRunDispatch_ForgejoRunnerDefaultsToForgejoDriver(t *testing.T) {
	clearForgejoRunnerEnv(t)
	// A GitHub-shaped payload on a Forgejo runner must not be mapped as a
	// GitHub event.
	eventPath := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(eventPath, []byte(`{
  "action": "labeled",
  "issue": {"number": 42, "html_url": "https://github.com/fullsend-ai/demo/issues/42", "labels": [{"name": "ready-for-ping"}]},
  "label": {"name": "ready-for-ping"},
  "repository": {"full_name": "fullsend-ai/demo"},
  "sender": {"login": "alice", "type": "User"}
}`), 0o644))
	t.Setenv("FORGEJO_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_PATH", eventPath)
	t.Setenv("GITHUB_EVENT_NAME", "issues")
	t.Setenv("GITHUB_REPOSITORY", "fullsend-ai/demo")
	t.Setenv("GITHUB_SERVER_URL", "https://code.example.com")

	_, err := runDispatchCapture(t, dispatchOpts{configDir: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forgejo event:")
	assert.Contains(t, err.Error(), "configured Forgejo instance")
}

func TestIsForgejoActionsRunner(t *testing.T) {
	clearForgejoRunnerEnv(t)
	assert.False(t, isForgejoActionsRunner())
	t.Setenv("GITEA_ACTIONS", "true")
	assert.True(t, isForgejoActionsRunner())
	t.Setenv("GITEA_ACTIONS", "")
	t.Setenv("FORGEJO_ACTIONS", "true")
	assert.True(t, isForgejoActionsRunner())
}
