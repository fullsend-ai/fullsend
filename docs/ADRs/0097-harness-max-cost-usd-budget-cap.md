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
`total_cost_usd` across `validation_loop` retries. The run loop refuses to
start another iteration once the cap is reached; in-flight work is never
interrupted, and `metrics.json` records `over_budget: true` when the cap
suppressed a retry. The field-level contract — validation, `base:`
inheritance, the enforcement boundary, and `over_budget` semantics — is
normative in
[`docs/normative/harness-budget/v1`](../normative/harness-budget/v1/README.md),
not here.

## Consequences

- A repo can bound the worst-case spend of a retrying harness without
  distorting `max_iterations` or `timeout_minutes`.
- The cap is soft by one iteration: a run can finish up to one full
  iteration past the budget, because cost is only known at iteration end.
- `over_budget` in `metrics.json` lets post-scripts and dashboards
  attribute halted runs to the budget rather than to model failure.
- Budget enforcement gains a compose-level subtlety (absent vs explicit
  `0`); the field reference documents the merge rule.
