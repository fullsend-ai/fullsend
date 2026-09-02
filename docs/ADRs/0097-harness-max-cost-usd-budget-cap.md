---
title: "97. Harness-level max_cost_usd budget cap"
status: Accepted
relates_to:
  - operational-observability
topics:
  - harness
  - configuration
  - observability
---

# 97. Harness-level max_cost_usd budget cap

Date: 2026-09-02

## Status

Accepted

## Context

The harness schema ([ADR 0024](0024-harness-definitions.md)) caps a run's
wall-clock time (`timeout_minutes`) but not its spend. A validation loop
([ADR 0022](0022-harness-level-output-schema-enforcement.md)) retries a
failing agent until `max_iterations` is exhausted regardless of what the
iterations cost, so a repo could only bound cost indirectly, by lowering
iteration counts or timeouts that exist for other reasons. Cost anomalies —
a single run costing many times the median — are an explicit operational
concern (see
[operational-observability.md](../problems/operational-observability.md)).

Claude Code reports `total_cost_usd` once, in the final result event of a
completed iteration, so no mechanism at this layer can interrupt an
iteration already in flight; the only enforceable boundary is between
iterations.

## Decision

Add an optional `max_cost_usd` field to the harness schema: a hard budget
in USD for one run, checked between iterations against the aggregated
`total_cost_usd` across `validation_loop` retries.

- The run loop refuses to start another iteration once aggregate cost has
  **reached** the cap (`>=` — a budget that is exactly spent is spent).
  In-flight work is never interrupted.
- `0` means unlimited and is the default. Validation rejects negative and
  non-finite (NaN/±Inf) values, since those would silently disable the cap.
- The field is presence-aware in `base:` composition: an absent field
  inherits the base's cap, while an explicit `0` in a child overrides an
  inherited cap with unlimited. It is top-level only — not
  forge-overridable (see the
  [Harness Field Reference](../contributing/harness-fields.md)).
- `metrics.json` records `over_budget: true` only when the cap actually
  suppressed a retry that was otherwise due, distinguishing "halted at
  budget" from a run that was ending anyway or a crash.

## Consequences

- A repo can bound the worst-case spend of a retrying harness without
  distorting `max_iterations` or `timeout_minutes`.
- The cap is soft by one iteration: a run can finish up to one full
  iteration past the budget, because cost is only known at iteration end.
- `over_budget` in `metrics.json` lets post-scripts and dashboards
  attribute halted runs to the budget rather than to model failure.
- Budget enforcement gains a compose-level subtlety (absent vs explicit
  `0`); the field reference documents the merge rule.
