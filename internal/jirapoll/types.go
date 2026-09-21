package jirapoll

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// JiraClient defines the Jira API surface the poller requires.
// Implemented by jira.LiveClient; mocked in tests.
type JiraClient interface {
	// SearchIssues executes a JQL search and returns up to limit matching
	// issues, stopping pagination once that many results are collected.
	// A limit <= 0 returns all matching issues (bounded by the
	// implementation's own pagination cap).
	SearchIssues(ctx context.Context, jql string, limit int) ([]jira.Issue, error)
	// SearchIssuesPage executes a single-page JQL search with cursor pagination.
	SearchIssuesPage(ctx context.Context, jql string, maxResults int, nextPageToken string) (*jira.SearchResult, error)
	GetIssue(ctx context.Context, issueIDOrKey string) (*jira.Issue, error)
	// GetStatus resolves a status name to its statusCategory, used to
	// classify status transitions (closed/reopened) without relying on
	// locale/workflow-specific status name substrings.
	GetStatus(ctx context.Context, idOrName string) (*jira.Status, error)
	ListComments(ctx context.Context, issueIDOrKey string) ([]jira.Comment, error)
	ListChangelog(ctx context.Context, issueIDOrKey string) ([]jira.ChangelogEntry, error)
	GetEntityProperty(ctx context.Context, issueIDOrKey, propertyKey string) (json.RawMessage, error)
	SetEntityProperty(ctx context.Context, issueIDOrKey, propertyKey string, value any) error
	DeleteEntityProperty(ctx context.Context, issueIDOrKey, propertyKey string) error
	GetMyself(ctx context.Context) (*jira.User, error)
	// GetProjectRoleActors returns direct users and group assignments per
	// role without enumerating group members (no pagination cap).
	GetProjectRoleActors(ctx context.Context, projectKey string) (map[string]jira.ProjectRoleActors, error)
	// GetUserGroups returns the groups a specific user belongs to, for
	// per-actor role resolution that avoids the group/member pagination cap.
	GetUserGroups(ctx context.Context, accountID string) ([]jira.UserGroupInfo, error)
	// ListRemoteLinks returns the remote issue links (web links) attached to an issue.
	ListRemoteLinks(ctx context.Context, issueIDOrKey string) ([]jira.RemoteLink, error)
}

// Compile-time interface check.
var _ JiraClient = (*jira.LiveClient)(nil)

// Options configures the Jira poller.
type Options struct {
	TargetRepo              string        // GitHub repo slug where agents run (e.g., "acme/platform")
	JiraBaseURL             string        // Jira instance base URL
	JiraProject             string        // Jira project key for default JQL
	JiraComponent           string        // Jira component name for candidate scoping
	JQL                     string        // Custom JQL override
	OutputPath              string        // Path to write dispatch records JSON
	M                       int           // Max candidate issues per cycle (default: 50)
	N                       int           // Max issues to process per cycle (default: 5)
	StaleThreshold          time.Duration // Lock stale threshold (default: 900s)
	FirstPollBackfillWindow time.Duration // How far back to backfill comments/changelog on an issue's first poll (default: 24h)
}

// JiraEvent is the intermediate event representation, analogous to poll.RoutableEvent.
type JiraEvent struct {
	Type      string    // "comment_added", "label_changed", "opened", "reopened", "edited", "closed"
	IssueID   string    // Jira numeric issue ID
	IssueKey  string    // e.g., "PROJ-123"
	IssueURL  string    // Browse URL
	UpdatedAt time.Time // When this event occurred
	Labels    []string  // Current labels at event time

	// Comment fields (when Type == "comment_added")
	CommentID     string
	CommentBody   string // Plain text extracted from ADF or raw string
	CommentAuthor jira.User
	CommentEdited bool // Detected via updated > created rather than a new comment

	// Changelog fields (when Type == "label_changed")
	ChangedLabel string
	LabelAction  string // "added" or "removed"

	// Changelog author (label changes, status changes, edits)
	ChangeAuthor jira.User

	// Issue reporter (for is_entity_author check)
	Reporter jira.User
}

// Key returns a deduplication key for the event.
func (e JiraEvent) Key() string {
	if e.CommentID != "" {
		return fmt.Sprintf("comment-%s", e.CommentID)
	}
	if e.ChangedLabel != "" {
		return fmt.Sprintf("label-%s-%s-%s-%d", e.IssueKey, e.ChangedLabel, e.LabelAction, e.UpdatedAt.Unix())
	}
	return fmt.Sprintf("%s-%s-%d", e.Type, e.IssueKey, e.UpdatedAt.Unix())
}

// LockValue is stored in the Jira entity property for coordination.
type LockValue struct {
	ID string `json:"id"` // Poll cycle UUID
	TS string `json:"ts"` // RFC3339 timestamp
	// Phase is reserved for the two-phase (pending → running) lock handoff
	// described by ADR 0063. Today it is always written as "pending" and
	// never read: lock ownership through dispatch scheduling is a tracked
	// follow-up (see the KNOWN LIMITATION notes in processIssue).
	Phase string `json:"phase"` // "pending" or "running"
}
