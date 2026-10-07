// Package gitlablifecycle implements GitLab-specific role ownership,
// credential attribution and trigger-owner transport. Repository policy owns
// elevation ordering, safety decisions, containment and project leases.
package gitlablifecycle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// TriggerOwner mints the webhook trigger token authenticated
// as the Poller service account and changes the Poller's membership, all with
// installer authority. The Poller's distributed runtime credential
// (FULLSEND_GITLAB_POLLER_TOKEN) is never used to create the trigger: it is
// revoked before the elevation, and an installer-only bootstrap personal
// access token, held in memory only, authenticates the trigger creation.
type TriggerOwner struct {
	Admin *gitlab.LiveClient
	// NewClient builds a client for a token; tests override it.
	NewClient func(token, baseURL string) (*gitlab.LiveClient, error)
	// Now is the clock for credential expiry; tests override it.
	Now func() time.Time
}

var (
	_ repos.GitLabTriggerOwner  = (*TriggerOwner)(nil)
	_ repos.ManagedPollerLister = (*TriggerOwner)(nil)
)

// NewTriggerOwner binds installer-authorized GitLab transport to the lifecycle.
func NewTriggerOwner(admin *gitlab.LiveClient) *TriggerOwner {
	return &TriggerOwner{
		Admin: admin,
		NewClient: func(token, baseURL string) (*gitlab.LiveClient, error) {
			return gitlab.New(token, gitlab.WithBaseURL(baseURL))
		},
		Now: time.Now,
	}
}

// PollerUserID identifies the fullsend-managed fullsend-poller project
// service account from the administrator's service-account inventory, so
// identification does not depend on the distributed runtime credential, which
// is revoked during every trigger-creation transaction. A credential that is
// installed and still authenticates must belong to that managed account and be
// its managed personal access token: a Poller enrolled as a human's personal
// access token (--gitlab-role-token) or a project access token bot reports
// forge.ErrNotFound, so install never changes a person's membership or
// replaces an administrator-supplied credential. A missing, expired, revoked,
// or blocked installed credential cannot say who it is; the managed account
// is then taken from the inventory alone, which is how an interrupted run is
// recovered.
func (o *TriggerOwner) PollerUserID(ctx context.Context, owner, repo string) (int64, error) {
	token, found, err := o.Admin.GetRepoSecretValue(ctx, owner, repo, forge.SecretGitLabPollerToken)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", forge.SecretGitLabPollerToken, err)
	}
	if !found || token == "" {
		return o.lowestManagedPollerID(ctx, owner, repo)
	}
	c, err := o.NewClient(token, o.Admin.BaseURL())
	if err != nil {
		return 0, fmt.Errorf("building GitLab Poller client: %w", err)
	}
	uid, err := c.GetAuthenticatedUserID(ctx)
	if err != nil {
		if PollerCredentialRejected(err) {
			return o.lowestManagedPollerID(ctx, owner, repo)
		}
		return 0, err
	}
	// Match the recorded service-account ID even after a display-name change.
	// The managed-account and supplied-exclusion checks below still gate it.
	named, err := o.namedPollerIDs(ctx, owner, repo)
	if err != nil {
		return 0, err
	}
	if !slices.Contains(named, uid) {
		return 0, fmt.Errorf("the Poller credential (user ID %d) is not the %s project service account: %w", uid, gitlabroles.PollerTokenName, forge.ErrNotFound)
	}
	managed, err := o.managedPollerIDs(ctx, owner, repo)
	if err != nil {
		return 0, err
	}
	isManaged := false
	for _, id := range managed {
		if id == uid {
			isManaged = true
			break
		}
	}
	if !isManaged {
		return 0, fmt.Errorf("the Poller credential (user ID %d) is not the %s project service account: %w", uid, gitlabroles.PollerTokenName, forge.ErrNotFound)
	}
	// Containment and revocation can only touch tokens named after the
	// managed PAT contract, so an installed token with another name is an
	// administrator-supplied credential that must not be displaced.
	self, err := c.GetOwnPersonalAccessToken(ctx)
	if err != nil {
		if PollerCredentialRejected(err) {
			return uid, nil
		}
		return 0, fmt.Errorf("identifying the installed Poller personal access token: %w", err)
	}
	if self.Name != gitlabroles.PollerTokenName || !self.Active || self.Revoked || int64(self.UserID) != uid {
		return 0, fmt.Errorf("the installed Poller personal access token is not an active %s token of user ID %d: %w", gitlabroles.PollerTokenName, uid, forge.ErrNotFound)
	}
	return uid, nil
}

// PollerCredentialRejected reports whether GitLab refused the installed Poller
// credential itself (expired, revoked, or blocked account), as opposed to a
// transient or unrelated failure.
func PollerCredentialRejected(err error) bool {
	var apiErr *gitlab.APIError
	return errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden)
}

