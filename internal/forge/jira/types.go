// Package jira implements an HTTP client for the Jira Cloud REST API v3.
package jira

import "encoding/json"

// Issue represents a Jira issue.
type Issue struct {
	ID     string      `json:"id"`
	Key    string      `json:"key"`
	Self   string      `json:"self"`
	Fields IssueFields `json:"fields"`
}

// IssueFields contains the standard fields of a Jira issue.
type IssueFields struct {
	Summary     string       `json:"summary"`
	Description any          `json:"description"` // ADF object or string
	Status      Status       `json:"status"`
	Labels      []string     `json:"labels"`
	Reporter    User         `json:"reporter"`
	Created     string       `json:"created"`
	Updated     string       `json:"updated"`
	Comment     *CommentPage `json:"comment,omitempty"`
}

// Status represents the status of a Jira issue.
type Status struct {
	Name           string         `json:"name"`
	StatusCategory StatusCategory `json:"statusCategory"`
}

// StatusCategory groups statuses into broad categories.
// Key is one of "new", "indeterminate", or "done".
type StatusCategory struct {
	Key string `json:"key"`
}

// Comment represents a single comment on a Jira issue.
type Comment struct {
	ID   string `json:"id"`
	Body any    `json:"body"` // ADF object or string
	// Author is the account that originally created the comment;
	// UpdateAuthor is the account that last modified it (set by Jira on
	// edit). They differ when someone with Edit-All-Comments edits another
	// user's comment — the poller must attribute an edit-detected event to
	// the editor, not the author, to avoid running attacker-authored text
	// under the original author's role.
	Author       User   `json:"author"`
	UpdateAuthor User   `json:"updateAuthor"`
	Created      string `json:"created"`
	Updated      string `json:"updated"`
	// Properties holds entity properties when the comment was fetched
	// with ?expand=properties. Each property has a key and an arbitrary
	// JSON value. Properties are invisible to users in the Jira UI, so
	// they are used for sticky-comment marker storage instead of
	// embedding markers in visible ADF body text.
	Properties []CommentProperty `json:"properties,omitempty"`
}

// CommentProperty represents an entity property on a Jira comment.
// Properties are opaque key/value pairs stored alongside a comment
// that do not appear in the Jira UI.
type CommentProperty struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// CommentPage is a paginated list of comments.
type CommentPage struct {
	Comments   []Comment `json:"comments"`
	Total      int       `json:"total"`
	MaxResults int       `json:"maxResults"`
	StartAt    int       `json:"startAt"`
}

// ChangelogEntry represents a single changelog entry on a Jira issue.
type ChangelogEntry struct {
	ID      string       `json:"id"`
	Author  User         `json:"author"`
	Created string       `json:"created"`
	Items   []ChangeItem `json:"items"`
}

// ChangeItem describes a single field change within a changelog entry.
type ChangeItem struct {
	Field      string `json:"field"`
	From       string `json:"from"` // Stable ID of the previous value (e.g. status ID); survives renames
	To         string `json:"to"`   // Stable ID of the new value
	FromString string `json:"fromString"`
	ToString   string `json:"toString"`
}

// User represents a Jira user account.
type User struct {
	AccountID   string `json:"accountId"`
	Name        string `json:"name"` // Data Center/Server username; unset on Cloud. Unused today since this client only targets Cloud (see apiURL); kept for future Data Center support.
	DisplayName string `json:"displayName"`
	AccountType string `json:"accountType"` // "atlassian", "app", "customer"
	Active      bool   `json:"active"`
}

// SearchResult is the response from the POST /rest/api/3/search/jql endpoint.
// Uses cursor-based pagination (nextPageToken + isLast).
type SearchResult struct {
	Issues        []Issue `json:"issues"`
	NextPageToken string  `json:"nextPageToken,omitempty"`
	IsLast        bool    `json:"isLast"`
}

// EntityPropertyValue wraps a JSON value stored as a Jira entity property.
type EntityPropertyValue struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// ProjectRoleList is the response from GET /rest/api/3/project/{key}/role.
// It maps role names to their URLs.
// Example: {"Administrators": "https://.../role/10002", "Developers": "https://.../role/10001"}
type ProjectRoleList map[string]string

// ProjectRoleDetail is the response from GET /rest/api/3/project/{key}/role/{id}.
type ProjectRoleDetail struct {
	Name   string      `json:"name"`
	Actors []RoleActor `json:"actors"`
}

// RoleActor represents a member of a project role.
type RoleActor struct {
	ID          int             `json:"id"`
	DisplayName string          `json:"displayName"`
	Type        string          `json:"type"` // "atlassian-user-role-actor", "atlassian-group-role-actor"
	ActorUser   *RoleActorUser  `json:"actorUser,omitempty"`
	ActorGroup  *RoleActorGroup `json:"actorGroup,omitempty"`
}

// RoleActorUser contains the account ID of a role actor.
type RoleActorUser struct {
	AccountID string `json:"accountId"`
}

// RoleActorGroup identifies a group granted a project role directly
// (as opposed to an individual user).
type RoleActorGroup struct {
	GroupID string `json:"groupId"`
	Name    string `json:"name"`
}

// UserGroupInfo represents a group returned by GET /rest/api/3/user/groups.
// Used for per-actor role resolution that checks the actor's group
// memberships instead of enumerating all members of a role-assigned group.
type UserGroupInfo struct {
	Name    string `json:"name"`
	GroupID string `json:"groupId"`
	Self    string `json:"self"`
}

// ProjectRoleActors describes the direct users and group assignments for
// a project role without enumerating group members, so it is not subject
// to the group/member pagination cap.
type ProjectRoleActors struct {
	DirectUsers map[string]bool // accountIDs directly assigned to this role
	GroupIDs    []string        // group IDs assigned to this role
}

// changelogPage is the paginated response from the changelog API.
type changelogPage struct {
	Values     []ChangelogEntry `json:"values"`
	Total      int              `json:"total"`
	MaxResults int              `json:"maxResults"`
	StartAt    int              `json:"startAt"`
	IsLast     bool             `json:"isLast"`
}
