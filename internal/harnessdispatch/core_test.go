package harnessdispatch

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
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

// captureLog redirects the standard logger for the duration of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// readyForPingEvent returns a GitHub label-added event matched by
// issuePingHarnessYAML's trigger.
func readyForPingEvent(t *testing.T) *normevent.Event {
	t.Helper()
	ev := mustEvent(t, "ready-to-code-labeled.json")
	ev.Transition.Label.Name = "ready-for-ping"
	ev.State.Labels = []string{"ready-for-ping"}
	return ev
}

func TestDispatch_GitHubBuiltinAgentDropped(t *testing.T) {
	for _, name := range []string{"review", "Review", "CODE"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeNamedHarnessConfig(t, dir, name, issuePingHarnessYAML())
			logs := captureLog(t)

			ev := readyForPingEvent(t)
			require.Equal(t, normevent.SystemGitHub, ev.Source.System)
			refs, err := Dispatch(context.Background(), Options{ConfigDir: dir, Event: ev})
			require.NoError(t, err)
			assert.Empty(t, refs, "built-in agents are routed by GitHub stage jobs, not CEL dispatch")
			assert.Contains(t, logs.String(),
				"harness dispatch: "+name+" is a built-in agent; its GitHub stage routing handles this event")
		})
	}
}

func TestDispatch_GitHubCustomAgentNotDropped(t *testing.T) {
	dir := t.TempDir()
	writeNamedHarnessConfig(t, dir, "review-extra", issuePingHarnessYAML())
	logs := captureLog(t)

	refs, err := Dispatch(context.Background(), Options{ConfigDir: dir, Event: readyForPingEvent(t)})
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "review-extra", refs[0].Agent)
	assert.NotContains(t, logs.String(), "is a built-in agent")
}

