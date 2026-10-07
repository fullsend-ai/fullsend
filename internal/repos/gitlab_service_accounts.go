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

// GitLabServiceAccount is a GitLab project service account. Fullsend
// names each role service account after its role token name
// (fullsend-poller, fullsend-analyst, fullsend-coder,
// fullsend-role-<name>).
type GitLabServiceAccount struct {
	ID   int
	Name string
}

// GitLabServiceAccountAPI is the subset of the GitLab API needed to
// manage role service accounts, their personal access tokens, and their
// project membership.
type GitLabServiceAccountAPI interface {
	CreateProjectServiceAccount(ctx context.Context, owner, repo, name string) (GitLabServiceAccount, error)
	// DeleteProjectServiceAccount removes a positively identified account whose
	// creation could not be recorded, before any credentials are minted.
	DeleteProjectServiceAccount(ctx context.Context, owner, repo string, userID int) error
	ListProjectServiceAccounts(ctx context.Context, owner, repo string) ([]GitLabServiceAccount, error)
	CreateServiceAccountPAT(ctx context.Context, owner, repo string, userID int, name string, scopes []string, expiresAt string) (*ProjectAccessToken, error)
	ListServiceAccountPATs(ctx context.Context, owner, repo string, userID int) ([]ProjectAccessToken, error)
	RevokeServiceAccountPAT(ctx context.Context, owner, repo string, userID, tokenID int) error
	AddProjectMember(ctx context.Context, owner, repo string, userID int64, accessLevel int) error
	UpdateProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64, accessLevel int) error
	// GetProjectMemberAccessLevel returns the effective access level,
	// including access inherited from groups.
	GetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64) (int, error)
}

// ServiceAccountTokenClient provisions GitLab role credentials as
// personal access tokens of per-role project service accounts, so each
// role identity is an ordinary project member whose access level can be
// changed (issue #8083). Project access token bots cannot be re-leveled,
// which is why the Poller must be a service account to own the webhook
// trigger token.
//
// It implements ProjectAccessTokenClient so provisioning, rotation,
// status, pipeline access, and uninstall see service-account tokens and
// any remaining legacy project access tokens through one inventory.
// When the instance does not offer project service accounts (404, 403,
// or not supported), Create falls back to Legacy project access tokens.
type ServiceAccountTokenClient struct {
	// ContainmentClient supplies durable ownership and secret deletion for
	// access failures at the token creation boundary. Live adapters set it.
	ContainmentClient forge.Client
	// InstalledRoleCredentialOwner attributes the live secret before containment.
	// Missing or unverifiable attribution preserves the secret.
	InstalledRoleCredentialOwner func(context.Context, string, string, string) (int, error)
	// ManagedLegacyTokenIDs supplies positive creation records for destructive
	// legacy operations. A nil callback supplies no ownership evidence.
	ManagedLegacyTokenIDs func(context.Context, string, string) ([]int, error)
	// LegacyTokenCreated durably records a token returned by this creation call.
	LegacyTokenCreated func(context.Context, string, string, *ProjectAccessToken) error
	SA                 GitLabServiceAccountAPI
	Legacy             ProjectAccessTokenClient
	// requireServiceAccount disables creation fallback during convergence while
	// retaining legacy credential inventory and revocation for grace cleanup.
	requireServiceAccount bool
	// AccountCreated durably records accounts created by this installer. Minting
	// a PAT on an existing same-named account is not ownership of that account.
	AccountCreated func(ctx context.Context, owner, repo string, account GitLabServiceAccount) error
	// ManagedAccountIDs supplies durable creation provenance for existing
	// account reuse and lifecycle inventories. Live adapters must set it;
	// nil is retained for callers that supply an independently trusted API.
	ManagedAccountIDs func(ctx context.Context, owner, repo string) ([]int, error)
	// ManagedAccountNames supplies the expected display name for each durably
	// owned account. Live adapters set it alongside ManagedAccountIDs to reject
	// cross-role renames. Nil retains generic role-name validation for callers
	// with an independently trusted API. Supplied exclusions take precedence.
	ManagedAccountNames func(ctx context.Context, owner, repo string) (map[int]string, error)
	// VerifyToken authenticates a newly minted credential before publication.
	VerifyToken func(ctx context.Context, owner, repo string, token *ProjectAccessToken) error
	// VerifyAccountDeletion checks unrelated credentials and owned resources.
	VerifyAccountDeletion func(ctx context.Context, owner, repo string, userID int) error
	// SuppliedAccountIDs, when set, returns the service accounts that own
	// administrator-supplied credentials for any role, including owners of
	// credentials since replaced. They are never selected, re-leveled,
	// inventoried for destructive cleanup, or scheduled for revocation: a
	// same-named account is not fullsend's to manage. Their token metadata
	// stays in the operational inventory so rotation can judge lifecycle. It
	// is consulted only when the project has a role service account, and an
	// error fails the operation closed.
	SuppliedAccountIDs func(ctx context.Context, owner, repo string) ([]int, error)
	// SuppliedTokenIDs, when set, returns for each supplied owner whose
	// enrolled credential's token ID was recorded, the IDs of those tokens. The
	// operational inventory then reports only those tokens of such an account as
	// the supplied credential; the account's other tokens are not fullsend's and
	// stay out of lifecycle analysis. An account absent from the result has no
	// recorded token ID, so all of its tokens stand in for the credential. An
	// error fails the operation closed. Each token is reported under the token
	// name of the role it was enrolled for, not the account's display name.
	SuppliedTokenIDs func(ctx context.Context, owner, repo string) (map[int][]SuppliedTokenRef, error)
	// CurrentSuppliedAccountIDs, when set, returns the subset of
	// SuppliedAccountIDs that own a currently installed supplied credential.
	// The operational inventory reports only those accounts' tokens; an
	// excluded account whose supplied credential was since replaced is a
	// historical exclusion for destructive operations only, and its tokens are
	// not the runtime credential so they must not stand in for the role. When
	// nil, every supplied owner is treated as current. An error fails the
	// operation closed.
	CurrentSuppliedAccountIDs func(ctx context.Context, owner, repo string) ([]int, error)
}

