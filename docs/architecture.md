# Architecture

What are the components of the agent execution stack?

> **This is a living document.** It must always reflect the current state of
> architectural decisions. When an ADR is accepted (or superseded), this
> document is updated to match. ADRs are point-in-time records that may receive minor annotations
> but are not substantially rewritten; this document is where the *current* truth lives.
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
- Installation model: ordered layer stack (install forward, uninstall reverse, analyze for status reporting) with idempotent operations. Current stack: config-repo → workflows → harness-wrappers → vendor-binary → secrets → inference → dispatch → enrollment ([ADR 0006](ADRs/0006-ordered-layer-model.md)).
- Cross-repo dispatch: enrolled repos call `.fullsend` via `workflow_call`; a dispatch workflow mints OIDC tokens exchanged at a central token mint (GCP Cloud Function) for scoped GitHub App installation tokens per agent role. App PEM secrets are stored in Secret Manager (GCF mint) or the local filesystem (standalone mint), not the config repo ([ADR 0008](ADRs/0008-workflow-dispatch-for-cross-repo-dispatch.md)).
- Shim workflow security: `pull_request_target` prevents PR authors from modifying the shim workflow. No long-lived secrets flow through the shim — OIDC tokens are issued by the GitHub runtime and scoped to the workflow run ([ADR 0009](ADRs/0009-pull-request-target-in-shim-workflows.md)).
- Repo maintenance: a workflow in `.fullsend` (`.github/workflows/repo-maintenance.yml`) reconciles enrollment shims in target repos when `config.yaml` changes or on manual dispatch. The CLI's `EnrollmentLayer.Install()` dispatches this workflow via `workflow_dispatch` and monitors it for completion, then reports any enrollment PRs created in target repos.
- Installer scaffold: the `WorkflowsLayer` deploys content from an embedded scaffold (`internal/scaffold/`), keeping deployable files as real files under version control rather than Go string constants.
- Reusable workflows: agent workflows in `.fullsend` are thin callers (~40-70 lines) that delegate infrastructure logic to upstream reusable workflows (`fullsend-ai/fullsend/.github/workflows/reusable-*.yml`) via `workflow_call`. Infrastructure patches ship once upstream and propagate to all orgs without re-install ([ADR 0031](ADRs/0031-reusable-workflows-for-action-installed-distribution.md)). **`--vendor`** ([ADR 0047](ADRs/0047-vendored-installs-with-vendor-flag.md)) commits workflows and agent content at install time; layered installs (default) fetch upstream at runtime.
- Event-driven stage dispatch: eliminate `workflow_dispatch` + `gh workflow run` fan-out from `dispatch.yml` in favor of synchronous `workflow_call` so the dispatched run stays linked to the caller ([ADR 0041](ADRs/0041-synchronous-workflow-call-event-dispatch.md)).
- Multi-repo management: a `fullsend repos` subcommand group with a declarative `repos.yaml` manifest for managing per-repo installations at scale — bulk install, status, sync, upgrade, and removal across repos and orgs ([ADR 0057](ADRs/0057-repos-management.md)).

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

**Decided:**

- Output schema enforcement: a harness post-script validates every agent's
  output against a declared JSON schema on the host. Non-compliant output
  triggers a retry (capped); exhaustion is a hard failure — no unvalidated
  output is emitted
  ([ADR 0022](ADRs/0022-harness-level-output-schema-enforcement.md)).
- Forge-portable harness schema: `role` and `slug` move into the harness
  YAML (eliminating the config.yaml `agents:` block dependency), and a
  `forge:` section separates platform-specific config (scripts, skills,
  runner_env) from platform-neutral fields. Forge blocks inherit from
  top-level defaults and override only deltas
  ([ADR 0045](ADRs/0045-forge-portable-harness-schema.md)).
- Unified env var delivery: a single `env:` key with `runner` and `sandbox`
  sub-maps replaces `runner_env` and manual `.env` files. The runner generates
  the sandbox `.env` file from `env.sandbox` at bootstrap. `runner_env` is
  deprecated ([ADR 0055](ADRs/0055-unified-env-var-delivery.md), amending
  [ADR 0024](ADRs/0024-harness-definitions.md)).
- Agent configuration env vars: behavioral knobs use `{AGENT}_{SETTING_NAME}`
  naming (e.g., `REVIEW_SEVERITY_THRESHOLD`), delivered via `env.runner` and
  `env.sandbox` in the harness YAML. Each agent documents its config vars in
  `docs/agents/<agent>.md`
  ([ADR 0049](ADRs/0049-agent-configuration-env-var-convention.md)).
