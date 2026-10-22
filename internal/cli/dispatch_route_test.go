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

// routeTestTrigger mirrors the default review trigger shape: PR opened,
// synchronized or marked ready, the ready-for-review label, and a human
// /fs-review comment.
const routeTestTrigger = `trigger: >
  (event.entity.kind == "change_proposal"
    && event.transition.kind in ["opened", "synchronized", "marked_ready"])
  || (event.entity.kind == "change_proposal"
    && event.transition.kind == "label_changed"
    && event.transition.label.name == "ready-for-review"
    && event.transition.label.action == "added")
  || (event.transition.kind == "comment_added"
    && has(event.transition.comment.command)
    && event.transition.comment.command == "/fs-review"
    && event.actor.kind == "human")
`

// routeTestCommentOnlyTrigger is a narrowed override: /fs-review only.
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
	routePROpenedNoRole = `{
  "repo": "fullsend-ai/demo",
  "entity": {"kind": "change_proposal", "id": 7, "url": "https://github.com/fullsend-ai/demo/pull/7"},
  "transition": {"kind": "opened"},
  "actor": {"id": "mallory", "kind": "human", "role": "none", "is_entity_author": true},
  "state": {"labels": []},
  "source": {"system": "github", "raw_type": "pull_request_target", "raw_action": "opened"}
}`
)

func TestRouteBuiltinAgent(t *testing.T) {
	disabled := false
	cases := []struct {
		name         string
		fixture      func(t *testing.T, agentsURL string) routeFixture
		agentsYAML   string // served as the agents-repo review harness
		event        string
		wantDecision string
		wantReason   string
		wantHarness  string
	}{
		{
			name:         "no review entry uses the agents-repo harness and matches",
			fixture:      func(t *testing.T, u string) routeFixture { return routeFixture{allowlist: []string{u}} },
			agentsYAML:   routeTestHarnessBody + routeTestTrigger,
			event:        routePROpened,
			wantDecision: routeRun,
			wantReason:   "trigger matched",
			wantHarness:  "agents@" + routeTestSHA[:12],
		},
		{
			name: "override-only entry still uses the agents-repo harness",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{
					allowlist: []string{u},
					agents:    []config.AgentEntry{{Name: "review", Model: "sonnet"}},
				}
			},
			agentsYAML:   routeTestHarnessBody + routeTestTrigger,
			event:        routeReviewComment,
			wantDecision: routeRun,
			wantHarness:  "agents@" + routeTestSHA[:12],
		},
		{
			name:         "agents-repo harness without a trigger stays on bash",
			fixture:      func(t *testing.T, u string) routeFixture { return routeFixture{allowlist: []string{u}} },
			agentsYAML:   routeTestHarnessBody,
			event:        routePROpened,
			wantDecision: routeBash,
			wantReason:   "resolved harness has no trigger",
		},
		{
			name: "override on a base without a trigger stays on bash",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{
					agents: []config.AgentEntry{{Name: "review", Source: "harness/review.yaml"}},
					files: map[string]string{
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
					},
				}
			},
			event:        routePROpened,
			wantDecision: routeBash,
			wantHarness:  "config:harness/review.yaml",
		},
		{
			name: "override inherits the trigger of its base",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{
					agents: []config.AgentEntry{{Name: "review", Source: "harness/review.yaml"}},
					files: map[string]string{
						"harness/base-new.yaml": routeTestHarnessBody + routeTestTrigger,
						"harness/review.yaml":   "base: base-new.yaml\nmodel: sonnet\n",
					},
				}
			},
			event:        routePROpened,
			wantDecision: routeRun,
		},
		{
			name: "narrowed override skips automatic review",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{
					agents: []config.AgentEntry{{Name: "review", Source: "harness/review.yaml"}},
					files: map[string]string{
						"harness/review.yaml": routeTestHarnessBody + routeTestCommentOnlyTrigger,
					},
				}
			},
			event:        routePROpened,
			wantDecision: routeSkip,
			wantReason:   "trigger did not match",
		},
		{
			name: "narrowed override keeps /fs-review",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{
					agents: []config.AgentEntry{{Name: "review", Source: "harness/review.yaml"}},
					files: map[string]string{
						"harness/review.yaml": routeTestHarnessBody + routeTestCommentOnlyTrigger,
					},
				}
			},
			event:        routeReviewComment,
			wantDecision: routeRun,
		},
		{
			name: "disabled agent is skipped",
			fixture: func(t *testing.T, u string) routeFixture {
				return routeFixture{agents: []config.AgentEntry{{Name: "review", Enabled: &disabled}}}
			},
			event:        routePROpened,
			wantDecision: routeSkip,
			wantReason:   "agent disabled in config",
		},
		{
			name:         "kill switch skips",
			fixture:      func(t *testing.T, u string) routeFixture { return routeFixture{killSwitch: true} },
			event:        routePROpened,
			wantDecision: routeSkip,
			wantReason:   "kill switch active",
		},
		{
			name:         "unauthorized actor is skipped",
			fixture:      func(t *testing.T, u string) routeFixture { return routeFixture{allowlist: []string{u}} },
			agentsYAML:   routeTestHarnessBody + routeTestTrigger,
			event:        routePROpenedNoRole,
			wantDecision: routeSkip,
			wantReason:   "actor not authorized",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agentsYAML := tc.agentsYAML
			if agentsYAML == "" {
				agentsYAML = routeTestHarnessBody + routeTestTrigger
			}
			client, policy, agentsURL := routeAgentsRepo(t, agentsYAML)
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

func TestRouteBuiltinAgent_FallbackUnavailableIsAnError(t *testing.T) {
	neutralizeAgentsRepoFallback(t)
	dir := writeRouteFixture(t, routeFixture{})

	_, err := routeBuiltinAgent(context.Background(), routeOpts{
		agent:       "review",
		configDir:   dir,
		event:       routeEvent(t, routePROpened),
		forgeClient: forge.NewFakeClient(),
		printer:     ui.New(io.Discard),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agents-repo fallback unavailable")
}

func TestRunDispatch_RouteRejectsUnsupportedAgent(t *testing.T) {
	err := runDispatch(context.Background(), dispatchOpts{route: "code", inputDriver: "json"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported --route agent "code"`)
}

func TestRunDispatch_RoutePrintsOneJSONLine(t *testing.T) {
	dir := writeRouteFixture(t, routeFixture{
		agents: []config.AgentEntry{{Name: "review", Source: "harness/review.yaml"}},
		files: map[string]string{
			"harness/review.yaml": routeTestHarnessBody + routeTestCommentOnlyTrigger,
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