// SuppliedTokenRef is an enrolled administrator-supplied credential's GitLab
// token ID together with the token name of the role it was enrolled for.
type SuppliedTokenRef struct {
	ID   int
	Name string
}

var _ ProjectAccessTokenClient = ServiceAccountTokenClient{}

// serviceAccountsUnavailable reports whether err means the instance or
// plan does not offer project service accounts to this caller.
func serviceAccountsUnavailable(err error) bool {
	return forge.IsNotFound(err) || forge.IsForbidden(err) || forge.IsNotSupported(err)
}

// serviceAccountsAbsent reports whether err positively establishes that
// the project offers no service accounts, so none can hold tokens to
// revoke. Unlike serviceAccountsUnavailable it excludes 403: GitLab returns
// that for plan gating and missing permissions alike, so service-account
// PATs may exist that the installer cannot see. Destructive cleanup treats
// a forbidden listing as an incomplete inventory.
func serviceAccountsAbsent(err error) bool {
	return forge.IsNotFound(err) || forge.IsNotSupported(err)
}

// incompleteInventoryError reports a token inventory that could not be
// fully enumerated. It keeps every failure in its message but exposes only
// unclassified causes to errors.Is/As: a 403 or 404 from a partial listing
// must never read as "nothing to revoke" or "capability unavailable" to
// callers that act destructively on the inventory.
type incompleteInventoryError struct {
	errs []error
}

func (e *incompleteInventoryError) Error() string {
	return "incomplete GitLab role token inventory: " + errors.Join(e.errs...).Error()
}

func (e *incompleteInventoryError) Unwrap() []error {
	var out []error
	for _, err := range e.errs {
		if !forge.IsNotFound(err) && !forge.IsForbidden(err) && !forge.IsNotSupported(err) {
			out = append(out, err)
		}
	}
	return out
}

