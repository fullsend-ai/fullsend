package harnessdispatch

import (
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// IsAuthorized applies ADR 0054 authorization before CEL evaluation.
//
// Most transitions require write-level actor.role from the collaborator
// permission API. On GitHub, label application requires triage/write access,
// so bash routing treats issues.labeled / pull_request_target.labeled as
// implicitly authorized (including bot-to-bot handoffs). Mirror that here
// for GitHub label-added events only so installation bots are not denied
// when the collaborator API returns no role for bot accounts.
func IsAuthorized(event *normevent.Event) bool {
	if event != nil &&
		event.Source.System == normevent.SystemGitHub &&
		event.Transition.Kind == normevent.TransitionLabelChanged &&
		event.Transition.Label != nil &&
		event.Transition.Label.Action == "added" {
		return true
	}
	return normevent.IsWriteAuthorized(event.Actor.Role)
}
