package repos

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// GitLabRoleCleanupConfig controls removal of GitLab role-identity
// installation state: the registry, rotation document, built-in and
// custom role secrets, and matching role project access tokens. It
// does not touch the legacy FULLSEND_FORGE_TOKEN shared secret or its
// fullsend-bot project access token — a repository installed before
// the role-only rollout may require manual cleanup of those.
type GitLabRoleCleanupConfig struct {
	Owner  string
	Repo   string
	Client forge.Client
	// Tokens, when set, revokes active role project access tokens. Nil
	// skips PAT revocation; CI/CD variables and secrets are still
	// deleted.
	Tokens ProjectAccessTokenClient
	DryRun bool
}

// GitLabRoleCleanupResult is the non-secret outcome of identity cleanup.
type GitLabRoleCleanupResult struct {
	AccountsDeleted int
	VarsDeleted     int
	TokensRevoked   int
	DryRun          bool
	Diagnostics     []string
}

// GitLabManagedAccountCleaner removes durably owned service accounts after
// credential cleanup. Wrappers must preserve this capability so ownership
// state is retired only after account cleanup succeeds. The forge client provides
// the ownership state and account deletion; the token client verifies credentials
// and resources, which does not require it to implement forge.Client.
// This optional capability is intentionally carried by token adapters so generic
// uninstall can preserve wrapper-specific exclusions and resource checks without
// depending on the concrete GitLab adapter. Adapters lacking it retain ownership.
type GitLabManagedAccountCleaner interface {
	DeleteManagedServiceAccounts(ctx context.Context, client forge.Client, owner, repo string) (int, error)
}

// CleanupGitLabRoleIdentity removes GitLab role-identity installation
// state without recreating any of it. Missing variables, secrets, or
// tokens are not errors, so retries and uninstall of partial installs
// stay idempotent. A token-inventory failure after variables are
// deleted is still returned so uninstall can fail closed and leave the
// manifest entry for retry.
//
// This function never reads or returns secret values.
func CleanupGitLabRoleIdentity(ctx context.Context, cfg GitLabRoleCleanupConfig) (_ GitLabRoleCleanupResult, err error) {
	result := GitLabRoleCleanupResult{DryRun: cfg.DryRun}
	if cfg.Client == nil {
		return result, fmt.Errorf("GitLab role identity cleanup requires a forge client")
	}
	release, lockErr := LockGitLabProject(ctx, cfg.Client, cfg.Owner, cfg.Repo, cfg.DryRun)
	if lockErr != nil {
		return result, lockErr
	}
	defer release(&err)
	return CleanupGitLabRoleIdentityLocked(ctx, cfg)
}

// CleanupGitLabRoleIdentityLocked is CleanupGitLabRoleIdentity for a caller
// that already holds the project lease from LockGitLabProject, such as an
// uninstall that serializes webhook teardown and role cleanup in one
// transaction. The lease is not reentrant.
func CleanupGitLabRoleIdentityLocked(ctx context.Context, cfg GitLabRoleCleanupConfig) (GitLabRoleCleanupResult, error) {
	result := GitLabRoleCleanupResult{DryRun: cfg.DryRun}
	if cfg.Client == nil {
		return result, fmt.Errorf("GitLab role identity cleanup requires a forge client")
	}

	names := gitlabRoleIdentityVarNames(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	if cfg.DryRun {
		result.VarsDeleted = len(names)
		result.Diagnostics = append(result.Diagnostics, "dry-run: would delete GitLab role identity variables, secrets, and project access tokens")
		return result, nil
	}

	var errs []error
	if _, ok := cfg.Tokens.(GitLabManagedAccountCleaner); !ok {
		state, _, readErr := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
		if readErr != nil {
			return result, safeAPIError("checking managed-account ownership before cleanup", readErr)
		}
		owned := map[int]bool{}
		for _, rs := range state.Roles {
			if rs.ManagedUserID > 0 {
				owned[rs.ManagedUserID] = true
			}
		}
		for _, rs := range state.Roles {
			delete(owned, rs.SuppliedUserID)
			for _, id := range rs.ExcludedUserIDs {
				delete(owned, id)
			}
		}
		if len(owned) > 0 {
			return result, fmt.Errorf("managed-account cleanup capability unavailable; account cleanup skipped and ownership retained for retry")
		}
	}
	// The rotation document records who owns an administrator-supplied Poller
	// credential. Deleting it before the tokens are revoked would let a retry
	// after a failed revocation lose that provenance and treat the supplied
	// account as managed, so it goes last and only once every other cleanup step
	// succeeded.
	hasRotation := false
	deleteName := func(name string) {
		if err := cfg.Client.DeleteRepoVariable(ctx, cfg.Owner, cfg.Repo, name); err != nil {
			errs = append(errs, fmt.Errorf("deleting variable %s: %w", name, err))
		} else {
			result.VarsDeleted++
		}
		if !isGitLabRoleSecretName(name) {
			return
		}
		if err := cfg.Client.DeleteRepoSecret(ctx, cfg.Owner, cfg.Repo, name); err != nil && !forge.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("deleting secret %s: %w", name, err))
		}
	}
	for _, name := range names {
		if name == forge.VarGitLabRoleRotation {
			hasRotation = true
			continue
		}
		deleteName(name)
	}

	revoked, diag, err := revokeGitLabIdentityTokens(ctx, cfg.Tokens, cfg.Owner, cfg.Repo)
	result.TokensRevoked = revoked
	if diag != "" {
		result.Diagnostics = append(result.Diagnostics, diag)
	}
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		if accounts, ok := cfg.Tokens.(GitLabManagedAccountCleaner); ok {
			result.AccountsDeleted, err = accounts.DeleteManagedServiceAccounts(ctx, cfg.Client, cfg.Owner, cfg.Repo)
			if err != nil {
				errs = append(errs, err)
			}
			if result.AccountsDeleted > 0 {
				result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("Deleted %d verified Fullsend-managed project service accounts", result.AccountsDeleted))
			}
		}
	}
	// The rotation document holds the exclusions that protect administrator-
	// supplied accounts, so it is kept whenever any cleanup step failed: a retry
	// then still knows which accounts are not fullsend's to demote or revoke.
	if len(errs) == 0 && hasRotation {
		if err := retireRotationState(ctx, cfg, &result); err != nil {
			errs = append(errs, err)
		}
	}
	return result, errors.Join(errs...)
}

