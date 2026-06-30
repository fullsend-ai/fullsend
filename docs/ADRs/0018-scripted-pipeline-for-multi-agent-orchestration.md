---
title: "18. Scripted pipeline for multi-agent orchestration"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - orchestration
  - determinism
  - pipeline
---

# 18. Scripted pipeline for multi-agent orchestration

Date: 2026-04-08

## Status

Accepted

<!-- Once this ADR is Accepted, its content is frozen. Do not edit the Context,
     Decision, or Consequences sections. If circumstances change, write a new
     ADR that supersedes this one. Only status changes and links to superseding
     ADRs should be added after acceptance. -->

## Context

The [agent-scoped-tools-triage experiment](https://github.com/fullsend-ai/fullsend/pull/123)
demonstrated a coordinator agent (an LLM) orchestrating multiple specialized
subagents. The experiment surfaced a fundamental reliability problem: **the
coordinator agent did not always invoke all the subagents declared in its
harness.** It sometimes skipped steps, improvised by doing work itself instead
of delegating, or changed execution order unpredictably. Stronger prompting
reduced but did not eliminate this behavior.

This is not a bug — it is inherent to using an LLM as an orchestrator. An LLM
optimizes for plausible next tokens, not for deterministic workflow execution.
When a multi-agent pipeline must be reproducible (e.g., every issue gets
duplicate detection, completeness assessment, and reproducibility verification),
the orchestration layer must guarantee that each stage runs.

[ADR 0016](0016-unidirectional-control-flow.md) establishes that control flows
strictly downward through the execution stack. The orchestration mechanism
sits in the **Agent Dispatch and Coordination Layer** — above all agents — and
must enforce which agents run, in what order, and under what conditions.

## Options

### Option A: Agent-driven orchestration (coordinator agent)

A top-level LLM agent reads a task description and decides which subagents to
invoke, in what order, and whether to skip steps based on context. This is the
approach tested in [PR #123](https://github.com/fullsend-ai/fullsend/pull/123).

**Trade-offs:**

- Flexible: the coordinator can adapt to novel situations and skip irrelevant
  steps.
- Non-deterministic: the coordinator may skip required steps, change order, or
  do work itself instead of delegating. Prompt engineering reduces but cannot
  eliminate this.
- Not auditable as a workflow: there is no static definition of what should
  run — only a prompt suggesting what the agent ought to do.
- Harder to test: verifying the pipeline requires running the LLM and checking
  whether it made the right calls.

### Option B: Scripted pipeline

A deterministic pipeline definition declares which agents run, in what order,
with what conditions. The pipeline executor — not an LLM — enforces execution.
Individual agents remain LLM-powered, but the orchestration is code.

A pipeline must support:

- **Sequential execution**: agent B runs after agent A completes.
- **Parallel execution**: agents A and B run concurrently.
- **Conditional execution**: agent C runs only if agent B's output meets a
  condition (e.g., issue is a bug report).
- **Fan-in**: agent D runs after agents A, B, and C have all completed, with
  access to their combined outputs.

**Trade-offs:**

- Deterministic: every declared stage runs (or is explicitly skipped by a
  condition).
- Auditable: the pipeline definition is a static artifact that can be reviewed,
  versioned, and tested without invoking an LLM.
- Less flexible: adding a new stage requires changing the pipeline definition,
  not just a prompt.
- The pipeline definition is an additional artifact to maintain.

## Decision

Use scripted pipelines for multi-agent orchestration. The pipeline definition —
not an LLM — determines which agents run, in what order, and under what
conditions.

Each agent within the pipeline remains an autonomous LLM execution: it receives
inputs, performs its task, and produces structured output. The pipeline layer
handles sequencing, parallelism, conditionals, and fan-in. Agents handle
domain work.

Whether the pipeline is defined using existing infrastructure capabilities
(GitHub Actions, GitLab CI, Tekton, etc.) or a custom pipeline layer is a
separate decision, deferred to a future ADR.

## Consequences

- **Reproducibility is guaranteed by the pipeline, not by prompt engineering.**
  If a stage is declared, it runs.
- **The coordinator agent pattern is retired for workflows that require
  deterministic execution.** Ad-hoc single-agent tasks that spawn subagents
  opportunistically remain valid where determinism is not required.
- **A pipeline definition format must be chosen or designed.** This is scoped
  to a future ADR.
- **Agent outputs must follow a structured contract** so the pipeline can
  evaluate conditions and pass data between stages.
- **Testing shifts from "did the LLM call the right subagents?" to "does the
  pipeline definition express the correct workflow?"** — a simpler, more
  reliable verification.
