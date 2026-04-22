# Fullsend

**Fully autonomous agentic software development for GitHub-hosted organizations.**

Fullsend is a living design document exploring how to get from human-driven software development to a fully agentic workflow — agents that triage issues, implement solutions, review code, and merge to production autonomously, while being secure by design.

This is not a product spec. It's an evolving exploration of a hard problem space, applicable to any organization considering autonomous agents for their software development lifecycle.

[Explore the document graph](/map/){ .md-button .md-button--primary }
[View on GitHub](https://github.com/fullsend-ai/fullsend){ .md-button }

---

## The problem

Modern coding agents have largely solved code generation. Given a well-scoped task and decent tests, agents produce working implementations reliably. But generation is only one piece. The hard unsolved problems are:

- **Code review** — including internal review before a PR is even submitted
- **Intent verification** — how does the system know a change is actually wanted?
- **Priority and backlog management** — what should be worked on next?
- **Authority and governance** — who decides what agents can do?
- **Security** — how do we prevent the autonomous system from being exploited?

## Key principles

- **Security is the foundation.** Every component designed with adversarial thinking from day one.
- **Autonomy is earned, not granted.** Repos graduate to higher autonomy based on demonstrated safety.
- **Humans set direction, agents execute.** The system amplifies human judgment, not replaces it.
- **Transparency over trust.** Every agent action is auditable, every decision traceable.

## Where to start

| If you want to... | Go to... |
|---|---|
| Understand the big picture | [Vision](vision.md) |
| See the component architecture | [Architecture](architecture.md) |
| Explore a specific problem area | [Problems](problems/intent-representation.md) |
| Read past decisions | [Architecture Decision Records](ADRs/0001-use-adrs-for-decision-making.md) |
| See the project roadmap | [Roadmap](roadmap.md) |
| Survey the landscape of tools | [Landscape](landscape.md) |
| Visualize how documents connect | [Document Graph](/map/) |

## How to contribute

Pick a problem area that interests you. Read the existing document. Add your perspective, propose solutions, poke holes. Open a PR.

| If you have... | Then... |
|---|---|
| A question or small suggestion | [File an issue](https://github.com/fullsend-ai/fullsend/issues) |
| A new problem area | Create a doc in `docs/problems/` |
| More to say about an existing problem | Expand the existing problem doc |
| A specific decision needing a yes/no | Propose an [ADR](ADRs/0001-use-adrs-for-decision-making.md) |

## License

This project is licensed under the [Apache License, Version 2.0](https://github.com/fullsend-ai/fullsend/blob/main/LICENSE).
