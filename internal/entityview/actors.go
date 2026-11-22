package entityview

import (
	"time"

	"github.com/fullsend-ai/fullsend/internal/actorcache"
	"github.com/fullsend-ai/fullsend/internal/normentity"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// roleRank orders the Authorization Contract roles; unknown roles rank 0.
var roleRank = map[normentity.Role]int{
	normentity.RoleExternal: 1,
	normentity.RoleNone:     2,
	normentity.RoleRead:     3,
	normentity.RoleTriage:   4,
	normentity.RoleWrite:    5,
	normentity.RoleMaintain: 6,
	normentity.RoleAdmin:    7,
}

type actorKey struct {
	id   actorcache.ActorID
	kind normentity.ActorKind
}

// decision is one actor's admission under the harness policy.
type decision struct {
	outcome  Outcome
	reason   string
	kind     normentity.ActorKind
	verdict  *Verdict // nil when no usable verdict exists
	evidence *ActorEvidence
}

// builder carries the state of one Build call.
type builder struct {
	in     Input
	system string
	host   string
	maxAge time.Duration
	memo   map[actorKey]*decision
	ev     Evidence
}

// admit decides whether actor a may contribute an actor-attributed value to
// this harness's view.
func (b *builder) admit(a normentity.Actor) *decision {
	id, ok := a.ID.Value()
	if !ok || id == "" {
		return &decision{outcome: Unresolved, reason: ReasonAttributionUnresolved}
	}
	kind, ok := a.Kind.Value()
	if !ok {
		return &decision{outcome: Unresolved, reason: ReasonAttributionUnresolved}
	}
	if kind != normentity.ActorHuman && kind != normentity.ActorBot {
		return &decision{outcome: Excluded, reason: ReasonNotAPerson, kind: kind}
	}
	actorID, err := actorcache.NewActorID(b.system, b.host, id)
	if err != nil {
		return &decision{outcome: Unresolved, reason: ReasonAttributionUnresolved, kind: kind}
	}
	key := actorKey{actorID, kind}
	if d, ok := b.memo[key]; ok {
		return d
	}
	d := &decision{kind: kind}
	v, source := b.lookup(actorID, kind)
	ev := &ActorEvidence{Actor: actorID, Kind: kind, Source: source}
	switch {
	case v == nil:
		d.outcome, d.reason = Unresolved, ReasonRoleUnresolved
	case kind == normentity.ActorBot && v.BotRole == "":
		d.outcome, d.reason = Excluded, ReasonUnregisteredBot
	case kind == normentity.ActorBot:
		// A registered bot uses its separate gate; the human minimum does
		// not apply to it (ADR 0151).
		d.outcome = Admitted
	case roleRank[v.Role] < roleRank[b.in.Policy.HumanMinRole] || roleRank[v.Role] < roleRank[normentity.RoleTriage]:
		d.outcome, d.reason = Excluded, ReasonBelowMinimum
	default:
		d.outcome = Admitted
	}
	if v != nil {
		d.verdict = v
		ev.Role, ev.BotRole, ev.PermissionSource = v.Role, v.BotRole, v.PermissionSource
		ev.LookupStart, ev.Age = v.LookupStart, b.in.Now.Sub(v.LookupStart)
	}
	ev.Outcome, ev.Reason = d.outcome, d.reason
	d.evidence = ev
	b.memo[key] = d
	return d
}

// lookup returns a usable verdict for the actor and where it came from.
func (b *builder) lookup(id actorcache.ActorID, kind normentity.ActorKind) (*Verdict, VerdictSource) {
	if v, ok := b.in.Roles.Live[id]; ok && b.usable(v, kind, actorcache.HardMaxAge) {
		return &v, SourceLive
	}
	scope := b.in.Roles.HumanScope
	if kind == normentity.ActorBot {
		scope = b.in.Roles.BotScope
	}
	if b.maxAge <= 0 || scope == nil {
		return nil, SourceNone
	}
	e, ok := b.in.Roles.Cache.Get(actorcache.Key{
		Actor:            id,
		Kind:             normevent.ActorKind(kind),
		Tenant:           scope.Tenant,
		Repository:       scope.Repository,
		Policy:           scope.Policy,
		PolicyRevision:   scope.PolicyRevision,
		PermissionSource: scope.PermissionSource,
		Ref:              scope.Ref,
		SourceRevision:   scope.SourceRevision,
	})
	if !ok {
		return nil, SourceNone
	}
	v := Verdict{
		Kind:             kind,
		Role:             normentity.Role(e.Role),
		BotRole:          e.BotRole,
		Verified:         e.RoleVerified,
		LookupStart:      e.LookupStart,
		PermissionSource: scope.PermissionSource,
	}
	// Recheck age and shape: a cache implementation is trusted to enforce
	// expiry, but the effective platform age is enforced here as well.
	if !b.usable(v, kind, b.maxAge) {
		return nil, SourceNone
	}
	if b.in.Roles.CacheShared {
		return &v, SourceSharedCache
	}
	return &v, SourceLocalCache
}

// usable reports whether v is a completed verdict for kind whose age is known
// and below maxAge at the check time.
func (b *builder) usable(v Verdict, kind normentity.ActorKind, maxAge time.Duration) bool {
	if !v.Verified || v.Kind != kind || v.LookupStart.IsZero() {
		return false
	}
	if b.in.Now.Before(v.LookupStart) || b.in.Now.Sub(v.LookupStart) >= maxAge {
		return false
	}
	switch kind {
	case normentity.ActorBot:
		return v.Role == normentity.RoleNone
	case normentity.ActorHuman:
		return v.BotRole == "" && roleRank[v.Role] > 0
	}
	return false
}

// withVerdict returns a with its role fields replaced by the verified
// verdict, or marked unknown when there is none, so CEL role comparisons
// never read an unverified or stale claim from the shared document.
func withVerdict(a normentity.Actor, d *decision) normentity.Actor {
	if d.verdict == nil {
		a.Role = normentity.Unknown[normentity.Role](normentity.ReasonNotLoaded)
		a.RoleVerified = normentity.Unknown[bool](normentity.ReasonNotLoaded)
		if d.kind == normentity.ActorHuman {
			a.BotRole = normentity.Field[string]{}
		} else {
			a.BotRole = normentity.Unknown[string](normentity.ReasonNotLoaded)
		}
		return a
	}
	a.Kind = normentity.Known(d.kind)
	a.Role = normentity.Known(d.verdict.Role)
	a.RoleVerified = normentity.Known(true)
	switch {
	case d.kind == normentity.ActorHuman:
		a.BotRole = normentity.Field[string]{}
	case d.verdict.BotRole == "":
		a.BotRole = normentity.Null[string]()
	default:
		a.BotRole = normentity.Known(d.verdict.BotRole)
	}
	return a
}

// subject projects an actor that is the subject of a relationship (an
// author, assignee, reviewer, or actor-valued extension) rather than the
// actor of an action. Subjects are not admission-gated; only their role
// fields are replaced with verified evidence. Team subjects are unchanged.
func (b *builder) subject(a normentity.Actor) normentity.Actor {
	if kind, ok := a.Kind.Value(); ok && kind == normentity.ActorTeam {
		return a
	}
	return withVerdict(a, b.admit(a))
}

// worst returns the more restrictive of two outcomes.
func worst(x, y Outcome) Outcome {
	if x == Unresolved || y == Unresolved {
		return Unresolved
	}
	if x == Excluded || y == Excluded {
		return Excluded
	}
	return Admitted
}