// managedPollerIDs returns the recorded Poller service-account ID, including
// renamed accounts, using the administrator credential. When
// duplicates exist the lowest ID is the one provisioning uses. A
// service-account API that is positively absent (not found or not supported)
// or no such account reports forge.ErrNotFound so no membership is changed. A
// forbidden listing is treated as the instance not offering service accounts
// (plan gating) only with affirmative evidence that no managed Poller can need
// recovery (see noPollerMember), since GitLab uses 403 for missing permissions
// too and an account left elevated by an interrupted run would otherwise go
// unreconciled. Any other forbidden or failed listing cannot establish that
// managed accounts are absent, so it is returned as an error and stops the
// caller instead of skipping reconciliation of an account that may still be
// elevated.
func (o *TriggerOwner) managedPollerIDs(ctx context.Context, owner, repo string) ([]int64, error) {
	ids, err := o.namedPollerIDs(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	// A service account and PAT that merely share the managed names are not
	// proof that fullsend minted the installed credential: every account that
	// rotation state records as owned by an administrator-supplied credential,
	// current or since replaced, is preserved rather than reconciled, rotated, or
	// revoked.
	excluded, supErr := o.excludedAccountIDs(ctx, owner, repo)
	if supErr != nil {
		return nil, supErr
	}
	managedID, ownershipErr := repos.ManagedGitLabRoleAccountID(ctx, o.Admin, owner, repo, gitlabroles.RolePoller)
	if ownershipErr != nil {
		return nil, ownershipErr
	}
	ids = slices.DeleteFunc(ids, func(id int64) bool {
		return managedID <= 0 || id != int64(managedID) || slices.Contains(excluded, id)
	})
	if len(ids) == 0 {
		return nil, fmt.Errorf("no %s project service account exists: %w", gitlabroles.PollerTokenName, forge.ErrNotFound)
	}
	return ids, nil
}

// namedPollerIDs returns same-named service accounts and the durably recorded
// Poller account, even after a rename. managedPollerIDs applies ownership and
// supplied exclusions before any membership or credential changes.
func (o *TriggerOwner) namedPollerIDs(ctx context.Context, owner, repo string) ([]int64, error) {
	accounts, err := o.Admin.ListProjectServiceAccounts(ctx, owner, repo)
	if err != nil {
		if forge.IsNotFound(err) || forge.IsNotSupported(err) {
			return nil, fmt.Errorf("project service accounts unavailable, so the Poller is not a service account: %w", forge.ErrNotFound)
		}
		if forge.IsForbidden(err) {
			// GitLab returns 403 both for plan gating and for missing
			// permissions, so the refusal alone says nothing about whether a
			// managed Poller exists. The inventory stays fail-closed unless the
			// project's membership affirmatively shows no such account.
			if memberErr := o.noPollerMember(ctx, owner, repo); memberErr != nil {
				return nil, fmt.Errorf("listing project service accounts to identify the managed Poller was refused and the project membership cannot show that no managed Poller needs recovery (%v): %w", memberErr, err)
			}
			return nil, fmt.Errorf("project service accounts unavailable and no %s member exists, so the Poller is not a service account: %w", gitlabroles.PollerTokenName, forge.ErrNotFound)
		}
		return nil, fmt.Errorf("listing project service accounts to identify the managed Poller: %w", err)
	}
	managedID, ownershipErr := repos.ManagedGitLabRoleAccountID(ctx, o.Admin, owner, repo, gitlabroles.RolePoller)
	if ownershipErr != nil {
		return nil, ownershipErr
	}
	var ids []int64
	for _, sa := range accounts {
		if sa.Name != gitlabroles.PollerTokenName && sa.ID != managedID {
			continue
		}
		ids = append(ids, int64(sa.ID))
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return nil, fmt.Errorf("no %s project service account exists: %w", gitlabroles.PollerTokenName, forge.ErrNotFound)
	}
	return ids, nil
}

// SuppliedPollerUserID returns the user that owns the installed Poller
// credential when rotation state records that credential as
// administrator-supplied. supplied is false when the credential is managed or
// there is no installed credential. The owner recorded at enrollment is used
// without authenticating, so an expired or revoked supplied credential keeps
// its owner's account excluded and can be replaced through the normal path.
// Unknown provenance is not "managed": when a Poller credential is installed
// but rotation state has no record of it, or a supplied credential has no
// recorded owner and cannot be attributed by authenticating, the result is an
// error wrapping repos.ErrPollerSuppliedUnresolved and the caller fails closed
// instead of treating a same-named account as managed.
func (o *TriggerOwner) SuppliedPollerUserID(ctx context.Context, owner, repo string) (int64, bool, error) {
	prov, err := repos.PollerCredentialProvenance(ctx, o.Admin, owner, repo)
	if err != nil {
		return 0, false, fmt.Errorf("%w: %w", repos.ErrPollerSuppliedUnresolved, repos.SafeAPIError("reading supplied credential provenance", err))
	}
	if prov.Known && !prov.Supplied {
		return 0, false, nil
	}
	if prov.Supplied && prov.UserID != 0 {
		return int64(prov.UserID), true, nil
	}
	token, found, err := o.Admin.GetRepoSecretValue(ctx, owner, repo, forge.SecretGitLabPollerToken)
	if err != nil {
		return 0, false, fmt.Errorf("%w: %w", repos.ErrPollerSuppliedUnresolved, repos.SafeAPIError(fmt.Sprintf("reading %s to attribute the installed Poller credential", forge.SecretGitLabPollerToken), err))
	}
	if !prov.Known {
		if !found || token == "" {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("%w: rotation state does not record who provisioned the installed %s credential, so the %s account cannot be treated as managed; re-enroll the credential with --gitlab-role-token", repos.ErrPollerSuppliedUnresolved, forge.SecretGitLabPollerToken, gitlabroles.PollerTokenName)
	}
	// Supplied, but enrolled without a recorded owner: attribute it by
	// authenticating with the installed credential.
	if !found || token == "" {
		return 0, false, fmt.Errorf("%w: the supplied Poller credential is no longer installed and its owner was not recorded; re-enroll it with --gitlab-role-token", repos.ErrPollerSuppliedUnresolved)
	}
	c, err := o.NewClient(token, o.Admin.BaseURL())
	if err != nil {
		return 0, false, fmt.Errorf("%w: building GitLab Poller client to attribute the supplied credential: %w", repos.ErrPollerSuppliedUnresolved, err)
	}
	uid, err := c.GetAuthenticatedUserID(ctx)
	if err != nil {
		if PollerCredentialRejected(err) {
			return 0, false, fmt.Errorf("%w: %w", repos.ErrPollerSuppliedUnresolved, repos.SafeAPIError(fmt.Sprintf("the supplied Poller credential is rejected by GitLab and its owner was not recorded, so the %s account cannot be treated as managed; re-enroll a working credential with --gitlab-role-token", gitlabroles.PollerTokenName), err))
		}
		return 0, false, fmt.Errorf("%w: %w", repos.ErrPollerSuppliedUnresolved, repos.SafeAPIError("attributing the supplied Poller credential to an account", err))
	}
	return uid, true, nil
}

// excludedAccountIDs lists every service account that owns, or ever owned, an
// administrator-supplied role credential, for any role. Exclusions recorded in
// rotation state persist across managed replacement and supplied replacement,
// and the owner of a currently installed supplied credential that was enrolled
// without a recorded owner is attributed by authenticating with it. When that
// cannot be done the error wraps repos.ErrPollerSuppliedUnresolved so callers
// fail closed.
func (o *TriggerOwner) excludedAccountIDs(ctx context.Context, owner, repo string) ([]int64, error) {
	out, _, err := o.ResolveExcludedAccounts(ctx, owner, repo)
	return out, err
}

// ResolveExcludedAccounts is excludedAccountIDs that also returns the owners it
// attributed by authenticating, keyed by role, for supplied credentials whose
// rotation entry did not record one. Callers that are about to delete those
// credentials persist them so a retry does not need the credential again.
func (o *TriggerOwner) ResolveExcludedAccounts(ctx context.Context, owner, repo string) ([]int64, map[gitlabroles.Role]int, error) {
	out, _, attributed, err := o.resolveSuppliedAccounts(ctx, owner, repo)
	return out, attributed, err
}

// CurrentSuppliedAccountIDs lists the accounts that own a currently installed
// supplied credential: the subset of the exclusions that is not merely
// historical, for the operational token inventory.
func (o *TriggerOwner) CurrentSuppliedAccountIDs(ctx context.Context, owner, repo string) ([]int, error) {
	_, current, _, err := o.resolveSuppliedAccounts(ctx, owner, repo)
	if err != nil || len(current) == 0 {
		return nil, err
	}
	out := make([]int, len(current))
	for i, id := range current {
		out[i] = int(id)
	}
	return out, nil
}

// resolveSuppliedAccounts computes the exclusions of ResolveExcludedAccounts
// together with the subset that owns a currently installed supplied credential.
func (o *TriggerOwner) resolveSuppliedAccounts(ctx context.Context, owner, repo string) ([]int64, []int64, map[gitlabroles.Role]int, error) {
	provs, err := repos.RoleCredentialProvenances(ctx, o.Admin, owner, repo)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %w", repos.ErrPollerSuppliedUnresolved, repos.SafeAPIError("reading supplied credential provenance", err))
	}
	attributed := map[gitlabroles.Role]int{}
	var out, current []int64
	add := func(id int64) {
		if id > 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	addCurrent := func(id int64) {
		add(id)
		if id > 0 && !slices.Contains(current, id) {
			current = append(current, id)
		}
	}
	for _, prov := range provs {
		for _, id := range prov.ExcludedUserIDs {
			add(int64(id))
		}
		if prov.Supplied && prov.UserID > 0 {
			addCurrent(int64(prov.UserID))
		}
	}
	// The Poller's installed credential is also attributed when its provenance
	// is unknown or its owner was not recorded.
	if id, supplied, err := o.SuppliedPollerUserID(ctx, owner, repo); err != nil {
		return nil, nil, nil, err
	} else if supplied {
		addCurrent(id)
		if pp := provs[gitlabroles.RolePoller]; pp.Supplied && pp.UserID == 0 {
			attributed[gitlabroles.RolePoller] = int(id)
		}
	}
	for role, prov := range provs {
		if role == gitlabroles.RolePoller || !prov.Supplied || prov.UserID != 0 {
			continue
		}
		id, err := o.AttributeSuppliedRole(ctx, owner, repo, role)
		if err != nil {
			return nil, nil, nil, err
		}
		addCurrent(id)
		attributed[role] = int(id)
	}
	// An installed own-credential role with no recorded provenance is not
	// "managed" either: an interrupted supplied enrollment stores the secret
	// before its provenance, and rotation state then has no entry to examine.
	if err := o.RefuseUnknownRoleProvenance(ctx, owner, repo, provs); err != nil {
		return nil, nil, nil, err
	}
	slices.Sort(out)
	slices.Sort(current)
	return out, current, attributed, nil
}

// RefuseUnknownRoleProvenance fails closed when a registered non-Poller
// own-credential role has an installed secret but rotation state does not
// establish who provisioned it. The roles come from the trusted registry, not
// from the rotation document, so a role with no entry at all is still examined.
// The Poller's own unknown provenance is handled by SuppliedPollerUserID.
func (o *TriggerOwner) RefuseUnknownRoleProvenance(ctx context.Context, owner, repo string, provs map[gitlabroles.Role]repos.RoleProvenance) error {
	// Flatten sanitized remote causes intentionally: inner API capability
	// errors must not classify unresolved ownership as permission to fall back.
	unresolved := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", repos.ErrPollerSuppliedUnresolved, fmt.Sprintf(format, args...))
	}
	raw, _, err := o.Admin.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRegistry)
	if err != nil {
		return unresolved("reading the role registry to check installed credential provenance: %v", repos.SafeAPIError("reading the role registry", err))
	}
	reg, err := gitlabroles.ParseRegistry(raw)
	if err != nil {
		return unresolved("parsing the role registry to check installed credential provenance: %v", repos.SafeAPIError("parsing the role registry", err))
	}
	for _, rec := range reg.Registrations() {
		if rec.Name == gitlabroles.RolePoller || rec.Credential.Kind == gitlabroles.CredentialReuse || rec.Credential.SecretName == "" {
			continue
		}
		if provs[rec.Name].Known {
			continue
		}
		installed, err := o.Admin.RepoSecretExists(ctx, owner, repo, rec.Credential.SecretName)
		if err != nil {
			return unresolved("checking whether %q is installed: %v", rec.Credential.SecretName, repos.SafeAPIError("checking the installed credential", err))
		}
		if installed {
			return unresolved("rotation state does not record who provisioned the installed %q credential, so the %q service account cannot be treated as managed; re-enroll it with --gitlab-role-token", rec.Name, rec.Name)
		}
	}
	return nil
}

