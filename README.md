# Fullsend

A project exploring fully autonomous agentic software development for GitHub-hosted organizations.

## What is this?

This repo explores how to get from the current state of human-driven software development to a fully-agentic workflow with zero human intervention for routine changes. The goal is agents that can triage issues, implement solutions, review code, and merge to production autonomously — while being secure by design.

This is not a product spec. It's an evolving exploration of a hard problem space, applicable to any organization considering autonomous agents for their software development lifecycle. The problem documents are organization-agnostic; organization-specific considerations live in `docs/problems/applied/`.

## What's here

- **[docs/vision.md](docs/vision.md)** — The big picture: what we're trying to achieve and why
- **[docs/roadmap.md](docs/roadmap.md)** — How this exploration progresses through phases
- **[docs/architecture.md](docs/architecture.md)** — Component vocabulary for the agent execution stack
- **[docs/problems/](docs/problems/)** — Deep dives into each major problem domain, each evolving independently:
  - [Intent Representation](docs/problems/intent-representation.md) — How do we capture, verify, and enforce what changes are wanted?
  - [Security Threat Model](docs/problems/security-threat-model.md) — Prompt injection, insider threats, agent drift, supply chain attacks
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
  - [Performance Verification](docs/problems/performance-verification.md) — Catching agent-introduced performance regressions before they reach production
  - [Production Feedback](docs/problems/production-feedback.md) — How platform execution signals feed back into what agents work on and how they assess risk
  - [Testing the Agents](docs/problems/testing-agents.md) — CI for prompts: regression testing, eval frameworks, and behavioral verification for agent instructions
- **[docs/problems/applied/](docs/problems/applied/)** — Organization-specific considerations for downstream consumers:
  - [konflux-ci](docs/problems/applied/konflux-ci/) — Kubernetes-native CI/CD platform (the original proving ground)
- **[docs/ADRs/](docs/ADRs/)** — Architecture Decision Records for crystallizing specific decisions (see [ADR 0001](docs/ADRs/0001-use-adrs-for-decision-making.md))
- **[docs/landscape.md](docs/landscape.md)** — Survey of existing AI code review tools and how they relate to our goals (time-sensitive — check the date)
- **[experiments/](experiments/)** — Logs and results from trying things in practice

## How to contribute

Pick a problem area that interests you. Read the existing document. Add your perspective, propose solutions, poke holes in existing proposals. Open a PR.

If you want to run an experiment — try an agent workflow in a repo, test a security guardrail, prototype an intent system — document what you did and what you learned in `experiments/`.

If you're applying fullsend to your own organization, consider adding your specific considerations to `docs/problems/applied/` — your experience and feedback will strengthen the general problem documents.

### Where does my contribution go?

| If you have... | Then... |
|---|---|
| A question, bug, or small suggestion | **File an issue** — lowest friction, can graduate later. |
| A new problem area no existing doc covers | **Create a problem doc** in `docs/problems/` and link it here. |
| More to say about an existing problem area | **Expand the existing problem doc.** |
| A specific decision that needs a yes-or-no answer | **Propose an ADR** in `docs/ADRs/` — even with only one option, file it as `Undecided` ([see ADR 0001](docs/ADRs/0001-use-adrs-for-decision-making.md)). |
| Something you want to try in practice | **Log an experiment** in `experiments/`. |

When in doubt, start with an issue.
