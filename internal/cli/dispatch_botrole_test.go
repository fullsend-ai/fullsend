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
	"sync/atomic"
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
	newDispatchBotRoleResolverFn = func(string) botidentity.BotRoleResolver { return r }
	t.Cleanup(func() { newDispatchBotRoleResolverFn = orig })
}

var errFakeLookup = fmt.Errorf("%w: mint answered HTTP 503: unavailable", botidentity.ErrLookup)

var errFakeConfig = fmt.Errorf("%w: %w: no mint URL", botidentity.ErrLookup, errBotRoleConfig)

// captureBotRoleReports runs reportBotRoleDenial as in GitHub Actions, with
// its annotations captured in the returned buffer and its step summary in a
// temp file, so tests never write to the real job's summary or annotations.
func captureBotRoleReports(t *testing.T) (*bytes.Buffer, string) {
	t.Helper()
	var annotations bytes.Buffer
	orig := botRoleAnnotationWriter
	botRoleAnnotationWriter = &annotations
	t.Cleanup(func() { botRoleAnnotationWriter = orig })
	summary := filepath.Join(t.TempDir(), "step-summary.md")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_STEP_SUMMARY", summary)
	return &annotations, summary
}

func readSummary(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

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
		// wantReport is the annotation title of a denial; "" means no
		// annotation and no step summary line.
		wantReport string
		// wantRemedy is text the denial must carry to tell the reader
		// what to do.
		wantRemedy string
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
			wantReport:   "Fullsend bot has no registered role",
			wantRemedy:   "Register fullsend-ai-coder[bot] with the mint",
		},
		{
			name:         "lookup failure is unverified, not an error",
			event:        botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, opened),
			resolver:     &fakeBotRoleResolver{err: errFakeLookup},
			wantQueried:  true,
			wantActor:    normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorBot, Role: normevent.RoleNone, RoleVerified: &unverified},
			wantLog:      "Bot-role lookup failed",
			wantDenyNote: "bot-role lookup failed",
			wantReport:   "Fullsend bot-role lookup failed",
			wantRemedy:   "re-run the workflow",
		},
		{
			name:         "unresolved mint configuration is named as a setup problem",
			event:        botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, opened),
			resolver:     &fakeBotRoleResolver{err: errFakeConfig},
			wantQueried:  true,
			wantActor:    normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorBot, Role: normevent.RoleNone, RoleVerified: &unverified},
			wantLog:      "could not resolve the mint configuration",
			wantDenyNote: "bot-role lookup failed",
			wantReport:   "Fullsend bot-role configuration unresolved",
			wantRemedy:   "Fix this repository's Fullsend setup",
		},
		{
			name:         "missing resolver fails closed",
			event:        botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, opened),
			nilResolver:  true,
			wantActor:    normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorBot, Role: normevent.RoleNone, RoleVerified: &unverified},
			wantLog:      "no bot-role resolver",
			wantDenyNote: "bot-role lookup failed",
			wantReport:   "Fullsend bot-role lookup failed",
		},
		{
			name:         "label removed still needs the lookup",
			event:        botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, labelRemoved),
			resolver:     &fakeBotRoleResolver{},
			wantQueried:  true,
			wantActor:    normevent.Actor{ID: "fullsend-ai-coder[bot]", Kind: normevent.ActorBot, Role: normevent.RoleNone, RoleVerified: &verified},
			wantDenyNote: "bot has no registered role",
			wantReport:   "Fullsend bot has no registered role",
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
			annotations, summaryPath := captureBotRoleReports(t)
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

			summary := readSummary(t, summaryPath)
			if tc.wantReport == "" {
				assert.Empty(t, annotations.String())
				assert.Empty(t, summary)
				return
			}
			// One ::warning:: workflow command naming the reason, the
			// actor and the repository, and one step summary line.
			lines := strings.Split(strings.TrimSuffix(annotations.String(), "\n"), "\n")
			require.Len(t, lines, 1, "annotations: %q", annotations.String())
			assert.True(t, strings.HasPrefix(lines[0], "::warning title="+tc.wantReport+"::"), "annotation: %q", lines[0])
			assert.Contains(t, lines[0], "fullsend-ai-coder[bot]")
			assert.Contains(t, lines[0], "fullsend-ai/demo")
			assert.Contains(t, lines[0], tc.wantRemedy)
			assert.Contains(t, summary, tc.wantReport)
			assert.Contains(t, summary, "fullsend-ai-coder[bot]")
			assert.Contains(t, summary, tc.wantRemedy)
			assert.Equal(t, 1, strings.Count(summary, "\n"), "summary: %q", summary)
		})
	}

	t.Run("nil event and nil printer are safe", func(t *testing.T) {
		captureBotRoleReports(t)
		applyBotRoleLookup(context.Background(), nil, &fakeBotRoleResolver{}, nil)
		ev := botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, opened)
		applyBotRoleLookup(context.Background(), ev, &fakeBotRoleResolver{role: "review"}, nil)
		assert.Equal(t, "review", ev.Actor.BotRole)
	})
}

