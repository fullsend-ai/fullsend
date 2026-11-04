package poll

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
)

// Push-to-open-MR re-review (#7681): a new head on an open MR emits one
// mr_event "synchronized" keyed on the head SHA, routed to review.

func pushedMR(iid int, author UserRef) MergeRequest {
	return MergeRequest{
		IID:             iid,
		State:           "opened",
		Author:          author,
		SourceProjectID: 1,
		TargetProjectID: 1,
		SourceBranch:    "agent/1-fix",
		TargetBranch:    "main",
		Labels:          []string{"ready-for-review"},
	}
}

func synchronizedEvents(events []RoutableEvent) []RoutableEvent {
	var out []RoutableEvent
	for _, e := range events {
		if e.Type == "mr_event" && e.Action == "synchronized" {
			out = append(out, e)
		}
	}
	return out
}

func TestLatestHeadPush(t *testing.T) {
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		versions []MergeRequestDiffVersion
		wantSHA  string
		wantAt   time.Time
	}{
		{name: "no versions"},
		{name: "only the open", versions: []MergeRequestDiffVersion{{ID: 1, HeadCommitSHA: "aaa", CreatedAt: t0}}},
		{
			name: "head moved (newest first)",
			versions: []MergeRequestDiffVersion{
				{ID: 2, HeadCommitSHA: "bbb", CreatedAt: t0.Add(time.Minute)},
				{ID: 1, HeadCommitSHA: "aaa", CreatedAt: t0},
			},
			wantSHA: "bbb",
		},
		{
			name: "head moved (any order)",
			versions: []MergeRequestDiffVersion{
				{ID: 1, HeadCommitSHA: "aaa", CreatedAt: t0},
				{ID: 3, HeadCommitSHA: "ccc", CreatedAt: t0.Add(2 * time.Minute)},
				{ID: 2, HeadCommitSHA: "bbb", CreatedAt: t0.Add(time.Minute)},
			},
			wantSHA: "ccc",
		},
		{
			name: "same head (target branch change)",
			versions: []MergeRequestDiffVersion{
				{ID: 2, HeadCommitSHA: "aaa", CreatedAt: t0.Add(time.Minute)},
				{ID: 1, HeadCommitSHA: "aaa", CreatedAt: t0},
			},
		},
		{
			name: "push followed by same-head versions reports the push",
			versions: []MergeRequestDiffVersion{
				{ID: 3, HeadCommitSHA: "bbb", CreatedAt: t0.Add(2 * time.Minute)},
				{ID: 2, HeadCommitSHA: "bbb", CreatedAt: t0.Add(time.Minute)},
				{ID: 1, HeadCommitSHA: "aaa", CreatedAt: t0},
			},
			wantSHA: "bbb",
			wantAt:  t0.Add(time.Minute),
		},
		{
			name: "head unchanged since the open",
			versions: []MergeRequestDiffVersion{
				{ID: 3, HeadCommitSHA: "aaa", CreatedAt: t0.Add(2 * time.Minute)},
				{ID: 2, HeadCommitSHA: "aaa", CreatedAt: t0.Add(time.Minute)},
				{ID: 1, HeadCommitSHA: "aaa", CreatedAt: t0},
			},
		},
		{
			name: "missing head SHA",
			versions: []MergeRequestDiffVersion{
				{ID: 2, CreatedAt: t0.Add(time.Minute)},
				{ID: 1, HeadCommitSHA: "aaa", CreatedAt: t0},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := latestHeadPush(tt.versions)
			if ok != (tt.wantSHA != "") {
				t.Fatalf("ok = %v, want %v", ok, tt.wantSHA != "")
			}
			if got.HeadCommitSHA != tt.wantSHA {
				t.Errorf("head = %q, want %q", got.HeadCommitSHA, tt.wantSHA)
			}
			if !tt.wantAt.IsZero() && !got.CreatedAt.Equal(tt.wantAt) {
				t.Errorf("introduced at %v, want %v", got.CreatedAt, tt.wantAt)
			}
		})
	}
}

