package jirapoll

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// newTestPoller creates a Poller with a no-op sleep for fast tests.
func newTestPoller(client JiraClient, matcher EventMatcher, opts Options) *Poller {
	p := New(client, matcher, opts)
	p.sleepFn = func(_ context.Context, _ time.Duration) {} // skip jitter in tests
	return p
}

// mockClient implements JiraClient with configurable return values.
type mockClient struct {
	mu sync.Mutex

	searchResult []jira.Issue
	searchErr    error
	lastQuery    string
	lastLimit    int

	issues   map[string]*jira.Issue
	issueErr map[string]error

	comments   map[string][]jira.Comment
	commentErr map[string]error

	changelog    map[string][]jira.ChangelogEntry
	changelogErr map[string]error

	properties     map[string]map[string]json.RawMessage // issueKey -> propertyKey -> value
	propertyGetErr map[string]error                      // propertyKey -> error
	propertySetErr map[string]error                      // propertyKey -> error

	myselfUser *jira.User
	myselfErr  error

	statuses  map[string]jira.Status // status name -> status (with category)
	statusErr map[string]error       // status name -> error

	roleMembership map[string]string // accountID -> role name
	roleErr        error

	roleActors    map[string]jira.ProjectRoleActors // explicit role actors
	roleActorsErr error

	userGroups     map[string][]jira.UserGroupInfo // accountID -> groups
	userGroupsErr  error                           // global error
	userGroupsErrs map[string]error                // per-actor error (overrides global)

	// getPropertyHook, if set, runs after each GetEntityProperty call
	// captures its return value, and before that value is returned. Used
	// to simulate a concurrent writer changing the stored property
	// between reads.
	getPropertyHook func(issueKey, propKey string)
}

func newMockClient() *mockClient {
	return &mockClient{
		issues:         make(map[string]*jira.Issue),
		issueErr:       make(map[string]error),
		comments:       make(map[string][]jira.Comment),
		commentErr:     make(map[string]error),
		changelog:      make(map[string][]jira.ChangelogEntry),
		changelogErr:   make(map[string]error),
		properties:     make(map[string]map[string]json.RawMessage),
		propertyGetErr: make(map[string]error),
		propertySetErr: make(map[string]error),
		statuses:       make(map[string]jira.Status),
		statusErr:      make(map[string]error),
	}
}

func (m *mockClient) SearchIssues(_ context.Context, jql string, limit int) ([]jira.Issue, error) {
	m.lastQuery = jql
	m.lastLimit = limit
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	if limit > 0 && len(m.searchResult) > limit {
		return m.searchResult[:limit], nil
	}
	return m.searchResult, nil
}

func (m *mockClient) GetIssue(_ context.Context, key string) (*jira.Issue, error) {
	if err, ok := m.issueErr[key]; ok && err != nil {
		return nil, err
	}
	issue, ok := m.issues[key]
	if !ok {
		return nil, nil
	}
	return issue, nil
}

func (m *mockClient) ListComments(_ context.Context, key string) ([]jira.Comment, error) {
	if err, ok := m.commentErr[key]; ok && err != nil {
		return nil, err
	}
	return m.comments[key], nil
}

func (m *mockClient) ListChangelog(_ context.Context, key string) ([]jira.ChangelogEntry, error) {
	if err, ok := m.changelogErr[key]; ok && err != nil {
		return nil, err
	}
	return m.changelog[key], nil
}

func (m *mockClient) GetEntityProperty(_ context.Context, issueKey, propKey string) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err, ok := m.propertyGetErr[propKey]; ok && err != nil {
		return nil, err
	}
	props, ok := m.properties[issueKey]
	if !ok {
		// Match real Jira behavior: 404 when property doesn't exist.
		return nil, fmt.Errorf("property %s not found on %s: %w", propKey, issueKey, forge.ErrNotFound)
	}
	val, ok := props[propKey]
	if !ok {
		return nil, fmt.Errorf("property %s not found on %s: %w", propKey, issueKey, forge.ErrNotFound)
	}
	if m.getPropertyHook != nil {
		m.getPropertyHook(issueKey, propKey)
	}
	return val, nil
}

func (m *mockClient) SetEntityProperty(_ context.Context, issueKey, propKey string, value any) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err, ok := m.propertySetErr[propKey]; ok && err != nil {
		return err
	}
	if m.properties[issueKey] == nil {
		m.properties[issueKey] = make(map[string]json.RawMessage)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	m.properties[issueKey][propKey] = data
	return nil
}

func (m *mockClient) DeleteEntityProperty(_ context.Context, issueKey, propKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if props, ok := m.properties[issueKey]; ok {
		delete(props, propKey)
	}
	return nil
}

func (m *mockClient) GetMyself(_ context.Context) (*jira.User, error) {
	if m.myselfErr != nil {
		return nil, m.myselfErr
	}
	return m.myselfUser, nil
}

func (m *mockClient) GetStatus(_ context.Context, idOrName string) (*jira.Status, error) {
	if err, ok := m.statusErr[idOrName]; ok && err != nil {
		return nil, err
	}
	status, ok := m.statuses[idOrName]
	if !ok {
		return nil, fmt.Errorf("status %s not found: %w", idOrName, forge.ErrNotFound)
	}
	return &status, nil
}

func (m *mockClient) GetProjectRoleActors(_ context.Context, _ string) (map[string]jira.ProjectRoleActors, error) {
	if m.roleActorsErr != nil {
		return nil, m.roleActorsErr
	}
	if m.roleErr != nil {
		return nil, m.roleErr
	}
	if m.roleActors != nil {
		return m.roleActors, nil
	}
	// Build from roleMembership for backward compat: existing tests set
	// roleMembership with direct user→role entries, so we convert them
	// into ProjectRoleActors with DirectUsers only (no groups).
	result := make(map[string]jira.ProjectRoleActors)
	for aid, roleName := range m.roleMembership {
		ra, ok := result[roleName]
		if !ok {
			ra = jira.ProjectRoleActors{DirectUsers: make(map[string]bool)}
		}
		ra.DirectUsers[aid] = true
		result[roleName] = ra
	}
	return result, nil
}

func (m *mockClient) GetUserGroups(_ context.Context, accountID string) ([]jira.UserGroupInfo, error) {
	if err, ok := m.userGroupsErrs[accountID]; ok {
		return nil, err
	}
	if m.userGroupsErr != nil {
		return nil, m.userGroupsErr
	}
	if m.userGroups != nil {
		return m.userGroups[accountID], nil
	}
	return nil, nil
}

// stubMatcher implements EventMatcher for testing. It returns a
// DispatchRecord per agent name for every event, or the configured error.
type stubMatcher struct {
	agents []string
	err    error
}

func (m *stubMatcher) Match(_ context.Context, event *normevent.Event) ([]DispatchRecord, error) {
	if m.err != nil {
		return nil, m.err
	}
	var records []DispatchRecord
	for _, agent := range m.agents {
		records = append(records, DispatchRecord{
			Agent:        agent,
			Role:         agent,
			SourceRepo:   event.Repo,
			EventType:    event.Source.RawType,
			StatusRepo:   event.Repo,
			StatusNumber: fmt.Sprintf("%d", event.Entity.ID),
		})
	}
	return records, nil
}

func TestNew(t *testing.T) {
	mc := newMockClient()
	p := New(mc, nil, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})
	if p.opts.M != 50 {
		t.Errorf("M = %d, want default 50", p.opts.M)
	}
	if p.opts.N != 5 {
		t.Errorf("N = %d, want default 5", p.opts.N)
	}
	if p.opts.StaleThreshold != 900*time.Second {
		t.Errorf("StaleThreshold = %v, want default 900s", p.opts.StaleThreshold)
	}
}

func TestRunEmptyPoll(t *testing.T) {
	mc := newMockClient()

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "dispatches.json")

	p := newTestPoller(mc, nil, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  outputPath,
	})

	err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "[]\n" {
		t.Errorf("output = %q, want empty JSON array", string(data))
	}
}

