package jirapoll

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

const jiraTimeLayout = "2006-01-02T15:04:05.000-0700"

// errAdminOnly is what Jira Cloud returns for GET /project/{key}/role to
// an account without the Administer Projects permission.
var errAdminOnly = fmt.Errorf("jira api: 401 You cannot edit the configuration of this project")

// gateMatcher applies the real dispatch authorization gate to every event
// and "matches" each authorized harness (no CEL), recording which actor
// dispatched which harness. It stands in for the CLI's celMatcher.
type gateMatcher struct {
	harnesses map[string]string // harness name -> harness role:
	opts      harnessdispatch.AuthOptions
	events    []normevent.Event
}

func newGateMatcher(opts harnessdispatch.AuthOptions) *gateMatcher {
	return &gateMatcher{
		harnesses: map[string]string{"triage": "triage", "code": "coder"},
		opts:      opts,
	}
}

func (m *gateMatcher) Match(_ context.Context, event *normevent.Event) ([]DispatchRecord, error) {
	m.events = append(m.events, *event)
	if !harnessdispatch.IsEventAuthorizedWith(event, m.opts) {
		return nil, nil
	}
	var records []DispatchRecord
	for name, role := range m.harnesses {
		if harnessdispatch.IsHarnessAuthorizedWith(event, role, m.opts) {
			records = append(records, DispatchRecord{
				Agent:        name,
				Role:         role,
				SourceRepo:   event.Repo,
				EventPayload: event.Actor.ID,
			})
		}
	}
	return records, nil
}

// dispatchedBy returns the sorted agent names dispatched for actor.
func dispatchedBy(p *Poller, actor string) []string {
	var out []string
	for _, d := range p.dispatches {
		if d.EventPayload == actor {
			out = append(out, d.Agent)
		}
	}
	sort.Strings(out)
	return out
}

// roleOf returns the actor.role the matcher saw for actor's first event.
func (m *gateMatcher) roleOf(actor string) string {
	for _, ev := range m.events {
		if ev.Actor.ID == actor {
			return string(ev.Actor.Role)
		}
	}
	return ""
}

func authTestIssue(key string, now time.Time) jira.Issue {
	return jira.Issue{
		ID:  strings.TrimPrefix(key, "PROJ-"),
		Key: key,
		Fields: jira.IssueFields{
			Status:   jira.Status{Name: "Open", StatusCategory: jira.StatusCategory{Key: "new"}},
			Reporter: jira.User{AccountID: "reporter-id", AccountType: "atlassian"},
			Created:  now.Add(-1 * time.Hour).Format(jiraTimeLayout),
			Updated:  now.Format(jiraTimeLayout),
		},
	}
}

func authTestComment(id, author, body string, at time.Time) jira.Comment {
	return jira.Comment{
		ID:      id,
		Body:    body,
		Created: at.Format(jiraTimeLayout),
		Author:  jira.User{AccountID: author, AccountType: "atlassian"},
	}
}

func authTestLabelAdded(id, author, label string, at time.Time) jira.ChangelogEntry {
	return jira.ChangelogEntry{
		ID:      id,
		Created: at.Format(jiraTimeLayout),
		Author:  jira.User{AccountID: author, AccountType: "atlassian"},
		Items:   []jira.ChangeItem{{Field: "labels", ToString: label}},
	}
}

func authTestPoller(mc *mockClient, m EventMatcher, auth Authorization) *Poller {
	return newTestPoller(mc, m, Options{
		TargetRepo:    "acme/platform",
		JiraBaseURL:   "https://acme.atlassian.net",
		JiraProject:   "PROJ",
		N:             50,
		Authorization: auth,
	})
}

func assertAgents(t *testing.T, p *Poller, actor string, want ...string) {
	t.Helper()
	got := dispatchedBy(p, actor)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dispatches for %s = %v, want %v", actor, got, want)
	}
}

func TestRunAuth_JiraEditorsLabelSkipsProjectRoles(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleErr = errAdminOnly // the poller holds no Administer Projects
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}
	mc.changelog["PROJ-1"] = []jira.ChangelogEntry{
		authTestLabelAdded("100", "editor", "ready-to-code", now.Add(-10*time.Minute)),
	}

	m := newGateMatcher(harnessdispatch.AuthOptions{JiraLabelsByEditPermission: true})
	p := authTestPoller(mc, m, Authorization{Configured: true})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	assertAgents(t, p, "editor", "code", "triage")
	if mc.roleActorsCalls != 0 {
		t.Errorf("GetProjectRoleActors called %d times, want 0", mc.roleActorsCalls)
	}
	if len(mc.userGroupsCalls) != 0 {
		t.Errorf("GetUserGroups called for %v, want no calls", mc.userGroupsCalls)
	}
	if got := m.roleOf("editor"); got != "none" {
		t.Errorf("actor.role = %q, want %q (not resolved under jira_editors)", got, "none")
	}
}

