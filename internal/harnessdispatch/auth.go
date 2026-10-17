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
//
// A bot actor that carries an ADR 0107 bot-role lookup result
// (actor.role_verified set) is authorized on non-label paths only when the
// lookup completed and recognized a registered role (actor.bot_role). A
// successful no-match and a failed lookup are both denied here, before CEL.
// Bot events without a lookup result keep the legacy behavior above until
// their adapter migrates.
//
// A human actor whose actor.role_verified is false is denied on non-label
// paths regardless of the role string (the role lookup failed, so the role is
// not authoritative). A human actor without the field is legacy input and keeps
// the write-role check above.
func IsAuthorized(event *normevent.Event) bool {
	if event == nil {
		return false
	}
	if event.Source.System == normevent.SystemGitHub &&
		event.Transition.Kind == normevent.TransitionLabelChanged &&
		event.Transition.Label != nil &&
		event.Transition.Label.Action == "added" {
		return true
	}
	if event.Actor.Kind == normevent.ActorBot && event.Actor.RoleVerified != nil {
		return IsRecognizedBot(event.Actor)
	}
	if event.Actor.Kind == normevent.ActorHuman &&
		event.Actor.RoleVerified != nil && !*event.Actor.RoleVerified {
		return false
	}
	if event.Source.System == normevent.SystemGitHub {
		if event.Transition.Kind == normevent.TransitionReviewSubmitted &&
			event.Actor.Kind == normevent.ActorBot {
			return true
		}
	}
	return normevent.IsWriteAuthorized(event.Actor.Role)
}

// IsRecognizedBot reports whether actor is a bot whose bot-role lookup
// completed and resolved a registered canonical role (ADR 0107).
func IsRecognizedBot(actor normevent.Actor) bool {
	return actor.Kind == normevent.ActorBot &&
		actor.RoleVerified != nil && *actor.RoleVerified &&
		actor.BotRole != ""
}
