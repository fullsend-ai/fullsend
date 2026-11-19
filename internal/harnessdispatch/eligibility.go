package harnessdispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/entitycel"
	"github.com/fullsend-ai/fullsend/internal/normentity"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// EntityOutcome is the final decision of one entity-source harness
// evaluation (ADR 0098, ADR 0147). Only EntityEligible may start a sandbox.
type EntityOutcome int

// Entity-source evaluation outcomes. The zero value is EntityDenied so an
// unset decision fails closed.
const (
	// EntityDenied: the evaluation is not permitted or cannot be performed —
	// no entity sources, an invalid or failing predicate, or a failed
	// authorization gate. Retrying with more data does not change it.
	EntityDenied EntityOutcome = iota
	// EntityDeferred: the entity data does not settle the predicate (unknown
	// fields, non-complete collection coverage, or a failed read). The
	// candidate stays retryable and is never recorded as a non-match.
	EntityDeferred
	// EntityNonMatch: the predicate is false for every completion of the
	// evidence.
	EntityNonMatch
	// EntityEligible: the predicate is true for every completion of the
	// evidence.
	EntityEligible
)

func (o EntityOutcome) String() string {
	switch o {
	case EntityDeferred:
		return "deferred"
	case EntityNonMatch:
		return "non-match"
	case EntityEligible:
		return "eligible"
	}
	return "denied"
}

// MayStartSandbox reports whether the outcome permits sandbox launch. Only
// EntityEligible does.
func (o EntityOutcome) MayStartSandbox() bool { return o == EntityEligible }

// EntityDecision is an entity-source evaluation's outcome and its reason.
type EntityDecision struct {
	Outcome EntityOutcome
	Reason  string
}

// EntityEvaluation is one entity-source harness predicate check: a required
// normalized entity and an optional prompting event.
//
// Harnesses without entity sources are not evaluated here; they stay on the
// unchanged event-only path ([MatchHarnesses]).
type EntityEvaluation struct {
	// Harness names the harness in decision reasons.
	Harness string
	// Trigger is the harness CEL predicate over entity and event.
	Trigger string
	// EntitySources reports whether the harness declares valid entity
	// sources. Without them the harness is event-only and is denied here.
	EntitySources bool
	// Event is the prompting event, or nil for an eventless evaluation
	// (scheduled discovery or a bound run's recheck), which binds event to
	// CEL null.
	Event *normevent.Event
	// Authorize runs the gate that precedes CEL: the selected harness's
	// event-actor gate when Event is set, or the trusted-origin and platform
	// eligibility gate when Event is nil. A nil Authorize or an error denies.
	Authorize func(context.Context) error
	// ResolveEntity resolves the candidate's current normalized entity after
	// authorization. An error defers the candidate; it is never a non-match.
	ResolveEntity func(context.Context) (*normentity.Entity, error)
}

// DecideEntityEligibility evaluates an entity-source harness predicate and
// returns exactly one of eligible, non-match, deferred, or denied.
//
// It never converts unresolved data to a non-match: an entity read failure,
// a malformed entity, or a predicate that the known data does not settle
// defers the candidate, while a missing gate, a failed authorization, or a
// predicate that is invalid or fails to evaluate denies it.
func DecideEntityEligibility(ctx context.Context, ev EntityEvaluation) EntityDecision {
	if !ev.EntitySources {
		return denied(ev, "harness declares no entity sources; it is evaluated only on the event-only path")
	}
	if err := entitycel.Validate(ev.Trigger); err != nil {
		return denied(ev, "invalid entity-source predicate: %v", err)
	}
	if ev.Authorize == nil {
		return denied(ev, "no authorization gate")
	}
	if ev.ResolveEntity == nil {
		return denied(ev, "no entity resolver")
	}
	var eventMap map[string]any
	if ev.Event != nil {
		m, err := ev.Event.ToMap()
		if err != nil {
			return denied(ev, "converting event: %v", err)
		}
		eventMap = m
	}
	if err := ctx.Err(); err != nil {
		return deferred(ev, "%v", err)
	}
	if err := ev.Authorize(ctx); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return deferred(ev, "authorization interrupted: %v", err)
		}
		return denied(ev, "authorization: %v", err)
	}
	entity, err := ev.ResolveEntity(ctx)
	if err != nil {
		return deferred(ev, "resolving entity: %v", err)
	}
	if entity == nil {
		return deferred(ev, "resolving entity: no entity")
	}
	// CEL interrupts fire only while comprehensions iterate, so a context
	// cancelled during resolution must be caught here, not left to Evaluate.
	if err := ctx.Err(); err != nil {
		return deferred(ev, "interrupted after resolving entity: %v", err)
	}
	res, err := entitycel.Evaluate(ctx, ev.Trigger, entity, eventMap)
	if err != nil {
		if errors.Is(err, entitycel.ErrMalformedEntity) {
			return deferred(ev, "%v", err)
		}
		if ctx.Err() != nil {
			return deferred(ev, "evaluation interrupted: %v", err)
		}
		return denied(ev, "evaluating predicate: %v", err)
	}
	// A predicate without comprehensions completes even if ctx was cancelled
	// mid-evaluation; never report a definite outcome for a cancelled run.
	if err := ctx.Err(); err != nil {
		return deferred(ev, "interrupted after evaluation: %v", err)
	}
	switch res {
	case entitycel.True:
		return EntityDecision{Outcome: EntityEligible, Reason: ev.Harness + ": predicate is true"}
	case entitycel.False:
		return EntityDecision{Outcome: EntityNonMatch, Reason: ev.Harness + ": predicate is false"}
	}
	return deferred(ev, "predicate is unresolved over the available entity data")
}

func denied(ev EntityEvaluation, format string, args ...any) EntityDecision {
	return EntityDecision{Outcome: EntityDenied, Reason: ev.Harness + ": " + fmt.Sprintf(format, args...)}
}

func deferred(ev EntityEvaluation, format string, args ...any) EntityDecision {
	return EntityDecision{Outcome: EntityDeferred, Reason: ev.Harness + ": " + fmt.Sprintf(format, args...)}
}
