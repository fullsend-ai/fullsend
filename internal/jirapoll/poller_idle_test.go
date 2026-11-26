package jirapoll

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// Tests for ADR 0160: the poller skips candidates with nothing new since
// their lastCheck before the random pick, and a look advances lastCheck to
// at least the issue's updated value.

// idleTestIssue is a search row for key with the given created and updated
// times. An empty updated time leaves the field empty.
func idleTestIssue(n int, created, updated time.Time) jira.Issue {
	issue := jira.Issue{
		ID:  fmt.Sprintf("%d", 10000+n),
		Key: fmt.Sprintf("PROJ-%d", n),
		Fields: jira.IssueFields{
			Labels:   []string{},
			Reporter: jira.User{AccountID: "reporter-id"},
			Created:  created.Format(jiraTimeLayout),
		},
	}
	if !updated.IsZero() {
		issue.Fields.Updated = updated.Format(jiraTimeLayout)
	}
	return issue
}

// addTriageComment adds a /fs-triage comment at t from a Developers member.
func addTriageComment(mc *mockClient, key, id string, t time.Time) {
	mc.roleMembership = map[string]string{"dev-account": "Developers"}
	mc.comments[key] = append(mc.comments[key], jira.Comment{
		ID:      id,
		Body:    "/fs-triage",
		Created: t.Format(jiraTimeLayout),
		Author:  jira.User{AccountID: "dev-account", AccountType: "atlassian"},
	})
}

func idleTestPoller(mc *mockClient, t *testing.T) *Poller {
	return newTestPoller(mc, &stubMatcher{agents: []string{"triage"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  filepath.Join(t.TempDir(), "dispatches.json"),
	})
}

// storedLastCheckAt returns PROJ-n's stored lastCheck, zero if unset.
func storedLastCheckAt(t *testing.T, mc *mockClient, key string) time.Time {
	t.Helper()
	raw, ok := mc.properties[key][lastCheckPropertyKey("acme", "platform")]
	if !ok {
		return time.Time{}
	}
	var ts string
	if err := json.Unmarshal(raw, &ts); err != nil {
		t.Fatalf("stored lastCheck for %s: %v", key, err)
	}
	got, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("stored lastCheck for %s: %v", key, err)
	}
	return got
}

func dispatchedKeys(p *Poller) []string {
	var keys []string
	for _, d := range p.dispatches {
		keys = append(keys, d.StatusNumber)
	}
	return keys
}