// AttributeSuppliedRole identifies the owner of a supplied non-Poller role
// credential enrolled without a recorded owner by authenticating with the
// installed secret. A credential that is no longer installed or no longer
// authenticates cannot be attributed, which fails closed.
func (o *TriggerOwner) AttributeSuppliedRole(ctx context.Context, owner, repo string, role gitlabroles.Role) (int64, error) {
	unresolved := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", repos.ErrPollerSuppliedUnresolved, fmt.Sprintf(format, args...))
	}
	raw, _, err := o.Admin.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRegistry)
	if err != nil {
		return 0, unresolved("reading the role registry to attribute the supplied %q credential: %v", role, repos.SafeAPIError("reading the role registry", err))
	}
	reg, err := gitlabroles.ParseRegistry(raw)
	if err != nil {
		return 0, unresolved("parsing the role registry to attribute the supplied %q credential: %v", role, repos.SafeAPIError("parsing the role registry", err))
	}
	secret := ""
	for _, rec := range reg.Registrations() {
		if rec.Name == role && rec.Credential.Kind != gitlabroles.CredentialReuse {
			secret = rec.Credential.SecretName
		}
	}
	if secret == "" {
		return 0, unresolved("the supplied %q credential has no recorded owner and its role is not registered; re-enroll it with --gitlab-role-token", role)
	}
	token, found, err := o.Admin.GetRepoSecretValue(ctx, owner, repo, secret)
	if err != nil {
		return 0, unresolved("reading %q to attribute the supplied %q credential: %v", secret, role, repos.SafeAPIError("reading the installed credential", err))
	}
	if !found || token == "" {
		return 0, unresolved("the supplied %q credential is no longer installed and its owner was not recorded; re-enroll it with --gitlab-role-token", role)
	}
	c, err := o.NewClient(token, o.Admin.BaseURL())
	if err != nil {
		return 0, unresolved("building a GitLab client to attribute the supplied %q credential: %v", role, repos.SafeAPIError("building a GitLab client", err))
	}
	uid, err := c.GetAuthenticatedUserID(ctx)
	if err != nil {
		return 0, unresolved("the supplied %q credential cannot be attributed (%v); re-enroll a working credential with --gitlab-role-token", role, repos.SafeAPIError("authenticating", err))
	}
	return uid, nil
}

