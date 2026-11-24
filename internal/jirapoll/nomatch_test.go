package jirapoll

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// reportingMatcher is a stubMatcher that also reports its candidates, as
// the CLI's CEL matcher does.
type reportingMatcher struct {
	stubMatcher
	candidates []string
	suppressed bool
}

func (m *reportingMatcher) DispatchCandidates() ([]string, bool) {
	return m.candidates, m.suppressed
}

// discardDispatcher accepts every record, standing in for the GitLab
// output driver.
type discardDispatcher struct{ records []DispatchRecord }

func (d *discardDispatcher) Dispatch(_ context.Context, r DispatchRecord) error {
	d.records = append(d.records, r)
	return nil
}

func captureAnnotations(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := annotationWriter
	annotationWriter = &buf
	t.Cleanup(func() { annotationWriter = prev })
	return &buf
}

// noMatchClient returns a mock client with one issue, PROJ-123, carrying
// a /fs-code comment by a project member.
func noMatchClient(body string) *mockClient {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{"557058:abc123def456": "Developers"}
	mc.searchResult = []jira.Issue{{
		ID:   "10042",
		Key:  "PROJ-123",
		Self: "https://acme.atlassian.net/rest/api/3/issue/10042",
		Fields: jira.IssueFields{
			Summary:  "Test issue",
			Labels:   []string{"bug"},
			Status:   jira.Status{Name: "Open", StatusCategory: jira.StatusCategory{Key: "new"}},
			Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
			Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
			Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
		},
	}}
	mc.comments["PROJ-123"] = []jira.Comment{{
		ID:      "50001",
		Body:    body,
		Created: now.Format("2006-01-02T15:04:05.000-0700"),
		Author:  jira.User{AccountID: "557058:abc123def456", AccountType: "atlassian"},
	}}
	return mc
}

// TestRun_WarnsWhenSelectedWorkMatchesNoAgent covers the per-cycle
// diagnostic for #9503 on both target forges: a cycle that selects an
// issue whose events match no agent writes a ::warning:: naming the issue
// key, the event and the agents considered.
func TestRun_WarnsWhenSelectedWorkMatchesNoAgent(t *testing.T) {
	for _, target := range []string{TargetForgeGitHub, TargetForgeGitLab} {
		t.Run(target, func(t *testing.T) {
			out := captureAnnotations(t)
			captureLog(t)
			opts := Options{
				TargetRepo:  "acme/platform",
				TargetForge: target,
				JiraBaseURL: "https://acme.atlassian.net",
				JiraProject: "PROJ",
			}
			if target == TargetForgeGitLab {
				opts.GitLabURL = "https://gitlab.example.com"
				opts.Dispatcher = &discardDispatcher{}
			} else {
				opts.OutputPath = filepath.Join(t.TempDir(), "dispatches.json")
			}
			m := &reportingMatcher{candidates: []string{"triage", "review"}}
			p := newTestPoller(noMatchClient("/fs-code please"), m, opts)
			if err := p.Run(context.Background()); err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			got := out.String()
			if !strings.HasPrefix(got, "::warning::jira-poll: selected 1 issue(s) but no agent matched") {
				t.Fatalf("expected a no-match warning annotation, got %q", got)
			}
			for _, want := range []string{
				"PROJ-123 (comment_added /fs-code)",
				"Agents considered: triage, review.",
				"agents: in .fullsend/config.yaml",
				"harness trigger matches the event",
				"Actor role resolution",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("warning missing %q: %q", want, got)
				}
			}
			if strings.Count(got, "\n") != 1 {
				t.Errorf("expected a single annotation line, got %q", got)
			}
		})
	}
}

func TestRun_NoMatchWarningSkipped(t *testing.T) {
	tests := map[string]struct {
		matcher EventMatcher
		body    string
	}{
		"an agent matched": {
			matcher: &reportingMatcher{stubMatcher: stubMatcher{agents: []string{"code"}}, candidates: []string{"code"}},
			body:    "/fs-code",
		},
		"kill switch suppresses": {
			matcher: &reportingMatcher{suppressed: true},
			body:    "/fs-code",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			out := captureAnnotations(t)
			captureLog(t)
			p := newTestPoller(noMatchClient(tt.body), tt.matcher, Options{
				TargetRepo:  "acme/platform",
				JiraBaseURL: "https://acme.atlassian.net",
				JiraProject: "PROJ",
				OutputPath:  filepath.Join(t.TempDir(), "dispatches.json"),
			})
			if err := p.Run(context.Background()); err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			if out.Len() != 0 {
				t.Errorf("expected no annotation, got %q", out.String())
			}
		})
	}

	t.Run("no events on the selected issue", func(t *testing.T) {
		out := captureAnnotations(t)
		captureLog(t)
		// lastCheck is newer than all of the issue's activity, so the
		// cycle selects the issue but evaluates no event.
		mc := noMatchClient("/fs-code")
		mc.comments = map[string][]jira.Comment{}
		setLastCheck(mc, "PROJ-123", "acme", "platform", time.Now().Truncate(time.Second))
		p := newTestPoller(mc, &reportingMatcher{}, Options{
			TargetRepo:  "acme/platform",
			JiraBaseURL: "https://acme.atlassian.net",
			JiraProject: "PROJ",
			OutputPath:  filepath.Join(t.TempDir(), "dispatches.json"),
		})
		if err := p.Run(context.Background()); err != nil {
			t.Fatalf("Run() error: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("expected no annotation, got %q", out.String())
		}
	})
}

