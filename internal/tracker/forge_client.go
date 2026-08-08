package tracker

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ForgeClient adapts a forge.Client to the tracker.Client interface. It
// works for both the GitHub and GitLab forge.Client implementations, since
// forge.Client already abstracts over the two — this adapter only needs to
// split the tracker's single "project" string back into the owner/repo
// pair that forge.Client expects, and convert forge.IssueComment's numeric
// ID to the string form tracker.Comment uses.
type ForgeClient struct {
	forge forge.Client
}

// NewForgeClient returns a tracker.Client backed by fc.
func NewForgeClient(fc forge.Client) *ForgeClient {
	return &ForgeClient{forge: fc}
}

// GetIssue implements Client by splitting project into owner/repo for the
// underlying forge call.
func (c *ForgeClient) GetIssue(ctx context.Context, project string, number int) (*Issue, error) {
	owner, repo, err := splitProject(project)
	if err != nil {
		return nil, err
	}
	issue, err := c.forge.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	return &Issue{
		Number: issue.Number,
		Title:  issue.Title,
		Body:   issue.Body,
		URL:    issue.URL,
		Labels: issue.Labels,
	}, nil
}

// ListComments implements Client by splitting project into owner/repo for
// the underlying forge call.
func (c *ForgeClient) ListComments(ctx context.Context, project string, number int) ([]Comment, error) {
	owner, repo, err := splitProject(project)
	if err != nil {
		return nil, err
	}
	comments, err := c.forge.ListIssueComments(ctx, owner, repo, number)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	result := make([]Comment, len(comments))
	for i, fc := range comments {
		result[i] = fromForgeComment(fc)
	}
	return result, nil
}

// CreateComment implements Client by splitting project into owner/repo for
// the underlying forge call.
func (c *ForgeClient) CreateComment(ctx context.Context, project string, number int, body string) (*Comment, error) {
	owner, repo, err := splitProject(project)
	if err != nil {
		return nil, err
	}
	comment, err := c.forge.CreateIssueComment(ctx, owner, repo, number, body)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	result := fromForgeComment(*comment)
	return &result, nil
}

// UpdateComment implements Client. number is unused: forge.Client's
// UpdateIssueComment identifies the comment by ID alone (GitHub/GitLab
// comment IDs are globally unique within the repo), unlike Jira which
// needs the issue key too.
func (c *ForgeClient) UpdateComment(ctx context.Context, project string, number int, commentID string, body string) error {
	owner, repo, err := splitProject(project)
	if err != nil {
		return err
	}
	id, err := strconv.Atoi(commentID)
	if err != nil {
		return fmt.Errorf("tracker: comment ID %q is not numeric: %w", commentID, err)
	}
	return wrapNotFound(c.forge.UpdateIssueComment(ctx, owner, repo, id, body))
}

// wrapNotFound translates a forge.ErrNotFound-satisfying error into one
// that also satisfies tracker.ErrNotFound, so ForgeClient upholds the
// Client interface's NotFound contract without leaking forge as part of
// tracker.Client's error surface. Non-NotFound errors, including nil,
// pass through unchanged.
func wrapNotFound(err error) error {
	if !forge.IsNotFound(err) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrNotFound, err)
}

func fromForgeComment(c forge.IssueComment) Comment {
	return Comment{
		ID:        strconv.Itoa(c.ID),
		HTMLURL:   c.HTMLURL,
		Body:      c.Body,
		Author:    c.Author,
		CreatedAt: c.CreatedAt,
	}
}

// splitProject splits "group/subgroup/project" into owner="group/subgroup"
// and repo="project". GitHub projects are always single-level
// ("owner/repo"), which this also handles correctly since there's only one
// "/". GitLab projects may be nested under subgroups, hence splitting on
// the last "/" rather than the first.
//
// It returns an error if project doesn't split into a non-empty owner and
// a non-empty repo, so callers don't silently forward malformed values
// (e.g. missing owner or repo) into forge.Client calls that require both.
func splitProject(project string) (owner, repo string, err error) {
	idx := strings.LastIndex(project, "/")
	if idx < 0 {
		return "", "", fmt.Errorf("tracker: invalid project %q: expected \"owner/repo\"", project)
	}
	owner, repo = project[:idx], project[idx+1:]
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("tracker: invalid project %q: owner and repo must both be non-empty", project)
	}
	return owner, repo, nil
}
