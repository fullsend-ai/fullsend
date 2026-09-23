package owners

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

var validUsername = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

// Role is the OWNERS-file role for a resolved user.
type Role int

const (
	RoleNone     Role = iota
	RoleReviewer      // triage-equivalent
	RoleApprover      // write-equivalent
)

func (r Role) String() string {
	switch r {
	case RoleReviewer:
		return "reviewer"
	case RoleApprover:
		return "approver"
	case RoleNone:
		return "none"
	default:
		return fmt.Sprintf("Role(%d)", int(r))
	}
}

// ownersFile represents a root-level Prow OWNERS file with flat
// approvers/reviewers lists. v1 limitation: filters: blocks and
// nested per-directory OWNERS files are not supported.
type ownersFile struct {
	Approvers []string `yaml:"approvers"`
	Reviewers []string `yaml:"reviewers"`
}

type aliasesFile struct {
	Aliases map[string][]string `yaml:"aliases"`
}

// Resolve checks whether username appears in the OWNERS file at
// ownersPath (directly or via aliases in aliasesPath). Returns
// RoleApprover if the user is an approver, RoleReviewer if only a reviewer,
// or RoleNone if not listed. Matching is case-insensitive.
//
// A missing OWNERS file returns an error. A missing OWNERS_ALIASES
// file is not an error — alias resolution is skipped. A malformed
// OWNERS_ALIASES file is an error: without it, alias keys cannot be told
// apart from logins.
func Resolve(ownersPath, aliasesPath, username string) (Role, error) {
	if !validUsername.MatchString(username) {
		return RoleNone, nil
	}
	data, err := os.ReadFile(ownersPath)
	if err != nil {
		return RoleNone, fmt.Errorf("reading OWNERS: %w", err)
	}
	var owners ownersFile
	if err := yaml.Unmarshal(data, &owners); err != nil {
		return RoleNone, fmt.Errorf("parsing OWNERS: %w", err)
	}

	var aliases aliasesFile
	if aliasData, err := os.ReadFile(aliasesPath); err == nil {
		if err := yaml.Unmarshal(aliasData, &aliases); err != nil {
			return RoleNone, fmt.Errorf("parsing OWNERS_ALIASES: %w", err)
		}
		if err := checkAliasKeys(aliases.Aliases); err != nil {
			return RoleNone, err
		}
	}

	// A login equal to an alias key never matches, whether the key appears in
	// OWNERS or nested in another alias; nobody can claim an alias by
	// registering its name. Nested aliases are not expanded.
	if _, ok := lookupAlias(aliases.Aliases, username); ok {
		return RoleNone, nil
	}

	if hasMember(owners.Approvers, username, aliases.Aliases) {
		return RoleApprover, nil
	}
	if hasMember(owners.Reviewers, username, aliases.Aliases) {
		return RoleReviewer, nil
	}
	return RoleNone, nil
}

// hasMember reports whether username is listed in entries. An entry that
// names an alias key matches only that alias's members.
func hasMember(entries []string, username string, aliases map[string][]string) bool {
	for _, entry := range entries {
		if members, ok := lookupAlias(aliases, entry); ok {
			if slices.ContainsFunc(members, func(m string) bool { return strings.EqualFold(m, username) }) {
				return true
			}
			continue
		}
		if strings.EqualFold(entry, username) {
			return true
		}
	}
	return false
}

// checkAliasKeys rejects alias keys that differ only by case, which would
// make lookupAlias ambiguous.
func checkAliasKeys(aliases map[string][]string) error {
	seen := make(map[string]bool, len(aliases))
	for key := range aliases {
		folded := strings.ToLower(key)
		if seen[folded] {
			return fmt.Errorf("parsing OWNERS_ALIASES: duplicate alias %q (keys are case-insensitive)", key)
		}
		seen[folded] = true
	}
	return nil
}

// lookupAlias finds name among the alias keys, ignoring case.
func lookupAlias(aliases map[string][]string, name string) ([]string, bool) {
	for key, members := range aliases {
		if strings.EqualFold(key, name) {
			return members, true
		}
	}
	return nil, false
}

// MapToActorRole upgrades currentRole based on the OWNERS role.
// An approver gets at least write; a reviewer gets at least triage.
// Never downgrades — if the collaborator API already granted a
// higher role, it is preserved.
func MapToActorRole(role Role, currentRole normevent.ActorRole) normevent.ActorRole {
	switch role {
	case RoleApprover:
		if !normevent.IsWriteAuthorized(currentRole) {
			return normevent.RoleWrite
		}
	case RoleReviewer:
		if currentRole == normevent.RoleNone || currentRole == normevent.RoleExternal || currentRole == normevent.RoleRead {
			return normevent.RoleTriage
		}
	}
	return currentRole
}