// managedServiceAccountList returns every role service account in the
// project, ordered by name then ID. Inventory and revocation use the full
// list so tokens on duplicate accounts are never skipped.
//
// Accounts that own administrator-supplied credentials are dropped when
// excludeSupplied is set, which every provisioning, membership, and
// destructive boundary requires. The operational lifecycle inventory passes
// false: it only reads token metadata, and rotation needs a healthy supplied
// credential's expiry to judge that it is not due.
func (c ServiceAccountTokenClient) managedServiceAccountList(ctx context.Context, owner, repo string, excludeSupplied bool) ([]GitLabServiceAccount, error) {
	// Ownership errors wrap only ErrPollerSuppliedUnresolved. Sanitized API
	// causes are flattened intentionally: exposing their capability sentinels
	// would turn an unresolved ownership check into legacy fallback or absence.
	accounts, err := c.SA.ListProjectServiceAccounts(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	var supplied []int
	if len(accounts) > 0 && c.SuppliedAccountIDs != nil {
		// Every role's administrator-owned accounts are excluded, whatever their
		// display names, not only the Poller's.
		supplied, err = c.suppliedOwnerIDs(ctx, owner, repo)
		if err != nil {
			return nil, err
		}
	}
	var owned []int
	if c.ManagedAccountIDs != nil {
		owned, err = c.ManagedAccountIDs(ctx, owner, repo)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrPollerSuppliedUnresolved, safeAPIError("resolving managed account ownership", err))
		}
	}
	var expectedNames map[int]string
	if c.ManagedAccountNames != nil {
		expectedNames, err = c.ManagedAccountNames(ctx, owner, repo)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrPollerSuppliedUnresolved, safeAPIError("resolving managed account display names", err))
		}
	}
	var out []GitLabServiceAccount
	for _, sa := range accounts {
		isSupplied := slices.Contains(supplied, sa.ID)
		if excludeSupplied && isSupplied {
			continue
		}
		expectedName, namedOwner := expectedNames[sa.ID]
		isOwned := slices.Contains(owned, sa.ID) || namedOwner
		isRoleName := gitlabroles.IsRoleProjectTokenName(sa.Name)
		// Check durable ownership against the complete inventory before name
		// filtering. A renamed owned account still holds its credentials; it
		// must not disappear from cleanup and let ownership state be retired.
		// Supplied exclusions override ownership even in read-only inventories.
		unexpectedName := !isRoleName
		if c.ManagedAccountNames != nil {
			unexpectedName = !namedOwner || expectedName == "" || sa.Name != expectedName
		}
		if isOwned && !isSupplied && unexpectedName {
			// This local drift error deliberately carries no forge capability
			// classification: callers must not fall back to legacy tokens.
			return nil, &managedServiceAccountNameDriftError{userID: sa.ID}
		}
		if (!isRoleName && !isSupplied) || ((c.ManagedAccountIDs != nil || c.ManagedAccountNames != nil) && !isOwned && !isSupplied) {
			continue
		}
		out = append(out, sa)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

type managedServiceAccountNameDriftError struct {
	userID int
}

func (e *managedServiceAccountNameDriftError) Error() string {
	return fmt.Sprintf("managed GitLab service account %d has an unexpected display name; restore its Fullsend role name before retrying; ownership retained for cleanup", e.userID)
}

// managedServiceAccounts returns the project's role service accounts by
// name for provisioning. Duplicates (for example from a retried create)
// resolve to the lowest ID so every caller picks the same account.
func (c ServiceAccountTokenClient) managedServiceAccounts(ctx context.Context, owner, repo string) (map[string]GitLabServiceAccount, error) {
	accounts, err := c.managedServiceAccountList(ctx, owner, repo, true)
	if err != nil {
		return nil, err
	}
	out := make(map[string]GitLabServiceAccount)
	for _, sa := range accounts {
		if _, ok := out[sa.Name]; !ok {
			out[sa.Name] = sa
		}
	}
	return out, nil
}

// CreateProjectAccessToken ensures the role service account named name
// exists and is a Developer project member, then mints a personal
// access token for it. The returned token's UserID is the service
// account's user ID. Access above Developer is refused: role identities
// never hold standing Maintainer access.
func (c ServiceAccountTokenClient) CreateProjectAccessToken(ctx context.Context, owner, repo, name string, scopes []string, accessLevel int, expiresAt string) (*ProjectAccessToken, error) {
	if accessLevel > gitlabroles.DeveloperAccessLevel {
		return nil, fmt.Errorf("refusing to provision GitLab role identity %q above Developer access (requested %d)", name, accessLevel)
	}
	if c.SA == nil {
		return c.legacyCreate(ctx, owner, repo, name, scopes, accessLevel, expiresAt, nil)
	}
	accounts, err := c.managedServiceAccounts(ctx, owner, repo)
	if err != nil {
		if serviceAccountsUnavailable(err) {
			return c.legacyCreate(ctx, owner, repo, name, scopes, accessLevel, expiresAt, err)
		}
		return nil, fmt.Errorf("listing GitLab project service accounts: %w", err)
	}
	sa, ok := accounts[name]
	if !ok {
		created, createErr := c.SA.CreateProjectServiceAccount(ctx, owner, repo, name)
		if createErr != nil {
			if serviceAccountsUnavailable(createErr) {
				return c.legacyCreate(ctx, owner, repo, name, scopes, accessLevel, expiresAt, createErr)
			}
			return nil, fmt.Errorf("creating GitLab project service account %q: %w", name, createErr)
		}
		sa = created
		if c.AccountCreated != nil {
			if err := c.AccountCreated(ctx, owner, repo, sa); err != nil {
				// No PAT or membership has been issued. This ID came directly
				// from create, not a same-named inventory match, so cleanup is
				// authorized even when the ownership write failed ambiguously.
				cleanupCtx, cancel := gitlabCleanupContext(ctx)
				deleteErr := c.SA.DeleteProjectServiceAccount(cleanupCtx, owner, repo, sa.ID)
				cancel()
				if forge.IsNotFound(deleteErr) {
					deleteErr = nil
				}
				return nil, errors.Join(safeAPIError("recording created service-account ownership", err), safeAPIError(fmt.Sprintf("deleting newly created service account %d after ownership could not be recorded; administrator cleanup is required if deletion failed", sa.ID), deleteErr))
			}
		}
	}
	if err := c.ensureMemberLevel(ctx, owner, repo, sa, accessLevel); err != nil {
		return nil, errors.Join(err, c.containCreationAccessFailure(ctx, owner, repo, name, sa.ID))
	}
	// The direct membership is at the requested level, but access inherited
	// from a group or a shared project can still exceed it. A credential is
	// never minted for an identity whose effective access cannot be shown to
	// stay within the ceiling.
	effective, err := c.SA.GetProjectMemberAccessLevel(ctx, owner, repo, int64(sa.ID))
	if err != nil {
		return nil, errors.Join(c.containCreationAccessFailure(ctx, owner, repo, name, sa.ID), safeAPIError(fmt.Sprintf("verifying the effective project access of GitLab service account %q (user ID %d) before issuing its token", name, sa.ID), err))
	}
	if effective != accessLevel {
		comparison := "does not match"
		if effective > accessLevel {
			comparison = "exceeds"
		}
		return nil, errors.Join(c.containCreationAccessFailure(ctx, owner, repo, name, sa.ID), fmt.Errorf("refusing to issue a token for GitLab service account %q (user ID %d): its effective project access (%d, including inherited membership) %s the requested level %d. Repair the membership, then re-run", name, sa.ID, effective, comparison, accessLevel))
	}
	tok, err := c.SA.CreateServiceAccountPAT(ctx, owner, repo, sa.ID, name, scopes, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("creating token for GitLab service account %q (user ID %d): %w", name, sa.ID, err)
	}
	if tok == nil {
		return nil, fmt.Errorf("service account %q returned no token", name)
	}
	if tok.UserID != 0 && tok.UserID != sa.ID {
		return nil, fmt.Errorf("service account %q returned a token for a different identity", name)
	}
	tok.UserID = sa.ID
	if tok.Name == "" {
		tok.Name = name
	}
	if c.VerifyToken != nil {
		if err := c.VerifyToken(ctx, owner, repo, tok); err != nil {
			cleanupCtx, cancel := gitlabCleanupContext(ctx)
			defer cancel()
			revokeErr := c.SA.RevokeServiceAccountPAT(cleanupCtx, owner, repo, sa.ID, tok.ID)
			return nil, errors.Join(safeAPIError("validating the replacement service-account credential", err), safeAPIError("revoking the invalid replacement credential", revokeErr))
		}
	}
	return tok, nil
}

// ensureMemberLevel makes the service account a direct project member at
// exactly accessLevel, adding it or correcting an existing membership.
func (c ServiceAccountTokenClient) ensureMemberLevel(ctx context.Context, owner, repo string, sa GitLabServiceAccount, accessLevel int) error {
	err := c.SA.AddProjectMember(ctx, owner, repo, int64(sa.ID), accessLevel)
	if err == nil {
		return nil
	}
	if !forge.IsAlreadyExists(err) {
		return fmt.Errorf("adding GitLab service account %q (user ID %d) as a project member: %w", sa.Name, sa.ID, err)
	}
	if err := c.SA.UpdateProjectMemberAccessLevel(ctx, owner, repo, int64(sa.ID), accessLevel); err != nil {
		return fmt.Errorf("setting GitLab service account %q (user ID %d) project access level to %d: %w", sa.Name, sa.ID, accessLevel, err)
	}
	return nil
}

func (c ServiceAccountTokenClient) legacyCreate(ctx context.Context, owner, repo, name string, scopes []string, accessLevel int, expiresAt string, saErr error) (*ProjectAccessToken, error) {
	if c.requireServiceAccount || c.Legacy == nil {
		if saErr != nil {
			return nil, fmt.Errorf("GitLab project service accounts unavailable for %q: %w", name, saErr)
		}
		return nil, fmt.Errorf("no GitLab token client configured for %q", name)
	}
	tok, err := c.Legacy.CreateProjectAccessToken(ctx, owner, repo, name, scopes, accessLevel, expiresAt)
	if err != nil || tok == nil {
		return tok, err
	}
	if c.LegacyTokenCreated != nil {
		if err := c.LegacyTokenCreated(ctx, owner, repo, tok); err != nil {
			cleanupCtx, cancel := gitlabCleanupContext(ctx)
			defer cancel()
			return nil, errors.Join(safeAPIError("recording newly created legacy token ownership", err), safeAPIError("revoking unpublished legacy token", c.Legacy.RevokeProjectAccessToken(cleanupCtx, owner, repo, tok.ID)))
		}
	}
	return tok, nil
}

// ListProjectAccessTokens returns legacy project access tokens together
// with the personal access tokens of every role service account. Uninstall
// revokes from this inventory, so an inventory source is skipped only when
// its absence is positively established: the service-account listing is
// absent (404/not supported) before any account was discovered, or the
// legacy listing is not supported or not found. Failures after
// accounts were discovered, and legacy authorization failures, make the
// inventory incomplete and are returned as errors. When both sources fail,
// both failures are preserved; the result matches forge.ErrNotFound only
// when each source reported not found.
//
// Operational consumers (rotation, status, protected-ref reconciliation) use
// ListProjectAccessTokens, which also accepts a forbidden listing of one
// source when the other succeeded: GitLab returns 403 for plan gating, so
// requiring both sources would stall the lifecycle on plan-gated instances.
// Destructive cleanup uses ListProjectAccessTokensStrict instead.
func (c ServiceAccountTokenClient) ListProjectAccessTokens(ctx context.Context, owner, repo string) ([]ProjectAccessToken, error) {
	return c.listInventory(ctx, owner, repo, serviceAccountsUnavailable, legacyListingUnavailable, false)
}

// StrictTokenInventory is implemented by token clients whose inventory has
// a stricter variant for destructive cleanup.
type StrictTokenInventory interface {
	ListProjectAccessTokensStrict(ctx context.Context, owner, repo string) ([]ProjectAccessToken, error)
}

var _ StrictTokenInventory = ServiceAccountTokenClient{}

// ListProjectAccessTokensStrict is ListProjectAccessTokens for destructive
// cleanup: a forbidden listing of either source is an incomplete inventory,
// because credentials may exist that the installer cannot see. It also lists
// the installer-only Poller bootstrap tokens an interrupted trigger-creation
// transaction can leave active, which operational consumers (rotation
// selection, status) must not see as runtime credentials.
func (c ServiceAccountTokenClient) ListProjectAccessTokensStrict(ctx context.Context, owner, repo string) ([]ProjectAccessToken, error) {
	return c.listInventory(ctx, owner, repo, serviceAccountsAbsent, legacyListingAbsent, true)
}

func (c ServiceAccountTokenClient) listInventory(ctx context.Context, owner, repo string, saSkippable, legacySkippable func(error) bool, includeBootstrap bool) ([]ProjectAccessToken, error) {
	var legacy []ProjectAccessToken
	var legacyErr error
	if c.Legacy != nil {
		legacy, legacyErr = c.Legacy.ListProjectAccessTokens(ctx, owner, repo)
		if legacyErr == nil {
			// Destructive cleanup (the strict inventory) never offers legacy
			// project access tokens owned by an administrator-supplied account.
			// Operational consumers keep them: rotation needs their expiry to
			// judge lifecycle, and revocation is refused for them at the
			// RevokeProjectAccessToken boundary instead.
			var filterErr error
			if legacy, filterErr = c.withoutSuppliedLegacy(ctx, owner, repo, legacy, includeBootstrap); filterErr != nil {
				return nil, filterErr
			}
		}
	} else {
		legacyErr = forge.ErrNotSupported
	}
	saTokens, saErr := c.listServiceAccountTokens(ctx, owner, repo, includeBootstrap)

	switch {
	case legacyErr == nil && saErr == nil:
		return append(legacy, saTokens...), nil
	case legacyErr == nil && saSkippable(saErr):
		return legacy, nil
	case legacyErr == nil:
		return nil, &incompleteInventoryError{errs: []error{safeAPIError("listing GitLab service account tokens", saErr)}}
	case saErr == nil && legacySkippable(legacyErr):
		return saTokens, nil
	case saErr == nil:
		return nil, &incompleteInventoryError{errs: []error{safeAPIError("listing GitLab project access tokens", legacyErr)}}
	case c.Legacy == nil:
		return nil, safeAPIError("listing GitLab service account tokens", saErr)
	case forge.IsNotFound(legacyErr) && forge.IsNotFound(saErr):
		return nil, errors.Join(safeAPIError("listing GitLab project access tokens", legacyErr), safeAPIError("listing GitLab service account tokens", saErr))
	default:
		return nil, &incompleteInventoryError{errs: []error{safeAPIError("listing GitLab project access tokens", legacyErr), safeAPIError("listing GitLab service account tokens", saErr)}}
	}
}

// suppliedOwnerIDs returns the accounts that own administrator-supplied role
// credentials. An error fails the caller closed and is flattened to text so a
// 403/404 from the ownership lookup is never mistaken for "capability
// unavailable".
func (c ServiceAccountTokenClient) suppliedOwnerIDs(ctx context.Context, owner, repo string) ([]int, error) {
	if c.SuppliedAccountIDs == nil {
		return nil, nil
	}
	excluded, err := c.SuppliedAccountIDs(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrPollerSuppliedUnresolved, safeAPIError("resolving the owner of the supplied role credentials", err))
	}
	return excluded, nil
}