// Event data in a denial cannot inject a second workflow command or break
// the summary line.
func TestReportBotRoleDenial_EscapesEventData(t *testing.T) {
	annotations, summaryPath := captureBotRoleReports(t)
	ev := botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, normevent.Transition{Kind: normevent.TransitionOpened})
	ev.Actor.ID = "evil[bot]\n::error::injected\r\n%0A::set-output name=x::y"
	applyBotRoleLookup(context.Background(), ev, &fakeBotRoleResolver{}, ui.New(io.Discard))

	out := annotations.String()
	require.Equal(t, 1, strings.Count(out, "\n"), "annotations: %q", out)
	assert.True(t, strings.HasPrefix(out, "::warning title=Fullsend bot has no registered role::"), "annotation: %q", out)
	assert.NotContains(t, out, "\r")
	assert.Contains(t, out, "%250A", "a literal %0A must not decode to a newline")
	assert.Equal(t, 1, strings.Count(readSummary(t, summaryPath), "\n"))
}

// Outside GitHub Actions no workflow command is printed, and without
// GITHUB_STEP_SUMMARY nothing is written; an unwritable summary is ignored.
// The deny decision is the same in every case.
func TestReportBotRoleDenial_OutsideActions(t *testing.T) {
	annotations, _ := captureBotRoleReports(t)
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	ev := botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, normevent.Transition{Kind: normevent.TransitionOpened})
	applyBotRoleLookup(context.Background(), ev, &fakeBotRoleResolver{}, ui.New(io.Discard))
	assert.Empty(t, annotations.String())
	assert.Equal(t, "bot has no registered role", botRoleDenialReason(ev.Actor))

	t.Setenv("GITHUB_STEP_SUMMARY", t.TempDir()) // a directory cannot be opened for append
	ev = botRoleTestEvent(normevent.ActorBot, normevent.SystemGitHub, normevent.Transition{Kind: normevent.TransitionOpened})
	applyBotRoleLookup(context.Background(), ev, &fakeBotRoleResolver{err: errFakeLookup}, ui.New(io.Discard))
	assert.Empty(t, annotations.String())
	assert.Equal(t, "bot-role lookup failed", botRoleDenialReason(ev.Actor))
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

	res, err := newDispatchBotRoleResolverFn("").ResolveBotRole(context.Background(), botidentity.BotRoleQuery{
		Forge: "github", Target: "fullsend-ai/demo", Login: "fullsend-ai-review[bot]",
	})
	require.NoError(t, err)
	assert.Equal(t, "review", res.Role)
}

func TestDefaultDispatchBotRoleResolver_FailsClosedWithoutEnvironment(t *testing.T) {
	t.Setenv(forge.VarMintURL, "https://mint.example.invalid")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	_, err := newDispatchBotRoleResolverFn("").ResolveBotRole(context.Background(), botidentity.BotRoleQuery{
		Forge: "github", Target: "fullsend-ai/demo", Login: "fullsend-ai-review[bot]",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, botidentity.ErrLookup))
	assert.True(t, errors.Is(err, errBotRoleConfig), "a missing OIDC environment is a setup problem: %v", err)

	t.Setenv(forge.VarMintURL, "")
	_, err = newDispatchBotRoleResolverFn("").ResolveBotRole(context.Background(), botidentity.BotRoleQuery{
		Forge: "github", Target: "fullsend-ai/demo", Login: "fullsend-ai-review[bot]",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, botidentity.ErrLookup))
	assert.True(t, errors.Is(err, errBotRoleConfig), "an unset mint URL is a setup problem: %v", err)
	assert.Contains(t, err.Error(), "no mint URL")
}

