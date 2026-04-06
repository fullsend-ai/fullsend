# Glossary

Shared vocabulary for the fullsend project. Terms are defined in the context of fullsend's architecture, workflow, and security model. Each entry includes a brief definition and a pointer to the relevant document for deeper context.

This is a living document. PRs that introduce new terminology should add to this glossary as part of the change.

---

## A

### Agent Infrastructure

The compute and orchestration layer that runs agent workloads — provisioning, scheduling, scaling, and lifecycle management of agent execution environments. This is the "where do agents physically run" question. Options include GitHub Actions, Tekton pipelines, OpenShift AI (KServe), or purpose-built platforms.
See [architecture.md](architecture.md) and [agent-infrastructure.md](problems/agent-infrastructure.md).

### Agent Registry

The catalog of available agent roles and their configurations. It bridges the abstract roles defined in the agent architecture (triage, implementation, review) and the concrete runtime configurations the harness uses to instantiate each agent. Fullsend provides a base set; adopting organizations extend it via their `.fullsend` repository.
See [architecture.md](architecture.md).

### Agent Runtime

The agent itself in execution — the LLM, its tool-use loop, and the interface to the model provider. This is the thing that actually reasons and acts; everything else in the architecture exists to support, constrain, or coordinate it. Currently, Claude Code and OpenCode are the primary runtime candidates.
See [architecture.md](architecture.md) and [agent-infrastructure.md](problems/agent-infrastructure.md).

### Automerge

The end-state goal where PRs that pass all agent review and CI checks are merged to the target branch without human intervention. Automerge is gated by the [autonomy spectrum](problems/autonomy-spectrum.md) — most workflows start with human-in-the-loop approval and graduate toward automerge as confidence increases. The team has explicitly decided not to implement automerge in the MVP; agents will comment that they approve, but a human must merge.
See [autonomy-spectrum.md](problems/autonomy-spectrum.md).

## B

### Blast Radius

The scope of damage a compromised or misbehaving agent can cause. A core design constraint: every architectural decision about sandboxing, identity scoping, and network policy is evaluated by asking "what is the blast radius if this agent is compromised?" Minimizing blast radius is the primary goal of the sandbox layer.
See [security-threat-model.md](problems/security-threat-model.md) and [architecture.md](architecture.md).

## D

### Debouncing

