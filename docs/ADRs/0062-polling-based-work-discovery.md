---
title: "62. Polling-based work discovery via dispatch drivers"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
  - operational-observability
topics:
  - polling
  - jira
  - dispatch
  - drivers
  - cli
  - per-repo
---

# 62. Polling-based work discovery via dispatch drivers

Date: 2026-06-18

## Status

Accepted

## Context

Fullsend's primary dispatch path is **event-driven**: forge webhooks are normalized
into a `NormalizedEvent`, authorized per
[ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md), matched
against harness CEL `trigger` expressions, and executed via pluggable output
drivers ([ADR 0061](0061-harness-cel-dispatch.md)).

Many teams using **per-repo installation mode**
([ADR 0033](0033-per-repo-installation-mode.md)) track work outside the git
forge — Jira is the most common example. Issues may live in Jira while code and
Fullsend configuration live in a single GitHub or GitLab repo. Webhook delivery
can also be delayed, dropped, or misconfigured on the forge itself. Polling
provides a **pull-based complement** that does not depend on inbound webhook
infrastructure.

We need a mechanism scoped to **per-repo mode** that:

1. **Discovers** candidate work items from remote systems on a schedule.
2. **Emits** `NormalizedEvent` values for changes since the last poll check.
3. **Dispatches** matched harnesses through the same pipeline as
   `fullsend dispatch` (authorize → CEL → output driver).
4. **Coordinates** safely when multiple poll processes run in parallel or when
   a poller crashes mid-cycle.

**Trigger routing is out of scope for this ADR.** Harness files declare CEL
`trigger` expressions evaluated by `fullsend dispatch`
([ADR 0061](0061-harness-cel-dispatch.md)). Poll input drivers populate
`NormalizedEvent`; they do not duplicate slash-command, label, or actor-guard
logic in `config.yaml`.

**Out of scope:** Per-org installation mode — no `.fullsend` config repo,
enrolled-repo shims, cross-repo dispatch, or org-level polling across multiple
repos.

**Initial delivery vs extensibility:** The first implementation targets **Jira
polling in per-repo mode**. Input and output driver interfaces are designed so
GitHub, GitLab, and additional sources can be added later without redesign.

## Options

### Option A: Extend webhook-only dispatch

Add Jira (and other) webhooks that translate remote events into forge dispatch.

- Pro: Near-real-time when webhooks work.
- Con: Requires webhook infrastructure per source; brittle for Jira; does not
  help when work items are not forge-native.

### Option B: Central orchestrator with a shared work queue

A long-lived service polls all sources, enqueues work in a database, and
dispatches agents.

- Pro: Strong locking and deduplication.
- Con: New operational component; diverges from Fullsend's repo-as-coordinator
  theme ([ADR 0002](0002-initial-fullsend-design.md)).

### Option C: `fullsend poll` as dispatch with poll input drivers (recommended)

`fullsend poll` reuses the **input/output driver architecture** from
`fullsend dispatch` ([ADR 0061](0061-harness-cel-dispatch.md)). Poll-specific
**input drivers** discover work and emit `NormalizedEvent` values; the shared
**dispatch core** authorizes, evaluates harness CEL triggers, and invokes an
**output driver** that dispatches agent runs directly (not a JSON plan).

Coordination state lives on work items themselves (Jira entity properties) rather
than in a central queue.

- Pro: No duplicated trigger configuration; routing stays on harness files;
  poll and webhook paths share authorization and CEL evaluation.
- Con: Lock semantics are driver-specific; Jira write-then-verify coordination
  adds API calls and requires careful stale-threshold tuning.

## Decision

Adopt **Option C**: expose polling as **`fullsend poll`**, implemented on the
same driver architecture as **`fullsend dispatch`**, scoped to **per-repo
mode**, with a **Jira poll input driver** as the first input adapter and a
**GitHub Actions dispatch output driver** as the first output adapter.

### Relationship to `fullsend dispatch`

[ADR 0061](0061-harness-cel-dispatch.md) defines:

```
input driver → authorize → enumerate harnesses → CEL triggers → output driver
```

`fullsend poll` is a **composition** of that pipeline:

```
poll input driver(s) → per-item coordination → dispatch core → output driver
```

| Piece | `fullsend dispatch` (webhook) | `fullsend poll` |
|-------|------------------------------|-----------------|
| Input | `gha-event`, `json`, … | `jira-poll`, `github-poll` (future), … |
| Normalization | Adapter maps webhook → `NormalizedEvent` | Poll adapter maps issue delta → `NormalizedEvent` |
| Authorization | Platform gate ([ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)) | **Same gate** — not reimplemented in poll config |
| Routing | Harness CEL `trigger` on `event` | **Same** — poll does not define triggers |
| Output | `gha-matrix`, `json`, … | `gha-dispatch` (direct workflow dispatch) instead of printing plans |

Poll input drivers are responsible for **discovery, change detection, and lock
management** on the remote system. The dispatch core and output drivers are
shared with `fullsend dispatch`.

### Scope

Polling is implemented **only for per-repo mode**
([ADR 0033](0033-per-repo-installation-mode.md)). A single target repository
owns poll configuration, credential references, and the dispatch output path.
Per-org installations continue to rely on event-driven dispatch only.

### Architecture overview

```
┌────────────────────────────────────────────────────────────────────┐
│  Target repo (per-repo install)                                    │
│                                                                    │
│  Scheduler (GHA schedule, cron, k8s CronJob, …)                    │
│       │                                                            │
│       ▼                                                            │
│  fullsend poll [--watch]                                           │
│       │                                                            │
│       ▼                                                            │
│  Poll input driver(s)  ──►  NormalizedEvent per detected change    │
│  (jira-poll, …)              + coordination (write-then-verify)    │
│       │                                                            │
│       ▼                                                            │
│  fullsend dispatch core                                            │
│    authorize (ADR 0054) → harness CEL triggers (ADR 0061)          │
│       │                                                            │
│       ▼                                                            │
│  Output driver (gha-dispatch) → agent workflows / fullsend run     │
└────────────────────────────────────────────────────────────────────┘
```

Control flow remains **unidirectional** per
[ADR 0016](0016-unidirectional-control-flow.md): the poll loop discovers work
and invokes infrastructure; agents do not drive the poll loop.

### CLI

- **`fullsend poll`** — runs one poll cycle: each configured poll input driver
  discovers changes, emits events for the dispatch core, then exits. Typical
  schedulers:
  - A **scheduled job** in the target repo's `.github/workflows/` that runs
    `fullsend poll` (same repo context as the shim).
  - External cron or Kubernetes CronJob with credentials for the remote system
    and workflow dispatch.
- **`fullsend poll --watch`** — runs poll cycles on an internal timer until
  interrupted (same pattern as `kubectl watch`). **Deferred** for initial
  implementation.
- **`fullsend poll cancel`** — operator override for a locked work item:

```bash
fullsend poll cancel --issue PROJ-123 [--input-driver jira-poll]
```

  - **Deletes** the repo-namespaced lock entity property on the issue (e.g.
    `fullsend.poll.{owner}.{repo}.lock`).
  - When the lock property stores a **workflow run id** (written by the output
    driver on successful dispatch), **cancels** that GitHub Actions run via the
    API before deleting the lock.
  - **Does not** advance `lastCheck` — the triggering change remains eligible
    for the next poll cycle after cancel.
  - Requires credentials for the poll input driver (Jira) and, when cancelling a
    run, workflow administration on the target repo.

  Use when an agent run is stuck, mis-dispatched, or an operator needs to unblock
  an issue without waiting for stale-lock expiry.

Flags mirror `fullsend dispatch` where applicable:

```bash
fullsend poll --input-driver jira-poll --output-driver gha-dispatch
```

When omitted, drivers are read from `.fullsend/config.yaml` or auto-detected
from environment (same resolution rules as `fullsend dispatch`).

