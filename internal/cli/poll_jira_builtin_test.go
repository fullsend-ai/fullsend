package cli

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// jiraBuiltinTestRoles are the harness roles of the built-in agents.
var jiraBuiltinTestRoles = map[string]string{
	"triage": "triage", "code": "coder", "review": "review",
	"fix": "coder", "retro": "retro", "prioritize": "prioritize",
}

// jiraCommandTrigger matches a Jira /<command> comment from a human, the
// shape of the built-in triage and code triggers.
func jiraCommandTrigger(command string) string {
	return `trigger: >
  event.entity.kind == "work_item"
    && event.source.system == "jira"
    && event.transition.kind == "comment_added"
    && has(event.transition.comment.command)
    && event.transition.comment.command == "` + command + `"
    && event.actor.kind == "human"
`
}

// jiraBuiltinTestHarness is a built-in agent harness with an optional
// trigger: block.
func jiraBuiltinTestHarness(name, trigger string) string {
	return "agent: agents/" + name + ".md\nrole: " + jiraBuiltinTestRoles[name] + "\nslug: fullsend-ai-" + name +
		"\nmodel: opus\nimage: ghcr.io/fullsend-ai/fullsend-sandbox:latest\n" + trigger
}

// defaultJiraBuiltinTriggers gives triage and code their Jira command
// triggers, as the agents release does.
func defaultJiraBuiltinTriggers() map[string]string {
	return map[string]string{
		"triage": jiraCommandTrigger("/fs-triage"),
		"code":   jiraCommandTrigger("/fs-code"),
	}
}

// stubJiraBuiltins makes newCELMatcher resolve built-in agents from local
// harness files instead of the agents repository. triggers maps a built-in
// name to its trigger: block (a nil map means defaultJiraBuiltinTriggers);
// a built-in it does not name gets a harness with no trigger, and one in
// unavailable fails to resolve. It returns the names the resolver was
// asked for.
func stubJiraBuiltins(t *testing.T, triggers map[string]string, unavailable ...string) *[]string {
	t.Helper()
	if triggers == nil {
		triggers = defaultJiraBuiltinTriggers()
	}
	dir := t.TempDir()
	requested := &[]string{}
	orig := newJiraBuiltinResolverFn
	newJiraBuiltinResolverFn = func() harnessdispatch.BuiltinResolver {
		return func(_ context.Context, name string, opts harness.ComposeOpts) (string, harness.ComposeOpts, bool) {
			*requested = append(*requested, name)
			if slices.Contains(unavailable, name) {
				return "", opts, false
			}
			path := filepath.Join(dir, name+".yaml")
			if err := os.WriteFile(path, []byte(jiraBuiltinTestHarness(name, triggers[name])), 0o644); err != nil {
				t.Errorf("write %s: %v", path, err)
				return "", opts, false
			}
			return path, opts, true
		}
	}
	t.Cleanup(func() { newJiraBuiltinResolverFn = orig })
	return requested
}

// jiraCommandEvent returns the normative Jira /fs-triage comment example
// with its command replaced by command.
func jiraCommandEvent(t *testing.T, command string) *normevent.Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "normative", "normalized-event", "v1", "examples", "jira-fs-triage-comment.json"))
	require.NoError(t, err)
	ev, err := normevent.ParseJSON(raw)
	require.NoError(t, err)
	require.NotNil(t, ev.Transition.Comment)
	ev.Transition.Comment.Command = command
	ev.Transition.Comment.Body = command
	return ev
}

// matchedAgents returns the agents m dispatches for a Jira comment with
// command.
func matchedAgents(t *testing.T, m *celMatcher, command string) []string {
	t.Helper()
	records, err := m.Match(context.Background(), jiraCommandEvent(t, command))
	require.NoError(t, err)
	var agents []string
	for _, r := range records {
		agents = append(agents, r.Agent)
	}
	return agents
}

func writeMatcherConfig(t *testing.T, dir, yamlText string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlText), 0o644))
}

func jiraCandidateNames(m *celMatcher) []string {
	names, _ := m.DispatchCandidates()
	return names
}