// An idle candidate is dropped before the pick and costs no lock write, no
// listing and no lastCheck write; the cycle line carries the active count.
func TestRunSkipsIdleCandidates(t *testing.T) {
	logs := captureLog(t)
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	for n := 1; n <= 10; n++ {
		key := fmt.Sprintf("PROJ-%d", n)
		setLastCheck(mc, key, "acme", "platform", now.Add(-30*time.Minute))
		if n <= 3 {
			mc.searchResult = append(mc.searchResult, idleTestIssue(n, now.Add(-48*time.Hour), now.Add(-time.Minute)))
			addTriageComment(mc, key, fmt.Sprintf("%d", 500+n), now.Add(-5*time.Minute))
			continue
		}
		// Idle: updated at or before lastCheck. An old comment is listed
		// if the issue is looked at.
		mc.searchResult = append(mc.searchResult, idleTestIssue(n, now.Add(-48*time.Hour), now.Add(-30*time.Minute-time.Duration(n)*time.Second)))
		addTriageComment(mc, key, fmt.Sprintf("%d", 500+n), now.Add(-40*time.Minute))
	}
	// One idle issue's updated equals its lastCheck exactly.
	mc.searchResult[3].Fields.Updated = now.Add(-30 * time.Minute).Format(jiraTimeLayout)

	p := idleTestPoller(mc, t)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if got := dispatchedKeys(p); !sameSet(got, []string{"10001", "10002", "10003"}) {
		t.Errorf("dispatched for %v, want the three active issues", got)
	}
	for n := 4; n <= 10; n++ {
		key := fmt.Sprintf("PROJ-%d", n)
		if c := mc.listCommentsCalls[key] + mc.listChangelogCalls[key] + mc.propertySetCalls[key] + mc.propertyDeleteCalls[key]; c != 0 {
			t.Errorf("idle %s: comments %d, changelog %d, property writes %d, deletes %d; want all zero",
				key, mc.listCommentsCalls[key], mc.listChangelogCalls[key], mc.propertySetCalls[key], mc.propertyDeleteCalls[key])
		}
	}
	if want := "poll complete: 10 candidates, 10 unlocked, 3 active, 3 selected, 3 dispatches"; !strings.Contains(logs.String(), want) {
		t.Errorf("log lacks %q:\n%s", want, logs.String())
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int)
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

// A candidate that holds a live lock is dropped before its lastCheck is
// read.
func TestFilterLockedDoesNotReadLockedLastCheck(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	p := idleTestPoller(mc, t)
	locked := idleTestIssue(1, now.Add(-48*time.Hour), now.Add(-time.Minute))
	free := idleTestIssue(2, now.Add(-48*time.Hour), now.Add(-time.Minute))
	if err := mc.SetEntityProperty(context.Background(), locked.Key, p.lockKey(), LockValue{ID: "other-cycle", TS: now.UTC().Format(time.RFC3339), Phase: "pending"}); err != nil {
		t.Fatal(err)
	}
	setLastCheck(mc, locked.Key, "acme", "platform", now.Add(-30*time.Minute))
	setLastCheck(mc, free.Key, "acme", "platform", now.Add(-30*time.Minute))

	var reads []string
	mc.getPropertyHook = func(issueKey, propKey string) { reads = append(reads, issueKey+" "+propKey) }

	active, unlocked, err := p.filterLocked(context.Background(), []jira.Issue{locked, free})
	if err != nil {
		t.Fatalf("filterLocked() error: %v", err)
	}
	if unlocked != 1 || len(active) != 1 || active[0].Key != free.Key {
		t.Fatalf("filterLocked() = %d active %+v, %d unlocked; want only %s", len(active), active, unlocked, free.Key)
	}
	for _, r := range reads {
		if r == locked.Key+" "+p.lastCheckKey() {
			t.Errorf("read the lastCheck of the locked candidate %s", locked.Key)
		}
	}
	if !slices.Contains(reads, free.Key+" "+p.lastCheckKey()) {
		t.Errorf("did not read the lastCheck of %s; reads: %v", free.Key, reads)
	}
}

// Clamped lastCheck values are reported in one line per cycle, not one
// WARNING per idle issue. The line is an alert only when an issue's
// activity after its lastCheck is itself older than the backfill window.
func TestRunLogsOneClampLinePerCycle(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	seed := func(mc *mockClient) {
		for n := 1; n <= 2; n++ {
			mc.searchResult = append(mc.searchResult, idleTestIssue(n, now.Add(-100*time.Hour), now.Add(-73*time.Hour)))
			setLastCheck(mc, fmt.Sprintf("PROJ-%d", n), "acme", "platform", now.Add(-72*time.Hour))
		}
		mc.searchResult = append(mc.searchResult, idleTestIssue(3, now.Add(-100*time.Hour), now.Add(-time.Hour)))
		setLastCheck(mc, "PROJ-3", "acme", "platform", now.Add(48*time.Hour))
	}
	markerLines := func(logs string) []string {
		var out []string
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, "activity filter:") {
				out = append(out, line)
			}
		}
		return out
	}

	t.Run("no activity lost", func(t *testing.T) {
		logs := captureLog(t)
		mc := newMockClient()
		seed(mc)
		// Active although its lastCheck predates the window: it is about
		// to be looked at, so nothing is lost and the line is no alert.
		mc.searchResult = append(mc.searchResult, idleTestIssue(5, now.Add(-100*time.Hour), now.Add(-time.Hour)))
		setLastCheck(mc, "PROJ-5", "acme", "platform", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
		// Idle, with updated a few ms after a lastCheck older than the
		// window: the offset Jira stamps after the newest entry, which a
		// lastCheck saved before ADR 0160 sits below. Nothing was missed.
		mc.searchResult = append(mc.searchResult, idleTestIssue(6, now.Add(-100*time.Hour), now.Add(-50*time.Hour+5*time.Millisecond)))
		setLastCheck(mc, "PROJ-6", "acme", "platform", now.Add(-50*time.Hour))
		if err := idleTestPoller(mc, t).Run(context.Background()); err != nil {
			t.Fatalf("Run() error: %v", err)
		}
		lines := markerLines(logs.String())
		if len(lines) != 1 {
			t.Fatalf("got %d activity filter lines, want 1:\n%s", len(lines), logs.String())
		}
		for _, key := range []string{"PROJ-1", "PROJ-2", "PROJ-3", "PROJ-5", "PROJ-6"} {
			if !strings.Contains(lines[0], key) {
				t.Errorf("activity filter line lacks %s: %s", key, lines[0])
			}
		}
		if strings.Contains(lines[0], "WARNING:") || strings.Contains(lines[0], "predates the backfill window") {
			t.Errorf("no activity was lost, but the activity filter line is an alert: %s", lines[0])
		}
		// The only backfill alert is the unchanged per-issue WARNING from
		// the look at the active PROJ-5.
		if c, own := strings.Count(logs.String(), "predates the backfill window"), strings.Count(logs.String(), `lastCheck for PROJ-5 ("2020-01-01T00:00:00Z") predates the backfill window`); c != 1 || own != 1 {
			t.Errorf("want exactly one backfill alert, the per-issue line for PROJ-5; got %d:\n%s", c, logs.String())
		}
		for _, key := range []string{"PROJ-1", "PROJ-2"} {
			if strings.Contains(logs.String(), "lastCheck for "+key) {
				t.Errorf("idle %s logged a per-issue lastCheck WARNING:\n%s", key, logs.String())
			}
		}
		if c := strings.Count(logs.String(), "lastCheck for PROJ-3 is in the future"); c != 1 {
			t.Errorf("per-issue future WARNING for the picked PROJ-3 logged %d times, want 1", c)
		}
	})

	t.Run("activity lost in the gap", func(t *testing.T) {
		logs := captureLog(t)
		mc := newMockClient()
		seed(mc)
		mc.searchResult = append(mc.searchResult, idleTestIssue(4, now.Add(-100*time.Hour), now.Add(-30*time.Hour)))
		setLastCheck(mc, "PROJ-4", "acme", "platform", now.Add(-40*time.Hour))
		// Just past the 1 s tolerance.
		mc.searchResult = append(mc.searchResult, idleTestIssue(7, now.Add(-100*time.Hour), now.Add(-50*time.Hour+2*time.Second)))
		setLastCheck(mc, "PROJ-7", "acme", "platform", now.Add(-50*time.Hour))
		if err := idleTestPoller(mc, t).Run(context.Background()); err != nil {
			t.Fatalf("Run() error: %v", err)
		}
		lines := markerLines(logs.String())
		if len(lines) != 1 {
			t.Fatalf("got %d activity filter lines, want 1:\n%s", len(lines), logs.String())
		}
		line := lines[0]
		if !strings.Contains(line, "WARNING: activity filter:") || !strings.Contains(line, "predates the backfill window") {
			t.Errorf("activity filter line is not an alert: %s", line)
		}
		first := strings.Index(line, "PROJ-4")
		for _, key := range []string{"PROJ-1", "PROJ-2", "PROJ-3"} {
			if i := strings.Index(line, key); i < 0 || first < 0 || i < first {
				t.Errorf("want PROJ-4 named before %s: %s", key, line)
			}
		}
		if i := strings.Index(line, "PROJ-7"); i < 0 || i > strings.Index(line, "PROJ-1") {
			t.Errorf("want PROJ-7 in the alert group, before PROJ-1: %s", line)
		}
		if strings.Contains(logs.String(), "lastCheck for PROJ-4") {
			t.Errorf("idle PROJ-4 logged a per-issue WARNING:\n%s", logs.String())
		}
	})
}

