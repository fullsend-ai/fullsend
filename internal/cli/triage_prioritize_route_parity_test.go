package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch/input"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// routeTrigger reads a default trigger fixture from testdata/triage-route.
func routeTrigger(t *testing.T, agent string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "triage-route", agent+"-trigger.cel"))
	require.NoError(t, err)
	return string(data)
}

// TestTriagePrioritizeRouteParity runs raw GitHub webhook payloads through
// the gha-event input and routeBuiltinAgent, with the default triage and
// prioritize triggers set in config, and compares each decision with the one
// the bash router in reusable-dispatch.yml makes for the same event.
//
// A row whose CEL result differs from bash must name the known difference in
// diff; any other mismatch is a parity regression.
func TestTriagePrioritizeRouteParity(t *testing.T) {
	// Local harnesses carry the default roles, so the route applies the
	// same per-harness thresholds as a real run.
	dir := writeRouteFixture(t, routeFixture{
		agents: []config.AgentEntry{
			{Name: "triage", Source: "harness/triage.yaml", Trigger: routeTrigger(t, "triage")},
			{Name: "prioritize", Source: "harness/prioritize.yaml", Trigger: routeTrigger(t, "prioritize")},
		},
		files: map[string]string{
			"harness/triage.yaml":     routeTestTriageHarness,
			"harness/prioritize.yaml": routeTestPrioritizeHarness,
		},
	})

	const repo = "acme/widgets"
	permissions := map[string]string{
		repo + "/writer":          "write",
		repo + "/triager":         "triage",
		repo + "/reader":          "read",
		repo + "/writer-bot[bot]": "write",
	}

	cases := []struct {
		name      string
		agent     string
		eventName string
		action    string
		payload   string
		bash      bool   // reusable-dispatch.yml routes STAGE=<agent>
		cel       bool   // CEL route decision is run
		diff      string // known, accepted difference; empty when cel == bash
		notRouted bool   // --route leaves the event to bash (not this agent's)
	}{
		// --- triage: issues events ---
		{
			name: "issue opened by a write author", agent: "triage",
			eventName: "issues", action: "opened",
			payload: issuePayload("opened", "writer", "writer", ""),
			bash:    true, cel: true,
		},
		{
			name: "issue opened by a triage author", agent: "triage",
			eventName: "issues", action: "opened",
			payload: issuePayload("opened", "triager", "triager", ""),
			bash:    true, cel: true,
		},
		{
			name: "issue opened by a read-only author", agent: "triage",
			eventName: "issues", action: "opened",
			payload: issuePayload("opened", "reader", "reader", ""),
			bash:    false, cel: false,
		},
		{
			name: "issue opened by a bot without a collaborator role", agent: "triage",
			eventName: "issues", action: "opened",
			payload: issuePayload("opened", "fullsend-ai-triage[bot]", "fullsend-ai-triage[bot]", ""),
			bash:    false, cel: false,
		},
		{
			name: "issue opened by a bot with a write role", agent: "triage",
			eventName: "issues", action: "opened",
			payload: issuePayload("opened", "writer-bot[bot]", "writer-bot[bot]", ""),
			bash:    true, cel: false,
			diff: "the default trigger triages only issues a human opens or edits; bot-created issues are triaged through the ready-for-triage label",
		},
		{
			name: "issue by a read-only author edited by a write user", agent: "triage",
			eventName: "issues", action: "edited",
			payload: issuePayload("edited", "reader", "writer", ""),
			bash:    true, cel: true,
		},
		{
			name: "issue edited by a read-only user", agent: "triage",
			eventName: "issues", action: "edited",
			payload: issuePayload("edited", "writer", "reader", ""),
			bash:    false, cel: false,
		},
		{
			name: "issue reopened", agent: "triage",
			eventName: "issues", action: "reopened",
			payload: issuePayload("reopened", "writer", "writer", ""),
			bash:    false, cel: false,
			notRouted: true,
		},
		{
			name: "ready-for-triage added by a read-only user", agent: "triage",
			eventName: "issues", action: "labeled",
			payload: issuePayload("labeled", "writer", "reader", "ready-for-triage"),
			bash:    true, cel: true,
		},
		{
			name: "ready-for-triage added by a bot", agent: "triage",
			eventName: "issues", action: "labeled",
			payload: issuePayload("labeled", "writer", "fullsend-ai-triage[bot]", "ready-for-triage"),
			bash:    true, cel: true,
		},
		{
			name: "another label added to an issue", agent: "triage",
			eventName: "issues", action: "labeled",
			payload: issuePayload("labeled", "writer", "writer", "documentation"),
			bash:    false, cel: false,
		},
		{
			name: "ready-for-triage removed", agent: "triage",
			eventName: "issues", action: "unlabeled",
			payload: issuePayload("unlabeled", "writer", "writer", "ready-for-triage"),
			bash:    false, cel: false,
		},
		// --- triage: /fs-triage ---
		{
			name: "/fs-triage on an issue by a write user", agent: "triage",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "writer", "User", "/fs-triage"),
			bash:    true, cel: true,
		},
		{
			name: "/fs-triage on a PR by a write user", agent: "triage",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "writer", "User", "/fs-triage"),
			bash:    true, cel: true,
		},
		{
			name: "/fs-triage on an issue by a triage user", agent: "triage",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "triager", "User", "/fs-triage"),
			bash:    true, cel: true,
		},
		{
			name: "/fs-triage on an issue by a read-only user", agent: "triage",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "reader", "User", "/fs-triage"),
			bash:    false, cel: false,
		},
		{
			name: "/fs-triage on an issue by a bot with a write role", agent: "triage",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "writer-bot[bot]", "Bot", "/fs-triage"),
			bash:    false, cel: false,
		},
		{
			name: "another comment on an issue", agent: "triage",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "writer", "User", "thanks"),
			bash:    false, cel: false,
		},
		// --- prioritize: /fs-prioritize ---
		{
			name: "/fs-prioritize on an issue by a write user", agent: "prioritize",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "writer", "User", "/fs-prioritize"),
			bash:    true, cel: true,
		},
		{
			name: "/fs-prioritize on a PR by a write user", agent: "prioritize",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "writer", "User", "/fs-prioritize"),
			bash:    true, cel: true,
		},
		{
			name: "/fs-prioritize with trailing punctuation", agent: "prioritize",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "writer", "User", "/fs-prioritize!"),
			bash:    true, cel: true,
		},
		{
			name: "/fs-prioritize by a triage user", agent: "prioritize",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "triager", "User", "/fs-prioritize"),
			bash:    false, cel: false,
		},
		{
			name: "/fs-prioritize by a bot with a write role", agent: "prioritize",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "writer-bot[bot]", "Bot", "/fs-prioritize"),
			bash:    false, cel: false,
		},
		{
			name: "/fs-triage is not prioritize's", agent: "prioritize",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "writer", "User", "/fs-triage"),
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

			neutralizeAgentsRepoFallback(t)
			d, err := routeBuiltinAgent(context.Background(), routeOpts{
				agent:       tc.agent,
				configDir:   dir,
				event:       ev,
				forgeClient: client,
				printer:     ui.New(io.Discard),
			})
			require.NoError(t, err)
			// With a trigger set, --route must decide every event it covers
			// rather than hand it back to bash.
			if tc.notRouted {
				require.Equal(t, routeBash, d.Decision, "reason: %s", d.Reason)
			} else {
				require.Contains(t, []string{routeRun, routeSkip}, d.Decision, "reason: %s", d.Reason)
			}
			// bash hands the event back to the bash router, so the
			// effective decision is bash's.
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

func issuePayload(action, author, sender, label string) string {
	labelField := ""
	if label != "" {
		labelField = `"label": {"name": "` + label + `"},`
	}
	senderType := "User"
	if len(sender) > 5 && sender[len(sender)-5:] == "[bot]" {
		senderType = "Bot"
	}
	return `{
  "action": "` + action + `",
  ` + labelField + `
  "issue": {
    "number": 8,
    "html_url": "https://github.com/acme/widgets/issues/8",
    "user": {"login": "` + author + `"},
    "labels": []
  },
  "sender": {"login": "` + sender + `", "type": "` + senderType + `"}
}`
}