// TestNewCELMatcher_FreshInstallDispatchesBuiltins is the #9503
// regression: a config written by setup (roles:, no agents:), or no config
// at all, dispatches the built-in triage and code agents from Jira.
func TestNewCELMatcher_FreshInstallDispatchesBuiltins(t *testing.T) {
	for name, configYAML := range map[string]string{
		"roles only":     "version: \"1\"\nroles: [triage, coder, review, fix, retro, prioritize]\n",
		"roles absent":   "version: \"1\"\n",
		"no config file": "",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if configYAML != "" {
				writeMatcherConfig(t, dir, configYAML)
			}
			requested := stubJiraBuiltins(t, nil)

			m, err := newCELMatcher(context.Background(), dir)
			require.NoError(t, err)
			assert.Equal(t, config.ValidAgentNames(), *requested, "each built-in is considered once")
			assert.Equal(t, []string{"triage", "code"}, jiraCandidateNames(m))
			assert.Equal(t, []string{"triage"}, matchedAgents(t, m, "/fs-triage"))
			assert.Equal(t, []string{"code"}, matchedAgents(t, m, "/fs-code"))
		})
	}
}

// TestNewCELMatcher_HonorsConfiguredRoles covers the roles: subset: only
// the built-in agents the roles select are fetched and dispatched, so an
// unselected built-in that cannot load does not fail the poll.
func TestNewCELMatcher_HonorsConfiguredRoles(t *testing.T) {
	t.Run("roles triage dispatches triage but not code", func(t *testing.T) {
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\nroles: [triage]\n")
		requested := stubJiraBuiltins(t, nil)

		m, err := newCELMatcher(context.Background(), dir)
		require.NoError(t, err)
		assert.Equal(t, []string{"triage"}, *requested, "an unselected built-in is not fetched")
		assert.Equal(t, []string{"triage"}, jiraCandidateNames(m))
		assert.Equal(t, []string{"triage"}, matchedAgents(t, m, "/fs-triage"))
		assert.Empty(t, matchedAgents(t, m, "/fs-code"))
	})

	t.Run("roles coder dispatches code", func(t *testing.T) {
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\nroles: [coder]\n")
		stubJiraBuiltins(t, nil)

		m, err := newCELMatcher(context.Background(), dir)
		require.NoError(t, err)
		assert.Equal(t, []string{"code"}, jiraCandidateNames(m))
		assert.Equal(t, []string{"code"}, matchedAgents(t, m, "/fs-code"))
		assert.Empty(t, matchedAgents(t, m, "/fs-triage"))
	})

	t.Run("an explicitly disabled agent stays disabled", func(t *testing.T) {
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\nroles: [triage, coder]\nagents:\n  - name: code\n    enabled: false\n")
		requested := stubJiraBuiltins(t, nil)

		m, err := newCELMatcher(context.Background(), dir)
		require.NoError(t, err)
		assert.NotContains(t, *requested, "code")
		assert.Equal(t, []string{"triage"}, jiraCandidateNames(m))
	})
}

// TestNewCELMatcher_ShorthandOverrideEntries covers agents: entries with
// no source (ADR 0091, ADR 0162): they tune the built-in agent and keep it
// dispatchable, with harness: blocks composed on the built-in harness.
func TestNewCELMatcher_ShorthandOverrideEntries(t *testing.T) {
	dir := t.TempDir()
	writeMatcherConfig(t, dir, `version: "1"
agents:
  - name: triage
    runtime: claude
  - name: code
    effort: low
    harness:
      model: sonnet
`)
	requested := stubJiraBuiltins(t, nil)

	m, err := newCELMatcher(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, config.ValidAgentNames(), *requested)
	require.Equal(t, []string{"triage", "code"}, jiraCandidateNames(m))
	assert.Equal(t, "sonnet", m.Candidates[1].Harness.Model, "harness: block override applies")
	assert.Equal(t, []string{"triage"}, matchedAgents(t, m, "/fs-triage"))
	assert.Equal(t, []string{"code"}, matchedAgents(t, m, "/fs-code"))
}