func TestDropGitHubRoutedBuiltins(t *testing.T) {
	matched := []TriggeredHarness{{Name: "code"}, {Name: "issue-ping"}, {Name: "Triage"}}
	captureLog(t)

	gh := &normevent.Event{Source: normevent.Source{System: normevent.SystemGitHub}}
	kept := dropGitHubRoutedBuiltins(matched, gh)
	require.Len(t, kept, 1)
	assert.Equal(t, "issue-ping", kept[0].Name)

	for _, sys := range []normevent.SourceSystem{normevent.SystemJira, normevent.SystemGitLab} {
		ev := &normevent.Event{Source: normevent.Source{System: sys}}
		assert.Equal(t, matched, dropGitHubRoutedBuiltins(matched, ev), "source %s must not be filtered", sys)
	}
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

func issueOpenedCoderHarnessYAML() string {
	return `agent: agents/code.md
role: coder
slug: fullsend-ai-issue-code
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
	// issue-opened (not label-added) with a role: coder harness requires
	// write. RoleNone is denied without the OWNERS approver upgrade, so
	// this proves the approver -> write upgrade is applied.
	ev := mustEvent(t, "issue-opened.json")

	dir := t.TempDir()

	configDir := writeHarnessConfigSubdir(t, dir, issueOpenedCoderHarnessYAML(), func(cfg config.PerRepoConfigWriter) {
		cfg.SetOwnersFileAuthEnabled(true)
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

	// issue-opened matches this coder harness, so write-level auth is the only gate.
	configDir := writeHarnessConfigSubdir(t, dir, issueOpenedCoderHarnessYAML(), func(cfg config.PerRepoConfigWriter) {
		cfg.SetOwnersFileAuthEnabled(true)
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

func TestDispatch_OwnersReviewerAllowedObservation(t *testing.T) {
	dir := t.TempDir()

	// The OWNERS reviewer upgrade yields triage, which satisfies the
	// observation threshold of a role: triage harness.
	configDir := writeHarnessConfigSubdir(t, dir, issueOpenedHarnessYAML(), func(cfg config.PerRepoConfigWriter) {
		cfg.SetOwnersFileAuthEnabled(true)
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
	require.Len(t, refs, 1, "OWNERS reviewer should satisfy the triage threshold of an observation harness")
	assert.Equal(t, normevent.RoleNone, ev.Actor.Role)
}

func TestDispatch_PerHarnessRoleThreshold(t *testing.T) {
	harnesses := map[string]string{
		"issue-triage": issueOpenedHarnessYAML(),
		"issue-code":   issueOpenedCoderHarnessYAML(),
	}
	tests := []struct {
		name      string
		actorRole normevent.ActorRole
		want      []string
	}{
		{name: "triage actor dispatches observation only", actorRole: normevent.RoleTriage, want: []string{"issue-triage"}},
		{name: "write actor dispatches both", actorRole: normevent.RoleWrite, want: []string{"issue-code", "issue-triage"}},
		{name: "read actor dispatches neither", actorRole: normevent.RoleRead},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeHarnessesConfig(t, dir, harnesses)

			ev := mustEvent(t, "issue-opened.json")
			ev.Actor.Role = tc.actorRole

			refs, err := Dispatch(context.Background(), Options{ConfigDir: dir, Event: ev})
			require.NoError(t, err)
			var got []string
			for _, r := range refs {
				got = append(got, r.Agent)
			}
			sort.Strings(got)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDispatch_OwnersDisabledNoUpgrade(t *testing.T) {
	// Use issue-opened (not label-added) so the role threshold applies.
	// The OWNERS file lists the actor as an approver, but with the
	// config flag off, OWNERS is not consulted — the actor stays at
	// RoleNone and is denied.
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

func TestDispatch_AllHarnessesUnresolvable(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.SetAgents([]config.AgentEntry{{Name: "code", Source: "code.yaml"}})
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	// Label-added events are authorized without a write role, so we reach
	// harness enumeration rather than returning an empty auth-deny result.
	ev := mustEvent(t, "ready-to-code-labeled.json")
	refs, err := Dispatch(context.Background(), Options{ConfigDir: dir, Event: ev})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no agents could be loaded")
	assert.Contains(t, err.Error(), "code")
	assert.Empty(t, refs)
}

func TestMergedConfigAgents_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(":\n- bad"), 0o644))
	_, err := MergedConfigAgents(dir)
	require.Error(t, err)
}

func writeHarnessConfig(t *testing.T, dir, harnessYAML string, opts ...func(config.PerRepoConfigWriter)) {
	t.Helper()
	writeNamedHarnessConfig(t, dir, "issue-ping", harnessYAML, opts...)
}

// writeNamedHarnessConfig registers a single sourced agent called name whose
// harness file contains harnessYAML.
func writeNamedHarnessConfig(t *testing.T, dir, name, harnessYAML string, opts ...func(config.PerRepoConfigWriter)) {
	t.Helper()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "issue-ping.yaml"), []byte(harnessYAML), 0o644))
	cfg := config.NewPerRepoConfig(nil, "fullsend-ai/demo")
	cfg.SetAgents([]config.AgentEntry{{Name: name, Source: "harness/issue-ping.yaml"}})
	for _, opt := range opts {
		opt(cfg)
	}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))
}

// writeHarnessesConfig registers one harness file per entry of harnesses
// (agent name -> harness YAML) in dir.
func writeHarnessesConfig(t *testing.T, dir string, harnesses map[string]string) {
	t.Helper()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	var agents []config.AgentEntry
	for name, harnessYAML := range harnesses {
		require.NoError(t, os.WriteFile(filepath.Join(harnessDir, name+".yaml"), []byte(harnessYAML), 0o644))
		agents = append(agents, config.AgentEntry{Name: name, Source: "harness/" + name + ".yaml"})
	}
	cfg := config.NewPerRepoConfig(nil, "fullsend-ai/demo")
	cfg.SetAgents(agents)
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
	writeHarnessConfig(t, configDir, harnessYAML, opts...)
	return configDir
}

// appendConfigYAML appends top-level YAML to dir/config.yaml.
func appendConfigYAML(t *testing.T, dir, extra string) {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, []byte(extra)...), 0o644))
}

func jiraReadyForPingEvent(t *testing.T) *normevent.Event {
	t.Helper()
	ev := mustEvent(t, "ready-to-code-labeled.json")
	ev.Source.System = normevent.SystemJira
	ev.Transition.Label.Name = "ready-for-ping"
	ev.State.Labels = []string{"ready-for-ping"}
	ev.Actor.Role = normevent.RoleNone
	return ev
}

func TestDispatch_JiraLabelPolicyFromConfig(t *testing.T) {
	tests := []struct {
		name      string
		authYAML  string
		wantAgent bool
	}{
		{name: "no jira provider requires a role", authYAML: "", wantAgent: false},
		{name: "jira_editors", authYAML: "authorization:\n  - provider: jira\n", wantAgent: true},
		{name: "allowlist requires a role", authYAML: "authorization:\n  - provider: jira\n    labels: allowlist\n", wantAgent: false},
		{name: "project_roles requires a role", authYAML: "authorization:\n  - provider: jira\n    labels: project_roles\n", wantAgent: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeHarnessConfig(t, dir, issuePingHarnessYAML())
			appendConfigYAML(t, dir, tt.authYAML)

			refs, err := Dispatch(context.Background(), Options{ConfigDir: dir, Event: jiraReadyForPingEvent(t)})
			require.NoError(t, err)
			if tt.wantAgent {
				require.Len(t, refs, 1)
			} else {
				assert.Empty(t, refs)
			}
		})
	}
}

func TestDispatch_RejectsInvalidAuthorizationConfig(t *testing.T) {
	for name, authYAML := range map[string]string{
		"misspelled provider": "authorization:\n  - provider: jria\n    labels: allowlist\n",
		"duplicate jira":      "authorization:\n  - provider: jira\n  - provider: jira\n    labels: allowlist\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeHarnessConfig(t, dir, issuePingHarnessYAML())
			appendConfigYAML(t, dir, authYAML)

			refs, err := Dispatch(context.Background(), Options{ConfigDir: dir, Event: jiraReadyForPingEvent(t)})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid authorization config")
			assert.Empty(t, refs)
		})
	}
}
