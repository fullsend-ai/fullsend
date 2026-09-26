# Steering a run in flight

How a fullsend run already working on a work item absorbs an update to that
item — a push, a comment, a stage command — rather than leaving it for the run
queued behind it.

This page is the contributor reference for the mechanics.
[ADR 0113](../ADRs/0113-steer-the-running-agent-on-work-item-updates.md) is the
decision to steer at all, and
[ADR 0117](../ADRs/0117-steer-interface-in-sandbox-mailbox.md)
decides the interface described here, and
[ADR 0119](../ADRs/0119-learn-of-later-events-by-listing-run-records.md) decides
how a run learns there is an update to absorb. The byte-level envelope the agent
receives is a versioned contract of its own, in
[normative/steer-envelope/v1](../normative/steer-envelope/v1/README.md).

On this page:

- [Concurrency](#concurrency) — the two switches, and why steering needs both
- [Amendments and context](#amendments-and-context) — which half of a delta may instruct the agent
- [The steer contract](#the-steer-contract) — `runtime.Steerer`, and the caller's lock obligation
- [Transport](#transport) — how an update reaches the running session, and the bound on retrying one
- [Provenance: what the runner verifies](#provenance-what-the-runner-verifies) — the seven checks
- [The work item's baseline](#the-work-items-baseline)
- [Settle](#settle) — how a steered run ends
- [Ceilings](#ceilings) — token life, cost, and the per-process guards
- [The skip check](#the-skip-check) — the receipt a queued run reads, and who may write one
- [The fleet-agent backstop](#the-fleet-agent-backstop)
- [Configuration](#configuration)
- [Known limits](#known-limits)

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

A `Steerer` that refuses some runs outright also implements `runtime.SteerDecliner`. The runner
asks `SteerDeclineReason` before it starts the watcher, so the refusal is announced once and no
poll or steer slot is spent discovering it. Pi uses it for a run that can fall back across models.

Both methods are called **with `sandboxMu` held**. They write into the sandbox — a mailbox
append, or on Codex the sandbox stop and start that interrupt the turn — and would otherwise race
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
rpc channel. Codex exec has no live channel. The runner stops the sandbox, which ends every process
in it (the codex turn included) and keeps its disk state. It starts the sandbox again and resumes
the same session with the message as the next prompt. When the sandbox stops, the turn's `exec`
returns the relay-closed exit. The runner marked the turn before stopping, so that exit counts as
its own interrupt: not an agent failure, and not a failed resume. If the stop fails, the runner
prints a warning and the steer stays queued. The steer is then delivered when the current turn ends
on its own, and falls to the queued run only if the run is stopped first, by an error or by its
budget running out. If the start fails, the resume fails and takes the bounded path below.

A steer counts as delivered only when the runtime observes the agent echo that specific message,
matched by the message's own identity rather than by counting — the mailbox lives in the runtime's
config directory, which the agent can write to, so an echo that matches nothing outstanding is
ignored.

Codex's resume is retried once and then abandoned. A resume can fail for a reason retrying cannot
clear — a thread the session no longer accepts fails identically every time — so a steer whose
resume fails twice is dropped with a warning naming the follow-up run. It is then undelivered, so
nothing acknowledges it, so no receipt claims it, and the queued run redoes the work: the same
fallback every other failure path takes.

On pi, steering and model fallback are exclusive, and fallback wins. A steered pi session and its
mailbox are bound to one launch of pi, and a fallback relaunches pi on the next model. So a pi run
that can fall back is not steerable: the runner declines it before its watcher starts, logs one
line naming the reason, and its updates go to the queued run. A pi run with no fallback models steers as described above.

The runner learns there is something to send by listing the platform's own records rather than by
being told: it polls `GET /repos/{repo}/actions/workflows/{shim}/runs?created>=<my start>` with
the **job token** — the `GH_TOKEN` the action passed in, which every stage job already grants
`actions: write` — and turns the runs that pass provenance into steers. No mailbox from outside,
no relay, and no re-implementation of the routing predicate.

A human on a workstation reaches the same transport with `fullsend steer <url> "<text>"`, which
posts the stage's own slash command — `/fs-review` for a pull request, `/fs-triage` for an issue,
or `--stage` to choose — and the comment fires the shim like any other event. There is no
steer-specific command: the watcher accepts follow-up runs on provenance alone, never on which
words the comment opened with. The existing arms already carry the right floor for each stage,
`/fs-fix` keeping the write floor that makes it a mutation stage.

Dispatch is never suppressed while a run is in flight. A route arm that skipped whenever
something was running would lose a steer that lands after the in-flight run's last check.

### Why stop, and what it costs

An OpenShell `exec` is not expected to end the processes it started when the caller goes away
([NVIDIA/OpenShell#3159](https://github.com/NVIDIA/OpenShell/issues/3159)). `sandbox stop` is the
released way to end every process in a sandbox while keeping its disk state, which a resume needs,
so it is the Codex interrupt. The stray-process sweep stays only where nothing has to survive it:
between validation iterations (`ClearIterationArtifacts`).

The cost is time. On the pinned OpenShell 0.0.116 with the podman driver, every stop waits out the
driver's full 45-second grace
([NVIDIA/OpenShell#2855](https://github.com/NVIDIA/OpenShell/issues/2855)). Stop and start together
measured about 46 seconds, paid once per steer, and the two steps are bounded at 120 seconds
together. The runner holds its sandbox lock for that time, so the credential refreshers wait it
out. The OIDC token is refreshed every 4 minutes and lives 5, so a refresh can wait 60 seconds
before the token expires. A typical interrupt fits in that margin; one that runs to its bound does
not. So the caller that delivers steers refreshes the OIDC token inside the same hold, immediately
before it interrupts; otherwise the token can expire before the delayed refresh lands. The runner
meets that obligation: a codex steer fetches a fresh OIDC token first and uploads it inside the
same lock hold, just before the stop, so the interrupt starts on a new token. The OpenAI credential
needs no such step: it is rotated at least 5 minutes before it expires. The fix
([NVIDIA/OpenShell#3036](https://github.com/NVIDIA/OpenShell/pull/3036)) ships in OpenShell
v0.1.0; a stop measured 0.25 to 0.48 seconds on the v0.1.0-pre.12 build. fullsend's CI still pins
0.0.116, so the 45-second cost holds until a separate bump moves the pin, and goes away with it.
Code that budgets a steered run's settle reserves `runtime.CodexSteerInterruptCost` per
interrupt.

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

A pull request's baseline is its head SHA and its labels, read from the issue record GitHub keeps
for every pull request. Labels count on both kinds of item: a label added to a pull request
mid-run reaches the agent as `Labels changed: added …`, the same state context an issue's does,
rather than leaving an accepted `labeled` follow-up with an empty delta.

## Settle

On a turn end — `runtime.ResultEvent`, which Claude's `result`, pi's `agent_settled` (on a
steerable run) and Codex's `turn.completed` all normalize to — the watcher polls once. A Codex
turn the runner interrupted emits no result; the resumed turn's result is its turn end. If something
new arrived it steers and the agent takes another turn.

It settles only when that poll finds nothing conclusively new. A poll that reached no verdict
leaves the session open for the next tick: a listing that failed transiently, and a candidate
whose Route or stage job has not finished, are both runs the watcher deliberately left judgeable.
A candidate counts as pending only while its own run is queued or in progress — one that finished
without a Route job has already answered. A failure that can never succeed is not pending either:
a 403, a 404 or a shim path that does not resolve reads the same way on every poll, so it settles
like an empty one. And because no further turn end is coming once the agent has finished, a run
waiting on a verdict settles after four consecutive polls that reach none, about a minute and a
half at the default interval, rather than holding its sandbox to the deadline.

A steer consumed mid-turn produces no turn end of its own, so turn ends are a settle signal and
are never counted against the steer budget.

Those three bounds are one rule. A candidate whose run finished without a Route job, a wait that
has reached four verdict-less polls, and a Codex resume that has failed twice all end the wait
rather than extend it, because none of them resolves by waiting longer. The costs are not
symmetric: settling early costs one redundant run, while waiting on an answer that is not coming
costs the sandbox until the deadline. A permanent condition is therefore never treated as
retryable.

The watcher settles on every exit path, including a cancelled context, on a context of its own —
otherwise `Run` would hold a session open for a watcher that has stopped watching.

## Ceilings

- **Forge token life.** The stage mints a GitHub App installation token at job start; those live
  one hour and the runner has no refresher for them. The budget is
  `min(agent timeout, token life − margin)`, owned by the runner: `internal/runtime` knows
  nothing about forge token life.
- **Cost.** A steered turn on a large diff can cost as much as a fresh run. `steer.max_steers`
  defaults to 2, which covers the burst patterns in #6573 and #4960; beyond the cap the run
  settles and the queued run does the work. The cap counts the **run**, not the iteration: a
  validation loop builds one watcher per iteration and carries the count into each, so a
  three-iteration run still absorbs `max_steers` updates in total.
- **Session files are agent-writable.** A resume reads a session store the agent controls, so a
  poisoned session is a prompt-injection vector into the next turn. It is not a credential leak,
  and the hooks still gate tools ([ADR 0090](../ADRs/0090-runtime-neutral-sandbox-hooks-contract.md)).
  This is documented, not signed.
- **Per-process guards.** pi's config-dir guard, Codex's hook-digest re-assert and Claude's
  `--settings` hooks run once per process. A live steer keeps the process, so they have already
  run and the hooks stay loaded; interrupt-and-resume re-runs them. Neither weakens ADR 0090.


## The skip check

After a successful run that absorbed at least one steer, the runner posts a **receipt** as its
own comment on the work item:

```text
<!-- fullsend:steer consumed=<run_id,...> head=<sha> -->
_The run already working on this item absorbed follow-up run(s) 101, 102, so a run queued for
those events exits without repeating the work._
```

It is a **processing receipt** in the sense of the entity-first evaluation ADR
([fullsend#6956](https://github.com/fullsend-ai/fullsend/pull/6956)): a durable record on the
subject of what a run handled, which is what lets a later run decide whether its own trigger is
already covered. In `fullsend run`'s pre-flight — before the start comment and before the
pre-script, whose side effects are not free — a queued run reads the latest receipt and exits 0
without starting the agent when its own `GITHUB_RUN_ID` is listed.

### Who may write a receipt

**The author is the whole of the authentication, and it must be the job token's.**

The runner captures the GitHub Actions job token — `GH_TOKEN` as the action passed it in —
before minting swaps in the role token, and posts the receipt under that identity. The reader
resolves that login from the token rather than hardcoding it, because it differs between
github.com and GitHub Enterprise Server.

Two conditions have to hold for that identity to mean anything, and both are enforced in code:

- **Minting must actually have happened.** The swap is what puts the job token out of reach: it
  replaces both `GH_TOKEN` and `GITHUB_TOKEN` before the sandbox exists, and every child
  script's environment — pre-flight, pre-script, post-script and `validation_loop` — drops any
  remaining entry whose value is the swapped-away token, so a caller cannot keep it reachable
  under a third name. With no mint URL or no role there is no swap,
  the same credential stays in the environment a post-script inherits, and a post-script
  shelling out to `gh` could sign a receipt. The receipt credential is the job token *only when
  a role token was minted*; otherwise it is empty and both the writer and the check turn off.
- **The two identities must differ.** The action's `github_token` input defaults to
  `${{ github.token }}` but is an input, so a caller can pass an App installation token. If that
  resolves to the login the role token posts under, the agent's own comments carry the trusted
  author and the check is inverted. The reader resolves both logins and refuses to skip when
  they match, warning as it goes.

With both holding, nothing inside the sandbox can post as the receipt's author. An edited
receipt is ignored: editing keeps the author, and any identity with write access can edit
another's comment, so only a body the job token wrote counts. A manual re-run never skips on a
receipt: it keeps its run id, so the receipt that consumed its first attempt would skip it again.

The exception is a provider that hands the sandbox the job token itself: while any definition in
`.fullsend/providers/` expands `GH_WORKFLOW_TOKEN`
([ADR 0114](../ADRs/0114-github-packages-via-host-bound-workflow-token-provider.md)), for any
harness, the agent can recover it, so the skip check honours no receipt and logs why.

**What the author check proves, exactly:** that the comment came from a workflow job token of
*this repository* — not that it came from this run, or from fullsend. Every job's default
`GITHUB_TOKEN` in a repository posts under the same login, so any other workflow there could
write a comment carrying the marker syntax and it would be honoured. That is a
maintainer-controlled boundary, and it is deliberately not narrowed: checking the named run id
against the Actions API would add nothing, because that id is public.

This is why the receipt is a **separate comment** rather than the marker on the terminal status
comment. That comment is posted by the App, the identity the agent's own output goes out under,
so a marker there authenticates two public strings rather than the code path that wrote them —
an injection can induce the agent to write a well-formed marker into its review body, and the
App then posts it. The runner still writes a copy of the marker into the status comment so a
reader can see what a run absorbed, but **the skip check does not honour it**, and no
App-authored marker of any shape is a receipt.

The boundary this leaves is a credential leaked out of the runner's own process, which is a
compromise of the runner rather than of the sandbox. Reading the Actions API is deliberately not
held to the same bar: those reads prove no authorship, so the watcher uses the job token whether
or not minting swapped it.

One limit is inside the sandbox rather than outside it. What an acknowledgement attests differs by
runtime: pi's carries only the rpc id while Claude Code's carries the message text, and `tail -f`
follows a truncation, so an agent that rewrites a mailbox line under the same id is acked on pi as
though the original text had been read. The run is the same principal on both sides and already
owns its own output, so this changes no trust boundary — but `consumed` on pi means the update was
delivered, not that its words survived. Put precisely: the acknowledgement record the receipt
intersects against is influenced by the agent on pi, which can change the text an id acknowledges
but cannot add an id the runner never delivered.

### Failure directions

The check fails open in every direction: no receipt, an unreadable timeline, an unresolvable
login, a malformed run id. A false "already handled" silently drops the work; a false "not
handled" costs one run, which for review is a full review of the same head. Writing the receipt is best-effort for the same reason — a failed
post costs one queued run that redoes finished work, where failing the run would throw away work
that succeeded.

Only an outright success leaves a receipt, and only one that absorbed something. A failed,
cancelled, skipped or unvalidated run writes none, because a receipt claims the work is done. A
run that absorbed nothing writes none either: the status comment still records the head it
settled on, but a receipt asserting that a queued run may skip has nothing to assert. One
receipt per run, not per steer, and it names only what the iteration whose output shipped
absorbed — an update absorbed by an iteration that then failed validation never reached that
output.

**A second skip covers review duplicates no receipt can.** Two events on one head, such as a PR
opened and labelled a second apart, each route a review. The run in flight rejects the other as
not fresh, absorbs nothing and writes no receipt. So a queued review also exits 0 when the review
App reviewed the PR's current head after the queued run was created. The App is matched on its
exact logins and the forge's App verdict. The event kind comes from the normalized event when the
run has one, and otherwise from the event GitHub delivered to the job when it names the same pull
request, since the built-in stages get no normalized event. A human `/fs-review`, a manual re-run
and an unreadable event always run. Any error runs the review. Every run that does not skip logs
which condition failed. The check runs with steering on or off
([ADR 0120](../ADRs/0120-receipt-the-absorbed-update-under-the-job-token.md)).

**On GitLab there is no receipt.** Its job token cannot post or read notes, so the skip check
stays fail-open exactly as it is today: the queued pipeline does the work.

## The fleet-agent backstop

The runner exports `FULLSEND_RUN_HEAD_SHA` and `FULLSEND_RUN_STARTED_AT` into the sandbox
unconditionally, so an agent definition can re-read the work item once before writing its result.
This is a backstop under the harness steer, not an alternative: the steer is deterministic and
lands during the run, while the re-check depends on the model following the instruction and lands
only at the end. It costs one or two API calls when nothing changed.

Both are written from `bootstrapEnv`, not from `env.sandbox` or an `env/*.env` file: `.env.d`
files are sourced later and would expand the references host-side to empty, and a `${VAR}` in
harness `env.sandbox` hard-fails `ValidateRunnerEnvWith` for consumers that do not define it.

`FULLSEND_RUN_STARTED_AT` is the runner's own clock at the top of `runAgent`, not the run's
server-side `created_at`, so the two halves reference different instants: the watcher compares
server-side timestamps, the agent's re-check this host-side one. The gap is the setup before
`runAgent` — checkout, sandbox create, bootstrap — and it runs one way, because the exported
instant is *later* than the run's true start. The re-check can miss an update that landed during
setup; it cannot invent one.

## Configuration

Per-agent, off by default while the surfaces steering depends on land. A harness opts in by name:

The runner also exports `FULLSEND_STEER_ACTIVE=1` into the sandbox for an iteration whose watcher
is running, and clears it otherwise, so a value set earlier in the sandbox's `.env` does not
survive. The agent definitions treat the envelope's opening line as an injection attempt unless
it is set, so unset is the default and presence is written only
after the watcher has actually started — a watcher that declines, or whose first API calls fail,
leaves the variable unset. See [`fullsend run` § Run baseline](../cli/run.md#run-baseline).

```yaml
steer:
  enabled: true          # default: false — this is the opt-in
  max_steers: 2          # default: 2
  poll_interval_seconds: 30   # default: 30
```

The watcher polls every `poll_interval_seconds` and spends `max_steers` across the whole run; see
[Ceilings](#ceilings).

`enabled` is a pointer internally so that absent and `false` mean different things: a block setting
only `max_steers` says nothing about whether steering is on, so it takes the default rather than
being read as either an opt-in or an opt-out, and the same config keeps its meaning when the
default changes.

The runner sets `RunParams.Steerable` only when the harness has opted in and the runtime implements
`Steerer`. Otherwise `Steerable` stays false and `Run` is single-turn exactly as before.

## Known limits

**Parsers see N results per run.** A steered run emits one `ResultEvent` per completed turn, so anything
that assumed one result per iteration — the Claude parser's `seenResult`
([#6932](https://github.com/fullsend-ai/fullsend/issues/6932)), `RunMetrics`, the agent span,
`eval-measure` — is now 1:N. `RunMetrics.Steers` records every acknowledged steer, written by
`Run` alone so the watcher's goroutine never races it.

**The prompt-injection surface grows.** The steer text is built from PR bodies, comments and
commit messages, and under a stage command from an authorized human — the same trust dispatch
already places in that person. The sanitizer and the sandbox hooks remain the controls.

**The sandbox checkout is not refreshed.** It stays a snapshot of the head the run started on,
because refreshing it from the runner would clobber uncommitted work for the fix and code stages,
which write to that tree. On a head move the envelope names the new SHA and carries what changed
as context; the agent does not fetch it.

**A poll sees the newest 400 shim runs.** The listing asks for one workflow file and everything
created at or after the run's start, and reads at most four pages of 100, newest first. A
repository that fires more than 400 shim runs inside one agent run's lifetime can therefore push
an older follow-up out of every poll's view, and that update falls to the queued run. Raising the
page cap costs a request per page on every poll against the job token's budget; carrying a
position between polls would make the listing a cursor, which is the thing
[ADR 0119](../ADRs/0119-learn-of-later-events-by-listing-run-records.md) exists to avoid. The
bound is accepted rather than worked around.

**GitLab is not wired.** GitLab pipelines already queue rather than cancel, and the provenance
join is different — `GET /pipelines/:id/variables` exposes the poller-set `STAGE` and
`RESOURCE_KEY`, already covered by the HMAC dispatch signature. The watcher is GitHub-only for now,
and on GitLab it declines quietly unless the harness set `enabled: true` itself.

**A steer needs time left.** The exec hosting a live session cannot be extended once running, so
the watcher settles rather than steering when less than `MinRemaining` (default five minutes) of
the run budget remains, and the update falls to the queued run. On Codex the floor adds `runtime.CodexSteerInterruptCost`,
because the interrupt spends that time before the resumed turn starts.
