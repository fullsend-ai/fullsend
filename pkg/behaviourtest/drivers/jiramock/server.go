// Package jiramock provides a stateful mock Jira REST API server for
// behaviour tests. The mock implements the endpoints that jira.LiveClient calls,
// backed by in-memory state that step definitions manipulate between requests.
package jiramock

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// State holds the mutable mock data. All methods are safe for concurrent use.
type State struct {
	mu         sync.Mutex
	issues     map[string]*jira.Issue                // key → issue
	comments   map[string][]jira.Comment             // issueKey → comments
	changelog  map[string][]jira.ChangelogEntry      // issueKey → changelog
	properties map[string]map[string]json.RawMessage // issueKey → propKey → value
	nextID     int
	roleGroups map[string]string   // role name → group ID, for roles backed by a group actor (in addition to the canned "Developers" direct-user role)
	userGroups map[string][]string // accountID → group IDs, backing GET /user/groups
}

// AddIssue inserts an issue with the given key and labels. A synthetic
// numeric ID and timestamp are assigned automatically.
func (s *State) AddIssue(key string, labels []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	id := strconv.Itoa(s.nextID + 10000)

	s.issues[key] = &jira.Issue{
		ID:  id,
		Key: key,
		Fields: jira.IssueFields{
			Summary: "Test issue " + key,
			Status: jira.Status{
				Name:           "Open",
				StatusCategory: jira.StatusCategory{Key: "new"},
			},
			Labels: labels,
			Reporter: jira.User{
				AccountID:   "reporter-001",
				DisplayName: "Test Reporter",
				AccountType: "atlassian",
				Active:      true,
			},
			Created: time.Now().Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
			Updated: time.Now().Format("2006-01-02T15:04:05.000-0700"),
		},
	}
}

// AddComment appends a comment to the given issue. The comment is authored
// by a canned human user (not a bot).
func (s *State) AddComment(issueKey, body string) {
	s.AddCommentFromActor(issueKey, body, "commenter-001", "Test Commenter")
}

// AddCommentFromActor appends a comment to the given issue, authored by
// the given account. Used to test role resolution for actors other than
// the default canned commenter.
func (s *State) AddCommentFromActor(issueKey, body, accountID, displayName string) {
	s.AddCommentFromActorAt(issueKey, body, accountID, displayName, time.Now())
}

// AddCommentFromActorAt is AddCommentFromActor with an explicit creation
// time. Tests that run several poll cycles use it to place each comment
// before the poller's change-detection safety margin and after the
// previous cycle's checkpoint.
func (s *State) AddCommentFromActorAt(issueKey, body, accountID, displayName string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := strconv.Itoa(len(s.comments[issueKey]) + 1)
	now := at.Format("2006-01-02T15:04:05.000-0700")

	s.comments[issueKey] = append(s.comments[issueKey], jira.Comment{
		ID:   id,
		Body: body,
		Author: jira.User{
			AccountID:   accountID,
			DisplayName: displayName,
			AccountType: "atlassian",
			Active:      true,
		},
		Created: now,
		Updated: now,
	})
}

// SetRoleGroup registers a project role as backed by the given group ID,
// in addition to the canned "Developers" role (whose direct users are
// commenter-001 and changer-001). Backs GET /project/{key}/role[/{id}]
// with a group-actor role, for testing per-actor group-based role
// resolution (GetProjectRoleActors + GetUserGroups) end-to-end.
func (s *State) SetRoleGroup(roleName, groupID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.roleGroups == nil {
		s.roleGroups = make(map[string]string)
	}
	s.roleGroups[roleName] = groupID
}

// SetUserGroups registers the groups a Jira user belongs to, backing
// GET /user/groups for per-actor role resolution.
func (s *State) SetUserGroups(accountID string, groupIDs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userGroups == nil {
		s.userGroups = make(map[string][]string)
	}
	s.userGroups[accountID] = groupIDs
}

// AddLabelChange appends a changelog entry representing a label addition
// and updates the issue's label list.
func (s *State) AddLabelChange(issueKey, label string) {
	s.AddLabelChangeAt(issueKey, label, time.Now())
}

