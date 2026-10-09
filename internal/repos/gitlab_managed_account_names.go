package repos

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// ManagedGitLabRoleAccountNames maps durable account-creation IDs to their
// expected role display names. Like ManagedGitLabRoleAccountIDs, it leaves
// supplied-account exclusions to the caller, where they override ownership.
// Removed custom registrations retain their derived name for uninstall, which
// retires the registry before account cleanup. Unreadable state fails closed.
func ManagedGitLabRoleAccountNames(ctx context.Context, client forge.Client, owner, repo string) (map[int]string, error) {
	state, err := loadManagedAccountOwnership(ctx, client, owner, repo)
	if err != nil {
		return nil, err
	}
	raw, _, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRegistry)
	if err != nil {
		return nil, safeAPIError("reading the registry for managed GitLab role account names", err)
	}
	reg, err := gitlabroles.ParseRegistry(raw)
	if err != nil {
		return nil, safeAPIError("parsing the registry for managed GitLab role account names", err)
	}
	names := make(map[int]string)
	for role, rs := range state.Roles {
		if rs.ManagedUserID <= 0 {
			continue
		}
		name := gitlabroles.CustomTokenName(gitlabroles.Role(role))
		if rec, ok := reg.Lookup(gitlabroles.Role(role)); ok && rec.Credential.TokenName != "" {
			name = rec.Credential.TokenName
		}
		if prior, exists := names[rs.ManagedUserID]; exists && prior != name {
			return nil, fmt.Errorf("managed GitLab service account %d is recorded for conflicting role names", rs.ManagedUserID)
		}
		names[rs.ManagedUserID] = name
	}
	return names, nil
}