func TestRunAuth_ListsAuthorizeSlashCommands(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleErr = errAdminOnly
	mc.userGroups = map[string][]jira.UserGroupInfo{
		"group-dev":    {{Name: "example-team-developers", GroupID: "gid-dev"}},
		"group-triage": {{Name: "example-team", GroupID: "gid-team"}},
		"stranger":     {{Name: "other-team", GroupID: "gid-other"}},
	}
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}
	mc.comments["PROJ-1"] = []jira.Comment{
		authTestComment("1", "acct-writer", "/fs-code please", now.Add(-50*time.Minute)),
		authTestComment("2", "group-dev", "/fs-code please", now.Add(-40*time.Minute)),
		authTestComment("3", "acct-triager", "/fs-code please", now.Add(-30*time.Minute)),
		authTestComment("4", "group-triage", "/fs-triage", now.Add(-20*time.Minute)),
		authTestComment("5", "stranger", "/fs-code please", now.Add(-10*time.Minute)),
	}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	p := authTestPoller(mc, m, Authorization{
		Configured: true,
		Write:      ActorList{Groups: []string{"example-team-developers"}, Accounts: []string{"acct-writer"}},
		Triage:     ActorList{Groups: []string{"gid-team"}, Accounts: []string{"acct-triager"}},
	})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	assertAgents(t, p, "acct-writer", "code", "triage")
	assertAgents(t, p, "group-dev", "code", "triage")
	assertAgents(t, p, "acct-triager", "triage")
	assertAgents(t, p, "group-triage", "triage") // matched by group ID
	assertAgents(t, p, "stranger")

	for actor, want := range map[string]string{
		"acct-writer": "write", "group-dev": "write", "acct-triager": "triage",
		"group-triage": "triage", "stranger": "none",
	} {
		if got := m.roleOf(actor); got != want {
			t.Errorf("actor.role for %s = %q, want %q", actor, got, want)
		}
	}
	if mc.roleActorsCalls != 0 {
		t.Errorf("GetProjectRoleActors called %d times, want 0", mc.roleActorsCalls)
	}
	// A write account needs no group lookup; everyone else is looked up
	// once because a write group could raise their role.
	if n := mc.userGroupsCalls["acct-writer"]; n != 0 {
		t.Errorf("GetUserGroups called %d times for a write account, want 0", n)
	}
	for _, actor := range []string{"group-dev", "acct-triager", "group-triage", "stranger"} {
		if n := mc.userGroupsCalls[actor]; n != 1 {
			t.Errorf("GetUserGroups called %d times for %s, want 1", n, actor)
		}
	}
}

func TestRunAuth_AccountListsMakeNoJiraCalls(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleErr = errAdminOnly
	mc.userGroupsErr = fmt.Errorf("unexpected GetUserGroups call")
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}
	mc.comments["PROJ-1"] = []jira.Comment{
		authTestComment("1", "acct-triager", "/fs-code", now.Add(-20*time.Minute)),
		authTestComment("2", "stranger", "/fs-triage", now.Add(-10*time.Minute)),
	}

	p := authTestPoller(mc, newGateMatcher(harnessdispatch.AuthOptions{}), Authorization{
		Configured: true,
		Write:      ActorList{Accounts: []string{"acct-writer"}},
		Triage:     ActorList{Accounts: []string{"acct-triager"}},
	})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	assertAgents(t, p, "acct-triager", "triage")
	assertAgents(t, p, "stranger")
	if mc.roleActorsCalls != 0 || len(mc.userGroupsCalls) != 0 {
		t.Errorf("Jira role calls: GetProjectRoleActors=%d GetUserGroups=%v, want none", mc.roleActorsCalls, mc.userGroupsCalls)
	}
}