// TestNewCELMatcher_CustomSourcesTakePrecedence covers a source entry
// named after a built-in, which replaces it, and an additional custom
// agent, which is dispatchable beside the built-ins.
func TestNewCELMatcher_CustomSourcesTakePrecedence(t *testing.T) {
	dir := t.TempDir()
	harnessDir := filepath.Join(dir, "harness")
	require.NoError(t, os.MkdirAll(harnessDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "code.yaml"),
		[]byte(strings.Replace(jiraBuiltinTestHarness("code", jiraCommandTrigger("/fs-implement")), "model: opus", "model: haiku", 1)), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(harnessDir, "docs-bot.yaml"),
		[]byte("agent: agents/docs.md\nrole: triage\nslug: docs-bot\nmodel: opus\nimage: ghcr.io/fullsend-ai/fullsend-sandbox:latest\n"+jiraCommandTrigger("/fs-docs")), 0o644))
	writeMatcherConfig(t, dir, `version: "1"
agents:
  - name: code
    source: harness/code.yaml
  - name: docs-bot
    source: harness/docs-bot.yaml
`)
	requested := stubJiraBuiltins(t, nil)

	m, err := newCELMatcher(context.Background(), dir)
	require.NoError(t, err)
	assert.NotContains(t, *requested, "code", "the built-in code harness is not resolved when a source entry replaces it")
	assert.Equal(t, []string{"code", "docs-bot", "triage"}, jiraCandidateNames(m))
	assert.Equal(t, "haiku", m.Candidates[0].Harness.Model, "the custom harness is used")

	assert.Empty(t, matchedAgents(t, m, "/fs-code"), "the built-in code trigger is replaced")
	assert.Equal(t, []string{"code"}, matchedAgents(t, m, "/fs-implement"))
	assert.Equal(t, []string{"docs-bot"}, matchedAgents(t, m, "/fs-docs"))
	assert.Equal(t, []string{"triage"}, matchedAgents(t, m, "/fs-triage"))
}

func TestNewCELMatcher_DisabledAgentsStayDisabled(t *testing.T) {
	t.Run("disabled built-in", func(t *testing.T) {
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\nagents:\n  - name: code\n    enabled: false\n")
		requested := stubJiraBuiltins(t, nil)

		m, err := newCELMatcher(context.Background(), dir)
		require.NoError(t, err)
		assert.NotContains(t, *requested, "code")
		assert.Equal(t, []string{"triage"}, jiraCandidateNames(m))
		assert.Empty(t, matchedAgents(t, m, "/fs-code"))
	})

	t.Run("every candidate disabled", func(t *testing.T) {
		dir := t.TempDir()
		yamlText := "version: \"1\"\nagents:\n"
		for _, name := range config.ValidAgentNames() {
			yamlText += "  - name: " + name + "\n    enabled: false\n"
		}
		writeMatcherConfig(t, dir, yamlText)
		requested := stubJiraBuiltins(t, nil)

		m, err := newCELMatcher(context.Background(), dir)
		require.NoError(t, err)
		assert.Empty(t, *requested, "the fallback does not reintroduce disabled built-ins")
		assert.Empty(t, jiraCandidateNames(m))
		assert.Empty(t, matchedAgents(t, m, "/fs-triage"))
		assert.Empty(t, matchedAgents(t, m, "/fs-code"))
	})
}