// A mint that answers with an error is a lookup failure, not a setup
// problem, so its denial asks for a retry rather than a config fix.
func TestDefaultDispatchBotRoleResolver_MintErrorIsNotConfigFailure(t *testing.T) {
	envMintServers(t)
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(mint.Close)
	t.Setenv(forge.VarMintURL, mint.URL)
	_, err := newDispatchBotRoleResolverFn("").ResolveBotRole(context.Background(), botidentity.BotRoleQuery{
		Forge: "github", Target: "fullsend-ai/demo", Login: "fullsend-ai-review[bot]",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, botidentity.ErrLookup))
	assert.False(t, errors.Is(err, errBotRoleConfig), "err: %v", err)
}

func TestDefaultDispatchBotRoleResolver_UsesLayeredMintURL(t *testing.T) {
	jwt := fakeJWT()
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"value": jwt})
	}))
	t.Cleanup(oidc.Close)
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(botidentity.MintBotRoleResponse{Outcome: botidentity.MintOutcomeRecognized, Role: "coder"})
	}))
	t.Cleanup(mint.Close)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("mint_url: "+mint.URL+"\n"), 0o644))
	// The environment variable points nowhere: the file layer must win.
	t.Setenv(forge.VarMintURL, "https://env-mint.example.invalid")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidc.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")

	res, err := newDispatchBotRoleResolverFn(dir).ResolveBotRole(context.Background(), botidentity.BotRoleQuery{
		Forge: "github", Target: "fullsend-ai/demo", Login: "fullsend-ai-coder[bot]",
	})
	require.NoError(t, err)
	assert.Equal(t, "coder", res.Role)

	// A file-only mint_url works with FULLSEND_MINT_URL unset.
	t.Setenv(forge.VarMintURL, "")
	res, err = newDispatchBotRoleResolverFn(dir).ResolveBotRole(context.Background(), botidentity.BotRoleQuery{
		Forge: "github", Target: "fullsend-ai/demo", Login: "fullsend-ai-coder[bot]",
	})
	require.NoError(t, err)
	assert.Equal(t, "coder", res.Role)
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
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITHUB_STEP_SUMMARY", "")
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
		wantReport  string
	}{
		{name: "recognized bot dispatches", action: "opened", resolver: &fakeBotRoleResolver{role: "coder"}, wantProbe: true, wantQueried: true},
		{name: "unregistered bot is denied", action: "opened", resolver: &fakeBotRoleResolver{}, wantQueried: true, wantReport: "Fullsend bot has no registered role"},
		{name: "lookup failure is denied without an error", action: "opened", resolver: &fakeBotRoleResolver{err: errFakeLookup}, wantQueried: true, wantReport: "Fullsend bot-role lookup failed"},
		{name: "unresolved configuration is denied without an error", action: "opened", resolver: &fakeBotRoleResolver{err: errFakeConfig}, wantQueried: true, wantReport: "Fullsend bot-role configuration unresolved"},
		{name: "label added by an unregistered bot is the control", action: "labeled", label: "ready-for-probe", resolver: &fakeBotRoleResolver{err: errFakeLookup}, wantProbe: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeBotProbeFixture(t)
			writeGHABotPREvent(t, tc.action, tc.label)
			annotations, summaryPath := captureBotRoleReports(t)
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
			// The denial is reported, but the command still succeeds
			// with an empty matrix.
			if tc.wantReport == "" {
				assert.Empty(t, annotations.String())
			} else {
				assert.Contains(t, annotations.String(), "::warning title="+tc.wantReport+"::")
				assert.Contains(t, readSummary(t, summaryPath), tc.wantReport)
			}
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
			annotations, _ := captureBotRoleReports(t)
			useBotRoleResolver(t, tc.resolver)

			out, err := captureDispatch(t, dispatchOpts{inputDriver: "gha-event", configDir: dir, route: "review"})
			require.NoError(t, err)
			var got routeDecision
			require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &got), "decision: %q", out)
			assert.Equal(t, tc.wantDecision, got.Decision)
			assert.Equal(t, tc.wantReason, got.Reason)
			require.Len(t, tc.resolver.queries, 1)
			// A routeSkip denial is annotated like a harness dispatch one;
			// the annotation stays off stdout, which holds only the decision.
			if tc.wantDecision == routeSkip {
				assert.Contains(t, annotations.String(), "::warning title=")
			} else {
				assert.Empty(t, annotations.String())
			}
			assert.NotContains(t, out, "::warning")
		})
	}
}

