package harnessdispatch

import (
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// IsAuthorized is the shared dispatch authorization gate before CEL evaluation.
// It is intentionally agent-generic: stage-specific constraints (review state,
// bot identity, fork/label guards, etc.) belong in harness trigger CEL
// expressions, not here. This layer only encodes cross-cutting GitHub rules
// that bash routing applied before CEL existed and that are awkward to express
// purely from normalized events alone.
//
// Most transitions require write-level actor.role from the collaborator
// permission API. Label-added events are authorized without a write role because
// GitHub label application needs triage/write access and bots often lack a
// collaborator role in that API (mirroring bash label routing). Bot-submitted
// pull_request_review events are also authorized without a write role so
// downstream CEL can filter review state, bot identity, and other review-stage
// guards.
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