// A change inside detectChanges' 10-second margin is handed off, stays
// active (lastCheck is held below it) and is handed off again next cycle:
// today's accepted duplicate, not a loss.
func TestRunInMarginChangeStaysActive(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{idleTestIssue(1, now.Add(-48*time.Hour), now.Add(-2*time.Second))}
	setLastCheck(mc, "PROJ-1", "acme", "platform", now.Add(-20*time.Minute))
	addTriageComment(mc, "PROJ-1", "501", now.Add(-2*time.Second))

	p := idleTestPoller(mc, t)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if len(p.dispatches) != 1 {
		t.Fatalf("cycle 1 dispatched %d, want 1", len(p.dispatches))
	}
	if lc := storedLastCheckAt(t, mc, "PROJ-1"); !lc.Before(now.Add(-2 * time.Second)) {
		t.Errorf("lastCheck %v not below the in-margin comment", lc)
	}

	p = idleTestPoller(mc, t)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if len(p.dispatches) != 1 {
		t.Errorf("cycle 2 dispatched %d, want the same comment again", len(p.dispatches))
	}
}

// A first look at an issue whose activity is all older than the backfill
// window hands off nothing, saves a lastCheck, and the next cycle skips it.
func TestRunFirstLookOldActivityThenIdle(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{idleTestIssue(1, old, old)}
	addTriageComment(mc, "PROJ-1", "501", old)

	p := idleTestPoller(mc, t)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if len(p.dispatches) != 0 {
		t.Errorf("cycle 1 dispatched %d, want 0", len(p.dispatches))
	}
	if storedLastCheckAt(t, mc, "PROJ-1").IsZero() {
		t.Fatal("cycle 1 saved no lastCheck")
	}

	before := mc.listCommentsCalls["PROJ-1"]
	if err := idleTestPoller(mc, t).Run(context.Background()); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if mc.listCommentsCalls["PROJ-1"] != before {
		t.Error("cycle 2 looked at an issue with nothing new")
	}
}