// envMintServers serves the Actions OIDC token endpoint and a mint that
// recognizes every bot as coder, points FULLSEND_MINT_URL and the Actions OIDC
// environment at them, and returns a counter of mint lookups. A lookup that
// reaches the mint registers the bot, so a denial proves it was not reached.
func envMintServers(t *testing.T) *atomic.Int32 {
	t.Helper()
	jwt := fakeJWT()
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"value": jwt})
	}))
	t.Cleanup(oidc.Close)
	var hits atomic.Int32
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(botidentity.MintBotRoleResponse{Outcome: botidentity.MintOutcomeRecognized, Role: "coder"})
	}))
	t.Cleanup(mint.Close)
	t.Setenv(forge.VarMintURL, mint.URL)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidc.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	return &hits
}

// malformedConfigDirs returns config directories whose files exist but do not
// load: malformed YAML in config.yaml, and in config.base.yaml.
func malformedConfigDirs(t *testing.T) map[string]string {
	t.Helper()
	overlay := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(overlay, "config.yaml"), []byte("agents: [\n"), 0o644))
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "config.base.yaml"), []byte("agents: [\n"), 0o644))
	return map[string]string{"malformed config.yaml": overlay, "malformed config.base.yaml": base}
}

// An absent optional config is no file layer, so FULLSEND_MINT_URL applies; a
// config that exists but fails to load or parse is a failed lookup, and the
// environment mint is never contacted.
func TestDefaultDispatchBotRoleResolver_ConfigLoadFailureIsLookupFailure(t *testing.T) {
	query := botidentity.BotRoleQuery{Forge: "github", Target: "fullsend-ai/demo", Login: "fullsend-ai-coder[bot]"}

	t.Run("absent config falls back to the environment mint", func(t *testing.T) {
		hits := envMintServers(t)
		res, err := newDispatchBotRoleResolverFn(t.TempDir()).ResolveBotRole(context.Background(), query)
		require.NoError(t, err)
		assert.Equal(t, "coder", res.Role)
		assert.Equal(t, int32(1), hits.Load())
	})

	for name, dir := range malformedConfigDirs(t) {
		t.Run(name, func(t *testing.T) {
			hits := envMintServers(t)
			_, err := newDispatchBotRoleResolverFn(dir).ResolveBotRole(context.Background(), query)
			require.Error(t, err)
			assert.True(t, errors.Is(err, botidentity.ErrLookup), "err: %v", err)
			assert.True(t, errors.Is(err, errBotRoleConfig), "err: %v", err)
			assert.Equal(t, int32(0), hits.Load(), "a malformed config must not fall back to FULLSEND_MINT_URL")
		})
	}
}

// A malformed config denies the bot in --route as a failed lookup, even with
// an environment mint that would recognize it, instead of falling back to
// FULLSEND_MINT_URL and routing it.
func TestRunDispatch_GHAEventBotRoleRoute_MalformedConfigDeniesBot(t *testing.T) {
	for name, dir := range malformedConfigDirs(t) {
		t.Run(name, func(t *testing.T) {
			hits := envMintServers(t)
			writeGHABotPREvent(t, "opened", "")

			out, err := captureDispatch(t, dispatchOpts{inputDriver: "gha-event", configDir: dir, route: "review"})
			require.NoError(t, err)
			var got routeDecision
			require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &got), "decision: %q", out)
			assert.Equal(t, routeSkip, got.Decision)
			assert.Equal(t, "bot-role lookup failed", got.Reason)
			assert.Equal(t, int32(0), hits.Load())
		})
	}
}

// Harness dispatch with a malformed config never reaches the environment
// mint to recognize the bot, and it dispatches nothing: the config error is
// the command's error. An absent config still resolves the bot through the
// environment mint.
func TestRunDispatch_GHAEventBotRoleMatrix_ConfigLoadFailure(t *testing.T) {
	for name, dir := range malformedConfigDirs(t) {
		t.Run(name, func(t *testing.T) {
			hits := envMintServers(t)
			writeGHABotPREvent(t, "opened", "")

			out, err := captureDispatch(t, dispatchOpts{inputDriver: "gha-event", outputDriver: "gha-matrix", configDir: dir})
			require.Error(t, err)
			assert.NotContains(t, out, "include")
			assert.Equal(t, int32(0), hits.Load())
		})
	}

	t.Run("absent config resolves the bot through the environment mint", func(t *testing.T) {
		hits := envMintServers(t)
		writeGHABotPREvent(t, "opened", "")

		_, err := captureDispatch(t, dispatchOpts{inputDriver: "gha-event", outputDriver: "gha-matrix", configDir: t.TempDir()})
		require.NoError(t, err)
		assert.Equal(t, int32(1), hits.Load())
	})
}
