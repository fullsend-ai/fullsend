package harnessdispatch

import (
	"log"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/owners"
)

// AuthorizeEvent applies the dispatch authorization gate (IsAuthorized) to
// event. When the per-repo config enables OWNERS-file authorization, the
// actor role is first upgraded from the OWNERS and OWNERS_ALIASES files in
// the parent of configDir. The caller's event is not mutated: the upgraded
// role is used only for the gate, and CEL evaluation keeps seeing the
// collaborator-API role.
func AuthorizeEvent(configDir string, cfg config.ConfigReader, event *normevent.Event) bool {
	if event == nil {
		return false
	}
	repoRoot := filepath.Dir(configDir)
	effectiveRole := event.Actor.Role
	if pr, ok := cfg.(config.PerRepoConfigReader); ok && pr.IsOwnersFileAuthEnabled() && event.Actor.ID != "" {
		ownersPath := filepath.Join(repoRoot, "OWNERS")
		aliasesPath := filepath.Join(repoRoot, "OWNERS_ALIASES")
		role, err := owners.Resolve(ownersPath, aliasesPath, event.Actor.ID)
		if err != nil {
			log.Printf("harness dispatch: OWNERS resolution failed for %s: %v", event.Actor.ID, err)
		} else if role != owners.RoleNone {
			effectiveRole = owners.MapToActorRole(role, effectiveRole)
			log.Printf("harness dispatch: OWNERS file resolved user %s as %s", event.Actor.ID, role)
		}
	}

	authCheck := *event
	authCheck.Actor.Role = effectiveRole
	return IsAuthorized(&authCheck)
}
