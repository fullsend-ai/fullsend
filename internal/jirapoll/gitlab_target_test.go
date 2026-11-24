package jirapoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// recordingDispatcher is a RecordDispatcher that records every dispatched
// record, fails records for the issue keys in failFor, and refuses records
// for the issue keys in refuseFor with dispatch.ErrTransportBudget — the way the
// Jira dispatch door (poll.Poller.DispatchJiraEvent) refuses a record whose
// payload cannot fit the transport budget.
type recordingDispatcher struct {
	failFor    map[string]bool
	refuseFor  map[string]bool
	dispatched []DispatchRecord
}

func (d *recordingDispatcher) Dispatch(_ context.Context, record DispatchRecord) error {
	d.dispatched = append(d.dispatched, record)
	if d.failFor[record.StatusNumber] {
		return errDispatchRejected
	}
	if d.refuseFor[record.StatusNumber] {
		return fmt.Errorf("dispatch %s for %s: %w", record.Agent, record.StatusNumber, dispatch.ErrTransportBudget)
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

// TestRunGitLabTarget_TransportBudgetRefusalAdvancesCheckpoint verifies a
// transport-budget refusal — deterministic, so a retry can never succeed —
// advances the refused issue's checkpoint past the record instead of failing
// the cycle and re-refusing the same record on every poll cycle forever,
// which would wedge the issue's later activity behind it. An ordinary
// dispatch failure still keeps the checkpoint
// (TestRunGitLabTarget_FailedDispatchKeepsCheckpoint).
func TestRunGitLabTarget_TransportBudgetRefusalAdvancesCheckpoint(t *testing.T) {
	mc := newMockClient()
	seedSlashCommandIssues(mc, "PROJ-1", "PROJ-2")
	d := &recordingDispatcher{refuseFor: map[string]bool{"PROJ-2": true}}
	p := newTestPoller(mc, &repoCapturingMatcher{}, gitlabTargetOptions(d))

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v, want a clean cycle: the refusal is permanent, not retried", err)
	}
	if len(d.dispatched) != 2 {
		t.Fatalf("dispatched %d records, want 2", len(d.dispatched))
	}

	ctx := context.Background()
	for _, key := range []string{"PROJ-1", "PROJ-2"} {
		ok, err := p.readLastCheck(ctx, key)
		if err != nil || ok.IsZero() {
			t.Errorf("%s lastCheck = %v (err %v), want advanced past the refused record", key, ok, err)
		}
	}
}

// labelTriggerMatcher dispatches one record only when the event's
// state.labels carry the trigger label — the shape of a CEL trigger keyed
// on state.labels — and records every event it saw, bounded or not.
type labelTriggerMatcher struct {
	trigger string
	matched []string
}

func (m *labelTriggerMatcher) Match(_ context.Context, event *normevent.Event) ([]DispatchRecord, error) {
	for _, label := range event.State.Labels {
		if label == m.trigger {
			m.matched = append(m.matched, event.Entity.Key)
			return []DispatchRecord{{
				Agent:        "triage",
				SourceRepo:   event.Repo,
				EventType:    event.Source.RawType,
				StatusRepo:   event.Repo,
				StatusNumber: event.Entity.Key,
			}}, nil
		}
	}
	return nil, nil
}

// seedLabeledSlashCommandIssues seeds one issue per key with a fresh
// /fs-triage comment from a Developers member, carrying the given labels.
func seedLabeledSlashCommandIssues(mc *mockClient, keys []string, labels map[string][]string) {
	now := time.Now().Truncate(time.Second)
	mc.roleMembership = map[string]string{"dev-account": "Developers"}
	for i, key := range keys {
		mc.searchResult = append(mc.searchResult, jira.Issue{
			ID:  "1000" + string(rune('1'+i)),
			Key: key,
			Fields: jira.IssueFields{
				Labels:   labels[key],
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

// TestRunGitLabTarget_LabelsBoundBeforeTriggerMatch verifies the GitLab
// target bounds state.labels before trigger matching, with the same bound
// the dispatch door applies when encoding the payload: a trigger keyed on
// a label past the 1024-byte JSON bound does not dispatch, so the matcher
// never runs on a label the transported event drops.
func TestRunGitLabTarget_LabelsBoundBeforeTriggerMatch(t *testing.T) {
	mc := newMockClient()
	// Twenty-four forty-character pad labels fill the 1024-byte JSON
	// bound (2 + 24*42 + 23 = 1033 bytes with quotes and commas), so the
	// trigger label after them is past the bound and dropped.
	pad := strings.Repeat("p", 40)
	overLabels := make([]string, 0, 25)
	for i := 0; i < 24; i++ {
		overLabels = append(overLabels, pad)
	}
	overLabels = append(overLabels, "ready-to-code")
	seedLabeledSlashCommandIssues(mc, []string{"PROJ-1", "PROJ-2"}, map[string][]string{
		"PROJ-1": overLabels,
		"PROJ-2": {"ready-to-code"},
	})
	d := &recordingDispatcher{}
	matcher := &labelTriggerMatcher{trigger: "ready-to-code"}
	p := newTestPoller(mc, matcher, gitlabTargetOptions(d))

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(d.dispatched) != 1 || d.dispatched[0].StatusNumber != "PROJ-2" {
		t.Fatalf("dispatched %v, want exactly PROJ-2: a trigger keyed on a label past the labels bound must not match", d.dispatched)
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

// embeddingMatcher returns one triage record per event, with the event
// embedded in the payload as the CEL matcher does.
type embeddingMatcher struct{}

func (embeddingMatcher) Match(_ context.Context, event *normevent.Event) ([]DispatchRecord, error) {
	ne, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]json.RawMessage{"_normalized_event": ne})
	if err != nil {
		return nil, err
	}
	return []DispatchRecord{{Agent: "triage", StatusNumber: event.Entity.Key, EventPayload: payload}}, nil
}

// validatingDispatcher parses and validates the embedded event as the
// GitLab output driver does, and records the events it accepts.
type validatingDispatcher struct {
	events []*normevent.Event
}

func (d *validatingDispatcher) Dispatch(_ context.Context, record DispatchRecord) error {
	var payload struct {
		NormalizedEvent json.RawMessage `json:"_normalized_event"`
	}
	if err := json.Unmarshal([]byte(record.EventPayload), &payload); err != nil {
		return err
	}
	event, err := normevent.ParseJSON(payload.NormalizedEvent)
	if err != nil {
		return err
	}
	d.events = append(d.events, event)
	return nil
}

// TestRunGitLabTarget_InvalidEventSkipped verifies that, with an output
// driver, an event that fails validation is skipped with a warning and its
// issue's checkpoint advances while the valid event dispatches. Before the
// skip, the invalid event failed the driver, held its checkpoint and failed
// the cycle.
func TestRunGitLabTarget_InvalidEventSkipped(t *testing.T) {
	mc := newMockClient()
	seedSlashCommandIssues(mc, "PROJ-1", "PROJ-2")
	mc.searchResult[0].ID = "not-a-number"
	d := &validatingDispatcher{}
	p := newTestPoller(mc, embeddingMatcher{}, gitlabTargetOptions(d))
	logBuf := captureLog(t)

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(d.events) != 1 || d.events[0].Entity.Key != "PROJ-2" {
		t.Fatalf("dispatched %v, want only PROJ-2", d.events)
	}
	if !strings.Contains(logBuf.String(), "WARNING: skipping event comment-50001 for issue PROJ-1") {
		t.Errorf("log = %q, want the skip warning for PROJ-1", logBuf.String())
	}
	for _, key := range []string{"PROJ-1", "PROJ-2"} {
		if got, err := p.readLastCheck(context.Background(), key); err != nil || got.IsZero() {
			t.Errorf("%s lastCheck = %v (err %v), want advanced", key, got, err)
		}
	}
}
