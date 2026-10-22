package cli

import (
	"context"
	"encoding/json"
	"io"
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
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// reviewTriggerYAML returns the default review trigger fixture
// (testdata/review-route/review-trigger.cel) as a harness trigger: field.
func reviewTriggerYAML(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "review-route", "review-trigger.cel"))
	require.NoError(t, err)
	var b strings.Builder
	b.WriteString("trigger: |\n")
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// routeTestCommentOnlyTrigger is a narrowed override: /fs-review only, with
// no actor.kind guard.
const routeTestCommentOnlyTrigger = `trigger: >
  event.transition.kind == "comment_added"
  && has(event.transition.comment.command)
  && event.transition.comment.command == "/fs-review"
`

const routeTestHarnessBody = `agent: agents/review.md
role: review
slug: fullsend-ai-review
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
`

const routeTestSHA = "abcdef1234567890abcdef1234567890abcdef12"

// routeAgentsRepo serves harness/review.yaml at routeTestSHA from a TLS test
// server standing in for the agents repo, and returns a forge client whose
// main ref resolves to that SHA plus the matching fetch policy.
func routeAgentsRepo(t *testing.T, harnessYAML string) (forge.Client, *fetch.FetchPolicy, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + routeTestSHA + "/harness/review.yaml":
			_, _ = w.Write([]byte(harnessYAML))
		case "/" + routeTestSHA + "/agents/review.md":
			_, _ = w.Write([]byte("# review\n"))
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

	orig := defaultAgentsRepoURLPrefix
	defaultAgentsRepoURLPrefix = srv.URL + "/"
	t.Cleanup(func() { defaultAgentsRepoURLPrefix = orig })

	client := forge.NewFakeClient()
	client.Refs["fullsend-ai/agents/heads/main"] = routeTestSHA
	return client, &policy, srv.URL + "/"
}

type routeFixture struct {
	agents     []config.AgentEntry
	allowlist  []string
	killSwitch bool
	files      map[string]string // relative to the config dir
}

func writeRouteFixture(t *testing.T, f routeFixture) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".fullsend")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for rel, content := range f.files {
		path := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	cfg := config.NewPerRepoConfig(nil, "fullsend-ai/demo")
	cfg.SetAgents(f.agents)
	if f.allowlist != nil {
		cfg.SetAllowedRemoteResources(f.allowlist)
	}
	if f.killSwitch {
		cfg.SetKillSwitch(true)
	}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))
	return dir
}

func routeEvent(t *testing.T, raw string) *normevent.Event {
	t.Helper()
	ev, err := normevent.ParseJSON([]byte(raw))
	require.NoError(t, err)
	return ev
}

const (
	routePROpened = `{
  "repo": "fullsend-ai/demo",
  "entity": {"kind": "change_proposal", "id": 7, "url": "https://github.com/fullsend-ai/demo/pull/7"},
  "transition": {"kind": "opened"},
  "actor": {"id": "alice", "kind": "human", "role": "write", "is_entity_author": true},
  "state": {"labels": []},
  "source": {"system": "github", "raw_type": "pull_request_target", "raw_action": "opened"}
}`
	routeReviewComment = `{
  "repo": "fullsend-ai/demo",
  "entity": {"kind": "change_proposal", "id": 7, "url": "https://github.com/fullsend-ai/demo/pull/7"},
  "transition": {"kind": "comment_added", "comment": {"id": "1", "command": "/fs-review", "body": "/fs-review"}},
  "actor": {"id": "alice", "kind": "human", "role": "write", "is_entity_author": false},
  "state": {"labels": []},
  "source": {"system": "github", "raw_type": "issue_comment", "raw_action": "created"}
}`
	routeBotReviewComment = `{
  "repo": "fullsend-ai/demo",
  "entity": {"kind": "change_proposal", "id": 7, "url": "https://github.com/fullsend-ai/demo/pull/7"},
  "transition": {"kind": "comment_added", "comment": {"id": "1", "command": "/fs-review", "body": "/fs-review"}},
  "actor": {"id": "helper[bot]", "kind": "bot", "role": "write", "is_entity_author": false},
  "state": {"labels": []},
  "source": {"system": "github", "raw_type": "issue_comment", "raw_action": "created"}
}`
	routeIssueReviewComment = `{
  "repo": "fullsend-ai/demo",
  "entity": {"kind": "work_item", "id": 8, "url": "https://github.com/fullsend-ai/demo/issues/8"},
  "transition": {"kind": "comment_added", "comment": {"id": "2", "command": "/fs-review", "body": "/fs-review"}},
  "actor": {"id": "alice", "kind": "human", "role": "write", "is_entity_author": false},
  "state": {"labels": []},
  "source": {"system": "github", "raw_type": "issue_comment", "raw_action": "created"}
}`
	routePROpenedNoRole = `{
  "repo": "fullsend-ai/demo",
  "entity": {"kind": "change_proposal", "id": 7, "url": "https://github.com/fullsend-ai/demo/pull/7"},
  "transition": {"kind": "opened"},
  "actor": {"id": "mallory", "kind": "human", "role": "none", "is_entity_author": true},
  "state": {"labels": []},
  "source": {"system": "github", "raw_type": "pull_request_target", "raw_action": "opened"}
}`
)

