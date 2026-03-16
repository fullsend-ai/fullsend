# Roadmap

How this exploration progresses. Not a rigid process — a description of the phases we expect to pass through and what characterizes each one.

## Phase 1: Divergent exploration (current)

We are here. The goal is to expand the problem space — identify as many distinct problem areas as possible, articulate them clearly, and resist the temptation to converge on solutions too early.

Each problem gets its own document in `docs/problems/`. Documents should present the problem, its dimensions, its relationship to other problems, and its open questions. They should not prescribe solutions. The point is to understand the terrain before choosing a path through it.

The right instinct in this phase is "what are we missing?" not "how do we solve this?"

## Phase 2: Architecture

With the problem space in mind, we need to solve _certain_ problems first that can give us a framework on which to explore solutions to the remaining problems.

* [agent-infrastructure](problems/agent-infrastructure.md) and [agent-architecture](problems/agent-architecture.md) stand out as foundational areas.
* [testing-agents](https://github.com/konflux-ci/fullsend/pull/14) is important to build a basis for continued expansion of the system.
* [intent-representation](problems/intent-representation.md) and [architectural-invariants](problems/architectural-invariants.md) need to be solved next.

With provisional solutions to those problems in place, you should begin to be able to see what the whole system could look like.

Solutions to the remaining problems can be explored as extensions to this foundation.

## Phase 3: Solution modeling

For the remaining problems, develop multiple possible solution models. Not one answer — several, with trade-offs made explicit. Each solution model may have multiple implementation options.

Where possible, devise experiments to build confidence in a solution. Experiments should test both the positive case (does this approach work when things go right?) and the negative case (does it fail safely when things go wrong? does it catch the thing it's supposed to catch?). Confidence comes from seeing a solution hold up under both conditions.

Experiments are documented in `experiments/`. The existing experiment format may evolve, but the core discipline is: state what you expect to learn, describe what you did, and record what actually happened.

Not every solution can be experimentally validated — some are organizational or procedural. For those, the trade-off analysis in the problem document is the primary tool.

* The problem of [code-review](problems/code-review.md) and the [security-threat-model](problems/security-threat-model.md) start to become real topics at this phase.
* Closely related is the [autonomy-spectrum](problems/autonomy-spectrum.md) and escalation handling.
* We need to be thinking seriously about [human-factors](problems/human-factors.md) at this stage.

## Phase 4: Domain Specificity

With a foundation and some higher level capabilities in place, we're going to need to tune the system to Konflux's specific needs.

We'll need agent skills that know how to debug konflux-ci e2e test failures and agent skills that know how to look for remote code execution problems in hermeto package managers.

* [repo-readiness](problems/repo-readiness.md), [agent-compatible-code](problems/agent-compatible-code.md), and [codebase-context](problems/codebase-context.md) will be ongoing areas of work here

## Phase 5: Convergence

The path from validated solutions to adoption depends on how we resolve the [governance](problems/governance.md) problem. Governance determines who has authority to make binding decisions, how the community participates in those decisions, and what process turns an explored solution into an adopted one.

Until governance is resolved, the prior phases can proceed independently — we can explore problems and test solutions without yet deciding how decisions get made. But convergence requires it.

At this stage, we should also be looking at choices made in the prior changes and consolidating any experiments that may have grown from PoCs into a de facto architecture.

What phase 5 looks like is deliberately left open.
