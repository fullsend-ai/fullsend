package gitlablifecycle

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// UninstallTokenClient is the role token client for uninstall. In
// addition to the shared inventory it reconciles every managed Poller service
// account (duplicates included) before credential cleanup. It also retains,
// per project, the account that owns an administrator-supplied Poller
// credential so reconciliation and revocation leave that account and its
// tokens alone.
type UninstallTokenClient struct {
	repos.ServiceAccountTokenClient
	owner *TriggerOwner
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
	_ repos.ProjectAccessTokenClient        = UninstallTokenClient{}
	_ repos.StrictTokenInventory            = UninstallTokenClient{}
	_ repos.GitLabPollerUninstallReconciler = UninstallTokenClient{}
	_ repos.GitLabManagedAccountCleaner     = UninstallTokenClient{}
)

// NewUninstallTokenClient adds uninstall reconciliation and durable supplied
// attribution caching to the role inventory configured by the caller.
func NewUninstallTokenClient(admin *gitlab.LiveClient, role repos.ServiceAccountTokenClient) UninstallTokenClient {
	triggerOwner := NewTriggerOwner(admin)
	supplied := &uninstallSuppliedExclusions{m: map[string]uninstallSuppliedExclusion{}}
	role.SuppliedTokenIDs = nil
	role.CurrentSuppliedAccountIDs = nil
	role.SuppliedAccountIDs = func(ctx context.Context, owner, repo string) ([]int, error) {
		if x, ok := supplied.get(owner, repo); ok {
			return x.userIDs, x.err
		}
		return triggerOwner.SuppliedAccountIDs(ctx, owner, repo)
	}
	return UninstallTokenClient{ServiceAccountTokenClient: role, owner: triggerOwner, supplied: supplied}
}

// ListProjectAccessTokens is the shared inventory without the tokens of an
// administrator-supplied Poller account.
func (u UninstallTokenClient) ListProjectAccessTokens(ctx context.Context, owner, repo string) ([]repos.ProjectAccessToken, error) {
	toks, err := u.ServiceAccountTokenClient.ListProjectAccessTokens(ctx, owner, repo)
	return u.withoutSupplied(owner, repo, toks, err)
}

// ListProjectAccessTokensStrict is the destructive-cleanup inventory without
// the tokens of an administrator-supplied Poller account.
func (u UninstallTokenClient) ListProjectAccessTokensStrict(ctx context.Context, owner, repo string) ([]repos.ProjectAccessToken, error) {
	toks, err := u.ServiceAccountTokenClient.ListProjectAccessTokensStrict(ctx, owner, repo)
	return u.withoutSupplied(owner, repo, toks, err)
}

// withoutSupplied drops every token owned by the supplied Poller account. A
// project whose supplied ownership could not be resolved fails closed rather
// than offering its tokens for revocation.
func (u UninstallTokenClient) withoutSupplied(owner, repo string, toks []repos.ProjectAccessToken, err error) ([]repos.ProjectAccessToken, error) {
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
func (u UninstallTokenClient) ReconcileGitLabPollersForUninstall(ctx context.Context, client forge.Client, owner, repo string) error {
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