func TestRunHappyPath_CommentWithSlashCommand(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{
		"557058:abc123def456": "Developers",
	}
	mc.searchResult = []jira.Issue{
		{
			ID:   "10042",
			Key:  "PROJ-123",
			Self: "https://acme.atlassian.net/rest/api/3/issue/10042",
			Fields: jira.IssueFields{
				Summary: "Test issue",
				Labels:  []string{"needs-info", "bug"},
				Status: jira.Status{
					Name:           "Open",
					StatusCategory: jira.StatusCategory{Key: "new"},
				},
				Reporter: jira.User{
					AccountID:   "reporter-id",
					AccountType: "atlassian",
				},
				Created: now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated: now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "50001",
			Body:    "/fs-triage check acceptance criteria",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author: jira.User{
				AccountID:   "557058:abc123def456",
				AccountType: "atlassian",
			},
		},
	}

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "dispatches.json")

	router := &stubMatcher{agents: []string{"triage"}}
	p := newTestPoller(mc, router, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  outputPath,
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var dispatches []DispatchRecord
	if err := json.Unmarshal(data, &dispatches); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	found := false
	for _, d := range dispatches {
		if d.Agent == "triage" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected triage dispatch, got dispatches: %+v", dispatches)
	}
}

// TestRunDispatchWriteFailure_NoCheckpointCommitted is a regression test:
// checkpoints must not advance in Jira until the dispatch file has been
// durably written. Previously each issue's lastCheck advanced inline
// during processing, before the dispatch file write in Step 5 — a local
// write failure after that point meant every event this cycle found was
// checkpointed past with no record of it ever written anywhere.
func TestRunDispatchWriteFailure_NoCheckpointCommitted(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{
		"557058:abc123def456": "Developers",
	}
	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "50001",
			Body:    "/fs-triage check acceptance criteria",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "557058:abc123def456", AccountType: "atlassian"},
		},
	}

	router := &stubMatcher{agents: []string{"triage"}}
	p := newTestPoller(mc, router, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		// A path under a nonexistent directory: os.WriteFile fails.
		OutputPath: filepath.Join(t.TempDir(), "no-such-dir", "dispatches.json"),
	})

	if err := p.Run(context.Background()); err == nil {
		t.Fatal("expected Run() to return an error when the dispatch file write fails")
	}

	lastCheck, err := p.readLastCheck(context.Background(), "PROJ-123")
	if err != nil {
		t.Fatalf("readLastCheck() error: %v", err)
	}
	if !lastCheck.IsZero() {
		t.Errorf("expected lastCheck to remain unset after a failed dispatch write, got %v", lastCheck)
	}
}

// TestRunCheckpointRespectsSafetyMargin is a regression test: processIssue
// previously lifted the checkpoint to each dispatched event's timestamp,
// which could only ever fire when detectChanges' cross-fetch clamp had
// lowered maxSeen — silently undoing the clamp and re-opening permanent
// loss of an entry created between the comment and changelog fetches. The
// committed checkpoint must stay at or below fetch-start minus the margin
// even when an in-margin event dispatches.
func TestRunCheckpointRespectsSafetyMargin(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	commentTime := now.Add(-2 * time.Second) // inside the 10s safety margin
	mc := newMockClient()
	mc.roleMembership = map[string]string{"u1": "Developers"}
	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "1",
			Body:    "/fs-triage recent",
			Created: commentTime.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "u1", AccountType: "atlassian"},
		},
	}

	p := newTestPoller(mc, &stubMatcher{agents: []string{"triage"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  filepath.Join(t.TempDir(), "dispatches.json"),
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(p.dispatches) == 0 {
		t.Fatal("expected the in-margin comment to dispatch")
	}

	lastCheck, err := p.readLastCheck(context.Background(), "PROJ-123")
	if err != nil {
		t.Fatalf("readLastCheck() error: %v", err)
	}
	if !lastCheck.IsZero() && !lastCheck.Before(commentTime) {
		t.Errorf("committed lastCheck %v must stay below the in-margin comment time %v (safety margin defeated)", lastCheck, commentTime)
	}
}

// TestProcessIssue_CheckpointNeverRegresses: when the safety-margin clamp
// pushes the candidate checkpoint at or below the stored lastCheck (two
// cycles within the margin of each other), the advance must be skipped
// entirely rather than moving lastCheck backwards.
func TestProcessIssue_CheckpointNeverRegresses(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	storedLastCheck := now.Add(-5 * time.Second)
	commentTime := now.Add(-2 * time.Second) // after lastCheck, inside margin
	mc := newMockClient()
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "1",
			Body:    "/fs-triage again",
			Created: commentTime.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "u1", AccountType: "atlassian"},
		},
	}

	p := newTestPoller(mc, &stubMatcher{agents: []string{"triage"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})
	if err := p.advanceLastCheck(context.Background(), "PROJ-123", storedLastCheck); err != nil {
		t.Fatalf("seed lastCheck: %v", err)
	}

	issue := jira.Issue{
		ID:  "10042",
		Key: "PROJ-123",
		Fields: jira.IssueFields{
			Reporter: jira.User{AccountID: "reporter-id"},
			Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
		},
	}
	checkpoint, err := p.processIssue(context.Background(), issue, "cycle-1")
	if err != nil {
		t.Fatalf("processIssue() error: %v", err)
	}
	// The clamp puts maxSeen ~10s before now, which is before the stored
	// lastCheck — the returned checkpoint must be zero (skip advance), not
	// an earlier-than-stored value.
	if !checkpoint.IsZero() && checkpoint.Before(storedLastCheck) {
		t.Errorf("checkpoint %v regresses behind stored lastCheck %v; expected zero (skip)", checkpoint, storedLastCheck)
	}
	if len(p.dispatches) == 0 {
		t.Error("expected the comment after lastCheck to still dispatch")
	}
}

// TestRunRoleLoadFailure_FailsCycle: a role-membership load failure must
// fail the cycle instead of degrading to an empty map. With an empty map
// every actor resolves to external, write-gated events route to nothing,
// and the checkpoint would advance past them — permanently dropping real
// events over a transient roles-API error.
func TestRunRoleLoadFailure_FailsCycle(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleErr = fmt.Errorf("jira api: 503 service unavailable")
	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
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

	if err := p.Run(context.Background()); err == nil {
		t.Fatal("expected Run() to fail when role membership cannot be loaded")
	}
	lastCheck, err := p.readLastCheck(context.Background(), "PROJ-123")
	if err != nil {
		t.Fatalf("readLastCheck() error: %v", err)
	}
	if !lastCheck.IsZero() {
		t.Errorf("expected lastCheck to remain unset after a failed role load, got %v", lastCheck)
	}
}

// TestDetectChanges_EditedCommentAttributedToEditor: a comment edited by
// someone other than its author must be attributed to the EDITOR, not the
// original author — otherwise attacker-injected slash-command text runs
// under the author's (possibly privileged) role.
func TestDetectChanges_EditedCommentAttributedToEditor(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	lastCheck := now.Add(-1 * time.Hour)
	created := now.Add(-48 * time.Hour)
	updated := now.Add(-10 * time.Minute)
	mc := newMockClient()
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:           "1",
			Body:         "/fs-code injected instruction",
			Created:      created.Format("2006-01-02T15:04:05.000-0700"),
			Updated:      updated.Format("2006-01-02T15:04:05.000-0700"),
			Author:       jira.User{AccountID: "admin-victim", AccountType: "atlassian"},
			UpdateAuthor: jira.User{AccountID: "attacker-editor", AccountType: "atlassian"},
		},
	}

	p := New(mc, nil, Options{TargetRepo: "acme/platform", JiraBaseURL: "https://acme.atlassian.net", JiraProject: "PROJ"})
	issue := jira.Issue{
		ID:  "10042",
		Key: "PROJ-123",
		Fields: jira.IssueFields{
			Reporter: jira.User{AccountID: "reporter-id"},
			Created:  created.Format("2006-01-02T15:04:05.000-0700"),
		},
	}
	result, err := p.detectChanges(context.Background(), issue, lastCheck)
	if err != nil {
		t.Fatalf("detectChanges() error: %v", err)
	}
	var found bool
	for _, e := range result.events {
		if e.Type == "comment_added" && e.CommentID == "1" {
			found = true
			if e.CommentAuthor.AccountID != "attacker-editor" {
				t.Errorf("edited comment attributed to %q, want the editor %q", e.CommentAuthor.AccountID, "attacker-editor")
			}
		}
	}
	if !found {
		t.Fatal("expected the edited comment to surface")
	}
}

// TestReadLastCheck_ClampsUntrustedValues: lastCheck is attacker-writable,
// so a future value is treated as unset (bounded first-poll path) and a
// value older than the backfill window is floored.
func TestReadLastCheck_ClampsUntrustedValues(t *testing.T) {
	now := time.Now()
	mc := newMockClient()
	p := New(mc, nil, Options{TargetRepo: "acme/platform", JiraBaseURL: "https://acme.atlassian.net"})

	// Future value → treated as unset (zero).
	if err := p.advanceLastCheck(context.Background(), "PROJ-1", now.Add(48*time.Hour)); err != nil {
		t.Fatalf("seed future lastCheck: %v", err)
	}
	got, err := p.readLastCheck(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("readLastCheck() error: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("future lastCheck = %v, want zero (treated as unset)", got)
	}

	// Ancient rewind → floored at now - backfill window (default 24h).
	if err := p.advanceLastCheck(context.Background(), "PROJ-2", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed ancient lastCheck: %v", err)
	}
	got, err = p.readLastCheck(context.Background(), "PROJ-2")
	if err != nil {
		t.Fatalf("readLastCheck() error: %v", err)
	}
	floor := now.Add(-24 * time.Hour)
	if got.Before(floor.Add(-time.Minute)) {
		t.Errorf("ancient lastCheck = %v, want clamped to ~%v (backfill floor)", got, floor)
	}
}

// TestIsLockStale_FutureTimestampIsStale: a future-dated lock (clock skew
// beyond tolerance or a tampered value) must be reclaimable, not treated
// as forever-fresh.
func TestIsLockStale_FutureTimestampIsStale(t *testing.T) {
	threshold := 900 * time.Second
	future := LockValue{ID: "x", TS: time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)}
	if !isLockStale(future, threshold) {
		t.Error("a far-future lock timestamp must be treated as stale/reclaimable")
	}
	// A small future skew (within threshold) is not stale.
	skew := LockValue{ID: "x", TS: time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339)}
	if isLockStale(skew, threshold) {
		t.Error("a small clock-skew-sized future offset must not be treated as stale")
	}
}