// A garbage lastCheck is treated as unset (#9766): the issue stays active
// (an editor must not be able to hide an issue by writing garbage), gets
// one first-poll look that saves a valid lastCheck, and is idle next cycle.
// Neither warning prints the stored value. The cases cover both ways a
// stored value can fail: JSON that is not a string, and a string that is
// not a timestamp.
func TestRunGarbageLastCheckTreatedAsUnset(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored string
	}{
		{name: "a string that is not a time", stored: `"garbage-time"`},
		{name: "JSON that is not a string", stored: `{"garbage":1}`},
		{name: "not JSON", stored: `garbage, not JSON`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().Truncate(time.Second)
			updated := now.Add(-time.Hour)
			mc := newMockClient()
			mc.searchResult = []jira.Issue{idleTestIssue(1, now.Add(-48*time.Hour), updated)}
			mc.properties["PROJ-1"] = map[string]json.RawMessage{lastCheckPropertyKey("acme", "platform"): json.RawMessage(tc.stored)}
			for n := 2; n <= 4; n++ {
				mc.searchResult = append(mc.searchResult, idleTestIssue(n, now.Add(-48*time.Hour), updated))
				setLastCheck(mc, fmt.Sprintf("PROJ-%d", n), "acme", "platform", updated)
			}

			logs := captureLog(t)
			if err := idleTestPoller(mc, t).Run(context.Background()); err != nil {
				t.Fatalf("cycle 1: %v", err)
			}
			if lc := storedLastCheckAt(t, mc, "PROJ-1"); !lc.Equal(updated) {
				t.Fatalf("lastCheck after cycle 1 = %v, want the issue's updated %v saved over the garbage", lc, updated)
			}
			for _, want := range []string{
				"lastCheck not a timestamp, treated as unset: PROJ-1",
				"WARNING: lastCheck for PROJ-1 is not a timestamp; treating as unset",
			} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("log lacks %q:\n%s", want, logs)
				}
			}
			if strings.Contains(logs.String(), "garbage") {
				t.Errorf("log prints the stored value:\n%s", logs)
			}

			before := mc.listCommentsCalls["PROJ-1"]
			if err := idleTestPoller(mc, t).Run(context.Background()); err != nil {
				t.Fatalf("cycle 2: %v", err)
			}
			if mc.listCommentsCalls["PROJ-1"] != before {
				t.Error("cycle 2 looked at an issue with nothing new")
			}
		})
	}
}

// A cycle whose every selected issue fails ends in an error. Here the one
// active issue's lastCheck cannot be read.
func TestRunAllSelectedIssuesFailedFailsCycle(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{idleTestIssue(1, now.Add(-48*time.Hour), now.Add(-time.Hour))}
	mc.propertyGetErr[lastCheckPropertyKey("acme", "platform")] = fmt.Errorf("503 service unavailable")

	p := idleTestPoller(mc, t)
	err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "all 1 selected issues failed to process") {
		t.Fatalf("Run() error = %v, want the all-failed guard", err)
	}
	if len(p.dispatches) != 0 {
		t.Errorf("dispatched %d, want 0", len(p.dispatches))
	}
}

