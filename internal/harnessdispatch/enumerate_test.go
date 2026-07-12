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
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestMergedConfigAgents_MissingFile(t *testing.T) {
	agents, err := MergedConfigAgents(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, agents)
}

func TestListTriggeredHarnesses_SkipsEmptyTrigger(t *testing.T) {
	dir := t.TempDir()
	writeHarnessConfig(t, dir, `agent: agents/triage.md
role: triage
slug: no-trigger
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
`)
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.Agents = []config.AgentEntry{{Name: "issue-ping", Source: "harness/issue-ping.yaml"}}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadFromDir(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestListTriggeredHarnesses_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.Agents = []config.AgentEntry{
		{Name: "Ping", Source: "harness/a.yaml"},
		{Name: "ping", Source: "harness/b.yaml"},
	}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadFromDir(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	_, err = ListTriggeredHarnesses(context.Background(), dir, dirCfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent name")
}

func TestListTriggeredHarnesses_MissingHarness(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.Agents = []config.AgentEntry{{Name: "missing", Source: "harness/missing.yaml"}}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadFromDir(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	_, err = ListTriggeredHarnesses(context.Background(), dir, dirCfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func TestMatchHarnesses_InvalidTrigger(t *testing.T) {
	ev := mustEvent(t, "issue-opened.json")
	matched, err := MatchHarnesses([]TriggeredHarness{{
		Name:    "bad",
		Harness: &harness.Harness{Trigger: "event.entity.kind == \"work_item\""},
	}, {
		Name:    "broken",
		Harness: &harness.Harness{Trigger: "!!!"},
	}}, ev)
	require.NoError(t, err)
	require.Len(t, matched, 1)
	assert.Equal(t, "bad", matched[0].Name)
}

func TestMatchHarnesses_NoCandidates(t *testing.T) {
	ev := mustEvent(t, "issue-opened.json")
	matched, err := MatchHarnesses(nil, ev)
	require.NoError(t, err)
	assert.Empty(t, matched)
}

func TestDispatch_PRMatch(t *testing.T) {
	dir := t.TempDir()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	prYAML := `agent: agents/triage.md
role: triage
slug: pr-ping
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: |
  event.entity.kind == "change_proposal"
  && event.transition.kind == "label_changed"
  && event.transition.label.name == "ready-for-pr-ping"
`
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "pr-ping.yaml"), []byte(prYAML), 0o644))
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.Agents = []config.AgentEntry{{Name: "pr-ping", Source: "harness/pr-ping.yaml"}}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	ev := mustEvent(t, "ready-to-code-labeled.json")
	ev.Entity = normevent.Entity{Kind: normevent.EntityChangeProposal, ID: 100, URL: "https://github.com/o/r/pull/100"}
	ev.Transition.Label.Name = "ready-for-pr-ping"

	refs, err := Dispatch(context.Background(), Options{ConfigDir: dir, Event: ev})
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "pr-ping", refs[0].Agent)
}
