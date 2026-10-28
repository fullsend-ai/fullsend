package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/botidentity"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// fakeBotRoleResolver answers every lookup with role and err and records
// the queries it received.
type fakeBotRoleResolver struct {
	role    string
	err     error
	queries []botidentity.BotRoleQuery
}

func (f *fakeBotRoleResolver) ResolveBotRole(_ context.Context, q botidentity.BotRoleQuery) (botidentity.BotRoleResult, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return botidentity.BotRoleResult{}, f.err
	}
	return botidentity.BotRoleResult{Role: f.role}, nil
}

func useBotRoleResolver(t *testing.T, r botidentity.BotRoleResolver) {
	t.Helper()
	orig := newDispatchBotRoleResolverFn
	newDispatchBotRoleResolverFn = func() botidentity.BotRoleResolver { return r }
	t.Cleanup(func() { newDispatchBotRoleResolverFn = orig })
}

var errFakeLookup = fmt.Errorf("%w: mint answered HTTP 503: unavailable", botidentity.ErrLookup)

func botRoleTestEvent(kind normevent.ActorKind, system normevent.SourceSystem, transition normevent.Transition) *normevent.Event {
	return &normevent.Event{
		Repo:       "fullsend-ai/demo",
		Entity:     normevent.Entity{Kind: normevent.EntityChangeProposal, ID: 7, URL: "https://github.com/fullsend-ai/demo/pull/7"},
		Transition: transition,
		Actor:      normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: kind, Role: normevent.RoleWrite},
		State:      normevent.State{Labels: []string{}},
		Source:     normevent.Source{System: system, RawType: "pull_request_target", RawAction: "opened"},
	}
}

