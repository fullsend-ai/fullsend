package repos

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// GitLabServiceAccountStatus exposes non-secret identity and effective access.
type GitLabServiceAccountStatus struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	AccessLevel int    `json:"access_level"`
	Managed     bool   `json:"managed"`
}

// GitLabServiceAccountStatusAppender is the optional status capability exposed
// by role-token clients and wrappers, without requiring a concrete downcast.
// The returned boolean reports whether new entries were added to status.Drifts.
type GitLabServiceAccountStatusAppender interface {
	AppendGitLabServiceAccountStatus(ctx context.Context, client forge.Client, owner, repo string, status *RepoStatus) bool
}

// AppendGitLabServiceAccountStatus adds account details and permission drift.
// An inaccessible inventory is reported explicitly, never as an empty result.
// The returned boolean reports whether new entries were added to status.Drifts.
func (c ServiceAccountTokenClient) AppendGitLabServiceAccountStatus(ctx context.Context, client forge.Client, owner, repo string, status *RepoStatus) bool {
	if status == nil {
		return false
	}
	status.GitLabServiceAccounts = nil
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return serviceAccountStatusUnverified(status, "gitlab-service-accounts", "Service-account ownership could not be verified")
	}
	owned := map[int]bool{}
	for _, rs := range state.Roles {
		if rs.ManagedUserID > 0 {
			owned[rs.ManagedUserID] = true
		}
	}
	for _, rs := range state.Roles {
		for _, id := range rs.ExcludedUserIDs {
			owned[id] = false
		}
		if rs.SuppliedUserID > 0 {
			owned[rs.SuppliedUserID] = false
		}
	}
	if c.SA == nil {
		for _, managed := range owned {
			if managed {
				return serviceAccountStatusUnverified(status, "gitlab-service-accounts", "Service-account client unavailable for recorded managed accounts")
			}
		}
		return false
	}
	accounts, err := c.managedServiceAccountList(ctx, owner, repo, false)
	if err != nil {
		if serviceAccountsUnavailable(err) {
			for _, managed := range owned {
				if managed {
					return serviceAccountStatusUnverified(status, "gitlab-service-accounts", "Service-account inventory unavailable for recorded managed accounts")
				}
			}
			status.GitLabRoleDiagnostics = append(status.GitLabRoleDiagnostics, "Project service-account inventory unavailable; older instances, account limits and installer permissions may require administrator-supplied credentials")
			return false
		}
		diagnostic := safeAPIError("Service-account inventory could not be verified", err).Error()
		var nameDrift *managedServiceAccountNameDriftError
		if errors.As(err, &nameDrift) {
			diagnostic = nameDrift.Error()
		}
		return serviceAccountStatusUnverified(status, "gitlab-service-accounts", diagnostic)
	}
	changed := false
	present := make(map[int]bool, len(accounts))
	for _, sa := range accounts {
		present[sa.ID] = true
	}
	var missing []int
	for id, managed := range owned {
		if managed && !present[id] {
			missing = append(missing, id)
		}
	}
	sort.Ints(missing)
	for _, id := range missing {
		serviceAccountStatusUnverified(status, fmt.Sprintf("gitlab-service-account:%d", id), fmt.Sprintf("Recorded managed service account user %d is missing from inventory", id))
		changed = true
	}
	for _, sa := range accounts {
		level, err := c.SA.GetProjectMemberAccessLevel(ctx, owner, repo, int64(sa.ID))
		if err != nil {
			serviceAccountStatusUnverified(status, fmt.Sprintf("gitlab-service-account:%d", sa.ID), fmt.Sprintf("Service account user %d: effective access unavailable", sa.ID))
			changed = true
			continue
		}
		status.GitLabServiceAccounts = append(status.GitLabServiceAccounts, GitLabServiceAccountStatus{ID: sa.ID, Name: sa.Name, AccessLevel: level, Managed: owned[sa.ID]})
		status.GitLabRoleDiagnostics = append(status.GitLabRoleDiagnostics, fmt.Sprintf("Service account %q: user %d, effective access %d, managed=%t", sa.Name, sa.ID, level, owned[sa.ID]))
		if owned[sa.ID] && level != gitlabroles.DeveloperAccessLevel {
			status.GitLabRolesPartial = status.GitLabRolesPartial || status.GitLabRolesReady
			status.GitLabRolesReady = false
			status.Drifts = append(status.Drifts, Drift{Field: fmt.Sprintf("gitlab-service-account:%d", sa.ID), Expected: "Developer (30)", Actual: fmt.Sprint(level)})
			changed = true
		}
	}
	return changed
}

