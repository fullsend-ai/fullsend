# Architecture

What are the components of the agent execution stack?

This document names the parts of the system without deciding how they work. It establishes shared vocabulary that the [problem documents](problems/) can reference when discussing design choices. Each component gets a responsibility statement and open questions — implementation decisions live in the problem docs and will crystallize into [ADRs](ADRs/) as they mature.

This is not exhaustive. Not every problem doc maps to a component here, and not every component here has a corresponding problem doc yet.

## Execution Stack

Five components form the vertical execution path from event to agent action:

1. **Agent Dispatch and Coordination Layer** — translates events into agent tasks
2. **Agent Infrastructure** — provisions and runs agent workloads
3. **Agent Sandbox** — enforces isolation (network, filesystem)
4. **Agent Harness** — assembles configuration and context (skills, prompts, tools)
5. **Agent Runtime** — the LLM in execution

Control flows strictly downward through this stack. No layer may influence, configure, or depend on layers above it. This is the execution stack's primary structural invariant. (See [ADR 0005](ADRs/0005-unidirectional-control-flow.md).)

The remaining components described in this document (Policy Store, Intent Source, Identity Provider, Observability, Agent Registry) are cross-cutting concerns that feed into the stack from the side. They are not part of the vertical control flow, but they follow the same principle: no component within the stack can modify the cross-cutting systems that constrain it.

## Agent Infrastructure

The compute and orchestration layer that runs agent workloads. Responsible for provisioning, scheduling, scaling, and lifecycle management of agent execution environments.

The initial execution platform is **GitHub Actions**. Enrolled repos contain a thin workflow stub that calls a reusable workflow in the org's `.fullsend` repo. The reusable workflow passes the raw forge event to a platform-agnostic entry point, which consults `.fullsend` config to select a sandbox policy, harness configuration, and agent runtime, then orchestrates their launch. Nothing below the infrastructure layer knows it is running on GitHub Actions. **Kubernetes**, **GitLab CI**, and **Forgejo Runners** are anticipated future platforms; when added, only the trigger and infrastructure layers change. (See [ADR 0007](ADRs/0007-github-actions-initial-execution-platform.md).)

Infrastructure platform choice and configuration are specified in the org's `<org>/.fullsend` repo. (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Open questions:**

- Can different agent types (short-lived review vs. long-running implementation) run on different infrastructure?
- Who in the org owns and operates this, and how does it relate to existing platform or CI ownership?
- What are the concrete resource limits (runner size, timeout, concurrency) that should be set as defaults for GitHub Actions runners?

## Agent Sandbox

The isolation boundary around a running agent. Responsible for filesystem access control and network regulation — ensuring an agent can only reach what it's authorized to reach and cannot affect other agents or systems outside its boundary.

The sandbox is a security primitive. Its job is containment: if an agent is compromised or misbehaves, the blast radius is limited to what the sandbox permits.

Sandbox defaults (network policy, filesystem restrictions) are configured in the org's `<org>/.fullsend` repo and can be overridden per-repo. (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Open questions:**

- What is the right isolation level — process, container, microVM, or separate cluster? (See [agent-infrastructure.md](problems/agent-infrastructure.md) and [security-threat-model.md](problems/security-threat-model.md).)
- How granular is network regulation? Allowlist of endpoints, or coarser controls?
- Does the sandbox provide a pre-built environment (tools, language runtimes, repo clones), or does the agent set up its own workspace within the sandbox?
- Is the sandbox the same for all agent roles, or does each role get a differently-scoped sandbox?

## Agent Harness

The configuration and context layer that prepares an agent for its task. Responsible for providing skills, system prompts, codebase context, tool definitions, and behavioral instructions to the agent runtime.

The harness is what makes a generic LLM into a specific agent with a specific role. It assembles what the agent needs to know and what it's allowed to do before the agent starts working.

The harness draws its configuration from the org's `<org>/.fullsend` repo — skills, workflow definitions, and agent behavioral instructions are assembled from the layered config (fullsend defaults < org config < per-repo overrides). (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Open questions:**