// TestPropertyKey_DottedSlugsDoNotCollide: distinct target repos with dots
// must not collapse to the same lock/lastCheck property key.
func TestPropertyKey_DottedSlugsDoNotCollide(t *testing.T) {
	o1, r1 := splitOwnerRepo("a.b/c")
	o2, r2 := splitOwnerRepo("a/b.c")
	if lockPropertyKey(o1, r1) == lockPropertyKey(o2, r2) {
		t.Errorf("dotted slugs a.b/c and a/b.c collide on lock key %q", lockPropertyKey(o1, r1))
	}
	// Common dot-free slug keeps the readable, documented format.
	if got := lockPropertyKey(splitOwnerRepo("acme/platform")); got != "fullsend.poll.acme.platform.lock" {
		t.Errorf("dot-free key = %q, want the documented fullsend.poll.acme.platform.lock", got)
	}
}

// TestProcessIssue_CapsEventsPerIssue: a single issue producing more than
// maxEventsPerIssue routable events truncates dispatch to the cap.
func TestProcessIssue_CapsEventsPerIssue(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	lastCheck := now.Add(-1 * time.Hour)
	mc := newMockClient()
	var comments []jira.Comment
	for i := 0; i < maxEventsPerIssue+25; i++ {
		comments = append(comments, jira.Comment{
			ID:      fmt.Sprintf("%d", i),
			Body:    "/fs-triage go",
			Created: now.Add(-time.Duration(i) * time.Second).Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "u1", AccountType: "atlassian"},
		})
	}
	mc.comments["PROJ-123"] = comments
	mc.roleMembership = map[string]string{"u1": "Developers"}

	p := newTestPoller(mc, &stubMatcher{agents: []string{"triage"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})
	if _, err := p.processIssue(context.Background(), jira.Issue{
		ID:  "10042",
		Key: "PROJ-123",
		Fields: jira.IssueFields{
			Reporter: jira.User{AccountID: "reporter-id"},
			Created:  now.Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
		},
	}, "cycle-1"); err != nil {
		// seed a lastCheck so it's not a first poll
		_ = lastCheck
		t.Fatalf("processIssue() error: %v", err)
	}
	if len(p.dispatches) > maxEventsPerIssue {
		t.Errorf("dispatched %d records, want capped at %d", len(p.dispatches), maxEventsPerIssue)
	}
}

func TestRunLabelChange(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{
		"user1": "Developers",
	}
	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Labels: []string{"ready-to-code"},
				Status: jira.Status{
					Name:           "Open",
					StatusCategory: jira.StatusCategory{Key: "new"},
				},
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	mc.changelog["PROJ-123"] = []jira.ChangelogEntry{
		{
			ID:      "100",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author: jira.User{
				AccountID:   "user1",
				AccountType: "atlassian",
			},
			Items: []jira.ChangeItem{
				{
					Field:      "labels",
					FromString: "",
					ToString:   "ready-to-code",
				},
			},
		},
	}

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "dispatches.json")

	router := &stubMatcher{agents: []string{"code"}}
	p := newTestPoller(mc, router, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  outputPath,
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var dispatches []DispatchRecord
	if err := json.Unmarshal(data, &dispatches); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	found := false
	for _, d := range dispatches {
		if d.Agent == "code" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected code dispatch from label change, got: %+v", dispatches)
	}
}

func TestRunBotFiltering(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Labels: []string{"bug"},
				Status: jira.Status{
					Name:           "Open",
					StatusCategory: jira.StatusCategory{Key: "new"},
				},
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	setLastCheck(mc, "PROJ-123", "acme", "platform", now.Add(-30*time.Minute))

	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "50001",
			Body:    "/fs-triage handle this",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author: jira.User{
				AccountID:   "bot-account",
				AccountType: "app",
			},
		},
	}

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "dispatches.json")

	router := &stubMatcher{agents: []string{"triage"}}
	p := newTestPoller(mc, router, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  outputPath,
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var dispatches []DispatchRecord
	if err := json.Unmarshal(data, &dispatches); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(dispatches) != 0 {
		t.Errorf("expected 0 dispatches (bot filtered), got %d", len(dispatches))
	}
}

// TestAttemptLock_LiveLockRejectedWithoutWriting is a regression test:
// attemptLock previously wrote its own lock unconditionally before ever
// checking for an existing one, so a poller reaching attemptLock well after
// filterLocked's read (e.g. behind other candidates in the same cycle)
// could overwrite an active holder's lock outright and both would proceed
// to dispatch. attemptLock must now check for a live lock first and bail
// out without writing.
func TestAttemptLock_LiveLockRejectedWithoutWriting(t *testing.T) {
	mc := newMockClient()
	holder := LockValue{ID: "holder-cycle", TS: time.Now().UTC().Format(time.RFC3339), Phase: "pending"}
	holderJSON, _ := json.Marshal(holder)
	mc.properties["PROJ-123"] = map[string]json.RawMessage{
		"fullsend.poll.acme.platform.lock": holderJSON,
	}

	p := newTestPoller(mc, nil, Options{TargetRepo: "acme/platform", JiraBaseURL: "https://acme.atlassian.net"})

	acquired, err := p.attemptLock(context.Background(), "PROJ-123", "late-arriver-cycle")
	if err != nil {
		t.Fatalf("attemptLock() error: %v", err)
	}
	if acquired {
		t.Error("expected attemptLock to reject a live lock held by another cycle")
	}

	// The holder's lock must be untouched — attemptLock must not have
	// written over it before checking.
	raw := mc.properties["PROJ-123"]["fullsend.poll.acme.platform.lock"]
	var current LockValue
	if err := json.Unmarshal(raw, &current); err != nil {
		t.Fatalf("unmarshal stored lock: %v", err)
	}
	if current.ID != "holder-cycle" {
		t.Errorf("expected the original holder's lock (ID %q) to remain, got ID %q", "holder-cycle", current.ID)
	}
}

func TestRunLockContention(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Labels:   []string{"bug"},
				Reporter: jira.User{AccountID: "reporter-id"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}

	lockPropKey := lockPropertyKey("acme", "platform")
	lockVal := LockValue{
		ID:    "other-poller-uuid",
		TS:    now.Format(time.RFC3339),
		Phase: "running",
	}
	lockData, _ := json.Marshal(lockVal)
	mc.properties["PROJ-123"] = map[string]json.RawMessage{
		lockPropKey: lockData,
	}

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "dispatches.json")

	router := &stubMatcher{agents: []string{"triage"}}
	p := newTestPoller(mc, router, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  outputPath,
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "[]\n" {
		t.Errorf("expected empty dispatches (locked), got %q", string(data))
	}
}

