package owners

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

var validUsername = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

// Role is the OWNERS-file role for a resolved user.
type Role int

const (
	None     Role = iota
	Reviewer      // triage-equivalent
	Approver      // write-equivalent
)

type ownersFile struct {
	Approvers []string `yaml:"approvers"`
	Reviewers []string `yaml:"reviewers"`
}

type aliasesFile struct {
	Aliases map[string][]string `yaml:"aliases"`
}

// Resolve checks whether username appears in the OWNERS file at
// ownersPath (directly or via aliases in aliasesPath). Returns
// Approver if the user is an approver, Reviewer if only a reviewer,
// or None if not listed. Matching is case-insensitive.
//
// A missing OWNERS file returns an error. A missing OWNERS_ALIASES
// file is not an error — alias resolution is skipped.
func Resolve(ownersPath, aliasesPath, username string) (Role, error) {
	if !validUsername.MatchString(username) {
		return None, nil
	}
	data, err := os.ReadFile(ownersPath)
	if err != nil {
		return None, fmt.Errorf("reading OWNERS: %w", err)
	}
	var owners ownersFile
	if err := yaml.Unmarshal(data, &owners); err != nil {
		return None, fmt.Errorf("parsing OWNERS: %w", err)
	}

	var aliases aliasesFile
	if aliasData, err := os.ReadFile(aliasesPath); err == nil {
		if err := yaml.Unmarshal(aliasData, &aliases); err != nil {
			return None, fmt.Errorf("parsing OWNERS_ALIASES: %w", err)
		}
	}

	if hasMember(owners.Approvers, username, aliases.Aliases) {
		return Approver, nil
	}
	if hasMember(owners.Reviewers, username, aliases.Aliases) {
		return Reviewer, nil
	}
	return None, nil
}

// hasMember checks if username is in entries, either directly or by
// expanding alias names through the aliases map.
func hasMember(entries []string, username string, aliases map[string][]string) bool {
	for _, entry := range entries {
		if strings.EqualFold(entry, username) {
			return true
		}
		if members, ok := aliases[entry]; ok {
			for _, m := range members {
				if strings.EqualFold(m, username) {
					return true
				}
			}
		}
	}
	return false
}

// MapToActorRole upgrades currentRole based on the OWNERS role.
// Approver grants at least write; reviewer grants at least triage.
// Never downgrades — if the collaborator API already granted a
// higher role, it is preserved.
func MapToActorRole(role Role, currentRole normevent.ActorRole) normevent.ActorRole {
	switch role {
	case Approver:
		if !normevent.IsWriteAuthorized(currentRole) {
			return normevent.RoleWrite
		}
	case Reviewer:
		if currentRole == normevent.RoleNone || currentRole == normevent.RoleExternal || currentRole == normevent.RoleRead {
			return normevent.RoleTriage
		}
	}
	return currentRole
}
