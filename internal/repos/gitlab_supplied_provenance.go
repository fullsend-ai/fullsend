package repos

import (
	"context"
	"fmt"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// PollerCredentialProvenance reads the Poller's credential provenance. A name
// match alone never proves fullsend minted a credential, so callers use this
// provenance to avoid revoking or re-membering a supplied credential. An
// unreadable or invalid state is an error so callers fail closed; a missing
// document or Poller entry is reported as unknown provenance, which callers
// must not treat as managed.
func PollerCredentialProvenance(ctx context.Context, client forge.Client, owner, repo string) (RoleProvenance, error) {
	provs, err := RoleCredentialProvenances(ctx, client, owner, repo)
	if err != nil {
		return RoleProvenance{}, err
	}
	return provs[gitlabroles.RolePoller], nil
}

// RoleCredentialProvenances reads the credential provenance of every role that
// has a rotation-state entry, keyed by role. A role without an entry is absent
// from the map, which callers must treat as unknown rather than managed.
func RoleCredentialProvenances(ctx context.Context, client forge.Client, owner, repo string) (map[gitlabroles.Role]RoleProvenance, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("reading GitLab role rotation state: %w", err)
	}
	out := make(map[gitlabroles.Role]RoleProvenance, len(state.Roles))
	for role, rs := range state.Roles {
		if err := validateRecordedExclusionIDs(role, rs); err != nil {
			return nil, err
		}
		out[gitlabroles.Role(role)] = provenanceOf(rs)
	}
	return out, nil
}

// RecordSuppliedExclusions durably adds administrator-owned account IDs to a
// role's exclusions. A role with no entry gets an exclusions-only entry, so the
// exclusions are never silently dropped. It is idempotent and writes only when
// something is new. Callers hold the project lease.
func RecordSuppliedExclusions(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, ids []int) error {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before recording supplied exclusions: %w", err)
	}
	cur := state.Roles[string(role)]
	before := len(cur.ExcludedUserIDs)
	for _, id := range ids {
		cur.excludeOwner(id)
	}
	if len(cur.ExcludedUserIDs) == before {
		return nil
	}
	state.Roles[string(role)] = cur
	return writeRotationState(ctx, client, owner, repo, state)
}

// RecordSuppliedOwners durably records the resolved owner of each role whose
// installed credential is administrator-supplied but whose rotation entry has
// no recorded owner. Once recorded, a later attempt reads the owner from state
// instead of re-authenticating with a credential that may since have been
// deleted. Roles that are not supplied or already have an owner are left alone,
// and every other recorded field of an updated entry is preserved. It is
// idempotent and writes only when something changes. Callers hold the project
// lease.
func RecordSuppliedOwners(ctx context.Context, client forge.Client, owner, repo string, owners map[gitlabroles.Role]int) error {
	if len(owners) == 0 {
		return nil
	}
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before recording supplied owners: %w", err)
	}
	changed := false
	for role, id := range owners {
		cur, ok := state.Roles[string(role)]
		if id <= 0 || !ok || !provenanceOf(cur).Supplied || cur.SuppliedUserID != 0 {
			continue
		}
		cur.excludeOwner(id)
		cur.Supplied = true
		cur.SuppliedUserID = id
		state.Roles[string(role)] = cur
		changed = true
	}
	if !changed {
		return nil
	}
	return writeRotationState(ctx, client, owner, repo, state)
}

// SuppliedIdentity is the GitLab owner and token ID of an administrator-supplied
// credential, resolved by authenticating with it.
type SuppliedIdentity struct {
	UserID  int
	TokenID int
}

// recordSuppliedEnrollment records an administrator-provided credential's
// enrollment as an explicit supplied transition. It replaces whatever entry the
// role holds, including a managed one or the stale supplied entry left when the
// credential variable was removed but its rotation state remained: it records
// the new owner and token ID (when the identity resolved) with distribution
// proof, permanently excludes both the new owner and any previously recorded
// supplied owner, and preserves creation records, managed-account ownership,
// earlier exclusions, any rotation lock and Poller generations. An unresolved
// identity (zero UserID) records no owner; the entry stays supplied so later
// operations attribute it or fail closed. It returns a rollback that restores
// the role's previous entry, for use when the credential then fails to publish:
// an ownerless supplied entry with no installed secret would otherwise fail
// every later attribution closed and refuse an ordinary retry. The rollback
// keeps the new owner excluded. Callers hold the project lease.
func recordSuppliedEnrollment(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, id SuppliedIdentity, now time.Time) (func(context.Context) error, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("reading rotation state before enrollment proof: %w", err)
	}
	previous, hadPrevious := state.Roles[string(role)]
	enrolled := rotationRoleState{
		CreatedTokenIDs: previous.CreatedTokenIDs,
		ManagedUserID:   previous.ManagedUserID,
		Holder:          previous.Holder,
		LockUntil:       previous.LockUntil,
		Phase:           rotationPhaseIdle,
		DistributedAt:   now.UTC().Format(time.RFC3339),
		Supplied:        true,
		ExcludedUserIDs: append([]int(nil), previous.ExcludedUserIDs...),
		// Poller identity generations describe accounts, not this credential.
		GenerationCurrentUserID:  previous.GenerationCurrentUserID,
		GenerationPendingUserID:  previous.GenerationPendingUserID,
		GenerationPendingPhase:   previous.GenerationPendingPhase,
		GenerationRetiringUserID: previous.GenerationRetiringUserID,
	}
	enrolled.excludeOwner(previous.SuppliedUserID)
	if id.UserID > 0 {
		enrolled.excludeOwner(id.UserID)
		enrolled.SuppliedUserID = id.UserID
		if id.TokenID > 0 {
			enrolled.SuppliedTokenID = id.TokenID
		}
	}
	state.Roles[string(role)] = enrolled
	if err := writeRotationState(ctx, client, owner, repo, state); err != nil {
		return nil, err
	}
	rollback := func(ctx context.Context) error {
		cur, _, err := loadRotationState(ctx, client, owner, repo)
		if err != nil {
			return fmt.Errorf("reading rotation state before enrollment rollback: %w", err)
		}
		switch {
		case hadPrevious:
			restored := previous
			restored.ExcludedUserIDs = append([]int(nil), enrolled.ExcludedUserIDs...)
			cur.Roles[string(role)] = restored
		case id.UserID > 0:
			// Keep the replacement owner's exclusion in an exclusions-only entry.
			cur.Roles[string(role)] = rotationRoleState{ExcludedUserIDs: []int{id.UserID}}
		default:
			delete(cur.Roles, string(role))
		}
		return writeRotationState(ctx, client, owner, repo, cur)
	}
	return rollback, nil
}

