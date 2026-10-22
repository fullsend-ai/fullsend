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

Harness `overlays:` ([ADR 0088](0088-cel-guarded-overlays.md)) already vary
a harness per event, but only its forge-oriented fields
([field reference](../contributing/harness-fields.md)), not `model` or `effort`.
They can already set `policy`, `providers` and `env` from any event field,
and the [normalized event](../normative/normalized-event/v1/README.md) is
gaining change facts the author shapes.

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
2. **Guarded fields.** Guarding stops a change's author from picking which of
   the harness's branches sets a guarded field. If an overlay keys on something
   the author controls, like which files changed, they can shape the change to
   win a looser `policy`, a broader token or a skipped scan. So a guarded field
   can only be set from **trusted facts**, which neither the author nor the
   event's actor can choose beyond what their permission allows:
   `runtime.forge`, `config` (from the same trusted ref as
   [OWNERS](../guides/user/owners-file-authorization.md) and the harness) and
   the event and entity fields
   ([ADR 0098](0098-entity-first-harness-evaluation.md)) the normalized event
   spec lists as trusted. Every field, including any added later, is guarded
   except `model`, `effort`, `description` and `doc`, which change how well or
   how cheaply an agent works, not what it may do. Validation fails an overlay
   that sets a guarded field when its `when:` reads anything else, such as a
   parent object of an untrusted field or an unresolvable path (`event != null`
   and `has()` on an object the schema always requires are allowed); on such an
   overlay a missing event or key is no match, as today, and any other
   evaluation error fails the run.
3. **Resolution trace.** The plan output and a dedicated span (for example
   `config.overlay_resolution`) record which overlays matched, the names of
   the fields each set (values only for `model` and `effort`), and for each of
   those fields whether it applied, which higher-precedence source overrode
   it, or, for `model` and `effort`, that a fallback replaced it (Decision 4).
4. **Routed-model fallback.** When the runtime reports that a model an
   overlay set isn't served, the runner retries the run once with the model
   and effort it would use without overlays; `FULLSEND_FALLBACK_MODELS`
   applies as each runtime allows.

## Consequences

- Per-task routing needs no mechanism of its own: a harness sets `model` and
  `effort` in overlays keyed on change facts.
- A repo routes a built-in agent by composing a custom harness on it through
  `base:`; overriding harness settings without a custom harness is separate
  work.
- An overlay that sets a guarded field from anything but trusted facts fails
  validation instead of relying on review, while today's built-in,
  `config`-keyed and no-event fall-through overlays keep working.
- A change's author can still steer which routing branch applies, so `model`
  and `effort` overlays can't enforce a floor: security-relevant harnesses
  should keep their strongest model and effort and any restrictive values at
  top level, and loosen them only in overlays whose `when:` positively proves
  a trusted fact; a restriction whose loosened value is false, zero or empty
  needs a separate harness composed through `base:`.
- The runtime and persona models are not harness fields (ADR 0091,
  [ADR 0104](0104-per-persona-model-resolution.md),
  [ADR 0126](0126-fullsend-owned-codex-subagents.md)), so overlays cannot
  route them.

### Deferred

- **Persona-model routing**, with the configuration standardization work in
  flight rather than a new harness field.
- **LLM-based routing** isn't decided here (see AISDLC-95) and would need its
  own ADR.