func TestRunAuth_EmptyConfiguredListsDenyEveryone(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleErr = errAdminOnly
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}
	mc.comments["PROJ-1"] = []jira.Comment{
		authTestComment("1", "dev", "/fs-code", now.Add(-10*time.Minute)),
	}

	// The lists were configured but every entry has been removed: nobody is
	// named, so nobody is authorized, and the project role roster (which a
	// project Developer would pass) is not consulted.
	p := authTestPoller(mc, newGateMatcher(harnessdispatch.AuthOptions{}), Authorization{Configured: true, ListsConfigured: true})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	assertAgents(t, p, "dev")
	if mc.roleActorsCalls != 0 {
		t.Errorf("GetProjectRoleActors called %d times, want 0", mc.roleActorsCalls)
	}
}

func TestRunAuth_ListGroupsCachedPerCycle(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.userGroups = map[string][]jira.UserGroupInfo{"dev": {{Name: "devs"}}}
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now), authTestIssue("PROJ-2", now)}
	mc.comments["PROJ-1"] = []jira.Comment{authTestComment("1", "dev", "/fs-code", now.Add(-20*time.Minute))}
	mc.comments["PROJ-2"] = []jira.Comment{authTestComment("2", "dev", "/fs-code", now.Add(-10*time.Minute))}

	p := authTestPoller(mc, newGateMatcher(harnessdispatch.AuthOptions{}), Authorization{
		Configured: true,
		Write:      ActorList{Groups: []string{"devs"}},
	})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if got := len(dispatchedBy(p, "dev")); got != 4 {
		t.Errorf("got %d dispatches for dev, want 4 (code+triage on two issues)", got)
	}
	if n := mc.userGroupsCalls["dev"]; n != 1 {
		t.Errorf("GetUserGroups called %d times, want 1 (cached per cycle)", n)
	}
}

func TestRunAuth_ListGroupLookupFailureFailsIssue(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.userGroupsErr = fmt.Errorf("jira api: 503")
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}
	mc.comments["PROJ-1"] = []jira.Comment{authTestComment("1", "dev", "/fs-code", now.Add(-10*time.Minute))}

	p := authTestPoller(mc, newGateMatcher(harnessdispatch.AuthOptions{}), Authorization{
		Configured: true,
		Write:      ActorList{Groups: []string{"devs"}},
	})
	if err := p.Run(context.Background()); err == nil {
		t.Fatal("expected Run() to fail when the only issue's group lookup fails")
	}
	if len(p.dispatches) != 0 {
		t.Errorf("expected no dispatches, got %+v", p.dispatches)
	}
	lastCheck, err := p.readLastCheck(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("readLastCheck() error: %v", err)
	}
	if !lastCheck.IsZero() {
		t.Errorf("expected lastCheck to stay unset, got %v", lastCheck)
	}
}

func TestRunAuth_LabelsAllowlist(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleErr = errAdminOnly
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}
	mc.changelog["PROJ-1"] = []jira.ChangelogEntry{
		authTestLabelAdded("100", "acct-triager", "ready-to-code", now.Add(-20*time.Minute)),
		authTestLabelAdded("101", "stranger", "ready-to-code", now.Add(-10*time.Minute)),
	}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	p := authTestPoller(mc, m, Authorization{
		Configured: true,
		Triage:     ActorList{Accounts: []string{"acct-triager"}},
		Labels:     LabelsAllowlist,
	})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	assertAgents(t, p, "acct-triager", "triage")
	assertAgents(t, p, "stranger")
	if mc.roleActorsCalls != 0 {
		t.Errorf("GetProjectRoleActors called %d times, want 0", mc.roleActorsCalls)
	}
}

func TestRunAuth_LabelsProjectRoles(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{"dev": "Developers"}
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now), authTestIssue("PROJ-2", now)}
	mc.changelog["PROJ-1"] = []jira.ChangelogEntry{
		authTestLabelAdded("100", "dev", "ready-to-code", now.Add(-20*time.Minute)),
	}
	mc.changelog["PROJ-2"] = []jira.ChangelogEntry{
		authTestLabelAdded("101", "editor", "ready-to-code", now.Add(-10*time.Minute)),
	}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	// Lists configured for comments do not apply to labels under
	// project_roles.
	p := authTestPoller(mc, m, Authorization{
		Configured: true,
		Write:      ActorList{Accounts: []string{"editor"}},
		Labels:     LabelsProjectRoles,
	})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	assertAgents(t, p, "dev", "code", "triage")
	assertAgents(t, p, "editor")
	if mc.roleActorsCalls != 1 {
		t.Errorf("GetProjectRoleActors called %d times, want 1 (loaded once per cycle)", mc.roleActorsCalls)
	}
}

