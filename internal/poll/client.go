// Package poll implements the GitLab cron-polling event dispatch loop.
// It discovers events from the GitLab API, converts them to
// NormalizedEvents, routes them through the dispatch core, and
// dispatches agent stages via API-triggered pipelines.
package poll

import (
	"context"
	"time"
)

// GitLabClient defines the GitLab API surface the poller requires.
// This interface is separate from forge.Client because the poller
// needs GitLab-specific methods (label events, project events) that
// are not part of the forge-neutral abstraction. Phase 1 wiring will
// provide a concrete type that satisfies this interface, backed by
// the forge.Client credential and HTTP plumbing.
//
// All List* methods MUST exhaust pagination (per_page=100, follow
// x-next-page) and return the complete result set. Returning only page 1
// (GitLab default: 20 items) causes silent event loss.
type GitLabClient interface {
	ListIssuesUpdatedSince(ctx context.Context, owner, repo string, since time.Time) ([]Issue, error)
	ListMergeRequestsUpdatedSince(ctx context.Context, owner, repo string, since time.Time) ([]MergeRequest, error)
	// ListProjectEvents returns events matching targetType (use lowercase
	// "note" for the request param). The after parameter is date-only
	// (ISO 8601 date, exclusive): implementations must widen to at least
	// since.AddDate(0,0,-1) and apply client-side timestamp filtering.
	ListProjectEvents(ctx context.Context, owner, repo string, targetType string, after time.Time) ([]ProjectEvent, error)
	// ListIssueNotes MUST return notes in ascending created_at order.
	ListIssueNotes(ctx context.Context, owner, repo string, issueIID int) ([]Note, error)
	ListMergeRequestNotes(ctx context.Context, owner, repo string, mrIID int) ([]Note, error)
	// ListResourceLabelEvents MUST return events in ascending ID order
	// (the poller iterates in reverse to find the most recent "add").
	ListResourceLabelEvents(ctx context.Context, owner, repo string, issueIID int) ([]ResourceLabelEvent, error)
	GetCIVariable(ctx context.Context, owner, repo, name string) (string, error)
	// UpdateCIVariable upserts a CI variable: update if it exists,
	// create if it does not. GitLab CI/CD variable values are capped
	// at 10,000 characters.
	UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error
	GetAuthenticatedUser(ctx context.Context) (string, error)
	GetAuthenticatedUserID(ctx context.Context) (int, error)
	// CreateNoteAwardEmoji adds an emoji reaction. noteableType must be
	// "Issue" or "MergeRequest" to select the correct API endpoint.
	CreateNoteAwardEmoji(ctx context.Context, owner, repo string, noteableType string, noteableIID, noteID int, emoji string) error
	GetIssue(ctx context.Context, owner, repo string, issueIID int) (*Issue, error)
	GetMergeRequest(ctx context.Context, owner, repo string, mrIID int) (*MergeRequest, error)
	// GetMemberAccessLevel returns the access level for a project member.
	// Implementations MUST use the /members/all/:user_id endpoint to
	// include inherited (group-level) membership, not /members/:user_id
	// which only returns direct members.
	GetMemberAccessLevel(ctx context.Context, owner, repo string, userID int) (int, error)
	GetProjectPath(ctx context.Context, projectID int) (string, error)
	// CreatePipeline creates a new pipeline on the given ref with the
	// given variables. Returns the pipeline ID and web URL.
	CreatePipeline(ctx context.Context, owner, repo, ref string, variables map[string]string) (int64, string, error)
}

// Issue represents a GitLab issue as returned by the API.
// Author is a nested object in the GitLab v4 response.
type Issue struct {
	IID       int       `json:"iid"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Labels    []string  `json:"labels"`
	Author    UserRef   `json:"author"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MergeRequest represents a GitLab merge request as returned by the API.
// Fields are derived from nested API objects (author, merge_user);
// GitLab does not expose flat author_id/merged_by_id fields on MRs.
type MergeRequest struct {
	IID             int       `json:"iid"`
	Title           string    `json:"title"`
	State           string    `json:"state"`
	Labels          []string  `json:"labels"`
	SourceProjectID int       `json:"source_project_id"`
	TargetProjectID int       `json:"target_project_id"`
	SourceBranch    string    `json:"source_branch"`
	TargetBranch    string    `json:"target_branch"`
	Author          UserRef   `json:"author"`
	MergeUser       UserRef   `json:"merge_user"`
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
// The Bot field is not present in all API responses (notably the
// Notes API author object omits it). Use isBotEvent() for reliable
// bot detection, which combines the API field, botUserID, and
// username-pattern heuristics.
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
