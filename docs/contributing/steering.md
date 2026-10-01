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
append, or on Codex the sandbox stop and start that interrupt the turn — and would otherwise race
the credential refreshers the runner already serializes through that lock. The lock lives in
`internal/cli`, so the runtime cannot take it itself; this is a caller obligation, documented on
the interface.

`Steer` returns `ErrSteerSessionLost` when an interrupt-and-resume runtime stopped the sandbox
and it did not come back. The error is terminal. The steer was not delivered, every later `Steer`
on that session returns it again, and `Run` ends with an error that wraps it. The caller stops
steering and leaves the update to the queued run.

A steer is **content, never capability**. It cannot widen tools, role, model, scope, or the L7
network policy. Runtimes render it as a user message.

## Transport

The runner reaches the sandbox only through `exec` and `upload`, and neither gives it a handle on
the stdin of a process already running inside — so there is nothing to write a message to from
outside. The live runtimes therefore start with stdin connected to an in-sandbox feeder tailing a
mailbox file, and
`Steer` appends one line to that mailbox. Claude Code reads it as stream-json input, pi over its
rpc channel. Codex exec has no live channel. The runner stops the sandbox, which ends every process
in it (the codex turn included) and keeps its disk state. It starts the sandbox again and resumes
the same session with the message as the next prompt. When the sandbox stops, the turn's `exec`
returns the relay-closed exit. The runner marked the turn before stopping, so that exit counts as
its own interrupt: not an agent failure, and not a failed resume. If the stop fails, the runner
prints a warning and the steer stays queued. The steer is then delivered when the current turn ends
on its own, and falls to the queued run only if the run is stopped first, by an error or by its
budget running out. If the sandbox does not come back — the start fails, or it leaves Ready within
the settle check after the start — the session is lost: `Steer` returns `ErrSteerSessionLost`, the
steer is never acknowledged, and the run fails so the queued run redoes the work. A resume that fails
after the sandbox came back takes the bounded path below.

A steer counts as delivered only when the runtime observes the agent echo that specific message,
matched by the message's own identity rather than by counting — the mailbox lives in the runtime's
config directory, which the agent can write to, so an echo that matches nothing outstanding is
ignored. On pi the identity is the rpc message id: pi's ack carries the id but not the body, so a
matched pi ack shows that pi accepted a message under that id, not that the text it read was the
runner's.

Codex's resume is retried once and then abandoned. A resume can fail for a reason retrying cannot
clear — a thread the session no longer accepts fails identically every time — so a steer whose
resume fails twice is dropped with a warning naming the follow-up run. It is then undelivered, so
nothing acknowledges it, so no receipt claims it, and the queued run redoes the work: the same
fallback every other failure path takes.

On pi, steering and model fallback are exclusive, and fallback wins. A steered pi session and its
mailbox are bound to one launch of pi, and a fallback relaunches pi on the next model. So a pi run
that can fall back is not steerable. It logs one line naming the reason, and its updates go to
the queued run. A pi run with no fallback models steers as described above.

### Why stop, and what it costs

An OpenShell `exec` is not expected to end the processes it started when the caller goes away
([NVIDIA/OpenShell#3159](https://github.com/NVIDIA/OpenShell/issues/3159)). `sandbox stop` is the
released way to end every process in a sandbox while keeping its disk state, which a resume needs,
so it is the Codex interrupt. The stray-process sweep stays only where nothing has to survive it:
between validation iterations (`ClearIterationArtifacts`).

The clean interrupt takes about a second. Over 35 steered runs on OpenShell 0.1.2 with the podman
driver, the stop measured 0.20 to 0.52 seconds and the start 0.37 to 1.66 seconds. In the runs that
resumed, the resumed turn reported its first event 3.1 to 7.9 seconds after the steer.

After the start, the runner re-reads the phase for 2 seconds (`runtime.CodexStartSettleWindow`)
before it resumes. The check exists because of a gateway race. In about 2 of 35 steered runs the
sandbox reported Ready and then, 54 to 91 milliseconds later, entered an Error phase it never
leaves: stop, start and exec are all refused ([NVIDIA/OpenShell#3308](https://github.com/NVIDIA/OpenShell/issues/3308)). The runner detects the flip and fails
closed. `Steer` returns `runtime.ErrSteerSessionLost`, the steer is never acknowledged, and the run
ends failed with an error that wraps the same sentinel, so the queued run redoes the work.

A stop can also be slow. One of the 35 runs waited 30 seconds on a gateway session that a
reconnect had superseded. A process that ignores SIGTERM holds the stop for podman's 45-second
kill grace. The stop is bounded at 60 seconds, the start at 30 and the settle check at 7, so one
interrupt takes at most 97 seconds. The runner holds its sandbox lock for that time, so the
credential refreshers wait it out. The OIDC token is refreshed every 4 minutes and lives 5, so a
refresh can wait 60 seconds before the token expires. A typical interrupt fits in that margin; one
that runs to its bound does not. So the caller that delivers steers refreshes the OIDC token inside
the same hold, immediately before it interrupts; otherwise the token can expire before the delayed
refresh lands. Code that budgets a steered run's settle reserves `runtime.CodexSteerInterruptCost`
(12 seconds) per interrupt.

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