func TestDiscoverAllEvents_MRPushEmitsSynchronized(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	bot := UserRef{ID: 100, Username: "project_1_bot_abc", Bot: true}
	mc := newMockClient()
	mr := pushedMR(12, bot)
	mr.CreatedAt = since.Add(-time.Hour)
	mr.UpdatedAt = now
	mc.mrs = []MergeRequest{mr}
	mc.mrVersions = map[int][]MergeRequestDiffVersion{12: {
		{ID: 31, HeadCommitSHA: "bbb", CreatedAt: now},
		{ID: 30, HeadCommitSHA: "aaa", CreatedAt: mr.CreatedAt},
	}}

	p := newEventsPoller(mc)
	events, _, minSkipped, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !minSkipped.IsZero() {
		t.Errorf("minSkippedAt = %v, want zero", minSkipped)
	}
	got := synchronizedEvents(events)
	if len(got) != 1 {
		t.Fatalf("expected 1 synchronized mr_event, got %+v", events)
	}
	ev := got[0]
	if ev.IID != 12 || ev.HeadSHA != "bbb" || !ev.UpdatedAt.Equal(now) {
		t.Errorf("event = %+v, want IID 12, head bbb, UpdatedAt %v", ev, now)
	}
	if ev.NoteAuthorID != bot.ID || ev.MRAuthorID != bot.ID || !ev.IsBot {
		t.Errorf("actor = %d/%d bot=%v, want MR author %d as bot", ev.NoteAuthorID, ev.MRAuthorID, ev.IsBot, bot.ID)
	}
	if ev.SourceBranch != "agent/1-fix" || ev.TargetBranch != "main" || ev.MRSource != 1 || ev.MRTarget != 1 {
		t.Errorf("MR fields not populated: %+v", ev)
	}
	if want := "mr_event-12-synchronized-bbb"; ev.Key() != want {
		t.Errorf("Key() = %q, want %q", ev.Key(), want)
	}
	if filtered := p.filterBotEvents(got); len(filtered) != 1 {
		t.Errorf("bot-authored synchronized event filtered out: %+v", filtered)
	}
}

func TestDiscoverAllEvents_MRPushNoEvent(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	alice := UserRef{ID: 42, Username: "alice"}
	tests := []struct {
		name     string
		state    string
		versions []MergeRequestDiffVersion
	}{
		{
			name:     "only the open version",
			state:    "opened",
			versions: []MergeRequestDiffVersion{{ID: 1, HeadCommitSHA: "aaa", CreatedAt: now}},
		},
		{
			name:  "head unchanged (comment or label update)",
			state: "opened",
			versions: []MergeRequestDiffVersion{
				{ID: 2, HeadCommitSHA: "aaa", CreatedAt: now},
				{ID: 1, HeadCommitSHA: "aaa", CreatedAt: since.Add(-time.Hour)},
			},
		},
		{
			name:  "push before the watermark",
			state: "opened",
			versions: []MergeRequestDiffVersion{
				{ID: 2, HeadCommitSHA: "bbb", CreatedAt: since.Add(-time.Second)},
				{ID: 1, HeadCommitSHA: "aaa", CreatedAt: since.Add(-time.Hour)},
			},
		},
		{
			name:  "closed MR",
			state: "closed",
			versions: []MergeRequestDiffVersion{
				{ID: 2, HeadCommitSHA: "bbb", CreatedAt: now},
				{ID: 1, HeadCommitSHA: "aaa", CreatedAt: since.Add(-time.Hour)},
			},
		},
		{
			name:  "merged MR",
			state: "merged",
			versions: []MergeRequestDiffVersion{
				{ID: 2, HeadCommitSHA: "bbb", CreatedAt: now},
				{ID: 1, HeadCommitSHA: "aaa", CreatedAt: since.Add(-time.Hour)},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMockClient()
			mr := pushedMR(13, alice)
			mr.State = tt.state
			mr.CreatedAt = since.Add(-time.Hour)
			mr.UpdatedAt = now
			mc.mrs = []MergeRequest{mr}
			mc.mrVersions = map[int][]MergeRequestDiffVersion{13: tt.versions}

			p := newEventsPoller(mc)
			events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := synchronizedEvents(events); len(got) != 0 {
				t.Fatalf("expected no synchronized event, got %+v", got)
			}
		})
	}
}