- Agent-driven branch targeting: the code agent writes its chosen target
  branch to structured output. The post-script validates the choice against
  an allowlist and falls back to the repo's auto-detected default branch.
  Branch-targeting logic lives in the portable post-script, not in workflow
  YAML ([ADR 0053](ADRs/0053-agent-driven-branch-targeting.md)).

**Open questions:**

- Does the harness live inside the sandbox (configuring the agent from within its isolation boundary) or outside it (preparing the environment before the agent starts)? (Tool permissions are injected as a host-managed `.claude/settings.json` — configured outside, enforced inside; see [ADR 0027](ADRs/0027-allowed-and-disallowed-tools-for-agents.md). General harness placement remains open.)
- How is codebase context assembled? (See [codebase-context.md](problems/codebase-context.md).)
- How do we version and test harness configurations? (See [testing-agents.md](problems/testing-agents.md).) (Functional tests now test the full pipeline including harness-assembled configuration — [ADR 0052](ADRs/0052-functional-tests-for-agent-pipelines.md). Harness versioning remains open.)

## Agent Runtime

The agent itself in execution — the LLM, its tool-use loop, and the interface to the model provider. Responsible for performing the assigned task within the boundaries set by the sandbox and the configuration provided by the harness.

This is the thing that actually reasons and acts. Everything else in this document exists to support, constrain, or coordinate it.

**Decided (implementation):**

- The `fullsend run` runner delegates in-sandbox agent execution to a `runtime.Runtime` interface; the MVP registers Claude Code only. Bootstrap uses a portable `BootstrapInput` interface with optional extensions such as `ClaudeHooksBootstrap` for sandbox tool hooks. Transcript and debug artifact handling use a separate `TranscriptHandler` interface. See [runtimes.md](runtimes.md) for the per-runtime security feature matrix required when adding a new backend.

**Open questions:**

- Is the runtime a single model call, a loop (plan-act-observe), or something more structured?
- How does the runtime interact with the sandbox boundaries — does it know what it can't do, or does it just hit walls? (For tool access: both — prose instructions inform the runtime, and `permissions.deny` hard-blocks execution; see [ADR 0027](ADRs/0027-allowed-and-disallowed-tools-for-agents.md). Broader sandbox interaction remains open.)
- How do we swap model providers or versions without changing the rest of the stack?
- What is the interface between the harness and the runtime? (A system prompt? A configuration file? An API contract?)

## Agent Identity Provider

The system that gives agents credentials to act on external services. Responsible for issuing, scoping, rotating, and revoking the identities agents use to interact with GitHub, container registries, and other APIs.

Identity is not the same as trust. An agent's identity lets it authenticate to external services; the trust model is defined by repository permissions and CODEOWNERS, not by which credentials the agent holds. (See [agent-architecture.md](problems/agent-architecture.md) — "trust derives from repository permissions, not agent identity.")

**Decided:**

- Credential delivery model: four tiers — (1) prefetch + post-process for agents with enumerable inputs (zero credential access), (2) OpenShell providers + L7 egress policies for static token auth (credentials never enter sandbox), (3) host-side REST server for operations providers cannot handle — long-running operations, sandbox capability gaps, credentials in request bodies, response transformation, and multi-step atomic operations (see [ADR 0046](ADRs/0046-host-side-api-server-design.md)), (4) host files + L7 policies for complex auth requiring in-sandbox credential files. L7 policies enforce both method + path and binary-level restrictions. Providers are preferred over REST servers when viable ([ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md), extended by [ADR 0025](ADRs/0025-provider-credential-delivery-for-sandboxed-agents.md)).
- Host-side API server design: Tier 3 servers follow a uniform process contract (`--port`, `--token`, `--bind-address`, `/healthz`, `/tools.json`, `SIGTERM`). Network access is controlled via composable provider profiles — atomic capability profiles composed per-harness. Per-run UUID bearer tokens are delivered through OpenShell provider placeholders. File transfer uses `openshell sandbox upload/download` ([ADR 0046](ADRs/0046-host-side-api-server-design.md)).
- Per-role GitHub Apps with manifest-based creation. Each agent role gets its own app with scoped permissions. PEMs stored in Secret Manager as `fullsend-{role}-app-pem` — one secret per role, shared across orgs on a mint. `ROLE_APP_IDS` uses the same shared-per-role model (`coder` → app ID). Org isolation is enforced via `ALLOWED_ORGS`, WIF conditions, and installation verification ([ADR 0007](ADRs/0007-per-role-github-apps.md), [ADR 0033](ADRs/0033-per-repo-installation-mode.md)).
- Standalone mint deployment: `cmd/mint/` provides a self-contained HTTP server that uses direct JWKS verification and filesystem PEM storage instead of GCP infrastructure. It shares the `internal/mintcore/` library with the GCF mint and adds support for custom role permissions and a fallback proxy to an upstream mint. Custom role permissions live in mintcore (not `cmd/mint/`) so that `RolePermissionsFor`, `HasRole`, and `CreateInstallationToken` return a unified view without callers needing to distinguish built-in from custom roles. The GCF mint never calls `RegisterCustomRolePermissions`, so the code is inert there. See the [standalone mint guide](guides/infrastructure/standalone-mint.md).

