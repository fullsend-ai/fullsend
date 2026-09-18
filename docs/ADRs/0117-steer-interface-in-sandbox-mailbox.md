---
title: "117. The steer interface: a runtime capability fed through an in-sandbox mailbox"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - runtime
  - concurrency
---

# 117. The steer interface: a runtime capability fed through an in-sandbox mailbox

Date: 2026-09-18

## Status

Accepted

## Context

[ADR 0113](0113-steer-the-running-agent-on-work-item-updates.md) decides that a run in flight absorbs
updates to its work item. This ADR decides how an update reaches the agent, and what a runtime must
implement to be steerable.

The constraint is the sandbox. The runner reaches it only through `exec` and `upload`, and neither
gives it a handle on the stdin of a process already running inside, so it cannot write to the
agent's stdin from outside. The runtimes differ too: Claude Code and pi can take a message into a
live session, Codex exec cannot.

## Options

### Restart the session with the new input

Kill the agent and start it again with the update appended to the prompt. Works on every runtime and
needs no new channel, but throws away the turn in progress — the cost ADR 0113 exists to avoid.

### `upload` the message as a file

The runner already uploads files into the sandbox, but an upload only lands a file: nothing tells the
agent it arrived, and a runtime reading a live input stream never notices it.

### Write to the agent's stdin over `exec`

The direct route, and the one the live runtimes' input formats are designed for. An `exec` gets its
own stdin, not the stdin of the process already running, so there is no channel to write on.

### A mailbox the agent's stdin is already tailing

Start the agent with stdin connected to an in-sandbox feeder — `tail -n +1 -f` over a mailbox path —
and steer by appending one line to that mailbox with `exec`. It needs only the two operations the
sandbox already gives the runner, and starting at the first line means the opening prompt is
delivered by the same channel as every later steer.

## Decision

Steering is an optional runtime capability, `runtime.Steerer`, with `Steer` and `Settle`.
`RunParams.Steerable` asks a `Steerer` runtime to keep its session open; `Run` then returns only after
`Settle` and the agent's current turn. A runtime that does not implement `Steerer` ignores the field
and its command line is unchanged.

The transport is the mailbox. The live runtimes — Claude Code over stream-json input, pi over its rpc
steer — start with stdin connected to the in-sandbox feeder, and `Steer` appends one line over `exec`.
Codex exec has no live channel: it interrupts the current turn and resumes the same session with the
message as the next prompt. The interface is therefore two methods rather than a single send.

Both methods are called **with the runner's sandbox write lock held**. They write into the running
sandbox and would otherwise race the credential refreshers the runner already serializes through that
lock. The lock lives in `internal/cli`, so a runtime cannot take it itself; this is a caller
obligation documented on the interface.

Delivery is **acknowledged, not assumed**: a steer counts as delivered only when the runtime observes
the agent echo that specific message, matched by the message's own identity rather than by counting.

Three ceilings bound a steered run, all owned by the runner rather than the runtime: `steer.max_steers`
per **run** rather than per validation-loop iteration, a remaining-time floor below which the run
settles instead of starting a turn it cannot finish, and a budget of `min(agent timeout, forge token
life − margin)`.

The bytes the agent receives are a contract with the fleet agent definitions rather than an
implementation detail of one runtime, and are versioned separately in
[normative/steer-envelope/v1](../normative/steer-envelope/v1/README.md). The mechanics are in
[steering.md](../contributing/steering.md).

## Consequences

- A runtime becomes steerable by implementing two methods, and the runner needs no per-runtime
  branching beyond the capability check.
- The sandbox needs no new operation: `exec` and `upload` are the whole transport.
- On the live runtimes a steered run keeps one process, so the per-process guards run once for the
  run; interrupt-and-resume re-runs them per steer.
- A resume reads a session store the agent controls, so steering widens the prompt-injection surface
  — documented rather than signed, with tools still gated by
  [ADR 0090](0090-runtime-neutral-sandbox-hooks-contract.md).
- Changing what the agent sees becomes a versioned change to the envelope contract rather than an
  edit to a runtime.
