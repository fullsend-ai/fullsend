package repos

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	// Preflight, when set, is the ownership resolution already produced by
	// PlanGitLabRoleCleanup under the same project lease. Cleanup then reuses
	// its frozen token client and excluded owner set instead of resolving
	// supplied owners a second time, so one operation never acts on two
	// different answers from an ownership resolver. Nil resolves afresh.
	Preflight *GitLabRoleCleanupPreflight
	DryRun    bool
}

// GitLabRoleCleanupPreflight is the frozen outcome of the cleanup ownership
// preflight: the token client cleanup must use and the project-wide excluded
// owner IDs. It is only valid for the owner, repo, and lease it was
// produced under.
type GitLabRoleCleanupPreflight struct {
	tokens   ProjectAccessTokenClient
	excluded []int
	// preserved is set when a supplied credential's owner was never recorded and
	// the token client cannot resolve it. Cleanup then leaves the project access
	// tokens named in preservedTokens untouched and keeps the rotation document,
	// so the supplied credentials and their provenance survive for a later,
	// ownership-aware cleanup instead of blocking uninstall.
	preserved       bool
	preservedTokens []string
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
	// Ownership and project-wide exclusions are read before any destructive
	// step. The exclusion-aware client is used for inventory, revocation and
	// account cleanup so an administrator-owned account recorded in rotation
	// state keeps its credentials even with no supplied-account callback.
	pre := cfg.Preflight
	if pre == nil {
		var err error
		pre, err = PlanGitLabRoleCleanup(ctx, cfg.Client, cfg.Owner, cfg.Repo, cfg.Tokens)
		if err != nil {
			return result, err
		}
	}
	tokens, excluded := pre.tokens, pre.excluded
	if pre.preserved {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("Supplied role credential ownership cannot be resolved by this client; leaving the supplied project access tokens and %s in place", forge.VarGitLabRoleRotation))
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

	revoked, diag, err := revokeGitLabIdentityTokensSkipping(ctx, tokens, cfg.Owner, cfg.Repo, excluded, pre.preservedTokens)
	result.TokensRevoked = revoked
	if diag != "" {
		result.Diagnostics = append(result.Diagnostics, diag)
	}
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		if accounts, ok := tokens.(GitLabManagedAccountCleaner); ok {
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
	if len(errs) == 0 && hasRotation && !pre.preserved {
		if err := retireRotationState(ctx, cfg, excluded, &result); err != nil {
			errs = append(errs, err)
		}
	}
	return result, errors.Join(errs...)
}

// PreflightGitLabRoleCleanupOwnership verifies, without changing anything,
// that role-identity cleanup can run safely: managed-account ownership must be
// readable and well formed, supplied-credential ownership must be resolvable,
// and recorded managed accounts need a token client that can clean them up.
// Callers that sequence other destructive steps around cleanup, such as
// uninstall, run it right after taking the project lease so a failure stops
// the whole operation before anything is removed. The caller holds the lease.
func PreflightGitLabRoleCleanupOwnership(ctx context.Context, client forge.Client, owner, repo string, tokens ProjectAccessTokenClient) error {
	_, err := PlanGitLabRoleCleanup(ctx, client, owner, repo, tokens)
	return err
}

// PlanGitLabRoleCleanup is PreflightGitLabRoleCleanupOwnership that also
// returns the frozen ownership resolution. Passing the result as
// GitLabRoleCleanupConfig.Preflight makes the later destructive steps reuse
// it rather than resolving supplied owners again.
func PlanGitLabRoleCleanup(ctx context.Context, client forge.Client, owner, repo string, tokens ProjectAccessTokenClient) (*GitLabRoleCleanupPreflight, error) {
	return preflightGitLabRoleCleanup(ctx, client, owner, repo, tokens)
}

// preflightGitLabRoleCleanup performs PreflightGitLabRoleCleanupOwnership and
// returns the token client cleanup must use together with the frozen set of
// project-wide excluded owner IDs: every exclusion recorded in rotation state
// joined with the supplied owners resolved now. A ServiceAccountTokenClient
// (value or pointer) is wrapped with that set, so inventory and revocation never
// touch an administrator-owned account's credentials even when no
// supplied-account callback is configured. Revocation also filters on the
// returned set for any other token client that reports token owners.
func preflightGitLabRoleCleanup(ctx context.Context, client forge.Client, owner, repo string, tokens ProjectAccessTokenClient) (*GitLabRoleCleanupPreflight, error) {
	plan := &GitLabRoleCleanupPreflight{}
	// Ownership must be readable before any variable, secret, or credential is
	// removed, whether or not the token client can delete accounts itself. A
	// malformed managed account ID fails closed here rather than reading as
	// "no managed account".
	state, readErr := loadManagedAccountOwnership(ctx, client, owner, repo)
	if readErr != nil {
		return nil, readErr
	}
	// A non-nil pointer satisfies the same interfaces as the value form;
	// normalize it so it is wrapped like the value form.
	tokens = normalizeServiceAccountClient(tokens)
	var excluded []int
	for _, rs := range state.Roles {
		excluded = appendExcludedID(excluded, rs.SuppliedUserID)
		for _, id := range rs.ExcludedUserIDs {
			excluded = appendExcludedID(excluded, id)
		}
	}
	if sa, ok := tokens.(ServiceAccountTokenClient); ok {
		// Recorded ownership is applied first so inventory, revocation and
		// account deletion all rely on the same ownership evidence.
		wrapped, frozen, exclErr := sa.withRecordedOwnership(state).withProjectExclusions(ctx, owner, repo, state)
		if exclErr != nil {
			// The error is already redacted by suppliedOwnerIDs.
			return nil, fmt.Errorf("resolving supplied-account exclusions before cleanup: %w", exclErr)
		}
		tokens = wrapped
		for _, id := range frozen {
			excluded = appendExcludedID(excluded, id)
		}
	} else if unresolvedSuppliedOwner(state) {
		// A supplied credential whose owner was never recorded can only be
		// attributed through an ownership-resolution capability. Any other client
		// would revoke same-named administrator-supplied tokens and retire their
		// provenance, and a nil client would retire that provenance without
		// revoking anything, so a later generic cleanup could revoke the supplied
		// token.
		if _, ok := tokens.(suppliedOwnerResolver); !ok {
			// No resolution capability at all (the live CLI token adapter, or no
			// client): refuse to guess, but do not block uninstall. Leave the
			// supplied roles' tokens and the rotation document untouched so a
			// later ownership-aware cleanup can still attribute them.
			plan.preserved = true
			plan.preservedTokens = unresolvedSuppliedTokenNames(state)
		} else {
			// A present capability that fails or attributes nothing refuses before
			// anything is removed and keeps the rotation document for recovery.
			ids, resolveErr := resolveSuppliedOwnersViaCapability(ctx, tokens, owner, repo)
			if resolveErr != nil {
				return nil, fmt.Errorf("resolving supplied-account exclusions before cleanup: %w", resolveErr)
			}
			for _, id := range ids {
				excluded = appendExcludedID(excluded, id)
			}
		}
	}
	if _, ok := tokens.(GitLabManagedAccountCleaner); !ok {
		owned := map[int]bool{}
		for _, rs := range state.Roles {
			if rs.ManagedUserID > 0 {
				owned[rs.ManagedUserID] = true
			}
		}
		// excluded holds the recorded exclusions and every owner resolved above.
		for _, id := range excluded {
			delete(owned, id)
		}
		if len(owned) > 0 {
			return nil, fmt.Errorf("managed-account cleanup capability unavailable; account cleanup skipped and ownership retained for retry")
		}
	}
	sort.Ints(excluded)
	plan.tokens, plan.excluded = tokens, excluded
	return plan, nil
}

// unresolvedSuppliedOwner reports whether any role records an administrator-
// supplied credential without its owner's user ID.
func unresolvedSuppliedOwner(state rotationStateFile) bool {
	for _, rs := range state.Roles {
		if provenanceOf(rs).Supplied && rs.SuppliedUserID <= 0 {
			return true
		}
	}
	return false
}

// suppliedOwnerResolver is the optional capability of a generic token client
// to attribute administrator-supplied credentials to their owners.
type suppliedOwnerResolver interface {
	SuppliedOwnerIDs(ctx context.Context, owner, repo string) ([]int, error)
}

// unresolvedSuppliedTokenNames returns the project access token names of the
// roles that record a supplied credential without an owner ID: the built-in
// token name for the three built-in roles and the custom-role name otherwise.
func unresolvedSuppliedTokenNames(state rotationStateFile) []string {
	var names []string
	for role, rs := range state.Roles {
		if !provenanceOf(rs).Supplied || rs.SuppliedUserID > 0 {
			continue
		}
		switch gitlabroles.Role(role) {
		case gitlabroles.RolePoller:
			names = append(names, gitlabroles.PollerTokenName, gitlabroles.PollerBootstrapTokenName)
		case gitlabroles.RoleAnalyst:
			names = append(names, gitlabroles.AnalystTokenName)
		case gitlabroles.RoleCoder:
			names = append(names, gitlabroles.CoderTokenName)
		default:
			names = append(names, gitlabroles.CustomTokenName(gitlabroles.Role(role)))
		}
	}
	sort.Strings(names)
	return names
}

// positiveIDs returns a copy of ids without nonpositive entries.
func positiveIDs(ids []int) []int {
	var out []int
	for _, id := range ids {
		if id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// resolveSuppliedOwnersViaCapability resolves administrator-supplied owners
// through the optional SuppliedOwnerIDs capability of a generic token client.
// An absent capability, a resolution error, and an empty or invalid-ID-only
// result all fail closed with an error that attributes nothing.
func resolveSuppliedOwnersViaCapability(ctx context.Context, tokens ProjectAccessTokenClient, owner, repo string) ([]int, error) {
	resolver, ok := tokens.(suppliedOwnerResolver)
	if !ok {
		return nil, ErrSuppliedCredentialUnresolved
	}
	ids, err := resolver.SuppliedOwnerIDs(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	if ids = positiveIDs(ids); len(ids) == 0 {
		return nil, ErrSuppliedCredentialUnresolved
	}
	return ids, nil
}

func appendExcludedID(ids []int, id int) []int {
	if id > 0 && !containsInt(ids, id) {
		ids = append(ids, id)
	}
	return ids
}

// retireRotationState removes the rotation document once cleanup succeeded,
// except for the supplied-account exclusions. Uninstall preserves administrator-
// supplied service accounts and their tokens, so the only durable record that
// those accounts are not fullsend's survives as an exclusions-only document: a
// later install or repeated uninstall would otherwise treat a same-named
// supplied account as managed. Everything else in the document (phases, token
// IDs, locks, provenance of installed credentials) is disposable and dropped. A
// document with no exclusions is deleted outright. frozen is the exclusion set
// preflight froze for this cleanup. It can include supplied owners resolved only
// through the supplied-account callback (a supplied enrollment recorded with no
// user ID), which the stored document does not name, so they are persisted too.
func retireRotationState(ctx context.Context, cfg GitLabRoleCleanupConfig, frozen []int, result *GitLabRoleCleanupResult) error {
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
	// Exclusions are project-wide, so any role entry can carry the frozen owners
	// that are not yet recorded. Prefer a supplied enrollment whose owner was
	// never stored, then the first role by name.
	if missing := missingExclusions(retained, frozen); len(missing) > 0 {
		if role, ok := exclusionCarrierRole(state); ok {
			keep := retained.Roles[role]
			for _, id := range missing {
				keep.excludeOwner(id)
			}
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

func missingExclusions(retained rotationStateFile, frozen []int) []int {
	var missing []int
	for _, id := range frozen {
		found := false
		for _, rs := range retained.Roles {
			if containsInt(rs.ExcludedUserIDs, id) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, id)
		}
	}
	return missing
}

func exclusionCarrierRole(state rotationStateFile) (string, bool) {
	roles := make([]string, 0, len(state.Roles))
	for role := range state.Roles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		rs := state.Roles[role]
		if provenanceOf(rs).Supplied && rs.SuppliedUserID <= 0 {
			return role, true
		}
	}
	if len(roles) > 0 {
		return roles[0], true
	}
	// A document with no role entries (for example {"roles":{}}) has nothing
	// to carry the frozen owners. Exclusions are project-wide, so use the
	// Poller entry, the role that enrolls supplied credentials, rather than
	// dropping the durable record.
	return string(gitlabroles.RolePoller), true
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
	return revokeGitLabIdentityTokensExcluding(ctx, tokens, owner, repo, nil)
}

// revokeGitLabIdentityTokensExcluding is revokeGitLabIdentityTokens that skips
// tokens whose owner is in excluded (administrator-owned accounts recorded in
// rotation state), whatever the token client implementation. When exclusions
// exist, a token that does not report an owner cannot be proven not to belong to
// an excluded account, so it is left in place and reported as an error.
func revokeGitLabIdentityTokensExcluding(ctx context.Context, tokens ProjectAccessTokenClient, owner, repo string, excluded []int) (int, string, error) {
	return revokeGitLabIdentityTokensSkipping(ctx, tokens, owner, repo, excluded, nil)
}

// revokeGitLabIdentityTokensSkipping is revokeGitLabIdentityTokensExcluding that
// also leaves every token whose name is in preservedNames untouched. Cleanup
// uses it for supplied credentials whose owner cannot be resolved.
func revokeGitLabIdentityTokensSkipping(ctx context.Context, tokens ProjectAccessTokenClient, owner, repo string, excluded []int, preservedNames []string) (int, string, error) {
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
		if slices.Contains(preservedNames, tok.Name) {
			continue
		}
		if tok.UserID > 0 && containsInt(excluded, tok.UserID) {
			continue
		}
		if tok.UserID <= 0 && len(excluded) > 0 {
			// An unattributable token could belong to an administrator-owned
			// account recorded as excluded. Never revoke it on a guess: report
			// an ownership-verification error so cleanup keeps the rotation
			// state and can be retried with an ownership-aware client.
			errs = append(errs, fmt.Errorf("not revoking GitLab identity token %s (id %d): owner cannot be verified while administrator-owned accounts are excluded", tok.Name, tok.ID))
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
