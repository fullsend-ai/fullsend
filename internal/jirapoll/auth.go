package jirapoll

import (
	"context"
	"fmt"
	"log"
	"slices"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
)

// roleSource says where an event's actor.role comes from.
type roleSource int

const (
	// roleFromProjectRoles resolves the actor through the Jira project
	// role roster (GET /project/{key}/role), which Jira Cloud only serves
	// to accounts with the Administer Projects permission.
	roleFromProjectRoles roleSource = iota
	// roleFromLists resolves the actor against the jira authorization
	// provider's write and triage lists.
	roleFromLists
	// roleNotResolved leaves the role unresolved. It applies only when a
	// jira provider entry is present (and does not select labels:
	// project_roles with no lists, which resolves every event through the
	// roster): to label-added events under the
	// jira_editors rule, which the dispatch gate authorizes by Jira's Edit
	// Issues permission, not by actor.role; and, when the entry sets no
	// lists, to events that are neither slash commands nor label
	// additions, so that they never require the admin-only project role
	// roster. With no jira entry the roster resolves every event, so
	// nothing is left unresolved.
	roleNotResolved
)

// unresolvedRole is the actor.role of an actor the jira lists do not name
// and of an event whose role is not resolved (roleNotResolved). It is below
// every dispatch threshold.
const unresolvedRole = "none"

// roleSourceFor picks the role source for event per the jira
// authorization provider:
//
//   - with no jira entry (Authorization.Configured false), every event
//     uses the project role roster;
//   - with labels: project_roles and no lists, every event uses the
//     project role roster, exactly as with no jira entry (the explicit
//     way to keep that behaviour);
//   - otherwise a label-added event follows the labels: rule (jira_editors: not
//     resolved; allowlist: the lists; project_roles: the roster);
//   - any other event uses the lists when any are configured;
//   - otherwise a slash-command comment uses the project role roster,
//     and every remaining event is not resolved.
//
// Only roleFromProjectRoles events make the poller load the roster, so a
// setup whose jira entry uses the lists without labels: project_roles, or
// only label triggers under jira_editors, never calls GET /project/{key}/role.
func (p *Poller) roleSourceFor(event JiraEvent) roleSource {
	if !p.opts.Authorization.Configured {
		return roleFromProjectRoles
	}
	auth := p.opts.Authorization
	if auth.labelMode() == LabelsProjectRoles && !auth.HasLists() {
		return roleFromProjectRoles
	}
	if event.Type == "label_changed" && event.LabelAction == "added" {
		switch auth.labelMode() {
		case LabelsAllowlist:
			return roleFromLists
		case LabelsProjectRoles:
			return roleFromProjectRoles
		default:
			return roleNotResolved
		}
	}
	if auth.HasLists() {
		return roleFromLists
	}
	if event.Type == "comment_added" {
		if cmd, _ := extractCommand(event.CommentBody); cmd != "" {
			return roleFromProjectRoles
		}
	}
	return roleNotResolved
}

// roleLoadError marks a failure to load the project role roster. Run fails
// the whole cycle on it, without committing any checkpoint, instead of
// failing only the issue being processed.
type roleLoadError struct {
	err error
}

func (e *roleLoadError) Error() string { return e.err.Error() }
func (e *roleLoadError) Unwrap() error { return e.err }

// ensureRoleStructure loads the project role roster once per cycle, the
// first time an event needs it. A load failure is returned as a
// *roleLoadError: with no roster every actor would resolve to "external",
// write-gated events would route to nothing, and the checkpoint would still
// advance past them, permanently dropping real events over a transient
// roles-API error. (A missing project key is different: JQL-only mode is
// documented as always-external, so that path proceeds.)
func (p *Poller) ensureRoleStructure(ctx context.Context) error {
	if p.rolesLoaded {
		return nil
	}
	if p.opts.JiraProject == "" {
		log.Printf("WARNING: no Jira project key set, cannot resolve actor roles (defaulting to external)")
		p.rolesLoaded = true
		return nil
	}
	if err := p.loadRoleStructure(ctx, p.opts.JiraProject); err != nil {
		return &roleLoadError{err: err}
	}
	p.rolesLoaded = true
	return nil
}