// serviceAccountStatusUnverified keeps configured roles from appearing ready
// when account ownership, inventory, or effective access cannot be verified.
func serviceAccountStatusUnverified(status *RepoStatus, field, diagnostic string) bool {
	status.GitLabRolesPartial = status.GitLabRolesPartial || status.GitLabRolesReady
	status.GitLabRolesReady = false
	status.GitLabRoleDiagnostics = append(status.GitLabRoleDiagnostics, "Role credentials unverified: "+diagnostic)
	status.Drifts = append(status.Drifts, Drift{Field: field, Expected: "verified service-account inventory and effective access", Actual: diagnostic})
	return true
}

// needsServiceAccountReplacement checks positive minted-token provenance,
// never just a name. Unsupported instances retain their existing credentials.
func (c ServiceAccountTokenClient) needsServiceAccountReplacement(ctx context.Context, owner, repo string, rs rotationRoleState) (bool, error) {
	if c.SA == nil || c.Legacy == nil || !provenanceOf(rs).Known || provenanceOf(rs).Supplied {
		return false, nil
	}
	accounts, err := c.managedServiceAccountList(ctx, owner, repo, true)
	if serviceAccountsUnavailable(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if rs.Phase == rotationPhaseDistributing {
		return true, nil // Use the existing recovery path, not another mint.
	}
	// A failed replacement-secret write clears IncomingID and keeps the prior
	// distributed credential among the outgoing IDs, so a failed migration is
	// recognized through positively owned outgoing legacy credentials.
	candidates := []int{rs.IncomingID}
	if rs.Phase == rotationPhaseFailed && rs.IncomingID == 0 {
		candidates = rs.OutgoingIDs
	}
	for _, sa := range accounts {
		tokens, err := c.SA.ListServiceAccountPATs(ctx, owner, repo, sa.ID)
		if err != nil {
			return false, err
		}
		for _, tok := range tokens {
			if tok.ID != 0 && slices.Contains(candidates, tok.ID) {
				return false, nil
			}
		}
	}
	legacy, err := c.Legacy.ListProjectAccessTokens(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	owned, err := c.managedLegacyIDs(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	for _, tok := range legacy {
		if tok.ID > 0 && slices.Contains(candidates, tok.ID) && slices.Contains(owned, tok.ID) {
			return true, nil
		}
	}
	return false, nil
}

// roleTokenName is the token and service-account name of a registered role.
func roleTokenName(rec gitlabroles.Registration) string {
	if rec.Credential.TokenName != "" {
		return rec.Credential.TokenName
	}
	return gitlabroles.CustomTokenName(rec.Name)
}

// normalizeServiceAccountClient converts a non-nil *ServiceAccountTokenClient to
// its value form. Every method has a value receiver, so the pointer satisfies
// the same interfaces as the value; callers that gate service-account
// capabilities, such as project-wide exclusion wrapping, on a value type
// assertion normalize first so a pointer client is never treated as a generic
// one. A typed-nil *ServiceAccountTokenClient is a non-nil interface whose
// methods would panic on dereference, so it is reported as an unavailable
// (nil) client instead.
func normalizeServiceAccountClient(tokens ProjectAccessTokenClient) ProjectAccessTokenClient {
	if p, ok := tokens.(*ServiceAccountTokenClient); ok {
		if p == nil {
			return nil
		}
		return *p
	}
	return tokens
}

// convergeInstalledServiceAccount runs under provisioning's project lease.
// It uses ordinary rotation state and its in-flight grace period; no migration
// command, runtime compatibility gate, or separate state format is required.
func convergeInstalledServiceAccount(ctx context.Context, cfg RoleProvisionConfig, rec gitlabroles.Registration, now time.Time, secretPresent bool, result *RoleProvisionResult) {
	c, ok := normalizeServiceAccountClient(cfg.Tokens).(ServiceAccountTokenClient)
	if !ok {
		return
	}
	state, _, err := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	if err != nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "reading service-account reconciliation state failed"})
		return
	}
	rs := state.Roles[string(rec.Name)]
	// Exclusions are project-wide, so resolve and freeze them regardless of
	// this role's ManagedUserID: a legacy role (ManagedUserID == 0) migrating
	// to a service account must also honor supplied/excluded accounts recorded
	// under other roles, and unresolved supplied ownership must fail closed.
	c, frozen, resolveErr := c.withProjectExclusions(ctx, cfg.Owner, cfg.Repo, state)
	if resolveErr != nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "supplied credential ownership unresolved; credentials left untouched"})
		return
	}
	if rs.ManagedUserID > 0 {
		excluded := slices.Contains(frozen, rs.ManagedUserID)
		if !excluded {
			if c.SA == nil {
				result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "managed service-account client unavailable"})
				return
			}
			accounts, err := c.withRecordedOwnership(state).managedServiceAccountList(ctx, cfg.Owner, cfg.Repo, true)
			var account *GitLabServiceAccount
			for i := range accounts {
				if accounts[i].ID == rs.ManagedUserID {
					account = &accounts[i]
					break
				}
			}
			if err != nil || account == nil {
				result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "managed account inventory or ownership unverified; credentials left untouched"})
				return
			}
			// Membership is changed only for this role's own Fullsend-managed
			// identity, never for another role's account recorded under it.
			if account.Name != roleTokenName(rec) {
				result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "managed account is not this role's service account; credentials left untouched"})
				return
			}
			if !cfg.DryRun {
				err = c.ensureMemberLevel(ctx, cfg.Owner, cfg.Repo, *account, gitlabroles.DeveloperAccessLevel)
			}
			if err == nil {
				var level int
				level, err = c.SA.GetProjectMemberAccessLevel(ctx, cfg.Owner, cfg.Repo, int64(rs.ManagedUserID))
				if err == nil && level != gitlabroles.DeveloperAccessLevel {
					err = fmt.Errorf("effective access is not Developer")
				}
			}
			if err != nil {
				if !cfg.DryRun {
					if containmentErr := c.containOwnedRole(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec, rs.ManagedUserID); containmentErr != nil {
						result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: managed credential containment incomplete: %s", rec.Name, containmentErr))
					}
				}
				result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "managed service-account membership could not be reconciled or verified"})
				return
			}
		}
	}
	// Missing secrets use ordinary provisioning after account reconciliation.
	// Do not rotate or migrate a credential that is no longer distributed.
	if !secretPresent {
		return
	}
	needed, err := c.needsServiceAccountReplacement(ctx, cfg.Owner, cfg.Repo, state.Roles[string(rec.Name)])
	if err != nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "checking service-account reconciliation failed"})
		return
	}
	if !needed {
		if rs.ManagedUserID == 0 && !provenanceOf(rs).Supplied {
			owned, err := c.managedLegacyIDs(ctx, cfg.Owner, cfg.Repo)
			if err != nil || !slices.Contains(owned, rs.IncomingID) {
				result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("%s: legacy token creation provenance is unverified; credentials left untouched; manual recovery or explicit supplied-credential enrollment required", rec.Name))
			}
		}
		return
	}
	// A replacement is published and the working credential retired only after
	// the new token's identity, scopes, expiry and protected-ref access (for the
	// Poller, VerifyGitLabReplacementPipelineAccess) are verified. Without a
	// verifier nothing can be verified, so fail closed before any mutation.
	if c.VerifyToken == nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "replacement credential verification unavailable; credentials left untouched"})
		return
	}
	listed, err := c.ListProjectAccessTokens(ctx, cfg.Owner, cfg.Repo)
	if err != nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "listing role credentials for reconciliation failed"})
		return
	}
	// Convergence must never replace a healthy legacy PAT with another legacy
	// PAT when listing is supported but service-account creation is not.
	c.requireServiceAccount = true
	rotated := RoleRotateResult{}
	rotateOneRole(ctx, RoleRotateConfig{Owner: cfg.Owner, Repo: cfg.Repo, Client: cfg.Client, Tokens: c, Force: true, ConvergeServiceAccounts: true, DryRun: cfg.DryRun}, rec,
		fmt.Sprintf("install-%d", now.UnixNano()), 2*time.Minute, now, gitlabroles.DefaultRotationLead, gitlabroles.DefaultRotationGrace, &listed, &rotated)
	result.Created = append(result.Created, rotated.Rotated...)
	result.Failed = append(result.Failed, rotated.Failed...)
	result.Diagnostics = append(result.Diagnostics, rotated.Diagnostics...)
}

