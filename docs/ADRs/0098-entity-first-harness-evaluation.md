---
title: "98. Evaluate harnesses against entities with optional event context"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - agents
  - cel
  - dispatch
  - entities
  - polling
---

# 98. Evaluate harnesses against entities with optional event context

Date: 2026-09-03

## Status

Accepted

Partially supersedes [ADR 0061](0061-harness-cel-dispatch.md): a
`NormalizedEvent` is no longer the sole CEL input to a harness trigger. ADR
0061's remaining dispatch decisions remain current.

Partially supersedes [ADR 0063](0063-polling-based-work-discovery.md): polling
no longer has to reconstruct changes as `NormalizedEvent` values. ADR 0063's
poll command, driver architecture, per-repo scope, and coordination decisions
remain current.

Extends [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)
with a scoped authorization path for Fullsend-originated entity discovery that
has no prompting event actor; event-backed dispatch authorization is unchanged.

## Context

[ADR 0061](0061-harness-cel-dispatch.md) made a `NormalizedEvent` the sole CEL
input to a harness trigger, and ADR 0063 extended that model to polling by
reconstructing entity changes as events. That fits transition-oriented rules,
but state-oriented agents must recover complete event history to answer durable
questions such as whether an authorized `/fs-fix` comment remains unhandled or
an issue needs periodic reconsideration
([#313](https://github.com/fullsend-ai/fullsend/issues/313),
[agents#1137](https://github.com/fullsend-ai/agents/issues/1137)).

Event reconstruction loses source-specific fidelity, consumes API capacity, and
can permanently miss work across checkpoint gaps. Conversely, replacing events
would discard useful transition and actor context. Harness authors need one rule
that works when either a live event or scheduled discovery prompts evaluation.

## Options

### Continue reconstructing events for polling

One trigger shape remains simple, but poll drivers must recover ordered history
and a missed transition may never be reconsidered.

### Add separate event and entity predicates

Each path is explicit, but authors must keep two routing rules consistent and
the two rules may disagree about the same entity.

### Use one predicate over an entity and optional event

Both paths share one decision rule; entity resolution and durable processing
state become platform responsibilities.

## Decision

- **Predicate context:** Adopt one harness CEL predicate evaluated with a
  required forge-neutral `entity` and a nullable `event`. The predicate may
  inspect either or both. Harnesses that declare entity sources and can
  therefore be evaluated without a prompting event MUST test `event != null`
  before accessing event fields. This test provides crash safety only; a
  harness intended to match during scheduled discovery MUST also express an
  entity-state condition that can succeed when `event` is null. Harnesses
  without entity sources remain event-triggered only, always receive an event,
  and need no compatibility change to existing event-based predicates. Trigger
  CEL represents a missing event as CEL null. Overlay `when` expressions also
  receive the required `entity` and nullable `event`. The normalized entity
  MUST expose its source system as `entity.source.system`; overlays used for
  entity discovery select source-specific configuration from that field rather
  than `event.source.system`.
- **Candidate sources:** Event-driven dispatch resolves the event's entity and
  supplies both values; scheduled discovery supplies the entity with `event`
  set to null. Events are a low-latency source of candidates, not the
  authoritative representation of whether an entity still needs work.
- **Entity resolution:** Harnesses declare entity sources that enumerate
  candidate identities and resolve an identity to its current normalized
  representation. Fullsend MAY combine compatible sources into shared provider
  queries, but each harness defines its own predicate and handled-state test.
  `fullsend poll` and its pluggable drivers remain valid mechanisms for
  scheduled enumeration and resolution.
- **Handled-state evidence:** The normalized entity contract MUST provide
  stable cross-system identity, current state, the bounded or queryable
  activity required by the harness, and actor context. An action-indicating
  element is actor-originated entity history whose content or state is treated
  as a request for a stage to run, including issue or change-proposal bodies,
  comments, reviews, and label applications when used for that purpose. Every
  such element MUST expose actor provenance and the actor's current forge
  permission level resolved at entity evaluation time. A harness MAY infer that
  qualifying activity has already been handled from entity state, such as an
  existing triage comment, or use an explicit per-harness receipt or poll
  checkpoint. The field-level contract and query-planning protocol belong in a
  versioned normative specification and MUST exist before entity-first
  execution is enabled.
- **Scheduling:** Recurring evaluation MAY be initiated by a platform/default
  clock or constrained by scheduling metadata in the harness. The clock is
  scheduling machinery, not an authorization principal. Scheduled entity
  discovery is denied unless the harness opts in through valid entity sources
  and effective platform policy permits it. Missing or malformed entity sources
  deny scheduled evaluation for that harness; missing, malformed, or
  unverifiable platform eligibility policy denies every scheduled evaluation
  governed by that policy. The effective schedule may still come from platform
  defaults or harness metadata. Every resulting run uses the harness's
  configured agent identity and permissions.
- **Authorization:** [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)
  continues to authorize event-backed dispatch from its event actor. A
  `fullsend poll` entity-discovery run is instead authorized by its trusted
  Fullsend-controlled origin; callers that cannot establish that provenance are
  denied. Entity history remains untrusted input. For each candidate harness,
  before evaluating its CEL predicate, the platform MUST enumerate a
  platform-defined closed superset of action-indicating elements for the entity
  kind and remove those whose actor's current permission does not meet that
  harness's observation or mutation threshold. Failed, unavailable, or
  incomplete enumeration denies evaluation of that entity; only successful
  enumeration may produce an empty set for state-only predicates. CEL can
  additionally restrict action using the retained elements and their current
  actor permissions, but does not select an element for a later authorization
  gate. Further trust and injection filtering MAY run after CEL routing and
  before the harness pre-script. Neither entity content nor a historical actor
  can alter the run's configured identity or permissions.
- **Attribution:** A scheduled run without a prompting human is attributable to
  the verified Fullsend invocation identity, target repository, effective
  platform policy, harness revision, and resolved entity. The audit record also
  identifies every retained action-indicating element and its actor. For
  state-only predicates such as staleness, the configured service identity and
  versioned policy/harness configuration are the initiating authority.

## Consequences

- Harness authors maintain one predicate across event and polling contexts.
  Harnesses opting into entity sources must guard event access with
  `event != null`, and those intended to match during scheduled discovery must
  include an entity-state condition that can succeed when `event` is null.
- Poll drivers can discover current actionable state without reconstructing a
  complete synthetic event stream.
- Harnesses may reuse entity state as handled-state evidence instead of writing
  separate receipts or checkpoints.
- Efficient cross-entity enumeration may still require provider-specific
  indexing beyond a forge's native query API; resolution and persistence do not
  necessarily require new infrastructure.
- Event actors and historical activity remain usable in CEL without weakening
  the centralized authorization boundary.
- Existing source-specific overlay expressions that inspect
  `event.source.system` must use `entity.source.system` for entity-discovery
  runs.
- Existing event-only harnesses remain compatible; harnesses opt into nullable
  event context by declaring entity sources. ADR 0063's event-reconstruction
  path still requires a migration plan for polling drivers.