// resolveEventRoles fetches what toNormalizedEvent needs to resolve each
// event's actor.role: the project role roster, only when some event uses
// it, and the group memberships of actors that the jira lists or the
// roster's role-assigned groups may match. Group memberships are cached
// per actor for the cycle.
//
// Any group lookup failure, partial or total, fails this issue so it is
// retried next cycle, rather than dispatching a role downgraded by the
// missing membership.
func (p *Poller) resolveEventRoles(ctx context.Context, events []JiraEvent) error {
	var listActors, roleActors []string
	needRoster := false
	for _, event := range events {
		source := p.roleSourceFor(event)
		if source == roleFromProjectRoles {
			needRoster = true
		}
		aid := actorID(event)
		if aid == "" {
			continue
		}
		switch source {
		case roleFromLists:
			if p.listsNeedGroups(aid) && !slices.Contains(listActors, aid) {
				listActors = append(listActors, aid)
			}
		case roleFromProjectRoles:
			if !slices.Contains(roleActors, aid) {
				roleActors = append(roleActors, aid)
			}
		}
	}

	if err := p.lookupUserGroups(ctx, listActors); err != nil {
		return fmt.Errorf("resolve jira list groups: %w", err)
	}

	if !needRoster {
		return nil
	}
	if err := p.ensureRoleStructure(ctx); err != nil {
		return err
	}
	if p.opts.JiraProject == "" || len(p.roleGroups) == 0 {
		return nil
	}
	// Check groups for actors below the highest possible priority
	// (Administrators). A direct role assignment doesn't rule out also
	// belonging to a higher-priority group, so every such actor is
	// checked, not just ones with no role yet. roleGroupsChecked skips
	// actors already resolved via groups earlier this cycle.
	var candidates []string
	for _, aid := range roleActors {
		if !p.roleGroupsChecked[aid] && jira.RolePriority(p.roleMembership[aid]) < maxRolePriority {
			candidates = append(candidates, aid)
		}
	}
	return p.resolveActorRoles(ctx, candidates)
}

// listsNeedGroups reports whether resolving aid against the jira lists
// needs the actor's group memberships: some list names a group that could
// raise the role the account entries alone give.
func (p *Poller) listsNeedGroups(aid string) bool {
	auth := p.opts.Authorization
	if slices.Contains(auth.Write.Accounts, aid) {
		return false
	}
	if len(auth.Write.Groups) > 0 {
		return true
	}
	return len(auth.Triage.Groups) > 0 && !slices.Contains(auth.Triage.Accounts, aid)
}

// lookupUserGroups fetches and caches the group memberships of each actor
// not already cached this cycle.
func (p *Poller) lookupUserGroups(ctx context.Context, actorIDs []string) error {
	var lastErr error
	var errorCount int
	for _, aid := range actorIDs {
		if _, err := p.userGroupsFor(ctx, aid); err != nil {
			log.Printf("WARNING: getting groups for actor %s: %v", aid, err)
			lastErr = err
			errorCount++
		}
	}
	if errorCount > 0 {
		return fmt.Errorf("%d of %d actor group lookups failed: %w", errorCount, len(actorIDs), lastErr)
	}
	return nil
}

// userGroupsFor returns aid's group memberships, calling GetUserGroups
// (GET /user/groups, which needs only the Browse users and groups global
// permission) at most once per actor per cycle.
func (p *Poller) userGroupsFor(ctx context.Context, aid string) ([]jira.UserGroupInfo, error) {
	if groups, ok := p.userGroups[aid]; ok {
		return groups, nil
	}
	groups, err := p.client.GetUserGroups(ctx, aid)
	if err != nil {
		return nil, err
	}
	if p.userGroups == nil {
		p.userGroups = make(map[string][]jira.UserGroupInfo)
	}
	p.userGroups[aid] = groups
	return groups, nil
}

// listRole resolves aid against the jira lists: write if a write entry
// names the account or one of its groups, else triage if a triage entry
// does, else unresolvedRole (denied by every threshold). Group membership
// comes from the cycle cache filled by resolveEventRoles; an actor with no
// cached groups matches no group entry (fail closed).
func (p *Poller) listRole(aid string) string {
	auth := p.opts.Authorization
	groups := p.userGroups[aid]
	if listMatches(auth.Write, aid, groups) {
		return "write"
	}
	if listMatches(auth.Triage, aid, groups) {
		return "triage"
	}
	return unresolvedRole
}

// listMatches reports whether list names aid directly or names one of
// groups by name or group ID.
func listMatches(list ActorList, aid string, groups []jira.UserGroupInfo) bool {
	if slices.Contains(list.Accounts, aid) {
		return true
	}
	for _, g := range groups {
		if (g.Name != "" && slices.Contains(list.Groups, g.Name)) ||
			(g.GroupID != "" && slices.Contains(list.Groups, g.GroupID)) {
			return true
		}
	}
	return false
}
