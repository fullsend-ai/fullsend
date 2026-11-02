---
title: "118. Steer runtime interface and in-sandbox delivery"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - runtime
  - sandbox
  - concurrency
---

# 118. Steer runtime interface and in-sandbox delivery

Date: 2026-11-02

## Status

Accepted

Companion decisions:

- ADR 0117 (proposed): when a steer happens and which records it carries.
- Steer envelope v1 (proposed with ADR 0117): the steer files and the message
  format.

## Context

ADR 0117 (proposed) decides that a running sandbox cycle takes in admitted
entity updates. This ADR decides how those steer messages reach the agent and
what a runtime must implement to be steerable. Background on agent structure
and trust is in [agent architecture](../problems/agent-architecture.md) and
the [security threat model](../problems/security-threat-model.md).

Two constraints shape this design:

- **The sandbox.** The runner reaches a running sandbox only through `exec`
  and `upload`. A new `exec` gets its own stdin, not the stdin of the agent
  process already running.
- **The runtimes differ.** Claude Code and pi can accept messages into a live
  session. Codex (`codex exec`) runs non-interactively, with no open input
  channel.

This ADR aims to give every runtime one interface for steering, with a
transport that needs no new sandbox operation for live runtimes.

## Options

| Option | Why not chosen |
|---|---|
| Restart the agent with the update added to its prompt | Throws away the turn in progress and the session history; Codex resume also drops the turn but keeps the history |
| `upload` a file and hope the agent notices | Nothing tells a live session that a file arrived |
| Write to the agent's stdin through `exec` | An `exec` has no handle on the running process's stdin |
| **A mailbox the agent's stdin already tails** (chosen) | — |

## Decision

We adopt an optional `runtime.Steerer` capability, fed through an in-sandbox
mailbox, to let any runtime become steerable by implementing two methods.

### Interface

| Method or field | Contract |
|---|---|
| `RunParams.Steerable` | Asks a `Steerer` runtime to keep its session open. A runtime that is not a `Steerer` ignores it, and its command line is unchanged |
| `Steer(ctx, msg, locked)` | Delivers one envelope (steer envelope v1, proposed with ADR 0117) and returns once the runtime has observed its acknowledgement, or an error. The runtime performs its in-sandbox steer action only inside `locked`, a runner-supplied callback that runs under the sandbox write lock, and waits for the acknowledgement after `locked` returns |
| `Settle(ctx, locked)` | Closes the session to further steers: waits outside `locked` for the agent's current turn to finish, then closes the agent's input inside `locked` (a no-op on Codex). `Run` returns once that turn and `Settle` complete |

The `msg` parameter carries the envelope bytes, the steer sequence number, the
admitted record keys, and any provisioned `head.sha`. The runtime does not
change the envelope text; it only wraps it in its own input framing (see
Mailbox).

**Not steerable this cycle.** A `Steerer` that finds at cycle start that it
cannot steer, for example because it cannot keep the mailbox agent-immutable,
returns `ErrNotSteerable` from `Steer` before publishing anything. The runner
then treats the cycle as not steerable, and ADR 0117 step 1 declines every
later attempt in that cycle without fetching or filtering records.

### Runtimes

