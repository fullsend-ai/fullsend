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

// TestReviewRouteParity runs raw GitHub webhook payloads through the gha-event
// input and routeBuiltinAgent (the code behind `fullsend dispatch --route
// review`), with the default review trigger set in config, and compares
// the decision with the one the bash router in reusable-dispatch.yml makes for
// the same event.
//
// A row whose CEL result differs from bash must name the known difference in
// diff; any other mismatch is a parity regression.
func TestReviewRouteParity(t *testing.T) {
	// The default trigger is set as agents[].trigger, the documented opt-in.
	// The review harness is local so the test needs no agents-repo fetch.
	dir := writeRouteFixture(t, routeFixture{
		agents: []config.AgentEntry{{Name: "review", Source: "harness/review.yaml", Trigger: reviewTrigger(t)}},
		files: map[string]string{
			"harness/review.yaml": routeTestHarnessBody,
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
		eventName string
		action    string
		payload   string
		bash      bool   // reusable-dispatch.yml routes STAGE=review
		cel       bool   // CEL route decision is run
		diff      string // known, accepted difference; empty when cel == bash
		// notRouted marks an event the review CEL route does not cover:
		// --route answers bash and the bash router decides.
		notRouted bool
	}{
		{
			name:      "PR opened by a write author",
			eventName: "pull_request_target", action: "opened",
			payload: prPayload("opened", "writer", "writer", ""),
			bash:    true, cel: true,
		},
		{
			name:      "PR synchronized by its write author",
			eventName: "pull_request_target", action: "synchronize",
			payload: prPayload("synchronize", "writer", "writer", ""),
			bash:    true, cel: true,
		},
		{
			name:      "PR marked ready by its write author",
			eventName: "pull_request_target", action: "ready_for_review",
			payload: prPayload("ready_for_review", "writer", "writer", ""),
			bash:    true, cel: true,
		},
		{
			name:      "PR opened by a read-only author",
			eventName: "pull_request_target", action: "opened",
			payload: prPayload("opened", "reader", "reader", ""),
			bash:    false, cel: false,
		},
		{
			name:      "PR opened by a triage author",
			eventName: "pull_request_target", action: "opened",
			payload: prPayload("opened", "triager", "triager", ""),
			bash:    true, cel: true,
		},
		{
			name:      "PR opened by a bot without a collaborator role",
			eventName: "pull_request_target", action: "opened",
			payload: prPayload("opened", "dep-updater[bot]", "dep-updater[bot]", ""),
			bash:    true, cel: false,
			diff: "bash reviews every [bot] author; the gha-event input does not run the ADR 0107 bot-role lookup yet, so a bot needs a write collaborator role",
		},
		{
			name:      "PR from a read-only author synchronized by a write maintainer",
			eventName: "pull_request_target", action: "synchronize",
			payload: prPayload("synchronize", "reader", "writer", ""),
			bash:    false, cel: true,
			diff: "bash checks the PR author; the dispatch gate checks the event sender",
		},
		{
			name:      "ready-for-review label added by a triage user",
			eventName: "pull_request_target", action: "labeled",
			payload: prPayload("labeled", "reader", "triager", "ready-for-review"),
			bash:    true, cel: true,
		},
		{
			name:      "another label added",
			eventName: "pull_request_target", action: "labeled",
			payload: prPayload("labeled", "writer", "writer", "documentation"),
			bash:    false, cel: false,
		},
		{
			name:      "PR closed",
			eventName: "pull_request_target", action: "closed",
			payload: prPayload("closed", "writer", "writer", ""),
			bash:    false, cel: false,
		},
		{
			name:      "/fs-review on a PR by a write user",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "writer", "User", "/fs-review"),
			bash:    true, cel: true,
		},
		{
			name:      "/fs-review on a PR with trailing punctuation",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "writer", "User", "/fs-review."),
			bash:    true, cel: true,
		},
		{
			name:      "/fs-review on a PR by a triage user",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "triager", "User", "/fs-review"),
			bash:    true, cel: true,
		},
		{
			name:      "/fs-review on a PR by a read-only user",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "reader", "User", "/fs-review"),
			bash:    false, cel: false,
		},
		{
			name:      "/fs-review on a PR by a bot",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "helper[bot]", "Bot", "/fs-review"),
			bash:    false, cel: false,
		},
		{
			name:      "/fs-review on a PR by a bot with a write role",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "writer-bot[bot]", "Bot", "/fs-review"),
			bash:    false, cel: false,
		},
		{
			name:      "/fs-review on an issue",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(false, "writer", "User", "/fs-review"),
			bash:    false, cel: false, notRouted: true,
		},
		{
			name:      "/fs-fix on a PR",
			eventName: "issue_comment", action: "created",
			payload: commentPayload(true, "writer", "User", "/fs-fix"),
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
				agent:       "review",
				configDir:   dir,
				event:       ev,
				forgeClient: client,
				printer:     ui.New(io.Discard),
			})
			require.NoError(t, err)
			// bash hands the event back to the bash router, so the
			// effective decision is bash's. Only rows the CEL route does not
			// cover may answer bash.
			if tc.notRouted {
				require.Equal(t, routeBash, d.Decision, "reason: %s", d.Reason)
			} else {
				require.Contains(t, []string{routeRun, routeSkip}, d.Decision, "reason: %s", d.Reason)
			}
			got := d.Decision == routeRun
			if d.Decision == routeBash {
				got = tc.bash
			}

			assert.Equal(t, tc.cel, got, "CEL route decision")
			if tc.diff == "" {
				assert.Equal(t, tc.bash, got, "CEL differs from bash without a documented difference")
			} else {
				assert.NotEqual(t, tc.bash, tc.cel, "row documents a difference but bash and CEL agree")
			}
		})
	}
}

func prPayload(action, author, sender, label string) string {
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
  "pull_request": {
    "number": 7,
    "html_url": "https://github.com/acme/widgets/pull/7",
    "user": {"login": "` + author + `"},
    "labels": [],
    "head": {"ref": "topic", "sha": "1111111111111111111111111111111111111111", "repo": {"full_name": "acme/widgets"}},
    "base": {"ref": "main", "repo": {"full_name": "acme/widgets"}}
  },
  "sender": {"login": "` + sender + `", "type": "` + senderType + `"}
}`
}

func commentPayload(onPR bool, commenter, userType, body string) string {
	prField := ""
	if onPR {
		prField = `"pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/7"},`
	}
	return `{
  "action": "created",
  "issue": {
    "number": 7,
    "html_url": "https://github.com/acme/widgets/issues/7",
    ` + prField + `
    "user": {"login": "someone"},
    "labels": []
  },
  "comment": {"id": 1, "body": "` + body + `", "user": {"login": "` + commenter + `", "type": "` + userType + `"}},
  "sender": {"login": "` + commenter + `", "type": "` + userType + `"}
}`
}
