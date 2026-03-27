# Repo Readiness

What's the current state of test coverage and CI maturity across a target organization, and what needs to improve before agents can be trusted?

> **Organization-specific coverage data:** For konkrete coverage numbers from downstream consumers, see their applied docs (e.g., [konflux-ci](applied/konflux-ci/)).

## Readiness beyond test coverage

Test coverage is necessary but not sufficient. For agents to merge autonomously, repos also need:

- **Integration and e2e tests** — unit tests catch local bugs; integration tests catch system-level regressions
- **Linting and formatting in CI** — prevents agent style drift
- **Clear CI signals** — tests must be reliable (no flaky tests that train agents to ignore failures)
- **Agent instruction files (CLAUDE.md or equivalent)** — agents need codebase context to work effectively (see [codebase-context.md](codebase-context.md) for what makes context files effective)
- **CODEOWNERS** — defines the human-required approval paths
- **Language properties** — type safety, tooling ecosystem, and deployment simplicity affect how effectively agents can operate (see [agent-compatible-code.md](agent-compatible-code.md))

## Backpressure as throughput investment

The readiness criteria above are typically framed as prerequisites — things that must be true before agents can operate. But there's a more productive framing: these same mechanisms (type checkers, linters, test suites, build systems) function as **backpressure** that keeps agents self-correcting during operation.

When a linter catches a style violation, the agent fixes it without human intervention. When a type checker rejects an invalid assignment, the agent corrects course immediately. When a test fails, the agent knows the implementation is wrong. Without backpressure, humans spend review time pointing out trivial mistakes. With it, agents self-correct and humans focus on architecture and judgment.

This reframes readiness investment: improving CI infrastructure is not just a prerequisite for agent autonomy — it's a continuous investment in agent throughput. Every new lint rule, every type annotation, every test case increases the rate at which agents can produce correct output without human correction. Organizations that treat readiness as a one-time gate miss the compounding returns of ongoing backpressure investment.

The implication for repo graduation: repos with stronger backpressure mechanisms can support higher autonomy tiers not just because they're "more ready," but because their agents produce fewer errors that require human attention per unit of work.

## Diagnostic tooling

[agentready](https://github.com/ambient-code/agentready) can assess repos against research-backed criteria for AI-assisted development readiness. Recommended as a diagnostic step to generate a baseline readiness assessment across an org, but not a dependency for the agentic system itself.

## Open questions

- What's the minimum coverage threshold for agent autonomy? Is it per-repo or per-package?
- Should agents help improve test coverage as a prerequisite to their own autonomy? (Chicken-and-egg: agents could write tests, but we need tests to trust agents.)
- How do we handle repos with significant package-level variance? (e.g., build-service: controller at 78.6%, git/github at 0%)
- Are there repos that should never be autonomous regardless of coverage? (e.g., security-critical infrastructure)
- How do we handle flaky tests? They erode confidence in the CI signal that agents rely on.