func TestRunStaleLock(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{
		"user1": "Developers",
	}
	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Labels:   []string{"bug"},
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}

	lockPropKey := lockPropertyKey("acme", "platform")
	lockVal := LockValue{
		ID:    "old-poller-uuid",
		TS:    now.Add(-2 * time.Hour).Format(time.RFC3339),
		Phase: "running",
	}
	lockData, _ := json.Marshal(lockVal)
	mc.properties["PROJ-123"] = map[string]json.RawMessage{
		lockPropKey: lockData,
	}

	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "50001",
			Body:    "/fs-triage handle this",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author: jira.User{
				AccountID:   "user1",
				AccountType: "atlassian",
			},
		},
	}

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "dispatches.json")

	router := &stubMatcher{agents: []string{"triage"}}
	p := newTestPoller(mc, router, Options{
		TargetRepo:     "acme/platform",
		JiraBaseURL:    "https://acme.atlassian.net",
		JiraProject:    "PROJ",
		OutputPath:     outputPath,
		StaleThreshold: 15 * time.Minute,
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var dispatches []DispatchRecord
	if err := json.Unmarshal(data, &dispatches); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(dispatches) == 0 {
		t.Error("expected dispatches after stale lock cleanup")
	}
}

// TestFilterLockedStaleCleanupPreservesConcurrentFreshLock guards against a
// regression where stale-lock cleanup released the lock property
// unconditionally (expectedID == "") instead of using the stale lock's own
// ID, so a fresh lock written by a different poller in the race window
// between the read and the release got deleted without any ownership check.
func TestFilterLockedStaleCleanupPreservesConcurrentFreshLock(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, nil, Options{
		TargetRepo:     "acme/platform",
		StaleThreshold: 15 * time.Minute,
	})

	propKey := lockPropertyKey("acme", "platform")
	staleLock := LockValue{
		ID:    "stale-poller-uuid",
		TS:    time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
		Phase: "running",
	}
	staleData, err := json.Marshal(staleLock)
	if err != nil {
		t.Fatalf("marshal stale lock: %v", err)
	}
	mc.properties["PROJ-123"] = map[string]json.RawMessage{propKey: staleData}

	// Simulate a different poller acquiring a fresh lock on the same issue
	// in the window between this poller's stale-lock read and its release.
	freshLock := LockValue{
		ID:    "fresh-poller-uuid",
		TS:    time.Now().UTC().Format(time.RFC3339),
		Phase: "pending",
	}
	freshData, err := json.Marshal(freshLock)
	if err != nil {
		t.Fatalf("marshal fresh lock: %v", err)
	}
	mc.getPropertyHook = func(issueKey, key string) {
		if issueKey == "PROJ-123" && key == propKey {
			mc.properties["PROJ-123"][propKey] = freshData
		}
	}

	if _, err := p.filterLocked(context.Background(), []jira.Issue{{Key: "PROJ-123"}}); err != nil {
		t.Fatalf("filterLocked() error: %v", err)
	}

	raw, ok := mc.properties["PROJ-123"][propKey]
	if !ok {
		t.Fatal("expected fresh lock to survive stale-lock cleanup, but property was deleted")
	}
	var current LockValue
	if err := json.Unmarshal(raw, &current); err != nil {
		t.Fatalf("unmarshal current lock: %v", err)
	}
	if current.ID != freshLock.ID {
		t.Errorf("expected surviving lock ID %q, got %q", freshLock.ID, current.ID)
	}
}

// TestFilterLockedErrorsWhenAllLockReadsFail guards against a regression
// where a lock-property read error was treated the same as "locked" and
// silently skipped, so a persistent auth/config problem that makes every
// read fail produced an empty (rather than error) result — indistinguishable
// from a genuinely quiet Jira project.
func TestFilterLockedErrorsWhenAllLockReadsFail(t *testing.T) {
	mc := newMockClient()
	propKey := lockPropertyKey("acme", "platform")
	mc.propertyGetErr[propKey] = fmt.Errorf("403 forbidden")

	p := newTestPoller(mc, nil, Options{TargetRepo: "acme/platform"})

	issues := []jira.Issue{{Key: "PROJ-1"}, {Key: "PROJ-2"}}
	unlocked, err := p.filterLocked(context.Background(), issues)
	if err == nil {
		t.Fatal("filterLocked() error = nil, want error when every lock read fails")
	}
	if len(unlocked) != 0 {
		t.Errorf("unlocked = %v, want empty", unlocked)
	}
}

// TestSearchCandidatesQuotesProjectKey checks that the default JQL quotes
// the project key rather than interpolating it bare, as defense in depth
// against JQL injection even though the key is already validated upstream.
func TestSearchCandidatesQuotesProjectKey(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, nil, Options{JiraProject: "PROJ"})

	if _, err := p.searchCandidates(context.Background()); err != nil {
		t.Fatalf("searchCandidates() error: %v", err)
	}

	want := `project = "PROJ" AND statusCategory != Done ORDER BY updated DESC`
	if mc.lastQuery != want {
		t.Errorf("JQL = %q, want %q", mc.lastQuery, want)
	}
}

// TestSearchCandidatesBoundsBySettingM checks that searchCandidates asks
// SearchIssues to stop paginating once M results are collected, instead of
// exhausting the full JQL match set and truncating client-side.
func TestSearchCandidatesBoundsBySettingM(t *testing.T) {
	mc := newMockClient()
	mc.searchResult = make([]jira.Issue, 200)
	for i := range mc.searchResult {
		mc.searchResult[i] = jira.Issue{ID: fmt.Sprintf("%d", i+1), Key: fmt.Sprintf("PROJ-%d", i+1)}
	}
	p := newTestPoller(mc, nil, Options{JiraProject: "PROJ", M: 50})

	candidates, err := p.searchCandidates(context.Background())
	if err != nil {
		t.Fatalf("searchCandidates() error: %v", err)
	}
	if len(candidates) != 50 {
		t.Errorf("len(candidates) = %d, want 50", len(candidates))
	}
	if mc.lastLimit != 50 {
		t.Errorf("SearchIssues called with limit %d, want 50 (p.opts.M)", mc.lastLimit)
	}
}

func TestRunNoChangesSinceLastCheck(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Labels:   []string{"bug"},
				Reporter: jira.User{AccountID: "reporter-id"},
				Created:  now.Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}

	// lastCheck at "now" (present, not future): all activity below is
	// older, so nothing dispatches. A future value would be treated as an
	// untrusted suppression attempt and reset (see readLastCheck clamp).
	setLastCheck(mc, "PROJ-123", "acme", "platform", now)

	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "50001",
			Body:    "old comment",
			Created: now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05.000-0700"),
			Author: jira.User{
				AccountID:   "user1",
				AccountType: "atlassian",
			},
		},
	}

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "dispatches.json")

	router := &stubMatcher{agents: []string{"triage"}}
	p := newTestPoller(mc, router, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  outputPath,
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "[]\n" {
		t.Errorf("expected empty dispatches (no changes), got %q", string(data))
	}
}

func TestRunMultipleEvents(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{
		"user1": "Developers",
	}
	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Labels: []string{"ready-to-code", "bug"},
				Status: jira.Status{
					Name:           "Open",
					StatusCategory: jira.StatusCategory{Key: "new"},
				},
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}

	setLastCheck(mc, "PROJ-123", "acme", "platform", now.Add(-30*time.Minute))

	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "50001",
			Body:    "/fs-triage check this",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "user1", AccountType: "atlassian"},
		},
	}
	mc.changelog["PROJ-123"] = []jira.ChangelogEntry{
		{
			ID:      "100",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "user1", AccountType: "atlassian"},
			Items: []jira.ChangeItem{
				{
					Field:      "labels",
					FromString: "bug",
					ToString:   "ready-to-code bug",
				},
			},
		},
	}

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "dispatches.json")

	router := &stubMatcher{agents: []string{"triage"}}
	p := newTestPoller(mc, router, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  outputPath,
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var dispatches []DispatchRecord
	if err := json.Unmarshal(data, &dispatches); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(dispatches) < 2 {
		t.Errorf("expected at least 2 dispatches (comment + label), got %d: %+v", len(dispatches), dispatches)
	}
}

