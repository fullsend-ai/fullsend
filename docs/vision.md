# Vision

## The goal

Everything autonomous is the goal. A GitHub organization's repos operate with agents handling the full software development lifecycle: issue triage, implementation, code review, testing, and merge-to-production. Humans participate at two points:

1. **Strategic intent** — defining what the system should do and become. Features, architecture, direction.
2. **Guardrails** — CODEOWNERS-enforced human approval for security-critical, API-changing, or architecturally significant code paths.

Getting there is incremental. Each phase and transition in the [agentic SDLC](#the-agentic-sdlc) can be configured — per workflow — as human-driven, agent-driven, or both. A bugfix workflow might run fully autonomously while a feature workflow keeps a human in the loop at refinement and review. Teams adopt at whatever pace builds confidence, shifting one phase at a time rather than flipping a giant switch.

## The agentic SDLC

Software development is a continuous loop. The left side discovers *what* to build; the right side *builds* it. Fullsend's scope covers the full loop — agents participate in every phase, not just code generation.

```
             ◄── what to build ──►          ◄── how to build it ──►

                   Refinement ──► Prioritization ──► Execution
                  ╱                                          ╲
         Discovery                                            Verification
                  ╲                                          ╱
                   Feedback ◄────── Monitor ◄─────── Delivery
```

The current bugfix workflow spans three phases. The remaining phases are planned:

```
             ◄── what to build ──►          ◄── how to build it ──►

                   Refinement ───► Prioritization ──► ***Execution***
                  ╱                                          ╲
         Discovery                                      ***Verification***
                  ╲                                          ╱
                ***Feedback*** ◄─── Monitor ◄─────── Delivery
```

**Current (bugfix workflow):** An issue reports a bug (***feedback***). An agent triages, implements a fix (***execution***), and validates it against tests and review (***verification***). Humans must set strategic intent and approve guardrails; the rest may be fully autonomous based on configuration.

**Planned:** Discovery (identifying what needs work from production signals and tech debt), refinement (scoping and decomposing work), prioritization (ordering the backlog), delivery (merge-to-production with graduated autonomy), and monitor (production observability feeding back into the loop).

## Why

Modern coding agents have largely solved the code generation problem. Given a well-scoped task and a codebase with decent tests, agents can produce working implementations reliably. But generation is only one piece of the development lifecycle. The hard unsolved problems are:

- **Code review** — including internal review before a PR is even submitted
- **Intent verification** — how does the system know a change is one we actually want?
- **Priority and backlog management** — what should be worked on next?
- **Authority and governance** — who decides what agents can do?
- **Security** — how do we prevent the autonomous system from being exploited?

This project exists to explore these problems in a way that's applicable to any GitHub-hosted organization, though the patterns may need adaptation for specific organizational contexts. See [docs/problems/applied/](problems/applied/) for organization-specific considerations.

## Principles

- **Security is not a layer — it's the foundation.** Every component of the agentic system must be designed with adversarial thinking from day one. Not bolted on after.
- **Autonomy is earned, not granted.** Repos and change types graduate to higher autonomy levels based on demonstrated safety — test coverage, agent track record, security posture.
- **Humans set direction, agents execute.** The system should amplify human judgment, not replace it for strategic decisions.
- **Transparency over trust.** Every agent action should be auditable. Every decision should be traceable to its inputs.
- **Start anywhere, learn everywhere.** Experimentation across different repos and approaches is expected. What works for one technology stack may not work for another.

## Inspiration

OpenAI's [Harness engineering](https://openai.com/index/harness-engineering/) work demonstrates agents handling engineering tasks end-to-end with sandboxed execution and automated testing as primary guardrails. Our technology stack and constraints are different, but the ambition is similar.

## The development system as a compounding asset

In an agentic development model, the organization maintains two products simultaneously: the software it ships, and the development system that produces it. Skills, CI pipelines, context files, architectural constraints, testing infrastructure, lint rules — these are the tooling of the factory, and they compound.

A team that spends a week improving test infrastructure ships future features faster — not because the tests themselves are the product, but because agents operating against stronger test suites produce correct output at higher rates with less human correction (see [repo-readiness: backpressure](problems/repo-readiness.md#backpressure-as-throughput-investment)). A team that writes a debugging skill lets agents resolve future incidents autonomously. Every investment in the development system multiplies the throughput of all future work done through it.

This has a practical implication for how organizations allocate effort: time spent improving agent infrastructure, writing skills, strengthening CI, and refining codebase context is not overhead — it's product development for the development system itself. Organizations that recognize this and invest accordingly will see compounding returns in agent effectiveness.

## What this is not

- Not a product spec. This is an exploration.
- Not prescriptive. Multiple solutions may coexist for different problem areas.
- Not finished. This is meant to evolve through community contribution and experimentation.
- Not tied to a single organization. The core problems are universal; organization-specific details belong in [applied docs](problems/applied/).
