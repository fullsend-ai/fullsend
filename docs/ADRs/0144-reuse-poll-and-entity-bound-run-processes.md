---
title: "144. Reuse poll and entity-bound run processes between work cycles"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - agents
  - dispatch
  - entities
  - polling
  - concurrency
---

# 144. Reuse poll and entity-bound run processes between work cycles

Date: 2026-10-20

## Status

Accepted

Partially supersedes [ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md):
a follow-up for the same entity may run in the active `fullsend run` process
instead of requiring a separate platform execution. ADR 0106's serialization
key, active-run preservation, and pending-work invariant remain current.

Partially supersedes [ADR 0063](0063-polling-based-work-discovery.md):
`fullsend poll` may remain resident instead of always exiting after one cycle.
Its per-repository scope and driver architecture remain current.

[ADR 0147](0147-evaluate-entity-cel-over-selectively-acquired-data.md)
partially supersedes the complete-entity due-check and full-history audit
requirements below. [ADR 0148](0148-reuse-verified-actor-roles-for-entity-cel.md)
separately permits bounded role reuse for entity CEL in place of mandatory
per-check actor-role refresh. The lifecycle, due-check scheduling, current
entity resolution, and fresh model-bound authorization rules remain current.

[ADR 0151](0151-harness-human-role-minimums.md) makes the selected harness's
effective `human_min_role` the platform-enforced threshold for each cycle's
event actor (when present), decision-relevant entity facts, and
Fullsend-controlled model records. Eventless checks still require trusted
Fullsend invocation provenance; model-bound actor checks remain fresh.

[ADR 0149](0149-coalesce-entity-source-discovery-queries.md) decides the
entity-source discovery-query strategy and requires a current local query
match, as well as true CEL, before an entity-driven sandbox cycle.

## Context

[ADR 0098](0098-entity-first-harness-evaluation.md) makes the current entity,
with an optional event, the input to harness routing. [ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md)
serializes runs for the same harness and entity, but requires a new platform
execution for follow-up work. Repeating dispatch and runner allocation when
another change arrives during or soon after a run delays the visible response.
Cluster platforms can instead deliver a wakeup to an existing process.

