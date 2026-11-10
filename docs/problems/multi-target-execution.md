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
execute. Multi-target execution needs to preserve those boundaries while
keeping each worker's source and evidence independent.

## Failure cases

- Two branches share one event or tracker and collide under its concurrency
  key, causing one branch's work to cancel or coalesce another's.
- A result from one branch is accepted for another because both name the same
  package or advisory.
- Several targets succeed while one never starts, yet the request is reported
  complete.
- Discovery finds a branch that the originating actor cannot authorize.
- A source or mapping change adds a required target while old workers are
  still running.
- Retrying or replacing a plan silently resets its consumed work bounds.

## Options and trade-offs

### Handwritten CI matrices

Matrices can launch isolated jobs with familiar infrastructure. Each workflow
must still define target identity, authorization, limits, result binding and
coverage accounting. A matrix's successful exit is not proof that the intended
target set was complete.

### Model-led coordination

A model can interpret a request and propose source targets or investigation
steps. Its choices cannot grant execution permission, establish source identity
or replace deterministic accounting of missing work.

### Host-admitted execution plans

A trusted adapter can propose a target set for host validation. An immutable
plan can bind each child to source, scope and policy while reusing existing
dispatch mechanisms. This needs a versioned interface and conformance tests;
it does not itself provide durable ownership or external-write recovery.

## Decision and acceptance

[ADR 0167](../ADRs/0167-execute-authorized-multi-target-plans.md) chooses
host-admitted execution plans. Durable recovery remains a separate proposal
tracked in [#9253](https://github.com/fullsend-ai/fullsend/issues/9253).

Acceptance must include distinct branches sharing one event, a wrong-target
result, exhausted and missing children, and a new plan adding a required branch
while an old worker is still alive. Every required target stays visible.
Complete accounting remains distinct from successful assessment, publication
and domain completion. A domain adapter may request investigation after
failure without turning it into an affected verdict or coding permission.

## Open questions

- Which existing dispatch interfaces can carry the admitted plan and child
  identities without introducing a second scheduler?
- What versioned plan/result schema and backend conformance tests are needed?
- How should adapters report incomplete or conflicting discovery coverage?
- Which evidence can be reused after a plan changes, and what revalidation
  must precede acceptance?
