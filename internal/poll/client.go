// Package poll implements the GitLab cron-polling event dispatch loop.
// It discovers events from the GitLab API, converts them to
// NormalizedEvents, routes them through the dispatch core, and
// dispatches agent stages via API-triggered pipelines.
package poll

import (
	"context"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
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
	// GetFileContentAtRef retrieves a file at a specific ref (branch,
	// tag, or SHA). Returns forge.ErrNotFound if the file or ref does
	// not exist. Used to load the HMAC-signed poll-state document.
	GetFileContentAtRef(ctx context.Context, owner, repo, path, ref string) ([]byte, error)
	// GetBranchRef returns the HEAD commit SHA for the named branch.
	// Returns forge.ErrNotFound if the branch does not exist. Used to
	// pin the CAS parent SHA at poll-state load time.
	GetBranchRef(ctx context.Context, owner, repo, branch string) (string, error)
	// CommitFileToBranch commits a single file to branch without force.
	// expectedSHA is the branch tip observed at load time and is sent as
	// start_sha so a concurrent writer surfaces forge.ErrNonFastForward.
	// An empty expectedSHA (no branch observed at load time) still commits
	// with start_sha pinned to the repository root, but leaves force unset
	// so a concurrent first writer racing branch creation surfaces the same
	// forge.ErrNonFastForward instead of being silently overwritten.
	// The commit message is suffixed with [skip ci] when not already present.
	CommitFileToBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte, expectedSHA string) error
	// ForceCommitFileToBranch force-updates branch to a single-file
	// commit re-rooted on a fixed base SHA. The branch is created if
	// it does not exist. History is pruned to base + 1 commit.
	// Used for install-time seeding; runtime persist uses CommitFileToBranch.
	ForceCommitFileToBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error
	// DeleteRef deletes a git ref (e.g., "heads/fullsend-poll-state-slash").
	// Returns forge.ErrNotFound if the ref does not exist. Used to
	// discard a tampered poll-state branch.
	DeleteRef(ctx context.Context, owner, repo, refPath string) error
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
	// CreatePipelineWithInputs creates a new pipeline on the given ref
	// using typed GitLab CI/CD pipeline inputs (spec:inputs) instead of
	// user-defined pipeline variables. Unlike CreatePipeline, this
	// remains usable when a project's
	// ci_pipeline_variables_minimum_override_role is
	// forge.PipelineVarOverrideNoOneAllowed, because that setting does
	// not govern pipeline inputs (#7850). dispatch() uses this for a
	// target repository whose committed wrapper declares the typed
	// contract (see usesTypedDispatch) and falls back to CreatePipeline
	// otherwise, since a poller binary upgrade is not synchronized with
	// that repository's own scaffold migration.
	CreatePipelineWithInputs(ctx context.Context, owner, repo, ref string, inputs map[string]forge.PipelineInputValue) (int64, string, error)
}

// Issue represents a GitLab issue as returned by the API.
// Author is a nested object in the GitLab v4 response.
type Issue struct {
	IID       int       `json:"iid"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Labels    []string  `json:"labels"`
	Author    UserRef   `json:"author"`
	ClosedBy  UserRef   `json:"closed_by"`
	ClosedAt  time.Time `json:"closed_at"`
	CreatedAt time.Time `json:"created_at"`
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
	ClosedBy        UserRef   `json:"closed_by"`
	MergedAt        time.Time `json:"merged_at"`
	ClosedAt        time.Time `json:"closed_at"`
	CreatedAt       time.Time `json:"created_at"`
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
	User      UserRef   `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}
