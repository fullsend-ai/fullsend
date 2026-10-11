package cli

import (
	"context"
	"fmt"
	"slices"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlablifecycle"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// newGitLabRoleTokenClient returns the role credential client used by
// install, rotation, status, and uninstall: role identities are project
// service accounts, with legacy project access tokens still inventoried
// and used when the instance offers no project service accounts.
//
// Both ownership resolvers (ManagedAccountIDs and ManagedAccountNames) and
// the supplied-credential exclusions are always set, so the live client
// never relies on the fail-closed fallback for absent resolvers.
//
// Administrator-supplied credentials (--gitlab-role-token) are attributed to
// their owner and token while they still authenticate:
// SuppliedCredentialIdentity resolves them at enrollment so rotation state
// records the owner, its permanent exclusion and the token ID, and
// AttributeSuppliedOwners lets rotation persist the owner of a credential
// enrolled earlier without one. Once recorded, an expired or revoked
// credential keeps its owner excluded and can be replaced normally.
// TriggerOwner.SuppliedAccountIDs still authenticates with the installed
// secret for an entry with no recorded owner and fails closed when it cannot,
// and SuppliedTokenIDs reports only the enrolled token under the role's name
// for an owner whose token ID was recorded.
func newGitLabRoleTokenClient(c *gitlab.LiveClient) repos.ServiceAccountTokenClient {
	owner := gitlablifecycle.NewTriggerOwner(c)
	return repos.ServiceAccountTokenClient{
		ContainmentClient: c,
		InstalledRoleCredentialOwner: func(ctx context.Context, project, repo, secret string) (int, error) {
			value, exists, err := c.GetRepoVariable(ctx, project, repo, secret)
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
		LegacyTokenCreated: func(ctx context.Context, project, repo string, tok *repos.ProjectAccessToken) error {
			return repos.RecordManagedGitLabLegacyToken(ctx, c, project, repo, tok)
		},
		ManagedLegacyTokenIDs: func(ctx context.Context, project, repo string) ([]int, error) {
			return repos.ManagedGitLabLegacyTokenIDs(ctx, c, project, repo)
		},
		ManagedAccountIDs: func(ctx context.Context, project, repo string) ([]int, error) {
			return repos.ManagedGitLabRoleAccountIDs(ctx, c, project, repo)
		},
		ManagedAccountNames: func(ctx context.Context, project, repo string) (map[int]string, error) {
			return repos.ManagedGitLabRoleAccountNames(ctx, c, project, repo)
		},
		AccountCreated: func(ctx context.Context, project, repo string, sa repos.GitLabServiceAccount) error {
			return repos.RecordManagedGitLabServiceAccount(ctx, c, project, repo, sa)
		},
		VerifyToken: func(ctx context.Context, project, repo string, tok *repos.ProjectAccessToken) error {
			return verifyGitLabRoleToken(ctx, c, project, repo, tok)
		},
		VerifyAccountDeletion: func(ctx context.Context, project, repo string, userID int) error {
			// Sanitize remote inventory failures at the callback boundary so
			// VerifyGitLabAccountDeletion's local policy diagnostic stays visible.
			return repos.VerifyGitLabAccountDeletion(ctx, userID, repos.GitLabAccountDeletionInventory{
				SSHKeys: func(ctx context.Context) ([]int, error) {
					ids, err := owner.PollerSSHKeyIDs(ctx, int64(userID))
					return ids, repos.SafeAPIError("listing service-account SSH keys before deletion", err)
				},
				Jobs: func(ctx context.Context) ([]int, error) {
					ids, err := owner.PollerActiveJobIDs(ctx, project, repo, int64(userID))
					return ids, repos.SafeAPIError("listing service-account unfinished jobs before deletion", err)
				},
				Schedules: func(ctx context.Context) ([]int, error) {
					ids, err := owner.PollerScheduleIDs(ctx, project, repo, int64(userID))
					return ids, repos.SafeAPIError("listing service-account pipeline schedules before deletion", err)
				},
				Triggers: func(ctx context.Context) ([]int, error) {
					ids, err := owner.PollerTriggerIDs(ctx, project, repo, int64(userID))
					return ids, repos.SafeAPIError("listing service-account pipeline triggers before deletion", err)
				},
			})
		},
		SA:                        gitlabServiceAccountAdapter{c: c},
		Legacy:                    gitlabTokenAdapter{c: c},
		SuppliedAccountIDs:        owner.SuppliedAccountIDs,
		SuppliedTokenIDs:          owner.SuppliedTokenIDs,
		CurrentSuppliedAccountIDs: owner.CurrentSuppliedAccountIDs,
		SuppliedCredentialIdentity: func(ctx context.Context, token string) (repos.SuppliedIdentity, error) {
			// Failures wrap the unresolved-ownership sentinel like the other
			// ownership resolvers, flattening the sanitized cause to text so
			// the server's error classification never leaks through.
			credential, err := gitlab.New(token, gitlab.WithBaseURL(c.BaseURL()))
			if err != nil {
				return repos.SuppliedIdentity{}, fmt.Errorf("%w: %s", repos.ErrSuppliedCredentialUnresolved, repos.SafeAPIError("building the supplied credential client", err))
			}
			self, err := credential.GetOwnPersonalAccessToken(ctx)
			if err != nil {
				return repos.SuppliedIdentity{}, fmt.Errorf("%w: %s", repos.ErrSuppliedCredentialUnresolved, repos.SafeAPIError("authenticating the supplied credential", err))
			}
			return repos.SuppliedIdentity{UserID: self.UserID, TokenID: self.ID}, nil
		},
		AttributeSuppliedOwners: func(ctx context.Context, project, repo string) (map[gitlabroles.Role]int, error) {
			_, attributed, err := owner.ResolveExcludedAccounts(ctx, project, repo)
			return attributed, err
		},
	}
}

// verifyGitLabRoleToken authenticates a newly minted role credential before
// it is published: it must be the active token that was created, of the
// identity it was created for, with api scope and effective Developer access.
// A replacement Poller must also hold protected-default-branch pipeline access.
func verifyGitLabRoleToken(ctx context.Context, admin *gitlab.LiveClient, project, repo string, tok *repos.ProjectAccessToken) error {
	if tok == nil {
		return fmt.Errorf("new role credential is missing")
	}
	client, err := gitlab.New(tok.Token, gitlab.WithBaseURL(admin.BaseURL()))
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
	level, err := client.GetProjectMemberAccessLevel(ctx, project, repo, int64(tok.UserID))
	if err != nil {
		return err
	}
	if level != gitlabroles.DeveloperAccessLevel {
		return fmt.Errorf("new role credential does not have effective Developer access")
	}
	if tok.Name == gitlabroles.PollerTokenName {
		return repos.VerifyGitLabReplacementPipelineAccess(ctx, admin, project, repo, tok.UserID)
	}
	return nil
}

// gitlabTriggerOwnerFor returns the Poller trigger owner for a live GitLab
// client, or nil (mint as the client's own identity) otherwise. The live
// owner does not implement repos.GitLabPollerQuiescenceVerifier, so new
// trigger creation and rotation stay deferred and polling remains the
// dispatch path.
func gitlabTriggerOwnerFor(client forge.Client) repos.GitLabTriggerOwner {
	glClient, ok := client.(*gitlab.LiveClient)
	if !ok {
		return nil
	}
	return gitlablifecycle.NewTriggerOwner(glClient)
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

func (a gitlabServiceAccountAdapter) DeleteProjectServiceAccount(ctx context.Context, owner, repo string, userID int) error {
	return a.c.DeleteProjectServiceAccount(ctx, owner, repo, userID)
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

func (a gitlabServiceAccountAdapter) CreateServiceAccountPAT(ctx context.Context, owner, repo string, userID int, name string, scopes []string, expiresAt string) (*repos.ProjectAccessToken, error) {
	tok, err := a.c.CreateServiceAccountPAT(ctx, owner, repo, userID, name, scopes, expiresAt)
	if err != nil {
		return nil, err
	}
	return &repos.ProjectAccessToken{ID: tok.ID, Name: tok.Name, Token: tok.Token, Active: tok.Active, ExpiresAt: tok.ExpiresAt, Revoked: tok.Revoked, UserID: tok.UserID}, nil
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
	return a.c.AddProjectMember(ctx, owner, repo, int(userID), accessLevel)
}

func (a gitlabServiceAccountAdapter) UpdateProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64, accessLevel int) error {
	return a.c.UpdateProjectMemberAccessLevel(ctx, owner, repo, int(userID), accessLevel)
}

func (a gitlabServiceAccountAdapter) GetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64) (int, error) {
	return a.c.GetProjectMemberAccessLevel(ctx, owner, repo, userID)
}
