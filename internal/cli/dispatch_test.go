package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
)

func TestRunDispatch_JSONDriver(t *testing.T) {
	dir := t.TempDir()
	writeDispatchFixture(t, dir)

	eventPath := filepath.Join(t.TempDir(), "event.json")
	eventJSON := []byte(`{
  "repo": "fullsend-ai/demo",
  "entity": {"kind": "work_item", "id": 42, "url": "https://github.com/fullsend-ai/demo/issues/42"},
  "transition": {"kind": "label_changed", "label": {"name": "ready-for-ping", "action": "added"}},
  "actor": {"id": "alice", "kind": "human", "role": "write", "is_entity_author": false},
  "state": {"labels": ["ready-for-ping"]},
  "source": {"system": "github", "raw_type": "issues", "raw_action": "labeled"}
}`)
	require.NoError(t, os.WriteFile(eventPath, eventJSON, 0o644))

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	errCh := make(chan error, 1)
	go func() {
		defer w.Close()
		errCh <- runDispatch(context.Background(), dispatchOpts{
			inputDriver:  "json",
			outputDriver: "gha-matrix",
			inputFile:    eventPath,
			configDir:    dir,
		})
	}()

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	os.Stdout = old

	require.NoError(t, <-errCh)
	assert.Contains(t, buf.String(), "issue-ping")
}

func TestRunDispatch_UnknownInputDriver(t *testing.T) {
	err := runDispatch(context.Background(), dispatchOpts{inputDriver: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown input driver")
}

func TestRunDispatch_RequiresInputDriver(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")
	err := runDispatch(context.Background(), dispatchOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "input driver required")
}

func TestRunDispatch_JSONOutputDriver(t *testing.T) {
	dir := t.TempDir()
	writeDispatchFixture(t, dir)
	eventPath := filepath.Join(t.TempDir(), "event.json")
	eventJSON := []byte(`{
  "repo": "fullsend-ai/demo",
  "entity": {"kind": "work_item", "id": 42, "url": "https://github.com/fullsend-ai/demo/issues/42"},
  "transition": {"kind": "label_changed", "label": {"name": "ready-for-ping", "action": "added"}},
  "actor": {"id": "alice", "kind": "human", "role": "write", "is_entity_author": false},
  "state": {"labels": ["ready-for-ping"]},
  "source": {"system": "github", "raw_type": "issues", "raw_action": "labeled"}
}`)
	require.NoError(t, os.WriteFile(eventPath, eventJSON, 0o644))

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	errCh := make(chan error, 1)
	go func() {
		defer w.Close()
		errCh <- runDispatch(context.Background(), dispatchOpts{
			inputDriver:  "json",
			outputDriver: "json",
			inputFile:    eventPath,
			configDir:    dir,
		})
	}()

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	os.Stdout = old

	require.NoError(t, <-errCh)
	assert.Contains(t, buf.String(), `"agent": "issue-ping"`)
}

func TestNewDispatchCmd_Flags(t *testing.T) {
	cmd := newDispatchCmd()
	assert.Equal(t, "dispatch", cmd.Use)
	flag := cmd.Flags().Lookup("output-driver")
	require.NotNil(t, flag)
	assert.Equal(t, "gha-matrix", flag.DefValue)
}

func TestRunDispatch_UnknownOutputDriver(t *testing.T) {
	dir := t.TempDir()
	writeDispatchFixture(t, dir)
	eventPath := filepath.Join(t.TempDir(), "event.json")
	eventJSON := []byte(`{
  "repo": "fullsend-ai/demo",
  "entity": {"kind": "work_item", "id": 42, "url": "https://github.com/fullsend-ai/demo/issues/42"},
  "transition": {"kind": "label_changed", "label": {"name": "ready-for-ping", "action": "added"}},
  "actor": {"id": "alice", "kind": "human", "role": "write", "is_entity_author": false},
  "state": {"labels": ["ready-for-ping"]},
  "source": {"system": "github", "raw_type": "issues", "raw_action": "labeled"}
}`)
	require.NoError(t, os.WriteFile(eventPath, eventJSON, 0o644))

	err := runDispatch(context.Background(), dispatchOpts{
		inputDriver:  "json",
		outputDriver: "nope",
		inputFile:    eventPath,
		configDir:    dir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown output driver")
}

func TestRunDispatch_GHAEventMissingEventPath(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")
	err := runDispatch(context.Background(), dispatchOpts{
		inputDriver: "gha-event",
	})
	require.Error(t, err)
}

func writeDispatchFixture(t *testing.T, dir string) {
	t.Helper()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	harnessYAML := `agent: agents/triage.md
role: triage
slug: fullsend-ai-issue-ping
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: |
  event.entity.kind == "work_item"
  && event.transition.kind == "label_changed"
  && event.transition.label.name == "ready-for-ping"
`
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "issue-ping.yaml"), []byte(harnessYAML), 0o644))
	cfg := config.NewPerRepoConfig(nil, "fullsend-ai/demo")
	cfg.Agents = []config.AgentEntry{{Name: "issue-ping", Source: "harness/issue-ping.yaml"}}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))
}