One concrete implementation option is [`oidcx`](https://github.com/oxidecomputer/oidcx): a service that accepts OIDC identity tokens and exchanges them for short-lived access tokens. It can mint tokens scoped to selected GitHub repositories and permissions, or to selected Oxide silos and permissions, and it also ships with a GitHub Action wrapper. In a Fullsend deployment, this can be used by the sandbox entrypoint to narrow a broad GitHub App identity down to only the specific permissions an agent needs for the current run.

**Open questions:**

- ~~What identity model fits best — separate bot accounts per agent role, a single bot account with role metadata, GitHub App installations, or something else?~~ Decided in [ADR 0007](ADRs/0007-per-role-github-apps.md).
- How are credentials rotated and revoked, and who has authority to do that?
- Does the identity provider integrate with existing secrets management, or is it a new system?
- How will per-role identity work on GitLab and Forgejo, which lack GitHub's app manifest flow?

## Agent Dispatch and Coordination Layer

The mechanism that assigns work to agents and prevents conflicts. Responsible for translating triggers (GitHub events, schedules, manual requests) into agent tasks and ensuring two agents don't work the same problem simultaneously.

The existing design principle is that [the repo is the coordinator](problems/agent-architecture.md#interaction-model-the-repo-as-coordinator) — branch protection, CODEOWNERS, status checks, and GitHub events provide coordination without a central orchestrator. The agent dispatch and coordination layer may be nothing more than the glue that connects GitHub webhooks to agent infrastructure. Or it may need to be more.

**Decided:**

- Event-driven stage dispatch runs synchronously via `workflow_call` to preserve run correlation in the GitHub Actions UI (see [ADR 0041](ADRs/0041-synchronous-workflow-call-event-dispatch.md)).

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
- Event-driven stage dispatch remains traceable end-to-end in the GitHub Actions UI by using synchronous `workflow_call` dispatch (see [ADR 0041](ADRs/0041-synchronous-workflow-call-event-dispatch.md)).
- Distributed tracing: framework-native OpenTelemetry instrumentation with zero-configuration baseline. Every run produces `run-telemetry.jsonl` and `run-summary.json` locally; optional OTLP export to any compatible backend. W3C trace context propagation links multi-agent pipelines into unified traces. OTEL GenAI semantic conventions enable LLM-aware backends ([ADR 0050](ADRs/0050-distributed-tracing-instrumentation.md)).

**Open questions:**

- What signals matter most — cost, latency, token usage, action logs, decision traces, or something else?
- ~~How do we balance detailed tracing (useful for debugging) with the volume of data agents will produce?~~ Decided in [ADR 0050](ADRs/0050-distributed-tracing-instrumentation.md): instrument all lifecycle steps comprehensively; volume is managed by backends not by suppressing data at the source.
- What is the retention and access model for agent logs? Who can see what? (JSONL trace access model decided in [ADR 0021](ADRs/0021-jsonl-reasoning-trace-exposure.md); retention policy and broader log access remain open.)
- How does observability interact with the security requirement that "every action is logged, attributable, and reviewable"? (See [security-threat-model.md](problems/security-threat-model.md).)
- Is there a real-time monitoring requirement (agent is stuck, agent is behaving anomalously), or is observability primarily forensic?

## Agent Registry

The catalog of available agent roles and their configurations. Responsible for defining what agent types exist, what capabilities each has, and how they are instantiated.

The registry is the bridge between the abstract roles defined in [agent-architecture.md](problems/agent-architecture.md) (correctness sub-agent, intent & coherence sub-agent, security sub-agent, etc.) and the concrete runtime configurations that the harness uses to set up each agent.

Fullsend provides a base set of agent definitions. The adopting organization's **`.fullsend`** repository extends this with org-specific agents in its `agents/` directory, following the inheritance model: fullsend defaults, then org config, then per-repo overrides. (See [ADR 0003](ADRs/0003-org-config-repo-convention.md).)

**Open questions:**

- How are new agent roles added, tested, and promoted to production? (See [testing-agents.md](problems/testing-agents.md).) (Functional tests provide a framework for testing agent roles against controlled fixtures — [ADR 0052](ADRs/0052-functional-tests-for-agent-pipelines.md). Promotion workflow remains open.)
- Does the registry include version information, so we can roll back to a previous agent configuration?
- How does the registry relate to the policy store — does policy reference registry entries, or are they independent?

## Reference workflow components (ADR 0002)

The [Initial Fullsend Design](ADRs/0002-initial-fullsend-design.md) describes a concrete GitHub-centric issue→merge workflow. Its **building blocks** are named below so this document and the ADR stay aligned. Descriptions are brief; the ADR is normative for behavior.

### 1. Webhook + dispatch service

Normalizes GitHub events (issue/PR/label/comment/check/merge), deduplicates flapping events, and dispatches work to agent runtimes.
ADR 0002: [Building block 1](ADRs/0002-initial-fullsend-design.md#1-webhook--dispatch-service).

### 2. Slash-command parser + ACL

Parses `/fs-triage`, `/fs-code`, `/fs-review`, and related commands and enforces who is allowed to invoke each.
ADR 0002: [Building block 2](ADRs/0002-initial-fullsend-design.md#2-slash-command-parser--acl).

### 3. Label state machine guard

Validates legal label transitions and enforces mutual exclusion and run-start reset semantics (triage start clears **`duplicate`** and downstream labels; **`blocked`** is cleared by the post-script when a non-blocked outcome is reached; PR/review strips per ADR).
ADR 0002: [Building block 3](ADRs/0002-initial-fullsend-design.md#3-label-state-machine-guard).

### 4. triage agent runtime

Runs triage from issue `title`/`body` + GitHub-native attachments only; each run starts with **`duplicate`** and other reset labels cleared; duplicate detection, prerequisite detection (cross-repo), readiness, reproducibility, test handoff; can close as duplicate again if still a match, label **`blocked`** when progress depends on another open issue or PR, or create upstream prerequisite issues when no tracking issue exists (controlled by `create_issues.allow_targets` config).
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

Implements changes, runs local/CI-equivalent tests, handles check failures, and opens or updates a PR. Review dispatch is triggered automatically by `pull_request_target` events.
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
- unanimous rework → triggers [fix agent](agents/fix.md)
- split/conflicting (including conflicting security severities) → `requires-manual-review`
- each **review run start** (including push-triggered re-review) clears **`ready-for-merge`** together with **`ready-for-review`** so merge approval is never stale after new commits
ADR 0002: [Building block 12](ADRs/0002-initial-fullsend-design.md#12-coordinator-merge-algorithm).

### 13. Observability

Traceability layer across issue, **Triage**, **Code**, **Review**, checks, and merge for incident response and correlation across automation runs.
ADR 0002: [Building block 13](ADRs/0002-initial-fullsend-design.md#13-observability).

### 14. retro agent runtime

Retrospective analyst — examines completed or in-progress agent workflows, identifies improvement opportunities, and files proposals as GitHub issues. Runs automatically on PR close (merged or rejected) and on-demand via `/fs-retro` command. Analyzes the full workflow graph (triage, code, review, fix agent interactions and human interventions) and posts a summary comment on the originating PR/issue linking to all filed proposals.

## Configuration layering

Fullsend uses a three-tier inheritance model for all configuration: agent definitions, skills, policies, harness definitions, and guardrails. Each tier can extend or override the one below it. Guardrails can only be tightened, never weakened.

```


  ┌──────────────────────────────────────────────────────────────────┐
  │  fullsend-ai/fullsend                    (upstream open source)  │
  │                                                                  │
  │  Framework defaults:                                             │
  │    base agents, skills, policies                                 │
  │    fullsend CLI (fullsend run, fullsend install, ...)            │
  │    scaffold templates, security scanners                         │
  │                                                                  │
  │  Owned by: fullsend project maintainers                          │
  ├──────────────────────────────────────────────────────────────────┤
  │  <org>/.fullsend                              (dedicated repo)   │
  │                                                                  │
  │  Org-wide configuration:                                         │
  │    agents/            org agent definitions (.md)                │
  │    skills/            org skills (shared across repos)           │
  │    policies/          sandbox network/filesystem policies        │
  │    harness/           per-agent harness configs (.yaml)          │
  │    guardrails.yaml    org-wide guardrails (can only be tightened)│
  │    config.yaml        intent repo, runtime, infrastructure       │
  │                                                                  │
  │  Owned by: org platform team (CODEOWNERS, human-only)            │
  ├──────────────────────────────────────────────────────────────────┤
  │  <org>/<repo>                               (directory in repo)  │
  │                                                                  │
  │  Repo-specific overrides:                                        │
  │    AGENTS.md          per-repo agent instructions                │
  │    skills/            repo-specific skills (domain knowledge)    │
  │    .fullsend/config   overrides -  adjust timeouts, prompts      │
  │                                                                  │
  │  Owned by: repo maintainers (CODEOWNERS)                         │
  └──────────────────────────────────────────────────────────────────┘

  Inheritance:  fullsend defaults  <  org .fullsend config  <  per-repo overrides
                (base)                (extend/override)        (extend/tighten)
```

Skills flow downward through this stack. A repo-level skill might encode domain knowledge ("this repo uses a custom ORM — here's how queries work"). An org-level skill might encode org conventions ("all services use structured logging via zerolog"). Upstream fullsend provides foundational skills (code implementation, triage coordination, testing conventions).

AGENTS.md files follow the same layering. A repo's `.fullsend/AGENTS.md` gives agents repo-specific instructions (build commands, test patterns, architectural constraints). The org's `.fullsend/agents/` directory provides role-specific agent definitions that apply across all enrolled repos.

See [ADR 0003](ADRs/0003-org-config-repo-convention.md) for the config repo convention and [ADR 0024](ADRs/0024-harness-definitions.md) for harness definitions.

**Decided:**

- Layered content resolution: upstream defaults (agents, skills, schemas,
  harness, policies, scripts) are provided at runtime via sparse checkout of
  `fullsend-ai/fullsend@v0`, or from vendored files when `--vendor` was used at
  install (detected via `.defaults/action.yml` — see
  [ADR 0047](ADRs/0047-vendored-installs-with-vendor-flag.md)). The
  scaffold installs only org-specific files and a `customized/` directory for org
  overrides. Org files in `customized/` overwrite upstream defaults at runtime
  ([ADR 0035](ADRs/0035-layered-content-resolution.md)).

## Multi-org deployment model

Each organization that adopts fullsend operates independently. There is no shared control plane, no central service, and no relationship between orgs. Each org brings its own inference API keys and runs its own version of fullsend.

```
  ┌──────────────────────┐  ┌──────────────────────┐  ┌──────────────────────┐
  │  Org A               │  │  Org B               │  │  Org C               │
  │                      │  │                      │  │                      │
  │  .fullsend repo      │  │  .fullsend repo      │  │  .fullsend repo      │
  │  ┌────────────────┐  │  │  ┌────────────────┐  │  │  ┌────────────────┐  │
  │  │ config.yaml    │  │  │  │ config.yaml    │  │  │  │ config.yaml    │  │
  │  │ agents/        │  │  │  │ agents/        │  │  │  │ agents/        │  │
  │  │ skills/        │  │  │  │ skills/        │  │  │  │ skills/        │  │
  │  │ harness/       │  │  │  │ harness/       │  │  │  │ harness/       │  │
  │  └────────────────┘  │  │  └────────────────┘  │  │  └────────────────┘  │
  │                      │  │                      │  │                      │
  │  API keys: own       │  │  API keys: own       │  │  API keys: own       │
  │  Enrolled repos: ... │  │  Enrolled repos: ... │  │  Enrolled repos: ... │
  │  fullsend v0.2.0     │  │  fullsend v0.4.1     │  │  fullsend v0.2.0     │
  │                      │  │                      │  │                      │
  └──────────┬───────────┘  └──────────┬───────────┘  └──────────┬───────────┘
             │                         │                         │
             │            no relationship between orgs           │
             │                         │                         │
             └─────────────────────────┼─────────────────────────┘
                                       │
                            ┌──────────┴───────────┐
                            │  fullsend-ai/fullsend│
                            │                      │
                            │  Open source project │
                            │  CLI, base agents,   │
                            │  skills, scaffold    │
                            │                      │
                            │  Orgs pull releases  │
                            │  at their own pace   │
                            └──────────────────────┘
```

Each org is a fully independent instance. They choose when to upgrade. They configure their own agents, skills, and policies. They use their own model providers and API keys. The only shared element is the upstream fullsend project they all pull from.

## Downstream/upstream federation

Independent orgs can optionally collaborate across the forge boundary. A downstream org — a vendor, contributor, or consumer — runs its own fullsend instance for internal work. An agent in that downstream instance can push feature proposals upstream to a project that has its own full SDLC.

```
  ┌─── Upstream Project ───────────────────────────────────────────┐
  │                                                                │
  │       Refinement ──► Prioritization ──► Execution              │
  │      ╱                                           ╲             │
  │  Discovery                                        Verification │
  │      ╲                                           ╱             │
  │       Feedback ◄─────── Monitor ◄──────── Release              │
  │          ▲                                   │                 │
  └──────────│───────────────────────────────────│─────────────────┘
             │                                   └─────────┐
             │      upstreaming agent                      │
             │     proposes enhancement                    │ release
             └────────────────────────────────┐            │
                                              │            │
  ┌─── Downstream Org (vendor/consumer) ──────│────────────│───────┐
  │                                           │            │       │
  │       Refinement ──► Prioritization ──► Execution      │       │
  │      ╱                                                 ▼       │
  │  Discovery                                        Verification │
  │      ╲                                           ╱             │
  │       Feedback ◄──── Monitor ◄──────── Delivery                │
  │                                                                │
  └────────────────────────────────────────────────────────────────┘
```

Both orgs run the full [SDLC loop](vision.md#the-agentic-sdlc). The two cross-org handoff points are:

1. **Downstream Prioritization → Upstreaming agent → Upstream Refinement.** When the downstream org's SDLC prioritizes work that belongs upstream, the handoff at Prioritization → Execution goes to an *upstreaming agent* instead of a coding agent. This agent drafts proposals (issues or PRs) and ferries them into the upstream project's Refinement or Prioritization process via the forge.

2. **Upstream Delivery → Downstream Verification.** When the upstream project delivers a release, the downstream org consumes it. The new release enters the downstream SDLC at Verification — the downstream validates against its own integration tests, compatibility requirements, and deployment constraints.

The forge (GitHub) is the interface between the two orgs. The upstream project doesn't need to know or care that the proposal was generated by an agent in a downstream fullsend instance — it evaluates contributions through its own SDLC the same way it evaluates any human or agent contribution.

This connects to the [downstream/upstream problem doc](problems/downstream-upstream.md), which explores how competing sources of strategic intent get reconciled when multiple downstream contributors propose features into the same upstream project.

## Runtime execution flow

The diagrams below show the runtime path from event to completed agent task. The installer, admin CLI, and enrollment machinery are not shown — only what happens when an agent actually runs.

The architecture is a set of concentric layers, each wrapping the next:

```
Dispatcher → Agent Runner → Sandbox → Agent Runtime → LLM
```

Each outer layer configures and constrains the layer inside it. No inner layer can modify an outer layer. Credentials exist only in the outermost layers and never cross the sandbox boundary inward.

### Abstract model

This diagram is platform-agnostic. It uses a nested-box layout to show the concentric wrapping structure: each layer wraps the one inside it, and control flows inward (setup), then outward (teardown and delivery). No specific SCM, CI system, sandbox runtime, or LLM is named.

```
event ──► DISPATCHER
          Filters event, selects agent role, dispatches run
                │
                ▼
          ╔═══════════════════════════════════════════════════════╗
          ║ AGENT RUNNER                                          ║
          ║                                                       ║
          ║ Loads harness definition for agent role:              ║
          ║   agent prompt, sandbox image, network policy,        ║
          ║   skills, pre/post scripts, validation config,        ║
          ║   output schema, host files, env vars                 ║
          ║                                                       ║
          ║ Runs pre-script on host:                              ║
          ║   validate inputs, prefetch data                      ║
          ║                                                       ║
          ║ ┌───────────────────────────────────────────────────┐ ║
          ║ │ SANDBOX (ephemeral, per-run)                      │ ║
          ║ │                                                   │ ║
          ║ │ Created with image + network policy.              │ ║
          ║ │ Bootstrapped with agent def, skills, repo code,   │ ║
          ║ │ env vars, host files, security hooks.             │ ║
          ║ │ No credentials present inside this boundary.      │ ║
          ║ │                                                   │ ║
          ║ │ Pre-agent security scan (context injection).      │ ║
          ║ │                                                   │ ║
          ║ │ ┌───────────────────────────────────────────────┐ │ ║
          ║ │ │ AGENT RUNTIME                                 │ │ ║
          ║ │ │                                               │ │ ║
          ║ │ │ LLM tool-use loop:                            │ │ ║
          ║ │ │   read code, edit files, run tests, iterate   │ │ ║
          ║ │ │                                               │ │ ║
          ║ │ │ Boundaries enforced by enclosing sandbox:     │ │ ║
          ║ │ │   network policy, security hooks,             │ │ ║
          ║ │ │   no credentials, filesystem restrictions     │ │ ║
          ║ │ │                                               │ │ ║
          ║ │ │ Produces: modified repo, output artifacts     │ │ ║
          ║ │ └───────────────────────────────────────────────┘ │ ║
          ║ │                                                   │ ║
          ║ └───────────────────────────────────────────────────┘ ║
          ║                                                       ║
          ║ Extracts from destroyed sandbox:                      ║
          ║   output files, reasoning transcripts, modified repo  ║
          ║                                                       ║
          ║ Post-agent security scan (redact secrets from output) ║
          ║                                                       ║
          ║ Validation loop (if configured):                      ║
          ║   schema check on host                                ║
          ║   ├─ pass: continue                                   ║
          ║   ├─ fail + retries remain: re-run agent w/ feedback  ║
          ║   └─ fail + retries exhausted: HARD FAILURE           ║
          ║     (no unvalidated output emitted)                   ║
          ║                                                       ║
          ║ Runs post-script on host (outside sandbox):           ║
          ║   push code, create PR, post comments, apply labels   ║
          ║                                                       ║
          ╚═══════════════════════════════════════════════════════╝
                │
                ▼
          Results applied to external system
```

**Key invariants visible in this layout:**

- **Credentials never cross the sandbox boundary.** They exist in the agent runner layer; the sandbox and everything inside it operate without them.
- **Control flows inward (setup) then outward (teardown).** The harness configures the sandbox; the sandbox constrains the runtime. No inner layer can modify an outer layer.
- **Validation gates output.** When configured, no unvalidated output crosses from runner to external system. Exhausted retries are a hard failure, not a fallback.
- **The sandbox is ephemeral.** Created per-run, destroyed after extraction. No state carries between runs.

### MVP embodiment: GitHub + GitHub Actions + OpenShell + Claude Code

The same wrapping structure, with each layer mapped to its concrete technology.

```
GitHub event ──► SHIM WORKFLOW (fullsend.yml in enrolled repo)
                 Evaluates dispatch conditions (event type, labels, /slash commands).
                 Calls workflow_call to .fullsend repo (dispatch.yml).
                       │
                       ▼
                 ╔═══════════════════════════════════════════════════════════════╗
                 ║ DISPATCH WORKFLOW (.fullsend repo, dispatch.yml)              ║
                 ║                                                               ║
                 ║ Mints OIDC token → Cloud Function (token mint) → scoped      ║
                 ║ GitHub App installation token per agent role.                  ║
                 ║ Dispatches per-role agent workflows (code.yml, triage.yml).   ║
                 ╚═══════════════════════════════════════════════════════════════╝
                       │
                       ▼
                 ╔═══════════════════════════════════════════════════════════════╗
                 ║ AGENT WORKFLOW (.fullsend repo, e.g. code.yml)               ║
                 ║                                                               ║
                 ║ Validates source repo is enrolled in config.yaml.             ║
                 ║ Uses scoped GitHub App tokens:                                ║
                 ║   read-only token → enters sandbox (clone, read issues)       ║
                 ║   read-write token → stays on runner (push, create PR)        ║
                 ║ Checks out .fullsend repo + target repo.                      ║
                 ║                                                               ║
                 ║ ┌───────────────────────────────────────────────────────────┐ ║
                 ║ │ FULLSEND CLI (fullsend run code)                          │ ║
                 ║ │                                                           │ ║
                 ║ │ Loads harness/code.yaml:                                  │ ║
                 ║ │   agent: agents/code.md                                   │ ║
                 ║ │   image: ghcr.io/fullsend-ai/fullsend-code:latest         │ ║
                 ║ │   policy: policies/code.yaml                              │ ║
                 ║ │   skills: [skills/code-implementation]                    │ ║
                 ║ │   pre_script: scripts/pre-code.sh                         │ ║
                 ║ │   post_script: scripts/post-code.sh                       │ ║
                 ║ │                                                           │ ║
                 ║ │ Pre-script: validates ISSUE_NUMBER, REPO_FULL_NAME,       │ ║
                 ║ │ URL consistency.                                          │ ║
                 ║ │                                                           │ ║
                 ║ │ ┌───────────────────────────────────────────────────────┐ │ ║
                 ║ │ │ OPENSHELL SANDBOX                                     │ │ ║
                 ║ │ │                                                       │ │ ║
                 ║ │ │ Created with --from image, --policy code.yaml.        │ │ ║
                 ║ │ │ Bootstrapped via openshell upload/exec:               │ │ ║
                 ║ │ │   agent def    → /sandbox/claude-config/agents/       │ │ ║
                 ║ │ │   skills       → /sandbox/claude-config/skills/       │ │ ║
                 ║ │ │   .env, host files (GCP creds), security hooks        │ │ ║
                 ║ │ │   target repo  → /sandbox/workspace/target-repo/      │ │ ║
                 ║ │ │                                                       │ │ ║
                 ║ │ │ Network policy enforced (L7, per-binary):             │ │ ║
                 ║ │ │   Vertex AI     → claude, node only                   │ │ ║
                 ║ │ │   GitHub API    → gh, git only                        │ │ ║
                 ║ │ │   Pkg registries → npm, pip, go                       │ │ ║
                 ║ │ │                                                       │ │ ║
                 ║ │ │ Pre-agent scan: fullsend scan context                 │ │ ║
                 ║ │ │ (injection detection on CLAUDE.md, AGENTS.md, etc.)   │ │ ║
                 ║ │ │                                                       │ │ ║
                 ║ │ │ ┌───────────────────────────────────────────────────┐ │ │ ║
                 ║ │ │ │ CLAUDE CODE (claude --agent code)                 │ │ │ ║
                 ║ │ │ │                                                   │ │ │ ║
                 ║ │ │ │ Tool-use loop:                                    │ │ │ ║
                 ║ │ │ │   read files, edit code, run tests, iterate       │ │ │ ║
                 ║ │ │ │                                                   │ │ │ ║
                 ║ │ │ │ Model: Opus (via Vertex AI)                       │ │ │ ║
                 ║ │ │ │ Security hooks active: Tirith, SSRF, secret scan  │ │ │ ║
                 ║ │ │ │ No credentials in environment.                    │ │ │ ║
                 ║ │ │ │                                                   │ │ │ ║
                 ║ │ │ │ Produces: modified repo, output artifacts         │ │ │ ║
                 ║ │ │ └───────────────────────────────────────────────────┘ │ │ ║
                 ║ │ │                                                       │ │ ║
                 ║ │ └───────────────────────────────────────────────────────┘ │ ║
                 ║ │                                                           │ ║
                 ║ │ Extracts from destroyed sandbox:                          │ ║
                 ║ │   /sandbox/workspace/output/, JSONL transcripts,          │ ║
                 ║ │   SafeDownload repo (sanitize symlinks, strip hooks)      │ ║
                 ║ │                                                           │ ║
                 ║ │ Post-agent secret scan (redact from extracted output).    │ ║
                 ║ │                                                           │ ║
                 ║ │ Post-script (scripts/post-code.sh, with PUSH_TOKEN):      │ ║
                 ║ │   1. Verify feature branch (not main/master)              │ ║
                 ║ │   2. Protected-path check                                 │ ║
                 ║ │   3. gitleaks secret scan                                 │ ║
                 ║ │   4. pre-commit hooks                                     │ ║
                 ║ │   5. git push --force-with-lease                          │ ║
                 ║ │   6. Create/update PR with ready-for-review label         │ ║
                 ║ │                                                           │ ║
                 ║ └───────────────────────────────────────────────────────────┘ ║
                 ║                                                               ║
                 ║ Upload artifacts (fullsend-code)                              ║
                 ╚═══════════════════════════════════════════════════════════════╝
                       │
                       ▼
                 Branch pushed, PR created with ready-for-review label
```

**Layer mapping (abstract → MVP):**

| Abstract layer | MVP technology | ADR |
|---|---|---|
| Dispatcher | Shim workflow (`fullsend.yml`) in enrolled repo → `workflow_call` to `.fullsend/dispatch.yml` → OIDC mint → per-role agent workflows (thin callers → upstream reusable workflows) | [ADR 0008](ADRs/0008-workflow-dispatch-for-cross-repo-dispatch.md), [ADR 0031](ADRs/0031-reusable-workflows-for-action-installed-distribution.md) |
| Agent runner | GitHub Actions job → `fullsend run` CLI (via `fullsend-ai/fullsend@<version>` composite action) | |
| Harness store | YAML files in `.fullsend/harness/` (e.g. `code.yaml`, `triage.yaml`) | |
| Sandbox | OpenShell with per-agent L7 network policies (endpoint + binary restrictions) | |
| Agent runtime | Claude Code (`claude --agent --dangerously-skip-permissions`) | |
| Sandbox image | `ghcr.io/fullsend-ai/fullsend-code:latest` (pre-built with tools, runtimes, security scanners) | |
| Credential isolation | Read-only GitHub App token inside sandbox; write token only in post-script | [ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md) |
| Validation | Host-side schema validation script with retry loop | [ADR 0022](ADRs/0022-harness-level-output-schema-enforcement.md) |
| Post-script | `post-code.sh`: protected-path check, gitleaks scan, pre-commit, push, PR creation | |
| Observability | JSONL transcript extraction, security findings, trace ID correlation | [ADR 0021](ADRs/0021-jsonl-reasoning-trace-exposure.md) |

## Repository layout (design workspace vs. web delivery)

The repository combines design documents, Go CLI code, and a small **public web** surface. **Decided:** Browser-oriented static source and future bundled UI live under **`web/`** (the landing page is `web/public/index.html` at `/` and the interactive document graph is `web/public/graph.html` at `/graph.html`). Cloudflare Wrangler configuration and deploy-time static assets live under **`cloudflare_site/`** (single `wrangler.toml`; CI stages **`_bundle/`** on the deploy runner and copies only **`public/`** and **`worker/`** from the artifact into that tree so **`wrangler.toml` is never taken from the PR-built zip**). See [ADR 0019](ADRs/0019-web-source-and-cloudflare-site-layout.md).
