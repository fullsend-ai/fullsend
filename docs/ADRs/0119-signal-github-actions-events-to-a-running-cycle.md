---
title: "119. Signal GitHub Actions events to a running cycle"
status: Accepted
relates_to:
  - agent-infrastructure
  - platform-nativeness
topics:
  - dispatch
  - github-actions
  - polling
  - concurrency
---

# 119. Signal GitHub Actions events to a running cycle

Date: 2026-11-02

## Status

Accepted

This ADR partially supersedes one sentence of
[ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md): "The bound
process never polls the platform's pending-work list." On GitHub Actions, the
invocation layer inside a bound `fullsend run` process may list its shim
workflow's run records, which include the queued pending execution, as a
change signal. The read never owns or consumes pending work; every other ADR
0144 rule stays current, including "Wake delivery alone never consumes pending
work".

## Context

ADR 0117 (proposed) steers a running cycle when a notification for its
serialization key arrives while its sandbox runs. On GitHub Actions, that
notification often never arrives:

- **No inbound path.** GitHub Actions cannot deliver input to a running job.
- **No listener.** ADR 0144 opens a wake listener only where the platform
  provides authenticated cross-worker traffic, which GitHub Actions does not.
- **Event-only harnesses.** A harness without entity sources has no bound
  timer that re-reads the entity. Its only follow-up is the queued platform
  execution, which starts after the current job ends.

Every event that reaches a harness on GitHub Actions fires the repository's
shim workflow. The resulting run records are a server-side log of what
happened since the current run started, and Fullsend's forge client can
already list a workflow's runs since a given time.

This ADR aims to give a running cycle on GitHub Actions a change signal without
moving pending-work ownership into the bound process.

## Options

| Option | Why not chosen |
|---|---|
| Platform pushes input into the running job | GitHub Actions has no such API |
| Poll forge events with input drivers and cursors | Needs a driver, a race-safe cursor, and normalization per forge, as [ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md) noted |
| A courier job or relay service | A new component to deploy, credential, and keep available |
| Rely on the bound timer's entity re-read | Works for entity-source harnesses only; event-only harnesses have no timer check |
| **List the shim workflow's run records in the invocation layer** (chosen) | — |

## Decision

We adopt the shim workflow's run records, listed by the invocation layer inside
a bound `fullsend run`, to signal a running cycle on GitHub Actions that its
entity changed.

Each relevant new record becomes an in-memory wake signal for that cycle.

### What the signal does and does not do

| The signal | Does |
|---|---|
| Prompts a steer attempt for the running sandbox (ADR 0117) | Yes |
| Makes a check due for an entity-source harness | Yes |
| Fills or takes the newest-pending slot as an event | No. A run record is not a verified event payload |
| Consumes, claims, cancels, or dispatches pending work | No. The queued workflow run stays the ADR 0106 pending execution |
| Carries authority for admission, CEL evaluation, or sandbox execution | No. It is a scheduling hint only |

### Where the read lives

- The event source is part of the invocation layer, the component that owns
  the ADR 0106 serialization scope and the newest-pending slot in ADR 0144.
  It is not part of the bound timer and does not change the timer's rules.
- It delivers an in-memory wake signal, the "trusted components in one
  process" path in ADR 0144.

### Which records count

A run record is a candidate when all of these hold:

- it belongs to the same repository and the same shim workflow file;
- it was created at or after the current `fullsend run` call started;
- its `run-name` names the same canonical entity; and
- its triggering actor is not the App identity of the running harness itself.

- **Other agents count.** Runs triggered by other registered agents still
  count: a review submitted or a handoff label added by another agent is an
  update the running cycle needs
  ([ADR 0107](0107-bot-identity-resolution-for-dispatch-authorization.md)
  allows these bot-to-bot signals). Excluding only the running harness's own
  identity stops its status writes from waking itself. Whether any update
  reaches the agent is decided by fresh admission (ADR 0117, proposed), not
  here.
- **Routing does not matter.** The match does not depend on which jobs the
  run contains, so legacy routed runs and CEL-routed runs signal alike.
- **Edits arrive with other signals.** The shim does not run on comment
  edits, so an edit produces no run record; it reaches the cycle with the
  next record or wake.
- **False matches.** A false match costs one steer attempt that admits nothing
  new, and it makes a check due. For an entity-source harness whose CEL stays
  true, that check can start another sandbox cycle. Fullsend adds no
  handled-state gate (ADR 0144), so avoiding a redundant cycle is the
  responsibility of the harness CEL or pre-script.

### Cadence and limits

- **Cadence.** Listing repeats at a configured interval and once at the end
  of each turn, when a steer is cheapest. Each listing costs API requests
  against the listing credential's rate budget.
- **Credential.** Each listing uses a host-held credential with `actions: read` that never
  reaches the sandbox. The bound process holds no such credential today: the
  agent roles' App tokens (except the retro role's) carry no `actions`
  permission, and the job's workflow token is kept after the mint only as the
  provider-only credential of
  [ADR 0114](0114-github-packages-via-host-bound-workflow-token-provider.md).
  This ADR does not widen ADR 0114. Until a listing credential is supplied, or
  whenever it cannot read Actions runs, the event source declines and the
  cycle runs without it.
- **No cursor.** The candidate set is re-derived from the call's start time on each listing,
  so no cursor is persisted; an in-memory set of records already seen
  prevents duplicate signals.

### Workflow change

The shim workflows set a `run-name` that names the canonical entity. Runs for
`issue_comment` and `issues` events carry no pull request list, so without a
`run-name` the record cannot be tied to an entity.

## Forge scope

- **GitHub:** GitHub Actions only.
- **GitLab and Forgejo:** not supported for this signal. It reads GitHub
  Actions run records, and no equivalent run-record listing is designed for
  GitLab pipelines or Forgejo Actions. Steering there uses ADR 0144 wakes
  alone, where ADR 0144 is implemented (ADR 0117).
- **Jira:** not applicable. Entity-context-snapshot v1 does not stage Jira
  entities, so they do not steer (ADR 0117).

## Consequences

- **Event-only harnesses steer.** Event-only harnesses on GitHub Actions can
  be steered during a cycle, but their next cycle still starts only from the
  queued workflow run.
- **API cost.** Listing adds API requests during every steerable cycle.
- **Visible titles.** Every shim run in an adopting repository gets a new
  title, which users see in the Actions UI.
- **Workflow dependency.** A change that renames shim workflows or drops the
  `run-name` silences the signal; steering then falls back to ADR 0144 wakes
  alone.

## Open questions

- Which credential lists runs: a copy of the job's workflow token held only by
  the invocation layer, or an `actions: read` grant on each agent role's mint?
  Either is a trust-boundary change, decided with the implementation.
- Should the signal also carry the run record's event name and actor into the
  steer span, for audit, even though they carry no authority?
