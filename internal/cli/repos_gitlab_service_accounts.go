package cli

import (
	"context"
	"fmt"
	"slices"
	"sync"

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
func newGitLabRoleTokenClient(c *gitlab.LiveClient) repos.ProjectAccessTokenClient {
	return repos.ServiceAccountTokenClient{
		ManagedAccountIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
			return repos.ManagedGitLabRoleAccountIDs(ctx, c, owner, repo)
		},
		ManagedAccountNames: func(ctx context.Context, owner, repo string) (map[int]string, error) {
			return repos.ManagedGitLabRoleAccountNames(ctx, c, owner, repo)
		},
		AccountCreated: func(ctx context.Context, owner, repo string, sa repos.GitLabServiceAccount) error {
			return repos.RecordManagedServiceAccount(ctx, c, owner, repo, sa)
		},
		CreateIntent: repos.AccountCreateIntent{
			Begin: func(ctx context.Context, owner, repo, name string) error {
				return repos.RecordServiceAccountCreateIntent(ctx, c, owner, repo, name)
			},
			Pending: func(ctx context.Context, owner, repo, name string) (bool, error) {
				return repos.ServiceAccountCreateIntentPending(ctx, c, owner, repo, name)
			},
			Clear: func(ctx context.Context, owner, repo, name string) error {
				return repos.ClearServiceAccountCreateIntent(ctx, c, owner, repo, name)
			},
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
			return nil
		},
		VerifyAccountDeletion: func(ctx context.Context, owner, repo string, userID int) error {
			checker := newGitLabPollerTriggerOwner(c)
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
					triggers, err := c.ListPipelineTriggerTokens(ctx, owner, repo)
					var ids []int
					for _, trigger := range triggers {
						if trigger.OwnerID == int64(userID) || trigger.OwnerID == 0 {
							ids = append(ids, int(trigger.ID))
						}
					}
					return ids, repos.SafeAPIError("listing service-account pipeline triggers before deletion", err)
				},
			})
		},
		SA:                 gitlabServiceAccountAdapter{c: c},
		Legacy:             gitlabTokenAdapter{c: c},
		SuppliedAccountIDs: newGitLabPollerTriggerOwner(c).SuppliedAccountIDs,
		SuppliedTokenIDs:   newGitLabPollerTriggerOwner(c).SuppliedTokenIDs,

		CurrentSuppliedAccountIDs: newGitLabPollerTriggerOwner(c).CurrentSuppliedAccountIDs,
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

// gitlabPollerTriggerOwner aliases the GitLab lifecycle implementation;
// the CLI owns only construction and command wiring.
type gitlabPollerTriggerOwner = gitlablifecycle.TriggerOwner

func newGitLabPollerTriggerOwner(admin *gitlab.LiveClient) *gitlabPollerTriggerOwner {
	return gitlablifecycle.NewTriggerOwner(admin)
}
func resolveProvidedTokenIDs(ctx context.Context, client forge.Client, owner, repo string, provided map[gitlabroles.Role]string) map[gitlabroles.Role]int {
	return gitlablifecycle.ResolveProvidedTokenIDs(ctx, client, owner, repo, provided)
}
func resolveProvidedRoleCredentials(ctx context.Context, client forge.Client, owner, repo string, provided map[gitlabroles.Role]string) map[gitlabroles.Role]repos.ProvidedRoleCredential {
	return gitlablifecycle.ResolveProvidedRoleCredentials(ctx, client, owner, repo, provided)
}
func resolveProvidedTokenOwners(ctx context.Context, client forge.Client, provided map[gitlabroles.Role]string) map[gitlabroles.Role]int {
	return gitlablifecycle.ResolveProvidedTokenOwners(ctx, client, provided)
}

// gitlabTriggerOwnerFor returns the Poller trigger owner for a live
// GitLab client, or nil (mint as the client's own identity) otherwise.
func gitlabTriggerOwnerFor(client forge.Client) repos.GitLabTriggerOwner {
	glClient, ok := client.(*gitlab.LiveClient)
	if !ok {
		return nil
	}
	return newGitLabPollerTriggerOwner(glClient)
}

