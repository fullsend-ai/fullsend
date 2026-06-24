# Fullsend

A living design document exploring fully autonomous agentic software development for GitHub-hosted organizations.

## What is this?

This repo is a living design document exploring how to get from the current state of human-driven software development to a fully-agentic workflow with zero human intervention for routine changes. The goal is agents that can triage issues, implement solutions, review code, and merge to production autonomously — while being secure by design.

This is not a product spec. It's an evolving exploration of a hard problem space, applicable to any organization considering autonomous agents for their software development lifecycle. The problem documents are organization-agnostic; organization-specific considerations live in `docs/problems/applied/`.

## What's here

- **[docs/vision.md](docs/vision.md)** — The big picture: what we're trying to achieve and why
- **[docs/roadmap.md](docs/roadmap.md)** — How this exploration progresses through phases
- **[docs/glossary.md](docs/glossary.md)** — Shared vocabulary: canonical definitions for project-specific and overloaded terms
- **[docs/architecture.md](docs/architecture.md)** — Component vocabulary for the agent execution stack
- **[docs/problems/](docs/problems/)** — Deep dives into each major problem domain, each evolving independently:
  - [Intent Representation](docs/problems/intent-representation.md) — How do we capture, verify, and enforce what changes are wanted?
  - [Security Threat Model](docs/problems/security-threat-model.md) — Prompt injection, insider threats, agent drift, supply chain attacks
  - [MCP Configuration Drift](docs/problems/mcp-config-drift.md) — Detecting unauthorized changes in MCP server configurations that define the agent tool surface
  - [Agent Architecture](docs/problems/agent-architecture.md) — What agents exist, what authority do they have, how do they interact?
  - [Agent Infrastructure](docs/problems/agent-infrastructure.md) — Where agents run, what resources they get, 3rd party vs internal vs build our own
  - [Autonomy Spectrum](docs/problems/autonomy-spectrum.md) — When to auto-merge vs. escalate to humans
  - [Governance](docs/problems/governance.md) — Who controls the agents and their configuration?
  - [Repo Readiness](docs/problems/repo-readiness.md) — Test coverage, CI/CD maturity, what's needed before agents can be trusted
  - [Code Review](docs/problems/code-review.md) — How agents review code, including security-focused sub-agents
  - [Architectural Invariants](docs/problems/architectural-invariants.md) — Enforcing things that must always be true, grounded in an organization's existing architecture documentation
  - [Agent-Compatible Code](docs/problems/agent-compatible-code.md) — Language properties that affect agent effectiveness
  - [Codebase Context](docs/problems/codebase-context.md) — How agents acquire codebase understanding and how to structure org-level context
  - [Downstream/Upstream](docs/problems/downstream-upstream.md) — How downstream contributors express business priorities and how competing sources of strategic intent get reconciled
  - [Human Factors](docs/problems/human-factors.md) — Domain ownership, role shift, review fatigue, and contributor motivation
  - [Contributor Guidance](docs/problems/contributor-guidance.md) — Making contribution rules clear to both humans and machines, without requiring AI to participate
  - [Contribution Volume](docs/problems/contribution-volume.md) — What happens when AI-generated external contributions overwhelm a project's capacity to evaluate them
  - [Performance Verification](docs/problems/performance-verification.md) — Catching agent-introduced performance regressions before they reach production
  - [Production Feedback](docs/problems/production-feedback.md) — How platform execution signals feed back into what agents work on and how they assess risk
  - [Testing the Agents](docs/problems/testing-agents.md) — CI for prompts: regression testing, eval frameworks, and behavioral verification for agent instructions
  - [GitLab Implementation](docs/problems/gitlab-implementation.md) — Implementation details for GitLab support: webhook security, dispatch pipelines, forge interface evolution
  - [Operational Observability](docs/problems/operational-observability.md) — How do the humans operating an autonomous software factory understand what it is doing, debug it when it goes wrong, and improve it over time?
  - [Adaptive Agent Selection](docs/problems/adaptive-agent-selection.md) — Learning which agent/team/workflow configurations work best for which problem classes, using evolutionary algorithms and Thompson Sampling
  - [Platform Nativeness](docs/problems/platform-nativeness.md) — When the platform you automate is also the one you build on: which problems are inherent vs. self-inflicted
  - [Cross-Run Memory](docs/problems/cross-run-memory.md) — How agents learn from prior run outcomes without violating the ephemeral sandbox invariant
