package harnessdispatch

import (
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// IsAuthorized applies ADR 0054: require write-level permission on actor.role.
func IsAuthorized(event *normevent.Event) bool {
	return normevent.IsWriteAuthorized(event.Actor.Role)
}