func TestRunAuth_LabelsProjectRolesLoadFailureFailsCycle(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleErr = errAdminOnly
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}
	mc.changelog["PROJ-1"] = []jira.ChangelogEntry{
		authTestLabelAdded("100", "dev", "ready-to-code", now.Add(-10*time.Minute)),
	}

	p := authTestPoller(mc, newGateMatcher(harnessdispatch.AuthOptions{}), Authorization{
		Configured: true,
		Labels:     LabelsProjectRoles,
	})
	err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "load project roles for PROJ") {
		t.Fatalf("Run() error = %v, want a project role load failure", err)
	}
	lastCheck, err := p.readLastCheck(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("readLastCheck() error: %v", err)
	}
	if !lastCheck.IsZero() {
		t.Errorf("expected lastCheck to stay unset, got %v", lastCheck)
	}
}

func TestRunAuth_NoProviderSlashCommandUsesProjectRoles(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{"dev": "Developers", "other": "Viewers"}
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now), authTestIssue("PROJ-2", now)}
	mc.comments["PROJ-1"] = []jira.Comment{authTestComment("1", "dev", "/fs-code", now.Add(-20*time.Minute))}
	mc.comments["PROJ-2"] = []jira.Comment{authTestComment("2", "other", "/fs-code", now.Add(-10*time.Minute))}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	p := authTestPoller(mc, m, Authorization{})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	assertAgents(t, p, "dev", "code", "triage")
	assertAgents(t, p, "other")
	if got := m.roleOf("dev"); got != "write" {
		t.Errorf("actor.role = %q, want write", got)
	}
	if mc.roleActorsCalls != 1 {
		t.Errorf("GetProjectRoleActors called %d times, want 1", mc.roleActorsCalls)
	}
}

func TestRunAuth_NoEntryLabelAddedUsesRoleThreshold(t *testing.T) {
	// With no jira entry, a label-added event takes the per-harness role
	// threshold and the actor resolves through the project role roster,
	// which is loaded once for the cycle.
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{"dev": "Developers"}
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now), authTestIssue("PROJ-2", now)}
	mc.changelog["PROJ-1"] = []jira.ChangelogEntry{
		authTestLabelAdded("100", "dev", "ready-to-code", now.Add(-20*time.Minute)),
	}
	mc.changelog["PROJ-2"] = []jira.ChangelogEntry{
		authTestLabelAdded("101", "norole", "ready-to-code", now.Add(-10*time.Minute)),
	}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	p := authTestPoller(mc, m, Authorization{})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	assertAgents(t, p, "dev", "code", "triage")
	assertAgents(t, p, "norole")
	if got := m.roleOf("dev"); got != "write" {
		t.Errorf("actor.role for dev = %q, want write", got)
	}
	if mc.roleActorsCalls != 1 {
		t.Errorf("GetProjectRoleActors called %d times, want 1 (loaded once per cycle)", mc.roleActorsCalls)
	}
}

func TestRunAuth_NoEntryIssueOpenedResolvesThroughProjectRoles(t *testing.T) {
	// With no jira entry, even an issue-opened event resolves its actor
	// through the roster: a Developers member is write and dispatches the
	// triage harness.
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{"reporter-id": "Developers"}
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	p := authTestPoller(mc, m, Authorization{})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if got := m.roleOf("reporter-id"); got != "write" {
		t.Errorf("actor.role for reporter-id = %q, want write", got)
	}
	if !slices.Contains(dispatchedBy(p, "reporter-id"), "triage") {
		t.Errorf("dispatches for reporter-id = %v, want the triage harness", dispatchedBy(p, "reporter-id"))
	}
	if mc.roleActorsCalls != 1 {
		t.Errorf("GetProjectRoleActors called %d times, want 1", mc.roleActorsCalls)
	}
}

func TestRunAuth_NoEventsSkipsProjectRoles(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleErr = errAdminOnly
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}

	// A jira entry is present: the issue's only event is its first-poll
	// opened event, which the entry without lists leaves unresolved.
	p := authTestPoller(mc, newGateMatcher(harnessdispatch.AuthOptions{}), Authorization{Configured: true})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if mc.roleActorsCalls != 0 {
		t.Errorf("GetProjectRoleActors called %d times for an issue with no events, want 0", mc.roleActorsCalls)
	}
}

