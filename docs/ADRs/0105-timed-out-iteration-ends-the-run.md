---
title: "105. The runner owns the iteration budget"
status: Accepted
relates_to:
  - agent-architecture
topics:
  - runner
  - validation-loop
  - sandbox
---

# 105. The runner owns the iteration budget

Date: 2026-09-05

## Status

Accepted

## Context

A harness with a `validation_loop` retries the agent up to `max_iterations`
times when the validation script rejects an iteration's output
([ADR 0022](0022-harness-level-output-schema-enforcement.md)). The retry was
built for output defects: the agent finished, wrote a result, and the result
did not match the schema.

The same loop also retried an iteration the runner had killed at
`timeout_minutes`. A killed iteration has no output, so validation fails and
the next iteration replays the same run with the same budget and the same
prompt. On the fleet review harness (20 minutes, two iterations) every
timed-out review took the retry — 11 of 11 in the 2026-08-18 → 2026-09-05
sample — and none of the retries produced a result. Each timeout cost two
budgets and ended as `validation failed after 2 iteration(s)`, which names
the wrong cause. Agents without a validation loop already ended with a
distinct error after a timeout
([#5075](https://github.com/fullsend-ai/fullsend/issues/5075)).

The agent could not avoid this. The runner knew the budget and the moment the
iteration would be killed; the sandbox did not. Skills carried their own copy
of the number (`TIMEOUT_SECONDS` in the agents repository's harness files)
and measured from their own start time.

## Decision

The budget is the runner's to enforce and to announce; the validation loop
retries output defects only.

- A killed iteration — non-zero process exit after at least 90 % of
  `timeout_minutes`, the `agentTimedOut` test from #5075 — ends the loop.
  The runner first terminates the agent's processes in the sandbox: an
  OpenShell exec timeout drops the relay and leaves the command running under
  PID 1 (upstream will not add a per-exec kill,
  [NVIDIA/OpenShell#3159](https://github.com/NVIDIA/OpenShell/issues/3159)),
  and before this change the next iteration's stray-process sweep was what
  stopped it. Every post-loop step still runs (validation sweep, metrics, transcript
  error surfacing, output redaction scan, audit-chain check, results). If no
  iteration produced a valid result the run ends with
  `agent timed out after <elapsed> without completing (timeout: <budget>)`,
  which the status comment renders as its failure detail. A valid result
  wins: the killed iteration's output is still validated. An iteration that
  exits early with invalid output is a validation failure and keeps its retry.
- Before every iteration the runner writes the budget
  (`FULLSEND_TIMEOUT_MINUTES`) and the kill time (`FULLSEND_ITERATION_DEADLINE`,
  Unix seconds, from the same start time its heartbeat uses) into a
  runner-owned file that the sandbox environment sources after every
  harness-controlled entry. Both names are reserved in `env.sandbox`. Every
  runtime sources that environment (claude, pi, codex); child processes — pi
  sub-agents included — inherit both.

## Consequences

- A review that cannot finish inside its budget fails once, in one budget,
  and the failure names the budget.
- Skills pace themselves from `FULLSEND_ITERATION_DEADLINE` instead of a
  copied number; the `TIMEOUT_SECONDS` mirror in the agents repository's
  harness files is redundant once this ships.
- The 90 % test is a heuristic, and it counts a transcript-reported error as
  a failure the way #5075 does (pi and codex fold that case into their exit
  code): an agent that fails in the last tenth of its budget is reported as
  timed out and gets no retry, whatever the cause.
- The variables are advisory: written before the kill timer starts (the kill
  lands at or a few seconds after the deadline), and if the runner cannot
  write them it warns and removes the previous iteration's file; only a stale
  deadline that survives into the next iteration is treated as fatal.
- Deferred under #7042: a wrap-up instruction to the running agent at T−N
  minutes needs the steer channel of
  [#6959](https://github.com/fullsend-ai/fullsend/pull/6959), not on `main`;
  resuming a killed iteration from its partial state is out of scope.

Note (2026-09-06): [NVIDIA/OpenShell#3159](https://github.com/NVIDIA/OpenShell/issues/3159)
was closed as expected usage rather than a refused feature: an exec's processes
are not expected to exit with the caller, and a runner that reuses one sandbox
across execs owns their cleanup. The sweep above is that cleanup, and it is
best effort (see [`fullsend run` § Budget and deadline](../cli/run.md#budget-and-deadline)).