// SuppliedAccountIDs lists the service accounts that own administrator-supplied
// credentials, for the shared token client's exclusions.
func (o *TriggerOwner) SuppliedAccountIDs(ctx context.Context, owner, repo string) ([]int, error) {
	ids, err := o.excludedAccountIDs(ctx, owner, repo)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	out := make([]int, len(ids))
	for i, id := range ids {
		out[i] = int(id)
	}
	return out, nil
}

// SuppliedTokenIDs maps each supplied owner account to the token IDs of the
// credentials enrolled for it, from rotation state. An owner whose enrolled
// credential has no recorded token ID for any role is omitted, so every token of
// its account stands in for the credential. Each token carries the token name
// of the role it was enrolled for.
func (o *TriggerOwner) SuppliedTokenIDs(ctx context.Context, owner, repo string) (map[int][]repos.SuppliedTokenRef, error) {
	provs, err := repos.RoleCredentialProvenances(ctx, o.Admin, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("reading the enrolled supplied credential tokens: %w", err)
	}
	known := map[int][]repos.SuppliedTokenRef{}
	unresolved := map[int]bool{}
	for role, prov := range provs {
		if !prov.Supplied || prov.UserID <= 0 {
			continue
		}
		if prov.TokenID <= 0 {
			unresolved[prov.UserID] = true
			continue
		}
		known[prov.UserID] = append(known[prov.UserID], repos.SuppliedTokenRef{ID: prov.TokenID, Name: RoleTokenName(role)})
	}
	for id := range unresolved {
		delete(known, id)
	}
	return known, nil
}

// RoleTokenName is the GitLab token name a role's credential carries: the
// fixed name of a built-in role, or the derived name of a custom one.
func RoleTokenName(role gitlabroles.Role) string {
	switch role {
	case gitlabroles.RolePoller:
		return gitlabroles.PollerTokenName
	case gitlabroles.RoleAnalyst:
		return gitlabroles.AnalystTokenName
	case gitlabroles.RoleCoder:
		return gitlabroles.CoderTokenName
	default:
		return gitlabroles.CustomTokenName(role)
	}
}