Keeping every process alive indefinitely would waste CI time and capacity.
The [infrastructure problem](../problems/agent-infrastructure.md#challenges-in-headless-and-cluster-hosted-runtimes)
also calls out the cost of idle compute. A reusable host process should not
imply a persistent agent sandbox or model session.

## Options

### Always exit after one cycle

This works on every platform, but each follow-up pays dispatch and runner
startup latency.

### Always remain resident

This reduces wakeup latency, but consumes CI job budgets and idle capacity even
when no more work arrives.

### Select a lifecycle from the invocation context

One-shot execution remains available; resident execution reuses the host while
there is a useful opportunity for another work cycle.

## Decision

Adopt three process lifecycles for `fullsend poll` and entity-bound
`fullsend run`. The **once** lifecycle performs one discovery cycle in `poll`,
or one entity eligibility check and at most one sandbox run in `run`, then
exits. The **poll** lifecycle repeats checks and waits for work; a bound `run`
exits after a finite platform-configured idle period, while `fullsend poll` may
remain resident until stopped. The **timed** lifecycle also repeats checks but
has a hard deadline; a bound `run` still exits on idle. Its effective deadline
is the earlier of its configured or derived timed deadline and any known outer
platform deadline. At command entry, the time remaining MUST accommodate at
least one complete initial cycle: one discovery and dispatch pass for `poll`,
or an entity check and, when selected, one full sandbox cycle for a bound
`run`. Size the reserve for a bound `run` to include current-entity
acquisition, authorization, CEL evaluation, context staging, scripts, the
configured sandbox timeout, cleanup, and final result publication; account
for platform startup in either lifecycle. The first pass or check is due
immediately, so a timer or shared-cache record cannot consume the reserve by
postponing it. The deadline bounds elapsed time, not a numeric cycle count.

An explicit timed selection with too little first-cycle budget fails visibly
before processing an initial event or claiming that work was deferred. The
automatic CI default does not select timed in that case. If the reserved first
cycle unexpectedly cannot complete a supplied initial event, the call fails
and identifies that event as incomplete, not as a protected continuation.

For a bound `run`, idle time starts at command entry or after a sandbox cycle
ends. False checks, no-op wakeups, and pre-script skips do not reset it. The
idle window for an entity-source harness MUST allow the immediate entry check
and at least one due check after a sandbox, including any shared-cache delay,
unless the hard deadline or provider backoff intervenes. After its reserved
first cycle, a timed run starts another sandbox only if its remaining budget can
accommodate expected execution, teardown, and final reporting; otherwise it
records a deferral for the recovery paths below.

Resident checks use rate-limit-aware intervals that may favor entities which
change more often, while retaining periodic reconciliation after missed
notifications. The repository-wide `fullsend poll` discovery interval SHOULD
default to longer than a bound `run`'s single-entity check interval, subject
to provider budgets and backoff. `fullsend poll` repeats repository-scoped
discovery through its configured drivers; a bound `run` has its own timer and
resolves only its canonical entity. The bound timer does not query queued
platform executions before checking that entity.

Resident timers MAY share a bounded-TTL cache of due-check records scoped by
harness identity and revision, repository, canonical entity, and effective
policy revision. A hit on a later scheduled check defers another read of that
known entity until expiry, accepting bounded reconciliation latency to reduce
API usage. `poll` still enumerates new candidates on its separate rate-aware
discovery cadence.
Compatible provider reads may be shared, but cached entity or authorization
data cannot substitute for a due check. An accepted event or eventless wake
invalidates the affected record and makes a check due. Completion of an
invocation invalidates its pre-invocation view for the next scheduled check.
Deferral for insufficient budget also invalidates the affected due-check record
so `fullsend poll` does not wait for its TTL. Each due check resolves a
complete current entity, refreshes actor authorization, and evaluates the
harness CEL predicate.
Failed or incomplete reads are not equivalent to no work.

A bound `run` accepts an event, entity, or entity ID and resolves it, using
source, kind, and repository context, to one canonical entity. Ambiguous or
unresolvable bindings fail rather than falling back to unbound execution. A
supplied entity snapshot or ID establishes the binding, not authoritative
current state. A supplied event is verified and retained as the first check's
prompting `event` while the bound run resolves current entity state. Once that
check consumes the event, later eventless checks use `event == null`; they do
not reuse it. With no binding input, `run` is unbound and once, primarily for
local use, and cannot select a resident lifecycle. An event-only harness can
bind to the entity resolved from its event, but cannot become actionable from
scheduled checks without another verified event.

An explicit lifecycle choice takes precedence for `poll` and bound `run`,
subject to eligibility.
`fullsend poll` defaults to once in every environment, preserving external
cron and CronJob use. A bound `run` defaults to once outside CI. In CI it
defaults to timed only when a usable job deadline meets the first-cycle
reserve above, entity sources are valid, effective policy permits eventless
evaluation, and a recurring repository-scoped `fullsend poll` discovery path
covers those sources; otherwise it defaults to once. An explicitly selected
resident bound lifecycle that performs eventless timer checks requires the
same coverage and fails closed without it.
Coverage means a configured recurring invocation or resident discovery cycle
that can eventually re-enumerate each eligible entity after a bound run exits.
Event-only harnesses may instead remain resident for verified event-bearing
wakes, with ADR 0106's pending execution as their recovery path. Thus the
timed CI default does not depend on a reachable listener.
The job deadline is distinct from an individual sandbox timeout. ADR 0063's
reserved `fullsend poll --watch` denotes the explicit poll lifecycle; the
lifecycle selection remains distinct from the existing `fullsend poll --mode`
input-discovery setting.

Each eventless check proves trusted Fullsend-controlled invocation provenance
and effective platform eligibility for its harness, repository, and entity;
the initial event actor or wake peer cannot supply that authority. Every check
applies platform authorization and complete entity acquisition before
evaluating the harness CEL predicate. A true predicate selects an actionable
cycle; if the timed budget permits it and setup succeeds, the sandbox runs
unless the harness's own pre-script explicitly skips it. Any desired
handled-state suppression belongs in harness CEL or that pre-script; Fullsend
adds no second handled-state or revision-deduplication gate. Each admitted cycle
assembles a fresh [ADR 0141](0141-deterministic-filtered-entity-context-staging.md)
snapshot; failed or incomplete assembly aborts before the pre-script or
sandbox. After valid assembly, the pre-script runs. A non-skipped cycle uses a
newly minted token, fresh sandbox, and applicable post-script. Cleanup occurs
at cycle end on success, failure, skip, or cancellation. Forge mutations occur
within that cycle rather than waiting for a later summary.

An explicitly resident bound `run` in the poll lifecycle can keep running
while CEL continues to select work; idle exit does not impose a numeric cycle
cap under continuous work. The need for rate, cost, or loop circuit breakers
identified by ADR 0106 extends to successive cycles inside
one call as well as to separate queued calls. Resource limits may stop or
delay work, but cannot mark a true CEL result as already handled.

The [Authorization Contract v1](../normative/authorization/v1/README.md)
ends authorization-cache lifetime on re-invocation, even in the same host
process. [ADR 0133](0133-actor-attributed-data-security-boundary.md) and that
contract still require model-bound actor checks after event or retrieval
admission, before sandbox initialization and first model exposure, and on later
retrieval; no cache entry crosses those checkpoints. An earlier CEL decision
or listener authentication carries no authority into a later cycle. The
resident process never runs two sandboxes concurrently. ADR 0106's event
serialization and coalescing still apply.

Harnesses without entity sources remain event-triggered and need verified
event-bearing notifications for later cycles.

In poll and timed lifecycles, a command MAY expose a private host-side wake
listener only where the platform provides authenticated cross-worker traffic
between trusted Fullsend processes. Otherwise it binds no wake port; trusted
components in one process use an in-memory signal. The wake channel never
receives CI or forge webhooks directly; a receiver that bridges such webhooks
to Fullsend wake messages is outside this ADR. A backend-specific WIF/OIDC
exchange or equivalent host-held capability must authenticate the Fullsend
peer and bind it to the active scope; a CI job identity alone does not prove
which process sent a request. If the credential cannot be confined to the
trusted host and kept from the sandbox, no listener opens. A bound `run`
rejects messages outside its harness, repository, and canonical entity; a
repository-scoped `poll` rejects other repositories.

For event-bearing delivery, the invocation path verifies provenance,
normalizes and authorizes the event, and applies harness routing before
forwarding a Fullsend-internal message. An invocation's initial event cannot
be displaced before its first check. Later accepted events occupy a
newest-pending slot per serialization key. A check without a reserved initial
event atomically takes that slot at its start; later arrivals remain for the
following check. `poll` must not coalesce events for different keys. Eventless
wakeups may coalesce into one prompt check but cannot replace a retained
event. A notification during a check or sandbox schedules an immediate
follow-up for its key. Authentication of the sender
and an eventless wakeup are scheduling signals, not authority to run a
sandbox. Where networking allows, `poll` may hand an accepted event to the
invocation layer to wake an active bound `run`. The processes retain
independent deadlines and results. The sandbox and agent runtime cannot
control the wake channel or host loop.

The invocation layer owns the [ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md)
scope of harness, target repository, and canonical entity. For event-backed
follow-ups it retains the durable newest-pending platform execution and MAY
also wake the one active bound process when reachable. Wake delivery alone
never consumes pending work. If an in-process cycle already handled the event,
the pending execution may still start after the active call exits. It resolves
current entity state and authorization and evaluates CEL with its retained
event; if CEL is now false, it exits before invoking a sandbox. If an
event-sensitive predicate remains true, a fresh sandbox may run again unless
the harness expresses handled state in CEL or its pre-script. The bound process
never polls the platform's pending-work list. A wake channel is a low-latency
hint, not the ownership record.

Timer-discovered eventless work creates no pending platform execution. If a
bound run cannot process it before exit, it records the deferral, releases any
source-native discovery lock, and invalidates its due-check cache entry. The
recurring repository-scoped `fullsend poll` discovery path re-enumerates the
harness's entity source on its next scheduled invocation or resident discovery
cycle; when it encounters the still-actionable entity, it dispatches a new
bounded `run`. The bound process does not dispatch a replacement platform execution
itself. This path may add the broader poll cadence to recovery latency.

One bound `fullsend run` call is one externally visible run with a stable run
identity and host-owned parent trace span. Each selected cycle has a child
span and, if its sandbox starts, a sandbox trace subtree under that parent.
Each child cycle records whether a verified event actor or the trusted
Fullsend invocation identity initiated it. Each eventless CEL evaluation
records its effective policy, harness revision, repository, and canonical
entity, and identifies every retained action-indicating element and its actor
as entity-history provenance. Neither a retained element's actor nor an
earlier event's actor is attributed as the initiator of that eventless
evaluation.
Starting a later CEL-selected cycle makes the earlier result historical and
the run's current state in progress. When that cycle ends, its validated
result or explicit skip, failure, or cancellation is the current outcome;
no-op checks do not replace it. The latest cycle determines run-level status
and validated output. A latest skip has no validated output even when the CI
job concludes successfully; an unrecovered host failure or timeout fails the
call.

On normal timed CI completion, the job publishes one final status and one
`fullsend-<agent>` artifact; the containing GitHub workflow then emits its
normal single `workflow_run` completion. The artifact has one top-level
legacy-shaped run directory; its `iteration-*/output/` path exposes only the
latest cycle's validated result, if any. Earlier outputs remain under a
nonmatching diagnostics subtree and must not be selected when the latest
cycle skips or fails. Its `metrics.json` aggregates resource use across all
cycles; per-cycle metrics remain in diagnostics and child spans. Host-written
final metadata identifies the canonical entity key, platform-verified result
revision, selected cycle, validation status, and any deferred or unchecked
continuation. It distinguishes event-backed work protected by an ADR 0106
pending execution from timer-discovered work awaiting `fullsend poll`
rediscovery; the latter has no direct platform handoff. Accepted work that
cannot run before exit does not erase the latest completed cycle's result, but
the metadata cannot claim the entity is caught up. If the reserved first cycle
fails before completing a supplied initial event, final metadata, when
writable, identifies the incomplete event and failed call rather than
classifying it as pending execution or poll rediscovery. Chained workflows that
discover the artifact and validate its final output can retain that path, but
must check the latest validation status before acting: a successful
`workflow_run` conclusion alone can describe a skipped cycle.
Revision-sensitive consumers must compare the verified result revision with
live entity state before a mutation; `workflow_run.head_sha` may describe an
earlier revision. Workflows that require a follow-up for every sandbox
invocation or treat run-level
metrics as one sandbox's usage also need migration.

A bound `run` in the long-lived poll lifecycle updates the same run-level
result when it becomes idle or within a bounded response window; later
updates supersede earlier ones. A hard termination can prevent artifact
publication and must be visible as failure rather than a successful no-op.

## Consequences

- A warm bound process can respond to later work without reallocating a runner,
  while each actionable attempt still receives a fresh sandbox.
- Timed processes can reuse a CI job briefly and release it on idle or before
  its deadline instead of occupying the full allotted time.
- Event-backed follow-ups retain pending platform work; eventless deferrals
  rely on slower `fullsend poll` rediscovery. Private wake delivery reduces
  latency without becoming a prerequisite for timed CI.
- Several sandbox invocations in one CI job retain one external result and
  artifact while their separate activity remains visible in the parent trace.
- The current one-shot CLI and platform workflows need lifecycle, event
  delivery, and result-reporting changes; entity-query and CEL data
  acquisition strategies remain separate decisions.
