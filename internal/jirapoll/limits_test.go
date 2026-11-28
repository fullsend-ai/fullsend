package jirapoll

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// The matrix output is measured as the compact JSON the managed workflow
// builds, at two bytes for each byte of its UTF-8 text.
func TestMatrixOutputSize(t *testing.T) {
	counted := func(s string) int { return 2 * len(s) }

	if got, want := matrixOutputSize(nil), counted(`{"include":[]}`); got != want {
		t.Errorf("empty matrix = %d, want %d", got, want)
	}

	records := []DispatchRecord{
		{Agent: "triage", Role: "triage", SourceRepo: "acme/platform", EventType: "comment", EventPayload: json.RawMessage(`{"body":"héllo 語 😀"}`), StatusRepo: "acme/platform", StatusNumber: "7"},
		{Agent: "code", TriggerSource: "label", EventPayload: json.RawMessage(`{ "body" : "a\u003cb" }`)},
	}
	// é is two bytes of UTF-8, 語 three and 😀 four. The second payload is
	// compacted, and its escaped < keeps its six characters.
	want := counted(`{"include":[` +
		`{"agent":"triage","role":"triage","source_repo":"acme/platform","event_type":"comment","event_payload":{"body":"héllo 語 😀"},"status_repo":"acme/platform","status_number":"7"},` +
		`{"agent":"code","role":"","source_repo":"","event_type":"","event_payload":{"body":"a\u003cb"},"trigger_source":"label","status_repo":"","status_number":""}` +
		`]}`)
	if got := matrixOutputSize(records); got != want {
		t.Errorf("matrixOutputSize() = %d, want %d", got, want)
	}

	// jq writes U+007F as \u007f: six bytes where Go wrote one.
	plain := recordOutputSize(DispatchRecord{EventPayload: json.RawMessage(`{"body":"ab"}`)})
	withDEL := recordOutputSize(DispatchRecord{EventPayload: json.RawMessage("{\"body\":\"a\x7fb\"}")})
	if got, want := withDEL-plain, counted(`\u007f`); got != want {
		t.Errorf("a U+007F adds %d, want %d", got, want)
	}

	if got := recordOutputSize(DispatchRecord{EventPayload: json.RawMessage(`{`)}); got != 0 {
		t.Errorf("a record that cannot be encoded = %d, want 0", got)
	}
}