// reviewTrigger returns the default review trigger fixture as a CEL string.
func reviewTrigger(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "review-route", "review-trigger.cel"))
	require.NoError(t, err)
	return string(data)
}

// sourcedReview registers review with a local harness file and extra files.
func sourcedReview(entry config.AgentEntry, files map[string]string) routeFixture {
	entry.Name = "review"
	entry.Source = "harness/review.yaml"
	return routeFixture{agents: []config.AgentEntry{entry}, files: files}
}

func TestRouteBuiltinAgent(t *testing.T) {
	disabled := false
	commentOnly := strings.TrimPrefix(routeTestCommentOnlyTrigger, "trigger: >\n")
	cases := []struct {
		name         string
		fixture      func(t *testing.T, agentsURL string) routeFixture
		event        string
		wantDecision string
		wantReason   string
		wantHarness  string
	}{
		// --- no explicit trigger: bash keeps routing ---
		{
			name:         "no review entry stays on bash, even though the default harness has a trigger",
			fixture:      func(t *testing.T, u string) routeFixture { return routeFixture{allowlist: []string{u}} },
			event:        routePROpened,
			wantDecision: routeBash,
			wantReason:   "no agents[].trigger",
		},
		{
			name: "override-only entry without a trigger stays on bash",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{allowlist: []string{u}, agents: []config.AgentEntry{{Name: "review", Model: "sonnet"}}}
			},
			event:        routeReviewComment,
			wantDecision: routeBash,
			wantReason:   "no agents[].trigger",
		},
		{
			name: "sourced override that inherits a base trigger stays on bash",
			fixture: func(t *testing.T, u string) routeFixture {
				return sourcedReview(config.AgentEntry{}, map[string]string{
					"harness/base-new.yaml": routeTestHarnessBody + reviewTriggerYAML(t),
					"harness/review.yaml":   "base: base-new.yaml\nmodel: sonnet\n",
				})
			},
			event:        routePROpened,
			wantDecision: routeBash,
			wantReason:   "no agents[].trigger",
		},
		{
			name: "sourced override on an old base with scripts and a forge block stays on bash",
			fixture: func(t *testing.T, u string) routeFixture {
				return sourcedReview(config.AgentEntry{}, map[string]string{
					"harness/base-old.yaml": routeTestHarnessBody,
					"harness/review.yaml": `base: base-old.yaml
model: sonnet
pre_script: scripts/pre-review.sh
post_script: scripts/post-review.sh
forge:
  github:
    runner_env:
      REVIEW_MODE: strict
`,
					"harness/scripts/pre-review.sh":  "#!/bin/sh\n",
					"harness/scripts/post-review.sh": "#!/bin/sh\n",
				})
			},
			event:        routePROpened,
			wantDecision: routeBash,
		},
		// --- config trigger ---
		{
			name: "config trigger on an override-only entry opts in",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{allowlist: []string{u}, agents: []config.AgentEntry{{Name: "review", Trigger: commentOnly}}}
			},
			event:        routeReviewComment,
			wantDecision: routeRun,
			wantReason:   "trigger matched",
			wantHarness:  "agents@" + routeTestSHA[:12],
		},
		{
			name: "config trigger skips automatic review",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{allowlist: []string{u}, agents: []config.AgentEntry{{Name: "review", Trigger: commentOnly}}}
			},
			event:        routePROpened,
			wantDecision: routeSkip,
			wantReason:   "trigger did not match",
		},
		{
			name: "config trigger wins over the harness trigger",
			fixture: func(t *testing.T, u string) routeFixture {
				return sourcedReview(config.AgentEntry{Trigger: commentOnly}, map[string]string{
					"harness/review.yaml": routeTestHarnessBody + reviewTriggerYAML(t),
				})
			},
			event:        routePROpened,
			wantDecision: routeSkip,
			wantHarness:  "config:harness/review.yaml",
		},
		// --- explicit harness trigger ---
		{
			name: "a trigger written in the sourced harness does not opt in",
			fixture: func(t *testing.T, u string) routeFixture {
				return sourcedReview(config.AgentEntry{}, map[string]string{
					"harness/review.yaml": routeTestHarnessBody + routeTestCommentOnlyTrigger,
				})
			},
			event:        routeReviewComment,
			wantDecision: routeBash,
			wantReason:   "no agents[].trigger",
		},
		{
			name: "a trigger written over a base does not opt in",
			fixture: func(t *testing.T, u string) routeFixture {
				return sourcedReview(config.AgentEntry{}, map[string]string{
					"harness/base-new.yaml": routeTestHarnessBody + reviewTriggerYAML(t),
					"harness/review.yaml":   "base: base-new.yaml\n" + routeTestCommentOnlyTrigger,
				})
			},
			event:        routePROpened,
			wantDecision: routeBash,
			wantReason:   "no agents[].trigger",
		},
		// --- gates ---
		{
			name: "bot /fs-review is skipped even when the trigger has no actor guard",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{agents: []config.AgentEntry{{Name: "review", Trigger: commentOnly}}}
			},
			event:        routeBotReviewComment,
			wantDecision: routeSkip,
			wantReason:   "bot comments do not start review",
		},
		{
			name: "disabled agent is left to bash",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{agents: []config.AgentEntry{{Name: "review", Enabled: &disabled}}}
			},
			event:        routePROpened,
			wantDecision: routeBash,
			wantReason:   "agent disabled in config",
		},
		{
			name: "kill switch is left to bash",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{killSwitch: true, agents: []config.AgentEntry{{Name: "review", Trigger: commentOnly}}}
			},
			event:        routeReviewComment,
			wantDecision: routeBash,
			wantReason:   "kill switch active",
		},
		{
			name: "unauthorized actor is skipped",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{allowlist: []string{u}, agents: []config.AgentEntry{{Name: "review", Trigger: reviewTrigger(t)}}}
			},
			event:        routePROpenedNoRole,
			wantDecision: routeSkip,
			wantReason:   "actor not authorized",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, policy, agentsURL := routeAgentsRepo(t, routeTestHarnessBody+reviewTriggerYAML(t))
			dir := writeRouteFixture(t, tc.fixture(t, agentsURL))

			got, err := routeBuiltinAgent(context.Background(), routeOpts{
				agent:       "review",
				configDir:   dir,
				event:       routeEvent(t, tc.event),
				forgeClient: client,
				fetchPolicy: policy,
				printer:     ui.New(io.Discard),
			})
			require.NoError(t, err)
			assert.Equal(t, "review", got.Agent)
			assert.Equal(t, tc.wantDecision, got.Decision, "reason: %s", got.Reason)
			if tc.wantReason != "" {
				assert.Equal(t, tc.wantReason, got.Reason)
			}
			if tc.wantHarness != "" {
				assert.Equal(t, tc.wantHarness, got.Harness)
			}
		})
	}
}