func TestIsLockStale(t *testing.T) {
	threshold := 15 * time.Minute

	tests := []struct {
		name string
		lock LockValue
		want bool
	}{
		{
			name: "fresh lock",
			lock: LockValue{ID: "uuid", TS: time.Now().Format(time.RFC3339)},
			want: false,
		},
		{
			name: "stale lock",
			lock: LockValue{ID: "uuid", TS: time.Now().Add(-1 * time.Hour).Format(time.RFC3339)},
			want: true,
		},
		{
			name: "unparseable timestamp",
			lock: LockValue{ID: "uuid", TS: "garbage"},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isLockStale(tc.lock, threshold)
			if got != tc.want {
				t.Errorf("isLockStale() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSelectRandom(t *testing.T) {
	issues := make([]jira.Issue, 10)
	for i := range issues {
		issues[i] = jira.Issue{Key: "PROJ-" + string(rune('0'+i))}
	}

	selected := selectRandom(issues, 3)
	if len(selected) != 3 {
		t.Errorf("selected %d, want 3", len(selected))
	}

	all := selectRandom(make([]jira.Issue, 2), 5)
	if len(all) != 2 {
		t.Errorf("selected %d, want 2", len(all))
	}
}

func TestDeduplicate(t *testing.T) {
	now := time.Now()
	events := []JiraEvent{
		{Type: "comment_added", CommentID: "123", IssueKey: "PROJ-1", UpdatedAt: now},
		{Type: "comment_added", CommentID: "123", IssueKey: "PROJ-1", UpdatedAt: now},
		{Type: "comment_added", CommentID: "456", IssueKey: "PROJ-1", UpdatedAt: now},
	}

	unique := deduplicate(events)
	if len(unique) != 2 {
		t.Errorf("expected 2 unique events, got %d", len(unique))
	}
}

func TestFilterBotEvents(t *testing.T) {
	events := []JiraEvent{
		{
			Type:          "comment_added",
			CommentAuthor: jira.User{AccountID: "bot", AccountType: "app"},
		},
		{
			Type:          "comment_added",
			CommentAuthor: jira.User{AccountID: "human", AccountType: "atlassian"},
		},
	}

	filtered := filterBotEvents(events)
	if len(filtered) != 1 {
		t.Errorf("expected 1 event after bot filter, got %d", len(filtered))
	}
	if filtered[0].CommentAuthor.AccountID != "human" {
		t.Error("expected human event to remain")
	}
}

func TestSplitOwnerRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
	}{
		{"acme/platform", "acme", "platform"},
		{"org/sub/project", "org/sub", "project"},
		{"project", "", "project"},
	}
	for _, tc := range tests {
		owner, repo := splitOwnerRepo(tc.input)
		if owner != tc.wantOwner || repo != tc.wantRepo {
			t.Errorf("splitOwnerRepo(%q) = (%q, %q), want (%q, %q)",
				tc.input, owner, repo, tc.wantOwner, tc.wantRepo)
		}
	}
}

func TestDetectChanges_FirstPoll(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()

	p := New(mc, nil, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})

	issue := jira.Issue{
		ID:  "10042",
		Key: "PROJ-123",
		Fields: jira.IssueFields{
			Labels:   []string{"bug"},
			Reporter: jira.User{AccountID: "reporter-id"},
			Created:  now.Format("2006-01-02T15:04:05.000-0700"),
		},
	}

	result, err := p.detectChanges(context.Background(), issue, time.Time{})
	if err != nil {
		t.Fatalf("detectChanges() error: %v", err)
	}

	found := false
	for _, e := range result.events {
		if e.Type == "opened" {
			found = true
		}
	}
	if !found {
		t.Error("expected 'opened' event on first poll")
	}
}

func TestDetectChanges_FirstPoll_BackfillWindow(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	recent := now.Add(-1 * time.Hour)   // inside default 24h backfill window
	ancient := now.Add(-72 * time.Hour) // outside default 24h backfill window
	mc := newMockClient()

	mc.comments["PROJ-123"] = []jira.Comment{
		{ID: "1", Created: recent.Format("2006-01-02T15:04:05.000-0700"), Author: jira.User{AccountID: "human"}},
		{ID: "2", Created: ancient.Format("2006-01-02T15:04:05.000-0700"), Author: jira.User{AccountID: "human"}},
	}
	mc.changelog["PROJ-123"] = []jira.ChangelogEntry{
		{
			ID:      "200",
			Created: ancient.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "user1", AccountType: "atlassian"},
			Items: []jira.ChangeItem{
				{Field: "labels", FromString: "", ToString: "bug"},
			},
		},
	}

	p := New(mc, nil, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})

	issue := jira.Issue{
		ID:  "10042",
		Key: "PROJ-123",
		Fields: jira.IssueFields{
			Labels:   []string{"bug"},
			Reporter: jira.User{AccountID: "reporter-id"},
			Created:  ancient.Format("2006-01-02T15:04:05.000-0700"),
		},
	}

	result, err := p.detectChanges(context.Background(), issue, time.Time{})
	if err != nil {
		t.Fatalf("detectChanges() error: %v", err)
	}

	var sawOpened, sawRecentComment, sawAncientComment, sawLabelChange bool
	for _, e := range result.events {
		switch {
		case e.Type == "opened":
			sawOpened = true
		case e.Type == "comment_added" && e.CommentID == "1":
			sawRecentComment = true
		case e.Type == "comment_added" && e.CommentID == "2":
			sawAncientComment = true
		case e.Type == "label_changed":
			sawLabelChange = true
		}
	}
	if sawOpened {
		t.Error("expected no 'opened' event for an issue created outside the backfill window")
	}
	if !sawRecentComment {
		t.Error("expected comment within the backfill window to be included")
	}
	if sawAncientComment {
		t.Error("expected comment outside the backfill window to be excluded")
	}
	if sawLabelChange {
		t.Error("expected changelog entry outside the backfill window to be excluded")
	}

	// maxSeen must reflect the true latest activity overall (the recent
	// comment, the newest entry here) regardless of the backfill window,
	// so the next poll's lastCheck starts past all history and doesn't
	// re-flood on cycle two.
	if !result.maxSeen.Equal(recent) {
		t.Errorf("maxSeen = %v, want %v (latest activity overall, regardless of backfill window)", result.maxSeen, recent)
	}
}

func TestDetectChanges_EditedComment(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	lastCheck := now.Add(-1 * time.Hour)
	created := now.Add(-48 * time.Hour) // before lastCheck: already seen when posted
	updated := now.Add(-10 * time.Minute)
	mc := newMockClient()

	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "1",
			Created: created.Format("2006-01-02T15:04:05.000-0700"),
			Updated: updated.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "human"},
		},
		{
			ID:      "2",
			Created: created.Format("2006-01-02T15:04:05.000-0700"),
			Updated: created.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "human"},
		},
	}

	p := New(mc, nil, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})

	issue := jira.Issue{
		ID:  "10042",
		Key: "PROJ-123",
		Fields: jira.IssueFields{
			Reporter: jira.User{AccountID: "reporter-id"},
			Created:  created.Format("2006-01-02T15:04:05.000-0700"),
		},
	}

	result, err := p.detectChanges(context.Background(), issue, lastCheck)
	if err != nil {
		t.Fatalf("detectChanges() error: %v", err)
	}

	var sawEdited, sawUnedited bool
	for _, e := range result.events {
		switch {
		case e.Type == "comment_added" && e.CommentID == "1":
			sawEdited = true
			if !e.UpdatedAt.Equal(updated) {
				t.Errorf("edited comment UpdatedAt = %v, want %v (the edit time)", e.UpdatedAt, updated)
			}
			if !e.CommentEdited {
				t.Error("expected CommentEdited to be set on an edit-detected comment")
			}
		case e.Type == "comment_added" && e.CommentID == "2":
			sawUnedited = true
		}
	}
	if !sawEdited {
		t.Error("expected a comment edited after lastCheck to be detected even though it was created before lastCheck")
	}
	if sawUnedited {
		t.Error("expected an unedited comment created before lastCheck to stay filtered out")
	}
	if !result.maxSeen.Equal(updated) {
		t.Errorf("maxSeen = %v, want %v (the edit time)", result.maxSeen, updated)
	}
}

func TestDetectChanges_FirstPoll_UnparseableCreated(t *testing.T) {
	mc := newMockClient()

	p := New(mc, nil, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})

	issue := jira.Issue{
		ID:  "10042",
		Key: "PROJ-123",
		Fields: jira.IssueFields{
			Reporter: jira.User{AccountID: "reporter-id"},
			Created:  "not-a-timestamp",
		},
	}

	result, err := p.detectChanges(context.Background(), issue, time.Time{})
	if err != nil {
		t.Fatalf("detectChanges() error: %v", err)
	}

	for _, e := range result.events {
		if e.Type == "opened" {
			t.Error("expected no 'opened' event when the issue created timestamp is unparseable (fail closed)")
		}
	}
	if !result.maxSeen.IsZero() {
		t.Errorf("maxSeen = %v, want zero (no wall-clock fallback in the checkpoint)", result.maxSeen)
	}
}

func TestDetectChanges_CommentTimestampFallbacks(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	lastCheck := now.Add(-1 * time.Hour)
	recent := now.Add(-10 * time.Minute)
	old := now.Add(-48 * time.Hour)
	mc := newMockClient()

	mc.comments["PROJ-123"] = []jira.Comment{
		// Unparseable Created but valid recent Updated: still considered.
		{
			ID:      "1",
			Created: "not-a-timestamp",
			Updated: recent.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "human"},
		},
		// Updated before Created (inconsistent data): Created wins.
		{
			ID:      "2",
			Created: recent.Format("2006-01-02T15:04:05.000-0700"),
			Updated: old.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "human"},
		},
		// Neither timestamp parseable: skipped.
		{
			ID:      "3",
			Created: "not-a-timestamp",
			Updated: "also-not-a-timestamp",
			Author:  jira.User{AccountID: "human"},
		},
	}

	p := New(mc, nil, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})

	issue := jira.Issue{
		ID:  "10042",
		Key: "PROJ-123",
		Fields: jira.IssueFields{
			Reporter: jira.User{AccountID: "reporter-id"},
			Created:  old.Format("2006-01-02T15:04:05.000-0700"),
		},
	}

	result, err := p.detectChanges(context.Background(), issue, lastCheck)
	if err != nil {
		t.Fatalf("detectChanges() error: %v", err)
	}

	got := make(map[string]JiraEvent)
	for _, e := range result.events {
		if e.Type == "comment_added" {
			got[e.CommentID] = e
		}
	}

	if e, ok := got["1"]; !ok {
		t.Error("expected comment with unparseable Created but valid recent Updated to be considered")
	} else if !e.UpdatedAt.Equal(recent) {
		t.Errorf("comment 1 UpdatedAt = %v, want %v (the Updated time)", e.UpdatedAt, recent)
	}
	if e, ok := got["2"]; !ok {
		t.Error("expected comment with Updated before Created to be filtered on Created")
	} else {
		if !e.UpdatedAt.Equal(recent) {
			t.Errorf("comment 2 UpdatedAt = %v, want %v (the Created time)", e.UpdatedAt, recent)
		}
		if e.CommentEdited {
			t.Error("expected CommentEdited unset when Updated is not after Created")
		}
	}
	if _, ok := got["3"]; ok {
		t.Error("expected comment with neither timestamp parseable to be skipped")
	}
	if !result.maxSeen.Equal(recent) {
		t.Errorf("maxSeen = %v, want %v", result.maxSeen, recent)
	}
}