func TestNewCELMatcher_BuiltinResolutionFailures(t *testing.T) {
	t.Run("one built-in unavailable", func(t *testing.T) {
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\n")
		stubJiraBuiltins(t, nil, "code")

		// An incomplete matcher would advance the checkpoints past events
		// only the unavailable built-in could match, so the poll fails.
		_, err := newCELMatcher(context.Background(), dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "some agents could not be loaded")
		assert.Contains(t, err.Error(), "built-in agent code")

		// Once the built-in resolves again, its command dispatches.
		stubJiraBuiltins(t, nil)
		m, err := newCELMatcher(context.Background(), dir)
		require.NoError(t, err)
		assert.Equal(t, []string{"triage", "code"}, jiraCandidateNames(m))
	})

	t.Run("every built-in unavailable", func(t *testing.T) {
		// Matching nothing would advance every checkpoint past the
		// cycle's events, so the poll fails and retries next cycle.
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\n")
		stubJiraBuiltins(t, nil, config.ValidAgentNames()...)

		_, err := newCELMatcher(context.Background(), dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no agents could be loaded")
	})

	t.Run("kill switch resolves nothing", func(t *testing.T) {
		dir := t.TempDir()
		writeMatcherConfig(t, dir, "version: \"1\"\nkill_switch: true\n")
		requested := stubJiraBuiltins(t, nil)

		m, err := newCELMatcher(context.Background(), dir)
		require.NoError(t, err)
		assert.Empty(t, *requested)
		names, suppressed := m.DispatchCandidates()
		assert.Empty(t, names)
		assert.True(t, suppressed)
	})
}

// countingRefClient counts GetRef calls on a fake forge client.
type countingRefClient struct {
	*forge.FakeClient
	getRef atomic.Int32
}

func (c *countingRefClient) GetRef(ctx context.Context, owner, repo, refPath string) (string, error) {
	c.getRef.Add(1)
	return c.FakeClient.GetRef(ctx, owner, repo, refPath)
}

// TestAgentsRepoBuiltinResolver_VersionPinned resolves built-in agents
// through the agents-repo fallback fullsend run uses: a release build reads
// its own release tag, other builds main, and every built-in is read from
// the one commit the ref resolves to.
func TestAgentsRepoBuiltinResolver_VersionPinned(t *testing.T) {
	const sha = "abcdef1234567890abcdef1234567890abcdef12"
	var served atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		switch r.URL.Path {
		case "/" + sha + "/harness/triage.yaml":
			_, _ = w.Write([]byte(jiraBuiltinTestHarness("triage", jiraCommandTrigger("/fs-triage"))))
		case "/" + sha + "/harness/code.yaml":
			_, _ = w.Write([]byte(jiraBuiltinTestHarness("code", jiraCommandTrigger("/fs-code"))))
		case "/" + sha + "/agents/triage.md", "/" + sha + "/agents/code.md":
			_, _ = w.Write([]byte("# agent\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	hostname, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	require.NoError(t, err)
	tlsCfg := srv.TLS.Clone()
	tlsCfg.InsecureSkipVerify = true
	policy := fetch.NewTestPolicy(tlsCfg, []string{hostname}, []string{port})
	origPrefix := defaultAgentsRepoURLPrefix
	defaultAgentsRepoURLPrefix = srv.URL + "/"
	t.Cleanup(func() { defaultAgentsRepoURLPrefix = origPrefix })

	tests := map[string]struct {
		version, commit, ref string
	}{
		"release build reads its tag": {version: "0.85.0", commit: "abc123def456", ref: "fullsend-ai/agents/tags/v0.85.0"},
		"dev build reads main":        {version: "dev", commit: "", ref: "fullsend-ai/agents/heads/main"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			origVersion, origSHA := version, commitSHA
			version, commitSHA = tt.version, tt.commit
			t.Cleanup(func() { version, commitSHA = origVersion, origSHA })

			dir := filepath.Join(t.TempDir(), ".fullsend")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			// The server serves only the triage and code harnesses; an
			// unresolvable built-in fails the listing, so disable the rest.
			writeMatcherConfig(t, dir, "version: \"1\"\nallowed_remote_resources:\n  - "+srv.URL+"/\nagents:\n"+
				"  - name: review\n    enabled: false\n  - name: fix\n    enabled: false\n"+
				"  - name: retro\n    enabled: false\n  - name: prioritize\n    enabled: false\n")
			cfg, err := config.LoadConfig(dir, config.LoadOpts{MissingOK: true})
			require.NoError(t, err)

			client := &countingRefClient{FakeClient: forge.NewFakeClient()}
			client.Refs[tt.ref] = sha
			resolver := agentsRepoBuiltinResolver(client, ui.New(io.Discard))

			candidates, err := harnessdispatch.ListEffectiveTriggeredHarnesses(context.Background(), dir, cfg, &policy, resolver)
			require.NoError(t, err)
			var names []string
			for _, c := range candidates {
				names = append(names, c.Name)
			}
			assert.Equal(t, []string{"triage", "code"}, names)
			assert.Equal(t, int32(1), client.getRef.Load(), "the agents ref is resolved once per matcher")
		})
	}
}

func TestAgentsRepoBuiltinResolver_Unresolvable(t *testing.T) {
	opts := harness.ComposeOpts{WorkspaceRoot: t.TempDir()}

	t.Run("unknown agent or no client", func(t *testing.T) {
		client := &countingRefClient{FakeClient: forge.NewFakeClient()}
		_, _, ok := agentsRepoBuiltinResolver(client, ui.New(io.Discard))(context.Background(), "custom", opts)
		assert.False(t, ok)
		assert.Zero(t, client.getRef.Load())
		_, _, ok = agentsRepoBuiltinResolver(nil, ui.New(io.Discard))(context.Background(), "triage", opts)
		assert.False(t, ok)
	})

	t.Run("ref lookup fails once for every built-in", func(t *testing.T) {
		client := &countingRefClient{FakeClient: forge.NewFakeClient()}
		resolve := agentsRepoBuiltinResolver(client, ui.New(io.Discard))
		for _, name := range []string{"triage", "code"} {
			_, _, ok := resolve(context.Background(), name, opts)
			assert.False(t, ok)
		}
		assert.Equal(t, int32(1), client.getRef.Load())
	})
}

func TestNewJiraBuiltinResolver_Default(t *testing.T) {
	assert.NotNil(t, newJiraBuiltinResolverFn())
}
