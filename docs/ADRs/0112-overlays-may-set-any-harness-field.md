---
title: "112. Harness overlays may set any harness field"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
  - adaptive-agent-selection
topics:
  - harness
  - overlays
  - cel
  - security
  - routing
---

# 112. Harness overlays may set any harness field

Date: 2026-10-01

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

Tasks differ: a typo fix can run cheaper, a security-sensitive change deserves
a deeper review (#5777, #2842, agents#497, agents#535) and a listed model is
sometimes not served (#6964), yet a repo picks one model and effort per agent
([ADR 0091](0091-per-agent-runtime-model-effort.md)).

Harness `overlays:` ([ADR 0088](0088-cel-guarded-overlays.md)) already
change a harness per event through a CEL `when:`, but only for the
forge-oriented fields in the
[harness field reference](../contributing/harness-fields.md); `model`,
`effort` and the other top-level-only fields cannot vary per event.

Overlays can already set `policy`, `providers` and `env` from any event
field, and the [normalized event](../normative/normalized-event/v1/README.md)
is gaining change facts the author shapes. A harness's top-level values are
fixed and reviewed with it, but an overlay's condition picks which values
apply, so a condition that reads author-shaped facts lets the change's
author choose between the harness's branches.

## Options

- **A separate `routing:` policy in `.fullsend/config.yaml`** (#7808): it
  also routes the runtime, but adds a second conditional mechanism.
- **Per-prompt routing in the sandbox** (#6361), rejected: it reads
  author-controlled prompts and loses the prompt cache mid-loop.
- **Overlays set any field**, chosen: routing and every other conditional
  setting reuse one mechanism.

## Decision

1. **Any field.** An overlay may set any harness field except `base`,
   `trigger`, `overlays`, `slug`, `role` and the deprecated `forge`, which
   decide how a harness is found, composed or dispatched and stay top level.
   Field merge rules and ADR 0091's precedence are unchanged, so an overlay
   can set a field only to a non-empty, non-zero value.
2. **Guarded fields.** A guarded field is one an overlay may set only from
   facts the change's author can't choose: an overlay that sets one may read
   only `runtime.forge`, `config` (read from the same trusted ref as
   [OWNERS](../guides/user/owners-file-authorization.md) and the harness
   itself) and the event and entity fields
   ([ADR 0098](0098-entity-first-harness-evaluation.md)) the normalized event
   spec lists as trusted. Every field is guarded except `model`, `effort`,
   `description` and `doc`, so a field added later is guarded by default.
   Validation rejects any other read, including the value of a parent object
   of an untrusted field or a path it cannot resolve; `event != null` and a
   `has()` test on an object the schema always requires, such as
   `has(event.source)`, are allowed. On such an overlay, a missing event or
   key counts as no match, as today, and any other evaluation error fails the
   run.
3. **Resolution trace.** The plan output and a dedicated span (for example
   `config.overlay_resolution`) record which overlays matched, the names of
   the fields each set (values only for `model` and `effort`), and for each of
   those fields whether it applied or which higher-precedence source overrode
   it.

## Consequences

- Per-task routing needs no mechanism of its own: a harness sets `model` and
  `effort` in overlays keyed on change facts, and may declare the models it
  expects ([ADR 0130](0130-harnesses-declare-supported-models.md)).
- A repo routes a built-in agent by composing a custom harness on it through
  `base:`; overriding harness settings without a custom harness is separate
  work.
- Setting a guarded field from something a change's author or an event's actor
  can choose is rejected at validation instead of being left to review, while
  today's built-in overlays, custom overlays keyed on `config` and overlays
  that fall through when there is no event keep working.
- A change's author can still steer which routing branch applies, so
  security-relevant harnesses should keep their strongest model and effort and
  any restrictive values at top level, and loosen them only in overlays whose
  `when:` positively proves a trusted fact; a restriction whose loosened value
  is false, zero or empty needs a separate harness composed through `base:`.
- The runtime and persona models are not harness fields (ADR 0091,
  [ADR 0104](0104-per-persona-model-resolution.md),
  [ADR 0126](0126-fullsend-owned-codex-subagents.md)), so overlays cannot
  route them.

### Deferred

- **Persona-model routing**, with the configuration standardization work in
  flight rather than a new harness field.
- **LLM-based routing** isn't decided here (see AISDLC-95) and would need its
  own ADR.