- Does the harness live inside the sandbox (configuring the agent from within its isolation boundary) or outside it (preparing the environment before the agent starts)?
- How is codebase context assembled? (See [codebase-context.md](problems/codebase-context.md).)
- How do we version and test harness configurations? (See [testing-agents.md](problems/testing-agents.md).)

## Agent Runtime

The agent itself in execution — the LLM, its tool-use loop, and the interface to the model provider. Responsible for performing the assigned task within the boundaries set by the sandbox and the configuration provided by the harness.

This is the thing that actually reasons and acts. Everything else in this document exists to support, constrain, or coordinate it.

**Open questions:**

- Is the runtime a single model call, a loop (plan-act-observe), or something more structured?
- How does the runtime interact with the sandbox boundaries — does it know what it can't do, or does it just hit walls?
- How do we swap model providers or versions without changing the rest of the stack?
- What is the interface between the harness and the runtime? (A system prompt? A configuration file? An API contract?)

## Agent Identity Provider

The system that gives agents credentials to act on external services. Responsible for issuing, scoping, rotating, and revoking the identities agents use to interact with the hosting forge, container registries, and other APIs. Credential issuance is deterministic code; `forgekit` handles forge-portable token generation (e.g., GitHub App installation tokens vs. GitLab project access tokens). The sandbox makes scoped tokens available to the layers it controls (harness and runtime). (See [ADR 0006](ADRs/0006-forge-abstraction-layer.md).)

Identity is not the same as trust. An agent's identity lets it authenticate to external services; the trust model is defined by repository permissions and CODEOWNERS, not by which credentials the agent holds. (See [agent-architecture.md](problems/agent-architecture.md) — "trust derives from repository permissions, not agent identity.")

**Open questions:**

- What identity model fits best — separate bot accounts per agent role, a single bot account with role metadata, GitHub App installations, or something else? (See [agent-architecture.md](problems/agent-architecture.md).)
- How are credentials scoped so that agents only get the permissions they need?
- How are credentials rotated and revoked, and who has authority to do that?
- Does the identity provider integrate with existing secrets management, or is it a new system?

## Forge Abstraction Layer

The boundary between fullsend's deterministic code and the hosting forge (GitHub, GitLab, Forgejo). A shared library (`forgekit`) provides a uniform interface to forge operations — issues, pull/merge requests, labels, status checks, and code ownership queries.

The abstraction applies to two specific code paths: the **agent runtime wrapper** (the script inside the sandbox that configures the harness and launches the agent) and **skill scripts** (deterministic scripts embedded in fullsend-shipped skills). These are the code paths fullsend controls and must be forge-portable. Agents themselves use native forge CLIs (`gh`, `glab`, etc.) — LLMs are naturally effective at adapting to the forge they're working with. (See [ADR 0006](ADRs/0006-forge-abstraction-layer.md).)

**Open questions:**

- What is the right implementation language for the library?
- How does the library authenticate — does it receive credentials from the Agent Identity Provider, or discover them from the environment?
- How are forge-specific features that have no cross-forge equivalent handled — silently ignored, explicitly errored, or degraded gracefully?

## Agent Dispatch and Coordination Layer

The mechanism that assigns work to agents and prevents conflicts. Responsible for translating triggers (forge events, schedules, manual requests) into agent tasks and ensuring two agents don't work the same problem simultaneously.

