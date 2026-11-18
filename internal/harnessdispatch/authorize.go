package harnessdispatch

import (
	"log"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/owners"
)

// AuthCheckEvent returns a copy of event to pass to the authorization gate
// (IsEventAuthorized, IsHarnessAuthorized, FilterAuthorizedHarnesses). When
// the per-repo config enables OWNERS-file authorization, the copy's actor
// role is upgraded from the OWNERS and OWNERS_ALIASES files in the parent of
// configDir. The caller's event is not mutated: the upgraded role is used
// only for the gate, and CEL evaluation keeps seeing the collaborator-API
// role.
func AuthCheckEvent(configDir string, cfg config.ConfigReader, event *normevent.Event) normevent.Event {
	authCheck := *event
	pr, ok := cfg.(config.PerRepoConfigReader)
	if !ok || !pr.IsOwnersFileAuthEnabled() || event.Actor.ID == "" {
		return authCheck
	}
	repoRoot := filepath.Dir(configDir)
	ownersPath := filepath.Join(repoRoot, "OWNERS")
	aliasesPath := filepath.Join(repoRoot, "OWNERS_ALIASES")
	role, err := owners.Resolve(ownersPath, aliasesPath, event.Actor.ID)
	if err != nil {
		log.Printf("harness dispatch: OWNERS resolution failed for %s: %v", event.Actor.ID, err)
	} else if role != owners.RoleNone {
		authCheck.Actor.Role = owners.MapToActorRole(role, authCheck.Actor.Role)
		log.Printf("harness dispatch: OWNERS file resolved user %s as %s", event.Actor.ID, role)
	}
	return authCheck
}