func TestRunAuth_SelfAuthoredEventsFilteredWithLists(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}
	mc.comments["PROJ-1"] = []jira.Comment{
		authTestComment("1", "poller-service-account", "/fs-code", now.Add(-20*time.Minute)),
	}
	mc.changelog["PROJ-1"] = []jira.ChangelogEntry{
		authTestLabelAdded("100", "poller-service-account", "ready-to-code", now.Add(-10*time.Minute)),
	}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	// Even if the poller account is listed, its own events never dispatch.
	p := authTestPoller(mc, m, Authorization{Configured: true, Write: ActorList{Accounts: []string{"poller-service-account"}}})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if got := m.roleOf("poller-service-account"); got != "" {
		t.Errorf("a self-authored event reached the matcher with actor.role %q", got)
	}
	if got := dispatchedBy(p, "poller-service-account"); len(got) != 0 {
		t.Errorf("self-authored events dispatched %v", got)
	}
}

func TestRunAuth_EntryWithoutListsNonCommandEventsSkipProjectRoles(t *testing.T) {
	// With a jira entry that sets no lists, only slash commands consult the
	// roster: an issue opened (first poll) and a plain comment are not
	// resolved and do not pass a role threshold, and the admin-only roster
	// is never loaded.
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleErr = errAdminOnly
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}
	mc.comments["PROJ-1"] = []jira.Comment{authTestComment("1", "commenter", "thanks!", now.Add(-10*time.Minute))}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	p := authTestPoller(mc, m, Authorization{Configured: true})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if mc.roleActorsCalls != 0 {
		t.Errorf("GetProjectRoleActors called %d times, want 0", mc.roleActorsCalls)
	}
	for _, actor := range []string{"reporter-id", "commenter"} {
		if got := m.roleOf(actor); got != "none" {
			t.Errorf("actor.role for %s = %q, want none", actor, got)
		}
		assertAgents(t, p, actor)
	}
}

func TestRunAuth_ProjectRolesWithoutListsResolvesIssueOpened(t *testing.T) {
	// labels: project_roles with no lists is the explicit "keep the no-entry
	// behaviour" entry: an issue opened by a Developers member resolves
	// through the roster and dispatches triage, under the real gate.
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{"reporter-id": "Developers"}
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	p := authTestPoller(mc, m, Authorization{Configured: true, Labels: LabelsProjectRoles})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if got := m.roleOf("reporter-id"); got != "write" {
		t.Errorf("actor.role for reporter-id = %q, want write", got)
	}
	if !slices.Contains(dispatchedBy(p, "reporter-id"), "triage") {
		t.Errorf("dispatches for reporter-id = %v, want the triage harness", dispatchedBy(p, "reporter-id"))
	}
	if mc.roleActorsCalls != 1 {
		t.Errorf("GetProjectRoleActors called %d times, want 1", mc.roleActorsCalls)
	}
}

func TestRunAuth_JiraEditorsWithoutListsDoesNotDispatchIssueOpened(t *testing.T) {
	// Under the default labels: jira_editors with no lists, the same
	// Developers member's issue-opened event is not resolved and does not
	// dispatch, and the roster is never loaded.
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleMembership = map[string]string{"reporter-id": "Developers"}
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	p := authTestPoller(mc, m, Authorization{Configured: true, Labels: LabelsJiraEditors})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if got := m.roleOf("reporter-id"); got != "none" {
		t.Errorf("actor.role for reporter-id = %q, want none", got)
	}
	assertAgents(t, p, "reporter-id")
	if mc.roleActorsCalls != 0 {
		t.Errorf("GetProjectRoleActors called %d times, want 0", mc.roleActorsCalls)
	}
}

func TestRunAuth_ListsAuthorizeIssueOpened(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newMockClient()
	mc.roleErr = errAdminOnly
	mc.searchResult = []jira.Issue{authTestIssue("PROJ-1", now)}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	p := authTestPoller(mc, m, Authorization{Configured: true, Triage: ActorList{Accounts: []string{"reporter-id"}}})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	assertAgents(t, p, "reporter-id", "triage")
	if mc.roleActorsCalls != 0 {
		t.Errorf("GetProjectRoleActors called %d times, want 0", mc.roleActorsCalls)
	}
}

