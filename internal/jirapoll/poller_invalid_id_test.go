package jirapoll

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// TestRun_InvalidEventSkippedWithoutRoster: an event whose issue id cannot be
// an entity id fails validation and is skipped, so it must not load the
// project-role roster or resolve group memberships; a roster or group failure
// would otherwise fail the cycle and hold the checkpoint for an event that is
// skipped anyway. With every event of the cycle invalid, Run fails and holds
// the checkpoint (criterion 1 as amended on PR #9191's review), which the
// error asserted below pins. That the early check still resolves roles for
// valid events is pinned by TestRunAuth_ListGroupsCachedPerCycle (a
// group-listed actor is dispatched with the role its groups give it).
func TestRun_InvalidEventSkippedWithoutRoster(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantLog string
	}{
		{"non-numeric id", "not-a-number", "WARNING: skipping event comment-1 for issue PROJ-123"},
		{"negative id", "-1", `WARNING: skipping event comment-1 for issue PROJ-123: normalized event: entity.id must be >= 1 (Jira issue id "-1")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().Truncate(time.Second)
			mc := newMockClient()
			mc.roleErr = fmt.Errorf("jira api: 503 service unavailable")
			mc.searchResult = []jira.Issue{
				{
					ID:  tc.id,
					Key: "PROJ-123",
					Fields: jira.IssueFields{
						Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
						Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
						Labels:   []string{},
					},
				},
			}
			mc.comments["PROJ-123"] = []jira.Comment{
				{
					ID:      "1",
					Body:    "/fs-code fix it",
					Created: now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05.000-0700"),
					Author:  jira.User{AccountID: "u1", AccountType: "atlassian"},
				},
			}

			p := newTestPoller(mc, &stubMatcher{agents: []string{"code"}}, Options{
				TargetRepo:  "acme/platform",
				JiraBaseURL: "https://acme.atlassian.net",
				JiraProject: "PROJ",
			})
			logBuf := captureLog(t)
			if err := p.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "failed normalized-event validation") {
				t.Fatalf("Run() error = %v, want the all-invalid cycle to fail", err)
			}
			if mc.roleActorsCalls != 0 {
				t.Errorf("GetProjectRoleActors called %d times, want 0", mc.roleActorsCalls)
			}
			if !strings.Contains(logBuf.String(), tc.wantLog) {
				t.Errorf("poll log lacks %q:\n%s", tc.wantLog, logBuf.String())
			}
			if got, err := p.readLastCheck(context.Background(), "PROJ-123"); err != nil || !got.IsZero() {
				t.Errorf("PROJ-123 lastCheck = %v (err %v), want held at zero so the cycle retries the events", got, err)
			}
		})
	}
}

// TestRun_MixedInvalidEventsCycleSucceeds: one bad issue among good ones is
// still skipped — the all-invalid guard must not fail a cycle in which any
// event validated. The valid issue dispatches, both checkpoints advance (the
// invalid issue's advances past its skipped events, as criterion 1 states for
// a partial skip), and the cycle summarizes how many events it skipped.
func TestRun_MixedInvalidEventsCycleSucceeds(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{
		{
			ID:  "10041",
			Key: "PROJ-122",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Labels:   []string{},
			},
		},
		{
			ID:  "not-a-number",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Labels:   []string{},
			},
		},
	}
	for _, key := range []string{"PROJ-122", "PROJ-123"} {
		mc.comments[key] = []jira.Comment{
			{
				ID:      "1",
				Body:    "/fs-code fix it",
				Created: now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05.000-0700"),
				Author:  jira.User{AccountID: "u1", AccountType: "atlassian"},
			},
		}
	}

	p := newTestPoller(mc, &stubMatcher{agents: []string{"code"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})
	logBuf := captureLog(t)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v, want a cycle with valid events to succeed", err)
	}
	const summary = "WARNING: skipped 2 of 4 events this cycle because they failed normalized-event validation"
	if !strings.Contains(logBuf.String(), summary) {
		t.Errorf("poll log lacks %q:\n%s", summary, logBuf.String())
	}
	// The positive shape, so the guard can fail: the stub matcher formats
	// StatusNumber from an int id, so an equality check against the valid
	// issue's id is the only form that a dispatched invalid event (whose
	// records would carry "0") trips.
	if len(p.dispatches) != 2 {
		t.Errorf("dispatches = %d record(s), want 2 (both of the valid issue PROJ-122's events): %+v", len(p.dispatches), p.dispatches)
	}
	for _, d := range p.dispatches {
		if d.StatusNumber != "10041" {
			t.Errorf("dispatch StatusNumber = %q, want only the valid issue PROJ-122 (10041): %+v", d.StatusNumber, p.dispatches)
		}
	}
	for _, key := range []string{"PROJ-122", "PROJ-123"} {
		if got, err := p.readLastCheck(context.Background(), key); err != nil || got.IsZero() {
			t.Errorf("%s lastCheck = %v (err %v), want advanced", key, got, err)
		}
	}
}

// rawSearchClient bypasses mockClient.SearchIssues's normalization of nil
// labels to [], so a test can serve an issue the way a Jira-side shape
// change would (labels missing or null) and pin what the poller does then.
type rawSearchClient struct {
	*mockClient
}

func (c *rawSearchClient) SearchIssues(ctx context.Context, jql string, limit int) ([]jira.Issue, error) {
	if c.searchErr != nil {
		return nil, c.searchErr
	}
	return c.searchResult, nil
}

func (c *rawSearchClient) SearchIssuesPage(_ context.Context, jql, nextPageToken string, maxResults int) (*jira.SearchResult, error) {
	if c.searchErr != nil {
		return nil, c.searchErr
	}
	return &jira.SearchResult{
		Issues: c.searchResult,
		IsLast: true,
	}, nil
}

// TestRun_NilLabelsTreatedAsUnlabeled: an issue Jira serves without a labels
// array (null or missing) is in effect unlabeled, so toNormalizedEvent maps
// nil labels to an empty list (criterion 2 as amended on PR #9191's review)
// and its events dispatch like any other issue's, with no event skipped.
// What Jira actually serves is not stated as fact: the
// poller tolerates both shapes instead of depending on one.
func TestRun_NilLabelsTreatedAsUnlabeled(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{
		{
			ID:  "10041",
			Key: "PROJ-122",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Labels:   []string{},
			},
		},
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	for _, key := range []string{"PROJ-122", "PROJ-123"} {
		mc.comments[key] = []jira.Comment{
			{
				ID:      "1",
				Body:    "/fs-code fix it",
				Created: now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05.000-0700"),
				Author:  jira.User{AccountID: "u1", AccountType: "atlassian"},
			},
		}
	}

	p := newTestPoller(&rawSearchClient{mc}, &stubMatcher{agents: []string{"code"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})
	logBuf := captureLog(t)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if strings.Contains(logBuf.String(), "skipping event") {
		t.Errorf("an unlabeled issue's events must not be skipped:\n%s", logBuf.String())
	}
	for _, want := range []string{"10041", "10042"} {
		found := false
		for _, d := range p.dispatches {
			if d.StatusNumber == want {
				found = true
			}
		}
		if !found {
			t.Errorf("issue %s was not dispatched: %+v", want, p.dispatches)
		}
	}
	for _, key := range []string{"PROJ-122", "PROJ-123"} {
		if got, err := p.readLastCheck(context.Background(), key); err != nil || got.IsZero() {
			t.Errorf("%s lastCheck = %v (err %v), want advanced", key, got, err)
		}
	}
}

// errOnStatusMatcher fails Match only for one issue's events, so a test can
// pin that a partially failing matcher drops those events while the cycle
// still succeeds when any event matched.
type errOnStatusMatcher struct {
	stubMatcher
	failStatus string
}

func (m *errOnStatusMatcher) Match(ctx context.Context, event *normevent.Event) ([]DispatchRecord, error) {
	if fmt.Sprintf("%d", event.Entity.ID) == m.failStatus {
		return nil, fmt.Errorf("matcher: trigger evaluation failed")
	}
	return m.stubMatcher.Match(ctx, event)
}

// TestRun_MatcherErrEventsDroppedNotLost: an event whose matcher.Match call
// errors is dropped as silently as one that fails validation, so the
// all-invalid guard counts matcher errors beside validation failures. A
// cycle in which every event failed matching fails and holds the checkpoint
// instead of advancing behind a green Run; a cycle in which any event
// matched succeeds, drops only the failing issue's events and advances both
// checkpoints.
func TestRun_MatcherErrEventsDroppedNotLost(t *testing.T) {
	newClient := func() *mockClient {
		now := time.Now().Truncate(time.Second)
		mc := newMockClient()
		mc.searchResult = []jira.Issue{
			{
				ID:  "10041",
				Key: "PROJ-122",
				Fields: jira.IssueFields{
					Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
					Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
					Labels:   []string{},
				},
			},
			{
				ID:  "10042",
				Key: "PROJ-123",
				Fields: jira.IssueFields{
					Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
					Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
					Labels:   []string{},
				},
			},
		}
		for _, key := range []string{"PROJ-122", "PROJ-123"} {
			mc.comments[key] = []jira.Comment{
				{
					ID:      "1",
					Body:    "/fs-code fix it",
					Created: now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05.000-0700"),
					Author:  jira.User{AccountID: "u1", AccountType: "atlassian"},
				},
			}
		}
		return mc
	}

	t.Run("all events fail matching", func(t *testing.T) {
		p := newTestPoller(newClient(), &stubMatcher{agents: []string{"code"}, err: fmt.Errorf("matcher: trigger evaluation failed")}, Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
			JiraProject: "PROJ",
		})
		logBuf := captureLog(t)
		err := p.Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "failed matching") {
			t.Fatalf("Run() error = %v, want the all-dropped cycle to fail naming the matcher errors", err)
		}
		const summary = "WARNING: dropped 4 of 4 events this cycle because matching them failed"
		if !strings.Contains(logBuf.String(), summary) {
			t.Errorf("poll log lacks %q:\n%s", summary, logBuf.String())
		}
		if len(p.dispatches) != 0 {
			t.Errorf("dispatches = %d record(s), want 0: %+v", len(p.dispatches), p.dispatches)
		}
		for _, key := range []string{"PROJ-122", "PROJ-123"} {
			if got, e := p.readLastCheck(context.Background(), key); e != nil || !got.IsZero() {
				t.Errorf("%s lastCheck = %v (err %v), want held at zero so the cycle retries the events", key, got, e)
			}
		}
	})

	t.Run("one issue's events fail matching", func(t *testing.T) {
		p := newTestPoller(newClient(), &errOnStatusMatcher{stubMatcher: stubMatcher{agents: []string{"code"}}, failStatus: "10042"}, Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
			JiraProject: "PROJ",
		})
		logBuf := captureLog(t)
		if err := p.Run(context.Background()); err != nil {
			t.Fatalf("Run() error: %v, want a cycle with matched events to succeed", err)
		}
		const summary = "WARNING: dropped 2 of 4 events this cycle because matching them failed"
		if !strings.Contains(logBuf.String(), summary) {
			t.Errorf("poll log lacks %q:\n%s", summary, logBuf.String())
		}
		if len(p.dispatches) != 2 {
			t.Fatalf("dispatches = %d record(s), want 2 (both of PROJ-122's events): %+v", len(p.dispatches), p.dispatches)
		}
		for _, d := range p.dispatches {
			if d.StatusNumber == "10042" {
				t.Errorf("the matcher-failed issue dispatched: %+v", p.dispatches)
			}
		}
		for _, key := range []string{"PROJ-122", "PROJ-123"} {
			if got, e := p.readLastCheck(context.Background(), key); e != nil || got.IsZero() {
				t.Errorf("%s lastCheck = %v (err %v), want advanced", key, got, e)
			}
		}
	})

	// The mixed branch of the all-dropped guard: some events fail
	// validation and the rest fail matching, so both counters are
	// non-zero and their sum equals eventsSeen. The combined error must
	// name both counts (the person-confirmed decision and the Jira
	// guide's troubleshooting row promise it), no checkpoint advances
	// and nothing dispatches.
	t.Run("validation and matching failures mix", func(t *testing.T) {
		mc := newClient()
		mc.searchResult[1].ID = "not-a-number" // PROJ-123's events fail validation; PROJ-122's fail matching
		p := newTestPoller(mc, &stubMatcher{agents: []string{"code"}, err: fmt.Errorf("matcher: trigger evaluation failed")}, Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
			JiraProject: "PROJ",
		})
		logBuf := captureLog(t)
		const wantErr = "all 4 events this cycle were dropped before dispatch (2 failed normalized-event validation, 2 failed matching)"
		if err := p.Run(context.Background()); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("Run() error = %v, want the combined all-dropped error %q", err, wantErr)
		}
		const skipped = "WARNING: skipped 2 of 4 events this cycle because they failed normalized-event validation"
		if !strings.Contains(logBuf.String(), skipped) {
			t.Errorf("poll log lacks %q:\n%s", skipped, logBuf.String())
		}
		const dropped = "WARNING: dropped 2 of 4 events this cycle because matching them failed"
		if !strings.Contains(logBuf.String(), dropped) {
			t.Errorf("poll log lacks %q:\n%s", dropped, logBuf.String())
		}
		if len(p.dispatches) != 0 {
			t.Errorf("dispatches = %d record(s), want 0: %+v", len(p.dispatches), p.dispatches)
		}
		for _, key := range []string{"PROJ-122", "PROJ-123"} {
			if got, e := p.readLastCheck(context.Background(), key); e != nil || !got.IsZero() {
				t.Errorf("%s lastCheck = %v (err %v), want held at zero so the cycle retries the events", key, got, e)
			}
		}
	})
}

// TestRun_MalformedKeyEventSkipped: normevent.Validate requires a jira
// entity.key to be a Jira work item key — the shape both dispatch consumers
// already enforce (internal/cli/run_workitem.go and
// internal/poll/jira_dispatch.go), so the Validate gate is defence in depth:
// its events are skipped like any invalid event instead of dispatching and
// failing downstream with the checkpoint held. Jira serves such an issue
// through a custom tracker.jira.poll.query, and on a legacy Server/Data
// Center deployment whose project key is a single character (which the
// config gate's jiraProjectKeyRe accepts) through the scoped default query
// too: the CLI always sets a JQL, so the project-key check in
// searchCandidates (which runs only with no JQL at all) never intercepts
// it. The mock serves the issue regardless of the JQL, as Jira does for
// either query.
func TestRun_MalformedKeyEventSkipped(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{
		{
			ID:  "10041",
			Key: "PROJ-122",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Labels:   []string{},
			},
		},
		{
			ID:  "10050",
			Key: "P-1",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Labels:   []string{},
			},
		},
	}
	for _, key := range []string{"PROJ-122", "P-1"} {
		mc.comments[key] = []jira.Comment{
			{
				ID:      "1",
				Body:    "/fs-code fix it",
				Created: now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05.000-0700"),
				Author:  jira.User{AccountID: "u1", AccountType: "atlassian"},
			},
		}
	}

	p := newTestPoller(mc, &stubMatcher{agents: []string{"code"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})
	logBuf := captureLog(t)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v, want a cycle with a valid issue to succeed", err)
	}
	const wantLog = `WARNING: skipping event comment-1 for issue P-1: normalized event: entity.key "P-1" is not a Jira work item key like PROJ-123`
	if !strings.Contains(logBuf.String(), wantLog) {
		t.Errorf("poll log lacks %q:\n%s", wantLog, logBuf.String())
	}
	for _, d := range p.dispatches {
		if d.StatusNumber == "10050" {
			t.Errorf("the malformed-key issue dispatched: %+v", p.dispatches)
		}
	}
	if len(p.dispatches) != 2 {
		t.Errorf("dispatches = %d record(s), want 2 (both of the valid issue PROJ-122's events): %+v", len(p.dispatches), p.dispatches)
	}
	if got, err := p.readLastCheck(context.Background(), "P-1"); err != nil || got.IsZero() {
		t.Errorf("P-1 lastCheck = %v (err %v), want advanced past the skipped event", got, err)
	}
}

// TestRun_LoneBadEventFailsEveryCycle: the all-invalid guard counts events,
// not issues, so one bad issue alone in a quiet cycle is "every event" — the
// cycle fails and holds the checkpoint, and because no checkpoint advanced,
// the next cycle re-detects the same event and fails again. The repeat is the
// guard's intent (nothing is lost behind a green run) and is bounded: it ends
// when the event ages past the first-poll backfill floor (24h by default) or
// another selected issue produces a valid event. Reviewed on PR #9191
// (thread 4526836669): this pins the one-issue-alone outcome as intended.
func TestRun_LoneBadEventFailsEveryCycle(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{
		{
			ID:  "not-a-number",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Labels:   []string{},
			},
		},
	}
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "1",
			Body:    "/fs-code fix it",
			Created: now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "u1", AccountType: "atlassian"},
		},
	}

	p := newTestPoller(mc, &stubMatcher{agents: []string{"code"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})
	for cycle := 1; cycle <= 2; cycle++ {
		logBuf := captureLog(t)
		if err := p.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "failed normalized-event validation") {
			t.Fatalf("cycle %d: Run() error = %v, want the all-invalid cycle to fail", cycle, err)
		}
		const summary = "WARNING: skipped 2 of 2 events this cycle because they failed normalized-event validation"
		if !strings.Contains(logBuf.String(), summary) {
			t.Errorf("cycle %d: poll log lacks %q:\n%s", cycle, summary, logBuf.String())
		}
		if got, err := p.readLastCheck(context.Background(), "PROJ-123"); err != nil || !got.IsZero() {
			t.Errorf("cycle %d: PROJ-123 lastCheck = %v (err %v), want held at zero so the next cycle re-detects the event", cycle, got, err)
		}
	}
}
