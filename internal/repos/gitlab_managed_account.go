package repos

import (
	"context"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// ManagedGitLabRoleAccountID returns only durable account-creation provenance.
// A role name or minted-token ID does not authorize changing a same-named
// account. Missing provenance returns zero; unreadable state fails closed.
func ManagedGitLabRoleAccountID(ctx context.Context, client forge.Client, owner, repo string, role gitlabroles.Role) (int, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return 0, safeAPIError("reading managed GitLab role account ownership", err)
	}
	id := state.Roles[string(role)].ManagedUserID
	return id, nil
}

// ManagedGitLabRoleAccountIDs lists positively recorded account IDs for the
// destructive token-client boundaries. Supplied exclusions are applied by the
// caller independently and always take precedence.
func ManagedGitLabRoleAccountIDs(ctx context.Context, client forge.Client, owner, repo string) ([]int, error) {
	state, _, err := loadRotationState(ctx, client, owner, repo)
	if err != nil {
		return nil, safeAPIError("reading managed GitLab role account ownership", err)
	}
	var ids []int
	for _, rs := range state.Roles {
		if rs.ManagedUserID > 0 {
			ids = append(ids, rs.ManagedUserID)
		}
	}
	return ids, nil
}
