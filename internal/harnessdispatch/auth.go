package harnessdispatch

import (
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// IsAuthorized applies ADR 0054 authorization before CEL evaluation.
//
// Most transitions require write-level actor.role from the collaborator
// permission API. Label add/remove is different: on GitHub, applying or
// removing a label requires triage/write access, so bash routing treats
// labeled events as implicitly authorized (including bot-to-bot handoffs).
// Mirror that here so installation bots are not denied when the API returns
// no collaborator role for bot accounts.
func IsAuthorized(event *normevent.Event) bool {
	if event != nil &&
		event.Transition.Kind == normevent.TransitionLabelChanged &&
		event.Transition.Label != nil &&
		event.Transition.Label.Action == "added" {
		return true
	}
	return normevent.IsWriteAuthorized(event.Actor.Role)
}