// The count is never below the size of the matrix jq builds from the same
// records, whatever characters a comment holds, and is exact for text that
// Go and jq write alike.
func TestMatrixOutputSizeIsNotBelowJq(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is not installed")
	}

	var ascii strings.Builder
	for c := rune(0); c < 0x80; c++ {
		ascii.WriteRune(c)
	}
	bodies := map[string]string{
		"plain text":            "/fs-triage please look at this",
		"every ASCII code":      ascii.String(),
		"U+007F":                strings.Repeat("\x7f", 100),
		"HTML characters":       strings.Repeat("<>&", 100),
		"line separators":       strings.Repeat("\u2028\u2029", 100),
		"two-byte characters":   strings.Repeat("é", 100),
		"three-byte characters": strings.Repeat("語", 100),
		"four-byte characters":  strings.Repeat("😀", 100),
		"invalid UTF-8":         strings.Repeat("\xff", 100),
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]string{"body": body})
			if err != nil {
				t.Fatal(err)
			}
			records := []DispatchRecord{
				{Agent: "triage", EventPayload: payload, StatusNumber: "7"},
				{Agent: "code", EventPayload: payload, StatusNumber: "8"},
			}
			file, err := json.Marshal(records)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(jq, "-cj", "{include: .}")
			cmd.Stdin = bytes.NewReader(file)
			matrix, err := cmd.Output()
			if err != nil {
				t.Fatalf("jq: %v", err)
			}

			got, min := matrixOutputSize(records), 2*len(matrix)
			if got < min {
				t.Errorf("matrixOutputSize() = %d, below the %d GitHub counts for jq's output", got, min)
			}
			if name == "plain text" && got != min {
				t.Errorf("matrixOutputSize() = %d, want exactly %d for plain text", got, min)
			}
		})
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
// kept in the order they were produced, the rest dropped with a WARNING
// that names the issue, and the checkpoint advances (ADR 0085, Decision
// item 3).
func TestProcessIssueKeepsOldest250Records(t *testing.T) {
	for _, targetForge := range []string{TargetForgeGitHub, TargetForgeGitLab} {
		t.Run(targetForge, func(t *testing.T) {
			// 90 comments with 3 records each: 270 records.
			pc, logs := limitsTestIssue(t, targetForge, 90, commentMatcher{agents: 3})

			if len(pc.records) != maxRecordsPerIssue {
				t.Fatalf("kept %d records, want %d", len(pc.records), maxRecordsPerIssue)
			}
			bodies := bodiesOf(t, pc.records)
			// The 83 oldest comments (c007 to c089) take 249 places and
			// c006 gets one. The listing order, newest first, is kept.
			if bodies[0] != "/fs-triage c006" || bodies[1] != "/fs-triage c007" || bodies[249] != "/fs-triage c089" {
				t.Errorf("kept %q, %q ... %q; want one record of c006, then c007 to c089", bodies[0], bodies[1], bodies[249])
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
		if got, want := strings.Join(bodiesOf(t, pc.records), ","), "/fs-triage c007,/fs-triage c008,/fs-triage c009"; got != want {
			t.Errorf("kept %s, want the three oldest comments in listing order: %s", got, want)
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

// cycleMatcher produces what each comment asks for with a body of the form
// "/fs-triage records=N pad=M": N records, each padded with M characters.
// A body of "/fs-triage fail" fails the match.
type cycleMatcher struct{}

func (cycleMatcher) Match(_ context.Context, event *normevent.Event) ([]DispatchRecord, error) {
	if event.Transition.Comment == nil {
		return nil, nil
	}
	body := event.Transition.Comment.Body
	if body == "/fs-triage fail" {
		return nil, fmt.Errorf("no route")
	}
	var n, pad int
	if _, err := fmt.Sscanf(body, "/fs-triage records=%d pad=%d", &n, &pad); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]string{"pad": strings.Repeat("x", pad)})
	if err != nil {
		return nil, err
	}
	records := make([]DispatchRecord, n)
	for i := range records {
		records[i] = DispatchRecord{Agent: fmt.Sprintf("agent-%d", i), StatusNumber: fmt.Sprintf("%d", event.Entity.ID), EventPayload: payload}
	}
	return records, nil
}

// cycleTestClient returns a client whose search lists one issue per entry of
// bodies, PROJ-1 first, each with those comments.
func cycleTestClient(bodies ...[]string) *mockClient {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{"dev-account": "Developers"}
	for i, issueBodies := range bodies {
		key := fmt.Sprintf("PROJ-%d", i+1)
		mc.searchResult = append(mc.searchResult, idleTestIssue(i+1, now.Add(-48*time.Hour), now.Add(-time.Minute)))
		for j, body := range issueBodies {
			mc.comments[key] = append(mc.comments[key], jira.Comment{
				ID:      fmt.Sprintf("%d%02d", i+1, j),
				Body:    body,
				Created: now.Add(-time.Minute).Format(jiraTimeLayout),
				Author:  jira.User{AccountID: "dev-account", AccountType: "atlassian"},
			})
		}
	}
	return mc
}

// runCycleWithLimit runs one cycle on mc with the given record limit and
// returns the issue ID of each dispatched record and the cycle's log.
func runCycleWithLimit(t *testing.T, mc *mockClient, targetForge string, limit int) ([]string, string) {
	t.Helper()
	logs := captureLog(t)
	p := newTestPoller(mc, cycleMatcher{}, Options{
		TargetRepo:            "acme/platform",
		TargetForge:           targetForge,
		GitLabURL:             "https://gitlab.example.com",
		JiraBaseURL:           "https://acme.atlassian.net",
		JiraProject:           "PROJ",
		OutputPath:            filepath.Join(t.TempDir(), "dispatches.json"),
		MaxDispatchesPerCycle: limit,
	})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	return dispatchedKeys(p), logs.String()
}

const oneRecord = "/fs-triage records=1 pad=0"

// The active issues are served in the search's order, with no random pick
// and no limit of five issues (ADR 0085, Decision item 3).
func TestRunServesActiveIssuesInSearchOrder(t *testing.T) {
	bodies := make([][]string, 8)
	for i := range bodies {
		bodies[i] = []string{oneRecord}
	}
	served, logs := runCycleWithLimit(t, cycleTestClient(bodies...), TargetForgeGitHub, 0)

	if got, want := strings.Join(served, ","), "10001,10002,10003,10004,10005,10006,10007,10008"; got != want {
		t.Errorf("served %s, want all eight in search order: %s", got, want)
	}
	if want := "poll complete: 8 candidates, 8 active, 8 served, 8 dispatches"; !strings.Contains(logs, want) {
		t.Errorf("log lacks %q:\n%s", want, logs)
	}
}

// A cycle stops reading issues once it holds max_dispatches_per_cycle
// records; the issues behind wait, unread, for the next cycle.
func TestRunStopsAtTheRecordLimit(t *testing.T) {
	mc := cycleTestClient([]string{oneRecord}, []string{oneRecord}, []string{oneRecord}, []string{oneRecord}, []string{oneRecord})

	served, logs := runCycleWithLimit(t, mc, TargetForgeGitHub, 3)
	if got := strings.Join(served, ","); got != "10001,10002,10003" {
		t.Errorf("cycle 1 served %s, want the first three", got)
	}
	if want := "the cycle holds 3 dispatch records, at its limit of 3; 2 active issue(s) wait for the next cycle"; !strings.Contains(logs, want) {
		t.Errorf("log lacks %q:\n%s", want, logs)
	}
	if mc.listCommentsCalls["PROJ-4"]+mc.listCommentsCalls["PROJ-5"]+mc.propertySetCalls["PROJ-4"]+mc.propertySetCalls["PROJ-5"] != 0 {
		t.Error("cycle 1 read or wrote an issue behind the limit")
	}

	served, _ = runCycleWithLimit(t, mc, TargetForgeGitHub, 3)
	if got := strings.Join(served, ","); got != "10004,10005" {
		t.Errorf("cycle 2 served %s, want the two that waited", got)
	}
}

// All of an issue's records go out in the same cycle. An issue that does
// not fit in the room left waits, and the cycle does not skip ahead to a
// smaller issue behind it. Its events are not counted in the cycle it sat
// out.
func TestRunIssueThatDoesNotFitWaits(t *testing.T) {
	mc := cycleTestClient(
		[]string{"/fs-triage records=2 pad=0"},
		[]string{"/fs-triage records=2 pad=0", "/fs-triage fail"},
		[]string{oneRecord},
	)

	served, logs := runCycleWithLimit(t, mc, TargetForgeGitHub, 3)
	if got := strings.Join(served, ","); got != "10001,10001" {
		t.Errorf("cycle 1 served %s, want only PROJ-1's two records", got)
	}
	if want := "the cycle holds 2 dispatch records and the 2 of PROJ-2 do not fit in the room left (limit: 3 records and 768 KB of GitHub job output); it and the 1 active issue(s) behind it wait for the next cycle"; !strings.Contains(logs, want) {
		t.Errorf("log lacks %q:\n%s", want, logs)
	}
	if !storedLastCheckAt(t, mc, "PROJ-2").IsZero() {
		t.Error("cycle 1 advanced the lastCheck of the issue that did not fit")
	}
	if mc.listCommentsCalls["PROJ-3"] != 0 {
		t.Error("cycle 1 skipped ahead to PROJ-3")
	}
	if strings.Contains(logs, "because matching them failed") {
		t.Errorf("cycle 1 counted the events of an issue it did not take:\n%s", logs)
	}

	served, logs = runCycleWithLimit(t, mc, TargetForgeGitHub, 3)
	if got := strings.Join(served, ","); got != "10002,10002,10003" {
		t.Errorf("cycle 2 served %s, want PROJ-2's two records and then PROJ-3", got)
	}
	if want := "dropped 1 of 3 events this cycle because matching them failed"; !strings.Contains(logs, want) {
		t.Errorf("log lacks %q:\n%s", want, logs)
	}
}

// An issue with more records than the limit is taken when the cycle holds
// none, so it is dispatched alone and does not hold back the issues behind
// it.
func TestRunIssueOverTheLimitGoesAlone(t *testing.T) {
	mc := cycleTestClient(
		[]string{oneRecord},
		[]string{"/fs-triage records=3 pad=0"},
		[]string{oneRecord},
	)

	for cycle, want := range []string{"10001", "10002,10002,10002", "10003"} {
		served, _ := runCycleWithLimit(t, mc, TargetForgeGitHub, 2)
		if got := strings.Join(served, ","); got != want {
			t.Errorf("cycle %d served %s, want %s", cycle+1, got, want)
		}
	}
}

// On the GitHub target a cycle also stops before the issue whose records
// would take the matrix output past 768 KB. GitLab has no batch output, so
// the size limit does not apply there.
func TestRunStopsBeforeTheSizeLimit(t *testing.T) {
	// Five issues with one record of about 200 KB each.
	big := []string{"/fs-triage records=1 pad=100000"}

	served, logs := runCycleWithLimit(t, cycleTestClient(big, big, big, big, big), TargetForgeGitHub, 0)
	if got := strings.Join(served, ","); got != "10001,10002,10003" {
		t.Errorf("GitHub served %s, want the three issues that fit in 768 KB", got)
	}
	if want := "the 1 of PROJ-4 do not fit in the room left"; !strings.Contains(logs, want) {
		t.Errorf("log lacks %q:\n%s", want, logs)
	}

	served, _ = runCycleWithLimit(t, cycleTestClient(big, big, big, big, big), TargetForgeGitLab, 0)
	if got := strings.Join(served, ","); got != "10001,10002,10003,10004,10005" {
		t.Errorf("GitLab served %s, want all five", got)
	}
}