func TestResolveRole_ListsApplyToNonCommandEvents(t *testing.T) {
	p := &Poller{
		opts: Options{
			JiraProject:   "PROJ",
			Authorization: Authorization{Configured: true, Triage: ActorList{Accounts: []string{"t"}}},
		},
		roleMembership: map[string]string{"t": "Administrators"},
	}
	opened := JiraEvent{Type: "opened", IssueKey: "PROJ-1", Reporter: jira.User{AccountID: "t"}}
	if got := p.toNormalizedEvent(opened).Actor.Role; got != "triage" {
		t.Errorf("actor.role = %q, want triage from the lists, not the project role", got)
	}
	removed := JiraEvent{Type: "label_changed", IssueKey: "PROJ-1", LabelAction: "removed", ChangedLabel: "x", ChangeAuthor: jira.User{AccountID: "t"}}
	if got := p.toNormalizedEvent(removed).Actor.Role; got != "triage" {
		t.Errorf("label removal actor.role = %q, want triage from the lists", got)
	}
}

func TestRoleSourceFor(t *testing.T) {
	label := JiraEvent{Type: "label_changed", LabelAction: "added"}
	comment := JiraEvent{Type: "comment_added", CommentBody: "/fs-code now"}
	plain := JiraEvent{Type: "comment_added", CommentBody: "looks good"}
	opened := JiraEvent{Type: "opened"}
	lists := Authorization{Configured: true, Write: ActorList{Accounts: []string{"a"}}}
	entry := Authorization{Configured: true}
	tests := []struct {
		name  string
		auth  Authorization
		event JiraEvent
		want  roleSource
	}{
		{"no entry: label", Authorization{}, label, roleFromProjectRoles},
		{"no entry: slash command", Authorization{}, comment, roleFromProjectRoles},
		{"no entry: plain comment", Authorization{}, plain, roleFromProjectRoles},
		{"no entry: opened", Authorization{}, opened, roleFromProjectRoles},
		{"label default", entry, label, roleNotResolved},
		{"label jira_editors with lists", Authorization{Configured: true, Write: lists.Write, Labels: LabelsJiraEditors}, label, roleNotResolved},
		{"label allowlist", Authorization{Configured: true, Labels: LabelsAllowlist}, label, roleFromLists},
		{"label project_roles", Authorization{Configured: true, Write: lists.Write, Labels: LabelsProjectRoles}, label, roleFromProjectRoles},
		{"comment without lists", Authorization{Configured: true, Labels: LabelsAllowlist}, comment, roleFromProjectRoles},
		{"comment with lists", lists, comment, roleFromLists},
		{"plain comment without lists", entry, plain, roleNotResolved},
		{"opened without lists", entry, opened, roleNotResolved},
		{"opened with lists", lists, opened, roleFromLists},
		{"project_roles without lists: label", Authorization{Configured: true, Labels: LabelsProjectRoles}, label, roleFromProjectRoles},
		{"project_roles without lists: slash command", Authorization{Configured: true, Labels: LabelsProjectRoles}, comment, roleFromProjectRoles},
		{"project_roles without lists: plain comment", Authorization{Configured: true, Labels: LabelsProjectRoles}, plain, roleFromProjectRoles},
		{"project_roles without lists: opened", Authorization{Configured: true, Labels: LabelsProjectRoles}, opened, roleFromProjectRoles},
		{"project_roles with lists: opened", Authorization{Configured: true, Write: lists.Write, Labels: LabelsProjectRoles}, opened, roleFromLists},
		{"jira_editors without lists: opened", Authorization{Configured: true, Labels: LabelsJiraEditors}, opened, roleNotResolved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Poller{opts: Options{Authorization: tt.auth}}
			if got := p.roleSourceFor(tt.event); got != tt.want {
				t.Errorf("roleSourceFor() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnsureRoleStructure_NoProjectKey(t *testing.T) {
	mc := newMockClient()
	p := &Poller{client: mc}
	if err := p.ensureRoleStructure(context.Background()); err != nil {
		t.Fatalf("ensureRoleStructure() error: %v", err)
	}
	if err := p.ensureRoleStructure(context.Background()); err != nil {
		t.Fatalf("ensureRoleStructure() second call error: %v", err)
	}
	if mc.roleActorsCalls != 0 {
		t.Errorf("GetProjectRoleActors called %d times without a project key, want 0", mc.roleActorsCalls)
	}
}

func TestRoleLoadError(t *testing.T) {
	err := &roleLoadError{err: errAdminOnly}
	if err.Error() != errAdminOnly.Error() {
		t.Errorf("Error() = %q", err.Error())
	}
	if err.Unwrap() != errAdminOnly {
		t.Error("Unwrap() did not return the cause")
	}
}
