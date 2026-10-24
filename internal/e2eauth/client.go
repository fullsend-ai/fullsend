package e2eauth

import (
	"context"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// GitHubGate is the forge surface required for e2e pull-request authorization.
type GitHubGate interface {
	GetPullRequestAuthorizationContext(ctx context.Context, owner, repo string, number int) (*forge.PullRequestAuthorizationContext, error)
	ListIssueLabelEvents(ctx context.Context, owner, repo string, number int) ([]forge.IssueLabelEvent, error)
	RemoveIssueLabel(ctx context.Context, owner, repo string, number int, label string) error
	GetCollaboratorPermissionDetail(ctx context.Context, owner, repo, username string) (forge.GitHubCollaboratorPermissionDetail, error)
}

// GateFromForge returns a GitHubGate when client implements the required methods.
func GateFromForge(client forge.Client) (GitHubGate, error) {
	ext, ok := client.(forge.GitHubExtensions)
	if !ok {
		return nil, forge.ErrNotSupported
	}
	return gateClient{client: client, ext: ext}, nil
}

type gateClient struct {
	client forge.Client
	ext    forge.GitHubExtensions
}

func (g gateClient) GetPullRequestAuthorizationContext(ctx context.Context, owner, repo string, number int) (*forge.PullRequestAuthorizationContext, error) {
	return g.client.GetPullRequestAuthorizationContext(ctx, owner, repo, number)
}

func (g gateClient) ListIssueLabelEvents(ctx context.Context, owner, repo string, number int) ([]forge.IssueLabelEvent, error) {
	return g.client.ListIssueLabelEvents(ctx, owner, repo, number)
}

func (g gateClient) RemoveIssueLabel(ctx context.Context, owner, repo string, number int, label string) error {
	return g.client.RemoveIssueLabel(ctx, owner, repo, number, label)
}

func (g gateClient) GetCollaboratorPermissionDetail(ctx context.Context, owner, repo, username string) (forge.GitHubCollaboratorPermissionDetail, error) {
	return g.ext.GetCollaboratorPermissionDetail(ctx, owner, repo, username)
}
