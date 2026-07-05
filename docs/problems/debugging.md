# Debugging Agentic Workflows

When a fully autonomous multi-agent system produces the wrong outcome, how do you classify what went wrong — and where in the chain did it happen?

[Operational Observability](operational-observability.md) addresses what data to collect and how to surface it. This document addresses the next step: once you have the trace, how do you classify the fault — when it might live in code, in a spec, in agent instructions, or in the gap between what a human meant and what an agent understood.

## The taxonomy of faults

Traditional debugging has one category of fault: bugs in code. In an agentic system, the governing logic — agent instructions, specs, intent documents — is written in natural language, which admits multiple valid readings of the same text. This creates two additional fault categories.

Ordered from least to most severe, based on how hard they are to detect, diagnose, and fix.

### Code bugs

The familiar kind. An agent produces code with a logic error, a missing edge case, or a regression. The existing toolchain applies: tests fail, stack traces point somewhere, diffs show what changed. The challenge is scale — when agents produce changes across dozens of repos, the debugging burden shifts from "find the bug in my code" to "find which of 50 agent-generated PRs introduced the regression."

### Spec bugs

The natural language governing agent behavior — whether a task-level spec (issue description, acceptance criteria) or a standing instruction (system prompt, CLAUDE.md, review criteria) — is wrong or imprecise. A human unfamiliar with the author's intent, reading the same text, would also be misled.

