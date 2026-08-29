package harnessdispatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
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

// captureAnnotations replaces annotationWriter with a buffer for the
// duration of the test and returns the buffer. Not safe for parallel use.
func captureAnnotations(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := annotationWriter
	annotationWriter = &buf
	t.Cleanup(func() { annotationWriter = old })
	return &buf
}

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
	cfg.SetAgents([]config.AgentEntry{{Name: "issue-ping", Source: "harness/issue-ping.yaml"}})
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, nil)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestListTriggeredHarnesses_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.SetAgents([]config.AgentEntry{
		{Name: "Ping", Source: "harness/a.yaml"},
		{Name: "ping", Source: "harness/b.yaml"},
	})
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
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
	cfg.SetAgents([]config.AgentEntry{
		{Name: "good", Source: "harness/good.yaml"},
		{Name: "missing", Source: "harness/missing.yaml"},
	})
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, nil)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "good", out[0].Name)
}

func TestDispatch_FetchPolicyPlumbing(t *testing.T) {
	// Verify that Options.FetchPolicy is threaded through Dispatch →
	// ListTriggeredHarnesses → ResolveRegisteredPath. A URL-sourced agent
	// pointing at a non-github domain should be skipped (not error) when
	// the default policy is used, confirming the policy is applied.
	dir := t.TempDir()
	rawURL := "https://evil.example.com/org/repo/sha/harness/evil.yaml#sha256=" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	allowlist := []string{"https://evil.example.com/"}

	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.SetAgents([]config.AgentEntry{{Name: "evil", Source: rawURL}})
	cfg.SetAllowedRemoteResources(allowlist)
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	ev := mustEvent(t, "issue-opened.json")

	// nil FetchPolicy → DefaultPolicy (allows only github.com, raw.githubusercontent.com).
	// evil.example.com is not in DefaultPolicy's AllowedDomains, so the agent
	// is skipped. Dispatch should return empty results, not an error.
	refs, err := Dispatch(context.Background(), Options{
		ConfigDir: dir,
		Event:     ev,
		// FetchPolicy: nil → uses DefaultPolicy
	})
	require.NoError(t, err)
	assert.Empty(t, refs, "URL-sourced agent with non-github domain should be skipped by DefaultPolicy")
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

// urlHarnessServer creates an httptest TLS server that serves harness YAML
// at a known path and returns the server, its FetchPolicy, and the raw URL
// with #sha256=... fragment. When badHash is true, the fragment contains a
// deliberately wrong hash to trigger an integrity failure.
func urlHarnessServer(t *testing.T, harnessName, harnessYAML string, badHash bool) (*httptest.Server, fetch.FetchPolicy, string) {
	t.Helper()
	content := []byte(harnessYAML)
	harnessPath := "/harness/" + harnessName + ".yaml"

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve harness YAML at any /harness/*.yaml path so a single server
		// can host multiple agents (e.g. fail1 and fail2 in the multi-failure test).
		if strings.HasPrefix(r.URL.Path, "/harness/") && strings.HasSuffix(r.URL.Path, ".yaml") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content)
			return
		}
		// Serve a minimal agent stub so resource resolution succeeds.
		if strings.HasSuffix(r.URL.Path, ".md") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# stub agent\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	// Extract hostname and port for the FetchPolicy.
	hostPort := strings.TrimPrefix(srv.URL, "https://")
	parts := strings.SplitN(hostPort, ":", 2)
	hostname, port := parts[0], parts[1]

	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true //nolint:gosec // test-only
	policy := fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})

	// Compute the real or fake hash.
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	if badHash {
		hash = "0000000000000000000000000000000000000000000000000000000000000000"
	}

	rawURL := srv.URL + harnessPath + "#sha256=" + hash
	return srv, policy, rawURL
}

func TestListTriggeredHarnesses_BaseComposition(t *testing.T) {
	dir := t.TempDir()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))

	// Base harness with shared config (agent, role, image).
	baseYAML := `agent: agents/triage.md
role: triage
slug: base-harness
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
`
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "base.yaml"), []byte(baseYAML), 0o644))

	// Child harness that inherits from base via base: field and adds its
	// own trigger. This is the standard per-repo pattern (ADR-0045): the
	// child overrides slug and trigger while inheriting agent, role, image
	// from the upstream base.
	childYAML := `base: base.yaml
slug: child-harness
trigger: |
  event.entity.kind == "work_item"
  && event.transition.kind == "label_changed"
`
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "child.yaml"), []byte(childYAML), 0o644))

	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.SetAgents([]config.AgentEntry{{Name: "child", Source: "harness/child.yaml"}})
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, nil)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "child", out[0].Name)
	// Verify base fields were inherited through composition.
	assert.Equal(t, "triage", out[0].Harness.Role)
	assert.Contains(t, out[0].Harness.Trigger, "work_item")
}

func TestListTriggeredHarnesses_URLIntegrityFailureContinues(t *testing.T) {
	t.Parallel()

	sharedTrigger := `event.entity.kind == "work_item" && event.transition.kind == "label_changed" && event.transition.label.name == "ready-for-integrity-test"`
	localHarnessYAML := fmt.Sprintf(`agent: agents/triage.md
role: triage
slug: good-local
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: |
  %s
`, sharedTrigger)
	urlHarnessYAML := fmt.Sprintf(`agent: agents/triage.md
role: triage
slug: bad-hash
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: |
  %s
`, sharedTrigger)

	t.Run("bad-hash URL agent first, then good local agent", func(t *testing.T) {
		t.Parallel()
		_, policy, rawURL := urlHarnessServer(t, "bad-hash", urlHarnessYAML, true)

		dir := t.TempDir()
		harnessDir := filepath.Join(dir, "harness")
		require.NoError(t, os.MkdirAll(harnessDir, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(harnessDir, "good-local.yaml"),
			[]byte(localHarnessYAML), 0o644))

		allowlist := []string{strings.TrimPrefix(rawURL[:strings.Index(rawURL, "/harness/")], "") + "/"}
		cfg := config.NewPerRepoConfig(nil, "o/r")
		cfg.SetAgents([]config.AgentEntry{
			{Name: "bad-hash", Source: rawURL},
			{Name: "good-local", Source: "harness/good-local.yaml"},
		})
		cfg.SetAllowedRemoteResources(allowlist)
		data, err := yaml.Marshal(cfg)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

		dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
		require.NoError(t, err)

		out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, &policy)
		require.NoError(t, err, "ListTriggeredHarnesses must not return error when one agent fails integrity")
		require.Len(t, out, 1, "only the good local agent should be returned")
		assert.Equal(t, "good-local", out[0].Name)
	})

	t.Run("good local agent first, then bad-hash URL agent", func(t *testing.T) {
		t.Parallel()
		_, policy, rawURL := urlHarnessServer(t, "bad-hash", urlHarnessYAML, true)

		dir := t.TempDir()
		harnessDir := filepath.Join(dir, "harness")
		require.NoError(t, os.MkdirAll(harnessDir, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(harnessDir, "good-local.yaml"),
			[]byte(localHarnessYAML), 0o644))

		allowlist := []string{strings.TrimPrefix(rawURL[:strings.Index(rawURL, "/harness/")], "") + "/"}
		cfg := config.NewPerRepoConfig(nil, "o/r")
		cfg.SetAgents([]config.AgentEntry{
			{Name: "good-local", Source: "harness/good-local.yaml"},
			{Name: "bad-hash", Source: rawURL},
		})
		cfg.SetAllowedRemoteResources(allowlist)
		data, err := yaml.Marshal(cfg)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

		dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
		require.NoError(t, err)

		out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, &policy)
		require.NoError(t, err, "ListTriggeredHarnesses must not return error when one agent fails integrity")
		require.Len(t, out, 1, "only the good local agent should be returned")
		assert.Equal(t, "good-local", out[0].Name)
	})

	t.Run("multiple failing URL agents before a valid one", func(t *testing.T) {
		t.Parallel()

		// Use a single server serving both failing harnesses on different paths.
		// This avoids needing to merge TLS configs from two separate servers.
		_, policy, rawURLBase := urlHarnessServer(t, "fail1", urlHarnessYAML, true)
		// Construct a second URL on the same server with a different name.
		rawURL1 := rawURLBase
		rawURL2 := strings.Replace(rawURLBase, "fail1", "fail2", 1)

		dir := t.TempDir()
		harnessDir := filepath.Join(dir, "harness")
		require.NoError(t, os.MkdirAll(harnessDir, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(harnessDir, "good-local.yaml"),
			[]byte(localHarnessYAML), 0o644))

		prefix := rawURL1[:strings.Index(rawURL1, "/harness/")] + "/"
		allowlist := []string{prefix}
		cfg := config.NewPerRepoConfig(nil, "o/r")
		cfg.SetAgents([]config.AgentEntry{
			{Name: "fail1", Source: rawURL1},
			{Name: "fail2", Source: rawURL2},
			{Name: "good-local", Source: "harness/good-local.yaml"},
		})
		cfg.SetAllowedRemoteResources(allowlist)
		data, err := yaml.Marshal(cfg)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

		dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
		require.NoError(t, err)

		out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, &policy)
		require.NoError(t, err, "ListTriggeredHarnesses must not return error even with multiple integrity failures")
		require.Len(t, out, 1, "only the good local agent should survive")
		assert.Equal(t, "good-local", out[0].Name)
	})
}

func TestDispatch_URLIntegrityFailureContinues(t *testing.T) {
	t.Parallel()

	sharedTrigger := `event.entity.kind == "work_item" && event.transition.kind == "label_changed" && event.transition.label.name == "ready-for-integrity-test"`
	localHarnessYAML := fmt.Sprintf(`agent: agents/triage.md
role: triage
slug: good-local
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: |
  %s
`, sharedTrigger)
	urlHarnessYAML := fmt.Sprintf(`agent: agents/triage.md
role: triage
slug: bad-hash
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: |
  %s
`, sharedTrigger)

	_, policy, rawURL := urlHarnessServer(t, "bad-hash", urlHarnessYAML, true)

	dir := t.TempDir()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(harnessDir, "good-local.yaml"),
		[]byte(localHarnessYAML), 0o644))

	allowlist := []string{rawURL[:strings.Index(rawURL, "/harness/")] + "/"}
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.SetAgents([]config.AgentEntry{
		{Name: "bad-hash", Source: rawURL},
		{Name: "good-local", Source: "harness/good-local.yaml"},
	})
	cfg.SetAllowedRemoteResources(allowlist)
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	ev := mustEvent(t, "ready-to-code-labeled.json")
	ev.Transition.Label.Name = "ready-for-integrity-test"
	ev.State.Labels = []string{"ready-for-integrity-test"}
	ev.Actor.Role = normevent.RoleNone

	refs, err := Dispatch(context.Background(), Options{
		ConfigDir:   dir,
		Event:       ev,
		FetchPolicy: &policy,
	})
	require.NoError(t, err, "Dispatch must not return error when one agent fails integrity")
	require.Len(t, refs, 1, "only good-local should produce an execution ref")
	assert.Equal(t, "good-local", refs[0].Agent)
	assert.Equal(t, "triage", refs[0].Role)
}

func TestListTriggeredHarnesses_ResolveFailureAnnotation(t *testing.T) {
	buf := captureAnnotations(t)

	dir := t.TempDir()
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.SetAgents([]config.AgentEntry{
		{Name: "missing", Source: "harness/missing.yaml"},
	})
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, nil)
	require.NoError(t, err)
	assert.Empty(t, out)

	annotation := buf.String()
	assert.Contains(t, annotation, "::error::")
	assert.Contains(t, annotation, "missing")
	assert.Contains(t, annotation, "resolve failed")
}

func TestListTriggeredHarnesses_LoadFailureAnnotation(t *testing.T) {
	buf := captureAnnotations(t)

	dir := t.TempDir()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	// Write invalid YAML so Load fails.
	require.NoError(t, os.WriteFile(
		filepath.Join(harnessDir, "broken.yaml"),
		[]byte(":\n  - bad yaml that cannot parse as a harness"), 0o644))
	cfg := config.NewPerRepoConfig(nil, "o/r")
	cfg.SetAgents([]config.AgentEntry{
		{Name: "broken", Source: "harness/broken.yaml"},
	})
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))

	dirCfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: false})
	require.NoError(t, err)

	out, err := ListTriggeredHarnesses(context.Background(), dir, dirCfg, nil)
	require.NoError(t, err)
	assert.Empty(t, out)

	annotation := buf.String()
	assert.Contains(t, annotation, "::error::")
	assert.Contains(t, annotation, "broken")
	assert.Contains(t, annotation, "load failed")
}

func TestMatchHarnesses_TriggerEvalFailureAnnotation(t *testing.T) {
	buf := captureAnnotations(t)

	ev := mustEvent(t, "issue-opened.json")
	matched, err := MatchHarnesses([]TriggeredHarness{{
		Name:    "bad-cel",
		Harness: &harness.Harness{Trigger: "!!!"},
	}}, ev)
	require.NoError(t, err)
	assert.Empty(t, matched)

	annotation := buf.String()
	assert.Contains(t, annotation, "::error::")
	assert.Contains(t, annotation, "bad-cel")
	assert.Contains(t, annotation, "trigger eval failed")
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
	cfg.SetAgents([]config.AgentEntry{{Name: "pr-ping", Source: "harness/pr-ping.yaml"}})
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
