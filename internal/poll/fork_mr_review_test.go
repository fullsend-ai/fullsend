package poll

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
)

// TestRun_SlashCommandOnForkMR: /fs-review on an existing fork MR
// dispatches review for an actor with at least Reporter (triage) access,
// while write-capable commands stay denied and every undispatched command
// logs why it was skipped (#9816).
func TestRun_SlashCommandOnForkMR(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		level      int
		wantStage  string
		wantLogged string
	}{
		{name: "review by reporter", body: "/fs-review", level: 20, wantStage: "review"},
		{name: "review by developer", body: "/fs-review", level: 30, wantStage: "review"},
		{name: "review by guest", body: "/fs-review", level: 10, wantLogged: `actor role "read" is below the required "triage" role`},
		{name: "fix by maintainer", body: "/fs-fix", level: 40, wantLogged: "only /fs-review is allowed on forks"},
		{name: "code by maintainer", body: "/fs-code", level: 40, wantLogged: "only /fs-review is allowed on forks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			now := time.Now().Truncate(time.Second)
			alice := UserRef{ID: 42, Username: "alice"}
			mc := newMockClient()
			mc.memberLevel[alice.ID] = tt.level
			mc.setSlashState(persistedPollState{LastPollAtFast: now.Add(-5 * time.Minute).Format(time.RFC3339)})
			mc.projectPaths[7] = "group/project"
			mc.projectPaths[8] = "contributor/project"
			mc.mr[3] = &MergeRequest{
				IID: 3, State: "opened",
				SourceProjectID: 8, TargetProjectID: 7,
				SourceBranch: "feat", TargetBranch: "main",
				Author:    UserRef{ID: 77, Username: "contributor"},
				SHA:       "cccccccccccccccccccccccccccccccccccccccc",
				CreatedAt: now.Add(-time.Hour),
				UpdatedAt: now,
			}
			mc.events = []ProjectEvent{{
				ID:        1,
				Author:    alice,
				Note:      EventNote{ID: 31, NoteableType: "MergeRequest", NoteableIID: 3, Body: tt.body},
				CreatedAt: now,
			}}

			router := dispatch.NewHarnessRouter([]string{"triage", "code", "review", "fix", "retro"})
			p := New(mc, router, "group/project", withTestSecret(Options{
				Mode:        "slash",
				BotUserID:   100,
				GitLabURL:   "https://gitlab.example.com",
				PipelineRef: "main",
			}))
			if err := p.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			if tt.wantStage != "" {
				if len(mc.pipelineCalls) != 1 {
					t.Fatalf("pipeline calls = %d, want 1: %+v", len(mc.pipelineCalls), mc.pipelineCalls)
				}
				vars := mc.pipelineCalls[0].Variables
				if vars["STAGE"] != tt.wantStage {
					t.Errorf("STAGE = %q, want %q", vars["STAGE"], tt.wantStage)
				}
				if vars["IS_FORK"] != "true" {
					t.Errorf("IS_FORK = %q, want %q", vars["IS_FORK"], "true")
				}
				return
			}
			if len(mc.pipelineCalls) != 0 {
				t.Fatalf("pipeline calls = %+v, want none", mc.pipelineCalls)
			}
			if !strings.Contains(logs.String(), "no stages routed for") || !strings.Contains(logs.String(), tt.wantLogged) {
				t.Errorf("logs missing skip reason %q:\n%s", tt.wantLogged, logs.String())
			}
		})
	}
}

func TestRouteWithReason_PlainRouterFallback(t *testing.T) {
	stages, reason, err := routeWithReason(&stubRouter{}, &dispatch.NormalizedEvent{})
	if err != nil || len(stages) != 0 {
		t.Fatalf("routeWithReason = (%v, %v), want no stages and no error", stages, err)
	}
	if reason == "" {
		t.Error("reason is empty for a router without RouteWithReason")
	}

	wantErr := errors.New("boom")
	stages, _, err = routeWithReason(&stubRouter{stages: []string{"review"}, err: wantErr}, &dispatch.NormalizedEvent{})
	if !errors.Is(err, wantErr) || len(stages) != 1 || stages[0] != "review" {
		t.Fatalf("routeWithReason = (%v, %v), want ([review], %v)", stages, err, wantErr)
	}
}
