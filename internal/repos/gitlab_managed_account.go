package repos

import (
	"context"
	"fmt"
	"slices"

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
	generationIDs, err := ManagedGitLabPollerGenerationAccountIDs(ctx, client, owner, repo)
	if err != nil {
		return nil, err
	}
	for _, id := range generationIDs {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// ManagedGitLabPollerGenerationAccountIDs lists the Poller accounts recorded
// in the generation document: the current Poller, the superseded Poller still
// retiring, and the pending replacement. A cutover replaces the managed Poller
// account in rotation state before it records the new current generation, so
// without the document's current ID the previous Poller drops out of every
// destructive credential inventory, uninstall, and cleanup during that
// window, and without the retiring and pending IDs the superseded or
// interrupted account drops out afterwards, while their credentials may still
// be usable (#8243). The IDs are deduplicated. An absent document records
// none; an unreadable one fails closed.
func ManagedGitLabPollerGenerationAccountIDs(ctx context.Context, client forge.Client, owner, repo string) ([]int, error) {
	st, err := loadPollerGenerations(ctx, client, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("reading recorded GitLab Poller generation accounts: %w", err)
	}
	var ids []int
	add := func(id int64) {
		if id > 0 && !slices.Contains(ids, int(id)) {
			ids = append(ids, int(id))
		}
	}
	add(st.CurrentUserID)
	add(st.RetiringUserID)
	for _, id := range st.AbandonedUserIDs {
		add(id)
	}
	if st.Pending != nil {
		add(st.Pending.UserID)
	}
	return ids, nil
}

// GitLabPollerGenerationReconcileAccountIDs lists the recorded Poller accounts
// whose project membership install must keep at Developer: the current Poller,
// the superseded Poller still retiring, and the abandoned replacements. It
// omits the pending replacement, which reconcilePendingPollerGeneration
// handles with its handoff phase. Its runtime credentials stay valid until
// retirement, so an elevated membership on any of these accounts must be
// restored by an ordinary install (#8243). An absent document records none;
// an unreadable one fails closed.
func GitLabPollerGenerationReconcileAccountIDs(ctx context.Context, client forge.Client, owner, repo string) ([]int64, error) {
	st, err := loadPollerGenerations(ctx, client, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("reading recorded GitLab Poller generation accounts: %w", err)
	}
	var ids []int64
	for _, id := range append([]int64{st.CurrentUserID, st.RetiringUserID}, st.AbandonedUserIDs...) {
		if id > 0 && !slices.Contains(ids, id) {
			ids = append(ids, id)
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