// withPollerPipelineAccess returns a client whose VerifyToken also establishes
// and rereads the protected-default-branch access of a replacement Poller
// identity (VerifyGitLabReplacementPipelineAccess) before it is published.
// VerifyToken's own contract is credential authentication, so replacement paths
// compose the access check here instead of trusting every verifier to include
// it. The check is idempotent when the verifier already performs it.
func (c ServiceAccountTokenClient) withPollerPipelineAccess(client forge.Client) ServiceAccountTokenClient {
	if client == nil {
		return c
	}
	authenticate := c.VerifyToken
	c.VerifyToken = func(ctx context.Context, owner, repo string, tok *ProjectAccessToken) error {
		if authenticate != nil {
			if err := authenticate(ctx, owner, repo, tok); err != nil {
				return err
			}
		}
		if tok == nil || tok.Name != gitlabroles.PollerTokenName {
			return nil
		}
		return VerifyGitLabReplacementPipelineAccess(ctx, client, owner, repo, tok.UserID)
	}
	return c
}

// withProjectExclusions returns a client whose supplied-account exclusion set
// is frozen to the resolved supplied owners joined with every exclusion already
// recorded in rotation state, plus that set. Exclusions are project-wide, so
// replacement, inventory, minting, missing-secret provisioning and containment
// all see the complete set even with no resolver callback. Freezing keeps a
// later inventory from repeating attribution after mutation, which could make
// partial failure look like absence and authorize containment. An unattributable
// supplied owner is an error: callers must leave credentials untouched.
func (c ServiceAccountTokenClient) withProjectExclusions(ctx context.Context, owner, repo string, state rotationStateFile) (ServiceAccountTokenClient, []int, error) {
	resolved, err := c.suppliedOwnerIDs(ctx, owner, repo)
	if err != nil {
		return c, nil, err
	}
	// A supplied credential recorded without its owner's ID can only be
	// attributed by the resolver. An absent resolver, or one that returns an
	// empty or invalid-ID-only set without error, attributes nothing.
	if unresolvedSuppliedOwner(state) && len(positiveIDs(resolved)) == 0 {
		return c, nil, ErrPollerSuppliedUnresolved
	}
	resolved = positiveIDs(resolved)
	for _, other := range state.Roles {
		if other.SuppliedUserID > 0 && !slices.Contains(resolved, other.SuppliedUserID) {
			resolved = append(resolved, other.SuppliedUserID)
		}
		for _, id := range other.ExcludedUserIDs {
			if id > 0 && !slices.Contains(resolved, id) {
				resolved = append(resolved, id)
			}
		}
	}
	frozen := slices.Clone(resolved)
	c.SuppliedAccountIDs = func(context.Context, string, string) ([]int, error) { return slices.Clone(frozen), nil }
	return c, frozen, nil
}

