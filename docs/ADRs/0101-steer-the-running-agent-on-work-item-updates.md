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

# 101. Steer the running agent on work-item updates instead of cancelling the run

Date: 2026-09-03

## Status

Accepted

## Context

Every stage job in `reusable-dispatch.yml` declares `cancel-in-progress: true`. When a second
event lands on the same work item while an agent is running — a push during a review, a comment
during triage — the run in flight is killed and a fresh one starts from nothing. Everything the
first run read is thrown away, and the same diff is read again from scratch.

The complaints this produces have been filed repeatedly and from both directions: as wasted
dispatches on a burst of pushes ([#1014](https://github.com/fullsend-ai/fullsend/issues/1014),
[#4960](https://github.com/fullsend-ai/fullsend/issues/4960),
[#1422](https://github.com/fullsend-ai/fullsend/issues/1422),
[#6573](https://github.com/fullsend-ai/fullsend/issues/6573)), and as an agent missing an update
that arrived while it worked ([#1207](https://github.com/fullsend-ai/fullsend/issues/1207)).
[#1637](https://github.com/fullsend-ai/fullsend/issues/1637) asked for the concurrency and cancel
semantics to be written down after ADR 0041; this ADR is that content.

**Flipping `cancel-in-progress` alone does not help.** GitHub Actions allows at most one pending
run per concurrency group by default (`queue: single`); a newer queued run replaces the pending
one, and `cancel-in-progress: true` additionally kills the running one. With the flag off and
nothing else changed, the run in flight finishes on the stale head and posts stale output — a
review of a commit that no longer exists, which is exactly #1207 — and the pending run then does
the full job anyway. Tokens are saved only when the run in flight *absorbs* the change and the
queued run skips work already covered. Those are two separate changes, and both are needed.

`queue: max` is not the answer either: it is incompatible with `cancel-in-progress: true`, and
N pending full runs is the failure mode this design removes. `queue: single` is the right
primitive — exactly one pending run, always representing the latest event, holding no VM.

The runner has no inbound path. It runs inside a CI job that can only make outbound calls, and
GitHub Actions has no API for delivering input to a running job.

## Decision

A run in flight absorbs work-item updates through a **follow-up run watcher** in the runner, and
the run queued behind it **skips work the settled run already covered**. The concurrency flip
that makes this possible is gated on a repository variable and ships inert.

### Concurrency

Every `reusable-dispatch.yml` stage job carries:

```yaml
concurrency:
  group: fullsend-<stage>-${{ github.repository }}-<item>
  cancel-in-progress: ${{ vars.FULLSEND_STEER != 'true' }}
```

Unset — the default everywhere — is today's behaviour byte for byte. Set to `"true"` in a
consumer repository, the run in flight survives and the newer event waits as the single pending
run `queue: single` allows. The per-role grouping is unchanged: review dispatches still do not
cancel triage.

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
run record is a server-side, unforgeable statement of *who asked for what*. So the runner needs
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
| 4 | The candidate's **`Route` job** concluded `success`, and the run was created after mine started | an unauthorized actor; a replayed old run |
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
delivered via the mailbox and never via argv. Only non-bot activity counts, so a run never steers
itself with its own start comment.

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
`<!-- fullsend:steer consumed=<run_id,...> head=<sha> -->`. In `fullsend run`'s pre-flight — before
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

## Consequences

**What each stage's contract becomes.** Review produces one review per *settled* head rather than
per dispatched head; the steered turn re-diffs A..B in-session, which is the incremental review
at zero re-read cost, and the existing `prior_sha` output becomes the skip key. Fix counts its
iteration once at start and treats a steered continuation as the same iteration, so the queued
run's skip check replaces its reliance on the concurrency group for TOCTOU. Triage receives the
new comment or title mid-run, which is #1207 closed — its `needs-info` flips must be idempotent,
which ADR 0063 already asks for. Code stops watching once the branch is pushed, since after the
PR exists the update belongs to fix or review.

**Parsers see N results per run.** A steered run emits one `ResultEvent` per turn. Anything that
assumed one result per iteration — the Claude parser's `seenResult`
([#6932](https://github.com/fullsend-ai/fullsend/issues/6932)), `RunMetrics`, the agent span,
`eval-measure` — is now 1:N. `RunMetrics.Steers` records every delivered steer, written by `Run`
alone so the watcher's goroutine never races it.

**Prompt injection surface grows.** The steer text is built from PR bodies, comments and commit
messages, and under `/fs-steer` from an authorized human. That is the same trust dispatch already
places in that person; the sanitizer and the sandbox hooks remain the controls. An authorized
human pasting attacker-supplied text is still an injection, and this design does not change that.

**The sandbox checkout is not refreshed.** It is a snapshot of the head the run started on.
Refreshing it from the runner would clobber uncommitted work for the fix and code stages, which
write to that tree, so on a head move the envelope names the new SHA and tells the agent to fetch
it with the forge token it already holds. A runner-side refresh for read-only stages is a
possible follow-up, not part of this decision.

**GitLab is not wired.** GitLab pipelines already queue rather than cancel, and its provenance
join is different — `GET /pipelines/:id/variables` exposes the poller-set `STAGE` and
`RESOURCE_KEY`, already covered by the HMAC dispatch signature. The watcher is GitHub-only for
now and says so when it declines to start.

**A steer that lands after the run settled is not consumed.** The queued run does the work, and
its skip check finds no marker for it. This is the residual race, and it is bounded by one run.

**Rollout is inert, and the two switches must be flipped in order.** Nothing changes until a
consumer sets `FULLSEND_STEER=true` *and* an agent's harness sets `steer.enabled`. Repositories
that do neither keep today's cancelling behaviour. The mixed state `FULLSEND_STEER=true` without
`steer.enabled` is worse than today: the run in flight finishes on stale input and posts stale
output, and the run queued behind it does the full work again with no marker to skip on. So the
order is: merge this change, enable `steer:` in the fleet harnesses (fullsend-ai/agents), and
only then set the repository variable, one repository at a time, after one real steer of each
runtime has been observed on OpenShell.

**A steer needs time left.** The exec that hosts a live session cannot be extended once running,
so the watcher settles instead of steering when less than `MinRemaining` (default five minutes)
of the run budget is left; the update falls to the queued run.

Related: [#5445](https://github.com/fullsend-ai/fullsend/issues/5445) and
[#2388](https://github.com/fullsend-ai/fullsend/issues/2388) — `/fs-cancel` gains a second
implementation as a "stop" verb on this arm; [#2399](https://github.com/fullsend-ai/fullsend/issues/2399) —
the watcher replaces stale-head re-dispatch;
[#459](https://github.com/fullsend-ai/fullsend/issues/459) — the session-id capture this needs is
the same one a local resume needs.
