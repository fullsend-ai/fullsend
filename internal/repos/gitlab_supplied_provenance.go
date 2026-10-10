package repos

import (
	"context"
	"fmt"

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