func TestDetectChanges_StatusChange(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	lastCheck := now.Add(-30 * time.Minute)
	mc := newMockClient()

	mc.changelog["PROJ-123"] = []jira.ChangelogEntry{
		{
			ID:      "200",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "user1", AccountType: "atlassian"},
			Items: []jira.ChangeItem{
				{
					Field:      "status",
					FromString: "Open",
					ToString:   "Done",
				},
			},
		},
	}
	mc.statuses["Done"] = jira.Status{Name: "Done", StatusCategory: jira.StatusCategory{Key: "done"}}
	mc.statuses["Open"] = jira.Status{Name: "Open", StatusCategory: jira.StatusCategory{Key: "new"}}

	p := New(mc, nil, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})

	issue := jira.Issue{
		ID:  "10042",
		Key: "PROJ-123",
		Fields: jira.IssueFields{
			Labels: []string{"bug"},
			Status: jira.Status{
				Name:           "Done",
				StatusCategory: jira.StatusCategory{Key: "done"},
			},
			Reporter: jira.User{AccountID: "reporter-id"},
			Created:  now.Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
		},
	}

	result, err := p.detectChanges(context.Background(), issue, lastCheck)
	if err != nil {
		t.Fatalf("detectChanges() error: %v", err)
	}

	found := false
	for _, e := range result.events {
		if e.Type == "closed" {
			found = true
		}
	}
	if !found {
		t.Error("expected 'closed' event from status change to Done category")
	}
}

func TestMapStatusTransition_CustomWorkflowNames(t *testing.T) {
	// Regression test: status names are fully customizable per project, so
	// classification must go through statusCategory rather than matching
	// English substrings like "done"/"closed"/"reopen" against the name.
	cases := []struct {
		name       string
		fromStatus string
		fromCat    string
		toStatus   string
		toCat      string
		want       string
	}{
		{"custom done-category name maps to closed", "In Progress", "indeterminate", "Won't Fix", "done", "closed"},
		{"non-English name lacking any recognizable substring is not miscategorized", "Open", "new", "Live", "indeterminate", ""},
		{"transition from a done-category status back to new is reopened", "Won't Fix", "done", "Open", "new", "reopened"},
		{"done-to-done workflow hygiene move is not a second closed", "Done", "done", "Won't Do", "done", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mc := newMockClient()
			mc.statuses[tc.fromStatus] = jira.Status{Name: tc.fromStatus, StatusCategory: jira.StatusCategory{Key: tc.fromCat}}
			mc.statuses[tc.toStatus] = jira.Status{Name: tc.toStatus, StatusCategory: jira.StatusCategory{Key: tc.toCat}}

			p := New(mc, nil, Options{TargetRepo: "acme/platform", JiraBaseURL: "https://acme.atlassian.net"})
			item := jira.ChangeItem{Field: "status", FromString: tc.fromStatus, ToString: tc.toStatus}
			got, err := p.mapStatusTransition(context.Background(), item)
			if err != nil {
				t.Fatalf("mapStatusTransition(%q, %q) error: %v", tc.fromStatus, tc.toStatus, err)
			}
			if got != tc.want {
				t.Errorf("mapStatusTransition(%q, %q) = %q, want %q", tc.fromStatus, tc.toStatus, got, tc.want)
			}
		})
	}
}

func TestMapStatusTransition_PrefersStableID(t *testing.T) {
	// The changelog's stable status IDs survive renames and stay unambiguous
	// where team-managed projects reuse names, so resolution must use the ID
	// when present and only fall back to the display name.
	mc := newMockClient()
	mc.statuses["10001"] = jira.Status{Name: "Completed", StatusCategory: jira.StatusCategory{Key: "done"}}
	mc.statuses["3"] = jira.Status{Name: "In Progress", StatusCategory: jira.StatusCategory{Key: "indeterminate"}}

	p := New(mc, nil, Options{TargetRepo: "acme/platform", JiraBaseURL: "https://acme.atlassian.net"})
	// The historical display names are stale (status since renamed) and are
	// NOT in the mock's status table — only ID resolution can succeed.
	item := jira.ChangeItem{Field: "status", From: "3", FromString: "Old Name", To: "10001", ToString: "Stale Name"}
	got, err := p.mapStatusTransition(context.Background(), item)
	if err != nil {
		t.Fatalf("mapStatusTransition() error: %v", err)
	}
	if got != "closed" {
		t.Errorf("mapStatusTransition() = %q, want %q (resolved via stable IDs)", got, "closed")
	}
}

func TestDetectChanges_StatusResolutionFailure(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	lastCheck := now.Add(-1 * time.Hour)
	changeTime := now.Add(-10 * time.Minute)

	newIssue := func() jira.Issue {
		return jira.Issue{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id"},
				Created:  now.Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
			},
		}
	}
	changelog := []jira.ChangelogEntry{
		{
			ID:      "400",
			Created: changeTime.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "user1", AccountType: "atlassian"},
			Items: []jira.ChangeItem{
				{Field: "status", FromString: "Open", ToString: "Done"},
			},
		},
	}

	t.Run("transient error propagates so the issue retries next cycle", func(t *testing.T) {
		mc := newMockClient()
		mc.changelog["PROJ-123"] = changelog
		mc.statuses["Open"] = jira.Status{Name: "Open", StatusCategory: jira.StatusCategory{Key: "new"}}
		mc.statusErr["Done"] = fmt.Errorf("jira api: 429 rate limited")

		p := New(mc, nil, Options{TargetRepo: "acme/platform", JiraBaseURL: "https://acme.atlassian.net"})
		if _, err := p.detectChanges(context.Background(), newIssue(), lastCheck); err == nil {
			t.Error("expected detectChanges to propagate a transient status-resolution error")
		}
	})

	t.Run("deleted status drops the event without failing the issue", func(t *testing.T) {
		mc := newMockClient()
		mc.changelog["PROJ-123"] = changelog
		mc.statuses["Open"] = jira.Status{Name: "Open", StatusCategory: jira.StatusCategory{Key: "new"}}
		// "Done" absent from the mock: GetStatus returns forge.ErrNotFound.

		p := New(mc, nil, Options{TargetRepo: "acme/platform", JiraBaseURL: "https://acme.atlassian.net"})
		result, err := p.detectChanges(context.Background(), newIssue(), lastCheck)
		if err != nil {
			t.Fatalf("detectChanges() error: %v", err)
		}
		for _, e := range result.events {
			if e.Type == "closed" || e.Type == "reopened" {
				t.Errorf("expected no transition event for a deleted status, got %q", e.Type)
			}
		}
		if !result.maxSeen.Equal(changeTime) {
			t.Errorf("maxSeen = %v, want %v", result.maxSeen, changeTime)
		}
	})

	t.Run("forbidden status drops the event without failing the issue", func(t *testing.T) {
		// A 403 can never resolve by retrying under the same credentials,
		// so it must drop the transition (like a 404) rather than
		// propagate — propagating would perpetually block dispatch of the
		// issue's other events.
		mc := newMockClient()
		mc.changelog["PROJ-123"] = changelog
		mc.statuses["Open"] = jira.Status{Name: "Open", StatusCategory: jira.StatusCategory{Key: "new"}}
		mc.statusErr["Done"] = fmt.Errorf("get status Done: %w", forge.ErrForbidden)

		p := New(mc, nil, Options{TargetRepo: "acme/platform", JiraBaseURL: "https://acme.atlassian.net"})
		result, err := p.detectChanges(context.Background(), newIssue(), lastCheck)
		if err != nil {
			t.Fatalf("detectChanges() error: %v (403 must drop, not propagate)", err)
		}
		for _, e := range result.events {
			if e.Type == "closed" || e.Type == "reopened" {
				t.Errorf("expected no transition event for a forbidden status, got %q", e.Type)
			}
		}
	})
}

