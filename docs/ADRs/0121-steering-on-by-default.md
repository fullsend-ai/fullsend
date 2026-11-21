---
title: "121. Steering on by default, with per-harness and per-repository opt-out"
status: Accepted
relates_to:
  - agent-architecture
  - flapping-convergence
topics:
  - configuration
  - concurrency
  - harness
---

# 121. Steering on by default, with per-harness and per-repository opt-out

Date: 2026-11-02

## Status

Accepted

[ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md)
refers to this ADR as the decision that turns steering on by default.

Companion decisions:

- ADR 0117 (proposed): what a steer is and what it may carry.
- ADR 0118 (proposed): runtime delivery.
- ADR 0119 (proposed): the GitHub Actions change signal.
- ADR 0120 (proposed): receipts for absorbed steers in host-written run
  metadata.

## Context

ADR 0117 (proposed) defines steering, the delivery of freshly admitted entity
updates into a running cycle, as an optional fast path inside an
[ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md) cycle. It leaves
open whether a harness steers when its configuration does not mention
steering.

The origin issue for this work,
[#6957](https://github.com/fullsend-ai/fullsend/issues/6957), sets steering on
by default in its validation criteria: a harness that sets
`steer: {enabled: false}` keeps the
[ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md)
behaviour, where later events wait for the next run.

This ADR aims to formalize that default, the switches that turn steering off,
how those switches compose across layers, and the per-cycle steer limit.

## Options

### Opt-in per harness (rejected)

Steering is off unless a harness turns it on. Every harness that does not
mention steering, which is most of them, keeps the cost of stale output.

### On by default, with opt-out (chosen)

Every eligible harness steers unless it, or its repository, turns steering
off. A harness whose agent does not act on the steer envelope (ADR 0117) must
be found and opted out.

## Decision

We adopt steering on by default, with a per-harness and a per-repository
opt-out and a per-cycle `max_steers` limit, to give every eligible harness the
steering gains without a per-harness edit.

After ADR 0144, what steering still adds is avoiding stale output from a turn
that finishes on old state, redirecting work in flight, and covering
event-only harnesses on GitHub Actions where ADR 0144 opens no listener. Those
gains apply to every harness.

| Switch | Where | Effect |
|---|---|---|
| `steer: {enabled: false}` | Harness | Steering off for that harness |
| `steer: {enabled: false}` | Repository configuration | Steering off for every harness in the repository |
| `steer: {max_steers: N}` | Harness | At most `N` steers per cycle |

### Composition

- **Any `false` wins.** An explicit `enabled: false` in any configuration
  layer turns steering off, and no other layer can turn it back on. Unlike
  the fields those ADRs override by the later layer, `steer.enabled` composes
  by restriction across all of these layers:
  - **Harness layers:** every base harness in the `base:` chain, the child
    harness, its forge overrides, and CEL-guarded overlays
    ([ADR 0088](0088-cel-guarded-overlays.md),
    [ADR 0112](0112-overlays-may-set-any-harness-field.md)).
  - **Repository layers:** `.fullsend/config.base.yaml` and
    `.fullsend/config.yaml`
    ([ADR 0069](0069-ready-made-configuration-presets.md)), and managed
    repository configuration
    ([ADR 0122](0122-declarative-repo-configuration.md)).
- **Tuning never re-enables.** A `steer` block merges field by field. A block
  that sets only tuning fields, such as `max_steers`, or an empty `steer: {}`,
  leaves `enabled` as the other layers set it. If no layer sets `enabled`,
  steering is on.
- **Dropping an opt-out is a relaxation.** This ADR adds `steer.enabled` to
  the settings the ADR 0122 pre-write safety gate compares: removing an
  effective `enabled: false` from repository configuration is a relaxation,
  rejected unless the manifest declares it.
- **Not before readiness.** The default applies only once the ADR 0117
  dependencies are in place, including the per-steer pre-script gate, active
  run preservation, and a writer the agent cannot impersonate. On GitHub
  Actions it also needs the ADR 0119 listing credential, since review and
  other event-only harnesses steer only through that signal.

### The `max_steers` limit

- **Layer.** A harness field only; repository configuration rejects it as an
  unknown key. Across the harness layers above, the lowest value wins.
- **Default 3.** Each steer costs a model turn, sandbox time, and, on a push,
  a host-side fetch and object upload. Three covers a three-push local
  iteration in one cycle: push 1 starts the cycle, pushes 2 and 3 take two
  steer slots, and one slot remains for another push or comment. Rapid pushes
  between checks coalesce into one steer, and the next cycle picks up anything
  beyond. To be revisited after the first release.
- **Range 1 to 9999, checked per layer.** Each layer is validated as written,
  before composition, so an invalid value in one layer is an error even when
  another layer sets a lower one. Omitted is distinct from `0`; `0`, negative
  numbers, values above 9999, non-integers, and unknown keys under `steer` fail
  validation. Use `enabled: false`, not `0`, to turn steering off. The upper
  bound comes from the four-digit steer directory names in steer envelope v1
  (proposed with ADR 0117).

### Harnesses that need action

- **Agent does not act on the envelope.** The harness opts out with
  `enabled: false`.
- **Harness has a pre-script.** Under ADR 0117 a pre-script is run as a
  steer gate only when the harness declares that its pre-script supports it,
  and a gate that does not write `steer_checked=true` never lets a steer
  through. Until the pre-script is updated and declared, the harness does not
  steer, and its pre-script is never run as a gate.
- **Pre-script skips work on ADR 0120 receipts.**
  - *Records (for example comments):* an acknowledged steer proves delivery,
    not action. A pre-script must not skip work on record receipts unless its
    agent acts on steer envelopes.
  - *Head moves (pushes):* ADR 0120 never lists a head as absorbed, so the
    follow-up cycle for a pushed head always runs, and only a full cycle can
    approve that head (ADR 0117).

### What an operator sees

Steering posts nothing on the entity when it is off or declines. The cycle's
trace records the effective `enabled` and `max_steers` values with the layer
that set each, any readiness blocker, and, for each declined attempt, the
ADR 0117 step and reason.

## Forge scope

The default applies wherever ADR 0117 steers a running cycle:

- **Any forge where ADR 0144 is implemented:** through ADR 0144 events and
  wakes.
- **GitHub Actions, additionally:** through the ADR 0119 run-record signal.
  That signal reads GitHub Actions run records only, so on GitLab and Forgejo
  the switches govern ADR 0144 wakes alone.
- **Jira and other non-forge entities:** they do not steer, because
  entity-context-snapshot v1 does not stage them, so the switches have no
  effect on them.

## Consequences

- Every eligible harness without a pre-script steers on the release that
  enables steering, without a per-harness edit. A harness with a pre-script
  steers only once it declares its pre-script supports the steer gate.
- Harnesses whose agents do not act on the steer envelope must opt out before
  that release. Otherwise updates delivered to them are acknowledged but not
  acted on until the next cycle.
- A steered sandbox stays up longer, so the default adds VM time on every
  harness that has not opted out.
- The new `steer` fields need entries in the harness and configuration
  references when steering is implemented.