// ResolveProvidedTokenIDs identifies the GitLab token behind each
// administrator-supplied role credential while it still authenticates, so the
// token can be recorded as the one that represents the role's lifecycle. A
// credential whose token cannot be identified is omitted, and so is one the
// project token inventory cannot enumerate (an ordinary user's personal access
// token, or a service account that is not a role account): its absence from the
// inventory would prove nothing, so its lifecycle stays with the enrollment's
// distribution proof.
func ResolveProvidedTokenIDs(ctx context.Context, client forge.Client, owner, repo string, provided map[gitlabroles.Role]string) map[gitlabroles.Role]int {
	glClient, ok := client.(*gitlab.LiveClient)
	if !ok {
		return nil
	}
	var out map[gitlabroles.Role]int
	var covered func(pat *gitlab.ProjectAccessToken) bool
	for role, token := range provided {
		if token == "" {
			continue
		}
		c, err := gitlab.New(token, gitlab.WithBaseURL(glClient.BaseURL()))
		if err != nil {
			continue
		}
		pat, err := c.GetOwnPersonalAccessToken(ctx)
		if err != nil || pat == nil || pat.ID <= 0 {
			continue
		}
		if covered == nil {
			covered = InventoryCoverage(ctx, glClient, owner, repo)
		}
		if !covered(pat) {
			continue
		}
		if out == nil {
			out = map[gitlabroles.Role]int{}
		}
		out[role] = pat.ID
	}
	return out
}

// InventoryCoverage returns a predicate reporting whether the project token
// inventory (role-named project service accounts and legacy project access
// tokens) enumerates a token. A listing failure covers nothing.
func InventoryCoverage(ctx context.Context, c *gitlab.LiveClient, owner, repo string) func(*gitlab.ProjectAccessToken) bool {
	roleAccounts := map[int]bool{}
	if accounts, err := c.ListProjectServiceAccounts(ctx, owner, repo); err == nil {
		for _, sa := range accounts {
			if gitlabroles.IsRoleProjectTokenName(sa.Name) {
				roleAccounts[sa.ID] = true
			}
		}
	}
	legacy := map[int]bool{}
	if toks, err := c.ListProjectAccessTokens(ctx, owner, repo); err == nil {
		for _, t := range toks {
			legacy[t.ID] = true
		}
	}
	return func(pat *gitlab.ProjectAccessToken) bool {
		return legacy[pat.ID] || (pat.UserID > 0 && roleAccounts[pat.UserID])
	}
}

// ResolveProvidedRoleCredentials resolves each supplied value and keeps its
// identity together instead of passing independent maps to provisioning.
func ResolveProvidedRoleCredentials(ctx context.Context, client forge.Client, owner, repo string, provided map[gitlabroles.Role]string) map[gitlabroles.Role]repos.ProvidedRoleCredential {
	owners := ResolveProvidedTokenOwners(ctx, client, provided)
	ids := ResolveProvidedTokenIDs(ctx, client, owner, repo, provided)
	out := make(map[gitlabroles.Role]repos.ProvidedRoleCredential, len(provided))
	for role, token := range provided {
		out[role] = repos.ProvidedRoleCredential{Token: token, OwnerID: owners[role], TokenID: ids[role]}
	}
	return out
}

// ResolveProvidedTokenOwners attributes each administrator-supplied role
// credential to its GitLab user while it still authenticates, so the owner can
// be recorded as durable provenance. A credential that cannot be attributed is
// omitted and later attributed (or refused) at use.
func ResolveProvidedTokenOwners(ctx context.Context, client forge.Client, provided map[gitlabroles.Role]string) map[gitlabroles.Role]int {
	glClient, ok := client.(*gitlab.LiveClient)
	if !ok {
		return nil
	}
	var out map[gitlabroles.Role]int
	for role, token := range provided {
		if token == "" {
			continue
		}
		c, err := gitlab.New(token, gitlab.WithBaseURL(glClient.BaseURL()))
		if err != nil {
			continue
		}
		uid, err := c.GetAuthenticatedUserID(ctx)
		if err != nil || uid <= 0 {
			continue
		}
		if out == nil {
			out = map[gitlabroles.Role]int{}
		}
		out[role] = int(uid)
	}
	return out
}

