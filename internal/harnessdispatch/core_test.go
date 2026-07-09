package harnessdispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestDispatch_KillSwitch(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.KillSwitch = true
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	ev := mustEvent(t, "ready-to-code-labeled.json")
	refs, err := Dispatch(context.Background(), Options{ConfigDir: dir, Event: ev})
	require.NoError(t, err)
	assert.Empty(t, refs)
}

func TestDispatch_AuthDeny(t *testing.T) {
	dir := t.TempDir()
	writeHarnessConfig(t, dir, issuePingHarnessYAML())

	ev := mustEvent(t, "issue-opened.json")
	refs, err := Dispatch(context.Background(), Options{ConfigDir: dir, Event: ev})
	require.NoError(t, err)
	assert.Empty(t, refs)
}

func TestDispatch_CELIssueMatch(t *testing.T) {
	dir := t.TempDir()
	writeHarnessConfig(t, dir, issuePingHarnessYAML())

	ev := mustEvent(t, "ready-to-code-labeled.json")
	ev.Transition.Label.Name = "ready-for-ping"
	ev.State.Labels = []string{"ready-for-ping"}
	// gha-event maps installation bots to role none when the collaborator API
	// has no entry; label-added events must still dispatch (ADR 0054).
	ev.Actor.Role = normevent.RoleNone

	refs, err := Dispatch(context.Background(), Options{ConfigDir: dir, Event: ev})
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "issue-ping", refs[0].Agent)
}

func TestDispatch_CELIssueDoesNotMatchPR(t *testing.T) {
	dir := t.TempDir()
	writeHarnessConfig(t, dir, issuePingHarnessYAML())

	ev := mustEvent(t, "pr-opened.json")
	refs, err := Dispatch(context.Background(), Options{ConfigDir: dir, Event: ev})
	require.NoError(t, err)
	assert.Empty(t, refs)
}

func TestProjectExecutionRef_Issue(t *testing.T) {
	ev := mustEvent(t, "ready-to-code-labeled.json")
	ref, err := ProjectExecutionRef("issue-ping", "triage", ev)
	require.NoError(t, err)
	assert.Equal(t, "issues", ref.EventType)
	assert.Contains(t, ref.EventPayload, `"issue"`)
}

func examplesDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "docs", "normative", "normalized-event", "v1", "examples")
}

func mustEvent(t *testing.T, name string) *normevent.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(examplesDir(t), name))
	require.NoError(t, err)
	ev, err := normevent.ParseJSON(data)
	require.NoError(t, err)
	return ev
}

func issuePingHarnessYAML() string {
	return `agent: agents/triage.md
role: triage
slug: fullsend-ai-issue-ping
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: |
  event.entity.kind == "work_item"
  && event.transition.kind == "label_changed"
  && event.transition.label.name == "ready-for-ping"
`
}

func TestDispatch_NilEvent(t *testing.T) {
	_, err := Dispatch(context.Background(), Options{ConfigDir: t.TempDir()})
	require.Error(t, err)
}

func TestMergedConfigAgents_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(":\n- bad"), 0o644))
	_, err := MergedConfigAgents(dir)
	require.Error(t, err)
}

func writeHarnessConfig(t *testing.T, dir, harnessYAML string) {
	t.Helper()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "issue-ping.yaml"), []byte(harnessYAML), 0o644))
	cfg := config.NewPerRepoConfig(nil, "fullsend-ai/demo")
	cfg.Agents = []config.AgentEntry{{Name: "issue-ping", Source: "harness/issue-ping.yaml"}}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))
}