// A matcher that does not report its candidates still gets the warning,
// without the agents clause.
func TestWarnNoMatch_MatcherWithoutCandidates(t *testing.T) {
	out := captureAnnotations(t)
	p := New(nil, &stubMatcher{}, Options{})
	p.unmatched = []unmatchedEvent{{issueKey: "PROJ-1", event: "label_changed ready-to-code added"}}
	p.warnNoMatch(1)
	got := out.String()
	if !strings.Contains(got, "PROJ-1 (label_changed ready-to-code added)") {
		t.Errorf("warning missing event: %q", got)
	}
	if strings.Contains(got, "Agents considered") {
		t.Errorf("warning names agents without a CandidateReporter: %q", got)
	}
}

func TestWarnNoMatch_SanitizesDynamicStrings(t *testing.T) {
	out := captureAnnotations(t)
	p := New(nil, &reportingMatcher{}, Options{})
	p.unmatched = []unmatchedEvent{{
		issueKey: "PROJ-1",
		event:    "comment_added /fs-x%0A::error::spoofed\n::set-output name=a::b\x1b[31m",
	}}
	p.warnNoMatch(1)
	got := out.String()
	if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Errorf("annotation is not a single line: %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("annotation keeps a control character: %q", got)
	}
	if strings.Contains(got[len("::warning::"):], "::error::") || strings.Contains(got[len("::warning::"):], "::set-output") {
		t.Errorf("annotation embeds a workflow command: %q", got)
	}
	if !strings.Contains(got, "%250A") || !strings.Contains(got, "%0A") {
		t.Errorf("annotation does not encode %% and LF: %q", got)
	}
}

func TestSanitizeAnnotation_NoDoubleColon(t *testing.T) {
	for _, in := range []string{"::", ":::", "::::", ":::::", "a:::error:::b", ":%0A::", "::\n::"} {
		got := sanitizeAnnotation(in)
		if strings.Contains(got, "::") {
			t.Errorf("sanitizeAnnotation(%q) = %q, still contains %q", in, got, "::")
		}
	}
}

func TestWarnNoMatch_BoundsEventList(t *testing.T) {
	out := captureAnnotations(t)
	p := New(nil, &reportingMatcher{}, Options{})
	for i := range maxNoMatchEvents + 3 {
		p.unmatched = append(p.unmatched, unmatchedEvent{issueKey: fmt.Sprintf("PROJ-%d", i+1), event: "comment_added"})
	}
	p.warnNoMatch(5)
	got := out.String()
	if !strings.Contains(got, fmt.Sprintf("PROJ-%d (comment_added)", maxNoMatchEvents)) {
		t.Errorf("warning missing the last listed event: %q", got)
	}
	if strings.Contains(got, fmt.Sprintf("PROJ-%d ", maxNoMatchEvents+1)) {
		t.Errorf("warning lists more than %d events: %q", maxNoMatchEvents, got)
	}
	if !strings.Contains(got, "and 3 more") {
		t.Errorf("warning missing the overflow count: %q", got)
	}
	if !strings.Contains(got, "Agents considered: none.") {
		t.Errorf("warning missing the empty candidate list: %q", got)
	}
}

func TestDescribeEvent(t *testing.T) {
	tests := []struct {
		ne   normevent.Event
		want string
	}{
		{normevent.Event{Transition: normevent.Transition{Kind: normevent.TransitionCommentAdded, Comment: &normevent.Comment{Command: "/fs-triage"}}}, "comment_added /fs-triage"},
		{normevent.Event{Transition: normevent.Transition{Kind: normevent.TransitionCommentAdded, Comment: &normevent.Comment{Body: "hello"}}}, "comment_added"},
		{normevent.Event{Transition: normevent.Transition{Kind: normevent.TransitionLabelChanged, Label: &normevent.LabelChange{Name: "ready-to-code", Action: "added"}}}, "label_changed ready-to-code added"},
		{normevent.Event{Transition: normevent.Transition{Kind: normevent.TransitionOpened}}, "opened"},
	}
	for _, tt := range tests {
		if got := describeEvent(&tt.ne); got != tt.want {
			t.Errorf("describeEvent() = %q, want %q", got, tt.want)
		}
	}
}
