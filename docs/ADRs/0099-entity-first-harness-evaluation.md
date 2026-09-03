---
title: "99. Evaluate harnesses against entities with optional event context"
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

# 99. Evaluate harnesses against entities with optional event context

Date: 2026-09-03

## Status

Accepted

Supersedes [ADR 0063](0063-polling-based-work-discovery.md). It preserves the
poll-command and driver architecture where compatible, but replaces its
requirement that polling reconstruct changes as `NormalizedEvent` values.

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

Adopt one harness CEL predicate evaluated with a required forge-neutral
`entity` and a nullable `event`. The predicate may inspect either or both.
Event-driven dispatch resolves the event's entity and supplies both values;
scheduled discovery supplies the entity with `event` set to null. Events are a
low-latency source of candidates, not the authoritative representation of
whether an entity still needs work.

Harnesses declare entity sources that can enumerate candidate entity identities
and resolve one identity to its current normalized representation. Fullsend MAY
combine compatible sources into shared provider queries, but predicates and
processing state remain per harness. `fullsend poll` and its pluggable drivers
remain valid mechanisms for scheduled enumeration and resolution.

The normalized entity contract MUST provide stable cross-system identity,
current state, the bounded or queryable activity required by the harness, actor
context, and durable per-harness processing receipts. This allows a predicate
to ask whether qualifying activity exists and has not been handled, rather than
whether that activity is the current event. The field-level contract and query
planning protocol belong in a versioned normative specification.

Platform authorization remains mandatory before execution. Activity capable of
making a predicate match MUST retain source actor and authorization provenance;
unauthorized historical activity MUST NOT independently authorize a run. The
platform, not harness CEL, enforces the applicable authorization policy as
required by [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md).

Runs use the harness plus stable entity identity as their coordination subject,
aligning scheduled and event-driven evaluation with the serialized follow-up
model proposed in [#6909](https://github.com/fullsend-ai/fullsend/pull/6909).
Singleton scheduled runs without a natural entity are outside this decision.

## Consequences

- Harness authors maintain one predicate across event and polling contexts, but
  must guard event access when `event` is null.
- Poll drivers can discover current actionable state without reconstructing a
  complete synthetic event stream.
- Entity providers must expose sufficient history and per-harness receipts,
  increasing schema, storage, query-planning, and rate-limit complexity.
- Event actors and historical activity remain usable in CEL without weakening
  the centralized authorization boundary.
- ADR 0061's event-only CEL context and ADR 0063's event-reconstruction polling
  require compatibility and migration paths for existing harnesses and drivers.
