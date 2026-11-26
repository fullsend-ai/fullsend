package jirapoll

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// The matrix output is measured as the compact JSON the managed workflow
// builds, two bytes per UTF-16 code unit.
func TestMatrixOutputSize(t *testing.T) {
	utf16Bytes := func(s string) int { return 2 * len(utf16.Encode([]rune(s))) }

	if got, want := matrixOutputSize(nil), utf16Bytes(`{"include":[]}`); got != want {
		t.Errorf("empty matrix = %d, want %d", got, want)
	}

	records := []DispatchRecord{
		{Agent: "triage", Role: "triage", SourceRepo: "acme/platform", EventType: "comment", EventPayload: json.RawMessage(`{"body":"héllo 😀"}`), StatusRepo: "acme/platform", StatusNumber: "7"},
		{Agent: "code", TriggerSource: "label", EventPayload: json.RawMessage(`{ "body" : "a\u003cb" }`)},
	}
	// é is one UTF-16 code unit and 😀 is two. The second payload is
	// compacted, and its escaped < keeps its six characters.
	want := utf16Bytes(`{"include":[` +
		`{"agent":"triage","role":"triage","source_repo":"acme/platform","event_type":"comment","event_payload":{"body":"héllo 😀"},"status_repo":"acme/platform","status_number":"7"},` +
		`{"agent":"code","role":"","source_repo":"","event_type":"","event_payload":{"body":"a\u003cb"},"trigger_source":"label","status_repo":"","status_number":""}` +
		`]}`)
	if got := matrixOutputSize(records); got != want {
		t.Errorf("matrixOutputSize() = %d, want %d", got, want)
	}

	if got := recordOutputSize(DispatchRecord{EventPayload: json.RawMessage(`{`)}); got != 0 {
		t.Errorf("a record that cannot be encoded = %d, want 0", got)
	}
}

// commentMatcher produces agents records per event. Each record carries the
// comment's body, so a test can tell which events' records were kept, and
// pad extra characters.
type commentMatcher struct {
	agents int
	pad    int
}

func (m commentMatcher) Match(_ context.Context, event *normevent.Event) ([]DispatchRecord, error) {
	body := ""
	if event.Transition.Comment != nil {
		body = event.Transition.Comment.Body
	}
	payload, err := json.Marshal(map[string]string{"body": body, "pad": strings.Repeat("x", m.pad)})
	if err != nil {
		return nil, err
	}
	records := make([]DispatchRecord, m.agents)
	for i := range records {
		records[i] = DispatchRecord{Agent: fmt.Sprintf("agent-%d", i), EventPayload: payload}
	}
	return records, nil
}

// limitsTestIssue processes an issue with n comments, listed newest first:
// comment c000 is the newest and the last one the oldest.
func limitsTestIssue(t *testing.T, targetForge string, n int, matcher EventMatcher) (pendingCheckpoint, string) {
	t.Helper()
	logs := captureLog(t)
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	for i := range n {
		mc.comments["PROJ-123"] = append(mc.comments["PROJ-123"], jira.Comment{
			ID:      fmt.Sprintf("%d", i),
			Body:    fmt.Sprintf("/fs-triage c%03d", i),
			Created: now.Add(-time.Duration(i+1) * time.Minute).Format(jiraTimeLayout),
			Author:  jira.User{AccountID: "u1", AccountType: "atlassian"},
		})
	}
	mc.roleMembership = map[string]string{"u1": "Developers"}

	p := newTestPoller(mc, matcher, Options{
		TargetRepo:  "acme/platform",
		TargetForge: targetForge,
		GitLabURL:   "https://gitlab.example.com",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})
	pc, err := p.processIssue(context.Background(), idleTestIssue(123, now.Add(-48*time.Hour), now))
	if err != nil {
		t.Fatalf("processIssue() error: %v", err)
	}
	if pc.t.IsZero() {
		t.Error("the checkpoint does not advance")
	}
	return pc, logs.String()
}

// bodiesOf returns the comment body each record carries.
func bodiesOf(t *testing.T, records []DispatchRecord) []string {
	t.Helper()
	bodies := make([]string, len(records))
	for i, record := range records {
		var payload struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(record.EventPayload, &payload); err != nil {
			t.Fatalf("record %d payload: %v", i, err)
		}
		bodies[i] = payload.Body
	}
	return bodies
}