func TestDetectChanges_UnsupportedFieldAdvancesMaxSeen(t *testing.T) {
	// Regression test: when a changelog entry has only unsupported fields
	// (e.g., "assignee"), detectChanges should still report the timestamp
	// in maxSeen so processIssue can advance lastCheck past it, preventing
	// the poller from stalling on the same updates every cycle.
	now := time.Now().Truncate(time.Second)
	lastCheck := now.Add(-30 * time.Minute)
	mc := newMockClient()

	assigneeChangeTime := now.Add(-10 * time.Minute)
	mc.changelog["PROJ-123"] = []jira.ChangelogEntry{
		{
			ID:      "300",
			Created: assigneeChangeTime.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "user1", AccountType: "atlassian"},
			Items: []jira.ChangeItem{
				{
					Field:      "assignee",
					FromString: "Alice",
					ToString:   "Bob",
				},
			},
		},
	}

	p := New(mc, nil, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
	})

	issue := jira.Issue{
		ID:  "10042",
		Key: "PROJ-123",
		Fields: jira.IssueFields{
			Labels:   []string{"bug"},
			Reporter: jira.User{AccountID: "reporter-id"},
			Created:  now.Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
		},
	}

	result, err := p.detectChanges(context.Background(), issue, lastCheck)
	if err != nil {
		t.Fatalf("detectChanges() error: %v", err)
	}

	if len(result.events) != 0 {
		t.Errorf("expected 0 routable events for unsupported field, got %d", len(result.events))
	}
	if result.maxSeen.IsZero() {
		t.Fatal("maxSeen should be non-zero for unsupported changelog entries")
	}
	if !result.maxSeen.Equal(assigneeChangeTime) {
		t.Errorf("maxSeen = %v, want %v", result.maxSeen, assigneeChangeTime)
	}
}

func TestParseJiraTimestamp(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"2026-01-15T10:30:00.000-0500", true},
		{"2026-01-15T10:30:00.000+0000", true},
		{"2026-01-15T10:30:00.000Z", true},
		{"2026-01-15T10:30:00Z", true},
		{"garbage", false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			_, err := parseJiraTimestamp(tc.input)
			if tc.valid && err != nil {
				t.Errorf("parseJiraTimestamp(%q) unexpected error: %v", tc.input, err)
			}
			if !tc.valid && err == nil {
				t.Errorf("parseJiraTimestamp(%q) expected error, got nil", tc.input)
			}
		})
	}
}

// setLastCheck is a test helper to pre-populate lastCheck for an issue.
func setLastCheck(mc *mockClient, issueKey, owner, repo string, t time.Time) {
	propKey := lastCheckPropertyKey(owner, repo)
	ts, _ := json.Marshal(t.UTC().Format(time.RFC3339Nano))
	if mc.properties[issueKey] == nil {
		mc.properties[issueKey] = make(map[string]json.RawMessage)
	}
	mc.properties[issueKey][propKey] = ts
}

func TestReadLock_NotFound(t *testing.T) {
	mc := newMockClient()
	p := New(mc, nil, Options{TargetRepo: "acme/platform"})

	// No properties set — mock returns forge.ErrNotFound.
	lock, err := p.readLock(context.Background(), "PROJ-123")
	if err != nil {
		t.Fatalf("readLock() should return nil error for missing property, got: %v", err)
	}
	if lock != nil {
		t.Errorf("readLock() = %+v, want nil (unlocked)", lock)
	}
}

func TestReadLastCheck_NotFound(t *testing.T) {
	mc := newMockClient()
	p := New(mc, nil, Options{TargetRepo: "acme/platform"})

	// No properties set — mock returns forge.ErrNotFound.
	lastCheck, err := p.readLastCheck(context.Background(), "PROJ-123")
	if err != nil {
		t.Fatalf("readLastCheck() should return zero time for missing property, got error: %v", err)
	}
	if !lastCheck.IsZero() {
		t.Errorf("readLastCheck() = %v, want zero time", lastCheck)
	}
}

func TestLastCheck_SubSecondPrecision(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, nil, Options{TargetRepo: "acme/platform"})

	ctx := context.Background()
	// A recent value (within the backfill window) with sub-second nanos:
	// recent so the untrusted-value clamp in readLastCheck leaves it
	// intact, letting this pin the RFC3339Nano round-trip precision.
	ts := time.Now().Add(-1 * time.Hour).UTC().Truncate(time.Millisecond)

	if err := p.advanceLastCheck(ctx, "PROJ-123", ts); err != nil {
		t.Fatalf("advanceLastCheck() error: %v", err)
	}

	got, err := p.readLastCheck(ctx, "PROJ-123")
	if err != nil {
		t.Fatalf("readLastCheck() error: %v", err)
	}

	if !got.Equal(ts) {
		t.Errorf("readLastCheck() = %v, want %v (sub-second precision lost)", got, ts)
	}

	// A comment at the exact same timestamp should NOT pass the After check.
	if ts.After(got) {
		t.Error("timestamp.After(lastCheck) should be false for equal times")
	}
}

func TestRunFirstPoll_NoLockProperty(t *testing.T) {
	// Verifies the full poll cycle works when no entity properties exist
	// (first poll on a fresh issue), which is the forge.ErrNotFound path.
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Labels:   []string{"bug"},
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "50001",
			Body:    "/fs-triage check this",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "user1", AccountType: "atlassian"},
		},
	}

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "dispatches.json")

	router := &stubMatcher{agents: []string{"triage"}}
	p := newTestPoller(mc, router, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  outputPath,
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var dispatches []DispatchRecord
	if err := json.Unmarshal(data, &dispatches); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(dispatches) == 0 {
		t.Error("expected dispatches on first poll with no prior entity properties")
	}
}

func TestRunUnsupportedChangelogField_AdvancesLastCheck(t *testing.T) {
	// Regression test: when a changelog entry contains only unsupported fields
	// (e.g., "assignee"), processIssue should still advance lastCheck so the
	// poller does not re-scan the same updates every cycle.
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Labels:   []string{"bug"},
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}

	setLastCheck(mc, "PROJ-123", "acme", "platform", now.Add(-30*time.Minute))

	// Only an unsupported changelog field — should produce zero routable events
	// but still advance lastCheck.
	assigneeChangeTime := now.Add(-10 * time.Minute)
	mc.changelog["PROJ-123"] = []jira.ChangelogEntry{
		{
			ID:      "300",
			Created: assigneeChangeTime.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "user1", AccountType: "atlassian"},
			Items: []jira.ChangeItem{
				{
					Field:      "assignee",
					FromString: "Alice",
					ToString:   "Bob",
				},
			},
		},
	}

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "dispatches.json")

	router := &stubMatcher{agents: []string{"triage"}}
	p := newTestPoller(mc, router, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  outputPath,
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify no dispatches were produced (unsupported field).
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "[]\n" {
		t.Errorf("expected empty dispatches for unsupported field change, got %q", string(data))
	}

	// Verify lastCheck was advanced past the changelog entry.
	lastCheck, err := p.readLastCheck(context.Background(), "PROJ-123")
	if err != nil {
		t.Fatalf("readLastCheck() error: %v", err)
	}
	if lastCheck.IsZero() {
		t.Fatal("lastCheck should have been advanced, but is zero")
	}
	if !lastCheck.Equal(assigneeChangeTime) && !lastCheck.After(now.Add(-30*time.Minute)) {
		t.Errorf("lastCheck = %v, expected it to be advanced past the original %v", lastCheck, now.Add(-30*time.Minute))
	}
}