func TestApplyBotRoleLookup(t *testing.T) {
	opened := normevent.Transition{Kind: normevent.TransitionOpened}
	labelAdded := normevent.Transition{Kind: normevent.TransitionLabelChanged, Label: &normevent.LabelChange{Name: "ready-to-code", Action: "added"}}
	labelRemoved := normevent.Transition{Kind: normevent.TransitionLabelChanged, Label: &normevent.LabelChange{Name: "ready-to-code", Action: "removed"}}
	verified, unverified := true, false

	cases := []struct {
		name         string
		event        *normevent.Event
		resolver     botidentity.BotRoleResolver
		nilResolver  bool
		wantQueried  bool
		wantActor    normevent.Actor
		wantLog      string
		wantDenyNote string
	}{
		{
			name:        "recognized bot gets its registered role",
			event:       botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, opened),
			resolver:    &fakeBotRoleResolver{role: "coder"},
			wantQueried: true,
			wantActor:   normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorBot, Role: normevent.RoleNone, BotRole: "coder", RoleVerified: &verified},
			wantLog:     "registered as coder",
		},
		{
			name:         "no-match is a completed lookup without a role",
			event:        botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, opened),
			resolver:     &fakeBotRoleResolver{},
			wantQueried:  true,
			wantActor:    normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorBot, Role: normevent.RoleNone, RoleVerified: &verified},
			wantLog:      "has no registered role",
			wantDenyNote: "bot has no registered role",
		},
		{
			name:         "lookup failure is unverified, not an error",
			event:        botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, opened),
			resolver:     &fakeBotRoleResolver{err: errFakeLookup},
			wantQueried:  true,
			wantActor:    normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorBot, Role: normevent.RoleNone, RoleVerified: &unverified},
			wantLog:      "Bot-role lookup failed",
			wantDenyNote: "bot-role lookup failed",
		},
		{
			name:         "missing resolver fails closed",
			event:        botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, opened),
			nilResolver:  true,
			wantActor:    normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorBot, Role: normevent.RoleNone, RoleVerified: &unverified},
			wantLog:      "no bot-role resolver",
			wantDenyNote: "bot-role lookup failed",
		},
		{
			name:         "label removed still needs the lookup",
			event:        botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, labelRemoved),
			resolver:     &fakeBotRoleResolver{},
			wantQueried:  true,
			wantActor:    normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorBot, Role: normevent.RoleNone, RoleVerified: &verified},
			wantDenyNote: "bot has no registered role",
		},
		{
			name:      "label added keeps the exception without a lookup",
			event:     botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, labelAdded),
			resolver:  &fakeBotRoleResolver{err: errFakeLookup},
			wantActor: normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorBot, Role: normevent.RoleWrite},
		},
		{
			name:      "human actor is not looked up",
			event:     botRoleTestEvent(normevent.ActorHuman, normevent.SystemGitHub, opened),
			resolver:  &fakeBotRoleResolver{err: errFakeLookup},
			wantActor: normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorHuman, Role: normevent.RoleWrite},
		},
		{
			name:      "non-GitHub bot is not looked up",
			event:     botRoleTestEvent(normevent.ActorBot, normevent.SystemJira, opened),
			resolver:  &fakeBotRoleResolver{err: errFakeLookup},
			wantActor: normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorBot, Role: normevent.RoleWrite},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			resolver := tc.resolver
			if tc.nilResolver {
				resolver = nil
			}
			applyBotRoleLookup(context.Background(), tc.event, resolver, ui.New(&buf))
			assert.Equal(t, tc.wantActor, tc.event.Actor)
			if fake, ok := tc.resolver.(*fakeBotRoleResolver); ok {
				if tc.wantQueried {
					assert.Equal(t, []botidentity.BotRoleQuery{{Forge: "github", Target: "fullsend-ai/demo", Login: "fullsend-ai-coder[bot]"}}, fake.queries)
				} else {
					assert.Empty(t, fake.queries)
				}
			}
			if tc.wantLog != "" {
				assert.Contains(t, buf.String(), tc.wantLog)
			}
			assert.Equal(t, tc.wantDenyNote, botRoleDenialReason(tc.event.Actor))
		})
	}

	t.Run("nil event and nil printer are safe", func(t *testing.T) {
		applyBotRoleLookup(context.Background(), nil, &fakeBotRoleResolver{}, nil)
		ev := botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, opened)
		applyBotRoleLookup(context.Background(), ev, &fakeBotRoleResolver{role: "review"}, nil)
		assert.Equal(t, "review", ev.Actor.BotRole)
	})
}

func TestBotRoleDenialReason_RecognizedAndHuman(t *testing.T) {
	verified := true
	assert.Empty(t, botRoleDenialReason(normevent.Actor{Kind: normevent.ActorBot, BotRole: "review", RoleVerified: &verified}))
	assert.Empty(t, botRoleDenialReason(normevent.Actor{Kind: normevent.ActorBot}))
	assert.Empty(t, botRoleDenialReason(normevent.Actor{Kind: normevent.ActorHuman, RoleVerified: &verified}))
}

// fakeJWT returns an unsigned JWT carrying iat and exp claims.
func fakeJWT() string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		enc.EncodeToString([]byte(`{"iat":1700000000,"exp":1700000600}`)) + ".sig"
}

// TestDefaultDispatchBotRoleResolver runs the production resolver against a
// loopback OIDC endpoint and mint: it reads FULLSEND_MINT_URL and the
// Actions OIDC environment and sends the mint-audience token.
func TestDefaultDispatchBotRoleResolver(t *testing.T) {
	jwt := fakeJWT()
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "bearer request-token", r.Header.Get("Authorization"))
		assert.Equal(t, "fullsend-mint", r.URL.Query().Get("audience"))
		_ = json.NewEncoder(w).Encode(map[string]string{"value": jwt})
	}))
	t.Cleanup(oidc.Close)
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, botidentity.MintBotRolePath, r.URL.Path)
		assert.Equal(t, "Bearer "+jwt, r.Header.Get("Authorization"))
		var req botidentity.MintBotRoleRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, botidentity.MintBotRoleRequest{ActorLogin: "fullsend-ai-review[bot]", Repo: "fullsend-ai/demo"}, req)
		_ = json.NewEncoder(w).Encode(botidentity.MintBotRoleResponse{Outcome: botidentity.MintOutcomeRecognized, Role: "review"})
	}))
	t.Cleanup(mint.Close)

	t.Setenv(forge.VarMintURL, mint.URL)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidc.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")

	res, err := newDispatchBotRoleResolverFn().ResolveBotRole(context.Background(), botidentity.BotRoleQuery{
		Forge: "github", Target: "fullsend-ai/demo", Login: "fullsend-ai-review[bot]",
	})
	require.NoError(t, err)
	assert.Equal(t, "review", res.Role)
}