// SuppliedOwnerIDs returns the project-wide set of accounts that own
// administrator-supplied role credentials, the same exclusion set every
// destructive boundary of this client applies. A nil lookup yields none.
func (c ServiceAccountTokenClient) SuppliedOwnerIDs(ctx context.Context, owner, repo string) ([]int, error) {
	return c.suppliedOwnerIDs(ctx, owner, repo)
}

// withoutSuppliedLegacy drops role-named legacy project access tokens that are
// lacking explicit creation provenance. Destructive inventory also excludes
// administrator-supplied owners. Names and distribution backfill never confer
// ownership, so unknown tokens cannot become outgoing rotation credentials.
func (c ServiceAccountTokenClient) withoutSuppliedLegacy(ctx context.Context, owner, repo string, toks []ProjectAccessToken, excludeSupplied bool) ([]ProjectAccessToken, error) {
	if !slices.ContainsFunc(toks, func(t ProjectAccessToken) bool { return gitlabroles.IsRoleProjectTokenName(t.Name) }) {
		return toks, nil
	}
	excluded, err := c.suppliedOwnerIDs(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	owned, err := c.managedLegacyIDs(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(slices.Clone(toks), func(t ProjectAccessToken) bool {
		return gitlabroles.IsRoleProjectTokenName(t.Name) && (!(slices.Contains(owned, t.ID) || (!excludeSupplied && t.UserID > 0 && slices.Contains(excluded, t.UserID))) || (excludeSupplied && len(excluded) > 0 && (t.UserID == 0 || slices.Contains(excluded, t.UserID))))
	}), nil
}

// refuseSuppliedLegacy is the revocation boundary for legacy project access
// tokens: explicit creation provenance is required, and supplied-owner
// exclusions override that provenance.
func (c ServiceAccountTokenClient) refuseSuppliedLegacy(ctx context.Context, owner, repo string, tokenID int) error {
	owned, err := c.managedLegacyIDs(ctx, owner, repo)
	if err != nil {
		return err
	}
	if !slices.Contains(owned, tokenID) {
		return fmt.Errorf("refusing to revoke legacy GitLab token %d without positive creation provenance; manual recovery required", tokenID)
	}
	excluded, err := c.suppliedOwnerIDs(ctx, owner, repo)
	if err != nil || len(excluded) == 0 {
		return err
	}
	toks, err := c.Legacy.ListProjectAccessTokens(ctx, owner, repo)
	if err != nil {
		return safeAPIError(fmt.Sprintf("verifying the owner of legacy GitLab token %d before revoking it", tokenID), err)
	}
	for _, tok := range toks {
		if tok.ID != tokenID {
			continue
		}
		if tok.UserID == 0 || slices.Contains(excluded, tok.UserID) {
			return fmt.Errorf("%w: refusing to revoke legacy GitLab token %d: it may belong to an administrator-supplied credential", ErrPollerSuppliedUnresolved, tokenID)
		}
		return nil
	}
	return fmt.Errorf("legacy GitLab token %d not found: %w", tokenID, forge.ErrNotFound)
}

func (c ServiceAccountTokenClient) managedLegacyIDs(ctx context.Context, owner, repo string) ([]int, error) {
	if c.ManagedLegacyTokenIDs == nil {
		return nil, nil
	}
	ids, err := c.ManagedLegacyTokenIDs(ctx, owner, repo)
	if err != nil {
		return nil, safeAPIError("reading legacy token creation provenance", err)
	}
	return ids, nil
}

// ManagedGitLabLegacyTokenIDs returns explicit creation records, never inferred
// IncomingID/outgoing/name-based distribution proof. Callers fail closed.
func ManagedGitLabLegacyTokenIDs(ctx context.Context, client forge.Client, owner, repo string) ([]int, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, rs := range state.Roles {
		ids = append(ids, rs.CreatedTokenIDs...)
	}
	return uniqueInts(ids), nil
}

// RecordManagedGitLabLegacyToken records only the result of a token creation
// performed by this installer. It must never be called from inventory/backfill.
func RecordManagedGitLabLegacyToken(ctx context.Context, client forge.Client, owner, repo string, token *ProjectAccessToken) error {
	if token == nil || token.ID <= 0 {
		return fmt.Errorf("created token has no ID")
	}
	raw, _, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRegistry)
	if err != nil {
		return err
	}
	reg, err := gitlabroles.ParseRegistry(raw)
	if err != nil {
		return err
	}
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return err
	}
	for _, rec := range reg.Registrations() {
		name := rec.Credential.TokenName
		if name == "" {
			name = gitlabroles.CustomTokenName(rec.Name)
		}
		if name != token.Name {
			continue
		}
		rs := state.Roles[string(rec.Name)]
		rs.CreatedTokenIDs = uniqueInts(append(rs.CreatedTokenIDs, token.ID))
		state.Roles[string(rec.Name)] = rs
		return writeRotationState(ctx, client, owner, repo, state)
	}
	return fmt.Errorf("created token does not match a registered role")
}