The command operates in per-repo context: it reads configuration from the
target repo's `.fullsend/` directory and dispatches workflows in that same repo.

### Configuration

Poll settings live in the target repo's `.fullsend/config.yaml`
([ADR 0033](0033-per-repo-installation-mode.md)). Poll configuration declares
**input drivers** (discovery and coordination) and an **output driver**
(dispatch execution). It does **not** declare triggers, slash commands, or
per-role routing — those live on harness files per
[ADR 0061](0061-harness-cel-dispatch.md).

```yaml
poll:
  input_drivers:
    - type: jira-poll
      connection: { ... }          # base URL, credential ref
      queries:                     # JQL expressions
        - project = PROJ AND status != Done
      lock:                        # optional overrides
        m: 50
        n: 5
        stale_threshold: 900s
        refresh_interval: 300s
  output_driver: gha-dispatch      # direct dispatch; not json plan output
```

Each poll input driver entry specifies at minimum:

- **Driver type** (`jira-poll`; later `github-poll`, `gitlab-poll`, …).
- **Connection** (base URL, credentials reference).
- **Queries** — one or more search expressions (JQL for Jira; equivalent filters
  for other systems when added).

Optional **lock** overrides tune coordination per driver (see below).

Harness enumeration for CEL evaluation uses agent registration per
[ADR 0058](0058-agent-registration.md) — the same path as `fullsend dispatch`.

### Poll input drivers and `NormalizedEvent`

Poll input drivers translate **changes on remote work items** into one or more
`NormalizedEvent` documents suitable for harness CEL evaluation
([`docs/normative/normalized-event/`](../normative/normalized-event/),
[ADR 0061](0061-harness-cel-dispatch.md)). Jira poll emits **work-item**
events only; change-proposal stages (e.g. `fix`) are routed from forge events,
not Jira issue comments.

Responsibilities:

1. **Discover** candidate issues via configured queries.
2. **Detect changes** since the per-issue `lastCheck` timestamp (see below).
3. **Emit** a `NormalizedEvent` per detected transition (comment added, label
   added, issue created, field updated, …) with `entity`, `actor`,
   `transition`, and `state` populated so harness `trigger` expressions can
   route the same way as forge webhook events.
4. **Coordinate** via driver-specific locking before handing events to the
   dispatch core.

The Jira poll input driver emits `NormalizedEvent` documents per the
**[Jira poll adapter](../normative/normalized-event/v1/jira-poll-adapter.md)**
and [`normalized-event.schema.json`](../normative/normalized-event/v1/normalized-event.schema.json).
Summary:

| Concern | Mapping |
|---------|---------|
| Target repo | `repo` = GitHub slug where agents run (not the Jira project) |
| Work item | `entity.kind: work_item`, numeric `entity.id`, `entity.key` (`PROJ-123`), `entity.url` |
| Provenance | `source.system: jira`, `raw_type` / `raw_action` from Jira API object |
| Routing input | `transition.*` (e.g. `comment_added` + `comment.command` / `instruction`) |
| Labels | `state.labels` snapshot |
| Actor | Jira `accountId`, bot detection, project-role → ADR 0054 `role` |

Example fixture: [`jira-fs-triage-comment.json`](../normative/normalized-event/v1/examples/jira-fs-triage-comment.json).

Poll input drivers MUST NOT perform authorization policy — that is the dispatch
core's responsibility per [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)
and [ADR 0061](0061-harness-cel-dispatch.md).

### Dispatch output driver

The poll command uses an output driver that **dispatches agent runs directly**
— typically `gha-dispatch`, which triggers the target repo's generic agent
runner workflow (e.g. `fullsend dispatch --output-driver gha-matrix` pattern)
rather than emitting a JSON dispatch plan to stdout.

The poll-trigger workflow SHOULD refresh the poll lock as its **first step**
before calling stage reusables or `fullsend run`, narrowing the gap between
poller exit and runner lock maintenance.

