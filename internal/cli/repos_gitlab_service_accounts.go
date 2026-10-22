package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

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
	triggerOwner := gitlablifecycle.NewTriggerOwner(c)
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
			return repos.RecordManagedGitLabServiceAccount(ctx, c, owner, repo, sa)
		},
		VerifyToken: func(ctx context.Context, owner, repo string, tok *repos.ProjectAccessToken) error {
			return verifyGitLabRoleToken(ctx, c, owner, repo, tok)
		},
		VerifyAccountDeletion: func(ctx context.Context, owner, repo string, userID int) error {
			// Sanitize remote inventory failures at the callback boundary so
			// VerifyGitLabAccountDeletion's local policy diagnostic stays visible.
			return repos.VerifyGitLabAccountDeletion(ctx, userID, repos.GitLabAccountDeletionInventory{
				SSHKeys: func(ctx context.Context) ([]int, error) {
					ids, err := triggerOwner.PollerSSHKeyIDs(ctx, int64(userID))
					return ids, repos.SafeAPIError("listing service-account SSH keys before deletion", err)
				},
				Jobs: func(ctx context.Context) ([]int, error) {
					ids, err := triggerOwner.PollerActiveJobIDs(ctx, owner, repo, int64(userID))
					return ids, repos.SafeAPIError("listing service-account unfinished jobs before deletion", err)
				},
				Schedules: func(ctx context.Context) ([]int, error) {
					ids, err := triggerOwner.PollerScheduleIDs(ctx, owner, repo, int64(userID))
					return ids, repos.SafeAPIError("listing service-account pipeline schedules before deletion", err)
				},
				Triggers: func(ctx context.Context) ([]int, error) {
					ids, err := triggerOwner.PollerTriggerIDs(ctx, owner, repo, int64(userID))
					return ids, repos.SafeAPIError("listing service-account pipeline triggers before deletion", err)
				},
			})
		},
		SA:                        gitlabServiceAccountAdapter{c: c},
		Legacy:                    gitlabTokenAdapter{c: c},
		SuppliedAccountIDs:        triggerOwner.SuppliedAccountIDs,
		SuppliedTokenIDs:          triggerOwner.SuppliedTokenIDs,
		CurrentSuppliedAccountIDs: triggerOwner.CurrentSuppliedAccountIDs,
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
			return suppliedIdentityOf(self), nil
		},
		VerifySuppliedAccess: func(ctx context.Context, owner, repo string, id repos.SuppliedIdentity) error {
			// Like a minted replacement, a supplied credential's owner must
			// hold exactly Developer access on the target project, counting
			// inherited membership (members/all). An elevated or unverifiable
			// level rejects the enrollment.
			if id.UserID <= 0 {
				return errors.New("the supplied credential's owner could not be determined to verify its project access")
			}
			level, err := c.GetProjectMemberAccessLevel(ctx, owner, repo, int64(id.UserID))
			if err != nil {
				return repos.SafeAPIError("verifying the supplied credential owner's project access", err)
			}
			if level != gitlabroles.DeveloperAccessLevel {
				return errors.New("the supplied credential's owner does not have exactly effective Developer access on the project")
			}
			return nil
		},
		VerifySuppliedPollerAccess: func(ctx context.Context, owner, repo string, userID int) error {
			// Read-only: the merge grant is applied by
			// EstablishSuppliedPollerAccess once enrollment is recorded, so a
			// rejected enrollment never leaves a grant behind.
			return repos.VerifyGitLabSuppliedPollerPipelineAccess(ctx, c, owner, repo, userID)
		},
		EstablishSuppliedPollerAccess: func(ctx context.Context, owner, repo string, userID int) error {
			return repos.VerifyGitLabReplacementPipelineAccess(ctx, c, owner, repo, userID)
		},
		InstalledRoleCredentialSelf: func(ctx context.Context, owner, repo, secret string) (repos.SuppliedIdentity, bool, error) {
			value, exists, err := c.GetRepoVariable(ctx, owner, repo, secret)
			if err != nil {
				return repos.SuppliedIdentity{}, false, err
			}
			if !exists || value == "" {
				return repos.SuppliedIdentity{}, false, nil
			}
			credential, err := gitlab.New(value, gitlab.WithBaseURL(c.BaseURL()))
			if err != nil {
				return repos.SuppliedIdentity{}, true, err
			}
			self, err := credential.GetOwnPersonalAccessToken(ctx)
			if err != nil {
				// Only GitLab's authoritative 401 means the credential is revoked
				// or expired; every other failure leaves its health unknown.
				var apiErr *gitlab.APIError
				if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
					return repos.SuppliedIdentity{}, true, repos.ErrInstalledCredentialRejected
				}
				return repos.SuppliedIdentity{}, true, err
			}
			return suppliedIdentityOf(self), true, nil
		},
		AttributeSuppliedOwners: func(ctx context.Context, owner, repo string) (map[gitlabroles.Role]int, error) {
			_, attributed, err := triggerOwner.ResolveExcludedAccounts(ctx, owner, repo)
			return attributed, err
		},
	}
}

