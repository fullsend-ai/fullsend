package poll

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

// Every GitLab MR event carries its head commit (#9071): synchronized
// events the diff-version head they are keyed on, every other MR event
// the MR's sha.

const (
	mrAPISHA   = "cccccccccccccccccccccccccccccccccccccccc"
	diffVerSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// captureLogs redirects the standard logger for the test's duration.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &logs
}

func TestDiscoverAllEvents_MREventsCarryHeadSHA(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	author := UserRef{ID: 42, Username: "alice"}
	closer := UserRef{ID: 43, Username: "bob"}

	// Opened inside the window and then pushed to, so it yields an open,
	// a push and a comment. The API sha deliberately differs from the
	// diff-version head to show which source each event uses.
	open := pushedMR(12, author)
	open.CreatedAt = since.Add(10 * time.Second)
	open.UpdatedAt = now
	open.SHA = mrAPISHA

	merged := pushedMR(13, author)
	merged.State = "merged"
	merged.CreatedAt = since.Add(-time.Hour)
	merged.MergedAt = now
	merged.MergeUser = closer
	merged.SHA = mrAPISHA

	closed := pushedMR(14, author)
	closed.State = "closed"
	closed.CreatedAt = since.Add(-time.Hour)
	closed.ClosedAt = now
	closed.ClosedBy = closer
	closed.SHA = mrAPISHA

	mc := newMockClient()
	mc.mrs = []MergeRequest{open, merged, closed}
	mc.mrVersions[12] = []MergeRequestDiffVersion{
		{ID: 31, HeadCommitSHA: diffVerSHA, CreatedAt: now},
		{ID: 30, HeadCommitSHA: "aaa", CreatedAt: open.CreatedAt},
	}
	mc.mrNotes[12] = []Note{{ID: 501, Body: "looks good", Author: closer, CreatedAt: now}}

	p := newEventsPoller(mc)
	events, _, _, err := p.discoverAllEvents(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byKey := make(map[string]RoutableEvent, len(events))
	for _, ev := range events {
		byKey[ev.Key()] = ev
	}
	// Keys are those used before head SHAs were carried: the SHA keys only
	// the synchronized event, and there it is still the diff-version head.
	want := map[string]string{
		fmt.Sprintf("mr_event-12-opened-%d", open.CreatedAt.Unix()): mrAPISHA,
		"mr_event-12-synchronized-" + diffVerSHA:                    diffVerSHA,
		"note-501":                                                  mrAPISHA,
		fmt.Sprintf("mr_event-13-%d", now.Unix()):                   mrAPISHA,
		fmt.Sprintf("mr_event-14-closed-%d", now.Unix()):            mrAPISHA,
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
	}
	for key, sha := range want {
		ev, ok := byKey[key]
		if !ok {
			t.Errorf("no event with key %q; got %+v", key, events)
			continue
		}
		if ev.HeadSHA != sha {
			t.Errorf("%s HeadSHA = %q, want %q", key, ev.HeadSHA, sha)
		}
	}
}

func TestDiscoverSlashCommands_MRNoteCarriesHeadSHA(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	since := now.Add(-time.Minute)
	mc := newMockClient()
	mc.events = []ProjectEvent{{
		ID:        1,
		Author:    UserRef{ID: 42, Username: "alice"},
		Note:      EventNote{ID: 77, NoteableType: "MergeRequest", NoteableIID: 12, Body: "/fs-review"},
		CreatedAt: now,
	}}
	mr := pushedMR(12, UserRef{ID: 42, Username: "alice"})
	mr.SHA = mrAPISHA
	mc.mr[12] = &mr

	p := newEventsPoller(mc)
	events, _, err := p.discoverSlashCommands(context.Background(), "group", "project", since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	if events[0].HeadSHA != mrAPISHA {
		t.Errorf("HeadSHA = %q, want %q", events[0].HeadSHA, mrAPISHA)
	}
	if events[0].Key() != "note-77" {
		t.Errorf("Key() = %q, want note-77", events[0].Key())
	}
}

func TestApplyMRFields_HeadSHA(t *testing.T) {
	mr := pushedMR(12, UserRef{ID: 42, Username: "alice"})
	mr.SHA = mrAPISHA

	ev := RoutableEvent{Type: "mr_event", Action: "opened", IID: 12}
	applyMRFields(&ev, &mr)
	if ev.HeadSHA != mrAPISHA {
		t.Errorf("HeadSHA = %q, want the MR sha %q", ev.HeadSHA, mrAPISHA)
	}

	synced := RoutableEvent{Type: "mr_event", Action: "synchronized", IID: 12, HeadSHA: diffVerSHA}
	applyMRFields(&synced, &mr)
	if synced.HeadSHA != diffVerSHA {
		t.Errorf("synchronized HeadSHA = %q, want the diff-version head %q kept", synced.HeadSHA, diffVerSHA)
	}
}

func TestChangeProposalHeadSHA(t *testing.T) {
	tests := []struct {
		name     string
		event    RoutableEvent
		mrErr    error
		want     string
		wantWarn bool
	}{
		{
			name:  "discovered SHA used without lookup",
			event: RoutableEvent{Type: "mr_note", IID: 12, HeadSHA: diffVerSHA},
			mrErr: errors.New("must not be called"),
			want:  diffVerSHA,
		},
		{
			name:  "synchronized keeps diff-version head",
			event: RoutableEvent{Type: "mr_event", Action: "synchronized", IID: 12, HeadSHA: diffVerSHA},
			want:  diffVerSHA,
		},
		{
			name:  "synchronized without head is not filled from the MR",
			event: RoutableEvent{Type: "mr_event", Action: "synchronized", IID: 12},
			want:  "",
		},
		{
			name:  "missing SHA falls back to MR lookup",
			event: RoutableEvent{Type: "mr_note", IID: 12},
			want:  mrAPISHA,
		},
		{
			name:     "lookup failure leaves SHA empty with a warning",
			event:    RoutableEvent{Type: "mr_event", Action: "opened", IID: 12},
			mrErr:    errors.New("boom"),
			want:     "",
			wantWarn: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			mc := newMockClient()
			mr := pushedMR(12, UserRef{ID: 42, Username: "alice"})
			mr.SHA = mrAPISHA
			mc.mr[12] = &mr
			if tt.mrErr != nil {
				mc.mrErr[12] = tt.mrErr
			}
			p := newEventsPoller(mc)
			if got := p.changeProposalHeadSHA(context.Background(), tt.event); got != tt.want {
				t.Errorf("head SHA = %q, want %q", got, tt.want)
			}
			if warned := strings.Contains(logs.String(), "WARNING: get MR !12 for head SHA"); warned != tt.wantWarn {
				t.Errorf("warning logged = %v, want %v; logs: %s", warned, tt.wantWarn, logs.String())
			}
		})
	}
}

// mrNoteForDispatch is a human comment on a same-project MR, resolvable
// by toNormalizedEvent against mc.
func mrNoteForDispatch(mc *mockClient) RoutableEvent {
	mc.memberLevel[42] = 30
	mc.projectPaths[1] = "group/project"
	return RoutableEvent{
		Type:            "mr_note",
		IID:             12,
		UpdatedAt:       time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		NoteBody:        "/fs-review",
		NoteID:          77,
		NoteAuthorID:    42,
		NoteAuthorLogin: "alice",
		MRSource:        1,
		MRTarget:        1,
		MRAuthorID:      42,
		MRAuthorLogin:   "alice",
		SourceBranch:    "feature",
		TargetBranch:    "main",
		Labels:          []string{},
	}
}

// signedChangeProposalHeadSHA normalizes and dispatches event and returns
// the change_proposal head SHA from the signed dispatch payload.
func signedChangeProposalHeadSHA(t *testing.T, mc *mockClient, event RoutableEvent) string {
	t.Helper()
	p := newTestPoller(mc, Options{GitLabURL: "https://gitlab.com"})
	ne, _, err := p.toNormalizedEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("toNormalizedEvent: %v", err)
	}
	if err := p.dispatch(context.Background(), "group", "project", "review", event, &ne); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("expected 1 pipeline call, got %d", len(mc.pipelineCalls))
	}
	data, err := base64.StdEncoding.DecodeString(mc.pipelineCalls[0].Variables["EVENT_PAYLOAD_B64"])
	if err != nil {
		t.Fatalf("decode EVENT_PAYLOAD_B64: %v", err)
	}
	cp := payloadEvent(t, data).State.ChangeProposal
	if cp == nil {
		t.Fatalf("signed payload has no change_proposal: %s", data)
	}
	return cp.HeadSHA
}

func TestDispatch_MRNoteSignedPayloadIncludesHeadSHA(t *testing.T) {
	mc := newMockClient()
	event := mrNoteForDispatch(mc)
	event.HeadSHA = testMRHeadSHA
	if got := signedChangeProposalHeadSHA(t, mc, event); got != testMRHeadSHA {
		t.Errorf("signed change_proposal.head_sha = %q, want %q", got, testMRHeadSHA)
	}
}

func TestDispatch_MRNoteHeadSHALookupFailureStillDispatches(t *testing.T) {
	logs := captureLogs(t)
	mc := newMockClient()
	mc.mrErr[12] = errors.New("boom")
	event := mrNoteForDispatch(mc)
	if got := signedChangeProposalHeadSHA(t, mc, event); got != "" {
		t.Errorf("signed change_proposal.head_sha = %q, want empty", got)
	}
	if !strings.Contains(logs.String(), "WARNING: get MR !12 for head SHA of mr_note") {
		t.Errorf("no warning logged for the failed lookup; logs: %s", logs.String())
	}
}
