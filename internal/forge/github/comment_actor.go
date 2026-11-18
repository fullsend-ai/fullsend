package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// maxCommentActorResponseBytes bounds a GraphQL response the comment-actor
// queries read.
const maxCommentActorResponseBytes = 1 << 20

// CommentActor is a comment's current-revision actor and the issue or pull
// request it is on, as GitHub GraphQL reports them (#9602). ActorType is
// the actor's GraphQL type (Bot, User, …) and ActorLogin its login as
// GraphQL spells it (a Bot without the REST "[bot]" suffix); both are empty
// for a deleted account.
type CommentActor struct {
	OnPullRequest bool
	EntityNodeID  string
	ActorType     string
	ActorLogin    string
}

// IssueOrPullRequestNodeID returns the GraphQL type ("Issue" or
// "PullRequest") and node ID of issue or pull request number in owner/repo.
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
	if err := c.graphqlQuery(ctx, q, map[string]any{"owner": owner, "name": repo, "number": number}, &data); err != nil {
		return "", "", err
	}
	if data.Repository == nil || data.Repository.IssueOrPullRequest == nil {
		return "", "", fmt.Errorf("%s/%s#%d not found", owner, repo, number)
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

// CommentActor reads a comment's current-revision actor — its last editor
// when it was edited, its author otherwise (ADR 0133, ADR 0141) — and the
// issue or pull request it is on. It accepts the comments the normalized
// entity lists: issue and pull-request conversation comments
// (IssueComment) and pull-request review-thread comments
// (PullRequestReviewComment). Any other node, and an edited comment whose
// editor is unknown, is an error: its current revision cannot be
// attributed.
func (c *LiveClient) CommentActor(ctx context.Context, commentID string) (CommentActor, error) {
	var data struct {
		Node *struct {
			Typename     string        `json:"__typename"`
			Author       *graphqlActor `json:"author"`
			Editor       *graphqlActor `json:"editor"`
			LastEditedAt *string       `json:"lastEditedAt"`
			Issue        *struct {
				ID string `json:"id"`
			} `json:"issue"`
			PullRequest *struct {
				ID string `json:"id"`
			} `json:"pullRequest"`
		} `json:"node"`
	}
	const q = `query($id:ID!){node(id:$id){__typename ` +
		`... on IssueComment{author{__typename login} editor{__typename login} lastEditedAt issue{id} pullRequest{id}} ` +
		`... on PullRequestReviewComment{author{__typename login} editor{__typename login} lastEditedAt pullRequest{id}}}}`
	if err := c.graphqlQuery(ctx, q, map[string]any{"id": commentID}, &data); err != nil {
		return CommentActor{}, err
	}
	n := data.Node
	if n == nil {
		return CommentActor{}, fmt.Errorf("comment %q not found", commentID)
	}
	if n.Typename != "IssueComment" && n.Typename != "PullRequestReviewComment" {
		return CommentActor{}, fmt.Errorf("node %q is a %s, not an issue, pull-request or review-thread comment", commentID, n.Typename)
	}
	var out CommentActor
	switch {
	case n.PullRequest != nil && n.PullRequest.ID != "":
		out.OnPullRequest, out.EntityNodeID = true, n.PullRequest.ID
	case n.Issue != nil && n.Issue.ID != "":
		out.EntityNodeID = n.Issue.ID
	default:
		return CommentActor{}, fmt.Errorf("comment %q has no issue or pull request", commentID)
	}
	actor := n.Author
	if n.LastEditedAt != nil {
		if n.Editor == nil {
			return CommentActor{}, fmt.Errorf("comment %q was edited by an unknown actor", commentID)
		}
		actor = n.Editor
	}
	if actor != nil {
		out.ActorType, out.ActorLogin = actor.Typename, actor.Login
	}
	return out, nil
}

// graphqlQuery runs one GraphQL query against the client's GraphQL endpoint
// and decodes its data into out. A transport error, a non-200 status or a
// GraphQL error fails.
func (c *LiveClient) graphqlQuery(ctx context.Context, query string, variables map[string]any, out any) error {
	resp, err := c.do(ctx, http.MethodPost, c.graphqlEndpoint(), map[string]any{"query": query, "variables": variables})
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
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode graphql response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("graphql query: %s", envelope.Errors[0].Message)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("graphql query: empty data")
	}
	return json.Unmarshal(envelope.Data, out)
}