The existing design principle is that [the repo is the coordinator](problems/agent-architecture.md#interaction-model-the-repo-as-coordinator) — branch protection, CODEOWNERS, status checks, and forge events provide coordination without a central orchestrator. The agent dispatch and coordination layer may be nothing more than the glue that connects forge webhooks to agent infrastructure. Or it may need to be more.

For the initial implementation, GitHub Actions' event system (`on:` triggers in workflow YAML) serves as the trigger layer — translating forge events into agent workload invocations. This collapses the trigger and infrastructure into a single system initially; a future platform (Kubernetes, GitLab CI, or Forgejo Runners) would decouple them. (See [ADR 0007](ADRs/0007-github-actions-initial-execution-platform.md).)

**Open questions:**

- Is the forge's event system sufficient, or do we need additional coordination logic (e.g. to prevent two implementation agents from picking up the same issue)?
- How does work assignment interact with the backlog/priority agent described in [agent-architecture.md](problems/agent-architecture.md)?
- What happens when work needs to be cancelled, retried, or reassigned?
- Does the coordinator need state (a queue, a lock, a claim system), or can it be stateless and event-driven?

## Policy Store

Where agent behavioral rules live. Responsible for holding autonomy levels, review requirements, allowed operations, and escalation rules — the configuration that governs what agents may do.

Policy is distinct from the harness (which configures *how* an agent works) and from intent (which defines *what* work is authorized). Policy defines the *boundaries* of agent behavior — what an agent is allowed to do regardless of what it's asked to do.

The org's `<org>/.fullsend` repo is the natural home for policy configuration — org-wide guardrails, per-repo autonomy levels, and escalation rules all live there, governed by the org's own CODEOWNERS and review process. (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Open questions:**

- How is policy versioned, and how do we ensure agents run under the correct policy version?
- Who can change policy, and what approval process governs policy changes? (See [governance.md](problems/governance.md).)
- How does policy interact with the autonomy spectrum — is the auto-merge vs. escalate decision a policy setting? (See [autonomy-spectrum.md](problems/autonomy-spectrum.md).)

## Intent Source

The system that provides authorized intent for agent work. Responsible for representing what changes are wanted, who authorized them, and at what tier of approval.

Intent answers the question "should this change exist?" before anyone asks "is this change correct?" Without authorized intent, an agent has no basis for deciding what to work on or whether its output matches what was asked for.

The org's `<org>/.fullsend` repo holds the pointer to the intent source (e.g., `intent_repo: <org>/features`), so tooling discovers where intent lives without hardcoding. (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Open questions:**

- What is the right representation — forge issues, a dedicated intent repo, RFCs, or tiered combinations? (See [intent-representation.md](problems/intent-representation.md).)
- How do agents verify that intent is authentic and hasn't been tampered with?
- How do different tiers of intent (standing rules, tactical issues, strategic features) map to different authorization requirements?
- How does intent interact with the "try it" phase — agents building exploratory drafts before authorization? (See [intent-representation.md](problems/intent-representation.md).)

## Observability

The logging, tracing, and audit layer for agent actions. Responsible for making every agent action attributable, traceable, and reviewable — both for debugging failures and for security auditability.

Observability is a cross-cutting concern that touches every other component. Each component produces signals; this component is responsible for collecting, storing, and making them useful.

**Open questions:**

- What signals matter most — cost, latency, token usage, action logs, decision traces, or something else?
- How do we balance detailed tracing (useful for debugging) with the volume of data agents will produce?
- What is the retention and access model for agent logs? Who can see what?
- How does observability interact with the security requirement that "every action is logged, attributable, and reviewable"? (See [security-threat-model.md](problems/security-threat-model.md).)
- Is there a real-time monitoring requirement (agent is stuck, agent is behaving anomalously), or is observability primarily forensic?

## Agent Registry

The catalog of available agent roles and their configurations. Responsible for defining what agent types exist, what capabilities each has, and how they are instantiated.

The registry is the bridge between the abstract roles defined in [agent-architecture.md](problems/agent-architecture.md) (correctness agent, intent alignment agent, etc.) and the concrete runtime configurations that the harness uses to set up each agent.

Fullsend provides a base set of agent definitions. The org's `<org>/.fullsend` repo extends this with org-specific agents in its `agents/` directory, following the inheritance model: fullsend defaults < org config < per-repo overrides. (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Open questions:**

- How are new agent roles added, tested, and promoted to production? (See [testing-agents.md](problems/testing-agents.md).)
- Does the registry include version information, so we can roll back to a previous agent configuration?
- How does the registry relate to the policy store — does policy reference registry entries, or are they independent?
