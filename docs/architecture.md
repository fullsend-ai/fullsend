# Architecture

What are the components of the agent execution stack?

> **This is a living document.** It must always reflect the current state of
> architectural decisions. When an ADR is accepted (or superseded), this
> document is updated to match. ADRs are point-in-time records and are not
> modified after acceptance; this document is where the *current* truth lives.
> A reader should be able to understand the system's architecture from this
> document alone, without tracing a chain of ADRs.

This document names the parts of the system without deciding how they work. It establishes shared vocabulary that the [problem documents](problems/) can reference when discussing design choices. Each component gets a responsibility statement and open questions — implementation decisions live in the problem docs and will crystallize into [ADRs](ADRs/) as they mature.

This is not exhaustive. Not every problem doc maps to a component here, and not every component here has a corresponding problem doc yet.

## Execution Stack

Five components form the vertical execution path from event to agent action:

1. **Agent Dispatch and Coordination Layer** — translates events into agent tasks
2. **Agent Infrastructure** — provisions and runs agent workloads
3. **Agent Sandbox** — enforces isolation (network, filesystem)
4. **Agent Harness** — assembles configuration and context (skills, prompts, tools)
5. **Agent Runtime** — the LLM in execution

Control flows strictly downward through this stack. No layer may influence, configure, or depend on layers above it. This is the execution stack's primary structural invariant. (See [ADR 0016](ADRs/0016-unidirectional-control-flow.md).)

The remaining components described in this document (Policy Store, Intent Source, Identity Provider, Observability, Agent Registry) are cross-cutting concerns that feed into the stack from the side. They are not part of the vertical control flow, but they follow the same principle: no component within the stack can modify the cross-cutting systems that constrain it.

## Agent Infrastructure

The compute and orchestration layer that runs agent workloads. Responsible for provisioning, scheduling, scaling, and lifecycle management of agent execution environments.

This is the "where do agents physically run" question — whether that's a managed platform, internal Kubernetes, CI runners repurposed for agent work, or something purpose-built.

Infrastructure platform choice and configuration are specified in the adopting organization's **`.fullsend`** repository. (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Decided:**

- Forge abstraction: all forge operations go through the `forge.Client` interface, keeping the rest of the codebase forge-agnostic ([ADR 0005](ADRs/0005-forge-abstraction-layer.md)).
- Installation model: ordered layer stack (install forward, uninstall reverse, analyze for status reporting) with idempotent operations. Current stack: config-repo → workflows → secrets → inference → dispatch-token → enrollment ([ADR 0006](ADRs/0006-ordered-layer-model.md)).
- Cross-repo dispatch: `workflow_dispatch` with an org-level dispatch token replaces `workflow_call`, keeping App PEM secrets in the config repo ([ADR 0008](ADRs/0008-workflow-dispatch-for-cross-repo-dispatch.md)).
- Shim workflow security: `pull_request_target` prevents PR authors from modifying the shim to exfiltrate the dispatch token ([ADR 0009](ADRs/0009-pull-request-target-in-shim-workflows.md)).

**Open questions:**

