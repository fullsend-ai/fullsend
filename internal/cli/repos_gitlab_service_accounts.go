package cli

import (
	"context"
	"fmt"
	"slices"

	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlablifecycle"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// newGitLabRoleTokenClient returns the role credential client used by
// install, rotation, status, and uninstall: role identities are project
// service accounts, with legacy project access tokens still inventoried
// and used when the instance offers no project service accounts.
func newGitLabRoleTokenClient(c *gitlab.LiveClient) repos.ServiceAccountTokenClient {
	return repos.ServiceAccountTokenClient{
		ContainmentClient: c,
		InstalledRoleCredentialOwner: func(ctx context.Context, owner, repo, secret string) (int, error) {
			value, exists, err := c.GetRepoVariable(ctx, owner, repo, secret)
			if err != nil || !exists || value == "" {
				return 0, err
			}
			credential, err := gitlab.New(value, gitlab.WithBaseURL(c.BaseURL()))
			if err != nil {
				return 0, err
			}
			token, err := credential.GetOwnPersonalAccessToken(ctx)
			if err != nil {
				return 0, err
			}
			return token.UserID, nil
		},
		LegacyTokenCreated: func(ctx context.Context, owner, repo string, tok *repos.ProjectAccessToken) error {
			return repos.RecordManagedGitLabLegacyToken(ctx, c, owner, repo, tok)
		},
		ManagedLegacyTokenIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
			return repos.ManagedGitLabLegacyTokenIDs(ctx, c, owner, repo)
		},
		ManagedAccountIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
			return repos.ManagedGitLabRoleAccountIDs(ctx, c, owner, repo)
		},
		ManagedAccountNames: func(ctx context.Context, owner, repo string) (map[int]string, error) {
			return repos.ManagedGitLabRoleAccountNames(ctx, c, owner, repo)
		},
		AccountCreated: func(ctx context.Context, owner, repo string, sa repos.GitLabServiceAccount) error {
			return repos.RecordManagedServiceAccount(ctx, c, owner, repo, sa)
		},
		VerifyToken: func(ctx context.Context, owner, repo string, tok *repos.ProjectAccessToken) error {
			client, err := gitlab.New(tok.Token, gitlab.WithBaseURL(c.BaseURL()))
			if err != nil {
				return err
			}
			self, err := client.GetOwnPersonalAccessToken(ctx)
			if err != nil {
				return err
			}
			if self.ID != tok.ID || self.UserID != tok.UserID || !self.Active || self.Revoked || !slices.Contains(self.Scopes, "api") {
				return fmt.Errorf("new role credential has an unexpected identity, scope or active state")
			}
			level, err := client.GetProjectMemberAccessLevel(ctx, owner, repo, int64(tok.UserID))
			if err != nil {
				return err
			}
			if level != gitlabroles.DeveloperAccessLevel {
				return fmt.Errorf("new role credential does not have effective Developer access")
			}
			if tok.Name == gitlabroles.PollerTokenName {
				return repos.VerifyGitLabReplacementPipelineAccess(ctx, c, owner, repo, tok.UserID)
			}
			return nil
		},
		VerifyAccountDeletion: func(ctx context.Context, owner, repo string, userID int) error {
			checker := gitlablifecycle.NewTriggerOwner(c)
			// Sanitize remote inventory failures at the callback boundary so
			// VerifyGitLabAccountDeletion's local policy diagnostic stays visible.
			return repos.VerifyGitLabAccountDeletion(ctx, userID, repos.GitLabAccountDeletionInventory{
				SSHKeys: func(ctx context.Context) ([]int, error) {
					ids, err := checker.PollerSSHKeyIDs(ctx, int64(userID))
					return ids, repos.SafeAPIError("listing service-account SSH keys before deletion", err)
				},
				Jobs: func(ctx context.Context) ([]int, error) {
					ids, err := checker.PollerActiveJobIDs(ctx, owner, repo, int64(userID))
					return ids, repos.SafeAPIError("listing service-account unfinished jobs before deletion", err)
				},
				Schedules: func(ctx context.Context) ([]int, error) {
					ids, err := checker.PollerScheduleIDs(ctx, owner, repo, int64(userID))
					return ids, repos.SafeAPIError("listing service-account pipeline schedules before deletion", err)
				},
				Triggers: func(ctx context.Context) ([]int, error) {
					ids, err := checker.PollerTriggerIDs(ctx, owner, repo, int64(userID))
					return ids, repos.SafeAPIError("listing service-account pipeline triggers before deletion", err)
				},
			})
		},
		SA:                 gitlabServiceAccountAdapter{c: c},
		Legacy:             gitlabTokenAdapter{c: c},
		SuppliedAccountIDs: gitlablifecycle.NewTriggerOwner(c).SuppliedAccountIDs,
		SuppliedTokenIDs:   gitlablifecycle.NewTriggerOwner(c).SuppliedTokenIDs,

		CurrentSuppliedAccountIDs: gitlablifecycle.NewTriggerOwner(c).CurrentSuppliedAccountIDs,
	}
}

