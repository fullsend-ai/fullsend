package jirapoll

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// recordingDispatcher is a RecordDispatcher that records every dispatched
// record and fails records for the issue keys in failFor.
type recordingDispatcher struct {
	failFor    map[string]bool
	dispatched []DispatchRecord
}

func (d *recordingDispatcher) Dispatch(_ context.Context, record DispatchRecord) error {
	d.dispatched = append(d.dispatched, record)
	if d.failFor[record.StatusNumber] {
		return errDispatchRejected
	}
	return nil
}

var errDispatchRejected = errors.New("pipeline rejected")

// repoCapturingMatcher records the target repo of every matched event and
// returns one triage record per event, keyed by the Jira issue key.
type repoCapturingMatcher struct {
	repos []string
}

func (m *repoCapturingMatcher) Match(_ context.Context, event *normevent.Event) ([]DispatchRecord, error) {
	m.repos = append(m.repos, event.Repo)
	return []DispatchRecord{{
		Agent:        "triage",
		SourceRepo:   event.Repo,
		EventType:    event.Source.RawType,
		StatusRepo:   event.Repo,
		StatusNumber: event.Entity.Key,
	}}, nil
}

// seedSlashCommandIssues adds one issue with a fresh /fs-triage comment from
// a Developers member for each key.
func seedSlashCommandIssues(mc *mockClient, keys ...string) {
	now := time.Now().Truncate(time.Second)
	mc.roleMembership = map[string]string{"dev-account": "Developers"}
	for i, key := range keys {
		mc.searchResult = append(mc.searchResult, jira.Issue{
			ID:  "1000" + string(rune('1'+i)),
			Key: key,
			Fields: jira.IssueFields{
				Labels:   []string{},
				Reporter: jira.User{AccountID: "reporter-id"},
				// Created before the first-poll backfill window, so only
				// the comment (not an "opened" event) is detected.
				Created: now.Add(-48 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
			},
		})
		mc.comments[key] = []jira.Comment{{
			ID:      "5000" + string(rune('1'+i)),
			Body:    "/fs-triage",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "dev-account", AccountType: "atlassian"},
		}}
	}
}

func gitlabTargetOptions(d RecordDispatcher) Options {
	return Options{
		TargetRepo:  "group/sub/project",
		TargetForge: TargetForgeGitLab,
		GitLabURL:   "https://gitlab.example.com",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		Dispatcher:  d,
	}
}

func TestRunGitLabTarget_DispatchesAndCommitsCheckpoints(t *testing.T) {
	mc := newMockClient()
	seedSlashCommandIssues(mc, "PROJ-1", "PROJ-2")
	matcher := &repoCapturingMatcher{}
	d := &recordingDispatcher{}
	p := newTestPoller(mc, matcher, gitlabTargetOptions(d))

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(d.dispatched) != 2 {
		t.Fatalf("dispatched %d records, want 2", len(d.dispatched))
	}
	for _, repo := range matcher.repos {
		if repo != "group/sub/project" {
			t.Errorf("normalized event repo = %q, want the full GitLab project path", repo)
		}
	}

	lastCheckKey := "fullsend.poll.gitlab.gitlab%2Eexample%2Ecom.group/sub.project.lastCheck"
	for _, key := range []string{"PROJ-1", "PROJ-2"} {
		if _, ok := mc.properties[key][lastCheckKey]; !ok {
			t.Errorf("%s: lastCheck not committed under %q; properties: %v", key, lastCheckKey, mc.properties[key])
		}
		if _, ok := mc.properties[key][lastCheckPropertyKey("group/sub", "project")]; ok {
			t.Errorf("%s: lastCheck written to the GitHub namespace", key)
		}
	}
}

// TestRunGitLabTarget_FailedDispatchKeepsCheckpoint verifies a failed
// pipeline dispatch fails the cycle and leaves only that issue's checkpoint
// untouched, so the next cycle retries it, while other issues advance.
func TestRunGitLabTarget_FailedDispatchKeepsCheckpoint(t *testing.T) {
	mc := newMockClient()
	seedSlashCommandIssues(mc, "PROJ-1", "PROJ-2")
	d := &recordingDispatcher{failFor: map[string]bool{"PROJ-2": true}}
	p := newTestPoller(mc, &repoCapturingMatcher{}, gitlabTargetOptions(d))

	err := p.Run(context.Background())
	if err == nil || !errors.Is(err, errDispatchRejected) || !strings.Contains(err.Error(), "retried next cycle") {
		t.Fatalf("Run() error = %v, want the wrapped dispatch failure", err)
	}

	ctx := context.Background()
	ok, err := p.readLastCheck(ctx, "PROJ-1")
	if err != nil || ok.IsZero() {
		t.Errorf("PROJ-1 lastCheck = %v (err %v), want advanced", ok, err)
	}
	failed, err := p.readLastCheck(ctx, "PROJ-2")
	if err != nil || !failed.IsZero() {
		t.Errorf("PROJ-2 lastCheck = %v (err %v), want unset after a failed dispatch", failed, err)
	}
}

func TestPropertyKeys_GitLabTargetNamespace(t *testing.T) {
	github := New(nil, nil, Options{TargetRepo: "group/project"})
	gitlab := New(nil, nil, Options{TargetRepo: "group/project", TargetForge: TargetForgeGitLab, GitLabURL: "https://gitlab.com"})
	if github.lockKey() != "fullsend.poll.group.project.lock" || github.lastCheckKey() != "fullsend.poll.group.project.lastCheck" {
		t.Errorf("GitHub keys changed: %q, %q", github.lockKey(), github.lastCheckKey())
	}
	if gitlab.lockKey() == github.lockKey() || gitlab.lastCheckKey() == github.lastCheckKey() {
		t.Error("GitLab and GitHub targets with the same path share coordination keys")
	}
	if got := gitlab.lockKey(); got != "fullsend.poll.gitlab.gitlab%2Ecom.group.project.lock" {
		t.Errorf("GitLab lock key = %q", got)
	}

	selfManaged := New(nil, nil, Options{TargetRepo: "group/project", TargetForge: TargetForgeGitLab, GitLabURL: "https://GitLab.Example.com/gitlab/"})
	if got := selfManaged.lastCheckKey(); got != "fullsend.poll.gitlab.gitlab%2Eexample%2Ecom/gitlab.group.project.lastCheck" {
		t.Errorf("self-managed lastCheck key = %q, want the lowercased host and path prefix", got)
	}
	if selfManaged.lockKey() == gitlab.lockKey() {
		t.Error("the same project path on two GitLab instances shares a lock key")
	}

	nested := New(nil, nil, Options{TargetRepo: "a.b/c", TargetForge: TargetForgeGitLab, GitLabURL: "https://gitlab.com"})
	dotted := New(nil, nil, Options{TargetRepo: "a/b.c", TargetForge: TargetForgeGitLab, GitLabURL: "https://gitlab.com"})
	if nested.lockKey() == dotted.lockKey() {
		t.Errorf("dotted GitLab paths collide on %q", nested.lockKey())
	}
}

func TestGitLabInstanceSegment_Unparseable(t *testing.T) {
	if got := gitlabInstanceSegment("not a url/"); got != "not a url" {
		t.Errorf("gitlabInstanceSegment = %q", got)
	}
}
