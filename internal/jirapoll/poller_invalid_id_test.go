package jirapoll

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
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

			// rawSearchClient serves the labels as given, nil included.
			p := newTestPoller(&rawSearchClient{mc}, &stubMatcher{agents: []string{"code"}}, Options{
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
	for _, d := range p.dispatches {
		if d.StatusNumber == "not-a-number" {
			t.Errorf("the invalid issue PROJ-123 was dispatched: %+v", d)
		}
	}
	if len(p.dispatches) == 0 {
		t.Errorf("the valid issue PROJ-122 was not dispatched: %+v", p.dispatches)
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

// TestRun_NilLabelsTreatedAsUnlabeled: an issue Jira serves without a labels
// array (null or missing) is in effect unlabeled, so toNormalizedEvent maps
// nil labels to an empty list (criterion 2 as amended on PR #9191's review)
// and its events dispatch like any other issue's, with no labels warning and
// no event skipped. What Jira actually serves is not stated as fact: the
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
	if strings.Contains(logBuf.String(), "skipping event") || strings.Contains(logBuf.String(), "without a labels array") {
		t.Errorf("an unlabeled issue's events must not be skipped or warned about:\n%s", logBuf.String())
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
