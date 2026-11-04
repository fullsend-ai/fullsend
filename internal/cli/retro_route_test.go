package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch/input"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// retroCommandOnlyTrigger turns off the automatic retro on close and keeps
// /fs-retro.
const retroCommandOnlyTrigger = `event.transition.kind == "comment_added"
&& has(event.transition.comment.command)
&& event.transition.comment.command == "/fs-retro"`

const routeTestRetroHarness = `agent: agents/retro.md
role: retro
slug: fullsend-ai-retro
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
`

// sourcedRouteFixture registers agent's entries with a local harness file,
// so the route reads the harness role: (and its threshold) without a fetch.
func sourcedRouteFixture(entries []config.AgentEntry, agent, harnessBody string) routeFixture {
	path := "harness/" + agent + ".yaml"
	out := make([]config.AgentEntry, len(entries))
	for i, e := range entries {
		if strings.EqualFold(e.Name, agent) && e.Source == "" {
			e.Source = path
		}
		out[i] = e
	}
	return routeFixture{agents: out, files: map[string]string{path: harnessBody}}
}

// routeRetroEvent builds a normalized event for the retro route tests.
func routeRetroEvent(entity, transition, actor, rawType, rawAction string) string {
	return `{
  "repo": "fullsend-ai/demo",
  "entity": ` + entity + `,
  "transition": ` + transition + `,
  "actor": ` + actor + `,
  "state": {"labels": []},
  "source": {"system": "github", "raw_type": "` + rawType + `", "raw_action": "` + rawAction + `"}
}`
}

const (
	routeRetroPR           = `{"kind": "change_proposal", "id": 7, "url": "https://github.com/fullsend-ai/demo/pull/7"}`
	routeRetroIssue        = `{"kind": "work_item", "id": 8, "url": "https://github.com/fullsend-ai/demo/issues/8"}`
	routeRetroWriter       = `{"id": "alice", "kind": "human", "role": "write", "is_entity_author": false}`
	routeRetroReadAuthor   = `{"id": "contributor", "kind": "human", "role": "read", "is_entity_author": true}`
	routeRetroTriager      = `{"id": "tri", "kind": "human", "role": "triage", "is_entity_author": false}`
	routeRetroAppBot       = `{"id": "dep-updater[bot]", "kind": "bot", "role": "none", "is_entity_author": true}`
	routeRetroClosed       = `{"kind": "closed"}`
	routeRetroMerged       = `{"kind": "merged"}`
	routeRetroCommand      = `{"kind": "comment_added", "comment": {"id": "1", "command": "/fs-retro", "body": "/fs-retro"}}`
	routeRetroOtherCommand = `{"kind": "comment_added", "comment": {"id": "1", "command": "/fs-review", "body": "/fs-review"}}`
)

func TestRetroRouteAccepts(t *testing.T) {
	cases := map[string]struct {
		event string
		want  bool
	}{
		"PR closed":          {routeRetroEvent(routeRetroPR, routeRetroClosed, routeRetroWriter, "pull_request_target", "closed"), true},
		"PR merged":          {routeRetroEvent(routeRetroPR, routeRetroMerged, routeRetroWriter, "pull_request_target", "closed"), true},
		"/fs-retro on a PR":  {routeRetroEvent(routeRetroPR, routeRetroCommand, routeRetroWriter, "issue_comment", "created"), true},
		"/fs-retro on issue": {routeRetroEvent(routeRetroIssue, routeRetroCommand, routeRetroWriter, "issue_comment", "created"), true},
		"/fs-review on a PR": {routeRetroEvent(routeRetroPR, routeRetroOtherCommand, routeRetroWriter, "issue_comment", "created"), false},
		"issue closed":       {routeRetroEvent(routeRetroIssue, routeRetroClosed, routeRetroWriter, "issues", "closed"), false},
		"PR opened":          {routePROpened, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, retroRouteAccepts(routeEvent(t, tc.event)))
		})
	}
}

