---
title: "101. Steer the running agent on work-item updates instead of cancelling the run"
status: Accepted
relates_to:
  - security-threat-model
  - operational-observability
  - flapping-convergence
topics:
  - concurrency
  - dispatch
  - runtime
---

<!-- Related work, developed in parallel and not claimed as an implementation of it:
     ADR 0098 (serialize agent runs and coalesce subsequent events),
     open as fullsend#6909; link it as 0098-serialize-agent-runs-and-coalesce-subsequent-events.md
     once that PR merges. -->

# 101. Steer the running agent on work-item updates instead of cancelling the run

Date: 2026-09-03

## Status

Accepted

## Context

[ADR 0098](https://github.com/fullsend-ai/fullsend/pull/6909) proposes
preserve-and-coalesce scheduling: the active run finishes, the execution platform
retains one pending run for the newest matching event, and the next run reconciles
the subject's current state. On GitHub Actions that is a subject-scoped concurrency
group, `cancel-in-progress: false`, and the default single-pending queue.

That ADR is open and under review, and it was written in parallel with this work,
against the same user feedback rather than against each other. This ADR does not claim
to implement it: the two reach for the same first move — a run in flight should not be
thrown away — from different starting points, and where they overlap they should be
reconciled before either merges.

Preserve-and-coalesce stops discarding work, but on its own it leaves two costs
standing. The run in flight finishes on the state it started with and posts output
that is already stale — a review of a commit that no longer exists, which is
[#1207](https://github.com/fullsend-ai/fullsend/issues/1207). The pending run then
does the full job over the same diff, which is the waste behind
[#1014](https://github.com/fullsend-ai/fullsend/issues/1014),
[#4960](https://github.com/fullsend-ai/fullsend/issues/4960),
[#1422](https://github.com/fullsend-ai/fullsend/issues/1422) and
[#6573](https://github.com/fullsend-ai/fullsend/issues/6573). Tokens are saved only
when the active run absorbs the retained event before the pending run starts.

The runner cannot be handed that event. It runs inside a CI job that can only make
outbound calls, and GitHub Actions has no API for delivering input to a running job.
[ADR 0041](0041-synchronous-workflow-call-event-dispatch.md) fixes the shape of the
dispatch chain this has to work within, and
[#1637](https://github.com/fullsend-ai/fullsend/issues/1637) asked for the
concurrency semantics to be written down, which the rest of this ADR does.

## Decision

Preserve the active run and coalesce later events into one pending run, and add an
**opt-in** extension on top of that: while the active run holds the subject, it absorbs
the retained event itself, so the pending run finds the work already done and exits.
With the extension off — the default — the behaviour is preserve-and-coalesce and
nothing more, which is also what ADR 0098 proposes.

### Relationship to ADR 0098's rejected polling option

ADR 0098 rejects "poll for later events within `fullsend run`" because it would
require every input driver to support polling and race-safe cursors, and would move
scheduling and repeated invocation into the execution command. This design is not
that option, and the difference is the thing to check when reviewing it:

- It polls the **execution platform's own run records**, never forge events. There is
  no cursor, no normalization, no ordering guarantee to preserve, and no input driver
  is involved — a follow-up run is only accepted because its own `Route` job already
  ran the normal authorization path.
- It **invokes nothing**. Scheduling stays with the platform: every follow-up event
  still creates its pending run, which preserve-and-coalesce requires. The active run only
  reads what the platform already decided.
- It is **bounded** — `max_steers` per run, and a remaining-time floor below which the
  watcher settles rather than starting a turn it cannot finish.
- It **never extends the active run's timeout**, a point ADR 0098 also makes explicitly. The
  budget is `min(stage timeout, forge token life − margin)` and the run settles inside it.
- Each run still **reconciles the subject's current state**, as ADR 0098 also calls for; a
  steer is a prompt to reconcile sooner, not a substitute for reconciling.

### Concurrency

Every `reusable-dispatch.yml` stage job carries:

```yaml
concurrency:
  group: fullsend-<stage>-${{ github.repository }}-<item>
  cancel-in-progress: ${{ vars.FULLSEND_STEER != 'true' }}
```

**Gated for now.** Unset — the default everywhere — keeps today's cancelling behaviour, and a
consumer opts into `cancel-in-progress: false` together with steering by setting the variable to
`"true"`. The gate exists only because ADR 0098 (fullsend#6909) is not merged at the time of
writing: if it is accepted and preserve-and-coalesce becomes the policy for every agent trigger, the expression
becomes a plain `cancel-in-progress: false`, the variable goes away, and steering stays separately
gated by the harness `steer.enabled` flag. That follow-up removes the mixed state described under
"Rollout order".

`queue: max` is deliberately unused: it is incompatible with
`cancel-in-progress: true`, and N pending full runs is the failure mode
preserve-and-coalesce removes.

### Amendments and context

Provenance authorizes runs, not the text they carry. An accepted run establishes that an authorized
principal caused *something* on this work item; it does not establish that every comment since the
baseline came from that principal. So the delta is split. An item is an **amendment** — an
instruction the agent acts on, taking precedence over its original task — only when its author is
the principal the `Route` job checked. Everything else is **context**: data the agent may read and
must not obey.

That is decidable only for events where the run's actor is by construction the login the arm
checked. The run record carries the event but not the action, so an event qualifies only if *every*
arm handling it checks the login the run reports. Auditing `reusable-dispatch.yml` leaves exactly
one:

| Event | Verdict |
|---|---|
| `issue_comment` | every slash-command arm checks the comment author, who is the run's sender. **Eligible.** |
| `issues` | `opened` and `edited` check the reported actor, but `labeled` with `ready-for-triage` or `ready-for-review` checks nobody and still selects a stage, and the action is invisible in the run record, so the authorized arms cannot be told from the unauthorized ones. Excluded. |
| `pull_request_target` | `opened`, `synchronize` and `ready_for_review` check the PR author while the run's actor is whoever pushed — on a fork PR a different person, who needs no upstream permission at all. `labeled` and `closed` check nobody. Excluded. |
| `pull_request_review` | checks the PR author while the actor is the review submitter, which the arm requires to be the review App. Excluded, and bot actors are filtered regardless. |
| `pull_request_review_comment` | has no arm at all, so every stage job is skipped and check 5 already rejects it. Excluded. |

A push, a label and a closure are state changes rather than instructions, which is the same reason
an issue's title, body and label edits are context. Excluding them costs the agent nothing it
needs: a head move still arrives as context, carrying the new SHA. An authorization also covers
only items that predate the run it came from, so a login that was authorized once does not promote
whatever it writes later.

### The steer contract

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

### Transport: follow-up runs are the requests

Every legitimate update to the work item already fires the repository's shim, and that run's
`Route` job already applied [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)'s authorization. That
run record is a server-side, unforgeable statement of *what ran and when*. It is not a statement of
who was authorized: the actor it reports is the principal the `Route` job checked only for
`issue_comment` (see "Amendments and context" below). So the runner needs
no mailbox, no relay, and no re-implementation of the routing predicate: it polls
`GET /repos/{repo}/actions/workflows/{shim}/runs?created>=<my start>` with the **job token** —
the `GH_TOKEN` the action passed in, which every stage job already grants `actions: write` — and
turns the runs that pass provenance into steers.

A human on a workstation reaches the same transport with `fullsend steer <url> "<text>"`, which
posts a `/fs-steer` comment; the comment fires the shim like any other event. The route logic has
a `/fs-steer` arm beside `/fs-triage`…`/fs-fix` that selects the target stage under the existing
`is_authorized` guard, with the floor of the stage it targets — fix is a mutation stage and keeps
its write floor, so `/fs-steer fix:` cannot reach fix from a triage-level account.

Dispatch is never suppressed while a run is in flight. A route arm that skipped whenever
something was running would lose a steer that lands after the in-flight run's last check.

### Provenance: what the runner verifies

Authorization is not the runner's job — it already happened, once, in the follow-up run's route
job. What the runner verifies is **provenance**, entirely from server-side records the sender
cannot write:

| # | Check | Rejects |
|---|---|---|
| 1 | Same repository | implicit in the API path |
| 2 | `path` is the shim and `event` is a work-item update (`issue_comment`, `issues`, `pull_request_target`, `pull_request_review`, `pull_request_review_comment`) | `push`, `pull_request`, `workflow_dispatch`, and any other workflow |
| 3 | `referenced_workflows` (path and ref) equals my own run's | a foreign or renamed reusable workflow, or one at another ref, by inequality — no version knowledge needed. The sha is not compared: a branch-pinned shim (`@main`, as on this repository) resolves to a new sha whenever the branch advances, which would drop every steer there |
| 4 | The candidate's **`Route` job** concluded `success`, and the run was created after mine started | a run whose `Route` job authorized nobody; a replayed old run. It does **not** establish that the run's reported actor is the authorized one |
| 5 | My stage's job has `conclusion != "skipped"` | a fork author's `/fs-steer`, whose run has every stage job skipped |
| 6 | Bound to my work item: `pull_requests[]`, else the shim's `run-name` as `display_title` | another item's run |
| 7 | Not judged before, by run id | a replay; a re-poll |

Check 4 deliberately **ignores the run's own conclusion**. Under `queue: single`, a later event
cancels the earlier pending stage job and that run concludes `cancelled` — while the
authorization its Route job established still stands. Check 5 counts a null conclusion as
selected: that is the run queued behind me, which is the common case.

`issue_comment` and `issues` runs carry no `pull_requests[]`, so the per-repo shim declares
`run-name: ${{ github.repository }}#${{ github.event.issue.number || github.event.pull_request.number }}`,
which the API returns as `display_title`. For a comment on a PR, `github.event.issue.number` *is*
the PR number, so the pair covers every event the shim listens for. A candidate that matches
neither is skipped rather than guessed at: a wrong binding steers one work item's agent with
another's content.

A run the watcher has *judged* is never re-examined, but only a run whose content actually
reached the agent is recorded as **consumed**. The marker is what the queued run reads to decide
whether to skip its own work, so a candidate that was dropped — an empty delta, a failed
delivery, a runtime that cannot steer — must not look handled.

Every accepted candidate in one poll folds into a **single** steer — the delta is the item's
current state against a baseline, so two comments that arrive together cost one turn, not two,
and both run ids are recorded as consumed.

The delta text is a runner-authored envelope through the same Unicode sanitizer
`buildFeedbackPrompt` uses (now `security.SanitizeAgentText`, shared so the two cannot drift),
delivered through the mailbox, so it never reaches the agent CLI's own argv. It is not out of
argv entirely: the mailbox write is a `printf ... >> mailbox` command string that `sandbox exec`
runs as `sh -c`, so the text is visible in that shell's argv inside the sandbox (to the sandbox
user, which is the agent that is about to read it) and in OpenShell's host-side command preview.
Plumbing the exec request's stdin field through the sandbox package would remove even that; it is
tracked separately. Only non-bot activity counts, so a run never steers itself with its own start
comment.

### The work item's baseline

The watcher asks the forge what the work item is, at startup, rather than reading it from the
job's environment. `PR_HEAD_SHA` is set only on the deprecated per-org dispatch path, so a
per-repo run has neither a head SHA nor any way to tell a pull request from an issue. Guessing
wrong is not cosmetic: an issue-shaped baseline of empty title, body and labels makes every delta
report the whole body as edited and every label as added, forever, so the run never settles and
the agent is handed the same "update" on each steer. A head SHA the environment *does* supply
still wins, because it is the head at run start and that is what a head move must be measured
against.

### Settle

On a turn end — `runtime.ResultEvent`, which Claude's `result`, pi's `agent_end` and Codex's
`turn.completed` all normalize to — the watcher polls once. If something new arrived it steers
and the agent takes another turn; otherwise it settles and the run ends. A steer consumed
mid-turn produces no turn end of its own, so turn ends are a settle signal and are never counted
against the steer budget.

The watcher settles on every exit path, including a cancelled context, on a context of its own —
otherwise `Run` would hold a session open for a watcher that has stopped watching.

### Ceilings

- **Forge token life.** The stage mints a GitHub App installation token at job start; those live
  one hour and the runner has no refresher for them. The budget is
  `min(agent timeout, token life − margin)`, owned by the runner: `internal/runtime` knows
  nothing about forge token life, so deciding it there would put a policy in the wrong layer.
- **Cost.** A steered turn on a large diff can cost as much as a fresh run. `steer.max_steers`
  defaults to 2, which covers the burst patterns in #6573 and #4960; beyond the cap the run
  settles and the queued run does the work.
- **Session files are agent-writable.** A resume reads a session store the agent controls, so a
  poisoned session is a prompt-injection vector into the next turn. It is not a credential leak,
  and the hooks still gate tools ([ADR 0090](0090-runtime-neutral-sandbox-hooks-contract.md)).
  This is documented, not signed.
- **Per-process guards.** pi's config-dir guard, Codex's hook-digest re-assert and Claude's
  `--settings` hooks run once per process. A live steer keeps the process, so they have already
  run and the hooks stay loaded; interrupt-and-resume re-runs them. Neither weakens ADR 0090.

### The skip check

After the run, the terminal status comment carries
`<!-- fullsend:steer consumed=<run_id,...> head=<sha> -->`. It is a **processing
receipt** in the sense of the entity-first evaluation ADR
([fullsend#6956](https://github.com/fullsend-ai/fullsend/pull/6956)): a durable,
App-authored record on the subject of what a run actually handled, which is what
lets a later run decide whether its own trigger is already covered. In `fullsend run`'s pre-flight — before
the start comment and before the pre-script, whose side effects are not free — a queued run reads
the latest **App-authored** marker on the work item and exits 0 without starting the agent when
its own `GITHUB_RUN_ID` is listed.

The check fails open in every direction: no marker, an unreadable timeline, an unresolvable App
login, a malformed run id. A false "already handled" silently drops the work; a false "not
handled" costs one short run. The marker is only honoured from the App login the runner resolved,
since any user can paste the HTML into a comment of their own.

Worst case is one short redundant run — the same window Actions has today, minus the wasted
in-flight tokens.

### The fleet-agent backstop

The runner exports `FULLSEND_RUN_HEAD_SHA` and `FULLSEND_RUN_STARTED_AT` into the sandbox
unconditionally, so an agent definition can re-read the work item once before writing its result.
This is a backstop under the harness steer, not an alternative: the steer is deterministic and
lands during the run, while the re-check depends on the model following the instruction and lands
only at the end. It costs one or two API calls when nothing changed.

Both are written from `bootstrapEnv`, not from `env.sandbox` or an `env/*.env` file: `.env.d`
files are sourced later and would expand the references host-side to empty, and a `${VAR}` in
harness `env.sandbox` hard-fails `ValidateRunnerEnvWith` for consumers that do not define it.

### Configuration

Per-agent, default off, because enabling it changes how long a run holds its VM:

```yaml
steer:
  enabled: true          # default: false
  max_steers: 2          # default: 2
  poll_interval_seconds: 30   # default: 30
```

The runner sets `RunParams.Steerable` only when all of: the harness opted in, the runtime
implements `Steerer`, and the job is a GitHub Actions run. Otherwise `Steerable` stays false and
`Run` is single-turn exactly as today.

### What changes for each stage

Review produces one review per *settled* head rather than per dispatched head; the steered turn
re-diffs A..B in-session, which is the incremental review at zero re-read cost, and the existing
`prior_sha` output becomes the skip key. Fix counts its iteration once at start and treats a
steered continuation as the same iteration, so the queued run's skip check replaces its reliance
on the concurrency group for TOCTOU. Triage receives the new comment or title mid-run, which is
[#1207](https://github.com/fullsend-ai/fullsend/issues/1207) closed — its `needs-info` flips must
be idempotent, which [ADR 0063](0063-polling-based-work-discovery.md) already asks for. Code stops
watching once the branch is pushed, since after the PR exists the update belongs to fix or review.

### Known limits

**Parsers see N results per run.** A steered run emits one `ResultEvent` per turn, so anything
that assumed one result per iteration — the Claude parser's `seenResult`
([#6932](https://github.com/fullsend-ai/fullsend/issues/6932)), `RunMetrics`, the agent span,
`eval-measure` — is now 1:N. `RunMetrics.Steers` records every acknowledged steer, written by
`Run` alone so the watcher's goroutine never races it.

**The prompt-injection surface grows.** The steer text is built from PR bodies, comments and
commit messages, and under `/fs-steer` from an authorized human — the same trust dispatch already
places in that person. The sanitizer and the sandbox hooks remain the controls; an authorized
human pasting attacker-supplied text is still an injection, and this design does not change that.

**The sandbox checkout is not refreshed.** It stays a snapshot of the head the run started on,
because refreshing it from the runner would clobber uncommitted work for the fix and code stages,
which write to that tree. On a head move the envelope names the new SHA and tells the agent to
fetch it with the forge token it already holds; a runner-side refresh for read-only stages is a
possible follow-up, not part of this decision.

**GitLab is not wired.** GitLab pipelines already queue rather than cancel, and the provenance
join is different — `GET /pipelines/:id/variables` exposes the poller-set `STAGE` and
`RESOURCE_KEY`, already covered by the HMAC dispatch signature. The watcher is GitHub-only for now
and says so when it declines to start.

**A steer needs time left.** The exec hosting a live session cannot be extended once running, so
the watcher settles rather than steering when less than `MinRemaining` (default five minutes) of
the run budget remains, and the update falls to the queued run.

### Rollout order

**Precondition: steering may not be enabled anywhere until receipts are authenticated by a channel
that agents and post-scripts cannot mint.** Scoping the receipt to a body carrying the status
markers is not that channel: it authenticates two public strings rather than the writer, and the
runner's status comments and the agent's own output are posted under the same App identity, so an
agent induced to emit those strings — through a post-script shelling out to `gh`, which reaches
none of the runner's sanitizing paths — produces a receipt that passes. A forged receipt makes the
queued run exit without doing its work, so the failure is a silently dropped update rather than a
wasted one. Closing it needs authenticity the agent cannot produce: a status-only credential
withheld from the sandbox, or a receipt the runner signs.

The receipt is load-bearing rather than an optimization. Without one, steering costs *more* than
cancelling does today: the active run absorbs the push and reviews head B, then the queued run
reviews head B again — two reviews where cancel-and-restart produces one. So the skip check and
the authenticity it depends on ship together, or neither ships.

Once that holds, the two switches must still be flipped in order, because the mixed state
`FULLSEND_STEER=true` without `steer.enabled` is worse than today: the run in flight finishes on
stale input and posts stale output, and the run queued behind it does the full work again with no
marker to skip on. So: merge this change, enable `steer:` in the fleet harnesses, and only then
set the repository variable, one repository at a time, after one real steer of each runtime has
been observed.

## Consequences

- A burst of events on one work item produces one agent run that absorbs them plus at most one
  short follow-up, instead of a cancelled run and a full re-run per event — but only once the
  receipt is authenticated, since the follow-up is only short if it can trust a receipt to skip on.
- Agents stop posting output computed from state the subject has already moved past, which is the
  complaint in [#1207](https://github.com/fullsend-ai/fullsend/issues/1207).
- The runner gains a dependency on the execution platform's run records and its per-stage
  `actions: write` grant, and steering is unavailable on any platform that exposes neither.
- A run now holds its sandbox until it settles rather than ending at its first result, so a
  steered run occupies a VM longer and can cost as much again per absorbed update.
- Nothing changes for a repository that does not opt in, and the fallback in every failure path —
  no ack, no time left, cap reached, runtime cannot steer — is plain preserve-and-coalesce.

Related: [#5445](https://github.com/fullsend-ai/fullsend/issues/5445) and
[#2388](https://github.com/fullsend-ai/fullsend/issues/2388) — `/fs-cancel` gains a second
implementation as a "stop" verb on this arm; [#2399](https://github.com/fullsend-ai/fullsend/issues/2399) —
the watcher replaces stale-head re-dispatch;
[#459](https://github.com/fullsend-ai/fullsend/issues/459) — the session-id capture this needs is
the same one a local resume needs.