// withRecordedOwnership returns a client whose managed-account allowlist is
// the durable ownership in state, already read under the project lease, when
// no ownership resolver is configured. A configured resolver is kept.
func (c ServiceAccountTokenClient) withRecordedOwnership(state rotationStateFile) ServiceAccountTokenClient {
	if c.ManagedAccountIDs != nil {
		return c
	}
	var ids []int
	for _, rs := range state.Roles {
		if rs.ManagedUserID > 0 {
			ids = append(ids, rs.ManagedUserID)
		}
	}
	c.ManagedAccountIDs = func(context.Context, string, string) ([]int, error) { return ids, nil }
	return c
}

// RecordManagedGitLabServiceAccount records a newly created account before any PAT
// is issued. The caller must have just created this account, not selected it by
// name. Callers hold the project lease. Existing credentials are not reclassified.
func RecordManagedGitLabServiceAccount(ctx context.Context, client forge.Client, owner, repo string, sa GitLabServiceAccount) error {
	if sa.ID <= 0 {
		return fmt.Errorf("created service account has no user ID")
	}
	raw, _, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRegistry)
	if err != nil {
		return err
	}
	reg, err := gitlabroles.ParseRegistry(raw)
	if err != nil {
		return err
	}
	var role gitlabroles.Role
	for _, rec := range reg.Registrations() {
		if rec.Credential.Kind != gitlabroles.CredentialReuse && roleTokenName(rec) == sa.Name {
			role = rec.Name
			break
		}
	}
	if role == "" {
		return fmt.Errorf("created service account %q has no registered role", sa.Name)
	}
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return err
	}
	rs := state.Roles[string(role)]
	rs.ManagedUserID = sa.ID
	state.Roles[string(role)] = rs
	return writeRotationState(ctx, client, owner, repo, state)
}

