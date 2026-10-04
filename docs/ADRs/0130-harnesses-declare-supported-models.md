---
title: "130. Harnesses declare the models and efforts they support"
status: Accepted
relates_to:
  - agent-architecture
  - adaptive-agent-selection
topics:
  - harness
  - models
  - routing
  - cost
---

# 130. Harnesses declare the models and efforts they support

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

A harness author knows which models and efforts their agent works with, and
the deciding sessions are not meant to run on a weak model (#6527). Nothing
lets the author say so: a flag, a `FULLSEND_*` variable, the agent's `agents:`
entry ([ADR 0091](0091-per-agent-runtime-model-effort.md)) or an overlay
([ADR 0112](0112-overlays-may-set-any-harness-field.md)) may pick any model.
When a model is not served, the fallback chain comes only from
`FULLSEND_FALLBACK_MODELS`, which the harness cannot set (#6964, agents#1225).

## Options

- **A model catalog with ranked tiers and a floor on protected harnesses**,
  rejected: fullsend would have to rank every model and keep that ranking
  current.
- **Fail runs outside the supported lists**, deferred: models change often,
  and until a repo can override the lists from `config.yaml`, opting out would
  mean composing a custom harness.
- **The harness lists what it expects and fullsend warns**, chosen.

## Decision

1. **Declaration.** A harness may declare a `supported:` block with `models`,
   an ordered list, and `efforts`, a set, for the agent's own model and
   effort, so `model:` stays the model it runs and `supported` says what it
   works with; the harness's own `model:` always counts as supported. A child
   composed through `base:` inherits the block unless it declares its own,
   which replaces it. `supported` is a guarded field
   ([ADR 0112](0112-overlays-may-set-any-harness-field.md)), so an overlay can
   change it only from trusted facts.
2. **Warning.** When the requested model, before `models.aliases` remapping,
   is not in `supported.models`, or the resolved effort is not in
   `supported.efforts`, the plan output and the resolution trace warn and name
   the source that chose it, and the run continues. When no source sets a
   model, the run uses the first entry; an unset effort doesn't warn. Failing
   the run instead is deferred until a repo can override the lists from
   `config.yaml`.
3. **Fallback.** When `supported.models` is declared, the fallback chain for
   the agent's model is the entries after that model in the list, in order, or
   the whole list when the model is not in it; the runtime's own fallback
   rules still apply. A set `FULLSEND_FALLBACK_MODELS` replaces the chain.

## Consequences

- Harness authors, not fullsend, say which models and efforts they expect;
  fullsend keeps no model ranking.
- A consumer can still run a newer or working model without waiting for the
  author to update the list, at the cost of a warning.
- #6527's protection is advisory until failing is enabled, but overlays keyed
  on author-shaped facts can't silence it by rewriting `supported`.
- Persona models ([ADR 0104](0104-per-persona-model-resolution.md)) are not
  checked against the lists, since personas often run on other models on
  purpose; they are deferred with persona routing.
- Harnesses that declare no `supported` block keep today's behavior;
  `supported` is a new structural container in
  [ADR 0127](0127-harness-schema-versioning-and-field-types.md)'s field-type
  classification, recorded in the harness field reference with the
  implementation.