func TestDefaultDispatchBotRoleResolver_FailsClosedWithoutEnvironment(t *testing.T) {
	t.Setenv(forge.VarMintURL, "https://mint.example.invalid")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	_, err := newDispatchBotRoleResolverFn().ResolveBotRole(context.Background(), botidentity.BotRoleQuery{
		Forge: "github", Target: "fullsend-ai/demo", Login: "fullsend-ai-review[bot]",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, botidentity.ErrLookup))

	t.Setenv(forge.VarMintURL, "")
	_, err = newDispatchBotRoleResolverFn().ResolveBotRole(context.Background(), botidentity.BotRoleQuery{
		Forge: "github", Target: "fullsend-ai/demo", Login: "fullsend-ai-review[bot]",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, botidentity.ErrLookup))
}

// botProbeHarness is a minimal non-built-in harness whose trigger matches a
// pull request opened and the ready-for-probe label, so the label row is the
// control that proves the harness and trigger loaded.
const botProbeHarness = `agent: agents/triage.md
role: triage
slug: fullsend-ai-bot-probe
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
trigger: |
  event.entity.kind == "change_proposal"
  && (event.transition.kind == "opened"
      || (event.transition.kind == "label_changed"
          && event.transition.label.name == "ready-for-probe"))
`

func writeBotProbeFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".fullsend")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "bot-probe.yaml"), []byte(botProbeHarness), 0o644))
	cfg := config.NewPerRepoConfig(nil, "fullsend-ai/demo")
	cfg.SetAgents([]config.AgentEntry{{Name: "bot-probe", Source: "harness/bot-probe.yaml"}})
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644))
	return dir
}