func TestRouteBuiltinAgent_Retro(t *testing.T) {
	cases := []struct {
		name         string
		agents       []config.AgentEntry
		event        string
		wantDecision string
		wantReason   string
	}{
		{
			name:         "no retro entry stays on bash",
			event:        routeRetroEvent(routeRetroPR, routeRetroClosed, routeRetroWriter, "pull_request_target", "closed"),
			wantDecision: routeBash, wantReason: "no agents[].trigger",
		},
		{
			name:         "command-only trigger skips the retro on close",
			agents:       []config.AgentEntry{{Name: "retro", Trigger: retroCommandOnlyTrigger}},
			event:        routeRetroEvent(routeRetroPR, routeRetroMerged, routeRetroWriter, "pull_request_target", "closed"),
			wantDecision: routeSkip, wantReason: "trigger did not match",
		},
		{
			name:         "command-only trigger keeps /fs-retro on an issue",
			agents:       []config.AgentEntry{{Name: "retro", Trigger: retroCommandOnlyTrigger}},
			event:        routeRetroEvent(routeRetroIssue, routeRetroCommand, routeRetroWriter, "issue_comment", "created"),
			wantDecision: routeRun,
		},
		{
			name:         "PR merged by a write user starts a retro",
			agents:       []config.AgentEntry{{Name: "retro", Trigger: routeTrigger(t, "retro")}},
			event:        routeRetroEvent(routeRetroPR, routeRetroMerged, routeRetroWriter, "pull_request_target", "closed"),
			wantDecision: routeRun,
		},
		{
			name:         "PR closed by its read-only author starts a retro (lifecycle-close exception)",
			agents:       []config.AgentEntry{{Name: "retro", Trigger: routeTrigger(t, "retro")}},
			event:        routeRetroEvent(routeRetroPR, routeRetroClosed, routeRetroReadAuthor, "pull_request_target", "closed"),
			wantDecision: routeRun,
		},
		{
			name:         "PR closed by an app bot without a role starts a retro (lifecycle-close exception)",
			agents:       []config.AgentEntry{{Name: "retro", Trigger: routeTrigger(t, "retro")}},
			event:        routeRetroEvent(routeRetroPR, routeRetroClosed, routeRetroAppBot, "pull_request_target", "closed"),
			wantDecision: routeRun,
		},
		{
			name:         "PR closed by a triage user starts a retro (lifecycle-close exception)",
			agents:       []config.AgentEntry{{Name: "retro", Trigger: routeTrigger(t, "retro")}},
			event:        routeRetroEvent(routeRetroPR, routeRetroClosed, routeRetroTriager, "pull_request_target", "closed"),
			wantDecision: routeRun,
		},
		{
			name:         "/fs-retro from a triage user is not authorized",
			agents:       []config.AgentEntry{{Name: "retro", Trigger: routeTrigger(t, "retro")}},
			event:        routeRetroEvent(routeRetroPR, routeRetroCommand, routeRetroTriager, "issue_comment", "created"),
			wantDecision: routeSkip, wantReason: "actor not authorized",
		},
		{
			name:         "/fs-retro from a bot is skipped even when the trigger has no actor guard",
			agents:       []config.AgentEntry{{Name: "retro", Trigger: retroCommandOnlyTrigger}},
			event:        routeRetroEvent(routeRetroPR, routeRetroCommand, `{"id": "helper[bot]", "kind": "bot", "role": "write", "is_entity_author": false}`, "issue_comment", "created"),
			wantDecision: routeSkip, wantReason: "bot comments do not start retro",
		},
		{
			name:         "/fs-review is left to bash",
			agents:       []config.AgentEntry{{Name: "retro", Trigger: "true"}},
			event:        routeRetroEvent(routeRetroPR, routeRetroOtherCommand, routeRetroWriter, "issue_comment", "created"),
			wantDecision: routeBash, wantReason: "event is not routable for retro",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			neutralizeAgentsRepoFallback(t)
			dir := writeRouteFixture(t, sourcedRouteFixture(tc.agents, "retro", routeTestRetroHarness))
			got, err := routeBuiltinAgent(context.Background(), routeOpts{
				agent: "retro", configDir: dir, event: routeEvent(t, tc.event),
				forgeClient: forge.NewFakeClient(), printer: ui.New(io.Discard),
			})
			require.NoError(t, err)
			assert.Equal(t, "retro", got.Agent)
			assert.Equal(t, tc.wantDecision, got.Decision, "reason: %s", got.Reason)
			if tc.wantReason != "" {
				assert.Equal(t, tc.wantReason, got.Reason)
			}
		})
	}
}