// gitlabServiceAccountAdapter adapts the live GitLab client to
// repos.GitLabServiceAccountAPI.
type gitlabServiceAccountAdapter struct {
	c *gitlab.LiveClient
}

var _ repos.GitLabServiceAccountAPI = gitlabServiceAccountAdapter{}

func (a gitlabServiceAccountAdapter) CreateProjectServiceAccount(ctx context.Context, owner, repo, name string) (repos.GitLabServiceAccount, error) {
	sa, err := a.c.CreateProjectServiceAccount(ctx, owner, repo, name)
	if err != nil {
		return repos.GitLabServiceAccount{}, err
	}
	return repos.GitLabServiceAccount{ID: sa.ID, Name: sa.Name}, nil
}

func (a gitlabServiceAccountAdapter) ListProjectServiceAccounts(ctx context.Context, owner, repo string) ([]repos.GitLabServiceAccount, error) {
	accounts, err := a.c.ListProjectServiceAccounts(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	out := make([]repos.GitLabServiceAccount, len(accounts))
	for i, sa := range accounts {
		out[i] = repos.GitLabServiceAccount{ID: sa.ID, Name: sa.Name}
	}
	return out, nil
}

func (a gitlabServiceAccountAdapter) DeleteProjectServiceAccount(ctx context.Context, owner, repo string, userID int) error {
	return a.c.DeleteProjectServiceAccount(ctx, owner, repo, userID)
}

func (a gitlabServiceAccountAdapter) CreateServiceAccountPAT(ctx context.Context, owner, repo string, userID int, name string, scopes []string, expiresAt string) (*repos.ProjectAccessToken, error) {
	tok, err := a.c.CreateServiceAccountPAT(ctx, owner, repo, userID, name, scopes, expiresAt)
	if err != nil {
		return nil, err
	}
	return &repos.ProjectAccessToken{ID: tok.ID, Name: tok.Name, Token: tok.Token, Active: tok.Active, ExpiresAt: tok.ExpiresAt, UserID: tok.UserID}, nil
}

func (a gitlabServiceAccountAdapter) ListServiceAccountPATs(ctx context.Context, owner, repo string, userID int) ([]repos.ProjectAccessToken, error) {
	toks, err := a.c.ListServiceAccountPATs(ctx, owner, repo, userID)
	if err != nil {
		return nil, err
	}
	out := make([]repos.ProjectAccessToken, len(toks))
	for i, t := range toks {
		out[i] = repos.ProjectAccessToken{ID: t.ID, Name: t.Name, Active: t.Active, ExpiresAt: t.ExpiresAt, Revoked: t.Revoked, UserID: t.UserID}
	}
	return out, nil
}

func (a gitlabServiceAccountAdapter) RevokeServiceAccountPAT(ctx context.Context, owner, repo string, userID, tokenID int) error {
	return a.c.RevokeServiceAccountPAT(ctx, owner, repo, userID, tokenID)
}

func (a gitlabServiceAccountAdapter) AddProjectMember(ctx context.Context, owner, repo string, userID int64, accessLevel int) error {
	return a.c.AddProjectMember(ctx, owner, repo, userID, accessLevel)
}

func (a gitlabServiceAccountAdapter) UpdateProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64, accessLevel int) error {
	return a.c.UpdateProjectMemberAccessLevel(ctx, owner, repo, userID, accessLevel)
}

func (a gitlabServiceAccountAdapter) GetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64) (int, error) {
	return a.c.GetProjectMemberAccessLevel(ctx, owner, repo, userID)
}
