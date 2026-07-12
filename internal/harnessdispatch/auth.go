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
//
// Installation bots submitting pull_request_review events are also authorized
// without a write role so harness dispatch stays agent-generic: CEL trigger
// expressions must filter review state, bot identity, fork/label guards, and
// any other stage-specific constraints (see normative normalized-event docs).
func IsAuthorized(event *normevent.Event) bool {
	if event == nil {
		return false
	}
	if event.Source.System == normevent.SystemGitHub {
		if event.Transition.Kind == normevent.TransitionLabelChanged &&
			event.Transition.Label != nil &&
			event.Transition.Label.Action == "added" {
			return true
		}
		if event.Transition.Kind == normevent.TransitionReviewSubmitted &&
			event.Actor.Kind == normevent.ActorBot {
			return true
		}
	}
	return normevent.IsWriteAuthorized(event.Actor.Role)
}
