---
title: "22. Harness-level output schema enforcement"
status: Accepted
relates_to:
  - security-threat-model
  - agent-architecture
topics:
  - security
  - output-validation
  - harness
---

# 22. Harness-level output schema enforcement

Date: 2026-04-15

## Status

Accepted

## Context

[ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md) requires
that agent outputs follow a structured contract so pipelines can evaluate
conditions and pass data between stages. It does not say where that contract is
enforced. [ADR 0016](0016-unidirectional-control-flow.md) establishes that
control flows strictly downward through the execution stack and that the
harness defines what the runtime can do — the runtime cannot modify its own
harness. [ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)
establishes that agents run inside sandboxes with restricted networking, with
host-side pre-scripts and post-scripts — both part of the harness — handling
data prefetch and output application. This post-script pattern already
processes agent output in a controlled, deterministic environment on the host.

The [threat model](../problems/security-threat-model.md) identifies
agent-to-agent prompt injection (Threat 5) as a distinct risk: a compromised
agent's output is consumed by downstream agents. Zero trust between agents
means every agent's output must be validated regardless of source. This
decision addresses where and how that structural validation occurs.

## Options

### Option A: Harness post-script enforcement

A harness post-script validates agent output against a declared schema on the
host, after the runtime finishes and before output reaches the pipeline or
downstream consumers. The schema is part of harness configuration — immutable
from the runtime (ADR 0016), governed by CODEOWNERS. Non-compliant output
triggers a retry: the harness re-invokes the runtime with the schema violation
fed back. Retries are capped; exhaustion is a hard failure.

**Trade-offs:** Runs on the host in a controlled environment — the runtime
cannot bypass it. Every agent is validated, not just pipeline endpoints.
Retries cost time and money.

### Option B: Pipeline-level enforcement

The pipeline executor validates agent output between stages, after it has left
the sandbox.

**Trade-offs:** Simpler to implement — one validation point per pipeline. But
single-agent invocations (no pipeline) skip validation entirely. Malformed
output has already exited the sandbox before validation occurs, widening the
window for downstream consumption of bad data. Does not satisfy zero trust:
the pipeline must be aware of every agent's schema.

### Option C: Dedicated validator agent

A separate LLM-based agent checks each agent's output for correctness and
safety.

**Trade-offs:** Can perform semantic checks beyond structural validation. But
adds latency, cost, and a new attack surface — the validator itself can be
compromised or manipulated. Non-deterministic: the validator may disagree with
itself across runs. Structural validation does not require an LLM.

### Option D: No enforcement (trust agent output)

Agents are expected to produce correct output via prompt engineering alone.

**Trade-offs:** Zero overhead. But prompt engineering cannot guarantee output
structure — LLMs produce non-compliant output unpredictably. Violates zero
trust. A compromised agent's malformed output propagates silently.

## Decision

The harness validates every agent's output against a declared JSON schema
via a post-script on the host, after the runtime finishes and before the
output reaches the pipeline or downstream consumers. The mechanism:

1. The harness configuration declares an output schema for the agent.
2. After the runtime produces output, a harness post-script on the host
   validates it against the schema.
3. If validation fails, the harness feeds the violation back to the runtime
   and re-invokes it, up to a configured retry limit.
4. If retries are exhausted, the agent fails. No unvalidated output is
   emitted. The pipeline receives a failure signal, not silently bad data.

Schema definitions are part of the harness configuration — governed by
CODEOWNERS and immutable from the runtime per ADR 0016. Specific per-agent
schemas are deferred to normative specs
([ADR 0015](0015-normative-specifications-directory.md)).

This extends the post-script pattern established in ADR 0017: where ADR 0017
uses post-scripts for application-level actions (applying labels, posting
comments), this ADR adds structural schema validation as a prior step. Schema
validation gates the output before any application-level post-scripts consume
it.

## Consequences

- **Schema validation is a security layer, not the security layer.** It
  catches crude compromises (wrong format, missing fields) but not
  sophisticated ones (conformant structure, malicious content). Content-level
  sanitization of string fields — including Unicode injection payloads in
  structured output — is a separate concern. This is one layer in a
  defense-in-depth model.
- **Retry exhaustion is a hard failure.** The system never falls back to
  emitting unvalidated output. This trades availability for integrity —
  acceptable in a zero-trust model where silent bad data is worse than a
  visible failure.
- **Schema and prompt must be versioned together.** If the schema changes but
  the agent's prompt still describes the old format, the agent will fail
  validation on every attempt. Both artifacts live in the harness
  configuration and should be updated atomically.
- **Retries have a cost.** Each retry is a full LLM invocation. The retry
  budget is a trade-off between resilience (more retries tolerate transient
  non-compliance) and cost (each retry costs time and money). The budget
  should be low — 1-2 retries — because a well-prompted agent with a clear
  schema should comply on the first attempt; repeated failure suggests a
  deeper problem that more retries will not fix.
- **Every agent is validated, not just pipeline endpoints.** In a multi-agent
  pipeline where parallel agents feed into an aggregator, each agent's output
  is schema-checked independently before the aggregator sees it.
