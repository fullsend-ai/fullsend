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
	cfg.SetKillSwitch(true)
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
	// has no entry; label-added events must still dispatch (shared auth allows label-added).
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

func issueOpenedHarnessYAML() string {
	return `agent: agents/triage.md
role: triage
slug: fullsend-ai-issue-triage
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: |
  event.entity.kind == "work_item"
  && event.transition.kind == "opened"
`
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

func TestDispatch_OwnersUpgradesActorRole(t *testing.T) {
	// Use issue-opened (not label-added) so IsAuthorized requires
	// write-level access. Without the OWNERS approver upgrade the
	// actor's RoleNone would be denied — this proves the OWNERS path
	// is actually exercised.
	ev := mustEvent(t, "issue-opened.json")

	dir := t.TempDir()

	configDir := writeHarnessConfigSubdir(t, dir, issueOpenedHarnessYAML(), func(cfg config.PerRepoConfigWriter) {
		cfg.SetAuthorizationOwnersFile(true)
	})
	ev.Actor.ID = "test-approver"
	ev.Actor.Role = normevent.RoleNone

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "OWNERS"),
		[]byte("approvers:\n  - test-approver\n"),
		0o644,
	))

	refs, err := Dispatch(context.Background(), Options{ConfigDir: configDir, Event: ev})
	require.NoError(t, err)
	require.Len(t, refs, 1, "OWNERS approver upgrade should grant write-level access")
	// The caller's event must NOT be mutated — the OWNERS upgrade is
	// used only for the auth gate, not leaked to downstream CEL or
	// the caller.
	assert.Equal(t, normevent.RoleNone, ev.Actor.Role)
}

func TestDispatch_OwnersReviewerDeniedWriteLevel(t *testing.T) {
	dir := t.TempDir()

	configDir := writeHarnessConfigSubdir(t, dir, issuePingHarnessYAML(), func(cfg config.PerRepoConfigWriter) {
		cfg.SetAuthorizationOwnersFile(true)
	})

	ev := mustEvent(t, "issue-opened.json")
	ev.Actor.ID = "test-reviewer"
	ev.Actor.Role = normevent.RoleNone

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "OWNERS"),
		[]byte("approvers: []\nreviewers:\n  - test-reviewer\n"),
		0o644,
	))

	refs, err := Dispatch(context.Background(), Options{ConfigDir: configDir, Event: ev})
	require.NoError(t, err)
	assert.Empty(t, refs, "OWNERS reviewer should be denied: triage-equivalent does not satisfy write-level harness auth")
}

func TestDispatch_OwnersDisabledNoUpgrade(t *testing.T) {
	// Use issue-opened (not label-added) so IsAuthorized requires
	// write-level access. The OWNERS file lists the actor as an
	// approver, but with the config flag off, OWNERS should not be
	// consulted — the actor stays at RoleNone and is denied.
	ev := mustEvent(t, "issue-opened.json")

	dir := t.TempDir()

	configDir := writeHarnessConfigSubdir(t, dir, issueOpenedHarnessYAML())
	ev.Actor.ID = "test-approver"
	ev.Actor.Role = normevent.RoleNone

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "OWNERS"),
		[]byte("approvers:\n  - test-approver\n"),
		0o644,
	))

	refs, err := Dispatch(context.Background(), Options{ConfigDir: configDir, Event: ev})
	require.NoError(t, err)
	assert.Empty(t, refs, "OWNERS auth is disabled — approver in OWNERS should not grant access")
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

func writeHarnessConfig(t *testing.T, dir, harnessYAML string, opts ...func(config.PerRepoConfigWriter)) {
	t.Helper()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "issue-ping.yaml"), []byte(harnessYAML), 0o644))
	cfg := config.NewPerRepoConfig(nil, "fullsend-ai/demo")
	cfg.SetAgents([]config.AgentEntry{{Name: "issue-ping", Source: "harness/issue-ping.yaml"}})
	for _, opt := range opts {
		opt(cfg)
	}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))
}

// writeHarnessConfigSubdir creates a .fullsend/ subdirectory inside
// repoRoot that mirrors the production layout and returns its path for
// use as ConfigDir. This ensures filepath.Dir(configDir) resolves to
// repoRoot, which is required for OWNERS-file resolution.
func writeHarnessConfigSubdir(t *testing.T, repoRoot, harnessYAML string, opts ...func(config.PerRepoConfigWriter)) string {
	t.Helper()
	configDir := filepath.Join(repoRoot, ".fullsend")
	harnessDir := filepath.Join(configDir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "issue-ping.yaml"), []byte(harnessYAML), 0o644))
	cfg := config.NewPerRepoConfig(nil, "fullsend-ai/demo")
	cfg.SetAgents([]config.AgentEntry{{Name: "issue-ping", Source: "harness/issue-ping.yaml"}})
	for _, opt := range opts {
		opt(cfg)
	}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), data, 0o644))
	return configDir
}
