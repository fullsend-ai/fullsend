---
title: "167. Use tracker-first dispatch for multiple targets"
status: Accepted
relates_to:
  - multi-target-execution
  - agent-architecture
  - agent-infrastructure
topics:
  - dispatch
  - coordination
  - authorization
---

# 167. Use tracker-first dispatch for multiple targets

Date: 2026-11-10

## Status

Accepted

## Context

[ADR 0061](0061-harness-cel-dispatch.md) dispatches eligible harnesses, while
[ADR 0063](0063-polling-based-work-discovery.md) integrates Jira work items.
[ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md)
serializes work for a harness and subject. A request spanning several source
branches needs independent target identities and complete accounting, but
that does not require a new multi-target execution interface in the runner.

[Multi-target execution](../problems/multi-target-execution.md) compares the
integration choices. Durable scheduling and publication recovery are tracked
separately in [#9253](https://github.com/fullsend-ai/fullsend/issues/9253).
This ADR aims to assess multiple authorized branches by reusing ordinary
single-target triage with visible work tracking for every branch.

## Options

A native Fullsend target-plan interface could enforce a reusable multi-target
contract, but requires broader platform implementation. A CI matrix launches
jobs without supplying target discovery, durable accounting or tracker
reconciliation. An adapter can instead create target-specific trackers first
and reuse existing single-target dispatch and triage, accepting additional
investigation tickets before applicability is known.

## Decision

Use tracker-first dispatch to assess each authorized source target through an
ordinary single-target Fullsend triage run.

A trusted workflow adapter resolves a bounded manifest of maintained targets
from reviewed domain mappings. It records the manifest revision, source and
policy identities, required targets and persistent dispatch/retry bounds in
the recovery journal. After current disclosure and target/action authorization
checks, it creates or reuses one investigation tracker per target. The initial
adapter uses Jira. Tracker identity combines the domain case and stable target
identity, such as repository, branch, component and product release; a new
source SHA identifies an attempt rather than a new ticket. Each ticket starts
with applicability unknown, not an affected verdict. A discovery assessment
is not a prerequisite for creating it.

The adapter populates the supported single-target intake from trusted mapping
and advisory data, then schedules triage through an authorized dispatch path.
A bot comment cannot impersonate a human command or bypass the harness actor
minimum. If no supported path can authorize triage, the tracker remains
visibly pending authorization. Each run verifies its actual origin, branch
and source revision against the selected intake; a release target must not
silently fall back to the default branch. The run owns its checkout, context,
case artifacts and fresh assessment receipt. Distinct target trackers provide
separate subjects for existing concurrency controls; the adapter also enforces
case-wide bounds over scheduled and running attempts until their termination
is reconciled.

The adapter retains separate receipts for tracker publication, scheduling and
triage outcome, using the recovery work in #9253 for duplicate prevention,
ownership and ambiguous writes. Worker errors and uncertain or failed
assessments remain attached to the same investigation ticket for retry or
human action. They never become an affected finding or coding permission.
Ticket creation is a publication milestone, not successful assessment or case
completion. Every required target remains accounted for, including blocked,
unmapped, unscheduled or exhausted work; domain policy determines completion.

Changed mappings, source or policy invalidate dependent attempts and require
fresh source binding and triage. New required branches extend the manifest
and reopen accounting; existing target trackers are reused and consumed work
bounds do not silently reset. Domain mapping, Jira rendering and aggregate
accounting remain in the workflow adapter. This initial route introduces no
native Fullsend multi-target scheduler or plan/result API. Later platform
support remains an option if independently justified by other consumers.

## Forge scope

The initial tracker adapter uses Jira for GitHub, GitLab and Forgejo source
targets. Each enabled single-target execution path must verify source binding
and authorization for its forge; unsupported combinations stay blocked.
Distinct Jira tickets do not grant source-repository access. Other tracker
systems require an adapter with equivalent identity and recovery behavior.

## Consequences

- Existing single-target dispatch and triage remain the worker interface.
- Investigation tickets become visible before applicability is known,
  including targets later found not affected.
- The workflow adapter owns branch discovery, case-wide bounds, accounting
  and tracker reconciliation, with durable recovery still required.
- Branch tickets isolate existing per-subject concurrency, but their creation
  cannot establish complete target coverage or successful triage.
- Coding, merge and issue closure retain their existing authorization gates;
  tracker-first dispatch grants none of those actions.