// Every row of the activity filter's decision table (ADR 0160, Decision
// item 1): only an issue whose usable lastCheck is at or after its updated
// value is dropped; every error and unset case keeps the issue.
func TestFilterLockedActivityDecision(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	key := lastCheckPropertyKey("acme", "platform")
	stamp := func(t time.Time) json.RawMessage {
		raw, _ := json.Marshal(t.UTC().Format(time.RFC3339Nano))
		return raw
	}
	elsewhere := time.FixedZone("UTC-5", -5*3600)
	cases := []struct {
		name    string
		stored  json.RawMessage // nil: no property
		readErr bool
		updated string
		active  bool
	}{
		{name: "read fails", readErr: true, updated: now.Add(-time.Hour).Format(jiraTimeLayout), active: true},
		{name: "not JSON", stored: json.RawMessage(`not json`), updated: now.Add(-time.Hour).Format(jiraTimeLayout), active: true},
		{name: "JSON but not a time", stored: json.RawMessage(`"yesterday"`), updated: now.Add(-time.Hour).Format(jiraTimeLayout), active: true},
		{name: "JSON number", stored: json.RawMessage(`42`), updated: now.Add(-time.Hour).Format(jiraTimeLayout), active: true},
		{name: "missing", updated: now.Add(-time.Hour).Format(jiraTimeLayout), active: true},
		{name: "empty", stored: json.RawMessage{}, updated: now.Add(-time.Hour).Format(jiraTimeLayout), active: true},
		{name: "missing, updated empty", updated: "", active: true},
		{name: "48 h ahead", stored: stamp(now.Add(48 * time.Hour)), updated: now.Add(-time.Hour).Format(jiraTimeLayout), active: true},
		{name: "1 min ahead", stored: stamp(now.Add(time.Minute)), updated: now.Add(-time.Hour).Format(jiraTimeLayout), active: false},
		{name: "set, updated empty", stored: stamp(now.Add(-time.Hour)), updated: "", active: true},
		{name: "set, updated unparseable", stored: stamp(now.Add(-time.Hour)), updated: "last tuesday", active: true},
		{name: "set, updated after", stored: stamp(now.Add(-time.Hour)), updated: now.Add(-time.Minute).Format(jiraTimeLayout), active: true},
		{name: "set, updated equal in another format", stored: stamp(now.Add(-time.Hour)), updated: now.Add(-time.Hour).In(elsewhere).Format(jiraTimeLayout), active: false},
		{name: "set, updated before", stored: stamp(now.Add(-time.Hour)), updated: now.Add(-2 * time.Hour).Format(jiraTimeLayout), active: false},
		{name: "2020, updated 1 h ago", stored: stamp(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)), updated: now.Add(-time.Hour).Format(jiraTimeLayout), active: true},
		{name: "2020, updated 3 days ago", stored: stamp(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)), updated: now.Add(-72 * time.Hour).Format(jiraTimeLayout), active: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mc := newMockClient()
			if tc.readErr {
				mc.propertyGetErr[key] = fmt.Errorf("503 service unavailable")
			}
			if tc.stored != nil {
				mc.properties["PROJ-1"] = map[string]json.RawMessage{key: tc.stored}
			}
			issue := jira.Issue{ID: "10001", Key: "PROJ-1", Fields: jira.IssueFields{Updated: tc.updated}}
			active, unlocked, err := idleTestPoller(mc, t).filterLocked(context.Background(), []jira.Issue{issue})
			if err != nil {
				t.Fatalf("filterLocked() error: %v", err)
			}
			if unlocked != 1 {
				t.Errorf("unlocked = %d, want 1", unlocked)
			}
			if got := len(active) == 1; got != tc.active {
				t.Errorf("active = %v, want %v", got, tc.active)
			}
		})
	}
}

