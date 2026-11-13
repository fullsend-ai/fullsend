package repos

import (
	"cmp"
	"slices"

	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// GitLabLegacyBotIdentity is the recorded bot user of a legacy project access
// token that Fullsend created for a registered role (ADR 0107, #9063). It
// carries account identity and role policy only, never token material.
type GitLabLegacyBotIdentity struct {
	// UserID is the bot user's numeric GitLab user ID, as returned when the
	// token was created.
	UserID int
	// Role is the role whose rotation state recorded the bot.
	Role gitlabroles.Role
	// SharedWith lists the other registered roles that reuse Role's
	// credential, in registry order.
	SharedWith []gitlabroles.Role
	// Ambiguous is set when the account cannot be resolved to exactly one
	// registered role: Role is no longer a registered own-credential role,
	// another registered role reuses its credential (SharedWith is not
	// empty), the same account is recorded for more than one role, or the
	// account is recorded as the owner of an administrator-supplied
	// credential. An ambiguous identity has no canonical role and must be
	// denied.
	Ambiguous bool
}

// GitLabLegacyBotIdentities lists the legacy project-access-token bot
// identities recorded in rotationState, the value of
// FULLSEND_GITLAB_ROLE_ROTATION, resolved against reg, the trusted registry
// from FULLSEND_GITLAB_ROLE_REGISTRY. Both inputs are administrator-controlled
// installation state; repository files, harness files and event content are
// never sources. The result is sorted by user ID, then role. A blank document
// records no identities. A document this binary cannot read is an error, so a
// lookup failure stays distinguishable from a successful lookup that records
// nothing.
func GitLabLegacyBotIdentities(reg gitlabroles.Registry, rotationState string) ([]GitLabLegacyBotIdentity, error) {
	state, err := parseRotationState(rotationState)
	if err != nil {
		return nil, err
	}
	rolesByUser := map[int][]string{}
	suppliedOwners := map[int]bool{}
	for role, rs := range state.Roles {
		for _, id := range uniqueInts(rs.LegacyBotUserIDs) {
			rolesByUser[id] = append(rolesByUser[id], role)
		}
		if rs.SuppliedUserID > 0 {
			suppliedOwners[rs.SuppliedUserID] = true
		}
		for _, id := range rs.ExcludedUserIDs {
			suppliedOwners[id] = true
		}
	}
	var out []GitLabLegacyBotIdentity
	for id, roles := range rolesByUser {
		for _, name := range roles {
			role := gitlabroles.Role(name)
			ident := GitLabLegacyBotIdentity{UserID: id, Role: role, SharedWith: reg.CredentialSharers(role)}
			rec, registered := reg.Lookup(role)
			ident.Ambiguous = !registered || rec.Credential.Kind != gitlabroles.CredentialOwn ||
				len(ident.SharedWith) > 0 || len(roles) > 1 || suppliedOwners[id]
			out = append(out, ident)
		}
	}
	slices.SortFunc(out, func(a, b GitLabLegacyBotIdentity) int {
		return cmp.Or(cmp.Compare(a.UserID, b.UserID), cmp.Compare(a.Role, b.Role))
	})
	return out, nil
}