// legacyListingAbsent reports whether err positively establishes that the
// legacy project access token listing is not offered. 403 is not enough:
// GitLab returns it for plan gating, group-level disablement, and missing
// permissions alike, so tokens may still exist.
func legacyListingAbsent(err error) bool {
	return forge.IsNotFound(err) || forge.IsNotSupported(err)
}

// legacyListingUnavailable is legacyListingAbsent for operational consumers:
// it also accepts 403, since service-account tokens were listed successfully.
func legacyListingUnavailable(err error) bool {
	return legacyListingAbsent(err) || forge.IsForbidden(err)
}

func (c ServiceAccountTokenClient) listServiceAccountTokens(ctx context.Context, owner, repo string, includeBootstrap bool) ([]ProjectAccessToken, error) {
	if c.SA == nil {
		return nil, forge.ErrNotSupported
	}
	// includeBootstrap marks the destructive inventory, which never offers a
	// supplied account's tokens; the operational one retains them read-only.
	accounts, err := c.managedServiceAccountList(ctx, owner, repo, includeBootstrap)
	if err != nil {
		return nil, err
	}
	var out []ProjectAccessToken
	var supplied []int
	suppliedResolved := false
	var current []int
	currentResolved := false
	var enrolled map[int][]SuppliedTokenRef
	enrolledResolved := false
	for _, sa := range accounts {
		// An administrator-supplied credential may carry any token name. The
		// operational inventory reports every token of a supplied owner's
		// account under the role name so lifecycle analysis sees a healthy
		// supplied credential's expiry, unless the enrolled credential's own
		// token ID was recorded: then only that token is reported, so an
		// unrelated token on the same account cannot mask its expiry or
		// revocation. The destructive inventory never lists these accounts at
		// all. The ownership lookup runs for every account of the operational
		// inventory, whatever its tokens are named, and fails closed.
		suppliedOwner := false
		if !includeBootstrap {
			if !suppliedResolved {
				supplied, err = c.suppliedOwnerIDs(ctx, owner, repo)
				if err != nil {
					return nil, &incompleteInventoryError{errs: []error{err}}
				}
				suppliedResolved = true
			}
			suppliedOwner = slices.Contains(supplied, sa.ID)
			if suppliedOwner && c.CurrentSuppliedAccountIDs != nil {
				if !currentResolved {
					current, err = c.CurrentSuppliedAccountIDs(ctx, owner, repo)
					if err != nil {
						return nil, &incompleteInventoryError{errs: []error{fmt.Errorf("%w: %s", ErrPollerSuppliedUnresolved, safeAPIError("resolving the owners of the installed supplied role credentials", err))}}
					}
					currentResolved = true
				}
				if !slices.Contains(current, sa.ID) {
					// A historical exclusion: the supplied credential this
					// account owned was replaced, so its tokens are not the
					// role's runtime credential.
					continue
				}
			}
		}
		var enrolledRefs []SuppliedTokenRef
		if suppliedOwner && c.SuppliedTokenIDs != nil {
			if !enrolledResolved {
				enrolled, err = c.SuppliedTokenIDs(ctx, owner, repo)
				if err != nil {
					return nil, &incompleteInventoryError{errs: []error{fmt.Errorf("%w: %s", ErrPollerSuppliedUnresolved, safeAPIError("resolving the enrolled supplied role credential tokens", err))}}
				}
				enrolledResolved = true
			}
			enrolledRefs = enrolled[sa.ID]
		}
		// Tokens are listed only once the account is known to matter, so a
		// listing failure on a historical supplied account (skipped above) does
		// not fail the inventory.
		toks, err := c.SA.ListServiceAccountPATs(ctx, owner, repo, sa.ID)
		if err != nil {
			return nil, &incompleteInventoryError{errs: []error{safeAPIError(fmt.Sprintf("listing tokens of GitLab service account %s (user ID %d)", sa.Name, sa.ID), err)}}
		}
		for _, tok := range toks {
			if suppliedOwner {
				tok.Name = sa.Name
				if len(enrolledRefs) > 0 {
					// Report the token under the role it was enrolled for, so
					// a custom-role credential on an account named like
					// another role is not read as that role.
					for _, ref := range enrolledRefs {
						if ref.ID != tok.ID {
							continue
						}
						snapshot := tok
						if ref.Name != "" {
							snapshot.Name = ref.Name
						}
						snapshot.UserID = sa.ID
						out = append(out, snapshot)
					}
					continue
				}
				tok.UserID = sa.ID
				out = append(out, tok)
				continue
			}
			// The service account's tokens carry the role name; any other
			// token on the account is not fullsend-managed. The one exception
			// is the installer's Poller bootstrap token, identified by its
			// name on the Poller account, for destructive cleanup only.
			isBootstrap := includeBootstrap && sa.Name == gitlabroles.PollerTokenName && tok.Name == gitlabroles.PollerBootstrapTokenName
			if tok.Name != sa.Name && !isBootstrap {
				continue
			}
			tok.UserID = sa.ID
			out = append(out, tok)
		}
	}
	return out, nil
}

