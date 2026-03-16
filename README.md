# Fullsend

Fully autonomous software development for the [konflux-ci](https://github.com/konflux-ci/) organization.

## What is this?

This repo is a living design document exploring how to get from the current state of development across konflux-ci to a fully-agentic workflow with zero human intervention for routine changes. The goal is agents that can triage issues, implement solutions, review code, and merge to production autonomously — while being secure by design.

This is not a product spec. It's an evolving exploration of a hard problem space, meant to rally the konflux-ci contributor community around the vision and invite experimentation and contribution.

## What's here

- **[docs/vision.md](docs/vision.md)** — The big picture: what we're trying to achieve and why
- **[docs/roadmap.md](docs/roadmap.md)** — How this exploration progresses through phases
- **[docs/problems/](docs/problems/)** — Deep dives into each major problem domain, each evolving independently:
  - [Intent Representation](docs/problems/intent-representation.md) — How do we capture, verify, and enforce what changes are wanted?
  - [Security Threat Model](docs/problems/security-threat-model.md) — Prompt injection, insider threats, agent drift, supply chain attacks
  - [Agent Architecture](docs/problems/agent-architecture.md) — What agents exist, what authority do they have, how do they interact?
  - [Agent Infrastructure](docs/problems/agent-infrastructure.md) — Where agents run, what resources they get, 3rd party vs internal vs build our own
  - [Autonomy Spectrum](docs/problems/autonomy-spectrum.md) — When to auto-merge vs. escalate to humans
  - [Governance](docs/problems/governance.md) — Who controls the agents and their configuration?
  - [Repo Readiness](docs/problems/repo-readiness.md) — Test coverage, CI/CD maturity, what's needed before agents can be trusted
  - [Code Review](docs/problems/code-review.md) — How agents review code, including security-focused sub-agents
  - [Architectural Invariants](docs/problems/architectural-invariants.md) — Enforcing things that must always be true, grounded in the existing architecture repo
  - [Agent-Compatible Code](docs/problems/agent-compatible-code.md) — Language properties that affect agent effectiveness
  - [Codebase Context](docs/problems/codebase-context.md) — How agents acquire codebase understanding and how to structure org-level context
  - [Human Factors](docs/problems/human-factors.md) — Domain ownership, role shift, review fatigue, and contributor motivation
- **[docs/landscape.md](docs/landscape.md)** — Survey of existing AI code review tools and how they relate to our goals (time-sensitive — check the date)
- **[docs/experiments/](docs/experiments/)** — Logs and results from trying things in practice

## How to contribute

Pick a problem area that interests you. Read the existing document. Add your perspective, propose solutions, poke holes in existing proposals. Open a PR.

If you want to run an experiment — try an agent workflow in a repo, test a security guardrail, prototype an intent system — document what you did and what you learned in `docs/experiments/`.
