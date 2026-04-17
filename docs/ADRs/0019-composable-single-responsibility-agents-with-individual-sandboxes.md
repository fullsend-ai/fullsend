---
title: "19. Composable single-responsibility agents with individual sandboxes"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
  - security-threat-model
topics:
  - sandbox
  - security
  - isolation
  - composability
---

# 19. Composable single-responsibility agents with individual sandboxes

Date: 2026-04-15

## Status

Accepted

## Context

The inter-stage decomposition (triage, code, review as separate pipeline stages)
is established ([ADR 0002](0002-initial-fullsend-design.md),
[ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md)). Each stage
involves multiple steps — triage includes deduplication, completeness
assessment, priority, reproducibility, and dependency analysis; review includes
security, linting, complexity, architecture, performance, and documentation
checks. This ADR decides how those steps are structured and isolated within
a stage.

Two open questions frame this decision: whether the sandbox is the same for all
agent roles or each gets a differently-scoped sandbox
([architecture.md](../architecture.md)), and whether agents should be isolated in
separate execution environments to limit lateral movement
([security-threat-model.md](../problems/security-threat-model.md)).

The [agent-scoped-tools-triage experiment](https://github.com/fullsend-ai/fullsend/pull/123)
validated per-agent sandboxing: three specialized triage agents
(duplicate-detector, completeness-assessor, reproducibility-verifier), each in
its own OpenShell sandbox with tailored L7 network policies.

## Options

### Option A: Single sandbox per stage

One agent and one sandbox per pipeline stage. The agent handles all steps within
that stage — either sequentially in a single context window, or by delegating to
internal child agents for better context management. Either way, the sandbox
boundary is the same: permissions must cover the union of what all steps need.
A single triage sandbox requires issues read access, web access (for
completeness assessment), and local filesystem access (for reproducibility) —
even though only one step needs each capability.

Simpler to operate: fewer sandboxes, fewer policies, fewer cold starts. Whether
the agent delegates internally or not is a context management choice, not a
security one — child agents inherit the parent's sandbox permissions, so the
blast radius is identical.

### Option B: One agent per step, one sandbox per agent

Each step gets its own agent, its own harness, and its own sandbox with a
tailored policy. Within triage: a duplicate-detector agent in a sandbox with
read-only issues API; a completeness-assessor agent in a sandbox with read-only
issues plus web access; a reproducibility-verifier agent in a sandbox with
read-only issues plus local filesystem. Each sandbox policy enforces least
privilege for that specific step.

More resource-intensive: N agents per stage means N sandbox instances, N
cold starts, N teardowns. Requires a sandbox runtime supporting per-instance
policy (e.g., OpenShell with L7 network policies).
[PR #123](https://github.com/fullsend-ai/fullsend/pull/123) validated this
model in the triage stage.

## Decision

Each step within a stage gets its own agent, its own harness, and its own
sandbox with policies designed for that step's responsibility.

**Decomposition:** each stage is made up of multiple composable agents — one
agent per step — rather than one monolithic agent handling all steps. A triage
stage has separate agents for duplicate detection, completeness assessment,
reproducibility verification, and so on. A review stage has separate agents for
security, linting, complexity, architecture, and performance. Agents are
composable building blocks — an organization can assemble different combinations
of agents into a pipeline stage, add new agents for new steps, or replace
existing ones without affecting others. "Single responsibility" means one step,
not minimal prompt size. The decomposition in
[agent-architecture.md](../problems/agent-architecture.md) provides the current
granularity.

**Isolation:** because each agent handles only one step, its sandbox policy can
be as restrictive as possible — granting only what that step requires. A
duplicate-detector gets read-only access to the issues API; a
completeness-assessor gets read-only issues plus outbound HTTPS; a
reproducibility-verifier gets read-only issues plus local filesystem access. No
agent receives permissions it does not need. This enforces least privilege at
the step level, mitigates lateral movement between agents
([Threat 5](../problems/security-threat-model.md)), limits the blast radius of
prompt injection (a compromised agent can only reach what its policy allows),
and contains credential scope
([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)).

Scripted pipelines ([ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md))
coordinate agents within a stage — the pipeline declares which agents run, and
the sandbox runtime provisions each one. Skill reusability is orthogonal to
sandbox boundaries — skills are portable definitions that work regardless of
isolation model.

## Consequences

- The open questions in [architecture.md](../architecture.md) and
  [security-threat-model.md](../problems/security-threat-model.md) regarding
  per-role sandboxes and agent isolation are answered.
- Agents are composable: organizations can assemble, replace, or extend agents
  within a stage without affecting other agents or their sandbox policies.
- Adding a new capability to a stage means adding a new agent, skill, and
  sandbox policy — not modifying an existing agent's permissions.
- Sandbox policy authoring scales linearly with agent count. Each new agent
  requires a reviewed sandbox policy.
- Infrastructure cost increases: N agents × M concurrent tasks × cold start per
  sandbox. Parallel provisioning (supported by scripted pipeline stages)
  mitigates latency but not total resource consumption.
- A sandbox runtime supporting per-instance policy is a hard dependency.
  Degradation strategy for environments without such a runtime is deferred.
