# Self-Improvement Flywheel

How does the system learn from its own outputs — and from human corrections to those outputs — to get better over time?

## Problem

This design is currently a collection of ideas. The problem documents describe how agents might review code, represent intent, enforce architectural invariants, and respond to production signals. But none of these ideas have been tested against real decisions. They exist as prose, not as working systems producing outputs that can be evaluated.

The risk of staying in this state too long is that the ideas become increasingly elaborate without ever contacting reality. A feedback loop requires something to feed back on. Right now there is nothing.

The flywheel problem is: **how do we move from ideas to decisions — even bad ones — and then systematically improve those decisions based on what we learn?**

This is distinct from [production feedback](production-feedback.md), which addresses how signals from the platform's runtime (pipeline failures, latency, error distributions) feed into what agents work on. The flywheel problem is about the system's own outputs — the code changes it proposes, the reviews it produces, the intent classifications it makes — and how the quality of those outputs feeds back into the system itself.

## Why this matters now

The [roadmap](../roadmap.md) describes Phase 1 as divergent exploration: expand the problem space, resist converging too early. That's been valuable. But divergent exploration has a failure mode: it can become a permanent state. The problem space keeps expanding because exploring problems is comfortable and low-risk. Nobody's wrong until someone makes a decision.

The transition from Phase 1 to Phase 2 requires producing concrete outputs — provisional solutions, working prototypes, actual agent configurations that make actual decisions on actual code. These outputs will be imperfect. That's the point. An imperfect decision that a human can correct is more valuable than a perfect analysis that never produces a decision, because the correction generates signal about what the system got wrong and why.

The flywheel metaphor captures this: the first turn is hard and slow. The system produces a bad output. A human corrects it. That correction, if captured and integrated, makes the next output slightly less bad. Over many turns, the system improves — not because someone designed the perfect system upfront, but because the system learned from its own failures.

But this only works if the correction loop exists. Without it, you get a system that makes the same mistakes repeatedly, with no mechanism for learning.

## What the flywheel acts on

The system produces several categories of output, each with its own feedback characteristics:

### Code changes

An agent proposes a code change via PR. A human reviews it and requests changes, rejects it, or approves it with modifications. The delta between what the agent proposed and what was actually acceptable is a learning signal.