| Runtime | Steerable | Delivery |
|---|---|---|
| Claude Code | Yes, once the dependencies below hold | Stream-JSON input and output with `--replay-user-messages`, fed by the mailbox |
| pi | Yes, once the dependencies below hold | RPC mode on stdin, fed by the mailbox; each steer is a `prompt` command with `streamingBehavior: "steer"`, which starts a turn when idle and steers an active one |
| Codex | Yes, once the dependencies below hold | [Interrupt and resume](#codex-interrupt-and-resume) |
| OpenCode | No | Declares itself not steerable |
| dummy, dummy-playback | No | Declare themselves not steerable |

### Mailbox

- A live runtime starts the agent with stdin connected to an in-sandbox
  feeder that tails a mailbox file from its first line. The opening prompt
  and every later steer use the same channel.
- `Steer` appends one mailbox entry through `exec`: the envelope wrapped by
  the runtime in its own input framing as one complete JSON line (a Claude
  Code stream-JSON user message, or a pi RPC `prompt` command with
  `streamingBehavior: "steer"`). The feeder passes lines through unchanged.
- The mailbox sits in a runner-owned directory that the agent cannot write,
  under the same immutability rule as the steer directory (steer envelope
  v1). If that cannot be enforced, the runtime reports itself not steerable
  for the cycle.
- The mailbox is only a transport: admission and filtering happen on the host
  before anything is written, and the envelope carries no record text.
- **One trusted writer.** The mailbox, the steer files, and a moved head's
  objects need an in-sandbox writer the agent cannot impersonate (an ADR 0117
  dependency). Until the sandbox provides one, no runtime is steerable.
- **Input-channel integrity.** No sandbox process other than the feeder may
  write the agent's input channel. A message injected there would be read as
  a runner-admitted steer, which carries more weight than tool output. The
  runtime verifies this isolation before it reports itself steerable for a
  cycle; if the sandbox cannot keep other processes off the agent's input,
  the runtime reports itself not steerable.

### Codex interrupt and resume

Codex `exec` has no live channel, so the runner stops and restarts the
sandbox, resuming the same session with the envelope as the next prompt. The
stop, the start, and the phase check are Codex's in-sandbox steer action, so
they run inside `locked`. If the restarted sandbox reaches an Error phase
([NVIDIA/OpenShell#3776](https://github.com/NVIDIA/OpenShell/issues/3776),
[#3308](https://github.com/NVIDIA/OpenShell/issues/3308)), the steer is lost
and the cycle fails closed: it fails instead of continuing without the steer.

### Caller obligations

The runner, not the runtime, owns these rules:

- **Write lock.** The runner holds its sandbox write lock for all of
  `locked`, so no step can race the credential refreshers. Inside `locked`
  the runner runs the release check, writes the steer directory, installs any
  moved head (ADR 0117, proposed), and then runs the runtime's in-sandbox
  steer action. If the release check or the head install fails, `locked`
  returns an error without running the action. The lock is released as soon
  as `locked` returns, before `Steer` waits for the acknowledgement, so a long
  agent turn never delays a credential refresh.
- **Deadline under the lock.** Everything inside `locked`, including the
  Codex stop, start, and phase check, runs under one deadline. The runner
  first refreshes short-lived credentials inside `locked`, then sets the
  deadline to fit within the remaining lifetime of every credential the
  refreshers maintain, with a safety margin. If the deadline cannot be met,
  the runner declines the steer before the action starts; if the action
  overruns it, the cycle fails closed.
- **Acknowledgement.** The runner treats a steer as delivered only when the
  runtime sees the agent consume that specific message, matched by its
  identity rather than by counting. Accepting it into a queue is not delivery.
  Each runtime meets this rule its own way:
  - Claude Code, launched with `--replay-user-messages`, replays the consumed
    user message; the runtime matches the replay to the steer by message
    identity. Claude Code counts as steerable only once tests show the replay
    marks consumption, not queueing.
  - pi acknowledges only with a later session event whose user message
    carries the envelope's identity (its `Steer:` and `Manifest:` header
    lines, unique per steer), not with the RPC response that queues it,
    because pi session events do not carry the RPC ID.
  - Codex acknowledges only when the resumed session's rollout file records
    a user message carrying the envelope's `Steer:` and `Manifest:` lines.
    The `exec --json` stream's `turn.started` event carries no prompt, so it
    is never an acknowledgement. Inside `locked` the runner stops, starts,
    and checks the sandbox phase; it launches the resume and reads the
    rollout after `locked` returns. A resume that fails before that record,
    for example on an integrity guard, is a failure after publication. Codex
    counts as steerable only once tests show the rollout record appears when
    the resumed turn consumes the envelope.
- **Errors before and after publication.** An error before `locked` publishes
  anything, such as a failing release check, a settled session, or
  `ErrNotSteerable`, leaves the cycle as if no steer was attempted (ADR 0117,
  proposed). A failure or timeout after publication begins and before the
  acknowledgement makes `Steer` return an error, never a delivery, and ADR
  0117 treats it as a possibly delivered steer.
- **Budget.** A steer starts only when the remaining time, bounded by the
  forge token's lifetime, covers a full turn and the
  [ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md) reserve for
  publication and cleanup.
- **Cap.** A cycle takes no more steers than its configured steer cap.

## Forge scope

Forge-neutral. The interface is runtime-level and does not call a forge. The
runner calls `Steer` wherever ADR 0117 (proposed) steers a running cycle.
ADR 0117 states which forges and entities are excluded, and why.

## Consequences

- A runtime becomes steerable by implementing two methods, with no
  per-runtime branching in the runner beyond the capability check.
- Live runtimes need no new sandbox operation beyond the trusted writer.
- The Codex path costs a sandbox stop, start, and session resume per steer,
  holds the write lock for that time, and re-runs per-process guards on each
  resume.
- A Codex resume reads a session store the agent controls, which widens the
  prompt-injection surface while tools stay gated by
  [ADR 0090](0090-runtime-neutral-sandbox-hooks-contract.md).
- On pi the acknowledgement proves the agent consumed a message carrying the
  envelope's identity.