// DeleteManagedServiceAccounts removes durably owned accounts after token
// cleanup, returning the deletion count and any safety or deletion errors.
// client supplies the durable ownership document and forge-level deletion;
// the token API supplies credential inventories and resource verification.
// Uninstall wrappers preserve this capability through GitLabManagedAccountCleaner;
// callers retain ownership state when cleanup fails. Only durably attributed
// accounts are removed; this runs after token cleanup, before the provenance
// document is retired.
func (c ServiceAccountTokenClient) DeleteManagedServiceAccounts(ctx context.Context, client forge.Client, owner, repo string) (int, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return 0, safeAPIError("reading managed-account ownership before deletion", err)
	}
	// A role recorded as supplied whose owner is unattributable could own any
	// account, so no account may be deleted until that ownership is resolved.
	// Freeze the project-wide exclusions once, here, so direct callers get the
	// same attribution as the cleanup wrappers and a resolver that answers
	// differently on a later call cannot change which accounts are protected.
	c, excludedIDs, err := c.withProjectExclusions(ctx, owner, repo, state)
	if err != nil {
		return 0, err
	}
	owned := map[int]bool{}
	for _, rs := range state.Roles {
		if rs.ManagedUserID > 0 {
			owned[rs.ManagedUserID] = true
		}
	}
	// Exclusions are project-wide even when a caller has no exclusion callback.
	// excludedIDs holds recorded exclusions and resolved supplied owners.
	for _, id := range excludedIDs {
		delete(owned, id)
	}
	// Capability absence does not establish that a previously created account
	// disappeared. Keep its ownership document for retry unless every recorded
	// account is excluded as administrator-owned.
	if c.SA == nil {
		if len(owned) > 0 {
			return 0, fmt.Errorf("cannot clean up recorded managed service accounts without a service-account client; ownership retained for retry: %w", forge.ErrNotSupported)
		}
		return 0, nil
	}
	accounts, err := c.withRecordedOwnership(state).managedServiceAccountList(ctx, owner, repo, true)
	if err != nil {
		if serviceAccountsAbsent(err) && len(owned) == 0 {
			return 0, nil
		}
		return 0, safeAPIError("listing service accounts for managed-account cleanup; ownership retained for retry", err)
	}
	// A configured ownership resolver filters the inventory, and a filtered
	// inventory cannot prove a recorded account is gone. Check every recorded,
	// non-excluded account against the full inventory so a resolver that omits
	// a live account keeps the ownership state instead of retiring it.
	listed := map[int]bool{}
	for _, sa := range accounts {
		listed[sa.ID] = true
	}
	var missing []int
	for id := range owned {
		if !listed[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		inventory, err := c.SA.ListProjectServiceAccounts(ctx, owner, repo)
		if err != nil {
			return 0, safeAPIError("verifying recorded managed service accounts against the full inventory; ownership retained for retry", err)
		}
		for _, sa := range inventory {
			if slices.Contains(missing, sa.ID) {
				return 0, fmt.Errorf("%w: recorded managed service account %d exists but the ownership resolver does not list it; ownership retained for retry", ErrSuppliedCredentialUnresolved, sa.ID)
			}
		}
	}
	var errs []error
	deleted := 0
	for _, sa := range accounts {
		if !owned[sa.ID] {
			continue
		}
		tokens, err := c.SA.ListServiceAccountPATs(ctx, owner, repo, sa.ID)
		if err != nil {
			errs = append(errs, safeAPIError(fmt.Sprintf("listing credentials of managed service account %d before deletion", sa.ID), err))
			continue
		}
		unsafe := false
		for _, tok := range tokens {
			if tok.Active && !tok.Revoked {
				unsafe = true
			}
		}
		if unsafe {
			errs = append(errs, fmt.Errorf("service account %d still has active credentials; not deleted", sa.ID))
			continue
		}
		if c.VerifyAccountDeletion == nil {
			errs = append(errs, fmt.Errorf("cannot verify resources of managed service account %d before deletion; ownership retained for retry", sa.ID))
			continue
		}
		if err := c.VerifyAccountDeletion(ctx, owner, repo, sa.ID); err != nil {
			errs = append(errs, safeAPIError(fmt.Sprintf("verifying managed service account %d before deletion", sa.ID), err))
			continue
		}
		if err := client.DeleteProjectServiceAccount(ctx, owner, repo, sa.ID); err != nil {
			errs = append(errs, safeAPIError(fmt.Sprintf("deleting managed service account %d", sa.ID), err))
		} else {
			deleted++
		}
	}
	return deleted, errors.Join(errs...)
}