// noPollerMember returns nil only when the administrator credential read the
// complete project membership and no member carries the managed Poller name:
// a managed Poller that an interrupted run left elevated is always a member,
// so its absence from the membership is affirmative evidence that none needs
// recovery. A member with the recorded managed ID or bearing the name, or a membership that cannot be read in
// full, is an error so the forbidden service-account inventory stays
// fail-closed. A matching member is never adopted as the managed account, since
// only the service-account inventory can attribute the name to fullsend. The
// one exception is a member that owns a listed project access token: that is a
// legacy token bot (which the 403 fallback itself creates under the Poller
// name), not a service account, and needs no recovery.
func (o *TriggerOwner) noPollerMember(ctx context.Context, owner, repo string) error {
	members, err := o.Admin.ListProjectMembers(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("listing project members: %w", err)
	}
	managedID, err := repos.ManagedGitLabRoleAccountID(ctx, o.Admin, owner, repo, gitlabroles.RolePoller)
	if err != nil {
		return err
	}
	if managedID > 0 {
		excluded, err := o.excludedAccountIDs(ctx, owner, repo)
		if err != nil {
			return err
		}
		for _, m := range members {
			if m.ID == managedID && !slices.Contains(excluded, int64(m.ID)) {
				return fmt.Errorf("recorded managed Poller user ID %d is still a project member; its recovery cannot be skipped", m.ID)
			}
		}
	}
	var legacyBots map[int]bool
	for _, m := range members {
		if m.Name != gitlabroles.PollerTokenName {
			continue
		}
		if legacyBots == nil {
			toks, tokErr := o.Admin.ListProjectAccessTokens(ctx, owner, repo)
			if tokErr != nil {
				return fmt.Errorf("project member user ID %d is named %s and the project access token inventory cannot show it is a legacy token bot: %w", m.ID, gitlabroles.PollerTokenName, tokErr)
			}
			legacyBots = map[int]bool{}
			for _, tok := range toks {
				if tok.UserID > 0 {
					legacyBots[tok.UserID] = true
				}
			}
		}
		if !legacyBots[m.ID] {
			return fmt.Errorf("project member user ID %d is named %s and may be a managed Poller", m.ID, gitlabroles.PollerTokenName)
		}
	}
	return nil
}

// ManagedPollerUserIDs lists every managed Poller service account, lowest ID
// first, so install reconciles duplicates as uninstall does.
func (o *TriggerOwner) ManagedPollerUserIDs(ctx context.Context, owner, repo string) ([]int64, error) {
	return o.managedPollerIDs(ctx, owner, repo)
}

// lowestManagedPollerID returns the managed Poller account provisioning uses,
// taken from the administrator's inventory alone.
func (o *TriggerOwner) lowestManagedPollerID(ctx context.Context, owner, repo string) (int64, error) {
	ids, err := o.managedPollerIDs(ctx, owner, repo)
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}

// bootstrapTokenExpiry is a short expiry for the installer-only bootstrap
// credential. GitLab requires a future date; the install revokes the token
// within the same transaction, so the expiry only bounds an orphan.
func (o *TriggerOwner) bootstrapTokenExpiry() string {
	return o.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")
}

// CreatePollerBootstrap creates the installer-only bootstrap personal access
// token for the Poller service account with the administrator credential.
// Any bootstrap token left by an interrupted run is revoked first, so at most
// one exists. The value is returned to the caller and never stored.
func (o *TriggerOwner) CreatePollerBootstrap(ctx context.Context, owner, repo string, userID int64) (*repos.PollerBootstrap, error) {
	if err := o.RevokePollerBootstrap(ctx, owner, repo, userID); err != nil {
		return nil, err
	}
	tok, err := o.Admin.CreateServiceAccountPAT(ctx, owner, repo, int(userID), gitlabroles.PollerBootstrapTokenName, gitlabroles.TokenScopes(), o.bootstrapTokenExpiry())
	if err != nil {
		return nil, err
	}
	return &repos.PollerBootstrap{ID: tok.ID, Token: tok.Token}, nil
}

// CreatePipelineTriggerTokenAsPoller creates the trigger token as the Poller
// identity by authenticating with the bootstrap credential.
func (o *TriggerOwner) CreatePipelineTriggerTokenAsPoller(ctx context.Context, owner, repo, description string, bootstrap *repos.PollerBootstrap) (*forge.PipelineTriggerToken, error) {
	if bootstrap == nil || bootstrap.Token == "" {
		return nil, errors.New("no bootstrap credential")
	}
	c, err := o.NewClient(bootstrap.Token, o.Admin.BaseURL())
	if err != nil {
		return nil, fmt.Errorf("building GitLab Poller bootstrap client: %w", err)
	}
	return c.CreatePipelineTriggerToken(ctx, owner, repo, description)
}

// RevokePollerBootstrap revokes every active bootstrap personal access token
// of the Poller service account and verifies that none remains active. It
// finds orphans by token name, so no secret value has to be persisted.
func (o *TriggerOwner) RevokePollerBootstrap(ctx context.Context, owner, repo string, userID int64) error {
	return o.revokeNamedPATs(ctx, owner, repo, userID, gitlabroles.PollerBootstrapTokenName)
}