func TestRouteBuiltinAgent_TriggerFromConfigBase(t *testing.T) {
	dir := writeRouteFixture(t, sourcedReview(config.AgentEntry{Model: "sonnet"}, map[string]string{
		"harness/review.yaml": routeTestHarnessBody,
		"config.base.yaml":    "agents:\n  - name: review\n    trigger: event.transition.kind == \"comment_added\"\n",
	}))
	got, err := routeBuiltinAgent(context.Background(), routeOpts{
		agent: "review", configDir: dir, event: routeEvent(t, routePROpened), printer: ui.New(io.Discard),
	})
	require.NoError(t, err)
	assert.Equal(t, routeSkip, got.Decision, "the base layer's trigger survives the overlay entry's merge")
	assert.Equal(t, "trigger did not match", got.Reason)
}

func TestRouteBuiltinAgent_NoTriggerNeedsNoNetwork(t *testing.T) {
	neutralizeAgentsRepoFallback(t)
	dir := writeRouteFixture(t, routeFixture{agents: []config.AgentEntry{{Name: "review", Model: "sonnet"}}})

	got, err := routeBuiltinAgent(context.Background(), routeOpts{
		agent: "review", configDir: dir, event: routeEvent(t, routePROpened),
		forgeClient: forge.NewFakeClient(), printer: ui.New(io.Discard),
	})
	require.NoError(t, err)
	assert.Equal(t, routeBash, got.Decision)
	assert.Empty(t, got.Harness, "no harness is resolved without an explicit trigger")
}

func TestRouteBuiltinAgent_ConfigTriggerDecidesWithoutHarness(t *testing.T) {
	neutralizeAgentsRepoFallback(t)
	dir := writeRouteFixture(t, routeFixture{agents: []config.AgentEntry{{Name: "review", Trigger: "true"}}})
	got, err := routeBuiltinAgent(context.Background(), routeOpts{
		agent: "review", configDir: dir, event: routeEvent(t, routePROpened),
		forgeClient: forge.NewFakeClient(), printer: ui.New(io.Discard),
	})
	require.NoError(t, err, "an unresolvable harness only loses the label")
	assert.Equal(t, routeRun, got.Decision)
	assert.Empty(t, got.Harness)
}