// TestDiscoverAllEvents_MRVersionsFailureHoldsWatermark: a failed versions
// lookup holds the watermark at the window start so the push is found by a
// later cycle, and the MR's notes are still discovered.
func TestDiscoverAllEvents_MRVersionsFailureHoldsWatermark(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	alice := UserRef{ID: 42, Username: "alice"}
	mc := newMockClient()
	mr := pushedMR(14, alice)
	mr.CreatedAt = since.Add(-time.Hour)
	mr.UpdatedAt = now
	mc.mrs = []MergeRequest{mr}
	mc.mrVersionsErr = map[int]error{14: errors.New("boom")}
	mc.mrNotes[14] = []Note{{ID: 77, Body: "looks good", Author: alice, CreatedAt: now}}

	p := newEventsPoller(mc)
	events, _, minSkipped, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !minSkipped.Equal(since) {
		t.Errorf("minSkippedAt = %v, want %v", minSkipped, since)
	}
	if got := synchronizedEvents(events); len(got) != 0 {
		t.Fatalf("expected no synchronized event, got %+v", got)
	}
	var notes int
	for _, e := range events {
		if e.Type == "mr_note" {
			notes++
		}
	}
	if notes != 1 {
		t.Errorf("mr_note events = %d, want 1", notes)
	}
}

func TestToNormalizedEvent_MRSynchronized(t *testing.T) {
	mc := newMockClient()
	mc.memberLevel[42] = 30
	mc.projectPaths[1] = "group/project"
	p := newEventsPoller(mc)

	ne, actorID, err := p.toNormalizedEvent(context.Background(), RoutableEvent{
		Type:          "mr_event",
		Action:        "synchronized",
		IID:           12,
		HeadSHA:       "bbb",
		UpdatedAt:     time.Now(),
		MRSource:      1,
		MRTarget:      1,
		SourceBranch:  "feature",
		TargetBranch:  "main",
		MRAuthorID:    42,
		MRAuthorLogin: "alice",
	})
	if err != nil {
		t.Fatalf("toNormalizedEvent: %v", err)
	}
	if ne.Transition.Kind != "synchronized" || ne.Source.RawAction != "synchronized" || ne.Source.RawType != "merge_request" {
		t.Errorf("transition/source = %q/%q/%q, want synchronized/synchronized/merge_request", ne.Transition.Kind, ne.Source.RawAction, ne.Source.RawType)
	}
	if ne.Entity.Kind != "change_proposal" {
		t.Errorf("entity kind = %q, want change_proposal", ne.Entity.Kind)
	}
	// Without push-actor fields the MR author is the actor, as for opens.
	if actorID != 42 || ne.Actor.ID != "alice" || ne.Actor.Role != "write" {
		t.Errorf("actor = %d %+v, want alice (42) with write", actorID, ne.Actor)
	}
	if ne.State.ChangeProposal == nil || ne.State.ChangeProposal.IsFork {
		t.Errorf("change proposal state = %+v, want same-project MR", ne.State.ChangeProposal)
	}
}

