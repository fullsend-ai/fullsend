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
// forge.Client (see ForgeClient). Consumers include statuscomment
// (run-status notifications) and reconcilestatus (orphan cleanup).
package tracker

import (
	"context"
	"encoding/json"
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

// ErrNotSupported indicates the tracker backend does not support the
// requested operation (e.g. typed issue links on GitHub or GitLab).
var ErrNotSupported = errors.New("not supported by this tracker")

// IsNotSupported reports whether err indicates the tracker backend does
// not support the requested operation.
func IsNotSupported(err error) bool {
	return errors.Is(err, ErrNotSupported)
}

// Body is Markdown-formatted issue/comment text, as produced by GitHub and
// GitLab. Jira doesn't speak Markdown — its v3 API requires comment and
// description bodies in Atlassian Document Format (ADF) and rejects plain
// strings outright. A Jira Client implementation is responsible for
// converting Body to and from ADF; a naive pass-through (wrapping the raw
// Markdown string in a single ADF text node) doesn't just lose formatting,
// it actively corrupts content — Jira's plain-text rendering path
// interprets stray Markdown characters (e.g. braces in code samples) as
// wiki-markup and mangles the surrounding text.
type Body string

// Issue represents an issue's content, independent of the tracker backend.
type Issue struct {
	Number int
	Title  string
	Body   Body
	URL    string
	Labels []string
	// IssueType is the tracker's issue type name (e.g. "Bug", "Story").
	// Set by the Jira client; empty for trackers that don't report one.
	IssueType string
	// CustomFields maps tracker-specific custom field IDs (e.g. Jira's
	// "customfield_10875") to their raw JSON values. Set by the Jira
	// client; nil for GitHub and GitLab.
	CustomFields map[string]json.RawMessage
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
	Body      Body
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
	CreateComment(ctx context.Context, project string, number int, body Body) (*Comment, error)
	// UpdateComment updates the body of commentID on the issue (project, number).
	// number is included because Jira requires the issue key to update a comment.
	UpdateComment(ctx context.Context, project string, number int, commentID string, body Body) error
	// DeleteComment removes the comment identified by commentID from the issue.
	// number is included because Jira requires the issue key to delete a comment.
	DeleteComment(ctx context.Context, project string, number int, commentID string) error
}

// StatusCommentClient is an optional capability for trackers that can store
// run-status identity and lifecycle state outside the visible comment body.
// Jira implements this with comment entity properties. Trackers without this
// capability keep using invisible HTML markers in comment bodies.
type StatusCommentClient interface {
	CreateStatusComment(ctx context.Context, project string, number int, body Body, marker string, terminal bool) (*Comment, error)
	UpdateStatusComment(ctx context.Context, project string, number int, commentID string, body Body, marker string, terminal bool) error
	FindStatusComment(ctx context.Context, project string, number int, marker string) (comment *Comment, terminal bool, err error)
	IsStatusComment(ctx context.Context, project string, number int, commentID string) (bool, error)
}

// Reactor is an optional capability for adding and removing emoji
// reactions on issues and comments. Tracker reaction support varies:
// GitHub and GitLab support reactions on both issues and comments;
// Jira Cloud supports reactions on comments but not on issues. Because
// the interface includes issue-level reactions, JiraClient does not
// implement Reactor currently — adding Jira comment-reaction support
// is straightforward once needed. Consumers should type-assert their
// tracker.Client to Reactor before calling reaction methods and
// silently skip reactions when the tracker does not implement it.
type Reactor interface {
	// AddIssueReaction adds an emoji reaction to an issue or pull request.
	// content is the reaction type (e.g. "eyes", "+1", "confused").
	AddIssueReaction(ctx context.Context, project string, number int, content string) (id int64, err error)
	// DeleteIssueReaction removes a previously added issue reaction by ID.
	DeleteIssueReaction(ctx context.Context, project string, number int, reactionID int64) error
	// AddCommentReaction adds an emoji reaction to a specific comment.
	AddCommentReaction(ctx context.Context, project string, number int, commentID string, content string) (id int64, err error)
	// DeleteCommentReaction removes a previously added comment reaction by ID.
	DeleteCommentReaction(ctx context.Context, project string, number int, commentID string, reactionID int64) error
}

// Linker is an optional capability for creating typed links between
// issues. Jira implements it with issue links; GitHub and GitLab have no
// first-class typed issue links and do not implement it. Consumers should
// type-assert their tracker.Client to Linker and report ErrNotSupported
// when the tracker does not implement it.
type Linker interface {
	// LinkIssues creates a link of type linkType from the issue
	// (fromProject, fromNumber) to the issue (toProject, toNumber).
	// linkType is passed through verbatim; its valid values and
	// direction semantics are tracker-specific (for Jira, link type
	// names such as "Blocks" are instance-specific).
	LinkIssues(ctx context.Context, fromProject string, fromNumber int, toProject string, toNumber int, linkType string) error
}

// MoveMethod names the mechanism an implementation used to relocate an
// issue. The two constants below are its only valid values.
type MoveMethod string

const (
	// MoveMethodNative means the tracker relocated the issue itself
	// (GitHub's transferIssue, GitLab's issue move, Jira's bulk move),
	// carrying its comment history to the destination. It says nothing
	// about what remains at the source: GitHub and Jira leave only a
	// redirect, while GitLab's move keeps the original as a closed issue
	// with system notes linking the two. Either way the old location
	// still leads a reader to the new one.
	MoveMethodNative MoveMethod = "native"
	// MoveMethodCloseAndRecreate means a copy was filed in the
	// destination and the original closed with a cross-link. The source
	// issue still exists, closed, and the destination issue is authored
	// by the bot rather than the original reporter.
	MoveMethodCloseAndRecreate MoveMethod = "close-and-recreate"
)

// MoveResult reports where an issue ended up after a move. It carries
// the source endpoint back alongside the destination so a caller
// describing what happened — posting provenance, reporting the move to
// whoever asked for it — has both sides to hand without threading the
// original location through itself.
type MoveResult struct {
	// SourceProject is the project the issue moved out of, in the form
	// Client methods take: "owner/repo" for GitHub and GitLab, a project
	// key for Jira.
	SourceProject string
	// SourceNumber is the number the issue had in SourceProject.
	SourceNumber int
	// DestProject is the project the issue moved into.
	DestProject string
	// DestNumber is the issue's number in DestProject. Trackers
	// renumber a moved issue to fit the destination's sequence, so it
	// generally differs from SourceNumber under either mechanism.
	DestNumber int
	// DestURL is the web URL of the issue at its new location.
	DestURL string
	// Method is the mechanism that performed the move, for audit and
	// reporting. It does not imply what became of the source issue —
	// see MoveMethodNative.
	Method MoveMethod
}

// Mover is an optional capability for relocating an issue from one
// project to another within the same tracker. Moves do not cross tracker
// boundaries: GitHub transfers issues between repositories under the same
// owner, GitLab moves them between projects, and Jira moves them between
// projects on the same site, but no backend can hand an issue to another
// backend. Consumers should type-assert their tracker.Client to Mover and
// report ErrNotSupported when the tracker does not implement it, the same
// way they handle Linker.
//
// Implementations relocate the issue natively where they can — GitHub's
// transferIssue, GitLab's issue move, Jira's bulk move — which carries
// its comment history to the destination. Where credentials or the
// backend rule that out, an implementation may instead file the issue in
// the destination and close the original with a cross-link; that costs
// the reporter's authorship and splits the history across two locations.
//
// Neither mechanism reliably removes the source issue — GitLab's native
// move keeps the original as a closed issue, the same as
// close-and-recreate does — so callers must not read MoveResult.Method
// as a statement about the source. Both mechanisms do leave the old
// location pointing at the new one, by redirect or by cross-link, so
// callers generally need not care which ran.
//
// Implementations are currently stubs that return ErrNotSupported; the
// per-tracker move operations land separately.
type Mover interface {
	// MoveIssue relocates the issue (sourceProject, number) into
	// targetProject and reports where it landed.
	//
	// targetProject typically originates from an agent's output, which
	// is untrusted: implementations must resolve it as an exact
	// canonical identifier and must not prefix- or fuzzy-match it onto
	// some other project. Authorizing the destination against the
	// caller's allowlist is the caller's job, not the implementation's.
	//
	// A failed MoveIssue is not safely retryable — a move that errored
	// after the tracker had already relocated the issue would duplicate
	// it on a second attempt. Implementations must not retry internally
	// or fall back to another strategy; they return the error and leave
	// adjudication to a human.
	//
	// It returns an error satisfying IsNotSupported when the tracker
	// cannot move issues, and one satisfying IsNotFound when the source
	// issue or the target project does not exist.
	MoveIssue(ctx context.Context, sourceProject string, number int, targetProject string) (*MoveResult, error)
}
