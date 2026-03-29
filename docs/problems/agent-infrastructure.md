# Agent Infrastructure

Where do agents run, what resources do they get, and do we adopt a 3rd party solution, use existing internal systems, or build our own?

This document explores the infrastructure layer that executes agents and provides them with the compute, tooling, and isolation they need to do their work. It is distinct from [agent-architecture.md](agent-architecture.md) (which defines *what* agents do and how they coordinate via the repo) and [governance.md](governance.md) (which defines *who* controls policy). Here we focus on *where* and *how* agents execute.

## Why this matters

Agents need:

- **Compute** — to run models, run tools (clones, linters, tests), and process context
- **Isolation** — so one compromised or buggy agent doesn't affect others; see [security-threat-model.md](security-threat-model.md) (agent-to-agent injection, lateral movement)
- **Resources** — access to repos, CI artifacts, intent sources, and possibly internal APIs in a controlled way
- **Observability** — logs, traces, and auditability so we can attribute actions and debug failures

The choice of platform affects cost, lock-in, compliance, integration with existing organizational systems, and how we scale (per-repo vs shared agents — see agent-architecture open questions).

## The three directions

### Adopt a 3rd party solution

Use a commercial or open-source platform that provides agent runtime, orchestration, and often model access (e.g. Cursor-like workflows, CodeRabbit-style review infrastructure, or generic agent platforms).

**Pros:** Faster to value, maintained by vendors, often includes model orchestration and tooling out of the box.

**Cons:** Vendor lock-in, data residency and compliance constraints, may not align with our zero-trust and repo-as-coordinator model, cost at scale, and we may still need to integrate with internal systems (GitHub, policy repos, internal APIs).

**Open questions:**

- Which vendors support our constraints (no coordinator agent, status-check–driven coordination, CODEOWNERS as authority)?
- What data leaves our boundary, and is that acceptable for the organization?
- Can we run their stack in our environment (e.g. self-hosted) or is it only SaaS?

### Use existing internal solutions

Leverage infrastructure the organization already runs — e.g. CI runners, internal Kubernetes, shared platforms — and run agents as workloads on that infrastructure.

**Pros:** No new vendor, data stays internal, consistent with existing operational and security practices, possible reuse of identity and secrets management.

**Cons:** Internal platforms may not be designed for long-running or bursty agent workloads; we may need to add isolation, scaling, and tooling; ownership and SLOs may be unclear.

**Open questions:**

- What internal platforms exist that could host agent workloads (e.g. OpenShift, internal CI, shared Kube)?
- What would we need to add (sandboxing, resource limits, agent-specific tooling)?
- Who operates and pays for the capacity?

### Build our own

Design and operate dedicated agent infrastructure: runner pool, sandboxing, tool access, and integration with GitHub and policy repos.

**Pros:** Full control over security model, isolation, and integration; no vendor lock-in; can align exactly with repo-as-coordinator and zero-trust principles.

**Cons:** High build and operational cost; we own reliability, scaling, and upgrades; may duplicate what vendors or internal platforms already provide.

**Open questions:**

- What is the minimum viable agent runtime (e.g. “run one review agent in a container with repo access and status-check API”)?
- How does it integrate with the organization's existing CI/CD infrastructure?
- Can we iterate with a thin custom layer on top of internal or 3rd party compute and only “build our own” where we must?

## Ambient Code Platform (ACP)

[Ambient Code Platform](https://github.com/ambient-code/platform) is a Kubernetes-native stack (API, operator, runner pods) for agentic sessions—aligned in spirit with “ambient,” CR-driven agent workloads on a cluster.

**Fit for fullsend (discussion notes):** For the **reliability, security, and scale** problems we care about most, ACP’s relevance is **limited** on its own. The points below are working notes for comparison against [security-threat-model.md](security-threat-model.md) and the isolation goals in this document—not a final product verdict.

- **Extra control plane** — Agent execution depends on an **additional controller** (operator) on top of baseline cluster operations. That adds components to run, upgrade, and troubleshoot when we already care about operational simplicity and blast radius.
- **UI- and chat-centric design** — The architecture appears oriented toward **interactive sessions and a web UI**, not first-class **automation from SCM or issue-tracker events** (webhooks, PR lifecycle, ticket transitions). For event-driven, repo-coordinated agents, that center of gravity is a poor fit unless we add substantial glue.
- **Parallel CR surface vs existing pipeline stacks** — ACP introduces its **own custom resources** and operator workflow. That **complicates integration** with established Kubernetes CI patterns—e.g. **Tekton** `PipelineRun` / `TaskRun`—where we would rather compose agent steps next to builds and tests instead of maintaining a second orchestration vocabulary.
- **Shared workspace** — **Multiple agents can occupy the same workspace**, which weakens separation between sessions. Untrusted content from one run can more easily influence another (cross-session **prompt injection** and lateral narrative control). That cuts against strong per-agent boundaries called out in the threat model (agent-to-agent trust and isolation).
- **Plain Pod execution** — Agents run as **ordinary Pods**, which makes **heavier or privileged workflows** awkward without further design—e.g. **building container images** (nested builds, dedicated builders, or image build policies) usually need more than a standard agent Pod out of the box.

ACP may still be useful for **narrow experiments** where those constraints are acceptable; it does not, by itself, deliver the isolation depth, supply-chain posture, or “agent as first-class workload with the right capabilities” that we are trying to reach here.

## Hybrid and incremental options

- **Thin orchestration layer** — We build a small layer that triggers agents, gathers results, and posts status checks; the actual compute is 3rd party or internal. This keeps coordination logic in our control while deferring platform choice.
- **Phase by phase** — Start with a 3rd party or internal option for early experiments (e.g. review agents only); decide later whether to replace or extend with custom infrastructure as autonomy expands.
- **By agent type** — Triage and review agents might run on one platform (e.g. event-driven, short-lived); implementation agents that need more tooling and longer runs might need a different environment.

## Relationship to other problem areas

- **Agent architecture** — Instance topology (per-repo vs shared) and “local vs remote” for pre-PR review depend on what infrastructure we have. Infrastructure enables or constrains those choices.
- **Security threat model** — Isolation and “separate execution environments” are implemented by this layer. Supply chain (what base images and dependencies the runtime uses) also lives here.
- **Governance** — Policy may be applied at runtime by agents reading from a policy repo; infrastructure determines where that runtime runs and how it accesses policy.
- **Repo readiness** — Repos need reliable CI and signals; agent infrastructure may consume or depend on the same CI (e.g. for “run tests” or “run linters”) and should not conflict with it.

## Open questions

- What is the right level of isolation per agent (process, container, microVM, separate cluster)?
- How do we provide agents with “resources to do their work” (clone, tools, APIs) without over-privileging them or creating a single high-value target?
- Do we need a dedicated “agent runner” image or environment with a known, auditable tool set?
- How do we compare 3rd party vs internal vs build-our-own on concrete criteria: cost, time to first agent, compliance, and alignment with our security and coordination model?
- Who in the org would own and operate agent infrastructure, and how does that align with existing platform or CI ownership?