// revokeNamedPATs revokes every active personal access token with the given
// name on the service account and re-lists to verify that none remains active.
func (o *TriggerOwner) revokeNamedPATs(ctx context.Context, owner, repo string, userID int64, name string) error {
	toks, err := o.Admin.ListServiceAccountPATs(ctx, owner, repo, int(userID))
	if err != nil {
		return fmt.Errorf("listing Poller service account tokens: %w", err)
	}
	var errs []error
	for _, tok := range toks {
		if tok.Revoked || !tok.Active || tok.Name != name {
			continue
		}
		if err := o.Admin.RevokeServiceAccountPAT(ctx, owner, repo, int(userID), tok.ID); err != nil && !forge.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("revoking %s token ID %d: %w", name, tok.ID, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	toks, err = o.Admin.ListServiceAccountPATs(ctx, owner, repo, int(userID))
	if err != nil {
		return fmt.Errorf("verifying revocation of %s tokens: %w", name, err)
	}
	for _, tok := range toks {
		if !tok.Revoked && tok.Active && tok.Name == name {
			return fmt.Errorf("%s token ID %d is still active after revocation", name, tok.ID)
		}
	}
	return nil
}

func (o *TriggerOwner) SetProjectMemberAccessLevel(ctx context.Context, owner, repo string, userID int64, accessLevel int) error {
	return o.Admin.UpdateProjectMemberAccessLevel(ctx, owner, repo, userID, accessLevel)
}

// VerifyPollerElevationSafe refuses elevation unless every active credential
// of the Poller account is accounted for: personal access tokens must be the
// managed runtime credential or the installer's bootstrap credential (both
// revoked by the install itself before the elevation), pipeline trigger
// tokens must be the managed ones, and the account must have no usable SSH
// authentication key and no unfinished job may run as the Poller (job tokens
// outlive the revocation of the account's tokens). Failure containment cannot revoke
// credentials fullsend does not manage, and they would gain Maintainer access
// during the create window. An inventory that cannot be read fails closed.
func (o *TriggerOwner) VerifyPollerElevationSafe(ctx context.Context, owner, repo string, userID int64) error {
	return o.verifyPollerCredentials(ctx, owner, repo, userID, false)
}

// VerifyPollerCredentialsRevoked rejects every active PAT, including managed
// names, after runtime revocation and before bootstrap creation.
func (o *TriggerOwner) VerifyPollerCredentialsRevoked(ctx context.Context, owner, repo string, userID int64) error {
	return o.verifyPollerCredentials(ctx, owner, repo, userID, true)
}

func (o *TriggerOwner) verifyPollerCredentials(ctx context.Context, owner, repo string, userID int64, revoked bool) error {
	return repos.VerifyGitLabPollerCredentials(ctx, userID, revoked, o.credentialOperations(owner, repo, userID).GitLabPollerCredentialInventory)
}

// credentialOperations binds GitLab-specific transport and attribution to the
// repository policy. The policy owns safety decisions and containment ordering.
func (o *TriggerOwner) credentialOperations(owner, repo string, userID int64) repos.GitLabPollerContainmentOperations {
	return repos.GitLabPollerContainmentOperations{
		GitLabPollerCredentialInventory: repos.GitLabPollerCredentialInventory{
			Tokens: func(ctx context.Context) ([]repos.ProjectAccessToken, error) {
				raw, err := o.Admin.ListServiceAccountPATs(ctx, owner, repo, int(userID))
				tokens := make([]repos.ProjectAccessToken, len(raw))
				for i, token := range raw {
					tokens[i] = repos.ProjectAccessToken{ID: token.ID, Name: token.Name, Active: token.Active, Revoked: token.Revoked, UserID: token.UserID, ExpiresAt: token.ExpiresAt}
				}
				return tokens, repos.SafeAPIError("listing Poller service account tokens", err)
			},
			UnmanagedTriggers: func(ctx context.Context) ([]int64, error) {
				ids, err := o.unmanagedPollerTriggers(ctx, owner, repo, userID)
				return ids, repos.SafeAPIError("listing unmanaged Poller triggers", err)
			},
			SSHKeys: func(ctx context.Context) ([]int, error) {
				ids, err := o.PollerSSHKeyIDs(ctx, userID)
				return ids, repos.SafeAPIError("listing Poller SSH keys", err)
			},
			Jobs: func(ctx context.Context) ([]int, error) {
				ids, err := o.PollerActiveJobIDs(ctx, owner, repo, userID)
				return ids, repos.SafeAPIError("listing Poller jobs", err)
			},
			Schedules: func(ctx context.Context) ([]int, error) {
				ids, err := o.PollerScheduleIDs(ctx, owner, repo, userID)
				return ids, repos.SafeAPIError("listing Poller schedules", err)
			},
		},
		RevokeToken: func(ctx context.Context, tokenID int) error {
			return repos.SafeAPIError("revoking managed Poller token", o.Admin.RevokeServiceAccountPAT(ctx, owner, repo, int(userID), tokenID))
		},
		InstalledCredentialBelongsTo: func(ctx context.Context) (bool, error) {
			belongs, err := o.installedPollerCredentialBelongsTo(ctx, owner, repo, userID)
			return belongs, repos.SafeAPIError("attributing the installed Poller credential", err)
		},
		DeleteInstalledCredential: func(ctx context.Context) error {
			return repos.SafeAPIError("deleting the contained Poller credential", o.Admin.DeleteRepoSecret(ctx, owner, repo, forge.SecretGitLabPollerToken))
		},
	}
}

// PollerSSHKeyIDs returns the IDs of the Poller account's usable SSH
// authentication keys.
func (o *TriggerOwner) PollerSSHKeyIDs(ctx context.Context, userID int64) ([]int, error) {
	keys, err := o.Admin.ListUserSSHKeys(ctx, int(userID))
	if err != nil {
		return nil, fmt.Errorf("listing Poller service account SSH keys: %w", err)
	}
	metadata := make([]repos.GitLabAuthenticationKey, len(keys))
	for i, key := range keys {
		metadata[i] = repos.GitLabAuthenticationKey{ID: key.ID, UsageType: key.UsageType, ExpiresAt: key.ExpiresAt}
	}
	return repos.UsableGitLabSSHKeyIDs(metadata, o.Now()), nil
}

// PollerActiveJobIDs returns the IDs of unfinished project jobs that run as
// the Poller (or as a user GitLab did not report).
func (o *TriggerOwner) PollerActiveJobIDs(ctx context.Context, owner, repo string, userID int64) ([]int, error) {
	jobs, err := o.Admin.ListProjectActiveJobs(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("listing active project jobs: %w", err)
	}
	var ids []int
	for _, j := range jobs {
		if j.UserID == int(userID) || j.UserID == 0 {
			ids = append(ids, j.ID)
		}
	}
	return ids, nil
}

// PollerScheduleIDs returns the IDs of pipeline schedules owned by the Poller
// (or by a user GitLab did not report).
func (o *TriggerOwner) PollerScheduleIDs(ctx context.Context, owner, repo string, userID int64) ([]int, error) {
	schedules, err := o.Admin.ListProjectPipelineSchedules(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("listing pipeline schedules: %w", err)
	}
	var ids []int
	for _, s := range schedules {
		if s.OwnerID == int(userID) || s.OwnerID == 0 {
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}

// unmanagedPollerTriggers returns the IDs of pipeline trigger tokens owned by
// userID (or whose owner GitLab did not report) that fullsend does not
// manage.
func (o *TriggerOwner) unmanagedPollerTriggers(ctx context.Context, owner, repo string, userID int64) ([]int64, error) {
	triggers, err := o.Admin.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("listing pipeline trigger tokens: %w", err)
	}
	var ids []int64
	for _, t := range triggers {
		if t.Description == repos.GitLabWebhookTriggerDescription || (t.OwnerID != userID && t.OwnerID != 0) {
			continue
		}
		ids = append(ids, t.ID)
	}
	return ids, nil
}

// RevokePollerRuntimeCredentials invalidates the distributed Poller runtime
// credential with installer authority. The CI/CD variable is removed first, so
// a run interrupted afterwards leaves the secret absent and the next install
// provisions a replacement instead of trusting a dead value. The managed
// runtime personal access tokens are then revoked and their revocation is
// verified: removing the variable alone would leave copies held by running
// jobs valid. Credentials fullsend does not manage are never touched.
func (o *TriggerOwner) RevokePollerRuntimeCredentials(ctx context.Context, owner, repo string, userID int64) error {
	if err := o.Admin.DeleteRepoSecret(ctx, owner, repo, forge.SecretGitLabPollerToken); err != nil {
		return fmt.Errorf("removing %s: %w", forge.SecretGitLabPollerToken, err)
	}
	return o.revokeNamedPATs(ctx, owner, repo, userID, gitlabroles.PollerTokenName)
}

// CreatePollerRuntimeToken creates the replacement runtime personal access
// token for the Poller service account. The caller stores it.
func (o *TriggerOwner) CreatePollerRuntimeToken(ctx context.Context, owner, repo string, userID int64, expiresAt string) (*repos.PollerRuntimeToken, error) {
	tok, err := o.Admin.CreateServiceAccountPAT(ctx, owner, repo, int(userID), gitlabroles.PollerTokenName, gitlabroles.TokenScopes(), expiresAt)
	if err != nil {
		return nil, err
	}
	return &repos.PollerRuntimeToken{ID: tok.ID, Token: tok.Token}, nil
}

// ContainPoller revokes the managed Poller service account's active
// personal access tokens (runtime and bootstrap) and removes the installed
// Poller CI/CD secret with the admin credential when that secret's credential
// authenticates as this account, so a Poller that could not be returned to
// Developer access stops authenticating. Credentials of other identities are
// preserved. Every step is
// attempted; failures are joined. Active tokens that fullsend does not manage
// are left untouched but reported as incomplete containment.
// Token revocation and secret removal each run on their own bounded context
// detached from ctx, so one step exhausting its budget cannot stop the other.
func (o *TriggerOwner) ContainPoller(ctx context.Context, owner, repo string, userID int64) error {
	return repos.ContainGitLabPoller(ctx, userID, o.credentialOperations(owner, repo, userID))
}

// installedPollerCredentialBelongsTo reports whether the installed Poller
// CI/CD secret holds a credential that authenticates as userID. A credential
// of another identity (the selected account when an unselected duplicate is
// contained, or an administrator-supplied one) is preserved. An absent secret has nothing to
// remove, and a credential GitLab already rejects is inert, so neither is
// removed. An unreadable secret or an unattributable credential is an error:
// the secret is then left in place.
func (o *TriggerOwner) installedPollerCredentialBelongsTo(ctx context.Context, owner, repo string, userID int64) (bool, error) {
	token, found, err := o.Admin.GetRepoSecretValue(ctx, owner, repo, forge.SecretGitLabPollerToken)
	if err != nil {
		return false, fmt.Errorf("reading %s to attribute it before containment: %w", forge.SecretGitLabPollerToken, err)
	}
	if !found || token == "" {
		return false, nil
	}
	c, err := o.NewClient(token, o.Admin.BaseURL())
	if err != nil {
		return false, fmt.Errorf("building GitLab Poller client to attribute %s: %w", forge.SecretGitLabPollerToken, err)
	}
	uid, err := c.GetAuthenticatedUserID(ctx)
	if err != nil {
		if PollerCredentialRejected(err) {
			return false, nil
		}
		return false, fmt.Errorf("attributing %s to an account before containment: %w", forge.SecretGitLabPollerToken, err)
	}
	return uid == userID, nil
}

// PollerTriggerIDs lists all trigger resources owned by the identity, including
// those whose owner GitLab omits. Unlike the unmanaged query, it includes the
// managed webhook trigger and is suitable for account-deletion verification.
func (o *TriggerOwner) PollerTriggerIDs(ctx context.Context, owner, repo string, userID int64) ([]int, error) {
	triggers, err := o.Admin.ListPipelineTriggerTokens(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("listing pipeline trigger tokens: %w", err)
	}
	var ids []int
	for _, trigger := range triggers {
		if trigger.OwnerID == userID || trigger.OwnerID == 0 {
			ids = append(ids, int(trigger.ID))
		}
	}
	return ids, nil
}