// writeGHABotPREvent writes a GitHub pull_request_target webhook payload
// sent by a bot and points the gha-event driver at it. No GitHub
// credentials are available, so the collaborator lookup is skipped.
func writeGHABotPREvent(t *testing.T, action, label string) {
	t.Helper()
	payload := map[string]any{
		"action": action,
		"sender": map[string]any{"login": "fullsend-ai-coder[bot]", "type": "Bot"},
		"pull_request": map[string]any{
			"number":   7,
			"html_url": "https://github.com/fullsend-ai/demo/pull/7",
			"user":     map[string]any{"login": "fullsend-ai-coder[bot]"},
			"labels":   []any{},
			"head":     map[string]any{"ref": "agent/7", "sha": "abc", "repo": map[string]any{"full_name": "fullsend-ai/demo"}},
			"base":     map[string]any{"ref": "main", "repo": map[string]any{"full_name": "fullsend-ai/demo"}},
		},
	}
	if label != "" {
		payload["label"] = map[string]any{"name": label}
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	t.Setenv("GITHUB_EVENT_PATH", path)
	t.Setenv("GITHUB_REPOSITORY", "fullsend-ai/demo")
	t.Setenv("GITHUB_EVENT_NAME", "pull_request_target")
	t.Setenv("GITHUB_ACTOR", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", "/nonexistent")
}

func captureDispatch(t *testing.T, opts dispatchOpts) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	errCh := make(chan error, 1)
	go func() {
		defer w.Close()
		errCh <- runDispatch(context.Background(), opts)
	}()
	out, _ := io.ReadAll(r)
	os.Stdout = old
	return string(out), <-errCh
}

// TestRunDispatch_GHAEventBotRoleMatrix checks the observable harness
// dispatch result for bot actors of gha-event input: a recognized bot
// dispatches the probe harness, a no-match or failed lookup yields an empty
// matrix without an error, and a label added by an unregistered bot still
// dispatches without a lookup.
func TestRunDispatch_GHAEventBotRoleMatrix(t *testing.T) {
	cases := []struct {
		name        string
		action      string
		label       string
		resolver    *fakeBotRoleResolver
		wantProbe   bool
		wantQueried bool
	}{
		{name: "recognized bot dispatches", action: "opened", resolver: &fakeBotRoleResolver{role: "coder"}, wantProbe: true, wantQueried: true},
		{name: "unregistered bot is denied", action: "opened", resolver: &fakeBotRoleResolver{}, wantQueried: true},
		{name: "lookup failure is denied without an error", action: "opened", resolver: &fakeBotRoleResolver{err: errFakeLookup}, wantQueried: true},
		{name: "label added by an unregistered bot is the control", action: "labeled", label: "ready-for-probe", resolver: &fakeBotRoleResolver{err: errFakeLookup}, wantProbe: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeBotProbeFixture(t)
			writeGHABotPREvent(t, tc.action, tc.label)
			useBotRoleResolver(t, tc.resolver)

			out, err := captureDispatch(t, dispatchOpts{inputDriver: "gha-event", outputDriver: "gha-matrix", configDir: dir})
			require.NoError(t, err)
			var matrix struct {
				Include []map[string]any `json:"include"`
			}
			require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &matrix), "matrix: %q", out)
			if tc.wantProbe {
				require.Len(t, matrix.Include, 1)
				assert.Equal(t, "bot-probe", matrix.Include[0]["agent"])
			} else {
				assert.Empty(t, matrix.Include)
			}
			assert.Equal(t, tc.wantQueried, len(tc.resolver.queries) == 1, "queries: %v", tc.resolver.queries)
		})
	}
}

// TestRunDispatch_GHAEventBotRoleRoute checks that --route sees the lookup
// result: a recognized bot runs review, and a no-match or a failed lookup
// is a skip decision with a distinct reason, never a CLI error that would
// fall back to bash routing.
func TestRunDispatch_GHAEventBotRoleRoute(t *testing.T) {
	cases := []struct {
		name         string
		resolver     *fakeBotRoleResolver
		wantDecision string
		wantReason   string
	}{
		{name: "recognized bot runs", resolver: &fakeBotRoleResolver{role: "coder"}, wantDecision: routeRun, wantReason: "trigger matched"},
		{name: "unregistered bot skips", resolver: &fakeBotRoleResolver{}, wantDecision: routeSkip, wantReason: "bot has no registered role"},
		{name: "lookup failure skips", resolver: &fakeBotRoleResolver{err: errFakeLookup}, wantDecision: routeSkip, wantReason: "bot-role lookup failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeRouteFixture(t, routeFixture{
				agents: []config.AgentEntry{{
					Name:    "review",
					Source:  "harness/review.yaml",
					Trigger: `event.entity.kind == "change_proposal" && event.transition.kind == "opened"`,
				}},
				files: map[string]string{"harness/review.yaml": routeTestHarnessBody},
			})
			writeGHABotPREvent(t, "opened", "")
			useBotRoleResolver(t, tc.resolver)

			out, err := captureDispatch(t, dispatchOpts{inputDriver: "gha-event", configDir: dir, route: "review"})
			require.NoError(t, err)
			var got routeDecision
			require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &got), "decision: %q", out)
			assert.Equal(t, tc.wantDecision, got.Decision)
			assert.Equal(t, tc.wantReason, got.Reason)
			require.Len(t, tc.resolver.queries, 1)
		})
	}
}