// RevokeProjectAccessToken revokes tokenID on whichever role service
// account owns it, or as a legacy project access token otherwise.
// GitLab project access tokens and service-account personal access
// tokens share one token ID space, so the ID is unambiguous.
func (c ServiceAccountTokenClient) RevokeProjectAccessToken(ctx context.Context, owner, repo string, tokenID int) error {
	if c.SA != nil {
		saTokens, err := c.listServiceAccountTokens(ctx, owner, repo, true)
		switch {
		case err == nil:
			for _, tok := range saTokens {
				if tok.ID == tokenID {
					return safeAPIError(fmt.Sprintf("revoking GitLab service account token %d", tokenID), c.SA.RevokeServiceAccountPAT(ctx, owner, repo, tok.UserID, tokenID))
				}
			}
		case !serviceAccountsUnavailable(err):
			return safeAPIError(fmt.Sprintf("locating GitLab token %d", tokenID), err)
		}
	}
	if c.Legacy == nil {
		return fmt.Errorf("GitLab token %d not found on any role service account: %w", tokenID, forge.ErrNotFound)
	}
	if err := c.refuseSuppliedLegacy(ctx, owner, repo, tokenID); err != nil {
		return err
	}
	return safeAPIError(fmt.Sprintf("revoking legacy GitLab token %d", tokenID), c.Legacy.RevokeProjectAccessToken(ctx, owner, repo, tokenID))
}