// TestRunPerActorGroupResolution verifies that an actor who belongs to a
// group assigned to a project role (but is NOT a direct user actor)
// resolves to the correct role via per-actor group lookup, overcoming
// the 100-page group member pagination cap.
func TestRunPerActorGroupResolution(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()

	// Set up role actors: the Developers role has a group, no direct users.
	mc.roleActors = map[string]jira.ProjectRoleActors{
		"Developers": {
			DirectUsers: make(map[string]bool),
			GroupIDs:    []string{"group-devs"},
		},
	}
	// The actor belongs to the group.
	mc.userGroups = map[string][]jira.UserGroupInfo{
		"group-member-user": {
			{Name: "dev-group", GroupID: "group-devs"},
		},
	}

	mc.searchResult = []jira.Issue{
		{
			ID:   "10042",
			Key:  "PROJ-123",
			Self: "https://acme.atlassian.net/rest/api/3/issue/10042",
			Fields: jira.IssueFields{
				Summary: "Test issue",
				Labels:  []string{"bug"},
				Status: jira.Status{
					Name:           "Open",
					StatusCategory: jira.StatusCategory{Key: "new"},
				},
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "50001",
			Body:    "/fs-triage check this",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author: jira.User{
				AccountID:   "group-member-user",
				AccountType: "atlassian",
			},
		},
	}

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "dispatches.json")

	router := &stubMatcher{agents: []string{"triage"}}
	p := newTestPoller(mc, router, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  outputPath,
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify the actor was resolved to "write" (Developers role) via group.
	if role, ok := p.roleMembership["group-member-user"]; !ok {
		t.Error("expected group-member-user to be in roleMembership")
	} else if role != "Developers" {
		t.Errorf("roleMembership[group-member-user] = %q, want %q", role, "Developers")
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var dispatches []DispatchRecord
	if err := json.Unmarshal(data, &dispatches); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(dispatches) == 0 {
		t.Error("expected dispatches for group-resolved actor")
	}

	// The stub matcher produces an agent="triage" record for every event.
	// Verify at least one dispatch was produced (the comment from
	// group-member-user). Role verification is done above via
	// roleMembership; the matcher stub does not carry actor-level payload.
	var foundTriage bool
	for _, d := range dispatches {
		if d.Agent == "triage" {
			foundTriage = true
			break
		}
	}
	if !foundTriage {
		t.Error("expected a triage dispatch for group-member-user")
	}
}

// TestRunPerActorGroupResolution_HighestPriorityWins verifies that when
// an actor belongs to groups assigned to multiple roles, the highest-
// priority role wins.
func TestRunPerActorGroupResolution_HighestPriorityWins(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()

	mc.roleActors = map[string]jira.ProjectRoleActors{
		"Administrators": {
			DirectUsers: make(map[string]bool),
			GroupIDs:    []string{"group-admins"},
		},
		"Developers": {
			DirectUsers: make(map[string]bool),
			GroupIDs:    []string{"group-devs"},
		},
	}
	// Actor belongs to both groups.
	mc.userGroups = map[string][]jira.UserGroupInfo{
		"dual-group-user": {
			{Name: "dev-group", GroupID: "group-devs"},
			{Name: "admin-group", GroupID: "group-admins"},
		},
	}

	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "50001",
			Body:    "/fs-triage go",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "dual-group-user", AccountType: "atlassian"},
		},
	}

	p := newTestPoller(mc, &stubMatcher{agents: []string{"triage"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  filepath.Join(t.TempDir(), "dispatches.json"),
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if role := p.roleMembership["dual-group-user"]; role != "Administrators" {
		t.Errorf("roleMembership[dual-group-user] = %q, want %q (highest priority)", role, "Administrators")
	}
}

// TestRunPerActorGroupResolution_DirectPlusGroupPriority verifies that an
// actor who is a direct member of a lower-priority role AND a member of
// a group mapped to a higher-priority role resolves to the higher
// priority role, rather than being capped at the direct assignment (see
// PR #6048 review).
func TestRunPerActorGroupResolution_DirectPlusGroupPriority(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()

	mc.roleActors = map[string]jira.ProjectRoleActors{
		"Developers": {
			DirectUsers: map[string]bool{"dev-and-admin-user": true},
		},
		"Administrators": {
			DirectUsers: make(map[string]bool),
			GroupIDs:    []string{"group-admins"},
		},
	}
	// The actor is also a member of the Administrators-mapped group.
	mc.userGroups = map[string][]jira.UserGroupInfo{
		"dev-and-admin-user": {
			{Name: "admin-group", GroupID: "group-admins"},
		},
	}

	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "50001",
			Body:    "/fs-triage go",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "dev-and-admin-user", AccountType: "atlassian"},
		},
	}

	p := newTestPoller(mc, &stubMatcher{agents: []string{"triage"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  filepath.Join(t.TempDir(), "dispatches.json"),
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if role := p.roleMembership["dev-and-admin-user"]; role != "Administrators" {
		t.Errorf("roleMembership[dev-and-admin-user] = %q, want %q (direct Developers + group Administrators should upgrade)", role, "Administrators")
	}
}

// TestRunPerActorGroupResolution_NoGroupMatch verifies that an actor
// not in any project-role group resolves to "external".
func TestRunPerActorGroupResolution_NoGroupMatch(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()

	mc.roleActors = map[string]jira.ProjectRoleActors{
		"Developers": {
			DirectUsers: make(map[string]bool),
			GroupIDs:    []string{"group-devs"},
		},
	}
	// Actor belongs to a different group that is NOT assigned to any role.
	mc.userGroups = map[string][]jira.UserGroupInfo{
		"outsider-user": {
			{Name: "other-group", GroupID: "group-other"},
		},
	}

	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "50001",
			Body:    "/fs-triage go",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "outsider-user", AccountType: "atlassian"},
		},
	}

	p := newTestPoller(mc, &stubMatcher{agents: []string{"triage"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  filepath.Join(t.TempDir(), "dispatches.json"),
	})

	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if _, ok := p.roleMembership["outsider-user"]; ok {
		t.Error("expected outsider-user to NOT be in roleMembership")
	}
}

// TestRunPerActorGroupResolution_APIError verifies that a GetUserGroups
// error for even one actor fails the issue closed (Run() returns an
// error, checkpoint untouched, retried next cycle) rather than silently
// leaving that actor unresolved (defaulting to "external" in resolveRole)
// and dispatching their event anyway with an unrecoverable privilege
// downgrade for this cycle (see PR #6048 review).
func TestRunPerActorGroupResolution_APIError(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()

	// direct-user and reporter-id are direct Developers members, but a
	// direct assignment doesn't rule out also being in a higher-priority
	// group, so they're still checked. Two group users: one whose lookup
	// fails, one succeeds.
	mc.roleActors = map[string]jira.ProjectRoleActors{
		"Developers": {
			DirectUsers: map[string]bool{
				"direct-user": true,
				"reporter-id": true,
			},
			GroupIDs: []string{"group-devs"},
		},
	}
	// Per-actor error: only group-user-fail fails.
	mc.userGroupsErrs = map[string]error{
		"group-user-fail": fmt.Errorf("jira api: 503 service unavailable"),
	}
	// group-user-ok succeeds and resolves via group membership.
	mc.userGroups = map[string][]jira.UserGroupInfo{
		"group-user-ok": {
			{Name: "dev-group", GroupID: "group-devs"},
		},
	}

	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "1",
			Body:    "/fs-triage go",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "direct-user", AccountType: "atlassian"},
		},
		{
			ID:      "2",
			Body:    "/fs-triage go too",
			Created: now.Add(1 * time.Second).Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "group-user-fail", AccountType: "atlassian"},
		},
		{
			ID:      "3",
			Body:    "/fs-triage also go",
			Created: now.Add(2 * time.Second).Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "group-user-ok", AccountType: "atlassian"},
		},
	}

	p := newTestPoller(mc, &stubMatcher{agents: []string{"triage"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  filepath.Join(t.TempDir(), "dispatches.json"),
	})

	// Should fail closed: even one actor's lookup failure fails this
	// issue's processing, so it's retried next cycle instead of silently
	// dispatching a downgraded role for the failed actor.
	if err := p.Run(context.Background()); err == nil {
		t.Fatal("expected Run() to fail when any actor group lookup fails (fail-closed)")
	}

	// group-user-fail should NOT be in roleMembership (API failed).
	if _, ok := p.roleMembership["group-user-fail"]; ok {
		t.Error("expected group-user-fail to NOT be in roleMembership after API error")
	}
}

// TestRunPerActorGroupResolution_AllActorsAPIError verifies that when
// ALL per-actor group lookups fail, the error propagates and the issue
// processing fails.
func TestRunPerActorGroupResolution_AllActorsAPIError(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()

	mc.roleActors = map[string]jira.ProjectRoleActors{
		"Developers": {
			DirectUsers: make(map[string]bool),
			GroupIDs:    []string{"group-devs"},
		},
	}
	mc.userGroupsErr = fmt.Errorf("jira api: 503 service unavailable")

	mc.searchResult = []jira.Issue{
		{
			ID:  "10042",
			Key: "PROJ-123",
			Fields: jira.IssueFields{
				Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
				Created:  now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
				Updated:  now.Format("2006-01-02T15:04:05.000-0700"),
			},
		},
	}
	mc.comments["PROJ-123"] = []jira.Comment{
		{
			ID:      "1",
			Body:    "/fs-triage go",
			Created: now.Format("2006-01-02T15:04:05.000-0700"),
			Author:  jira.User{AccountID: "unknown-user", AccountType: "atlassian"},
		},
	}

	p := newTestPoller(mc, &stubMatcher{agents: []string{"triage"}}, Options{
		TargetRepo:  "acme/platform",
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		OutputPath:  filepath.Join(t.TempDir(), "dispatches.json"),
	})

	// Should fail because ALL actors have errors and only 1 issue → all failed.
	if err := p.Run(context.Background()); err == nil {
		t.Fatal("expected Run() to fail when all actor group lookups fail for all issues")
	}
}
