---
title: "97. Harness-level max_cost_usd budget cap"
status: Accepted
relates_to:
  - operational-observability
  - security-threat-model
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
[operational-observability.md](../problems/operational-observability.md)),
and the threat model's agentic-DOS section proposes cost budgets and asks
whether they should hard-stop or gate on human approval (see
[security-threat-model.md](../problems/security-threat-model.md)).

Fullsend enforces at the iteration boundary because that is the
runtime-agnostic layer: cost arrives as a runtime-reported aggregate (for
Claude Code, once, in the final result event of a completed iteration), and
not every runtime offers an in-flight budget control — pi has none, and
codex reports no cost at all (see [runtimes.md](../runtimes.md)).

Claude Code is the exception: it ships a native `--max-budget-usd` flag
that stops an invocation once its own API spend reaches the amount. This
ADR does not pass it through — `buildRunCommand`
([`internal/runtime/claude.go`](../../internal/runtime/claude.go)) never
sets it, and no runtime reads `max_cost_usd`. That is a deliberate gap,
not an oversight: the flag bounds one invocation, while `max_cost_usd`
bounds a run's total across `validation_loop` retries, so forwarding it
means deciding what per-iteration remainder each invocation gets and what
a mid-iteration budget trip should mean for the run's outcome. Wiring it
would tighten the cap on the Claude runtime only, and is left to its own
decision.

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
- The cap is soft — by one iteration when every iteration reports its
  cost (cost is only known at iteration end), and wider when they do
  not (a crashed or unpriced iteration contributes $0 — see the
  normative contract's cost-reporting section) — leaving the Claude
  runtime's native per-invocation budget flag as an undecided future
  tightening for that runtime.
- `over_budget` in `metrics.json` lets post-scripts and dashboards
  attribute halted runs to the budget rather than to model failure.
- Budget enforcement gains a compose-level subtlety (absent vs explicit
  `0`); the field reference documents the merge rule.