- **[docs/problems/applied/](docs/problems/applied/)** — Organization-specific considerations for downstream consumers:
  - [konflux-ci](docs/problems/applied/konflux-ci/) — Kubernetes-native CI/CD platform (the original proving ground)
- **[docs/plans/](docs/plans/)** — Implementation plans for accepted or in-progress designs:
  - [Universal Harness Access](docs/plans/universal-harness-access.md) — Making harnesses and agents universally accessible via URLs and paths, enabling community sharing and composability
  - [Universal Harness Access — Phase 1 Implementation](docs/plans/universal-harness-access-phase1.md) — Phased PR breakdown for ADR-0038 Phase 1 (MVP)
  - [Universal Harness Access — Phase 2 Implementation](docs/plans/universal-harness-access-phase2.md) — Phased PR breakdown for ADR-0038 Phase 2 (transitive dependency resolution)
  - [Universal Harness Access — Phase 3 Implementation](docs/plans/universal-harness-access-phase3.md) — Phased PR breakdown for ADR-0038 Phase 3 (lock files and integrity verification)
  - [Universal Harness Access — Phase 4 Implementation](docs/plans/universal-harness-access-phase4.md) — Phased PR breakdown for ADR-0038 Phase 4 (runtime dependency loading)
  - [Agent Execution Environment](docs/plans/agent-execution-environment.md) — Sandbox and runtime environment for agent execution
  - [Vertex AI Inference Provisioning](docs/plans/vertex-inference-provisioning.md) — Provisioning and configuration for Vertex AI inference endpoints
  - [ADR-0045 Forge-Portable Harness Schema — Phase 1](docs/plans/adr-0045-forge-portable-harness-phase1.md) — Implementation plan for ADR-0045 forge-portable harness schema (Phase 1)
  - [ADR-0045 Forge-Portable Harness Schema — Phase 2](docs/plans/adr-0045-forge-portable-harness-phase2.md) — Implementation plan for ADR-0045 Phase 2: adopt new schema fields across install, scaffold, and lock flows
  - [ADR-0045 Forge-Portable Harness Schema — Phase 3](docs/plans/adr-0045-forge-portable-harness-phase3.md) — Implementation plan for ADR-0045 Phase 3: deprecate config.yaml agents block, add Lint() diagnostics, migrate to harness-first discovery
  - [ADR-0046 Drift Scanner](docs/plans/2026-03-06-adr46-drift-scanner.md) — Implementation plan for ADR-0046 drift detection tool
- **[docs/guides/](docs/guides/)** — Practical how-to documentation for administrators and developers (see [ADR 0023](docs/ADRs/0023-user-documentation-structure.md))
- **[docs/ADRs/](docs/ADRs/)** — Architecture Decision Records for crystallizing specific decisions (see [ADR 0001](docs/ADRs/0001-use-adrs-for-decision-making.md))
- **[web/](web/)** — Browser-delivered assets for the public site (document graph today; future Vite app here). Cloudflare Worker config lives in [`cloudflare_site/`](cloudflare_site/) ([ADR 0019](docs/ADRs/0019-web-source-and-cloudflare-site-layout.md)).
- **[docs/landscape.md](docs/landscape.md)** — Survey of AI code review tools, orchestration patterns, and connectivity gateways; how they relate to our goals (time-sensitive — check the date)
- **[experiments](https://github.com/fullsend-ai/experiments)** — Logs and results from trying things in practice (separate repository)

## How to contribute

Pick a problem area that interests you. Read the existing document. Add your perspective, propose solutions, poke holes in existing proposals. Open a PR.

If you want to run an experiment — try an agent workflow in a repo, test a security guardrail, prototype an intent system — document what you did and what you learned in https://github.com/fullsend-ai/experiments.

If you're applying fullsend to your own organization, consider adding your specific considerations to `docs/problems/applied/` — your experience and feedback will strengthen the general problem documents.

### Where does my contribution go?

| If you have... | Then... |
|---|---|
| A question, bug, or small suggestion | **File an issue** — lowest friction, can graduate later. |
| A new problem area no existing doc covers | **Create a problem doc** in `docs/problems/` and link it here. |
| More to say about an existing problem area | **Expand the existing problem doc.** |
| A specific decision that needs a yes-or-no answer | **Propose an ADR** in `docs/ADRs/` via a pull request ([see ADR 0001](docs/ADRs/0001-use-adrs-for-decision-making.md)). |

When in doubt, start with an issue.

## License

This project is licensed under the [Apache License, Version 2.0](LICENSE).
