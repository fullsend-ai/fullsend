package install

import (
	"context"
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// actorGrant is the org-level access a human-like test actor must hold.
// permission is the all-repository organization role ("write" or "triage").
type actorGrant struct {
	login      string
	permission string
}

// actorGrantEnv maps each actor PAT to the all-repository organization
// role its account must hold (docs/guides/dev/e2e-testing.md,
// "Test actor permissions"). The outsider is deliberately absent: it
// must stay outside the organization.
var actorGrantEnv = []struct{ patEnv, permission string }{
	{"TEST_ACTOR_WRITE_PAT", "write"},
	{"TEST_ACTOR_TRIAGE_PAT", "triage"},
}

const outsiderPATEnv = "TEST_ACTOR_OUTSIDER_PAT"

const setupOrgHint = "run hack/setup-new-e2e-org.sh to grant organization membership and the all-repository role"

// actorGrantsFromEnv resolves the login behind each actor PAT that is set.
// An actor whose login cannot be resolved is logged and skipped.
func actorGrantsFromEnv(ctx context.Context, logf func(string, ...any)) []actorGrant {
	var grants []actorGrant
	for _, a := range actorGrantEnv {
		pat := os.Getenv(a.patEnv)
		if pat == "" {
			continue
		}
		login, err := e2etest.NewLiveClient(pat).GetAuthenticatedUser(ctx)
		if err != nil {
			logf("[ensure] skipping %s access check: resolving login: %v", a.patEnv, err)
			continue
		}
		grants = append(grants, actorGrant{login: login, permission: a.permission})
	}
	return grants
}

// outsiderLoginFromEnv resolves the outsider actor login when its PAT is
// set. Unlike actorGrantsFromEnv, a resolution failure here is not
// silently skipped: outsider exclusion is a security invariant (#7777),
// and treating a transient lookup failure as "no outsider to check" would
// disable that check for the whole ensurer lifetime (the org gets cached
// as verified in verifyActors). The caller must fail closed instead.
func outsiderLoginFromEnv(ctx context.Context) (string, error) {
	pat := os.Getenv(outsiderPATEnv)
	if pat == "" {
		return "", nil
	}
	login, err := e2etest.NewLiveClient(pat).GetAuthenticatedUser(ctx)
	if err != nil {
		return "", fmt.Errorf("%s is set but resolving the outsider login failed: %w", outsiderPATEnv, err)
	}
	return login, nil
}

// verifyActors checks that test actors are active organization members.
// Direct collaborator grants are not applied: resetRepo deletes the
// repo, which would drop those grants and re-adding them creates
// pending invitations (#7777).
//
// The all-repository role itself (write/triage) is verified only by
// hack/setup-new-e2e-org.sh, which runs with an org-admin-authenticated
// gh session — not here. Checking it at ensure time would require the
// e2e GitHub App installation on every pool org to hold the
// "Organization custom roles" (organization_custom_roles) permission
// purely so ListUserOrganizationRoles can be called; membership is a
// reliable proxy because the setup script is the only thing that grants
// or revokes the org-level role, and that role is independent of the
// per-repo delete/recreate cycle this ensurer drives.
//
// The check is cached per org for the ensurer's lifetime so delete+
// recreate cycles do not repeat the org-level lookup.
func (e *repoEnsurer) verifyActors(ctx context.Context, org string) error {
	if len(e.actorGrants) == 0 && e.outsiderLogin == "" {
		return nil
	}

	e.mu.Lock()
	if _, ok := e.verifiedOrgs[org]; ok {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	gh, ok := e.client.(forge.GitHubExtensions)
	if !ok {
		return fmt.Errorf("verifying test actors on %s: forge client has no organization membership API", org)
	}

	for _, g := range e.actorGrants {
		if err := e.verifyActorOrgAccess(ctx, gh, org, g); err != nil {
			return err
		}
	}
	if e.outsiderLogin != "" {
		if err := e.verifyOutsider(ctx, gh, org); err != nil {
			return err
		}
	}

	e.mu.Lock()
	if e.verifiedOrgs == nil {
		e.verifiedOrgs = make(map[string]struct{})
	}
	e.verifiedOrgs[org] = struct{}{}
	e.mu.Unlock()
	e.logf("[ensure] verified org-level actor access on %s", org)
	return nil
}

func (e *repoEnsurer) verifyActorOrgAccess(ctx context.Context, gh forge.GitHubExtensions, org string, g actorGrant) error {
	membership, err := gh.GetOrgMembership(ctx, org, g.login)
	if err != nil {
		if forge.IsNotFound(err) {
			return fmt.Errorf("%s is not a member of %s; org-level %s access is required (%s)",
				g.login, org, g.permission, setupOrgHint)
		}
		return fmt.Errorf("checking membership of %s in %s: %w", g.login, org, err)
	}
	if membership.State != "active" {
		return fmt.Errorf("%s is not an active member of %s (state=%s); org-level %s access is required (%s)",
			g.login, org, membership.State, g.permission, setupOrgHint)
	}
	e.logf("[ensure] %s is an active member of %s; all-repository %s role verified by hack/setup-new-e2e-org.sh",
		g.login, org, g.permission)
	return nil
}

func (e *repoEnsurer) verifyOutsider(ctx context.Context, gh forge.GitHubExtensions, org string) error {
	membership, err := gh.GetOrgMembership(ctx, org, e.outsiderLogin)
	if err != nil {
		if forge.IsNotFound(err) {
			e.logf("[ensure] %s has no org membership on %s", e.outsiderLogin, org)
			return nil
		}
		return fmt.Errorf("checking outsider membership of %s in %s: %w", e.outsiderLogin, org, err)
	}
	return fmt.Errorf("%s has org membership in %s (state=%s); outsider must remain outside the organization",
		e.outsiderLogin, org, membership.State)
}