// recordProvisionedDistribution records distribution proof for a managed
// credential that missing-secret provisioning just minted and published. A role
// with no lifecycle entry, or one carrying only ownership or exclusion records,
// takes the create-if-absent proof of recordInitialDistribution. A role whose
// entry still describes a supplied credential (the secret was removed but its
// rotation state remained) takes an explicit managed-replacement transition
// instead: the published credential is the managed one, so the supplied
// provenance is cleared, the previous supplied owner stays permanently
// excluded, and creation records, managed-account ownership, earlier
// exclusions, unconfirmed outgoing IDs, any rotation lock and Poller
// generations are preserved. Callers hold the project lease.
func recordProvisionedDistribution(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, tokenID int, expiresAt string, now time.Time) error {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before initial distribution proof: %w", err)
	}
	existing, ok := state.Roles[string(role)]
	if !ok || !provenanceOf(existing).Supplied {
		return recordInitialDistribution(ctx, client, owner, repo, role, tokenID, expiresAt, now)
	}
	existing.excludeOwner(existing.SuppliedUserID)
	existing.Supplied, existing.SuppliedUserID, existing.SuppliedTokenID, existing.SuppliedDistributed = false, 0, 0, false
	existing.Phase = rotationPhaseIdle
	if len(existing.OutgoingIDs) > 0 {
		existing.Phase = rotationPhaseOverlapping
	}
	existing.IncomingID = tokenID
	existing.DistributedAt = now.UTC().Format(time.RFC3339)
	existing.ExpiresAt = expiresAt
	existing.Error = ""
	state.Roles[string(role)] = existing
	return writeRotationState(ctx, client, owner, repo, state)
}

// applySuppliedTransition marks rs as describing an administrator-supplied
// credential owned by id. A previously recorded supplied owner is moved into the
// permanent exclusions and is not carried over as the new credential's owner;
// the new owner (when resolved) is excluded and recorded. An unresolved
// identity leaves the entry supplied without an owner.
func applySuppliedTransition(rs *rotationRoleState, id SuppliedIdentity) {
	rs.excludeOwner(rs.SuppliedUserID)
	rs.Supplied = true
	rs.SuppliedUserID, rs.SuppliedTokenID = 0, 0
	if id.UserID > 0 {
		rs.excludeOwner(id.UserID)
		rs.SuppliedUserID = id.UserID
		if id.TokenID > 0 {
			rs.SuppliedTokenID = id.TokenID
		}
	}
}

// resolveSuppliedIdentity authenticates a supplied credential value through the
// token client. A client without that capability yields a zero identity, which
// records nothing.
func resolveSuppliedIdentity(ctx context.Context, tokens ProjectAccessTokenClient, provided string) (SuppliedIdentity, error) {
	sa, ok := normalizeServiceAccountClient(tokens).(ServiceAccountTokenClient)
	if !ok || sa.SuppliedCredentialIdentity == nil {
		return SuppliedIdentity{}, nil
	}
	id, err := sa.SuppliedCredentialIdentity(ctx, provided)
	if err != nil {
		return SuppliedIdentity{}, SafeAPIError("resolving the supplied credential owner", err)
	}
	return id, nil
}

// RecordSuppliedIdentity durably records the owner and token ID of a role's
// administrator-supplied credential, and permanently excludes the owner, while
// the credential still authenticates. Only a supplied entry that has no
// recorded owner is updated, so a later replacement, rotation or uninstall
// reads the owner from state instead of re-authenticating with a credential
// that may have expired, and every other recorded field is preserved. A
// non-positive owner or token ID is never recorded. It is idempotent and writes
// only when something changes. Callers hold the project lease.
func RecordSuppliedIdentity(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role, id SuppliedIdentity) error {
	if id.UserID <= 0 {
		return nil
	}
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return fmt.Errorf("reading rotation state before recording the supplied credential identity: %w", err)
	}
	cur, ok := state.Roles[string(role)]
	if !ok || !provenanceOf(cur).Supplied || cur.SuppliedUserID != 0 {
		return nil
	}
	cur.excludeOwner(id.UserID)
	cur.Supplied = true
	cur.SuppliedUserID = id.UserID
	if id.TokenID > 0 {
		cur.SuppliedTokenID = id.TokenID
	}
	state.Roles[string(role)] = cur
	return writeRotationState(ctx, client, owner, repo, state)
}