- Do we adopt a 3rd party platform, use existing internal infrastructure, or build our own? (See [agent-infrastructure.md](problems/agent-infrastructure.md) for the three directions.)
- Can different agent types (short-lived review vs. long-running code) run on different infrastructure?
- Who in the org owns and operates this, and how does it relate to existing platform or CI ownership?
- Should model and MCP (or other tool-protocol) traffic from agent runtimes go through a **shared gateway** for authentication, spend limits, allowlists, and telemetry? (See [landscape.md](landscape.md#agent-gateway).)

## Agent Sandbox

The isolation boundary around a running agent. Responsible for filesystem access control and network regulation — ensuring an agent can only reach what it's authorized to reach and cannot affect other agents or systems outside its boundary.

The sandbox is a security primitive. Its job is containment: if an agent is compromised or misbehaves, the blast radius is limited to what the sandbox permits.

Ecosystem projects reuse the word *sandbox* for different workload shapes. For example, [Kubernetes SIG Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) targets **stateful, singleton** agent runtimes (long-lived sessions), whereas many fullsend-style workflows emphasize **short-lived, task-scoped** runs with tight isolation and observability. How those patterns compare is discussed in [agent-infrastructure.md](problems/agent-infrastructure.md#kubernetes-sig-agent-sandbox).

Sandbox defaults (network policy, filesystem restrictions) are configured in the adopting organization's **`.fullsend`** repository and can be overridden per-repo. (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Open questions:**

- What is the right isolation level — process, container, microVM, or separate cluster? (See [agent-infrastructure.md](problems/agent-infrastructure.md) and [security-threat-model.md](problems/security-threat-model.md).)
- How granular is network regulation? Allowlist of endpoints, or coarser controls? (A **protocol gateway** toward approved model and MCP endpoints is one way to narrow egress without handing agents raw internet access; see [landscape.md](landscape.md#agent-gateway).)
- Does the sandbox provide a pre-built environment (tools, language runtimes, repo clones), or does the agent set up its own workspace within the sandbox?
- ~~Is the sandbox the same for all agent roles, or does each role get a differently-scoped sandbox?~~ Decided in [ADR 0020](ADRs/0020-composable-single-responsibility-agents-with-individual-sandboxes.md): each agent gets its own sandbox with policies designed for its responsibility.

## Agent Harness

The configuration and context layer that prepares an agent for its task. Responsible for providing skills, system prompts, codebase context, tool definitions, and behavioral instructions to the agent runtime.

The harness is what makes a generic LLM into a specific agent with a specific role. It assembles what the agent needs to know and what it's allowed to do before the agent starts working.

The harness draws its configuration from the adopting organization's **`.fullsend`** repository — skills, workflow definitions, and agent behavioral instructions are assembled from the layered config (fullsend defaults, then org config, then per-repo overrides). (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

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

The system that gives agents credentials to act on external services. Responsible for issuing, scoping, rotating, and revoking the identities agents use to interact with GitHub, container registries, and other APIs.

Identity is not the same as trust. An agent's identity lets it authenticate to external services; the trust model is defined by repository permissions and CODEOWNERS, not by which credentials the agent holds. (See [agent-architecture.md](problems/agent-architecture.md) — "trust derives from repository permissions, not agent identity.")

**Decided:**

- Credential isolation model: credentials stay in host-side servers, never enter agent sandboxes. Agents access credentialed services via REST APIs, with L7 network policy enforcing per-agent method/path restrictions ([ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md)).
- Per-role GitHub Apps with manifest-based creation. Each agent role gets its own app with scoped permissions. PEMs stored as repo secrets on `.fullsend` ([ADR 0007](ADRs/0007-per-role-github-apps.md)).

One concrete implementation option is [`oidcx`](https://github.com/oxidecomputer/oidcx): a service that accepts OIDC identity tokens and exchanges them for short-lived access tokens. It can mint tokens scoped to selected GitHub repositories and permissions, or to selected Oxide silos and permissions, and it also ships with a GitHub Action wrapper. In a Fullsend deployment, this can be used by the sandbox entrypoint to narrow a broad GitHub App identity down to only the specific permissions an agent needs for the current run.

**Open questions:**

- ~~What identity model fits best — separate bot accounts per agent role, a single bot account with role metadata, GitHub App installations, or something else?~~ Decided in [ADR 0007](ADRs/0007-per-role-github-apps.md).
- How are credentials rotated and revoked, and who has authority to do that?
- Does the identity provider integrate with existing secrets management, or is it a new system?
- How will per-role identity work on GitLab and Forgejo, which lack GitHub's app manifest flow?

## Agent Dispatch and Coordination Layer

The mechanism that assigns work to agents and prevents conflicts. Responsible for translating triggers (GitHub events, schedules, manual requests) into agent tasks and ensuring two agents don't work the same problem simultaneously.

The existing design principle is that [the repo is the coordinator](problems/agent-architecture.md#interaction-model-the-repo-as-coordinator) — branch protection, CODEOWNERS, status checks, and GitHub events provide coordination without a central orchestrator. The agent dispatch and coordination layer may be nothing more than the glue that connects GitHub webhooks to agent infrastructure. Or it may need to be more.

**Open questions:**

- Is GitHub's event system sufficient, or do we need additional coordination logic (e.g. to prevent two code agents from picking up the same issue)?
- How does work assignment interact with the backlog/priority agent described in [agent-architecture.md](problems/agent-architecture.md)?
- What happens when work needs to be cancelled, retried, or reassigned?
- Does the coordinator need state (a queue, a lock, a claim system), or can it be stateless and event-driven?

## Policy Store

Where agent behavioral rules live. Responsible for holding autonomy levels, review requirements, allowed operations, and escalation rules — the configuration that governs what agents may do.

Policy is distinct from the harness (which configures *how* an agent works) and from intent (which defines *what* work is authorized). Policy defines the *boundaries* of agent behavior — what an agent is allowed to do regardless of what it's asked to do.

The adopting organization's **`.fullsend`** repository is the natural home for policy configuration — org-wide guardrails, per-repo autonomy levels, and escalation rules all live there, governed by the org's own CODEOWNERS and review process. (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Open questions:**

- How is policy versioned, and how do we ensure agents run under the correct policy version?
- Who can change policy, and what approval process governs policy changes? (See [governance.md](problems/governance.md).)
- How does policy interact with the autonomy spectrum — is the auto-merge vs. escalate decision a policy setting? (See [autonomy-spectrum.md](problems/autonomy-spectrum.md).)

## Intent Source

The system that provides authorized intent for agent work. Responsible for representing what changes are wanted, who authorized them, and at what tier of approval.

Intent answers the question "should this change exist?" before anyone asks "is this change correct?" Without authorized intent, an agent has no basis for deciding what to work on or whether its output matches what was asked for.

The adopting organization's **`.fullsend`** repository holds the pointer to the intent source (for example, `intent_repo: your-org/features`), so tooling discovers where intent lives without hardcoding. (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Open questions:**

- What is the right representation — forge issues, a dedicated intent repo, RFCs, or tiered combinations? (See [intent-representation.md](problems/intent-representation.md).)
- How do agents verify that intent is authentic and hasn't been tampered with?
- How do different tiers of intent (standing rules, tactical issues, strategic features) map to different authorization requirements?
- How does intent interact with the "try it" phase — agents building exploratory drafts before authorization? (See [intent-representation.md](problems/intent-representation.md).)

## Observability

The logging, tracing, and audit layer for agent actions. Responsible for making every agent action attributable, traceable, and reviewable — both for debugging failures and for security auditability.

Observability is a cross-cutting concern that touches every other component. Each component produces signals; this component is responsible for collecting, storing, and making them useful.

**Decided:**

- JSONL reasoning trace exposure: raw JSONL conversation transcripts are extracted from sandboxes and stored with owner-scoped access. Credential scanning acts as an invariant check on [ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md)'s isolation model. Agents handling data from protected sources beyond the target repo can opt in to JSONL suppression via configuration ([ADR 0021](ADRs/0021-jsonl-reasoning-trace-exposure.md)).

**Open questions:**

- What signals matter most — cost, latency, token usage, action logs, decision traces, or something else?
- How do we balance detailed tracing (useful for debugging) with the volume of data agents will produce?
- What is the retention and access model for agent logs? Who can see what? (JSONL trace access model decided in [ADR 0021](ADRs/0021-jsonl-reasoning-trace-exposure.md); retention policy and broader log access remain open.)
- How does observability interact with the security requirement that "every action is logged, attributable, and reviewable"? (See [security-threat-model.md](problems/security-threat-model.md).)
- Is there a real-time monitoring requirement (agent is stuck, agent is behaving anomalously), or is observability primarily forensic?

## Agent Registry

The catalog of available agent roles and their configurations. Responsible for defining what agent types exist, what capabilities each has, and how they are instantiated.

The registry is the bridge between the abstract roles defined in [agent-architecture.md](problems/agent-architecture.md) (correctness agent, intent alignment agent, etc.) and the concrete runtime configurations that the harness uses to set up each agent.

Fullsend provides a base set of agent definitions. The adopting organization's **`.fullsend`** repository extends this with org-specific agents in its `agents/` directory, following the inheritance model: fullsend defaults, then org config, then per-repo overrides. (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Open questions:**

- How are new agent roles added, tested, and promoted to production? (See [testing-agents.md](problems/testing-agents.md).)
- Does the registry include version information, so we can roll back to a previous agent configuration?
- How does the registry relate to the policy store — does policy reference registry entries, or are they independent?

## Reference workflow components (ADR 0002)

The [Initial Fullsend Design](ADRs/0002-initial-fullsend-design.md) describes a concrete GitHub-centric issue→merge workflow. Its **building blocks** are named below so this document and the ADR stay aligned. Descriptions are brief; the ADR is normative for behavior.

### 1. Webhook + dispatch service

Normalizes GitHub events (issue/PR/label/comment/check/merge), deduplicates flapping events, and dispatches work to agent runtimes.
ADR 0002: [Building block 1](ADRs/0002-initial-fullsend-design.md#1-webhook--dispatch-service).

### 2. Slash-command parser + ACL

Parses `/triage`, `/code`, `/review`, and related commands and enforces who is allowed to invoke each.
ADR 0002: [Building block 2](ADRs/0002-initial-fullsend-design.md#2-slash-command-parser--acl).

### 3. Label state machine guard

Validates legal label transitions and enforces mutual exclusion and run-start reset semantics (triage start clears **`duplicate`** and downstream labels; PR/review strips per ADR).
ADR 0002: [Building block 3](ADRs/0002-initial-fullsend-design.md#3-label-state-machine-guard).

### 4. triage agent runtime

Runs triage from issue `title`/`body` + GitHub-native attachments only; each run starts with **`duplicate`** and other reset labels cleared; duplicate detection, readiness, reproducibility, test handoff; can close as duplicate again if still a match.
ADR 0002: [Building block 4](ADRs/0002-initial-fullsend-design.md#4-triage-agent-runtime).

### 5. Duplicate / similarity search

Provides candidate duplicate retrieval and confidence scoring for triage duplicate decisions.
ADR 0002: [Building block 5](ADRs/0002-initial-fullsend-design.md#5-duplicate--similarity-search).

### 6. Repro sandbox template

Isolated environment used by triage for reproducibility checks.
ADR 0002: [Building block 6](ADRs/0002-initial-fullsend-design.md#6-repro-sandbox-template).

### 7. Test artifact formatter

Formats triage test artifacts in repo-native conventions for PR handoff.
ADR 0002: [Building block 7](ADRs/0002-initial-fullsend-design.md#7-test-artifact-formatter).

### 8. code agent runtime

Implements changes, runs local/CI-equivalent tests, handles check failures, and advances handoff to **Review** (`ready-for-review`).
ADR 0002: [Building block 8](ADRs/0002-initial-fullsend-design.md#8-implementation-agent-runtime).

### 9. PR sandbox / CI mirror

Execution environment for **Code** and test loops, aligned to contributor/CI toolchains.
ADR 0002: [Building block 9](ADRs/0002-initial-fullsend-design.md#9-pr-sandbox--ci-mirror).

### 10. Check failure triage

Fetches and classifies failing check logs to guide **code agent** remediation loops.
ADR 0002: [Building block 10](ADRs/0002-initial-fullsend-design.md#10-check-failure-triage).

### 11. review agent runtime

Runs N parallel **review agent** invocations and produces structured review verdicts/comments.
ADR 0002: [Building block 11](ADRs/0002-initial-fullsend-design.md#11-review-agent-runtime).

### 12. Coordinator merge algorithm

Aggregates review verdicts and applies labels:

- unanimous approve-merge → `ready-for-merge` (for the **current** PR head at the end of that round only)
- unanimous rework → `ready-to-code`
- split/conflicting (including conflicting security severities) → `requires-manual-review`
- each **review run start** (including push-triggered re-review) clears **`ready-for-merge`** together with **`ready-for-review`** so merge approval is never stale after new commits
ADR 0002: [Building block 12](ADRs/0002-initial-fullsend-design.md#12-coordinator-merge-algorithm).

### 13. Observability

Traceability layer across issue, **Triage**, **Code**, **Review**, checks, and merge for incident response and correlation across automation runs.
ADR 0002: [Building block 13](ADRs/0002-initial-fullsend-design.md#13-observability).

## Repository layout (design workspace vs. web delivery)

The repository combines design documents, Go CLI code, and a small **public web** surface. **Decided:** Browser-oriented static source and future bundled UI live under **`web/`** (the interactive document graph is `web/public/index.html` at `/`). Cloudflare Wrangler configuration and deploy-time static assets live under **`cloudflare_site/`** (single `wrangler.toml`; CI stages **`_bundle/`** on the deploy runner and copies only **`public/`** and **`worker/`** from the artifact into that tree so **`wrangler.toml` is never taken from the PR-built zip**). See [ADR 0019](ADRs/0019-web-source-and-cloudflare-site-layout.md).
