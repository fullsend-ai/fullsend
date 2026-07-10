// Package poll implements the GitLab cron-polling event dispatch loop.
// It discovers events from the GitLab API, converts them to
// NormalizedEvents, routes them through the dispatch core, and
// triggers child pipelines for matched stages.
package poll

import (
	"context"
	"time"
)

// GitLabClient defines the GitLab API surface the poller requires.
// The interface is satisfied by the GitLab forge client (Phase 1).
type GitLabClient interface {
	ListIssuesUpdatedSince(ctx context.Context, owner, repo string, since time.Time) ([]Issue, error)
	ListMergeRequestsUpdatedSince(ctx context.Context, owner, repo string, since time.Time) ([]MergeRequest, error)
	ListProjectEvents(ctx context.Context, owner, repo string, targetType string, after time.Time) ([]ProjectEvent, error)
	ListIssueNotes(ctx context.Context, owner, repo string, issueIID int) ([]Note, error)
	ListMergeRequestNotes(ctx context.Context, owner, repo string, mrIID int) ([]Note, error)
	ListResourceLabelEvents(ctx context.Context, owner, repo string, issueIID int) ([]ResourceLabelEvent, error)
	GetCIVariable(ctx context.Context, owner, repo, name string) (string, error)
	UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error
	GetAuthenticatedUser(ctx context.Context) (string, error)
	CreateNoteAwardEmoji(ctx context.Context, owner, repo string, noteableIID, noteID int, emoji string) error
	GetIssue(ctx context.Context, owner, repo string, issueIID int) (*Issue, error)
	GetMemberAccessLevel(ctx context.Context, owner, repo string, userID int) (int, error)
	GetProjectPath(ctx context.Context, projectID int) (string, error)
}

// Issue represents a GitLab issue as returned by the API.
type Issue struct {
	IID       int       `json:"iid"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Labels    []string  `json:"labels"`
	AuthorID  int       `json:"author_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MergeRequest represents a GitLab merge request as returned by the API.
type MergeRequest struct {
	IID             int       `json:"iid"`
	Title           string    `json:"title"`
	State           string    `json:"state"`
	Labels          []string  `json:"labels"`
	SourceProjectID int       `json:"source_project_id"`
	TargetProjectID int       `json:"target_project_id"`
	SourceBranch    string    `json:"source_branch"`
	TargetBranch    string    `json:"target_branch"`
	AuthorID        int       `json:"author_id"`
	MergedByID      int       `json:"merged_by_id"`
	MergedBy        UserRef   `json:"merged_by"`
	MergedAt        time.Time `json:"merged_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Note represents a GitLab note (comment) on an issue or MR.
type Note struct {
	ID        int       `json:"id"`
	Body      string    `json:"body"`
	Author    UserRef   `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

// UserRef is a minimal user reference from the GitLab API.
type UserRef struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot"`
}

// ProjectEvent represents an event from the GitLab Events API.
type ProjectEvent struct {
	ID        int       `json:"id"`
	Author    UserRef   `json:"author"`
	Note      EventNote `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

// EventNote is the embedded note object in a project event.
type EventNote struct {
	ID           int    `json:"id"`
	NoteableType string `json:"noteable_type"`
	NoteableIID  int    `json:"noteable_iid"`
	Body         string `json:"body"`
}

// ResourceLabelEvent represents a label change event from the GitLab API.
type ResourceLabelEvent struct {
	ID     int    `json:"id"`
	Action string `json:"action"`
	Label  struct {
		Name string `json:"name"`
	} `json:"label"`
	User UserRef `json:"user"`
}