func TestRouteBuiltinAgent_WhitespaceTriggerIsNoTrigger(t *testing.T) {
	neutralizeAgentsRepoFallback(t)
	assert.False(t, config.AgentEntry{Name: "review", Trigger: "  \n"}.HasSettings())
	dir := writeRouteFixture(t, sourcedReview(config.AgentEntry{Trigger: "  "}, map[string]string{
		"harness/review.yaml": routeTestHarnessBody,
	}))
	got, err := routeBuiltinAgent(context.Background(), routeOpts{
		agent: "review", configDir: dir, event: routeEvent(t, routePROpened), printer: ui.New(io.Discard),
	})
	require.NoError(t, err)
	assert.Equal(t, routeBash, got.Decision)
}

func TestReviewRouteAccepts(t *testing.T) {
	cases := map[string]struct {
		event string
		want  bool
	}{
		"PR opened":           {routePROpened, true},
		"comment on a PR":     {routeReviewComment, true},
		"comment on an issue": {routeIssueReviewComment, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, reviewRouteAccepts(routeEvent(t, tc.event)))
		})
	}
}

func TestRouteBuiltinAgent_IssueCommentIsLeftToBash(t *testing.T) {
	dir := writeRouteFixture(t, routeFixture{agents: []config.AgentEntry{{Name: "review", Trigger: "true"}}})
	got, err := routeBuiltinAgent(context.Background(), routeOpts{
		agent: "review", configDir: dir, event: routeEvent(t, routeIssueReviewComment), printer: ui.New(io.Discard),
	})
	require.NoError(t, err)
	assert.Equal(t, routeBash, got.Decision)
	assert.Equal(t, "event is not routable for review", got.Reason)
}

func TestRouteBuiltinAgent_Errors(t *testing.T) {
	t.Run("invalid config trigger", func(t *testing.T) {
		dir := writeRouteFixture(t, routeFixture{agents: []config.AgentEntry{{Name: "review", Trigger: "event.x =="}}})
		_, err := routeBuiltinAgent(context.Background(), routeOpts{
			agent: "review", configDir: dir, event: routeEvent(t, routePROpened), printer: ui.New(io.Discard),
		})
		require.ErrorContains(t, err, "evaluating review trigger")
	})
	t.Run("an invalid unrelated entry does not affect routing", func(t *testing.T) {
		dir := writeRouteFixture(t, routeFixture{agents: []config.AgentEntry{
			{Name: "review", Trigger: "true"},
			{Name: "lint", Source: "https://example.com/lint.yaml"},
		}})
		got, err := routeBuiltinAgent(context.Background(), routeOpts{
			agent: "review", configDir: dir, event: routeEvent(t, routePROpened), printer: ui.New(io.Discard),
		})
		require.NoError(t, err)
		assert.Equal(t, routeRun, got.Decision)
	})
	t.Run("unsupported agent", func(t *testing.T) {
		_, err := routeBuiltinAgent(context.Background(), routeOpts{agent: "code", configDir: t.TempDir(), event: routeEvent(t, routePROpened)})
		require.ErrorContains(t, err, `unsupported --route agent "code" (supported: review)`)
	})
	t.Run("nil event", func(t *testing.T) {
		_, err := routeBuiltinAgent(context.Background(), routeOpts{agent: "review", configDir: t.TempDir()})
		require.ErrorContains(t, err, "event is required")
	})
}

func TestRunDispatch_RouteRejectsUnsupportedAgent(t *testing.T) {
	err := runDispatch(context.Background(), dispatchOpts{route: "code", inputDriver: "json"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported --route agent "code"`)
}

func TestRunDispatch_RoutePrintsOneJSONLine(t *testing.T) {
	dir := writeRouteFixture(t, routeFixture{
		agents: []config.AgentEntry{{Name: "review", Source: "harness/review.yaml", Trigger: strings.TrimPrefix(routeTestCommentOnlyTrigger, "trigger: >\n")}},
		files: map[string]string{
			"harness/review.yaml": routeTestHarnessBody,
		},
	})
	eventPath := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(eventPath, []byte(routeReviewComment), 0o644))

	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	errCh := make(chan error, 1)
	go func() {
		defer w.Close()
		errCh <- runDispatch(context.Background(), dispatchOpts{
			inputDriver: "json",
			inputFile:   eventPath,
			configDir:   dir,
			route:       "Review",
		})
	}()
	out, _ := io.ReadAll(r)
	os.Stdout = old
	require.NoError(t, <-errCh)

	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	require.Len(t, lines, 1, "stdout must hold exactly one JSON line: %q", out)
	var got routeDecision
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))
	assert.Equal(t, routeDecision{
		Agent:    "review",
		Decision: routeRun,
		Reason:   "trigger matched",
		Harness:  "config:harness/review.yaml",
	}, got)
}