Non-event entry uses a dedicated poll workflow per
[ADR 0041](0041-synchronous-workflow-call-event-dispatch.md) (e.g.
`.github/workflows/fullsend-poll.yml` via `workflow_dispatch`). The event shim
does not listen on `workflow_dispatch`.

### Change detection (`lastCheck`)

Per work item, each poll input driver maintains an entity property
(e.g. `fullsend.poll.{owner}.{repo}.lastCheck`) storing the timestamp of the
last **successfully dispatched** change.

Per poll cycle, for each locked candidate:

1. Read `lastCheck`. If absent, treat the baseline as issue creation time. On
   first deployment against an existing backlog, operators SHOULD seed
   `lastCheck` or narrow queries to recently changed issues to avoid a
   one-time thundering herd.
2. Inspect changes since `lastCheck` and emit one `NormalizedEvent` per
   qualifying transition.
3. For each event, run the dispatch core (authorize → CEL). When the output
   driver **successfully schedules** a run, advance `lastCheck` to the timestamp
   of that change. On scheduling failure, leave `lastCheck` unchanged so the
   next cycle retries.

**Jira API constraints:**

- Changelog API has **no server-side timestamp filter** — adapters paginate and
  filter client-side. Prefer JQL `updated >= "<lastCheck>"` in discovery
  queries to limit candidates.
- Comment API supports newest-first pagination — stop when comments are older
  than `lastCheck`.

### Jira poll input driver — write-then-verify coordination

Jira has **no compare-and-swap** on single-issue entity property `PUT` — writes
are unconditional overwrites. The coordination algorithm is **write-then-verify
with jitter**, not true optimistic locking.

**Duplicate dispatch mitigation** uses two layers:

1. **Jira lock** — at most one poller owns an issue through dispatch scheduling.
2. **GitHub Actions concurrency** — reusable agent workflows already define
   per-stage `concurrency` groups keyed by `source_repo` and work-item identity
   (`issue.number` / `pull_request.number` from projected `event_payload`), with
   `cancel-in-progress: true`. Poll dispatch MUST populate those fields from
   `NormalizedEvent.entity` (for Jira: `event_payload.issue.number` from
   `entity.id`, or an equivalent stable key) so a duplicate dispatch for the
   **same harness stage** on the **same work item** cancels the in-flight run.

GHA concurrency covers the common duplicate case (same stage, same item). It does
**not** prevent duplicate side effects when two different harness stages match one
event, when both runs start before either enters the concurrency group, or when
a cancelled run has already committed partial work. Agent implementations SHOULD
still be safe to re-run (idempotent or gracefully no-op on repeat) as defense in
depth — but polling does not impose a new idempotency requirement beyond what
event-driven dispatch already assumes under `cancel-in-progress`.

Property keys are namespaced by target repo to avoid collisions when multiple
repos poll the same Jira project:

- `fullsend.poll.{owner}.{repo}.lock`
- `fullsend.poll.{owner}.{repo}.lastCheck`

Each `fullsend poll` invocation:

1. **Assigns a UUID** at startup.
2. **Queries** JQL for up to **M** candidate issues. Entity properties are
   **not searchable in JQL** without a Connect/Forge app index — the driver
   MUST filter locked issues **client-side** by reading each candidate's lock
   property (budget this in API cost estimates).
3. **Randomly selects N** issues from candidates (`N < M`) to spread load.
   Document that issues beyond the top **M** JQL results may be starved unless
   queries use rotating `ORDER BY` or a cursor across cycles.
4. **Attempts to lock** each selected issue (UUID + timestamp in lock property).
5. **Waits** 500–1500 ms (jitter).
6. **Re-reads** lock properties for the N issues.
7. For each issue:
   - If the lock timestamp is **stale**, remove the lock (accepted race —
     mitigated by GHA concurrency for same-stage duplicates).
   - If the lock still contains **this UUID**, emit `NormalizedEvent`(s) for
     changes since `lastCheck` and pass them to the dispatch core.

**Recommended defaults** (overridable per driver):