// TestRun_MRPushDispatchesReviewOncePerHead covers the end-to-end loop: a
// fix-agent push to the code agent's MR dispatches review once, polling the
// same head again dispatches nothing, and the next head dispatches again.
func TestRun_MRPushDispatchesReviewOncePerHead(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-5 * time.Minute)
	bot := UserRef{ID: 100, Username: "project_1_bot_abc", Bot: true}
	mc := newMockClient()
	mc.memberLevel[bot.ID] = 30
	mc.projectPaths[1] = "group/project"
	mc.setPollState(persistedPollState{LastPollAtFull: since.Format(time.RFC3339), IssueOpenCutoff: since})
	mr := pushedMR(12, bot)
	mr.CreatedAt = since.Add(-time.Hour)
	mr.UpdatedAt = now
	mc.mrs = []MergeRequest{mr}
	mc.mrVersions = map[int][]MergeRequestDiffVersion{12: {
		{ID: 31, HeadCommitSHA: "bbb", CreatedAt: now},
		{ID: 30, HeadCommitSHA: "aaa", CreatedAt: mr.CreatedAt},
	}}

	router := dispatch.NewHarnessRouter([]string{"triage", "code", "review", "fix", "retro"})
	run := func(n int) {
		t.Helper()
		p := New(mc, router, "group/project", withTestSecret(Options{
			Mode:        "events",
			BotUserID:   bot.ID,
			GitLabURL:   "https://gitlab.example.com",
			PipelineRef: "main",
		}))
		if err := p.Run(context.Background()); err != nil {
			t.Fatalf("Run #%d: %v", n, err)
		}
	}

	run(1)
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls after first push = %d, want 1", len(mc.pipelineCalls))
	}
	if stage := mc.pipelineCalls[0].Variables["STAGE"]; stage != "review" {
		t.Errorf("STAGE = %q, want review", stage)
	}
	state, _ := mc.getPollState()
	if _, ok := state.DispatchedKeysFull["review:mr_event-12-synchronized-bbb"]; !ok {
		t.Errorf("DispatchedKeysFull = %v, want review:mr_event-12-synchronized-bbb", state.DispatchedKeysFull)
	}

	run(2)
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls after re-poll of the same head = %d, want 1", len(mc.pipelineCalls))
	}

	later := now.Add(time.Minute)
	mc.mrs[0].UpdatedAt = later
	mc.mrVersions[12] = append([]MergeRequestDiffVersion{{ID: 32, HeadCommitSHA: "ccc", CreatedAt: later}}, mc.mrVersions[12]...)
	run(3)
	if len(mc.pipelineCalls) != 2 {
		t.Fatalf("pipeline calls after second push = %d, want 2", len(mc.pipelineCalls))
	}
	state, _ = mc.getPollState()
	if _, ok := state.DispatchedKeysFull["review:mr_event-12-synchronized-ccc"]; !ok {
		t.Errorf("DispatchedKeysFull missing the second head: %v", state.DispatchedKeysFull)
	}
}

func TestRoutableEventKey_SynchronizedWithoutHeadFallsBack(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	ev := RoutableEvent{Type: "mr_event", Action: "synchronized", IID: 3, UpdatedAt: at}
	if want := fmt.Sprintf("mr_event-3-synchronized-%d", at.Unix()); ev.Key() != want {
		t.Errorf("Key() = %q, want %q", ev.Key(), want)
	}
}

// TestRun_MRVersionsFailureFailsCycle: a failed diff-version lookup holds
// the watermark and must also fail the cycle, so a persistent failure does
// not look like a healthy poll.
func TestRun_MRVersionsFailureFailsCycle(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-5 * time.Minute)
	alice := UserRef{ID: 42, Username: "alice"}
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	mc.projectPaths[1] = "group/project"
	mc.setPollState(persistedPollState{LastPollAtFull: since.Format(time.RFC3339), IssueOpenCutoff: since})
	mr := pushedMR(14, alice)
	mr.CreatedAt = since.Add(-time.Hour)
	mr.UpdatedAt = now
	mc.mrs = []MergeRequest{mr}
	mc.mrVersionsErr[14] = errors.New("boom")

	router := dispatch.NewHarnessRouter([]string{"review"})
	p := New(mc, router, "group/project", withTestSecret(Options{
		Mode:        "events",
		BotUserID:   100,
		GitLabURL:   "https://gitlab.example.com",
		PipelineRef: "main",
	}))
	err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list diff versions for MR 14") {
		t.Fatalf("Run error = %v, want diff version lookup failure", err)
	}
	state, _ := mc.getPollState()
	if got, perr := time.Parse(time.RFC3339, state.LastPollAtFull); perr != nil || got.After(since) {
		t.Errorf("watermark = %q, want held at or before %v", state.LastPollAtFull, since)
	}
}