// One issue contributes at most 250 records on both targets: the oldest are
// kept, the rest dropped with a WARNING that names the issue, and the
// checkpoint advances (ADR 0085, Decision item 3).
func TestProcessIssueKeepsOldest250Records(t *testing.T) {
	for _, targetForge := range []string{TargetForgeGitHub, TargetForgeGitLab} {
		t.Run(targetForge, func(t *testing.T) {
			// 90 comments with 3 records each: 270 records.
			pc, logs := limitsTestIssue(t, targetForge, 90, commentMatcher{agents: 3})

			if len(pc.records) != maxRecordsPerIssue {
				t.Fatalf("kept %d records, want %d", len(pc.records), maxRecordsPerIssue)
			}
			bodies := bodiesOf(t, pc.records)
			// The 83 oldest comments (c089 down to c007) fill 249 places
			// and c006 gets the last one.
			if bodies[0] != "/fs-triage c089" || bodies[249] != "/fs-triage c006" {
				t.Errorf("kept from %q to %q, want the oldest first: c089 to c006", bodies[0], bodies[249])
			}
			for _, body := range bodies {
				if body < "/fs-triage c006" {
					t.Fatalf("kept a record of %q, one of the newest comments", body)
				}
			}
			if want := "WARNING: PROJ-123 produced 270 dispatch records this cycle"; !strings.Contains(logs, want) {
				t.Errorf("log lacks %q:\n%s", want, logs)
			}
			if want := "the oldest 250 are kept and 20 dropped"; !strings.Contains(logs, want) {
				t.Errorf("log lacks %q:\n%s", want, logs)
			}
		})
	}
}

// On the GitHub target one issue also contributes at most 768 KB of matrix
// output; GitLab has no batch output, so the size limit does not apply
// there.
func TestProcessIssueKeepsOldestRecordsWithinSizeLimit(t *testing.T) {
	// 10 records of about 200 KB each: 100,000 padding characters at two
	// bytes each.
	matcher := commentMatcher{agents: 1, pad: 100_000}

	t.Run("github", func(t *testing.T) {
		pc, logs := limitsTestIssue(t, TargetForgeGitHub, 10, matcher)

		if len(pc.records) != 3 {
			t.Fatalf("kept %d records, want 3 of about 200 KB each", len(pc.records))
		}
		size := matrixOutputSize(pc.records)
		if size > maxMatrixOutputBytes {
			t.Errorf("kept %d bytes, over the %d limit", size, maxMatrixOutputBytes)
		}
		if next := size + 2 + recordOutputSize(pc.records[0]); next <= maxMatrixOutputBytes {
			t.Errorf("one more record would make %d bytes, within the %d limit, yet it was dropped", next, maxMatrixOutputBytes)
		}
		if bodies := bodiesOf(t, pc.records); bodies[0] != "/fs-triage c009" || bodies[2] != "/fs-triage c007" {
			t.Errorf("kept %v, want the three oldest comments, oldest first", bodies)
		}
		if want := "one issue contributes at most 250 records and 768 KB of GitHub job output, so the oldest 3 are kept and 7 dropped"; !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	})

	// A record that is over the limit on its own is dropped too: dispatching
	// it would lose the whole batch.
	t.Run("github, one record over the limit", func(t *testing.T) {
		pc, logs := limitsTestIssue(t, TargetForgeGitHub, 1, commentMatcher{agents: 1, pad: 400_000})

		if len(pc.records) != 0 {
			t.Errorf("kept %d records, want 0", len(pc.records))
		}
		if want := "the oldest 0 are kept and 1 dropped"; !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	})

	t.Run("gitlab", func(t *testing.T) {
		pc, logs := limitsTestIssue(t, TargetForgeGitLab, 10, matcher)

		if len(pc.records) != 10 {
			t.Errorf("kept %d records, want all 10: the size limit is for GitHub only", len(pc.records))
		}
		if strings.Contains(logs, "dispatch records this cycle") {
			t.Errorf("logged a limit that was not reached:\n%s", logs)
		}
	})
}

// Records within the limits keep the order they were produced in.
func TestProcessIssueWithinLimitsKeepsOrder(t *testing.T) {
	pc, logs := limitsTestIssue(t, TargetForgeGitHub, 3, commentMatcher{agents: 1})

	if got, want := strings.Join(bodiesOf(t, pc.records), ","), "/fs-triage c000,/fs-triage c001,/fs-triage c002"; got != want {
		t.Errorf("records = %s, want the listing order %s", got, want)
	}
	if strings.Contains(logs, "dispatch records this cycle") {
		t.Errorf("logged a limit that was not reached:\n%s", logs)
	}
}