// containOwnedRole is called only after durable creation ownership and supplied
// exclusions were verified. Cleanup ignores caller cancellation and retains the
// ownership document so retries can finish containment.
func (c ServiceAccountTokenClient) containOwnedRole(ctx context.Context, client forge.Client, owner, repo string, rec gitlabroles.Registration, userID int) error {
	cleanupCtx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	var errs []error
	removeSecret := false
	if c.InstalledRoleCredentialOwner != nil {
		id, err := c.InstalledRoleCredentialOwner(cleanupCtx, owner, repo, rec.Credential.SecretName)
		if err != nil {
			errs = append(errs, safeAPIError("attributing installed role credential", err))
		} else {
			removeSecret = id == userID
		}
	}
	name := roleTokenName(rec)
	tokens, err := c.SA.ListServiceAccountPATs(cleanupCtx, owner, repo, userID)
	if err != nil {
		errs = append(errs, safeAPIError("listing role tokens for containment", err))
	}
	for _, tok := range tokens {
		if !tok.Active || tok.Revoked {
			continue
		}
		if tok.Name != name {
			errs = append(errs, fmt.Errorf("unmanaged active token %d requires administrator containment", tok.ID))
			continue
		}
		if err := c.SA.RevokeServiceAccountPAT(cleanupCtx, owner, repo, userID, tok.ID); err != nil {
			errs = append(errs, safeAPIError("revoking role token during containment", err))
		}
	}
	verified, err := c.SA.ListServiceAccountPATs(cleanupCtx, owner, repo, userID)
	if err != nil {
		errs = append(errs, safeAPIError("verifying role token containment", err))
	}
	for _, tok := range verified {
		if tok.Active && !tok.Revoked {
			errs = append(errs, fmt.Errorf("active token %d remains; administrator containment required", tok.ID))
		}
	}
	if removeSecret {
		if err := client.DeleteRepoSecret(cleanupCtx, owner, repo, rec.Credential.SecretName); err != nil {
			errs = append(errs, safeAPIError("deleting attributed contained role secret", err))
		}
	}
	return errors.Join(errs...)
}

// containCreationAccessFailure revalidates durable ownership and the complete
// supplied exclusion set before destructive cleanup at the mint boundary.
func (c ServiceAccountTokenClient) containCreationAccessFailure(ctx context.Context, owner, repo, name string, userID int) error {
	if c.ContainmentClient == nil {
		return nil
	}
	cleanupCtx, cancel := gitlabCleanupContext(ctx)
	defer cancel()
	state, _, err := loadRotationState(cleanupCtx, c.ContainmentClient, owner, repo)
	if err != nil {
		return safeAPIError("verifying containment ownership", err)
	}
	supplied, err := c.suppliedOwnerIDs(cleanupCtx, owner, repo)
	if err != nil {
		return safeAPIError("verifying containment supplied exclusions", err)
	}
	if unresolvedSuppliedOwner(state) && len(positiveIDs(supplied)) == 0 {
		return ErrPollerSuppliedUnresolved
	}
	for _, rs := range state.Roles {
		if rs.SuppliedUserID == userID || slices.Contains(rs.ExcludedUserIDs, userID) || slices.Contains(supplied, userID) {
			return nil
		}
	}
	raw, _, err := c.ContainmentClient.GetRepoVariable(cleanupCtx, owner, repo, forge.VarGitLabRoleRegistry)
	if err != nil {
		return safeAPIError("reading containment role registry", err)
	}
	reg, err := gitlabroles.ParseRegistry(raw)
	if err != nil {
		return safeAPIError("parsing containment role registry", err)
	}
	for _, rec := range reg.Registrations() {
		if roleTokenName(rec) == name && state.Roles[string(rec.Name)].ManagedUserID == userID {
			return c.containOwnedRole(cleanupCtx, c.ContainmentClient, owner, repo, rec, userID)
		}
	}
	return nil
}
