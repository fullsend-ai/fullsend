---
title: "5. Unidirectional control flow through the execution stack"
status: Proposed
relates_to:
  - agent-architecture
  - agent-infrastructure
  - security-threat-model
topics:
  - architecture
  - security
  - portability
---

# 5. Unidirectional control flow through the execution stack

Date: 2026-03-27

## Status

Proposed

## Context

The [architecture document](../architecture.md) defines five components that
form the execution stack — the vertical path from event to agent action:

1. **Agent Dispatch and Coordination Layer** — translates events into agent tasks
2. **Agent Infrastructure** — compute and orchestration that runs agents
3. **Agent Sandbox** — isolation boundary (network, filesystem)
4. **Agent Harness** — configuration and context layer (skills, prompts, tools)
5. **Agent Runtime** — the LLM in execution

Other components (Policy Store, Intent Source, Identity Provider, Observability,
Agent Registry) exist alongside the stack but are not part of its vertical
control flow.

Today, the architecture document names these components and their
responsibilities but does not state the structural relationship between them.
Without an explicit rule, it is ambiguous whether a lower layer may influence a
higher one — whether an agent runtime can modify its own harness, or a harness
can reconfigure its sandbox.

This matters for four reasons:

**Security.** A compromised agent runtime must not be able to weaken its own
sandbox. A poisoned skill must not be able to expand network access. Each layer
constrains the layers below it; those constraints must be immutable from below.
This directly supports the threat model's top priority (external prompt
injection) by ensuring that an injected instruction cannot escalate the agent's
own capabilities.

**Portability.** Each layer can be swapped independently when control flows in
one direction. Replacing the infrastructure layer (GitHub Actions to Kubernetes)
requires re-implementing only that layer's interface to the layer below it.
Nothing in the sandbox, harness, or runtime changes. This is critical because
we intend to support multiple execution platforms.

**Testability.** Each layer can be tested in isolation by mocking the layer
above it. A harness test does not need real infrastructure; a sandbox test does
not need a real dispatch layer.

**Reasoning.** When debugging or auditing, control flow traces in one direction.
You never have to ask "did the agent change its own sandbox config?" or "did a
skill modify the dispatch layer?" The answer is always no.

## Options

### Option A: Unidirectional control flow (strict top-down)

Control flows strictly downward through the execution stack. No layer may
influence, configure, or depend on layers above it. A layer that needs something
not provided by the layer above must fail or escalate — it cannot
self-provision.

**Trade-offs:**
- Eliminates an entire class of security vulnerabilities (privilege escalation
  from within the stack).
- Simple to reason about, audit, and test.
- Agents that need additional capabilities must fail and surface the gap, which
  is the correct behavior in a zero-trust system.
- Slightly less flexible: an agent cannot dynamically request additional tools
  or network access mid-execution.

### Option B: Bidirectional control flow (allow upward requests)

Lower layers may request changes from higher layers through a controlled
protocol — for example, the agent runtime could request an additional tool from
the harness, or the harness could request expanded network access from the
sandbox.

**Trade-offs:**
- More flexible: agents can adapt to unanticipated needs at runtime.
- Introduces a request/approval protocol between layers, adding complexity.
- Every upward channel is an attack surface. A compromised runtime could use the
  request mechanism to escalate its own capabilities.
- Violates zero-trust: the system must evaluate whether to grant requests from a
  potentially compromised component.
- Makes reasoning harder: control flow becomes a graph, not a line.

**Why we reject this:** The security risk and complexity outweigh the
flexibility gain. In a zero-trust model, a layer that can request changes to its
own constraints is a layer that can potentially weaken its own constraints. The
correct response to insufficient capabilities is failure and escalation to a
human or a higher-level process — not self-provisioning.

## Decision

Control flows strictly downward through the execution stack:

```
Agent Dispatch → Agent Infrastructure → Agent Sandbox → Agent Harness → Agent Runtime
```

No layer may influence, configure, or depend on layers above it:

- The **agent runtime** cannot modify the harness (its own system prompt,
  skills, tool definitions).
- The **agent harness** cannot modify the sandbox (network policy, filesystem
  restrictions).
- The **agent sandbox** cannot modify the infrastructure (compute resources,
  scheduling).
- The **agent infrastructure** cannot modify the agent dispatch and coordination
  layer (what events cause agent invocations).

### Control flow vs. data flow

The unidirectional rule applies to **control flow** — configuration, behavior,
and constraints. It does not prohibit upward **data flow**:

- **Prohibited (upward control flow):** A lower layer modifying the
  configuration, behavior, or constraints of a higher layer. The agent runtime
  cannot add tools to its own harness. The sandbox cannot expand its own network
  policy. The harness cannot reconfigure infrastructure scheduling.
- **Permitted (upward data flow):** Telemetry, logs, traces, and failure signals
  flowing from any layer to Observability. Exit codes and error messages
  indicating failure. Forge comments explaining what an agent could not do.

This distinction matters because Observability inherently collects data from
every layer in the stack — that is its job. The rule prohibits a layer from
*changing* layers above it, not from *emitting signals* that layers above
observe. A runtime that writes a structured log entry is emitting data upward; a
runtime that modifies its own system prompt is exerting control upward. Only the
latter is prohibited.

Each component interface is a one-way contract: the layer above provides
configuration, the layer below consumes it.

Cross-cutting concerns (Observability, Identity Provider, Policy Store) sit
alongside the stack and feed into layers from the side. They too follow the
unidirectional principle: an agent runtime cannot modify its own policy,
identity, or observability configuration.

## Consequences

- **Each layer boundary is a security boundary.** Compromise of a lower layer
  cannot propagate upward. This is the stack's primary security property.
- **Agent runtimes that need capabilities not provided by their harness or
  sandbox must fail or escalate to humans.** They cannot self-provision, and
  they cannot request the missing capability from the harness — that would be
  upward control flow. Instead, the runtime fails and the failure is surfaced
  to humans through observability and the forge (e.g., posting a comment
  explaining what it could not do and why). The human decides whether to
  reconfigure the harness or sandbox for next time. This forces capability
  gaps to surface during harness design and testing rather than at runtime
  through ad-hoc self-provisioning. (See
  [dual-interpretation escalation](../problems/code-review.md#dual-interpretation-escalation)
  for the pattern of structured escalation to humans, and
  [agent-architecture.md](../problems/agent-architecture.md#how-deadlocks-are-resolved)
  for the principle that persistent disagreement escalates to humans.)
- **Swapping any layer requires only re-implementing that layer's interface to
  the layer below it.** Moving from GitHub Actions to Kubernetes changes the
  infrastructure layer; the sandbox, harness, and runtime are unaffected.
- **Cross-cutting concerns follow the same principle.** The Policy Store feeds
  policy into the harness and sandbox from the side, but the runtime cannot
  write back to the Policy Store. Observability collects signals from every
  layer but no layer can modify its own observability configuration.
- **The architecture document gains an overarching structural principle** that
  the individual component descriptions can reference.
