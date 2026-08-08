// Package tracker defines a narrow, forge-agnostic interface for reading
// and writing issue content (title, body, comments), keyed by
// (project string, number int) rather than the (owner, repo string, number
// int) shape used by forge.Client.
//
// forge.Client already covers this surface for GitHub and GitLab, but it
// stays scoped to git-hosting operations — Jira is explicitly not a forge
// (it has no branches, pull requests, or CI). Keying by a single project
// string lets a future Jira implementation use its natural project key
// (e.g. "PROJECT") instead of forcing an owner/repo split that Jira
// doesn't have; the issue number is passed separately, as with GitHub and
// GitLab.
//
// This package only defines the interface and thin adapters over
// forge.Client (see ForgeClient). Nothing calls tracker.Client yet.
package tracker

import (
	"context"
	"errors"
)

// ErrNotFound indicates a requested issue or comment was not found.
// Implementations of Client must return an error satisfying errors.Is(err,
// ErrNotFound) — checkable via IsNotFound — for missing resources, rather
// than requiring callers to reach into a specific tracker backend (e.g.
// forge.ErrNotFound) to detect this case.
var ErrNotFound = errors.New("not found")

// IsNotFound reports whether err indicates a requested issue or comment
// was not found.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// Issue represents an issue's content, independent of the tracker backend.
type Issue struct {
	Number int
	Title  string
	Body   string
	URL    string
	Labels []string
}

// Comment represents a comment on an issue.
//
// ID is a string for JSON round-tripping safety and to allow for
// non-numeric IDs from a possible future tracker, even though GitHub,
// GitLab, and Jira comment IDs are all numeric under the hood. Callers
// that need to update a comment pass the ID back verbatim via
// UpdateComment.
type Comment struct {
	ID        string
	HTMLURL   string
	Body      string
	Author    string
	CreatedAt string
}

// Client abstracts issue-content read/write operations across trackers
// (GitHub, GitLab, and eventually Jira). Project identifies the issue's
// container: "owner/repo" for GitHub/GitLab, a Jira project key for Jira.
//
// Implementations must return an error satisfying IsNotFound when the
// requested issue or comment doesn't exist.
type Client interface {
	// GetIssue returns the issue identified by project and number.
	GetIssue(ctx context.Context, project string, number int) (*Issue, error)
	// ListComments returns all comments on the issue identified by project and number.
	ListComments(ctx context.Context, project string, number int) ([]Comment, error)
	// CreateComment adds a new comment with the given body to the issue.
	CreateComment(ctx context.Context, project string, number int, body string) (*Comment, error)
	// UpdateComment updates the body of commentID on the issue (project, number).
	// number is included because Jira requires the issue key to update a comment.
	UpdateComment(ctx context.Context, project string, number int, commentID string, body string) error
}