Feedback forms:
- PR review comments explaining why something is wrong
- Requested changes showing what should have been different
- Rejection with rationale
- Approval after human-applied modifications (the diff between the agent's version and the merged version)

### Code reviews

An agent reviews a PR and produces findings — approves, requests changes, flags concerns. A human then agrees or disagrees with the agent's assessment. An agent that flags something a human dismisses is producing a false positive. An agent that misses something a human catches has a gap.

Feedback forms:
- Human overriding an agent approval (the agent missed something)
- Human dismissing an agent concern (false positive)
- Human escalating something the agent approved at a lower tier
- Patterns across many reviews — the agent consistently misses a class of issue

### Intent classification

An agent classifies a change as Tier 0, 1, or 2 (per [intent representation](intent-representation.md)). A human disagrees. The disagreement reveals a gap in the classification criteria or in the agent's understanding of the criteria.

### Architectural enforcement

An agent evaluates a change against [architectural invariants](architectural-invariants.md) and either flags a violation or doesn't. False positives (flagging conformant code) and false negatives (missing violations) are both feedback signals.

## The capture problem

The most critical part of the flywheel is not generating outputs — it's capturing the feedback signal when humans correct those outputs. Today, human corrections happen in PR review comments, Slack messages, verbal conversations, and mental models that never get written down. Almost none of this signal is structured enough for a system to learn from.

### What needs to be captured

For each correction, the system needs:
- **What the agent produced** — the specific output (PR diff, review comment, classification decision)
- **What the human changed** — the specific correction (requested change, override, reclassification)
- **Why the human changed it** — the rationale, which is often the most valuable and hardest-to-capture part

The "why" is where the real learning lives. "This function needs error handling" is useful. "This function needs error handling because the pipeline controllers retry on transient errors and a nil return here causes an infinite retry loop" is much more useful — it encodes domain knowledge that the agent was missing.

### Structured vs. unstructured feedback

There's a spectrum of how structured feedback can be:

**Fully structured** — the human fills out a form: "Agent missed: [category]. Correct answer: [value]. Reason: [text]." High signal, low adoption. Humans hate forms, especially when they're already doing the work of correcting the output.

**Semi-structured** — the correction itself is captured automatically (the PR diff, the review override), and the human provides free-text rationale in a natural location (PR comment, commit message). Moderate signal, moderate adoption.

**Unstructured** — the human corrects the output and moves on. The correction is visible in version control but the rationale is implicit or absent. Low signal, high adoption (because it's zero additional effort).

The practical starting point is semi-structured: capture the corrections automatically, make it easy (not mandatory) to explain why, and extract patterns from the accumulated corrections over time.

## How improvements propagate

Once feedback is captured, it needs to change the system's behavior. There are several mechanisms, and they're not mutually exclusive:

### Instruction refinement

Agent instructions (system prompts, CLAUDE.md files, review criteria) are the most direct lever. If the system consistently misses a class of issue, the instruction set needs to address it. The question is who writes the refinement.

**Human-authored:** A human reviews the accumulated feedback, identifies a pattern, and updates the agent's instructions. This is the simplest and most reliable approach, but it doesn't scale and depends on humans noticing patterns in feedback they may not be monitoring systematically.

**Agent-proposed, human-approved:** The system itself analyzes accumulated feedback, identifies patterns, and proposes instruction changes. "In the last 30 reviews, I was overridden 8 times for missing nil-check requirements in controller code. I propose adding the following to my review checklist: [specific addition]." The human reviews and approves or modifies the proposal before it takes effect. This is the actual flywheel — the system proposing improvements to itself, with humans as the approval gate.

**Fully autonomous:** The system updates its own instructions without human approval. This violates the design principle that [CODEOWNERS files — and by extension, agent guardrails — are always human-owned](../vision.md). Ruled out for the foreseeable future.

### Context enrichment

Some failures aren't instruction problems — they're context problems. The agent had the right instructions but lacked the domain knowledge to apply them correctly. Feedback in this category should flow into [codebase context](codebase-context.md) rather than agent instructions.

Example: an agent approves a change that removes a seemingly-unused mutex. A human catches it because they know the mutex protects against a race condition that only manifests under high pipeline concurrency. The fix isn't to add "check for mutexes" to the review instructions — it's to ensure the codebase context explains why that mutex exists.

### Golden-set expansion

Per [testing agents](testing-agents.md), agents should have test suites (golden sets) that verify their behavior. Every human correction is a candidate test case: "given this input, the agent produced X but should have produced Y." Feeding corrections into the golden set directly improves test coverage and creates a regression gate — the system can't re-introduce a mistake it's already been corrected on.

### Pattern libraries

Repeated corrections in the same category suggest a systematic gap. These can be distilled into reusable patterns — "when reviewing Tekton Task changes, always check for parameter injection" — that are more durable than individual test cases and more specific than general instructions.

### Deterministic tool extraction

The most impactful form of improvement is when a recurring agent judgment gets codified into a deterministic tool — a linter rule, a scanner policy, a test assertion, a CI check. The agent's highest-value role in many cases is not performing a check but *discovering that a check should exist*. The progression looks like this:

1. Agents review PRs using a combination of deterministic tools (linters, scanners) and non-deterministic judgment.
2. An observer process (which could itself be an agent) analyzes traces of agent reviews over time, looking for recurring patterns — the same kind of feedback given repeatedly across different PRs.
3. When a pattern is identified, it gets codified: a new linter rule, a new scanner policy, a new automation. What was non-deterministic becomes deterministic.
4. The review agent's scope shrinks to genuinely novel judgments that haven't yet been codified, while the deterministic tooling layer grows.

This creates a virtuous cycle distinct from instruction refinement. Instructions make the agent better at its current job; tool extraction *eliminates parts of that job entirely* by moving them into faster, cheaper, reproducible tooling. The system becomes more reliable, more auditable, and less dependent on LLM judgment over time.

Crucially, this also reduces coupling to AI. If the agents disappeared tomorrow, the deterministic tools they helped create would remain. The SDLC improves permanently, not just while agents are running. The agents are a catalyst for tooling improvement, not a permanent dependency.

From an [operational observability](operational-observability.md) perspective, "where should we invest improvement effort?" becomes answerable by looking at which agent judgments recur most frequently and are most amenable to codification. "Is the system getting better?" can be partially measured by the rate at which non-deterministic checks get converted to deterministic ones.

## The self-proposal mechanism

The most interesting part of the flywheel is the system proposing improvements to itself. This is where the feedback loop becomes genuinely self-reinforcing rather than just human-driven.

### How it could work

1. The system accumulates corrections over a window (time-based or count-based)
2. Periodically, an analysis agent reviews the corrections and looks for patterns
3. When a pattern is identified, the agent drafts a proposed change: an instruction update, a new golden-set case, a context addition, a pattern library entry
4. The proposal is submitted as a PR against the relevant configuration
5. A human reviews and approves, modifies, or rejects the proposal
6. If approved, [testing agents](testing-agents.md) verify that the change doesn't regress existing behavior

### What the system can propose

- **Instruction additions:** "Add X to the review checklist because of pattern Y in recent corrections"
- **Instruction refinements:** "Strengthen requirement Z from 'consider' to 'always check' because it was consistently missed"
- **New test cases:** "Add this scenario to the golden set based on correction C"
- **Context updates:** "Add this explanation to repo context because agents consistently lack this knowledge"
- **Deterministic tool proposals:** "I've flagged missing nil checks in controller code 12 times in the last month — propose adding a static analysis rule for this pattern"
- **Escalation rule changes:** "Reclassify changes to path P as Tier 2 because corrections show they require deeper review than Tier 1 provides"
- **Its own problem documents:** "Based on accumulated feedback, I've identified a problem area not covered by existing docs: [description]"

### What the system should not propose

- Changes to CODEOWNERS
- Changes to its own approval gates
- Removal of security constraints
- Weakening its own review criteria (the system should only propose making itself more rigorous, never less — a human can decide to relax criteria, but the system should never suggest it)

## Bootstrapping the flywheel

The hardest part is the first turn. The system needs to produce outputs to get feedback, but it needs some baseline capability to produce outputs worth correcting (as opposed to outputs so bad that correction is indistinguishable from doing the work from scratch).

### Possible bootstrapping sequence

1. **Shadow mode:** Run agents against real PRs but don't act on their outputs. Humans review the PRs normally. After the fact, compare the agent's output to the human's decision. This generates feedback without risk.

2. **Advisory mode:** Agent outputs are visible (as PR comments, for example) but not authoritative. Humans can see what the agent would have done and correct it explicitly. This generates higher-quality feedback because the human is actively engaging with the agent's output.

3. **Supervised mode:** Agent outputs are used as drafts. A human reviews and approves each output before it takes effect. Corrections are captured as described above.

4. **Semi-autonomous mode:** The agent acts autonomously on low-risk decisions (Tier 0) and surfaces higher-risk decisions for human approval. The correction rate on autonomous decisions determines whether the autonomy boundary expands or contracts.

This progression mirrors the [autonomy spectrum](autonomy-spectrum.md), but viewed through the lens of feedback generation rather than risk management. Each stage generates different types and volumes of feedback.

## Feedback on the feedback loop

There's a meta-question: how do we know the flywheel itself is working? The system proposes improvements to itself, but are those improvements actually improving outcomes?

Metrics that could indicate flywheel health:
- **Correction rate over time** — is the fraction of agent outputs that humans correct decreasing?
- **Correction severity over time** — are the remaining corrections becoming less severe (style nits vs. missed security issues)?
- **Self-proposal acceptance rate** — are human reviewers accepting the system's proposed improvements, or consistently rejecting them?
- **Regression frequency** — is the system re-introducing mistakes it was previously corrected on?
- **Time-to-pattern** — how quickly does the system identify a pattern in corrections and propose a fix?

If the correction rate isn't decreasing, the flywheel isn't working. If self-proposals are consistently rejected, the analysis agent's judgment is miscalibrated. If regressions are frequent, the golden-set pipeline has gaps.

## The asymmetry between positive and negative feedback

Human corrections are predominantly negative feedback — they indicate when the system got something wrong. Positive feedback (the system got it right and the human confirms this) is mostly implicit: the absence of correction. This creates an asymmetry:

- The system learns a lot about what not to do
- The system learns little about what it does well (and should keep doing)
- Over time, the system may become overly conservative — flagging everything to avoid being corrected — because the feedback signal punishes false negatives more visibly than false positives

Mitigating this requires explicit positive signal. When a human approves an agent's output without modification, that's a weak positive signal. When a human explicitly commends an agent's catch ("good find, I would have missed this"), that's a strong positive signal. The system needs to weight both.

## Relationship to other problems

**[Production Feedback](production-feedback.md)** — Production feedback is about signals from the platform's runtime feeding into agent work. The flywheel is about signals from the system's own outputs feeding back into itself. They're complementary: production feedback tells agents what to work on, the flywheel tells agents how to work better. A fully closed loop has both.

**[Testing the Agents](testing-agents.md)** — The flywheel generates test cases. Every human correction is a candidate golden-set entry. The testing framework verifies that the flywheel's improvements don't regress existing behavior. Testing is the verification layer; the flywheel is the improvement layer.

**[Intent Representation](intent-representation.md)** — Intent classification is one of the outputs the flywheel acts on. When humans correct tier classifications, that feedback should refine the classification criteria. The intent system defines what "correct" means; the flywheel improves the system's ability to achieve it.

**[Code Review](code-review.md)** — Code review is the highest-volume output that generates feedback. Most human corrections will come from review disagreements. The review sub-agent decomposition in code-review.md means feedback needs to be routed to the right sub-agent — a correction about missed security issues goes to the security agent's instructions, not the correctness agent's.

**[Governance](governance.md)** — The flywheel proposes changes to agent configuration. Governance determines who can approve those changes. If the flywheel is working well, it generates a steady stream of small improvement proposals — governance needs to handle this volume without becoming a bottleneck.

**[Contributor Guidance](contributor-guidance.md)** — The flywheel can identify gaps in contributor guidance. If agents consistently misunderstand a repo's conventions, that's a signal that the contributor guidance is unclear — not just to agents, but potentially to human contributors too.

**[Autonomy Spectrum](autonomy-spectrum.md)** — The flywheel's correction rate is a natural input to autonomy decisions. An agent with a declining correction rate has earned more autonomy; an agent with a rising correction rate should have autonomy reduced.

**[Operational Observability](operational-observability.md)** — Observability provides the raw data the flywheel needs: structured traces of agent decisions, human override records, and cost/latency metrics. Without observability, the flywheel has nothing to analyze. The flywheel also gives observability a purpose beyond monitoring — the "is the system getting better?" question is answerable through flywheel metrics like correction rate decline and the rate of deterministic tool extraction.

## Open questions

- What is the minimum viable version of the flywheel? Can we start with just capturing PR review overrides and manually analyzing them periodically, before building any automated analysis?
- How do we avoid the system becoming overly conservative in response to negative feedback? What explicit positive signal mechanisms are practical?
- How long a feedback window does the analysis agent need before proposing an improvement? Too short and it overfits to noise; too long and learning is slow.
- Should self-improvement proposals be treated differently from human-authored instruction changes in the [testing agents](testing-agents.md) pipeline, or should they go through the same process?
- How do we handle conflicting corrections? Two humans correct the same class of agent output in opposite directions — which correction does the system learn from?
- Can the system distinguish between corrections that reflect objective errors (the agent missed a bug) and corrections that reflect subjective preferences (the human prefers a different code style)? Should it try?
- What happens when the flywheel identifies a problem that spans multiple sub-agents? Who owns the improvement proposal?
- How do we prevent the flywheel from amplifying biases in the correction data? If one reviewer's preferences dominate the corrections, the system optimizes for that reviewer rather than for quality.
- Is there a role for the flywheel in improving the problem documents themselves — identifying gaps in the design based on implementation experience?
- What is the right mechanism for a human to explicitly say "this agent output was good" without adding friction to their workflow?
