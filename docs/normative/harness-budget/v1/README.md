# Harness budget v1

The **`max_cost_usd` contract** — the budget cap a harness places on one
`fullsend run`
([ADR 0097](../../../ADRs/0097-harness-max-cost-usd-budget-cap.md)).

This document is normative. The Go implementation lives in
[`internal/harness`](../../../../internal/harness/harness.go) (validation and
`base:` composition) and
[`internal/cli/run.go`](../../../../internal/cli/run.go) (enforcement and the
`metrics.json` marker).

## Field

```yaml
max_cost_usd: 5.00
```

Optional float. A hard budget in USD for one run, checked against the run's
aggregated `total_cost_usd` summed across `validation_loop` retries.

## Validation

| Value | Behavior |
|-------|----------|
| absent | Unlimited (and inherits via `base:` — see below). |
| `0` | Unlimited, explicitly. |
| positive finite | The cap. |
| negative | Rejected at harness load. |
| `.nan`, `.inf`, `-.inf` | Rejected at harness load — either would silently disable the cap (NaN fails every comparison; no finite aggregate exceeds +Inf). |

## Inheritance

The field is **presence-aware** in `base:` composition: an absent field
inherits the base's value, while any explicit value — including `0` —
overrides it. A child can therefore disable an inherited cap with
`max_cost_usd: 0`. The field is top-level only; it cannot be overridden in
`forge.<platform>` blocks or `overlays:` entries (see the
[Harness Field Reference](../../../contributing/harness-fields.md)).

## Enforcement boundary

The cap is checked **between iterations**: once the aggregate cost has
**reached** the cap (`>=` — a budget that is exactly spent is spent), the run
loop refuses to start another iteration. The runtime reports cost once, in
the final result event of a completed iteration, so an iteration already in
flight is never interrupted; the cap is soft by at most one iteration.

## The `over_budget` marker

`metrics.json` records `over_budget: true` **if and only if** the cap
suppressed a retry that was otherwise due. The suppressed retry can follow a
failed validation *or* a failed repository extraction (which retries without
reaching validation). The marker is absent when the run ended for its own
reasons — validation passed, iterations exhausted, a single-iteration
harness, or a crash — even if the final iteration's cost crossed the cap on
the way out.

The marker records why retries stopped. It implies nothing about the final
validation state: the post-loop validation sweep may still pass an earlier
completed iteration of a run marked `over_budget`. Read it as "do not blame
the model for stopping here", never as a success or failure signal.

## Version skew

An older CLI ignores `max_cost_usd` (unknown fields are not rejected) and
runs uncapped — prior behavior. An older consumer of `metrics.json` never
sees `over_budget` (it is omitted when false) and is unaffected.