| Parameter | Default | Rationale |
|-----------|---------|-----------|
| **M** | `50` | Jira Cloud default page size; tune to backlog size. |
| **N** | `5` | Reduces contention among concurrent pollers. |
| **Stale lock threshold** | `900s` | Covers P99 GHA queue latency on hosted runners plus runner startup. Tune via `workflow_job` queue → in_progress metrics. Max tolerable queue latency ≈ `stale_threshold − runner_startup − refresh_interval/2`. |
| **Runner refresh interval** | `300s` | SHOULD be ≤ half the stale threshold. |

Consider a **two-phase lock** in the lock property: short `pending` TTL while
dispatch is queued, then `running` refreshed by the poll-trigger workflow and
agent runner.

### Lock lifecycle during agent execution

1. **Pre-invoke verification** — immediately before output dispatch, re-read the
   lock; abort if UUID mismatch or stale.
2. **Lock handoff** — pass lock metadata to the runner via environment
   variables (see below). The lock property SHOULD record the dispatched
   **workflow run id** when available so `fullsend poll cancel` can cancel it.
3. **Lock removal on dispatch failure** — if the output driver fails after
   retries, remove the lock so another cycle can retry.
4. **Runner maintenance** — agent runner refreshes the lock during execution and
   removes it on teardown (or harness `post_script`); stale expiry is fallback.

Runner verifies `FULLSEND_POLL_LOCK_ID` matches before starting the LLM; abort
if the lock was lost.

### Runner environment (lock + work item)

Stage workflows and `fullsend run` receive poll lock fields in addition to
execution-ref projection from `NormalizedEvent`
([ADR 0061](0061-harness-cel-dispatch.md)):

| Variable | Purpose |
|----------|---------|
| `FULLSEND_WORK_ITEM_URL` | Canonical work item URL (from `entity`) |
| `FULLSEND_WORK_ITEM_SOURCE` | `jira`, `github`, … |
| `FULLSEND_WORK_ITEM_KEY` | Stable key (`PROJ-123`, issue number, …) |
| `FULLSEND_POLL_LOCK_ID` | Poller UUID |
| `FULLSEND_POLL_LOCK_DRIVER` | Input driver name (`jira-poll`, …) |
| `FULLSEND_POLL_LOCK_PROPERTY` | Entity-property key for the lock |

`GITHUB_ISSUE_URL` and related forge fields remain populated when
`entity` maps to a GitHub issue for backward compatibility.

### Concurrency model

Within one `fullsend poll` invocation:

- Each configured input driver runs its discovery loop.
- Per-issue change detection, locking, and dispatch scheduling run concurrently
  up to a configurable limit.
- Lock refresh during agent execution runs in the runner process, not the poller.

Multiple concurrent `fullsend poll` processes are expected (overlapping cron,
`--watch`, manual runs). Write-then-verify coordination limits duplicate
dispatch; GHA per-stage concurrency is the primary safety net for same-item
re-dispatch.

### Observability

**MVP (in scope):** `fullsend poll` emits **structured logs** per cycle —
driver name, candidates scanned, locks acquired/released, events emitted,
dispatches scheduled/failed, and rate-limit backoff events. Sufficient for
initial tuning of M, N, and stale thresholds.