// retireRotationState removes the rotation document once cleanup succeeded,
// except for the supplied-account exclusions. Uninstall preserves administrator-
// supplied service accounts and their tokens, so the only durable record that
// those accounts are not fullsend's survives as an exclusions-only document: a
// later install or repeated uninstall would otherwise treat a same-named
// supplied account as managed. Everything else in the document (phases, token
// IDs, locks, provenance of installed credentials) is disposable and dropped. A
// document with no exclusions is deleted outright.
func retireRotationState(ctx context.Context, cfg GitLabRoleCleanupConfig, result *GitLabRoleCleanupResult) error {
	state, _, err := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	if err != nil {
		// A document that cannot be fetched, decoded, or validated may still hold
		// exclusions that protect administrator-owned accounts. Keep the variable
		// untouched and require explicit administrator recovery.
		return safeAPIError(fmt.Sprintf("reading rotation state to preserve supplied-account exclusions (%s left in place; repair or remove it manually)", forge.VarGitLabRoleRotation), err)
	}
	retained := rotationStateFile{Roles: map[string]rotationRoleState{}}
	for role, rs := range state.Roles {
		keep := rotationRoleState{}
		keep.ExcludedUserIDs = append(keep.ExcludedUserIDs, rs.ExcludedUserIDs...)
		keep.excludeOwner(rs.SuppliedUserID)
		if len(keep.ExcludedUserIDs) > 0 {
			retained.Roles[role] = keep
		}
	}
	if len(retained.Roles) == 0 {
		if err := cfg.Client.DeleteRepoVariable(ctx, cfg.Owner, cfg.Repo, forge.VarGitLabRoleRotation); err != nil {
			return safeAPIError("deleting variable "+forge.VarGitLabRoleRotation, err)
		}
		result.VarsDeleted++
		return nil
	}
	if err := writeRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo, retained); err != nil {
		return safeAPIError("preserving supplied-account exclusions in "+forge.VarGitLabRoleRotation, err)
	}
	result.Diagnostics = append(result.Diagnostics, "Preserved supplied-account exclusions in "+forge.VarGitLabRoleRotation+"; the administrator-owned service accounts were left in place")
	return nil
}

// revokeGitLabIdentityTokens lists and revokes active role project
// access tokens (Poller, Analyst, Coder, and custom own-credential
// roles). It never revokes the legacy fullsend-bot shared token — that
// is left for manual cleanup. Only a confirmed-gone project (404) is reported
// as a diagnostic and treated as "nothing to revoke" rather than a hard
// failure, since deletion can never converge there. Every other listing
// failure — including 403/Forbidden, which GitLab returns identically for
// plan-tier feature gating, group-level PAT disablement, and insufficient
// token permissions — fails the call closed so the manifest entry is
// retried rather than silently reporting uninstall success while tokens
// stay live. Operators stuck on a genuinely unsupported plan use the
// documented manual `--manifest-only` recovery path after confirming
// manual token revocation.
func revokeGitLabIdentityTokens(ctx context.Context, tokens ProjectAccessTokenClient, owner, repo string) (int, string, error) {
	if tokens == nil {
		return 0, "", nil
	}
	list := tokens.ListProjectAccessTokens
	if strict, ok := tokens.(StrictTokenInventory); ok {
		list = strict.ListProjectAccessTokensStrict
	}
	listed, err := list(ctx, owner, repo)
	if err != nil {
		if forge.IsNotFound(err) {
			return 0, fmt.Sprintf("Warning: GitLab project access token inventory unavailable (%v); treating as nothing to revoke — verify manually if the project no longer exists", err), nil
		}
		return 0, "", fmt.Errorf("listing GitLab project tokens for uninstall: %w", err)
	}
	// Stable order so joined errors and TokensRevoked are deterministic.
	sort.Slice(listed, func(i, j int) bool {
		if listed[i].Name == listed[j].Name {
			return listed[i].ID < listed[j].ID
		}
		return listed[i].Name < listed[j].Name
	})
	var errs []error
	revoked := 0
	for _, tok := range listed {
		if !tok.Active || tok.Revoked {
			continue
		}
		if !gitlabroles.IsRoleProjectTokenName(tok.Name) && tok.Name != gitlabroles.PollerBootstrapTokenName {
			continue
		}
		if err := tokens.RevokeProjectAccessToken(ctx, owner, repo, tok.ID); err != nil {
			errs = append(errs, fmt.Errorf("revoking GitLab identity token %s (id %d): %w", tok.Name, tok.ID, err))
			continue
		}
		revoked++
	}
	return revoked, "", errors.Join(errs...)
}