// A GitLab-target poller compares updated with its own lastCheck key, not
// the GitHub-style one.
func TestFilterLockedGitLabTargetReadsItsOwnKey(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	opts := Options{
		TargetRepo:  "group/project",
		TargetForge: TargetForgeGitLab,
		GitLabURL:   "https://gitlab.example.com",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	}
	issue := idleTestIssue(1, now.Add(-48*time.Hour), now.Add(-time.Hour))

	mc := newMockClient()
	p := newTestPoller(mc, nil, opts)
	if err := p.advanceLastCheck(context.Background(), issue.Key, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if active, _, err := p.filterLocked(context.Background(), []jira.Issue{issue}); err != nil || len(active) != 0 {
		t.Errorf("lastCheck equal to updated under the GitLab key: active %v, err %v; want idle", active, err)
	}

	mc = newMockClient()
	setLastCheck(mc, issue.Key, "group", "project", now.Add(-time.Hour))
	if active, _, err := newTestPoller(mc, nil, opts).filterLocked(context.Background(), []jira.Issue{issue}); err != nil || len(active) != 1 {
		t.Errorf("lastCheck only under the GitHub-style key: active %v, err %v; want active", active, err)
	}
}

// runCycle runs one cycle on mc and returns the poller.
func runCycle(t *testing.T, mc *mockClient, name string) *Poller {
	t.Helper()
	p := idleTestPoller(mc, t)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("%s: Run() error: %v", name, err)
	}
	return p
}

// After a look, lastCheck reaches the issue's updated value even when that
// is after the newest comment, so the next cycle skips the issue.
func TestRunLastCheckReachesUpdated(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	updated := now.Add(-time.Minute)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{idleTestIssue(1, now.Add(-48*time.Hour), updated)}
	setLastCheck(mc, "PROJ-1", "acme", "platform", now.Add(-20*time.Minute))
	addTriageComment(mc, "PROJ-1", "501", now.Add(-10*time.Minute))

	if p := runCycle(t, mc, "cycle 1"); len(p.dispatches) != 1 {
		t.Fatalf("cycle 1 dispatched %d, want 1", len(p.dispatches))
	}
	if lc := storedLastCheckAt(t, mc, "PROJ-1"); !lc.Equal(updated) {
		t.Errorf("lastCheck after cycle 1 = %v, want updated %v", lc, updated)
	}
	listed := mc.listCommentsCalls["PROJ-1"]
	if p := runCycle(t, mc, "cycle 2"); len(p.dispatches) != 0 {
		t.Errorf("cycle 2 dispatched %d, want 0", len(p.dispatches))
	}
	if mc.listCommentsCalls["PROJ-1"] != listed {
		t.Error("cycle 2 listed comments of an issue with nothing new")
	}
}

// Jira moves updated for changes that leave no comment or changelog entry
// (a worklog, an attachment, a deleted comment). One look finds nothing,
// moves lastCheck to updated, and the issue goes quiet.
func TestRunUpdatedWithoutEntryLooksOnce(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	updated := now.Add(-time.Minute)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{idleTestIssue(1, now.Add(-48*time.Hour), updated)}
	setLastCheck(mc, "PROJ-1", "acme", "platform", now.Add(-20*time.Minute))
	addTriageComment(mc, "PROJ-1", "501", now.Add(-30*time.Minute))

	if p := runCycle(t, mc, "cycle 1"); len(p.dispatches) != 0 {
		t.Errorf("cycle 1 dispatched %d, want 0", len(p.dispatches))
	}
	if mc.listCommentsCalls["PROJ-1"] != 1 {
		t.Errorf("cycle 1 listed comments %d times, want 1", mc.listCommentsCalls["PROJ-1"])
	}
	if lc := storedLastCheckAt(t, mc, "PROJ-1"); !lc.Equal(updated) {
		t.Errorf("lastCheck after cycle 1 = %v, want updated %v", lc, updated)
	}
	runCycle(t, mc, "cycle 2")
	if mc.listCommentsCalls["PROJ-1"] != 1 {
		t.Error("cycle 2 looked at an issue with nothing new")
	}
}