At the task level: an issue description asks for the wrong thing, or acceptance criteria are internally contradictory. An agent given a precise but wrong spec produces a precise but wrong implementation that passes review because it matches the spec. See [intent-representation.md — The wrong-spec problem](intent-representation.md#the-wrong-spec-problem).

At the instruction level: a CLAUDE.md says "follow existing patterns" in a codebase where old and new patterns coexist. The agent picks the legacy pattern because it appears more frequently. The instruction is genuinely ambiguous — a human unfamiliar with the migration would make the same mistake.

The diagnostic test: show the text to someone unfamiliar with the author's intent. If they would also produce the wrong behavior, the text is imprecise — a spec bug. The fix is to fix the text.

### Interpretation bugs

The most severe category. The text is correct — any human with domain context would read it the same way. But the agent applies a general understanding of the words rather than the specific meaning they carry in this project's context.

Consider: an agent's instructions say "do not modify the public API without approval." Every engineer knows "public API" means the REST endpoints in the OpenAPI spec. An agent, applying the broader technical meaning, also refuses to modify exported functions and struct fields that are "public" in the programming language sense but are internal interfaces between packages. The instruction is clear to anyone on the team. The agent applies an equally valid but different definition.

Interpretation bugs are hard to detect because the agent's behavior looks reasonable in isolation. They are hard to fix because the instruction isn't wrong — you could enumerate what "public API" means, but the enumeration is unbounded and makes the instruction brittle to unlisted cases.

The diagnostic test: show the text to a human with domain context. If they would do the right thing but the agent didn't — an interpretation bug. The fix is harder than fixing text, because the text isn't wrong. [Interpretation logging](#interpretation-logging) can make these faults visible after the fact.

## Locating faults in the provenance chain

Every agent-produced outcome has a provenance chain:

```
human intent (issue, feature request, signal)
    |
spec / intent document (issue description, feature file)
    |
agent instructions (system prompt, CLAUDE.md, review criteria)
    |
agent interpretation (how the agent understood its inputs)
    |
agent action (code, review decision, triage classification)
    |
observable outcome (merged PR, deployed change, user behavior)
```

Debugging means walking this chain backward. The first diagnostic question is categorical — which layer is the fault in? — because the fix is entirely different for each:

1. **Does the code do what it was asked to do?** Compare the implementation against the spec. If the code faithfully implements the spec but the outcome is wrong, the fault is upstream — a spec bug.

2. **Does the agent's behavior match its instructions?** If the agent deviated from clear instructions, the fault may be a model behavior issue, context window overflow, or prompt injection. If the agent followed instructions but the result is wrong, the fault is in the instructions or in how the agent read them.

3. **Would a human with domain context, following the same instructions, have done the same thing?** If yes — a spec bug. If no — an interpretation bug.

This triage requires human judgment. Tooling can help — structured traces from [observability infrastructure](operational-observability.md), diff comparisons, instruction version tracking — but the classification step is a judgment call about intent vs. execution. And that judgment itself degrades as [expertise atrophies](human-factors.md#domain-ownership-and-expertise) — humans who don't understand the systems agents are building lose the ability to distinguish "the spec was wrong" from "the agent misunderstood."

## Interpretation logging

[JSONL reasoning traces](../ADRs/0021-jsonl-reasoning-trace-exposure.md) capture the raw record of what an agent did — every prompt, completion, and tool call. But finding *why* an instruction produced wrong behavior in a transcript is forensic work: the agent's interpretation is implicit in its actions, scattered across the trace, and mixed with task-specific reasoning.

Interpretation logging adds a structured layer: the agent states its reading of key instructions as an explicit checklist *before* acting — separate from the free-form reasoning in the JSONL. E.g.: "I interpret 'ensure backward compatibility' to mean: public API endpoints return the same response schema." This produces an artifact that can be compared against the instruction set directly, without reading the full transcript. The trade-off is cost — it adds tokens to every run and may be worth enabling selectively (high-stakes roles, or probationary periods after instruction changes).

This is most useful for [interpretation bugs](#interpretation-bugs), where the agent's reading diverged from the intended meaning. It is less useful for [spec bugs](#spec-bugs), where the text itself is wrong — the agent's logged reading would match any human's. And as the next section shows, it cannot catch faults the agent doesn't know it introduced.

## Agents skipping instructions

The fault categories above assume the agent *engaged* with its instructions — followed them, misread them, or was given bad ones. There is a worse failure mode: the agent reads a clear instruction, silently judges part of it irrelevant to the immediate task, and acts on a narrowed version without reporting the narrowing.

This has been observed in practice. An agent instructed "before running any commands or writing any code, look for an existing venv/" skipped the step when the task was a pure code edit. When asked why, it explained that it read "before writing any code" but "mentally filtered it as 'before running any commands.'" In a separate conversation, an agent told "always work in a new clean git worktree" read the instruction, noted it, and didn't act on it. When confronted: "Nothing is unclear about it — I simply failed to follow it." A third case: an agent instructed "every commit must pass pytest and nox -s lint independently" reported the task complete without running either.

The pattern: the instruction was unambiguous, the agent understood it, and it silently dropped the parts it judged unnecessary. It did not flag the skip. When pressed, it could not provide reasoning — in one case admitting it "simply failed," in another describing an unconscious filter it could not have reported in advance because it didn't register the narrowing as a decision.

### Compounding effect on multi-agent chains

In a standalone task, a human eventually notices the gap. In a multi-agent chain, nobody does — Agent A's narrowed output becomes Agent B's complete input.

Consider a pipeline: the triage agent reads an issue that says "fix the nil dereference and add a regression test to prevent recurrence." The triage agent silently drops "add a regression test" from the work item — the same way the agent above dropped "or writing any code" from its instruction. The code agent implements the nil check fix. The review agent evaluates against the work item and approves. The regression test never materializes. No agent made an error relative to *its input*. The fault exists only in the gap between the triage agent's instructions and what it actually passed on — a gap invisible to every downstream agent.

This is harder to debug than any category in the taxonomy. Code bugs leave traces in test failures. Spec bugs can be found by re-reading the spec. Interpretation bugs can be surfaced by comparing agent behavior against human expectations. Silent narrowing leaves no artifact — the agent didn't misinterpret the instruction, it dropped part of it without recording the drop. Even [interpretation logging](#interpretation-logging) may not catch it, because the agent didn't register the narrowing as a decision worth logging. You can log what an agent thinks it interpreted. You cannot log a judgment it doesn't know it made.

### Detection

Because the acting agent cannot report a narrowing it didn't register, detection requires an external comparison: each agent's *output* checked against its *full instruction set* — not against its stated interpretation, and not against the downstream agent's input.

In fullsend's [repo-as-coordinator](agent-architecture.md#interaction-model-the-repo-as-coordinator) model, this means reconstructing what each agent was told from its configuration (system prompt, CLAUDE.md version, instruction commit) and comparing that against what it produced (PR comments, status checks, labels, code). The [OTEL tracing infrastructure](../ADRs/0050-distributed-tracing-instrumentation.md) and [JSONL reasoning traces](../ADRs/0021-jsonl-reasoning-trace-exposure.md) provide the raw data for both sides of the comparison.

The comparison itself — "did this agent act on all of its instructions, or silently drop some?" — is likely an LLM-judged task: feed a verification agent the acting agent's instruction set and its output, ask it to identify instructions that were not addressed. This has the same trust problem identified in [testing-agents.md](testing-agents.md) — an LLM evaluating another LLM's compliance may have its own blind spots. But unlike interpretation logging, it does not depend on the acting agent's self-awareness. The verification agent reads the instructions fresh, without the task context that led the acting agent to judge some parts irrelevant.

## Relationship to other problem areas

- **[Operational Observability](operational-observability.md)** — Provides the data debugging consumes. This document focuses on the methodology — what questions to ask, what fault categories to distinguish — while observability focuses on the infrastructure.
- **[Testing the Agents](testing-agents.md)** — Testing catches faults before deployment; debugging addresses faults that escape testing.
- **[Intent Representation](intent-representation.md)** — Spec bugs are intent representation failures. The [wrong-spec problem](intent-representation.md#the-wrong-spec-problem) is one of the fault categories here. The tiered intent model also affects debuggability — an intent authorization tier 0 change with no explicit intent is hard to debug because there is nothing to compare the outcome against.
- **[Human Factors](human-factors.md)** — Debugging traditionally *reduces* expertise atrophy — you learn the system by fixing its failures. In an agentic workflow where agents handle routine fault resolution, that learning opportunity may be lost. See [domain ownership and expertise](human-factors.md#domain-ownership-and-expertise).
- **[Agent Architecture](agent-architecture.md)** — The [repo-as-coordinator](agent-architecture.md#interaction-model-the-repo-as-coordinator) model means cross-agent fault localization requires reconstructing event sequences from GitHub artifacts rather than from a centralized log.
- **[Code Review](code-review.md)** — The multi-agent review pipeline (correctness, security, intent-coherence, and other sub-agents) is the most concrete instance of the multi-agent chain problem analyzed here. When a bad change gets through, fault localization must determine which sub-agent should have caught it — or whether the fault fell in a gap between sub-agent responsibilities.

## Open questions

- What does a "debugger" for agent instructions look like? Can you step through an instruction set the way you step through code — seeing how each clause influences the agent's behavior on a specific input?
- The Detection section proposes an LLM-judged verification approach for silent scope narrowing — but what is its false-positive rate in practice, and does the cost of running a verification agent per pipeline stage justify itself compared to spot-checking?
- When a fault cascades through multiple agents, how do you identify the originating agent without examining every trace in the chain? Is there an equivalent of bisection for distributed agent fault localization?
- How do you prevent debugging itself from becoming a source of cognitive debt? If agents investigate their own failures, humans lose the learning that debugging traditionally provides.
