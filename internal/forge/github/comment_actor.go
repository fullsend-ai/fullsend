package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// maxCommentActorResponseBytes bounds a GraphQL response the comment-actor
// queries read.
const maxCommentActorResponseBytes = 1 << 20

// IssueOrPullRequestNodeID returns the GraphQL type ("Issue" or
// "PullRequest") and node ID of issue or pull request number in owner/repo.
// The node ID is in GitHub's new global-ID format (nextGlobalIDHeader), so
// long-lived issues and pull requests report the same ID as newly created
// ones and as every other producer of entity.source_id.
func (c *LiveClient) IssueOrPullRequestNodeID(ctx context.Context, owner, repo string, number int) (string, string, error) {
	var data struct {
		Repository *struct {
			IssueOrPullRequest *struct {
				Typename string `json:"__typename"`
				ID       string `json:"id"`
			} `json:"issueOrPullRequest"`
		} `json:"repository"`
	}
	const q = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){issueOrPullRequest(number:$number){__typename ... on Issue{id} ... on PullRequest{id}}}}`
	if err := c.graphqlQuery(ctx, q, map[string]any{"owner": owner, "name": repo, "number": number}, &data, withHeader(nextGlobalIDHeader, "1")); err != nil {
		return "", "", err
	}
	if data.Repository == nil || data.Repository.IssueOrPullRequest == nil {
		return "", "", fmt.Errorf("%w: %s/%s#%d", forge.ErrNotFound, owner, repo, number)
	}
	n := data.Repository.IssueOrPullRequest
	if (n.Typename != "Issue" && n.Typename != "PullRequest") || n.ID == "" {
		return "", "", fmt.Errorf("%s/%s#%d: unexpected node %q", owner, repo, number, n.Typename)
	}
	return n.Typename, n.ID, nil
}

// graphqlActor is a GraphQL Actor: its type and login.
type graphqlActor struct {
	Typename string `json:"__typename"`
	Login    string `json:"login"`
}

// nextGlobalIDHeader asks GitHub's GraphQL API to return node IDs in the
// new global-ID format on every node, new or old, so the entity IDs this
// package produces match every other producer of entity.source_id. node()
// accepts either input format, so a caller holding a legacy-format comment
// node ID still resolves.
const nextGlobalIDHeader = "X-Github-Next-Global-ID"

// CommentActor reads a comment's current-revision actor and the issue or
// pull request it is on (ADR 0133, ADR 0141). For an edited comment the
// actor is the editor of the edit-history entry the lastEditedAt field
// names: GitHub does not document whether the top-level editor field is
// the first or the last editor, and it does not document the order of the
// userContentEdits connection either, so a small page of the edit history
// is read and the entry whose editedAt equals lastEditedAt is the current
// revision, whichever end of the page it sits at. A page that does not
// contain it, whose entry has no editor, or whose timestamp matches more
// than one entry, is an error: the lookup fails closed rather than
// guessing. An unedited comment's actor is its author.
// It accepts the comments the normalized entity lists: issue and
// pull-request conversation comments (IssueComment) and pull-request
// review-thread comments (PullRequestReviewComment). Any other node, and
// an edited comment whose current revision cannot be attributed, is an
// error.
func (c *LiveClient) CommentActor(ctx context.Context, commentID string) (forge.CommentActor, error) {
	var data struct {
		Node *struct {
			Typename         string        `json:"__typename"`
			Author           *graphqlActor `json:"author"`
			LastEditedAt     *string       `json:"lastEditedAt"`
			UserContentEdits *struct {
				Nodes []struct {
					Editor   *graphqlActor `json:"editor"`
					EditedAt string        `json:"editedAt"`
				} `json:"nodes"`
			} `json:"userContentEdits"`
			Issue *struct {
				ID string `json:"id"`
			} `json:"issue"`
			PullRequest *struct {
				ID string `json:"id"`
			} `json:"pullRequest"`
		} `json:"node"`
	}
	const q = `query($id:ID!){node(id:$id){__typename ` +
		`... on IssueComment{author{__typename login} lastEditedAt userContentEdits(first:10){nodes{editor{__typename login} editedAt}} issue{id} pullRequest{id}} ` +
		`... on PullRequestReviewComment{author{__typename login} lastEditedAt userContentEdits(first:10){nodes{editor{__typename login} editedAt}} pullRequest{id}}}}`
	if err := c.graphqlQuery(ctx, q, map[string]any{"id": commentID}, &data, withHeader(nextGlobalIDHeader, "1")); err != nil {
		return forge.CommentActor{}, err
	}
	n := data.Node
	if n == nil {
		return forge.CommentActor{}, fmt.Errorf("%w: comment %q", forge.ErrNotFound, commentID)
	}
	if n.Typename != "IssueComment" && n.Typename != "PullRequestReviewComment" {
		return forge.CommentActor{}, fmt.Errorf("node %q is a %s, not an issue, pull-request or review-thread comment", commentID, n.Typename)
	}
	var out forge.CommentActor
	switch {
	case n.PullRequest != nil && n.PullRequest.ID != "":
		out.OnPullRequest, out.EntityNodeID = true, n.PullRequest.ID
	case n.Issue != nil && n.Issue.ID != "":
		out.EntityNodeID = n.Issue.ID
	default:
		return forge.CommentActor{}, fmt.Errorf("comment %q has no issue or pull request", commentID)
	}
	actor := n.Author
	if n.LastEditedAt != nil {
		// The current-revision actor is the editor of the edit whose
		// editedAt equals lastEditedAt. GitHub does not document the
		// order of the userContentEdits connection, so the revision is
		// identified by its timestamp, not by its position in the page:
		// whichever end the matching entry sits at, it is the one
		// lastEditedAt names. A page that does not contain the last
		// revision, whose entry has no editor, or whose timestamp
		// matches more than one entry (DateTime values carry no
		// sub-second precision, so two edits in one second are
		// ambiguous), fails closed.
		var editor *graphqlActor
		matches := 0
		if n.UserContentEdits != nil {
			for _, e := range n.UserContentEdits.Nodes {
				if e.EditedAt == *n.LastEditedAt {
					matches++
					editor = e.Editor
				}
			}
		}
		if matches > 1 || editor == nil {
			return forge.CommentActor{}, fmt.Errorf("comment %q's current edit is not in its edit history", commentID)
		}
		actor = editor
	}
	if actor != nil {
		out.ActorType, out.ActorLogin = actor.Typename, actor.Login
	}
	return out, nil
}

// graphqlQuery runs one GraphQL query against the client's GraphQL endpoint
// and decodes its data into out. A transport error, a non-200 status or a
// GraphQL error fails; a NOT_FOUND GraphQL error is returned wrapped in
// forge.ErrNotFound, the sentinel the forge.GitHubExtensions doc comments
// promise for a missing node. Extra headers (e.g. nextGlobalIDHeader) are
// set on the request.
func (c *LiveClient) graphqlQuery(ctx context.Context, query string, variables map[string]any, out any, headers ...requestHeader) error {
	resp, err := c.do(ctx, http.MethodPost, c.graphqlEndpoint(), map[string]any{"query": query, "variables": variables}, headers...)
	if err != nil {
		return fmt.Errorf("graphql query: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCommentActorResponseBytes))
	if err != nil {
		return fmt.Errorf("read graphql response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode, Message: "graphql query failed"}
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode graphql response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		if envelope.Errors[0].Type == "NOT_FOUND" {
			return fmt.Errorf("%w: %s", forge.ErrNotFound, envelope.Errors[0].Message)
		}
		return fmt.Errorf("graphql query: %s", envelope.Errors[0].Message)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("graphql query: empty data")
	}
	return json.Unmarshal(envelope.Data, out)
}
