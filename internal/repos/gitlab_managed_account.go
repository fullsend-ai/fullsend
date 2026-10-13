package repos

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// ManagedGitLabRoleAccountID returns only durable account-creation provenance.
// A role name or minted-token ID does not authorize changing a same-named
// account. Missing provenance returns zero; unreadable state fails closed.
func ManagedGitLabRoleAccountID(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role) (int, error) {
	state, err := loadManagedAccountOwnership(ctx, client, owner, repo)
	if err != nil {
		return 0, err
	}
	id := state.Roles[string(role)].ManagedUserID
	return id, nil
}

// ManagedGitLabRoleAccountIDs lists positively recorded account IDs for the
// destructive token-client boundaries. Supplied exclusions are applied by the
// caller independently and always take precedence.
func ManagedGitLabRoleAccountIDs(ctx context.Context, client forge.Client, owner, repo string) ([]int, error) {
	state, err := loadManagedAccountOwnership(ctx, client, owner, repo)
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, rs := range state.Roles {
		if rs.ManagedUserID > 0 {
			ids = append(ids, rs.ManagedUserID)
		}
	}
	return ids, nil
}

// loadManagedAccountOwnership reads rotation state for the managed-account
// readers. A negative recorded account ID is malformed ownership evidence and
// fails closed rather than reading as "no managed account".
func loadManagedAccountOwnership(ctx context.Context, client forge.Client, owner, repo string) (rotationStateFile, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return rotationStateFile{}, safeAPIError("reading managed GitLab role account ownership", err)
	}
	for role, rs := range state.Roles {
		if rs.ManagedUserID < 0 {
			return rotationStateFile{}, fmt.Errorf("invalid GitLab role rotation state %q: managed account ID must not be negative", role)
		}
		if err := validateRecordedExclusionIDs(role, rs); err != nil {
			return rotationStateFile{}, err
		}
	}
	return state, nil
}

// validateRecordedExclusionIDs rejects malformed persisted exclusion evidence.
// appendExcludedID drops nonpositive IDs, so a persisted one would silently
// shrink the exclusion set. SuppliedUserID 0 means "not recorded" and is valid;
// only a negative one is malformed.
func validateRecordedExclusionIDs(role string, rs rotationRoleState) error {
	invalid := rs.SuppliedUserID < 0
	for _, id := range rs.ExcludedUserIDs {
		invalid = invalid || id <= 0
	}
	if invalid {
		return fmt.Errorf("invalid GitLab role rotation state %q: excluded account IDs must be positive and a supplied account ID must not be negative", role)
	}
	return nil
}
