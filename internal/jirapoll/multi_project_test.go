package jirapoll

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
)

// testRosters returns a rosters map holding membership as the direct-user
// role roster of project, for tests that build a Poller by hand.
func testRosters(project string, membership map[string]string) map[string]*projectRoster {
	return map[string]*projectRoster{project: {
		membership:    membership,
		groups:        map[string][]string{},
		groupsChecked: map[string]bool{},
	}}
}

// membershipOf returns the role membership loaded for project this cycle,
// or nil when its roster was not loaded.
func membershipOf(p *Poller, project string) map[string]string {
	if roster := p.rosters[project]; roster != nil {
		return roster.membership
	}
	return nil
}

// noRequestClient fails the test on any Jira request: its embedded client
// is nil, so every method call panics.
type noRequestClient struct {
	JiraClient
}

// projectRolesClient serves a separate role roster per project and counts
// the searches and roster loads it serves.
type projectRolesClient struct {
	*mockClient
	rosters     map[string]map[string]string // project → accountID → role name
	rosterErr   map[string]error             // project → load error
	rosterCalls map[string]int
	searchCalls int
}

func newProjectRolesClient() *projectRolesClient {
	return &projectRolesClient{
		mockClient:  newMockClient(),
		rosters:     map[string]map[string]string{},
		rosterErr:   map[string]error{},
		rosterCalls: map[string]int{},
	}
}

func (c *projectRolesClient) SearchIssues(ctx context.Context, jql string, limit int) ([]jira.Issue, error) {
	c.searchCalls++
	return c.mockClient.SearchIssues(ctx, jql, limit)
}

func (c *projectRolesClient) GetProjectRoleActors(_ context.Context, project string) (map[string]jira.ProjectRoleActors, error) {
	c.rosterCalls[project]++
	if err := c.rosterErr[project]; err != nil {
		return nil, err
	}
	out := map[string]jira.ProjectRoleActors{}
	for aid, role := range c.rosters[project] {
		ra, ok := out[role]
		if !ok {
			ra = jira.ProjectRoleActors{DirectUsers: map[string]bool{}}
		}
		ra.DirectUsers[aid] = true
		out[role] = ra
	}
	return out, nil
}

// multiProjectIssue is an open issue with numeric id, created an hour ago.
func multiProjectIssue(id, key string, now time.Time) jira.Issue {
	issue := authTestIssue(key, now)
	issue.ID = id
	return issue
}

// roleByIssue returns the actor.role the matcher saw for each issue key.
func roleByIssue(m *gateMatcher) map[string]string {
	out := map[string]string{}
	for _, ev := range m.events {
		out[ev.Entity.Key] = string(ev.Actor.Role)
	}
	return out
}

