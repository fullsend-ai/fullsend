package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normentity"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// githubGraphQLTimeout bounds one GitHub lookup the bot-role helper makes.
const githubGraphQLTimeout = 30 * time.Second

// githubCommentActors answers the bot-role helper's GitHub questions (#9602)
// — the canonical entity ID of the issue or pull request a run handles, and
// a comment's current-revision actor — through forge.GitHubExtensions, the
// interface the forge abstraction doc requires callers outside
// internal/forge/github/ to consume. A client is built per lookup with the
// run's GitHub token at that moment (newAuthenticatedGitHubClient), so a
// re-minted stage token is used for that stage's lookups, and the API
// endpoint is the configured one (GITHUB_API_URL, GitHub Enterprise Server
// included).
type githubCommentActors struct {
	// host is the source host the canonical entity ID names:
	// GITHUB_SERVER_URL's host (config.GitHubServerBaseURL), default
	// github.com.
	host string
}

func newGitHubCommentActors() *githubCommentActors {
	host := "github.com"
	if base := config.GitHubServerBaseURL(os.Getenv("GITHUB_SERVER_URL")); base != "" {
		host = strings.TrimPrefix(base, "https://")
	}
	return &githubCommentActors{host: host}
}

// canonicalEntityID is the normalized-entity v1 ID of a GitHub issue
// (work_item) or pull request (change_proposal) node.
func (g *githubCommentActors) canonicalEntityID(onPullRequest bool, nodeID string) (string, error) {
	kind := normentity.KindWorkItem
	if onPullRequest {
		kind = normentity.KindChangeProposal
	}
	id, err := normentity.NewID(string(normevent.SystemGitHub), g.host, string(kind), nodeID)
	return string(id), err
}

// EntityID resolves the canonical entity ID of issue or pull request number
// in repo (owner/name).
func (g *githubCommentActors) EntityID(ctx context.Context, repo, number string) (string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("repository %q is not owner/name", repo)
	}
	n, err := strconv.Atoi(number)
	if err != nil || n < 1 {
		return "", fmt.Errorf("issue number %q is not a positive integer", number)
	}
	var client forge.GitHubExtensions
	client, err = newAuthenticatedGitHubClient("", "")
	if err != nil {
		return "", err
	}
	typename, nodeID, err := client.IssueOrPullRequestNodeID(ctx, owner, name, n)
	if err != nil {
		return "", err
	}
	return g.canonicalEntityID(typename == "PullRequest", nodeID)
}

// CommentActor reads a comment's current-revision actor and the entity it is
// on. A Bot actor's login is returned in the REST form (slug[bot]) the mint
// takes; a deleted account is not a bot.
func (g *githubCommentActors) CommentActor(ctx context.Context, commentID string) (commentActor, error) {
	var client forge.GitHubExtensions
	client, err := newAuthenticatedGitHubClient("", "")
	if err != nil {
		return commentActor{}, err
	}
	a, err := client.CommentActor(ctx, commentID)
	if err != nil {
		return commentActor{}, err
	}
	entity, err := g.canonicalEntityID(a.OnPullRequest, a.EntityNodeID)
	if err != nil {
		return commentActor{}, err
	}
	if a.ActorType == "Bot" {
		return commentActor{Entity: entity, Bot: true, Login: a.ActorLogin + "[bot]"}, nil
	}
	return commentActor{Entity: entity, Login: a.ActorLogin}, nil
}
