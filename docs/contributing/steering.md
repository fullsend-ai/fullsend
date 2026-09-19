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
- [Amendments and context](#amendments-and-context) — which half of a delta may instruct the agent
- [The steer contract](#the-steer-contract) — `runtime.Steerer`, and the caller's lock obligation
- [Transport](#transport) — how an update reaches the running session, and the bound on retrying one
- [Provenance: what the runner verifies](#provenance-what-the-runner-verifies) — the seven checks
- [The work item's baseline](#the-work-items-baseline)
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

## Amendments and context

Provenance authorizes runs, not the text they carry: an accepted run proves an authorized
principal caused *something* on the item, not that every comment since the baseline is theirs. So
the delta is split. An **amendment** — an instruction the agent acts on, over its original task —
is the one comment the `Route` job evaluated. Everything else is **context**: data the agent may
read and must not obey.

The run record names the actor and the run's creation instant, not the comment. A run whose
`run-name` carries the comment id (`comment:<id>`, see [Provenance](#provenance-what-the-runner-verifies))
binds to that comment **exactly**. Otherwise the comment precedes the run's creation (a real
`/fs-retro` at 14:06:16Z produced its run at 14:06:19Z) and the pairing is **by interval**: walking
that login's runs in creation order — *every* run the watcher has observed, accepted or rejected,
so a command whose run the `Route` job refused keeps that run and stays context — a run binds only
when exactly one unclaimed stage command by that login lies after the login's previous run and
up to this one. Two commands there (a burst, or a run that trailed its command past the next one)
read the same as an inversion in which a refused command's run comes after an accepted one's, so
the run binds nothing and its commands stay context; a run created more than ten minutes after
its command is not that comment's run either. Every authorized run left without a comment is
unbound and the queued run redoes its work. The text must be unchanged since: a
comment edited after the run was created is context, and its run is **unbound** — not counted as
consumed, because nothing reached the agent under its authority. The actor is the run's `actor`;
`triggering_actor` names whoever caused the latest attempt, so a re-run (`run_attempt > 1`)
confers nothing. A review is never an amendment.

That is decidable only for events where the run's actor is by construction the login the arm
checked. The run record carries the event but not the action, so an event qualifies only if *every*
arm handling it checks the login the run reports. Auditing `reusable-dispatch.yml` leaves exactly
one:

| Event | Verdict |
|---|---|
| `issue_comment` | every slash-command arm checks the comment author, who is the run's sender. **Eligible.** |
| `issues` | `opened` and `edited` check the reported actor, but `labeled` with `ready-for-triage` or `ready-for-review` checks nobody and still selects a stage, and the action is invisible in the run record, so the authorized arms cannot be told from the unauthorized ones. Excluded. |
| `pull_request_target` | `opened`, `synchronize` and `ready_for_review` check the PR author while the run's actor is whoever pushed — on a fork PR a different person, who needs no upstream permission at all. `labeled` and `closed` check nobody. Excluded. |
| `pull_request_review` | checks the PR author while the actor is the review submitter, which the arm requires to be the review App. Excluded: a review reaches the agent as context at most. |
| `pull_request_review_comment` | has no arm at all, so every stage job is skipped and check 5 already rejects it. Excluded. |

A push, a label and a closure are state changes, not instructions — the same reason an issue's
title, body and label edits are context. A head move still arrives as context: the new SHA, and
as the first item of the context block what changed, read from the forge's compare API because
the agent may not fetch in the sandbox. The change is shown only when the comparison is complete
(the new head is simply ahead and fewer than 300 files came back); a force-push, a listing at the
cap or a forge error is reported as a move that could not be read, and the agent goes to the pull
request itself. Patches are bounded, whole files at a time, and the cut is stated.

### Whose text is context

Everything on the work item is context except fullsend's own output, and that exclusion never
reads the shape of a login: a user account can be named `fullsend-ai-review` or end in `-bot`,
and the old `[bot]` rule also threw away the reviews of Apps the repository installed. `ownOutput`
in `delta.go` applies two rules:

- **Exact login — the primary rule.** `Config.SelfLogins` holds the logins the runner resolved at
  start: its own App login from the forge token, and the review App logins as
  `reusable-dispatch.yml` builds `REVIEW_BOT` and `SHARED_REVIEW_BOT` (`steerwatch.ReviewBotLogins`,
  pinned to the workflow file by a test). `Start` refuses an empty list, because the post-fix and
  post-code comments carry no marker and only the login keeps them out.
- **App plus marker — the supplement.** A body carrying a `<!-- fullsend:` marker whose author the
  forge reports as an App (`user.type == "Bot"`, `forge.IssueComment.AuthorIsApp`) is the runner's
  own under any login. This keeps out the processing receipt, posted as `github-actions[bot]`.

A human is never own output, so an authorized `/fs-fix` that quotes a status comment is still an
amendment. A miss leaves an App's text in context, and no rule moves an author toward amendments;
the one gap is a fullsend App the runner did not name posting without a marker, which enters
context like any other App's comment.

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

## Provenance: what the runner verifies

Every legitimate update to the work item already fires the repository's shim, and that run's
`Route` job already applied
[ADR 0054](../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)'s authorization. The
run record is a server-side, unforgeable statement of *what ran and when* — not of who was
authorized: its actor is the principal the `Route` job checked only for `issue_comment` (see
[Amendments and context](#amendments-and-context)). So the runner re-implements no routing
predicate and calls no permission API. It verifies **provenance**, entirely from records the
sender cannot write:

| # | Check | Rejects |
|---|---|---|
| 1 | Same repository | implicit in the API path |
| 2 | `path` is the shim and `event` is a work-item update (`issue_comment`, `issues`, `pull_request_target`, `pull_request_review`, `pull_request_review_comment`) | `push`, `pull_request`, `workflow_dispatch`, and any other workflow |
| 3 | `referenced_workflows` (path and ref) equals my own run's | a foreign or renamed reusable workflow, or one at another ref, by inequality — no version knowledge needed. The sha is not compared: a branch-pinned shim (`@main`, as on this repository) resolves to a new sha whenever the branch advances, which would drop every steer there. This trusts the ref exactly as far as it is protected; a shim pinned to an unprotected ref extends it to whoever can push there |
| 4 | The candidate's **`Route` job** concluded `success`, and the run was created after mine started | a run whose `Route` job authorized nobody; a replayed old run. It does **not** establish that the run's reported actor is the authorized one |
| 5 | My stage's job has `conclusion != "skipped"` | a fork author's stage command, whose run has every stage job skipped |
| 6 | Bound to my work item: `pull_requests[]`, else the work-item half of the shim's `run-name` as `display_title` (its optional `comment:<id>` suffix is read by the binding, not by this check) | another item's run |
| 7 | Not judged before, by run id | a replay; a second look at the same listing |

Check 4 deliberately **ignores the run's own conclusion**. Under `queue: single`, a later event
cancels the earlier pending stage job and that run concludes `cancelled` — while the
authorization its Route job established still stands. A Route job still running is "not yet",
judged again on the next poll; a *completed* run with no `Route` job at all is rejected for good,
since the shim's own `if` skipped the event and the reusable workflow never ran — every status
comment the runner posts fires exactly such a run. Check 5 counts a null conclusion as selected:
that is the run queued behind me, which is the common case.

`issue_comment` and `issues` runs carry no `pull_requests[]`, so they bind only through the
run's `display_title`, which is the shim's `run-name`. The contract this package expects is
`run-name: ${{ github.repository }}#${{ github.event.issue.number || github.event.pull_request.number }}`,
optionally followed by ` comment:${{ github.event.comment.id }}` on an `issue_comment` run — a
single space, lowercase `comment:`, the bare decimal id, last in the title — which lets the
binding pair that run with its comment exactly instead of by timestamp. For a comment on a PR,
`github.event.issue.number` *is* the PR number, so the pair covers every event the shim listens
for. The shim gains both with the runner wiring that starts the watcher; a shim without a
`run-name` binds pull-request events only. A candidate that matches neither is skipped rather
than guessed at: a wrong binding steers one work item's agent with another's content.

A run the watcher has *judged* is never re-examined, but only a run whose content reached the
agent is recorded as **consumed**: the consumed set is what tells a later run its event was
handled, so a dropped candidate — an empty delta, a failed delivery, a runtime that cannot steer —
must not look handled. Every accepted candidate in one batch folds into a **single** steer: the
delta is the item's current state against a baseline, so two comments that arrive together cost
one turn, and both run ids are recorded as consumed.

The envelope's first line is a cross-repo interface with
[fullsend-ai/agents](https://github.com/fullsend-ai/agents), which matches on it twice: to
recognise a runner amendment, and to flag the same line appearing *inside* work-item content as
an injection attempt. It is, byte for byte:

```text
Runner update: your task inputs changed after this run started.
```

In this repository it is the exported constant `runtime.SteerEnvelopeOpeningLine`, written once
and pinned by a test. That line is deliberately *not* defanged inside work-item content: its
appearing there is the injection signal the agent definitions act on, and rewriting it would
delete the evidence. The rest of the envelope's structure — the two section headings, the
amendment prefix, and the fence around the untrusted block — carries only authority when a
stranger writes it, so a context body carrying any of them is defanged before it is wrapped —
in any letter case, including spellings that only become those tokens under NFKC folding, and nothing is scanned
again once the block is assembled, since a folding pass over the whole envelope is exactly what
would turn such a spelling into a live token.

The delta text goes through the same Unicode sanitizer `buildFeedbackPrompt` uses
(`security.SanitizeAgentText`, shared so the two cannot drift) and is delivered through the
mailbox, so it never reaches the agent CLI's own argv. It is not out of argv entirely: the mailbox
write is a `printf ... >> mailbox` command that `sandbox exec` runs as `sh -c`, so the text is
visible in that shell's argv inside the sandbox and in OpenShell's host-side command preview;
plumbing the exec request's stdin through the sandbox package would remove that and is tracked
separately. Fullsend's own output is excluded before any of this
([whose text is context](#whose-text-is-context)), so a run never steers itself.

## The work item's baseline

The watcher asks the forge what the work item is at startup rather than reading the job's
environment: a per-repo run's environment carries neither a head SHA nor a way to tell a pull
request from an issue (only the deprecated per-org path sets `PR_HEAD_SHA`). Guessing wrong is not
cosmetic — an issue-shaped baseline of empty title, body and labels reports the whole body as
edited and every label as added on every delta, so the run never settles. A head SHA the
environment *does* supply still wins: it is the head at run start, which is what a head move is
measured against.

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