// TestRun_ForkMRPushNotDispatched: a push to a fork MR authored by a
// project Developer must not trigger review under the author's identity,
// since the pusher is unknown and may not be a project member.
func TestRun_ForkMRPushNotDispatched(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-5 * time.Minute)
	dev := UserRef{ID: 42, Username: "dev"}
	mc := newMockClient()
	mc.memberLevel[dev.ID] = 30
	mc.projectPaths[1] = "group/project"
	mc.projectPaths[2] = "contributor/project"
	mc.setPollState(persistedPollState{LastPollAtFull: since.Format(time.RFC3339), IssueOpenCutoff: since})
	mr := pushedMR(15, dev)
	mr.SourceProjectID = 2
	mr.CreatedAt = since.Add(-time.Hour)
	mr.UpdatedAt = now
	mc.mrs = []MergeRequest{mr}
	mc.mrVersions[15] = []MergeRequestDiffVersion{
		{ID: 41, HeadCommitSHA: "bbb", CreatedAt: now},
		{ID: 40, HeadCommitSHA: "aaa", CreatedAt: mr.CreatedAt},
	}

	router := dispatch.NewHarnessRouter([]string{"triage", "code", "review", "fix", "retro"})
	p := New(mc, router, "group/project", withTestSecret(Options{
		Mode:        "events",
		BotUserID:   100,
		GitLabURL:   "https://gitlab.example.com",
		PipelineRef: "main",
	}))
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(mc.pipelineCalls) != 0 {
		t.Fatalf("pipeline calls = %d, want 0 for a fork MR push: %+v", len(mc.pipelineCalls), mc.pipelineCalls)
	}
}

// TestRun_MRPushStateLookupFailureHoldsWatermark: a failed project-path
// lookup for a synchronized event must fail the cycle and hold the
// watermark before the push, even when a newer unrelated event is
// discovered, so the push review is retried once the lookup recovers.
func TestRun_MRPushStateLookupFailureHoldsWatermark(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-10 * time.Minute)
	pushAt := since.Add(time.Minute)
	alice := UserRef{ID: 42, Username: "alice"}
	mc := newMockClient()
	mc.memberLevel[alice.ID] = 30
	// No projectPaths entry: GetProjectPath fails for the pushed MR.
	mc.setPollState(persistedPollState{LastPollAtFull: since.Format(time.RFC3339), IssueOpenCutoff: since})
	pushed := pushedMR(16, alice)
	pushed.CreatedAt = since.Add(-time.Hour)
	pushed.UpdatedAt = pushAt
	other := pushedMR(17, alice)
	other.CreatedAt = now
	other.UpdatedAt = now
	mc.mrs = []MergeRequest{pushed, other}
	mc.mrVersions[16] = []MergeRequestDiffVersion{
		{ID: 51, HeadCommitSHA: "bbb", CreatedAt: pushAt},
		{ID: 50, HeadCommitSHA: "aaa", CreatedAt: pushed.CreatedAt},
	}

	router := dispatch.NewHarnessRouter([]string{"review"})
	p := New(mc, router, "group/project", withTestSecret(Options{
		Mode:        "events",
		BotUserID:   100,
		GitLabURL:   "https://gitlab.example.com",
		PipelineRef: "main",
	}))
	err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "build change proposal state") {
		t.Fatalf("Run error = %v, want change proposal state failure", err)
	}
	state, _ := mc.getPollState()
	if got, perr := time.Parse(time.RFC3339, state.LastPollAtFull); perr != nil || got.After(pushAt) {
		t.Errorf("watermark = %q, want held at or before the push %v", state.LastPollAtFull, pushAt)
	}
}
