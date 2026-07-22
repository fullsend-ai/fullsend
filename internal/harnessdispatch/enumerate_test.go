package harnessdispatch

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
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

	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, nil)
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

	_, err = ListTriggeredHarnesses(context.Background(), dir, dirCfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate agent name")
}

func TestListTriggeredHarnesses_MissingHarness(t *testing.T) {
	dir := t.TempDir()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "good.yaml"), []byte(`agent: agents/triage.md
role: triage
slug: good
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: event.entity.kind == "work_item"
`), 0o644))
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.Agents = []config.AgentEntry{
		{Name: "good", Source: "harness/good.yaml"},
		{Name: "missing", Source: "harness/missing.yaml"},
	}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadFromDir(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, nil)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "good", out[0].Name)
}

func TestListTriggeredHarnesses_URLSourcedAgent(t *testing.T) {
	// Verify that ListTriggeredHarnesses can resolve URL-sourced agents
	// via the FetchPolicy. Before the fix, FetchPolicy was zero-valued
	// (AllowedDomains == nil), causing isAllowedDomain to reject all hosts
	// and making URL-sourced harness dispatch impossible.
	harnessContent := []byte(`agent: agents/triage.md
role: triage
slug: url-ping
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: |
  event.entity.kind == "work_item"
  && event.transition.kind == "label_changed"
`)
	harnessHash := fetch.ComputeSHA256(harnessContent)
	commitSHA := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/org/repo/"+commitSHA+"/harness/url-ping.yaml" {
			w.Write(harnessContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	hostPort := strings.TrimPrefix(srv.URL, "https://")
	hostname, port, _ := net.SplitHostPort(hostPort)
	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	policy := fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})

	dir := t.TempDir()
	rawURL := srv.URL + "/org/repo/" + commitSHA + "/harness/url-ping.yaml#sha256=" + harnessHash
	allowlist := []string{srv.URL + "/"}

	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.Agents = []config.AgentEntry{{Name: "url-ping", Source: rawURL}}
	cfg.AllowedRemoteResources = allowlist
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadFromDir(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, &policy)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "url-ping", out[0].Name)
	assert.NotEmpty(t, out[0].Harness.Trigger)
}

func TestListTriggeredHarnesses_URLSourcedAgentZeroPolicyFails(t *testing.T) {
	// Verify that without a FetchPolicy (nil), the DefaultPolicy is used.
	// A URL-sourced agent pointing to a non-github domain should be skipped
	// (logged) rather than causing a hard error, because ListTriggeredHarnesses
	// logs and continues on resolve failures.
	dir := t.TempDir()
	rawURL := "https://evil.example.com/org/repo/sha/harness/evil.yaml#sha256=" + strings.Repeat("a", 64)
	allowlist := []string{"https://evil.example.com/"}

	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.Agents = []config.AgentEntry{{Name: "evil", Source: rawURL}}
	cfg.AllowedRemoteResources = allowlist
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadFromDir(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	// nil fetchPolicy → DefaultPolicy (allows only github.com, raw.githubusercontent.com).
	// evil.example.com is not in DefaultPolicy's AllowedDomains, so the agent
	// is skipped with a log message (not a hard error).
	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, nil)
	require.NoError(t, err)
	assert.Empty(t, out, "agent with non-github domain should be skipped when using DefaultPolicy")
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