// gitlabUninstallTokenClient is the role token client for uninstall. In
// addition to the shared inventory it reconciles every managed Poller service
// account (duplicates included) before credential cleanup. It also retains,
// per project, the account that owns an administrator-supplied Poller
// credential so reconciliation and revocation leave that account and its
// tokens alone.
type gitlabUninstallTokenClient struct {
	repos.ServiceAccountTokenClient
	owner *gitlabPollerTriggerOwner
	// supplied holds the resolved supplied-credential exclusion per project.
	// It is shared across the value copies of this client.
	supplied *uninstallSuppliedExclusions
}

// uninstallSuppliedExclusion is the resolved ownership of a project's
// administrator-supplied Poller credential. When err is set the ownership could
// not be established and inventory fails closed.
type uninstallSuppliedExclusion struct {
	userIDs []int
	err     error
}

type uninstallSuppliedExclusions struct {
	mu sync.Mutex
	m  map[string]uninstallSuppliedExclusion
}

func (e *uninstallSuppliedExclusions) set(owner, repo string, x uninstallSuppliedExclusion) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.m[owner+"/"+repo] = x
}

func (e *uninstallSuppliedExclusions) get(owner, repo string) (uninstallSuppliedExclusion, bool) {
	if e == nil {
		return uninstallSuppliedExclusion{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	x, ok := e.m[owner+"/"+repo]
	return x, ok
}

var (
	_ repos.ProjectAccessTokenClient        = gitlabUninstallTokenClient{}
	_ repos.StrictTokenInventory            = gitlabUninstallTokenClient{}
	_ repos.GitLabPollerUninstallReconciler = gitlabUninstallTokenClient{}
	_ repos.GitLabManagedAccountCleaner     = gitlabUninstallTokenClient{}
)

func newGitLabUninstallTokenClient(c *gitlab.LiveClient) gitlabUninstallTokenClient {
	triggerOwner := newGitLabPollerTriggerOwner(c)
	supplied := &uninstallSuppliedExclusions{m: map[string]uninstallSuppliedExclusion{}}
	return gitlabUninstallTokenClient{
		ServiceAccountTokenClient: repos.ServiceAccountTokenClient{
			ManagedAccountIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
				return repos.ManagedGitLabRoleAccountIDs(ctx, c, owner, repo)
			},
			ManagedAccountNames: func(ctx context.Context, owner, repo string) (map[int]string, error) {
				return repos.ManagedGitLabRoleAccountNames(ctx, c, owner, repo)
			},
			VerifyAccountDeletion: newGitLabRoleTokenClient(c).(repos.ServiceAccountTokenClient).VerifyAccountDeletion,
			SA:                    gitlabServiceAccountAdapter{c: c},
			Legacy:                gitlabTokenAdapter{c: c},
			// Once reconciliation resolved a project's exclusions they are used
			// as cached: role cleanup deletes the installed credentials that
			// attribution authenticates with, so re-resolving afterwards would
			// fail or lose the exclusion.
			SuppliedAccountIDs: func(ctx context.Context, owner, repo string) ([]int, error) {
				if x, ok := supplied.get(owner, repo); ok {
					return x.userIDs, x.err
				}
				return triggerOwner.SuppliedAccountIDs(ctx, owner, repo)
			},
		},
		owner:    triggerOwner,
		supplied: supplied,
	}
}

// ListProjectAccessTokens is the shared inventory without the tokens of an
// administrator-supplied Poller account.
func (u gitlabUninstallTokenClient) ListProjectAccessTokens(ctx context.Context, owner, repo string) ([]repos.ProjectAccessToken, error) {
	toks, err := u.ServiceAccountTokenClient.ListProjectAccessTokens(ctx, owner, repo)
	return u.withoutSupplied(owner, repo, toks, err)
}

// ListProjectAccessTokensStrict is the destructive-cleanup inventory without
// the tokens of an administrator-supplied Poller account.
func (u gitlabUninstallTokenClient) ListProjectAccessTokensStrict(ctx context.Context, owner, repo string) ([]repos.ProjectAccessToken, error) {
	toks, err := u.ServiceAccountTokenClient.ListProjectAccessTokensStrict(ctx, owner, repo)
	return u.withoutSupplied(owner, repo, toks, err)
}

// withoutSupplied drops every token owned by the supplied Poller account. A
// project whose supplied ownership could not be resolved fails closed rather
// than offering its tokens for revocation.
func (u gitlabUninstallTokenClient) withoutSupplied(owner, repo string, toks []repos.ProjectAccessToken, err error) ([]repos.ProjectAccessToken, error) {
	if err != nil {
		return toks, err
	}
	x, ok := u.supplied.get(owner, repo)
	if !ok || (len(x.userIDs) == 0 && x.err == nil) {
		return toks, nil
	}
	if x.err != nil {
		return nil, x.err
	}
	return slices.DeleteFunc(toks, func(t repos.ProjectAccessToken) bool { return slices.Contains(x.userIDs, t.UserID) }), nil
}

// ReconcileGitLabPollersForUninstall restores and verifies Developer access of
// every managed Poller service account and revokes their bootstrap
// credentials. Only a positively absent service-account API (not found or not
// supported) means there is nothing to reconcile; a forbidden or failed
// listing cannot establish that and fails the uninstall.
//
// A same-named service account that owns the administrator-supplied Poller
// credential is not managed: it is excluded from reconciliation and its tokens
// from later revocation. When the supplied credential cannot be attributed the
// uninstall fails closed with repos.ErrPollerSuppliedUnresolved.
func (u gitlabUninstallTokenClient) ReconcileGitLabPollersForUninstall(ctx context.Context, client forge.Client, owner, repo string) error {
	accounts, err := u.owner.Admin.ListProjectServiceAccounts(ctx, owner, repo)
	// A positively absent service-account API skips only the membership
	// reconciliation below. Supplied-credential ownership is still resolved and
	// durably recorded first, because role cleanup deletes the credentials it is
	// attributed with and legacy project tokens named like a role are filtered
	// by the same exclusions. That also holds when the listing fails for another
	// reason: the failure is returned only after ownership is recorded, since
	// uninstall still proceeds to role cleanup.
	apiAbsent := false
	var listErr error
	if err != nil {
		if !forge.IsNotFound(err) && !forge.IsNotSupported(err) {
			listErr = repos.SafeAPIError("listing GitLab project service accounts to reconcile the Poller before uninstall", err)
		} else {
			apiAbsent = true
		}
		accounts = nil
	}
	excluded, attributed, supErr := u.owner.ResolveExcludedAccounts(ctx, owner, repo)
	if supErr != nil {
		supErr = fmt.Errorf("the GitLab Poller service accounts and role credentials were left untouched: %w", supErr)
		u.supplied.set(owner, repo, uninstallSuppliedExclusion{err: supErr})
		return supErr
	}
	excludedInts := make([]int, len(excluded))
	for i, id := range excluded {
		excludedInts[i] = int(id)
	}
	u.supplied.set(owner, repo, uninstallSuppliedExclusion{userIDs: excludedInts})
	// Persist what attribution resolved before role cleanup deletes the
	// credentials it authenticated with, so a retry after an interrupted
	// uninstall still excludes these accounts. Failing to persist stops the
	// uninstall rather than risking an unrecoverable exclusion. Owners that were
	// attributed by authenticating are recorded on their roles first, so the
	// retry resolves them from state once the secrets are gone.
	if err := repos.RecordSuppliedOwners(ctx, client, owner, repo, attributed); err != nil {
		return fmt.Errorf("%w: persisting the supplied-credential owners before uninstall: %w", repos.ErrPollerSuppliedUnresolved, repos.SafeAPIError("recording supplied owners", err))
	}
	if len(excludedInts) > 0 {
		if err := repos.RecordSuppliedExclusions(ctx, client, owner, repo, gitlabroles.RolePoller, excludedInts); err != nil {
			return fmt.Errorf("%w: persisting the supplied-account exclusions before uninstall: %w", repos.ErrPollerSuppliedUnresolved, repos.SafeAPIError("recording exclusions", err))
		}
	}
	if listErr != nil {
		return listErr
	}
	if apiAbsent {
		return nil
	}
	managedID, ownershipErr := repos.ManagedGitLabRoleAccountID(ctx, u.owner.Admin, owner, repo, gitlabroles.RolePoller)
	if ownershipErr != nil {
		return ownershipErr
	}
	var ids []int64
	for _, sa := range accounts {
		if managedID <= 0 || sa.ID != managedID || slices.Contains(excluded, int64(sa.ID)) {
			continue
		}
		ids = append(ids, int64(sa.ID))
	}
	slices.Sort(ids)
	return repos.ReconcileManagedPollersForUninstall(ctx, client, u.owner, owner, repo, ids)
}
