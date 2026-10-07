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
	for _, sa := range accounts {
		tokens, err := c.SA.ListServiceAccountPATs(ctx, owner, repo, sa.ID)
		if err != nil {
			return false, err
		}
		for _, tok := range tokens {
			if tok.ID == rs.IncomingID {
				return false, nil
			}
		}
	}
	legacy, err := c.Legacy.ListProjectAccessTokens(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	for _, tok := range legacy {
		if tok.ID == rs.IncomingID && tok.ID > 0 {
			return true, nil
		}
	}
	return false, nil
}

// convergeInstalledServiceAccount runs under provisioning's project lease.
// It uses ordinary rotation state and its in-flight grace period; no migration
// command, runtime compatibility gate, or separate state format is required.
func convergeInstalledServiceAccount(ctx context.Context, cfg RoleProvisionConfig, rec gitlabroles.Registration, now time.Time, result *RoleProvisionResult) {
	c, ok := cfg.Tokens.(ServiceAccountTokenClient)
	if !ok {
		return
	}
	state, _, err := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	if err != nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "reading service-account reconciliation state failed"})
		return
	}
	rs := state.Roles[string(rec.Name)]
	if rs.ManagedUserID > 0 {
		excluded := false
		for _, other := range state.Roles {
			excluded = excluded || other.SuppliedUserID == rs.ManagedUserID || slices.Contains(other.ExcludedUserIDs, rs.ManagedUserID)
		}
		if !excluded {
			if c.SA == nil {
				result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "managed service-account client unavailable"})
				return
			}
			accounts, err := c.managedServiceAccountList(ctx, cfg.Owner, cfg.Repo, true)
			var account *GitLabServiceAccount
			for i := range accounts {
				if accounts[i].ID == rs.ManagedUserID {
					account = &accounts[i]
					break
				}
			}
			if err == nil && account == nil {
				err = fmt.Errorf("recorded managed account missing")
			}
			if err == nil && !cfg.DryRun {
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
				result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "managed service-account membership could not be reconciled or verified"})
				return
			}
		}
	}
	needed, err := c.needsServiceAccountReplacement(ctx, cfg.Owner, cfg.Repo, state.Roles[string(rec.Name)])
	if err != nil {
		result.Failed = append(result.Failed, RoleProvisionFailure{Role: rec.Name, Secret: rec.Credential.SecretName, Reason: "checking service-account reconciliation failed"})
		return
	}
	if !needed {
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

// RecordManagedServiceAccount records a newly created account before any PAT
// is issued. The caller must have just created this account, not selected it by
// name. Callers hold the project lease. Existing credentials are not reclassified.
func RecordManagedServiceAccount(ctx context.Context, client forge.Client, owner, repo string, sa GitLabServiceAccount) error {
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
		name := rec.Credential.TokenName
		if name == "" {
			name = gitlabroles.CustomTokenName(rec.Name)
		}
		if rec.Credential.Kind != gitlabroles.CredentialReuse && name == sa.Name {
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
// callers retain ownership state when cleanup fails.
func (c ServiceAccountTokenClient) DeleteManagedServiceAccounts(ctx context.Context, client forge.Client, owner, repo string) (int, error) {
	return c.deleteManagedServiceAccounts(ctx, client, owner, repo)
}

// deleteManagedServiceAccounts removes only durably attributed accounts.
// It runs after token cleanup, before its provenance document is retired.
func (c ServiceAccountTokenClient) deleteManagedServiceAccounts(ctx context.Context, client forge.Client, owner, repo string) (int, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return 0, safeAPIError("reading managed-account ownership before deletion", err)
	}
	owned := map[int]bool{}
	for _, rs := range state.Roles {
		if rs.ManagedUserID > 0 {
			owned[rs.ManagedUserID] = true
		}
	}
	// Exclusions are project-wide even when a caller has no exclusion callback.
	for _, rs := range state.Roles {
		for _, id := range rs.ExcludedUserIDs {
			delete(owned, id)
		}
		if rs.SuppliedUserID > 0 {
			delete(owned, rs.SuppliedUserID)
		}
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
	accounts, err := c.managedServiceAccountList(ctx, owner, repo, true)
	if err != nil {
		if serviceAccountsAbsent(err) && len(owned) == 0 {
			return 0, nil
		}
		return 0, safeAPIError("listing service accounts for managed-account cleanup; ownership retained for retry", err)
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
			errs = append(errs, err)
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