// The search row can be older than the listings. The entry is handed off
// on the look that finds it; the row then catches up a few milliseconds
// past the entry, which costs one more look with no hand-off, and then the
// issue is idle.
func TestRunSearchRowOlderThanListing(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	commentAt := now.Add(-5 * time.Minute)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{idleTestIssue(1, now.Add(-48*time.Hour), now.Add(-15*time.Minute))}
	setLastCheck(mc, "PROJ-1", "acme", "platform", now.Add(-20*time.Minute))
	addTriageComment(mc, "PROJ-1", "501", commentAt)

	if p := runCycle(t, mc, "cycle 1"); len(p.dispatches) != 1 {
		t.Fatalf("cycle 1 dispatched %d, want 1", len(p.dispatches))
	}
	if lc := storedLastCheckAt(t, mc, "PROJ-1"); !lc.Equal(commentAt) {
		t.Errorf("lastCheck after cycle 1 = %v, want the comment's time %v", lc, commentAt)
	}

	mc.searchResult[0].Fields.Updated = commentAt.Add(3 * time.Millisecond).Format(jiraTimeLayout)
	if p := runCycle(t, mc, "cycle 2"); len(p.dispatches) != 0 {
		t.Errorf("cycle 2 dispatched %d, want 0", len(p.dispatches))
	}
	if mc.listCommentsCalls["PROJ-1"] != 2 {
		t.Errorf("comment listings after cycle 2 = %d, want 2", mc.listCommentsCalls["PROJ-1"])
	}
	runCycle(t, mc, "cycle 3")
	if mc.listCommentsCalls["PROJ-1"] != 2 {
		t.Error("cycle 3 looked at an issue with nothing new")
	}
}

// A search row can lag a new comment (the search is eventually
// consistent). The filter then skips the issue, but the comment is not
// lost: it is handed off once the row catches up.
func TestRunStaleSearchRowDelaysButDoesNotLose(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	lastCheck := now.Add(-20 * time.Minute)
	commentAt := now.Add(-5 * time.Minute)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{idleTestIssue(1, now.Add(-48*time.Hour), lastCheck)}
	setLastCheck(mc, "PROJ-1", "acme", "platform", lastCheck)
	addTriageComment(mc, "PROJ-1", "501", commentAt)

	if p := runCycle(t, mc, "cycle 1"); len(p.dispatches) != 0 || mc.listCommentsCalls["PROJ-1"] != 0 {
		t.Fatalf("cycle 1 on a stale row: dispatched %d, listed %d; want the issue skipped", len(p.dispatches), mc.listCommentsCalls["PROJ-1"])
	}

	mc.searchResult[0].Fields.Updated = commentAt.Add(3 * time.Millisecond).Format(jiraTimeLayout)
	if p := runCycle(t, mc, "cycle 2"); len(p.dispatches) != 1 {
		t.Errorf("cycle 2 after the row caught up dispatched %d, want the comment once", len(p.dispatches))
	}
}

// detectChanges raises maxSeen to the search row's updated value when that
// is later, never lowers it, ignores an unparseable value, and still caps
// the result below fetch start.
func TestDetectChangesMaxSeenReachesUpdated(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	lastCheck := now.Add(-time.Hour)
	commentAt := now.Add(-10 * time.Minute)
	cases := []struct {
		name    string
		updated string
		want    time.Time // zero: capped below fetch start
	}{
		{name: "after", updated: now.Add(-time.Minute).Format(jiraTimeLayout), want: now.Add(-time.Minute)},
		{name: "before", updated: now.Add(-20 * time.Minute).Format(jiraTimeLayout), want: commentAt},
		{name: "equal", updated: commentAt.Format(jiraTimeLayout), want: commentAt},
		{name: "unparseable", updated: "soon", want: commentAt},
		{name: "empty", updated: "", want: commentAt},
		{name: "inside the margin", updated: now.Add(time.Second).Format(jiraTimeLayout)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mc := newMockClient()
			addTriageComment(mc, "PROJ-1", "501", commentAt)
			p := idleTestPoller(mc, t)
			issue := jira.Issue{ID: "10001", Key: "PROJ-1", Fields: jira.IssueFields{Created: now.Add(-48 * time.Hour).Format(jiraTimeLayout), Updated: tc.updated}}
			res, err := p.detectChanges(context.Background(), issue, lastCheck)
			if err != nil {
				t.Fatalf("detectChanges() error: %v", err)
			}
			if tc.want.IsZero() {
				// Raised past the comment to updated, then capped at
				// fetch start minus the 10 s margin.
				if res.maxSeen.Before(now.Add(-10*time.Second)) || !res.maxSeen.Before(now.Add(-5*time.Second)) {
					t.Errorf("maxSeen = %v, want raised to updated and capped 10 s below fetch start (now %v)", res.maxSeen, now)
				}
				return
			}
			if !res.maxSeen.Equal(tc.want) {
				t.Errorf("maxSeen = %v, want %v", res.maxSeen, tc.want)
			}
		})
	}
}