// suppliedIdentityOf maps the token a supplied credential authenticates as to
// its owner, token ID and the metadata the credential policy is checked against.
func suppliedIdentityOf(self *gitlab.ProjectAccessToken) repos.SuppliedIdentity {
	return repos.SuppliedIdentity{
		UserID:    self.UserID,
		TokenID:   self.ID,
		Described: true,
		Name:      self.Name,
		Scopes:    slices.Clone(self.Scopes),
		ExpiresAt: self.ExpiresAt,
		Active:    self.Active,
		Revoked:   self.Revoked,
	}
}

// verifyGitLabRoleToken authenticates a newly minted role credential before
// it is published: it must be the active token that was created, of the
// identity it was created for, with the scopes the CLI mints, and effective
// Developer access. Its name and expiry are compared against the requested
// role policy carried on tok (not GitLab's creation response), and the expiry
// must also fall within the lifetime the CLI requests (repos.GitLabPATExpiresAt).
// A replacement Poller must also hold protected-default-branch pipeline access.
func verifyGitLabRoleToken(ctx context.Context, admin *gitlab.LiveClient, owner, repo string, tok *repos.ProjectAccessToken) error {
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
	// tok carries the requested name and expiry (repos pins them before
	// verification), so both are required and compared exactly.
	if tok.Name == "" || tok.ExpiresAt == "" {
		return fmt.Errorf("new role credential has no requested name or expiry to verify against")
	}
	if self.ID != tok.ID || self.UserID != tok.UserID || !self.Active || self.Revoked ||
		!slices.Equal(self.Scopes, gitlabroles.TokenScopes()) ||
		self.Name != tok.Name || self.ExpiresAt != tok.ExpiresAt {
		return fmt.Errorf("new role credential has an unexpected identity, scope or active state")
	}
	if err := verifyGitLabRoleTokenExpiry(self.ExpiresAt, time.Now()); err != nil {
		return err
	}
	level, err := client.GetProjectMemberAccessLevel(ctx, owner, repo, int64(tok.UserID))
	if err != nil {
		return err
	}
	if level != gitlabroles.DeveloperAccessLevel {
		return fmt.Errorf("new role credential does not have effective Developer access")
	}
	if tok.Name == gitlabroles.PollerTokenName {
		return repos.VerifyGitLabReplacementPipelineAccess(ctx, admin, owner, repo, tok.UserID)
	}
	return nil
}

// verifyGitLabRoleTokenExpiry rejects an authenticated role credential whose
// expiry is missing, malformed or later than the lifetime the CLI requests.
func verifyGitLabRoleTokenExpiry(expiresAt string, now time.Time) error {
	const layout = "2006-01-02"
	if expiresAt == "" {
		return fmt.Errorf("new role credential has no expiry")
	}
	got, err := time.Parse(layout, expiresAt)
	if err != nil {
		return fmt.Errorf("new role credential has a malformed expiry")
	}
	limit, err := time.Parse(layout, repos.GitLabPATExpiresAt(now))
	if err != nil {
		return fmt.Errorf("computing the permitted credential lifetime: %w", err)
	}
	if got.After(limit) {
		return fmt.Errorf("new role credential expires later than the permitted lifetime")
	}
	return nil
}

// gitlabTriggerOwnerFor returns the Poller trigger owner for a live GitLab
// client, or nil (mint as the client's own identity) otherwise. The live
// owner implements repos.GitLabPollerHandoff, so new trigger creation and
// rotation mint through a fresh Poller generation that is demoted and
// verified at Developer before its runtime credential is published; it does
// not implement repos.GitLabPollerQuiescenceVerifier, so the current Poller is
// never re-elevated.
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
