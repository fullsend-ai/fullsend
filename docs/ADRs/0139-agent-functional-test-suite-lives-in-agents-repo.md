---
title: "139. The agent functional-test suite lives in the agents repo"
status: Accepted
relates_to:
  - testing-agents
topics:
  - testing
---

# 139. The agent functional-test suite lives in the agents repo

Date: 2026-10-11

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

Supersedes [ADR 0052](0052-functional-tests-for-agent-pipelines.md) where it
places the functional-test suite in this repo's `eval/` directory and runs it
on changes to `eval/` and `internal/scaffold/`. The test category itself, and
its place in the test pyramid
([testing-agents.md](../problems/testing-agents.md)), are unchanged.

Since then, agent definitions moved to
[fullsend-ai/agents](https://github.com/fullsend-ai/agents), which grew its
own functional-test suite for every agent: `release: true` smoke cases
(the release tier) and the full case set (the full tier). The `eval/triage`
suite here duplicated cases that agents already holds, ran up to three times
per change, and failed mostly on infrastructure rather than on regressions
([#8035](https://github.com/fullsend-ai/fullsend/issues/8035)).

## Decision

Agent functional tests live in fullsend-ai/agents. This repo keeps no
functional-test suite of its own.

The fullsend release gate calls the agents functional-test workflow and runs
the release tier: each agent's `release: true` case(s). The full tier runs
nightly in the agents repo against fullsend `main`
(`functional-tests-nightly.yml`) and in the agents merge queue.

## Consequences

- A change to `internal/scaffold/` or `fullsend run` no longer gets a
  functional-test run on its PR; the agents nightly catches a regression it
  causes within a day.
- Releases still exercise every agent end to end, through one release-tier
  case per agent.
- Adding or changing a functional-test case is an agents-repo PR; `make test`
  here no longer lints eval cases.
- The `EVAL_GH_TOKEN` and `E2E_GCP_*` secrets stay in this repo, for the
  release gate's call into agents.