**Deferred (out of scope for poll MVP):** exportable metrics (Prometheus
counters/histograms), dashboards, and alerting. Run-level source/destination
annotations ([#896](https://github.com/fullsend-ai/fullsend/issues/896)) apply
to agent workflows regardless of poll vs webhook trigger; poll-specific cycle
metrics are a separate follow-up when platform observability matures.

### API budget and rate limits

Jira Cloud uses **points-based quotas** (not flat request counts). Per-cycle
budget MUST account for: search, per-candidate property reads (lock filter),
property writes, changelog pagination, comment fetches, and any group lookups
required by the authorization gate. Implementations SHOULD track
`X-RateLimit-*` headers and apply adaptive backoff.

## Consequences

### Positive

- Per-repo installations can trigger agents from Jira without webhooks on Jira.
- **No duplicated trigger config** — routing lives on harness CEL per ADR 0061.
- **Shared dispatch pipeline** — poll and webhook paths use the same
  authorization and CEL evaluation.
- **Driver composition** — multiple poll input drivers in one config.
- Parallel poll cycles are safe via write-then-verify locks and stale expiry.
- External one-shot scheduling keeps the initial implementation simple;
  `--watch` reserved for later.

### Negative / risks

- **Per-org gap** — per-org installs cannot poll until a separate design exists.
- **Polling latency** — discovery at scheduler granularity, not real-time.
- **Jira API cost** — client-side lock filtering and changelog pagination are
  expensive; M, N, and interval must be tuned.
- **Write-then-verify races** — duplicate dispatch possible before GHA
  concurrency applies; mitigated by per-stage `cancel-in-progress` groups when
  `event_payload` projection is correct.
- **Work item abstraction** — harnesses and pre-scripts may need
  `FULLSEND_WORK_ITEM_*` plumbing for non-GitHub sources.

## Open questions

Questions below are **intentionally deferred** — see *Handling deferred
questions* for recommended resolution path.

| Topic | Default / constraint | Resolve in |
|-------|---------------------|------------|
| `poll.input_drivers` config schema | YAML example in this ADR; validate in `fullsend poll` impl | Implementation ([#2263](https://github.com/fullsend-ai/fullsend/issues/2263)) |
| `gha-dispatch` output driver | Reuse `fullsend dispatch` output driver registry; poll passes same `NormalizedEvent` | Implementation (shared with ADR 0061) |
| Credential placement | Jira token + `GITHUB_TOKEN` / App creds in target repo secrets; scheduled workflow host | Implementation + install guide |
| Runner lock refresh | Default interval = half stale threshold; refresh from `fullsend run` host using `FULLSEND_POLL_LOCK_*` | Implementation |
| GitHub/GitLab poll input drivers | Deferred past Jira MVP | Future issue per driver |
| Async completion → lock release | Runner teardown + stale expiry; optional GHA status poll in open follow-up | Implementation |
| `--watch` interval | Default 60s, exponential backoff on errors, SIGINT/SIGTERM clean shutdown | Implementation when flag ships |
| Exportable poll metrics / dashboards | Structured logs suffice for MVP | Post-MVP observability |

### Handling deferred questions

1. **Do not block ADR acceptance** on implementation detail that ADR 0061 already
   patterns (output drivers, authorization, CEL).
2. **Resolve in the implementation epic** ([#2263](https://github.com/fullsend-ai/fullsend/issues/2263))
   with sub-issues per driver: `jira-poll` input, `gha-dispatch` output, lock
   refresh in runner, `fullsend poll cancel`, scheduled workflow template.
3. **Post-MVP** — GitHub/GitLab poll input drivers, exportable poll metrics:
   file issues when Jira MVP ships; do not expand this ADR.

## References

- [ADR 0002 — Initial Fullsend Design](0002-initial-fullsend-design.md)
- [ADR 0016 — Unidirectional control flow](0016-unidirectional-control-flow.md)
- [ADR 0033 — Per-repo installation mode](0033-per-repo-installation-mode.md)
- [ADR 0041 — Synchronous workflow_call for event-driven dispatch](0041-synchronous-workflow-call-event-dispatch.md)
- [ADR 0045 — Forge-portable harness schema](0045-forge-portable-harness-schema.md)
- [ADR 0054 — Require authorization on all agent dispatch paths](0054-require-authorization-on-all-agent-dispatch-paths.md)
- [ADR 0058 — Agent registration](0058-agent-registration.md)
- [ADR 0061 — Harness CEL triggers and fullsend dispatch drivers](0061-harness-cel-dispatch.md)
- [NormalizedEvent v1](../normative/normalized-event/v1/)
- [Jira poll adapter (NormalizedEvent extension)](../normative/normalized-event/v1/jira-poll-adapter.md)