func TestSearchJQL(t *testing.T) {
	tests := []struct {
		name     string
		projects []string
		query    string
		want     string
	}{
		{
			name:     "one project, default predicate",
			projects: []string{"PROJ"},
			want:     `project = "PROJ" AND (statusCategory != Done) ORDER BY updated DESC`,
		},
		{
			name:     "one project, configured predicate with ORDER BY",
			projects: []string{"PROJ"},
			query:    `labels = "fullsend" ORDER BY updated DESC`,
			want:     `project = "PROJ" AND (labels = "fullsend") ORDER BY updated DESC`,
		},
		{
			name:     "two projects",
			projects: []string{"ALPHA", "BETA"},
			query:    `labels = "fullsend" ORDER BY created ASC`,
			want:     `project in ("ALPHA", "BETA") AND (labels = "fullsend") ORDER BY created ASC`,
		},
		{
			name:     "predicate OR stays inside the scope",
			projects: []string{"ALPHA", "BETA"},
			query:    `labels = a OR labels = b`,
			want:     `project in ("ALPHA", "BETA") AND (labels = a OR labels = b)`,
		},
		{
			name:     "predicate's own project clause cannot widen the scope",
			projects: []string{"ALPHA", "BETA"},
			query:    `project = OTHER OR project in (GAMMA) ORDER BY key`,
			want:     `project in ("ALPHA", "BETA") AND (project = OTHER OR project in (GAMMA)) ORDER BY key`,
		},
		{
			name:     "single-character key",
			projects: []string{"P"},
			query:    `labels = a`,
			want:     `project = "P" AND (labels = a)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(nil, nil, Options{JiraProjects: tt.projects, JQL: tt.query})
			got, err := p.searchJQL()
			if err != nil {
				t.Fatalf("searchJQL() error: %v", err)
			}
			if got != tt.want {
				t.Errorf("searchJQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSearchJQL_OneProjectMatchesConfigScoping pins that a one-item set
// searches exactly the JQL the single-project config path built before
// the poller took a project set.
func TestSearchJQL_OneProjectMatchesConfigScoping(t *testing.T) {
	for _, query := range []string{
		config.DefaultJiraPollQuery,
		`labels = "fullsend" AND statusCategory != Done ORDER BY updated DESC`,
		`labels = a OR labels = b`,
		`summary ~ "order by" AND status = Open`,
		`ORDER BY updated DESC`,
	} {
		p := New(nil, nil, Options{JiraProjects: []string{"PROJ"}, JQL: query})
		got, err := p.searchJQL()
		if err != nil {
			t.Fatalf("searchJQL(%q) error: %v", query, err)
		}
		if want := config.ScopeJQLToProject("PROJ", query); got != want {
			t.Errorf("searchJQL(%q) = %q, want %q", query, got, want)
		}
	}
}

func TestSearchJQL_Rejects(t *testing.T) {
	tests := []struct {
		name     string
		projects []string
		query    string
		wantErr  string
	}{
		{"nil set", nil, "", "no Jira project keys"},
		{"empty set", []string{}, "", "no Jira project keys"},
		{"empty key", []string{""}, "", `invalid Jira project key ""`},
		{"lowercase key", []string{"proj"}, "", `invalid Jira project key "proj"`},
		{"leading digit", []string{"1PROJ"}, "", `invalid Jira project key "1PROJ"`},
		{"quote injection", []string{`PROJ" OR project = "X`}, "", "invalid Jira project key"},
		{"malformed among valid", []string{"ALPHA", "be ta"}, "", `invalid Jira project key "be ta"`},
		{"duplicate key", []string{"ALPHA", "BETA", "ALPHA"}, "", `duplicate Jira project key "ALPHA"`},
		{"predicate closes the scope", []string{"PROJ"}, `labels = a) OR (project = OTHER`, "unbalanced parentheses"},
		{"predicate leaves a parenthesis open", []string{"PROJ"}, `(labels = a`, "unbalanced parentheses"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(nil, nil, Options{JiraProjects: tt.projects, JQL: tt.query})
			_, err := p.searchJQL()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("searchJQL() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestRun_InvalidScopeMakesNoJiraRequest: an empty or malformed project
// set (or an unscopable predicate) fails the cycle before the auth
// preflight or any other Jira request.
func TestRun_InvalidScopeMakesNoJiraRequest(t *testing.T) {
	for name, opts := range map[string]Options{
		"empty set":        {},
		"malformed key":    {JiraProjects: []string{"PROJ", "bad-key"}},
		"duplicate key":    {JiraProjects: []string{"PROJ", "PROJ"}},
		"unbalanced JQL":   {JiraProjects: []string{"PROJ"}, JQL: `a = b) OR (c = d`},
		"empty key in set": {JiraProjects: []string{""}},
	} {
		t.Run(name, func(t *testing.T) {
			p := newTestPoller(noRequestClient{}, nil, opts)
			err := p.Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), "invalid poll scope") {
				t.Fatalf("Run() error = %v, want an invalid poll scope error", err)
			}
		})
	}
}

// TestRun_TwoProjectsShareOneSearch: the set is searched once, scoped to
// both projects, so M and the pagination budget apply across the set and
// N selects across it.
func TestRun_TwoProjectsShareOneSearch(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newProjectRolesClient()
	mc.searchResult = []jira.Issue{
		multiProjectIssue("1", "ALPHA-1", now),
		multiProjectIssue("2", "BETA-1", now),
		multiProjectIssue("3", "ALPHA-2", now),
		multiProjectIssue("4", "BETA-2", now),
		multiProjectIssue("5", "ALPHA-3", now),
	}
	p := newTestPoller(mc, nil, Options{
		TargetRepo:   "acme/platform",
		JiraBaseURL:  "https://acme.atlassian.net",
		JiraProjects: []string{"ALPHA", "BETA"},
		JQL:          `labels = x OR project = OTHER ORDER BY created ASC`,
		M:            3,
		N:            2,
	})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if mc.searchCalls != 1 {
		t.Errorf("SearchIssues called %d times, want once for the whole set", mc.searchCalls)
	}
	if mc.lastLimit != 3 {
		t.Errorf("SearchIssues limit = %d, want M = 3 across the set", mc.lastLimit)
	}
	if want := `project in ("ALPHA", "BETA") AND (labels = x OR project = OTHER) ORDER BY created ASC`; mc.lastQuery != want {
		t.Errorf("JQL = %q, want %q", mc.lastQuery, want)
	}
	// N = 2 issues of the 3 candidates are processed: each processed issue
	// gets its lastCheck read under the lock.
	locked := 0
	for _, key := range []string{"ALPHA-1", "BETA-1", "ALPHA-2", "BETA-2", "ALPHA-3"} {
		if _, ok := mc.properties[key]; ok {
			locked++
		}
	}
	if locked > 2 {
		t.Errorf("%d issues were processed, want at most N = 2 across the set", locked)
	}
}

// TestRun_MixedProjectRoleResolution: each issue's actor resolves through
// its own project's roster, loaded once per project per cycle.
func TestRun_MixedProjectRoleResolution(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newProjectRolesClient()
	mc.rosters["ALPHA"] = map[string]string{"dev": "Developers"}
	mc.rosters["BETA"] = map[string]string{"dev": "Viewers", "lead": "Administrators"}
	mc.searchResult = []jira.Issue{
		multiProjectIssue("1", "ALPHA-1", now),
		multiProjectIssue("2", "BETA-1", now),
		multiProjectIssue("3", "ALPHA-2", now),
	}
	mc.comments["ALPHA-1"] = []jira.Comment{authTestComment("10", "dev", "/fs-code go", now.Add(-10*time.Minute))}
	mc.comments["BETA-1"] = []jira.Comment{authTestComment("20", "dev", "/fs-code go", now.Add(-10*time.Minute))}
	mc.comments["ALPHA-2"] = []jira.Comment{authTestComment("30", "lead", "/fs-code go", now.Add(-10*time.Minute))}

	m := newGateMatcher(harnessdispatch.AuthOptions{})
	p := newTestPoller(mc, m, Options{
		TargetRepo:   "acme/platform",
		JiraBaseURL:  "https://acme.atlassian.net",
		JiraProjects: []string{"ALPHA", "BETA"},
		N:            50,
	})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	got := roleByIssue(m)
	want := map[string]string{
		"ALPHA-1": "write",    // Developers in ALPHA
		"BETA-1":  "read",     // Viewers in BETA, not ALPHA's Developers
		"ALPHA-2": "external", // BETA's Administrator has no role in ALPHA
	}
	for key, role := range want {
		if got[key] != role {
			t.Errorf("actor.role on %s = %q, want %q", key, got[key], role)
		}
	}
	for _, project := range []string{"ALPHA", "BETA"} {
		if mc.rosterCalls[project] != 1 {
			t.Errorf("roster for %s loaded %d times, want once per cycle", project, mc.rosterCalls[project])
		}
	}
}

// TestRun_ProjectRosterFailureFailsClosedThenRetries: a roster load
// failure for any project of the set fails the cycle without advancing
// any issue's checkpoint, and the next cycle retries the same activity.
func TestRun_ProjectRosterFailureFailsClosedThenRetries(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newProjectRolesClient()
	mc.rosters["ALPHA"] = map[string]string{"dev": "Developers"}
	mc.rosters["BETA"] = map[string]string{"dev": "Developers"}
	mc.rosterErr["BETA"] = errAdminOnly
	mc.searchResult = []jira.Issue{
		multiProjectIssue("1", "ALPHA-1", now),
		multiProjectIssue("2", "BETA-1", now),
	}
	mc.comments["ALPHA-1"] = []jira.Comment{authTestComment("10", "dev", "/fs-code go", now.Add(-10*time.Minute))}
	mc.comments["BETA-1"] = []jira.Comment{authTestComment("20", "dev", "/fs-code go", now.Add(-10*time.Minute))}

	opts := Options{
		TargetRepo:   "acme/platform",
		JiraBaseURL:  "https://acme.atlassian.net",
		JiraProjects: []string{"ALPHA", "BETA"},
		N:            50,
	}
	p := newTestPoller(mc, newGateMatcher(harnessdispatch.AuthOptions{}), opts)
	err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "load project roles for BETA") {
		t.Fatalf("Run() error = %v, want a BETA role load failure", err)
	}
	if !errors.Is(err, errAdminOnly) {
		t.Errorf("Run() error = %v, want it to wrap the roster error", err)
	}
	for _, key := range []string{"ALPHA-1", "BETA-1"} {
		lastCheck, err := p.readLastCheck(context.Background(), key)
		if err != nil {
			t.Fatalf("readLastCheck(%s) error: %v", key, err)
		}
		if !lastCheck.IsZero() {
			t.Errorf("lastCheck of %s advanced to %v after a failed cycle, want unset", key, lastCheck)
		}
	}

	delete(mc.rosterErr, "BETA")
	m := newGateMatcher(harnessdispatch.AuthOptions{})
	p = newTestPoller(mc, m, opts)
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("retry Run() error: %v", err)
	}
	got := roleByIssue(m)
	for _, key := range []string{"ALPHA-1", "BETA-1"} {
		if got[key] != "write" {
			t.Errorf("retry actor.role on %s = %q, want write", key, got[key])
		}
		lastCheck, err := p.readLastCheck(context.Background(), key)
		if err != nil {
			t.Fatalf("readLastCheck(%s) error: %v", key, err)
		}
		if lastCheck.IsZero() {
			t.Errorf("lastCheck of %s not advanced after the retry succeeded", key)
		}
	}
}

// TestRun_DuplicateCanonicalIssueProcessedOnce: the same issue returned
// twice, under one key or under an old and a new key after a move within
// the set, is processed and dispatched once per cycle.
func TestRun_DuplicateCanonicalIssueProcessedOnce(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	mc := newProjectRolesClient()
	mc.rosters["ALPHA"] = map[string]string{"dev": "Developers"}
	mc.rosters["BETA"] = map[string]string{"dev": "Developers"}
	mc.searchResult = []jira.Issue{
		multiProjectIssue("1", "ALPHA-1", now),
		multiProjectIssue("1", "ALPHA-1", now),
		multiProjectIssue("2", "ALPHA-2", now),
		multiProjectIssue("2", "BETA-9", now), // ALPHA-2 moved to BETA
	}
	mc.comments["ALPHA-1"] = []jira.Comment{authTestComment("10", "dev", "/fs-code go", now.Add(-10*time.Minute))}
	mc.comments["ALPHA-2"] = []jira.Comment{authTestComment("20", "dev", "/fs-code go", now.Add(-10*time.Minute))}
	mc.comments["BETA-9"] = mc.comments["ALPHA-2"]

	p := newTestPoller(mc, &stubMatcher{agents: []string{"code"}}, Options{
		TargetRepo:   "acme/platform",
		JiraBaseURL:  "https://acme.atlassian.net",
		JiraProjects: []string{"ALPHA", "BETA"},
		N:            50,
	})
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	// Each canonical issue dispatches its opened event and its comment
	// once: no row is processed a second time.
	perIssue := map[string]int{}
	for _, d := range p.dispatches {
		perIssue[d.StatusNumber+"/"+d.EventType]++
	}
	want := map[string]int{"1/issue": 1, "1/comment": 1, "2/issue": 1, "2/comment": 1}
	if len(perIssue) != len(want) {
		t.Errorf("dispatches = %v, want %v", perIssue, want)
	}
	for k, n := range want {
		if perIssue[k] != n {
			t.Errorf("dispatches for %s = %d, want %d (all: %v)", k, perIssue[k], n, perIssue)
		}
	}
	if _, ok := mc.properties["BETA-9"]; ok {
		t.Error("the duplicate row BETA-9 of issue 2 was processed")
	}
}

func TestUniqueIssues(t *testing.T) {
	got := uniqueIssues([]jira.Issue{
		{ID: "1", Key: "A-1"},
		{ID: "2", Key: "A-2"},
		{ID: "1", Key: "B-1"},
		{Key: "A-3"},
		{Key: "A-3"},
		{Key: "A-4"},
	})
	var keys []string
	for _, issue := range got {
		keys = append(keys, issue.Key)
	}
	if want := "A-1,A-2,A-3,A-4"; strings.Join(keys, ",") != want {
		t.Errorf("uniqueIssues() keys = %v, want %s", keys, want)
	}
	if got := uniqueIssues(nil); len(got) != 0 {
		t.Errorf("uniqueIssues(nil) = %v, want empty", got)
	}
}
