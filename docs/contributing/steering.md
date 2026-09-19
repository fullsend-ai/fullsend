# Steering a run in flight

How a fullsend run already working on a work item absorbs an update to that
item — a push, a comment, a stage command — rather than leaving it for the run
queued behind it.

This page is the contributor reference for the mechanics.
[ADR 0113](../ADRs/0113-steer-the-running-agent-on-work-item-updates.md) is the
decision to steer at all, and
[ADR 0117](../ADRs/0117-steer-interface-in-sandbox-mailbox.md)
decides the interface described here. The byte-level envelope the agent
receives is a versioned contract of its own, in
[normative/steer-envelope/v1](../normative/steer-envelope/v1/README.md).

On this page:

- [Concurrency](#concurrency) — the two switches, and why steering needs both
- [The steer contract](#the-steer-contract) — `runtime.Steerer`, and the caller's lock obligation
- [Transport](#transport) — how an update reaches the running session, and the bound on retrying one
- [Configuration](#configuration)

## Concurrency

Two independent switches, and steering needs both. Whether the run in flight survives a newer
event is decided elsewhere, by [ADR 0106](../ADRs/0106-serialize-agent-runs-and-coalesce-subsequent-events.md)
and the change that implements it — which this one is stacked on and merges before it. Under that
decision every `reusable-dispatch.yml` stage job carries

```yaml
concurrency:
  group: fullsend-<stage>-${{ github.repository }}-<item>
  cancel-in-progress: false
```

so the active run always finishes while one run waits behind it as the pending run — normally but
not necessarily the newest event — and works from the item's current state. Whether that surviving
run is *steered* is decided here, by the harness `steer:` block. Preserving is useful on
its own; steering builds on it.

`queue: max` is deliberately unused: it is incompatible with `cancel-in-progress: true`, and N
pending full runs is the failure mode preserving the active run removes.

## The steer contract

`runtime.Steerer` is an optional capability on a runtime:

```go
type Steerer interface {
    Steer(ctx context.Context, sandboxName string, msg SteerMessage) error
    Settle(ctx context.Context, sandboxName string) error
}
```

`RunParams.Steerable` asks a `Steerer` runtime to keep the session open; `Run` then returns only
after `Settle` and the agent's current turn. A runtime that does not implement `Steerer` ignores
the field, and its command line is unchanged.

Both methods are called **with `sandboxMu` held**. They write into the sandbox — a mailbox
append, or on Codex the stray-process sweep that interrupts the turn — and would otherwise race
the credential refreshers the runner already serializes through that lock. The lock lives in
`internal/cli`, so the runtime cannot take it itself; this is a caller obligation, documented on
the interface.

A steer is **content, never capability**. It cannot widen tools, role, model, scope, or the L7
network policy. Runtimes render it as a user message.

## Transport

The runner reaches the sandbox only through `exec` and `upload`, and neither gives it a handle on
the stdin of a process already running inside — so there is nothing to write a message to from
outside. The live runtimes therefore start with stdin connected to an in-sandbox feeder tailing a
mailbox file, and
`Steer` appends one line to that mailbox. Claude Code reads it as stream-json input, pi over its
rpc channel. Codex exec has no live channel: it interrupts the current turn and resumes the same
session with the message as the next prompt.

A steer counts as delivered only when the runtime observes the agent echo that specific message,
matched by the message's own identity rather than by counting — the mailbox lives in the runtime's
config directory, which the agent can write to, so an echo that matches nothing outstanding is
ignored.

Codex's resume is retried once and then abandoned. A resume can fail for a reason retrying cannot
clear — a thread the session no longer accepts fails identically every time — so a steer whose
resume fails twice is dropped with a warning naming the follow-up run. It is then undelivered, so
nothing acknowledges it, so no receipt claims it, and the queued run redoes the work: the same
fallback every other failure path takes.

## Configuration

Per-agent, off by default while the surfaces steering depends on land. A harness opts in by name:

```yaml
steer:
  enabled: true          # default: false — this is the opt-in
  max_steers: 2          # default: 2
  poll_interval_seconds: 30   # default: 30
```

`max_steers` and `poll_interval_seconds` are parsed and validated by this change; the code that
spends the cap and paces the interval arrives with the change that looks for updates.

`enabled` is a pointer internally so that absent and `false` mean different things: a block setting
only `max_steers` says nothing about whether steering is on, so it takes the default rather than
being read as either an opt-in or an opt-out, and the same config keeps its meaning when the
default changes.

No caller reads `enabled` yet. Nothing in this change sets `RunParams.Steerable`, so a harness that
opts in today gets an ordinary single-turn run — the block is accepted and validated, and the code
that consults it arrives with the change that looks for updates. When it does, `Steerable` will be
set only where the harness has opted in AND the runtime implements `Steerer`; otherwise it stays
false and `Run` is single-turn exactly as before.