// The lifecycle-close exception is scoped to the retro role: the review
// route still denies a closer without write, even with a trigger that
// matches the close.
func TestRouteBuiltinAgent_LifecycleCloseExceptionIsRetroOnly(t *testing.T) {
	neutralizeAgentsRepoFallback(t)
	dir := writeRouteFixture(t, sourcedRouteFixture([]config.AgentEntry{{Name: "review", Trigger: "true"}}, "review", routeTestHarnessBody))
	got, err := routeBuiltinAgent(context.Background(), routeOpts{
		agent: "review", configDir: dir,
		event:       routeEvent(t, routeRetroEvent(routeRetroPR, routeRetroClosed, routeRetroReadAuthor, "pull_request_target", "closed")),
		forgeClient: forge.NewFakeClient(), printer: ui.New(io.Discard),
	})
	require.NoError(t, err)
	assert.Equal(t, routeSkip, got.Decision)
	assert.Equal(t, "actor not authorized", got.Reason)

	// A review entry whose harness declares role: retro still goes through
	// the review route, which never grants the exception.
	dir = writeRouteFixture(t, sourcedRouteFixture([]config.AgentEntry{{Name: "review", Trigger: "true"}}, "review", routeTestRetroHarness))
	got, err = routeBuiltinAgent(context.Background(), routeOpts{
		agent: "review", configDir: dir,
		event:       routeEvent(t, routeRetroEvent(routeRetroPR, routeRetroClosed, routeRetroReadAuthor, "pull_request_target", "closed")),
		forgeClient: forge.NewFakeClient(), printer: ui.New(io.Discard),
	})
	require.NoError(t, err)
	assert.Equal(t, routeSkip, got.Decision)
	assert.Equal(t, "actor not authorized", got.Reason)
}

// When the retro harness cannot be resolved, the lifecycle-close exception
// falls back to the agent's own role (the Retro job mints the retro role),
// so a read-only closer still starts a retro, as in bash.
func TestRouteBuiltinAgent_LifecycleCloseHarnessUnavailable(t *testing.T) {
	neutralizeAgentsRepoFallback(t)
	dir := writeRouteFixture(t, routeFixture{agents: []config.AgentEntry{{Name: "retro", Trigger: routeTrigger(t, "retro")}}})
	got, err := routeBuiltinAgent(context.Background(), routeOpts{
		agent: "retro", configDir: dir,
		event:       routeEvent(t, routeRetroEvent(routeRetroPR, routeRetroClosed, routeRetroReadAuthor, "pull_request_target", "closed")),
		forgeClient: forge.NewFakeClient(), printer: ui.New(io.Discard),
	})
	require.NoError(t, err)
	assert.Empty(t, got.Harness)
	assert.Equal(t, routeRun, got.Decision, "reason: %s", got.Reason)
}

