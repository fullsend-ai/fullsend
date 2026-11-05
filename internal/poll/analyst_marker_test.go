package poll

import (
	"context"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Distinct-role identities (#8807): the Poller credential that runs the
// cron poll and the webhook dispatcher, the Analyst that the review stage
// posts its changes-requested marker as, and an unrelated bot.
var (
	distinctPoller  = UserRef{ID: 1, Username: "project_7_bot_poller", Bot: true}
	distinctAnalyst = UserRef{ID: 2, Username: "project_7_bot_analyst", Bot: true}
	unrelatedBot    = UserRef{ID: 3, Username: "project_7_bot_other", Bot: true}
	// analystServiceAccount is an Analyst provisioned as a service account:
	// GitLab may omit its bot flag and its username defeats the project
	// access token heuristic, so only the registered ID identifies it.
	analystServiceAccount = UserRef{ID: 2, Username: "fullsend-analyst"}
)

const markerBody = "Changes needed " + forge.ChangesRequestedMarker

func TestFilterBotEvents_AnalystChangesRequestedMarker(t *testing.T) {
	tests := []struct {
		name      string
		pollerID  int
		analystID int
		author    UserRef
		body      string
		want      bool
	}{
		{name: "marker from Analyst", pollerID: 1, analystID: 2, author: distinctAnalyst, body: markerBody, want: true},
		{name: "marker from service-account Analyst", pollerID: 1, analystID: 2, author: analystServiceAccount, body: markerBody, want: true},
		{name: "marker from unrelated bot", pollerID: 1, analystID: 2, author: unrelatedBot, body: markerBody, want: false},
		{name: "marker from Poller when roles differ", pollerID: 1, analystID: 2, author: distinctPoller, body: markerBody, want: false},
		{name: "Analyst note without marker", pollerID: 1, analystID: 2, author: distinctAnalyst, body: "review comment <!-- fullsend:review-agent -->", want: false},
		{name: "service-account Analyst note without marker", pollerID: 1, analystID: 2, author: analystServiceAccount, body: "review comment", want: false},
		{name: "Analyst is Poller, marker", pollerID: 1, analystID: 1, author: distinctPoller, body: markerBody, want: true},
		{name: "Analyst unknown, marker from Poller", pollerID: 1, analystID: 0, author: distinctPoller, body: markerBody, want: false},
		{name: "Analyst unknown, marker from bot with no ID", pollerID: 1, analystID: 0, author: UserRef{Username: "project_7_bot_x", Bot: true}, body: markerBody, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(newMockClient(), nil, "group/project", Options{BotUserID: tt.pollerID, AnalystUserID: tt.analystID})
			filtered := p.filterBotEvents([]RoutableEvent{{
				Type:            "mr_note",
				IID:             5,
				NoteBody:        tt.body,
				NoteID:          30,
				NoteAuthorID:    tt.author.ID,
				NoteAuthorLogin: tt.author.Username,
				IsBot:           tt.author.Bot,
			}})
			if got := len(filtered) == 1; got != tt.want {
				t.Errorf("retained = %v, want %v", got, tt.want)
			}
		})
	}
}

// fixRouter routes like production: a bot-authored changes-requested
// marker on a same-project MR selects the fix stage.
func fixRouter() dispatch.EventRouter {
	return dispatch.NewHarnessRouter([]string{"triage", "code", "review", "fix", "retro"})
}

func fixStageCalls(mc *mockClient) []pipelineCall {
	var calls []pipelineCall
	for _, c := range mc.pipelineCalls {
		if c.Variables["STAGE"] == "fix" {
			calls = append(calls, c)
		}
	}
	return calls
}

// TestRun_DistinctAnalystMarkerReachesFixStage is the cron-poll regression
// for #8807: with distinct Poller and Analyst identities, only the
// Analyst's marker dispatches the fix stage.
func TestRun_DistinctAnalystMarkerReachesFixStage(t *testing.T) {
	tests := []struct {
		name      string
		analystID int
		author    UserRef
		wantFix   bool
	}{
		{name: "Analyst marker", analystID: 2, author: distinctAnalyst, wantFix: true},
		{name: "service-account Analyst marker", analystID: 2, author: analystServiceAccount, wantFix: true},
		{name: "unrelated bot marker", analystID: 2, author: unrelatedBot},
		{name: "Poller marker", analystID: 2, author: distinctPoller},
		{name: "Analyst unknown", analystID: 0, author: distinctAnalyst},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now().Truncate(time.Second)
			mc := newMockClient()
			mc.setPollState(persistedPollState{LastPollAtFull: now.Add(-20 * time.Minute).Format(time.RFC3339)})
			mc.projectPaths[7] = "group/project"
			mc.mrs = []MergeRequest{{
				IID: 3, State: "opened", Labels: []string{"bug"},
				SourceProjectID: 7, TargetProjectID: 7,
				SourceBranch: "feat", TargetBranch: "main",
				Author:    alice,
				CreatedAt: now.Add(-time.Hour),
				UpdatedAt: now,
			}}
			mc.mrNotes[3] = []Note{{ID: 31, Body: markerBody, Author: tt.author, CreatedAt: now}}

			p := New(mc, fixRouter(), "group/project", withTestSecret(Options{
				Mode:          "events",
				BotUserID:     distinctPoller.ID,
				AnalystUserID: tt.analystID,
			}))
			if err := p.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			calls := fixStageCalls(mc)
			if tt.wantFix {
				if len(calls) != 1 {
					t.Fatalf("fix dispatches = %d (pipeline calls %+v), want 1", len(calls), mc.pipelineCalls)
				}
				return
			}
			if len(mc.pipelineCalls) != 0 {
				t.Errorf("pipeline calls = %+v, want none", mc.pipelineCalls)
			}
		})
	}
}

