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
aggregated `total_cost_usd` summed across `validation_loop` retries. The
budget is enforced against the runtime's self-reported cost — see
[Cost reporting](#cost-reporting).

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
loop refuses to start another iteration. The boundary is deliberate — it is
the runtime-agnostic layer. Cost arrives as a runtime-reported aggregate
(Claude Code reports it once, in the final result event of a completed
iteration) and not every runtime offers an in-flight budget control (pi has
none), so fullsend does not interrupt an iteration already in flight. When
every iteration reports its cost the cap is soft by at most one iteration;
iterations that report no cost widen the overshoot (see Cost reporting
below). Claude Code's native per-invocation
`--max-budget-usd` flag is not used today; it could complement this cap as a
tighter in-flight bound for that runtime.

## Cost reporting

Enforcement depends entirely on the runtime's self-reported cost — fullsend
has no pricing table and accepts the reported total as-is (see the
[cost data contract](../../../guides/infrastructure/distributed-tracing.md#cost-data-contract)).
A runtime that reports zero or no cost under-counts the aggregate and
weakens the cap. Per-runtime coverage, per
[runtimes.md](../../../runtimes.md):

| Runtime | Cost reported | Effect on the cap |
|---|---|---|
| Claude Code | Only in a final result event | A crashed or killed iteration contributes $0 despite spending tokens |
| pi | Summed from provider-priced usage | An entry for an unpriced provider/model reports $0 |
| codex | None — codex sends no cost | The cap never trips; it is inert on this runtime |

`fullsend run` emits a warning when a cap is set and a completed iteration
reports no cost, so an inert cap is at least visible in the run log.

## The `over_budget` marker

`metrics.json` records `over_budget: true` **if and only if** the cap
suppressed a retry that was otherwise due. The suppressed retry can follow a
failed validation *or* a failed repository extraction (which retries without
reaching validation). The marker is absent when the run ended for its own
reasons — validation passed, iterations exhausted, a single-iteration
harness, or a crash — even if the final iteration's cost crossed the cap on
the way out. An iteration the runner killed at `timeout_minutes` ends the run
before any retry is due ([ADR 0105](../../../ADRs/0105-timed-out-iteration-ends-the-run.md)),
so such a run is not marked either, whatever its cost.

The marker records why retries stopped. It implies nothing about the final
validation state: the post-loop validation sweep may still pass an earlier
completed iteration of a run marked `over_budget`. Read it as "do not blame
the model for stopping here", never as a success or failure signal.

## Version skew

An older CLI ignores `max_cost_usd` (unknown fields are not rejected) and
runs uncapped — prior behavior. An older consumer of `metrics.json` never
sees `over_budget` (it is omitted when false) and is unaffected.