// ConfirmOutgoingTokenInactive verifies an outgoing rotation obligation can be
// retired without revocation. A token located in an authoritative source can
// be classified immediately. Every configured inventory must succeed to prove
// absence; neither a DELETE 404 nor an unavailable inventory establishes it.
func (c ServiceAccountTokenClient) ConfirmOutgoingTokenInactive(ctx context.Context, owner, repo string, tokenID int) (bool, error) {
	if c.SA == nil {
		return false, fmt.Errorf("service-account inventory unavailable")
	}
	// Inactivity is independent of destructive authorization. Inspect every
	// account and every PAT, including excluded owners and renamed tokens.
	accounts, err := c.SA.ListProjectServiceAccounts(ctx, owner, repo)
	if err != nil {
		return false, safeAPIError("verifying outgoing service-account token absence", err)
	}
	var tokens []ProjectAccessToken
	for _, account := range accounts {
		listed, err := c.SA.ListServiceAccountPATs(ctx, owner, repo, account.ID)
		if err != nil {
			return false, safeAPIError("verifying outgoing account PAT inventory", err)
		}
		for _, token := range listed {
			if token.ID == tokenID {
				return !token.Active || token.Revoked, nil
			}
		}
	}
	if c.Legacy != nil {
		legacy, err := c.Legacy.ListProjectAccessTokens(ctx, owner, repo)
		if err != nil {
			return false, safeAPIError("verifying outgoing legacy token absence", err)
		}
		tokens = append(tokens, legacy...)
	}
	for _, token := range tokens {
		if token.ID == tokenID {
			return !token.Active || token.Revoked, nil
		}
	}
	return true, nil
}

// SafeAPIError reports an operation failure without the server's error text,
// for callers outside this package that handle credential-bearing calls.
// errors.Is and errors.As still see the original error.
func SafeAPIError(op string, err error) error {
	return safeAPIError(op, err)
}