Collapsing rapid-fire events on the same issue or PR into a single agent invocation. Without debouncing, a burst of edits to an issue body could trigger multiple redundant triage runs. The [webhook + dispatch service](ADRs/0002-initial-fullsend-design.md#1-webhook--dispatch-service) is responsible for deduplicating flapping events before dispatching work to agents.
See [architecture.md](architecture.md) (building block 1).

## E

### Entry Point

The single deterministic component that receives GitHub events (webhooks) and decides which agent combination to run. Previously called **wrapper** — the rename was adopted to avoid confusion with the sandbox/wrapping layer (see [#101](https://github.com/fullsend-ai/fullsend/issues/101) for the terminology evolution). The entry point is non-AI: it is a conventional program (currently Go) that parses events, enforces ACLs on slash commands, validates label transitions, and dispatches to agent runtimes. It does not make LLM calls.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 1 and [#101](https://github.com/fullsend-ai/fullsend/issues/101).

### Escalation

Stopping automated processing and routing to a human. Escalation is triggered when agents cannot reach consensus (flapping), when trust violations are detected, when loop limits are exceeded, or when the work falls outside the authorized scope (e.g., a change that looks like a feature when only bug fixes are authorized). The escalation queue is the "dead letter queue" — the place humans monitor for items the system could not resolve autonomously.
See [autonomy-spectrum.md](problems/autonomy-spectrum.md) and [agent-architecture.md](problems/agent-architecture.md).

### Evergreen

A workflow concept where a repository automatically stays up-to-date with dependency updates (e.g., Renovate PRs) by automerging changes that consist solely of known-safe dependency bumps. Named by analogy with evergreen browsers that silently self-update. Proposed as a stretch-goal supplementary workflow.

## F

### Flapping

When agents enter a cycle of conflicting feedback that prevents convergence. Example: the security review agent rejects what the implementation agent produces to satisfy the correctness review agent, and vice versa, creating an oscillating loop. Flapping is a primary trigger for [escalation](#escalation) — after a configurable number of cycles, the system stops and routes to humans.
See [autonomy-spectrum.md](problems/autonomy-spectrum.md).

## H

### Harness

The configuration and context layer that prepares an agent for its task. The harness assembles skills, system prompts, codebase context, tool definitions, and behavioral instructions — it is what transforms a generic LLM into a specific agent with a specific role. "Harness engineering" is a relatively new term in the industry (emerging early 2026); in fullsend, the harness is a distinct architectural layer between the sandbox and the agent runtime.
See [architecture.md](architecture.md).

## I

### Identity

A distinct GitHub App installation representing a specific agent role (e.g., triage, coder, reviewer). Each agent role gets its own identity so that actions are attributable and permissions can be scoped per-role. Identity is not the same as trust — an agent's identity lets it authenticate; trust derives from repository permissions and CODEOWNERS, not from which credentials the agent holds.
See [architecture.md](architecture.md) and [agent-architecture.md](problems/agent-architecture.md).

## L

### Label State Machine

The set of valid label transitions on issues and PRs that encode workflow state. Labels like `ready-to-implement`, `ready-for-review`, `ready-for-merge`, and `requires-manual-review` are control markers that drive agent dispatch and enforce ordering. The label state machine guard validates that transitions are legal and enforces mutual exclusion — for example, starting a triage run clears downstream labels so stale state does not carry forward.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 3.

## M

### MCP Server (Model Context Protocol)

An external process that exposes tools to an agent via the Model Context Protocol. In fullsend, MCP servers are used as controlled access points outside the sandbox — for example, an MCP server that wraps the `gh` CLI can provide GitHub access while keeping credentials out of the agent's environment. MCP servers are preferred where necessary (particularly for mediating writes that the sandbox cannot natively constrain), while direct API calls or skills are preferred for static, deterministic processes to avoid performance overhead.
See [#101](https://github.com/fullsend-ai/fullsend/issues/101) and [security-threat-model.md](problems/security-threat-model.md).

### Model Armor

Google's API for prompt injection detection and defense. Referenced in the security threat model as a potential defense layer that can be placed in front of every agent to detect and block prompt injection attempts in inputs. The team is working to obtain access for evaluation.
See [security-threat-model.md](problems/security-threat-model.md).

## O

### Observability

The logging, tracing, and audit layer for agent actions. Every agent action must be attributable, traceable, and reviewable — both for debugging failures and for security auditability. In practice, this includes capturing agent JSONL logs (including "thinking" traces), converting them to human-readable format, and uploading them as artifacts. Observability is a cross-cutting concern that touches every other component.
See [architecture.md](architecture.md).

## P

### Policy Store

Where agent behavioral rules live — autonomy levels, review requirements, allowed operations, and escalation rules. Policy is distinct from the harness (which configures *how* an agent works) and from intent (which defines *what* work is authorized). Policy defines the *boundaries* of agent behavior — what an agent is allowed to do regardless of what it's asked to do. The adopting organization's `.fullsend` repository is the natural home for policy configuration.
See [architecture.md](architecture.md) and [governance.md](problems/governance.md).

## R

### Ready to Implement

A label indicating an issue has passed triage and is cleared for the implementation agent to begin work. It is a key transition point in the [label state machine](#label-state-machine) — the triage agent sets it after confirming the issue is not a duplicate, is reproducible (if applicable), is a bug (not a feature, unless features are in scope), and has sufficient detail for implementation. The implementation agent watches for this label as its trigger to begin work.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md).

### Rework Rate

A quality metric measuring how many review-fix cycles a PR goes through before reaching approval. Visible on PRs when review happens post-submission (requested changes → fix → re-review). If review moves to a pre-PR inner loop, rework rate becomes harder to measure from the PR alone and must be extracted from agent logs.

## S

### Sandbox

The isolation boundary around a running agent. Responsible for filesystem access control and network regulation — ensuring an agent can only reach what it's authorized to reach and cannot affect other agents or systems outside its boundary. The sandbox is a **security primitive**, not the entire execution environment. Its job is containment: if an agent is compromised, the blast radius is limited to what the sandbox permits. Do not confuse with the broader execution environment (which also includes the harness and runtime). [NVIDIA/OpenShell](https://github.com/NVIDIA/OpenShell) is the current leading candidate for sandbox implementation.
See [architecture.md](architecture.md) and [security-threat-model.md](problems/security-threat-model.md).

### Sidecar

An external process running alongside (but outside) the agent's sandbox that mediates access to resources the sandbox cannot natively constrain. Example: an ephemeral Git server that receives `git push` from the agent and forwards it only to the one branch the agent is authorized to write to. Unlike an MCP server (which the agent explicitly calls as a tool), a sidecar can be transparent — the agent may not know it's interacting with a mediator rather than the real service.
See [architecture.md](architecture.md) and [#101](https://github.com/fullsend-ai/fullsend/issues/101).

### Skill

A markdown file (optionally with a `scripts/` directory) that gives an agent context and tool authorizations for a specific task. Skills are not general "agent capabilities" — they are concrete, scoped instruction sets. A skill can declare which tools it is authorized to use; when a user or system approves the skill, they implicitly authorize those tools. Skills are assembled by the [harness](#harness) and are the primary mechanism for encoding agent behavior.
See [architecture.md](architecture.md) and [codebase-context.md](problems/codebase-context.md).

### Stage

A higher-level workflow component in the fullsend pipeline (e.g., triage, implementation, review). The team formally chose "stage" over "phase" to avoid overloading the general SDLC use of "phase" and to maintain distinct vocabulary from Tekton's pipeline/task/step hierarchy, since fullsend may run on Tekton infrastructure. Each stage contains one or more [steps](#step).
See [ADR 0002](ADRs/0002-initial-fullsend-design.md).

### Step

A discrete unit of work within a [stage](#stage). For example, the triage stage may include steps for duplicate detection, reproducibility checking, and label assignment. "Stages and steps" is the agreed-upon workflow hierarchy for fullsend.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md).

### Slash Command

A GitHub comment in the form `/triage`, `/implement`, `/review`, etc., that manually triggers an agent workflow. Slash commands are parsed by the entry point and gated by an ACL — not every user can invoke every command. They provide an explicit human-initiated trigger alongside the automatic label-based triggers.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 2.

## T

### Trigger

What initiates an agent run. Could be a GitHub event (issue filed, label applied, comment posted, PR opened, check completed), a [slash command](#slash-command), or a scheduled action. The term is used loosely in discussions — sometimes meaning the raw GitHub webhook event, sometimes meaning the processed signal that actually starts an agent after debouncing and validation. In fullsend's architecture, triggers flow through the [entry point](#entry-point), which normalizes and dispatches them.
See [architecture.md](architecture.md) (building block 1).

### Triage

In fullsend, triage means routing, deduplicating, assessing completeness, and checking reproducibility — **not** fixing. The triage agent reads the issue, determines if it is a duplicate, assesses whether it is a bug or a feature (and denies if features are not in scope), checks if the issue has enough detail for implementation, and optionally attempts reproduction. The scope of triage has been a recurring discussion point, particularly around whether reproducibility and test generation belong in triage or implementation.
See [ADR 0002](ADRs/0002-initial-fullsend-design.md) building block 4 and [#86](https://github.com/fullsend-ai/fullsend/issues/86).

### Trust

In fullsend, trust is not a single concept — it appears in at least three distinct contexts:

1. **Identity trust** — Can we verify who is making a request? Addressed by agent identities and GitHub App installations.
2. **Content trust** — Can we trust the content of inputs (issue bodies, comments, PR descriptions)? The answer is always **no** under the zero-trust model; all inputs are sanitized regardless of source.
3. **Execution trust** — Can we trust that an agent will do what it's supposed to? Addressed by sandboxing, scoped permissions, and the principle that trust derives from repository permissions, not agent identity.

The overloading of "trust" across these contexts has been a recurring source of confusion in design discussions.
See [security-threat-model.md](problems/security-threat-model.md) and [agent-architecture.md](problems/agent-architecture.md).

## W

### Work Coordinator

The mechanism that assigns work to agents and prevents conflicts. The existing design principle is that the **repo is the coordinator** — branch protection, CODEOWNERS, status checks, and GitHub events provide coordination without a central orchestrator. The work coordinator may be just the glue connecting GitHub webhooks to agent infrastructure, or it may need to be more (e.g., a claim/lock system to prevent two implementation agents from picking up the same issue).
See [architecture.md](architecture.md) and [#77](https://github.com/fullsend-ai/fullsend/issues/77).

## Z

### Zero Trust

In fullsend's agent-to-agent model, zero trust means **nothing is trusted implicitly based on identity alone**. It does **not** mean "accept zero inputs" or "block everything." Every agent assumes every other agent — and every external input — could be compromised. The implementation agent assumes the triage output may contain prompt injection. The review agent assumes the submitted PR is designed to trick it. Defense is layered: input sanitization, scoped permissions, sandbox containment, and output validation all work together.
See [security-threat-model.md](problems/security-threat-model.md) (Threat 5) and [#102](https://github.com/fullsend-ai/fullsend/issues/102).
