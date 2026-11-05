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
// an entity id, or whose issue was served without labels, fails validation and
// is skipped, so it must not load the
// project-role roster or resolve group memberships; a roster or group failure
// would otherwise fail the cycle and hold the checkpoint for an event that is
// skipped anyway. That the early check still resolves roles for valid events
// is pinned by TestRunAuth_ListGroupsCachedPerCycle (a group-listed actor is
// dispatched with the role its groups give it).
func TestRun_InvalidEventSkippedWithoutRoster(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		labels  []string
		wantLog string
	}{
		{"non-numeric id", "not-a-number", []string{}, "WARNING: skipping event comment-1 for issue PROJ-123"},
		{"negative id", "-1", []string{}, `WARNING: skipping event comment-1 for issue PROJ-123: normalized event: entity.id must be >= 1 (jira issue id "-1")`},
		{"nil labels", "10042", nil, "WARNING: skipping event comment-1 for issue PROJ-123: normalized event: state.labels is required"},
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
						Labels:   tc.labels,
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
			if err := p.Run(context.Background()); err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			if mc.roleActorsCalls != 0 {
				t.Errorf("GetProjectRoleActors called %d times, want 0", mc.roleActorsCalls)
			}
			// the distinct labels warning names a shape problem, so it is for nil labels only
			if gotLabels := strings.Contains(logBuf.String(), "without a labels array"); gotLabels != (tc.labels == nil) {
				t.Errorf("labels warning logged = %v, want %v:\n%s", gotLabels, tc.labels == nil, logBuf.String())
			}
			if !strings.Contains(logBuf.String(), tc.wantLog) {
				t.Errorf("poll log lacks %q:\n%s", tc.wantLog, logBuf.String())
			}
			if got, err := p.readLastCheck(context.Background(), "PROJ-123"); err != nil || got.IsZero() {
				t.Errorf("PROJ-123 lastCheck = %v (err %v), want advanced past the skipped event", got, err)
			}
		})
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

// TestRun_NilLabelsSkippedWithDistinctWarning: Jira Cloud was observed serving
// an issue without labels with an empty list (checked 2026-11-01), and the
// poller has no labels default. An issue served without a labels array has
// its events skipped like any invalid event, so the other issues are still
// polled and dispatched, and it gets one distinct warning, so it can be told
// from an ordinary invalid event.
func TestRun_NilLabelsSkippedWithDistinctWarning(t *testing.T) {
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
	const distinct = "WARNING: jira served issue PROJ-123 without a labels array (null or missing); its events are skipped"
	if n := strings.Count(logBuf.String(), distinct); n != 1 {
		t.Errorf("distinct labels warning logged %d times, want once:\n%s", n, logBuf.String())
	}
	if strings.Contains(logBuf.String(), "jira served issue PROJ-122 without a labels array") {
		t.Errorf("an issue served with an empty labels list must not get the labels warning:\n%s", logBuf.String())
	}
	valid := 0
	for _, d := range p.dispatches {
		switch d.StatusNumber {
		case "10042":
			t.Errorf("the nil-labels issue PROJ-123 was dispatched: %+v", d)
		case "10041":
			valid++
		}
	}
	if valid == 0 {
		t.Errorf("the valid issue PROJ-122 was not dispatched: %+v", p.dispatches)
	}
	for _, key := range []string{"PROJ-122", "PROJ-123"} {
		if got, err := p.readLastCheck(context.Background(), key); err != nil || got.IsZero() {
			t.Errorf("%s lastCheck = %v (err %v), want advanced", key, got, err)
		}
	}
}
