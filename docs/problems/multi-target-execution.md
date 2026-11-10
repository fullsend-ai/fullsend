---
title: Multi-target execution
---

# Multi-target execution

One request may require checking several repositories or maintained branches.
Launching several CI jobs does not define which targets are required, what
authorizes each action or whether every result has been collected.

The [repository coordination model](agent-architecture.md#interaction-model-the-repo-as-coordinator)
keeps authority in repository-visible controls. The
[infrastructure discussion](agent-infrastructure.md) considers where workers
execute. Multiple targets need independent source and evidence identities,
without making branch discovery an authorization decision.

## Failure cases

- Two branches share one subject identity and collide under its concurrency
  key, causing one branch's work to cancel or coalesce another's.
- A result from one branch is accepted for another because both name the same
  package or advisory.
- Several targets succeed while one never starts, yet the request is reported
  complete.
- Discovery finds a branch that the originating actor cannot authorize.
- A mapping change adds a required target while old workers still run.
- A ticket is created before its acknowledgement is retained, and retrying
  creates another ticket for the same branch.
- Creating all tickets is mistaken for successful assessment, or a failed
  worker is described as an affected finding.

## Options and trade-offs

### Tracker-first dispatch

A trusted adapter creates or reuses one investigation ticket per authorized
source target, then invokes normal single-target triage. Existing workers and
per-subject concurrency remain reusable. Tickets become visible before an
applicability verdict, including targets later found not affected. The adapter
still owns complete discovery, case-wide bounds, recovery and aggregation.

### Adapter-led workers before tracker creation

An adapter can dispatch independent assessment workers and create tickets only
after collecting results. This reduces unnecessary investigation tickets, but
needs a pre-tracker source/result contract and still owns durable coordination.
An uncertain or failed worker needs an explicit investigation handoff.

### Fullsend-native multi-target execution

A host-admitted plan could give all consumers a reusable target, isolation and
collection contract. That requires a platform interface and conformance tests.
The value must justify its implementation rather than merely duplicating a
single consumer's adapter.

### Handwritten CI matrices

Matrices launch isolated jobs with familiar infrastructure. They do not supply
maintained-target mapping, cross-run recovery or complete coverage accounting.
They can implement execution underneath an adapter, rather than replace one.

## Decision and acceptance

[ADR 0167](../ADRs/0167-use-tracker-first-dispatch.md) chooses tracker-first
dispatch for the initial route. Durable recovery is a separate requirement
tracked in [#9253](https://github.com/fullsend-ai/fullsend/issues/9253).

Acceptance must demonstrate create/reuse before assessment, one branch per
tracker, authorized single-target triage with exact source binding, and
independent subjects for branches sharing one originating case. Inject lost
Jira acknowledgements, dispatch failure, uncertain results and worker loss.
Recovery must reuse the existing ticket and keep applicability unknown until
triage establishes a supported finding. Add a required branch during an old
attempt and verify accounting reopens without duplicating existing tickets or
resetting consumed work bounds. A ticket's existence cannot imply triage ran,
all targets were discovered or remediation completed.

## Open questions

- Which supported dispatch path can authorize adapter-scheduled triage while
  preserving the configured actor minimum and target permissions?
- Which existing single-target intake fields and checkout controls need an
  adapter for each source forge?
- How should adapters expose incomplete or conflicting discovery coverage?
- How should target trackers record superseded attempts and operator retries
  without losing earlier evidence or repeating ambiguous publications?