// TestRunWebhook_DistinctAnalystMarkerReachesFixStage is the webhook
// dispatcher regression for #8807.
func TestRunWebhook_DistinctAnalystMarkerReachesFixStage(t *testing.T) {
	tests := []struct {
		name      string
		analystID int
		author    UserRef
		wantFix   bool
	}{
		{name: "Analyst marker", analystID: 2, author: distinctAnalyst, wantFix: true},
		{name: "service-account Analyst marker", analystID: 2, author: analystServiceAccount, wantFix: true},
		{name: "Analyst is Poller", analystID: 1, author: distinctPoller, wantFix: true},
		{name: "unrelated bot marker", analystID: 2, author: unrelatedBot},
		{name: "Poller marker", analystID: 2, author: distinctPoller},
		{name: "Analyst unknown", analystID: 0, author: distinctAnalyst},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMockClient()
			mc.mr[3] = sameProjectMR(3, "opened")
			mc.projectPaths[7] = "group/project"
			mc.mrNotes[3] = []Note{{ID: 21, Body: markerBody, Author: tt.author, CreatedAt: recent}}

			p := New(mc, fixRouter(), "group/project", Options{
				BotUserID:      distinctPoller.ID,
				AnalystUserID:  tt.analystID,
				GitLabURL:      "https://gitlab.example.com",
				PipelineRef:    "main",
				DispatchSecret: testDispatchSecret,
			})
			p.now = func() time.Time { return webhookTestNow }
			if err := p.RunWebhook(context.Background(), notePayload(t, "MergeRequest", tt.author.ID, 21, 3)); err != nil {
				t.Fatalf("RunWebhook: %v", err)
			}
			calls := fixStageCalls(mc)
			if tt.wantFix {
				if len(calls) != 1 {
					t.Fatalf("fix dispatches = %d (pipeline calls %+v), want 1", len(calls), mc.pipelineCalls)
				}
				return
			}
			if len(mc.pipelineCalls) != 0 {
				t.Errorf("pipeline calls = %+v, want none", mc.pipelineCalls)
			}
		})
	}
}