// AddLabelChangeAt is AddLabelChange with an explicit changelog time.
func (s *State) AddLabelChangeAt(issueKey, label string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	issue, ok := s.issues[issueKey]
	if !ok {
		return
	}

	// Build from/to strings from current labels.
	fromLabels := strings.Join(issue.Fields.Labels, " ")
	issue.Fields.Labels = append(issue.Fields.Labels, label)
	toLabels := strings.Join(issue.Fields.Labels, " ")

	id := strconv.Itoa(len(s.changelog[issueKey]) + 1)
	now := at.Format("2006-01-02T15:04:05.000-0700")

	s.changelog[issueKey] = append(s.changelog[issueKey], jira.ChangelogEntry{
		ID: id,
		Author: jira.User{
			AccountID:   "changer-001",
			DisplayName: "Test Changer",
			AccountType: "atlassian",
			Active:      true,
		},
		Created: now,
		Items: []jira.ChangeItem{
			{
				Field:      "labels",
				FromString: fromLabels,
				ToString:   toLabels,
			},
		},
	})
}

// NewServer creates a mock Jira REST API server and returns the httptest
// server and its backing state. The caller should defer srv.Close().
func NewServer() (*httptest.Server, *State) {
	state := &State{
		issues:     make(map[string]*jira.Issue),
		comments:   make(map[string][]jira.Comment),
		changelog:  make(map[string][]jira.ChangelogEntry),
		properties: make(map[string]map[string]json.RawMessage),
	}

	mux := http.NewServeMux()
	registerHandlers(mux, state)
	srv := httptest.NewServer(requireAuth(mux))
	return srv, state
}

// requireAuth rejects requests without a plausible Basic or Bearer
// Authorization header, mirroring the auth methods jira.LiveClient
// supports. This catches misconfiguration in the real client early,
// since a request with no auth header would otherwise be served
// identically to an authenticated one.
func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Basic ") && !strings.HasPrefix(auth, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"errorMessages": []string{"Unauthorized"},
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func registerHandlers(mux *http.ServeMux, s *State) {
	mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		handleSearch(w, r, s)
	})
	mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		handleMyself(w, r)
	})
	// Catch-all for /rest/api/3/ routes using path-based dispatch.
	mux.HandleFunc("/rest/api/3/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/rest/api/3/")
		handleAPIRoute(w, r, s, path)
	})
}

func handleSearch(w http.ResponseWriter, r *http.Request, s *State) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	issues := make([]jira.Issue, 0, len(s.issues))
	for _, issue := range s.issues {
		issues = append(issues, *issue)
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, jira.SearchResult{
		Issues: issues,
		IsLast: true,
	})
}

func handleMyself(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jira.User{
		AccountID:   "bot-001",
		DisplayName: "Fullsend Bot",
		AccountType: "app",
		Active:      true,
	})
}

func handleAPIRoute(w http.ResponseWriter, r *http.Request, s *State, path string) {
	// /issue/{key}/comment
	// /issue/{key}/changelog
	// /issue/{key}/properties/{propKey}
	// /project/{key}/role
	// /project/{key}/role/{id}
	// /user/groups

	parts := strings.Split(path, "/")

	if len(parts) >= 2 && parts[0] == "project" {
		handleProjectRoute(w, r, s, parts)
		return
	}

	if len(parts) >= 2 && parts[0] == "issue" {
		handleIssueRoute(w, r, s, parts)
		return
	}

	if len(parts) >= 2 && parts[0] == "user" && parts[1] == "groups" {
		handleUserGroups(w, r, s)
		return
	}

	http.NotFound(w, r)
}