// TestRetroRouteParity runs raw GitHub webhook payloads through the gha-event
// input and routeBuiltinAgent (`fullsend dispatch --route retro`), with the
// default retro trigger set in config and a local harness with role: retro,
// and compares the decision with the one the bash router in
// reusable-dispatch.yml makes for the same event. A row whose CEL result
// differs from bash must name the known difference in diff.
func TestRetroRouteParity(t *testing.T) {
	neutralizeAgentsRepoFallback(t)
	dir := writeRouteFixture(t, sourcedRouteFixture([]config.AgentEntry{{Name: "retro", Trigger: routeTrigger(t, "retro")}}, "retro", routeTestRetroHarness))

	const repo = "acme/widgets"
	permissions := map[string]string{
		repo + "/writer":  "write",
		repo + "/triager": "triage",
		repo + "/reader":  "read",
	}
	merged := func(payload string) string {
		return strings.Replace(payload, `"number": 7,`, `"number": 7, "merged": true,`, 1)
	}

	cases := []struct {
		name      string
		eventName string
		action    string
		payload   string
		bash      bool   // reusable-dispatch.yml routes STAGE=retro
		cel       bool   // CEL route decision is run
		diff      string // known, accepted difference; empty when cel == bash
	}{
		{
			name:      "PR merged by a write user (merge queue: the user who enqueued it)",
			eventName: "pull_request_target", action: "closed",
			payload: merged(prPayload("closed", "dep-updater[bot]", "writer", "")),
			bash:    true, cel: true,
		},
		{
			name:      "PR closed by its read-only author",
			eventName: "pull_request_target", action: "closed",
			payload: prPayload("closed", "reader", "reader", ""),
			bash:    true, cel: true,
		},
		{
			name:      "PR closed by a triage user",
			eventName: "pull_request_target", action: "closed",
			payload: prPayload("closed", "writer", "triager", ""),
			bash:    true, cel: true,
		},
		{
			name:      "PR closed by an app bot without a collaborator role",
			eventName: "pull_request_target", action: "closed",
			payload: prPayload("closed", "dep-updater[bot]", "dep-updater[bot]", ""),
			bash:    true, cel: true,
		},
		{
			name:      "PR opened",
			eventName: "pull_request_target", action: "opened",
			payload: prPayload("opened", "writer", "writer", ""),
			bash:    false, cel: false,
		},
		{
			name:      "/fs-retro on a PR by a write user",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "writer", "User", "/fs-retro"),
			bash:    true, cel: true,
		},
		{
			name:      "/fs-retro on an issue by a write user",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "writer", "User", "/fs-retro"),
			bash:    true, cel: true,
		},
		{
			name:      "/fs-retro with trailing punctuation",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "writer", "User", "/fs-retro."),
			bash:    true, cel: true,
		},
		{
			name:      "/fs-retro by a triage user",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "triager", "User", "/fs-retro"),
			bash:    false, cel: false,
		},
		{
			name:      "/fs-retro by a read-only user",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "reader", "User", "/fs-retro"),
			bash:    false, cel: false,
		},
		{
			name:      "/fs-retro by a bot",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "helper[bot]", "Bot", "/fs-retro"),
			bash:    false, cel: false,
		},
		{
			name:      "/fs-review on a PR",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "writer", "User", "/fs-review"),
			bash:    false, cel: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eventPath := filepath.Join(t.TempDir(), "event.json")
			require.NoError(t, os.WriteFile(eventPath, []byte(tc.payload), 0o644))

			client := forge.NewFakeClient()
			client.CollaboratorPermissions = permissions
			ev, err := input.LoadGHAEvent(context.Background(), input.GHAEventOptions{
				EventPath:   eventPath,
				EventName:   tc.eventName,
				EventAction: tc.action,
				Repository:  repo,
				Forge:       client,
			})
			require.NoError(t, err)

			d, err := routeBuiltinAgent(context.Background(), routeOpts{
				agent:       "retro",
				configDir:   dir,
				event:       ev,
				forgeClient: client,
				printer:     ui.New(io.Discard),
			})
			require.NoError(t, err)
			got := d.Decision == routeRun
			if d.Decision == routeBash {
				got = tc.bash
			}

			assert.Equal(t, tc.cel, got, "CEL route decision (%s: %s)", d.Decision, d.Reason)
			if tc.diff == "" {
				assert.Equal(t, tc.bash, got, "CEL differs from bash without a documented difference")
			} else {
				assert.NotEqual(t, tc.bash, tc.cel, "row documents a difference but bash and CEL agree")
			}
		})
	}
}

// The lifecycle-close exception follows the resolved harness role: a retro
// entry whose harness declares another role keeps that role's threshold.
func TestRouteBuiltinAgent_LifecycleCloseFollowsHarnessRole(t *testing.T) {
	neutralizeAgentsRepoFallback(t)
	coderHarness := strings.Replace(routeTestRetroHarness, "role: retro", "role: coder", 1)
	dir := writeRouteFixture(t, sourcedRouteFixture([]config.AgentEntry{{Name: "retro", Trigger: "true"}}, "retro", coderHarness))
	got, err := routeBuiltinAgent(context.Background(), routeOpts{
		agent: "retro", configDir: dir,
		event:       routeEvent(t, routeRetroEvent(routeRetroPR, routeRetroClosed, routeRetroReadAuthor, "pull_request_target", "closed")),
		forgeClient: forge.NewFakeClient(), printer: ui.New(io.Discard),
	})
	require.NoError(t, err)
	assert.Equal(t, routeSkip, got.Decision)
	assert.Equal(t, "actor not authorized", got.Reason)
}