// groupBackedRoleNames returns the names of roles registered via
// SetRoleGroup, sorted for deterministic role-ID assignment across the
// separate list and detail requests below.
func groupBackedRoleNames(s *State) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.roleGroups))
	for name := range s.roleGroups {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func handleProjectRoute(w http.ResponseWriter, _ *http.Request, s *State, parts []string) {
	// GET /project/{key}/role → role list
	// GET /project/{key}/role/{id} → role detail
	if len(parts) < 3 || parts[2] != "role" {
		http.NotFound(w, nil)
		return
	}

	// Group-backed roles registered via SetRoleGroup get role IDs
	// starting at 10002, assigned in sorted-name order so the list and
	// detail responses agree on which ID maps to which role.
	groupRoleNames := groupBackedRoleNames(s)

	if len(parts) == 3 {
		roleList := jira.ProjectRoleList{
			"Developers": fmt.Sprintf("http://localhost/rest/api/3/project/%s/role/10001", parts[1]),
		}
		for i, name := range groupRoleNames {
			roleList[name] = fmt.Sprintf("http://localhost/rest/api/3/project/%s/role/%d", parts[1], 10002+i)
		}
		writeJSON(w, http.StatusOK, roleList)
		return
	}

	if len(parts) == 4 {
		if parts[3] == "10001" {
			// Role detail — return the commenter and changer as developers.
			writeJSON(w, http.StatusOK, jira.ProjectRoleDetail{
				Name: "Developers",
				Actors: []jira.RoleActor{
					{
						ID:          1,
						DisplayName: "Test Commenter",
						Type:        "atlassian-user-role-actor",
						ActorUser:   &jira.RoleActorUser{AccountID: "commenter-001"},
					},
					{
						ID:          2,
						DisplayName: "Test Changer",
						Type:        "atlassian-user-role-actor",
						ActorUser:   &jira.RoleActorUser{AccountID: "changer-001"},
					},
				},
			})
			return
		}
		for i, name := range groupRoleNames {
			if parts[3] == strconv.Itoa(10002+i) {
				s.mu.Lock()
				groupID := s.roleGroups[name]
				s.mu.Unlock()
				writeJSON(w, http.StatusOK, jira.ProjectRoleDetail{
					Name: name,
					Actors: []jira.RoleActor{
						{
							ID:          3,
							DisplayName: name + "-group",
							Type:        "atlassian-group-role-actor",
							ActorGroup:  &jira.RoleActorGroup{GroupID: groupID, Name: name + "-group"},
						},
					},
				})
				return
			}
		}
	}
	http.NotFound(w, nil)
}

func handleUserGroups(w http.ResponseWriter, r *http.Request, s *State) {
	accountID := r.URL.Query().Get("accountId")
	s.mu.Lock()
	groupIDs := s.userGroups[accountID]
	s.mu.Unlock()

	groups := make([]jira.UserGroupInfo, 0, len(groupIDs))
	for _, gid := range groupIDs {
		groups = append(groups, jira.UserGroupInfo{GroupID: gid, Name: gid})
	}
	writeJSON(w, http.StatusOK, groups)
}

func handleIssueRoute(w http.ResponseWriter, r *http.Request, s *State, parts []string) {
	issueKey := parts[1]

	if len(parts) == 2 {
		handleGetIssue(w, s, issueKey)
		return
	}

	switch parts[2] {
	case "comment":
		handleComments(w, s, issueKey)
	case "changelog":
		handleChangelog(w, s, issueKey)
	case "properties":
		if len(parts) < 4 {
			http.NotFound(w, r)
			return
		}
		propKey := strings.Join(parts[3:], "/")
		handleProperty(w, r, s, issueKey, propKey)
	default:
		http.NotFound(w, r)
	}
}

func handleGetIssue(w http.ResponseWriter, s *State, key string) {
	s.mu.Lock()
	issue, ok := s.issues[key]
	s.mu.Unlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"errorMessages": []string{"Issue does not exist"},
		})
		return
	}
	writeJSON(w, http.StatusOK, issue)
}

func handleComments(w http.ResponseWriter, s *State, issueKey string) {
	s.mu.Lock()
	comments := s.comments[issueKey]
	s.mu.Unlock()

	if comments == nil {
		comments = []jira.Comment{}
	}

	writeJSON(w, http.StatusOK, jira.CommentPage{
		Comments:   comments,
		Total:      len(comments),
		MaxResults: 100,
		StartAt:    0,
	})
}

func handleChangelog(w http.ResponseWriter, s *State, issueKey string) {
	s.mu.Lock()
	entries := s.changelog[issueKey]
	s.mu.Unlock()

	if entries == nil {
		entries = []jira.ChangelogEntry{}
	}

	writeJSON(w, http.StatusOK, struct {
		Values     []jira.ChangelogEntry `json:"values"`
		Total      int                   `json:"total"`
		MaxResults int                   `json:"maxResults"`
		StartAt    int                   `json:"startAt"`
		IsLast     bool                  `json:"isLast"`
	}{
		Values:     entries,
		Total:      len(entries),
		MaxResults: 100,
		StartAt:    0,
		IsLast:     true,
	})
}

func handleProperty(w http.ResponseWriter, r *http.Request, s *State, issueKey, propKey string) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		props := s.properties[issueKey]
		var val json.RawMessage
		if props != nil {
			val = props[propKey]
		}
		s.mu.Unlock()

		if val == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"errorMessages": []string{fmt.Sprintf("Property '%s' not found", propKey)},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"key":   propKey,
			"value": json.RawMessage(val),
		})

	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		s.mu.Lock()
		if s.properties[issueKey] == nil {
			s.properties[issueKey] = make(map[string]json.RawMessage)
		}
		s.properties[issueKey][propKey] = json.RawMessage(body)
		s.mu.Unlock()

		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		s.mu.Lock()
		if props := s.properties[issueKey]; props != nil {
			delete(props, propKey)
		}
		s.mu.Unlock()

		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
